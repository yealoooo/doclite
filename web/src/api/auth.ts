// 会话：登录、退出、当前用户和菜单。

import { request } from './http'
import type { Me, MyMenu } from './types'

/** 登录。错误（`invalid_credentials`、`user_disabled`……）由登录页自己显示。 */
export function login(username: string, password: string) {
  return request<Me>('/api/auth/login', {
    method: 'POST',
    body: { username, password },
    handle: true,
  })
}

/** 退出。失败时抛出 ApiError，由调用方提示（会话可能仍然有效）。 */
export function logout() {
  return request<void>('/api/auth/logout', { method: 'POST', handle: true })
}

/**
 * 当前用户。`probe: true` 用于启动时探测登录状态：没登录（401）不触发跳转、不弹提示；
 * 其他时候 401 按会话失效统一处理（跳登录页）。
 */
export function fetchMe(opts: { probe?: boolean } = {}) {
  return opts.probe
    ? request<Me>('/api/me', { authRedirect: false, handle: ['unauthorized'] })
    : request<Me>('/api/me')
}

export function fetchMyMenus() {
  return request<MyMenu[]>('/api/me/menus')
}
