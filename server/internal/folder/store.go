package folder

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/catalog"
	"doclite/server/internal/file"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/pathcode"
)

// folder 的 SQL：folders、folder_files。
//
// 跨模块的读（只读）：projects（是否已删除）、file_snapshots（当前版本号）、archive_item_files、archive_items（是否
// 已归类、条目是否修改中）。封存检查和文件夹的归档信息经 catalog 的包级函数（SealedCheck、FolderArchives）。
// 跨模块的写（例外）：删除文件时把进行中的分配（file_reviews 里 open 的行）改成 cancelled——删除必须在同一个事务里
// 取消分配（requirements 5.5、5.8），而 internal/review 引用本包，这里不能反过来调它。files、file_snapshots、file_logs
// 经 internal/file 的包级函数写。

// folderRow 是 folders 的一行。
type folderRow struct {
	ID         uuid.UUID
	ProjectID  uuid.UUID
	ParentID   *uuid.UUID
	FolderName string
	PathCode   string
	Remark     *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Deleted    bool
}

const folderColumns = `f.id, f.project_id, f.parent_id, f.folder_name, f.path_code, f.remark, f.created_at, f.updated_at,
  f.deleted_at IS NOT NULL`

func scanFolder(sc pgx.Row) (*folderRow, error) {
	var f folderRow
	if err := sc.Scan(&f.ID, &f.ProjectID, &f.ParentID, &f.FolderName, &f.PathCode, &f.Remark, &f.CreatedAt, &f.UpdatedAt,
		&f.Deleted); err != nil {
		return nil, err
	}
	return &f, nil
}

// getFolder 读一个文件夹（含已删除的）；不存在为 nil。
func getFolder(ctx context.Context, q db.Querier, id uuid.UUID) (*folderRow, error) {
	f, err := scanFolder(q.QueryRow(ctx, `SELECT `+folderColumns+` FROM folders f WHERE f.id = $1`, id))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return f, err
}

// lockFolders 按 id 升序锁住这些文件夹行（FOR NO KEY UPDATE），锁住之后读（含已删除的）。
func lockFolders(ctx context.Context, q db.Querier, ids ...uuid.UUID) (map[uuid.UUID]*folderRow, error) {
	rows, err := q.Query(ctx, `SELECT `+folderColumns+` FROM folders f WHERE f.id = ANY($1) ORDER BY f.id FOR NO KEY UPDATE`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]*folderRow{}
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out[f.ID] = f
	}
	return out, rows.Err()
}

// liveFolderProject：未删除的文件夹所在的项目（项目是否已删除由项目授权检查）；没有时 ok 为 false。
func liveFolderProject(ctx context.Context, q db.Querier, id uuid.UUID) (pid uuid.UUID, ok bool, err error) {
	err = q.QueryRow(ctx, `SELECT project_id FROM folders WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&pid)
	if db.IsNoRows(err) {
		return uuid.Nil, false, nil
	}
	return pid, err == nil, err
}

// Ancestor 是祖先链里的一个文件夹。
type Ancestor struct {
	ID         uuid.UUID `json:"id"`
	FolderName string    `json:"folder_name"`
	PathCode   string    `json:"path_code"`
}

// ancestors 是 path 的祖先（从根到父）。
func ancestors(ctx context.Context, q db.Querier, projectID uuid.UUID, path string) ([]Ancestor, error) {
	out := []Ancestor{}
	prefixes := pathcode.Ancestors(path)
	if len(prefixes) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id, folder_name, path_code FROM folders
WHERE project_id = $1 AND path_code = ANY($2::text[]) ORDER BY path_code`, projectID, prefixes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Ancestor
		if err := rows.Scan(&a.ID, &a.FolderName, &a.PathCode); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SubfolderEntry 是 children 里的一个子文件夹。
type SubfolderEntry struct {
	ID         uuid.UUID `json:"id"`
	FolderName string    `json:"folder_name"`
	Remark     *string   `json:"remark"`
	PathCode   string    `json:"path_code"`
	// FolderCount、FileCount 是未删除的直接子文件夹数、文件数。
	FolderCount int `json:"folder_count"`
	FileCount   int `json:"file_count"`
	// Archive 是归档信息（是某个未删除条目的来源时；否则为 null）。
	Archive *catalog.FolderArchive `json:"archive"`
}

// FileEntry 是 children 里的一个文件（folder_files 的一行）。
type FileEntry struct {
	FolderFileID uuid.UUID `json:"folder_file_id"`
	FileID       uuid.UUID `json:"file_id"`
	FileName     string    `json:"file_name"`
	SortNo       int       `json:"sort_no"`
	ReviewStatus string    `json:"review_status"`
	ReviewRound  int       `json:"review_round"`
	// VersionNo 是当前版本号。
	VersionNo int `json:"version_no"`
	// Cataloged：已归类（archive_item_files 里有它）。
	Cataloged bool      `json:"cataloged"`
	CreatedAt time.Time `json:"created_at"`
}

func listSubfolders(ctx context.Context, q db.Querier, folderID uuid.UUID) ([]SubfolderEntry, error) {
	rows, err := q.Query(ctx, `SELECT c.id, c.folder_name, c.remark, c.path_code,
  (SELECT count(*) FROM folders g WHERE g.parent_id = c.id AND g.deleted_at IS NULL),
  (SELECT count(*) FROM folder_files x WHERE x.folder_id = c.id AND x.deleted_at IS NULL)
FROM folders c WHERE c.parent_id = $1 AND c.deleted_at IS NULL
ORDER BY c.sort_no, c.folder_name, c.id`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SubfolderEntry{}
	for rows.Next() {
		var e SubfolderEntry
		if err := rows.Scan(&e.ID, &e.FolderName, &e.Remark, &e.PathCode, &e.FolderCount, &e.FileCount); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func listFiles(ctx context.Context, q db.Querier, folderID uuid.UUID) ([]FileEntry, error) {
	rows, err := q.Query(ctx, `SELECT ff.id, ff.file_id, ff.file_name, ff.sort_no, ff.review_status, ff.review_round,
  (SELECT max(s.version_no) FROM file_snapshots s WHERE s.file_id = ff.file_id),
  EXISTS (SELECT 1 FROM archive_item_files af WHERE af.file_id = ff.file_id),
  ff.created_at
FROM folder_files ff WHERE ff.folder_id = $1 AND ff.deleted_at IS NULL
ORDER BY ff.sort_no, ff.file_name, ff.id`, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FileEntry{}
	for rows.Next() {
		var e FileEntry
		if err := rows.Scan(&e.FolderFileID, &e.FileID, &e.FileName, &e.SortNo, &e.ReviewStatus, &e.ReviewRound, &e.VersionNo,
			&e.Cataloged, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// siblingNamed：parentID 下未删除的、名字（不区分大小写）是 name 的文件夹，exclude 除外；没有为 nil。
func siblingNamed(ctx context.Context, q db.Querier, parentID uuid.UUID, name string, exclude uuid.UUID) (*folderRow, error) {
	f, err := scanFolder(q.QueryRow(ctx, `SELECT `+folderColumns+` FROM folders f
WHERE f.parent_id = $1 AND f.deleted_at IS NULL AND lower(f.folder_name) = lower($2) AND f.id <> $3 LIMIT 1`,
		parentID, name, exclude))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return f, err
}

// allocateChild 在父文件夹上原子地领一个子序号（last_child_no + 1），返回父编码和序号。
func allocateChild(ctx context.Context, q db.Querier, parentID uuid.UUID) (string, int, error) {
	var path string
	var n int
	err := q.QueryRow(ctx, `UPDATE folders SET last_child_no = last_child_no + 1 WHERE id = $1 RETURNING path_code, last_child_no`,
		parentID).Scan(&path, &n)
	return path, n, err
}

func insertFolder(ctx context.Context, q db.Querier, f *folderRow, sortNo int, createdBy uuid.UUID) error {
	_, err := q.Exec(ctx, `INSERT INTO folders (id, project_id, parent_id, folder_name, path_code, sort_no, remark, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, f.ID, f.ProjectID, f.ParentID, f.FolderName, f.PathCode, sortNo, f.Remark, createdBy)
	return err
}

// updateFolder 写回名称、备注；移动时另外换 parent_id、sort_no（sortNo 为 0 表示不移动）。
func updateFolder(ctx context.Context, q db.Querier, f *folderRow, sortNo int) error {
	if sortNo > 0 {
		_, err := q.Exec(ctx, `UPDATE folders SET folder_name = $2, remark = $3, parent_id = $4, sort_no = $5, updated_at = now()
WHERE id = $1`, f.ID, f.FolderName, f.Remark, f.ParentID, sortNo)
		return err
	}
	_, err := q.Exec(ctx, `UPDATE folders SET folder_name = $2, remark = $3, updated_at = now() WHERE id = $1`,
		f.ID, f.FolderName, f.Remark)
	return err
}

// rebaseSubtree 用一条 UPDATE 给整棵子树（含已删除的）换编码前缀。
func rebaseSubtree(ctx context.Context, q db.Querier, projectID uuid.UUID, oldPrefix, newPrefix string) error {
	lo, hi := oldPrefix, pathcode.Upper(oldPrefix)
	_, err := q.Exec(ctx, `UPDATE folders SET path_code = $4 || substr(path_code, length($2) + 1), updated_at = now()
WHERE project_id = $1 AND path_code >= $2 COLLATE "C" AND path_code < $3 COLLATE "C"`, projectID, lo, hi, newPrefix)
	return err
}

// subtreeLiveFileIDs 是子树里未删除的文件的 file_id。
func subtreeLiveFileIDs(ctx context.Context, q db.Querier, projectID uuid.UUID, prefix string) ([]uuid.UUID, error) {
	lo, hi := prefix, pathcode.Upper(prefix)
	return db.CollectIDs(ctx, q, `SELECT ff.file_id FROM folder_files ff JOIN folders f ON f.id = ff.folder_id
WHERE f.project_id = $1 AND f.path_code >= $2 COLLATE "C" AND f.path_code < $3 COLLATE "C" AND ff.deleted_at IS NULL`,
		projectID, lo, hi)
}

// subtreeLiveFileCount 是子树里未删除的文件数，最多数到 limit。
func subtreeLiveFileCount(ctx context.Context, q db.Querier, projectID uuid.UUID, prefix string, limit int) (int, error) {
	lo, hi := prefix, pathcode.Upper(prefix)
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM folder_files ff JOIN folders f ON f.id = ff.folder_id
WHERE f.project_id = $1 AND f.path_code >= $2 COLLATE "C" AND f.path_code < $3 COLLATE "C" AND ff.deleted_at IS NULL
LIMIT $4) x`, projectID, lo, hi, limit).Scan(&n)
	return n, err
}

// subtreeLiveFolderIDs 是子树里未删除的文件夹。
func subtreeLiveFolderIDs(ctx context.Context, q db.Querier, projectID uuid.UUID, prefix string) ([]uuid.UUID, error) {
	lo, hi := prefix, pathcode.Upper(prefix)
	return db.CollectIDs(ctx, q, `SELECT id FROM folders
WHERE project_id = $1 AND path_code >= $2 COLLATE "C" AND path_code < $3 COLLATE "C" AND deleted_at IS NULL`, projectID, lo, hi)
}

// catalogedAmong 是这些文件里删不掉的已归类文件（在某个条目里、条目不在修改中），按给的顺序。修改中的条目的页
// 可以删（来源文件夹和未归档的一样可以整理，重新归档时移出）。
func catalogedAmong(ctx context.Context, q db.Querier, fileIDs []uuid.UUID) ([]uuid.UUID, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	return db.CollectIDs(ctx, q, `SELECT t.id FROM unnest($1::uuid[]) WITH ORDINALITY AS t(id, i)
WHERE EXISTS (SELECT 1 FROM archive_item_files af JOIN archive_items a ON a.id = af.archive_item_id
              WHERE af.file_id = t.id AND a.source_status <> 'modifying') ORDER BY t.i`, fileIDs)
}

// softDeleteFolders 软删除这些文件夹。
func softDeleteFolders(ctx context.Context, q db.Querier, ids []uuid.UUID, by uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE folders SET deleted_at = now(), deleted_by = $2 WHERE id = ANY($1) AND deleted_at IS NULL`, ids, by)
	return err
}

// deletedFile 是被删掉的文件（写日志用）。
type deletedFile struct {
	FileID   uuid.UUID
	FolderID uuid.UUID
	FileName string
}

// softDeleteFilesIn 软删除这些文件夹里未删除的文件。
func softDeleteFilesIn(ctx context.Context, q db.Querier, folderIDs []uuid.UUID, by uuid.UUID) ([]deletedFile, error) {
	return scanDeleted(q.Query(ctx, `UPDATE folder_files SET deleted_at = now(), deleted_by = $2
WHERE folder_id = ANY($1) AND deleted_at IS NULL RETURNING file_id, folder_id, file_name`, folderIDs, by))
}

// softDeleteFolderFiles 软删除这些 folder_files（按 id）。
func softDeleteFolderFiles(ctx context.Context, q db.Querier, ids []uuid.UUID, by uuid.UUID) ([]deletedFile, error) {
	return scanDeleted(q.Query(ctx, `UPDATE folder_files SET deleted_at = now(), deleted_by = $2
WHERE id = ANY($1) AND deleted_at IS NULL RETURNING file_id, folder_id, file_name`, ids, by))
}

func scanDeleted(rows interface {
	Next() bool
	Scan(...any) error
	Close()
	Err() error
}, err error) ([]deletedFile, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []deletedFile
	for rows.Next() {
		var d deletedFile
		if err := rows.Scan(&d.FileID, &d.FolderID, &d.FileName); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// cancelOpenReviews 把这些文件进行中的分配改成 cancelled（跨模块的写，见文件开头）。
func cancelOpenReviews(ctx context.Context, q db.Querier, fileIDs []uuid.UUID) error {
	if len(fileIDs) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `UPDATE file_reviews SET status = 'cancelled' WHERE status = 'open' AND file_id = ANY($1)`, fileIDs)
	return err
}

// folderFileRow 是 folder_files 的一行（界面上的"文件"）。
type folderFileRow struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	FolderID  uuid.UUID
	FileID    uuid.UUID
	FileName  string
	SortNo    int
	// FolderPathCode 是所在文件夹的 path_code（批量移动按它排序）。
	FolderPathCode string
}

// liveFolderFiles：这些 folder_files 里未删除的（所在文件夹、项目也未删除）。
func liveFolderFiles(ctx context.Context, q db.Querier, ids []uuid.UUID) (map[uuid.UUID]*folderFileRow, error) {
	rows, err := q.Query(ctx, `SELECT ff.id, ff.project_id, ff.folder_id, ff.file_id, ff.file_name, ff.sort_no, f.path_code
FROM folder_files ff JOIN folders f ON f.id = ff.folder_id JOIN projects p ON p.id = ff.project_id
WHERE ff.id = ANY($1) AND ff.deleted_at IS NULL AND f.deleted_at IS NULL AND p.deleted_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]*folderFileRow{}
	for rows.Next() {
		var e folderFileRow
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.FolderID, &e.FileID, &e.FileName, &e.SortNo, &e.FolderPathCode); err != nil {
			return nil, err
		}
		out[e.ID] = &e
	}
	return out, rows.Err()
}

// nameTaken：文件夹里有没有未删除的、名字（不区分大小写）是 name 的文件，exclude 除外。
func nameTaken(ctx context.Context, q db.Querier, folderID uuid.UUID, name string, exclude uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM folder_files
WHERE folder_id = $1 AND deleted_at IS NULL AND lower(file_name) = lower($2) AND id <> $3)`, folderID, name, exclude).Scan(&ok)
	return ok, err
}

// firstFreeName 是 candidates 里第一个在文件夹里没被占用的名字（不区分大小写，只看未删除的）；都被占了为空串。
func firstFreeName(ctx context.Context, q db.Querier, folderID uuid.UUID, candidates []string) (string, error) {
	var name string
	err := q.QueryRow(ctx, `SELECT c.name FROM unnest($2::text[]) WITH ORDINALITY AS c(name, i)
WHERE NOT EXISTS (SELECT 1 FROM folder_files ff
                  WHERE ff.folder_id = $1 AND ff.deleted_at IS NULL AND lower(ff.file_name) = lower(c.name))
ORDER BY c.i LIMIT 1`, folderID, candidates).Scan(&name)
	if db.IsNoRows(err) {
		return "", nil
	}
	return name, err
}

// nextSortNo 是文件夹里未删除文件的 max(sort_no) + 1。
func nextSortNo(ctx context.Context, q db.Querier, folderID uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COALESCE(max(sort_no), 0) + 1 FROM folder_files WHERE folder_id = $1 AND deleted_at IS NULL`,
		folderID).Scan(&n)
	return n, err
}

const insertFolderFileSQL = `INSERT INTO folder_files (id, project_id, folder_id, file_id, file_name, sort_no, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)`

// slotOf 一次查出放进文件夹要的三样：candidates 里第一个没被占用的名字（见 firstFreeName，都被占了为空串）、
// max(sort_no) + 1（见 nextSortNo）、文件夹的路径（见 file.FolderPaths）。
func slotOf(ctx context.Context, q db.Querier, folderID uuid.UUID, candidates []string) (name string, sortNo int, path string, err error) {
	var free *string
	err = q.QueryRow(ctx, `SELECT (SELECT c.name FROM unnest($2::text[]) WITH ORDINALITY AS c(name, i)
    WHERE NOT EXISTS (SELECT 1 FROM folder_files ff
                      WHERE ff.folder_id = $1 AND ff.deleted_at IS NULL AND lower(ff.file_name) = lower(c.name))
    ORDER BY c.i LIMIT 1),
  (SELECT COALESCE(max(sort_no), 0) + 1 FROM folder_files WHERE folder_id = $1 AND deleted_at IS NULL),
  `+file.FolderPathSQL("$1::uuid"), folderID, candidates).Scan(&free, &sortNo, &path)
	if free != nil {
		name = *free
	}
	return name, sortNo, path, err
}

// folderNames 是文件夹里未删除文件的名字（小写）和 max(sort_no) + 1。
func folderNames(ctx context.Context, q db.Querier, folderID uuid.UUID) (map[string]bool, int, error) {
	var list []string
	var next int
	err := q.QueryRow(ctx, `SELECT coalesce(array_agg(lower(file_name)), '{}'), coalesce(max(sort_no), 0) + 1
FROM folder_files WHERE folder_id = $1 AND deleted_at IS NULL`, folderID).Scan(&list, &next)
	if err != nil {
		return nil, 0, err
	}
	used := make(map[string]bool, len(list))
	for _, n := range list {
		used[n] = true
	}
	return used, next, nil
}

// fileMove 是批量移动的一个文件：folder_files 的 id、新名字、新 sort_no。
type fileMove struct {
	ID     uuid.UUID
	Name   string
	SortNo int
}

// moveFolderFiles 把这些文件一次写到文件夹 folderID（只改 folder_files，硬性规则 3）。
func moveFolderFiles(ctx context.Context, q db.Querier, folderID uuid.UUID, moves []fileMove) error {
	if len(moves) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(moves))
	ns := make([]string, len(moves))
	sorts := make([]int, len(moves))
	for i, m := range moves {
		ids[i], ns[i], sorts[i] = m.ID, m.Name, m.SortNo
	}
	_, err := q.Exec(ctx, `UPDATE folder_files ff SET folder_id = $1, file_name = u.name, sort_no = u.sort_no, updated_at = now()
FROM unnest($2::uuid[], $3::text[], $4::int[]) AS u(id, name, sort_no) WHERE ff.id = u.id`, folderID, ids, ns, sorts)
	return err
}

// updateFolderFile 写回位置和名字（只改 folder_files，硬性规则 3）。
func updateFolderFile(ctx context.Context, q db.Querier, id, folderID uuid.UUID, name string, sortNo int) error {
	_, err := q.Exec(ctx, `UPDATE folder_files SET folder_id = $2, file_name = $3, sort_no = $4, updated_at = now() WHERE id = $1`,
		id, folderID, name, sortNo)
	return err
}
