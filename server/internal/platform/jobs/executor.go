package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/platform/db"
)

// 单实例锁：会话级咨询锁 (6006, 0)，在执行器专用的一条连接上持有（类别号登记在 project/locks.go 的注释里）。
const (
	lockClass = 6006
	lockKey   = 0
)

// ErrBusy：另一个 server 进程正拿着后台任务的锁（Start 不启动）。
var ErrBusy = errors.New("另一个 server 进程正在执行后台任务")

// 一次执行被停下的原因（context.Cause）。
var (
	errShutdown = errors.New("server 正在退出")
	errCancel   = errors.New("任务已取消")
	errLost     = errors.New("后台任务已经不归这次执行了")
	errLockLost = errors.New("失去了后台任务的锁")
)

// 回收时写进 error_message 的原因。
const (
	whyRestart = "执行中断（server 被停止或重启）"
	whyOrphan  = "执行中断（没能记下执行结果）"
)

// Executor 是执行器。用 [New] 建，[Executor.Register] 注册各种任务，[Executor.Start] 启动，[Executor.Stop] 退出。
type Executor struct {
	pool     *pgxpool.Pool
	opts     Options
	log      *slog.Logger
	runnerID string
	handlers map[string]Handler
	onStart  []func()

	wake chan struct{}

	mu  sync.Mutex
	cur *session // 正在运行的这一轮（Start 到 Stop）；没在运行时为 nil
	wg  sync.WaitGroup
	// running 是这个进程里正在执行的 job（含停下后正在改状态的）；claiming 是正在领取（job 行已锁、事务还没提交或者
	// 还没交给执行）的。回收跳过这两类。
	running  map[uuid.UUID]context.CancelCauseFunc
	claiming map[uuid.UUID]bool
}

// session 是一轮运行（Start 到 Stop）。
type session struct {
	base    context.Context
	stop    context.CancelCauseFunc
	slots   chan struct{}
	lock    *pgx.Conn     // 拿着咨询锁的专用连接（只有 keeper 用）
	release chan struct{} // 关闭时 keeper 释放锁、退出
	workers sync.WaitGroup
	keeper  chan struct{} // keeper 退出时关闭
}

// New 建执行器（还不启动）。
func New(pool *pgxpool.Pool, opts Options, log *slog.Logger) *Executor {
	if log == nil {
		log = slog.Default()
	}
	host, _ := os.Hostname()
	return &Executor{
		pool:     pool,
		opts:     opts.withDefaults(),
		log:      log,
		runnerID: fmt.Sprintf("%s:%d", host, os.Getpid()),
		handlers: map[string]Handler{},
		wake:     make(chan struct{}, 1),
		running:  map[uuid.UUID]context.CancelCauseFunc{},
		claiming: map[uuid.UUID]bool{},
	}
}

// Register 注册一种任务（Start 之前）。
func (e *Executor) Register(jobType string, h Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cur != nil {
		panic("jobs: Register 要在 Start 之前")
	}
	e.handlers[jobType] = h
}

// OnStart 登记 Start 拿到锁之后、开始领取之前要做的事（清理临时目录之类：只有拿到锁的进程才能动它们）。
func (e *Executor) OnStart(fn func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onStart = append(e.onStart, fn)
}

// Enabled：Concurrency 大于 0（这个进程要执行后台任务）。
func (e *Executor) Enabled() bool { return e.opts.Concurrency > 0 }

// Active：正在运行（拿着锁、在领取）。
func (e *Executor) Active() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cur != nil
}

func (e *Executor) types() []string {
	out := make([]string, 0, len(e.handlers))
	for t := range e.handlers {
		out = append(out, t)
	}
	return out
}

// tryLock 在一条新连接上拿单实例锁。拿不到时 conn 为 nil。
func (e *Executor) tryLock(ctx context.Context) (*pgx.Conn, error) {
	conn, err := pgx.ConnectConfig(ctx, e.pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, err
	}
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::int4, $2::int4)`, lockClass, lockKey).Scan(&ok); err != nil || !ok {
		conn.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return conn, nil
}

// Start 启动：拿单实例锁（拿不到返回 [ErrBusy]，什么都不做）→ OnStart 登记的事 → 回收上次留下的 running（放回队列）
// → 开始领取，并且每个心跳间隔回收一次。Concurrency 为 0 时什么都不做。ctx 只用于启动这一段；Stop 之后可以再 Start
// （测试里模拟重启）。
func (e *Executor) Start(ctx context.Context) error {
	if !e.Enabled() {
		return nil
	}
	if e.Active() {
		return errors.New("jobs: 已经在运行了")
	}
	conn, err := e.tryLock(ctx)
	if err != nil {
		return fmt.Errorf("后台任务：拿单实例锁失败：%w", err)
	}
	if conn == nil {
		return ErrBusy
	}
	ss := &session{slots: make(chan struct{}, e.opts.Concurrency), lock: conn, release: make(chan struct{}), keeper: make(chan struct{})}
	ss.base, ss.stop = context.WithCancelCause(context.Background())
	e.mu.Lock()
	if e.cur != nil {
		e.mu.Unlock()
		conn.Close(ctx)
		return errors.New("jobs: 已经在运行了")
	}
	e.cur = ss
	hooks := append([]func(){}, e.onStart...)
	e.mu.Unlock()
	for _, fn := range hooks {
		fn()
	}
	if n, err := e.reap(ctx, whyRestart); err != nil {
		e.log.Error("后台任务：启动时放回队列失败", "error", err)
	} else if n > 0 {
		e.log.Info("后台任务：上次执行中的已放回队列", "count", n)
	}
	ss.workers.Add(2)
	go e.loop(ss)
	go e.reaper(ss)
	go e.keep(ss)
	return nil
}

// Stop 优雅退出：不再领取；正在执行的停下、job 放回队列（不算一次尝试）；然后释放单实例锁。最多等到 ctx 结束；
// 来不及收尾的 job 留在 running，下次启动时回收。
func (e *Executor) Stop(ctx context.Context) {
	e.mu.Lock()
	ss := e.cur
	e.cur = nil
	e.mu.Unlock()
	if ss == nil {
		return
	}
	ss.stop(errShutdown)
	done := make(chan struct{})
	go func() {
		ss.workers.Wait()
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		e.log.Warn("后台任务：退出时没等到正在执行的收尾，下次启动时放回队列")
	}
	close(ss.release)
	select {
	case <-ss.keeper:
	case <-ctx.Done():
	}
}

// Wake 让执行器马上看一次有没有可领的任务（建任务之后调用）。没启动时什么都不做。
func (e *Executor) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Interrupt 停下正在这个进程里执行的 job（业务对象刚被取消）。不在执行的什么都不做。
func (e *Executor) Interrupt(jobID uuid.UUID) {
	e.mu.Lock()
	cancel := e.running[jobID]
	e.mu.Unlock()
	if cancel != nil {
		cancel(errCancel)
	}
}

// keep 守着单实例锁：每个心跳间隔看一次专用连接；断了就重新拿锁，拿不到就停下执行器（执行中的放回队列）。
// release 关闭时释放锁、关连接、退出。
func (e *Executor) keep(ss *session) {
	defer close(ss.keeper)
	t := time.NewTicker(e.opts.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ss.release:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = ss.lock.Exec(ctx, `SELECT pg_advisory_unlock($1::int4, $2::int4)`, lockClass, lockKey)
			ss.lock.Close(ctx)
			cancel()
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
		_, err := ss.lock.Exec(ctx, `SELECT 1`)
		if err == nil {
			cancel()
			continue
		}
		e.log.Warn("后台任务：拿着单实例锁的连接断了，重新拿锁", "error", err)
		ss.lock.Close(ctx)
		conn, lerr := e.tryLock(ctx)
		cancel()
		if conn != nil {
			ss.lock = conn
			continue
		}
		e.log.Error("后台任务：重新拿单实例锁失败，停下执行器（执行中的放回队列）", "error", lerr)
		e.mu.Lock()
		if e.cur == ss {
			e.cur = nil
		}
		e.mu.Unlock()
		ss.stop(errLockLost)
		return
	}
}

func (e *Executor) loop(ss *session) {
	defer ss.workers.Done()
	for {
		select {
		case ss.slots <- struct{}{}:
		case <-ss.base.Done():
			return
		}
		job, h, err := e.claim(ss.base)
		if err != nil && ss.base.Err() == nil {
			e.log.Error("后台任务：领取失败", "error", err)
		}
		if job == nil {
			<-ss.slots
			select {
			case <-e.wake:
			case <-time.After(e.opts.PollInterval):
			case <-ss.base.Done():
				return
			}
			continue
		}
		// 领到了：登记为执行中（同一把锁里去掉"领取中"），回收不会碰它。
		ctx, cancel := context.WithCancelCause(ss.base)
		e.mu.Lock()
		e.running[job.ID] = cancel
		delete(e.claiming, job.ID)
		e.mu.Unlock()
		e.wg.Add(1)
		go e.execute(ss, ctx, cancel, job, h)
	}
}

// reaper 每个心跳间隔回收一次没人执行的 running。
func (e *Executor) reaper(ss *session) {
	defer ss.workers.Done()
	t := time.NewTicker(e.opts.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ss.base.Done():
			return
		case <-t.C:
		}
		if n, err := e.reap(ss.base, whyOrphan); err != nil && ss.base.Err() == nil {
			e.log.Error("后台任务：回收失败", "error", err)
		} else if n > 0 {
			e.log.Warn("后台任务：回收了没人执行的 running", "count", n)
		}
	}
}

// Executing：这个进程正在执行（含正在领取）这个 job。
func (e *Executor) Executing(id uuid.UUID) bool { return e.busy(id) }

// busy：这个进程正在执行或者正在领取这个 job。
func (e *Executor) busy(id uuid.UUID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running[id] != nil || e.claiming[id]
}

// claim 领一个：一个短事务，FOR UPDATE SKIP LOCKED 挑一个到时间的 queued，改成 running、attempts + 1，再让 Handler
// 把业务对象改成执行中。没有可领的返回 nil。业务对象已取消、没法执行的在这里就改好状态，也返回 nil（循环接着领）。
// 锁住 job 行之后、提交之前就登记为"领取中"，提交之后到交给执行之间回收也不会碰它。
func (e *Executor) claim(base context.Context) (*Job, Handler, error) {
	ctx, cancel := context.WithTimeout(base, dbTimeout)
	defer cancel()
	var job *Job
	var h Handler
	var settled bool
	var claimedID uuid.UUID
	err := db.InTx(ctx, e.pool, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM background_jobs
WHERE status = 'queued' AND run_after <= now() AND job_type = ANY($1)
ORDER BY priority DESC, run_after
LIMIT 1
FOR UPDATE SKIP LOCKED`, e.types()).Scan(&id)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		claimedID = id
		e.mu.Lock()
		e.claiming[id] = true
		e.mu.Unlock()
		j, err := scanJob(tx.QueryRow(ctx, `UPDATE background_jobs SET status = 'running', runner_id = $2, started_at = now(),
  heartbeat_at = now(), attempts = attempts + 1, progress_done = 0, error_message = NULL, finished_at = NULL
WHERE id = $1
RETURNING `+jobColumns, id, e.runnerID))
		if err != nil {
			return err
		}
		h = e.handlers[j.Type]
		err = h.Claimed(ctx, tx, j)
		var f *Failure
		switch {
		case err == nil:
			job = j
			return nil
		case errors.Is(err, ErrCanceled):
			settled = true
			_, err = tx.Exec(ctx, `UPDATE background_jobs SET status = 'canceled', finished_at = now(), runner_id = NULL WHERE id = $1`, j.ID)
		case errors.As(err, &f):
			settled = true
			_, err = tx.Exec(ctx, `UPDATE background_jobs SET status = 'failed', finished_at = now(), runner_id = NULL, error_message = $2
WHERE id = $1`, j.ID, f.Message)
		}
		if err == nil && settled {
			job = j
		}
		return err
	})
	if err != nil || job == nil || settled {
		if claimedID != uuid.Nil {
			e.mu.Lock()
			delete(e.claiming, claimedID)
			e.mu.Unlock()
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if job == nil {
		return nil, nil, nil
	}
	h.Notify(job)
	if settled {
		e.log.Info("后台任务：领到的任务不用执行", "job_id", job.ID, "type", job.Type)
		return nil, nil, nil
	}
	return job, h, nil
}

// execute 执行一次：Handler.Run 在自己的 goroutine 里跑，这里管心跳、进度、取消和最后的状态。
func (e *Executor) execute(ss *session, ctx context.Context, cancel context.CancelCauseFunc, job *Job, h Handler) {
	defer e.wg.Done()
	defer func() {
		<-ss.slots
		e.Wake()
	}()
	defer func() {
		e.mu.Lock()
		delete(e.running, job.ID)
		e.mu.Unlock()
		cancel(nil)
	}()
	e.log.Info("后台任务：开始执行", "job_id", job.ID, "type", job.Type, "attempt", job.Attempt)
	// 领取之后、登记之前取消的（Interrupt 时还找不到它）：马上再看一次。
	if canceled, err := h.CancelRequested(ctx, e.pool, job); err == nil && canceled {
		cancel(errCancel)
	}

	run := &Run{}
	result := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				result <- fmt.Errorf("执行时出错（内部错误）：%v", r)
			}
		}()
		result <- h.Run(ctx, job, run)
	}()
	heartbeat := time.NewTicker(e.opts.Heartbeat)
	defer heartbeat.Stop()
	tick := time.NewTicker(progressEvery)
	defer tick.Stop()
	written := int64(0)
	for {
		select {
		case err := <-result:
			e.finish(job, h, context.Cause(ctx), err)
			return
		case <-tick.C:
			if p := run.progress.Load(); p != written {
				written = p
				e.beat(job, h, &p, cancel)
			}
		case <-heartbeat.C:
			e.beat(job, h, nil, cancel)
		}
	}
}

// beat 刷新心跳（progress 不为 nil 时顺带写进度、发事件），查业务对象是不是已经取消。
func (e *Executor) beat(job *Job, h Handler, progress *int64, cancel context.CancelCauseFunc) {
	ctx, done := context.WithTimeout(context.Background(), dbTimeout)
	defer done()
	tag, err := e.pool.Exec(ctx, `UPDATE background_jobs SET heartbeat_at = now(), progress_done = COALESCE($3, progress_done)
WHERE id = $1 AND status = 'running' AND attempts = $2`, job.ID, job.Attempt, progress)
	if err != nil {
		e.log.Warn("后台任务：心跳写库失败", "job_id", job.ID, "error", err)
		return
	}
	if tag.RowsAffected() == 0 {
		e.log.Error("后台任务：job 已经不归这次执行了，停下", "job_id", job.ID)
		cancel(errLost)
		return
	}
	if progress != nil {
		h.Notify(job)
	}
	canceled, err := h.CancelRequested(ctx, e.pool, job)
	if err != nil {
		e.log.Warn("后台任务：查是否取消失败", "job_id", job.ID, "error", err)
		return
	}
	if canceled {
		cancel(errCancel)
	}
}

// outcome 是一次执行之后 job 要改成什么。
type outcome int

const (
	outRequeue outcome = iota // 退出时放回队列（不算一次尝试）
	outRetry                  // 排队重试
	outFailed
	outCanceled
)

// finish 按 Run 的结果改 job 的状态。改不成（写库失败）时 job 留在 running，由回收接手。
func (e *Executor) finish(job *Job, h Handler, cause, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	if err == nil {
		// Run 已经在业务的事务里把 job 改成了 done；防御：万一没改，按内部错误失败。
		var status string
		if qerr := e.pool.QueryRow(ctx, `SELECT status FROM background_jobs WHERE id = $1`, job.ID).Scan(&status); qerr == nil &&
			status == string(Running) {
			e.settle(ctx, job, h, outFailed, "执行结束了但没有记下结果（内部错误）")
			return
		}
		e.log.Info("后台任务：完成", "job_id", job.ID, "attempt", job.Attempt)
		h.Notify(job)
		return
	}
	switch {
	case errors.Is(cause, errLost) || errors.Is(err, ErrLost):
		e.log.Error("后台任务：job 已经不归这次执行了", "job_id", job.ID, "error", err)
	case errors.Is(cause, errCancel) || errors.Is(err, ErrCanceled):
		e.log.Info("后台任务：已取消，清理完", "job_id", job.ID)
		e.settle(ctx, job, h, outCanceled, "")
	case errors.Is(cause, errShutdown) || errors.Is(cause, errLockLost):
		e.log.Info("后台任务：退出时放回队列", "job_id", job.ID, "attempt", job.Attempt)
		e.settle(ctx, job, h, outRequeue, "")
	case Retryable(err) && job.Attempt < job.MaxAttempts:
		delay := RetryDelay(e.opts.RetryBackoff, job.Attempt)
		e.log.Warn("后台任务：失败，排队重试", "job_id", job.ID, "attempt", job.Attempt, "delay", delay, "error", err)
		e.settle(ctx, job, h, outRetry, FailureMessage(job.Attempt, err.Error(), delay))
	default:
		e.log.Warn("后台任务：失败", "job_id", job.ID, "attempt", job.Attempt, "error", err)
		e.settle(ctx, job, h, outFailed, FailureMessage(job.Attempt, err.Error(), 0))
	}
}

// settle 在一个事务里改 job（先锁 job 行、核对是这次执行的）和业务对象（Handler.Settled）。业务对象已取消的一律
// 改成 canceled。失败时只记日志：job 留在 running、这次执行结束后不在 running 表里，下一轮回收接手。
func (e *Executor) settle(ctx context.Context, job *Job, h Handler, out outcome, message string) {
	err := db.InTx(ctx, e.pool, func(tx pgx.Tx) error {
		owned, err := LockOwned(ctx, tx, job)
		if err != nil || !owned {
			return err
		}
		canceled, err := h.CancelRequested(ctx, tx, job)
		if err != nil {
			return err
		}
		if canceled {
			out = outCanceled
		}
		var to Status
		switch out {
		case outCanceled:
			to = Canceled
			_, err = tx.Exec(ctx, `UPDATE background_jobs SET status = 'canceled', finished_at = now(), runner_id = NULL WHERE id = $1`, job.ID)
		case outRequeue:
			to = Queued
			_, err = tx.Exec(ctx, `UPDATE background_jobs SET status = 'queued', attempts = GREATEST(attempts - 1, 0), run_after = now(),
  runner_id = NULL, heartbeat_at = NULL, progress_done = 0
WHERE id = $1`, job.ID)
		case outRetry:
			to = Queued
			delay := RetryDelay(e.opts.RetryBackoff, job.Attempt)
			_, err = tx.Exec(ctx, `UPDATE background_jobs SET status = 'queued', run_after = now() + make_interval(secs => $2),
  runner_id = NULL, heartbeat_at = NULL, progress_done = 0, error_message = $3
WHERE id = $1`, job.ID, delay.Seconds(), message)
		case outFailed:
			to = Failed
			_, err = tx.Exec(ctx, `UPDATE background_jobs SET status = 'failed', finished_at = now(), runner_id = NULL, error_message = $2
WHERE id = $1`, job.ID, message)
		}
		if err != nil {
			return err
		}
		return h.Settled(ctx, tx, job, to)
	})
	if err != nil {
		e.log.Error("后台任务：改状态失败，留给回收", "job_id", job.ID, "error", err)
		return
	}
	h.Notify(job)
}

// RequeueRunning 回收库里 running 而这个进程里没有在执行、也没有在领取的 job（启动时、每个心跳间隔调用）。返回处理
// 了几个。
func (e *Executor) RequeueRunning(ctx context.Context) (int, error) { return e.reap(ctx, whyRestart) }

// reap 按"执行中断"处理没人执行的 running：业务对象已取消的改成 canceled；次数用完的改成 failed；其余放回队列（算
// 一次尝试，run_after 是现在）。每个 job 一个短事务，锁住 job 行之后再确认一次没人执行。
func (e *Executor) reap(ctx context.Context, why string) (int, error) {
	rows, err := e.pool.Query(ctx, `SELECT id FROM background_jobs WHERE status = 'running' AND job_type = ANY($1) ORDER BY id`, e.types())
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	n := 0
	var firstErr error
	for _, id := range ids {
		if e.busy(id) {
			continue
		}
		var job *Job
		var h Handler
		err := db.InTx(ctx, e.pool, func(tx pgx.Tx) error {
			j, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM background_jobs WHERE id = $1 AND status = 'running'
FOR NO KEY UPDATE`, id))
			if db.IsNoRows(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if e.busy(id) { // 锁住之前刚被领走（领取的事务提交前就登记了）
				return nil
			}
			h = e.handlers[j.Type]
			canceled, err := h.CancelRequested(ctx, tx, j)
			if err != nil {
				return err
			}
			var to Status
			switch {
			case canceled:
				to = Canceled
				_, err = tx.Exec(ctx, `UPDATE background_jobs SET status = 'canceled', finished_at = now(), runner_id = NULL WHERE id = $1`, id)
			case j.Attempt >= j.MaxAttempts:
				to = Failed
				_, err = tx.Exec(ctx, `UPDATE background_jobs SET status = 'failed', finished_at = now(), runner_id = NULL, error_message = $2
WHERE id = $1`, id, FailureMessage(j.Attempt, why, 0))
			default:
				to = Queued
				_, err = tx.Exec(ctx, `UPDATE background_jobs SET status = 'queued', run_after = now(), runner_id = NULL,
  heartbeat_at = NULL, progress_done = 0, error_message = $2
WHERE id = $1`, id, fmt.Sprintf("第 %d 次%s，已放回队列", j.Attempt, why))
			}
			if err != nil {
				return err
			}
			job = j
			return h.Settled(ctx, tx, j, to)
		})
		if err != nil {
			// 这一个改不成（下一轮再试），不挡后面的。
			if firstErr == nil {
				firstErr = fmt.Errorf("回收 job %s：%w", id, err)
			}
			continue
		}
		if job != nil {
			n++
			h.Notify(job)
			e.Wake()
		}
	}
	return n, firstErr
}
