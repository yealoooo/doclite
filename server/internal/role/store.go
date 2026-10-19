package role

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/db"
)

// role 的 SQL：roles、role_menus。
// 跨模块的只读查询：user_roles（角色的用户数、用户的角色和菜单，给防提权判断用）、menus（菜单排序）。

// builtinOrder 是内置角色在列表里的顺序（requirements 第 4 节的表）。
var builtinOrder = []string{"admin", "manager", "scanner", "reviewer", "cataloger"}

// Role 是 `/api/system/roles` 的一个角色。
type Role struct {
	ID          uuid.UUID `json:"id"`
	RoleCode    string    `json:"role_code"`
	RoleName    string    `json:"role_name"`
	Description *string   `json:"description"`
	// IsBuiltin：内置角色不能删除；admin 的菜单不能改。
	IsBuiltin bool `json:"is_builtin"`
	// MenuCodes 按菜单的 sort_no 排序。
	MenuCodes []string `json:"menu_codes"`
	// UserCount 是拥有这个角色的用户数（含已停用的）。
	UserCount int64     `json:"user_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const roleSelect = `SELECT r.id, r.role_code, r.role_name, r.description, r.is_builtin, r.created_at, r.updated_at,
  ARRAY(SELECT rm.menu_code FROM role_menus rm JOIN menus m ON m.menu_code = rm.menu_code
        WHERE rm.role_id = r.id ORDER BY m.sort_no, m.menu_code) AS menu_codes,
  (SELECT count(*) FROM user_roles ur WHERE ur.role_id = r.id) AS user_count
FROM roles r`

func scanRole(row pgx.Row) (*Role, error) {
	var r Role
	if err := row.Scan(&r.ID, &r.RoleCode, &r.RoleName, &r.Description, &r.IsBuiltin, &r.CreatedAt, &r.UpdatedAt,
		&r.MenuCodes, &r.UserCount); err != nil {
		return nil, err
	}
	if r.MenuCodes == nil {
		r.MenuCodes = []string{}
	}
	return &r, nil
}

// listRoles：内置角色按固定顺序在前，自建角色按创建时间在后。
func listRoles(ctx context.Context, q db.Querier) ([]Role, error) {
	rows, err := q.Query(ctx, roleSelect+`
ORDER BY array_position($1::text[], r.role_code) NULLS LAST, r.created_at, r.id`, builtinOrder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func getRole(ctx context.Context, q db.Querier, id uuid.UUID) (*Role, error) {
	r, err := scanRole(q.QueryRow(ctx, roleSelect+` WHERE r.id = $1`, id))
	if db.IsNoRows(err) {
		return nil, nil
	}
	return r, err
}

type roleRow struct {
	ID          uuid.UUID
	RoleCode    string
	RoleName    string
	Description *string
	IsBuiltin   bool
}

// lockRole 锁住并读出一个角色（FOR NO KEY UPDATE：不挡别的事务写 user_roles 时的外键检查）。不存在时为 nil。
func lockRole(ctx context.Context, q db.Querier, id uuid.UUID) (*roleRow, error) {
	var r roleRow
	err := q.QueryRow(ctx, `SELECT id, role_code, role_name, description, is_builtin FROM roles WHERE id = $1 FOR NO KEY UPDATE`, id).
		Scan(&r.ID, &r.RoleCode, &r.RoleName, &r.Description, &r.IsBuiltin)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func insertRole(ctx context.Context, q db.Querier, id uuid.UUID, code, name string, desc *string) error {
	_, err := q.Exec(ctx, `INSERT INTO roles (id, role_code, role_name, description, is_builtin) VALUES ($1, $2, $3, $4, false)`,
		id, code, name, desc)
	return err
}

func updateRole(ctx context.Context, q db.Querier, id uuid.UUID, name string, desc *string) error {
	_, err := q.Exec(ctx, `UPDATE roles SET role_name = $2, description = $3, updated_at = now() WHERE id = $1`, id, name, desc)
	return err
}

func touchRole(ctx context.Context, q db.Querier, id uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE roles SET updated_at = now() WHERE id = $1`, id)
	return err
}

// deleteRole 删除角色；user_roles、role_menus、project_roles 由外键级联删除。
func deleteRole(ctx context.Context, q db.Querier, id uuid.UUID) error {
	_, err := q.Exec(ctx, `DELETE FROM roles WHERE id = $1`, id)
	return err
}

// menuCodesOf 是角色现在的菜单，按菜单 sort_no 排序。
func menuCodesOf(ctx context.Context, q db.Querier, roleID uuid.UUID) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT rm.menu_code FROM role_menus rm JOIN menus m ON m.menu_code = rm.menu_code
WHERE rm.role_id = $1 ORDER BY m.sort_no, m.menu_code`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func replaceMenus(ctx context.Context, q db.Querier, roleID uuid.UUID, codes []string) error {
	if _, err := q.Exec(ctx, `DELETE FROM role_menus WHERE role_id = $1`, roleID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO role_menus (role_id, menu_code) SELECT $1, unnest($2::text[])`, roleID, codes)
	return err
}

// userIDsOfRole 是拥有这个角色的用户（删除角色时记进审计）。
func userIDsOfRole(ctx context.Context, q db.Querier, roleID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT user_id FROM user_roles WHERE role_id = $1 ORDER BY user_id`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// lockPermissionChanges 是"权限变更锁"：锁住 admin 角色那一行（FOR NO KEY UPDATE）。
//
// users、roles、user_roles、role_menus 的所有写事务第一步都拿它（user 模块的新建、修改、停用、启用、重置密码、
// 改角色；role 模块的新建、修改、删除、改菜单），于是：
//   - 事务里读到的操作人权限、对象状态就是提交时的状态，防提权检查在并发下绕不过去；
//   - "至少保留一个启用的管理员"不会被两个同时进行的停用绕过；
//   - 这些事务之间不会死锁：拿到锁之前不持有任何行锁，拿到之后同一时间只有一个在跑。
//
// 登录（锁一个用户行、写会话）、会话的 touch 和撤销、初始管理员（LOCK TABLE users）不在这把锁上排队。
func lockPermissionChanges(ctx context.Context, q db.Querier) error {
	var id uuid.UUID
	return q.QueryRow(ctx, `SELECT id FROM roles WHERE role_code = $1 FOR NO KEY UPDATE`, auth.AdminRoleCode).Scan(&id)
}

const grantSelect = `SELECT r.id, r.role_code, r.role_name,
  ARRAY(SELECT rm.menu_code FROM role_menus rm WHERE rm.role_id = r.id ORDER BY rm.menu_code)`

func scanGrants(ctx context.Context, q db.Querier, sql string, args ...any) ([]Grant, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.RoleID, &g.RoleCode, &g.RoleName, &g.MenuCodes); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// grantsOfUser 是用户现在的角色和每个角色的菜单，按 role_code 排序。
func grantsOfUser(ctx context.Context, q db.Querier, userID uuid.UUID) ([]Grant, error) {
	return scanGrants(ctx, q, grantSelect+`
FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = $1 ORDER BY r.role_code`, userID)
}

// grantsByIDs 是一组角色和它们的菜单，按 role_code 排序。
func grantsByIDs(ctx context.Context, q db.Querier, ids []uuid.UUID) ([]Grant, error) {
	return scanGrants(ctx, q, grantSelect+` FROM roles r WHERE r.id = ANY($1) ORDER BY r.role_code`, ids)
}
