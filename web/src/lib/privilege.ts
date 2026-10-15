// 防提权规则的前端预判，和 server 的 doclite_domain::privilege 一致（design §3）。
// 只用来预先禁用按钮、给出说明；后端仍然会检查，违反时返回 403 `admin_required`。
// 每个函数返回"为什么不能做"（给人看的说明），可以做时返回 null。持有 admin 角色的操作人不受限。

import type { RoleItem } from '@/api/types'

export const ADMIN_ROLE_CODE = 'admin'
export const SYSTEM_MENU_PREFIX = 'system.'

/** 是否是系统管理菜单（`system.` 开头）。 */
export function isSystemMenu(menuCode: string): boolean {
  return menuCode.startsWith(SYSTEM_MENU_PREFIX)
}

/** 当前操作人：是否持有 admin 角色，以及自己所有角色的菜单并集。 */
export interface Operator {
  isAdmin: boolean
  menuCodes: ReadonlySet<string>
}

type RoleLike = Pick<RoleItem, 'role_code' | 'role_name' | 'menu_codes'>

function missingSystemMenu(op: Operator, menuCodes: readonly string[]): string | undefined {
  return menuCodes.find((m) => isSystemMenu(m) && !op.menuCodes.has(m))
}

/**
 * 规则 1、4：修改一个用户（改资料、停用、启用、重置密码、改角色）。
 * `targetRoles` 是他现在的角色（带菜单，来自角色列表）。
 */
export function manageUserBlock(op: Operator, targetRoles: readonly RoleLike[]): string | null {
  if (op.isAdmin) return null
  if (targetRoles.some((r) => r.role_code === ADMIN_ROLE_CODE)) {
    return '这个用户是系统管理员，只有系统管理员才能修改他'
  }
  for (const role of targetRoles) {
    const menu = missingSystemMenu(op, role.menu_codes)
    if (menu) return `这个用户有你没有的系统管理权限（${menu}），只有系统管理员才能修改他`
  }
  return null
}

/**
 * 规则 1、3：给用户勾选或取消一个角色。`hadBefore` 表示用户原来就有这个角色（新建用户时为 false）。
 * 取消原有的非 admin 角色总是可以；授予或收回 admin、新增含有自己没有的 system.* 菜单的角色要管理员。
 */
export function assignRoleBlock(op: Operator, role: RoleLike, hadBefore: boolean): string | null {
  if (op.isAdmin) return null
  if (role.role_code === ADMIN_ROLE_CODE) return '只有系统管理员才能授予或收回系统管理员角色'
  if (hadBefore) return null
  const menu = missingSystemMenu(op, role.menu_codes)
  if (menu) return `含有你没有的系统管理菜单（${menu}），只有系统管理员才能分配`
  return null
}

/** 规则 2：勾选或取消角色的某个菜单。 */
export function roleMenuBlock(op: Operator, menuCode: string): string | null {
  if (op.isAdmin || !isSystemMenu(menuCode)) return null
  return '只有系统管理员才能增减角色的系统管理菜单（system.*）'
}

/** 删除角色：内置角色不能删；含 system.* 菜单的角色只有管理员能删（规则 2）。 */
export function deleteRoleBlock(op: Operator, role: RoleItem): string | null {
  if (role.is_builtin) return '内置角色不能删除'
  if (!op.isAdmin && role.menu_codes.some(isSystemMenu)) {
    return '这个角色含有系统管理菜单（system.*），只有系统管理员才能删除'
  }
  return null
}
