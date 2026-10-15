// 合成"扫描页"：给 seed:files 和截图脚本造能真正解码的图片（M1-10）。
//
// - 原图是 PNG（Node 自带的 zlib 压缩），A4 竖版：灰度 8 位，每 10 页里有一页是 RGB 彩色（带红色印章）。
//   pHYs 写 DPI；页面上画页码、标题、正文行（长短由种子决定）、表格页、近乎空白的页。
// - 封面按 design §4.3 直接画一张缩小的同一页面：长边 400、长宽比和原图一致，用纯 JS 的 jpeg-js 编成 JPEG
//   （质量 75，不带 EXIF）。只在脚本里用，不进前端产物。
// - 同一个种子总是生成同样的字节：seed:files 重跑时命中 server 的"断网重试"（同一文件夹里 upload_name 和 sha
//   都相同，返回 200），不会重复入库。不同文件夹、不同页的种子不同，内容（sha）也不同，不会被判为重复。
//   PNG 里另有一个 tEXt 块记下种子，保证即使画面碰巧相同 sha 也不同。
//
// 用法：
//   import { makeScanPage } from './lib/scan-pages.mjs'
//   const page = makeScanPage({ seed: 'DEMO-A/盒 001#1', pageNo: 1, dpi: 200 })
//   // → { png: Buffer, cover: Buffer, width, height, dpi, color: boolean, coverWidth, coverHeight }

import { crc32, deflateSync } from 'node:zlib'

import jpeg from 'jpeg-js'

/** A4 的宽高（毫米）。 */
const A4 = { w: 210, h: 297 }
/** 封面的长边（design §4.3）。 */
export const COVER_LONG_EDGE = 400

/** 按 DPI 算 A4 的像素尺寸。 */
export function a4Pixels(dpi) {
  return { width: Math.round((A4.w / 25.4) * dpi), height: Math.round((A4.h / 25.4) * dpi) }
}

/** 封面尺寸：长边 min(400, 原图长边)，短边按 server 的校验公式取整（design §4.3）。 */
export function coverSize(width, height) {
  const long = Math.max(width, height)
  const short = Math.min(width, height)
  const coverLong = Math.min(COVER_LONG_EDGE, long)
  const coverShort = Math.max(1, Math.round((short * coverLong) / long))
  return width >= height ? { width: coverLong, height: coverShort } : { width: coverShort, height: coverLong }
}

// ---------- 随机数 ----------

function hashString(s) {
  // FNV-1a 32 位。
  let h = 0x811c9dc5
  for (const ch of Buffer.from(s, 'utf8')) {
    h ^= ch
    h = Math.imul(h, 0x01000193)
  }
  return h >>> 0
}

function mulberry32(seed) {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

// ---------- 画布 ----------

/** 灰度（1 字节）或 RGB（3 字节）画布，带 PNG 每行开头的过滤字节（0 = None）。 */
class PngCanvas {
  constructor(width, height, channels) {
    this.w = width
    this.h = height
    this.ch = channels
    this.stride = 1 + width * channels
    this.data = Buffer.alloc(this.stride * height, 255)
    for (let y = 0; y < height; y++) this.data[y * this.stride] = 0
  }

  /** 画实心矩形，坐标是像素（左上含、右下不含），颜色 [r, g, b]。 */
  rect(x0, y0, x1, y1, color) {
    x0 = Math.max(0, Math.round(x0))
    y0 = Math.max(0, Math.round(y0))
    x1 = Math.min(this.w, Math.round(x1))
    y1 = Math.min(this.h, Math.round(y1))
    if (x1 <= x0 || y1 <= y0) return
    if (this.ch === 1) {
      const v = luminance(color)
      for (let y = y0; y < y1; y++) {
        const row = y * this.stride + 1
        this.data.fill(v, row + x0, row + x1)
      }
    } else {
      const pattern = Buffer.from(color)
      for (let y = y0; y < y1; y++) {
        const row = y * this.stride + 1
        this.data.fill(pattern, row + x0 * 3, row + x1 * 3)
      }
    }
  }

  /** 圆环（印章）：圆心 (cx, cy)，外半径 r，环宽 t。 */
  ring(cx, cy, r, t, color) {
    const r2 = r * r
    const i2 = (r - t) * (r - t)
    for (let y = Math.max(0, Math.floor(cy - r)); y < Math.min(this.h, Math.ceil(cy + r)); y++) {
      for (let x = Math.max(0, Math.floor(cx - r)); x < Math.min(this.w, Math.ceil(cx + r)); x++) {
        const d = (x - cx) * (x - cx) + (y - cy) * (y - cy)
        if (d <= r2 && d >= i2) this.set(x, y, color)
      }
    }
  }

  set(x, y, color) {
    const o = y * this.stride + 1 + x * this.ch
    if (this.ch === 1) this.data[o] = luminance(color)
    else {
      this.data[o] = color[0]
      this.data[o + 1] = color[1]
      this.data[o + 2] = color[2]
    }
  }
}

/** RGBA 画布（封面，交给 jpeg-js）。 */
class RgbaCanvas {
  constructor(width, height, gray) {
    this.w = width
    this.h = height
    this.gray = gray
    this.data = Buffer.alloc(width * height * 4, 255)
  }

  rect(x0, y0, x1, y1, color) {
    x0 = Math.max(0, Math.round(x0))
    y0 = Math.max(0, Math.round(y0))
    x1 = Math.min(this.w, Math.round(x1))
    y1 = Math.min(this.h, Math.round(y1))
    for (let y = y0; y < y1; y++) for (let x = x0; x < x1; x++) this.set(x, y, color)
  }

  ring(cx, cy, r, t, color) {
    PngCanvas.prototype.ring.call(this, cx, cy, r, t, color)
  }

  set(x, y, color) {
    const o = (y * this.w + x) * 4
    if (this.gray) {
      const v = luminance(color)
      this.data[o] = v
      this.data[o + 1] = v
      this.data[o + 2] = v
    } else {
      this.data[o] = color[0]
      this.data[o + 1] = color[1]
      this.data[o + 2] = color[2]
    }
    this.data[o + 3] = 255
  }
}

function luminance([r, g, b]) {
  return Math.round(0.299 * r + 0.587 * g + 0.114 * b)
}

// ---------- 5×7 点阵字（只有数字、P、-、空格） ----------

const GLYPHS = {
  0: ['01110', '10001', '10011', '10101', '11001', '10001', '01110'],
  1: ['00100', '01100', '00100', '00100', '00100', '00100', '01110'],
  2: ['01110', '10001', '00001', '00010', '00100', '01000', '11111'],
  3: ['11111', '00010', '00100', '00010', '00001', '10001', '01110'],
  4: ['00010', '00110', '01010', '10010', '11111', '00010', '00010'],
  5: ['11111', '10000', '11110', '00001', '00001', '10001', '01110'],
  6: ['00110', '01000', '10000', '11110', '10001', '10001', '01110'],
  7: ['11111', '00001', '00010', '00100', '01000', '01000', '01000'],
  8: ['01110', '10001', '10001', '01110', '10001', '10001', '01110'],
  9: ['01110', '10001', '10001', '01111', '00001', '00010', '01100'],
  P: ['11110', '10001', '10001', '11110', '10000', '10000', '10000'],
  '-': ['00000', '00000', '00000', '11111', '00000', '00000', '00000'],
  ' ': ['00000', '00000', '00000', '00000', '00000', '00000', '00000'],
}

/** 文字宽度（像素）：每个字 5 列加 1 列间隔。 */
function textWidth(text, size) {
  return text.length * 6 * size - size
}

function drawText(canvas, text, x, y, size, color) {
  let cx = x
  for (const ch of text) {
    const glyph = GLYPHS[ch] ?? GLYPHS[' ']
    for (let row = 0; row < 7; row++) {
      for (let col = 0; col < 5; col++) {
        if (glyph[row][col] === '1') {
          canvas.rect(cx + col * size, y + row * size, cx + (col + 1) * size, y + (row + 1) * size, color)
        }
      }
    }
    cx += 6 * size
  }
}

// ---------- 页面 ----------

const INK = [70, 72, 78]
const INK_LIGHT = [150, 152, 158]
const RULE = [120, 122, 128]
const SEAL = [200, 40, 40]

/**
 * 页面的版式（与尺寸无关，坐标是宽高的比例），由种子决定。
 * kind：text 正文；form 表格；blank 近乎空白的页（只有页码）。
 */
function layoutFor(seed, pageNo) {
  const rnd = mulberry32(hashString(seed))
  const kind = pageNo % 25 === 0 ? 'blank' : pageNo % 6 === 0 ? 'form' : 'text'
  const lines = []
  if (kind === 'text') {
    const count = 14 + Math.floor(rnd() * 8)
    for (let i = 0; i < count; i++) {
      // 段落：每 4–6 行一段，段末短一些，段首缩进。
      const paraEnd = rnd() < 0.2
      const indent = i === 0 || rnd() < 0.15 ? 0.06 : 0
      lines.push({ indent, len: paraEnd ? 0.15 + rnd() * 0.4 : 0.62 + rnd() * 0.14 })
    }
  }
  const rows = kind === 'form' ? 8 + Math.floor(rnd() * 6) : 0
  const cols = kind === 'form' ? 3 + Math.floor(rnd() * 3) : 0
  const title = 0.24 + rnd() * 0.22
  const skew = (rnd() - 0.5) * 0.01
  return { kind, lines, rows, cols, title, skew }
}

function drawPage(canvas, layout, pageNo, color) {
  const { w, h } = canvas
  const sx = (f) => f * w
  const sy = (f) => f * h
  // 页码：右上角大号数字，页脚 "- n -"。
  const big = Math.max(1, Math.round(h / 120))
  const no = String(pageNo).padStart(4, '0')
  drawText(canvas, no, sx(0.93) - textWidth(no, big), sy(0.03), big, INK)
  const small = Math.max(1, Math.round(h / 300))
  const footer = `- ${pageNo} -`
  drawText(canvas, footer, (w - textWidth(footer, small)) / 2, sy(0.94), small, INK_LIGHT)

  if (layout.kind === 'blank') return

  // 标题和副标题。
  const tw = layout.title
  canvas.rect(sx(0.5 - tw / 2), sy(0.1), sx(0.5 + tw / 2), sy(0.118), INK)
  canvas.rect(sx(0.38), sy(0.135), sx(0.62), sy(0.142), INK_LIGHT)

  if (layout.kind === 'text') {
    const top = 0.18
    const step = (0.8 - top) / Math.max(layout.lines.length, 1)
    layout.lines.forEach((line, i) => {
      const y = top + i * step + layout.skew * i
      canvas.rect(sx(0.12 + line.indent), sy(y), sx(0.12 + line.indent + line.len), sy(y + 0.009), INK)
    })
  } else {
    // 表格：外框和格线。
    const x0 = 0.1
    const x1 = 0.9
    const y0 = 0.18
    const y1 = 0.18 + layout.rows * 0.045
    const lw = Math.max(1, Math.round(w / 500))
    for (let r = 0; r <= layout.rows; r++) {
      const y = sy(y0 + r * 0.045)
      canvas.rect(sx(x0), y, sx(x1), y + lw, RULE)
    }
    for (let c = 0; c <= layout.cols; c++) {
      const x = sx(x0 + ((x1 - x0) * c) / layout.cols)
      canvas.rect(x, sy(y0), x + lw, sy(y1) + lw, RULE)
    }
    // 第一行是表头，深一点。
    canvas.rect(sx(x0) + lw, sy(y0) + lw, sx(x1), sy(y0 + 0.045), [225, 226, 230])
  }

  if (color) {
    const r = sx(0.1)
    const cx = sx(0.74)
    const cy = sy(0.86)
    canvas.ring(cx, cy, r, Math.max(1, r * 0.1), SEAL)
    // 印章里的三行字。
    for (const dy of [-0.3, 0, 0.3]) {
      const half = dy === 0 ? 0.5 : 0.35
      canvas.rect(cx - r * half, cy + r * (dy - 0.06), cx + r * half, cy + r * (dy + 0.06), SEAL)
    }
  }
}

// ---------- 编码 ----------

function chunk(type, data) {
  const len = Buffer.alloc(4)
  len.writeUInt32BE(data.length)
  const td = Buffer.concat([Buffer.from(type, 'latin1'), data])
  const crc = Buffer.alloc(4)
  crc.writeUInt32BE(crc32(td) >>> 0)
  return Buffer.concat([len, td, crc])
}

function encodePng(canvas, dpi, text) {
  const ihdr = Buffer.alloc(13)
  ihdr.writeUInt32BE(canvas.w, 0)
  ihdr.writeUInt32BE(canvas.h, 4)
  ihdr[8] = 8 // 位深
  ihdr[9] = canvas.ch === 1 ? 0 : 2 // 灰度 / RGB
  ihdr[10] = 0
  ihdr[11] = 0
  ihdr[12] = 0
  const parts = [Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk('IHDR', ihdr)]
  if (dpi) {
    // pHYs：每米像素数，单位 1（米）。server 换算回 DPI 时四舍五入。
    const ppm = Math.round(dpi / 0.0254)
    const phys = Buffer.alloc(9)
    phys.writeUInt32BE(ppm, 0)
    phys.writeUInt32BE(ppm, 4)
    phys[8] = 1
    parts.push(chunk('pHYs', phys))
  }
  if (text) parts.push(chunk('tEXt', Buffer.from(`Comment\0${text}`, 'latin1')))
  parts.push(chunk('IDAT', deflateSync(canvas.data, { level: 6 })))
  parts.push(chunk('IEND', Buffer.alloc(0)))
  return Buffer.concat(parts)
}

/**
 * 生成一页。
 * - seed：决定版式和字节（同一个种子总是得到同样的结果）；
 * - pageNo：画在页面上的页码；
 * - dpi：写进 pHYs 的 DPI，同时决定像素尺寸（A4）；传 null 表示不写分辨率（server 记 DPI 未知）；
 * - pixelDpi：按这个 DPI 算像素尺寸（默认同 dpi；dpi 为 null 时默认 200）；
 * - color：彩色页（RGB，带红色印章）；不传时每 10 页一页彩色。
 */
export function makeScanPage({ seed, pageNo, dpi = 200, pixelDpi, color }) {
  const { width, height } = a4Pixels(pixelDpi ?? dpi ?? 200)
  const isColor = color ?? pageNo % 10 === 7
  const layout = layoutFor(seed, pageNo)

  const page = new PngCanvas(width, height, isColor ? 3 : 1)
  drawPage(page, layout, pageNo, isColor)
  // 只用 ASCII 写种子（tEXt 是 Latin-1）：中文部分换成哈希。
  const png = encodePng(page, dpi, `doclite-seed ${hashString(seed).toString(16)} ${seed.replace(/[^\x20-\x7e]/g, '?')}`)

  const cs = coverSize(width, height)
  const cover = new RgbaCanvas(cs.width, cs.height, !isColor)
  drawPage(cover, layout, pageNo, isColor)
  const jpg = jpeg.encode({ data: cover.data, width: cs.width, height: cs.height }, 75).data

  return {
    png,
    cover: Buffer.from(jpg),
    width,
    height,
    dpi,
    color: isColor,
    coverWidth: cs.width,
    coverHeight: cs.height,
  }
}
