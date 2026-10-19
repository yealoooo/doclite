// 存储配置页用到的文字、状态规则预判和表单校验，和 server 的 doclite_domain::storage_profiles 一致。
// 预判只是为了把按钮提前禁用并说明原因，后端仍会检查（违反时返回 409）。

import { isApiError } from '@/api/http'
import type {
  ConnectionTestResultCode,
  PublicEndpointCheck,
  StorageMigration,
  StorageMigrationFailureCode,
  StorageProfileItem,
  StorageStatus,
} from '@/api/types'

import type { StatusStyle } from './status'
import { charCount } from './validate'

export const STATUS_LABEL: Record<StorageStatus, string> = {
  active: '可读写',
  readonly: '只读',
  disabled: '停用',
}

/** 存储状态的徽标。 */
export const STORAGE_STATUS: Record<StorageStatus, StatusStyle> = {
  active: { label: STATUS_LABEL.active, badge: 'bg-ok-soft text-ok' },
  readonly: { label: STATUS_LABEL.readonly, badge: 'bg-warn-soft text-warn' },
  disabled: { label: STATUS_LABEL.disabled, badge: 'bg-destructive-soft text-destructive' },
}

export const RESULT_LABEL: Record<ConnectionTestResultCode, string> = {
  ok: '连接成功',
  unreachable: '连不上',
  auth_failed: '密钥错误',
  bucket_not_found: '桶不存在',
  secret_unreadable: '主密钥不匹配',
  other: '测试失败',
}

export const PUBLIC_RESULT_LABEL: Record<PublicEndpointCheck['result'], string> = {
  ok: '工作站地址可用',
  unreachable: 'server 访问不到工作站地址',
  rejected: '工作站地址不可用',
}

/** 409 错误码 → 中文标题（说明用后端返回的 message）。 */
const CONFLICT_TITLE: Record<string, string> = {
  storage_not_active: '只有可读写（启用）的存储才能设为默认',
  storage_is_default: '默认存储不能这样操作',
  storage_in_use: '存储还在使用中',
  invalid_state: '存储正在迁移中',
}

/** 状态规则的 409 错误的中文标题；不是这类错误时返回 null。 */
export function conflictTitle(err: unknown): string | null {
  if (!isApiError(err)) return null
  return CONFLICT_TITLE[err.code] ?? null
}

// ---------- 状态规则预判 ----------

/** 设为默认不能做的原因；可以做时为 null。 */
export function setDefaultBlock(p: StorageProfileItem): string | null {
  if (p.status !== 'active') return '只有可读写（启用）的存储才能设为默认'
  if (migrationActive(p.migration)) return '这个存储正在迁出，设为默认后新文件又会写回来；等迁移结束或先取消'
  return null
}

/** 改成 `to` 状态不能做的原因；可以做时为 null。 */
export function statusBlock(p: StorageProfileItem, to: StorageStatus): string | null {
  if (p.status === to) return null
  if (p.is_default && to !== 'active') return '默认存储不能改为只读或停用，请先把别的存储设为默认'
  if (to !== 'active' && p.incoming_migration) return '正在往这个存储迁移文件，不能改为只读或停用；等迁移结束或先取消'
  if (to === 'disabled' && p.in_use) return '还有文件存在这个存储里，不能停用；可以改为只读，或者先迁到别的存储'
  return null
}

/** 删除不能做的原因；可以做时为 null。 */
export function deleteBlock(p: StorageProfileItem): string | null {
  if (p.is_default) return '默认存储不能删除，请先把别的存储设为默认'
  if (migrationActive(p.migration) || p.incoming_migration) return '这个存储正在迁移中，不能删除；等迁移结束或先取消'
  if (!p.deletable) return '这个存储还被文件快照或导出任务引用，不能删除；不再使用可以改为只读或停用'
  return null
}

// ---------- 存储迁移（M7-2） ----------

/** 迁移排队中或执行中。 */
export function migrationActive(m: StorageMigration | null | undefined): m is StorageMigration {
  return !!m && (m.status === 'queued' || m.status === 'running')
}

/** 能迁入的存储：别的、可读写的、不是同一个位置、不在迁出的。 */
export function migrationTargets(p: StorageProfileItem, all: StorageProfileItem[]): StorageProfileItem[] {
  return all.filter(
    (t) =>
      t.id !== p.id &&
      t.status === 'active' &&
      !migrationActive(t.migration) &&
      !(t.endpoint === p.endpoint && t.bucket === p.bucket && t.path_prefix === p.path_prefix),
  )
}

/** "迁移到…"不能做的原因；可以做时为 null。整个系统同一时间只有一个迁移在排队或执行。 */
export function migrateBlock(p: StorageProfileItem, all: StorageProfileItem[]): string | null {
  if (migrationActive(p.migration)) return '这个存储正在迁出'
  const running = all.find((x) => migrationActive(x.migration))
  if (running) return `已有存储迁移在进行（「${running.profile_name}」迁到「${running.migration?.target_profile_name}」），请等它完成或取消`
  if (!p.in_use) return '这个存储里没有文件，不用迁移'
  if (p.is_default) return '这是默认存储，新上传的文件还会写进来；请先把别的存储（比如要迁去的那个）设为默认，再迁移'
  if (!p.secret_readable) return '这个存储的 Secret Key 解不开，请先重新填写'
  if (migrationTargets(p, all).length === 0) return '没有别的可读写的存储可以迁入，请先新增一个'
  return null
}

/** 迁移状态的中文和配色。 */
export function migrationStatusOf(m: StorageMigration): StatusStyle {
  if (m.status === 'running' && m.cancel_requested) return { label: '正在取消', badge: 'bg-muted text-muted-foreground' }
  if (m.status === 'queued' && m.attempts > 0) return { label: '等待重试', badge: 'bg-warn-soft text-warn' }
  switch (m.status) {
    case 'queued':
      return { label: '排队中', badge: 'bg-muted text-muted-foreground' }
    case 'running':
      return { label: '迁移中', badge: 'bg-info-soft text-info' }
    case 'done':
      return m.failed > 0
        ? { label: '完成（有失败）', badge: 'bg-warn-soft text-warn' }
        : { label: '完成', badge: 'bg-ok-soft text-ok' }
    case 'failed':
      return { label: '失败', badge: 'bg-destructive-soft text-destructive' }
    case 'canceled':
      return { label: '已取消', badge: 'bg-muted text-muted-foreground' }
  }
}

/** 进度百分比（已迁 + 失败）/ 总数。 */
export function migrationPercent(m: StorageMigration): number {
  if (m.total <= 0) return m.status === 'done' ? 100 : 0
  return Math.min(100, Math.floor(((m.migrated + m.failed) * 100) / m.total))
}

export const MIGRATION_FAILURE_LABEL: Record<StorageMigrationFailureCode, string> = {
  source_missing: '源对象缺失',
  cover_missing: '封面缺失',
  sha256_mismatch: 'sha256 不一致',
  size_mismatch: '大小不一致',
  verify_failed: '复制后核对不一致',
  inconsistent: '快照记录不一致',
}

// ---------- 表单校验（只做预检，后端仍会校验） ----------

export const MAX_PROFILE_NAME_CHARS = 64
export const MAX_REMARK_CHARS = 500

export function checkProfileName(v: string): string {
  const s = v.trim()
  if (!s || charCount(s) > MAX_PROFILE_NAME_CHARS) return `名称不能为空，最长 ${MAX_PROFILE_NAME_CHARS} 个字符`
  return ''
}

/** S3 地址：http:// 或 https:// 开头，只能是根路径。`label` 是字段名。 */
export function checkEndpoint(v: string, label: string, required: boolean): string {
  const s = v.trim()
  if (!s) return required ? `${label}不能为空` : ''
  let url: URL
  try {
    url = new URL(s)
  } catch {
    return `${label}不是合法的地址，例如 http://127.0.0.1:8333`
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') return `${label}要以 http:// 或 https:// 开头`
  if (url.username || url.password) return `${label}不能带用户名和密码`
  if (url.pathname !== '/' || url.search || url.hash) return `${label}只能是根路径（http://主机:端口），不支持子路径`
  return ''
}

/** 桶名：3–63 个字符，小写字母、数字、. 和 -，以字母或数字开头和结尾。 */
export function checkBucket(v: string): string {
  const s = v.trim()
  if (!/^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/.test(s) || s.includes('..')) {
    return '桶名要有 3 到 63 个字符，只能有小写字母、数字、. 和 -，以字母或数字开头和结尾'
  }
  return ''
}

export function checkRegion(v: string): string {
  if (!/^[A-Za-z0-9_-]{1,64}$/.test(v.trim())) return 'region 只能有字母、数字、- 和 _，例如 us-east-1'
  return ''
}

/** key 前缀：可以为空；不为空时以 / 结尾、不以 / 开头，每段只能有英文字母、数字和 - _ .。 */
export function checkPathPrefix(v: string): string {
  const s = v.trim()
  if (!s) return ''
  if (s.startsWith('/')) return '统一前缀不能以 / 开头'
  if (!s.endsWith('/')) return '统一前缀不为空时要以 / 结尾，例如 doclite/'
  const segments = s.slice(0, -1).split('/')
  if (segments.some((seg) => !seg || seg === '.' || seg === '..' || !/^[A-Za-z0-9._-]+$/.test(seg))) {
    return '统一前缀的每一段只能有英文字母、数字和 - _ .，不能有空段、. 或 ..'
  }
  return ''
}

export function checkKey(v: string, label: string, required: boolean): string {
  const s = v.trim()
  if (!s) return required ? `${label}不能为空` : ''
  if (/\s/.test(s)) return `${label}不能有空格`
  return ''
}

export function checkUrlExpireHours(v: number | string): string {
  const n = Number(v)
  if (!Number.isInteger(n) || n < 1 || n > 168) return 'URL 有效期要在 1 到 168 小时之间'
  return ''
}

export function checkRemark(v: string): string {
  if (charCount(v.trim()) > MAX_REMARK_CHARS) return `备注最长 ${MAX_REMARK_CHARS} 个字符`
  return ''
}
