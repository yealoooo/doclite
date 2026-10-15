// Package jobs 是后台任务执行器（硬性规则 13，requirements 5.11"执行"）：在 server 进程里运行，从 background_jobs
// 表用 `FOR UPDATE SKIP LOCKED` 领取任务，按 job_type 交给 [Handler] 执行。和业务无关：业务对象（导出任务）的
// 状态由 Handler 在执行器开的事务里改（领取、改状态的事务都先锁 job 行，再由 Handler 锁业务对象的行）。
//
// 单服务器版（硬性规则 16，requirements 5.11 的"可简化"）：
//   - 同时最多执行 Concurrency 个（进程内信号量）；Concurrency 为 0 时不启动；
//   - 一个库同时只有一个进程执行后台任务：执行器在一条专用连接上拿会话级咨询锁 (6006, 0)，拿不到就不启动（不放回
//     队列、不执行，打一条 warn）；那条连接断了先重新拿，拿不到就停下执行器；
//   - 心跳：每 Heartbeat 刷新 heartbeat_at，顺便问 Handler 业务对象是不是已经取消，取消了就停下这次执行；
//     Handler 报的进度最多每秒写一次库（progress_done，同样刷新心跳、查取消），写完让 Handler 发事件。
//     同一个进程里的取消接口另外调 [Executor.Interrupt]，不用等心跳；
//   - 回收：库里 running 而这个进程里没有在执行（也没有正在领取）的 job 按"执行中断"处理——业务对象已取消的
//     改成 canceled，次数用完的改成 failed，其余放回队列（算一次尝试，马上可以再领）。启动时做一次（上次进程被杀时
//     正在执行的），之后每个心跳间隔做一次（改状态写库失败、领取后没交给执行的）；
//   - 失败：可以重试的（[Retryable]）而且没到 max_attempts 的排队，run_after = 现在 + RetryBackoff × 4^(n−1)（最多
//     1 小时）；否则 failed；
//   - 优雅退出（[Executor.Stop]）：不再领取；正在执行的停下，job 放回队列（attempts − 1，这次不算一次尝试，马上
//     可以再领）；之后才释放咨询锁；
//   - 不做多实例用的租约、心跳超时回收。一次执行由 (job id, attempts) 标识，改 job 行时都核对它。
//
// job 行永久保留，重试复用同一行，payload 不改写。
package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/db"
)

// Status 是 background_jobs.status。
type Status string

const (
	Queued   Status = "queued"
	Running  Status = "running"
	Done     Status = "done"
	Failed   Status = "failed"
	Canceled Status = "canceled"
)

// 默认值。
const (
	DefaultConcurrency  = 2
	DefaultHeartbeat    = 10 * time.Second
	DefaultRetryBackoff = 30 * time.Second
	// MaxRetryDelay 是重试前最多等多久。
	MaxRetryDelay = time.Hour
	// progressEvery：进度最多这么久写一次库、发一次事件。
	progressEvery = time.Second
	// defaultPoll：没有可领的任务时多久再看一次（建任务时另有 Wake，马上领）。
	defaultPoll = 2 * time.Second
	// dbTimeout：执行器自己写库（改状态、心跳）最多等多久。
	dbTimeout = 30 * time.Second
)

// Options 是执行器的配置（JOB_CONCURRENCY、JOB_HEARTBEAT_SECS、JOB_RETRY_BACKOFF_SECS）。零值用默认值，
// 只有 Concurrency 的 0 就是 0（不执行）。
type Options struct {
	// Concurrency 是同时最多执行几个；0 表示这个进程不执行后台任务。
	Concurrency int
	// Heartbeat 是心跳间隔（默认 10 秒）。
	Heartbeat time.Duration
	// RetryBackoff 是第一次重试前等多久（默认 30 秒），之后每次 × 4，最多 1 小时。
	RetryBackoff time.Duration
	// PollInterval 是没有可领的任务时多久再看一次（默认 2 秒）。
	PollInterval time.Duration
}

func (o Options) withDefaults() Options {
	if o.Heartbeat <= 0 {
		o.Heartbeat = DefaultHeartbeat
	}
	if o.RetryBackoff <= 0 {
		o.RetryBackoff = DefaultRetryBackoff
	}
	if o.PollInterval <= 0 {
		o.PollInterval = defaultPoll
	}
	return o
}

// RetryDelay 是第 attempt 次执行失败后、重试前等多久：backoff × 4^(attempt−1)，最多 1 小时。
func RetryDelay(backoff time.Duration, attempt int) time.Duration {
	d := backoff
	for i := 1; i < attempt && d < MaxRetryDelay; i++ {
		d *= 4
	}
	return min(d, MaxRetryDelay)
}

// Job 是领到的一次执行。
type Job struct {
	ID   uuid.UUID
	Type string
	// ProjectID、CreatedBy 为空时是 uuid.Nil。
	ProjectID     uuid.UUID
	CreatedBy     uuid.UUID
	Payload       []byte
	Attempt       int // 这次是第几次执行（领取后的 attempts）
	MaxAttempts   int
	ProgressTotal int
}

// Handler 是一种后台任务的执行逻辑。
type Handler interface {
	// Claimed 在领取的事务里、job 行改成 running 之后调用：把业务对象改成执行中。返回 [ErrCanceled] 表示业务对象
	// 已经取消（job 改成 canceled，不执行），返回 [Fail] 的错误表示没法执行（job 改成 failed）；其他错误回滚，
	// 下一轮再领。
	Claimed(ctx context.Context, tx pgx.Tx, job *Job) error
	// Run 执行一次。ctx 在退出（Stop）或业务对象被取消（心跳、Interrupt）时取消。成功时 Run 自己在业务的事务里用
	// [MarkDone] 把 job 改成 done，返回 nil；返回 [ErrCanceled] 表示发现业务对象已经取消（清理完了）；其他错误按
	// [Retryable] 决定重试还是失败，错误信息（中文）写进 error_message。
	Run(ctx context.Context, job *Job, run *Run) error
	// CancelRequested：业务对象是不是已经取消了（心跳、改状态时问）。
	CancelRequested(ctx context.Context, q db.Querier, job *Job) (bool, error)
	// Settled 在执行器把 job 改成 queued（重试、退出放回）、failed、canceled 的同一个事务里调用，改业务对象的状态。
	Settled(ctx context.Context, tx pgx.Tx, job *Job, to Status) error
	// Notify 在 job 的状态、进度变了（事务提交后）调用：发事件。
	Notify(job *Job)
}

// ErrCanceled：业务对象已经取消。
var ErrCanceled = errors.New("任务已取消")

// ErrLost：job 已经不是这次执行的了（不是 running，或者 attempts 对不上）。单服务器下正常不会出现。
var ErrLost = errors.New("后台任务已经不归这次执行了")

// Failure 是没法执行的原因（[Fail]）。
type Failure struct{ Message string }

func (f *Failure) Error() string { return f.Message }

// Fail 是 Handler.Claimed 用的"没法执行"（job 改成 failed，message 写进 error_message）。
func Fail(format string, args ...any) error { return &Failure{Message: fmt.Sprintf(format, args...)} }

type retryable struct{ err error }

func (r *retryable) Error() string   { return r.err.Error() }
func (r *retryable) Unwrap() error   { return r.err }
func (r *retryable) Retryable() bool { return true }

// Retry 把 err 标成可以重试的（读写 S3、写库失败之类）。
func Retry(err error) error {
	if err == nil {
		return nil
	}
	return &retryable{err: err}
}

// Retryable：err 链里有 `Retryable() bool` 返回 true 的错误（[Retry] 包过的、bundle.Error 里可以重试的）。
func Retryable(err error) bool {
	var r interface{ Retryable() bool }
	return errors.As(err, &r) && r.Retryable()
}

// Run 是一次执行的运行时：Handler 用它报进度。
type Run struct {
	progress atomic.Int64
}

// Progress 报进度（处理完的数量，和 progress_total 同一个单位）。执行器最多每秒写一次库。
func (r *Run) Progress(done int) { r.progress.Store(int64(done)) }

// MarkDone 在 Handler 完成业务的事务里调用：把这次执行的 job 改成 done（进度满、清掉错误）。job 已经不归这次执行时
// 返回 [ErrLost]（调用方回滚）。
func MarkDone(ctx context.Context, tx pgx.Tx, job *Job) error {
	tag, err := tx.Exec(ctx, `UPDATE background_jobs SET status = 'done', finished_at = now(), heartbeat_at = now(),
  progress_done = progress_total, error_message = NULL
WHERE id = $1 AND status = 'running' AND attempts = $2`, job.ID, job.Attempt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLost
	}
	return nil
}

// LockOwned 在调用方的事务里锁住 job 行（FOR NO KEY UPDATE），返回它是不是还归这次执行（running、attempts 对得上）。
// 加锁顺序：job 行 → 业务对象的行（和取消接口一样）。
func LockOwned(ctx context.Context, tx pgx.Tx, job *Job) (bool, error) {
	var status string
	var attempts int
	err := tx.QueryRow(ctx, `SELECT status, attempts FROM background_jobs WHERE id = $1 FOR NO KEY UPDATE`, job.ID).
		Scan(&status, &attempts)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == string(Running) && attempts == job.Attempt, nil
}

// FailureMessage 是写进 error_message 的失败信息：要重试时"第 n 次执行失败，x 后自动重试：……"；不再重试时第一次
// 就是原因本身，之后"已经执行了 n 次，都没有成功。最后一次：……"。
func FailureMessage(attempt int, message string, retryIn time.Duration) string {
	switch {
	case retryIn > 0:
		return fmt.Sprintf("第 %d 次执行失败，%s后自动重试：%s", attempt, HumanDuration(retryIn), message)
	case attempt > 1:
		return fmt.Sprintf("已经执行了 %d 次，都没有成功。最后一次：%s", attempt, message)
	}
	return message
}

// HumanDuration："30 秒"、"2 分钟"、"2 分 30 秒"、"1 小时 5 分钟"（不到 1 秒按 1 秒）。
func HumanDuration(d time.Duration) string {
	secs := int((d + time.Second - 1) / time.Second)
	secs = max(secs, 1)
	switch {
	case secs < 60:
		return fmt.Sprintf("%d 秒", secs)
	case secs < 3600:
		m, s := secs/60, secs%60
		if s == 0 {
			return fmt.Sprintf("%d 分钟", m)
		}
		return fmt.Sprintf("%d 分 %d 秒", m, s)
	}
	h, m := secs/3600, secs%3600/60
	if m == 0 {
		return fmt.Sprintf("%d 小时", h)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", h, m)
}

const jobColumns = `id, job_type, COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid),
  COALESCE(created_by, '00000000-0000-0000-0000-000000000000'::uuid), payload, attempts, max_attempts, progress_total`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	if err := row.Scan(&j.ID, &j.Type, &j.ProjectID, &j.CreatedBy, &j.Payload, &j.Attempt, &j.MaxAttempts, &j.ProgressTotal); err != nil {
		return nil, err
	}
	return &j, nil
}
