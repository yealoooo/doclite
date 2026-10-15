/** 登录后要回到的路径（`/login?redirect=`）：只接受站内的绝对路径，防止跳到外站。 */
export function safeRedirect(value: unknown): string | null {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//')) return null
  if (value.startsWith('/login')) return null
  return value
}
