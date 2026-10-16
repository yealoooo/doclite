// 审计日志（M7-3，菜单 system.audit）：只读的列表和筛选栏的选项。

import { request, type RequestOptions } from './http'
import type { AuditFilterOptions, AuditLogList } from './types'

/** 每页条数（server 默认 50，1–200）。 */
export const AUDIT_PAGE_SIZE = 50

export interface AuditQuery {
  /** RFC 3339；created_at 在 [from, to) 里。 */
  from?: string | null
  to?: string | null
  operator_id?: string | null
  project_id?: string | null
  target_type?: string | null
  action?: string | null
  limit?: number
  /** 上一页的 `next_cursor`。 */
  cursor?: string | null
}

/** 一页审计记录，新的在前。 */
export function listAuditLogs(q: AuditQuery, handle?: RequestOptions['handle']) {
  return request<AuditLogList>('/api/system/audit-logs', { query: { ...q }, handle })
}

/** 筛选栏的选项。 */
export function getAuditFilterOptions(handle?: RequestOptions['handle']) {
  return request<AuditFilterOptions>('/api/system/audit-logs/filter-options', { handle })
}
