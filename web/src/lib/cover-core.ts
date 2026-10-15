// 浏览器里生成封面的核心（M1-12，design §4.3）。一般在 Web Worker 里跑（lib/cover.worker.ts）；浏览器没有 Worker 或
// OffscreenCanvas 时由 lib/cover.ts 在页面里跑。这里不碰 document：画布由调用方给的 `CanvasEnv` 创建。
//
// 规格（和壳、server 的校验一致）：JPEG，长边 min(400, 原图长边)，按转正后的方向，不带 EXIF，带 alpha 的铺白底，
// 质量 0.75；短边按 server 的公式 round(原图短边 × 封面长边 ÷ 原图长边)。
//
// - JPEG、PNG：先读文件头里的宽高（像素数超过上限就不解码），再 createImageBitmap（JPEG 按 EXIF 转正；PNG 不转，
//   和 server、壳一致）→
//   画到画布上逐级减半缩小（每步 1/2，直到不到目标的 2 倍）→ 最后一步画到铺了白底的目标尺寸画布 → 编成 JPEG。
//   一次缩到位（A4 300 DPI 要缩 8 倍多）时，平滑质量低或浏览器不做多级纹理就会出锯齿（M1-12 实测 Chrome 的
//   low 档页脚小字断开）；逐级减半每步都是 2×2 平均，不依赖浏览器的实现。
// - TIFF：自己按偏移读第一个 IFD 里白名单内的标签（不把文件交给 UTIF.decode，见下面 TIFF 一节），不支持的组合、
//   方向不是 1、内存预算超了的直接不生成；再整份读进来，自己拼 IFD 对象交给 UTIF.decodeImage 解成 RGBA（Deflate 先
//   自己解开，每块有上限），在 JS 里按整数倍做盒式平均（同时把 alpha 合成到白底），缩到目标的 1–2 倍后放到画布上，
//   最后一步缩到目标尺寸、编成 JPEG。
// - 生成不了时返回原因（代码同壳的 CoverError：unsupported_format、unsupported、orientation、too_large、corrupt、
//   internal；超时由 lib/cover.ts 判断），调用方照样上传原图、不带封面。
// - 不用要求安全上下文的 API（WebCrypto 的摘要、随机 UUID 等；内网 http 地址不是安全上下文，design §7）。

import type { IFD } from 'utif'

import UTIF, { resetUtifState } from './utif'

/** 封面的长边（design §4.3）。 */
export const COVER_LONG_EDGE = 400
/** JPEG 质量。 */
export const COVER_QUALITY = 0.75
/** server 收的封面上限（256 KiB）。 */
export const COVER_MAX_BYTES = 256 * 1024
/**
 * JPEG、PNG 的像素上限：超过就不解码、不带封面。M1-12 实测（macOS，Chrome）：createImageBitmap 整张解码再逐级缩小，
 * 渲染进程的内存峰值约每像素 10 字节，1 亿像素约 1 GB、0.3 秒；A4 300 DPI（870 万像素）约 30 毫秒。
 */
export const MAX_BITMAP_PIXELS = 100_000_000
/**
 * TIFF 的像素上限。UTIF 把整个文件读进内存、解成原始数据、再转 RGBA（每像素 4 字节），都在 JS 内存里。
 * M1-12 实测：峰值约"文件 + 原始数据 + 4 字节/像素"，8000 万像素的 RGB 无压缩 TIFF 约 750 MB、0.8 秒。
 */
export const MAX_TIFF_PIXELS = 100_000_000
/**
 * TIFF 解码的内存预算：文件本身 + 解压后的原始数据（Deflate 再加一份）+ 分块缓冲 + JPEG 压缩时解码器的约 9 字节/像素
 * + RGBA，超过就不生成（彩色大图、特制的分块尺寸先碰到它）。见 tiffDecodeBytes。
 */
export const MAX_TIFF_DECODE_BYTES = 1024 * 1024 * 1024

export type CoverFormat = 'jpeg' | 'png' | 'tiff'

/** 生成不了的原因（和壳的 `CoverError::code()` 同名）。 */
export type CoverFailure =
  | 'unsupported_format'
  | 'unsupported'
  | 'orientation'
  | 'too_large'
  | 'corrupt'
  | 'timeout'
  | 'internal'

export interface CoverResult {
  /** 封面 JPEG；生成不了时为 null。 */
  blob: Blob | null
  reason: CoverFailure | null
  /** 给人看的说明（生成不了时）。 */
  message: string | null
  /** 按文件头识别的格式；不是 JPEG、PNG、TIFF 时为 null。 */
  format: CoverFormat | null
  /** 原图转正后的宽高（解码出来的）。 */
  sourceWidth: number | null
  sourceHeight: number | null
  /** 封面的宽高。 */
  width: number | null
  height: number | null
  /** 用了多少毫秒（含读文件）。 */
  ms: number
}

export class CoverError extends Error {
  readonly code: CoverFailure
  constructor(code: CoverFailure, message: string) {
    super(message)
    this.code = code
  }
}

// ---------- 画布 ----------

export type AnyCanvas = OffscreenCanvas | HTMLCanvasElement
type Ctx2D = OffscreenCanvasRenderingContext2D | CanvasRenderingContext2D

/** 建画布、把画布编成图片（Worker 里用 OffscreenCanvas；页面里没有 OffscreenCanvas 时用 <canvas>）。 */
export interface CanvasEnv {
  create(width: number, height: number): AnyCanvas
  encode(canvas: AnyCanvas, type: string, quality: number): Promise<Blob | null>
}

export const offscreenEnv: CanvasEnv = {
  create: (w, h) => new OffscreenCanvas(w, h),
  encode: (c, type, quality) => (c as OffscreenCanvas).convertToBlob({ type, quality }),
}

function ctx2d(canvas: AnyCanvas): Ctx2D {
  const ctx = (canvas as OffscreenCanvas).getContext('2d') as Ctx2D | null
  if (!ctx) throw new CoverError('internal', '浏览器建不了 2D 画布')
  ctx.imageSmoothingEnabled = true
  ctx.imageSmoothingQuality = 'high'
  return ctx
}

/** 封面尺寸：长边 min(400, 原图长边)，短边 round(短边 × 封面长边 ÷ 长边)（和 server 的校验公式一致）。 */
export function coverSize(width: number, height: number): { width: number; height: number } {
  const long = Math.max(width, height)
  const short = Math.min(width, height)
  const coverLong = Math.min(COVER_LONG_EDGE, long)
  const coverShort = Math.max(1, Math.round((short * coverLong) / long))
  return width >= height ? { width: coverLong, height: coverShort } : { width: coverShort, height: coverLong }
}

/**
 * 逐级减半缩小：每步宽高各减半（2×2 平均），直到不到目标的 2 倍；最后一步画到铺了白底的目标尺寸画布上
 * （带 alpha 的在这一步合成到白底）。
 */
function scaleDown(src: CanvasImageSource, sw: number, sh: number, tw: number, th: number, env: CanvasEnv): AnyCanvas {
  let cur: CanvasImageSource = src
  let cw = sw
  let ch = sh
  while (cw >= tw * 2 && ch >= th * 2) {
    const nw = Math.max(tw, Math.round(cw / 2))
    const nh = Math.max(th, Math.round(ch / 2))
    const step = env.create(nw, nh)
    ctx2d(step).drawImage(cur, 0, 0, cw, ch, 0, 0, nw, nh)
    cur = step
    cw = nw
    ch = nh
  }
  const out = env.create(tw, th)
  const ctx = ctx2d(out)
  ctx.fillStyle = '#ffffff'
  ctx.fillRect(0, 0, tw, th)
  ctx.drawImage(cur, 0, 0, cw, ch, 0, 0, tw, th)
  return out
}

async function encodeJpeg(canvas: AnyCanvas, env: CanvasEnv): Promise<Blob> {
  for (const quality of [COVER_QUALITY, 0.5]) {
    const blob = await env.encode(canvas, 'image/jpeg', quality)
    // 不支持编 JPEG 的浏览器会退回 PNG。
    if (!blob || blob.type !== 'image/jpeg') throw new CoverError('internal', '浏览器不能把画布编成 JPEG')
    if (blob.size <= COVER_MAX_BYTES) return blob
  }
  throw new CoverError('internal', '封面编出来超过 256 KiB')
}

// ---------- 读文件头 ----------

/** 按魔数识别格式（不看扩展名）。经典 TIFF 和 BigTIFF 都算 tiff（BigTIFF 在读 IFD 时拒绝）。 */
export function sniffFormat(head: Uint8Array): CoverFormat | null {
  if (head.length >= 3 && head[0] === 0xff && head[1] === 0xd8 && head[2] === 0xff) return 'jpeg'
  if (head.length >= 8 && head[0] === 0x89 && head[1] === 0x50 && head[2] === 0x4e && head[3] === 0x47) return 'png'
  if (head.length >= 4) {
    const ii = head[0] === 0x49 && head[1] === 0x49 && (head[2] === 42 || head[2] === 43) && head[3] === 0
    const mm = head[0] === 0x4d && head[1] === 0x4d && head[2] === 0 && (head[3] === 42 || head[3] === 43)
    if (ii || mm) return 'tiff'
  }
  return null
}

/** 按需分块读 Blob（每块 64 KiB），给按偏移读文件头用；读到文件末尾之外返回 undefined。 */
class BlobBytes {
  private start = -1
  private buf = new Uint8Array(0)
  private readonly blob: Blob
  constructor(blob: Blob) {
    this.blob = blob
  }

  async u8(pos: number): Promise<number | undefined> {
    if (pos < 0 || pos >= this.blob.size) return undefined
    if (pos < this.start || pos >= this.start + this.buf.length) {
      this.start = pos
      this.buf = new Uint8Array(await this.blob.slice(pos, pos + 65536).arrayBuffer())
    }
    return this.buf[pos - this.start]
  }

  async u16be(pos: number): Promise<number | undefined> {
    const a = await this.u8(pos)
    const b = await this.u8(pos + 1)
    return a === undefined || b === undefined ? undefined : (a << 8) | b
  }
}

/** JPEG 的 SOF 宽高（存储方向，没转正；只用来算像素数）。找标记的规则同 server（design §4.3 细化）。 */
async function jpegSize(blob: Blob): Promise<{ width: number; height: number } | null> {
  const r = new BlobBytes(blob)
  let pos = 2
  let markers = 0
  let skipped = 0
  while (markers < 1024) {
    const b = await r.u8(pos)
    if (b === undefined) return null
    if (b !== 0xff) {
      // 标记段之间的多余字节：跳过（最多 64 KiB）。
      pos++
      if (++skipped > 65536) return null
      continue
    }
    let m = await r.u8(pos + 1)
    while (m === 0xff) {
      pos++
      if (++skipped > 65536) return null
      m = await r.u8(pos + 1)
    }
    if (m === undefined) return null
    pos += 2
    if (m === 0x00) {
      if (++skipped > 65536) return null
      continue
    }
    markers++
    if (m === 0xd8 || m === 0x01 || (m >= 0xd0 && m <= 0xd7)) continue
    if (m === 0xd9 || m === 0xda) return null
    const len = await r.u16be(pos)
    if (len === undefined || len < 2) return null
    const isSof = m >= 0xc0 && m <= 0xcf && m !== 0xc4 && m !== 0xc8 && m !== 0xcc
    if (isSof) {
      const height = await r.u16be(pos + 3)
      const width = await r.u16be(pos + 5)
      return width && height ? { width, height } : null
    }
    pos += len
  }
  return null
}

/** PNG 的 IHDR 宽高。 */
async function pngSize(blob: Blob): Promise<{ width: number; height: number } | null> {
  if (blob.size < 24) return null
  const v = new DataView(await blob.slice(0, 24).arrayBuffer())
  if (v.getUint32(12) !== 0x49484452) return null // 'IHDR'
  const width = v.getUint32(16)
  const height = v.getUint32(20)
  return width && height ? { width, height } : null
}

// ---------- JPEG、PNG ----------

/**
 * 按 EXIF 方向转正解码。`imageOrientation: 'from-image'` 是 Chrome 112 起才认的写法，更早的版本抛 TypeError：
 * 这时去掉这个选项再解一次（那些版本默认也按 EXIF 转正）。
 */
async function decodeOriented(blob: Blob): Promise<ImageBitmap> {
  try {
    return await createImageBitmap(blob, { imageOrientation: 'from-image' })
  } catch (e) {
    if (e instanceof TypeError) return createImageBitmap(blob)
    throw e
  }
}

/**
 * 去掉 PNG 里的 eXIf 块（不读整个文件：按块头跳着读，用 Blob 拼接）。Chrome 会按 eXIf 里的方向转 PNG，
 * server 和壳都不看 PNG 的方向，不去掉的话封面方向和 server 记的宽高对不上（M1-12 审查）。
 */
export async function pngWithoutExif(blob: Blob): Promise<Blob> {
  const skip: [number, number][] = []
  let pos = 8
  for (let i = 0; i < 4096 && pos + 8 <= blob.size; i++) {
    const head = new DataView(await blob.slice(pos, pos + 8).arrayBuffer())
    const len = head.getUint32(0)
    const type = String.fromCharCode(head.getUint8(4), head.getUint8(5), head.getUint8(6), head.getUint8(7))
    const end = pos + 12 + len
    if (type === 'eXIf') skip.push([pos, end])
    // eXIf 只能在 IDAT 之前。
    if (type === 'IDAT' || type === 'IEND' || end > blob.size) break
    pos = end
  }
  if (!skip.length) return blob
  const parts: Blob[] = []
  let from = 0
  for (const [a, b] of skip) {
    parts.push(blob.slice(from, a))
    from = b
  }
  parts.push(blob.slice(from))
  return new Blob(parts, { type: 'image/png' })
}

async function bitmapCover(file: Blob, format: 'jpeg' | 'png', env: CanvasEnv) {
  const size = format === 'jpeg' ? await jpegSize(file) : await pngSize(file)
  if (!size) throw new CoverError('corrupt', '读不出图片的宽高，文件可能已损坏')
  if (size.width * size.height > MAX_BITMAP_PIXELS) {
    throw new CoverError('too_large', `图片太大（${megapixels(size.width * size.height)}），浏览器里不生成封面`)
  }
  let bitmap: ImageBitmap
  try {
    // PNG 不按 EXIF 转正（和 server、壳一致：server 不看 PNG 的方向）：去掉 eXIf 块再解码。
    // JPEG 按 EXIF 转正（server 也按 EXIF 方向交换宽高）。
    bitmap = format === 'png' ? await createImageBitmap(await pngWithoutExif(file)) : await decodeOriented(file)
  } catch {
    throw new CoverError('corrupt', '浏览器解不了这张图，文件可能已损坏')
  }
  try {
    const sw = bitmap.width
    const sh = bitmap.height
    const t = coverSize(sw, sh)
    const canvas = scaleDown(bitmap, sw, sh, t.width, t.height, env)
    const blob = await encodeJpeg(canvas, env)
    return { blob, sourceWidth: sw, sourceHeight: sh, width: t.width, height: t.height }
  } finally {
    bitmap.close()
  }
}

// ---------- TIFF ----------
//
// 不把原文件交给 `UTIF.decode`：它不限制标签的 count、会递归读 SubIFD、EXIF、MakerNote（可以成环），特制的文件能把
// 标签页的内存吃到几个 GB 或卡死（M1-12 审查）。这里自己按偏移读第一个 IFD，只取白名单里的标签（每个标签
// count × 类型大小不超过文件大小、值落在文件里），检查条带或分块的位置和数量，把内存预算算全，再自己拼出 UTIF 的
// IFD 对象，只调 `UTIF.decodeImage` 解像素。

/** 浏览器里能解的压缩方式（UTIF 支持且验证过的）。32946 是旧的 Deflate 编号，按 8 解。 */
const TIFF_COMPRESSIONS: Record<number, string> = {
  1: '无压缩',
  3: 'CCITT G3',
  4: 'CCITT G4',
  5: 'LZW',
  7: 'JPEG',
  8: 'Deflate',
  32946: 'Deflate',
  32773: 'PackBits',
}

/** 读的标签（其余一律不读）。338（ExtraSamples）只用来判断 alpha 是不是预乘的（M2-4 审查第 1 轮），不交给 UTIF。 */
const TIFF_TAGS = new Set([256, 257, 258, 259, 262, 266, 273, 274, 277, 278, 279, 284, 317, 320, 322, 323, 324, 325, 338, 347])

/** 类型 → 每个值的字节数：BYTE、SHORT、LONG、UNDEFINED（JPEGTables 用）。 */
const TIFF_TYPE_SIZE: Record<number, number> = { 1: 1, 3: 2, 4: 4, 7: 1 }

interface TiffEntry {
  type: number
  count: number
  /** 值在文件里的位置（不超过 4 字节时就在条目里）。 */
  pos: number
}

export interface TiffInfo {
  le: boolean
  width: number
  height: number
  compression: number
  photometric: number | null
  /** 已补齐成 SamplesPerPixel 个（只写了一个值的按每个通道都是它）。 */
  bitsPerSample: number[]
  samplesPerPixel: number
  planar: number
  /** 没写时为 1；不在 1–8 里的按 1（和 server 一样）。 */
  orientation: number
  fillOrder: number
  predictor: number | null
  rowsPerStrip: number
  tiled: boolean
  tileWidth: number
  tileHeight: number
  /** ExtraSamples 的第一个值（0 没说明，1 预乘的 alpha，2 不预乘的 alpha）；没写为 null。 */
  extraSamples: number | null
  entries: Map<number, TiffEntry>
}

function tiffCorrupt(why = 'TIFF 的文件头不完整或已损坏'): CoverError {
  return new CoverError('corrupt', why)
}

/**
 * 按偏移读第一个 IFD 里白名单内的标签（只读文件头的几块，不把整个文件读进来）。
 * 也接受已经整份读进内存的 ArrayBuffer（M2-4 看原图的 Worker：下载完直接读，不再包成 Blob 多拷一份）。
 */
export async function readTiffInfo(file: Blob | ArrayBuffer): Promise<TiffInfo> {
  const fileSize = file instanceof ArrayBuffer ? file.byteLength : file.size
  const read = async (start: number, len: number) => {
    if (start < 0 || len < 0 || start + len > fileSize) throw tiffCorrupt()
    if (file instanceof ArrayBuffer) return new DataView(file, start, len)
    return new DataView(await file.slice(start, start + len).arrayBuffer())
  }
  const h = await read(0, 8)
  const le = h.getUint8(0) === 0x49
  const magic = h.getUint16(2, le)
  if (magic === 43) throw new CoverError('unsupported', 'BigTIFF 浏览器里不生成封面')
  if (magic !== 42) throw tiffCorrupt()
  const ifd = h.getUint32(4, le)
  if (ifd < 8) throw tiffCorrupt()
  const count = (await read(ifd, 2)).getUint16(0, le)
  if (count === 0 || count > 1024) throw tiffCorrupt()
  const table = await read(ifd + 2, count * 12)
  const entries = new Map<number, TiffEntry>()
  for (let i = 0; i < count; i++) {
    const at = i * 12
    const tag = table.getUint16(at, le)
    if (!TIFF_TAGS.has(tag) || entries.has(tag)) continue
    const type = table.getUint16(at + 2, le)
    const n = table.getUint32(at + 4, le)
    const size = TIFF_TYPE_SIZE[type]
    if (!size || n === 0) continue
    const bytes = size * n
    if (bytes > fileSize) throw tiffCorrupt(`TIFF 的标签 ${tag} 声明了 ${n} 个值，比文件还大`)
    const pos = bytes <= 4 ? ifd + 2 + at + 8 : table.getUint32(at + 8, le)
    if (pos + bytes > fileSize) throw tiffCorrupt(`TIFF 的标签 ${tag} 的值超出文件末尾`)
    entries.set(tag, { type, count: n, pos })
  }

  /** 一个标签的前几个值（数字类型）；没写时返回 undefined。 */
  async function values(tag: number, max: number): Promise<number[] | undefined> {
    const e = entries.get(tag)
    if (!e || e.type === 7) return undefined
    const size = TIFF_TYPE_SIZE[e.type]!
    const take = Math.min(e.count, max)
    const dv = await read(e.pos, size * take)
    return readNumbers(dv, 0, e.type, take, le)
  }
  const first = async (tag: number) => (await values(tag, 1))?.[0]

  const width = await first(256)
  const height = await first(257)
  if (!width || !height) throw tiffCorrupt()
  const spp = (await first(277)) ?? 1
  if (spp < 1 || spp > 16) throw tiffCorrupt(`TIFF 的通道数（${spp}）不对`)
  // BitsPerSample 只写一个值时按每个通道都是它（UTIF 按数组长度算通道数，不补齐会解出全白或乱色）。
  const bps = (await values(258, spp)) ?? [1]
  while (bps.length < spp) bps.push(bps[0]!)
  const orientation = (await first(274)) ?? 1
  const tiled = entries.has(322) || entries.has(324)
  return {
    le,
    width,
    height,
    compression: (await first(259)) ?? 1,
    photometric: (await first(262)) ?? null,
    bitsPerSample: bps,
    samplesPerPixel: spp,
    planar: (await first(284)) ?? 1,
    orientation: orientation >= 1 && orientation <= 8 ? orientation : 1,
    fillOrder: (await first(266)) ?? 1,
    predictor: (await first(317)) ?? null,
    rowsPerStrip: Math.min((await first(278)) ?? height, height) || height,
    tiled,
    tileWidth: (await first(322)) ?? 0,
    tileHeight: (await first(323)) ?? 0,
    extraSamples: (await first(338)) ?? null,
    entries,
  }
}

function readNumbers(dv: DataView, at: number, type: number, n: number, le: boolean): number[] {
  const out = new Array<number>(n)
  for (let i = 0; i < n; i++) {
    out[i] = type === 1 || type === 7 ? dv.getUint8(at + i) : type === 3 ? dv.getUint16(at + i * 2, le) : dv.getUint32(at + i * 4, le)
  }
  return out
}

/** UTIF 的 toRGBA8 能正确转换的组合；不支持时返回原因。 */
export function tiffUnsupported(info: TiffInfo): string | null {
  const name = TIFF_COMPRESSIONS[info.compression]
  if (!name) return `TIFF 的压缩方式（${info.compression}）浏览器里解不了`
  const spp = info.samplesPerPixel
  const bps = info.bitsPerSample[0] ?? 1
  if (info.bitsPerSample.some((b) => b !== bps)) return 'TIFF 各通道的位深不同'
  if (spp > 1 && info.planar !== 1) return 'TIFF 的通道分开存放（PlanarConfiguration=2）'
  if (info.tiled && (!info.tileWidth || !info.tileHeight)) return 'TIFF 的分块尺寸没写'
  const ok = (() => {
    switch (info.photometric) {
      case 0: // WhiteIsZero
        return spp === 1 && [1, 4, 8].includes(bps)
      case 1: // BlackIsZero
        return spp === 1 && [1, 2, 8, 16].includes(bps)
      case 2: // RGB（可以带一个 alpha）
        return (spp === 3 || spp === 4) && (bps === 8 || bps === 16)
      case 3: // 调色板
        return spp === 1 && bps === 8
      case 5: // CMYK
        return (spp === 4 || spp === 5) && bps === 8
      case 6: // YCbCr：只认 JPEG 压缩的（解 JPEG 时转成 RGB）
        return info.compression === 7 && spp === 3 && bps === 8
      default:
        return false
    }
  })()
  return ok ? null : `TIFF 的颜色格式（Photometric=${info.photometric ?? '未写'}，${spp} 通道 × ${bps} 位）浏览器里解不了`
}

/** 解码要用的内存（字节），和 UTIF 的分配一致；超过预算就不生成封面。 */
export function tiffDecodeBytes(info: TiffInfo, fileSize: number) {
  const { width: w, height: h } = info
  const bitsPerPixel = info.bitsPerSample.reduce((a, b) => a + b, 0)
  const rowBytes = Math.ceil((w * bitsPerPixel) / 8)
  const raw = rowBytes * h
  const pixels = w * h
  const deflate = info.compression === 8 || info.compression === 32946
  const tileBytes = info.tiled ? Math.ceil((info.tileWidth * info.tileHeight * bitsPerPixel) / 8) : 0
  const total =
    fileSize + // 整个文件
    raw + // UTIF 解出的原始数据
    (deflate ? raw : 0) + // Deflate 先解成一份无压缩的（inflateStrips）
    tileBytes + // UTIF 的分块缓冲
    (info.compression === 7 ? pixels * 9 : 0) + // JPEG 压缩：UTIF 的 JpegDecoder 每像素约多 9 字节
    pixels * 4 // RGBA
  return { total, rowBytes, raw, tileBytes }
}

/** 条带或分块的位置和长度：按需要的个数截短，逐个确认落在文件里。 */
function tiffChunks(info: TiffInfo, buf: ArrayBuffer, rowBytes: number, tileBytes: number) {
  const dv = new DataView(buf)
  const arr = (tag: number) => {
    const e = info.entries.get(tag)
    return e && e.type !== 7 ? readNumbers(dv, e.pos, e.type, e.count, info.le) : undefined
  }
  const offsets = arr(info.tiled ? 324 : 273)
  const counts = arr(info.tiled ? 325 : 279)
  if (!offsets) throw tiffCorrupt('TIFF 没写条带（或分块）的位置')
  const need = info.tiled
    ? Math.ceil(info.width / info.tileWidth) * Math.ceil(info.height / info.tileHeight)
    : Math.ceil(info.height / info.rowsPerStrip)
  if (offsets.length < need) throw tiffCorrupt('TIFF 的条带（或分块）个数不够')
  /** 每块解开后应有的字节数（Deflate 解压的上限；无压缩的条带最多拷这么多）。 */
  const chunkBytes = info.tiled ? tileBytes : info.rowsPerStrip * rowBytes
  const off = offsets.slice(0, need)
  let cnt: number[]
  if (counts && counts.length >= need) cnt = counts.slice(0, need)
  else if (info.compression === 1) cnt = off.map(() => chunkBytes)
  else throw tiffCorrupt('TIFF 没写条带（或分块）的长度')
  for (let i = 0; i < need; i++) {
    if (info.compression === 1) cnt[i] = Math.min(cnt[i]!, chunkBytes)
    if (off[i]! + cnt[i]! > buf.byteLength) throw tiffCorrupt('TIFF 的条带（或分块）超出文件末尾')
  }
  return { offsets: off, counts: cnt, chunkBytes }
}

let pakoModule: Promise<{ Inflate: new () => PakoInflate }> | null = null
interface PakoInflate {
  onData: (chunk: Uint8Array) => void
  push(data: Uint8Array, last: boolean): boolean
  err: number
}

/**
 * 解一块 Deflate，最多 `limit` 字节：够了就停（多出来的不要），解压炸弹不会撑爆内存。
 * 优先用浏览器自带的 DecompressionStream；没有时退回 pako（utif 的依赖，已经打包进来，按需加载）。
 */
async function inflateLimited(src: Uint8Array<ArrayBuffer>, limit: number): Promise<Uint8Array> {
  const out = new Uint8Array(limit)
  let n = 0
  if (typeof DecompressionStream !== 'undefined') {
    const reader = new Blob([src]).stream().pipeThrough(new DecompressionStream('deflate')).getReader()
    try {
      while (n < limit) {
        const { done, value } = await reader.read()
        if (done) break
        const take = Math.min(value.length, limit - n)
        out.set(value.subarray(0, take), n)
        n += take
      }
    } catch {
      // 数据坏了：已经解出的照用（一般是末尾的校验和不对），一点都没有就算坏文件。
      if (n === 0) throw tiffCorrupt('TIFF 的 Deflate 数据解不开')
    } finally {
      reader.cancel().catch(() => {})
    }
    return out.subarray(0, n)
  }
  pakoModule ??= import('pako').then((m) => (m as { default?: unknown }).default ?? m) as Promise<{
    Inflate: new () => PakoInflate
  }>
  const pako = await pakoModule
  const inf = new pako.Inflate()
  const FULL = new Error('full')
  inf.onData = (chunk) => {
    const take = Math.min(chunk.length, limit - n)
    out.set(chunk.subarray(0, take), n)
    n += take
    if (n >= limit) throw FULL
  }
  try {
    inf.push(src, true)
  } catch (e) {
    if (e !== FULL && n === 0) throw tiffCorrupt('TIFF 的 Deflate 数据解不开')
  }
  if (n === 0 && inf.err) throw tiffCorrupt('TIFF 的 Deflate 数据解不开')
  return out.subarray(0, n)
}

/**
 * Deflate 的各块先解开（每块不超过应有的大小），拼成一份"无压缩"的数据交给 UTIF；预测器（Predictor=2）仍由 UTIF
 * 处理。返回新的缓冲区（开头 8 字节照抄原文件头，UTIF 靠它判断字节序）和新的位置、长度。
 */
async function inflateChunks(buf: ArrayBuffer, offsets: number[], counts: number[], chunkBytes: number) {
  const parts: Uint8Array[] = []
  let total = 8
  for (let i = 0; i < offsets.length; i++) {
    const part = await inflateLimited(new Uint8Array(buf, offsets[i]!, counts[i]!), chunkBytes)
    parts.push(part)
    total += part.length
  }
  const merged = new Uint8Array(total)
  merged.set(new Uint8Array(buf, 0, 8), 0)
  const newOffsets: number[] = []
  const newCounts: number[] = []
  let pos = 8
  for (const p of parts) {
    merged.set(p, pos)
    newOffsets.push(pos)
    newCounts.push(p.length)
    pos += p.length
  }
  return { buf: merged.buffer, offsets: newOffsets, counts: newCounts }
}

/** 用自己读出的标签拼 UTIF 的 IFD 对象（`t<标签>` → 数组）。 */
function utifIfd(info: TiffInfo, buf: ArrayBuffer, compression: number, offsets: number[], counts: number[]): IFD {
  const ifd: IFD = {
    t256: [info.width],
    t257: [info.height],
    t258: info.bitsPerSample,
    t259: [compression],
    t262: [info.photometric!],
    t266: [info.fillOrder],
    t277: [info.samplesPerPixel],
    t284: [info.planar],
  }
  if (info.predictor !== null) ifd.t317 = [info.predictor]
  if (info.tiled) {
    ifd.t322 = [info.tileWidth]
    ifd.t323 = [info.tileHeight]
    ifd.t324 = offsets
    ifd.t325 = counts
  } else {
    ifd.t278 = [info.rowsPerStrip]
    ifd.t273 = offsets
    ifd.t279 = counts
  }
  const dv = new DataView(buf)
  const cmap = info.entries.get(320)
  if (info.photometric === 3) {
    if (!cmap || cmap.type !== 3 || cmap.count < 768) throw tiffCorrupt('调色板 TIFF 的颜色表不完整')
    ifd.t320 = readNumbers(dv, cmap.pos, 3, cmap.count, info.le)
  }
  const tables = info.entries.get(347)
  if (tables && compression === 7) ifd.t347 = new Uint8Array(buf.slice(tables.pos, tables.pos + tables.count))
  return ifd
}

async function tiffCover(file: Blob, env: CanvasEnv) {
  const info = await readTiffInfo(file)
  if (info.orientation !== 1) {
    throw new CoverError('orientation', `TIFF 的方向标记是 ${info.orientation}（不是 1），浏览器里不转正，不生成封面`)
  }
  const why = tiffUnsupported(info)
  if (why) throw new CoverError('unsupported', why)
  const { width: w, height: h } = info
  const pixels = w * h
  const mem = tiffDecodeBytes(info, file.size)
  if (pixels > MAX_TIFF_PIXELS || mem.total > MAX_TIFF_DECODE_BYTES) {
    throw new CoverError('too_large', `TIFF 太大（${megapixels(pixels)}，解码约要 ${Math.round(mem.total / 1048576)} MB），浏览器里不生成封面`)
  }

  const rgba = await decodeTiffRgba(await file.arrayBuffer(), info)

  const t = coverSize(w, h)
  const k = Math.max(1, Math.floor(Math.max(w, h) / Math.max(t.width, t.height)))
  const small = boxDownsample(rgba, w, h, k, info.extraSamples === 1)
  const mid = env.create(small.width, small.height)
  ctx2d(mid).putImageData(new ImageData(small.data, small.width, small.height), 0, 0)
  const canvas = scaleDown(mid, small.width, small.height, t.width, t.height, env)
  const blob = await encodeJpeg(canvas, env)
  return { blob, sourceWidth: w, sourceHeight: h, width: t.width, height: t.height }
}

/**
 * 把整份 TIFF（已经按 readTiffInfo 读出标签、查过支持范围和预算）解成 RGBA（每像素 4 字节，宽 × 高 = info 里的）。
 * Deflate 先自己逐块解开（每块有上限），其余交给 UTIF.decodeImage。解不了抛 CoverError('corrupt')。
 * 封面和 M2-4 看原图的 Worker 共用：调用方先做 orientation、tiffUnsupported、预算（MAX_TIFF_PIXELS、
 * MAX_TIFF_DECODE_BYTES）的检查。
 */
export async function decodeTiffRgba(file: ArrayBuffer, info: TiffInfo): Promise<Uint8Array> {
  const { width: w, height: h } = info
  const mem = tiffDecodeBytes(info, file.byteLength)
  let buf = file
  let { offsets, counts } = tiffChunks(info, buf, mem.rowBytes, mem.tileBytes)
  let compression = info.compression === 32946 ? 8 : info.compression
  if (compression === 8) {
    const chunkBytes = info.tiled ? mem.tileBytes : info.rowsPerStrip * mem.rowBytes
    ;({ buf, offsets, counts } = await inflateChunks(buf, offsets, counts, chunkBytes))
    compression = 1
  }
  let rgba: Uint8Array
  try {
    const ifd = utifIfd(info, buf, compression, offsets, counts)
    resetUtifState()
    UTIF.decodeImage(buf, ifd)
    if (!ifd.data || ifd.width !== w || ifd.height !== h) throw new Error('解出来的尺寸不对')
    rgba = UTIF.toRGBA8(ifd)
    ifd.data = undefined
  } catch (e) {
    if (e instanceof CoverError) throw e
    throw new CoverError('corrupt', `TIFF 解码失败：${e instanceof Error ? e.message : String(e)}`)
  }
  if (rgba.length < w * h * 4) throw new CoverError('corrupt', 'TIFF 解码失败：像素数据不完整')
  return rgba
}

/**
 * 盒式平均缩小 k 倍（k×k 个像素平均成一个），同时把 alpha 合成到白底。输出 RGBA（alpha 都是 255）。
 * 合成白底的公式和壳一致（shell/src-tauri/src/tiles/source.rs 的 flatten8）：不预乘 `c × a / 255 + (255 − a)`；
 * 预乘（TIFF 的 ExtraSamples=1，`premultiplied` 为 true）`min(255, c + (255 − a))`。
 */
export function boxDownsample(rgba: Uint8Array, w: number, h: number, k: number, premultiplied = false) {
  const ow = Math.ceil(w / k)
  const oh = Math.ceil(h / k)
  const out = new Uint8ClampedArray(ow * oh * 4)
  const sums = new Float64Array(ow * 3)
  const counts = new Uint32Array(ow)
  const col = new Uint32Array(w)
  for (let x = 0; x < w; x++) col[x] = Math.floor(x / k)
  for (let oy = 0; oy < oh; oy++) {
    sums.fill(0)
    counts.fill(0)
    const y1 = Math.min(h, (oy + 1) * k)
    for (let y = oy * k; y < y1; y++) {
      let i = y * w * 4
      for (let x = 0; x < w; x++, i += 4) {
        const o = col[x]!
        const a = rgba[i + 3]!
        const s = o * 3
        if (a === 255) {
          sums[s] += rgba[i]!
          sums[s + 1] += rgba[i + 1]!
          sums[s + 2] += rgba[i + 2]!
        } else if (premultiplied) {
          const bg = 255 - a
          sums[s] += Math.min(255, rgba[i]! + bg)
          sums[s + 1] += Math.min(255, rgba[i + 1]! + bg)
          sums[s + 2] += Math.min(255, rgba[i + 2]! + bg)
        } else {
          const bg = 255 - a
          sums[s] += (rgba[i]! * a + 255 * bg) / 255
          sums[s + 1] += (rgba[i + 1]! * a + 255 * bg) / 255
          sums[s + 2] += (rgba[i + 2]! * a + 255 * bg) / 255
        }
        counts[o]!++
      }
    }
    let p = oy * ow * 4
    for (let o = 0; o < ow; o++, p += 4) {
      const n = counts[o]! || 1
      out[p] = sums[o * 3]! / n
      out[p + 1] = sums[o * 3 + 1]! / n
      out[p + 2] = sums[o * 3 + 2]! / n
      out[p + 3] = 255
    }
  }
  return { data: out, width: ow, height: oh }
}

export function megapixels(n: number): string {
  return `${Math.round(n / 1e5) / 10} 百万像素`
}

// ---------- 入口 ----------

/** 给一个文件生成封面；任何失败都不抛出，返回原因。 */
export async function makeCoverCore(file: Blob, env: CanvasEnv = offscreenEnv): Promise<CoverResult> {
  const t0 = performance.now()
  let format: CoverFormat | null = null
  const finish = (r: Partial<CoverResult>): CoverResult => ({
    blob: null,
    reason: null,
    message: null,
    format,
    sourceWidth: null,
    sourceHeight: null,
    width: null,
    height: null,
    ...r,
    ms: Math.round(performance.now() - t0),
  })
  try {
    format = sniffFormat(new Uint8Array(await file.slice(0, 16).arrayBuffer()))
    if (!format) return finish({ reason: 'unsupported_format', message: '不是 JPEG、PNG 或 TIFF' })
    const r = format === 'tiff' ? await tiffCover(file, env) : await bitmapCover(file, format, env)
    return finish(r)
  } catch (e) {
    if (e instanceof CoverError) return finish({ reason: e.code, message: e.message })
    return finish({ reason: 'internal', message: e instanceof Error ? e.message : String(e) })
  }
}
