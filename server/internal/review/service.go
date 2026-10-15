// Package review 是审核分配和审核（requirements 5.8）、重扫和待重扫（5.6）。
//
// 表：file_reviews（一行是一次分配和它的结果：open / done / cancelled）。审核状态和轮次在 folder_files 上
// （review_status、review_round），属于 internal/folder，经 folder.Service 的 SetReviewStatus、SetReviewState 写。
//
// 状态流转（requirements 5.8 的表）：
//
//	unassigned --分配--> pending --改派--> pending
//	pending --通过/驳回--> approved / rejected
//	rejected --重扫--> pending（最近一次驳回它的人仍能审）或 unassigned（不能再审：停用、没有 review 菜单、失去授权）
//	approved --撤回通过--> pending（最近一次通过它的人仍能审）或 unassigned，轮次不变
//	任意 --删除--> 软删除，取消进行中的分配（internal/folder 做）
//
// 加锁（requirements 3.6）：写操作在一个事务里依次 文件树共享锁（project.LockFileTree）→（按文件夹、按子树时）
// 不加锁地挑候选 → files 行按 id 升序（file.LockFiles）→ 锁住之后重读 folder_files、当前快照、open 的分配 → 写。
// **不拿归类锁、不锁条目行**：审核不等归类。folder_files、file_reviews 的行不另外锁：同一个文件上改它们的写操作
// （分配、改派、审核、撤回、重扫、删除、挂接）都先锁 files 行，部分唯一索引 ux_file_reviews_open 兜底。
// 重扫另外按 file_id 用进程内的互斥串行（定版本号 → 传 S3 → 写库），见 rescan.go。
//
// "最近一次通过 / 驳回"按 file_reviews 的 id（UUIDv7）倒序取；新行的 id 在锁住 files 行之后生成，同一个文件上递增。
// 一个文件同时只有一行 open。
//
// 事件：提交后、真有变化时发 files.changed（folder_ids 是这些文件所在的文件夹，> 500 个文件只带 folder_ids）。
package review

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/auth"
	"doclite/server/internal/file"
	"doclite/server/internal/folder"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/project"
	"doclite/server/internal/storage"
)

// 上限（review-settings 里给前端）。
const (
	// MaxBatchFiles：按选中的文件分配、批量改派、批量通过一次最多的文件数。
	MaxBatchFiles = 500
	// MaxFolderFiles：按文件夹分配、按子树改派一次最多的文件数。
	MaxFolderFiles = 10000
	// MaxCommentChars：说明最多的字符数。
	MaxCommentChars = 500
	// maxReasons：驳回原因最多的个数（另外不能超过项目原因列表的长度）。
	maxReasons = 64
	// LowDPI："DPI 不足"的界限（参照 DA/T 31），和上传的 low_dpi 提示一致。
	LowDPI = file.LowDPI
	// RescanReassign 是 review.rescan_reassign 唯一的取值。
	RescanReassign = "same_reviewer"
)

// file_reviews.status、result。
const (
	statusOpen      = "open"
	statusDone      = "done"
	statusCancelled = "cancelled"
	resultApproved  = "approved"
	resultRejected  = "rejected"
)

// 错误码。
const (
	// CodeNotAssignee：审的、批量通过的、撤回的不是分给自己的（403，不用 forbidden，免得前端刷新会话）。
	CodeNotAssignee = "not_assignee"
)

var (
	errFileNotFound       = httpx.NotFound("文件")
	errFolderNotFound     = httpx.NotFound("文件夹")
	errAssignmentNotFound = httpx.NotFound("分配记录")
)

func notAssignee(msg string) *httpx.Error {
	return httpx.NewError(403, CodeNotAssignee, msg)
}

func invalidState(msg string) *httpx.Error {
	return httpx.Conflict(httpx.CodeInvalidState, msg)
}

// Service 是审核的业务规则。
type Service struct {
	pool    *pgxpool.Pool
	events  *events.Bus
	auth    *auth.Service
	storage *storage.Service
	folders *folder.Service
	files   *file.Service
	// rescans：同一个文件的重扫串行（进程内，硬性规则 16）。
	rescans keyedMutex
}

// NewService 建 Service。
func NewService(pool *pgxpool.Pool, bus *events.Bus, authSvc *auth.Service, storageSvc *storage.Service,
	folderSvc *folder.Service, fileSvc *file.Service) *Service {
	return &Service{pool: pool, events: bus, auth: authSvc, storage: storageSvc, folders: folderSvc, files: fileSvc}
}

// readOnly 在一个只读的可重复读事务里执行 fn：一个读接口的计数、列表、路径看到同一个快照。
func (s *Service) readOnly(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	return fn(tx)
}

// lockFiles 在写事务里按 requirements 3.6 锁住这些文件：文件树共享锁 → 只锁这个项目里的 files 行（id 升序）→ 锁住之后
// 重读。返回未删除（所在文件夹、项目也未删除）、在这个项目里的文件，按所在文件夹 path_code、sort_no、file_id 排。
func (s *Service) lockFiles(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, fileIDs []uuid.UUID) (*lockedFiles, error) {
	if err := project.LockFileTree(ctx, tx, projectID, false); err != nil {
		return nil, err
	}
	return s.lockFilesHeld(ctx, tx, projectID, fileIDs)
}

// lockFilesHeld 同 lockFiles，调用方已经拿了文件树共享锁（按文件夹、按子树时先挑候选）。
func (s *Service) lockFilesHeld(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, fileIDs []uuid.UUID) (*lockedFiles, error) {
	in, err := filesInProject(ctx, tx, projectID, fileIDs)
	if err != nil {
		return nil, err
	}
	if err := file.LockFiles(ctx, tx, in); err != nil {
		return nil, err
	}
	list, err := rereadFiles(ctx, tx, projectID, in)
	if err != nil {
		return nil, err
	}
	out := &lockedFiles{list: list, byID: make(map[uuid.UUID]*fileState, len(list))}
	for _, f := range list {
		out.byID[f.FileID] = f
	}
	return out, nil
}

// lockedFiles 是锁住之后重读的文件。
type lockedFiles struct {
	list []*fileState
	byID map[uuid.UUID]*fileState
}

// missing 是 ids 里不在的（不存在、已删除、不在这个项目里），按 ids 的顺序。
func (l *lockedFiles) missing(ids []uuid.UUID) []uuid.UUID {
	var out []uuid.UUID
	for _, id := range ids {
		if l.byID[id] == nil {
			out = append(out, id)
		}
	}
	return out
}

// withPaths 给这些文件填上所在文件夹的路径（日志 detail 的 folder_path）。
func (s *Service) withPaths(ctx context.Context, q db.Querier, files []*fileState) error {
	ids := make([]uuid.UUID, 0, len(files))
	for _, f := range files {
		ids = append(ids, f.FolderID)
	}
	paths, err := s.folders.Paths(ctx, q, dedup(ids))
	if err != nil {
		return err
	}
	for _, f := range files {
		f.FolderPath = paths[f.FolderID]
	}
	return nil
}

// filesChanged 是这些文件的 files.changed（所在文件夹 + file_ids；> 500 个时 events 只带 folder_ids）。
func filesChanged(projectID uuid.UUID, files []*fileState) events.Event {
	folders := make([]uuid.UUID, 0, len(files))
	ids := make([]uuid.UUID, 0, len(files))
	for _, f := range files {
		folders = append(folders, f.FolderID)
		ids = append(ids, f.FileID)
	}
	return events.FilesChanged(projectID, folders, ids)
}

// notFoundIDs 是批量接口的 404（details.ids 是 file_id）。
func notFoundIDs(ids []uuid.UUID) error {
	return httpx.NewError(404, httpx.CodeNotFound, "有 "+itoa(len(ids))+" 个文件不存在、已删除或不在这个项目里（可能刚被别人删除），请刷新后重试").
		WithDetails(map[string]any{"ids": ids})
}

// eligibility 检查审核人能不能分给（分配、改派的目标）：不存在、停用、没有 review 菜单、进不了项目都是 400。
func (s *Service) eligibility(ctx context.Context, projectID, reviewerID uuid.UUID) (*reviewerFacts, error) {
	facts, err := reviewerFactsOf(ctx, s.pool, projectID, []uuid.UUID{reviewerID})
	if err != nil {
		return nil, err
	}
	f := facts[reviewerID]
	if f == nil {
		return nil, httpx.Invalid("审核人不存在")
	}
	switch {
	case !f.Active:
		return nil, httpx.Invalid("审核人「%s」已停用，不能分给他", f.DisplayName)
	case !f.HasReviewMenu:
		return nil, httpx.Invalid("审核人「%s」没有审核菜单（review），不能分给他", f.DisplayName)
	case !f.HasAccess:
		return nil, httpx.Invalid("审核人「%s」不能进这个项目，不能分给他", f.DisplayName)
	}
	return f, nil
}

// normalizeComment：去掉首尾空白，空串为 nil；最多 500 个字符；不能有控制字符（换行、回车、制表符可以）。
func normalizeComment(v *string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	c := strings.TrimSpace(*v)
	if c == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(c) > MaxCommentChars {
		return nil, httpx.Invalid("说明最多 %d 个字", MaxCommentChars)
	}
	for _, r := range c {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return nil, httpx.Invalid("说明不能包含控制字符")
		}
	}
	return &c, nil
}

func dedup(ids []uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// newIDs 生成 n 个主键（UUIDv7，在锁住 files 行之后调用，同一个文件上的分配按先后递增）。
func newIDs(n int) []uuid.UUID {
	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = db.NewID()
	}
	return out
}
