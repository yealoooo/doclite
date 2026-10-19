// Package user 是系统管理里的用户（requirements 5.2）：列表、详情、新建、修改、停用、启用、重置密码、改角色，
// 以及首次启动时建初始管理员。
//
// 规则：
//   - 登录名 1–64 个字符、不含空白（去掉首尾空白后），唯一、不区分大小写；姓名 1–64；密码 6–128（原样使用）；
//   - 不提供删除，不用的账号停用；停用、重置密码时在同一个事务里撤销他所有的会话；
//   - 不能停用自己（409 `cannot_disable_self`）；
//   - 系统里至少保留一个"启用且有 admin 角色"的用户：停用、改角色时检查（409 `last_admin`）；
//   - 防提权规则 1、3、4（见 role/privilege.go），违反 403 `admin_required`；
//   - 每个写事务第一步拿"权限变更锁"（role.Service.LockPermissionChanges），防提权和最后一个管理员的检查在并发下绕不过去；
//   - 修改都写 audit_logs（target_type = user），不记密码和哈希；没有实际变化时不写库、不审计，直接返回当前数据。
package user

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/audit"
	"doclite/server/internal/auth"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/xslices"
	"doclite/server/internal/role"
)

const (
	maxUsernameChars    = 64
	maxDisplayNameChars = 64
	minPasswordChars    = 6
	maxPasswordChars    = 128
	maxKeywordChars     = 64
)

// Service 是用户的业务规则。
type Service struct {
	pool  *pgxpool.Pool
	auth  *auth.Service
	roles *role.Service
}

// NewService 建 Service。
func NewService(pool *pgxpool.Pool, authSvc *auth.Service, roleSvc *role.Service) *Service {
	return &Service{pool: pool, auth: authSvc, roles: roleSvc}
}

var errNotFound = httpx.NotFound("用户")

// List 是用户列表（keyword 匹配登录名或姓名，不区分大小写）。
func (s *Service) List(ctx context.Context, keyword string, page httpx.PageRequest) (httpx.Page[User], error) {
	keyword = strings.TrimSpace(keyword)
	if utf8.RuneCountInString(keyword) > maxKeywordChars {
		return httpx.Page[User]{}, httpx.Invalid("搜索关键字最长 %d 个字符", maxKeywordChars)
	}
	users, total, err := listUsers(ctx, s.pool, keyword, page)
	if err != nil {
		return httpx.Page[User]{}, err
	}
	return httpx.NewPage(users, total, page), nil
}

// Get 读一个用户（含角色）；不存在 404。
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*User, error) {
	u, err := getUser(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, errNotFound
	}
	return u, nil
}

// CreateInput 是新建用户的输入。
type CreateInput struct {
	Username    string      `json:"username"`
	DisplayName string      `json:"display_name"`
	Password    string      `json:"password"`
	RoleIDs     []uuid.UUID `json:"role_ids"`
}

// Create 新建用户（状态 active）并赋予角色。登录名重复 409 `conflict`；有不存在的角色 400。
func (s *Service) Create(ctx context.Context, operatorID uuid.UUID, in CreateInput) (*User, error) {
	username, err := normalizeUsername(in.Username)
	if err != nil {
		return nil, err
	}
	displayName, err := normalizeDisplayName(in.DisplayName)
	if err != nil {
		return nil, err
	}
	if err := checkPassword(in.Password); err != nil {
		return nil, err
	}
	roleIDs := xslices.Unique(in.RoleIDs)
	hash, err := s.auth.HashPassword(ctx, in.Password)
	if err != nil {
		return nil, err
	}
	id := db.NewID()
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.roles.LockPermissionChanges(ctx, tx); err != nil {
			return err
		}
		op, err := s.roles.Operator(ctx, tx, operatorID)
		if err != nil {
			return err
		}
		grants, err := s.roles.GrantsByIDs(ctx, tx, roleIDs)
		if err != nil {
			return err
		}
		if err := role.CheckAssignRoles(op, nil, grants); err != nil {
			return err
		}
		if err := insertUser(ctx, tx, id, username, displayName, hash); err != nil {
			if db.UniqueViolation(err, "ux_users_username") {
				return errDuplicateUsername(username)
			}
			return err
		}
		if err := replaceRoles(ctx, tx, id, roleIDs); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			OperatorID: audit.Operator(operatorID), TargetType: audit.TargetUser, TargetID: id, Action: "create",
			Detail: map[string]any{"after": map[string]any{
				"username": username, "display_name": displayName, "status": "active", "roles": roleCodes(grants),
			}},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// UpdateInput 是修改用户的输入；nil 表示不改。
type UpdateInput struct {
	Username    *string `json:"username"`
	DisplayName *string `json:"display_name"`
}

// Update 改登录名、姓名。
func (s *Service) Update(ctx context.Context, operatorID, id uuid.UUID, in UpdateInput) (*User, error) {
	var username, displayName string
	var err error
	if in.Username != nil {
		if username, err = normalizeUsername(*in.Username); err != nil {
			return nil, err
		}
	}
	if in.DisplayName != nil {
		if displayName, err = normalizeDisplayName(*in.DisplayName); err != nil {
			return nil, err
		}
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, _, err := s.lockForManage(ctx, tx, operatorID, id)
		if err != nil {
			return err
		}
		newUsername, newDisplayName := before.Username, before.DisplayName
		if in.Username != nil {
			newUsername = username
		}
		if in.DisplayName != nil {
			newDisplayName = displayName
		}
		if newUsername == before.Username && newDisplayName == before.DisplayName {
			return nil // 没有变化
		}
		if err := updateProfile(ctx, tx, id, newUsername, newDisplayName); err != nil {
			if db.UniqueViolation(err, "ux_users_username") {
				return errDuplicateUsername(newUsername)
			}
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			OperatorID: audit.Operator(operatorID), TargetType: audit.TargetUser, TargetID: id, Action: "update",
			Detail: map[string]any{
				"before": map[string]any{"username": before.Username, "display_name": before.DisplayName},
				"after":  map[string]any{"username": newUsername, "display_name": newDisplayName},
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Disable 停用并让他立即下线。不能停用自己；不能停用最后一个启用的管理员。
func (s *Service) Disable(ctx context.Context, operatorID, id uuid.UUID) (*User, error) {
	if id == operatorID {
		return nil, httpx.Conflict("cannot_disable_self", "不能停用自己的账号")
	}
	return s.setStatus(ctx, operatorID, id, "disabled")
}

// Enable 启用。
func (s *Service) Enable(ctx context.Context, operatorID, id uuid.UUID) (*User, error) {
	return s.setStatus(ctx, operatorID, id, "active")
}

func (s *Service) setStatus(ctx context.Context, operatorID, id uuid.UUID, status string) (*User, error) {
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		before, grants, err := s.lockForManage(ctx, tx, operatorID, id)
		if err != nil {
			return err
		}
		if before.Status == status {
			return nil // 没有变化
		}
		if err := updateStatus(ctx, tx, id, status); err != nil {
			return err
		}
		after := map[string]any{"status": status}
		action := "enable"
		if status == "disabled" {
			action = "disable"
			if role.HasAdmin(grants) {
				if err := ensureActiveAdmin(ctx, tx); err != nil {
					return err
				}
			}
			n, err := s.auth.RevokeUserSessions(ctx, tx, id)
			if err != nil {
				return err
			}
			after["revoked_sessions"] = n
		}
		return audit.Write(ctx, tx, audit.Entry{
			OperatorID: audit.Operator(operatorID), TargetType: audit.TargetUser, TargetID: id, Action: action,
			Detail: map[string]any{"before": map[string]any{"status": before.Status}, "after": after},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// ResetPassword 换密码，并撤销他所有的会话（包括操作人重置自己时的当前会话）。
func (s *Service) ResetPassword(ctx context.Context, operatorID, id uuid.UUID, password string) error {
	if err := checkPassword(password); err != nil {
		return err
	}
	// 先确认用户存在，免得白算一次哈希。
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	hash, err := s.auth.HashPassword(ctx, password)
	if err != nil {
		return err
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := s.lockForManage(ctx, tx, operatorID, id); err != nil {
			return err
		}
		if err := updatePassword(ctx, tx, id, hash); err != nil {
			return err
		}
		n, err := s.auth.RevokeUserSessions(ctx, tx, id)
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			OperatorID: audit.Operator(operatorID), TargetType: audit.TargetUser, TargetID: id, Action: "reset_password",
			Detail: map[string]any{"after": map[string]any{"password_reset": true, "revoked_sessions": n}},
		})
	})
}

// SetRoles 整体替换用户的角色。拿掉最后一个启用的管理员的 admin 角色 409 `last_admin`。
func (s *Service) SetRoles(ctx context.Context, operatorID, id uuid.UUID, roleIDs []uuid.UUID) (*User, error) {
	roleIDs = xslices.Unique(roleIDs)
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.roles.LockPermissionChanges(ctx, tx); err != nil {
			return err
		}
		u, err := lockUser(ctx, tx, id)
		if err != nil {
			return err
		}
		if u == nil {
			return errNotFound
		}
		op, err := s.roles.Operator(ctx, tx, operatorID)
		if err != nil {
			return err
		}
		before, err := s.roles.GrantsOfUser(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := role.CheckManageUser(op, before); err != nil {
			return err
		}
		after, err := s.roles.GrantsByIDs(ctx, tx, roleIDs)
		if err != nil {
			return err
		}
		if sameRoles(before, after) {
			return nil // 没有变化
		}
		if err := role.CheckAssignRoles(op, before, after); err != nil {
			return err
		}
		if err := replaceRoles(ctx, tx, id, roleIDs); err != nil {
			return err
		}
		if u.Status == "active" && role.HasAdmin(before) {
			if err := ensureActiveAdmin(ctx, tx); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, audit.Entry{
			OperatorID: audit.Operator(operatorID), TargetType: audit.TargetUser, TargetID: id, Action: "set_roles",
			Detail: map[string]any{
				"before": map[string]any{"roles": roleCodes(before)},
				"after":  map[string]any{"roles": roleCodes(after)},
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// lockForManage 是"修改一个用户"的公共步骤：拿权限变更锁 → 锁住用户（不存在 404）→ 防提权规则 1、4。
// 返回用户和他现在的角色。
func (s *Service) lockForManage(ctx context.Context, tx pgx.Tx, operatorID, id uuid.UUID) (*userRow, []role.Grant, error) {
	if err := s.roles.LockPermissionChanges(ctx, tx); err != nil {
		return nil, nil, err
	}
	u, err := lockUser(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	if u == nil {
		return nil, nil, errNotFound
	}
	op, err := s.roles.Operator(ctx, tx, operatorID)
	if err != nil {
		return nil, nil, err
	}
	grants, err := s.roles.GrantsOfUser(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	if err := role.CheckManageUser(op, grants); err != nil {
		return nil, nil, err
	}
	return u, grants, nil
}

// ensureActiveAdmin：改完后系统里一个"启用且有 admin 角色"的用户都不剩时 409 `last_admin`（事务回滚）。
func ensureActiveAdmin(ctx context.Context, tx pgx.Tx) error {
	ok, err := activeAdminRemains(ctx, tx)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.Conflict("last_admin", "系统里至少要保留一个启用的系统管理员")
	}
	return nil
}

// EnsureInitialAdmin 首次启动时建初始管理员：只在库里一个用户都没有时建，赋予 admin 角色，写审计（操作人为系统）。
// 没有用户也没有配置时打一条警告（这时谁都登录不了）。
func (s *Service) EnsureInitialAdmin(ctx context.Context, username, displayName, password string, configured bool, log *slog.Logger) error {
	exists, err := anyUserExists(ctx, s.pool)
	if err != nil || exists {
		return err
	}
	if !configured {
		log.Warn("系统里还没有用户，也没有设置 INIT_ADMIN_USERNAME、INIT_ADMIN_PASSWORD：现在谁都无法登录")
		return nil
	}
	username, err = normalizeUsername(username)
	if err != nil {
		return fmt.Errorf("INIT_ADMIN_USERNAME 不合规则：%s", err.(*httpx.Error).Message)
	}
	displayName, err = normalizeDisplayName(displayName)
	if err != nil {
		return fmt.Errorf("INIT_ADMIN_DISPLAY_NAME 不合规则：%s", err.(*httpx.Error).Message)
	}
	if err := checkPassword(password); err != nil {
		return fmt.Errorf("INIT_ADMIN_PASSWORD 不合规则：%s", err.(*httpx.Error).Message)
	}
	hash, err := s.auth.HashPassword(ctx, password)
	if err != nil {
		return err
	}
	id := db.NewID()
	created := false
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockUsersTable(ctx, tx); err != nil {
			return err
		}
		exists, err := anyUserExists(ctx, tx)
		if err != nil || exists {
			return err
		}
		if err := insertUser(ctx, tx, id, username, displayName, hash); err != nil {
			return err
		}
		ok, err := grantAdminRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("找不到内置角色 admin（迁移里的初始数据缺失？）")
		}
		created = true
		return audit.Write(ctx, tx, audit.Entry{
			TargetType: audit.TargetUser, TargetID: id, Action: "create",
			Detail: map[string]any{"source": "init_admin", "after": map[string]any{
				"username": username, "display_name": displayName, "status": "active", "roles": []string{auth.AdminRoleCode},
			}},
		})
	})
	if err != nil {
		return fmt.Errorf("建初始管理员失败：%w", err)
	}
	if created {
		log.Info("已创建初始管理员", "username", username)
	}
	return nil
}

// ---- 校验 ----

func normalizeUsername(v string) (string, error) {
	v = strings.TrimSpace(v)
	bad := v == "" || utf8.RuneCountInString(v) > maxUsernameChars ||
		strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0
	if bad {
		return "", httpx.Invalid("登录名不能为空，不能包含空格，最长 %d 个字符", maxUsernameChars)
	}
	return v, nil
}

func normalizeDisplayName(v string) (string, error) {
	v = strings.TrimSpace(v)
	bad := v == "" || utf8.RuneCountInString(v) > maxDisplayNameChars || strings.IndexFunc(v, unicode.IsControl) >= 0
	if bad {
		return "", httpx.Invalid("姓名不能为空，最长 %d 个字符", maxDisplayNameChars)
	}
	return v, nil
}

// checkPassword：6–128 个字符，原样使用（不去空白）。
func checkPassword(v string) error {
	n := utf8.RuneCountInString(v)
	if n < minPasswordChars || n > maxPasswordChars {
		return httpx.Invalid("密码要有 %d 到 %d 个字符", minPasswordChars, maxPasswordChars)
	}
	return nil
}

func errDuplicateUsername(username string) error {
	return httpx.Conflict("", fmt.Sprintf("登录名 %s 已存在（不区分大小写）", username))
}

func roleCodes(grants []role.Grant) []string {
	out := make([]string, 0, len(grants))
	for _, g := range grants {
		out = append(out, g.RoleCode)
	}
	return out
}

func sameRoles(a, b []role.Grant) bool {
	if len(a) != len(b) {
		return false
	}
	ids := map[uuid.UUID]bool{}
	for _, g := range a {
		ids[g.RoleID] = true
	}
	for _, g := range b {
		if !ids[g.RoleID] {
			return false
		}
	}
	return true
}
