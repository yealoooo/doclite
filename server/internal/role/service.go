// Package role 是角色和角色的菜单（requirements 5.2、第 4 节）。
//
// 规则：
//   - 角色是全局的；role_code `^[a-z][a-z0-9_]{0,31}$`，建好后不能改；
//   - 内置角色（is_builtin）不能删除（409 `builtin_role`），可以改名称和说明；
//   - admin 的菜单固定为全部菜单，不能改（409 `admin_menus_locked`）；
//   - 分配给角色的菜单必须存在、而且不能是分组；
//   - 删除自建角色时 user_roles、role_menus、project_roles 由外键级联删除；
//   - 防提权规则 2（见 privilege.go）；
//   - 增删改都写 audit_logs（target_type = role），和改动在同一个事务里；没有实际变化时不写库、不审计。
//
// 另外给 user 模块提供防提权需要的事实（权限变更锁、操作人和用户的角色与菜单），在调用方的事务里执行。
package role

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/audit"
	"doclite/server/internal/auth"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/httpx"
)

const (
	maxRoleNameRunes    = 64
	maxDescriptionRunes = 500
)

var roleCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Service 是角色的业务规则。
type Service struct {
	pool *pgxpool.Pool
	auth *auth.Service
}

// NewService 建 Service。
func NewService(pool *pgxpool.Pool, authSvc *auth.Service) *Service {
	return &Service{pool: pool, auth: authSvc}
}

var errNotFound = httpx.NotFound("角色")

// List 是全部角色：内置的在前，自建的按创建时间。
func (s *Service) List(ctx context.Context) ([]Role, error) {
	return listRoles(ctx, s.pool)
}

// Get 读一个角色；不存在 404。
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Role, error) {
	r, err := getRole(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errNotFound
	}
	return r, nil
}

// CreateInput 是新建角色的输入。
type CreateInput struct {
	RoleCode    string
	RoleName    string
	Description *string
	MenuCodes   []string
}

// Create 新建自建角色。编码重复 409 `conflict`；菜单不存在或是分组 400。
func (s *Service) Create(ctx context.Context, operatorID uuid.UUID, in CreateInput) (*Role, error) {
	code := strings.TrimSpace(in.RoleCode)
	if !roleCodePattern.MatchString(code) {
		return nil, httpx.Invalid("角色编码要以小写字母开头，只能包含小写字母、数字和下划线，最长 32 个字符")
	}
	name, err := normalizeName(in.RoleName)
	if err != nil {
		return nil, err
	}
	desc, err := normalizeDescription(in.Description)
	if err != nil {
		return nil, err
	}
	menus, err := s.normalizeMenus(ctx, in.MenuCodes)
	if err != nil {
		return nil, err
	}
	id := db.NewID()
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockPermissionChanges(ctx, tx); err != nil {
			return err
		}
		op, err := s.Operator(ctx, tx, operatorID)
		if err != nil {
			return err
		}
		if err := checkChangeRoleMenus(op, nil, menus); err != nil {
			return err
		}
		if err := insertRole(ctx, tx, id, code, name, desc); err != nil {
			if db.UniqueViolation(err, "roles_role_code_key") {
				return httpx.Conflict("", fmt.Sprintf("角色编码 %s 已存在", code))
			}
			return err
		}
		if err := replaceMenus(ctx, tx, id, menus); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			OperatorID: audit.Operator(operatorID), TargetType: audit.TargetRole, TargetID: id, Action: "create",
			Detail: map[string]any{"after": map[string]any{
				"role_code": code, "role_name": name, "description": desc, "menu_codes": menus,
			}},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// UpdateInput 是修改角色的输入；nil 表示不改，Description 为空字符串表示清空。
type UpdateInput struct {
	RoleName    *string
	Description *string
}

// Update 改名称、说明（内置角色也可以改）。
func (s *Service) Update(ctx context.Context, operatorID, id uuid.UUID, in UpdateInput) (*Role, error) {
	var name *string
	if in.RoleName != nil {
		n, err := normalizeName(*in.RoleName)
		if err != nil {
			return nil, err
		}
		name = &n
	}
	var desc *string
	if in.Description != nil {
		d, err := normalizeDescription(in.Description)
		if err != nil {
			return nil, err
		}
		desc = d
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// 改名不涉及权限，也在同一把锁上排队，避免和其他权限变更的行锁顺序相反。
		if err := lockPermissionChanges(ctx, tx); err != nil {
			return err
		}
		before, err := lockRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if before == nil {
			return errNotFound
		}
		newName, newDesc := before.RoleName, before.Description
		if name != nil {
			newName = *name
		}
		if in.Description != nil {
			newDesc = desc
		}
		if newName == before.RoleName && equalPtr(newDesc, before.Description) {
			return nil // 没有变化
		}
		if err := updateRole(ctx, tx, id, newName, newDesc); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			OperatorID: audit.Operator(operatorID), TargetType: audit.TargetRole, TargetID: id, Action: "update",
			Detail: map[string]any{
				"before": map[string]any{"role_name": before.RoleName, "description": before.Description},
				"after":  map[string]any{"role_name": newName, "description": newDesc},
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Delete 删除自建角色。内置角色 409 `builtin_role`；含 system.* 菜单的角色只有 admin 能删。
func (s *Service) Delete(ctx context.Context, operatorID, id uuid.UUID) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockPermissionChanges(ctx, tx); err != nil {
			return err
		}
		before, err := lockRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if before == nil {
			return errNotFound
		}
		if before.IsBuiltin {
			return httpx.Conflict("builtin_role", "内置角色不能删除")
		}
		menus, err := menuCodesOf(ctx, tx, id)
		if err != nil {
			return err
		}
		op, err := s.Operator(ctx, tx, operatorID)
		if err != nil {
			return err
		}
		if err := checkChangeRoleMenus(op, menus, nil); err != nil {
			return err
		}
		userIDs, err := userIDsOfRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := deleteRole(ctx, tx, id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			OperatorID: audit.Operator(operatorID), TargetType: audit.TargetRole, TargetID: id, Action: "delete",
			Detail: map[string]any{"before": map[string]any{
				"role_code": before.RoleCode, "role_name": before.RoleName, "description": before.Description,
				"menu_codes": menus, "user_ids": userIDs,
			}},
		})
	})
}

// SetMenus 整体替换角色的菜单。admin 角色 409 `admin_menus_locked`。
func (s *Service) SetMenus(ctx context.Context, operatorID, id uuid.UUID, codes []string) (*Role, error) {
	menus, err := s.normalizeMenus(ctx, codes)
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockPermissionChanges(ctx, tx); err != nil {
			return err
		}
		r, err := lockRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if r == nil {
			return errNotFound
		}
		if r.RoleCode == auth.AdminRoleCode {
			return httpx.Conflict("admin_menus_locked", "系统管理员角色的菜单不能修改")
		}
		before, err := menuCodesOf(ctx, tx, id)
		if err != nil {
			return err
		}
		if sameSet(before, menus) {
			return nil // 没有变化
		}
		op, err := s.Operator(ctx, tx, operatorID)
		if err != nil {
			return err
		}
		if err := checkChangeRoleMenus(op, before, menus); err != nil {
			return err
		}
		if err := replaceMenus(ctx, tx, id, menus); err != nil {
			return err
		}
		if err := touchRole(ctx, tx, id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{
			OperatorID: audit.Operator(operatorID), TargetType: audit.TargetRole, TargetID: id, Action: "set_menus",
			Detail: map[string]any{
				"before": map[string]any{"menu_codes": before},
				"after":  map[string]any{"menu_codes": menus},
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// AllMenus 是全部菜单（含分组），给角色页勾选。
func (s *Service) AllMenus(ctx context.Context) ([]auth.Menu, error) {
	return s.auth.AllMenus(ctx)
}

// ---- 给 user 模块用：在调用方的事务里执行 ----

// LockPermissionChanges 拿权限变更锁（见 lockPermissionChanges）。用户、角色的写事务第一步调用。
func (s *Service) LockPermissionChanges(ctx context.Context, tx pgx.Tx) error {
	return lockPermissionChanges(ctx, tx)
}

// Operator 读操作人现在的角色和菜单。
func (s *Service) Operator(ctx context.Context, q db.Querier, userID uuid.UUID) (*Operator, error) {
	grants, err := grantsOfUser(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	op := &Operator{MenuCodes: map[string]bool{}}
	for _, g := range grants {
		op.RoleCodes = append(op.RoleCodes, g.RoleCode)
		for _, m := range g.MenuCodes {
			op.MenuCodes[m] = true
		}
	}
	return op, nil
}

// GrantsOfUser 是用户现在的角色和菜单，按 role_code 排序。
func (s *Service) GrantsOfUser(ctx context.Context, q db.Querier, userID uuid.UUID) ([]Grant, error) {
	return grantsOfUser(ctx, q, userID)
}

// GrantsByIDs 是一组角色（ids 已去重）和它们的菜单；有不存在的角色时 400。
func (s *Service) GrantsByIDs(ctx context.Context, q db.Querier, ids []uuid.UUID) ([]Grant, error) {
	if len(ids) == 0 {
		return []Grant{}, nil
	}
	grants, err := grantsByIDs(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	if len(grants) != len(ids) {
		return nil, httpx.Invalid("有的角色不存在（可能刚被删除），请刷新后重试")
	}
	return grants, nil
}

// ---- 校验 ----

func normalizeName(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || utf8.RuneCountInString(v) > maxRoleNameRunes {
		return "", httpx.Invalid("角色名称不能为空，最长 %d 个字符", maxRoleNameRunes)
	}
	return v, nil
}

// normalizeDescription：去掉首尾空白，空的当作没有；最长 500 个字符。
func normalizeDescription(v *string) (*string, error) {
	if v == nil {
		return nil, nil
	}
	d := strings.TrimSpace(*v)
	if d == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(d) > maxDescriptionRunes {
		return nil, httpx.Invalid("角色说明最长 %d 个字符", maxDescriptionRunes)
	}
	return &d, nil
}

// normalizeMenus 校验要分配给角色的菜单码：去掉重复，每个都必须存在、类型为 menu（分组不能分配）。
// 返回按 sort_no 排好序的菜单码。
func (s *Service) normalizeMenus(ctx context.Context, codes []string) ([]string, error) {
	all, err := s.auth.AllMenus(ctx)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, c := range codes {
		wanted[strings.TrimSpace(c)] = true
	}
	byCode := map[string]auth.Menu{}
	for _, m := range all {
		byCode[m.MenuCode] = m
	}
	for c := range wanted {
		m, ok := byCode[c]
		if !ok {
			return nil, httpx.Invalid("菜单不存在：%s", c)
		}
		if m.MenuType != "menu" {
			return nil, httpx.Invalid("%s 是菜单分组，不能分配给角色，请勾选它下面的菜单", c)
		}
	}
	out := []string{}
	for _, m := range all { // all 已按 sort_no 排序
		if wanted[m.MenuCode] {
			out = append(out, m.MenuCode)
		}
	}
	return out, nil
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
