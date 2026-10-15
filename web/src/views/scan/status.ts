// /scan 页的显示文字：队列项的状态、错误原因、封面情况（M1-13）。字段含义见 lib/scan-queue.ts。

import type { ItemStatus, ProgressEvent, QueueItem } from '@/lib/scan-queue'

/** 计数格子和筛选用的状态名（顺序就是页面上的顺序）。 */
export const STATUS_TILES: { status: ItemStatus; label: string }[] = [
  { status: 'waiting', label: '等待写入完成' },
  { status: 'queued', label: '排队' },
  { status: 'uploading', label: '上传中' },
  { status: 'done', label: '已入库' },
  { status: 'duplicate', label: '内容重复，已跳过' },
  { status: 'failed', label: '失败' },
]

/** 壳在本地产生的错误代码（其余是 server 的 code，说明直接用 error_message）。 */
const LOCAL_ERRORS: Record<string, string> = {
  file_missing: '本地文件不见了',
  file_unreadable: '读不了本地文件',
  file_changed: '文件在上传时又被改了',
  file_locked: '文件被其他程序占用',
  incomplete_file: '文件不完整（扫描软件没写完）',
  folder_rejected: '服务器拒绝建这个子文件夹',
  target_not_found: '目标文件夹已被删除，请重新选择目标文件夹',
  network: '连不上服务器',
  auth_required: '登录已失效',
  upgrade_required: '客户端版本过低',
  shell_internal: '客户端内部出错',
}

/** 错误原因：优先用壳记下的说明（中文），没有时按代码给一句。 */
export function errorText(item: Pick<QueueItem, 'error_code' | 'error_message'>): string {
  if (item.error_message) return item.error_message
  if (item.error_code) return LOCAL_ERRORS[item.error_code] ?? item.error_code
  return '失败'
}

/** 等重试（临时失败过）。 */
export function isRetrying(item: QueueItem): boolean {
  return item.status === 'queued' && item.attempts > 0 && item.next_attempt_at != null
}

/** 封面没带上（没生成、被 server 拒绝后去掉）。 */
export function missingCover(item: QueueItem): boolean {
  return item.cover_status === 'not_generated' || item.cover_status === 'rejected'
}

export function coverNote(item: QueueItem): string {
  const why = item.cover_reason ? `：${item.cover_reason}` : ''
  return item.cover_status === 'rejected' ? `封面被服务器拒绝，已去掉封面上传${why}` : `没有生成封面${why}`
}

export function isLowDpi(item: QueueItem): boolean {
  return item.warnings.includes('low_dpi')
}

/** 进度条宽度（0–100）。 */
export function progressPercent(item: QueueItem, p: ProgressEvent | null): number {
  if (item.status === 'done' || item.status === 'duplicate') return 100
  if (item.status !== 'uploading' || !p || p.size <= 0) return 0
  return Math.min(100, Math.round((p.sent / p.size) * 100))
}

/** 状态那一列的文字。 */
export function statusText(item: QueueItem, p: ProgressEvent | null, now: number): string {
  switch (item.status) {
    case 'waiting':
      return item.error_code === 'file_locked' ? '等待写入完成 · 文件被其他程序占用' : '等待写入完成'
    case 'queued':
      if (isRetrying(item)) {
        const secs = Math.max(0, Math.ceil((item.next_attempt_at! - now) / 1000))
        return secs > 0 ? `第 ${item.attempts} 次失败，${secs} 秒后重试` : `第 ${item.attempts} 次失败，正在重试`
      }
      return '排队'
    case 'uploading':
      return p ? `上传中 ${progressPercent(item, p)}%` : '准备上传（预检、生成封面）'
    case 'done':
      return '已入库'
    case 'duplicate':
      return '内容重复，已跳过'
    case 'failed':
      return errorText(item)
  }
}

/** 内容重复时已有文件的位置（server 给的文件夹路径加文件名，例如 `演示项目 A/2025 文书档案/盒 001/IMG_3.jpg`）。 */
export function duplicateOf(item: QueueItem): string | null {
  const d = item.duplicate_of
  if (!d?.file_name) return null
  const dir = (d.folder_path ?? '').replace(/\/+$/, '')
  return dir ? `${dir}/${d.file_name}` : d.file_name
}

/** 本地子路径 + 文件名，用来在一行里显示"盒 WS-2025-001 / IMG_1.jpg"。 */
export function localLabel(item: QueueItem): { dir: string; name: string } {
  return { dir: item.rel_dir ? item.rel_dir.split('/').join(' / ') : '', name: item.file_name }
}
