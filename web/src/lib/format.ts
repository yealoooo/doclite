function pad(n: number): string {
  return String(n).padStart(2, '0')
}

/** RFC3339 时间 → 本地时间 `YYYY-MM-DD HH:mm`；空值返回 `—`。 */
export function formatDateTime(value: string | null | undefined): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
