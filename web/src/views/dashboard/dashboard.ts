// 看板的取数和显示文字（M3-7，用户决定 Q6；KPI 取法见 roadmap M3-4 的须知）。

import type { CatalogCounts, CatalogRowCounts, ReviewStatus } from '@/api/types'
import { REVIEW_STATUS } from '@/lib/files'

import type { BarSegment } from './StackedBar.vue'

/** 矩阵的行（审核状态），按流程顺序。 */
export const REVIEW_ROWS: ReviewStatus[] = ['unassigned', 'pending', 'approved', 'rejected']

/** 矩阵的列（归类状态）。 */
export type CatalogColumn = 'uncataloged' | 'unlocked' | 'locked'
export const CATALOG_COLUMNS: CatalogColumn[] = ['uncataloged', 'unlocked', 'locked']
export const CATALOG_COLUMN_LABEL: Record<CatalogColumn, string> = {
  uncataloged: '未归档',
  unlocked: '已归档未锁定',
  locked: '已锁定',
}

/** 审核状态的颜色（和 /files、/review-assign 的状态点一致）。 */
export const REVIEW_COLOR: Record<ReviewStatus, string> = {
  approved: 'bg-ok',
  pending: 'bg-warn',
  rejected: 'bg-destructive',
  unassigned: 'bg-muted-foreground/35',
}

/** 归档状态的颜色：一种色相由浅到深（未归档是底色）。 */
export const CATALOG_COLOR: Record<CatalogColumn, string> = {
  locked: 'bg-violet-700',
  unlocked: 'bg-violet-300',
  uncataloged: 'bg-muted',
}

/** 审核进度条：通过、待审、驳回、待分配。 */
export function reviewSegments(c: CatalogCounts): BarSegment[] {
  return (['approved', 'pending', 'rejected', 'unassigned'] as ReviewStatus[]).map((s) => ({
    key: s,
    label: REVIEW_STATUS[s].label,
    value: c.review[s],
    color: REVIEW_COLOR[s],
  }))
}

/** 归档进度条：已锁定、已归档未锁定（含修改中；其余是底色：未归档）。 */
export function catalogSegments(c: CatalogCounts): BarSegment[] {
  return (['locked', 'unlocked'] as CatalogColumn[]).map((k) => ({
    key: k,
    label: CATALOG_COLUMN_LABEL[k],
    value: c.catalog[k],
    color: CATALOG_COLOR[k],
  }))
}

/** 矩阵里一格属于"可导出"吗（按 `export.require_locked_item`：true 只算已锁定，false 算已归档）。 */
export function isExportableColumn(col: CatalogColumn, requireLocked: boolean): boolean {
  return col === 'locked' || (!requireLocked && col === 'unlocked')
}

/** 矩阵每格的一句说明（plan.html 看板 Mockup 的写法）。 */
export function cellNote(row: ReviewStatus, col: CatalogColumn, requireLocked: boolean): string {
  if (row === 'approved') {
    if (col === 'uncataloged') return '待归档'
    if (isExportableColumn(col, requireLocked)) return '可导出'
    return '待锁定'
  }
  if (row === 'rejected') return col === 'uncataloged' ? '等重扫' : '重扫后自动跟随'
  if (col === 'uncataloged') return row === 'unassigned' ? '刚入库' : '审核中'
  return '归档先行'
}

/** 占比（0–100 的整数；分母为 0 时 null）。 */
export function percent(part: number, total: number): number | null {
  if (total <= 0) return null
  const p = (part / total) * 100
  // 没到 100% 时不显示成 100%，有一点时不显示成 0%。
  if (part > 0 && p < 1) return 1
  if (part < total && p > 99) return 99
  return Math.round(p)
}

export function percentText(part: number, total: number): string {
  const p = percent(part, total)
  return p === null ? '—' : `${p}%`
}

/** 数字加千分位；没有时 `…`。 */
export function num(v: number | null | undefined): string {
  return v == null ? '…' : v.toLocaleString()
}

/** 矩阵一行的取值。 */
export function rowValue(r: CatalogRowCounts, col: CatalogColumn): number {
  return r[col]
}
