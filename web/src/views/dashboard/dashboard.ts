// 看板的显示文字（M3-7；2026-10-19 用户决定简化：一行五个数 + 按文件夹的表，审核按页，归档、锁定、导出按件）。

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

/** 进度条的宽度（CSS 百分比；分母为 0 时 0%）。 */
export function barWidth(part: number, total: number): string {
  return `${percent(part, total) ?? 0}%`
}

/** 数字加千分位；没有时 `…`。 */
export function num(v: number | null | undefined): string {
  return v == null ? '…' : v.toLocaleString()
}

/** "可导出"的说明（和导出预检勾"只导出审核通过的"时的"就绪"相同，按 `export.require_locked_item`）。 */
export function exportableText(requireLocked: boolean): { short: string; long: string } {
  return requireLocked
    ? { short: '已锁定且全部页审核通过', long: '已锁定、全部页审核通过、有档号、不在修改中的件（和导出预检勾"只导出审核通过的"时的就绪相同）' }
    : {
        short: '已归档且全部页审核通过',
        long: '全部页审核通过、有档号、不在修改中的件；项目设置不要求锁定（和导出预检勾"只导出审核通过的"时的就绪相同）',
      }
}
