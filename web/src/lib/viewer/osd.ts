// OpenSeadragon（BSD-3-Clause）的封装（M2-4）：建查看器的默认设置，以及几种图源（TileSource）。
//
// - 画布：一律用 canvas 绘制器（不用 WebGL）：封面经壳的 img 协议、S3 的预签名 URL 加载时没有 CORS 头，
//   WebGL 不能用这种"被污染"的图；canvas 画没有这个限制。对比视图同时两个查看器，也不占 WebGL 上下文。
// - 键盘：关掉 OpenSeadragon 自带的按键（它的 r 会旋转、f 会翻转），由使用查看器的页面绑定（M2-6）。
// - 鼠标：滚轮缩放、拖动平移、双击放大；单击不缩放。
//
// 图源（坐标都按原图转正后的宽高）：
// - CoverTileSource：封面（长边 400）当作原图大小的一块，原图就绪前先显示，缩放和位置与原图一致；
// - ShellTileSource：壳的分块（M2-3，DZI），按块用 fetch 取，换图时一起取消；
// - ImageModeTileSource：浏览器里 JPEG、PNG 的整图模式（OpenSeadragon 的 ImageTileSource），缩小的各级逐级减半；
// - WorkerTileSource：浏览器里 TIFF，块由 Worker 从内存里的金字塔切出来（lib/viewer/tiff.worker.ts）。

import OpenSeadragon from 'openseadragon'

import type { TileSourceDescriptor } from '../image-url'
import { dziMaxLevel, TILE_OVERLAP, TILE_SIZE } from './pyramid'
import type { TiffReady, TiffWorker } from './tiff-source'

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
    // 每个查看器同时最多取几块（壳的 img 协议、Worker 都在本机）。
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
 * img 协议给描述和块都带了 `Access-Control-Allow-Origin: *`，CSP 的 connect-src 也放行了 img:），
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
        // 壳的块响应带 `Access-Control-Expose-Headers: x-doclite-cache, x-doclite-error`（img.rs 的 cors()），
        // 错误码读得到；读不到时（别的响应）只写状态码。
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

// ---------- 浏览器：JPEG、PNG 的整图模式 ----------

/**
 * OpenSeadragon 的整图模式（ImageTileSource，每级一块）：原图整张解码，最高一级就是这张图；缩小的各级从原图
 * 逐级减半画出来（每步 2×2 平均），不像原版一次缩到位那样在适合窗口时出锯齿。`url` 是下载好的原图的 blob: 地址。
 * `loaded` 在图片解码完成（或失败）后落定。
 */
export class ImageModeTileSource extends OpenSeadragon.ImageTileSource {
  declare loaded: Promise<void>
  declare private settle: { resolve: () => void; reject: (e: Error) => void } | undefined

  constructor(url: string) {
    super({ url, buildPyramid: true, crossOriginPolicy: false })
  }

  override getImageInfo(url: string): void {
    // 父类的构造函数里用 setTimeout 调这里；loaded 在这之前已经建好（见下面的 ensureLoaded）。
    this.ensureLoaded()
    const image = new Image()
    image.decoding = 'async'
    image.onload = () => {
      image.onload = image.onerror = null
      image
        .decode()
        .catch(() => {})
        .then(() => {
          // 和父类的 getImageInfo 一样填好尺寸，再发 ready（父类的 ready 处理按这些字段初始化）。
          const self = this as unknown as Record<string, unknown>
          self.width = image.naturalWidth
          self.height = image.naturalHeight
          self.tileWidth = image.naturalWidth
          self.tileHeight = image.naturalHeight
          this.tileOverlap = 0
          this.minLevel = 0
          this.image = image
          this.levels = imageLevels(image.naturalWidth, image.naturalHeight)
          this.maxLevel = this.levels.length - 1
          this.settle!.resolve()
          this.raiseEvent('ready', { tileSource: this })
        })
    }
    image.onerror = () => {
      image.onload = image.onerror = null
      this.image = null
      this.settle!.reject(new Error('浏览器解不了这张图（文件可能已损坏）'))
      this.raiseEvent('open-failed', { message: '浏览器解不了这张图', source: url })
    }
    image.src = url
  }

  /** 第一次调用时建 loaded（构造函数里父类先跑，字段初始化在它之后，所以不能用字段初始化器）。 */
  ensureLoaded(): Promise<void> {
    if (!this.settle) {
      this.loaded = new Promise<void>((resolve, reject) => {
        this.settle = { resolve, reject }
      })
      this.loaded.catch(() => {})
    }
    return this.loaded
  }

  override downloadTileStart(job: OpenSeadragon.ImageJob): void {
    const { level } = job.postData as { level: number }
    const image = this.image
    if (!image) {
      job.fail('原图还没解码', null)
      return
    }
    if (level === this.maxLevel) {
      job.finish(image, null, 'image')
      return
    }
    const size = this.levels[level]
    if (!size) {
      job.fail(`没有第 ${level} 级`, null)
      return
    }
    try {
      job.finish(scaleImage(image, image.naturalWidth, image.naturalHeight, size.width, size.height), null, 'context2d')
    } catch (e) {
      job.fail(e instanceof Error ? e.message : String(e), null)
    }
  }

  /** 不再用了：放掉解码出来的图。 */
  release() {
    if (this.image) {
      this.image.src = ''
      this.image = null
    }
  }
}

/** 整图模式的各级尺寸（和 OpenSeadragon 的 ImageTileSource 一样：每级宽高减半向下取整，直到不到 2 像素）。 */
function imageLevels(width: number, height: number) {
  const levels = [{ width, height }]
  let w = width
  let h = height
  while (w >= 2 && h >= 2) {
    w = Math.floor(w / 2)
    h = Math.floor(h / 2)
    levels.push({ width: w, height: h })
  }
  return levels.reverse()
}

/** 逐级减半缩到目标尺寸（每步 2×2 平均），返回新画布的 2D 上下文（交给 OpenSeadragon，它负责释放）。 */
function scaleImage(src: CanvasImageSource, sw: number, sh: number, tw: number, th: number): CanvasRenderingContext2D {
  let cur: CanvasImageSource = src
  let cw = sw
  let ch = sh
  const temps: HTMLCanvasElement[] = []
  const draw = (w: number, h: number) => {
    const c = document.createElement('canvas')
    c.width = w
    c.height = h
    const ctx = c.getContext('2d')
    if (!ctx) throw new Error('浏览器建不了 2D 画布')
    ctx.imageSmoothingEnabled = true
    ctx.imageSmoothingQuality = 'high'
    ctx.drawImage(cur, 0, 0, cw, ch, 0, 0, w, h)
    return ctx
  }
  while (cw >= tw * 4 && ch >= th * 4) {
    const nw = Math.max(tw, Math.round(cw / 2))
    const nh = Math.max(th, Math.round(ch / 2))
    const ctx = draw(nw, nh)
    temps.push(ctx.canvas)
    cur = ctx.canvas
    cw = nw
    ch = nh
  }
  const out = draw(tw, th)
  for (const c of temps) {
    c.width = 0
    c.height = 0
  }
  return out
}

// ---------- 浏览器：TIFF（Worker） ----------

/** 块由 Worker 从内存里的金字塔切出来（DZI 同样的级和块）。`key` 用来区分不同的图（块的缓存键）。 */
export class WorkerTileSource extends OpenSeadragon.TileSource {
  private readonly tiff: TiffWorker
  private readonly key: string

  constructor(tiff: TiffWorker, meta: TiffReady, key: string) {
    super({
      width: meta.width,
      height: meta.height,
      tileSize: TILE_SIZE,
      tileOverlap: TILE_OVERLAP,
      minLevel: 0,
      maxLevel: meta.maxLevel,
    })
    this.tiff = tiff
    this.key = key
  }

  override supports(): boolean {
    return false
  }

  override getTileUrl(level: number, x: number, y: number): string {
    return `doclite-tiff:${this.key}/${level}/${x}_${y}`
  }

  override getTilePostData(level: number, x: number, y: number) {
    return { level, x, y }
  }

  override hasTransparency(): boolean {
    return false
  }

  override downloadTileStart(job: OpenSeadragon.ImageJob): void {
    const { level, x, y } = job.postData as { level: number; x: number; y: number }
    this.tiff
      .tile(level, x, y)
      .then((img) => job.finish(img, null, img instanceof ImageBitmap ? 'imageBitmap' : 'context2d'))
      .catch((e: unknown) => job.fail(e instanceof Error ? e.message : String(e), null))
  }

  override downloadTileAbort(): void {
    // Worker 切一块很快，不单独取消；结果回来时 OpenSeadragon 已经不要了，会忽略。
  }
}
