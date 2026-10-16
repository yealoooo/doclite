package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/jobs"
	"doclite/server/internal/platform/s3"
	"doclite/server/internal/project"
)

// 存储迁移在后台执行器里的 Handler（设计和并发见 migrate.go 开头）。

// JobHandler 是迁移在执行器里的 Handler（app 里 Register(MigrateJobType, …)）。
func (m *Migrator) JobHandler() jobs.Handler { return &migrateRunner{m: m} }

type migrateRunner struct{ m *Migrator }

var _ jobs.Handler = (*migrateRunner)(nil)

// stopError 是不再重试的失败（目标存储不再可读写、密钥解不开……），错误信息写进 error_message。
type stopError struct{ msg string }

func (e *stopError) Error() string { return e.msg }

func stop(format string, args ...any) error { return &stopError{msg: fmt.Sprintf(format, args...)} }

// errRegroup：改位置时发现有快照的封面不在复制过的里面（回滚，重读这个对象的快照再来）。
var errRegroup = errors.New("引用这个对象的快照变了")

func (r *migrateRunner) Claimed(ctx context.Context, tx pgx.Tx, job *jobs.Job) error {
	p, err := parsePayload(job.Payload)
	if err != nil {
		return jobs.Fail("存储迁移的后台任务不对（内部错误）：%v", err)
	}
	if p.Cancel != nil {
		// 排队中取消时取消接口已经改好了状态，这里只是防御：照样记结束。
		if err := r.m.writeEnd(ctx, tx, job.ID, p, string(jobs.Canceled), nil); err != nil {
			return err
		}
		return jobs.ErrCanceled
	}
	return nil
}

func (r *migrateRunner) CancelRequested(ctx context.Context, q db.Querier, job *jobs.Job) (bool, error) {
	return cancelRequested(ctx, q, job.ID)
}

// Settled：失败、取消时补数一次迁过的快照（断点之后、进程被杀前迁的），写断点和结束的审计。重试、放回队列不用做什么
// （下一次执行从断点接着做，开始时也会补数）。
func (r *migrateRunner) Settled(ctx context.Context, tx pgx.Tx, job *jobs.Job, to jobs.Status) error {
	if to != jobs.Failed && to != jobs.Canceled {
		return nil
	}
	p, msg, err := jobPayload(ctx, tx, job.ID)
	if err != nil {
		return err
	}
	if err := closeState(ctx, tx, job.ID, p); err != nil {
		return err
	}
	if to == jobs.Canceled {
		msg = nil
	}
	return r.m.writeEnd(ctx, tx, job.ID, p, string(to), msg)
}

// closeState 是不再执行时（失败、取消）的最后一次断点：按审计补数断点之后迁过的快照，记下源存储里还剩的快照数。
func closeState(ctx context.Context, tx pgx.Tx, jobID uuid.UUID, p *migratePayload) error {
	st := &p.State
	n, mark, err := countMovedSince(ctx, tx, jobID, st.AuditMark)
	if err != nil {
		return err
	}
	st.Migrated += n
	st.AuditMark = mark
	st.Total = max(st.Total, st.Migrated+st.Failed)
	remaining, err := countSnapshots(ctx, tx, p.SourceProfileID)
	if err != nil {
		return err
	}
	st.Remaining = &remaining
	return saveState(ctx, tx, jobID, st)
}

// Notify：系统管理的页面不走实时通道（存储配置页轮询进度），这里只记日志。
func (r *migrateRunner) Notify(*jobs.Job) {}

// endpoints 是源、目标存储的配置和客户端（每取一页对象之前重读配置，变了才换客户端）。
type endpoints struct {
	src, dst         *row
	srcC, dstC       *s3.Client
	cacheControl     string
	srcStamp, dstStp time.Time
}

func (r *migrateRunner) endpoints(ctx context.Context, p *migratePayload, prev *endpoints) (*endpoints, error) {
	s := r.m.svc
	src, err := getRow(ctx, s.pool, p.SourceProfileID)
	if err != nil {
		return nil, jobs.Retry(err)
	}
	dst, err := getRow(ctx, s.pool, p.TargetProfileID)
	if err != nil {
		return nil, jobs.Retry(err)
	}
	if src == nil {
		return nil, stop("源存储“%s”已经删除了", p.SourceProfileName)
	}
	if dst == nil {
		return nil, stop("目标存储“%s”已经删除了", p.TargetProfileName)
	}
	if dst.Status != StatusActive {
		return nil, stop("目标存储“%s”不再是可读写的（改成了只读或停用），迁移停下；已经迁的不受影响，改回可读写后可以重新迁移", dst.ProfileName)
	}
	e := &endpoints{src: src, dst: dst, srcStamp: src.UpdatedAt, dstStp: dst.UpdatedAt,
		cacheControl: fmt.Sprintf("private, max-age=%d, immutable", int64(s3.PresignExpiry(dst.URLExpireHours).Seconds()))}
	if prev != nil && prev.srcStamp.Equal(src.UpdatedAt) {
		e.srcC = prev.srcC
	} else if e.srcC, err = s.clientOf(src); err != nil {
		return nil, stop("源存储的连接配置不能用：%v", err)
	}
	if prev != nil && prev.dstStp.Equal(dst.UpdatedAt) {
		e.dstC = prev.dstC
	} else if e.dstC, err = s.clientOf(dst); err != nil {
		return nil, stop("目标存储的连接配置不能用：%v", err)
	}
	return e, nil
}

// Run 执行一次迁移（流水线，不按批等齐）：
//   - 取 key 的 goroutine 从断点的 cursor 之后一页一页取源存储里的对象（每页之前重读两个存储的配置，变了换客户端），
//     交给几个 worker；
//   - worker 一个对象一个对象地复制、核对、改位置（每个对象一个短事务）；
//   - 这里收结果：按取出的顺序找从头连续处理完的那一段，cursor 推进到它的最后一个 key，这一段里核对不一致的进失败清单；
//     大约每秒写一次断点，结束、出错、取消、退出时再写一次。已经迁走但还在那一段后面的不影响（它们不在源存储里了），
//     那一段后面核对不一致的不记，下次执行会再试。
//
// 有一个出了要停下的错误（读写存储、写库失败，取消，目标存储不再可读写），别的 worker 做完手上这个就停。
func (r *migrateRunner) Run(ctx context.Context, job *jobs.Job, run *jobs.Run) error {
	m := r.m
	p, err := parsePayload(job.Payload)
	if err != nil {
		return stop("存储迁移的后台任务不对（内部错误）：%v", err)
	}
	st := p.State
	// 上次执行在两次断点之间迁过的（进程被杀、写断点失败）：按审计补数。
	n, mark, err := countMovedSince(ctx, m.svc.pool, job.ID, st.AuditMark)
	if err != nil {
		return jobs.Retry(err)
	}
	st.Migrated += n
	st.AuditMark = mark
	run.Progress(st.Migrated + st.Failed)
	m.log.Info("存储迁移：开始执行", "job_id", job.ID, "attempt", job.Attempt, "cursor", st.Cursor, "migrated", st.Migrated,
		"failed", st.Failed)
	first, err := r.endpoints(ctx, p, nil)
	if err != nil {
		return err
	}
	var ends atomic.Pointer[endpoints]
	ends.Store(first)

	pctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	type task struct {
		seq  int
		key  string
		rows []groupRow
	}
	type done struct {
		seq int
		key string
		res keyResult
	}
	tasks := make(chan task)
	results := make(chan done)
	var exhausted atomic.Bool
	startCursor := st.Cursor

	go func() { // 取 key
		defer close(tasks)
		cursor, seq := startCursor, 0
		for page := 0; ; page++ {
			if page > 0 {
				e, err := r.endpoints(pctx, p, ends.Load())
				if err != nil {
					cancel(err)
					return
				}
				ends.Store(e)
			}
			keys, err := nextKeys(pctx, m.svc.pool, p.SourceProfileID, cursor, m.opts.BatchSize)
			if err != nil {
				cancel(jobs.Retry(err))
				return
			}
			if len(keys) == 0 {
				exhausted.Store(true)
				return
			}
			groups, err := loadGroups(pctx, m.svc.pool, p.SourceProfileID, keys)
			if err != nil {
				cancel(jobs.Retry(err))
				return
			}
			for _, k := range keys {
				select {
				case tasks <- task{seq: seq, key: k, rows: groups[k]}:
					seq++
				case <-pctx.Done():
					return
				}
			}
			cursor = keys[len(keys)-1]
		}
	}()
	var wg sync.WaitGroup
	for range m.opts.Workers {
		wg.Go(func() {
			for t := range tasks {
				if pctx.Err() != nil {
					return
				}
				res := r.object(pctx, job, ends.Load(), t.key, t.rows)
				if res.err != nil {
					cancel(res.err)
				}
				results <- done{seq: t.seq, key: t.key, res: res}
			}
		})
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	// 收结果、写断点。
	pending := map[int]done{}
	next, live, dirty := 0, 0, false
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	save := func() {
		if err := r.checkpoint(ctx, job, &st); err != nil {
			cancel(err)
			return
		}
		live, dirty = 0, false
		run.Progress(st.Migrated + st.Failed)
	}
	for open := true; open; {
		select {
		case d, ok := <-results:
			if !ok {
				open = false
				break
			}
			if d.res.err != nil {
				break // 没做完：连续的那一段停在它前面
			}
			live += d.res.moved
			pending[d.seq] = d
			for {
				c, ok := pending[next]
				if !ok {
					break
				}
				delete(pending, next)
				next++
				st.Cursor = c.key
				if f := c.res.failure; f != nil {
					st.Failed += f.SnapshotCount
					st.FailedObjects++
					if len(st.Failures) < migrateFailuresKept {
						st.Failures = append(st.Failures, *f)
					} else {
						st.FailuresTruncated = true
					}
				}
			}
			dirty = true
			run.Progress(st.Migrated + live + st.Failed)
		case <-tick.C:
			if dirty {
				save()
			}
		}
	}
	if dirty {
		save()
	}
	if err := ctx.Err(); err != nil {
		return err // 取消、退出：执行器按它自己的原因收尾
	}
	if pctx.Err() != nil {
		return context.Cause(pctx)
	}
	if !exhausted.Load() {
		return jobs.Retry(errors.New("迁移没有取完就停了（内部错误）"))
	}
	return r.finish(ctx, job, p, &st)
}

// keyResult 是一个对象的处理结果。
type keyResult struct {
	moved   int               // 改了位置的快照数
	failure *MigrationFailure // 核对不一致（这个对象的快照都没改）
	err     error             // 停下这次执行的错误（读写存储、写库失败，取消，目标不再可读写……）；有它时没做完
}

// checkpoint 写断点：st（cursor、失败清单已经由调用方推进好）加上按审计数的 migrated。和改位置的事务一样先锁 job 行
// （FOR NO KEY UPDATE，等正在改位置的事务——它们拿着 FOR SHARE——都提交了再数，之后的改位置拿到的审计 id 都比 mark
// 大）。不用调用方的 ctx（取消、退出时也要写下来）。写不成时 st 不变（只有内存里的 cursor、失败已经推进，下次执行从库里
// 的断点接着做）。
func (r *migrateRunner) checkpoint(ctx context.Context, job *jobs.Job, st *migrateState) error {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	next := *st
	err := db.InTx(wctx, r.m.svc.pool, func(tx pgx.Tx) error {
		owned, err := jobs.LockOwned(wctx, tx, job)
		if err != nil {
			return err
		}
		if !owned {
			return jobs.ErrLost
		}
		n, mark, err := countMovedSince(wctx, tx, job.ID, st.AuditMark)
		if err != nil {
			return err
		}
		next.Migrated += n
		next.AuditMark = mark
		next.Total = max(next.Total, next.Migrated+next.Failed)
		return saveState(wctx, tx, job.ID, &next)
	})
	if err != nil {
		if errors.Is(err, jobs.ErrLost) {
			return err
		}
		return jobs.Retry(fmt.Errorf("写断点失败：%w", err))
	}
	*st = next
	return nil
}

// finish：源存储里没有 cursor 之后的对象了。数还剩的快照，写断点和结束的审计，job done（同一个事务；已经要求取消就
// 按取消收尾）。
func (r *migrateRunner) finish(ctx context.Context, job *jobs.Job, p *migratePayload, st *migrateState) error {
	m := r.m
	err := db.InTx(ctx, m.svc.pool, func(tx pgx.Tx) error {
		owned, err := jobs.LockOwned(ctx, tx, job)
		if err != nil {
			return err
		}
		if !owned {
			return jobs.ErrLost
		}
		if canceled, err := cancelRequested(ctx, tx, job.ID); err != nil || canceled {
			if err == nil {
				err = jobs.ErrCanceled
			}
			return err
		}
		remaining, err := countSnapshots(ctx, tx, p.SourceProfileID)
		if err != nil {
			return err
		}
		st.Remaining = &remaining
		st.Total = st.Migrated + st.Failed
		if err := saveState(ctx, tx, job.ID, st); err != nil {
			return err
		}
		p.State = *st
		if err := m.writeEnd(ctx, tx, job.ID, p, string(jobs.Done), nil); err != nil {
			return err
		}
		return jobs.MarkDone(ctx, tx, job)
	})
	if err != nil {
		if errors.Is(err, jobs.ErrCanceled) || errors.Is(err, jobs.ErrLost) {
			return err
		}
		return jobs.Retry(err)
	}
	m.log.Info("存储迁移：完成", "job_id", job.ID, "migrated", st.Migrated, "failed", st.Failed, "remaining", *st.Remaining)
	return nil
}

// ---------------------------------------------------------------------------
// 一个对象

// object 处理一个对象：复制原图和封面、核对，一致就改位置。
//   - 引用它的快照中途变了（理论上不会）：重读再来，3 次都不行记 inconsistent；
//   - 读写存储（或写库）暂时失败：等 RetryBackoff × 4^(n−1)（1、4、16 秒）重读再来，3 次都不行才停下这次执行，交给
//     执行器重试。重来是安全的：复制会覆盖目标里同一个 key，改位置只改还在源存储里的快照（上次其实改成了的，重读后
//     就没有了）。
func (r *migrateRunner) object(ctx context.Context, job *jobs.Job, ends *endpoints, key string, rows []groupRow) keyResult {
	regroups, retries := 0, 0
	for {
		if len(rows) == 0 {
			return keyResult{}
		}
		res := r.objectOnce(ctx, job, ends, key, rows)
		switch {
		case errors.Is(res.err, errRegroup):
			if regroups >= 2 {
				return keyResult{failure: r.failure(ctx, key, rows, "inconsistent",
					"引用这个对象的快照记的内容（sha256、大小）或封面和别的快照对不上，没有迁")}
			}
			regroups++
		case retryableHere(res.err) && retries < migrateObjectRetries && ctx.Err() == nil:
			delay := r.m.opts.RetryBackoff << (2 * retries)
			retries++
			r.m.log.Warn("存储迁移：一个对象暂时失败，稍后重试", "storage_key", key, "retry", retries, "delay", delay,
				"error", res.err)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return res
			}
		default:
			return res
		}
		groups, err := loadGroups(ctx, r.m.svc.pool, ends.src.ID, []string{key})
		if err != nil {
			return keyResult{err: jobs.Retry(err)}
		}
		rows = groups[key]
	}
}

// retryableHere：一个对象里可以就地重试的错误（读写存储、临时盘、写库暂时失败）；取消、退出、job 不归这次执行、
// 目标存储不再可读写都不是。
func retryableHere(err error) bool {
	return err != nil && jobs.Retryable(err) && !errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, jobs.ErrCanceled) && !errors.Is(err, jobs.ErrLost)
}

func (r *migrateRunner) objectOnce(ctx context.Context, job *jobs.Job, ends *endpoints, key string, rows []groupRow) keyResult {
	head := rows[0]
	for _, g := range rows[1:] {
		if !bytes.Equal(g.SHA256, head.SHA256) || g.Size != head.Size {
			return keyResult{failure: r.failure(ctx, key, rows, "inconsistent",
				"引用这个对象的快照记的 sha256 或大小不一样，没有迁")}
		}
	}
	newKey := targetKey(key, ends.src.PathPrefix, ends.dst.PathPrefix)
	code, why, err := r.copyObject(ctx, ends, key, newKey, &head, head.MimeType)
	if err != nil {
		return keyResult{err: err}
	}
	if code != "" {
		return keyResult{failure: r.failure(ctx, key, rows, code, why)}
	}
	// 封面：同一对象的快照一般是同一个封面（或都没有）。
	var oldCovers, newCovers []*string
	seen := map[string]bool{}
	hasNone := false
	for _, g := range rows {
		if g.CoverKey == nil {
			hasNone = true
			continue
		}
		c := *g.CoverKey
		if seen[c] {
			continue
		}
		seen[c] = true
		nc := targetKey(c, ends.src.PathPrefix, ends.dst.PathPrefix)
		code, why, err := r.copyObject(ctx, ends, c, nc, nil, "image/jpeg")
		if err != nil {
			return keyResult{err: err}
		}
		if code != "" {
			if code == "source_missing" {
				code, why = "cover_missing", "源存储里没有这个快照的封面（"+c+"），没有迁"
			}
			return keyResult{failure: r.failure(ctx, key, rows, code, why)}
		}
		oldCovers, newCovers = append(oldCovers, &c), append(newCovers, &nc)
	}
	if hasNone {
		oldCovers, newCovers = append(oldCovers, nil), append(newCovers, nil)
	}
	moved, err := r.switchObject(ctx, job, ends, key, newKey, &head, rows, oldCovers, newCovers)
	if err != nil {
		return keyResult{err: err}
	}
	return keyResult{moved: moved}
}

// switchObject 在一个短事务里把引用 key 的快照改到目标存储（加锁顺序见 migrate.go 开头）。
func (r *migrateRunner) switchObject(ctx context.Context, job *jobs.Job, ends *endpoints, key, newKey string, head *groupRow,
	rows []groupRow, oldCovers, newCovers []*string) (int, error) {
	var operator *uuid.UUID
	if job.CreatedBy != uuid.Nil {
		operator = &job.CreatedBy
	}
	projects := make([]uuid.UUID, 0, 1)
	for _, g := range rows {
		if g.ProjectID != nil && !slices.Contains(projects, *g.ProjectID) {
			projects = append(projects, *g.ProjectID)
		}
	}
	n := 0
	err := db.InTx(ctx, r.m.svc.pool, func(tx pgx.Tx) error {
		owned, canceled, err := lockJobForMove(ctx, tx, job.ID, job.Attempt)
		if err != nil {
			return err
		}
		if !owned {
			return jobs.ErrLost
		}
		if canceled {
			return jobs.ErrCanceled
		}
		status, _, found, err := lockForSnapshot(ctx, tx, ends.dst.ID)
		if err != nil {
			return err
		}
		if !found || status != StatusActive {
			return stop("目标存储“%s”不再是可读写的（改成了只读或停用），迁移停下；已经迁的不受影响", ends.dst.ProfileName)
		}
		if err := project.LockContents(ctx, tx, projects, head.SHA256); err != nil {
			return err
		}
		moved, err := moveObject(ctx, tx, ends.src.ID, key, ends.dst.ID, newKey, head.SHA256, head.Size, oldCovers, newCovers)
		if err != nil {
			return err
		}
		if left, err := countAt(ctx, tx, ends.src.ID, key); err != nil {
			return err
		} else if left > 0 {
			return errRegroup
		}
		n = len(moved)
		if n == 0 {
			return nil
		}
		return auditMoved(ctx, tx, job.ID, operator, ends.src, ends.dst, key, newKey, moved)
	})
	if err == nil {
		return n, nil
	}
	var se *stopError
	if errors.Is(err, errRegroup) || errors.Is(err, jobs.ErrCanceled) || errors.Is(err, jobs.ErrLost) ||
		errors.Is(err, context.Canceled) || errors.As(err, &se) {
		return 0, err
	}
	return 0, jobs.Retry(fmt.Errorf("改快照的位置失败：%w", err))
}

// failure 记一个核对不一致的对象（补上快照所在的项目、文件名和位置；查不出来只记 id）。
func (r *migrateRunner) failure(ctx context.Context, key string, rows []groupRow, code, reason string) *MigrationFailure {
	listed := rows[:min(len(rows), migrateFailureSnaps)]
	fids := make([]uuid.UUID, 0, len(listed))
	for _, g := range listed {
		fids = append(fids, g.FileID)
	}
	info, err := failedSnapshotInfo(ctx, r.m.svc.pool, fids)
	if err != nil {
		r.m.log.Warn("存储迁移：查失败快照的位置失败", "error", err)
	}
	f := &MigrationFailure{StorageKey: key, ReasonCode: code, Reason: reason, FailedAt: time.Now().UTC(),
		Snapshots: make([]FailedSnapshot, 0, len(listed)), SnapshotCount: len(rows)}
	for _, g := range listed {
		s := info[g.FileID]
		s.SnapshotID, s.FileID, s.VersionNo = g.SnapshotID, g.FileID, g.VersionNo
		f.Snapshots = append(f.Snapshots, s)
	}
	r.m.log.Warn("存储迁移：核对不一致，没有迁", "storage_key", key, "reason", reason, "snapshots", len(rows))
	return f
}
