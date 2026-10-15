// 驳回原因的显示名（design §2.2、用户决定 Q4）：内置的 8 个存代码，前端翻成中文；项目自定义的原因直接存中文，原样显示。
// 顺序同项目设置的默认值（server `default_settings()` 的 review.reject_reasons）。
// 这是唯一的一份（M2-6 合并）：lib/reviews.ts 的 `REJECT_REASON_LABELS`、`rejectReasonLabel` 从这里转出去。

export const BUILTIN_REJECT_REASONS: Readonly<Record<string, string>> = {
  blur: '模糊',
  skew: '倾斜',
  cut: '裁切过度',
  stain: '污渍',
  missing: '缺页',
  dup: '重页',
  orient: '方向',
  other: '其他',
}

/** 一个原因的显示名：内置代码翻成中文，其余原样。 */
export function rejectReasonLabel(reason: string): string {
  return BUILTIN_REJECT_REASONS[reason] ?? reason
}

/** 一组原因连起来：`模糊、倾斜`。 */
export function rejectReasonsText(reasons: readonly string[] | null | undefined): string {
  return (reasons ?? []).map(rejectReasonLabel).join('、')
}

/** 选了它时说明必填（Q4）。 */
export const OTHER_REJECT_REASON = 'other'
