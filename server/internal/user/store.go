package user

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
)

// user 的 SQL：users、user_roles。跨模块的只读查询：roles（用户的角色名称）。

// User 是 `/api/system/users` 的一个用户。
type User struct {
	ID          uuid.UUID  `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Status      string     `json:"status"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	// Roles 按 role_code 排序。
	Roles []auth.RoleBrief `json:"roles"`
}

const userColumns = `u.id, u.username, u.display_name, u.status, u.last_login_at, u.created_at, u.updated_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Status, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	u.Roles = []auth.RoleBrief{}
	return &u, nil
}

// listUsers：登录名或姓名包含 keyword（不区分大小写；空表示不筛选），按登录名排序（不区分大小写）。
func listUsers(ctx context.Context, q db.Querier, keyword string, page httpx.PageRequest) ([]User, int, error) {
	var pattern *string
	if keyword != "" {
		p := db.ContainsPattern(keyword)
		pattern = &p
	}
	const filter = `($1::text IS NULL OR u.username ILIKE $1 OR u.display_name ILIKE $1)`
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM users u WHERE `+filter, pattern).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, `SELECT `+userColumns+` FROM users u WHERE `+filter+`
ORDER BY lower(u.username), u.id LIMIT $2 OFFSET $3`, pattern, page.Limit(), page.Offset())
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		users = append(users, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := fillRoles(ctx, q, users); err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

// fillRoles 一次查出这些用户的角色（按 role_code 排序）。
func fillRoles(ctx context.Context, q db.Querier, users []User) error {
	if len(users) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(users))
	idx := map[uuid.UUID]int{}
	for i, u := range users {
		ids[i] = u.ID
		idx[u.ID] = i
	}
	rows, err := q.Query(ctx, `SELECT ur.user_id, r.id, r.role_code, r.role_name
FROM user_roles ur JOIN roles r ON r.id = ur.role_id
WHERE ur.user_id = ANY($1) ORDER BY r.role_code`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uid uuid.UUID
		var r auth.RoleBrief
		if err := rows.Scan(&uid, &r.ID, &r.RoleCode, &r.RoleName); err != nil {
			return err
		}
		i := idx[uid]
		users[i].Roles = append(users[i].Roles, r)
	}
	return rows.Err()
}

func getUser(ctx context.Context, q db.Querier, id uuid.UUID) (*User, error) {
	u, err := scanUser(q.QueryRow(ctx, `SELECT `+userColumns+` FROM users u WHERE u.id = $1`, id))
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	list := []User{*u}
	if err := fillRoles(ctx, q, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

type userRow struct {
	ID          uuid.UUID
	Username    string
	DisplayName string
	Status      string
}

// lockUser 锁住并读出一个用户（FOR NO KEY UPDATE：和登录的同级锁互斥，不挡外键检查）。不存在时为 nil。
func lockUser(ctx context.Context, q db.Querier, id uuid.UUID) (*userRow, error) {
	var u userRow
	err := q.QueryRow(ctx, `SELECT id, username, display_name, status FROM users WHERE id = $1 FOR NO KEY UPDATE`, id).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Status)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func insertUser(ctx context.Context, q db.Querier, id uuid.UUID, username, displayName, passwordHash string) error {
	_, err := q.Exec(ctx, `INSERT INTO users (id, username, display_name, password_hash, status)
VALUES ($1, $2, $3, $4, 'active')`, id, username, displayName, passwordHash)
	return err
}

func updateProfile(ctx context.Context, q db.Querier, id uuid.UUID, username, displayName string) error {
	_, err := q.Exec(ctx, `UPDATE users SET username = $2, display_name = $3, updated_at = now() WHERE id = $1`,
		id, username, displayName)
	return err
}

func updateStatus(ctx context.Context, q db.Querier, id uuid.UUID, status string) error {
	_, err := q.Exec(ctx, `UPDATE users SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	return err
}

func updatePassword(ctx context.Context, q db.Querier, id uuid.UUID, hash string) error {
	_, err := q.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, id, hash)
	return err
}

// replaceRoles 用 roleIDs 整体替换用户的角色。
func replaceRoles(ctx context.Context, q db.Querier, userID uuid.UUID, roleIDs []uuid.UUID) error {
	if _, err := q.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, userID); err != nil {
		return err
	}
	if len(roleIDs) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) SELECT $1, unnest($2::uuid[])`, userID, roleIDs)
	return err
}

// activeAdminRemains：系统里还有没有"启用且有 admin 角色"的用户。
func activeAdminRemains(ctx context.Context, q db.Querier) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (
  SELECT 1 FROM users u JOIN user_roles ur ON ur.user_id = u.id JOIN roles r ON r.id = ur.role_id
  WHERE r.role_code = $1 AND u.status = 'active')`, auth.AdminRoleCode).Scan(&ok)
	return ok, err
}

func anyUserExists(ctx context.Context, q db.Querier) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&ok)
	return ok, err
}

// lockUsersTable 让同时启动的进程只有一个能建初始管理员（和自己互斥，不挡普通读）。
func lockUsersTable(ctx context.Context, q db.Querier) error {
	_, err := q.Exec(ctx, `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE`)
	return err
}

func grantAdminRole(ctx context.Context, q db.Querier, userID uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) SELECT $1, id FROM roles WHERE role_code = $2`,
		userID, auth.AdminRoleCode)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
