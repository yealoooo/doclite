// 导出（M4-1 的预检、建任务、列表、明细、交付后变更、取消，M4-3 的下载；design §6"导出任务（M4-1）""导出的执行
// （M4-3）"、§7"M4-1 新增的接口""M4-3 新增的接口"）。都要 export 菜单 + 能进项目（内置只有负责人和 admin）。
//
// - 检查顺序：菜单码（403）→ 能否进项目（`{pid}` 的：项目不存在 404、没有授权 403；`{id}` 的：任务不存在或项目已删除
//   404、进不了任务所在的项目 403）→ 参数（400）→ 范围里的 id（404，`details.ids`）、补交的任务（404）→ 状态（409
//   `invalid_state`）。
// - 建任务不满足条件（范围里没有条目、一件就绪的都没有、`only_ready` 为 false 而有不就绪的、超过 20 万页）：409
//   `invalid_state`，`details` 和预检的响应一样（界面用它更新预检结果）。
// - 补交："只补交有变更的 n 件"用 `resubmit_of`（不要把 id 列表放进 `scope_ids`：不受 5000 个的限制，范围由 server 在
//   同一个快照里算）。
// - 下载：先调 `/download` 拿预签名地址，再 `location.href = url`（浏览器直接从 S3 下载，不经过 server）。
// - 事件：项目实时通道 `export.changed {task_ids}`（建任务、取消、开始执行、进度（每秒最多一次）、完成、失败、排队重试、
//   取消后收尾完、退出时放回、超时回收），收到后只重新拉这些任务。
//
// 注意 403 `forbidden` 走全局处理（刷新会话、跳无权提示页），调用前先确认有 export 菜单。每个函数的最后一个参数可以传
// `handle`（见 http.ts）。

import { request, requestQuiet, type RequestOptions } from './http'
import type {
  CreateExportTaskBody,
  ExportChanges,
  ExportDownload,
  ExportPrecheck,
  ExportPrecheckBody,
  ExportTask,
  ExportTaskFiles,
  ExportTaskList,
} from './types'

type Handle = RequestOptions['handle']

/** 手选的范围最多多少个节点或条目（server：1–5000）。 */
export const MAX_SCOPE_IDS = 5000
/** 列表每页最多多少个（server：1–200，默认 50）。 */
export const MAX_TASKS_PAGE = 200
/** 明细每页最多多少页（server：1–2000，默认 500）。 */
export const MAX_FILES_PAGE = 2000

/** 预检：范围里的件数、页数，就绪的件数、页数，不就绪的条目和原因（最多 500 件），规模上限。不写库。 */
export function precheckExport(projectId: string, body: ExportPrecheckBody, handle?: Handle) {
  return request<ExportPrecheck>(`/api/projects/${projectId}/export-tasks:precheck`, { method: 'POST', body, handle })
}

/** 建导出任务（201）：冻结范围里就绪条目的每页当时的最新快照，任务排队等执行。 */
export function createExportTask(projectId: string, body: CreateExportTaskBody, handle?: Handle) {
  return request<ExportTask>(`/api/projects/${projectId}/export-tasks`, { method: 'POST', body, handle })
}

/** 任务列表：最新的在前，keyset 分页（cursor 原样传回）。 */
export function listExportTasks(
  projectId: string,
  opts: { limit?: number; cursor?: string | null } = {},
  handle?: Handle,
) {
  return request<ExportTaskList>(`/api/projects/${projectId}/export-tasks`, {
    query: { limit: opts.limit, cursor: opts.cursor },
    handle,
  })
}

export function getExportTask(id: string, handle?: Handle) {
  return request<ExportTask>(`/api/export-tasks/${id}`, { handle })
}

/**
 * 取任务，但 403 `forbidden` 不走全局处理（审查第 1 轮）：地址栏 `?resubmit=<id>` 里的任务可能属于进不了的项目，
 * 这时当作没有这个任务（预检会报 404），不是"你的权限变了"。
 */
export function probeExportTask(id: string, handle?: Handle) {
  return requestQuiet<ExportTask>(`/api/export-tasks/${id}`, { handle })
}

/** 明细：按条目在导出里的序号、页号，keyset 分页；件号、档号、题名、快照都是建任务时冻结的。 */
export function listExportTaskFiles(id: string, opts: { limit?: number; cursor?: string | null } = {}, handle?: Handle) {
  return request<ExportTaskFiles>(`/api/export-tasks/${id}/files`, {
    query: { limit: opts.limit, cursor: opts.cursor },
    handle,
  })
}

/** 交付后变更（按件的原因）：只对 done 的任务，其他状态 409 `invalid_state`。每次实时比较，约 130 ms，按需调。 */
export function getExportTaskChanges(id: string, handle?: Handle) {
  return request<ExportChanges>(`/api/export-tasks/${id}/changes`, { handle })
}

/** 取消：排队中的直接取消；执行中的立即取消、`cleaning_up` 为 true 直到执行器清理完。已完成、失败、已取消的 409。 */
export function cancelExportTask(id: string, handle?: Handle) {
  return request<ExportTask>(`/api/export-tasks/${id}/cancel`, { method: 'POST', handle })
}

/** 下载地址（预签名，签名按天取整）：不是 done、已过期或已清理 409 `invalid_state`；存储停用 409 `storage_unavailable`。 */
export function getExportDownload(id: string, handle?: Handle) {
  return request<ExportDownload>(`/api/export-tasks/${id}/download`, { handle })
}
