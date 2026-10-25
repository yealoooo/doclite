// 审计日志页（M7-3）的显示文字：对象类型、操作的中文名，对象名、一句话说明，展开后的前后差异。
//
// 现在 server 会写的审计（server/internal 里各模块调 audit.Write 的地方，和存储迁移逐个快照写的 storage_migrate）都在
// AUDIT_ACTIONS 里；不认识的类型、动作、字段原样显示（以后新加的审计不用先改这里也能看）。detail 一般是
// `{before, after}`（只放变了的字段），另有各动作自己的键（source、batch、cause、moved……）。

import type { AuditLog } from '@/api/types'
import { ARCHIVE_STATE, CATEGORY_TYPE_LABEL } from '@/lib/catalog'
import type { StatusStyle } from '@/lib/status'
import { formatDateTime } from '@/lib/format'

/** 对象类型的中文名。 */
export const TARGET_LABELS: Record<string, string> = {
  user: '用户',
  role: '角色',
  project: '项目',
  storage_profile: '存储配置',
  folder: '文件夹',
  archive_category: '归类节点',
  archive_item: '条目',
  export_task: '导出任务',
  file_snapshot: '文件快照',
  license: '许可证',
}

/** 各类型现在会写的动作（筛选栏按类型列出；顺序就是下拉框里的顺序）。 */
export const AUDIT_ACTIONS: Record<string, string[]> = {
  user: ['create', 'update', 'disable', 'enable', 'reset_password', 'set_roles'],
  role: ['create', 'update', 'delete', 'set_menus'],
  project: ['create', 'update', 'delete', 'set_access'],
  storage_profile: ['create', 'update', 'delete', 'set_default', 'unset_default', 'migrate_start', 'migrate_cancel', 'migrate_end'],
  folder: ['create', 'update', 'move', 'delete', 'reorder'],
  archive_category: ['create', 'update', 'move', 'delete', 'renumber'],
  archive_item: ['archive', 'update', 'move', 'unseal', 'reseal', 'lock', 'unlock', 'delete', 'renumber', 'reorder'],
  export_task: ['create', 'cancel', 'download'],
  file_snapshot: ['storage_migrate'],
  license: ['import', 'replace'],
}

const ACTION_LABELS: Record<string, string> = {
  create: '新建',
  update: '修改',
  delete: '删除',
  move: '移动',
  disable: '停用',
  enable: '启用',
  reset_password: '重置密码',
  set_roles: '改角色',
  set_menus: '改菜单',
  set_default: '设为默认',
  unset_default: '取消默认',
  set_access: '改授权',
  archive: '归档',
  unseal: '解除封存',
  reseal: '重新归档',
  lock: '锁定',
  unlock: '解锁',
  renumber: '重算档号',
  reorder: '调整页序',
  cancel: '取消',
  download: '下载',
  storage_migrate: '存储迁移',
  migrate_start: '开始迁移',
  migrate_cancel: '取消迁移',
  migrate_end: '迁移结束',
  import: '导入',
  replace: '替换',
}

/** 同一个动作在某些类型上的叫法。 */
const ACTION_OVERRIDES: Record<string, Record<string, string>> = {
  archive_category: { renumber: '按新模板重新生成档号' },
  archive_item: { delete: '撤销归档' },
  export_task: { create: '新建导出', cancel: '取消导出', download: '下载导出物' },
  license: { import: '导入许可证', replace: '替换许可证' },
}

export function targetLabel(type: string): string {
  return TARGET_LABELS[type] ?? type
}

/** 操作的中文名；type 为空时按通用的叫法。不认识的原样返回。 */
export function actionLabel(action: string, type?: string | null): string {
  return (type && ACTION_OVERRIDES[type]?.[action]) || ACTION_LABELS[action] || action
}

/** 操作的徽标颜色：新建绿、删除停用红，归档、锁定、解除封存和归类页同色，其余灰。 */
export function auditActionStatusOf(action: string, label: string): StatusStyle {
  const badge =
    action === 'create'
      ? 'bg-ok-soft text-ok'
      : action === 'delete' || action === 'disable'
        ? 'bg-destructive-soft text-destructive'
        : action === 'archive' || action === 'reseal'
          ? ARCHIVE_STATE.archived.badge
          : action === 'lock'
            ? ARCHIVE_STATE.locked.badge
            : action === 'unseal'
              ? ARCHIVE_STATE.modifying.badge
              : 'bg-muted text-muted-foreground'
  return { label, badge }
}

/** 筛选栏"操作"的选项：选了类型时是这个类型的动作，否则是全部动作（去重）。 */
export function actionOptions(type: string | null): { value: string; label: string }[] {
  const list = type ? (AUDIT_ACTIONS[type] ?? []) : [...new Set(Object.values(AUDIT_ACTIONS).flat())]
  return list.map((a) => ({ value: a, label: actionLabel(a, type) }))
}

// ---------- detail ----------

type Obj = Record<string, unknown>

function isObj(v: unknown): v is Obj {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function before(log: AuditLog): Obj {
  return isObj(log.detail.before) ? log.detail.before : {}
}

function after(log: AuditLog): Obj {
  return isObj(log.detail.after) ? log.detail.after : {}
}

function str(v: unknown): string | null {
  if (typeof v === 'string' && v !== '') return v
  if (typeof v === 'number') return String(v)
  return null
}

/** 路径的最后一段（"项目根/盒 1/补扫" → "补扫"）。 */
function lastSegment(path: string): string {
  const i = path.lastIndexOf('/')
  return i >= 0 ? path.slice(i + 1) : path
}

/**
 * 对象：名字 + 完整的说明（鼠标移上去看）。从 detail 里找（之后的值优先），找不到时用 target_id 的前 8 位。
 * 名字是记录时的（之后改过名也照旧），所以和现在的名字可能不一样。
 */
export function objectName(log: AuditLog, known?: Map<string, string>): { name: string; title: string } {
  const b = before(log)
  const a = after(log)
  const get = (k: string) => str(a[k]) ?? str(b[k]) ?? str(log.detail[k])
  let name: string | null = null
  let title: string | null = null
  switch (log.target_type) {
    case 'user': {
      const u = get('username')
      const d = get('display_name')
      name = d && u ? `${d}（${u}）` : (d ?? u)
      break
    }
    case 'role':
      name = get('role_name') ?? get('role_code')
      break
    case 'project':
      name = get('project_name') ?? log.project_name
      title = get('project_code')
      break
    case 'storage_profile':
      name = get('profile_name')
      break
    case 'folder': {
      const path = get('folder_path')
      name = get('folder_name') ?? (path ? lastSegment(path) : null)
      title = path
      break
    }
    case 'archive_category': {
      const path = get('category_path')
      name = get('category_name') ?? (path ? lastSegment(path) : null)
      title = path
      break
    }
    case 'archive_item': {
      const no = get('item_no')
      name = get('archive_no') ?? (no ? `第 ${no} 件` : null) ?? get('title')
      title = get('title')
      break
    }
    case 'export_task':
      name = get('file_name')
      break
    case 'file_snapshot': {
      const file = get('file_name')
      const v = get('version_no')
      name = file ? (v ? `${file} v${v}` : file) : null
      title = get('file_id') ? `file_id ${get('file_id')}` : null
      break
    }
    case 'license':
      name = get('company')
      title = get('license_id')
      break
  }
  const id = log.target_id.slice(0, 8)
  // 这条里没有名字（例如只改了备注）：用同一对象别的记录里的名字。
  name ??= known?.get(log.target_id) ?? null
  return { name: name ?? `#${id}`, title: [title, `id ${log.target_id}`].filter(Boolean).join('\n') }
}

/** 已加载的记录里每个对象的名字（target_id → 名字，新的记录优先），给没有名字的记录用。 */
export function knownNames(logs: AuditLog[]): Map<string, string> {
  const out = new Map<string, string>()
  for (const log of logs) {
    if (out.has(log.target_id)) continue
    const { name } = objectName(log)
    if (!name.startsWith('#')) out.set(log.target_id, name)
  }
  return out
}

/** 字段的中文名；不认识的原样返回。 */
export const FIELD_LABELS: Record<string, string> = {
  username: '登录名',
  display_name: '姓名',
  status: '状态',
  roles: '角色',
  revoked_sessions: '强制下线的会话',
  password_reset: '重置密码',
  role_code: '角色编码',
  role_name: '角色名称',
  description: '说明',
  menu_codes: '菜单',
  user_ids: '用户',
  users: '授权的用户',
  project_code: '项目编码',
  project_name: '项目名称',
  settings: '项目设置',
  profile_name: '名称',
  endpoint: '服务端地址',
  public_endpoint: '工作站地址',
  bucket: '桶',
  region: '区域',
  path_prefix: '路径前缀',
  access_key: 'Access Key',
  secret_key_changed: '改了 Secret Key',
  force_path_style: '路径风格访问',
  url_expire_hours: '链接有效期（小时）',
  is_default: '默认存储',
  remark: '备注',
  folder_name: '名称',
  folder_path: '位置',
  parent_id: '上级 id',
  path_code: '路径编码',
  file_count: '文件数',
  folder_count: '文件夹数（含自己）',
  category_code: '编码',
  category_name: '名称',
  category_type: '类型',
  category_path: '位置',
  renumbered: '重算档号的条目数',
  deleted_categories: '删除的节点数',
  template: '档号模板',
  skipped_locked: '跳过的已锁定的件',
  archive_no_conflicts: '档号被占用的条目',
  title: '题名',
  item_no: '件号',
  archive_no: '档号',
  page_count: '页数',
  category_id: '节点 id',
  extra_fields: '著录项',
  scope_type: '范围',
  scope_ids: '范围 id',
  export_format: '格式',
  item_count: '件数',
  only_approved: '只导出审核通过的',
  only_ready: '只导出就绪的',
  require_locked_item: '要求已锁定',
  skipped_item_count: '跳过的件数',
  job_id: '后台任务 id',
  resubmit_of: '补交自',
  file_name: '文件名',
  size: '大小（字节）',
  output_key: '对象 key',
  url_expires_at: '链接到期',
  storage_profile_id: '存储 id',
  storage_profile_name: '存储',
  file_id: 'file_id',
  version_no: '版本号',
  target_profile_id: '目标存储 id',
  target_profile_name: '目标存储',
  total: '要迁的快照',
  migrated: '已迁的快照',
  failed: '核对不一致的快照',
  failed_objects: '核对不一致的对象',
  remaining: '源存储里还剩的快照',
  result: '结果',
  error_message: '原因',
  storage_key: '对象 key',
  cover_key: '封面 key',
  source: '来源',
  batch: '批量',
  cause: '原因',
  conflict: '撞号',
  lock_requires_approved: '锁定要求全部通过',
  moved: '挪动的页',
  linked: '挂上的页数',
  unlinked: '移出的页数',
  license_id: '许可证编号',
  company: '公司',
  edition: '版本',
  max_users: '同时在线的会话数',
  issued_at: '签发时间',
  expires_at: '到期时间',
}

export function fieldLabel(key: string): string {
  return FIELD_LABELS[key] ?? key
}

const STATUS_LABELS: Record<string, string> = {
  active: '启用',
  disabled: '停用',
  readonly: '只读',
  archived: '已归档',
  draft: '未锁定',
  locked: '已锁定',
  sealed: '已封存',
  modifying: '修改中',
  queued: '排队',
  running: '执行中',
  done: '已完成',
  canceled: '已取消',
  cancelled: '已取消',
  failed: '失败',
}

const EDITION_LABELS: Record<string, string> = { trial: '试用版', standard: '标准版', enterprise: '企业版' }

const SOURCE_LABELS: Record<string, string> = {
  init_admin: '首次启动建的管理员',
  init_storage: '首次启动登记的默认存储',
}

/** 字段值给人看的写法：空、是否、数组、对象（JSON）。 */
export function formatValue(key: string, v: unknown): string {
  if (v === null || v === undefined || v === '') return '（空）'
  if (typeof v === 'boolean') return v ? '是' : '否'
  if (typeof v === 'string') {
    if (key === 'status' || key === 'result') return STATUS_LABELS[v] ?? v
    if (key === 'source') return SOURCE_LABELS[v] ?? v
    if (key === 'category_type' || key === 'scope_type') return CATEGORY_TYPES[v] ?? v
    if (key === 'edition') return EDITION_LABELS[v] ?? v
    if (key.endsWith('_at') && !Number.isNaN(Date.parse(v))) return formatAuditTime(v)
    return v
  }
  if (Array.isArray(v) && v.every((x) => typeof x === 'string' || typeof x === 'number')) {
    return v.length ? v.join('、') : '（无）'
  }
  return JSON.stringify(v)
}

const CATEGORY_TYPES: Record<string, string> = { ...CATEGORY_TYPE_LABEL, item: '条目' }

/** 展开后的一行：字段、之前、之后（只有一个值的键 before 为 undefined）。 */
export interface DetailRow {
  key: string
  label: string
  before?: string
  after: string
}

/** detail 摊成行：before/after 里的字段（两边合起来，按出现的先后），再加上其他键。 */
export function detailRows(log: AuditLog): { changes: DetailRow[]; others: DetailRow[] } {
  const b = before(log)
  const a = after(log)
  const hasBefore = isObj(log.detail.before)
  const keys = [...new Set([...Object.keys(b), ...Object.keys(a)])]
  const changes = keys.map((k) => ({
    key: k,
    label: fieldLabel(k),
    before: hasBefore ? formatValue(k, b[k]) : undefined,
    after: k in a ? formatValue(k, a[k]) : '—',
  }))
  const others = Object.entries(log.detail)
    .filter(([k]) => k !== 'before' && k !== 'after')
    .map(([k, v]) => ({ key: k, label: fieldLabel(k), after: formatValue(k, v) }))
  return { changes, others }
}

function count(v: unknown): number | null {
  return Array.isArray(v) ? v.length : null
}

/** 一句话说明（列表里的"说明"一列）。说不出什么时列出改了哪些字段。 */
export function describe(log: AuditLog): string {
  const d = log.detail
  const b = before(log)
  const a = after(log)
  const source = str(d.source)
  if (source) return SOURCE_LABELS[source] ?? source
  const changed = () => {
    const keys = [...new Set([...Object.keys(b), ...Object.keys(a)])].filter(
      (k) => !['renumbered', 'revoked_sessions', 'parent_id', 'path_code'].includes(k),
    )
    return keys.length ? `改了：${keys.map(fieldLabel).join('、')}` : ''
  }
  switch (`${log.target_type}/${log.action}`) {
    case 'user/create':
      return `角色：${formatValue('roles', a.roles)}`
    case 'user/disable':
    case 'user/reset_password': {
      const n = Number(a.revoked_sessions ?? 0)
      return n > 0 ? `强制下线 ${n} 个会话` : ''
    }
    case 'user/set_roles':
    case 'role/set_menus': {
      const k = log.action === 'set_roles' ? 'roles' : 'menu_codes'
      return `${fieldLabel(k)}：${formatValue(k, b[k])} → ${formatValue(k, a[k])}`
    }
    case 'role/create':
      return `菜单：${formatValue('menu_codes', a.menu_codes)}`
    case 'project/set_access':
      return `授权给 ${count(a.users) ?? 0} 个人、${count(a.roles) ?? 0} 个角色（原来 ${count(b.users) ?? 0} 个人、${count(b.roles) ?? 0} 个角色）`
    case 'folder/create':
    case 'folder/move':
      return `位置：${formatValue('folder_path', a.folder_path)}`
    case 'folder/delete': {
      // folder_count 含它自己。
      const files = Number(b.file_count ?? 0)
      const folders = Math.max(0, Number(b.folder_count ?? 1) - 1)
      return files || folders ? `连同 ${folders} 个子文件夹、${files} 个文件` : ''
    }
    case 'folder/reorder':
      return `挪动 ${count(d.moved) ?? 0} 个文件，共 ${Number(d.file_count ?? 0)} 个`
    case 'archive_category/create':
      return `${formatValue('category_type', a.category_type)} ${str(a.category_code) ?? ''}`.trim()
    case 'archive_category/move':
      return `位置：${formatValue('category_path', a.category_path)}`
    case 'archive_category/delete': {
      const n = Number(a.deleted_categories ?? 0)
      return n > 1 ? `连同下级共 ${n} 个节点` : ''
    }
    case 'archive_category/renumber':
      return `重算 ${Number(a.renumbered ?? 0)} 件`
    case 'archive_item/create':
    case 'archive_item/archive':
      return [str(a.title), a.page_count ? `${a.page_count} 页` : null, str(a.source_folder_path) ? `来源 ${str(a.source_folder_path)}` : null]
        .filter(Boolean)
        .join('，')
    case 'archive_item/unseal':
      return [str(d.source_folder_path) ? `来源 ${str(d.source_folder_path)}` : null, str(d.reason) ? `原因：${str(d.reason)}` : null]
        .filter(Boolean)
        .join('，')
    case 'archive_item/reseal':
      return `${Number(b.page_count ?? 0)} 页 → ${Number(a.page_count ?? 0)} 页（新增 ${count(d.added) ?? 0}、去掉 ${count(d.removed) ?? 0}、调序 ${count(d.moved) ?? 0}）`
    case 'archive_item/move':
      return `移到 ${formatValue('category_path', a.category_path)}`
    case 'archive_item/renumber':
      return `档号：${formatValue('archive_no', b.archive_no)} → ${formatValue('archive_no', a.archive_no)}`
    case 'archive_item/lock':
      return d.batch ? '批量锁定' : ''
    case 'archive_item/reorder': {
      const n = count(d.moved) ?? 0
      return `挪动 ${n} 页，共 ${Number(d.page_count ?? 0)} 页`
    }
    case 'export_task/create': {
      const fmt = a.export_format === 'pdf' ? 'PDF' : a.export_format === 'images' ? '原图包' : String(a.export_format ?? '')
      return `${fmt}，${Number(a.item_count ?? 0)} 件 ${Number(a.page_count ?? 0)} 页`
    }
    case 'export_task/download':
      return str(d.file_name) ?? ''
    case 'file_snapshot/storage_migrate':
      return `「${str(b.storage_profile_name) ?? '—'}」→「${str(a.storage_profile_name) ?? '—'}」`
    case 'storage_profile/migrate_start':
      return `迁到「${str(d.target_profile_name) ?? '—'}」，共 ${Number(d.total ?? 0)} 个快照`
    case 'storage_profile/migrate_cancel':
      return `迁到「${str(d.target_profile_name) ?? '—'}」的迁移`
    case 'storage_profile/migrate_end': {
      const parts = [
        `${formatValue('result', d.result)}：迁到「${str(d.target_profile_name) ?? '—'}」${Number(d.migrated ?? 0)} 个快照`,
        Number(d.failed ?? 0) > 0 ? `核对不一致 ${Number(d.failed)} 个` : null,
        `源存储里还剩 ${Number(d.remaining ?? 0)} 个`,
        str(d.error_message),
      ]
      return parts.filter(Boolean).join('，')
    }
    case 'license/import':
    case 'license/replace': {
      const exp = str(a.expires_at)
      return [
        formatValue('edition', a.edition),
        a.max_users != null ? `${a.max_users} 个会话` : null,
        exp ? `到期 ${formatAuditTime(exp).slice(0, 10)}` : null,
      ]
        .filter(Boolean)
        .join('，')
    }
  }
  if (log.action === 'create' || log.action === 'delete' || log.action === 'mark_done' ||
    log.action === 'back_to_draft' || log.action === 'unlock' || log.action === 'set_default' ||
    log.action === 'unset_default' || log.action === 'enable' || log.action === 'cancel') {
    return ''
  }
  return changed()
}

/** RFC 3339 → 本机时间 `YYYY-MM-DD HH:mm:ss`。 */
export function formatAuditTime(value: string): string {
  return formatDateTime(value, { seconds: true })
}
