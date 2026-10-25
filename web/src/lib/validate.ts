// 表单校验，和 server 的规则一致（doclite_domain::user_admin、doclite_domain::roles）。
// 长度按 Unicode 字符（码点）计数，和后端的 `chars().count()` 一致；不能用 `.length`（UTF-16 单元，
// 一个生僻字或表情符号算 2 个）。只做体验上的预检，后端仍会校验。

export const MAX_USERNAME_CHARS = 64
export const MAX_DISPLAY_NAME_CHARS = 64
export const MIN_PASSWORD_CHARS = 6
export const MAX_PASSWORD_CHARS = 128
export const MAX_KEYWORD_CHARS = 64
export const MAX_ROLE_NAME_CHARS = 64
export const MAX_DESCRIPTION_CHARS = 500

/** 字符数（按码点）。 */
export function charCount(s: string): number {
  return [...s].length
}

// 控制字符（Unicode Cc 类：C0、DEL、C1），和 server 一致。
export const CONTROL = /[\u0000-\u001f\u007f-\u009f]/
/** 同上，但换行、回车、制表符可以（多行的备注、说明）。 */
export const CONTROL_EXCEPT_NEWLINE = /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/

/** 登录名：去掉首尾空白后不能为空，不能含空白或控制字符，最长 64 个字符。返回错误说明，合法时返回空字符串。 */
export function checkUsername(value: string): string {
  const v = value.trim()
  if (!v || charCount(v) > MAX_USERNAME_CHARS || /\s/u.test(v) || CONTROL.test(v)) {
    return `登录名不能为空，不能包含空格，最长 ${MAX_USERNAME_CHARS} 个字符`
  }
  return ''
}

/** 姓名：去掉首尾空白后不能为空，不能含控制字符，最长 64 个字符。 */
export function checkDisplayName(value: string): string {
  const v = value.trim()
  if (!v || charCount(v) > MAX_DISPLAY_NAME_CHARS || CONTROL.test(v)) {
    return `姓名不能为空，最长 ${MAX_DISPLAY_NAME_CHARS} 个字符`
  }
  return ''
}

/** 密码：6 到 128 个字符，原样使用（不去空白）。 */
export function checkPassword(value: string): string {
  const n = charCount(value)
  if (n < MIN_PASSWORD_CHARS || n > MAX_PASSWORD_CHARS) {
    return `密码要 ${MIN_PASSWORD_CHARS} 到 ${MAX_PASSWORD_CHARS} 个字符`
  }
  return ''
}

/** 角色编码：小写字母开头，只能有小写字母、数字和下划线，最长 32 个字符。 */
export function checkRoleCode(value: string): string {
  if (!/^[a-z][a-z0-9_]{0,31}$/.test(value.trim())) {
    return '角色编码要以小写字母开头，只能有小写字母、数字和下划线，最长 32 个字符'
  }
  return ''
}

/** 角色名称：去掉首尾空白后不能为空，最长 64 个字符。 */
export function checkRoleName(value: string): string {
  const v = value.trim()
  if (!v || charCount(v) > MAX_ROLE_NAME_CHARS) return `角色名称不能为空，最长 ${MAX_ROLE_NAME_CHARS} 个字符`
  return ''
}

/** 角色说明：去掉首尾空白后最长 500 个字符（空的当作没有）。 */
export function checkDescription(value: string): string {
  if (charCount(value.trim()) > MAX_DESCRIPTION_CHARS) return `说明最长 ${MAX_DESCRIPTION_CHARS} 个字符`
  return ''
}

/** 用户搜索关键字：去掉首尾空白后最长 64 个字符。 */
export function checkKeyword(value: string): string {
  if (charCount(value.trim()) > MAX_KEYWORD_CHARS) return `搜索关键字最长 ${MAX_KEYWORD_CHARS} 个字符`
  return ''
}

// ---------- 文件名和文件夹名（design §1.7，和 server 的 doclite_domain::tree::validate_name 一致；M1-12） ----------

export const MAX_ENTRY_NAME_CHARS = 255
export const MAX_REMARK_CHARS = 500

const FORBIDDEN_NAME_CHARS = /[\\/:*?"<>|]/u

/** Windows 保留名：第一个点之前的部分去掉尾部空格后，不区分大小写等于 CON、PRN、AUX、NUL、COM1–9、LPT1–9。 */
export function isWindowsReservedName(name: string): boolean {
  const stem = (name.split('.')[0] ?? name).replace(/ +$/, '')
  return /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/i.test(stem)
}

/** 文件名、文件夹名（原样校验，不去空白）。返回错误说明，合法时返回空字符串。 */
export function checkEntryName(name: string, kind: 'file' | 'folder'): string {
  const label = kind === 'file' ? '文件名' : '文件夹名'
  if (!name) return `${label}不能为空`
  if (charCount(name) > MAX_ENTRY_NAME_CHARS) return `${label}最长 ${MAX_ENTRY_NAME_CHARS} 个字符`
  if (name === '.' || name === '..') return `${label}不能是 . 或 ..`
  const bad = name.match(FORBIDDEN_NAME_CHARS)
  if (bad) return `${label}不能包含 ${bad[0]}（不能用的字符：\\ / : * ? < > | 和双引号）`
  if (CONTROL.test(name)) return `${label}不能包含控制字符（换行、制表符等）`
  if (/^\s|\s$/u.test(name)) return `${label}开头和结尾不能是空白`
  if (name.endsWith('.')) return `${label}不能以点结尾`
  if (isWindowsReservedName(name)) {
    return `${label}不能是 Windows 保留名（CON、PRN、AUX、NUL、COM1–COM9、LPT1–LPT9，不区分大小写，带扩展名也不行）`
  }
  return ''
}

/** 文件夹备注（原件位置等）：去掉首尾空白后最长 500 个字符，不能有控制字符（换行、回车、制表符可以）。 */
export function checkRemark(value: string): string {
  const v = value.trim()
  if (charCount(v) > MAX_REMARK_CHARS) return `备注最长 ${MAX_REMARK_CHARS} 个字符`
  if (CONTROL_EXCEPT_NEWLINE.test(v)) return '备注不能包含控制字符'
  return ''
}
