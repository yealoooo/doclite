// 壳的上传队列、热文件夹和托盘（M1-9、M1-13）给页面的命令和事件，只在 isShell() 时调用。
// Wails 壳：托盘在 client/internal/shell/tray.go、upload_tray.go，上传队列和热文件夹在 client/internal/upload（M6-2，
// 命令和事件在 client/internal/shell/upload.go、hotfolder.go）；字段说明见 design §5。
//
// - 命令失败时 reject `{kind, message}`（ShellError，见 lib/shell.ts）。
// - 事件用 `listen()`（lib/wails.ts，壳用 app.Event.Emit 发）。

import { invoke, listen } from './wails'

/** 队列项的状态。`queued` 且 `attempts > 0`、有 `next_attempt_at` 表示临时失败后等重试。 */
export type ItemStatus = 'waiting' | 'queued' | 'uploading' | 'done' | 'duplicate' | 'failed'

export const ITEM_STATUSES: readonly ItemStatus[] = ['waiting', 'queued', 'uploading', 'done', 'duplicate', 'failed']

/** 封面情况：已带上；没生成（原因在 cover_reason）；被 server 拒绝后去掉了；null 表示还没到这一步。 */
export type CoverStatus = 'included' | 'not_generated' | 'rejected'

/** 队列里的一项（一个本地文件）。 */
export interface QueueItem {
  id: number
  /** `hotfolder` 或 `manual`（壳里的上传按钮）。 */
  source: string
  server_url: string
  local_path: string
  file_name: string
  /** 相对热文件夹根目录（或上传按钮选的文件夹）的子路径，`/` 连接；根目录里的文件为 ''。 */
  rel_dir: string
  target_folder_id: string
  folder_id: string | null
  size: number
  mtime_ns: number
  sha256: string | null
  status: ItemStatus
  attempts: number
  /** 下次重试的时间（Unix 毫秒）。 */
  next_attempt_at: number | null
  file_id: string | null
  /** server 上的文件名（同名时 server 自动改名，例如 `IMG_9 (1).jpg`）。 */
  server_file_name: string | null
  sort_no: number | null
  cover_status: CoverStatus | null
  cover_reason: string | null
  error_code: string | null
  error_message: string | null
  /** server 的提示：`low_dpi`（DPI 低于 200）、`dpi_unknown`。 */
  warnings: string[]
  /** 内容重复时已有文件的位置。 */
  duplicate_of: { file_id?: string; folder_path?: string; file_name?: string } | null
  created_at: number
  updated_at: number
  finished_at: number | null
}

export interface QueueStats {
  waiting: number
  queued: number
  uploading: number
  done: number
  duplicate: number
  failed: number
  /** 排队里正在等重试的，已经算在 queued 里。 */
  retrying: number
  total: number
}

export interface PauseInfo {
  /** 401：会话失效，重新登录后自动继续；426：壳的版本太低。 */
  reason: 'auth_required' | 'upgrade_required'
  message: string
  /** 426 时是 server 的 details：`{client_version, min_version, latest_version, download_url}`。 */
  details: Record<string, unknown> | null
  since: number
}

export interface QueueStatus {
  /** 当前服务器；统计只算这个服务器的项。 */
  server_url: string | null
  /** 整个队列暂停的原因；null 表示没有暂停。 */
  paused: PauseInfo | null
  /** 被暂停的来源（热文件夹没在运行时有 `hotfolder`）。 */
  paused_sources: string[]
  stats: QueueStats
  /** 正在上传的子文件夹数（≤ 2）。 */
  running: number
}

export interface HotFolderConfig {
  watch_dir: string
  target_folder_id: string
  /** 目标文件夹所属的服务器。 */
  server_url: string
  scan_interval_secs: number
  /** 壳重启后是否自动开始。 */
  running: boolean
}

export interface HotFolderStatus {
  config: HotFolderConfig | null
  running: boolean
  /** 目录事件监控建起来了（false 时只靠定时的全量扫描）。 */
  watching: boolean
  /** 最近的问题（监控目录读不了、服务器换了……）。 */
  error: string | null
  last_scan_at: number | null
  scans: number
}

export interface TrayStatus {
  created: boolean
  error: string | null
  /** 关闭主窗口时缩到托盘。 */
  close_to_tray: boolean
  tooltip: string | null
  status_line: string | null
  counts_line: string | null
  toggle: 'pause' | 'start' | 'unavailable' | null
  /** `--smoke` 时壳启动后模拟一次关窗的结果（窗口只是缩到托盘才算通过）；不是自检或还没做完时为 null。 */
  close_check: { ok: boolean; error: string | null } | null
}

// ---- 命令 ----

export function uploadStatus() {
  return invoke<QueueStatus>('upload_status')
}

/** 最近变化的项（按更新时间倒序），`status` 只要这几种。 */
export function uploadRecent(limit = 100, status?: ItemStatus[]) {
  return invoke<QueueItem[]>('upload_recent', { limit, status: status ?? null })
}

/** 重试失败项（不给 ids 时是全部）。返回重试的个数。 */
export function uploadRetryFailed(ids?: number[]) {
  return invoke<number>('upload_retry_failed', { ids: ids ?? null })
}

/** 清除 401、426 引起的暂停。 */
export function uploadResume() {
  return invoke<QueueStatus>('upload_resume')
}

export function hotfolderStatus() {
  return invoke<HotFolderStatus>('hotfolder_status')
}

/** 用系统对话框选监控目录（壳记住它，`hotfolderSet` 只接受它或现有配置里的目录）。取消时为 null。 */
export function hotfolderPickDir() {
  return invoke<string | null>('hotfolder_pick_dir')
}

export function hotfolderSet(watchDir: string, targetFolderId: string) {
  return invoke<HotFolderStatus>('hotfolder_set', { watchDir, targetFolderId })
}

export function hotfolderStart() {
  return invoke<HotFolderStatus>('hotfolder_start')
}

export function hotfolderPause() {
  return invoke<HotFolderStatus>('hotfolder_pause')
}

export function trayStatus() {
  return invoke<TrayStatus>('tray_status')
}

// ---- 事件 ----

export interface ProgressEvent {
  id: number
  sent: number
  size: number
}

export interface QueueEventHandlers {
  item?: (item: QueueItem) => void
  itemRemoved?: (id: number) => void
  progress?: (p: ProgressEvent) => void
  state?: (s: QueueStatus) => void
  authRequired?: () => void
  upgradeRequired?: (details: Record<string, unknown> | null) => void
  hotfolder?: (s: HotFolderStatus) => void
  /** 托盘菜单上"继续热文件夹"失败（例如监控目录不在了）。 */
  trayError?: (message: string) => void
}

/** 订阅队列、热文件夹和托盘的事件，返回取消订阅的函数。某个事件订阅失败时，已订阅的都取消再抛出。 */
export async function listenQueueEvents(h: QueueEventHandlers): Promise<() => void> {
  const subs: [string, (payload: unknown) => void][] = []
  const add = <T>(name: string, fn: ((p: T) => void) | undefined) => {
    if (fn) subs.push([name, (p) => fn(p as T)])
  }
  add<QueueItem>('upload:item', h.item)
  add<{ id: number }>('upload:item-removed', h.itemRemoved && ((p) => h.itemRemoved!(p.id)))
  add<ProgressEvent>('upload:progress', h.progress)
  add<QueueStatus>('upload:state', h.state)
  add<null>('upload:auth-required', h.authRequired && (() => h.authRequired!()))
  add<{ details: Record<string, unknown> | null }>(
    'upload:upgrade-required',
    h.upgradeRequired && ((p) => h.upgradeRequired!(p?.details ?? null)),
  )
  add<HotFolderStatus>('hotfolder:state', h.hotfolder)
  add<{ message: string }>('tray:error', h.trayError && ((p) => h.trayError!(p.message)))

  const unlistens: (() => void)[] = []
  const stopAll = () => {
    for (const u of unlistens.splice(0)) {
      void Promise.resolve(u()).catch(() => {})
    }
  }
  try {
    for (const [name, fn] of subs) {
      unlistens.push(await listen(name, fn))
    }
  } catch (e) {
    stopAll()
    throw e
  }
  return stopAll
}
