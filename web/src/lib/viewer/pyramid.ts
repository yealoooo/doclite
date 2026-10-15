// 浏览器里看 TIFF 原图（M2-4）用的金字塔：Worker 解出整张像素后，在内存里按 DZI 的规则逐级缩小，按块交给
// OpenSeadragon。规则和壳的分块（M2-3，shell/src-tauri/src/tiles/）一致：
// - 最高一级 `ceil(log2(max(宽, 高)))` 是原尺寸，每低一级宽高减半（向上取整），第 0 级 1×1；
// - 每级由上一级 2×2 取平均（四舍五入；奇数边上的最后一列、一行按实际有的像素平均）；
// - 每块 512×512，和相邻块各重叠 1 像素（块最大 514×514）。
// 像素按通道存：灰度和黑白 1 个通道（每像素 1 字节），其余 RGBA 4 个通道（alpha 已合成到白底，都是 255）。
// 纯函数，不碰 DOM，Worker 和测试都能用。

/** 每块的边长和重叠（和壳的分块一致）。 */
export const TILE_SIZE = 512
export const TILE_OVERLAP = 1

export interface PixelLevel {
  width: number
  height: number
  /** 1：灰度（R=G=B）；4：RGBA。 */
  channels: 1 | 4
  data: Uint8Array
}

/** DZI 的最高级：`ceil(log2(max(宽, 高)))`（1×1 的图是 0）。 */
export function dziMaxLevel(width: number, height: number): number {
  const m = Math.max(width, height)
  let level = 0
  while (2 ** level < m) level++
  return level
}

/** 第 `level` 级的宽高：原尺寸除以 2^(maxLevel − level)，向上取整。 */
export function dziLevelSize(width: number, height: number, maxLevel: number, level: number) {
  const div = 2 ** Math.max(0, maxLevel - level)
  return { width: Math.ceil(width / div), height: Math.ceil(height / div) }
}

/** 第 i 块在长度为 len 的边上覆盖的范围 [lo, hi)（含两侧的重叠）。 */
export function tileSpan(i: number, len: number, tile = TILE_SIZE, overlap = TILE_OVERLAP): [number, number] {
  const start = i * tile
  return [Math.max(0, start - overlap), Math.min(len, start + tile + overlap)]
}

/** RGBA → 灰度（取 R：灰度和黑白 TIFF 经 UTIF.toRGBA8 后 R=G=B）。 */
export function rgbaToGray(rgba: Uint8Array, pixels: number): Uint8Array {
  const out = new Uint8Array(pixels)
  for (let i = 0, j = 0; i < pixels; i++, j += 4) out[i] = rgba[j]!
  return out
}

/**
 * 带 alpha 的像素合成到白底（原地改，alpha 都变成 255）；没有半透明的像素时只扫一遍。公式和壳一致
 * （shell/src-tauri/src/tiles/source.rs 的 flatten8）：
 * - 不预乘（ExtraSamples=2 或没写）：`round(c × a / 255 + (255 − a))`；
 * - 预乘（ExtraSamples=1，颜色已经乘过 alpha）：`min(255, c + (255 − a))`。
 */
export function compositeOnWhite(rgba: Uint8Array, premultiplied = false): void {
  for (let i = 3; i < rgba.length; i += 4) {
    const a = rgba[i]!
    if (a === 255) continue
    const bg = 255 - a
    if (premultiplied) {
      rgba[i - 3] = Math.min(255, rgba[i - 3]! + bg)
      rgba[i - 2] = Math.min(255, rgba[i - 2]! + bg)
      rgba[i - 1] = Math.min(255, rgba[i - 1]! + bg)
    } else {
      rgba[i - 3] = Math.round((rgba[i - 3]! * a + 255 * bg) / 255)
      rgba[i - 2] = Math.round((rgba[i - 2]! * a + 255 * bg) / 255)
      rgba[i - 1] = Math.round((rgba[i - 1]! * a + 255 * bg) / 255)
    }
    rgba[i] = 255
  }
}

/** 缩成一半（2×2 取平均，四舍五入；奇数边上按实际有的像素平均）。RGBA 的 alpha 都是 255。 */
export function halve(src: PixelLevel): PixelLevel {
  const { width: w, height: h, channels: ch, data } = src
  const ow = Math.ceil(w / 2)
  const oh = Math.ceil(h / 2)
  const out = new Uint8Array(ow * oh * ch)
  const rowBytes = w * ch
  for (let oy = 0; oy < oh; oy++) {
    const y0 = oy * 2
    const twoRows = y0 + 1 < h
    const r0 = y0 * rowBytes
    const r1 = twoRows ? r0 + rowBytes : r0
    let o = oy * ow * ch
    for (let ox = 0; ox < ow; ox++) {
      const x0 = ox * 2
      const twoCols = x0 + 1 < w
      const n = (twoRows ? 2 : 1) * (twoCols ? 2 : 1)
      const half = n >> 1
      const a = r0 + x0 * ch
      const b = r1 + x0 * ch
      if (ch === 1) {
        let s = data[a]!
        if (twoCols) s += data[a + 1]!
        if (twoRows) {
          s += data[b]!
          if (twoCols) s += data[b + 1]!
        }
        out[o++] = ((s + half) / n) | 0
      } else {
        for (let c = 0; c < 3; c++) {
          let s = data[a + c]!
          if (twoCols) s += data[a + 4 + c]!
          if (twoRows) {
            s += data[b + c]!
            if (twoCols) s += data[b + 4 + c]!
          }
          out[o++] = ((s + half) / n) | 0
        }
        out[o++] = 255
      }
    }
  }
  return { width: ow, height: oh, channels: ch, data: out }
}

/** 从最高一级（原尺寸）逐级缩到第 0 级。返回按级号排的数组（下标就是级号）。 */
export function buildPyramid(base: PixelLevel): PixelLevel[] {
  const maxLevel = dziMaxLevel(base.width, base.height)
  const levels: PixelLevel[] = new Array(maxLevel + 1)
  levels[maxLevel] = base
  for (let l = maxLevel - 1; l >= 0; l--) levels[l] = halve(levels[l + 1]!)
  return levels
}

/** 金字塔里比原尺寸那一级多出来的字节数（每级约是上一级的 1/4，合计不超过 1/3；内存预算按这个算）。 */
export function pyramidExtraBytes(baseBytes: number): number {
  return Math.ceil(baseBytes / 3)
}

/** 金字塔各级像素的总字节数。 */
export function pyramidBytes(levels: PixelLevel[]): number {
  return levels.reduce((s, l) => s + l.data.byteLength, 0)
}

/**
 * 取第 `level` 级的块 (x, y)（含重叠），输出 RGBA。坐标不在这一级的范围里时返回 null。
 */
export function extractTile(
  level: PixelLevel,
  x: number,
  y: number,
): { width: number; height: number; rgba: Uint8ClampedArray<ArrayBuffer> } | null {
  const cols = Math.ceil(level.width / TILE_SIZE)
  const rows = Math.ceil(level.height / TILE_SIZE)
  if (x < 0 || y < 0 || x >= cols || y >= rows) return null
  const [x0, x1] = tileSpan(x, level.width)
  const [y0, y1] = tileSpan(y, level.height)
  const tw = x1 - x0
  const th = y1 - y0
  const out = new Uint8ClampedArray(tw * th * 4)
  const { data, channels: ch, width: w } = level
  if (ch === 4) {
    for (let row = 0; row < th; row++) {
      const from = ((y0 + row) * w + x0) * 4
      out.set(data.subarray(from, from + tw * 4), row * tw * 4)
    }
  } else {
    let o = 0
    for (let row = 0; row < th; row++) {
      let i = (y0 + row) * w + x0
      for (let col = 0; col < tw; col++, i++) {
        const v = data[i]!
        out[o] = v
        out[o + 1] = v
        out[o + 2] = v
        out[o + 3] = 255
        o += 4
      }
    }
  }
  return { width: tw, height: th, rgba: out }
}
