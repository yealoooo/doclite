// Package export 是导出（requirements 5.11）：范围和预检、建任务（冻结）、任务列表和明细、交付后变更和补交、取消、
// 后台执行（jobs.Handler）、下载、到期清理。导出物的生成在子包 bundle（M5-1）。
//
// 规则摘要：
//   - 范围：`category`（节点子树里未删除的条目，重叠算一次）或 `item`；scope_ids 1–5000；补交用 resubmit_of（server 在
//     同一个快照里按那个任务的交付后变更算条目，不受 5000 的限制）。条目按节点 path_code、件号、id 排；页 = 挂接行里
//     文件和文件夹都未删除的，页号 1..n；
//   - 预检、建任务、交付后变更都在一个 REPEATABLE READ 快照里读，不拿树锁、不锁行（建任务只插新行）；
//   - 冻结：建任务时把就绪条目的页和每页当时的最新快照写进 export_task_files，条目当时的状态写进 job 的 payload
//     （payload.go），之后的变化不影响这次导出，归入交付后变更；
//   - 执行（run.go）：后台执行器（internal/platform/jobs）领取，读冻结的页 → bundle.Generate → 分片上传；成功时任务
//     done、job done、每页一条 file_logs `export` 在一个事务里；执行器改任务状态都带 `WHERE status = 'running'`；
//   - 取消：排队中的任务和 job 一起取消；执行中的任务立即取消，job 等执行器停下、中止上传后再取消（期间
//     `cleaning_up: true`）；
//   - 加锁顺序：job 行 → 任务行（取消接口、执行器都一样）。
//
// 检查顺序：菜单 403 → 项目 404/403（`{id}` 的：任务不存在 404、进不了任务所在的项目 403）→ 参数 400 → 范围里的
// id 404（details.ids）、补交的任务 404 → 状态 409 `invalid_state`。
package export

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/storage"
)

// JobType 是导出在 background_jobs.job_type 里的值。
const JobType = "export"

// 规模和分页。
const (
	// MaxScopeIDs：手选的范围最多多少个节点或条目（按请求里的个数算）。
	MaxScopeIDs = 5000
	// MaxListed：预检的不就绪条目、交付后变更的条目最多列多少件。
	MaxListed = 500
	// MaxExamplePages：每种原因最多举几页的例子。
	MaxExamplePages = 5
	// messageExamples：说明文字里最多写几页。
	messageExamples = 3
	// MaxTaskPages：一个任务最多导出多少页（建任务是同步的）。
	MaxTaskPages = 200_000

	defaultTaskLimit = 50
	maxTaskLimit     = 200
	defaultFileLimit = 500
	maxFileLimit     = 2000
)

// 任务状态（export_tasks.status，和 jobs.Status 的取值一样）。
const (
	StatusQueued   = "queued"
	StatusRunning  = "running"
	StatusDone     = "done"
	StatusFailed   = "failed"
	StatusCanceled = "canceled"
)

var statusLabels = map[string]string{
	StatusQueued: "排队中", StatusRunning: "执行中", StatusDone: "已完成", StatusFailed: "失败", StatusCanceled: "已取消",
}

func statusLabel(s string) string {
	if l, ok := statusLabels[s]; ok {
		return l
	}
	return s
}

var errTaskNotFound = httpx.NotFound("导出任务")

// Jobs 是执行器给导出用的两个方法（internal/platform/jobs.Executor 实现）。
type Jobs interface {
	// Wake 让执行器马上看一次有没有可领的任务（建任务后）。
	Wake()
	// Interrupt 停下正在执行的 job（执行中的任务被取消后）。
	Interrupt(jobID uuid.UUID)
}

// Options 是导出的配置。
type Options struct {
	// TmpDir 是临时目录（TMP_DIR）；导出在它下面的 exports/ 里建每次执行的临时目录。
	TmpDir string
	// PartSize 是分片上传每片的大小；<= 0 时按导出物的预计大小算（s3.PartSizeFor，至少 8 MiB）。只给测试调小
	// （让小导出物也走分片上传；真实的 S3 要求除最后一片外每片至少 5 MiB）。
	PartSize int64
}

// Service 是导出的业务规则。
type Service struct {
	pool     *pgxpool.Pool
	events   *events.Bus
	storage  *storage.Service
	jobs     Jobs
	tmpDir   string
	partSize int64
	log      *slog.Logger
	now      func() time.Time

	// taskOfJob 缓存 job id → 任务 id（发事件时不用每次解析 payload）。
	taskOfJob sync.Map
}

// NewService 建 Service。jobs 可以是还没启动的执行器（JOB_CONCURRENCY=0 时任务一直排队）。
func NewService(pool *pgxpool.Pool, bus *events.Bus, storageSvc *storage.Service, jobs Jobs, opts Options, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, events: bus, storage: storageSvc, jobs: jobs, tmpDir: TmpDir(opts.TmpDir), partSize: opts.PartSize,
		log: log, now: time.Now}
}

// ProjectOfTask 是任务所在的项目；任务不存在 404（项目已删除由 auth.InProject 按 404 处理）。
func (s *Service) ProjectOfTask(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	pid, err := taskProject(ctx, s.pool, id)
	if err != nil {
		return uuid.Nil, err
	}
	if pid == uuid.Nil {
		return uuid.Nil, errTaskNotFound
	}
	return pid, nil
}

// Task 是导出任务（列表、详情、建任务、取消的响应）。
type Task struct {
	ID                uuid.UUID       `json:"id"`
	ProjectID         uuid.UUID       `json:"project_id"`
	ScopeType         string          `json:"scope_type"`
	ScopeIDs          []uuid.UUID     `json:"scope_ids"`
	ScopeCategories   []ScopeCategory `json:"scope_categories"`
	ResubmitOf        *uuid.UUID      `json:"resubmit_of"`
	ExportFormat      string          `json:"export_format"`
	OnlyApproved      bool            `json:"only_approved"`
	OnlyReady         bool            `json:"only_ready"`
	RequireLockedItem bool            `json:"require_locked_item"`
	Status            string          `json:"status"`
	// CleaningUp：任务已取消、执行器还在停下和清理（job 还在 running）。
	CleaningUp       bool       `json:"cleaning_up"`
	ItemCount        int        `json:"item_count"`
	PageCount        int        `json:"page_count"`
	SkippedItemCount int        `json:"skipped_item_count"`
	ProgressDone     int        `json:"progress_done"`
	ProgressTotal    int        `json:"progress_total"`
	Attempts         int        `json:"attempts"`
	MaxAttempts      int        `json:"max_attempts"`
	ErrorMessage     *string    `json:"error_message"`
	OutputSize       *int64     `json:"output_size"`
	ExpiresAt        *time.Time `json:"expires_at"`
	Expired          bool       `json:"expired"`
	CreatedBy        uuid.UUID  `json:"created_by"`
	CreatedByName    *string    `json:"created_by_name"`
	CreatedAt        time.Time  `json:"created_at"`
	StartedAt        *time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
}

func (r *taskRow) view() Task {
	cats := r.Header.ScopeCategories
	if cats == nil || r.ScopeType != scopeTypeCategory {
		cats = []ScopeCategory{}
	}
	ids := r.ScopeIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return Task{
		ID: r.ID, ProjectID: r.ProjectID, ScopeType: r.ScopeType, ScopeIDs: ids, ScopeCategories: cats,
		ResubmitOf: r.Header.ResubmitOf, ExportFormat: r.Format, OnlyApproved: r.OnlyApproved, OnlyReady: r.Header.OnlyReady,
		RequireLockedItem: r.Header.RequireLockedItem, Status: r.Status,
		CleaningUp: r.Status == StatusCanceled && r.JobStatus != nil && *r.JobStatus == StatusRunning,
		ItemCount:  r.Header.ItemCount, PageCount: r.Header.PageCount, SkippedItemCount: r.Header.SkippedItemCount,
		ProgressDone: r.ProgressDone, ProgressTotal: r.ProgressTotal, Attempts: r.Attempts, MaxAttempts: r.MaxAttempts,
		ErrorMessage: r.ErrorMessage, OutputSize: r.OutputSize, ExpiresAt: r.ExpiresAt, Expired: r.Expired,
		CreatedBy: r.CreatedBy, CreatedByName: r.CreatedByName, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt,
		FinishedAt: r.FinishedAt,
	}
}

// Get 是一个任务。
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Task, error) {
	r, err := getTask(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errTaskNotFound
	}
	t := r.view()
	return &t, nil
}

// TaskList 是 `GET /api/projects/{pid}/export-tasks` 的响应。
type TaskList struct {
	Items      []Task  `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// List 是项目的任务：最新的在前，keyset 分页（cursor `<微秒>_<id>`，原样传回）。
func (s *Service) List(ctx context.Context, projectID uuid.UUID, limitArg, cursor string) (*TaskList, error) {
	limit, err := parseLimit(limitArg, defaultTaskLimit, maxTaskLimit)
	if err != nil {
		return nil, err
	}
	var afterAt *time.Time
	var afterID uuid.UUID
	if cursor != "" {
		us, id, ok := splitCursor(cursor)
		if !ok {
			return nil, errBadCursor
		}
		t := time.UnixMicro(us).UTC()
		afterAt, afterID = &t, id
	}
	rows, err := listTasks(ctx, s.pool, projectID, afterAt, afterID, limit)
	if err != nil {
		return nil, err
	}
	out := &TaskList{Items: []Task{}}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			c := strconv.FormatInt(last.CreatedAt.UnixMicro(), 10) + "_" + last.ID.String()
			out.NextCursor = &c
			break
		}
		out.Items = append(out.Items, r.view())
	}
	return out, nil
}

// TaskFiles 是 `GET /api/export-tasks/{id}/files` 的响应。
type TaskFiles struct {
	ExportTaskID uuid.UUID  `json:"export_task_id"`
	ItemCount    int        `json:"item_count"`
	PageCount    int        `json:"page_count"`
	Items        []TaskFile `json:"items"`
	NextCursor   *string    `json:"next_cursor"`
}

// Files 是任务的明细：按条目在导出里的序号、页号，keyset 分页（cursor `<序号>_<页号>`）。
func (s *Service) Files(ctx context.Context, id uuid.UUID, limitArg, cursor string) (*TaskFiles, error) {
	limit, err := parseLimit(limitArg, defaultFileLimit, maxFileLimit)
	if err != nil {
		return nil, err
	}
	afterSeq, afterPage := 0, 0
	if cursor != "" {
		a, b, ok := strings.Cut(cursor, "_")
		x, err1 := strconv.Atoi(a)
		y, err2 := strconv.Atoi(b)
		if !ok || err1 != nil || err2 != nil || x < 1 || y < 0 {
			return nil, errBadCursor
		}
		afterSeq, afterPage = x, y
	}
	r, err := getTask(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errTaskNotFound
	}
	out := &TaskFiles{ExportTaskID: id, ItemCount: r.Header.ItemCount, PageCount: r.Header.PageCount, Items: []TaskFile{}}
	if r.JobID == nil {
		return out, nil
	}
	rows, err := taskFiles(ctx, s.pool, id, *r.JobID, afterSeq, afterPage, limit)
	if err != nil {
		return nil, err
	}
	if len(rows) > limit {
		last := rows[limit-1]
		c := strconv.Itoa(last.Seq) + "_" + strconv.Itoa(last.PageNo)
		out.NextCursor = &c
		rows = rows[:limit]
	}
	out.Items = append(out.Items, rows...)
	return out, nil
}

var errBadCursor = httpx.Invalid("cursor 不合法，请用上一页响应里的 next_cursor")

func parseLimit(v string, def, max int) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		return 0, httpx.Invalid("limit 要在 1 到 %d 之间", max)
	}
	return n, nil
}

func splitCursor(c string) (int64, uuid.UUID, bool) {
	a, b, ok := strings.Cut(c, "_")
	if !ok {
		return 0, uuid.Nil, false
	}
	us, err := strconv.ParseInt(a, 10, 64)
	if err != nil {
		return 0, uuid.Nil, false
	}
	id, err := uuid.Parse(b)
	if err != nil {
		return 0, uuid.Nil, false
	}
	return us, id, true
}

// conflictState 是 409 invalid_state。
func conflictState(format string, args ...any) error {
	return httpx.Conflict(httpx.CodeInvalidState, fmt.Sprintf(format, args...))
}
