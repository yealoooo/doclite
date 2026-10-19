// 截图脚本和 seed 用的样本（M1-12；M10-2 起浏览器里不能上传，脚本用接口传）。都是真能解码的图片：
// - JPEG、TIFF（无压缩、LZW、1 位 CCITT G4……）、PNG 样本来自 web 自己带的 `scripts/samples/`（从
//   server/testdata/samples 复制的，清单见那里的 README.md）。`sample(name)` 每个进程第一次调用时读进内存，并往每个样本
//   里加一段这次运行特有的注释（JPEG 加 COM 段、PNG 加 tEXt 块、TIFF 在末尾追加），图片不变、内容各次运行不同，不会和
//   以前上传过的判为内容重复；同一进程里多次取同一个样本得到同样的字节。
// - PNG 用 scan-pages.mjs 的 makeScanPage（和 seed:files 同一份代码），可以指定 DPI（150 就是低 DPI）。

import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { crc32 } from 'node:zlib'

import { makeScanPage } from './scan-pages.mjs'

/** web 自己带的样本目录（scripts/samples）。 */
export const SAMPLES_DIR = fileURLToPath(new URL('../samples/', import.meta.url))

/** 这次运行的标记：写进每个样本，让内容和以前的运行不同。只用 ASCII（PNG 的 tEXt 是 Latin-1）。 */
const RUN_TAG = `doclite-shots ${Date.now()} ${Math.random().toString(36).slice(2)}`

const PNG_SIGNATURE = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])

/** 往样本里加一段这次运行的注释，图片本身不变。 */
function withRunTag(name, buf) {
  return addComment(buf, `${RUN_TAG} ${name}`)
}

/** 往图片里加一段注释（只用 ASCII），图片本身不变。 */
function addComment(buf, text) {
  const tag = Buffer.from(text, 'latin1')
  if (buf[0] === 0xff && buf[1] === 0xd8) {
    // JPEG：在 SOI 和紧跟的 APPn 段（JFIF、EXIF）之后插一个 COM 段。
    let i = 2
    while (i + 4 <= buf.length && buf[i] === 0xff && buf[i + 1] >= 0xe0 && buf[i + 1] <= 0xef) i += 2 + buf.readUInt16BE(i + 2)
    const head = Buffer.from([0xff, 0xfe, 0, 0])
    head.writeUInt16BE(tag.length + 2, 2)
    return Buffer.concat([buf.subarray(0, i), head, tag, buf.subarray(i)])
  }
  if (buf.subarray(0, 8).equals(PNG_SIGNATURE)) {
    // PNG：在 IEND（最后 12 字节）之前插一个 tEXt 块。
    const data = Buffer.concat([Buffer.from('Comment\0', 'latin1'), tag])
    const chunk = Buffer.alloc(12 + data.length)
    chunk.writeUInt32BE(data.length, 0)
    chunk.write('tEXt', 4, 'latin1')
    data.copy(chunk, 8)
    chunk.writeUInt32BE(crc32(chunk.subarray(4, 8 + data.length)), 8 + data.length)
    const at = buf.length - 12
    return Buffer.concat([buf.subarray(0, at), chunk, buf.subarray(at)])
  }
  // TIFF：偏移都是从文件开头算的，末尾多出的字节阅读器不看。
  return Buffer.concat([buf, tag])
}

let samples = null

/** 读 scripts/samples 里的全部样本（加了这次运行的标记），返回 Map<文件名, Buffer>。每个进程只读一次。 */
export function loadSamples() {
  if (!samples) {
    samples = new Map()
    for (const e of readdirSync(SAMPLES_DIR, { withFileTypes: true })) {
      if (!e.isFile() || e.name === 'README.md') continue
      samples.set(e.name, withRunTag(e.name, readFileSync(path.join(SAMPLES_DIR, e.name))))
    }
  }
  return samples
}

/** 一个样本（文件名见 scripts/samples/README.md，例如 `jpeg-rgb.jpg`、`tiff-g4-1bit.tif`）。 */
export async function sample(name) {
  const buf = loadSamples().get(name)
  if (!buf) throw new Error(`scripts/samples 里没有 ${name}（有：${[...loadSamples().keys()].join('、')}）`)
  return buf
}

/** 合成扫描页（PNG，A4，dpi 写进 pHYs）和按封面规格画的 JPEG 封面。 */
export function scanPng(seed, pageNo, dpi = 200) {
  return makeScanPage({ seed, pageNo, dpi })
}
