// 归类的显示文字和档号模板（M3-7）：
// - 归类节点类型、著录项类型的中文名（条目状态的中文名见下面的 ITEM_STATUS，M3-5）；
// - 档号模板的解析和预览：server 没有预览接口（M3-1 须知），这里按 design §2.5 的规则在前端拼，规则和
//   `doclite_domain::archive_no::ArchiveNoTemplate::parse` 一一对应（占位符、件号补零只认 `{item_no}`、
//   `{item_no:01}`–`{item_no:09}`、`{item_no:010}`，`{item_no}` 正好一次，节点占位符各最多一次，相邻占位符之间要有
//   分隔符，文字部分不能有空白、控制字符和 `\ / : * ? " < > |`，1–200 个字符）。保存时仍以 server 的校验为准。
// - 分隔符：文字部分里所有不是字母、数字的字符（Unicode，和 Rust 的 `char::is_alphanumeric` 一致）；归类编码
//   不能含它们。
// - 一个文件挂在哪个条目的显示（用户决定 Q10：只给档号、题名、第几页；档号为空时"第 n 件"）。
// - M3-5：条目状态的中文名（全 web 只在这里定义一份，和 server `ItemStatus::label` 一致）、著录的前端校验（题名、
//   每种著录项，和 `doclite_domain::archive_items::{check_title, check_value}` 一致）、归类节点编码和名称的前端校验
//   （`doclite_domain::catalog`）、档号为空的原因的简短说明、400 `invalid_input` 的 `details.fields`。
//
// 截图脚本会在 Node 里直接载入这个文件（类型擦除），这里只能有 `import type`，不能引用运行时的模块。

import type {
  ArchiveNoReason,
  CatalogPlacement,
  CatalogSettings,
  FieldIssue,
  ItemFieldDef,
  ItemFieldType,
  ItemStatus,
} from '@/api/types'

/** 能新建、能用在模板里的归类节点类型（不含 root），按层级的常见顺序。 */
export const CATEGORY_TYPES = ['fonds', 'category', 'year', 'retention', 'org', 'custom'] as const
export type CategoryType = (typeof CATEGORY_TYPES)[number]

export const CATEGORY_TYPE_LABEL: Record<string, string> = {
  root: '根节点',
  fonds: '全宗',
  category: '门类',
  year: '年度',
  retention: '保管期限',
  org: '机构',
  custom: '自定义',
}

export function categoryTypeLabel(t: string): string {
  return CATEGORY_TYPE_LABEL[t] ?? t
}

export const ITEM_FIELD_TYPE_LABEL: Record<ItemFieldType, string> = {
  text: '文本',
  date: '日期',
  enum: '选项',
}

// ---------- 档号模板 ----------

/** 默认模板（`doclite_domain::projects::default_settings`）。 */
export const DEFAULT_ARCHIVE_NO_TEMPLATE = '{fonds}-{category}·{year}-{retention}-{org}-{item_no:04}'
export const MAX_TEMPLATE_CHARS = 200
export const MAX_ITEM_NO_WIDTH = 10
/** 文字部分、归类编码里一律不能有的字符（Windows 文件名不允许的）。 */
export const FILE_NAME_FORBIDDEN_CHARS = ['\\', '/', ':', '*', '?', '"', '<', '>', '|']

/** 预览用的示例编码（plan.html 的 Mockup：Z109-WS·2025-Y-BGS-0012）。 */
export const SAMPLE_CODES: Record<CategoryType, string> = {
  fonds: 'Z109',
  category: 'WS',
  year: '2025',
  retention: 'Y',
  org: 'BGS',
  custom: 'C01',
}

export type TemplateSegment =
  | { kind: 'literal'; text: string }
  | { kind: 'category'; type: CategoryType }
  | { kind: 'item_no'; width: number }

export interface ParsedTemplate {
  segments: TemplateSegment[]
  /** 用到的节点类型，按模板里的顺序。 */
  types: CategoryType[]
  /** 件号补零的位数（0 表示不补零）。 */
  itemNoWidth: number
  /** 分隔符（文字部分里不是字母、数字的字符），按码位排序、不重复。 */
  separators: string[]
}

export type TemplateParseResult = { ok: true; template: ParsedTemplate } | { ok: false; error: string }

const ALNUM = /^[\p{Alphabetic}\p{N}]$/u
const SPACE_OR_CONTROL = /^[\p{White_Space}\p{Cc}]$/u

/** 不是字母、数字的字符（和 Rust 的 `!char::is_alphanumeric()` 一致）。 */
export function isSeparatorChar(c: string): boolean {
  return !ALNUM.test(c)
}

/**
 * 和 Rust 的 `format!("{s:?}")`（str 的 Debug）一样加引号、转义：`\0 \t \r \n \\ \"`，其他控制字符写成 `\u{hex}`。
 * server 的错误说明里引用模板片段时用的就是它，前端照抄，说明才能一字不差。
 */
function rustDebugStr(v: string): string {
  const ESC: Record<string, string> = { '\0': '\\0', '\t': '\\t', '\r': '\\r', '\n': '\\n', '\\': '\\\\', '"': '\\"' }
  let out = '"'
  for (const c of v) {
    if (ESC[c] !== undefined) out += ESC[c]
    else if (/^\p{Cc}$/u.test(c)) out += `\\u{${c.codePointAt(0)!.toString(16)}}`
    else out += c
  }
  return `${out}"`
}

function placeholderText(s: TemplateSegment): string {
  if (s.kind === 'literal') return s.text
  if (s.kind === 'category') return `{${s.type}}`
  return s.width === 0 ? '{item_no}' : `{item_no:0${s.width}}`
}

function parsePlaceholder(inner: string): TemplateSegment | string {
  const i = inner.indexOf(':')
  const name = i >= 0 ? inner.slice(0, i) : inner
  const spec = i >= 0 ? inner.slice(i + 1) : null
  if (name === 'item_no') {
    if (spec === null) return { kind: 'item_no', width: 0 }
    // 只接受 `0` 后面跟 N（1–10，不带前导 0）：`04`、`010`；`004`、`0010`、`00` 都不行。
    const digits = spec.startsWith('0') ? spec.slice(1) : null
    const width =
      digits && !digits.startsWith('0') && /^[0-9]+$/.test(digits) ? Number(digits) : Number.NaN
    if (width >= 1 && width <= MAX_ITEM_NO_WIDTH) return { kind: 'item_no', width }
    return `{${inner}} 的格式不对：补零写成 :0 加位数（1–${MAX_ITEM_NO_WIDTH}），例如 {item_no:04}`
  }
  if ((CATEGORY_TYPES as readonly string[]).includes(name)) {
    if (spec === null) return { kind: 'category', type: name as CategoryType }
    return `{${inner}}：只有 {item_no} 能带格式，节点占位符直接写 {${name}}`
  }
  const allowed = [...CATEGORY_TYPES.map((t) => `{${t}}`), '{item_no}', '{item_no:04}'].join('、')
  return `不认识的占位符 {${inner}}，能用的有 ${allowed}`
}

/** 解析并校验档号模板（规则见文件开头）。错误说明和 server 的一致（不带"settings 不对："前缀）。 */
export function parseArchiveNoTemplate(source: string): TemplateParseResult {
  const fail = (why: string): TemplateParseResult => ({ ok: false, error: `档号模板不对：${why}` })
  const chars = Array.from(source).length
  if (chars === 0 || chars > MAX_TEMPLATE_CHARS) return fail(`不能为空，最长 ${MAX_TEMPLATE_CHARS} 个字符`)

  const segments: TemplateSegment[] = []
  let literal = ''
  let rest = source
  while (rest.length > 0) {
    const c = String.fromCodePoint(rest.codePointAt(0)!)
    if (c === '{') {
      const end = rest.indexOf('}')
      if (end < 0) return fail(`${rustDebugStr(rest)} 里的 { 没有对应的 }`)
      const inner = rest.slice(1, end)
      if (inner.includes('{')) return fail(`${rustDebugStr(rest.slice(0, end + 1))} 里的 { 没有对应的 }`)
      if (literal) {
        segments.push({ kind: 'literal', text: literal })
        literal = ''
      }
      const seg = parsePlaceholder(inner)
      if (typeof seg === 'string') return fail(seg)
      segments.push(seg)
      rest = rest.slice(end + 1)
      continue
    }
    if (c === '}') return fail('有多出来的 }（不支持转义，文字里不能有花括号）')
    if (SPACE_OR_CONTROL.test(c)) return fail('文字部分不能有空白或控制字符')
    if (FILE_NAME_FORBIDDEN_CHARS.includes(c)) {
      return fail(`文字部分不能有 ${c}（档号可能用作导出时的目录名、文件名，不能有 \\ / : * ? " < > |）`)
    }
    literal += c
    rest = rest.slice(c.length)
  }
  if (literal) segments.push({ kind: 'literal', text: literal })

  const itemNos = segments.filter((s) => s.kind === 'item_no').length
  if (itemNos !== 1) {
    return fail(`{item_no} 要正好出现一次（现在 ${itemNos} 次），否则同一个节点里的条目档号都一样`)
  }
  const seen = new Set<string>()
  for (const s of segments) {
    if (s.kind !== 'category') continue
    if (seen.has(s.type)) return fail(`{${s.type}} 出现了不止一次`)
    seen.add(s.type)
  }
  // 相邻两个占位符之间要有分隔符（连续的文字已经并成一段，两个占位符之间最多一段文字）。
  const ph = segments.flatMap((s, i) => (s.kind === 'literal' ? [] : [i]))
  for (let k = 0; k + 1 < ph.length; k++) {
    const a = ph[k]!
    const b = ph[k + 1]!
    const mid = segments[a + 1]
    const ok = b === a + 2 && mid?.kind === 'literal' && Array.from(mid.text).some(isSeparatorChar)
    if (!ok) {
      return fail(
        `${placeholderText(segments[a]!)} 和 ${placeholderText(segments[b]!)} 之间要有分隔符（至少一个不是字母、数字的字符，例如 - 或 ·）`,
      )
    }
  }

  const separators = new Set<string>()
  for (const s of segments) {
    if (s.kind === 'literal') for (const c of s.text) if (isSeparatorChar(c)) separators.add(c)
  }
  return {
    ok: true,
    template: {
      segments,
      types: segments.flatMap((s) => (s.kind === 'category' ? [s.type] : [])),
      itemNoWidth: (segments.find((s) => s.kind === 'item_no') as { width: number } | undefined)?.width ?? 0,
      separators: [...separators].sort((x, y) => x.codePointAt(0)! - y.codePointAt(0)!),
    },
  }
}

/**
 * 按模板拼档号：每个节点占位符取 `codes` 里的编码（祖先链上最近的那个，由调用方给出），件号按格式补零（位数多时
 * 全部输出）。缺某一级时返回 `value: null` 和缺的类型（档号留空，界面显示"第 n 件"）。
 */
export function renderArchiveNo(
  t: ParsedTemplate,
  codes: Partial<Record<CategoryType, string>>,
  itemNo: number,
): { value: string | null; missing: CategoryType[] } {
  let out = ''
  const missing: CategoryType[] = []
  for (const s of t.segments) {
    if (s.kind === 'literal') out += s.text
    else if (s.kind === 'category') {
      const code = codes[s.type]
      if (code === undefined) {
        if (!missing.includes(s.type)) missing.push(s.type)
      } else out += code
    } else out += String(itemNo).padStart(s.width, '0')
  }
  return { value: missing.length ? null : out, missing }
}

/** 模板的中文写法（给人看）：占位符换成节点类型的名称，例如"全宗-门类·年度-保管期限-机构-件号"。 */
export function describeArchiveNoTemplate(t: ParsedTemplate): string {
  return t.segments
    .map((s) => (s.kind === 'literal' ? s.text : s.kind === 'category' ? categoryTypeLabel(s.type) : '件号'))
    .join('')
}

/** 编码里含有的分隔符（按出现顺序、不重复）。 */
export function separatorsIn(code: string, separators: readonly string[]): string[] {
  const found: string[] = []
  for (const c of code) if (separators.includes(c) && !found.includes(c)) found.push(c)
  return found
}

// ---------- 著录项 ----------

/** text 著录项不写 max_length 时的最大字符数（用户决定 Q1）。 */
export const DEFAULT_TEXT_MAX_LENGTH = 500
export const MAX_TEXT_MAX_LENGTH = 10000
export const DATE_FORMATS = ['YYYY', 'YYYY-MM', 'YYYY-MM-DD']

/** date 的值：`YYYY`、`YYYY-MM`、`YYYY-MM-DD` 之一，年份 0001–9999，月、日真实存在（含闰年）。 */
export function isValidDateValue(v: string): boolean {
  const m = /^(\d{4})(?:-(\d{2})(?:-(\d{2}))?)?$/.exec(v)
  if (!m) return false
  const y = Number(m[1])
  if (y < 1) return false
  if (m[2] === undefined) return true
  const mo = Number(m[2])
  if (mo < 1 || mo > 12) return false
  if (m[3] === undefined) return true
  const d = Number(m[3])
  const leap = (y % 4 === 0 && y % 100 !== 0) || y % 400 === 0
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][mo - 1]!
  return d >= 1 && d <= days
}

/** text 的值：不超过 max_length 个字符，不能有控制字符（换行、回车、制表符可以）。不合规则时返回说明。 */
export function checkTextValue(v: string, maxLength: number): string | null {
  if (Array.from(v).length > maxLength) return `最长 ${maxLength} 个字符`
  if (/[\p{Cc}]/u.test(v.replace(/[\n\r\t]/g, ''))) return '不能有控制字符'
  return null
}

/**
 * 项目设置里存的 item_fields（`GET /api/system/projects/{id}` 的 settings，可能是 M3-1 之前写入的）按 catalog-settings
 * 的格式补齐（和 `doclite_domain::catalog::CatalogSettings::from_stored` 一样宽松：解析不了的项跳过）：required 默认
 * false，非 enum 的 options 为空数组，非 text 的 max_length 为 null。
 */
export function normalizeItemFields(raw: unknown): ItemFieldDef[] {
  if (!Array.isArray(raw)) return []
  const out: ItemFieldDef[] = []
  for (const v of raw) {
    if (!v || typeof v !== 'object') continue
    const f = v as Record<string, unknown>
    const text = (k: string) => (typeof f[k] === 'string' && (f[k] as string).trim() ? (f[k] as string) : null)
    const key = text('key')
    const label = text('label')
    const type = text('type')
    if (!key || !label || (type !== 'text' && type !== 'date' && type !== 'enum')) continue
    const options =
      type === 'enum' && Array.isArray(f.options) ? f.options.filter((o): o is string => typeof o === 'string') : []
    const ml = typeof f.max_length === 'number' && Number.isInteger(f.max_length) ? f.max_length : null
    out.push({
      key,
      label,
      type,
      required: f.required === true,
      options,
      max_length: type === 'text' ? (ml !== null && ml >= 1 && ml <= MAX_TEXT_MAX_LENGTH ? ml : DEFAULT_TEXT_MAX_LENGTH) : null,
      default: typeof f.default === 'string' ? f.default : null,
    })
  }
  return out
}

// ---------- 一个文件挂在哪个条目（Q10） ----------

/** 档号；为空（缺某一级编码）时"第 n 件"。 */
export function placementNo(p: Pick<CatalogPlacement, 'archive_no' | 'item_no'>): string {
  return p.archive_no ?? `第 ${p.item_no} 件`
}

// ---------- 档案条目（M3-5） ----------

/** 条目状态的中文名和徽标颜色（M9 起只有两种：draft 未锁定、locked 已锁定，用户 2026-10-19）。 */
export const ITEM_STATUS: Record<ItemStatus, { label: string; badge: string; dot: string }> = {
  draft: { label: '未锁定', badge: 'bg-muted text-muted-foreground', dot: 'bg-muted-foreground/45' },
  locked: { label: '已锁定', badge: 'bg-violet-100 text-violet-700', dot: 'bg-violet-700' },
}

export function itemStatusOf(status: string) {
  return ITEM_STATUS[status as ItemStatus] ?? { label: status, badge: 'bg-muted text-muted-foreground', dot: 'bg-muted-foreground/45' }
}

/** 件号按模板的补零位数显示（模板解析不了时不补零）。 */
export function formatItemNo(itemNo: number, width: number): string {
  return String(itemNo).padStart(width, '0')
}

/** 件号的范围（server：1–999999999）。 */
export const MAX_ITEM_NO = 999_999_999
/** 题名最多多少个字符（server `MAX_TITLE_CHARS`）。 */
export const MAX_TITLE_CHARS = 1000
/** 题名的 key（著录校验错误里用它指题名；项目设置的著录项不能叫 title）。 */
export const TITLE_KEY = 'title'

const CONTROL = /\p{Cc}/u

/** 题名：最多 1000 个字符，不能有控制字符（含换行）。不合规则时返回说明（带"题名"）。 */
export function checkTitle(v: string): string | null {
  if (Array.from(v).length > MAX_TITLE_CHARS) return `题名最长 ${MAX_TITLE_CHARS} 个字符`
  if (CONTROL.test(v)) return '题名不能有控制字符（含换行）'
  return null
}

/** 一个值是否符合著录项现在的定义（类型、格式、长度、选项）。不合规则时返回说明（带著录项名称）。 */
export function checkFieldValue(def: ItemFieldDef, v: string): string | null {
  if (def.type === 'text') {
    const why = checkTextValue(v, def.max_length ?? DEFAULT_TEXT_MAX_LENGTH)
    return why ? `${def.label}${why}` : null
  }
  if (def.type === 'date') {
    return isValidDateValue(v) ? null : `${def.label}不是合法的日期（只能是 ${DATE_FORMATS.join('、')}，日期要真实存在）`
  }
  if (def.options.includes(v)) return null
  const shown = def.options.slice(0, 10).join('、')
  return `${def.label}只能从选项里选（${shown}${def.options.length > 10 ? ` 等 ${def.options.length} 项` : ''}）`
}

/** 库里存的著录值转成表单里的字符串（以前写进去的非字符串值按 JSON 显示）。 */
export function storedFieldText(v: unknown): string {
  if (v === null || v === undefined) return ''
  return typeof v === 'string' ? v : JSON.stringify(v)
}

/**
 * 400 `invalid_input` 的 `details.fields`（著录校验不过，key 是 `title` 时指题名）；不是这种错误时返回 null。
 * 用鸭子类型判断（这个文件不能引用 ApiError）。
 */
export function fieldIssuesOf(err: unknown): FieldIssue[] | null {
  if (!err || typeof err !== 'object') return null
  const e = err as { code?: unknown; details?: unknown }
  if (e.code !== 'invalid_input' || !e.details || typeof e.details !== 'object') return null
  const fields = (e.details as { fields?: unknown }).fields
  if (!Array.isArray(fields)) return null
  return fields.flatMap((f) =>
    f && typeof f === 'object' && typeof f.key === 'string' && typeof f.message === 'string'
      ? [{ key: f.key as string, message: f.message as string }]
      : [],
  )
}

/** 档号为空的原因的简短说明（详情的标题行用；完整说明是 reason.message）。 */
export function archiveNoReasonShort(r: ArchiveNoReason | null | undefined): string {
  if (!r) return '档号为空'
  switch (r.code) {
    case 'incomplete':
      return `缺级：${(r.missing ?? []).map(categoryTypeLabel).join('、') || '—'}`
    case 'conflict':
      return '撞号'
    case 'not_generated':
      return '未生成'
    case 'template_invalid':
      return '模板不对'
    default:
      return '档号为空'
  }
}

// ---------- 整个文件夹归档和封存（M9，requirements 5.5、5.9） ----------

/** 归档目录里一件的简称："节点名 件号"（件号按位数补零），例如"办公室 0003"。 */
export function itemShortLabel(a: { category_names: string[]; item_no: number }, width: number): string {
  const node = a.category_names[a.category_names.length - 1]
  const no = formatItemNo(a.item_no, width)
  return node ? `${node} ${no}` : `第 ${no} 件`
}

/**
 * 不知道模板的件号位数时（文件目录页没有取 catalog-settings），从档号末尾的数字猜：末尾那串数字的值就是件号时用它的
 * 长度，否则不补零。
 */
export function itemNoWidthFrom(archiveNo: string | null, itemNo: number): number {
  const m = archiveNo ? /(\d+)$/.exec(archiveNo) : null
  return m && Number(m[1]) === itemNo ? m[1]!.length : 0
}

/** 解除封存时可以选的原因（选填，用户 2026-10-19）。 */
export const UNSEAL_REASONS = ['漏扫了页', '多扫了页', '页的顺序不对', '其他'] as const

/** 归档时跳过的原因的短名。 */
export const ARCHIVE_SKIP_LABEL: Record<string, string> = {
  archived: '已归档',
  has_subfolders: '含下级文件夹',
  empty: '空文件夹',
  too_many_pages: '超过 2000 页',
  file_cataloged: '有文件在别的件里',
}

// ---------- 归类节点的编码和名称（M3-5，规则见 design §2.3"归类节点"） ----------

/** 编码：1–N 个字符，不能有空白、控制字符和不能用的字符（catalog-settings 给出）。不合规则时返回说明。 */
export function checkCategoryCode(
  code: string,
  rules: Pick<CatalogSettings, 'category_code_max_chars' | 'category_code_forbidden_chars'>,
): string | null {
  const chars = Array.from(code)
  if (chars.length === 0) return '编码不能为空'
  if (chars.length > rules.category_code_max_chars) return `编码最长 ${rules.category_code_max_chars} 个字符`
  if (chars.some((c) => /^[\p{White_Space}\p{Cc}]$/u.test(c))) return '编码不能有空白或控制字符'
  const bad = chars.filter((c, i) => rules.category_code_forbidden_chars.includes(c) && chars.indexOf(c) === i)
  if (bad.length) return `编码不能有 ${bad.join(' ')}（档号模板的分隔符、花括号和文件名不允许的字符）`
  return null
}

/** 名称：去掉首尾空白后 1–N 个字符，不能有控制字符。不合规则时返回说明。 */
export function checkCategoryName(name: string, maxChars: number): string | null {
  const t = name.trim()
  if (!t) return '名称不能为空'
  if (Array.from(t).length > maxChars) return `名称最长 ${maxChars} 个字符`
  if (CONTROL.test(t)) return '名称不能有控制字符'
  return null
}
