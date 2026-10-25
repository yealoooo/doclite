package auth

import (
	"context"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"doclite/server/internal/platform/db"
)

// auth 的 SQL：会话（user_sessions，auth 自己的表）和菜单的查询。
//
// 跨模块的读：users（登录名、密码哈希、状态）、user_roles、roles、role_menus、menus 都是只读查询，
// 例外是登录时在同一个事务里写 users.last_login_at（锁住用户行、核对密码哈希和状态、写会话、记登录时间，
// 必须在一个事务里，见 [Service.Login]）。用户和角色的增删改在 user、role 模块。

type credentials struct {
	UserID       uuid.UUID
	PasswordHash string
	Status       string
}

// findCredentials 按登录名（不区分大小写，走唯一索引 ux_users_username）找用户和密码哈希。
func findCredentials(ctx context.Context, q db.Querier, username string) (*credentials, error) {
	var c credentials
	err := q.QueryRow(ctx,
		`SELECT id, password_hash, status FROM users WHERE lower(username) = lower($1)`, username).
		Scan(&c.UserID, &c.PasswordHash, &c.Status)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// lockUserForLogin 锁住用户行（FOR NO KEY UPDATE：和重置密码、停用互斥，不挡外键检查），读出当前的密码哈希和状态。
func lockUserForLogin(ctx context.Context, q db.Querier, userID uuid.UUID) (hash, status string, found bool, err error) {
	err = q.QueryRow(ctx, `SELECT password_hash, status FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID).
		Scan(&hash, &status)
	if db.IsNoRows(err) {
		return "", "", false, nil
	}
	return hash, status, err == nil, err
}

type newSession struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	TokenHash  []byte
	ClientType string
	IP         *netip.Addr
	UserAgent  *string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

func insertSession(ctx context.Context, q db.Querier, s newSession) error {
	_, err := q.Exec(ctx, `INSERT INTO user_sessions
  (id, user_id, token_hash, client_type, ip_address, user_agent, created_at, last_seen_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8)`,
		s.ID, s.UserID, s.TokenHash, s.ClientType, s.IP, s.UserAgent, s.CreatedAt, s.ExpiresAt)
	return err
}

func setLastLogin(ctx context.Context, q db.Querier, userID uuid.UUID, at time.Time) error {
	_, err := q.Exec(ctx, `UPDATE users SET last_login_at = $2 WHERE id = $1`, userID, at)
	return err
}

type sessionRow struct {
	SessionID   uuid.UUID
	UserID      uuid.UUID
	ClientType  string
	LastSeenAt  time.Time
	ExpiresAt   time.Time
	Revoked     bool
	Username    string
	DisplayName string
	UserStatus  string
	IsAdmin     bool
	MenuCodes   []string
}

func findSession(ctx context.Context, q db.Querier, tokenHash []byte) (*sessionRow, error) {
	var s sessionRow
	err := q.QueryRow(ctx, `SELECT s.id, s.user_id, s.client_type, s.last_seen_at, s.expires_at, s.revoked_at IS NOT NULL,
       u.username, u.display_name, u.status,
       `+IsAdminSQL("s.user_id")+`,
       ARRAY(SELECT DISTINCT rm.menu_code FROM user_roles ur JOIN role_menus rm ON rm.role_id = ur.role_id
             WHERE ur.user_id = s.user_id)
FROM user_sessions s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1`, tokenHash).
		Scan(&s.SessionID, &s.UserID, &s.ClientType, &s.LastSeenAt, &s.ExpiresAt, &s.Revoked,
			&s.Username, &s.DisplayName, &s.UserStatus, &s.IsAdmin, &s.MenuCodes)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// touchSession 更新 last_seen_at；距离上次不到 1 分钟时不写（两个请求同时到时只有一个真的写）。
func touchSession(ctx context.Context, q db.Querier, id uuid.UUID) error {
	_, err := q.Exec(ctx, `UPDATE user_sessions SET last_seen_at = now()
WHERE id = $1 AND last_seen_at < now() - interval '1 minute'`, id)
	return err
}

func revokeSession(ctx context.Context, q db.Querier, tokenHash []byte) error {
	_, err := q.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash)
	return err
}

// revokeUserSessions 撤销某个用户所有没撤销的会话，返回撤销的个数。
func revokeUserSessions(ctx context.Context, q db.Querier, userID uuid.UUID) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE user_sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// findMe 一条语句读用户的基本信息、角色（按 role_code 排序）和有没有许可证菜单（GET /api/me、登录的响应）。
func findMe(ctx context.Context, q db.Querier, userID uuid.UUID) (me *Me, licenseMenu bool, err error) {
	me = &Me{}
	var codes, names []string
	err = q.QueryRow(ctx, `SELECT u.id, u.username, u.display_name, u.status,
  ARRAY(SELECT r.role_code FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = u.id ORDER BY r.role_code),
  ARRAY(SELECT r.role_name FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = u.id ORDER BY r.role_code),
  EXISTS (SELECT 1 FROM user_roles ur JOIN role_menus rm ON rm.role_id = ur.role_id
          WHERE ur.user_id = u.id AND rm.menu_code = $2)
FROM users u WHERE u.id = $1`, userID, MenuSystemLicense).
		Scan(&me.ID, &me.Username, &me.DisplayName, &me.Status, &codes, &names, &licenseMenu)
	if err != nil {
		return nil, false, err
	}
	me.Roles = make([]MeRole, len(codes))
	for i := range codes {
		me.Roles[i] = MeRole{RoleCode: codes[i], RoleName: names[i]}
	}
	return me, licenseMenu, nil
}

// RoleBrief 是用户拥有的一个角色。
type RoleBrief struct {
	ID       uuid.UUID `json:"id"`
	RoleCode string    `json:"role_code"`
	RoleName string    `json:"role_name"`
}

const menuColumns = `m.menu_code, m.parent_code, m.menu_name, m.menu_type, m.route_path, m.sort_no`

func scanMenus(ctx context.Context, q db.Querier, sql string, args ...any) ([]Menu, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Menu{}
	for rows.Next() {
		var m Menu
		if err := rows.Scan(&m.MenuCode, &m.ParentCode, &m.MenuName, &m.MenuType, &m.RoutePath, &m.SortNo); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// menusOfUser 是用户所有角色的菜单的并集，按 sort_no 排序。
func menusOfUser(ctx context.Context, q db.Querier, userID uuid.UUID) ([]Menu, error) {
	return scanMenus(ctx, q, `SELECT DISTINCT `+menuColumns+`
FROM user_roles ur
JOIN role_menus rm ON rm.role_id = ur.role_id
JOIN menus m ON m.menu_code = rm.menu_code
WHERE ur.user_id = $1
ORDER BY m.sort_no, m.menu_code`, userID)
}

// allMenus 是全部菜单（含分组），按 sort_no 排序。
func allMenus(ctx context.Context, q db.Querier) ([]Menu, error) {
	return scanMenus(ctx, q, `SELECT `+menuColumns+` FROM menus m ORDER BY m.sort_no, m.menu_code`)
}
