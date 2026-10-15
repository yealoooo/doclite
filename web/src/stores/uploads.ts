// 浏览器上传（M1-12，design §4.3）：上传文件、上传文件夹，结果列表的状态。
//
//   const uploads = useUploadsStore()
//   uploads.uploadFiles(target, files)       // 传到 target 本身
//   uploads.uploadFolder(target, files)      // files 带 webkitRelativePath（<input webkitdirectory>，或截图脚本注入的）
//
// - 上传文件夹：按相对路径用 `POST /api/folders`（if_exists: return）逐层建子文件夹（连同选中的文件夹本身）。
//   建某一层失败（名字不合法、tree_full、父文件夹不在了……）时，这一层和下面各层的文件都记为失败。
// - 并发：同时最多传 2 个文件。同一个文件夹里的文件按文件名的自然顺序一个接一个传（server 按到达顺序给 sort_no，
//   并行传会打乱纸张顺序），不同的子文件夹之间并行，和壳的上传队列一样。任务之间排队，一次只跑一个。
// - 顺序（和壳的错误分类一致，roadmap M1-9）：
//   - 临时失败（连不上、408、429、5xx、409 `storage_changed`/`storage_unavailable`）先在原位退避重试：
//     1、2、4、8、16 秒，最多 5 次，期间同一个文件夹后面的文件等着；还不行就停下这个文件夹，这一页记为失败，
//     后面的记为"未传（前一页失败）"。"重试失败的"从失败的那一页起按原顺序补传，所以 sort_no 仍是文件名顺序。
//   - 永久失败（400、413、415 等）只记这一页，后面的照常传（这一页本身有问题，重传也不会好）。
//   - 目标文件夹不在了（404）：这个文件夹剩下的都不传了。
//   - 401（会话失效）、403（没权限了）：停下整个任务，剩下的记为未传（全局处理会跳登录页或刷新菜单）。
// - 封面（lib/cover.ts）：传当前文件时先生成同一个文件夹里下一个文件的封面（流水作业）；生成不了就不带封面上传。
//   server 返回 400 `invalid_cover` 时去掉封面重传一次。
// - 浏览器不算 sha、不调预检（内网 http 不是安全上下文），由 server 判重：201 已入库；200 之前已上传过；
//   409 `duplicate_content` 内容重复，已跳过。
// - 计数（结果列表的标题）随状态变化增量维护（`job.counts`），文件多时不用每次都数一遍。
// - 换了登录用户：停下正在进行的，清空记录。同一个用户重新登录（会话失效后）不清，可以接着"重试失败的"。

import { defineStore } from 'pinia'
import { reactive, ref, watch } from 'vue'

import { createFolder } from '@/api/folders'
import { uploadToFolder } from '@/api/folder-files'
import { ApiError, errorMessage, isApiError } from '@/api/http'
import type { DuplicateContentDetails, IngestResponse } from '@/api/types'
import { coverFailureLabel, makeCover, type CoverResult } from '@/lib/cover'
import { isLowDpi } from '@/lib/files'
import { dirKey, planUpload, type UploadPlan } from '@/lib/upload-plan'
import { useSessionStore } from '@/stores/session'

/** 同时上传的文件数（也是同时进行的子文件夹数）。 */
export const UPLOAD_CONCURRENCY = 2
/** 临时失败时原位重试的等待（毫秒），用完还不行就停下这个文件夹。 */
export const RETRY_DELAYS_MS = [1000, 2000, 4000, 8000, 16000]

export type UploadItemState =
  | 'waiting'
  | 'cover'
  | 'uploading'
  | 'retrying'
  | 'done'
  | 'already'
  | 'duplicate'
  | 'failed'
  | 'held'
  | 'skipped'
  | 'cancelled'

/** 封面：pending 还没生成；included 带上了；none 生成不了；rejected 被 server 拒收、去掉后重传了。 */
export type UploadCoverState = 'pending' | 'included' | 'none' | 'rejected'

export interface UploadItem {
  id: number
  file: File
  name: string
  /** 相对目标文件夹的子目录。 */
  dir: string[]
  state: UploadItemState
  cover: UploadCoverState
  /** 生成不了封面、被拒收的原因（给人看）。 */
  coverReason: string | null
  /** 生成封面用了多少毫秒。 */
  coverMs: number | null
  /** 已入库、之前已上传过时 server 的响应。 */
  result: IngestResponse | null
  /** 内容重复时已有的文件。 */
  duplicate: DuplicateContentDetails | null
  errorCode: string | null
  error: string | null
  /** 等待重试时的说明（"第 2 次重试，4 秒后"）。 */
  retry: string | null
}

export interface UploadTarget {
  id: string
  name: string
  projectId: string
}

export type UploadJobState = 'queued' | 'running' | 'done' | 'cancelled'

/** 结果列表标题上的计数。 */
export interface UploadCounts {
  total: number
  done: number
  already: number
  duplicate: number
  failed: number
  held: number
  skipped: number
  cancelled: number
  /** 还没结束的（等待、生成封面、上传中、等重试）。 */
  active: number
  renamed: number
  lowDpi: number
  noCover: number
}

export interface UploadJob {
  id: number
  kind: 'files' | 'folder'
  target: UploadTarget
  items: UploadItem[]
  state: UploadJobState
  counts: UploadCounts
  /** 直接忽略的系统文件、隐藏文件个数。 */
  ignored: number
  /** 建子文件夹失败的说明。 */
  folderErrors: { path: string; message: string }[]
  /** 建了（或取到已有的）几个子文件夹（不重复计）。 */
  folders: number
  startedAt: number | null
  finishedAt: number | null
}

const FINAL: ReadonlySet<UploadItemState> = new Set(['done', 'already', 'duplicate', 'failed', 'held', 'skipped', 'cancelled'])

export function isFinal(state: UploadItemState): boolean {
  return FINAL.has(state)
}

function emptyCounts(): UploadCounts {
  return { total: 0, done: 0, already: 0, duplicate: 0, failed: 0, held: 0, skipped: 0, cancelled: 0, active: 0, renamed: 0, lowDpi: 0, noCover: 0 }
}

/** 一项对计数的贡献（状态变化时先减旧的、再加新的）。 */
function contribute(c: UploadCounts, it: UploadItem, sign: 1 | -1) {
  switch (it.state) {
    case 'done':
    case 'already':
    case 'duplicate':
    case 'failed':
    case 'held':
    case 'skipped':
    case 'cancelled':
      c[it.state] += sign
      break
    default:
      c.active += sign
  }
  const r = it.result
  if (r && (it.state === 'done' || it.state === 'already')) {
    if (it.state === 'done' && r.renamed) c.renamed += sign
    if (isLowDpi(r.dpi)) c.lowDpi += sign
    if (!r.has_cover) c.noCover += sign
  }
}

/** 临时失败：连不上、408、429、5xx、409 `storage_changed`/`storage_unavailable`（和壳的分类一致）。 */
export function isTransient(e: unknown): boolean {
  if (!(e instanceof ApiError)) return false
  if (e.code === 'aborted') return false
  if (e.status === 0) return e.code === 'network'
  if (e.status === 408 || e.status === 429 || e.status >= 500) return true
  return e.status === 409 && (e.code === 'storage_changed' || e.code === 'storage_unavailable')
}

/** 上传失败时给人看的说明。 */
function uploadErrorMessage(e: unknown): string {
  if (!(e instanceof ApiError)) return errorMessage(e)
  switch (e.code) {
    case 'not_found':
      return '目标文件夹已不存在（可能刚被删除）'
    case 'payload_too_large':
      return `文件太大：${e.message}`
    case 'unsupported_media_type':
      return `格式不支持：${e.message}`
    case 'invalid_image':
      return `图片不合规：${e.message}`
    case 'invalid_input':
      return `文件名或请求不合规：${e.message}`
    case 'storage_changed':
      return '默认存储刚被更换'
    case 'network':
      return '网络中断'
    default:
      return e.message
  }
}

/** 等 ms 毫秒；signal 中止时提前结束（返回 false）。 */
function sleep(ms: number, signal: AbortSignal): Promise<boolean> {
  return new Promise((resolve) => {
    if (signal.aborted) return resolve(false)
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort)
      resolve(true)
    }, ms)
    const onAbort = () => {
      clearTimeout(timer)
      resolve(false)
    }
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

const HELD = '未传（前一页失败）'

export const useUploadsStore = defineStore('uploads', () => {
  const jobs = ref<UploadJob[]>([])
  let nextJobId = 1
  let nextItemId = 1
  /** 任务排队：一次只跑一个（保证全局同时最多传 2 个）。 */
  let chain: Promise<unknown> = Promise.resolve()
  const controllers = new Map<number, AbortController>()
  /** 每个任务已经建好（或取到）的子文件夹：重试时不重复计数。 */
  const createdFolders = new Map<number, Set<string>>()

  // 还在上传时关闭或刷新页面：浏览器先确认一下（上传在页面里进行，刷新就中断了）。
  if (typeof window !== 'undefined') {
    window.addEventListener('beforeunload', (e) => {
      if (!jobs.value.some((j) => j.state === 'running' || j.state === 'queued')) return
      e.preventDefault()
      // 老一些的浏览器要设 returnValue 才弹确认。
      e.returnValue = ''
    })
  }

  /** 改一项：同时维护任务的计数。所有对状态、结果的修改都走这里。 */
  function update(job: UploadJob, it: UploadItem, patch: Partial<UploadItem>) {
    contribute(job.counts, it, -1)
    Object.assign(it, patch)
    contribute(job.counts, it, 1)
  }

  function makeItem(f: { file: File; name: string; dir: string[] }, state: UploadItemState = 'waiting'): UploadItem {
    return {
      id: nextItemId++,
      file: f.file,
      name: f.name,
      dir: f.dir,
      state,
      cover: 'pending',
      coverReason: null,
      coverMs: null,
      result: null,
      duplicate: null,
      errorCode: null,
      error: null,
      retry: null,
    }
  }

  function createJob(kind: 'files' | 'folder', target: UploadTarget, plan: UploadPlan): UploadJob {
    const items: UploadItem[] = []
    for (const g of plan.groups) for (const f of g.files) items.push(makeItem(f))
    for (const s of plan.skipped) {
      const it = makeItem(s, 'skipped')
      it.error = s.reason
      items.push(it)
    }
    const counts = emptyCounts()
    for (const it of items) {
      counts.total++
      contribute(counts, it, 1)
    }
    const job = reactive<UploadJob>({
      id: nextJobId++,
      kind,
      target,
      items,
      state: 'queued',
      counts,
      ignored: plan.ignored,
      folderErrors: [],
      folders: 0,
      startedAt: null,
      finishedAt: null,
    }) as UploadJob
    jobs.value.unshift(job)
    return job
  }

  function enqueue(job: UploadJob) {
    const run = chain.then(() => runJob(job))
    chain = run.catch(() => {})
    return run
  }

  /** 上传文件到 target 本身（按文件名的自然顺序）。返回结果列表的任务（立即返回，上传在后台进行）。 */
  function uploadFiles(target: UploadTarget, files: readonly File[]): UploadJob {
    const job = createJob('files', target, planUpload(files, 'files'))
    void enqueue(job)
    return job
  }

  /**
   * 上传文件夹：files 是 `<input webkitdirectory>` 选出的文件（每个都带 `webkitRelativePath`，形如
   * `选中的文件夹/子文件夹/文件名`）。按相对路径逐层建子文件夹（连同选中的文件夹本身），再把文件传进对应的文件夹。
   */
  function uploadFolder(target: UploadTarget, files: readonly File[]): UploadJob {
    const job = createJob('folder', target, planUpload(files, 'folder'))
    void enqueue(job)
    return job
  }

  /** 失败、未传、取消的按原顺序重新上传（之前已建好的子文件夹不再建）。 */
  function retryFailed(job: UploadJob) {
    if (job.state === 'running' || job.state === 'queued') return
    let n = 0
    for (const it of job.items) {
      if (it.state !== 'failed' && it.state !== 'held' && it.state !== 'cancelled') continue
      update(job, it, { state: 'waiting', cover: 'pending', coverReason: null, coverMs: null, errorCode: null, error: null, retry: null })
      n++
    }
    if (!n) return
    job.folderErrors = []
    job.state = 'queued'
    job.finishedAt = null
    void enqueue(job)
  }

  /** 停止：还没开始的不再传，正在传的中止。 */
  function cancel(job: UploadJob) {
    if (job.state === 'done' || job.state === 'cancelled') return
    job.state = 'cancelled'
    controllers.get(job.id)?.abort()
    for (const it of job.items) {
      if (!isFinal(it.state) && it.state !== 'uploading') update(job, it, { state: 'cancelled', retry: null })
    }
  }

  /** 从列表里去掉已经结束的任务。 */
  function clearFinished() {
    jobs.value = jobs.value.filter((j) => j.state === 'running' || j.state === 'queued')
  }

  function dismiss(job: UploadJob) {
    if (job.state === 'running' || job.state === 'queued') return
    jobs.value = jobs.value.filter((j) => j !== job)
    createdFolders.delete(job.id)
  }

  /** 全部停下并清空（换了登录用户）。 */
  function reset() {
    for (const j of jobs.value) cancel(j)
    jobs.value = []
    createdFolders.clear()
  }

  // 换了登录用户：停下并清空（别人的上传记录不留给下一个人）。会话失效时 me 变成 null，同一个人重新登录不清。
  const session = useSessionStore()
  let lastUser: string | null = null
  watch(
    () => session.me?.id ?? null,
    (id) => {
      if (!id) return
      if (lastUser && lastUser !== id) reset()
      lastUser = id
    },
    { immediate: true },
  )

  // ---------- 执行 ----------

  async function runJob(job: UploadJob) {
    if (job.state === 'cancelled') return
    job.state = 'running'
    job.startedAt ??= Date.now()
    const ac = new AbortController()
    controllers.set(job.id, ac)
    /** 401、403：整个任务停下。 */
    let stopped: string | null = null
    const stop = (why: string) => {
      stopped ??= why
    }
    const live = () => !stopped && job.state === 'running'

    try {
      const pending = job.items.filter((it) => it.state === 'waiting')
      // 子目录 → server 文件夹 id（目标本身是空键）。
      const folderIds = new Map<string, string | null>([['', job.target.id]])
      if (job.kind === 'folder') await ensureFolders(job, pending, folderIds, ac.signal, stop)

      // 按子文件夹分组（保持计划里的顺序：组按路径、组内按文件名的自然顺序）。
      const groups = new Map<string, UploadItem[]>()
      for (const it of pending) {
        if (it.state !== 'waiting') continue
        const k = dirKey(it.dir)
        let g = groups.get(k)
        if (!g) groups.set(k, (g = []))
        g.push(it)
      }
      const queue = [...groups.entries()]

      // 封面流水作业：makeCover 内部排队，一次只生成一张；每个文件开始传时，先请求同组下一个的封面。
      const covers = new Map<number, Promise<CoverResult>>()
      const coverFor = (it: UploadItem) => {
        let p = covers.get(it.id)
        if (!p) {
          p = makeCover(it.file)
          covers.set(it.id, p)
        }
        return p
      }

      /** 这一组从 i 起剩下的（还在等的）记为某个状态。 */
      const markRest = (items: UploadItem[], from: number, patch: Partial<UploadItem>) => {
        for (const rest of items.slice(from)) if (rest.state === 'waiting') update(job, rest, patch)
      }

      const worker = async () => {
        for (;;) {
          const next = queue.shift()
          if (!next) return
          const [key, items] = next
          const folderId = folderIds.get(key) ?? null
          for (let i = 0; i < items.length; i++) {
            const it = items[i]!
            if (!live()) break
            if (it.state !== 'waiting') continue
            if (!folderId) {
              update(job, it, { state: 'failed', error: it.error ?? '子文件夹没有建成' })
              continue
            }
            update(job, it, { state: 'cover' })
            const cover = await coverFor(it)
            covers.delete(it.id)
            const coverPatch: Partial<UploadItem> = { coverMs: cover.ms }
            if (!cover.blob) {
              coverPatch.cover = 'none'
              coverPatch.coverReason = cover.message ?? coverFailureLabel(cover.reason)
            }
            update(job, it, coverPatch)
            // 流水作业：传这个的时候生成下一个的封面。
            const following = items[i + 1]
            if (following && live()) void coverFor(following)
            if (!live()) {
              update(job, it, { state: 'cancelled' })
              break
            }
            const outcome = await uploadWithRetry(job, it, folderId, cover.blob, ac.signal, stop)
            if (outcome === 'gone') {
              // 目标文件夹没了：这一组剩下的都不传了。
              markRest(items, i + 1, { state: 'failed', errorCode: 'not_found', error: '目标文件夹已不存在（可能刚被删除）' })
              break
            }
            if (outcome === 'blocked') {
              // 临时失败重试用完：停下这一组，后面的等"重试失败的"按顺序补传。
              markRest(items, i + 1, { state: 'held', error: HELD })
              break
            }
          }
        }
      }
      await Promise.all(Array.from({ length: UPLOAD_CONCURRENCY }, worker))
    } finally {
      controllers.delete(job.id)
      for (const it of job.items) {
        if (isFinal(it.state)) continue
        update(job, it, stopped ? { state: 'held', error: stopped, retry: null } : { state: 'cancelled', retry: null })
      }
      if (job.state === 'running') job.state = 'done'
      job.finishedAt = Date.now()
    }
  }

  /**
   * 临时失败原位重试：`attempt()` 抛出临时错误时按 RETRY_DELAYS_MS 等了再试，期间调 `onWait(第几次, 秒)`。
   * 返回结果，或抛出最后一次的错误（重试用完、永久错误、被中止）。
   */
  async function withRetry<T>(
    attempt: () => Promise<T>,
    signal: AbortSignal,
    onWait: (n: number, seconds: number, e: unknown) => void,
  ): Promise<T> {
    for (let n = 0; ; n++) {
      try {
        return await attempt()
      } catch (e) {
        const delay = RETRY_DELAYS_MS[n]
        if (!isTransient(e) || delay === undefined) throw e
        onWait(n + 1, delay / 1000, e)
        if (!(await sleep(delay, signal))) throw new ApiError(0, 'aborted', '已取消')
      }
    }
  }

  /** 逐层建子文件夹（父在前）。建不成的层，它和下面各层的文件记为失败。临时失败照样原位重试。 */
  async function ensureFolders(
    job: UploadJob,
    items: UploadItem[],
    folderIds: Map<string, string | null>,
    signal: AbortSignal,
    stop: (why: string) => void,
  ) {
    let created = createdFolders.get(job.id)
    if (!created) createdFolders.set(job.id, (created = new Set()))
    for (const dir of planDirs(items)) {
      if (job.state !== 'running') return
      const key = dirKey(dir)
      if (folderIds.has(key)) continue
      const parentId = folderIds.get(dirKey(dir.slice(0, -1))) ?? null
      if (!parentId) {
        folderIds.set(key, null)
        continue
      }
      const name = dir[dir.length - 1]!
      try {
        const folder = await withRetry(
          () => createFolder({ parent_id: parentId, folder_name: name, if_exists: 'return' }, true),
          signal,
          () => {},
        )
        folderIds.set(key, folder.id)
        created.add(key)
        job.folders = created.size
      } catch (e) {
        folderIds.set(key, null)
        if (isApiError(e, 'aborted')) return
        const message = folderErrorMessage(e, name)
        job.folderErrors.push({ path: dir.join('/'), message })
        for (const it of items) {
          if (it.state === 'waiting' && dirKey(it.dir.slice(0, dir.length)) === key) {
            update(job, it, {
              state: 'failed',
              errorCode: isApiError(e) ? e.code : 'internal',
              error: `建子文件夹「${dir.join('/')}」失败：${message}`,
            })
          }
        }
        if (isApiError(e) && (e.status === 401 || e.status === 403)) {
          stop(e.status === 401 ? '登录已失效，请重新登录后点"重试失败的"' : '没有权限上传到这个项目')
          return
        }
      }
    }
  }

  /** 要建的各层子文件夹（父在前）。 */
  function planDirs(items: UploadItem[]): string[][] {
    const seen = new Map<string, string[]>()
    for (const it of items) {
      for (let i = 1; i <= it.dir.length; i++) {
        const sub = it.dir.slice(0, i)
        const k = dirKey(sub)
        if (!seen.has(k)) seen.set(k, sub)
      }
    }
    return [...seen.values()].sort((a, b) => a.length - b.length)
  }

  function folderErrorMessage(e: unknown, name: string): string {
    if (!(e instanceof ApiError)) return errorMessage(e)
    if (e.code === 'tree_full') return '这一层的子文件夹编号已用完（最多 99999 个）'
    if (e.code === 'not_found') return '上一级文件夹已不存在'
    if (e.code === 'invalid_input') return `名称「${name}」不合规：${e.message}`
    return e.message
  }

  /**
   * 传一个文件（临时失败原位重试，`invalid_cover` 去掉封面重传一次）。
   * 返回 ok（入库、之前已上传过、内容重复、永久失败、取消）、gone（目标文件夹不在了）、blocked（临时失败重试用完）。
   */
  async function uploadWithRetry(
    job: UploadJob,
    it: UploadItem,
    folderId: string,
    cover: Blob | null,
    signal: AbortSignal,
    stop: (why: string) => void,
  ): Promise<'ok' | 'gone' | 'blocked'> {
    let withCover = cover
    update(job, it, { state: 'uploading' })
    for (;;) {
      try {
        const res = await withRetry(
          () => uploadToFolder(folderId, { file_name: it.name, cover: withCover, file: it.file }, { handle: true, signal }),
          signal,
          (n, seconds, e) =>
            update(job, it, {
              state: 'retrying',
              retry: `${uploadErrorMessage(e)}，${seconds} 秒后第 ${n} 次重试（最多 ${RETRY_DELAYS_MS.length} 次）`,
            }),
        )
        const data = res.data
        update(job, it, {
          state: res.status === 200 ? 'already' : 'done',
          result: data,
          retry: null,
          cover: data.has_cover ? 'included' : it.cover === 'pending' ? (withCover ? 'included' : 'none') : it.cover,
        })
        return 'ok'
      } catch (e) {
        if (isApiError(e, 'invalid_cover') && withCover) {
          update(job, it, { state: 'uploading', cover: 'rejected', coverReason: `服务器没收封面：${e.message}`, retry: null })
          withCover = null
          continue
        }
        if (isApiError(e, 'aborted')) {
          update(job, it, { state: 'cancelled', retry: null })
          return 'ok'
        }
        if (isApiError(e, 'duplicate_content')) {
          update(job, it, { state: 'duplicate', duplicate: (e.details ?? null) as DuplicateContentDetails | null, retry: null })
          return 'ok'
        }
        const transient = isTransient(e)
        update(job, it, {
          state: 'failed',
          errorCode: isApiError(e) ? e.code : 'internal',
          error: transient ? `${uploadErrorMessage(e)}（已重试 ${RETRY_DELAYS_MS.length} 次）` : uploadErrorMessage(e),
          retry: null,
        })
        if (isApiError(e) && e.status === 401) stop('登录已失效，请重新登录后点"重试失败的"')
        else if (isApiError(e) && e.status === 403) stop('没有权限上传到这个项目')
        if (isApiError(e, 'not_found')) return 'gone'
        return transient ? 'blocked' : 'ok'
      }
    }
  }

  return { jobs, uploadFiles, uploadFolder, retryFailed, cancel, clearFinished, dismiss, reset }
})
