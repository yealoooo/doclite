package project

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
)

// project 的 SQL：projects、project_members、project_roles。
//
// 跨模块的读：folders（根文件夹）、archive_categories（归类根）、folder_files（项目里还有没有文件）、
// users、user_roles、roles（授权、选人候选、是不是 admin）。
// 跨模块的写（例外，必须和项目在同一个事务里，requirements 5.3）：建项目时插入根文件夹（folders）和归类根
// （archive_categories），项目改名时一起改它们的名称。

// rootPathCode 是根文件夹和归类根的 path_code（硬性规则 8）。
const rootPathCode = "00001."

// builtinRoleOrder 是内置角色在列表里的顺序（和 role 模块一致）。
var builtinRoleOrder = []string{"admin", "manager", "scanner", "reviewer", "cataloger"}

// MyProject 是 `GET /api/projects` 的一项。
type MyProject struct {
	ID          uuid.UUID `json:"id"`
	ProjectCode string    `json:"project_code"`
	ProjectName string    `json:"project_name"`
	Status      string    `json:"status"`
	// RootFolderID 是根文件夹：文件目录、热文件夹从这里开始。
	RootFolderID uuid.UUID `json:"root_folder_id"`
	// ScanHint 是扫描参数建议（M11-5，扫描页显示）。
	ScanHint ScanHint `json:"scan_hint"`
}

// listMyProjects：用户能进的未删除项目（含已归档的），按编码排序。
// 能进 = admin，或在 project_members 里，或拥有 project_roles 里的某个角色（requirements 第 4 节"项目授权"）。
func listMyProjects(ctx context.Context, q db.Querier, userID uuid.UUID) ([]MyProject, error) {
	rows, err := q.Query(ctx, `SELECT p.id, p.project_code, p.project_name, p.status, f.id, p.settings
FROM projects p
JOIN folders f ON f.project_id = p.id AND f.parent_id IS NULL
WHERE p.deleted_at IS NULL
  AND `+auth.ProjectAccessSQL("$1", "p.id")+`
ORDER BY p.project_code, p.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MyProject{}
	for rows.Next() {
		var p MyProject
		var settings []byte
		if err := rows.Scan(&p.ID, &p.ProjectCode, &p.ProjectName, &p.Status, &p.RootFolderID, &settings); err != nil {
			return nil, err
		}
		p.ScanHint = ParseSettings(settings).ScanHint
		out = append(out, p)
	}
	return out, rows.Err()
}

// Item 是 `GET /api/system/projects` 列表里的一个项目。
type Item struct {
	ID           uuid.UUID `json:"id"`
	ProjectCode  string    `json:"project_code"`
	ProjectName  string    `json:"project_name"`
	Description  *string   `json:"description"`
	Status       string    `json:"status"`
	RootFolderID uuid.UUID `json:"root_folder_id"`
	// MemberCount 是个人授权的人数（含已停用的用户）。
	MemberCount int `json:"member_count"`
	// RoleCount 是授权的角色数。
	RoleCount int       `json:"role_count"`
	CreatedBy uuid.UUID `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 是项目的版本（PATCH 的 expected_updated_at 原样传回它），微秒精度。
	UpdatedAt time.Time `json:"updated_at"`
}

// listProjects：未删除的项目，新建的在前。keyword 匹配编码或名称（不区分大小写），status 为空不筛选。
func listProjects(ctx context.Context, q db.Querier, keyword, status string, page httpx.PageRequest) ([]Item, int, error) {
	var pattern, st *string
	if keyword != "" {
		p := db.ContainsPattern(keyword)
		pattern = &p
	}
	if status != "" {
		st = &status
	}
	const filter = `p.deleted_at IS NULL AND ($1::text IS NULL OR p.project_code ILIKE $1 OR p.project_name ILIKE $1)
  AND ($2::text IS NULL OR p.status = $2)`
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM projects p WHERE `+filter, pattern, st).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, `SELECT p.id, p.project_code, p.project_name, p.description, p.status, f.id,
  (SELECT count(*) FROM project_members m WHERE m.project_id = p.id),
  (SELECT count(*) FROM project_roles r WHERE r.project_id = p.id),
  p.created_by, p.created_at, p.updated_at
FROM projects p JOIN folders f ON f.project_id = p.id AND f.parent_id IS NULL
WHERE `+filter+`
ORDER BY p.created_at DESC, p.id DESC LIMIT $3 OFFSET $4`, pattern, st, page.Limit(), page.Offset())
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.ProjectCode, &it.ProjectName, &it.Description, &it.Status, &it.RootFolderID,
			&it.MemberCount, &it.RoleCount, &it.CreatedBy, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// projectRow 是 projects 的一行（未删除），加上根文件夹、归类根。
type projectRow struct {
	ID             uuid.UUID
	ProjectCode    string
	ProjectName    string
	Description    *string
	Settings       []byte
	Status         string
	CreatedBy      uuid.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
	RootFolderID   uuid.UUID
	CategoryRootID uuid.UUID
}

const projectSelect = `SELECT p.id, p.project_code, p.project_name, p.description, p.settings, p.status, p.created_by,
  p.created_at, p.updated_at,
  (SELECT f.id FROM folders f WHERE f.project_id = p.id AND f.parent_id IS NULL),
  (SELECT c.id FROM archive_categories c WHERE c.project_id = p.id AND c.parent_id IS NULL)
FROM projects p`

func scanProject(sc pgx.Row) (*projectRow, error) {
	var p projectRow
	if err := sc.Scan(&p.ID, &p.ProjectCode, &p.ProjectName, &p.Description, &p.Settings, &p.Status, &p.CreatedBy,
		&p.CreatedAt, &p.UpdatedAt, &p.RootFolderID, &p.CategoryRootID); err != nil {
		return nil, err
	}
	return &p, nil
}

// getProject 读未删除的项目；不存在或已删除为 nil。
func getProject(ctx context.Context, q db.Querier, id uuid.UUID) (*projectRow, error) {
	p, err := scanProject(q.QueryRow(ctx, projectSelect+` WHERE p.id = $1 AND p.deleted_at IS NULL`, id))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return p, err
}

// lockProject 锁住未删除的项目行（FOR NO KEY UPDATE）；不存在或已删除为 nil。
func lockProject(ctx context.Context, q db.Querier, id uuid.UUID) (*projectRow, error) {
	p, err := scanProject(q.QueryRow(ctx, projectSelect+` WHERE p.id = $1 AND p.deleted_at IS NULL FOR NO KEY UPDATE OF p`, id))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return p, err
}

// lockCategoryRoot 锁住项目的归类根（改名前，按加锁顺序在文件树锁之前）。
func lockCategoryRoot(ctx context.Context, q db.Querier, projectID uuid.UUID) error {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM archive_categories WHERE project_id = $1 AND parent_id IS NULL FOR NO KEY UPDATE`,
		projectID).Scan(&id)
	if db.IsNoRows(err) {
		return nil
	}
	return err
}

func insertProject(ctx context.Context, q db.Querier, p *projectRow) error {
	if _, err := q.Exec(ctx, `INSERT INTO projects (id, project_code, project_name, description, settings, status, created_by)
VALUES ($1, $2, $3, $4, '{}', 'active', $5)`, p.ID, p.ProjectCode, p.ProjectName, p.Description, p.CreatedBy); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `INSERT INTO folders (id, project_id, parent_id, folder_name, path_code, created_by)
VALUES ($1, $2, NULL, $3, $4, $5)`, p.RootFolderID, p.ID, p.ProjectName, rootPathCode, p.CreatedBy); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO archive_categories (id, project_id, parent_id, path_code, category_type, category_code, category_name)
VALUES ($1, $2, NULL, $3, 'root', $4, $5)`, p.CategoryRootID, p.ID, rootPathCode, p.ProjectCode, p.ProjectName)
	return err
}

// updateProject 写回名称、说明、状态、settings，updated_at 写成 max(now, 旧值 + 1µs)（严格递增），返回新的 updated_at。
func updateProject(ctx context.Context, q db.Querier, p *projectRow) (time.Time, error) {
	var at time.Time
	err := q.QueryRow(ctx, `UPDATE projects SET project_name = $2, description = $3, status = $4, settings = $5,
  updated_at = GREATEST(now(), updated_at + interval '1 microsecond')
WHERE id = $1 RETURNING updated_at`, p.ID, p.ProjectName, p.Description, p.Status, json.RawMessage(p.Settings)).Scan(&at)
	return at, err
}

// touchProject 只换版本（改授权时）。
func touchProject(ctx context.Context, q db.Querier, id uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE projects SET updated_at = GREATEST(now(), updated_at + interval '1 microsecond') WHERE id = $1`, id)
	return err
}

// renameRoots 项目改名时一起改根文件夹名和归类根的名称。
func renameRoots(ctx context.Context, q db.Querier, projectID uuid.UUID, name string) error {
	if _, err := q.Exec(ctx, `UPDATE folders SET folder_name = $2, updated_at = now() WHERE project_id = $1 AND parent_id IS NULL`,
		projectID, name); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `UPDATE archive_categories SET category_name = $2 WHERE project_id = $1 AND parent_id IS NULL`,
		projectID, name)
	return err
}

func hasLiveFiles(ctx context.Context, q db.Querier, projectID uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM folder_files WHERE project_id = $1 AND deleted_at IS NULL)`, projectID).Scan(&ok)
	return ok, err
}

func softDeleteProject(ctx context.Context, q db.Querier, id uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE projects SET deleted_at = now() WHERE id = $1`, id)
	return err
}

// AccessUser 是授权里的一个人。已停用的用户仍留在授权里。
type AccessUser struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
}

// AccessRole 是授权里的一个角色。
type AccessRole struct {
	ID       uuid.UUID `json:"id"`
	RoleCode string    `json:"role_code"`
	RoleName string    `json:"role_name"`
}

// Access 是项目授权：个人授权按登录名排序，角色授权内置角色在前。
type Access struct {
	Users []AccessUser `json:"users"`
	Roles []AccessRole `json:"roles"`
}

// roleOrder：内置角色按固定顺序在前，自建角色按创建时间（param 是 builtinRoleOrder 的参数位置，如 "$2"）。
func roleOrder(param string) string {
	return `ORDER BY array_position(` + param + `::text[], r.role_code) NULLS LAST, r.created_at, r.id`
}

func getAccess(ctx context.Context, q db.Querier, projectID uuid.UUID) (*Access, error) {
	a := &Access{Users: []AccessUser{}, Roles: []AccessRole{}}
	rows, err := q.Query(ctx, `SELECT u.id, u.username, u.display_name, u.status
FROM project_members m JOIN users u ON u.id = m.user_id
WHERE m.project_id = $1 ORDER BY lower(u.username), u.id`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u AccessUser
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Status); err != nil {
			rows.Close()
			return nil, err
		}
		a.Users = append(a.Users, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT r.id, r.role_code, r.role_name
FROM project_roles pr JOIN roles r ON r.id = pr.role_id
WHERE pr.project_id = $1 `+roleOrder("$2"), projectID, builtinRoleOrder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r AccessRole
		if err := rows.Scan(&r.ID, &r.RoleCode, &r.RoleName); err != nil {
			return nil, err
		}
		a.Roles = append(a.Roles, r)
	}
	return a, rows.Err()
}

// existingIDs 是 ids 里在表 table（users 或 roles）中存在的。
func existingIDs(ctx context.Context, q db.Querier, table string, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT id FROM `+table+` WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// replaceAccess 整体替换授权：删掉不在新集合里的，补上缺的（已有的保留授权时间）。
func replaceAccess(ctx context.Context, q db.Querier, projectID uuid.UUID, userIDs, roleIDs []uuid.UUID) error {
	if _, err := q.Exec(ctx, `DELETE FROM project_members WHERE project_id = $1 AND NOT (user_id = ANY($2))`, projectID, userIDs); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `INSERT INTO project_members (project_id, user_id)
SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, projectID, userIDs); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `DELETE FROM project_roles WHERE project_id = $1 AND NOT (role_id = ANY($2))`, projectID, roleIDs); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO project_roles (project_id, role_id)
SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, projectID, roleIDs)
	return err
}

// CandidateUser 是选人候选里的一个用户。
type CandidateUser struct {
	AccessUser
	// RoleIDs 是用户拥有的角色 id（按 id 排序）。
	RoleIDs []uuid.UUID `json:"role_ids"`
}

// Candidates 是授权时的选人候选：全部用户（按登录名排序）和全部角色（内置角色在前）。
type Candidates struct {
	Users []CandidateUser `json:"users"`
	Roles []AccessRole    `json:"roles"`
}

func accessCandidates(ctx context.Context, q db.Querier) (*Candidates, error) {
	c := &Candidates{Users: []CandidateUser{}, Roles: []AccessRole{}}
	rows, err := q.Query(ctx, `SELECT u.id, u.username, u.display_name, u.status,
  ARRAY(SELECT ur.role_id FROM user_roles ur WHERE ur.user_id = u.id ORDER BY ur.role_id)
FROM users u ORDER BY lower(u.username), u.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u CandidateUser
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Status, &u.RoleIDs); err != nil {
			rows.Close()
			return nil, err
		}
		if u.RoleIDs == nil {
			u.RoleIDs = []uuid.UUID{}
		}
		c.Users = append(c.Users, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT r.id, r.role_code, r.role_name FROM roles r `+roleOrder("$1"), builtinRoleOrder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r AccessRole
		if err := rows.Scan(&r.ID, &r.RoleCode, &r.RoleName); err != nil {
			return nil, err
		}
		c.Roles = append(c.Roles, r)
	}
	return c, rows.Err()
}
