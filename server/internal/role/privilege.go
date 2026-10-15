package role

import (
	"fmt"
	"slices"

	"github.com/google/uuid"

	"doclite/server/internal/auth"
	"doclite/server/internal/platform/httpx"
)

// 防提权（requirements 第 4 节，用户决定 2026-09-26）：非 admin 的操作人违反下列规则时 403 `admin_required`；
// 持有 admin 角色的操作人不受限。
//
//  1. 授予或收回 admin 角色、修改持有 admin 角色的用户（改资料、停用、启用、重置密码、改角色），操作人必须是 admin；
//  2. 给任何角色增减 `system.*` 菜单（新建时带、改菜单、删除含这类菜单的角色），操作人必须是 admin；
//  3. 给用户新增的角色里的 `system.*` 菜单，必须是操作人自己菜单的子集；
//  4. 被修改的用户现有的 `system.*` 菜单，也必须是操作人菜单的子集。
//
// 这里只做判断。判断用的事实（操作人的角色和菜单、对象修改前后的状态）由调用方在事务里读出，
// 并且先拿"权限变更锁"（[Service.LockPermissionChanges]），读到的就是提交时的状态，并发时绕不过去。

// Operator 是操作人现在的角色和菜单（所有角色的并集）。
type Operator struct {
	RoleCodes []string
	MenuCodes map[string]bool
}

// IsAdmin 是否持有 admin 角色。
func (o *Operator) IsAdmin() bool { return slices.Contains(o.RoleCodes, auth.AdminRoleCode) }

// missingSystemMenu 返回 codes 里第一个操作人没有的系统管理菜单；都有时为空。
func (o *Operator) missingSystemMenu(codes []string) string {
	for _, c := range codes {
		if auth.IsSystemMenu(c) && !o.MenuCodes[c] {
			return c
		}
	}
	return ""
}

// Grant 是一个角色和它的菜单。
type Grant struct {
	RoleID    uuid.UUID
	RoleCode  string
	RoleName  string
	MenuCodes []string
}

// HasAdmin 判断这组角色里有没有 admin。
func HasAdmin(grants []Grant) bool { return hasAdmin(grants) }

func hasAdmin(grants []Grant) bool {
	return slices.ContainsFunc(grants, func(g Grant) bool { return g.RoleCode == auth.AdminRoleCode })
}

// CheckManageUser 是规则 1、4：修改一个用户之前检查，target 是他现在的角色。
func CheckManageUser(op *Operator, target []Grant) error {
	if op.IsAdmin() {
		return nil
	}
	if hasAdmin(target) {
		return httpx.AdminRequired("这个用户是系统管理员，只有系统管理员才能修改他")
	}
	for _, g := range target {
		if m := op.missingSystemMenu(g.MenuCodes); m != "" {
			return httpx.AdminRequired(fmt.Sprintf("这个用户有你没有的系统管理权限（%s），只有系统管理员才能修改他", m))
		}
	}
	return nil
}

// CheckAssignRoles 是规则 1、3：用户的角色从 before 换成 after（新建用户时 before 为空）。
func CheckAssignRoles(op *Operator, before, after []Grant) error {
	if op.IsAdmin() {
		return nil
	}
	if hasAdmin(before) != hasAdmin(after) {
		return httpx.AdminRequired("只有系统管理员才能授予或收回系统管理员角色")
	}
	for _, g := range after {
		if slices.ContainsFunc(before, func(b Grant) bool { return b.RoleID == g.RoleID }) {
			continue
		}
		if m := op.missingSystemMenu(g.MenuCodes); m != "" {
			return httpx.AdminRequired(fmt.Sprintf("角色「%s」含有你没有的系统管理菜单（%s），只有系统管理员才能分配", g.RoleName, m))
		}
	}
	return nil
}

// checkChangeRoleMenus 是规则 2：角色的菜单从 before 换成 after（新建时 before 为空，删除时 after 为空）。
func checkChangeRoleMenus(op *Operator, before, after []string) error {
	if op.IsAdmin() {
		return nil
	}
	if !slices.Equal(systemMenus(before), systemMenus(after)) {
		return httpx.AdminRequired("只有系统管理员才能增减角色的系统管理菜单（system.*）")
	}
	return nil
}

func systemMenus(codes []string) []string {
	var out []string
	for _, c := range codes {
		if auth.IsSystemMenu(c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
