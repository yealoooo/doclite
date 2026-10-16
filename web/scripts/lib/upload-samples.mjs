// 浏览器上传的截图脚本用的合成文件和助手（M1-12）。都是真能解码的图片：
// - JPEG、TIFF（无压缩、LZW、1 位 CCITT G4……）、PNG 样本来自 web 自己带的 `scripts/samples/`（从
//   server/testdata/samples 复制的，清单见那里的 README.md；原来是每次现场调 cargo 跑上一版的 gen-samples，M1-2 起
//   改为读提交的文件，M7-5 起 web 不再读 server 目录里的文件）。
//   `sample(name)` 每个进程第一次调用时读进内存，并往每个样本里加一段这次运行特有的注释（JPEG 加 COM 段、PNG 加
//   tEXt 块、TIFF 在末尾追加），图片不变、内容各次运行不同，不会和以前上传过的判为内容重复；同一进程里多次取同一个
//   样本得到同样的字节。
// - PNG 用 scan-pages.mjs 的 makeScanPage（和 seed:files 同一份代码），可以指定 DPI（150 就是低 DPI）；
// - 方向标记为 6 的 TIFF 用 UTIF.encodeImage 现场编（浏览器里不转正，不生成封面，用来看"无封面"）。
//
// 页面里用 `injectFiles(page, mode, files)` 把文件交给文件目录页：在隐藏的选择框上派发 `doclite-inject-files`
// 事件（detail 是 File[]；上传文件夹的带 webkitRelativePath），走和选择框完全相同的代码。

import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { crc32 } from 'node:zlib'

import UTIF from 'utif'

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
      // 子目录 encoding/ 是上传会被拒的罕见编码（m7-audit 直接读），不算样本。
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

/**
 * 同一个样本的第 n 个变体：在 `sample(name)` 的基础上再加一段注释，图片不变、字节各不相同（M5-3：m1-accept 原来用
 * gen-samples --pages 3 现场生成多页，改为读提交的样本时用它造"第 1–3 页"，免得被判为内容重复）。
 */
export async function sampleVariant(name, n) {
  return addComment(await sample(name), `${RUN_TAG} ${name} page ${n}`)
}

/** 合成扫描页（PNG，A4，dpi 写进 pHYs）和按封面规格画的 JPEG 封面。 */
export function scanPng(seed, pageNo, dpi = 200) {
  return makeScanPage({ seed, pageNo, dpi })
}

/** 方向标记为 6 的 RGBA TIFF（无压缩，A4 150 DPI 的像素）。 */
export function orientedTiff(seed) {
  const w = 1240
  const h = 1754
  const rgba = new Uint8Array(w * h * 4).fill(255)
  // 横线（行数由种子决定，让内容各不相同）。
  let n = 0
  for (const ch of seed) n = (n * 31 + ch.charCodeAt(0)) >>> 0
  for (let y = 80; y < h - 80; y += 24 + (n % 7)) {
    for (let x = 100; x < w - 100 - ((y * n) % 300); x++) {
      const o = (y * w + x) * 4
      rgba[o] = rgba[o + 1] = rgba[o + 2] = 40
    }
  }
  return Buffer.from(UTIF.encodeImage(rgba, w, h, { t274: [6] }))
}

/**
 * 把文件交给文件目录页上传（`mode`：files 或 folder）。files：[{name, data: Buffer, path?, type?}]，
 * path 是上传文件夹时的相对路径（`选中的文件夹/子文件夹/文件名`）。
 */
export async function injectFiles(page, mode, files) {
  await page.evaluate(() => {
    window.__injectParts = []
  })
  const CHUNK = 6 * 1024 * 1024
  for (let i = 0; i < files.length; i++) {
    const data = files[i].data
    await page.evaluate((idx) => {
      window.__injectParts[idx] = []
    }, i)
    for (let off = 0; off < data.length || off === 0; off += CHUNK) {
      await page.evaluate(
        (idx, b64) => {
          const bin = atob(b64)
          const bytes = new Uint8Array(bin.length)
          for (let k = 0; k < bin.length; k++) bytes[k] = bin.charCodeAt(k)
          window.__injectParts[idx].push(bytes)
        },
        i,
        data.subarray(off, off + CHUNK).toString('base64'),
      )
      if (data.length === 0) break
    }
  }
  await page.evaluate(
    (meta, mode) => {
      const list = meta.map((m, i) => {
        const f = new File(window.__injectParts[i], m.name, { type: m.type ?? '' })
        if (m.path) Object.defineProperty(f, 'webkitRelativePath', { value: m.path })
        return f
      })
      window.__injectParts = null
      const input = document.querySelector(
        mode === 'folder' ? '[data-testid=upload-folder-input]' : '[data-testid=upload-files-input]',
      )
      if (!input) throw new Error('页面上没有上传的选择框')
      input.dispatchEvent(new CustomEvent('doclite-inject-files', { detail: list }))
    },
    files.map((f) => ({ name: f.name, path: f.path, type: f.type })),
    mode,
  )
}

/**
 * 等最新的上传任务结束，返回它每一项的状态（来自结果列表上的 data 属性）。
 * 明细是虚拟列表（UploadJobItems，最高 400 像素），一次只渲染看得见的几十行：从头到尾滚一遍，按行号收齐全部行，
 * 最后滚回顶部（M5-3：21 个文件时最后一行没渲染，原来只读 DOM 里已有的行会漏掉）。
 */
export async function waitUploadDone(page, timeout = 120000) {
  await page.waitForSelector('[data-testid=upload-job]', { timeout: 10000 })
  await page.waitForFunction(
    () => {
      const job = document.querySelector('[data-testid=upload-job]')
      return job && (job.dataset.state === 'done' || job.dataset.state === 'cancelled')
    },
    { timeout },
  )
  return page.evaluate(async () => {
    const job = document.querySelector('[data-testid=upload-job]')
    const read = (e) => ({
      name: e.dataset.name,
      path: e.dataset.path,
      state: e.dataset.state,
      cover: e.dataset.cover,
      coverMs: e.dataset.coverMs ? Number(e.dataset.coverMs) : null,
      fileId: e.dataset.fileId || null,
      hasCover: e.dataset.hasCover === '' ? null : e.dataset.hasCover === 'true',
      warnings: e.dataset.warnings ? e.dataset.warnings.split(',') : [],
      errorCode: e.dataset.errorCode || null,
      text: e.textContent.replace(/\s+/g, ' ').trim(),
    })
    const box = job.querySelector('[data-testid=upload-items]')
    if (!box) return [...job.querySelectorAll('[data-testid=upload-item]')].map(read)
    const rows = new Map()
    const collect = () => {
      for (const w of box.querySelectorAll('[data-index]')) {
        const e = w.querySelector('[data-testid=upload-item]')
        if (e) rows.set(Number(w.dataset.index), read(e))
      }
    }
    const frames = () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))
    box.scrollTop = 0
    await frames()
    collect()
    for (let guard = 0; box.scrollTop + box.clientHeight < box.scrollHeight - 1 && guard < 10000; guard++) {
      box.scrollTop += Math.max(20, Math.floor(box.clientHeight / 2))
      await frames()
      collect()
    }
    box.scrollTop = 0
    await frames()
    return [...rows.entries()].sort((a, b) => a[0] - b[0]).map(([, v]) => v)
  })
}

/** JPEG 的 SOF 宽高（不对时返回 null）。 */
export function jpegSize(buf) {
  if (buf[0] !== 0xff || buf[1] !== 0xd8) return null
  let i = 2
  while (i + 4 < buf.length) {
    if (buf[i] !== 0xff) {
      i++
      continue
    }
    const m = buf[i + 1]
    if (m === 0xff) {
      i++
      continue
    }
    if (m === 0xd8 || m === 0x01 || (m >= 0xd0 && m <= 0xd7)) {
      i += 2
      continue
    }
    const len = buf.readUInt16BE(i + 2)
    if (m >= 0xc0 && m <= 0xcf && m !== 0xc4 && m !== 0xc8 && m !== 0xcc) {
      return { height: buf.readUInt16BE(i + 5), width: buf.readUInt16BE(i + 7) }
    }
    i += 2 + len
  }
  return null
}

/**
 * 核对封面：batch 里有 cover_url 的，下载下来是 JPEG、长边不超过 400、和原图同方向；`expectNoCover` 里的
 * cover_url 为 null。返回每个文件的 {file_id, cover_url, width, height}。`api(method, path, body)` 是调接口的函数。
 */
export async function checkCovers(api, fileIds, expectNoCover = []) {
  const res = await api('POST', '/api/files/batch', { file_ids: fileIds })
  if (res.missing.length) throw new Error(`batch 里缺文件：${res.missing.join(',')}`)
  const out = []
  for (const it of res.items) {
    if (expectNoCover.includes(it.file_id)) {
      if (it.cover_url !== null) throw new Error(`${it.file_id} 应该没有封面，batch 却有 cover_url`)
      out.push({ file_id: it.file_id, cover_url: null })
      continue
    }
    if (!it.cover_url) throw new Error(`${it.file_id} 应该有封面，batch 的 cover_url 为 null`)
    const r = await fetch(it.cover_url)
    if (!r.ok) throw new Error(`下载封面失败：HTTP ${r.status}`)
    const buf = Buffer.from(await r.arrayBuffer())
    const size = jpegSize(buf)
    if (!size) throw new Error(`${it.file_id} 的封面不是 JPEG`)
    if (Math.max(size.width, size.height) > 400) throw new Error(`${it.file_id} 的封面长边超过 400：${size.width}×${size.height}`)
    const portrait = it.height_px >= it.width_px
    if (it.width_px !== it.height_px && (size.height >= size.width) !== portrait) {
      throw new Error(`${it.file_id} 的封面方向和原图不一致：原图 ${it.width_px}×${it.height_px}，封面 ${size.width}×${size.height}`)
    }
    out.push({ file_id: it.file_id, cover_url: it.cover_url, width: size.width, height: size.height, bytes: buf.length })
  }
  return out
}
