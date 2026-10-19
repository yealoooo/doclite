// M2-4 查看原图的截图脚本用的助手（scripts/shots/m2-viewer.mjs、m2-viewer-lan.mjs）。
//
// - 接口：页面之外用 admin 调接口（登录、batch、建样本）；
// - 样本（M9-3 起）：DEMO-A 下 `npm run seed` 建的「格式样本」（JPEG 彩色、灰度，TIFF LZW、G4 等，没有封面），外加本任务的
//   专用文件夹"M2-4 查看器样本"（第一次运行时建好，之后复用、不删）：
//   - 对比样本.png：v1 是 150 DPI（被驳回：模糊，带说明），v2 是 300 DPI 的重扫；
//   - 超出预算 A0 300dpi.tif：1 位黑白无压缩 TIFF，9933×14043（约 1.4 亿像素，超过浏览器 1 亿像素的预算，17 MB）；
//   - 子文件夹"200 MB 切换"（ensureBench，只在要它的几张截图第一次运行时建）：a3-600-none-rgb.tif（7016×9921 RGB 无压缩，
//     约 209 MB）、a4-300-none-gray.tif（2480×3508 灰度无压缩，8.7 MB）、a4-300-deflate-rgb.tif（同尺寸 Deflate 彩色，几百 KB），
//     按这个顺序上传（切换文件的场景按"下一个"依次打开）。演示数据不放大文件，要这几张的截图建议在临时库上跑。
// - 页面：打开文件夹、右键"查看原图"、等查看器的状态；
// - 壳的模拟（M6-4）：壳模式的地址 + Wails 桥的模拟（scripts/lib/wails-mock.mjs），--base 要指向 devshell
//   （client/cmd/devshell）：封面、分块描述和块由 devshell 用客户端的 Go 代码（internal/imagecache、internal/tiles，
//   和打包的壳同一份）现场下载、切块；来源记录同壳（devshell 转发 batch、snapshots 响应时记下），其余 404。
//   截图脚本只拦截取图地址的请求做统计和故障注入（422、503、延迟）。
// - 渲染进程的内存：按 ps 轮询这个浏览器的 renderer、GPU 进程的 RSS。
// - 共用内存预算的模拟（M2-4 审查第 1 轮）：拦截对比样本的 snapshots 响应，把两个版本换成现场生成的大 TIFF
//   （Deflate 压缩的彩色，文件只有几 MB，解码要几百 MB），原图地址也由截图脚本回答（interceptCompareTiffs）。

import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { deflateSync } from 'node:zlib'

import jpeg from 'jpeg-js'

import { coverSize, makeScanPage } from './scan-pages.mjs'
import { installWailsMock } from './wails-mock.mjs'

export const PROJECT = 'DEMO-A'
export const SAMPLES_FOLDER = '格式样本'
export const OWN_FOLDER = 'M2-4 查看器样本'
export const BENCH_FOLDER = '200 MB 切换'
export const COMPARE_FILE = '对比样本.png'
export const BIG_FILE = '超出预算 A0 300dpi.tif'
/** "200 MB 切换"里的三个，按上传顺序（即文件夹里的顺序）。 */
export const BIG200 = 'a3-600-none-rgb.tif'
export const GRAY_FILE = 'a4-300-none-gray.tif'
export const DEFLATE_FILE = 'a4-300-deflate-rgb.tif'

export function assert(cond, msg) {
  if (!cond) throw new Error(msg)
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

// ---------- 接口 ----------

export function apiClient(origin) {
  let cookie = ''
  async function call(method, p, body, { allow = [] } = {}) {
    const isForm = body instanceof FormData
    const res = await fetch(origin + p, {
      method,
      headers: {
        Accept: 'application/json',
        ...(body !== undefined && !isForm ? { 'Content-Type': 'application/json' } : {}),
        ...(cookie ? { Cookie: cookie } : {}),
      },
      body: body === undefined ? undefined : isForm ? body : JSON.stringify(body),
    })
    const sc = res.headers.get('set-cookie')
    if (sc) cookie = sc.split(';')[0]
    const text = await res.text()
    const data = text ? JSON.parse(text) : undefined
    if (!res.ok && !allow.includes(res.status)) throw new Error(`${method} ${p} → ${res.status} ${data?.code}: ${data?.message}`)
    return data
  }
  return { call }
}

async function resolvePath(call, fromId, p) {
  let id = fromId
  for (const name of p.split('/').filter(Boolean)) {
    const ch = await call('GET', `/api/folders/${id}/children`)
    const f = ch.folders.find((x) => x.folder_name === name)
    if (!f) throw new Error(`找不到文件夹 ${p}（缺「${name}」）`)
    id = f.id
  }
  return id
}

let ctxPromise = null

/**
 * 登录 admin，找到 DEMO-A 和各个样本文件夹，缺的样本建好（只做一次；"200 MB 切换"另见 ensureBench）。返回
 * {origin, api, project, me, folders: {samples, own}, files: {<文件名>: {file_id, folder_id, folder_key, ...}}}。
 */
export function viewerContext(origin, { user, password }) {
  ctxPromise ??= (async () => {
    const { call } = apiClient(origin)
    await call('POST', '/api/auth/login', { username: user, password })
    const me = await call('GET', '/api/me')
    const projects = await call('GET', '/api/projects')
    const project = projects.find((p) => p.project_code === PROJECT)
    assert(project, `找不到项目 ${PROJECT}，先跑 npm run seed`)
    const root = project.root_folder_id
    const folders = {
      samples: await resolvePath(call, root, SAMPLES_FOLDER),
      own: await ensureFolder(call, root, OWN_FOLDER, 'M2-4 截图脚本的样本（对比、超出预算），保留，脚本重跑时复用'),
    }
    await ensureSamples(call, project, me, folders.own)
    const files = {}
    for (const [key, id] of Object.entries(folders)) {
      const ch = await call('GET', `/api/folders/${id}/children`)
      for (const f of ch.files) files[f.file_name] ??= { ...f, folder_id: id, folder_key: key }
    }
    return { origin, api: call, project, me: me.user ?? me, folders, files }
  })()
  return ctxPromise
}

async function ensureFolder(call, parentId, name, remark) {
  const ch = await call('GET', `/api/folders/${parentId}/children`)
  const f = ch.folders.find((x) => x.folder_name === name)
  if (f) return f.id
  const created = await call('POST', '/api/folders', { parent_id: parentId, folder_name: name, remark })
  console.log(`  建了样本文件夹「${name}」`)
  return created.id
}

let benchPromise = null

/**
 * "M2-4 查看器样本/200 MB 切换"：缺的按顺序现场生成、上传（第一次约 210 MB，之后复用），并补进 ctx.files（folder_key 是
 * bench）。只有要它的几张截图调用，别的截图（和 m2-viewer-lan）不会造大文件。
 */
export function ensureBench(ctx) {
  benchPromise ??= (async () => {
    const { api: call } = ctx
    const id = await ensureFolder(call, ctx.folders.own, BENCH_FOLDER, 'M2-4 截图脚本：限速下载 200 MB TIFF 时切换文件，保留，脚本重跑时复用')
    let ch = await call('GET', `/api/folders/${id}/children`)
    const makers = [
      [BIG200, () => plainTiff(7016, 9921, 3, 600)],
      [GRAY_FILE, () => plainTiff(2480, 3508, 1, 300)],
      [DEFLATE_FILE, () => ({ tiff: syntheticTiff(2480, 3508, 7), cover: null })],
    ]
    for (const [name, make] of makers) {
      if (ch.files.some((f) => f.file_name === name)) continue
      const { tiff, cover } = make()
      await call('POST', `/api/folders/${id}/files`, uploadForm(name, tiff, 'image/tiff', cover))
      console.log(`  上传了 ${BENCH_FOLDER}/${name}（${(tiff.length / 1e6).toFixed(1)} MB）`)
    }
    ch = await call('GET', `/api/folders/${id}/children`)
    for (const f of ch.files) ctx.files[f.file_name] ??= { ...f, folder_id: id, folder_key: 'bench' }
    ctx.folders.bench = id
    return ctx
  })()
  return benchPromise
}

function uploadForm(name, data, type, cover) {
  const form = new FormData()
  form.append('file_name', name)
  if (cover) form.append('cover', new Blob([cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([data], { type }), name)
  return form
}

async function ensureSamples(call, project, me, folderId) {
  const ch = await call('GET', `/api/folders/${folderId}/children`)
  const has = (n) => ch.files.find((f) => f.file_name === n)
  const meId = (me.user ?? me).id

  // 对比样本：v1 150 DPI → 分给自己 → 驳回（模糊）→ 重扫 v2 300 DPI。
  let cmp = has(COMPARE_FILE)
  if (!cmp) {
    const v1 = makeScanPage({ seed: 'M2-4 对比样本', pageNo: 3, dpi: 150, color: true })
    cmp = await call('POST', `/api/folders/${folderId}/files`, uploadForm(COMPARE_FILE, v1.png, 'image/png', v1.cover))
    console.log(`  上传了 ${COMPARE_FILE} v1（150 DPI）`)
  }
  if (cmp.version_no === undefined || cmp.version_no < 2) {
    const fid = cmp.file_id
    const reviews = await call('GET', `/api/files/${fid}/logs`)
    const rejected = reviews.logs.some((l) => l.action === 'reject')
    if (!rejected) {
      await call('POST', `/api/projects/${project.id}/review-assignments`, { reviewer_id: meId, file_ids: [fid] })
      await call('POST', `/api/files/${fid}/review`, {
        result: 'rejected',
        reject_reasons: ['blur'],
        comment: '150 dpi，字迹发虚，请按 300 dpi 重扫',
      })
      console.log(`  ${COMPARE_FILE} 已驳回（模糊）`)
    }
    const v2 = makeScanPage({ seed: 'M2-4 对比样本', pageNo: 3, dpi: 300, color: true })
    await call('POST', `/api/files/${fid}/rescan`, uploadForm(COMPARE_FILE, v2.png, 'image/png', v2.cover))
    console.log(`  ${COMPARE_FILE} 重扫了 v2（300 DPI）`)
  }

  if (!has(BIG_FILE)) {
    const { tiff, cover } = bigBilevelTiff()
    await call('POST', `/api/folders/${folderId}/files`, uploadForm(BIG_FILE, tiff, 'image/tiff', cover))
    console.log(`  上传了 ${BIG_FILE}（${(tiff.length / 1e6).toFixed(1)} MB）`)
  }
}

/**
 * 版式（按 300 DPI 的像素写，scale 换算到别的 DPI）：边框、标题块、几十行"正文"。返回 [x, y, 宽, 高] 的列表。
 */
function pageRects(W, H, scale = 1) {
  const k = (v) => Math.round(v * scale)
  const rects = []
  const m = k(300)
  const t = Math.max(2, k(24))
  rects.push([m, m, W - 2 * m, t], [m, H - m - t, W - 2 * m, t], [m, m, t, H - 2 * m], [W - m - t, m, t, H - 2 * m])
  rects.push([m + k(400), m + k(400), k(3200), k(360)]) // 标题块
  const right = W - m - k(400)
  for (let i = 0, y = m + k(1200); y < H - m - k(600); i++, y += k(260)) {
    const len = Math.min(right - m - k(400), k(5000 + ((i * 7919) % 3600)))
    for (let x = m + k(400); x < m + k(400) + len; x += k(520 + ((i * 131 + x) % 260))) rects.push([x, y, k(420), k(90)])
  }
  return rects
}

/** 按同样版式画的封面（JPEG，长边 400）：rects 画成深灰。 */
function rectsCover(W, H, rects) {
  const cs = coverSize(W, H)
  const rgba = Buffer.alloc(cs.width * cs.height * 4, 255)
  const sx = cs.width / W
  const sy = cs.height / H
  for (const [x0, y0, w, h] of rects) {
    const cx0 = Math.floor(x0 * sx)
    const cy0 = Math.floor(y0 * sy)
    const cx1 = Math.max(cx0 + 1, Math.round((x0 + w) * sx))
    const cy1 = Math.max(cy0 + 1, Math.round((y0 + h) * sy))
    for (let y = cy0; y < cy1 && y < cs.height; y++) {
      for (let x = cx0; x < cx1 && x < cs.width; x++) {
        const o = (y * cs.width + x) * 4
        rgba[o] = rgba[o + 1] = rgba[o + 2] = 30
      }
    }
  }
  return Buffer.from(jpeg.encode({ data: rgba, width: cs.width, height: cs.height }, 80).data)
}

/**
 * 拼一个单 IFD 的 TIFF：entries 是 [tag, type, count, value]（type 3 是 SHORT、4 是 LONG、5 是 RATIONAL），
 * 值放不下的（258 的 3 个 BitsPerSample、273/279 的多个条带、282/283 的有理数）由这里放到 IFD 后面；strips 是各条带的数据。
 */
function buildTiff(entries, strips, dpi) {
  const all = [...entries, [273, 4, strips.length, 0], [279, 4, strips.length, 0], [282, 5, 1, 0], [283, 5, 1, 0]].sort((x, y) => x[0] - y[0])
  const ifdSize = 2 + all.length * 12 + 4
  let extraAt = 8 + ifdSize
  const extra = new Map() // tag → 偏移
  for (const [tag, type, count] of all) {
    const size = count * (type === 3 ? 2 : type === 5 ? 8 : 4)
    if (size <= 4) continue
    extra.set(tag, extraAt)
    extraAt += size + (size % 2)
  }
  let dataAt = extraAt
  const head = Buffer.alloc(dataAt)
  head.write('II', 0, 'latin1')
  head.writeUInt16LE(42, 2)
  head.writeUInt32LE(8, 4)
  head.writeUInt16LE(all.length, 8)
  const offsets = []
  for (const st of strips) {
    offsets.push(dataAt)
    dataAt += st.length
  }
  all.forEach(([tag, type, count, value], i) => {
    const at = 10 + i * 12
    head.writeUInt16LE(tag, at)
    head.writeUInt16LE(type, at + 2)
    head.writeUInt32LE(count, at + 4)
    const values = tag === 273 ? offsets : tag === 279 ? strips.map((st) => st.length) : null
    const where = extra.get(tag)
    if (tag === 282 || tag === 283) {
      head.writeUInt32LE(dpi, where)
      head.writeUInt32LE(1, where + 4)
      head.writeUInt32LE(where, at + 8)
    } else if (where !== undefined) {
      const list = values ?? Array(count).fill(value)
      list.forEach((v, j) => (type === 3 ? head.writeUInt16LE(v, where + j * 2) : head.writeUInt32LE(v, where + j * 4)))
      head.writeUInt32LE(where, at + 8)
    } else {
      const v = values ? values[0] : value
      if (type === 3) head.writeUInt16LE(v, at + 8)
      else head.writeUInt32LE(v, at + 8)
    }
  })
  head.writeUInt32LE(0, 10 + all.length * 12)
  return Buffer.concat([head, ...strips])
}

/**
 * 1 位黑白无压缩 TIFF，A0 300 DPI（9933×14043，约 1.4 亿像素）：边框、标题块、几十行"正文"。外加按同样版式画的封面
 * （JPEG，长边 400）。
 */
export function bigBilevelTiff() {
  const W = 9933
  const H = 14043
  const rowBytes = Math.ceil(W / 8)
  const data = Buffer.alloc(rowBytes * H) // WhiteIsZero：0 是白
  const rects = pageRects(W, H)
  for (const [x0, y0, w, h] of rects) {
    for (let y = y0; y < y0 + h; y++) {
      const row = y * rowBytes
      for (let x = x0; x < x0 + w; x++) data[row + (x >> 3)] |= 0x80 >> (x & 7)
    }
  }
  const entries = [
    [256, 4, 1, W],
    [257, 4, 1, H],
    [258, 3, 1, 1],
    [259, 3, 1, 1],
    [262, 3, 1, 0],
    [277, 3, 1, 1],
    [278, 4, 1, H],
    [296, 3, 1, 2],
  ]
  return { tiff: buildTiff(entries, [data], 300), cover: rectsCover(W, H, rects) }
}

/**
 * 8 位无压缩 TIFF（channels 是 1：灰度 BlackIsZero，或 3：RGB），每 64 行一个条带，白底、按 dpi 缩放的版式（深灰），
 * 彩色的另有一块红色"印章"。外加封面。7016×9921 RGB（A3 600 DPI）约 209 MB。
 */
export function plainTiff(W, H, channels, dpi) {
  const rowBytes = W * channels
  const data = Buffer.alloc(rowBytes * H, 255)
  const rects = pageRects(W, H, dpi / 300)
  const paint = ([x0, y0, w, h], c) => {
    for (let y = Math.max(0, y0); y < Math.min(H, y0 + h); y++) {
      const row = y * rowBytes
      for (let x = Math.max(0, x0); x < Math.min(W, x0 + w); x++) {
        for (let k = 0; k < channels; k++) data[row + x * channels + k] = c[k]
      }
    }
  }
  for (const r of rects) paint(r, [50, 52, 58])
  if (channels === 3) {
    const s = Math.round(W / 8)
    paint([W - Math.round(W / 4), H - Math.round(H / 5), s, s], [200, 40, 40])
  }
  const rps = 64
  const strips = []
  for (let y = 0; y < H; y += rps) strips.push(data.subarray(y * rowBytes, Math.min(H, y + rps) * rowBytes))
  const entries = [
    [256, 4, 1, W],
    [257, 4, 1, H],
    [258, 3, channels, 8],
    [259, 3, 1, 1],
    [262, 3, 1, channels === 3 ? 2 : 1],
    [277, 3, 1, channels],
    [278, 4, 1, rps],
    [296, 3, 1, 2],
  ]
  return { tiff: buildTiff(entries, strips, dpi), cover: rectsCover(W, H, rects) }
}

// ---------- 页面 ----------

/** 页面现在的 origin（壳模式是 http://wails.localhost:<端口>，在页面里跳转要用它，不然就离开了壳模式）。 */
export function pageOrigin(page) {
  return new URL(page.url()).origin
}

/** 页面之外调接口用的 origin：壳模式的地址换回 127.0.0.1（同一个 devshell；Windows 上 node 不一定解析 *.localhost）。 */
export function apiOrigin(page) {
  const u = new URL(page.url())
  if (u.hostname === 'wails.localhost') u.hostname = '127.0.0.1'
  return u.origin
}

export async function login(page, origin, user, password) {
  await page.goto(`${origin}/login`, { waitUntil: 'networkidle0' })
  await page.type('#username', user)
  await page.type('#password', password)
  await Promise.all([
    page.waitForFunction(() => !location.pathname.startsWith('/login'), { timeout: 15000 }),
    page.click('[data-testid=login-submit]'),
  ])
}

export async function openFolder(page, origin, folderId) {
  await page.goto(`${origin}/files?folder=${folderId}`, { waitUntil: 'networkidle2' })
  await page.waitForSelector('[data-testid=files-page][data-list=ready]', { timeout: 15000 })
}

/** 右键文件 → 查看原图，等查看器出来。 */
export async function openViewer(page, fileName) {
  const handle = await page.evaluateHandle(
    (name) =>
      [...document.querySelectorAll('[data-testid=file-cell],[data-testid=file-row]')].find((el) =>
        [...el.querySelectorAll('[title]')].some((t) => t.getAttribute('title') === name),
      ),
    fileName,
  )
  const el = handle.asElement()
  assert(el, `页面上找不到文件「${fileName}」`)
  await el.click({ button: 'right' })
  await page.waitForSelector('[data-testid=context-menu] [data-testid=menu-view-original]', { visible: true, timeout: 5000 })
  await sleep(150)
  await page.click('[data-testid=context-menu] [data-testid=menu-view-original]')
  await page.waitForSelector('[data-testid=original-viewer] [data-testid=image-viewer]', { timeout: 10000 })
}

/** 等查看器（第 index 个）的状态是 states 之一，返回状态和各个 data 属性。 */
export async function waitViewer(page, states = ['ready'], { timeout = 60000, index = 0 } = {}) {
  await page.waitForFunction(
    (st, i) => {
      const el = document.querySelectorAll('[data-testid=image-viewer]')[i]
      return !!el && st.includes(el.dataset.state)
    },
    { timeout, polling: 50 },
    states,
    index,
  )
  return viewerData(page, index)
}

export function viewerData(page, index = 0) {
  return page.evaluate((i) => ({ ...document.querySelectorAll('[data-testid=image-viewer]')[i].dataset }), index)
}

/**
 * 在适合窗口的状态下，把鼠标移到页面上的 (fx, fy)（相对页面宽高的比例，竖版 A4），滚轮放大 steps 次。
 * `area` 是查看器（或对比的一侧）所在的元素选择器。
 */
export async function zoomAtPage(page, area, fx, fy, steps, aspect = 210 / 297) {
  const box = await (await page.$(area)).boundingBox()
  let w = box.height * aspect
  let h = box.height
  if (w > box.width) {
    w = box.width
    h = box.width / aspect
  }
  const x = box.x + (box.width - w) / 2 + fx * w
  const y = box.y + (box.height - h) / 2 + fy * h
  await page.mouse.move(x, y)
  for (let i = 0; i < steps; i++) {
    await page.mouse.wheel({ deltaY: -120 })
    await sleep(70)
  }
}

/** 在查看器（area）中间按住拖动 (dx, dy)，分几次拖（每次不超过查看器的一半），用来平移到页面的某个地方。 */
export async function dragBy(page, area, dx, dy) {
  const box = await (await page.$(area)).boundingBox()
  const cx = box.x + box.width / 2
  const cy = box.y + box.height / 2
  const n = Math.max(1, Math.ceil(Math.max(Math.abs(dx) / (box.width / 2), Math.abs(dy) / (box.height / 2))))
  for (let i = 0; i < n; i++) {
    await page.mouse.move(cx, cy)
    await page.mouse.down()
    await page.mouse.move(cx + dx / n, cy + dy / n, { steps: 8 })
    await page.mouse.up()
    await sleep(80)
  }
}

/** 等查看器的动画停下来（缩放比例连续 300 毫秒不变）。 */
export async function settle(page, index = 0) {
  let last = null
  let same = 0
  for (let i = 0; i < 100 && same < 6; i++) {
    const z = await page.evaluate((k) => document.querySelectorAll('[data-testid=image-viewer]')[k]?.dataset.zoom, index)
    same = z === last ? same + 1 : 0
    last = z
    await sleep(50)
  }
  await sleep(200)
}

// ---------- 原图请求的计数（浏览器：S3 上的原图。壳里在页面里统计，见 installTauriMock 的 __shellImgStats） ----------

/** S3 预签名的原图（不含封面）。 */
export function isOriginalUrl(url) {
  return /\/v\d+\.(jpg|png|tif)\?/.test(url) && !url.includes('.cover.')
}

/**
 * 跟踪"正在加载的原图"（浏览器里 S3 上原图的 GET，按 keyOf 分组）。返回 {maxConcurrent, started, finished, failed,
 * terminated, current()}：maxConcurrent 是同时在加载的不同原图的最大个数。
 */
export function trackOriginals(page, keyOf) {
  const inflight = new Map() // request → key
  const state = { maxConcurrent: 0, failed: [], finished: [], started: [], terminated: [] }
  const distinct = () => new Set(inflight.values()).size
  page.on('request', (req) => {
    const key = keyOf(req.url())
    if (!key) return
    inflight.set(req, key)
    state.started.push({ key, url: req.url(), at: Date.now() })
    state.maxConcurrent = Math.max(state.maxConcurrent, distinct())
  })
  const end = (list) => (req) => {
    const key = inflight.get(req)
    if (!key) return
    inflight.delete(req)
    list.push({ key, url: req.url(), at: Date.now(), error: req.failure()?.errorText })
  }
  page.on('requestfinished', end(state.finished))
  page.on('requestfailed', end(state.failed))
  // Worker 被 terminate 时，DevTools 收不到它的请求的 loadingFailed（实际已取消，见 netlogCancelled）：
  // Worker 发出的请求（frame() 为 null）随 Worker 一起结束。
  page.on('workerdestroyed', () => {
    for (const [req, key] of inflight) {
      if (req.frame() !== null) continue
      inflight.delete(req)
      state.terminated.push({ key, url: req.url(), at: Date.now() })
    }
  })
  state.current = () => [...new Set(inflight.values())]
  return state
}

/** Worker 的创建和销毁（页面的 workercreated、workerdestroyed）。 */
export function trackWorkers(page) {
  const state = { live: 0, maxLive: 0, created: 0, destroyed: 0, log: [] }
  page.on('workercreated', (w) => {
    if (!w.url().includes('tiff.worker')) return
    state.created++
    state.live++
    state.maxLive = Math.max(state.maxLive, state.live)
    state.log.push({ at: Date.now(), event: 'created' })
  })
  page.on('workerdestroyed', (w) => {
    if (!w.url().includes('tiff.worker')) return
    state.destroyed++
    state.live--
    state.log.push({ at: Date.now(), event: 'destroyed' })
  })
  return state
}

// ---------- 壳的模拟 ----------

/**
 * 在页面里执行（会被序列化）：包一层 fetch，统计页面里在途的取图请求（dzi、tile，按快照分组）——同一时间在加载的
 * 原图个数、被取消的请求（window.__shellImgStats）。
 */
function trackShellImages() {
  const inflight = new Map()
  const imgStats = { max: 0, aborted: [] }
  window.__shellImgStats = imgStats
  const prevFetch = window.fetch
  window.fetch = (input, init) => {
    const url = new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url, location.href)
    const m = url.origin === location.origin && url.pathname.match(/^\/shell\/img\/(dzi|tile)\/[^/]+\/([^/]+)/)
    if (!m) return prevFetch(input, init)
    const sid = m[2]
    inflight.set(sid, (inflight.get(sid) ?? 0) + 1)
    imgStats.max = Math.max(imgStats.max, [...inflight.values()].filter((n) => n > 0).length)
    let ended = false
    const done = (aborted) => {
      if (ended) return
      ended = true
      inflight.set(sid, inflight.get(sid) - 1)
      if (aborted) imgStats.aborted.push({ sid, kind: m[1] })
    }
    // 页面一取消（signal abort，同步发生）就算结束，不等 fetch 的 Promise 之后才 reject。
    init?.signal?.addEventListener('abort', () => done(true), { once: true })
    return prevFetch(input, init).then(
      (r) => {
        done(false)
        return r
      },
      (e) => {
        done(e?.name === 'AbortError')
        throw e
      },
    )
  }
}

/**
 * 壳模式（页面要用 shellOrigin 的地址打开）：Wails 桥的模拟 + 统计页面里的取图请求。取图地址（/shell/img/）由
 * devshell 用客户端的 Go 代码回答（下载、本机缓存、切块，和打包的壳同一份）；这里只拦截它们做统计和故障注入：
 * - `fail`：file_id → {code, message}，这些文件的分块描述回 422（例如 too_large）；
 * - `dziDelayMs`：每次分块描述都晚这么久才放行（模拟第一次打开要等壳下载原图、切块）；
 * - `stats.tileFail(sid, level, name)` 返回非 0 时这一块回那个状态码（模拟壳取块失败，例如 503）。
 * 返回统计：每个快照请求过的块（按级号、坐标去重）、每块被请求的次数、分块描述的请求次数和内容。
 * cacheDir 不再用（缓存在 devshell 的 -cache 目录里），留着参数是为了调用处不用改。
 */
export async function installShellMock(page, { fail = new Map(), dziDelayMs = 0 } = {}) {
  await installWailsMock(page, { serverUrl: 'http://127.0.0.1:9080', dataDir: 'C:\\Users\\review01\\AppData\\Local\\Doclite' })
  await page.evaluateOnNewDocument(trackShellImages)
  await page.setRequestInterception(true)

  // tileFail：(sid, level, name) → 状态码（0 正常回答）；tileAttempts：每一块（sid/级/名字）被请求的次数（含查看器的自动重试）。
  const stats = { tiles: new Map(), dzi: new Map(), descriptors: new Map(), tileFail: null, tileAttempts: new Map() }

  page.on('response', async (res) => {
    const u = new URL(res.url())
    const m = u.pathname.match(/^\/shell\/img\/dzi\/[^/]+\/([^/]+)$/)
    if (!m || res.status() !== 200) return
    try {
      stats.descriptors.set(m[1], await res.json())
    } catch {
      // 页面取消了请求、响应体读不到，就不记。
    }
  })

  const json = (status, body, extra = {}) => ({
    status,
    contentType: 'application/json; charset=utf-8',
    headers: { 'Cache-Control': 'no-store', ...extra },
    body: JSON.stringify(body),
  })

  async function handle(req) {
    const u = new URL(req.url())
    const [kind, fid, sid, level, name] = u.pathname.slice('/shell/img/'.length).split('/')
    if (kind === 'dzi') {
      stats.dzi.set(sid, (stats.dzi.get(sid) ?? 0) + 1)
      if (dziDelayMs) await sleep(dziDelayMs)
      const f = fail.get(fid)
      if (f) return req.respond(json(422, { code: f.code, message: f.message }, { 'X-Doclite-Error': f.code }))
    } else if (kind === 'tile') {
      const set = stats.tiles.get(sid) ?? new Set()
      stats.tiles.set(sid, set)
      set.add(`${level}/${name}`)
      const key = `${sid}/${level}/${name}`
      stats.tileAttempts.set(key, (stats.tileAttempts.get(key) ?? 0) + 1)
      const failStatus = stats.tileFail?.(sid, Number(level), name) ?? 0
      if (failStatus) {
        return req.respond({
          status: failStatus,
          contentType: 'application/json; charset=utf-8',
          headers: { 'Cache-Control': 'no-store', 'X-Doclite-Error': 'unavailable' },
          body: JSON.stringify({ code: 'unavailable', message: '本机图片缓存暂时不可用（截图模拟）' }),
        })
      }
    }
    return req.continue()
  }

  page.on('request', (req) => {
    let u
    try {
      u = new URL(req.url())
    } catch {
      req.continue().catch(() => {})
      return
    }
    if (!u.pathname.startsWith('/shell/img/')) {
      req.continue().catch(() => {})
      return
    }
    handle(req).catch(() => req.continue().catch(() => {}))
  })
  return stats
}

/** 浏览器里"正在加载的原图"的键：S3 原图的路径。 */
export function browserOriginalKey(url) {
  return isOriginalUrl(url) ? new URL(url).pathname : null
}

// ---------- NetLog：核对请求真的被取消了 ----------

/**
 * 用一个单独的、开着 NetLog 的无头 Chrome 跑 `fn(page)`（DevTools 看不到被 terminate 的 Worker 的请求结束，
 * NetLog 是网络栈自己的记录），关掉浏览器后读 NetLog：返回 URL 里含 `match` 的请求 [{url, bytes, cancelled, netError}]。
 */
export async function withNetLog(executablePath, dir, fn, match) {
  const { default: puppeteer } = await import('puppeteer-core')
  mkdirSync(dir, { recursive: true })
  const file = path.join(dir, `netlog-${Date.now()}.json`)
  const browser = await puppeteer.launch({
    executablePath,
    headless: true,
    args: ['--no-proxy-server', '--lang=zh-CN', '--hide-scrollbars', `--log-net-log=${file}`],
    defaultViewport: { width: 1440, height: 900 },
  })
  try {
    await fn(await browser.newPage())
  } finally {
    await browser.close()
  }
  let text = readFileSync(file, 'utf8').trimEnd()
  if (!text.endsWith('}')) text = `${text.replace(/,$/, '')}]}`
  const log = JSON.parse(text)
  const names = Object.fromEntries(Object.entries(log.constants.logEventTypes).map(([k, v]) => [v, k]))
  const reqs = new Map()
  for (const e of log.events) {
    const name = names[e.type]
    if (name === 'REQUEST_ALIVE' && e.phase === 1 && typeof e.params?.url === 'string' && e.params.url.includes(match)) {
      reqs.set(e.source.id, { url: e.params.url, bytes: 0, cancelled: false, netError: null })
    }
    const r = reqs.get(e.source.id)
    if (!r) continue
    if (name === 'URL_REQUEST_JOB_FILTERED_BYTES_READ') r.bytes += e.params?.byte_count ?? 0
    if (name === 'CANCELLED') r.cancelled = true
    if (name === 'REQUEST_ALIVE' && e.phase === 2 && e.params?.net_error !== undefined) r.netError = e.params.net_error
  }
  return [...reqs.values()]
}

// ---------- 内存 ----------

/**
 * 每隔 intervalMs 用 ps 取这个浏览器的 renderer、GPU 进程的 RSS，返回 {stop()}。stop() 返回（MB）：
 * renderer 这段时间里涨得最多的那个 renderer 进程（页面和它的 Worker 在同一个 renderer 进程里；别的上下文的
 * renderer 闲着，不涨）的峰值、开始时的值、结束时的值；gpu 是 GPU 进程的峰值。
 */
export function sampleMemory(browser, intervalMs = 100) {
  const root = browser.process()?.pid
  assert(root, '取不到浏览器进程')
  const renderers = new Map() // pid → {base, peak, last}
  let gpuPeak = 0
  const tick = () => {
    let out = ''
    try {
      out = execFileSync('ps', ['-A', '-o', 'pid=,ppid=,rss=,command='], { encoding: 'utf8', maxBuffer: 16 << 20 })
    } catch {
      return
    }
    const procs = out
      .split('\n')
      .map((l) => l.trim().match(/^(\d+)\s+(\d+)\s+(\d+)\s+(.*)$/))
      .filter(Boolean)
      .map((m) => ({ pid: +m[1], ppid: +m[2], rss: +m[3] / 1024, cmd: m[4] }))
    let gpu = 0
    for (const p of procs) {
      if (p.ppid !== root) continue
      if (p.cmd.includes('--type=gpu-process')) gpu += p.rss
      if (!p.cmd.includes('--type=renderer')) continue
      const r = renderers.get(p.pid) ?? { base: p.rss, peak: 0, last: 0 }
      r.peak = Math.max(r.peak, p.rss)
      r.last = p.rss
      renderers.set(p.pid, r)
    }
    gpuPeak = Math.max(gpuPeak, gpu)
  }
  tick()
  const timer = setInterval(tick, intervalMs)
  return {
    stop() {
      clearInterval(timer)
      tick()
      const best = [...renderers.values()].sort((a, b) => b.peak - b.base - (a.peak - a.base))[0] ?? { base: 0, peak: 0, last: 0 }
      const round = Math.round
      return { renderer: round(best.peak), rendererBase: round(best.base), rendererEnd: round(best.last), gpu: round(gpuPeak) }
    },
  }
}

/** 截图脚本的临时目录（壳模拟的原图和分块）。 */
export function shotsTmp(name) {
  const dir = path.join(process.env.SHOTS_TMP ?? os.tmpdir(), name)
  mkdirSync(dir, { recursive: true })
  return dir
}

// ---------- 共用内存预算的模拟（浏览器） ----------

/**
 * 现场生成一张 Deflate 压缩的 8 位 RGB TIFF（宽 × 高，每 64 行一个条带）：白底、四周的边框、一行行灰色的"字"。
 * 内容大多是空白，文件只有几 MB，解码却和同样大小的无压缩 TIFF 一样吃内存（Deflate 还要多解一份）。
 */
export function syntheticTiff(W, H, seed = 0) {
  const rps = 64
  const strips = Math.ceil(H / rps)
  const rowBytes = W * 3
  const cache = new Map()
  const stripData = []
  for (let i = 0; i < strips; i++) {
    const y0 = i * rps
    const rows = Math.min(rps, H - y0)
    // 一个条带里的"字"：每 4 个条带一行，长短按行号变。
    const line = i % 4 === 2 && y0 > 400 && y0 < H - 400 ? (i * 7919 + seed) % 5 : -1
    const edge = y0 < 300 || y0 + rows > H - 300 ? 'edge' : 'mid'
    const key = `${rows}/${line}/${edge}`
    let z = cache.get(key)
    if (!z) {
      const buf = Buffer.alloc(rows * rowBytes, 255)
      for (let r = 0; r < rows; r++) {
        const y = y0 + r
        const row = r * rowBytes
        const paint = (x0, x1, c) => {
          for (let x = Math.max(0, x0); x < Math.min(W, x1); x++) buf.set(c, row + x * 3)
        }
        if (y >= 200 && y < 230 && edge === 'edge') paint(200, W - 200, [40, 40, 40])
        if (y >= H - 230 && y < H - 200) paint(200, W - 200, [40, 40, 40])
        if (y >= 200 && y < H - 200) {
          paint(200, 230, [40, 40, 40])
          paint(W - 230, W - 200, [40, 40, 40])
        }
        if (line >= 0 && r >= 16 && r < 48) {
          for (let x = 500, k = 0; x < W - 500 - line * 400; x += 360, k++) paint(x, x + 260 + ((k * 37) % 60), [70, 70, 90])
        }
      }
      z = deflateSync(buf, { level: 1 })
      cache.set(key, z)
    }
    stripData.push(z)
  }
  // IFD：256、257、258（3 个，放在后面）、259=8、262=2、273、277=3、278、279、284=1。
  const entries = [
    [256, 4, 1, W],
    [257, 4, 1, H],
    [258, 3, 3, 0],
    [259, 3, 1, 8],
    [262, 3, 1, 2],
    [273, 4, strips, 0],
    [277, 3, 1, 3],
    [278, 4, 1, rps],
    [279, 4, strips, 0],
    [284, 3, 1, 1],
  ]
  const ifdSize = 2 + entries.length * 12 + 4
  const bpsAt = 8 + ifdSize
  const offsAt = bpsAt + 6 + (bpsAt % 2)
  const countsAt = offsAt + strips * 4
  let dataAt = countsAt + strips * 4
  const head = Buffer.alloc(dataAt)
  head.write('II', 0, 'latin1')
  head.writeUInt16LE(42, 2)
  head.writeUInt32LE(8, 4)
  head.writeUInt16LE(entries.length, 8)
  entries.forEach(([tag, type, count, value], i) => {
    const at = 10 + i * 12
    head.writeUInt16LE(tag, at)
    head.writeUInt16LE(type, at + 2)
    head.writeUInt32LE(count, at + 4)
    if (tag === 258) value = bpsAt
    if (tag === 273) value = strips === 1 ? dataAt : offsAt
    if (tag === 279) value = strips === 1 ? stripData[0].length : countsAt
    if (type === 3 && count === 1) head.writeUInt16LE(value, at + 8)
    else head.writeUInt32LE(value, at + 8)
  })
  head.writeUInt32LE(0, 10 + entries.length * 12)
  for (let k = 0; k < 3; k++) head.writeUInt16LE(8, bpsAt + k * 2)
  for (let i = 0; i < strips; i++) {
    head.writeUInt32LE(dataAt, offsAt + i * 4)
    head.writeUInt32LE(stripData[i].length, countsAt + i * 4)
    dataAt += stripData[i].length
  }
  return Buffer.concat([head, ...stripData])
}

/**
 * 浏览器里对比的两侧换成现场生成的大 TIFF：拦截 `GET /api/files/<fileId>/snapshots`（用 api 取真实的响应，把各个版本的
 * 原图换成 `sizes` 里对应的尺寸，封面不变），原图地址 `http://doclite-synthetic.test/<sid>.tif` 由这里回答（带 CORS 头）。
 * `sizes` 是函数（每次取 snapshots 时调，可以按场景换）→ [[左宽, 左高], [右宽, 右高]]。返回 {requests}：每张合成原图被
 * 请求的次数（sid → 次数）。页面要已经 setRequestInterception(true)（这里会打开）。
 */
export async function interceptCompareTiffs(page, api, fileId, sizes) {
  await page.setRequestInterception(true)
  const files = new Map() // sid → Buffer
  const requests = new Map()
  page.on('request', (req) => {
    const url = req.url()
    if (req.isInterceptResolutionHandled?.()) return
    if (new URL(url).pathname === `/api/files/${fileId}/snapshots`) {
      api('GET', `/api/files/${fileId}/snapshots`)
        .then((real) => {
          const want = sizes()
          const n = real.snapshots.length
          real.snapshots.forEach((s, i) => {
            const size = i === n - 1 ? want[1] : i === n - 2 ? want[0] : null
            if (!size) return
            const [W, H] = size
            const tiff = syntheticTiff(W, H, i)
            const sid = `${s.snapshot_id}`
            files.set(sid, tiff)
            Object.assign(s, {
              mime_type: 'image/tiff',
              width_px: W,
              height_px: H,
              file_size: tiff.length,
              color_mode: 'color',
              original_url: `http://doclite-synthetic.test/${sid}-${W}x${H}.tif?sig=1`,
            })
          })
          return req.respond({ status: 200, contentType: 'application/json; charset=utf-8', body: JSON.stringify(real) })
        })
        .catch((e) => req.respond({ status: 500, contentType: 'text/plain', body: String(e?.message ?? e) }).catch(() => {}))
      return
    }
    if (url.startsWith('http://doclite-synthetic.test/')) {
      const sid = new URL(url).pathname.slice(1).split('-').slice(0, 5).join('-')
      requests.set(sid, (requests.get(sid) ?? 0) + 1)
      const body = files.get(sid)
      req
        .respond(
          body
            ? { status: 200, contentType: 'image/tiff', headers: { 'Access-Control-Allow-Origin': '*' }, body }
            : { status: 404, contentType: 'text/plain', headers: { 'Access-Control-Allow-Origin': '*' }, body: 'no such synthetic tiff' },
        )
        .catch(() => {})
      return
    }
    req.continue().catch(() => {})
  })
  return { requests }
}

/** 记下页面上各个查看器经过的加载步骤（data-step）和状态（data-state），{side, step|state} 的列表在 window.__viewerSteps。 */
export async function recordViewerSteps(page) {
  await page.evaluate(() => {
    const log = []
    window.__viewerSteps = log
    const seen = (el) => {
      const side = el.closest('[data-testid=compare-pane]')?.dataset.side ?? 'single'
      log.push({ side, step: el.dataset.step ?? null, state: el.dataset.state, at: Math.round(performance.now()) })
    }
    new MutationObserver((ms) => {
      for (const m of ms) {
        const el = m.target
        if (el instanceof HTMLElement && el.dataset.testid === 'image-viewer') seen(el)
      }
    }).observe(document.body, { subtree: true, attributes: true, attributeFilter: ['data-step', 'data-state'] })
  })
}
