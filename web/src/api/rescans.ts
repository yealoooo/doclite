// 待重扫列表（M2-7；接口是 M2-2 的 rescans）。重扫件不从页面发文件，走壳的一次性命令 `pick_and_rescan`（lib/rescan.ts）。

import { type Handle, request } from './http'
import type { RescanList } from './types'

/** 待重扫列表一页取多少个（server 默认 500，1–2000）。 */
export const RESCAN_PAGE_SIZE = 200

export interface RescansQuery {
  /** 子树；不传是整个项目。 */
  folder_id?: string | null
  limit?: number
  /** 上一页的 `next_cursor`。 */
  cursor?: string | null
}

/** 已驳回、等重扫的文件（scan 或 files 菜单）：带文件夹路径、备注（原件位置）和最近一次驳回的原因、说明、审核人。 */
export function listRescans(projectId: string, q: RescansQuery = {}, handle?: Handle) {
  return request<RescanList>(`/api/projects/${projectId}/rescans`, { query: { ...q }, handle })
}
