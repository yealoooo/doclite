// 上传重扫件（M2-7，design §1.5、§4.3；roadmap M2 的 Q6）：浏览器和壳两条路，结果统一成 RescanOutcome。
//
// - 浏览器：选一个文件 → `lib/cover.ts` 生成封面 → `upload()` 发到 `POST /api/files/{fid}/rescan`（不算 sha）。
// - 壳：`pick_and_rescan {fileId}`（Go，client/internal/shell/rescan.go、internal/upload/rescan.go）：系统对话框选一个文件 → 算 sha → 生成封面 →
//   multipart 发到重扫接口，同步返回，不进上传队列。页面不能指定本机路径。
// 两边一样的规则：
// - 400 `invalid_cover`：去掉封面重传一次；
// - 409 `conflict`（版本号刚被改了）：再发一次，第二次的结果照实给出（例如别人刚重扫成功，就是 `invalid_state`）；
// - 其余错误不重试，交给 `describeRescanError()` 写成提示：`invalid_state` 是"上一次已经成功"（不是失败），
//   `duplicate_content` 是"可能选错了文件"，写出 details 里的位置；
// - 401、403 `forbidden`、426 走请求封装的全局处理（跳登录页、无权提示、整页升级提示），和 `request()` 一样。
// - 选的文件先核对，不合格的不发请求（重扫会把"已驳回"消耗掉，不能撤回）：扩展名只收 jpg、jpeg、tif、tiff、png；
//   文件尾要完整（JPEG 有 EOI、PNG 有 IEND，TIFF 不查，规则同壳的热文件夹 `upload.TailComplete`（client/internal/upload/settle.go）），
//   不完整的多半是扫描软件还在写入。
// - 进度：壳发 `rescan:progress {file_id, phase: hashing|cover|uploading, sent, size}` 事件；浏览器的 fetch 没有上传
//   进度，只报阶段。
//
// 重扫件不要扫进热文件夹：热文件夹会把它当成新的一页，传成另一个文件（另一个 file_id），原来的文件仍是已驳回。


import { ApiError, failResponse, isApiError } from '@/api/http'
import { listRescans, rescanUpload } from '@/api/rescans'
import type { DuplicateContentDetails, LastReject, RescanItem, RescanResponse } from '@/api/types'
import { makeCover } from '@/lib/cover'
import { isShellError } from '@/lib/shell'
import { isSupportedUploadName } from '@/lib/upload-plan'
import { invoke, listen } from '@/lib/wails'

/** 要重扫的文件（对话框显示用）：待重扫列表的一项，或 /files 里的一个已驳回文件。 */
export interface RescanTarget {
  file_id: string
  file_name: string
  folder_id: string
  /** 项目里的位置（不含根文件夹，例如 `2025 文书档案/盒 003`）；null 时对话框按 rescans 补上。 */
  folder_path: string | null
  /** 所在文件夹的备注（原件位置）。 */
  folder_remark: string | null
  version_no: number
  review_round: number
  /** 最近一次驳回；undefined 表示还不知道（对话框按所在文件夹去 rescans 里找），null 表示没有驳回记录。 */
  last_reject?: LastReject | null
}

/** 待重扫列表的一项 → 对话框的 target。 */
export function targetOfRescan(it: RescanItem): RescanTarget {
  return {
    file_id: it.file_id,
    file_name: it.file_name,
    folder_id: it.folder_id,
    folder_path: withinProject(it.folder_path),
    folder_remark: it.folder_remark,
    version_no: it.version_no,
    review_round: it.review_round,
    last_reject: it.last_reject,
  }
}

/** 在待重扫列表里找一个文件的结果。 */
export type RescanLookup =
  | { status: 'found'; item: RescanItem }
  /** 不在待重扫列表里（已经不是已驳回了）。 */
  | { status: 'missing' }
  /** 查失败（网络、权限……），不知道。 */
  | { status: 'error'; message: string }

/** 找驳回原因时一页取多少个。 */
const LOOKUP_PAGE = 500

/**
 * 在文件所在文件夹（子树）的待重扫列表里找这个文件（/files 打开对话框时补驳回原因）。列表按 path_code、sort_no
 * 排序，直接放在这个文件夹里的文件排在子文件夹的前面：跟着 next_cursor 往下翻，找到它、或者翻到了别的文件夹
 * （已经越过这个文件夹）、或者翻完了，就停。
 */
export async function findRescan(projectId: string, folderId: string, fileId: string): Promise<RescanLookup> {
  let cursor: string | null = null
  try {
    for (;;) {
      const res = await listRescans(projectId, { folder_id: folderId, limit: LOOKUP_PAGE, cursor }, true)
      for (const it of res.items) {
        if (it.folder_id !== folderId) return { status: 'missing' }
        if (it.file_id === fileId) return { status: 'found', item: it }
      }
      if (!res.next_cursor || res.items.length === 0) return { status: 'missing' }
      cursor = res.next_cursor
    }
  } catch (e) {
    return { status: 'error', message: e instanceof Error ? e.message : String(e) }
  }
}

/** 封面的情况：带上了、生成不了没带、被 server 拒绝后去掉了。 */
export type RescanCoverStatus = 'included' | 'not_generated' | 'rejected'

export interface RescanCover {
  status: RescanCoverStatus
  /** 没带上的原因（生成不了的原因代码和说明，或 server 的 message）。 */
  reason: string | null
  ms: number | null
}

/** 一次重扫的结论。 */
export type RescanOutcome =
  | { kind: 'cancelled' }
  | {
      kind: 'done'
      result: RescanResponse
      /** 选的本机文件名。 */
      localName: string
      cover: RescanCover
      /** 发了几次请求（invalid_cover、conflict 各多一次）。 */
      attempts: number
      ms: number
    }
  | {
      kind: 'failed'
      error: ApiError
      localName: string | null
      cover: RescanCover | null
      attempts: number
    }

/** 进行到哪一步（对话框里显示）。 */
export type RescanPhase = 'picking' | 'hashing' | 'cover' | 'uploading' | 'retry_without_cover' | 'retry_conflict'

/** 上传的进度（只有壳里有）：已经发出的原图字节数。 */
export interface RescanBytes {
  sent: number
  size: number
}

export type RescanPhaseFn = (phase: RescanPhase, bytes?: RescanBytes) => void

const PATH = (fileId: string) => `/api/files/${fileId}/rescan`

/** 不完整、还在写入的文件（和壳的提示一样）。 */
export const MSG_INCOMPLETE = '选的文件不完整或还在写入，请等扫描软件写完再选'
/** 扩展名不对（和壳的提示一样）。 */
export const MSG_EXTENSION = '只能选 jpg、jpeg、tif、tiff、png 的图片'

/** 文件尾检查的余量（和壳的 `upload.tailSlack` 一样）。 */
const TAIL_SLACK = 4096
const PNG_MAGIC = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]
const PNG_IEND = [0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82]
const JPEG_MAGIC = [0xff, 0xd8, 0xff]
const JPEG_EOI = [0xff, 0xd9]

function startsWith(bytes: Uint8Array, prefix: readonly number[]): boolean {
  return prefix.every((b, i) => bytes[i] === b)
}

function contains(hay: Uint8Array, needle: readonly number[]): boolean {
  outer: for (let i = 0; i + needle.length <= hay.length; i++) {
    for (let j = 0; j < needle.length; j++) if (hay[i + j] !== needle[j]) continue outer
    return true
  }
  return false
}

/**
 * 文件尾是否完整（规则同壳的 `upload.TailComplete`（client/internal/upload/settle.go））：JPEG（魔数 `FF D8 FF`）要在最后 4 KiB + 2 字节里有 EOI
 * `FF D9`；PNG 要在最后 4 KiB + 12 字节里有 IEND 块（含 CRC）；TIFF 和别的格式不查。空文件、只写了魔数开头几个字节的
 * 算不完整。只读开头 8 字节和末尾几 KiB。
 */
export async function tailComplete(file: Blob): Promise<boolean> {
  const head = new Uint8Array(await file.slice(0, 8).arrayBuffer())
  let marker: readonly number[]
  let window: number
  if (head.length >= 3 && startsWith(head, JPEG_MAGIC)) {
    marker = JPEG_EOI
    window = TAIL_SLACK + 2
  } else if (head.length === 8 && startsWith(head, PNG_MAGIC)) {
    marker = PNG_IEND
    window = TAIL_SLACK + 12
  } else if (head.length < 8 && (startsWith(head, PNG_MAGIC.slice(0, head.length)) || startsWith(head, JPEG_MAGIC.slice(0, Math.min(3, head.length))))) {
    // 只写了开头几个字节（含空文件）。
    return false
  } else {
    return true
  }
  const tail = new Uint8Array(await file.slice(Math.max(0, file.size - window)).arrayBuffer())
  return contains(tail, marker)
}

/** 本地文件不合格（不发请求）：code 是 `local_file`，message 写原因。 */
function localError(message: string): ApiError {
  return new ApiError(0, 'local_file', message)
}

// ---------- 浏览器 ----------

/**
 * 浏览器里把 `file` 作为 `fileId` 的重扫件上传。不会抛出（错误放在 outcome 里）。
 * 401、403 `forbidden`、426 照常触发全局处理。
 */
export async function rescanInBrowser(fileId: string, file: File, onPhase: RescanPhaseFn = () => {}): Promise<RescanOutcome> {
  const started = performance.now()
  if (!isSupportedUploadName(file.name)) {
    return { kind: 'failed', error: localError(MSG_EXTENSION), localName: file.name, cover: null, attempts: 0 }
  }
  onPhase('hashing')
  let complete: boolean
  try {
    complete = await tailComplete(file)
  } catch (e) {
    return {
      kind: 'failed',
      error: localError(`读不了选的文件：${e instanceof Error ? e.message : String(e)}`),
      localName: file.name,
      cover: null,
      attempts: 0,
    }
  }
  if (!complete) return { kind: 'failed', error: localError(MSG_INCOMPLETE), localName: file.name, cover: null, attempts: 0 }
  onPhase('cover')
  const made = await makeCover(file)
  let blob = made.blob
  let cover: RescanCover = blob
    ? { status: 'included', reason: null, ms: made.ms }
    : { status: 'not_generated', reason: made.reason ? `${made.reason}：${made.message ?? ''}` : null, ms: null }
  let attempts = 0
  let conflictRetried = false
  onPhase('uploading')
  for (;;) {
    attempts++
    try {
      const { data } = await rescanUpload(fileId, { file_name: file.name, cover: blob, file }, { handle: true })
      return { kind: 'done', result: data, localName: file.name, cover, attempts, ms: Math.round(performance.now() - started) }
    } catch (e) {
      const err = e instanceof ApiError ? e : new ApiError(0, 'internal', e instanceof Error ? e.message : String(e))
      if (blob && err.code === 'invalid_cover') {
        // 封面可以缺，不能因为封面丢页：去掉封面重传一次。
        blob = null
        cover = { status: 'rejected', reason: err.message, ms: cover.ms }
        onPhase('retry_without_cover')
        continue
      }
      if (err.code === 'conflict' && !conflictRetried) {
        conflictRetried = true
        onPhase('retry_conflict')
        continue
      }
      return { kind: 'failed', error: err, localName: file.name, cover, attempts }
    }
  }
}

// ---------- 壳 ----------

/** `pick_and_rescan` 的结果（Go 的 upload.RescanOutcome）。 */
interface ShellRescanOutcome {
  cancelled: boolean
  local_name: string | null
  size: number | null
  sha256: string | null
  cover: { status: RescanCoverStatus; reason: string | null; ms: number | null } | null
  attempts: number
  result: RescanResponse | null
  error: { status: number; code: string; message: string; details: unknown } | null
  elapsed_ms: number
}

/** 壳的 `rescan:progress` 事件。 */
interface ShellRescanProgress {
  file_id: string
  phase: 'hashing' | 'cover' | 'uploading'
  sent: number
  size: number
}

/**
 * 壳里：系统对话框选一个文件，由壳（Go）核对文件、算 sha、生成封面、上传（同步返回，不进上传队列）。期间壳发
 * `rescan:progress` 事件，转给 `onPhase`。
 * 命令本身失败（还没配置服务器、选的文件不完整或读不了、扩展名不对、fileId 不对、正在传另一张）时抛出
 * ShellError `{kind, message}`。
 */
export async function rescanInShell(fileId: string, onPhase: RescanPhaseFn = () => {}): Promise<RescanOutcome> {
  onPhase('picking')
  let lastSent = -1
  const unlisten = await listen<ShellRescanProgress>('rescan:progress', (p) => {
    if (p.file_id !== fileId) return
    // invalid_cover 去掉封面重传时，上传从 0 重新开始。
    if (p.phase === 'uploading' && p.sent === 0 && lastSent > 0) onPhase('retry_without_cover', { sent: 0, size: p.size })
    else onPhase(p.phase, { sent: p.sent, size: p.size })
    lastSent = p.phase === 'uploading' ? p.sent : -1
  }).catch(() => null)
  let out: ShellRescanOutcome
  try {
    out = await invoke<ShellRescanOutcome>('pick_and_rescan', { fileId })
  } finally {
    unlisten?.()
  }
  if (out.cancelled) return { kind: 'cancelled' }
  const cover = out.cover ? { status: out.cover.status, reason: out.cover.reason, ms: out.cover.ms } : null
  if (out.result) {
    return {
      kind: 'done',
      result: out.result,
      localName: out.local_name ?? '',
      cover: cover ?? { status: 'not_generated', reason: null, ms: null },
      attempts: out.attempts,
      ms: out.elapsed_ms,
    }
  }
  const e = out.error ?? { status: 0, code: 'internal', message: '壳没有返回结果', details: null }
  // 和 request() 一样走全局处理（401 跳登录页、403 forbidden、426 整页升级提示）；其余不弹 toast，由对话框显示。
  const error =
    e.status > 0
      ? failResponse(PATH(fileId), e.status, JSON.stringify({ code: e.code, message: e.message, details: e.details }), { handle: true })
      : new ApiError(0, e.code, e.message || '无法连接服务器，请检查网络后重试')
  return { kind: 'failed', error, localName: out.local_name, cover, attempts: out.attempts }
}

// ---------- 提示文字 ----------

/** 给人看的结论。tone：ok（成功或"上一次已经成功"）、warn（可能选错了文件、要重试）、error（其他）。 */
export interface RescanMessage {
  tone: 'ok' | 'warn' | 'error'
  title: string
  detail: string | null
  /** 内容相同的那个文件的位置（`duplicate_content`）。 */
  location: string | null
  /** 这一次没有改变文件的状态，可以重新选择再传。 */
  canRetry: boolean
  /** 文件的状态可能已经变了，列表要刷新。 */
  refresh: boolean
}

/** folder_path 去掉开头的根文件夹（项目名），只留项目里的位置：`2025 文书档案/盒 003`。 */
export function withinProject(folderPath: string): string {
  const i = folderPath.indexOf('/')
  return i < 0 ? '' : folderPath.slice(i + 1)
}

function isDuplicate(d: unknown): d is DuplicateContentDetails {
  return (
    typeof d === 'object' &&
    d !== null &&
    typeof (d as DuplicateContentDetails).file_id === 'string' &&
    typeof (d as DuplicateContentDetails).folder_path === 'string'
  )
}

/** 失败的重扫写成提示（`fileId` 是要重扫的文件，`versionNo` 是它现在的版本号）。 */
export function describeRescanError(err: unknown, fileId: string, versionNo?: number): RescanMessage {
  if (isShellError(err)) {
    return { tone: 'error', title: '没有上传', detail: err.message, location: null, canRetry: true, refresh: false }
  }
  if (!isApiError(err)) {
    return { tone: 'error', title: '没有上传', detail: err instanceof Error ? err.message : String(err), location: null, canRetry: true, refresh: false }
  }
  switch (err.code) {
    case 'invalid_state':
      return {
        tone: 'ok',
        title: '这个文件已经不是"已驳回"了',
        detail: '上一次重扫可能已经成功（例如等结果时网络断了），也可能刚被别人重扫或处理过。列表已刷新，请看它现在的状态，不用再传。',
        location: null,
        canRetry: false,
        refresh: true,
      }
    case 'duplicate_content': {
      const d = isDuplicate(err.details) ? err.details : null
      if (d && d.file_id === fileId) {
        return {
          tone: 'warn',
          title: '选的图片和这个文件现在的版本一模一样，可能选错了文件',
          detail: `等于没有重扫${versionNo ? `（和现在的 v${versionNo} 相同）` : ''}。请确认选的是新扫的那一张，再重新选择。`,
          location: null,
          canRetry: true,
          refresh: false,
        }
      }
      const where = d ? [withinProject(d.folder_path), d.file_name].filter(Boolean).join('/') : null
      return {
        tone: 'warn',
        title: '选的图片和项目里另一个文件内容相同，可能选错了文件',
        detail: '没有上传。请确认选的是这一页新扫的图片，再重新选择。',
        location: where,
        canRetry: true,
        refresh: false,
      }
    }
    case 'conflict':
      return {
        tone: 'warn',
        title: '这个文件的版本号刚被改了，没有上传',
        detail: '可能有人同时在重扫它。列表已刷新：如果它仍是"已驳回"，请重新选择再传。',
        location: null,
        canRetry: true,
        refresh: true,
      }
    case 'not_found':
      return { tone: 'error', title: '文件不存在或已被删除', detail: '列表已刷新。', location: null, canRetry: false, refresh: true }
    case 'local_file':
      return { tone: 'error', title: '没有上传', detail: err.message, location: null, canRetry: true, refresh: false }
    case 'checksum_mismatch':
      return { tone: 'error', title: '图片在上传过程中被改动了', detail: '请等扫描软件写完，再重新选择。', location: null, canRetry: true, refresh: false }
    case 'network':
      return {
        tone: 'error',
        title: '无法连接服务器，不知道这次有没有传上去',
        detail: '请检查网络后刷新列表：文件仍是"已驳回"就重新选择再传；已经不在列表里说明传上去了。',
        location: null,
        canRetry: true,
        refresh: true,
      }
    default:
      return { tone: 'error', title: '没有上传', detail: err.message, location: null, canRetry: err.status !== 403, refresh: false }
  }
}

/**
 * 成功后自动分给了谁。`lastReject` 是这次重扫之前最近一次驳回；null 表示页面没拿到它（查不到或查失败），
 * 这时只用中性的说法，不猜原因。驳回原因的中文一律用 `lib/reviews.ts` 的 `rejectReasonLabel`。
 */
export function reassignText(result: RescanResponse, lastReject: LastReject | null): string {
  if (result.review_status === 'unassigned' || !result.reviewer_id) {
    return lastReject ? `${lastReject.reviewer_name} 已不能再审这个项目，改为待分配` : '改为待分配，请负责人重新分配'
  }
  if (!lastReject) return '已自动分给审核人'
  if (result.reviewer_id === lastReject.reviewer_id) return `自动分给 ${lastReject.reviewer_name}`
  // 重扫前这个文件已经有进行中的分配（M2-1）：回到那一行的审核人。
  return '已分给审核人（文件原来就有进行中的分配）'
}
