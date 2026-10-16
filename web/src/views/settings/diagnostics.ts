// 设置页的"复制诊断信息"（M7-11）：把设置页上显示的非敏感信息（版本、运行环境、服务器地址和连接、账号、本机目录、
// 缓存、上传队列、热文件夹、客户端的能力）拼成一段文字，用户贴给维护的人。
// **不带令牌、Cookie、密码**：页面本来就拿不到会话 Cookie（在客户端的 Go 端），这里也只用下面这些字段，不整个转储
// shell_info、接口响应。

import { formatBytes } from '@/lib/files'
import type { HotFolderStatus, QueueStatus } from '@/lib/scan-queue'
import type { LocalDirs } from '@/lib/shell'

export type HealthResult =
  | { ok: true; ms: number; version: string }
  | { ok: false; ms: number | null; error: string }

export interface DiagnosticsInput {
  at: Date
  clientVersion: string | null
  webVersion: string
  /** 运行环境（platformLabel 的结果）。 */
  runtime: string
  serverUrl: string | null
  health: HealthResult | null
  user: { username: string; displayName: string; roles: string[] } | null
  dataDir: string | null
  dirs: LocalDirs | null
  cache: { totalBytes: number; limitBytes: number; fileCount: number; available: boolean } | null
  queue: QueueStatus | null
  hotfolder: HotFolderStatus | null
  capabilities: string[]
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}

function timeText(d: Date): string {
  const off = -d.getTimezoneOffset()
  const sign = off >= 0 ? '+' : '-'
  const tz = `UTC${sign}${pad(Math.floor(Math.abs(off) / 60))}:${pad(Math.abs(off) % 60)}`
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}（${tz}）`
}

/** 拼成给人看的几行文字。 */
export function formatDiagnostics(d: DiagnosticsInput): string {
  const lines: string[] = ['Doclite 诊断信息', `时间：${timeText(d.at)}`]
  lines.push(`客户端版本：${d.clientVersion ?? '—'}`, `界面版本：${d.webVersion}`, `运行环境：${d.runtime}`)

  lines.push(`服务器：${d.serverUrl ?? '还没有配置'}`)
  const h = d.health
  if (h?.ok) lines.push(`连接：正常（${h.ms} ms），服务端版本 ${h.version}`)
  else if (h) lines.push(`连接：连不上（${h.error}）`)

  if (d.user) {
    const roles = d.user.roles.length ? d.user.roles.join('、') : '没有角色'
    lines.push(`登录：${d.user.displayName}（${d.user.username}，${roles}）`)
  } else {
    lines.push('登录：未登录')
  }

  if (d.dirs) {
    lines.push(`数据目录：${d.dirs.data.path}（${formatBytes(d.dirs.data.size_bytes)}）`)
    lines.push(`日志目录：${d.dirs.logs.path}（${formatBytes(d.dirs.logs.size_bytes)}，${d.dirs.logs.file_count} 个文件）`)
  } else if (d.dataDir) {
    lines.push(`数据目录：${d.dataDir}`)
  }
  if (d.cache) {
    const state = d.cache.available ? '' : '，不可用'
    lines.push(
      `本机图片缓存：${formatBytes(d.cache.totalBytes)} / ${formatBytes(d.cache.limitBytes)}，${d.cache.fileCount} 个文件${state}`,
    )
  }
  if (d.queue) {
    const c = d.queue.stats
    let text = `上传队列：等待 ${c.waiting + c.queued} · 上传中 ${c.uploading} · 失败 ${c.failed} · 已完成 ${c.done + c.duplicate}`
    if (d.queue.paused) text += `（已暂停：${d.queue.paused.reason === 'auth_required' ? '需要重新登录' : '客户端需要升级'}）`
    lines.push(text)
  }
  if (d.hotfolder) {
    const hf = d.hotfolder
    const state = hf.running ? '运行中' : hf.config ? '已暂停' : '未设置'
    let text = `热文件夹：${state}`
    if (hf.config) text += `，监控 ${hf.config.watch_dir}`
    if (hf.error) text += `；最近的问题：${hf.error}`
    lines.push(text)
  }
  lines.push(`客户端的能力：${d.capabilities.length ? d.capabilities.join(', ') : '—'}`)
  return lines.join('\n')
}

/** 运行环境：系统（版本）· 架构。浏览器里是"浏览器"。 */
export function platformLabel(info: { platform?: string; arch?: string; os_version?: string | null } | null): string {
  if (!info) return '浏览器'
  const name = info.platform === 'macos' ? 'macOS' : info.platform === 'windows' ? 'Windows' : (info.platform ?? '未知系统')
  const parts = [info.os_version || name]
  if (info.arch) parts.push(info.arch)
  return parts.join(' · ')
}
