// 待重扫列表和浏览器里的重扫件上传（M2-7；接口是 M2-2 的 rescans、M1-7 的重扫，design §1.5、§4.3、§7）。
// 壳里的重扫件不从页面发文件，走 Rust 的一次性命令 `pick_and_rescan`（lib/rescan.ts）。

import { request, type RequestOptions, upload, type UploadOptions, type UploadParts } from './http'
import type { RescanList, RescanResponse } from './types'

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
export function listRescans(projectId: string, q: RescansQuery = {}, handle?: RequestOptions['handle']) {
  return request<RescanList>(`/api/projects/${projectId}/rescans`, { query: { ...q }, handle })
}

/**
 * 浏览器里上传重扫件（multipart 同上传：`file_name`、`cover`?、`file`；浏览器不算 sha）。成功 201。
 * 错误：409 `invalid_state`（已经不是已驳回：上一次可能已经成功）、409 `duplicate_content`（和项目里别的文件或
 * 它自己的当前版本相同，details 是位置）、409 `conflict`（版本号刚被改了）、400 `invalid_cover` 等同上传。
 */
export function rescanUpload(fileId: string, parts: UploadParts, opts: UploadOptions = {}) {
  return upload<RescanResponse>(`/api/files/${fileId}/rescan`, parts, opts)
}
