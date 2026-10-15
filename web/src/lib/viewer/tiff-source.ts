// 浏览器里看 TIFF 原图（M2-4）：页面这一侧管 Worker（lib/viewer/tiff.worker.ts）。
//
//   const t = new TiffWorker(url, fileSize, { onProgress, onDecoding, onNeed })
//   const meta = await t.ready              // 解码完成：{width, height, maxLevel, channels, bytes, ms}
//   const tile = await t.tile(level, x, y)  // ImageBitmap 或 2D 画布
//   t.dispose()                             // 换图、关掉时：terminate（下载请求和解码一起停掉）
//
// - 每张图一个 Worker，换图时上一张的 Worker 直接 terminate，不排队。
// - Worker 读了文件头后报出要占的内存（峰值、常驻），`onNeed` 决定放不放（页面共用的预算，见 budget.ts；
//   可以等别的查看器解完再放行）；不给 onNeed 时一律放行（单张的预算 Worker 自己查过）。
// - 解码（含金字塔）从放行起最多 DECODE_TIMEOUT_MS，超时 terminate，返回 `timeout`（下载另有 30 秒空闲超时，在 Worker 里）。
//   超时按"解不了"处理（permanent，和壳切块超时的 422 一致：本次会话内不再试，M2-4 审查第 1 轮）。
// - 失败时 `ready` reject 一个 ViewerLoadError（code 见 tiff.worker.ts）。

import { ViewerLoadError } from './errors'

/** 下载完之后，解码和建金字塔最多等多久。 */
export const DECODE_TIMEOUT_MS = 60_000

export interface TiffReady {
  width: number
  height: number
  maxLevel: number
  channels: 1 | 4
  /** 金字塔各级像素的总字节数（Worker 里常驻的内存）。 */
  bytes: number
  ms: { download: number; decode: number; pyramid: number }
}

type OutMessage =
  | { type: 'progress'; loaded: number; total: number }
  | { type: 'decoding' }
  | { type: 'need'; peak: number; resident: number }
  | ({ type: 'ready' } & TiffReady)
  | { type: 'failed'; code: string; message: string; status?: number; permanent?: boolean }
  | { type: 'tile'; id: number; bitmap?: ImageBitmap; rgba?: Uint8ClampedArray<ArrayBuffer>; width?: number; height?: number }
  | { type: 'tile-failed'; id: number; message: string }

export type TileImage = ImageBitmap | CanvasRenderingContext2D

export interface TiffHooks {
  onProgress?: (loaded: number, total: number) => void
  /** 下载完了，开始解码。 */
  onDecoding?: () => void
  /** 读了文件头，要占这么多内存（字节）：放行返回 null，不放行返回原因（Worker 随即按 over_budget 失败）。可以先等。 */
  onNeed?: (need: { peak: number; resident: number }) => string | null | Promise<string | null>
}

interface Pending {
  resolve: (t: TileImage) => void
  reject: (e: Error) => void
}

/** 已经建过、还没 terminate 的 Worker 个数（截图脚本核对"换图时上一张的 Worker 被取消"）。 */
let liveWorkers = 0
export function liveTiffWorkers(): number {
  return liveWorkers
}

export class TiffWorker {
  readonly ready: Promise<TiffReady>
  private worker: Worker | null
  private pending = new Map<number, Pending>()
  private nextId = 1
  private timer: ReturnType<typeof setTimeout> | undefined
  private settle!: { resolve: (r: TiffReady) => void; reject: (e: ViewerLoadError) => void }
  private done = false
  private readonly hooks: TiffHooks

  constructor(url: string, fileSize: number, hooks: TiffHooks = {}) {
    this.hooks = hooks
    this.ready = new Promise((resolve, reject) => {
      this.settle = { resolve, reject }
    })
    // 调用方总会处理 ready；这里先接住，免得 dispose 之后的 reject 变成未处理的 Promise。
    this.ready.catch(() => {})
    let w: Worker
    try {
      w = new Worker(new URL('./tiff.worker.ts', import.meta.url), { type: 'module' })
    } catch (e) {
      this.worker = null
      this.fail(new ViewerLoadError('internal', `浏览器建不了后台线程：${e instanceof Error ? e.message : String(e)}`))
      return
    }
    this.worker = w
    liveWorkers++
    w.onmessage = (e: MessageEvent<OutMessage>) => this.onMessage(e.data)
    w.onerror = (e) => {
      e.preventDefault()
      this.fail(new ViewerLoadError('internal', `解码原图的后台线程出错${e.message ? `：${e.message}` : ''}`))
      this.dispose()
    }
    w.postMessage({ type: 'open', url, fileSize })
  }

  private onMessage(msg: OutMessage) {
    switch (msg.type) {
      case 'progress':
        this.hooks.onProgress?.(msg.loaded, msg.total)
        break
      case 'decoding':
        this.hooks.onDecoding?.()
        break
      case 'need': {
        const answer = async () => {
          try {
            return (await this.hooks.onNeed?.({ peak: msg.peak, resident: msg.resident })) ?? null
          } catch (e) {
            return e instanceof Error ? e.message : String(e)
          }
        }
        void answer().then((why) => {
          if (!this.worker || this.done) return
          if (why) {
            this.worker.postMessage({ type: 'budget', ok: false, message: why })
            return
          }
          this.worker.postMessage({ type: 'budget', ok: true })
          // 从放行起算解码超时（等预算的时间不算）。
          this.timer = setTimeout(() => {
            this.fail(
              new ViewerLoadError('timeout', `原图太复杂，浏览器里解码超过 ${DECODE_TIMEOUT_MS / 1000} 秒，已放弃`, undefined, false, true),
            )
            this.dispose()
          }, DECODE_TIMEOUT_MS)
        })
        break
      }
      case 'ready': {
        clearTimeout(this.timer)
        const { type: _type, ...meta } = msg
        if (!this.done) {
          this.done = true
          this.settle.resolve(meta)
        }
        break
      }
      case 'failed':
        clearTimeout(this.timer)
        this.fail(new ViewerLoadError(msg.code, msg.message, msg.status, false, msg.permanent === true))
        this.dispose()
        break
      case 'tile': {
        const p = this.pending.get(msg.id)
        if (!p) {
          msg.bitmap?.close()
          return
        }
        this.pending.delete(msg.id)
        if (msg.bitmap) {
          p.resolve(msg.bitmap)
        } else if (msg.rgba && msg.width && msg.height) {
          const canvas = document.createElement('canvas')
          canvas.width = msg.width
          canvas.height = msg.height
          const ctx = canvas.getContext('2d')
          if (!ctx) {
            p.reject(new Error('建不了 2D 画布'))
            return
          }
          ctx.putImageData(new ImageData(msg.rgba, msg.width, msg.height), 0, 0)
          p.resolve(ctx)
        } else {
          p.reject(new Error('后台线程返回的块是空的'))
        }
        break
      }
      case 'tile-failed': {
        const p = this.pending.get(msg.id)
        this.pending.delete(msg.id)
        p?.reject(new Error(msg.message))
        break
      }
    }
  }

  private fail(e: ViewerLoadError) {
    if (this.done) return
    this.done = true
    this.settle.reject(e)
  }

  /** 要一块（ready 之后）。dispose 之后 reject。 */
  tile(level: number, x: number, y: number): Promise<TileImage> {
    if (!this.worker) return Promise.reject(new Error('已关闭'))
    const id = this.nextId++
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
      this.worker!.postMessage({ type: 'tile', id, level, x, y })
    })
  }

  /** 不再要这一张：terminate Worker（下载请求、解码一起停掉），没回来的块都 reject。 */
  dispose() {
    clearTimeout(this.timer)
    this.fail(new ViewerLoadError('aborted', '已取消'))
    if (this.worker) {
      this.worker.terminate()
      this.worker = null
      liveWorkers--
    }
    for (const p of this.pending.values()) p.reject(new Error('已取消'))
    this.pending.clear()
  }
}
