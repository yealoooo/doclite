// 服务端的健康检查（不用登录，不检查客户端版本）：设置页测连接、看服务端版本用。

import { HANDLE_ALL, request } from './http'

export interface Health {
  status: string
  /** 服务端的版本。 */
  version: string
}

/** GET /api/health。错误由调用方自己显示（不弹提示、401 不跳登录页）。 */
export function fetchHealth() {
  return request<Health>('/api/health', { handle: HANDLE_ALL, authRedirect: false })
}
