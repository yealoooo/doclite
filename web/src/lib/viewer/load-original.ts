// 取原图交给 OpenSeadragon（M2-4，design §4.4 的"看原图"，用户决定 Q1）：
// - 壳里：M2-3 的分块（img 协议的 dzi 描述 + 按块取），界面只取看得到的块；第一次打开要等壳下载原图、切块；
// - 浏览器里 JPEG、PNG：整图模式（下载整张 → 浏览器解码 → OpenSeadragon 的 ImageTileSource）；
// - 浏览器里 TIFF：Worker 里下载、按封面的预算解码（带补丁的 UTIF）、建金字塔，按块交给 OpenSeadragon。
// 超出预算（too_large）、解不了（unsupported 等）时抛 ViewerLoadError，查看器只显示封面并提示下载原图（浏览器里另提示
// 改用客户端）。`signal` 取消时（换图、关掉）请求、Worker 一起停掉。
//
// M2-4 审查第 1 轮补的规则：
// - 壳里换文件之后先只显示封面，停留 `settleMs`（约 250 毫秒，由查看器给）再取分块描述；这段时间里又换了就不发请求。
//   壳那边的下载原图、切块一旦开始，页面取消请求也会做完（M2-3 的设计），快速翻页时不停下来会越积越多。
// - 浏览器里页面上所有查看器共用一份内存预算（对比时两侧共用，见 budget.ts）：开始下载前按估算占下，TIFF 读了文件头后
//   按实际的重算，解完回落到常驻的大小，换图、关掉时放掉；放不下的那一张只显示封面，提示改用客户端（over_budget）。
// - 浏览器里文件本身的问题（读了文件头、解码之后才知道的 too_large、unsupported、orientation、corrupt，以及解码超时）
//   本次会话内记住这个快照，再打开时直接按同样的原因失败，不再下载（和壳的 422 一样）。
//
// 浏览器里下载原图要 S3 返回 CORS 头（SeaweedFS 默认对所有来源放行，M0-2、M2-4 实测）；只用不要求安全上下文的 API。

import type { SnapshotImageFields } from '@/api/types'

import { shellImages } from '../client'
import { loadTileSource, TileSourceError } from '../image-url'
import { acquireBudget, BudgetError, type BudgetLease, VIEWER_BUDGET_BYTES } from './budget'
import { ViewerLoadError } from './errors'
import { ImageModeTileSource, type OpenSeadragon, ShellTileSource, type TileRequestHook, WorkerTileSource } from './osd'
import { pyramidExtraBytes } from './pyramid'
import { type TiffReady, TiffWorker } from './tiff-source'

/** 查看器用到的图片信息（batch 的一项或 snapshots 的一个版本）。 */
export type ViewerImageInfo = Pick<
  SnapshotImageFields,
  | 'snapshot_id'
  | 'version_no'
  | 'width_px'
  | 'height_px'
  | 'dpi'
  | 'color_mode'
  | 'file_size'
  | 'mime_type'
  | 'cover_url'
  | 'original_url'
  | 'url_expires_at'
  | 'url_error'
>

/** 原图从哪来：tiles 壳的分块；image 浏览器整图模式；worker 浏览器里 Worker 解 TIFF。 */
export type OriginalMode = 'tiles' | 'image' | 'worker'

/**
 * 加载的步骤：tiles 等壳准备分块；wait 浏览器里等别的查看器解完（共用的内存预算）；download 下载（有进度）；
 * decode 解码；render 取块、画出来。
 */
export type LoadStep = 'tiles' | 'wait' | 'download' | 'decode' | 'render'

export interface LoadHooks {
  onStep?: (step: LoadStep, progress: number | null) => void
  /** 壳里每取一块调一次。 */
  onTile?: TileRequestHook
}

export interface LoadOptions {
  /** 壳里先停留这么久（毫秒）再取分块描述（查看器刚换了文件时给，见文件开头）；取消了就不取。 */
  settleMs?: number
}

export interface LoadedOriginal {
  mode: OriginalMode
  tileSource: OpenSeadragon.TileSource
  /** 原图（转正后）的宽高。 */
  width: number
  height: number
  /** 壳：分块描述的几项；浏览器 TIFF：Worker 的耗时和常驻内存。 */
  detail: Record<string, unknown>
  /** 不再用了（换图、关掉）：停掉 Worker、放掉解码出来的图。可以重复调。 */
  dispose(): void
}

/** 下载时多久收不到数据就算中断。 */
const IDLE_TIMEOUT_MS = 30_000

/**
 * 浏览器里 JPEG、PNG 整图模式每像素约占的内存（M1-12 实测：解码再逐级缩小，渲染进程峰值约每像素 10 字节），
 * 再加上下载下来的文件。对比时和 TIFF 一起计入共用的预算。
 */
const BITMAP_BYTES_PER_PIXEL = 10

/** 记住"解不了"的快照最多几个（超出时丢掉最早的）。 */
const MAX_REMEMBERED = 1024
/** 本次会话内解不了的快照（snapshot_id → 当时的错误），见文件开头。 */
const unopenable = new Map<string, ViewerLoadError>()

function remember(snapshotId: string, e: ViewerLoadError) {
  if (unopenable.size >= MAX_REMEMBERED) {
    const oldest = unopenable.keys().next().value
    if (oldest !== undefined) unopenable.delete(oldest)
  }
  unopenable.set(snapshotId, e)
}

/** 原图的格式（按 mime_type）。 */
export function originalFormat(mime: string): 'jpeg' | 'png' | 'tiff' | null {
  if (mime === 'image/jpeg') return 'jpeg'
  if (mime === 'image/png') return 'png'
  if (mime === 'image/tiff') return 'tiff'
  return null
}

/** cover-core（带 UTIF）按需加载：只有浏览器里看原图时才用到它的预算常数和 PNG 处理，不进页面的主包。 */
type CoverCore = typeof import('../cover-core')
let coreModule: Promise<CoverCore> | null = null
function coverCore(): Promise<CoverCore> {
  coreModule ??= import('../cover-core')
  return coreModule
}

/** 浏览器里解一张原图要占的内存（字节）：peak 下载、解码、建金字塔时的峰值；resident 解完以后常驻的。 */
export interface MemoryEstimate {
  peak: number
  resident: number
}

/**
 * 浏览器里打开这张原图大约要占的内存（按 batch 里的宽高、颜色、大小估算；不知道宽高时返回 null）。TIFF 和 Worker 的
 * 算法一致（解码 + 灰度转换时多出的一份 + 金字塔多出的 1/3），只是位深按颜色猜、不知道压缩方式（Deflate、JPEG 压缩的
 * 要多一些，Worker 读了文件头后按实际的重算）；JPEG、PNG 按每像素约 10 字节再加文件本身。
 */
export function estimateBrowserMemory(info: ViewerImageInfo): MemoryEstimate | null {
  const w = info.width_px
  const h = info.height_px
  if (!w || !h) return null
  const pixels = w * h
  const format = originalFormat(info.mime_type)
  if (format === 'jpeg' || format === 'png') {
    const bytes = pixels * BITMAP_BYTES_PER_PIXEL + info.file_size
    return { peak: bytes, resident: bytes }
  }
  if (format === 'tiff') {
    const gray = info.color_mode === 'bw' || info.color_mode === 'gray'
    const bits = info.color_mode === 'bw' ? 1 : gray ? 8 : 24
    const base = gray ? pixels : pixels * 4
    const extra = pyramidExtraBytes(base)
    // 文件 + UTIF 解出的原始数据 + RGBA + 灰度那一份 + 金字塔多出的部分。
    const peak = info.file_size + Math.ceil((w * bits) / 8) * h + pixels * 4 + (gray ? pixels : 0) + extra
    return { peak, resident: base + extra }
  }
  return null
}

/**
 * 不下载就能判断的"超出预算"（单看这一张；按 batch 里的宽高、颜色、大小估算，和封面的预算一致）。返回原因，没超时
 * 返回 null。TIFF 的准确判断在 Worker 里读了文件头之后（压缩方式、位深、分块都会影响）；和别的查看器共用的预算
 * 在 loadOriginal 里另外占。
 */
export async function browserBudgetProblem(info: ViewerImageInfo): Promise<string | null> {
  const { MAX_BITMAP_PIXELS, MAX_TIFF_DECODE_BYTES, MAX_TIFF_PIXELS, megapixels } = await coverCore()
  const w = info.width_px
  const h = info.height_px
  if (!w || !h) return null
  const pixels = w * h
  const format = originalFormat(info.mime_type)
  if (format === 'jpeg' || format === 'png') {
    return pixels > MAX_BITMAP_PIXELS ? `原图太大（${megapixels(pixels)}），超出浏览器里的预算（1 亿像素）` : null
  }
  if (format === 'tiff') {
    if (pixels > MAX_TIFF_PIXELS) return `原图太大（${megapixels(pixels)}），超出浏览器里的预算（1 亿像素）`
    const estimate = estimateBrowserMemory(info)!.peak
    if (estimate > MAX_TIFF_DECODE_BYTES) {
      return `原图太大（${megapixels(pixels)}，解码约要 ${Math.round(estimate / 1048576)} MB），超出浏览器里的预算（1 GB）`
    }
  }
  return null
}

/**
 * 取原图，返回交给 OpenSeadragon 的图源。失败抛 ViewerLoadError（signal 取消时 code 为 aborted）。
 * 浏览器里文件本身的问题记住这个快照（见文件开头），再调时直接抛同样的错误。
 */
export async function loadOriginal(
  fileId: string,
  info: ViewerImageInfo,
  signal: AbortSignal,
  hooks: LoadHooks = {},
  options: LoadOptions = {},
): Promise<LoadedOriginal> {
  if (!info.original_url) {
    throw new ViewerLoadError('no_url', info.url_error?.message ?? '取不到原图地址')
  }
  if (shellImages()) return loadShellTiles(fileId, info, signal, hooks, options.settleMs ?? 0)
  const known = unopenable.get(info.snapshot_id)
  if (known) throw known
  const format = originalFormat(info.mime_type)
  if (!format) throw new ViewerLoadError('unsupported_format', `浏览器里看不了这种格式（${info.mime_type}）`)
  const problem = await browserBudgetProblem(info)
  if (signal.aborted) throw aborted()
  if (problem) throw new ViewerLoadError('too_large', problem)
  try {
    return format === 'tiff' ? await loadBrowserTiff(fileId, info, signal, hooks) : await loadBrowserImage(info, format, signal, hooks)
  } catch (e) {
    if (e instanceof ViewerLoadError && e.permanent && !signal.aborted) remember(info.snapshot_id, e)
    throw e
  }
}

/** 在共用的预算里占下这张原图的估算值（不知道宽高时按 0，只靠 Worker 读了文件头之后的准确值）。 */
async function acquire(info: ViewerImageInfo, signal: AbortSignal, hooks: LoadHooks): Promise<BudgetLease> {
  const est = estimateBrowserMemory(info) ?? { peak: 0, resident: 0 }
  try {
    return await acquireBudget(Math.min(est.peak, VIEWER_BUDGET_BYTES), est.resident, signal, () =>
      hooks.onStep?.('wait', null),
    )
  } catch (e) {
    if (signal.aborted) throw aborted()
    if (e instanceof BudgetError) throw new ViewerLoadError('over_budget', e.message)
    throw e
  }
}

function aborted(): ViewerLoadError {
  return new ViewerLoadError('aborted', '已取消')
}

/** 等 ms 毫秒；signal 取消时 reject aborted。 */
function pause(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(aborted())
      return
    }
    const onAbort = () => {
      clearTimeout(timer)
      reject(aborted())
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

// ---------- 壳 ----------

async function loadShellTiles(fileId: string, info: ViewerImageInfo, signal: AbortSignal, hooks: LoadHooks, settleMs: number) {
  hooks.onStep?.('tiles', null)
  // 刚换了文件：先停一下（只显示封面），用户接着翻页时这一张就不发请求（见文件开头）。
  if (settleMs > 0) await pause(settleMs, signal)
  let d
  try {
    d = await loadTileSource({ file_id: fileId, snapshot_id: info.snapshot_id, cover_url: info.cover_url, original_url: info.original_url }, signal)
  } catch (e) {
    if (signal.aborted) throw aborted()
    if (e instanceof TileSourceError) {
      throw new ViewerLoadError(e.code, shellMessage(e), e.status, true)
    }
    throw new ViewerLoadError('internal', e instanceof Error ? e.message : String(e), undefined, true)
  }
  if (signal.aborted) throw aborted()
  hooks.onStep?.('render', null)
  return {
    mode: 'tiles' as const,
    tileSource: new ShellTileSource(d, signal, hooks.onTile),
    width: d.width,
    height: d.height,
    detail: {
      levels: d.levels,
      tile_count: d.tile_count,
      format: d.format,
      mode: d.mode,
      streamed: d.streamed,
      elapsed_ms: d.elapsed_ms,
      hit: d.hit,
    },
    dispose() {},
  }
}

function shellMessage(e: TileSourceError): string {
  switch (e.code) {
    case 'too_large':
      return '原图太大，客户端切不了块'
    case 'timeout':
      return '原图太复杂，客户端切块超时'
    case 'unsupported_format':
    case 'unsupported':
      return '这种格式客户端切不了块'
    case 'orientation':
      return '原图的方向标记客户端处理不了'
    case 'corrupt':
      return '原图已损坏，客户端解不了'
    case 'not_found':
      return '客户端没有这张图的下载记录（图片信息可能过期了），请重试'
    case 'unavailable':
      return '客户端的图片缓存暂时不可用，请稍后重试'
    case 'upstream':
    case 'integrity':
      return '客户端下载原图失败（存储连不上或内容不对），请稍后重试'
    default:
      return e.message || `客户端返回 ${e.status}`
  }
}

// ---------- 浏览器：JPEG、PNG ----------

async function loadBrowserImage(info: ViewerImageInfo, format: 'jpeg' | 'png', signal: AbortSignal, hooks: LoadHooks) {
  const lease = await acquire(info, signal, hooks)
  let blob: Blob
  try {
    hooks.onStep?.('download', 0)
    blob = await fetchWithProgress(info.original_url!, info.file_size, info.mime_type, signal, (loaded, total) =>
      hooks.onStep?.('download', total ? loaded / total : null),
    )
    // PNG 不按 eXIf 转正（和 server、壳、封面一致）。
    if (format === 'png') blob = await (await coverCore()).pngWithoutExif(blob)
  } catch (e) {
    lease.release()
    throw e
  }
  if (signal.aborted) {
    lease.release()
    throw aborted()
  }
  hooks.onStep?.('decode', null)
  const url = URL.createObjectURL(blob)
  const source = new ImageModeTileSource(url)
  let disposed = false
  const dispose = () => {
    if (disposed) return
    disposed = true
    source.release()
    URL.revokeObjectURL(url)
    lease.release()
  }
  signal.addEventListener('abort', dispose, { once: true })
  try {
    await source.ensureLoaded()
  } catch (e) {
    dispose()
    if (signal.aborted) throw aborted()
    throw new ViewerLoadError('corrupt', e instanceof Error ? e.message : String(e))
  }
  if (signal.aborted) throw aborted()
  hooks.onStep?.('render', null)
  const dims = source.dimensions
  return {
    mode: 'image' as const,
    tileSource: source,
    width: dims.x,
    height: dims.y,
    detail: { bytes: blob.size },
    dispose,
  }
}

/** 下载原图（带进度、可取消、30 秒收不到数据算中断）。 */
async function fetchWithProgress(
  url: string,
  expected: number,
  mime: string,
  signal: AbortSignal,
  onProgress: (loaded: number, total: number) => void,
): Promise<Blob> {
  const ac = new AbortController()
  const onAbort = () => ac.abort()
  signal.addEventListener('abort', onAbort, { once: true })
  let idle: ReturnType<typeof setTimeout> | undefined
  let timedOut = false
  const arm = () => {
    clearTimeout(idle)
    idle = setTimeout(() => {
      timedOut = true
      ac.abort()
    }, IDLE_TIMEOUT_MS)
  }
  const failNetwork = (e: unknown, what: string) => {
    if (signal.aborted) return aborted()
    if (timedOut) return new ViewerLoadError('network', `${what}超过 ${IDLE_TIMEOUT_MS / 1000} 秒没有收到数据`)
    return new ViewerLoadError('network', `${what}失败（连不上存储，或存储没有放行跨域访问）：${e instanceof Error ? e.message : String(e)}`)
  }
  try {
    arm()
    let res: Response
    try {
      res = await fetch(url, { signal: ac.signal, credentials: 'omit', mode: 'cors' })
    } catch (e) {
      throw failNetwork(e, '下载原图')
    }
    if (!res.ok || !res.body) {
      throw new ViewerLoadError(
        'http',
        res.status === 403 ? '存储拒绝访问（原图地址可能已过期），请重试' : `存储返回 ${res.status}`,
        res.status,
      )
    }
    const reader = res.body.getReader()
    const chunks: Uint8Array<ArrayBuffer>[] = []
    let loaded = 0
    try {
      for (;;) {
        arm()
        const { done, value } = await reader.read()
        if (done) break
        loaded += value.length
        if (loaded > expected) throw new ViewerLoadError('corrupt', '下载到的原图比记录的大小大，可能不是这个版本的文件')
        chunks.push(value as Uint8Array<ArrayBuffer>)
        onProgress(loaded, expected)
      }
    } catch (e) {
      if (e instanceof ViewerLoadError) throw e
      throw failNetwork(e, '下载原图中途')
    }
    if (loaded !== expected) throw new ViewerLoadError('corrupt', `原图只下载到 ${loaded} 字节，记录的是 ${expected} 字节`)
    return new Blob(chunks, { type: mime })
  } finally {
    clearTimeout(idle)
    signal.removeEventListener('abort', onAbort)
  }
}

// ---------- 浏览器：TIFF ----------

async function loadBrowserTiff(fileId: string, info: ViewerImageInfo, signal: AbortSignal, hooks: LoadHooks) {
  const lease = await acquire(info, signal, hooks)
  hooks.onStep?.('download', 0)
  const tiff = new TiffWorker(info.original_url!, info.file_size, {
    onProgress: (loaded, total) => hooks.onStep?.('download', total ? loaded / total : null),
    onDecoding: () => hooks.onStep?.('decode', null),
    // 读了文件头之后的准确值：比估算的大时再占，现在不够就等别的查看器解完（见 budget.ts），怎么都不够就拒绝。
    onNeed: async ({ peak, resident }) => {
      const ok = await lease.resize(peak, resident, () => hooks.onStep?.('wait', null))
      if (!ok) return lease.refusal(peak).message
      hooks.onStep?.('decode', null)
      return null
    },
  })
  const dispose = () => {
    tiff.dispose()
    lease.release()
  }
  signal.addEventListener('abort', dispose, { once: true })
  let meta: TiffReady
  try {
    meta = await tiff.ready
  } catch (e) {
    dispose()
    if (signal.aborted) throw aborted()
    throw e instanceof ViewerLoadError ? e : new ViewerLoadError('internal', String(e))
  }
  if (signal.aborted) throw aborted()
  // 解完了：回落到金字塔常驻的大小。
  lease.settle(meta.bytes)
  hooks.onStep?.('render', null)
  return {
    mode: 'worker' as const,
    tileSource: new WorkerTileSource(tiff, meta, `${fileId}/${info.snapshot_id}`),
    width: meta.width,
    height: meta.height,
    detail: { ...meta.ms, bytes: meta.bytes, channels: meta.channels },
    dispose,
  }
}
