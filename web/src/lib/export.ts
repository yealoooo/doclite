// 导出页（M4-5）用的名称、颜色和小工具：任务状态、格式、预检的不就绪原因、交付后变更的原因、范围的显示，以及
// "新建导出"的地址约定（/catalog 的入口带着范围跳过来）。

import type { ExportFormat, ExportTask } from '@/api/types'
import { type StatusStyle, unknownStatus } from '@/lib/status'

// ---------- 任务状态 ----------

export type TaskStatusInfo = StatusStyle

/** 任务状态的中文名和徽标颜色（排队中、生成中、完成、失败、已取消）。 */
export const TASK_STATUS: Record<string, TaskStatusInfo> = {
  queued: { label: '排队中', badge: 'bg-muted text-muted-foreground' },
  running: { label: '生成中', badge: 'bg-info-soft text-info' },
  done: { label: '完成', badge: 'bg-ok-soft text-ok' },
  failed: { label: '失败', badge: 'bg-destructive-soft text-destructive' },
  canceled: { label: '已取消', badge: 'bg-muted text-muted-foreground' },
  // 不是 server 的状态，是任务行按其他字段显示的：失败后等待重试、取消后正在清理、导出物已过期删除。
  retrying: { label: '等待重试', badge: 'bg-warn-soft text-warn' },
  cleaning: { label: '正在清理', badge: 'bg-muted text-muted-foreground' },
  expired: { label: '已过期', badge: 'bg-muted text-muted-foreground' },
}

export function taskStatusOf(status: string): TaskStatusInfo {
  return TASK_STATUS[status] ?? unknownStatus(status)
}

/** 排队中、生成中：可以取消（已取消而还在清理的不能再取消）。 */
export function isCancelable(task: Pick<ExportTask, 'status'>): boolean {
  return task.status === 'queued' || task.status === 'running'
}

/** 排队中而且已经失败过（等退避后自动重试）。 */
export function isWaitingRetry(task: Pick<ExportTask, 'status' | 'attempts' | 'error_message'>): boolean {
  return task.status === 'queued' && task.attempts > 0 && !!task.error_message
}

/** 导出物已过期（接口的 expired，或者页面开着期间 `expires_at` 到了）。 */
export function isExpired(task: Pick<ExportTask, 'expired' | 'expires_at'>, now: number): boolean {
  if (task.expired) return true
  if (!task.expires_at) return false
  const t = Date.parse(task.expires_at)
  return !Number.isNaN(t) && t <= now
}

/** 进度的百分比（0–100，取整）；总数为 0 时 0。 */
export function progressPercent(task: Pick<ExportTask, 'progress_done' | 'progress_total'>): number {
  if (!task.progress_total) return 0
  return Math.max(0, Math.min(100, Math.floor((task.progress_done / task.progress_total) * 100)))
}

// ---------- 格式 ----------

export interface FormatOption {
  value: ExportFormat | 'pdf_ocr' | 'ofd'
  label: string
  description: string
  /** 以后的版本才有（只显示，不能选）。 */
  future?: boolean
}

/** 格式选项（用户决定 Q1、Q2）；双层 PDF、OFD 只显示为"以后的版本"。 */
export const FORMAT_OPTIONS: FormatOption[] = [
  { value: 'pdf', label: 'PDF', description: '每件一个 PDF，原图原样嵌入（不重新压缩）' },
  { value: 'images', label: '原图包', description: '每件一个文件夹，原图原样，按页号命名' },
  { value: 'pdf_ocr', label: '双层 PDF', description: '带 OCR 文字层，可检索', future: true },
  { value: 'ofd', label: 'OFD', description: '国标版式文件', future: true },
]

export function formatLabel(format: string): string {
  return FORMAT_OPTIONS.find((f) => f.value === format)?.label ?? format
}

// ---------- 预检、交付后变更的原因 ----------

/** 不就绪原因的短名（预检的汇总标签）。 */
export const NOT_READY_LABEL: Record<string, string> = {
  source_modifying: '来源文件夹修改中',
  not_locked: '没有锁定',
  no_archive_no: '没有档号',
  no_pages: '没有页',
  pages_not_approved: '有页不是审核通过',
}

export const NOT_READY_ORDER = ['source_modifying', 'not_locked', 'no_archive_no', 'no_pages', 'pages_not_approved'] as const

/** 交付后变更原因的短名（design §6"交付后变更"的 8 种）。 */
export const CHANGE_LABEL: Record<string, string> = {
  new_version: '有新版本',
  pages_removed: '页被移出',
  pages_moved: '页移到别的条目',
  pages_reordered: '页序变了',
  pages_added: '新加了页',
  not_approved: '现在不是通过',
  item_changed: '著录变了',
  item_deleted: '条目已删除',
}

export const CHANGE_ORDER = [
  'new_version',
  'pages_removed',
  'pages_moved',
  'pages_reordered',
  'pages_added',
  'not_approved',
  'item_changed',
  'item_deleted',
] as const

// ---------- 范围的显示 ----------

/** 条目的称呼：有档号用档号，没有用"第 n 件"。 */
export function itemName(archiveNo: string | null | undefined, itemNo: number): string {
  return archiveNo ? archiveNo : `第 ${itemNo} 件`
}

/** 一个节点的名称链：顶层（根节点）是"全部"。 */
export function categoryChainText(names: readonly string[]): string {
  return names.length ? names.join(' · ') : '全部'
}

/**
 * 任务范围的主标题：节点是名称链（几个节点"<第一个>等 n 个分类"）；条目是第一件的档号（已知时，几件"<档号>等 n 件"），
 * 不知道时"n 件条目"；补交是"补交 n 件"。和下载文件名里的范围名同一个口径（design §6"下载"）。
 */
export function scopeTitle(task: ExportTask, firstItem?: string | null): string {
  if (task.resubmit_of) return `补交 ${task.item_count} 件`
  if (task.scope_type === 'category') {
    const first = task.scope_categories[0]
    const head = first ? categoryChainText(first.category_names) : `${task.scope_ids.length} 个分类`
    return task.scope_categories.length > 1 ? `${head} 等 ${task.scope_categories.length} 个分类` : head
  }
  const n = task.scope_ids.length
  if (firstItem) return n > 1 ? `${firstItem} 等 ${n} 件` : firstItem
  return `${n} 件条目`
}

// ---------- 新建导出的地址约定 ----------
//
// /export?new=1                          空的新建导出
// /export?new=category&ids=<id>,<id>     范围是这些归类节点（/catalog 的节点入口）
// /export?new=item&ids=<id>              范围是这些条目（/catalog 的条目入口）
// /export?resubmit=<任务 id>              补交那个任务的交付后变更
// 另可带 &format=pdf|images、&approved=0（"只导出审核通过的"不勾）。

export interface NewExportDraft {
  mode: 'category' | 'item' | 'resubmit'
  ids: string[]
  resubmitOf: string | null
  format: ExportFormat
  onlyApproved: boolean
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

function str(v: unknown): string | null {
  return typeof v === 'string' ? v : null
}

/** 地址栏 → 新建导出的初始值；不是新建导出时返回 null。 */
export function parseNewExportQuery(query: Record<string, unknown>): NewExportDraft | null {
  const format: ExportFormat = str(query.format) === 'images' ? 'images' : 'pdf'
  const onlyApproved = str(query.approved) !== '0'
  const resubmit = str(query.resubmit)
  if (resubmit && UUID.test(resubmit)) return { mode: 'resubmit', ids: [], resubmitOf: resubmit, format, onlyApproved }
  const kind = str(query.new)
  if (!kind) return null
  const ids = [...new Set((str(query.ids) ?? '').split(',').filter((s) => UUID.test(s)))]
  return { mode: kind === 'item' ? 'item' : 'category', ids, resubmitOf: null, format, onlyApproved }
}

/** 新建导出的地址参数（/catalog 的入口、任务的"重新导出"、"只补交有变更的"用）。 */
export function newExportQuery(draft: Partial<NewExportDraft> & Pick<NewExportDraft, 'mode'>): Record<string, string> {
  const q: Record<string, string> = {}
  if (draft.mode === 'resubmit') {
    if (draft.resubmitOf) q.resubmit = draft.resubmitOf
  } else {
    q.new = draft.ids?.length ? draft.mode : '1'
    if (draft.ids?.length) q.ids = draft.ids.join(',')
  }
  if (draft.format && draft.format !== 'pdf') q.format = draft.format
  if (draft.onlyApproved === false) q.approved = '0'
  return q
}
