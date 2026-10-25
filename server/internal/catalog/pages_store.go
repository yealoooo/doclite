package catalog

import (
	"context"

	"github.com/google/uuid"

	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
)

// 挂接（archive_item_files）的 SQL。写挂接的语句只写 archive_item_files，不碰快照和文件夹（硬性规则 4）。
// ux_archive_item_page 是 DEFERRABLE INITIALLY DEFERRED，提交时才检查，中间可以重号。
//
// 跨模块的读（只读）：folder_files、folders（文件名、所在文件夹、是否已删除、审核状态、在哪个项目）、file_snapshots
// （当前版本号）。

// itemPageList 是条目现在的页（按页号）。
func itemPageList(ctx context.Context, q db.Querier, itemID uuid.UUID) ([]curPage, error) {
	rows, err := q.Query(ctx, `SELECT file_id, page_no FROM archive_item_files WHERE archive_item_id = $1 ORDER BY page_no`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []curPage
	for rows.Next() {
		var p curPage
		if err := rows.Scan(&p.FileID, &p.PageNo); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// pageFile 是一个文件现在的样子（锁住 files 行之后用新语句读）。
type pageFile struct {
	FileID       uuid.UUID
	FolderID     uuid.UUID
	FileName     string
	ReviewStatus string
	// Live：文件和所在文件夹都未删除。
	Live      bool
	VersionNo int
	// FolderPath 是所在文件夹的路径（从项目根文件夹起用 `/` 连接）。
	FolderPath string
}

// pageFiles 读这个项目里这些文件现在的样子（不在这个项目里的不在结果里）。
func pageFiles(ctx context.Context, q db.Querier, projectID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]*pageFile, error) {
	out := map[uuid.UUID]*pageFile{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT ff.file_id, ff.folder_id, ff.file_name, ff.review_status,
  (ff.deleted_at IS NULL AND f.deleted_at IS NULL),
  coalesce((SELECT max(s.version_no) FROM file_snapshots s WHERE s.file_id = ff.file_id), 0)
FROM folder_files ff JOIN folders f ON f.id = ff.folder_id
WHERE ff.file_id = ANY($1) AND ff.project_id = $2`, ids, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	folderSet := map[uuid.UUID]bool{}
	for rows.Next() {
		var f pageFile
		if err := rows.Scan(&f.FileID, &f.FolderID, &f.FileName, &f.ReviewStatus, &f.Live, &f.VersionNo); err != nil {
			return nil, err
		}
		out[f.FileID] = &f
		folderSet[f.FolderID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	folders := make([]uuid.UUID, 0, len(folderSet))
	for id := range folderSet {
		folders = append(folders, id)
	}
	paths, err := file.FolderPaths(ctx, q, folders)
	if err != nil {
		return nil, err
	}
	for _, f := range out {
		f.FolderPath = paths[f.FolderID]
	}
	return out, nil
}

// placementRow 是一个文件挂在哪个条目。
type placementRow struct {
	FileID     uuid.UUID
	ItemID     uuid.UUID
	ItemNo     int
	ArchiveNo  *string
	Title      string
	PageNo     int
	ItemStatus string
	CategoryID uuid.UUID
}

// placementsOf 是这些文件里挂在条目里的（except 那个条目里的不算）：file_id → 在哪。
func placementsOf(ctx context.Context, q db.Querier, fileIDs []uuid.UUID, except uuid.UUID) (map[uuid.UUID]*placementRow, error) {
	out := map[uuid.UUID]*placementRow{}
	if len(fileIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT af.file_id, a.id, a.item_no, a.archive_no, a.title, af.page_no, a.status, a.category_id
FROM archive_item_files af JOIN archive_items a ON a.id = af.archive_item_id
WHERE af.file_id = ANY($1) AND af.archive_item_id <> $2`, fileIDs, except)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p placementRow
		if err := rows.Scan(&p.FileID, &p.ItemID, &p.ItemNo, &p.ArchiveNo, &p.Title, &p.PageNo, &p.ItemStatus, &p.CategoryID); err != nil {
			return nil, err
		}
		out[p.FileID] = &p
	}
	return out, rows.Err()
}

// liveProjectPlacements 是这些文件里在这个项目、未删除（所在文件夹也未删除）的（file_id → 在哪，没挂接的为 nil）。
func liveProjectPlacements(ctx context.Context, q db.Querier, projectID uuid.UUID, fileIDs []uuid.UUID) (map[uuid.UUID]*placementRow, error) {
	out := map[uuid.UUID]*placementRow{}
	if len(fileIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT ff.file_id, a.id, a.item_no, a.archive_no, a.title, af.page_no, a.status, a.category_id
FROM unnest($2::uuid[]) AS t(file_id)
JOIN folder_files ff ON ff.file_id = t.file_id AND ff.project_id = $1 AND ff.deleted_at IS NULL
JOIN folders f ON f.id = ff.folder_id AND f.deleted_at IS NULL
LEFT JOIN archive_item_files af ON af.file_id = ff.file_id
LEFT JOIN archive_items a ON a.id = af.archive_item_id`, projectID, fileIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var fid uuid.UUID
		var itemID, catID *uuid.UUID
		var itemNo, pageNo *int
		var archiveNo, title, status *string
		if err := rows.Scan(&fid, &itemID, &itemNo, &archiveNo, &title, &pageNo, &status, &catID); err != nil {
			return nil, err
		}
		if itemID == nil {
			out[fid] = nil
			continue
		}
		out[fid] = &placementRow{FileID: fid, ItemID: *itemID, ItemNo: *itemNo, ArchiveNo: archiveNo, Title: *title,
			PageNo: *pageNo, ItemStatus: *status, CategoryID: *catID}
	}
	return out, rows.Err()
}

// liveFileProject 是未删除文件（所在文件夹也未删除）所在的项目；没有为 uuid.Nil。
func liveFileProject(ctx context.Context, q db.Querier, fileID uuid.UUID) (uuid.UUID, error) {
	var pid uuid.UUID
	err := q.QueryRow(ctx, `SELECT ff.project_id FROM folder_files ff JOIN folders f ON f.id = ff.folder_id
WHERE ff.file_id = $1 AND ff.deleted_at IS NULL AND f.deleted_at IS NULL`, fileID).Scan(&pid)
	if db.IsNoRows(err) {
		return uuid.Nil, nil
	}
	return pid, err
}

// deletePages 移出这些页。
func deletePages(ctx context.Context, q db.Querier, itemID uuid.UUID, fileIDs []uuid.UUID) error {
	if len(fileIDs) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `DELETE FROM archive_item_files WHERE archive_item_id = $1 AND file_id = ANY($2)`, itemID, fileIDs)
	return err
}

// repage 改留下的页的页号。
func repage(ctx context.Context, q db.Querier, itemID uuid.UUID, moves []pageMove) error {
	if len(moves) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(moves))
	pages := make([]int32, len(moves))
	for i, m := range moves {
		ids[i], pages[i] = m.FileID, int32(m.To)
	}
	_, err := q.Exec(ctx, `UPDATE archive_item_files af SET page_no = t.page_no
FROM unnest($2::uuid[], $3::int[]) AS t(file_id, page_no)
WHERE af.archive_item_id = $1 AND af.file_id = t.file_id`, itemID, ids, pages)
	return err
}

// insertPages 挂上新页。
func insertPages(ctx context.Context, q db.Querier, projectID, itemID, linkedBy uuid.UUID, moves []pageMove) error {
	if len(moves) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(moves))
	pages := make([]int32, len(moves))
	for i, m := range moves {
		ids[i], pages[i] = m.FileID, int32(m.To)
	}
	_, err := q.Exec(ctx, `INSERT INTO archive_item_files (project_id, archive_item_id, file_id, page_no, linked_by)
SELECT $1, $2, t.file_id, t.page_no, $3 FROM unnest($4::uuid[], $5::int[]) AS t(file_id, page_no)`,
		projectID, itemID, linkedBy, ids, pages)
	return err
}

// CategoryNames 是这些节点的名称链（从根节点的下一级起到节点自己；根节点为空数组）。祖先按编码精确查。
func CategoryNames(ctx context.Context, q db.Querier, ids []uuid.UUID) (map[uuid.UUID][]string, error) {
	out := make(map[uuid.UUID][]string, len(ids))
	for _, id := range ids {
		out[id] = []string{}
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT c.id, array_agg(x.category_name ORDER BY x.path_code)
FROM archive_categories c
CROSS JOIN LATERAL generate_series(12, length(c.path_code), 6) AS g(n)
JOIN archive_categories x ON x.project_id = c.project_id AND x.path_code = left(c.path_code, g.n)
WHERE c.id = ANY($1)
GROUP BY c.id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var names []string
		if err := rows.Scan(&id, &names); err != nil {
			return nil, err
		}
		out[id] = names
	}
	return out, rows.Err()
}
