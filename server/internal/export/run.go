package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/export/bundle"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/jobs"
	"doclite/server/internal/platform/s3"
	"doclite/server/internal/project"
)

// 执行（requirements 5.11"执行"）：导出在后台执行器（internal/platform/jobs）里的 Handler。
//
//  1. 条目来自 payload（冻结的档号、题名、著录、分类路径、状态）；页用一条语句读出全部冻结的页（快照的大小、sha256、
//     mime、存储和 key），审核状态是执行时 folder_files.review_status 的当前值；
//  2. 临时盘检查：PDF 要最大的一件原图之和 × 1.2，原图包要最大的一页 × 1.2，另加 64 MiB；不够时第 1 次执行重试，
//     之后失败；
//  3. 导出物写到执行时的默认存储，key `<path_prefix>exports/<project_code>/<task_id>/export-<第几次执行>.zip`；开始前
//     在一个短事务里记到任务上（上次执行留下的另一个 key 提交后删掉）；
//  4. bundle.Generate 边生成边分片上传；进度每页报给执行器（它每秒最多写一次库、发一次事件）；
//  5. 成功：一个事务里先写每页一条 file_logs `export`（不锁 job 行和任务行），再锁 job 行 → 任务行，任务 done（带
//     `WHERE status = 'running'`）、job done；任务已经取消就整个回滚、删掉刚传完的导出物；
//  6. 失败、取消：中止分片上传（不留对象），bundle 自己删临时文件。原图有问题时用 bundle.Inspect 把剩下的页查一遍，
//     一次列出全部有问题的页（最多 50 条），不重试；读写 S3、临时盘、写库出错可以重试。
//
// 生成、上传期间不开事务、不拿锁。

// Handler 是导出在执行器里的 Handler（app 里 Register(JobType, …)）。
func (s *Service) JobHandler() jobs.Handler { return &runner{s: s} }

type runner struct{ s *Service }

var _ jobs.Handler = (*runner)(nil)

// taskID 是 job 对应的导出任务（payload 的 export_task_id；缓存）。
func (r *runner) taskID(job *jobs.Job) (uuid.UUID, error) {
	if v, ok := r.s.taskOfJob.Load(job.ID); ok {
		return v.(uuid.UUID), nil
	}
	var head struct {
		ExportTaskID uuid.UUID `json:"export_task_id"`
	}
	if err := json.Unmarshal(job.Payload, &head); err != nil || head.ExportTaskID == uuid.Nil {
		return uuid.Nil, errInternal("后台任务 %s 的 payload 里没有 export_task_id", job.ID)
	}
	r.s.taskOfJob.Store(job.ID, head.ExportTaskID)
	return head.ExportTaskID, nil
}

func (r *runner) Claimed(ctx context.Context, tx pgx.Tx, job *jobs.Job) error {
	tid, err := r.taskID(job)
	if err != nil {
		return jobs.Fail("导出任务的后台任务不对（内部错误）：%v", err)
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM export_tasks WHERE id = $1 FOR NO KEY UPDATE`, tid).Scan(&status)
	if db.IsNoRows(err) {
		return jobs.Fail("导出任务 %s 不存在（内部错误）", tid)
	}
	if err != nil {
		return err
	}
	switch status {
	case StatusCanceled:
		return jobs.ErrCanceled
	case StatusQueued, StatusRunning:
		_, err = tx.Exec(ctx, `UPDATE export_tasks SET status = 'running' WHERE id = $1`, tid)
		return err
	}
	return jobs.Fail("导出任务已经是“%s”，不再执行", statusLabel(status))
}

func (r *runner) CancelRequested(ctx context.Context, q db.Querier, job *jobs.Job) (bool, error) {
	tid, err := r.taskID(job)
	if err != nil {
		return false, err
	}
	var canceled bool
	err = q.QueryRow(ctx, `SELECT status = 'canceled' FROM export_tasks WHERE id = $1`, tid).Scan(&canceled)
	if db.IsNoRows(err) {
		return false, nil
	}
	return canceled, err
}

func (r *runner) Settled(ctx context.Context, tx pgx.Tx, job *jobs.Job, to jobs.Status) error {
	tid, err := r.taskID(job)
	if err != nil {
		return err
	}
	switch to {
	case jobs.Queued:
		_, err = tx.Exec(ctx, `UPDATE export_tasks SET status = 'queued' WHERE id = $1 AND status = 'running'`, tid)
	case jobs.Failed:
		_, err = tx.Exec(ctx, `UPDATE export_tasks SET status = 'failed', finished_at = now() WHERE id = $1 AND status = 'running'`, tid)
	}
	// Canceled：任务早就是 canceled 了（取消接口改的），job 清理完不用再改任务。
	return err
}

func (r *runner) Notify(job *jobs.Job) {
	tid, err := r.taskID(job)
	if err != nil {
		return
	}
	r.s.events.Publish(events.ExportChanged(job.ProjectID, tid))
}

func (r *runner) Run(ctx context.Context, job *jobs.Job, run *jobs.Run) error {
	return r.s.run(ctx, job, run)
}

// 临时盘：固定多留的空间。
const tmpReserve = 64 << 20

func (s *Service) run(ctx context.Context, job *jobs.Job, run *jobs.Run) error {
	pl, err := parsePayload(job.Payload)
	if err != nil {
		return err
	}
	taskID := pl.ExportTaskID
	t, err := runTask(ctx, s.pool, taskID)
	if err != nil {
		return retryDB(ctx, "读导出任务", err)
	}
	if t == nil {
		return errInternal("导出任务 %s 不存在", taskID)
	}
	bjob, err := s.buildJob(ctx, t, pl)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.tmpDir, 0o755); err != nil {
		return jobs.Retry(fmt.Errorf("建临时目录失败：%w", err))
	}
	if err := checkTmpSpace(s.tmpDir, bjob, job.Attempt); err != nil {
		return err
	}

	target, err := s.storage.DefaultTarget(ctx)
	if err != nil {
		if he, ok := httpx.AsError(err); ok {
			return errors.New("没法写导出物：" + he.Message)
		}
		return retryDB(ctx, "读默认存储", err)
	}
	code, err := file.ProjectCode(ctx, s.pool, t.ProjectID)
	if err != nil {
		return retryDB(ctx, "读项目编码", err)
	}
	key := fmt.Sprintf("%sexports/%s/%s/export-%d.zip", target.PathPrefix, code, taskID, job.Attempt)
	if err := s.recordOutput(ctx, job, taskID, target.ProfileID, key); err != nil {
		return err
	}

	var total int64
	for _, it := range bjob.Items {
		for _, p := range it.Pages {
			total += p.Size
		}
	}
	src := newSource(s.storage)
	partSize := s.partSize
	if partSize <= 0 {
		partSize = s3.PartSizeFor(total)
	}
	up := target.Client.NewUpload(ctx, key, partSize, s3.PutOptions{ContentType: "application/zip"})
	// 没完成就中止（含 panic）：不留下对象，也不留下没完成的分片上传。
	completed := false
	defer func() {
		if !completed {
			_ = up.Abort()
		}
	}()
	opts := bundle.Options{TmpDir: s.tmpDir, Now: s.now().In(exportZone)}
	_, err = bundle.Generate(ctx, bjob, src, up, opts, func(p bundle.Progress) { run.Progress(p.PagesDone) })
	if err != nil {
		_ = up.Abort()
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return s.generateFailed(ctx, bjob, src, opts, err)
	}
	size, err := up.Complete()
	completed = err == nil
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return jobs.Retry(fmt.Errorf("上传导出物失败：%w", err))
	}

	// 传完了：记录完成不受退出、取消信号影响（任务被取消由事务里的状态判断）。
	cctx := context.WithoutCancel(ctx)
	err = s.complete(cctx, job, t, pl, bjob, size)
	if err == nil {
		return nil
	}
	if errors.Is(err, jobs.ErrCanceled) || errors.Is(err, jobs.ErrLost) {
		if derr := target.Client.Delete(cctx, key); derr != nil {
			s.log.Warn("导出：任务已取消，删掉刚传完的导出物失败（交给到期清理）", "task_id", taskID, "key", key, "error", derr)
		}
		return err
	}
	// 写库失败：导出物留着（任务上记着这个 key），下一次执行开始时删掉。
	return jobs.Retry(fmt.Errorf("记录导出完成失败：%w", err))
}

// retryDB 把读写库的错误标成可以重试（ctx 已经取消时返回取消的原因）。
func retryDB(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return jobs.Retry(fmt.Errorf("%s失败：%w", what, err))
}

// buildJob 把 payload 的条目和冻结的页组装成 bundle.Job（条目按导出顺序，页按页号）。
func (s *Service) buildJob(ctx context.Context, t *taskRow, pl *payload) (*bundle.Job, error) {
	pages, err := runPages(ctx, s.pool, t.ID)
	if err != nil {
		return nil, retryDB(ctx, "读冻结的页", err)
	}
	if len(pages) != pl.PageCount {
		return nil, errInternal("冻结的页有 %d 页，payload 里是 %d 页", len(pages), pl.PageCount)
	}
	byItem := map[uuid.UUID][]bundle.Page{}
	for _, p := range pages {
		var sha [32]byte
		if len(p.SHA256) != len(sha) {
			return nil, errInternal("快照 %s 的 sha256 不对", p.SnapshotID)
		}
		copy(sha[:], p.SHA256)
		up := ""
		if p.UploadName != nil {
			up = *p.UploadName
		}
		byItem[p.ItemID] = append(byItem[p.ItemID], bundle.Page{
			PageNo: p.PageNo, FileID: p.FileID, SnapshotID: p.SnapshotID, VersionNo: p.VersionNo, SHA256: sha, Size: p.Size,
			MIME: p.MIME, UploadName: up, ReviewStatus: p.ReviewStatus, StorageProfileID: p.StorageProfileID, StorageKey: p.StorageKey,
		})
	}
	j := &bundle.Job{Format: bundle.Format(t.Format), FieldNames: make([]string, len(pl.ItemFields))}
	for i, f := range pl.ItemFields {
		j.FieldNames[i] = f.Label
	}
	for _, it := range pl.Items {
		ps := byItem[it.ID]
		if len(ps) == 0 {
			return nil, errInternal("条目 %s 没有冻结的页", it.ID)
		}
		slices.SortFunc(ps, func(a, b bundle.Page) int { return a.PageNo - b.PageNo })
		values := make([]string, len(pl.ItemFields))
		for i, f := range pl.ItemFields {
			values[i] = fieldText(it.ExtraFields[f.Key])
		}
		j.Items = append(j.Items, bundle.Item{ArchiveNo: str(it.ArchiveNo), Title: it.Title, FieldValues: values,
			CategoryNames: it.CategoryNames, Status: it.Status, Pages: ps})
	}
	return j, nil
}

// fieldText 是著录值写进目录.xlsx 的文本（字符串原样，没有值为空，其他按 JSON）。
func fieldText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// checkTmpSpace：临时盘够不够（PDF 要最大的一件原图之和 × 1.2，原图包要最大的一页 × 1.2，另加 64 MiB 和每页 1 KiB）。
// 不够时第 1 次执行重试一次（可能正被别的导出、上传占着），之后不再重试。查不了剩余空间时不查。
func checkTmpSpace(dir string, j *bundle.Job, attempt int) error {
	free, ok := freeSpace(dir)
	if !ok {
		return nil
	}
	var largest int64
	var largestWhat string
	pages := 0
	for _, it := range j.Items {
		var sum int64
		for i, p := range it.Pages {
			pages++
			sum += p.Size
			if j.Format == bundle.FormatImages && p.Size > largest {
				largest, largestWhat = p.Size, fmt.Sprintf("最大的一页（档号 %s 第 %d 页，%s）", it.ArchiveNo, i+1, file.HumanBytes(p.Size))
			}
		}
		if j.Format == bundle.FormatPDF && sum > largest {
			largest, largestWhat = sum, fmt.Sprintf("最大的一件（档号 %s，%d 页，原图共 %s）", it.ArchiveNo, len(it.Pages), file.HumanBytes(sum))
		}
	}
	need := largest*6/5 + tmpReserve + int64(pages)<<10
	if free >= need {
		return nil
	}
	msg := fmt.Sprintf("临时盘（%s）剩 %s，这次导出要 %s：%s", dir, file.HumanBytes(free), file.HumanBytes(need), largestWhat)
	if attempt == 1 {
		return jobs.Retry(errors.New(msg + "（临时盘可能正被别的导出、上传占着，会自动重试一次）"))
	}
	return errors.New(msg + "。请腾出临时盘的空间后重新导出")
}

// recordOutput 把这次的导出物记到任务上。任务上已经记着上次执行留下的另一个导出物时，先删掉它（不受取消信号影响），
// 删不掉就可以重试、不覆盖（不然它就没人记着了）；删掉之后再在一个短事务里改（锁 job 行核对是这次执行的 → 锁任务
// 行，还是 running、记着的还是刚才删的那个才改）。任务已取消返回 jobs.ErrCanceled。
func (s *Service) recordOutput(ctx context.Context, job *jobs.Job, taskID, profileID uuid.UUID, key string) error {
	var status string
	var oldProfile *uuid.UUID
	var oldKey *string
	if err := s.pool.QueryRow(ctx, `SELECT status, storage_profile_id, output_key FROM export_tasks WHERE id = $1`, taskID).
		Scan(&status, &oldProfile, &oldKey); err != nil {
		return retryDB(ctx, "读导出物的位置", err)
	}
	if status != StatusRunning {
		return jobs.ErrCanceled
	}
	if oldKey != nil && oldProfile != nil && (*oldKey != key || *oldProfile != profileID) {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		c, err := s.storage.Client(dctx, *oldProfile)
		if err == nil {
			err = c.Delete(dctx, *oldKey)
		}
		cancel()
		if err != nil {
			return jobs.Retry(fmt.Errorf("删上次执行留下的导出物 %s 失败：%w", *oldKey, err))
		}
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		owned, err := jobs.LockOwned(ctx, tx, job)
		if err != nil {
			return err
		}
		if !owned {
			return jobs.ErrLost
		}
		var cur *string
		if err := tx.QueryRow(ctx, `SELECT status, output_key FROM export_tasks WHERE id = $1 FOR NO KEY UPDATE`, taskID).
			Scan(&status, &cur); err != nil {
			return err
		}
		if status != StatusRunning {
			return jobs.ErrCanceled
		}
		if str(cur) != str(oldKey) {
			return errors.New("任务上记着的导出物变了")
		}
		_, err = tx.Exec(ctx, `UPDATE export_tasks SET storage_profile_id = $2, output_key = $3, output_size = NULL
WHERE id = $1 AND status = 'running'`, taskID, profileID, key)
		return err
	})
	if err != nil {
		if errors.Is(err, jobs.ErrCanceled) || errors.Is(err, jobs.ErrLost) {
			return err
		}
		return retryDB(ctx, "记下导出物的位置", err)
	}
	return nil
}

// complete 记录完成：一个事务里先写每页一条 file_logs export（不锁 job 行和任务行），再锁 job 行（核对是这次执行的）
// → 任务行，任务 done（带 WHERE status = 'running'，到期时间按项目设置 export.retention_days）、job done。任务已经
// 取消返回 jobs.ErrCanceled（整个回滚）。
func (s *Service) complete(ctx context.Context, job *jobs.Job, t *taskRow, pl *payload, bjob *bundle.Job, size int64) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		logs := make([]file.Log, 0, pl.PageCount)
		for i, it := range bjob.Items {
			for _, p := range it.Pages {
				logs = append(logs, file.Log{ProjectID: t.ProjectID, FileID: p.FileID, Action: file.ActionExport, OperatorID: t.CreatedBy,
					Detail: map[string]any{"export_task_id": t.ID, "archive_no": pl.Items[i].ArchiveNo, "page_no": p.PageNo,
						"version_no": p.VersionNo, "snapshot_id": p.SnapshotID, "export_format": t.Format}})
			}
		}
		if err := file.WriteLogs(ctx, tx, logs...); err != nil {
			return err
		}
		owned, err := jobs.LockOwned(ctx, tx, job)
		if err != nil {
			return err
		}
		if !owned {
			return jobs.ErrLost
		}
		var status string
		var settings []byte
		if err := tx.QueryRow(ctx, `SELECT t.status, p.settings FROM export_tasks t JOIN projects p ON p.id = t.project_id
WHERE t.id = $1 FOR NO KEY UPDATE OF t`, t.ID).Scan(&status, &settings); err != nil {
			return err
		}
		if status != StatusRunning {
			return jobs.ErrCanceled
		}
		days := project.ParseSettings(settings).RetentionDays
		if _, err := tx.Exec(ctx, `UPDATE export_tasks SET status = 'done', finished_at = now(), output_size = $2,
  expires_at = now() + make_interval(days => $3)
WHERE id = $1 AND status = 'running'`, t.ID, size, days); err != nil {
			return err
		}
		return jobs.MarkDone(ctx, tx, job)
	})
}

// ---------------------------------------------------------------------------
// 原图有问题

// maxListedProblems：失败信息里最多列几页。
const maxListedProblems = 50

// inspectWorkers：检查剩下的页时同时查几页。
const inspectWorkers = 4

var problemLabels = map[bundle.Kind]string{
	bundle.KindIntegrity:   "原图和快照对不上",
	bundle.KindCorrupt:     "原图损坏",
	bundle.KindUnsupported: "格式不支持",
	bundle.KindTooLarge:    "原图太大",
}

func describeProblem(e *bundle.Error) string {
	return fmt.Sprintf("档号 %s 第 %d 页（file_id %s，snapshot_id %s）%s：%s", e.ArchiveNo, e.PageNo, e.FileID, e.SnapshotID,
		problemLabels[e.Kind], e.Reason)
}

// generateFailed 处理 Generate 的错误：原图有问题时把剩下的页只看文件头查一遍，一次列出全部有问题的页（不重试）；
// 其他按 bundle 的分类（读原图、临时盘、写导出物可以重试）。
func (s *Service) generateFailed(ctx context.Context, j *bundle.Job, src bundle.PageSource, opts bundle.Options, err error) error {
	var be *bundle.Error
	if !errors.As(err, &be) || !be.PageProblem() {
		return err
	}
	type target struct {
		item bundle.Item
		page bundle.Page
	}
	var rest []target
	for i := max(be.ItemIndex, 0); i < len(j.Items); i++ {
		from := 0
		if i == be.ItemIndex {
			from = be.PageIndex + 1
		}
		for k := from; k < len(j.Items[i].Pages); k++ {
			rest = append(rest, target{item: j.Items[i], page: j.Items[i].Pages[k]})
		}
	}
	results := make([]error, len(rest))
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < inspectWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				results[i] = bundle.Inspect(ctx, src, j.Format, rest[i].item, rest[i].page, opts)
			}
		}()
	}
	for i := range rest {
		if ctx.Err() != nil {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	problems := []string{describeProblem(be)}
	unchecked := 0
	for _, r := range results {
		var pe *bundle.Error
		switch {
		case r == nil:
		case errors.As(r, &pe) && pe.PageProblem():
			problems = append(problems, describeProblem(pe))
		default:
			unchecked++
		}
	}
	return errors.New(pageProblemsMessage(problems, unchecked))
}

// pageProblemsMessage 是原图有问题时的失败信息。
func pageProblemsMessage(problems []string, unchecked int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "有 %d 页原图有问题，重试没用。请重扫这些页，或者把它们移出条目后重新导出：", len(problems))
	for i, p := range problems {
		if i == maxListedProblems {
			fmt.Fprintf(&b, "\n另外 %d 页", len(problems)-maxListedProblems)
			break
		}
		fmt.Fprintf(&b, "\n%d. %s", i+1, p)
	}
	if unchecked > 0 {
		fmt.Fprintf(&b, "\n（另有 %d 页检查时读原图失败，没能检查）", unchecked)
	}
	b.WriteString("\n（除第 1 条外是只看文件头检查出来的；大小、sha256 对不上和压缩数据内部的损坏要正式导出时才发现）")
	return b.String()
}
