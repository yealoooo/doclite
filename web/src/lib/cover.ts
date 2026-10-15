// 浏览器上传时生成封面（M1-12，design §4.3）：输入原图文件，输出封面 JPEG（长边 400）或 null。
//
//   const blob = await generateCover(file)        // Blob（image/jpeg）或 null
//   const r = await makeCover(file)               // 带原因和耗时：{blob, reason, message, format, width, height, ms…}
//
// - 具体做法见 cover-core.ts（JPEG 用 createImageBitmap 按 EXIF 转正，PNG 不转；TIFF 用 UTIF.js 解第一页）。
// - 默认在一个 Web Worker 里做（不卡页面；解码卡死时能终止）。Worker 出错（脚本加载不了、里面崩了）就丢掉它，
//   下一张重新建；连续出错 3 次才放弃 Worker，之后在页面里做。浏览器没有 Worker 或 OffscreenCanvas 时也在页面里做，
//   这时 TIFF 解码会占住主线程。
// - 一次只生成一张（排队），每张最多 60 秒：Worker 里超时就终止它（下一张重新建）；页面里超时只能不再等它
//   （同步的解码停不下来），都返回 `timeout`。
// - 生成不了（不是这三种格式、解码失败、太大、TIFF 方向不是 1、压缩方式解不了）时 blob 为 null，调用方照样上传
//   原图、不带封面。
// - 只用不要求安全上下文的 API（createImageBitmap、OffscreenCanvas、Worker、Blob），内网 http 地址也能用。

import type { CoverResult } from './cover-core'

export type { CoverFailure, CoverResult } from './cover-core'

/** 每张封面最多等多久。 */
export const COVER_TIMEOUT_MS = 60_000
/** Worker 连续出错这么多次就不再用它。 */
const MAX_WORKER_FAILURES = 3

interface Pending {
  resolve: (r: CoverResult) => void
  timer: ReturnType<typeof setTimeout>
}

let worker: Worker | null = null
/** Worker 连续出错的次数（成功一次清零）。 */
let workerFailures = 0
let nextId = 1
const pending = new Map<number, Pending>()
/** 排队：一次只做一张（解码要整张放进内存）。 */
let queue: Promise<unknown> = Promise.resolve()

function failed(reason: CoverResult['reason'], message: string, ms = 0): CoverResult {
  return {
    blob: null,
    reason,
    message,
    format: null,
    sourceWidth: null,
    sourceHeight: null,
    width: null,
    height: null,
    ms,
  }
}

/** 终止 Worker，没回来的都按 `reason` 结束。下一张会重新建。 */
function killWorker(reason: CoverResult['reason'], message: string) {
  worker?.terminate()
  worker = null
  for (const [id, p] of pending) {
    clearTimeout(p.timer)
    pending.delete(id)
    p.resolve(failed(reason, message))
  }
}

function getWorker(): Worker | null {
  if (worker) return worker
  if (workerFailures >= MAX_WORKER_FAILURES || typeof Worker === 'undefined' || typeof OffscreenCanvas === 'undefined') {
    return null
  }
  try {
    worker = new Worker(new URL('./cover.worker.ts', import.meta.url), { type: 'module' })
  } catch {
    workerFailures++
    return null
  }
  worker.onmessage = (e: MessageEvent<{ id: number; result: CoverResult }>) => {
    workerFailures = 0
    const p = pending.get(e.data.id)
    if (!p) return
    clearTimeout(p.timer)
    pending.delete(e.data.id)
    p.resolve(e.data.result)
  }
  worker.onerror = (e) => {
    // Worker 脚本加载不了，或者里面出了没接住的错：这次的按失败结束，丢掉这个 Worker（下一张重新建）。
    e.preventDefault()
    workerFailures++
    killWorker('internal', `生成封面的后台线程出错${e.message ? `：${e.message}` : ''}`)
  }
  return worker
}

function runInWorker(w: Worker, file: Blob): Promise<CoverResult> {
  return new Promise((resolve) => {
    const id = nextId++
    const started = performance.now()
    const timer = setTimeout(() => {
      pending.delete(id)
      resolve(failed('timeout', `生成封面超过 ${COVER_TIMEOUT_MS / 1000} 秒，已放弃`, Math.round(performance.now() - started)))
      // 卡住的解码只能连 Worker 一起终止。
      killWorker('timeout', '生成封面超时')
    }, COVER_TIMEOUT_MS)
    pending.set(id, { resolve, timer })
    w.postMessage({ id, file })
  })
}

async function runInPage(file: Blob): Promise<CoverResult> {
  const core = await import('./cover-core')
  const env =
    typeof OffscreenCanvas !== 'undefined'
      ? core.offscreenEnv
      : {
          create(width: number, height: number) {
            const c = document.createElement('canvas')
            c.width = width
            c.height = height
            return c
          },
          encode(c: OffscreenCanvas | HTMLCanvasElement, type: string, quality: number) {
            return new Promise<Blob | null>((resolve) => (c as HTMLCanvasElement).toBlob(resolve, type, quality))
          },
        }
  let timer: ReturnType<typeof setTimeout> | undefined
  const started = performance.now()
  const timeout = new Promise<CoverResult>((resolve) => {
    timer = setTimeout(
      () => resolve(failed('timeout', `生成封面超过 ${COVER_TIMEOUT_MS / 1000} 秒，已放弃`, Math.round(performance.now() - started))),
      COVER_TIMEOUT_MS,
    )
  })
  try {
    return await Promise.race([core.makeCoverCore(file, env), timeout])
  } finally {
    clearTimeout(timer)
  }
}

/** 生成封面，带原因和耗时。不会抛出。 */
export function makeCover(file: Blob): Promise<CoverResult> {
  const job = queue.then(async () => {
    const w = getWorker()
    if (w) return runInWorker(w, file)
    try {
      return await runInPage(file)
    } catch (e) {
      return failed('internal', e instanceof Error ? e.message : String(e))
    }
  })
  queue = job.catch(() => {})
  return job
}

/** 生成封面：JPEG Blob；生成不了时返回 null（照样上传原图，不带封面）。 */
export async function generateCover(file: File): Promise<Blob | null> {
  return (await makeCover(file)).blob
}

/** 生成不了封面的原因，给人看的短说明。 */
export function coverFailureLabel(reason: string | null | undefined): string {
  switch (reason) {
    case 'unsupported_format':
      return '不是 JPEG、PNG 或 TIFF'
    case 'unsupported':
      return '这种 TIFF 浏览器里解不了'
    case 'orientation':
      return 'TIFF 的方向标记不是 1'
    case 'too_large':
      return '图片太大'
    case 'corrupt':
      return '解码失败'
    case 'timeout':
      return '生成超时'
    case 'rejected':
      return '服务器没收封面'
    default:
      return '生成失败'
  }
}
