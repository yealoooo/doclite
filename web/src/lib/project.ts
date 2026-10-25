// 项目页用到的文字和表单校验，和 server 的 doclite_domain::projects 一致（只做预检，后端仍会校验）。

import type { ColorMode, ProjectStatus, ScanFormat, ScanHint } from '@/api/types'
import { colorModeLabel } from '@/lib/files'
import type { StatusStyle } from '@/lib/status'

import { charCount, CONTROL } from './validate'

export const PROJECT_STATUS_LABEL: Record<ProjectStatus, string> = {
  active: '进行中',
  archived: '已归档',
}

export const PROJECT_STATUS: Record<ProjectStatus, StatusStyle> = {
  active: { label: PROJECT_STATUS_LABEL.active, badge: 'bg-ok-soft text-ok' },
  archived: { label: PROJECT_STATUS_LABEL.archived, badge: 'bg-muted text-muted-foreground' },
}

export const MAX_PROJECT_CODE_CHARS = 32
export const MAX_PROJECT_NAME_CHARS = 100
export const MAX_PROJECT_DESCRIPTION_CHARS = 500

/** 项目编码：英文字母或数字开头，只能有英文字母、数字和 . _ -，最长 32 个字符；建好后不能改。 */
export function checkProjectCode(value: string): string {
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/.test(value.trim())) {
    return `项目编码要以英文字母或数字开头，只能包含英文字母、数字和 . _ -，最长 ${MAX_PROJECT_CODE_CHARS} 个字符`
  }
  return ''
}

/** 项目名称：它也是根文件夹的名称，按文件夹名的规则（design §1.7）。 */
export function checkProjectName(value: string): string {
  const v = value.trim()
  if (!v || charCount(v) > MAX_PROJECT_NAME_CHARS) return `项目名称不能为空，最长 ${MAX_PROJECT_NAME_CHARS} 个字符`
  const bad = /[\\/:*?<>|"]/.exec(v)
  if (bad) return `项目名称不能含 ${bad[0]}（它也是根文件夹的名称，不能含 \\ / : * ? < > | 和双引号）`
  if (CONTROL.test(v)) return '项目名称不能含控制字符'
  if (v.endsWith('.')) return '项目名称不能以点结尾（它也是根文件夹的名称）'
  return ''
}

export function checkProjectDescription(value: string): string {
  if (charCount(value.trim()) > MAX_PROJECT_DESCRIPTION_CHARS) return `说明最长 ${MAX_PROJECT_DESCRIPTION_CHARS} 个字符`
  return ''
}

// ---------- 扫描参数建议（M11-5，settings.scan_hint） ----------

export const SCAN_COLOR_MODES: ColorMode[] = ['color', 'gray', 'bw']
export const SCAN_FORMATS: ScanFormat[] = ['jpeg', 'png', 'tiff']
export const SCAN_FORMAT_LABEL: Record<ScanFormat, string> = { jpeg: 'JPEG', png: 'PNG', tiff: 'TIFF' }
export const MIN_SCAN_DPI = 100
export const MAX_SCAN_DPI = 1200

/** 从项目详情的 settings 读扫描参数建议（宽松：不认识的值当作没设，和 server 读时一样）。 */
export function scanHintFromSettings(settings: Record<string, unknown>): ScanHint {
  const raw = settings.scan_hint
  const h = raw && typeof raw === 'object' && !Array.isArray(raw) ? (raw as Record<string, unknown>) : {}
  const dpi = h.dpi
  return {
    color_mode: SCAN_COLOR_MODES.includes(h.color_mode as ColorMode) ? (h.color_mode as ColorMode) : null,
    dpi: Number.isInteger(dpi) && (dpi as number) >= MIN_SCAN_DPI && (dpi as number) <= MAX_SCAN_DPI ? (dpi as number) : null,
    format: SCAN_FORMATS.includes(h.format as ScanFormat) ? (h.format as ScanFormat) : null,
  }
}

/** 扫描参数建议的文字：`彩色 · 300 dpi · JPEG`（只有设了的项）；都没设时为空串。 */
export function formatScanHint(h: ScanHint | null | undefined): string {
  if (!h) return ''
  const parts: string[] = []
  if (h.color_mode) parts.push(colorModeLabel(h.color_mode))
  if (h.dpi) parts.push(`${h.dpi} dpi`)
  if (h.format) parts.push(SCAN_FORMAT_LABEL[h.format] ?? h.format)
  return parts.join(' · ')
}

/** 建议的 DPI（输入框原文，空表示不建议）。 */
export function checkScanDpi(value: string): string {
  const v = value.trim()
  if (v === '') return ''
  if (!/^[0-9]+$/.test(v) || Number(v) < MIN_SCAN_DPI || Number(v) > MAX_SCAN_DPI) {
    return `扫描参数建议的 DPI 要是 ${MIN_SCAN_DPI}–${MAX_SCAN_DPI} 的整数，不建议就留空`
  }
  return ''
}
