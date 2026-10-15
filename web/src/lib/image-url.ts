// 图片地址（design §4.4、§4.5）：页面里所有 <img> 的 src 都用 `imageSrc()` 生成。
// - 浏览器里：直接用 batch / snapshots 接口返回的预签名 URL（直连 S3）；
// - 壳里：用壳的取图地址（先查本机缓存，没有再用预签名 URL 下载）。Wails 壳的取图在 M6-3 做：在那之前
//   `shellImages()` 为 false，壳里也直接用预签名 URL（和浏览器一样）。
//
// 壳里（M6-3 起）：壳只用这个快照最近一次出现在 batch、snapshots 响应里的预签名 URL 下载（Go 代理转发时记下），
// 所以 info 必须来自这两个接口；不在记录里的图片返回 404（<img> 的 error），存储连不上返回 502。
// 缓存管理见 lib/image-cache.ts。测试图（SHELL_TEST_IMAGE_PATH）给 --smoke 的 img 一步用。
//
// 原图分块（只在壳里有，M6-3）：`tileSourceSrc()` 是分块描述的地址，OpenSeadragon 直接当 tile source 用
// （DZI 的 JSON 写法）。壳从本机缓存的原图切块（还没切就先切，第一次打开大图要等），块地址在描述的 Image.Url 里。
// 来源和封面、原图一样：info 必须来自 batch 或 snapshots 接口。用法：
//   OpenSeadragon({ tileSources: tileSourceSrc(info), crossOriginPolicy: 'Anonymous', ... })
// 或者先 `await loadTileSource(info)` 拿到描述（能区分失败原因），再把描述对象交给 tileSources。
// 失败：404 不在记录里；422 切不了（code 为 too_large、timeout、unsupported、corrupt……，页面只显示封面）；503 暂时不行（重试）；
// 502 下载原图失败。提前给下一张切块用 lib/image-cache.ts 的 `prepareTiles()`。

import { shellImages } from './client'

/**
 * 壳里取图的地址前缀（M6-3 定稿）：建议由壳的资产服务器中间件处理这个路径——和页面同源，不需要 CORS，
 * 也不受 CSP 的 connect-src 限制；图片是完整的响应，Windows 上资产服务器整块交给 WebView2 也没问题。
 */
const SHELL_IMAGE_BASE = '/shell/img/'

function shellImageUrl(path: string): string {
  return SHELL_IMAGE_BASE + path
}

export type ImageKind = 'cover' | 'original'

/** `POST /api/files/batch` 每一项（design §4.4）里和图片地址有关的字段。 */
export interface ImageInfo {
  file_id: string
  snapshot_id: string
  /** 没有封面时为 null。 */
  cover_url: string | null
  /** 存储停用或密钥解不开时为 null。 */
  original_url: string | null
}

/** 图片的 src；这张图没有对应的 URL（没有封面、存储不可用）时返回 null，由页面显示占位。 */
export function imageSrc(info: ImageInfo, kind: ImageKind): string | null {
  const url = kind === 'cover' ? info.cover_url : info.original_url
  if (!url) return null
  if (!shellImages()) return url
  return shellImageUrl(`${kind}/${info.file_id}/${info.snapshot_id}`)
}

/** 壳里打包的测试图（验证取图地址在 CSP 下能显示）。 */
export const SHELL_TEST_IMAGE_PATH = 'test/probe.png'

export function shellTestImageSrc(): string {
  return shellImageUrl(SHELL_TEST_IMAGE_PATH)
}

/** 分块描述的地址（M2-3）：壳里是取图地址下的 `dzi/<file_id>/<snapshot_id>`；浏览器里、壳还没有切块（M6-3 之前）、
 * 没有原图地址（存储不可用）时返回 null。 */
export function tileSourceSrc(info: ImageInfo): string | null {
  if (!shellImages() || !info.original_url) return null
  return shellImageUrl(`dzi/${info.file_id}/${info.snapshot_id}`)
}

/** 分块描述（壳的 img 协议 `dzi/` 返回的 JSON）：`Image` 一项是 DZI 的 JSON 写法，OpenSeadragon 直接认；
 * 其余字段方便页面用（级数、格式、是否命中……）。 */
export interface TileSourceDescriptor {
  Image: {
    xmlns: string
    /** 块地址的前缀（以 / 结尾），按 `<前缀><级>/<x>_<y>.<Format>` 取块。 */
    Url: string
    Format: 'jpg' | 'png'
    /** 和相邻块重叠的像素数（"1"）。 */
    Overlap: string
    /** 每块的边长（"512"）。 */
    TileSize: string
    Size: { Width: string; Height: string }
  }
  /** 原图转正后的宽高（最高一级的尺寸）。 */
  width: number
  height: number
  tile_size: number
  overlap: number
  /** 级数（max_level + 1），第 0 级是 1×1，最高一级是原尺寸。 */
  levels: number
  max_level: number
  /** jpg（灰度、彩色）或 png（1 位黑白）。 */
  format: 'jpg' | 'png'
  mode: 'gray' | 'rgb' | 'bilevel'
  tile_count: number
  tiles_bytes: number
  /** 是否按 TIFF 条带逐段处理（否则是整张解码）。 */
  streamed: boolean
  /** 切块用了多少毫秒（命中时是当初切块的耗时）。 */
  elapsed_ms: number
  /** 这次请求之前就已经切好了。 */
  hit: boolean
}

/** 取分块描述失败。status 为 img 协议的状态码（0 表示请求本身失败），code 为壳给的错误码。 */
export class TileSourceError extends Error {
  readonly status: number
  readonly code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }

  /** 原图切不了块（超预算、解不了、坏了）：页面只显示封面，提示下载原图。 */
  get unsupported(): boolean {
    return this.status === 422
  }
}

/** 取分块描述（壳里；浏览器里或没有原图地址时抛 TileSourceError，status 0、code `unavailable`）。
 * 第一次打开一张图时壳要先切块，可能要等几秒。 */
export async function loadTileSource(info: ImageInfo, signal?: AbortSignal): Promise<TileSourceDescriptor> {
  const src = tileSourceSrc(info)
  if (!src) throw new TileSourceError(0, 'unavailable', '只有客户端能看分块，或这张图没有原图地址')
  let res: Response
  try {
    res = await fetch(src, { signal, cache: 'no-store' })
  } catch (e) {
    if (signal?.aborted) throw e
    throw new TileSourceError(0, 'network', `取分块描述失败：${e instanceof Error ? e.message : String(e)}`)
  }
  const text = await res.text()
  let body: unknown = null
  try {
    body = JSON.parse(text)
  } catch {
    // 下面按状态码报错。
  }
  if (!res.ok) {
    const err = (body ?? {}) as { code?: string; message?: string }
    throw new TileSourceError(res.status, err.code ?? 'error', err.message ?? (text || `HTTP ${res.status}`))
  }
  return body as TileSourceDescriptor
}

/** 第 `level` 级的宽高（原尺寸除以 2^(max_level − level)，向上取整）。 */
export function tileLevelSize(d: TileSourceDescriptor, level: number): { width: number; height: number } {
  const div = 2 ** (d.max_level - level)
  return { width: Math.ceil(d.width / div), height: Math.ceil(d.height / div) }
}
