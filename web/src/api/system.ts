// 系统管理：用户（菜单码 system.users）、角色与菜单（system.roles）。
// 每个函数的最后一个参数可以传 `handle`，声明调用方自己显示哪些错误（见 http.ts）。

import { type Handle, request } from './http'
import type { MenuItem, Page, RoleItem, UserItem } from './types'

// ---------- 用户 ----------

export interface UserListQuery {
  keyword?: string
  page?: number
  page_size?: number
}

export function listUsers(q: UserListQuery) {
  return request<Page<UserItem>>('/api/system/users', { query: { ...q } })
}

export interface CreateUserBody {
  username: string
  display_name: string
  password: string
  role_ids?: string[]
}

export function createUser(body: CreateUserBody, handle?: Handle) {
  return request<UserItem>('/api/system/users', { method: 'POST', body, handle })
}

export interface UpdateUserBody {
  username?: string
  display_name?: string
}

export function updateUser(id: string, body: UpdateUserBody, handle?: Handle) {
  return request<UserItem>(`/api/system/users/${id}`, { method: 'PATCH', body, handle })
}

export function disableUser(id: string, handle?: Handle) {
  return request<UserItem>(`/api/system/users/${id}/disable`, { method: 'POST', handle })
}

export function enableUser(id: string, handle?: Handle) {
  return request<UserItem>(`/api/system/users/${id}/enable`, { method: 'POST', handle })
}

export function resetPassword(id: string, password: string, handle?: Handle) {
  return request<void>(`/api/system/users/${id}/reset-password`, {
    method: 'POST',
    body: { password },
    handle,
  })
}

export function setUserRoles(id: string, roleIds: string[], handle?: Handle) {
  return request<UserItem>(`/api/system/users/${id}/roles`, {
    method: 'PUT',
    body: { role_ids: roleIds },
    handle,
  })
}

// ---------- 角色和菜单 ----------

/** 全部角色：内置在前，自建按创建时间。system.roles 或 system.users 都能调。 */
export function listRoles() {
  return request<RoleItem[]>('/api/system/roles')
}

export interface CreateRoleBody {
  role_code: string
  role_name: string
  description?: string
  menu_codes?: string[]
}

export function createRole(body: CreateRoleBody, handle?: Handle) {
  return request<RoleItem>('/api/system/roles', { method: 'POST', body, handle })
}

export interface UpdateRoleBody {
  role_name?: string
  /** 空字符串表示清空。 */
  description?: string
}

export function updateRole(id: string, body: UpdateRoleBody, handle?: Handle) {
  return request<RoleItem>(`/api/system/roles/${id}`, { method: 'PATCH', body, handle })
}

export function deleteRole(id: string, handle?: Handle) {
  return request<void>(`/api/system/roles/${id}`, { method: 'DELETE', handle })
}

export function setRoleMenus(id: string, menuCodes: string[], handle?: Handle) {
  return request<RoleItem>(`/api/system/roles/${id}/menus`, {
    method: 'PUT',
    body: { menu_codes: menuCodes },
    handle,
  })
}

/** 全部菜单（含分组），按 sort_no 排序。 */
export function listAllMenus() {
  return request<MenuItem[]>('/api/system/menus')
}
