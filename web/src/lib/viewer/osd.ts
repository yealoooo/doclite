// OpenSeadragon（BSD-3-Clause）的封装（M2-4）：建查看器的默认设置，以及几种图源（TileSource）。
//
// - 画布：一律用 canvas 绘制器（不用 WebGL）：封面经壳的 img 协议、S3 的预签名 URL 加载时没有 CORS 头，
//   WebGL 不能用这种"被污染"的图；canvas 画没有这个限制。对比视图同时两个查看器，也不占 WebGL 上下文。
// - 键盘：关掉 OpenSeadragon 自带的按键（它的 r 会旋转、f 会翻转），由使用查看器的页面绑定（M2-6）。
// - 鼠标：滚轮缩放、拖动平移、双击放大；单击不缩放。
//
// 图源（坐标都按原图转正后的宽高）：
// - CoverTileSource：封面（长边 400）当作原图大小的一块，原图就绪前先显示，缩放和位置与原图一致；
// - ShellTileSource：壳的分块（M2-3，DZI），按块用 fetch 取，换图时一起取消。

import OpenSeadragon from 'openseadragon'

import type { TileSourceDescriptor } from '../image-url'

/** 壳的分块的块大小（DZI 描述里没写时用）。 */
const TILE_SIZE = 512

/** DZI 的最高一级：长边 ≤ 2^level 的最小 level。 */
function dziMaxLevel(width: number, height: number): number {
  const m = Math.max(width, height)
  let level = 0
  while (2 ** level < m) level++
  return level
}

export { OpenSeadragon }
export type OsdViewer = OpenSeadragon.Viewer
export type OsdTiledImage = OpenSeadragon.TiledImage

/** 100% 之后最多再放大几倍（4 = 400%）。 */
export const MAX_ZOOM_PIXEL_RATIO = 4

/** 建一个查看器（只有画布，没有自带的按钮）。 */
export function createOsdViewer(element: HTMLElement): OsdViewer {
  return OpenSeadragon({
    element,
    drawer: 'canvas',
    showNavigationControl: false,
    showSequenceControl: false,
    showNavigator: false,
    keyboardNavEnabled: false,
    gestureSettingsMouse: {
      dragToPan: true,
      scrollToZoom: true,
      clickToZoom: false,
      dblClickToZoom: true,
      pinchToZoom: false,
      flickEnabled: false,
    },
    animationTime: 0.4,
    blendTime: 0,
    minZoomImageRatio: 0.5,
    maxZoomPixelRatio: MAX_ZOOM_PIXEL_RATIO,
    visibilityRatio: 0.2,
    zoomPerScroll: 1.2,
    zoomPerClick: 2,
    // 每个查看器同时最多取几块（壳的 img 协议在本机）。
    imageLoaderLimit: 6,
    // 每块 512×512 约 1 MB；对比视图有两个查看器。
    maxImageCacheCount: 150,
    timeout: 60_000,
    crossOriginPolicy: false,
    preserveImageSizeOnResize: true,
    // 没有自带的按钮，不需要它的按钮图片。
    prefixUrl: '',
  } as OpenSeadragon.Options)
}

/** addTiledImage 的参数（OpenSeadragon 自带的类型把 success、error 写成了 DOM 的 Event，这里按实际的写）。 */
export interface AddImageOptions {
  tileSource: unknown
  /** 放在第几层（0 是最底下）；不写放在最上面。 */
  index?: number
  x?: number
  y?: number
  width?: number
  success?: (e: { item: OsdTiledImage }) => void
  error?: (e: { message?: string }) => void
}

export function addImage(viewer: OsdViewer, options: AddImageOptions): void {
  viewer.addTiledImage(options as unknown as OpenSeadragon.TileSourceSpecifier)
}

// ---------- 封面 ----------

/**
 * 封面当作原图大小的一块：只有一级（原图那一级）、一块，图片按这块的范围拉伸（封面和原图的长宽比一致）。
 * 这样封面和原图在同一个坐标系里，原图就绪后直接盖在上面，缩放、位置、100% 都不用变。
 * 不带 CORS 加载（壳的 img 协议的封面响应没有 CORS 头；canvas 绘制器不需要）。
 */
export class CoverTileSource extends OpenSeadragon.TileSource {
  private readonly coverUrl: string

  constructor(url: string, width: number, height: number) {
    const level = dziMaxLevel(width, height)
    super({ width, height, tileSize: Math.max(width, height), tileOverlap: 0, minLevel: level, maxLevel: level })
    this.coverUrl = url
  }

  override supports(): boolean {
    return false
  }

  override getTileUrl(): string {
    return this.coverUrl
  }

  override hasTransparency(): boolean {
    return false
  }

  override downloadTileStart(job: OpenSeadragon.ImageJob): void {
    const img = new Image()
    job.userData.img = img
    img.decoding = 'async'
    img.onload = () => {
      img.onload = img.onerror = null
      job.finish(img, null, 'image')
    }
    img.onerror = () => {
      img.onload = img.onerror = null
      job.fail('封面加载失败', null)
    }
    img.src = job.src
  }

  override downloadTileAbort(job: OpenSeadragon.ImageJob): void {
    const img = job.userData.img as HTMLImageElement | undefined
    if (img) {
      img.onload = img.onerror = null
      img.src = ''
    }
  }
}

// ---------- 壳的分块 ----------

/** 按块取的回调（截图脚本核对"只取看得到的块"时用）。 */
export type TileRequestHook = (level: number, x: number, y: number) => void

/** 一块最多试几次（第一次 + 重试），每次重试前等多久（毫秒）。 */
const TILE_ATTEMPTS = 3
const TILE_RETRY_DELAYS_MS = [500, 1500]
/** 一次取块最多等多久（毫秒）：壳在重新切块（分块被淘汰后）时块请求会等着切完。三次加上间隔要在 OpenSeadragon 的
 * timeout（60 秒）之内，否则它先判失败。 */
const TILE_ATTEMPT_TIMEOUT_MS = 18_000

/** 取一块失败（status 为 0 是没拿到响应：网络、CORS、超时）。 */
class TileFetchError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
  /** 值得重试：没拿到响应、5xx（503 缓存暂时不可用、502 下载原图失败、500 本机读写出错）。404、422 不重试。 */
  get retryable(): boolean {
    return this.status === 0 || this.status >= 500
  }
}

/** 等 ms 毫秒；signal 取消时提前结束（返回 false）。 */
function wait(ms: number, signal: AbortSignal): Promise<boolean> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve(false)
      return
    }
    const onAbort = () => {
      clearTimeout(timer)
      resolve(false)
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort)
      resolve(true)
    }, ms)
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

/**
 * 壳的分块（M2-3）：描述是 DZI 的 JSON 写法，块地址 `<Image.Url><级>/<x>_<y>.<jpg|png>`。块用 fetch 取（壳的
 * 取图地址和页面同源，CSP 的 connect-src 'self' 就放行），
 * `signal` 取消时（换图、关掉）正在取的块一起取消；单个块被 OpenSeadragon 放弃时也取消它。
 *
 * 失败的块 OpenSeadragon 当作"不存在"，这张图的这一层再也不取（留下空白）。所以没拿到响应、5xx 时在这里先重试
 * （共 TILE_ATTEMPTS 次），还不行才报失败；查看器数失败的块，提示"重试"（重新加载整张，M2-4 审查第 1 轮）。
 */
export class ShellTileSource extends OpenSeadragon.TileSource {
  private readonly prefix: string
  private readonly format: string
  private readonly signal: AbortSignal
  private readonly onTile?: TileRequestHook

  constructor(d: TileSourceDescriptor, signal: AbortSignal, onTile?: TileRequestHook) {
    super({
      width: d.width,
      height: d.height,
      tileSize: Number(d.Image.TileSize) || TILE_SIZE,
      tileOverlap: Number(d.Image.Overlap) || 0,
      minLevel: 0,
      maxLevel: d.max_level,
    })
    this.prefix = d.Image.Url
    this.format = d.Image.Format
    this.signal = signal
    this.onTile = onTile
  }

  override supports(): boolean {
    return false
  }

  override getTileUrl(level: number, x: number, y: number): string {
    return `${this.prefix}${level}/${x}_${y}.${this.format}`
  }

  override getTilePostData(level: number, x: number, y: number) {
    return { level, x, y }
  }

  override hasTransparency(): boolean {
    return false
  }

  override downloadTileStart(job: OpenSeadragon.ImageJob): void {
    const ac = new AbortController()
    const onAbort = () => ac.abort()
    if (this.signal.aborted) ac.abort()
    else this.signal.addEventListener('abort', onAbort, { once: true })
    job.userData.ac = ac
    const pos = job.postData as { level: number; x: number; y: number }
    this.onTile?.(pos.level, pos.x, pos.y)
    this.fetchTile(job.src, ac.signal)
      .then((blob) => job.finish(blob, null, 'rasterBlob'))
      .catch((e: unknown) => job.fail(ac.signal.aborted ? '已取消' : e instanceof Error ? e.message : String(e), null))
      .finally(() => this.signal.removeEventListener('abort', onAbort))
  }

  /** 取一块，没拿到响应、5xx 时重试（见类的说明）。`signal` 取消时 reject。 */
  private async fetchTile(src: string, signal: AbortSignal): Promise<Blob> {
    for (let attempt = 1; ; attempt++) {
      try {
        return await this.fetchOnce(src, signal)
      } catch (e) {
        if (signal.aborted) throw e
        const retry = e instanceof TileFetchError && e.retryable && attempt < TILE_ATTEMPTS
        if (!retry) throw e
        if (!(await wait(TILE_RETRY_DELAYS_MS[attempt - 1] ?? 1500, signal))) throw e
      }
    }
  }

  private async fetchOnce(src: string, signal: AbortSignal): Promise<Blob> {
    const ac = new AbortController()
    const onAbort = () => ac.abort()
    signal.addEventListener('abort', onAbort, { once: true })
    let timedOut = false
    const timer = setTimeout(() => {
      timedOut = true
      ac.abort()
    }, TILE_ATTEMPT_TIMEOUT_MS)
    try {
      let res: Response
      try {
        res = await fetch(src, { signal: ac.signal })
      } catch (e) {
        if (signal.aborted) throw e
        // 地址本身不对时（级号、扩展名不对）壳回的是没有 CORS 头的纯文本，fetch 也会走到这里。
        throw new TileFetchError(0, timedOut ? `取块超过 ${TILE_ATTEMPT_TIMEOUT_MS / 1000} 秒` : `取块失败：${e instanceof Error ? e.message : String(e)}`)
      }
      if (!res.ok) {
        // 壳的块响应和页面同源，错误码（X-Doclite-Error）读得到；没有时只写状态码。
        const code = res.headers.get('X-Doclite-Error')
        throw new TileFetchError(res.status, `取块失败：HTTP ${res.status}${code ? ` ${code}` : ''}`)
      }
      try {
        return await res.blob()
      } catch (e) {
        if (signal.aborted) throw e
        throw new TileFetchError(0, timedOut ? `取块超过 ${TILE_ATTEMPT_TIMEOUT_MS / 1000} 秒` : `取块中途失败：${e instanceof Error ? e.message : String(e)}`)
      }
    } finally {
      clearTimeout(timer)
      signal.removeEventListener('abort', onAbort)
    }
  }

  override downloadTileAbort(job: OpenSeadragon.ImageJob): void {
    ;(job.userData.ac as AbortController | undefined)?.abort()
  }
}
