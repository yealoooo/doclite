// 时间、数字的显示（各页面共用）。

/** 两位数字，不够补 0。 */
export function pad2(n: number): string {
  return String(n).padStart(2, '0')
}

type TimeValue = string | number | Date | null | undefined

/** RFC3339 时间、毫秒数或 Date → 本地时间 `YYYY-MM-DD HH:mm`（`seconds` 时带秒）；空值返回 `—`，解析不了的原样返回。 */
export function formatDateTime(value: TimeValue, opts: { seconds?: boolean } = {}): string {
  if (value === null || value === undefined || value === '') return '—'
  const d = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(d.getTime())) return String(value)
  const s = opts.seconds ? `:${pad2(d.getSeconds())}` : ''
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}${s}`
}

/** 本地时间 `HH:mm:ss`；空值返回空串。 */
export function formatClock(value: TimeValue): string {
  if (value === null || value === undefined || value === '') return ''
  const d = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`
}

/** 数字加千分位；没有时 `…`（还在加载）。 */
export function formatCount(v: number | null | undefined): string {
  return v == null ? '…' : v.toLocaleString()
}
