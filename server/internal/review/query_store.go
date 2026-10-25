package review

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/db"
)

// 读接口的 SQL（只读，不加锁；一个接口的几条查询在同一个只读快照里）。只含未删除的文件、未删除的文件夹。
// 跨模块的读同 store.go。
//
// 大查询用未命名的预备语句（pgx.QueryExecModeDescribeExec）：每次按实际参数做计划。整个项目和一个盒子的行数差很多，
// 命名语句执行 5 次以后 PG 可能换成通用计划。
//
// 索引：我的审核走 0002 的 ix_file_reviews_reviewer_project (reviewer_id, project_id) WHERE status <> 'cancelled'，
// 当前版本按文件逐个取（file_snapshots 的 (file_id, version_no) 唯一索引）；状态计数、待重扫带 ff.project_id，能用
// ix_folder_files_review (project_id, review_status)；文件夹子树用 folders 的 (project_id, path_code) 唯一索引；
// 一个项目 open 的分配（可以分配的人、改派列表）走 0005 的 ix_file_reviews_project_open (project_id, reviewer_id)
// WHERE status = 'open'。

const unnamed = pgx.QueryExecModeDescribeExec

// keyset 是列表的排序键：所在文件夹 path_code、sort_no、file_id（上一页最后一行；第一页为 nil）。
type keyset struct {
	PathCode string
	SortNo   int
	FileID   uuid.UUID
}

// args 是 ($c_path, $c_sort, $c_file)：第一页 c_path 为 NULL。
func (k *keyset) args() (*string, int, uuid.UUID) {
	if k == nil {
		return nil, 0, uuid.Nil
	}
	return &k.PathCode, k.SortNo, k.FileID
}

// myRow 是"我的审核"一条 SQL 的一行：kind 0 是按文件夹的计数，kind 1 是这一页的一行。
type myRow struct {
	Kind     int
	FolderID uuid.UUID
	PathCode string
	Counts   MyReviewCounts
	Item     MyReviewItem
}

// myReviews 一条 SQL 出按文件夹的计数（不受 state、low_dpi 影响）和这一页（limit 行，调用方多要一行判断有没有下一页）。
// 每个文件取最近一次分给我、没有取消的那一行（DISTINCT ON，按 id 不按时间：只扫一遍我在这个项目里的行再排序，
// 不用每行再按 file_id 回查一次）；is_current：这个文件上没有 id 更大的分配。
func myReviews(ctx context.Context, q db.Querier, userID, projectID uuid.UUID, lo, hi, state string, lowDPI bool,
	after *keyset, limit int) ([]myRow, error) {
	cPath, cSort, cFile := after.args()
	rows, err := q.Query(ctx, `WITH mine AS (
  SELECT DISTINCT ON (r.file_id) r.id, r.file_id, r.status, r.result
  FROM file_reviews r
  WHERE r.reviewer_id = $1 AND r.project_id = $2 AND r.status <> 'cancelled'
  ORDER BY r.file_id, r.id DESC),
base AS MATERIALIZED (
  SELECT m.id, m.file_id, m.status, m.result, ff.folder_id, ff.sort_no, f.path_code, s.version_no, s.dpi
  FROM mine m
  JOIN folder_files ff ON ff.file_id = m.file_id AND ff.deleted_at IS NULL
  JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL AND f.path_code >= $3 AND f.path_code < $4
  CROSS JOIN LATERAL (SELECT x.version_no, x.dpi FROM file_snapshots x WHERE x.file_id = m.file_id
                      ORDER BY x.version_no DESC LIMIT 1) s),
page AS (
  SELECT b.* FROM base b
  WHERE ($5::text = 'all' OR ($5::text = 'open' AND b.status = 'open') OR b.result = $5::text)
    AND (NOT $6::bool OR b.dpi < $7::int)
    AND ($8::text IS NULL OR (b.path_code, b.sort_no, b.file_id) > ($8::text, $9::int, $10::uuid))
  ORDER BY b.path_code, b.sort_no, b.file_id
  LIMIT $11)
SELECT 0, b.folder_id, min(b.path_code), count(*),
  count(*) FILTER (WHERE b.status = 'open'), count(*) FILTER (WHERE b.result = 'approved'),
  count(*) FILTER (WHERE b.result = 'rejected'), count(*) FILTER (WHERE b.dpi < $7::int),
  NULL::uuid, NULL::uuid, NULL::uuid, NULL::text, NULL::int, NULL::int, NULL::text, NULL::int, NULL::int,
  NULL::timestamptz, NULL::text, NULL::text, NULL::timestamptz, NULL::bool
FROM base b GROUP BY b.folder_id
UNION ALL
SELECT 1, p.folder_id, p.path_code, 0, 0, 0, 0, 0,
  p.id, p.file_id, ff.id, ff.file_name, p.sort_no, ff.review_round, ff.review_status, p.version_no, p.dpi,
  r.assigned_at, r.status, r.result, r.reviewed_at,
  NOT EXISTS (SELECT 1 FROM file_reviews x WHERE x.file_id = p.file_id AND x.id > p.id)
FROM page p JOIN folder_files ff ON ff.file_id = p.file_id JOIN file_reviews r ON r.id = p.id
ORDER BY 1, 3, 13, 10`, unnamed, userID, projectID, lo, hi, state, lowDPI, LowDPI, cPath, cSort, cFile, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []myRow
	for rows.Next() {
		var r myRow
		var assignmentID, fileID, ffID *uuid.UUID
		var fileName, reviewStatus, status, result *string
		var sortNo, round, version *int
		var assignedAt, reviewedAt *time.Time
		var isCurrent *bool
		var dpi *int
		if err := rows.Scan(&r.Kind, &r.FolderID, &r.PathCode, &r.Counts.All, &r.Counts.Open, &r.Counts.Approved,
			&r.Counts.Rejected, &r.Counts.LowDPI, &assignmentID, &fileID, &ffID, &fileName, &sortNo, &round, &reviewStatus,
			&version, &dpi, &assignedAt, &status, &result, &reviewedAt, &isCurrent); err != nil {
			return nil, err
		}
		if r.Kind == 1 {
			r.Item = MyReviewItem{AssignmentID: *assignmentID, FileID: *fileID, FolderFileID: *ffID, FolderID: r.FolderID,
				FileName: *fileName, SortNo: *sortNo, ReviewRound: *round, ReviewStatus: *reviewStatus, VersionNo: *version,
				DPI: dpi, AssignedAt: *assignedAt, Status: *status, Result: result, ReviewedAt: reviewedAt, IsCurrent: *isCurrent,
				pathCode: r.PathCode}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// accessPredicate 是"用户 $1 能进项目 p"（auth 的项目授权条件）。
var accessPredicate = auth.ProjectAccessSQL("$1", "p.id")

// otherProjectsOpen 是我能进的其他未删除项目（含已归档的）里 open 的分配个数（未删除的文件），按项目编码排，只有 > 0 的。
func otherProjectsOpen(ctx context.Context, q db.Querier, userID, projectID uuid.UUID) ([]OtherProjectOpen, error) {
	rows, err := q.Query(ctx, `SELECT r.project_id, p.status, count(*)
FROM file_reviews r
JOIN projects p ON p.id = r.project_id AND p.deleted_at IS NULL
CROSS JOIN LATERAL (SELECT 1 FROM folder_files ff JOIN folders f ON f.id = ff.folder_id
                    WHERE ff.file_id = r.file_id AND ff.deleted_at IS NULL AND f.deleted_at IS NULL LIMIT 1) live
WHERE r.reviewer_id = $1 AND r.status = 'open' AND r.project_id <> $2 AND `+accessPredicate+`
GROUP BY r.project_id, p.project_code, p.status ORDER BY p.project_code`, userID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OtherProjectOpen{}
	for rows.Next() {
		var o OtherProjectOpen
		if err := rows.Scan(&o.ProjectID, &o.Status, &o.Open); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// projectReviewers 是能分给的人（启用、有 review 菜单、能进这个项目）和每人在这个项目里未删除文件上的 open 个数，
// 按显示名、登录名排。
func projectReviewers(ctx context.Context, q db.Querier, projectID uuid.UUID) ([]ProjectReviewer, error) {
	rows, err := q.Query(ctx, `WITH open AS (
  SELECT r.reviewer_id, count(*) AS n FROM file_reviews r
  JOIN folder_files ff ON ff.file_id = r.file_id AND ff.deleted_at IS NULL
  JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
  WHERE r.project_id = $1 AND r.status = 'open' GROUP BY r.reviewer_id)
SELECT u.id, u.username, u.display_name, COALESCE(o.n, 0)
FROM users u LEFT JOIN open o ON o.reviewer_id = u.id
WHERE u.status = 'active'
  AND EXISTS (SELECT 1 FROM user_roles ur JOIN role_menus rm ON rm.role_id = ur.role_id
              WHERE ur.user_id = u.id AND rm.menu_code = '`+auth.MenuReview+`')
  AND `+auth.ProjectAccessSQL("u.id", "$1")+`
ORDER BY u.display_name, lower(u.username), u.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectReviewer{}
	for rows.Next() {
		var r ProjectReviewer
		if err := rows.Scan(&r.ID, &r.Username, &r.DisplayName, &r.Open); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// statusBuckets 是子树里每个文件按"落在哪个直接子文件夹的子树里"分组的状态计数：键是所在文件夹编码的前
// len(path)+6 个字符（等于 path 的是直接放在这个文件夹里的）。
func statusBuckets(ctx context.Context, q db.Querier, projectID uuid.UUID, path, lo, hi string) (map[string]*StatusCounts, error) {
	rows, err := q.Query(ctx, `SELECT left(f.path_code, length($2::text) + 6), ff.review_status, count(*)
FROM folders f JOIN folder_files ff ON ff.folder_id = f.id AND ff.project_id = $1 AND ff.deleted_at IS NULL
WHERE f.project_id = $1 AND f.path_code >= $3 AND f.path_code < $4 AND f.deleted_at IS NULL
GROUP BY 1, 2`, unnamed, projectID, path, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*StatusCounts{}
	for rows.Next() {
		var bucket, status string
		var n int
		if err := rows.Scan(&bucket, &status, &n); err != nil {
			return nil, err
		}
		c := out[bucket]
		if c == nil {
			c = &StatusCounts{}
			out[bucket] = c
		}
		c.add(status, n)
	}
	return out, rows.Err()
}

// subfolder 是未删除的直接子文件夹。
type subfolder struct {
	ID         uuid.UUID
	FolderName string
	PathCode   string
}

// subfolders 是未删除的直接子文件夹，按 sort_no、名称排（和 children 接口一样）。
func subfolders(ctx context.Context, q db.Querier, parentID uuid.UUID) ([]subfolder, error) {
	rows, err := q.Query(ctx, `SELECT id, folder_name, path_code FROM folders
WHERE parent_id = $1 AND deleted_at IS NULL ORDER BY sort_no, folder_name, id`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []subfolder
	for rows.Next() {
		var f subfolder
		if err := rows.Scan(&f.ID, &f.FolderName, &f.PathCode); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// openByReviewer 是范围里每个审核人 open 的分配个数（未删除的文件）。
func openByReviewer(ctx context.Context, q db.Querier, projectID uuid.UUID, lo, hi string) (map[uuid.UUID]int, error) {
	rows, err := q.Query(ctx, `SELECT r.reviewer_id, count(*) FROM file_reviews r
JOIN folder_files ff ON ff.file_id = r.file_id AND ff.project_id = $1 AND ff.deleted_at IS NULL
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL AND f.path_code >= $2 AND f.path_code < $3
WHERE r.project_id = $1 AND r.status = 'open'
GROUP BY r.reviewer_id`, unnamed, projectID, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]int{}
	for rows.Next() {
		var id uuid.UUID
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// openAssignments 是范围里 open 的分配（reviewer 不为 nil 时只要这个人的），按 keyset 取 limit 行。
func openAssignments(ctx context.Context, q db.Querier, projectID uuid.UUID, lo, hi string, reviewer *uuid.UUID,
	after *keyset, limit int) ([]OpenAssignment, error) {
	cPath, cSort, cFile := after.args()
	rows, err := q.Query(ctx, `SELECT r.id, r.file_id, ff.id, ff.folder_id, ff.file_name, ff.sort_no, ff.review_round, f.path_code,
  r.reviewer_id, u.display_name, r.assigned_by, r.assigned_at
FROM file_reviews r
JOIN folder_files ff ON ff.file_id = r.file_id AND ff.project_id = $1 AND ff.deleted_at IS NULL
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL AND f.path_code >= $2 AND f.path_code < $3
JOIN users u ON u.id = r.reviewer_id
WHERE r.project_id = $1 AND r.status = 'open'
  AND ($4::uuid IS NULL OR r.reviewer_id = $4::uuid)
  AND ($5::text IS NULL OR (f.path_code, ff.sort_no, ff.file_id) > ($5::text, $6::int, $7::uuid))
ORDER BY f.path_code, ff.sort_no, ff.file_id LIMIT $8`, unnamed, projectID, lo, hi, reviewer, cPath, cSort, cFile, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpenAssignment{}
	for rows.Next() {
		var a OpenAssignment
		if err := rows.Scan(&a.AssignmentID, &a.FileID, &a.FolderFileID, &a.FolderID, &a.FileName, &a.SortNo, &a.ReviewRound,
			&a.pathCode, &a.ReviewerID, &a.ReviewerName, &a.AssignedBy, &a.AssignedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// rejectedCount 是范围里已驳回的文件数。
func rejectedCount(ctx context.Context, q db.Querier, projectID uuid.UUID, lo, hi string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM folder_files ff
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL AND f.path_code >= $2 AND f.path_code < $3
WHERE ff.project_id = $1 AND ff.review_status = 'rejected' AND ff.deleted_at IS NULL`, unnamed, projectID, lo, hi).Scan(&n)
	return n, err
}

// rescanItems 是范围里已驳回的文件，带文件夹备注、当前版本和最近一次驳回（按 id）；按 keyset 取 limit 行。
func rescanItems(ctx context.Context, q db.Querier, projectID uuid.UUID, lo, hi string, after *keyset, limit int) ([]RescanItem, error) {
	cPath, cSort, cFile := after.args()
	rows, err := q.Query(ctx, `SELECT ff.file_id, ff.id, ff.folder_id, f.folder_name, f.remark, f.path_code, ff.file_name, ff.sort_no,
  ff.review_round, cur.version_no,
  rj.id, rj.reviewer_id, u.display_name, rj.reject_reasons, rj.comment, rj.snapshot_id, rs.version_no, rj.reviewed_at
FROM folder_files ff
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL AND f.path_code >= $2 AND f.path_code < $3
CROSS JOIN LATERAL (SELECT x.version_no FROM file_snapshots x WHERE x.file_id = ff.file_id
                    ORDER BY x.version_no DESC LIMIT 1) cur
LEFT JOIN LATERAL (SELECT r.* FROM file_reviews r WHERE r.file_id = ff.file_id AND r.status = 'done'
                   AND r.result = 'rejected' ORDER BY r.id DESC LIMIT 1) rj ON true
LEFT JOIN users u ON u.id = rj.reviewer_id
LEFT JOIN file_snapshots rs ON rs.id = rj.snapshot_id
WHERE ff.project_id = $1 AND ff.review_status = 'rejected' AND ff.deleted_at IS NULL
  AND ($4::text IS NULL OR (f.path_code, ff.sort_no, ff.file_id) > ($4::text, $5::int, $6::uuid))
ORDER BY f.path_code, ff.sort_no, ff.file_id LIMIT $7`, unnamed, projectID, lo, hi, cPath, cSort, cFile, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RescanItem{}
	for rows.Next() {
		var it RescanItem
		var rjID, rjReviewer, rjSnap *uuid.UUID
		var rjName, rjComment *string
		var rjReasons []string
		var rjVersion *int
		var rjAt *time.Time
		if err := rows.Scan(&it.FileID, &it.FolderFileID, &it.FolderID, &it.FolderName, &it.FolderRemark, &it.pathCode,
			&it.FileName, &it.SortNo, &it.ReviewRound, &it.VersionNo, &rjID, &rjReviewer, &rjName, &rjReasons, &rjComment,
			&rjSnap, &rjVersion, &rjAt); err != nil {
			return nil, err
		}
		if rjID != nil {
			if rjReasons == nil {
				rjReasons = []string{}
			}
			it.LastReject = &LastReject{AssignmentID: *rjID, ReviewerID: *rjReviewer, ReviewerName: *rjName, Reasons: rjReasons,
				Comment: rjComment, SnapshotID: *rjSnap, VersionNo: *rjVersion, ReviewedAt: *rjAt}
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// fileReviewRows 是一个文件的全部分配（含已取消的），按 id 升序，带所审快照的版本号和现在的显示名。
func fileReviewRows(ctx context.Context, q db.Querier, fileID uuid.UUID) ([]FileReviewRow, error) {
	rows, err := q.Query(ctx, `SELECT r.id, r.reviewer_id, u.display_name, r.assigned_by, a.display_name, r.assigned_at, r.status,
  r.result, r.snapshot_id, s.version_no, r.reject_reasons, r.comment, r.reviewed_at
FROM file_reviews r JOIN users u ON u.id = r.reviewer_id JOIN users a ON a.id = r.assigned_by
LEFT JOIN file_snapshots s ON s.id = r.snapshot_id
WHERE r.file_id = $1 ORDER BY r.id`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FileReviewRow{}
	for rows.Next() {
		var r FileReviewRow
		if err := rows.Scan(&r.AssignmentID, &r.ReviewerID, &r.ReviewerName, &r.AssignedBy, &r.AssignedByName, &r.AssignedAt,
			&r.Status, &r.Result, &r.SnapshotID, &r.VersionNo, &r.RejectReasons, &r.Comment, &r.ReviewedAt); err != nil {
			return nil, err
		}
		if r.RejectReasons == nil {
			r.RejectReasons = []string{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
