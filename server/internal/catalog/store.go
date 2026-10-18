package catalog

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/platform/db"
)

// catalog 的 SQL：archive_categories、archive_items、archive_item_files（pages_store.go）。
//
// 跨模块的读（只读）：projects.settings（归类设置）；folder_files、folders、file_snapshots（条目的页：文件名、
// 文件夹路径、审核状态、当前版本；挂接时锁住 files 行之后重读；归类信息、看板）；audit_logs 里 archive_item / lock
// 的时间（锁定时间）。
// 跨模块的写：file_logs 经 file.WriteLogs，files 行锁经 file.LockFiles（挂接、移出）。
// 跨模块的写：audit_logs 的批量插入（[insertAudits]，格式同 audit.Write，重算档号时一次写很多条）。

// readSnapshot 在一个只读的 REPEATABLE READ 事务里执行 fn（列表、详情的几条查询看到同一个快照）。
func readSnapshot(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// projectSettings 读未删除项目的 settings（跨模块的读）；项目不存在或已删除为 nil。
func projectSettings(ctx context.Context, q db.Querier, projectID uuid.UUID) ([]byte, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT settings FROM projects WHERE id = $1 AND deleted_at IS NULL`, projectID).Scan(&raw)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if raw == nil && err == nil {
		raw = []byte("{}")
	}
	return raw, err
}

// auditEntry 是批量写的一条审计（项目、操作人由 insertAudits 统一给）。
type auditEntry struct {
	TargetType string
	TargetID   uuid.UUID
	Action     string
	Detail     any
}

// insertAudits 一条语句写多条审计（跨模块的写：格式和 audit.Write 一样）。
func insertAudits(ctx context.Context, q db.Querier, projectID, operatorID uuid.UUID, entries []auditEntry) error {
	if len(entries) == 0 {
		return nil
	}
	types := make([]string, len(entries))
	ids := make([]uuid.UUID, len(entries))
	actions := make([]string, len(entries))
	details := make([]any, len(entries))
	for i, e := range entries {
		types[i], ids[i], actions[i] = e.TargetType, e.TargetID, e.Action
		details[i] = e.Detail
		if details[i] == nil {
			details[i] = map[string]any{}
		}
	}
	_, err := q.Exec(ctx, `INSERT INTO audit_logs (project_id, operator_id, target_type, target_id, action, detail)
SELECT $1, $2, t.target_type, t.target_id, t.action, t.detail
FROM unnest($3::text[], $4::uuid[], $5::text[], $6::jsonb[]) AS t(target_type, target_id, action, detail)`,
		projectID, operatorID, types, ids, actions, details)
	return err
}

// ---------------------------------------------------------------------------
// 归类节点
// ---------------------------------------------------------------------------

// catRow 是 archive_categories 的一行。
type catRow struct {
	ID          uuid.UUID
	ProjectID   uuid.UUID
	ParentID    *uuid.UUID
	PathCode    string
	LastChildNo int
	Type        string
	Code        string
	Name        string
	SortNo      int
	CreatedAt   time.Time
	Deleted     bool
}

const catColumns = `id, project_id, parent_id, path_code, last_child_no, category_type, category_code, category_name, sort_no,
  created_at, deleted_at IS NOT NULL`

func scanCat(sc interface{ Scan(...any) error }) (*catRow, error) {
	var c catRow
	if err := sc.Scan(&c.ID, &c.ProjectID, &c.ParentID, &c.PathCode, &c.LastChildNo, &c.Type, &c.Code, &c.Name, &c.SortNo,
		&c.CreatedAt, &c.Deleted); err != nil {
		return nil, err
	}
	return &c, nil
}

// getCategory 读项目里未删除的节点；不存在、已删除、不在这个项目里为 nil。
func getCategory(ctx context.Context, q db.Querier, projectID, id uuid.UUID) (*catRow, error) {
	c, err := scanCat(q.QueryRow(ctx, `SELECT `+catColumns+` FROM archive_categories
WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL`, id, projectID))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return c, err
}

// rootCategory 读项目的归类根。
func rootCategory(ctx context.Context, q db.Querier, projectID uuid.UUID) (*catRow, error) {
	c, err := scanCat(q.QueryRow(ctx, `SELECT `+catColumns+` FROM archive_categories
WHERE project_id = $1 AND parent_id IS NULL`, projectID))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return c, err
}

// categoryProject 是节点所在的项目（节点未删除）；没有为 uuid.Nil。
func categoryProject(ctx context.Context, q db.Querier, id uuid.UUID) (uuid.UUID, error) {
	var pid uuid.UUID
	err := q.QueryRow(ctx, `SELECT project_id FROM archive_categories WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&pid)
	if db.IsNoRows(err) {
		return uuid.Nil, nil
	}
	return pid, err
}

// lockCategories 按 id 升序锁住这些节点行（FOR NO KEY UPDATE，含已删除的，只锁这个项目里的）。
func lockCategories(ctx context.Context, q db.Querier, projectID uuid.UUID, ids ...uuid.UUID) (map[uuid.UUID]*catRow, error) {
	rows, err := q.Query(ctx, `SELECT `+catColumns+` FROM archive_categories
WHERE id = ANY($1) AND project_id = $2 ORDER BY id FOR NO KEY UPDATE`, ids, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]*catRow{}
	for rows.Next() {
		c, err := scanCat(rows)
		if err != nil {
			return nil, err
		}
		out[c.ID] = c
	}
	return out, rows.Err()
}

// categoryChain 是 path 节点的祖先链（含它自己），从根往下。
func categoryChain(ctx context.Context, q db.Querier, projectID uuid.UUID, path string) (chain, error) {
	rows, err := q.Query(ctx, `SELECT path_code, category_type, category_code, category_name FROM archive_categories
WHERE project_id = $1 AND path_code = ANY($2) ORDER BY path_code`, projectID, pathPrefixes(path))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out chain
	for rows.Next() {
		var n chainNode
		if err := rows.Scan(&n.PathCode, &n.Type, &n.Code, &n.Name); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// chainNodesForSubtree 是子树里未删除的节点和子树根的祖先（path_code → 节点），重算档号时拼祖先链。
func chainNodesForSubtree(ctx context.Context, q db.Querier, projectID uuid.UUID, path string) (map[string]chainNode, error) {
	rows, err := q.Query(ctx, `SELECT path_code, category_type, category_code, category_name FROM archive_categories
WHERE project_id = $1 AND deleted_at IS NULL AND ((path_code >= $2 AND path_code < $3) OR path_code = ANY($4))`,
		projectID, path, subtreeUpper(path), pathPrefixes(path))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]chainNode{}
	for rows.Next() {
		var n chainNode
		if err := rows.Scan(&n.PathCode, &n.Type, &n.Code, &n.Name); err != nil {
			return nil, err
		}
		out[n.PathCode] = n
	}
	return out, rows.Err()
}

// Category 是归类树的一个节点（接口的响应）。
type Category struct {
	ID           uuid.UUID  `json:"id"`
	ProjectID    uuid.UUID  `json:"project_id"`
	ParentID     *uuid.UUID `json:"parent_id"`
	PathCode     string     `json:"path_code"`
	CategoryType string     `json:"category_type"`
	CategoryCode string     `json:"category_code"`
	CategoryName string     `json:"category_name"`
	SortNo       int        `json:"sort_no"`
	// ChildCount 是未删除的直接子节点数。
	ChildCount int `json:"child_count"`
	// ItemCount 是子树里（含自己）未删除的条目数。
	ItemCount int       `json:"item_count"`
	CreatedAt time.Time `json:"created_at"`
}

// categoryViewSelect 带子节点数和子树条目数；条件里用别名 c。
const categoryViewSelect = `SELECT c.id, c.project_id, c.parent_id, c.path_code, c.category_type, c.category_code,
  c.category_name, c.sort_no,
  (SELECT count(*) FROM archive_categories k WHERE k.parent_id = c.id AND k.deleted_at IS NULL),
  (SELECT count(*) FROM archive_items i JOIN archive_categories d ON d.id = i.category_id
   WHERE d.project_id = c.project_id AND d.path_code >= c.path_code
     AND d.path_code < left(c.path_code, -1) || '/' AND i.deleted_at IS NULL),
  c.created_at
FROM archive_categories c`

func scanCategory(sc interface{ Scan(...any) error }) (Category, error) {
	var c Category
	err := sc.Scan(&c.ID, &c.ProjectID, &c.ParentID, &c.PathCode, &c.CategoryType, &c.CategoryCode, &c.CategoryName, &c.SortNo,
		&c.ChildCount, &c.ItemCount, &c.CreatedAt)
	return c, err
}

// categoryView 读一个节点（带计数，不管是否删除）。
func categoryView(ctx context.Context, q db.Querier, id uuid.UUID) (Category, error) {
	return scanCategory(q.QueryRow(ctx, categoryViewSelect+` WHERE c.id = $1`, id))
}

// childCategories 是 parentID 的未删除直接子节点（带计数），按 sort_no、编码排。
func childCategories(ctx context.Context, q db.Querier, parentID uuid.UUID) ([]Category, error) {
	rows, err := q.Query(ctx, categoryViewSelect+` WHERE c.parent_id = $1 AND c.deleted_at IS NULL
ORDER BY c.sort_no, c.category_code, c.id`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// allocChildPath 在父节点上原子地领一个子编码（父节点行要已锁住）。超过 99999 时 ok 为 false。
func allocChildPath(ctx context.Context, q db.Querier, parentID uuid.UUID) (path string, childNo int, ok bool, err error) {
	var parentPath string
	if err := q.QueryRow(ctx, `UPDATE archive_categories SET last_child_no = last_child_no + 1 WHERE id = $1
RETURNING path_code, last_child_no`, parentID).Scan(&parentPath, &childNo); err != nil {
		return "", 0, false, err
	}
	if childNo > maxChildNo {
		return "", childNo, false, nil
	}
	return parentPath + pad5(childNo) + ".", childNo, true, nil
}

const maxChildNo = 99999

func pad5(n int) string {
	b := []byte("00000")
	for i := 4; i >= 0 && n > 0; i-- {
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b)
}

// siblingWithCode 是 parentID 下编码为 code 的未删除节点（除了 except）；没有为 uuid.Nil。区分大小写。
func siblingWithCode(ctx context.Context, q db.Querier, parentID uuid.UUID, code string, except uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM archive_categories
WHERE parent_id = $1 AND category_code = $2 AND deleted_at IS NULL AND id <> $3 LIMIT 1`, parentID, code, except).Scan(&id)
	if db.IsNoRows(err) {
		return uuid.Nil, nil
	}
	return id, err
}

func insertCategory(ctx context.Context, q db.Querier, c *catRow) error {
	_, err := q.Exec(ctx, `INSERT INTO archive_categories
  (id, project_id, parent_id, path_code, category_type, category_code, category_name, sort_no)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, c.ID, c.ProjectID, c.ParentID, c.PathCode, c.Type, c.Code, c.Name, c.SortNo)
	return err
}

// moveSubtreePaths 给 oldPath 子树（含已删除的节点）换前缀。
func moveSubtreePaths(ctx context.Context, q db.Querier, projectID uuid.UUID, oldPath, newPath string) error {
	_, err := q.Exec(ctx, `UPDATE archive_categories SET path_code = $3 || substr(path_code, length($2) + 1)
WHERE project_id = $1 AND path_code >= $2 AND path_code < $4`, projectID, oldPath, newPath, subtreeUpper(oldPath))
	return err
}

// updateCategoryRow 改名称、编码、类型、父节点、排序（一条语句，唯一索引按语句检查）。
func updateCategoryRow(ctx context.Context, q db.Querier, c *catRow) error {
	_, err := q.Exec(ctx, `UPDATE archive_categories
SET category_name = $2, category_code = $3, category_type = $4, parent_id = $5, sort_no = $6 WHERE id = $1`,
		c.ID, c.Name, c.Code, c.Type, c.ParentID, c.SortNo)
	return err
}

// subtreeItemExample 是子树里一个未删除条目的例子。
type subtreeItemExample struct {
	ItemNo       int
	Title        string
	CategoryName string
}

// subtreeItems 数子树里未删除的条目（status 为空时不分状态），给一个例子（按 path_code、件号最前的）。
func subtreeItems(ctx context.Context, q db.Querier, projectID uuid.UUID, path, status string) (int, *subtreeItemExample, error) {
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM archive_items i JOIN archive_categories c ON c.id = i.category_id
WHERE c.project_id = $1 AND c.path_code >= $2 AND c.path_code < $3 AND i.deleted_at IS NULL
  AND ($4 = '' OR i.status = $4)`, projectID, path, subtreeUpper(path), status).Scan(&n); err != nil {
		return 0, nil, err
	}
	if n == 0 {
		return 0, nil, nil
	}
	var ex subtreeItemExample
	err := q.QueryRow(ctx, `SELECT i.item_no, i.title, c.category_name FROM archive_items i
JOIN archive_categories c ON c.id = i.category_id
WHERE c.project_id = $1 AND c.path_code >= $2 AND c.path_code < $3 AND i.deleted_at IS NULL
  AND ($4 = '' OR i.status = $4)
ORDER BY c.path_code, i.item_no, i.id LIMIT 1`, projectID, path, subtreeUpper(path), status).
		Scan(&ex.ItemNo, &ex.Title, &ex.CategoryName)
	return n, &ex, err
}

// softDeleteSubtree 软删除 path 子树里未删除的节点，返回删掉的 id。
func softDeleteSubtree(ctx context.Context, q db.Querier, projectID uuid.UUID, path string) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `UPDATE archive_categories SET deleted_at = now()
WHERE project_id = $1 AND path_code >= $2 AND path_code < $3 AND deleted_at IS NULL RETURNING id`,
		projectID, path, subtreeUpper(path))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// ---------------------------------------------------------------------------
// 档号
// ---------------------------------------------------------------------------

// renumberRow 是重算档号的一个候选条目。
type renumberRow struct {
	ID           uuid.UUID
	ItemNo       int
	ArchiveNo    *string
	CategoryPath string
}

// subtreeItemsForRenumber 是 path 子树里未删除、未锁定的条目（按 id），以及已锁定的条目数。
func subtreeItemsForRenumber(ctx context.Context, q db.Querier, projectID uuid.UUID, path string) ([]renumberRow, int, error) {
	rows, err := q.Query(ctx, `SELECT i.id, i.item_no, i.archive_no, c.path_code, i.status = 'locked'
FROM archive_items i JOIN archive_categories c ON c.id = i.category_id
WHERE c.project_id = $1 AND c.path_code >= $2 AND c.path_code < $3 AND i.deleted_at IS NULL
ORDER BY i.id`, projectID, path, subtreeUpper(path))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []renumberRow
	locked := 0
	for rows.Next() {
		var r renumberRow
		var isLocked bool
		if err := rows.Scan(&r.ID, &r.ItemNo, &r.ArchiveNo, &r.CategoryPath, &isLocked); err != nil {
			return nil, 0, err
		}
		if isLocked {
			locked++
			continue
		}
		out = append(out, r)
	}
	return out, locked, rows.Err()
}

// archiveNoHolders 是项目里占着这些档号的未删除条目（档号 → 条目）。
func archiveNoHolders(ctx context.Context, q db.Querier, projectID uuid.UUID, nos []string) (map[string]uuid.UUID, error) {
	out := map[string]uuid.UUID{}
	if len(nos) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT archive_no, id FROM archive_items
WHERE project_id = $1 AND deleted_at IS NULL AND archive_no = ANY($2)`, projectID, nos)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var no string
		var id uuid.UUID
		if err := rows.Scan(&no, &id); err != nil {
			return nil, err
		}
		out[no] = id
	}
	return out, rows.Err()
}

// holder 是占着某个档号的条目。
type holder struct {
	ID         uuid.UUID
	CategoryID uuid.UUID
	ItemNo     int
	Title      string
}

// archiveNoHolder 是项目里占着档号 no 的未删除条目（除了 except）；没有为 nil。
func archiveNoHolder(ctx context.Context, q db.Querier, projectID uuid.UUID, no string, except uuid.UUID) (*holder, error) {
	var h holder
	err := q.QueryRow(ctx, `SELECT id, category_id, item_no, title FROM archive_items
WHERE project_id = $1 AND archive_no = $2 AND deleted_at IS NULL AND id <> $3`, projectID, no, except).
		Scan(&h.ID, &h.CategoryID, &h.ItemNo, &h.Title)
	if db.IsNoRows(err) {
		return nil, nil
	}
	return &h, err
}

// writeArchiveNos 写重算的结果：先把 reused 置空，再给 ids 写 nos（row_version + 1）。
func writeArchiveNos(ctx context.Context, q db.Querier, operatorID uuid.UUID, reused, ids []uuid.UUID, nos []*string) error {
	if len(reused) > 0 {
		if _, err := q.Exec(ctx, `UPDATE archive_items SET archive_no = NULL WHERE id = ANY($1)`, reused); err != nil {
			return err
		}
	}
	_, err := q.Exec(ctx, `UPDATE archive_items a SET archive_no = t.archive_no, row_version = a.row_version + 1,
  updated_by = $3, updated_at = now()
FROM unnest($1::uuid[], $2::text[]) AS t(id, archive_no) WHERE a.id = t.id`, ids, nos, operatorID)
	return err
}
