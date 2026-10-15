package catalog

import (
	"context"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/platform/db"
)

// 条目的 SQL。条目的页（archive_item_files）在 M4-2 才有接口，这里只读。

// itemRow 是 archive_items 的一行（加上所在节点的 path_code）。
type itemRow struct {
	ID           uuid.UUID
	ProjectID    uuid.UUID
	CategoryID   uuid.UUID
	ItemNo       int
	ArchiveNo    *string
	Title        string
	ExtraFields  map[string]any
	Status       string
	RowVersion   int
	CreatedBy    uuid.UUID
	CreatedAt    time.Time
	UpdatedBy    *uuid.UUID
	UpdatedAt    time.Time
	Deleted      bool
	CategoryPath string
}

const itemColumns = `i.id, i.project_id, i.category_id, i.item_no, i.archive_no, i.title, i.extra_fields, i.status,
  i.row_version, i.created_by, i.created_at, i.updated_by, i.updated_at, i.deleted_at IS NOT NULL, c.path_code`

func scanItemRow(sc interface{ Scan(...any) error }) (*itemRow, error) {
	var r itemRow
	if err := sc.Scan(&r.ID, &r.ProjectID, &r.CategoryID, &r.ItemNo, &r.ArchiveNo, &r.Title, &r.ExtraFields, &r.Status,
		&r.RowVersion, &r.CreatedBy, &r.CreatedAt, &r.UpdatedBy, &r.UpdatedAt, &r.Deleted, &r.CategoryPath); err != nil {
		return nil, err
	}
	if r.ExtraFields == nil {
		r.ExtraFields = map[string]any{}
	}
	return &r, nil
}

// getItem 读未删除的条目；没有为 nil。
func getItem(ctx context.Context, q db.Querier, id uuid.UUID) (*itemRow, error) {
	r, err := scanItemRow(q.QueryRow(ctx, `SELECT `+itemColumns+`
FROM archive_items i JOIN archive_categories c ON c.id = i.category_id WHERE i.id = $1 AND i.deleted_at IS NULL`, id))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return r, err
}

// lockItem 锁住条目行（FOR NO KEY UPDATE），含已删除的；没有为 nil。
func lockItem(ctx context.Context, q db.Querier, id uuid.UUID) (*itemRow, error) {
	r, err := scanItemRow(q.QueryRow(ctx, `SELECT `+itemColumns+`
FROM archive_items i JOIN archive_categories c ON c.id = i.category_id WHERE i.id = $1 FOR NO KEY UPDATE OF i`, id))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return r, err
}

// lockItems 按 id 升序锁住项目里这些未删除的条目。
func lockItems(ctx context.Context, q db.Querier, projectID uuid.UUID, ids []uuid.UUID) ([]*itemRow, error) {
	rows, err := q.Query(ctx, `SELECT `+itemColumns+`
FROM archive_items i JOIN archive_categories c ON c.id = i.category_id
WHERE i.id = ANY($1) AND i.project_id = $2 AND i.deleted_at IS NULL ORDER BY i.id FOR NO KEY UPDATE OF i`, ids, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*itemRow
	for rows.Next() {
		r, err := scanItemRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// itemProject 是未删除条目所在的项目；没有为 uuid.Nil。
func itemProject(ctx context.Context, q db.Querier, id uuid.UUID) (uuid.UUID, error) {
	var pid uuid.UUID
	err := q.QueryRow(ctx, `SELECT project_id FROM archive_items WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&pid)
	if db.IsNoRows(err) {
		return uuid.Nil, nil
	}
	return pid, err
}

// nextItemNo 是节点里的 max(件号) + 1，算上已删除的条目（不回收）。拆成未删除、已删除两段，各走一个部分索引
// （0001 的 ix_archive_items_category、0003 的 ix_archive_items_category_deleted）。
func nextItemNo(ctx context.Context, q db.Querier, categoryID uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT GREATEST(
  (SELECT max(item_no) FROM archive_items WHERE category_id = $1 AND deleted_at IS NULL),
  (SELECT max(item_no) FROM archive_items WHERE category_id = $1 AND deleted_at IS NOT NULL),
  0) + 1`, categoryID).Scan(&n)
	return n, err
}

// itemNoUser 是占着某个件号的条目。
type itemNoUser struct {
	ID      uuid.UUID
	Title   string
	Deleted bool
}

// itemNoTaken 查节点里件号 itemNo 被谁占着（含已删除的；两段都有时给未删除的那个），except 是自己；没有为 nil。
func itemNoTaken(ctx context.Context, q db.Querier, categoryID uuid.UUID, itemNo int, except uuid.UUID) (*itemNoUser, error) {
	var u itemNoUser
	err := q.QueryRow(ctx, `SELECT id, title, deleted FROM (
  (SELECT id, title, false AS deleted FROM archive_items
   WHERE category_id = $1 AND item_no = $2 AND deleted_at IS NULL AND id <> $3 LIMIT 1)
  UNION ALL
  (SELECT id, title, true FROM archive_items
   WHERE category_id = $1 AND item_no = $2 AND deleted_at IS NOT NULL LIMIT 1)
) t ORDER BY deleted LIMIT 1`, categoryID, itemNo, except).Scan(&u.ID, &u.Title, &u.Deleted)
	if db.IsNoRows(err) {
		return nil, nil
	}
	return &u, err
}

func insertItem(ctx context.Context, q db.Querier, r *itemRow) error {
	_, err := q.Exec(ctx, `INSERT INTO archive_items
  (id, project_id, category_id, item_level, item_no, archive_no, title, extra_fields, status, row_version, created_by)
VALUES ($1, $2, $3, 'item', $4, $5, $6, $7, 'draft', 1, $8)`,
		r.ID, r.ProjectID, r.CategoryID, r.ItemNo, r.ArchiveNo, r.Title, r.ExtraFields, r.CreatedBy)
	return err
}

// updateItemRow 写条目的可变列，row_version + 1。
func updateItemRow(ctx context.Context, q db.Querier, r *itemRow, operatorID uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE archive_items SET category_id = $2, item_no = $3, archive_no = $4, title = $5,
  extra_fields = $6, status = $7, row_version = row_version + 1, updated_by = $8, updated_at = now()
WHERE id = $1`, r.ID, r.CategoryID, r.ItemNo, r.ArchiveNo, r.Title, r.ExtraFields, r.Status, operatorID)
	return err
}

// softDeleteItem 软删除条目，row_version + 1。
func softDeleteItem(ctx context.Context, q db.Querier, id, operatorID uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE archive_items SET deleted_at = now(), row_version = row_version + 1, updated_by = $2,
  updated_at = now() WHERE id = $1`, id, operatorID)
	return err
}

// setItemsStatus 改一批条目的状态，row_version + 1。
func setItemsStatus(ctx context.Context, q db.Querier, ids []uuid.UUID, status string, operatorID uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE archive_items SET status = $2, row_version = row_version + 1, updated_by = $3,
  updated_at = now() WHERE id = ANY($1)`, ids, status, operatorID)
	return err
}

// ---------------------------------------------------------------------------
// 列表、详情
// ---------------------------------------------------------------------------

// PageCounts 是条目里各审核状态的页数。
type PageCounts struct {
	Unassigned int `json:"unassigned"`
	Pending    int `json:"pending"`
	Approved   int `json:"approved"`
	Rejected   int `json:"rejected"`
}

// ItemSummary 是列表里的一个条目。
type ItemSummary struct {
	ID         uuid.UUID  `json:"id"`
	CategoryID uuid.UUID  `json:"category_id"`
	ItemNo     int        `json:"item_no"`
	ArchiveNo  *string    `json:"archive_no"`
	Title      string     `json:"title"`
	Status     string     `json:"status"`
	RowVersion int        `json:"row_version"`
	PageCount  int        `json:"page_count"`
	PageCounts PageCounts `json:"page_counts"`
	// LockedAt 是最近一次定稿的时间（最近一条 archive_item / lock 审计）；没有定稿为 null。
	LockedAt *time.Time `json:"locked_at"`
	// NewVersionAfterLock：已定稿，而且有页的当前版本是定稿之后出的。
	NewVersionAfterLock bool      `json:"new_version_after_lock"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// summarySelect 是列表、详情共用的部分；条件里用别名 i。页数按 folder_files 的审核状态分（挂接的文件不会被删除，
// 已归类的文件删不掉）；定稿时间读审计。
const summarySelect = `SELECT i.id, i.category_id, i.item_no, i.archive_no, i.title, i.status, i.row_version,
  pc.total, pc.unassigned, pc.pending, pc.approved, pc.rejected, la.locked_at,
  (la.locked_at IS NOT NULL AND EXISTS (
     SELECT 1 FROM archive_item_files af JOIN file_snapshots s ON s.file_id = af.file_id
     WHERE af.archive_item_id = i.id AND s.created_at > la.locked_at)),
  i.updated_at
FROM archive_items i
CROSS JOIN LATERAL (
  SELECT count(*) AS total,
    count(*) FILTER (WHERE ff.review_status = 'unassigned') AS unassigned,
    count(*) FILTER (WHERE ff.review_status = 'pending') AS pending,
    count(*) FILTER (WHERE ff.review_status = 'approved') AS approved,
    count(*) FILTER (WHERE ff.review_status = 'rejected') AS rejected
  FROM archive_item_files af JOIN folder_files ff ON ff.file_id = af.file_id
  WHERE af.archive_item_id = i.id) pc
CROSS JOIN LATERAL (
  SELECT max(a.created_at) AS locked_at FROM audit_logs a
  WHERE i.status = 'locked' AND a.target_type = 'archive_item' AND a.target_id = i.id AND a.action = 'lock') la`

func scanSummary(sc interface{ Scan(...any) error }) (ItemSummary, error) {
	var s ItemSummary
	err := sc.Scan(&s.ID, &s.CategoryID, &s.ItemNo, &s.ArchiveNo, &s.Title, &s.Status, &s.RowVersion,
		&s.PageCount, &s.PageCounts.Unassigned, &s.PageCounts.Pending, &s.PageCounts.Approved, &s.PageCounts.Rejected,
		&s.LockedAt, &s.NewVersionAfterLock, &s.UpdatedAt)
	return s, err
}

// listItemSummaries 是节点里直接放的未删除条目，按件号、id 排，从 (afterNo, afterID) 之后取 limit 个（after 为 nil 从头）。
func listItemSummaries(ctx context.Context, q db.Querier, categoryID uuid.UUID, afterNo *int, afterID uuid.UUID, limit int) ([]ItemSummary, error) {
	rows, err := q.Query(ctx, summarySelect+`
WHERE i.category_id = $1 AND i.deleted_at IS NULL AND ($2::int IS NULL OR (i.item_no, i.id) > ($2, $3))
ORDER BY i.item_no, i.id LIMIT $4`, categoryID, afterNo, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ItemSummary{}
	for rows.Next() {
		s, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// StatusCounts 是节点里直接放的未删除条目按状态的个数。
type StatusCounts struct {
	Draft  int `json:"draft"`
	Done   int `json:"done"`
	Locked int `json:"locked"`
}

func countItems(ctx context.Context, q db.Querier, categoryID uuid.UUID) (int, StatusCounts, error) {
	var total int
	var c StatusCounts
	err := q.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status = 'draft'),
  count(*) FILTER (WHERE status = 'done'), count(*) FILTER (WHERE status = 'locked')
FROM archive_items WHERE category_id = $1 AND deleted_at IS NULL`, categoryID).Scan(&total, &c.Draft, &c.Done, &c.Locked)
	return total, c, err
}

func itemSummary(ctx context.Context, q db.Querier, id uuid.UUID) (ItemSummary, error) {
	return scanSummary(q.QueryRow(ctx, summarySelect+` WHERE i.id = $1`, id))
}

// Page 是条目的一页（详情的 pages，按页号排）。
type Page struct {
	FileID   uuid.UUID `json:"file_id"`
	PageNo   int       `json:"page_no"`
	FileName string    `json:"file_name"`
	FolderID uuid.UUID `json:"folder_id"`
	// FolderPath 是所在文件夹的路径（从项目根文件夹起用 `/` 连接）。
	FolderPath   string `json:"folder_path"`
	ReviewStatus string `json:"review_status"`
	ReviewRound  int    `json:"review_round"`
	// VersionNo 是当前版本号。
	VersionNo int `json:"version_no"`
	// NewVersionAfterLock：条目已定稿，而且这一页的当前版本是定稿之后出的。
	NewVersionAfterLock bool `json:"new_version_after_lock"`
}

// itemPages 读条目的页（跨模块的读：folder_files、file_snapshots、folders）。lockedAt 是定稿时间（没定稿为 nil）。
func itemPages(ctx context.Context, q db.Querier, itemID uuid.UUID, lockedAt *time.Time) ([]Page, error) {
	rows, err := q.Query(ctx, `SELECT af.file_id, af.page_no, ff.file_name, ff.folder_id, ff.review_status, ff.review_round,
  coalesce(s.version_no, 0), s.created_at
FROM archive_item_files af
JOIN folder_files ff ON ff.file_id = af.file_id
LEFT JOIN LATERAL (SELECT version_no, created_at FROM file_snapshots
                   WHERE file_id = af.file_id ORDER BY version_no DESC LIMIT 1) s ON true
WHERE af.archive_item_id = $1
ORDER BY af.page_no`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Page{}
	folderIDs := map[uuid.UUID]bool{}
	for rows.Next() {
		var p Page
		var snapAt *time.Time
		if err := rows.Scan(&p.FileID, &p.PageNo, &p.FileName, &p.FolderID, &p.ReviewStatus, &p.ReviewRound,
			&p.VersionNo, &snapAt); err != nil {
			return nil, err
		}
		p.NewVersionAfterLock = lockedAt != nil && snapAt != nil && snapAt.After(*lockedAt)
		folderIDs[p.FolderID] = true
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(folderIDs))
	for id := range folderIDs {
		ids = append(ids, id)
	}
	paths, err := folderPaths(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].FolderPath = paths[out[i].FolderID]
	}
	return out, nil
}

// folderPaths 是这些文件夹从项目根文件夹起用 `/` 连接的路径（跨模块的读）。
func folderPaths(ctx context.Context, q db.Querier, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := q.Query(ctx, `SELECT f.id, string_agg(a.folder_name, '/' ORDER BY a.path_code)
FROM folders f
CROSS JOIN LATERAL generate_series(1, length(f.path_code) / 6) AS k(n)
JOIN folders a ON a.project_id = f.project_id AND a.path_code = left(f.path_code, 6 * k.n)
WHERE f.id = ANY($1)
GROUP BY f.id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

// pageStats 是定稿前置条件要看的页：页数、没审核通过的页数和其中最前的页号（审核状态按这一刻读，不锁文件）。
type pageStats struct {
	Pages       int
	NotApproved int
	FirstNotOK  int
}

func itemPageStats(ctx context.Context, q db.Querier, ids []uuid.UUID) (map[uuid.UUID]pageStats, error) {
	rows, err := q.Query(ctx, `SELECT af.archive_item_id, count(*),
  count(*) FILTER (WHERE ff.review_status <> 'approved'),
  coalesce(min(af.page_no) FILTER (WHERE ff.review_status <> 'approved'), 0)
FROM archive_item_files af JOIN folder_files ff ON ff.file_id = af.file_id
WHERE af.archive_item_id = ANY($1) GROUP BY af.archive_item_id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]pageStats{}
	for rows.Next() {
		var id uuid.UUID
		var s pageStats
		if err := rows.Scan(&id, &s.Pages, &s.NotApproved, &s.FirstNotOK); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}
