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

// errShutdown 是退出时停下执行的原因（context.Cause；取消、不归这次执行了用 ErrCanceled、ErrLost）。
var errShutdown = errors.New("server 正在退出")

// whyRestart 是启动时放回队列写进 error_message 的原因。
const whyRestart = "执行中断（server 被停止或重启）"

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
	// running 是这个进程里正在执行的 job（含停下后正在改状态的），Interrupt 用。
	running map[uuid.UUID]context.CancelCauseFunc
}

// session 是一轮运行（Start 到 Stop）。
type session struct {
	base    context.Context
	stop    context.CancelCauseFunc
	slots   chan struct{}
	workers sync.WaitGroup
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

// OnStart 登记 Start 时、开始领取之前要做的事（清理临时目录之类）。
func (e *Executor) OnStart(fn func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onStart = append(e.onStart, fn)
}

// Enabled：Concurrency 大于 0（这个进程要执行后台任务）。
func (e *Executor) Enabled() bool { return e.opts.Concurrency > 0 }

func (e *Executor) types() []string {
	out := make([]string, 0, len(e.handlers))
	for t := range e.handlers {
		out = append(out, t)
	}
	return out
}

// Start 启动：OnStart 登记的事 → 把上次留下的 running 放回队列（[Executor.reap]）→ 开始领取。Concurrency 为 0 时
// 什么都不做。ctx 只用于启动这一段；Stop 之后可以再 Start（测试里模拟重启）。
func (e *Executor) Start(ctx context.Context) error {
	if !e.Enabled() {
		return nil
	}
	ss := &session{slots: make(chan struct{}, e.opts.Concurrency)}
	ss.base, ss.stop = context.WithCancelCause(context.Background())
	e.mu.Lock()
	if e.cur != nil {
		e.mu.Unlock()
		return errors.New("jobs: 已经在运行了")
	}
	e.cur = ss
	hooks := append([]func(){}, e.onStart...)
	e.mu.Unlock()
	for _, fn := range hooks {
		fn()
	}
	if n, err := e.reap(ctx); err != nil {
		e.log.Error("后台任务：启动时放回队列失败", "error", err)
	} else if n > 0 {
		e.log.Info("后台任务：上次执行中的已放回队列", "count", n)
	}
	ss.workers.Add(1)
	go e.loop(ss)
	return nil
}

// Stop 优雅退出：不再领取；正在执行的停下、job 放回队列（不算一次尝试）。最多等到 ctx 结束；来不及收尾的 job 留在
// running，下次启动时放回队列。
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
		cancel(ErrCanceled)
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
		job, h, settled, err := e.claim(ss.base)
		if err != nil && ss.base.Err() == nil {
			e.log.Error("后台任务：领取失败", "error", err)
		}
		if job == nil {
			<-ss.slots
			if settled {
				continue // 领到的不用执行（已经改好状态）：马上接着领
			}
			select {
			case <-e.wake:
			case <-time.After(e.opts.PollInterval):
			case <-ss.base.Done():
				return
			}
			continue
		}
		// 领到了：登记为执行中（Interrupt 能找到它）。
		ctx, cancel := context.WithCancelCause(ss.base)
		e.mu.Lock()
		e.running[job.ID] = cancel
		e.mu.Unlock()
		e.wg.Add(1)
		go e.execute(ss, ctx, cancel, job, h)
	}
}

// busy：这个进程正在执行这个 job。
func (e *Executor) busy(id uuid.UUID) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running[id] != nil
}

// claim 领一个：一个短事务，FOR UPDATE SKIP LOCKED 挑一个到时间的 queued，改成 running、attempts + 1，再让 Handler
// 把业务对象改成执行中。没有可领的返回 nil。业务对象已取消、没法执行的在这里就改好状态，也返回 nil，settled 为 true
// （循环马上接着领）。
func (e *Executor) claim(base context.Context) (job *Job, h Handler, settled bool, err error) {
	ctx, cancel := context.WithTimeout(base, dbTimeout)
	defer cancel()
	err = db.InTx(ctx, e.pool, func(tx pgx.Tx) error {
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
	if err != nil {
		return nil, nil, false, err
	}
	if job == nil {
		return nil, nil, false, nil
	}
	h.Notify(job)
	if settled {
		e.log.Info("后台任务：领到的任务不用执行", "job_id", job.ID, "type", job.Type)
		return nil, nil, true, nil
	}
	return job, h, false, nil
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
		cancel(ErrCanceled)
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
		// Run 刚在业务的事务里把这次执行改成了 done（结果还没交回来）：不是被别人接管，等 finish 收尾。
		var status string
		var attempt int
		if err := e.pool.QueryRow(ctx, `SELECT status, attempts FROM background_jobs WHERE id = $1`, job.ID).
			Scan(&status, &attempt); err == nil && status == string(Done) && attempt == job.Attempt {
			return
		}
		e.log.Error("后台任务：job 已经不归这次执行了，停下", "job_id", job.ID)
		cancel(ErrLost)
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
		cancel(ErrCanceled)
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

// finish 按 Run 的结果改 job 的状态。改不成（写库失败）时 job 留在 running，下次启动时放回队列。
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
	case errors.Is(cause, ErrLost) || errors.Is(err, ErrLost):
		e.log.Error("后台任务：job 已经不归这次执行了", "job_id", job.ID, "error", err)
	case errors.Is(cause, ErrCanceled) || errors.Is(err, ErrCanceled):
		e.log.Info("后台任务：已取消，清理完", "job_id", job.ID)
		e.settle(ctx, job, h, outCanceled, "")
	case errors.Is(cause, errShutdown):
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
// 改成 canceled。失败时只记日志：job 留在 running，下次启动时放回队列。
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
		e.log.Error("后台任务：改状态失败，下次启动时放回队列", "job_id", job.ID, "error", err)
		return
	}
	h.Notify(job)
}

// reap 在启动时按"执行中断"处理库里的 running（上次进程被杀、或者改状态写库失败留下的）：业务对象已取消的改成
// canceled；次数用完的改成 failed；其余放回队列（算一次尝试，run_after 是现在）。每个 job 一个短事务。
// 这个进程里还在执行的跳过（上次 Stop 没等到它收尾就又 Start 了）。
func (e *Executor) reap(ctx context.Context) (int, error) {
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
WHERE id = $1`, id, FailureMessage(j.Attempt, whyRestart, 0))
			default:
				to = Queued
				_, err = tx.Exec(ctx, `UPDATE background_jobs SET status = 'queued', run_after = now(), runner_id = NULL,
  heartbeat_at = NULL, progress_done = 0, error_message = $2
WHERE id = $1`, id, fmt.Sprintf("第 %d 次%s，已放回队列", j.Attempt, whyRestart))
			}
			if err != nil {
				return err
			}
			job = j
			return h.Settled(ctx, tx, j, to)
		})
		if err != nil {
			// 这一个改不成（下次启动再试），不挡后面的。
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
