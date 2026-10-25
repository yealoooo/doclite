package review

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/auth"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/pathcode"
)

// review 的写操作用到的 SQL。表 file_reviews 属于本模块。
//
// 跨模块的读（只读）：folder_files、folders（文件在哪、叫什么、审核状态和轮次、是否已删除）、file_snapshots（当前版本）、
// projects（settings、是否已删除）、users、user_roles、role_menus、roles、project_members、project_roles（审核人能不能审）。
// folder_files 的审核状态、轮次经 folder.Service 写；files 行的锁、文件日志、快照经 internal/file 的包级函数。

// fileState 是锁住 files 行之后重读的一个文件。
type fileState struct {
	FileID       uuid.UUID
	FolderFileID uuid.UUID
	ProjectID    uuid.UUID
	FolderID     uuid.UUID
	PathCode     string
	FileName     string
	SortNo       int
	Status       string
	Round        int
	// SnapshotID、VersionNo 是当前快照（max(version_no)）。
	SnapshotID uuid.UUID
	VersionNo  int
	// Open 是 open 的那一行分配；没有为 nil。
	Open *openRow
	// FolderName、FolderPath 由 Service.withPaths 填（日志用）。
	FolderName string
	FolderPath string
}

// openRow 是 open 的分配。
type openRow struct {
	ID           uuid.UUID
	ReviewerID   uuid.UUID
	ReviewerName string
	AssignedBy   uuid.UUID
}

// rereadFiles 在锁住 files 行之后重读这些文件：未删除（所在文件夹、项目也未删除）、在这个项目里的，带当前快照和 open 的
// 分配；按所在文件夹 path_code、sort_no、file_id 排。
func rereadFiles(ctx context.Context, q db.Querier, projectID uuid.UUID, ids []uuid.UUID) ([]*fileState, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT ff.file_id, ff.id, ff.project_id, ff.folder_id, f.path_code, ff.file_name, ff.sort_no,
  ff.review_status, ff.review_round, s.id, s.version_no, r.id, r.reviewer_id, u.display_name, r.assigned_by
FROM folder_files ff
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
JOIN projects p ON p.id = ff.project_id AND p.deleted_at IS NULL
CROSS JOIN LATERAL (SELECT x.id, x.version_no FROM file_snapshots x WHERE x.file_id = ff.file_id
                    ORDER BY x.version_no DESC LIMIT 1) s
LEFT JOIN file_reviews r ON r.file_id = ff.file_id AND r.status = 'open'
LEFT JOIN users u ON u.id = r.reviewer_id
WHERE ff.file_id = ANY($1) AND ff.project_id = $2 AND ff.deleted_at IS NULL
ORDER BY f.path_code, ff.sort_no, ff.file_id`, ids, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*fileState
	for rows.Next() {
		var f fileState
		var openID, reviewer, assignedBy *uuid.UUID
		var reviewerName *string
		if err := rows.Scan(&f.FileID, &f.FolderFileID, &f.ProjectID, &f.FolderID, &f.PathCode, &f.FileName, &f.SortNo,
			&f.Status, &f.Round, &f.SnapshotID, &f.VersionNo, &openID, &reviewer, &reviewerName, &assignedBy); err != nil {
			return nil, err
		}
		if openID != nil {
			f.Open = &openRow{ID: *openID, ReviewerID: *reviewer, ReviewerName: *reviewerName, AssignedBy: *assignedBy}
		}
		out = append(out, &f)
	}
	return out, rows.Err()
}

// reviewerFacts 是一个用户能不能审这个项目的三个条件（requirements 5.8"能分给的人"）。
type reviewerFacts struct {
	ID            uuid.UUID
	Username      string
	DisplayName   string
	Active        bool
	HasReviewMenu bool
	HasAccess     bool
}

// 不能再审的原因（review-assignments 的 problems）。
const (
	problemDisabled        = "disabled"
	problemNoReviewMenu    = "no_review_menu"
	problemNoProjectAccess = "no_project_access"
)

// problems 是不能审这个项目的原因；为空即能审。
func (f *reviewerFacts) problems() []string {
	out := []string{}
	if !f.Active {
		out = append(out, problemDisabled)
	}
	if !f.HasReviewMenu {
		out = append(out, problemNoReviewMenu)
	}
	if !f.HasAccess {
		out = append(out, problemNoProjectAccess)
	}
	return out
}

func (f *reviewerFacts) canReview() bool { return f.Active && f.HasReviewMenu && f.HasAccess }

// reviewerFactsColumns 是 users u 在项目 $1 上的三个条件（项目授权用 auth 的条件）。
var reviewerFactsColumns = `u.status = 'active',
  EXISTS (SELECT 1 FROM user_roles ur JOIN role_menus rm ON rm.role_id = ur.role_id
          WHERE ur.user_id = u.id AND rm.menu_code = '` + auth.MenuReview + `'),
  ` + auth.ProjectAccessSQL("u.id", "$1")

// reviewerFactsOf 是这些用户在项目上的三个条件（按 id；不存在的用户不在结果里）。
func reviewerFactsOf(ctx context.Context, q db.Querier, projectID uuid.UUID, userIDs []uuid.UUID) (map[uuid.UUID]*reviewerFacts, error) {
	out := map[uuid.UUID]*reviewerFacts{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT u.id, u.username, u.display_name, `+reviewerFactsColumns+`
FROM users u WHERE u.id = ANY($2)`, projectID, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f reviewerFacts
		if err := rows.Scan(&f.ID, &f.Username, &f.DisplayName, &f.Active, &f.HasReviewMenu, &f.HasAccess); err != nil {
			return nil, err
		}
		out[f.ID] = &f
	}
	return out, rows.Err()
}

// newAssignment 是要新增的 open 分配。
type newAssignment struct {
	ID, FileID, ReviewerID, AssignedBy uuid.UUID
}

// insertOpen 新增 open 的分配（一条 INSERT）。
func insertOpen(ctx context.Context, q db.Querier, projectID uuid.UUID, rows []newAssignment) error {
	if len(rows) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, insertOpenSQL, insertOpenArgs(projectID, rows)...)
	return err
}

const insertOpenSQL = `INSERT INTO file_reviews (id, project_id, file_id, reviewer_id, assigned_by)
SELECT t.id, $1, t.file_id, t.reviewer_id, t.assigned_by
FROM unnest($2::uuid[], $3::uuid[], $4::uuid[], $5::uuid[]) AS t(id, file_id, reviewer_id, assigned_by)`

func insertOpenArgs(projectID uuid.UUID, rows []newAssignment) []any {
	ids := make([]uuid.UUID, len(rows))
	files := make([]uuid.UUID, len(rows))
	reviewers := make([]uuid.UUID, len(rows))
	by := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i], files[i], reviewers[i], by[i] = r.ID, r.FileID, r.ReviewerID, r.AssignedBy
	}
	return []any{projectID, ids, files, reviewers, by}
}

// cancelOpenSQL 把这些 open 的分配（$1）改成 cancelled（改派：先取消旧行，再插新行，部分唯一索引）。
const cancelOpenSQL = `UPDATE file_reviews SET status = 'cancelled' WHERE id = ANY($1) AND status = 'open'`

// finished 是写了结果的一行。
type finished struct {
	ID, SnapshotID uuid.UUID
}

// queueFinishReviews 把"这些 open 的分配写成 done"排进批量：结果、所审快照、原因、说明、reviewed_at（这条语句开始的
// 时刻：锁都拿到了，批量通过的各行相同），执行后 reviewed_at 写进 at。
func queueFinishReviews(b *pgx.Batch, rows []finished, result string, reasons []string, comment *string, at *time.Time) {
	ids := make([]uuid.UUID, len(rows))
	snaps := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i], snaps[i] = r.ID, r.SnapshotID
	}
	if reasons == nil {
		reasons = []string{}
	}
	b.Queue(`WITH t AS (SELECT * FROM unnest($1::uuid[], $2::uuid[]) AS t(id, snapshot_id)),
u AS (
  UPDATE file_reviews r SET status = 'done', result = $3, snapshot_id = t.snapshot_id, reject_reasons = $4, comment = $5,
    reviewed_at = statement_timestamp()
  FROM t WHERE r.id = t.id AND r.status = 'open'
  RETURNING r.reviewed_at)
SELECT max(reviewed_at) FROM u`, ids, snaps, result, reasons, comment).QueryRow(func(row pgx.Row) error { return row.Scan(at) })
}

// resultRow 是最近一次通过或驳回的那一行（按 id 倒序）。
type resultRow struct {
	ID           uuid.UUID
	ReviewerID   uuid.UUID
	ReviewerName string
	AssignedBy   uuid.UUID
}

// latestResult 是文件最近一次结果为 result 的分配（done，按 id 倒序）；没有为 nil。
func latestResult(ctx context.Context, q db.Querier, fileID uuid.UUID, result string) (*resultRow, error) {
	var r resultRow
	err := q.QueryRow(ctx, `SELECT r.id, r.reviewer_id, u.display_name, r.assigned_by
FROM file_reviews r JOIN users u ON u.id = r.reviewer_id
WHERE r.file_id = $1 AND r.status = 'done' AND r.result = $2
ORDER BY r.id DESC LIMIT 1`, fileID, result).Scan(&r.ID, &r.ReviewerID, &r.ReviewerName, &r.AssignedBy)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// assignmentFile 是分配所在的文件和项目；文件不在了（已删除，或所在文件夹、项目已删除）时 ok 为 false。
func assignmentFile(ctx context.Context, q db.Querier, id uuid.UUID) (fileID, projectID uuid.UUID, ok bool, err error) {
	err = q.QueryRow(ctx, `SELECT r.file_id, r.project_id FROM file_reviews r
JOIN folder_files ff ON ff.file_id = r.file_id AND ff.deleted_at IS NULL
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
JOIN projects p ON p.id = r.project_id AND p.deleted_at IS NULL
WHERE r.id = $1`, id).Scan(&fileID, &projectID)
	if db.IsNoRows(err) {
		return uuid.Nil, uuid.Nil, false, nil
	}
	return fileID, projectID, err == nil, err
}

// assignmentRow 读一行分配的审核人和状态（锁住 files 行之后读）。
func assignmentRow(ctx context.Context, q db.Querier, id uuid.UUID) (reviewerID uuid.UUID, status string, err error) {
	err = q.QueryRow(ctx, `SELECT reviewer_id, status FROM file_reviews WHERE id = $1`, id).Scan(&reviewerID, &status)
	return reviewerID, status, err
}

// scopeFolder 是范围文件夹（未删除、在这个项目里）。
type scopeFolder struct{ file.LiveFolder }

// subtreeRange 是这棵子树的编码区间 [lo, hi)（见 pathcode.Upper）。
func (f *scopeFolder) subtreeRange() (lo, hi string) {
	return f.PathCode, pathcode.Upper(f.PathCode)
}

// inSubtree：path 在这棵子树里（含自己）。
func (f *scopeFolder) inSubtree(path string) bool { return pathcode.InSubtree(path, f.PathCode) }

// getScopeFolder 读范围文件夹：folderID 为 nil 时是项目的根文件夹；不在这个项目里或已删除为 nil。
func getScopeFolder(ctx context.Context, q db.Querier, projectID uuid.UUID, folderID *uuid.UUID) (*scopeFolder, error) {
	f, err := file.GetLiveFolder(ctx, q, projectID, folderID)
	if f == nil || err != nil {
		return nil, err
	}
	return &scopeFolder{*f}, nil
}

// subtreeUnassigned 是子树里未删除的待分配文件（不加锁，按文件夹分配的候选，最多取 limit 个），和不是待分配的文件数
// （按文件夹分配的 skipped，只是提示：在锁住 files 行之前数，一条语句里出，不在锁里再数一遍整棵子树）。
func subtreeUnassigned(ctx context.Context, q db.Querier, projectID uuid.UUID, f *scopeFolder, limit int) (ids []uuid.UUID,
	notUnassigned int, err error) {
	lo, hi := f.subtreeRange()
	rows, err := q.Query(ctx, `SELECT NULL::uuid, count(*) FROM folders f
JOIN folder_files ff ON ff.folder_id = f.id AND ff.project_id = $1 AND ff.deleted_at IS NULL
WHERE f.project_id = $1 AND f.path_code >= $2 AND f.path_code < $3 AND f.deleted_at IS NULL
  AND ff.review_status <> 'unassigned'
UNION ALL
(SELECT ff.file_id, 0 FROM folders f
JOIN folder_files ff ON ff.folder_id = f.id AND ff.project_id = $1 AND ff.deleted_at IS NULL
WHERE f.project_id = $1 AND f.path_code >= $2 AND f.path_code < $3 AND f.deleted_at IS NULL
  AND ff.review_status = 'unassigned'
LIMIT $4)`, projectID, lo, hi, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id *uuid.UUID
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, 0, err
		}
		if id == nil {
			notUnassigned = n
		} else {
			ids = append(ids, *id)
		}
	}
	return ids, notUnassigned, rows.Err()
}

// subtreeOpenOf 是某人在子树里未删除文件上的 open 分配的 file_id（不加锁，按子树改派的候选）。最多取 limit 个。
func subtreeOpenOf(ctx context.Context, q db.Querier, projectID, reviewerID uuid.UUID, f *scopeFolder, limit int) ([]uuid.UUID, error) {
	lo, hi := f.subtreeRange()
	return db.CollectIDs(ctx, q, `SELECT r.file_id FROM file_reviews r
JOIN folder_files ff ON ff.file_id = r.file_id AND ff.deleted_at IS NULL
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
WHERE r.project_id = $1 AND r.status = 'open' AND r.reviewer_id = $2
  AND f.path_code >= $3 AND f.path_code < $4
LIMIT $5`, projectID, reviewerID, lo, hi, limit)
}
