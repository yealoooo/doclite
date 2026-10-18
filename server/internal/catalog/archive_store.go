package catalog

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/platform/db"
)

// 整个文件夹归档、封存的 SQL（archive.go、seal.go）。
//
// 跨模块的读（只读）：folders（文件夹名、路径、子文件夹数、文件数、是否已删除）、folder_files（文件夹里的文件和
// 顺序）、file_snapshots（当前版本号）、users（解除封存的人的名字）。
// 跨模块的锁（例外）：归档、重新归档读来源文件夹的文件清单之前锁住文件夹行（[lockFolderRows]，和文件目录的写操作锁的
// 是同一行、同一种锁，见 seal.go）。

// folderNode 是归档时读的一个文件夹。
type folderNode struct {
	ID        uuid.UUID
	ParentID  *uuid.UUID
	Name      string
	PathCode  string
	ProjectID uuid.UUID
	Deleted   bool
}

// folderNodes 读这些文件夹（含已删除的，不在结果里的是不存在）。
func folderNodes(ctx context.Context, q db.Querier, ids []uuid.UUID) (map[uuid.UUID]*folderNode, error) {
	rows, err := q.Query(ctx, `SELECT id, parent_id, folder_name, path_code, project_id, deleted_at IS NOT NULL
FROM folders WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]*folderNode{}
	for rows.Next() {
		var f folderNode
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.PathCode, &f.ProjectID, &f.Deleted); err != nil {
			return nil, err
		}
		out[f.ID] = &f
	}
	return out, rows.Err()
}

// liveChildren 是这些文件夹未删除的直接子文件夹（父文件夹 → 子文件夹，按 sort_no、名称、id 排，和 children 接口一样）。
func liveChildren(ctx context.Context, q db.Querier, parentIDs []uuid.UUID) (map[uuid.UUID][]*folderNode, error) {
	rows, err := q.Query(ctx, `SELECT id, parent_id, folder_name, path_code, project_id, false FROM folders
WHERE parent_id = ANY($1) AND deleted_at IS NULL ORDER BY parent_id, sort_no, folder_name, id`, parentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID][]*folderNode{}
	for rows.Next() {
		var f folderNode
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.PathCode, &f.ProjectID, &f.Deleted); err != nil {
			return nil, err
		}
		out[*f.ParentID] = append(out[*f.ParentID], &f)
	}
	return out, rows.Err()
}

// lockFolderRows 按 id 升序锁住这些文件夹行（FOR NO KEY UPDATE，和文件目录的 lockFolders 同一种锁）。
func lockFolderRows(ctx context.Context, q db.Querier, ids []uuid.UUID) error {
	rows, err := q.Query(ctx, `SELECT id FROM folders WHERE id = ANY($1) ORDER BY id FOR NO KEY UPDATE`, ids)
	if err != nil {
		return err
	}
	rows.Close()
	return rows.Err()
}

// folderState 是一个文件夹现在能不能成为一件要看的：子文件夹数、文件数、是不是哪个未删除条目的来源。
type folderState struct {
	Deleted    bool
	Subfolders int
	Files      int
	SourceOf   *archivedAs
}

// archivedAs 是"已归档为某件"。
type archivedAs struct {
	ItemID     uuid.UUID `json:"item_id"`
	ItemNo     int       `json:"item_no"`
	ArchiveNo  *string   `json:"archive_no"`
	Title      string    `json:"title"`
	CategoryID uuid.UUID `json:"category_id"`
	// CategoryNames 是条目所在节点的名称链，从根节点的下一级起。
	CategoryNames []string `json:"category_names"`
}

// folderStates 读这些文件夹现在的状态（锁住文件夹行之后读）。
func folderStates(ctx context.Context, q db.Querier, ids []uuid.UUID) (map[uuid.UUID]*folderState, error) {
	rows, err := q.Query(ctx, `SELECT f.id, f.deleted_at IS NOT NULL,
  (SELECT count(*) FROM folders g WHERE g.parent_id = f.id AND g.deleted_at IS NULL),
  (SELECT count(*) FROM folder_files x WHERE x.folder_id = f.id AND x.deleted_at IS NULL),
  a.id, a.item_no, a.archive_no, a.title, a.category_id
FROM folders f
LEFT JOIN archive_items a ON a.source_folder_id = f.id AND a.deleted_at IS NULL
WHERE f.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]*folderState{}
	var cats []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var s folderState
		var itemID, catID *uuid.UUID
		var itemNo *int
		var archiveNo, title *string
		if err := rows.Scan(&id, &s.Deleted, &s.Subfolders, &s.Files, &itemID, &itemNo, &archiveNo, &title, &catID); err != nil {
			return nil, err
		}
		if itemID != nil {
			s.SourceOf = &archivedAs{ItemID: *itemID, ItemNo: *itemNo, ArchiveNo: archiveNo, Title: *title, CategoryID: *catID}
			cats = append(cats, *catID)
		}
		out[id] = &s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names, err := categoryNames(ctx, q, cats)
	if err != nil {
		return nil, err
	}
	for _, s := range out {
		if s.SourceOf != nil {
			s.SourceOf.CategoryNames = names[s.SourceOf.CategoryID]
		}
	}
	return out, nil
}

// folderFile 是来源文件夹里的一个未删除文件。
type folderFile struct {
	FileID       uuid.UUID
	FileName     string
	ReviewStatus string
	VersionNo    int
}

// folderFiles 是这些文件夹里未删除的文件（文件夹 → 文件，按 sort_no、名称、id 排，就是页序）。
func folderFiles(ctx context.Context, q db.Querier, folderIDs []uuid.UUID) (map[uuid.UUID][]folderFile, error) {
	rows, err := q.Query(ctx, `SELECT ff.folder_id, ff.file_id, ff.file_name, ff.review_status,
  coalesce((SELECT max(s.version_no) FROM file_snapshots s WHERE s.file_id = ff.file_id), 0)
FROM folder_files ff WHERE ff.folder_id = ANY($1) AND ff.deleted_at IS NULL
ORDER BY ff.folder_id, ff.sort_no, ff.file_name, ff.id`, folderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID][]folderFile{}
	for rows.Next() {
		var folderID uuid.UUID
		var f folderFile
		if err := rows.Scan(&folderID, &f.FileID, &f.FileName, &f.ReviewStatus, &f.VersionNo); err != nil {
			return nil, err
		}
		out[folderID] = append(out[folderID], f)
	}
	return out, rows.Err()
}

// newItem 是归档新建的一件。
type newItem struct {
	ID          uuid.UUID
	ItemNo      int
	ArchiveNo   *string
	Title       string
	ExtraFields map[string]any
	FolderID    uuid.UUID
}

// insertItems 一条语句插入归档新建的条目（draft、sealed、row_version 1）。
func insertItems(ctx context.Context, q db.Querier, projectID, categoryID, createdBy uuid.UUID, items []newItem) error {
	ids := make([]uuid.UUID, len(items))
	nos := make([]int32, len(items))
	archiveNos := make([]*string, len(items))
	titles := make([]string, len(items))
	fields := make([]map[string]any, len(items))
	folders := make([]uuid.UUID, len(items))
	for i, it := range items {
		ids[i], nos[i], archiveNos[i], titles[i], fields[i], folders[i] =
			it.ID, int32(it.ItemNo), it.ArchiveNo, it.Title, it.ExtraFields, it.FolderID
	}
	_, err := q.Exec(ctx, `INSERT INTO archive_items (id, project_id, category_id, item_level, item_no, archive_no, title,
  extra_fields, status, row_version, created_by, source_folder_id, source_status)
SELECT t.id, $1, $2, 'item', t.item_no, t.archive_no, t.title, t.extra_fields, 'draft', 1, $3, t.folder_id, 'sealed'
FROM unnest($4::uuid[], $5::int[], $6::text[], $7::text[], $8::jsonb[], $9::uuid[])
  AS t(id, item_no, archive_no, title, extra_fields, folder_id)`,
		projectID, categoryID, createdBy, ids, nos, archiveNos, titles, fields, folders)
	return err
}

// insertItemPages 一条语句挂上多个条目的页。
func insertItemPages(ctx context.Context, q db.Querier, projectID, linkedBy uuid.UUID, itemIDs, fileIDs []uuid.UUID, pages []int32) error {
	if len(fileIDs) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO archive_item_files (project_id, archive_item_id, file_id, page_no, linked_by)
SELECT $1, t.item_id, t.file_id, t.page_no, $2 FROM unnest($3::uuid[], $4::uuid[], $5::int[]) AS t(item_id, file_id, page_no)`,
		projectID, linkedBy, itemIDs, fileIDs, pages)
	return err
}

// setSourceStatus 改条目的封存状态（row_version + 1）：解除封存时记下人、时间、原因，重新归档时清空。
func setSourceStatus(ctx context.Context, q db.Querier, id uuid.UUID, status string, by uuid.UUID, reason *string) error {
	if status == sourceModifying {
		_, err := q.Exec(ctx, `UPDATE archive_items SET source_status = 'modifying', unsealed_by = $2, unsealed_at = now(),
  unseal_reason = $3, row_version = row_version + 1, updated_by = $2, updated_at = now() WHERE id = $1`, id, by, reason)
		return err
	}
	_, err := q.Exec(ctx, `UPDATE archive_items SET source_status = 'sealed', unsealed_by = NULL, unsealed_at = NULL,
  unseal_reason = NULL, row_version = row_version + 1, updated_by = $2, updated_at = now() WHERE id = $1`, id, by)
	return err
}

// FolderArchive 是一个文件夹的归档信息（children 的子文件夹、`GET folders/{id}` 的 archive；没归档为 null）。
type FolderArchive struct {
	ItemID    uuid.UUID `json:"item_id"`
	ItemNo    int       `json:"item_no"`
	ArchiveNo *string   `json:"archive_no"`
	Title     string    `json:"title"`
	// CategoryID、CategoryNames 是条目所在的节点和它的名称链（从根节点的下一级起，条目直接在根节点下时为空数组）。
	CategoryID    uuid.UUID `json:"category_id"`
	CategoryNames []string  `json:"category_names"`
	// Status 是条目的状态 draft / locked；Locked 即 status = locked。
	Status string `json:"status"`
	Locked bool   `json:"locked"`
	// SourceStatus 是 sealed（封存）/ modifying（修改中）；UnsealedBy 等是修改中的那次解除封存（sealed 时为 null）。
	SourceStatus   string     `json:"source_status"`
	UnsealedBy     *uuid.UUID `json:"unsealed_by"`
	UnsealedByName *string    `json:"unsealed_by_name"`
	UnsealedAt     *time.Time `json:"unsealed_at"`
	UnsealReason   *string    `json:"unseal_reason"`
}

// FolderArchives 是这些文件夹里是某个未删除条目的来源的（文件夹 → 归档信息），在调用方的连接或事务里读
// （文件目录的 children、`GET folders/{id}` 用）。
func FolderArchives(ctx context.Context, q db.Querier, folderIDs []uuid.UUID) (map[uuid.UUID]*FolderArchive, error) {
	out := map[uuid.UUID]*FolderArchive{}
	if len(folderIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT a.source_folder_id, a.id, a.item_no, a.archive_no, a.title, a.category_id, a.status,
  a.source_status, a.unsealed_by, u.display_name, a.unsealed_at, a.unseal_reason
FROM archive_items a LEFT JOIN users u ON u.id = a.unsealed_by
WHERE a.source_folder_id = ANY($1) AND a.deleted_at IS NULL`, folderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cats []uuid.UUID
	for rows.Next() {
		var folderID uuid.UUID
		var a FolderArchive
		if err := rows.Scan(&folderID, &a.ItemID, &a.ItemNo, &a.ArchiveNo, &a.Title, &a.CategoryID, &a.Status,
			&a.SourceStatus, &a.UnsealedBy, &a.UnsealedByName, &a.UnsealedAt, &a.UnsealReason); err != nil {
			return nil, err
		}
		a.Locked = a.Status == statusLocked
		out[folderID] = &a
		cats = append(cats, a.CategoryID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names, err := categoryNames(ctx, q, cats)
	if err != nil {
		return nil, err
	}
	for _, a := range out {
		a.CategoryNames = names[a.CategoryID]
	}
	return out, nil
}

// sealedFolder 是一个封存的文件夹和它对应的条目。
type sealedFolder struct {
	FolderID   uuid.UUID
	FolderName string
	archivedAs
	Status string
}

// sealedAmong 是这些文件夹里封存的（某个未删除条目的来源、条目不在修改中），按 folderIDs 的顺序。
func sealedAmong(ctx context.Context, q db.Querier, folderIDs []uuid.UUID) ([]sealedFolder, error) {
	rows, err := q.Query(ctx, `SELECT f.id, f.folder_name, a.id, a.item_no, a.archive_no, a.title, a.category_id, a.status
FROM unnest($1::uuid[]) WITH ORDINALITY AS t(id, i)
JOIN folders f ON f.id = t.id
JOIN archive_items a ON a.source_folder_id = f.id AND a.deleted_at IS NULL AND a.source_status = 'sealed'
ORDER BY t.i`, folderIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (sealedFolder, error) {
		var s sealedFolder
		err := r.Scan(&s.FolderID, &s.FolderName, &s.ItemID, &s.ItemNo, &s.ArchiveNo, &s.Title, &s.CategoryID, &s.Status)
		return s, err
	})
}
