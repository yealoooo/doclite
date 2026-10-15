// 浏览器里看 TIFF 原图的 Web Worker（M2-4）：下载 → 解码 → 金字塔 → 按块交给页面的 OpenSeadragon。
// 每张图一个 Worker；页面换图、关掉查看器时直接 terminate（下载请求、解码一起停掉）。
//
// 消息（页面 → Worker）：
//   {type: 'open', url, fileSize}            开始下载并解码（只发一次）
//   {type: 'budget', ok, message?}            回答 need：ok 才开始解码（页面上的查看器共用一份内存预算，见 budget.ts）
//   {type: 'tile', id, level, x, y}           要一块（ready 之后）
// 消息（Worker → 页面）：
//   {type: 'progress', loaded, total}         下载进度（最多每 100 毫秒一次）
//   {type: 'decoding'}                        下载完了，开始解码（页面从这时起算解码超时）
//   {type: 'need', peak, resident}            读了文件头，按实际的格式算出的内存：峰值、解完常驻的（字节），等页面回答 budget
//   {type: 'ready', width, height, maxLevel, channels, bytes, ms: {download, decode, pyramid}}
//   {type: 'failed', code, message, status?, permanent}
//       code：too_large、over_budget、unsupported、orientation、corrupt、http、network、internal；
//       permanent：文件本身的问题（读了文件头或解码之后才知道的 too_large、unsupported、orientation、corrupt），
//       页面本次会话内记住这个快照，不再下载（下载失败、预算不够、分配不出内存的不算）
//   {type: 'tile', id, bitmap} 或 {type: 'tile', id, rgba, width, height}（没有 createImageBitmap 时）
//   {type: 'tile-failed', id, message}
//
// 解码照搬封面的做法（lib/cover-core.ts）：自己读第一个 IFD 里白名单内的标签、查支持范围和内存预算
// （MAX_TIFF_PIXELS、MAX_TIFF_DECODE_BYTES，和封面同一套），再交给带补丁的 UTIF（`@/lib/utif`）解像素。
// 内存按"解码（文件 + 原始数据 + RGBA……，见 tiffDecodeBytes）+ 灰度转换时多出的一份灰度 + 金字塔多出的 1/3"算全
// （M2-4 审查第 1 轮）。超出预算返回 too_large，页面只显示封面，提示下载原图或改用客户端。只用不要求安全上下文的 API。

import {
  CoverError,
  decodeTiffRgba,
  MAX_TIFF_DECODE_BYTES,
  MAX_TIFF_PIXELS,
  megapixels,
  readTiffInfo,
  tiffDecodeBytes,
  tiffUnsupported,
} from '../cover-core'
import {
  buildPyramid,
  compositeOnWhite,
  extractTile,
  type PixelLevel,
  pyramidBytes,
  pyramidExtraBytes,
  rgbaToGray,
} from './pyramid'

/** 下载时多久收不到数据就算中断。 */
const IDLE_TIMEOUT_MS = 30_000
const PROGRESS_INTERVAL_MS = 100

interface WorkerScope {
  onmessage: ((e: MessageEvent<InMessage>) => void) | null
  postMessage(message: unknown, transfer?: Transferable[]): void
}

type InMessage =
  | { type: 'open'; url: string; fileSize: number }
  | { type: 'budget'; ok: boolean; message?: string }
  | { type: 'tile'; id: number; level: number; x: number; y: number }

const scope = self as unknown as WorkerScope

let levels: PixelLevel[] | null = null
let opened = false
/** 等页面回答 need（预算够不够）。 */
let budgetAnswer: ((answer: { ok: boolean; message?: string }) => void) | null = null

class ViewError extends Error {
  readonly code: string
  readonly status?: number
  /** 文件本身的问题（换个时间再试也一样），见文件开头的 failed。 */
  readonly permanent: boolean
  constructor(code: string, message: string, status?: number, permanent = false) {
    super(message)
    this.code = code
    this.status = status
    this.permanent = permanent
  }
}

async function download(url: string, fileSize: number): Promise<ArrayBuffer> {
  const ac = new AbortController()
  let idle: ReturnType<typeof setTimeout> | undefined
  let timedOut = false
  const arm = () => {
    clearTimeout(idle)
    idle = setTimeout(() => {
      timedOut = true
      ac.abort()
    }, IDLE_TIMEOUT_MS)
  }
  arm()
  try {
    let res: Response
    try {
      res = await fetch(url, { signal: ac.signal, credentials: 'omit', mode: 'cors' })
    } catch (e) {
      throw new ViewError(
        'network',
        timedOut
          ? `下载原图超过 ${IDLE_TIMEOUT_MS / 1000} 秒没有响应`
          : `下载原图失败（连不上存储，或存储没有放行跨域访问）：${e instanceof Error ? e.message : String(e)}`,
      )
    }
    if (!res.ok || !res.body) {
      throw new ViewError(
        'http',
        res.status === 403 ? '存储拒绝访问（原图地址可能已过期），请重试' : `存储返回 ${res.status}`,
        res.status,
      )
    }
    const total = fileSize
    const out = new Uint8Array(total)
    let loaded = 0
    let lastPost = 0
    const reader = res.body.getReader()
    try {
      for (;;) {
        arm()
        const { done, value } = await reader.read()
        if (done) break
        if (loaded + value.length > total) throw new ViewError('corrupt', '下载到的原图比记录的大小大，可能不是这个版本的文件')
        out.set(value, loaded)
        loaded += value.length
        const now = performance.now()
        if (now - lastPost >= PROGRESS_INTERVAL_MS) {
          lastPost = now
          scope.postMessage({ type: 'progress', loaded, total })
        }
      }
    } catch (e) {
      if (e instanceof ViewError) throw e
      throw new ViewError(
        'network',
        timedOut ? `下载原图超过 ${IDLE_TIMEOUT_MS / 1000} 秒没有收到数据` : `下载原图中断：${e instanceof Error ? e.message : String(e)}`,
      )
    }
    if (loaded !== total) throw new ViewError('corrupt', `原图只下载到 ${loaded} 字节，记录的是 ${total} 字节`)
    scope.postMessage({ type: 'progress', loaded, total })
    return out.buffer
  } finally {
    clearTimeout(idle)
  }
}

async function open(url: string, fileSize: number) {
  const t0 = performance.now()
  let buf: ArrayBuffer | null = await download(url, fileSize)
  const t1 = performance.now()
  scope.postMessage({ type: 'decoding' })

  const info = await readTiffInfo(buf)
  if (info.orientation !== 1) {
    throw new ViewError('orientation', `TIFF 的方向标记是 ${info.orientation}（不是 1），浏览器里不转正`, undefined, true)
  }
  const why = tiffUnsupported(info)
  if (why) throw new ViewError('unsupported', why, undefined, true)
  const pixels = info.width * info.height
  // 灰度和黑白只留一个通道（每像素 1 字节），其余 RGBA（alpha 合成到白底）。
  const gray = (info.photometric === 0 || info.photometric === 1) && info.samplesPerPixel === 1
  const baseBytes = gray ? pixels : pixels * 4
  const extra = pyramidExtraBytes(baseBytes)
  // 峰值：解码（文件 + 原始数据 + RGBA……）+ 灰度转换时 RGBA 和灰度同时在的那一份 + 金字塔多出的部分（不指望中途能回收）。
  const peak = tiffDecodeBytes(info, buf.byteLength).total + (gray ? pixels : 0) + extra
  if (pixels > MAX_TIFF_PIXELS || peak > MAX_TIFF_DECODE_BYTES) {
    throw new ViewError(
      'too_large',
      `原图太大（${megapixels(pixels)}，解码约要 ${Math.round(peak / 1048576)} MB），超出浏览器里的预算`,
      undefined,
      true,
    )
  }
  // 页面上的查看器共用一份预算（对比时两侧共用）：问页面这一张的峰值放不放得下。
  const answer = await new Promise<{ ok: boolean; message?: string }>((resolve) => {
    budgetAnswer = resolve
    scope.postMessage({ type: 'need', peak, resident: baseBytes + extra })
  })
  if (!answer.ok) throw new ViewError('over_budget', answer.message ?? '浏览器里的内存预算不够')

  let rgba: Uint8Array | null = await decodeTiffRgba(buf, info)
  buf = null
  let base: PixelLevel
  if (gray) {
    base = { width: info.width, height: info.height, channels: 1, data: rgbaToGray(rgba, pixels) }
    rgba = null
  } else {
    compositeOnWhite(rgba, info.extraSamples === 1)
    base = { width: info.width, height: info.height, channels: 4, data: rgba }
  }
  const t2 = performance.now()
  levels = buildPyramid(base)
  const t3 = performance.now()
  scope.postMessage({
    type: 'ready',
    width: info.width,
    height: info.height,
    maxLevel: levels.length - 1,
    channels: base.channels,
    bytes: pyramidBytes(levels),
    ms: { download: Math.round(t1 - t0), decode: Math.round(t2 - t1), pyramid: Math.round(t3 - t2) },
  })
}

async function tile(id: number, level: number, x: number, y: number) {
  const src = levels?.[level]
  const t = src ? extractTile(src, x, y) : null
  if (!t) {
    scope.postMessage({ type: 'tile-failed', id, message: `没有这一块：${level}/${x}_${y}` })
    return
  }
  const image = new ImageData(t.rgba, t.width, t.height)
  if (typeof createImageBitmap === 'function') {
    const bitmap = await createImageBitmap(image)
    scope.postMessage({ type: 'tile', id, bitmap }, [bitmap])
  } else {
    scope.postMessage({ type: 'tile', id, rgba: t.rgba, width: t.width, height: t.height }, [t.rgba.buffer])
  }
}

scope.onmessage = (e) => {
  const msg = e.data
  if (msg.type === 'open') {
    if (opened) return
    opened = true
    open(msg.url, msg.fileSize).catch((err: unknown) => {
      if (err instanceof ViewError) {
        scope.postMessage({ type: 'failed', code: err.code, message: err.message, status: err.status, permanent: err.permanent })
      } else if (err instanceof CoverError) {
        // 读文件头、解码时发现的：文件本身的问题。
        scope.postMessage({ type: 'failed', code: err.code, message: err.message, permanent: true })
      } else if (err instanceof RangeError) {
        // 分配不出这么大的数组（内存不够；可能只是当时别的页面占着，不记住）。
        scope.postMessage({ type: 'failed', code: 'too_large', message: `浏览器分配不出解码用的内存：${err.message}`, permanent: false })
      } else {
        scope.postMessage({ type: 'failed', code: 'internal', message: err instanceof Error ? err.message : String(err), permanent: false })
      }
    })
  } else if (msg.type === 'budget') {
    const answer = budgetAnswer
    budgetAnswer = null
    answer?.({ ok: msg.ok, message: msg.message })
  } else if (msg.type === 'tile') {
    tile(msg.id, msg.level, msg.x, msg.y).catch((err: unknown) =>
      scope.postMessage({ type: 'tile-failed', id: msg.id, message: err instanceof Error ? err.message : String(err) }),
    )
  }
}
