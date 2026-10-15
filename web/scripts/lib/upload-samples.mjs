// 浏览器上传的截图脚本用的合成文件和助手（M1-12）。都是真能解码的图片：
// - JPEG、TIFF（无压缩、LZW、1 位 CCITT G4……）来自 M1-9 的 `cargo run --example gen-samples`（shell/src-tauri），
//   A4 300 DPI，每次运行内容随机（不会和上一次的判为内容重复）；
// - PNG 用 scan-pages.mjs 的 makeScanPage（和 seed:files 同一份代码），可以指定 DPI（150 就是低 DPI）；
// - 方向标记为 6 的 TIFF 用 UTIF.encodeImage 现场编（浏览器里不转正，不生成封面，用来看"无封面"）。
//
// 页面里用 `injectFiles(page, mode, files)` 把文件交给文件目录页：在隐藏的选择框上派发 `doclite-inject-files`
// 事件（detail 是 File[]；上传文件夹的带 webkitRelativePath），走和选择框完全相同的代码。
//
// gen-samples 的临时目录放在 SHOTS_TMP（默认系统临时目录）下，读完就删。

import { execFile } from 'node:child_process'
import { mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

import UTIF from 'utif'

import { makeScanPage } from './scan-pages.mjs'

const SHELL_DIR = fileURLToPath(new URL('../../../shell/src-tauri/', import.meta.url))

let samplesPromise = null

/** 调 gen-samples 生成 18 种样本，返回 Map<文件名, Buffer>。每个进程只跑一次（第一次要编译，约 1–3 分钟）。 */
export function genSamples() {
  samplesPromise ??= (async () => {
    const dir = mkdtempSync(path.join(process.env.SHOTS_TMP ?? os.tmpdir(), 'gen-samples-'))
    try {
      const t0 = Date.now()
      await promisify(execFile)('cargo', ['run', '--quiet', '--example', 'gen-samples', '--', '--out', dir], {
        cwd: SHELL_DIR,
        maxBuffer: 16 * 1024 * 1024,
      })
      const map = new Map()
      for (const name of readdirSync(dir)) map.set(name, readFileSync(path.join(dir, name)))
      console.log(`  gen-samples：${map.size} 个样本（${((Date.now() - t0) / 1000).toFixed(0)} 秒）`)
      return map
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })()
  return samplesPromise
}

/** gen-samples 的一个样本。 */
export async function sample(name) {
  const buf = (await genSamples()).get(name)
  if (!buf) throw new Error(`gen-samples 没有输出 ${name}`)
  return buf
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

/** 等最新的上传任务结束，返回它每一项的状态（来自结果列表上的 data 属性）。 */
export async function waitUploadDone(page, timeout = 120000) {
  await page.waitForSelector('[data-testid=upload-job]', { timeout: 10000 })
  await page.waitForFunction(
    () => {
      const job = document.querySelector('[data-testid=upload-job]')
      return job && (job.dataset.state === 'done' || job.dataset.state === 'cancelled')
    },
    { timeout },
  )
  return page.$$eval('[data-testid=upload-job]:first-of-type [data-testid=upload-item]', (els) =>
    els.map((e) => ({
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
    })),
  )
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
