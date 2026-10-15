// 项目编辑对话框里"著录项和档号"的草稿（M3-7）：从项目详情的 settings 读出来，编辑，写回 settings。
// - 读：`GET /api/system/projects/{id}` 的 settings（补上了默认值）。不用 catalog-settings：那个接口要求能进项目，
//   持 system.projects 的人不一定能进（403 会走全局的无权处理）。item_fields 按 catalog-settings 的格式补齐
//   （`normalizeItemFields`），所以写回去的格式和 catalog-settings 读出来的一样（M3-1 验收过能原样写回）。
// - 写：`PATCH /api/system/projects/{id}` 的 settings 是整体替换——在详情的 settings 上只换 `archive_no.template`、
//   `item_fields`、`catalog.lock_requires_approved` 三处，其余键原样带回（含不认识的键）。整体替换不会盖掉别人在这期间
//   的修改，靠的是 PATCH 的版本检查 `expected_updated_at`（M4-4，见 ProjectFormDialog.vue；替换了 M3-7 的
//   "保存前重读、深比较 settings"）。
// - 本地检查（`fieldProblems`、模板预览）只是提示，不挡保存：以 server 的校验为准，它返回的 400（message 以
//   "settings 不对："开头）显示在对话框里，`item_fields[i]` 对应第 i+1 行。

import type { ItemFieldDef, ItemFieldType } from '@/api/types'
import {
  checkTextValue,
  DEFAULT_TEXT_MAX_LENGTH,
  isValidDateValue,
  MAX_TEXT_MAX_LENGTH,
  normalizeItemFields,
} from '@/lib/catalog'

export interface DraftField {
  /** 只在界面里用的行号（排序、删除时跟踪行）。 */
  uid: number
  key: string
  label: string
  type: ItemFieldType
  required: boolean
  /** enum 的选项，一行一个。 */
  optionsText: string
  /** text 的最大字符数（输入框原文，空表示默认 500）。 */
  maxLength: string
  /** 默认值（空表示没有）。 */
  defaultValue: string
  /** 读出来时的 key（新加的为 null），改 key 时提示。 */
  originalKey: string | null
}

export interface CatalogDraft {
  template: string
  lockRequiresApproved: boolean
  fields: DraftField[]
  /** settings 里格式不对、读不出来的著录项个数（M3-1 之前写入的；保存时会去掉，界面上提示）。 */
  skippedFields: number
}

let uidSeq = 0
export function nextUid(): number {
  return ++uidSeq
}

function obj(v: unknown): Record<string, unknown> {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {}
}

/** 从项目详情的 settings 读出草稿。 */
export function draftFromSettings(settings: Record<string, unknown>): CatalogDraft {
  const fields = normalizeItemFields(settings.item_fields).map<DraftField>((f) => ({
    uid: nextUid(),
    key: f.key,
    label: f.label,
    type: f.type,
    required: f.required,
    optionsText: f.options.join('\n'),
    maxLength: f.type === 'text' ? String(f.max_length ?? DEFAULT_TEXT_MAX_LENGTH) : '',
    defaultValue: f.default ?? '',
    originalKey: f.key,
  }))
  const template = obj(settings.archive_no).template
  const lock = obj(settings.catalog).lock_requires_approved
  return {
    template: typeof template === 'string' ? template : '',
    lockRequiresApproved: typeof lock === 'boolean' ? lock : true,
    fields,
    skippedFields: Array.isArray(settings.item_fields) ? settings.item_fields.length - fields.length : 0,
  }
}

export function newField(): DraftField {
  return {
    uid: nextUid(),
    key: '',
    label: '',
    type: 'text',
    required: false,
    optionsText: '',
    maxLength: '',
    defaultValue: '',
    originalKey: null,
  }
}

/**
 * enum 选项：一行一个，去掉首尾空白，空行不要。发给 server 的就是这个结果：首尾带空白的选项永远选不上（提交的
 * 著录值会去掉首尾空白再比较），M3-2 起 server 也拒绝首尾带空白的选项和空选项。
 */
export function optionLines(text: string): string[] {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
}

/**
 * 一个著录项写进 settings 的样子（catalog-settings 的格式）。最大长度不是整数时原样（字符串）发给 server，让它报错，
 * 不悄悄当作没写。
 */
export function fieldToSetting(f: DraftField): Omit<ItemFieldDef, 'max_length'> & { max_length: number | string | null } {
  const ml = f.maxLength.trim()
  return {
    key: f.key.trim(),
    label: f.label.trim(),
    type: f.type,
    required: f.required,
    options: f.type === 'enum' ? optionLines(f.optionsText) : [],
    max_length: f.type !== 'text' || ml === '' ? null : /^[0-9]+$/.test(ml) ? Number(ml) : ml,
    default: f.defaultValue === '' ? null : f.type === 'text' ? f.defaultValue : f.defaultValue.trim(),
  }
}

/** 草稿写回 settings：只换三处，其余原样。 */
export function settingsWithDraft(settings: Record<string, unknown>, d: CatalogDraft): Record<string, unknown> {
  return {
    ...settings,
    archive_no: { ...obj(settings.archive_no), template: d.template },
    item_fields: d.fields.map(fieldToSetting),
    catalog: { ...obj(settings.catalog), lock_requires_approved: d.lockRequiresApproved },
  }
}

/** 比较用：草稿里会写进 settings 的内容。 */
export function draftSignature(d: CatalogDraft): string {
  return JSON.stringify([d.template, d.lockRequiresApproved, d.fields.map(fieldToSetting)])
}

/** 一个著录项的本地检查（和 server 的 validate_settings 一致；只提示，不挡保存）。 */
export function fieldProblems(f: DraftField, all: DraftField[]): string[] {
  const out: string[] = []
  const key = f.key.trim()
  if (!key) out.push('key 不能为空')
  // M3-2 起 server 拒绝 key 为 title（题名是条目的固定列）；M3-2 合入前前端先提示。
  else if (key === 'title') out.push('title 是题名的固定 key，不能用作著录项')
  else if (all.some((o) => o !== f && o.key.trim() === key)) out.push(`key「${key}」重复`)
  if (!f.label.trim()) out.push('名称不能为空')
  const options = optionLines(f.optionsText)
  if (f.type === 'enum') {
    if (options.length === 0) out.push('选项至少要有一个')
    const dup = options.find((o, i) => options.indexOf(o) !== i)
    if (dup !== undefined) out.push(`选项「${dup}」重复`)
    // 首尾空白和空行保存时会去掉（optionLines），这里只说明一下，免得以为原样保存。
    if (f.optionsText.split('\n').some((l) => l.trim() && l !== l.trim())) out.push('选项首尾的空白会去掉')
  }
  let maxLength = DEFAULT_TEXT_MAX_LENGTH
  if (f.type === 'text' && f.maxLength.trim() !== '') {
    const n = Number(f.maxLength.trim())
    if (!/^[0-9]+$/.test(f.maxLength.trim()) || n < 1 || n > MAX_TEXT_MAX_LENGTH) {
      out.push(`最大长度要是 1–${MAX_TEXT_MAX_LENGTH} 的整数`)
    } else maxLength = n
  }
  const d = f.defaultValue
  if (d !== '') {
    if (f.type === 'text') {
      const why = checkTextValue(d, maxLength)
      if (why) out.push(`默认值${why}`)
    } else if (f.type === 'date') {
      if (!isValidDateValue(d.trim())) out.push('默认值不是合法的日期（YYYY、YYYY-MM、YYYY-MM-DD）')
    } else if (!options.includes(d.trim())) out.push('默认值不在选项里')
  }
  return out
}

/** server 的错误里指向第几个著录项（`item_fields[2]` → 2）；不是著录项的错误返回 null。 */
export function fieldIndexOfError(message: string): number | null {
  const m = /item_fields\[(\d+)\]/.exec(message)
  return m ? Number(m[1]) : null
}
