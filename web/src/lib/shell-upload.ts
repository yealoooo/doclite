// 壳的上传队列（上一版 M1-9 的命令；Wails 壳在 M6-2 实现，M6-1 的壳返回 unsupported），只在 isShell() 时调用。
// M1-12 的文件目录页只用 `pick_and_enqueue_upload`；队列状态、最近文件、重试等命令和事件给 M1-13 的 /scan 页。
// 命令失败时 reject `{kind, message}`（kind：not_configured、invalid_request、internal），用 shellErrorMessage() 取说明。

import { invoke } from './wails'

export type PickMode = 'files' | 'folder'

export interface PickSkipped {
  /** 本机路径。 */
  path: string
  /** 为什么没加入（例如不支持的扩展名、读不到）。 */
  reason: string
}

/** `pick_and_enqueue_upload` 的结果。 */
export interface PickResult {
  /** 用户在系统对话框里点了取消。 */
  cancelled: boolean
  /** 新加入队列的项的 id。 */
  added: number[]
  skipped: PickSkipped[]
}

/**
 * 用系统对话框选文件（可多选）或一个文件夹，加入壳的上传队列，传到 `folderId`。
 * 选文件夹时连同它本身按相对路径逐层建子文件夹（和浏览器的"上传文件夹"一样）。上传进度在 /scan 页看。
 */
export function pickAndEnqueueUpload(folderId: string, mode: PickMode) {
  return invoke<PickResult>('pick_and_enqueue_upload', { folderId, mode })
}
