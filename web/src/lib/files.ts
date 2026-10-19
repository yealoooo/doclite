// 文件目录的显示文字：审核状态、颜色模式、快照类型、文件日志、大小和规格（M1-10；审核动作的日志说明 M2-5；
// 挂接、移出的日志说明 M3-7）。

import type { ColorMode, FileLog, ReviewStatus, SnapshotImageFields } from '@/api/types'
import { rejectReasonLabel } from '@/lib/reviews'
import { type StatusStyle, unknownStatus } from '@/lib/status'

/** 审核状态的文字和颜色（design §2.2）。 */
export const REVIEW_STATUS: Record<ReviewStatus, Required<Pick<StatusStyle, 'label' | 'badge' | 'dot'>>> = {
  unassigned: { label: '待分配', dot: 'bg-muted-foreground/45', badge: 'bg-muted text-muted-foreground' },
  pending: { label: '待审', dot: 'bg-warn', badge: 'bg-warn-soft text-warn' },
  approved: { label: '通过', dot: 'bg-ok', badge: 'bg-ok-soft text-ok' },
  rejected: { label: '驳回', dot: 'bg-destructive', badge: 'bg-destructive-soft text-destructive' },
}

export function reviewStatusOf(status: string): StatusStyle {
  return REVIEW_STATUS[status as ReviewStatus] ?? unknownStatus(status)
}

const COLOR_MODE: Record<ColorMode, string> = { bw: '黑白', gray: '灰度', color: '彩色' }

export function colorModeLabel(mode: string | null | undefined): string {
  if (!mode) return '未知'
  return COLOR_MODE[mode as ColorMode] ?? mode
}

const SNAPSHOT_TYPE: Record<string, string> = {
  original: '原始上传',
  rescan: '重扫',
  copy: '复制',
  edit: '编辑',
  restore: '恢复',
}

export function snapshotTypeLabel(type: string): string {
  return SNAPSHOT_TYPE[type] ?? type
}

/** DPI 低于这个值时提示（design §4.3 的 `low_dpi`）。 */
export const LOW_DPI = 200

export function isLowDpi(dpi: number | null | undefined): boolean {
  return dpi != null && dpi < LOW_DPI
}

/** 字节数 → `2.3 MB`（按 1024 换算）。 */
export function formatBytes(n: number | null | undefined): string {
  if (n == null) return '—'
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`
}

/** 规格：`2480×3508 · 300 dpi · 灰度`（缺的部分不显示，DPI 未知写"DPI 未知"）。 */
export function formatSpec(info: Pick<SnapshotImageFields, 'width_px' | 'height_px' | 'dpi' | 'color_mode'>, withColor = true): string {
  const parts: string[] = []
  if (info.width_px && info.height_px) parts.push(`${info.width_px}×${info.height_px}`)
  parts.push(info.dpi != null ? `${info.dpi} dpi` : 'DPI 未知')
  if (withColor && info.color_mode) parts.push(colorModeLabel(info.color_mode))
  return parts.join(' · ')
}

const LOG_ACTION: Record<string, string> = {
  upload: '上传',
  rescan: '重扫',
  rename: '改名',
  move: '移动',
  copy: '复制',
  delete: '删除',
  assign_review: '分配审核',
  reassign_review: '改派审核',
  approve: '审核通过',
  reject: '驳回',
  reopen: '撤回通过',
  link: '归档',
  unlink: '移出条目',
  export: '导出',
  edit: '编辑',
  restore: '恢复',
}

export function logActionLabel(action: string): string {
  return LOG_ACTION[action] ?? action
}

function str(v: unknown): string | null {
  return typeof v === 'string' && v ? v : typeof v === 'number' ? String(v) : null
}

/** 文件夹路径：detail 里的 folder_path 从根文件夹起用 / 连接，显示时加上开头的 /。 */
function path(v: unknown): string | null {
  const s = str(v)
  return s === null ? null : s.startsWith('/') ? s : `/${s}`
}

/** 审核动作的版本和轮次：`v2 · 第 2 轮`（detail 的 version_no、review_round，design §2.2）。 */
function versionRound(d: Record<string, unknown>): string {
  const v = str(d.version_no)
  const round = str(d.review_round)
  return [v ? `v${v}` : '', round ? `第 ${round} 轮` : ''].filter(Boolean).join(' · ')
}

/** 几段说明用 ` · ` 连起来（空的不要）。 */
function joinParts(...parts: (string | null | false | undefined)[]): string {
  return parts.filter((p): p is string => !!p).join(' · ')
}

/** 驳回原因：内置代码翻成中文，自定义的原样。 */
function reasons(v: unknown): string {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string').map(rejectReasonLabel).join('、') : ''
}

/**
 * 一条文件日志的说明（不认识的 action 返回空串）。detail 的字段见 roadmap M1-7、M1-8 的后续须知和
 * design §2.2 的"接口细化（M2-1）"：审核动作都带 file_name、folder_path、version_no、review_round。
 * 操作人和时间由调用方另外显示，这里不重复。
 */
export function logSummary(log: FileLog): string {
  const d = log.detail ?? {}
  switch (log.action) {
    case 'upload': {
      const where = path(d.folder_path) ?? str(d.folder)
      const renamed = str(d.upload_name) && str(d.file_name) && d.upload_name !== d.file_name
      return [where ? `上传到 ${where}` : '', renamed ? `（原名 ${str(d.upload_name)}，重名改为 ${str(d.file_name)}）` : '']
        .join('')
        .trim()
    }
    case 'rescan': {
      const v = str(d.version_no)
      const round = str(d.review_round)
      const head = [v ? `新版本 v${v}` : '', round ? `第 ${round} 轮审核` : ''].filter(Boolean).join('，')
      // 重扫后自动再分配（M2-1）：分给了谁；最近一次驳回它的审核人不能再审时没有分配（skipped_reviewer）。
      let tail = ''
      if (str(d.skipped_reviewer)) tail = `${str(d.skipped_reviewer)} 已不能再审这个项目，改为待分配`
      else if (str(d.reviewer)) tail = `自动分给 ${str(d.reviewer)}`
      else if (d.review_status === 'unassigned') tail = '待分配'
      return [head, tail].filter(Boolean).join('，')
    }
    case 'assign_review':
      return joinParts(str(d.reviewer) ? `分配给 ${str(d.reviewer)}` : '', versionRound(d))
    case 'reassign_review':
      return joinParts(`从 ${str(d.from) ?? '?'} 改派给 ${str(d.to) ?? '?'}`, versionRound(d))
    // 动作名（"审核通过""驳回"）由调用方显示，这里只写审的版本、原因和说明。
    case 'approve':
      return joinParts(versionRound(d), d.batch === true && '批量通过', str(d.comment) && `说明：${str(d.comment)}`)
    case 'reject':
      return joinParts(
        reasons(d.reasons) && `原因：${reasons(d.reasons)}`,
        str(d.comment) && `说明：${str(d.comment)}`,
        versionRound(d),
      )
    case 'reopen': {
      // 撤回通过（Q8）：重新分给通过它的审核人；他不能再审这个项目时没有分配、改为待分配（skipped_reviewer）。
      let what = '改为待分配'
      if (str(d.skipped_reviewer)) what = `${str(d.skipped_reviewer)} 已不能再审这个项目，改为待分配`
      else if (str(d.reviewer)) what = `重新分给 ${str(d.reviewer)}`
      return joinParts(what, versionRound(d))
    }
    case 'rename':
      return `${str(d.from) ?? '?'} → ${str(d.to) ?? '?'}`
    case 'move': {
      const s = `${path(d.from_path) ?? str(d.from) ?? '?'} → ${path(d.to_path) ?? str(d.to) ?? '?'}`
      return str(d.renamed_from) ? `${s}（重名，${str(d.renamed_from)} 改为 ${str(d.file_name)}）` : s
    }
    case 'copy': {
      const from = [path(d.source_folder_path) ?? str(d.source_folder), str(d.source_file_name)].filter(Boolean).join('/')
      return from ? `复制自 ${from}` : ''
    }
    case 'delete': {
      const where = path(d.folder_path) ?? str(d.folder)
      return where ? `从 ${where} 删除` : ''
    }
    // 挂接、移出条目（M3-7 补；日志由 M3-3 写，detail 按 roadmap M3 全局约定：item_id、archive_no、title、page_no、
    // file_name、folder_path、version_no、review_status，都是当时的值）。档号为空时有 item_no 就写"第 n 件"。
    // 动作名（"挂接""移出条目"）由调用方显示。删除条目时一并移出的 unlink 带 `item_deleted: true`（M3-3），
    // 写成"条目已删除，从 … 移出"（M3-6）。
    case 'link':
    case 'unlink': {
      const no = str(d.archive_no) ?? (str(d.item_no) ? `第 ${str(d.item_no)} 件` : '（档号为空的条目）')
      const page = str(d.page_no)
      const deleted = log.action === 'unlink' && d.item_deleted === true
      const head =
        log.action === 'link'
          ? `挂到 ${no}${page ? ` 第 ${page} 页` : ''}`
          : `${deleted ? '条目已删除，' : ''}从 ${no} 移出${page ? `（原第 ${page} 页）` : ''}`
      const status = typeof d.review_status === 'string' && d.review_status ? reviewStatusOf(d.review_status).label : null
      return joinParts(
        head,
        str(d.title) && `题名：${str(d.title)}`,
        str(d.version_no) && `v${str(d.version_no)}`,
        status && `当时审核：${status}`,
      )
    }
    default:
      return ''
  }
}
