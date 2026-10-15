#!/usr/bin/env node
// 演示数据：往某个项目的某个文件夹里上传 N 张合成的扫描页（M1-10）。
//
//   npm run seed:files -- --project DEMO-A --folder "2025 文书档案/盒 WS-2025-001" --count 2000 \
//     [--low-dpi K] [--no-cover K] [--remark 原件位置] [--prefix IMG_] [--start 1] \
//     [--base http://127.0.0.1:9080] [--user 登录名:密码]
//
// - --project：项目编码或 id（必须是这个账号能进的项目）。
// - --folder：从项目根文件夹起的路径，用 / 分隔；不存在的各层用 `POST /api/folders`（if_exists: return）逐层建好。
//   不传表示根文件夹。--remark 写到最后一层（原件位置，面包屑旁显示）；已有的文件夹备注不同时用 PATCH 改。
// - 文件名 `<prefix><页码 4 位>.png`，页码从 --start 起。按页码顺序逐个上传，所以 sort_no 和文件名顺序一致。
// - 原图是 PNG（Node 自带的 zlib 生成），A4、200 DPI（pHYs），每 10 页一页彩色；内容由种子决定，各不相同。
//   种子是"项目编码/文件夹路径#页码"，--prefix 不是默认的 IMG_ 时再加上前缀（不同前缀传进同一个项目不会撞内容；
//   默认前缀不进种子，和 M1-10 已经造好的演示数据保持一致）。
//   --low-dpi K：其中 K 页按 150 DPI 生成（像素也按 150 DPI 算），入库时带 low_dpi 提示。
// - 封面由客户端生成（design §4.3）：用 jpeg-js 按封面尺寸（长边 400、长宽比和原图一致）直接画一张缩小的
//   同一页面，一起上传。--no-cover K：其中 K 页不带封面（列表里显示"无封面"占位）。
// - 哪几页低 DPI、哪几页无封面，只看这一页从 --start 数起是第几页（第 k 页，k 从 0 起），不看 --count：
//   低 DPI 是 k = 1、4、7…的前 K 个；无封面是 k = 2、6、10…里避开低 DPI 的前 K 个。页数不够放下 K 个时只放得下的几个，
//   并给出提示。所以加大 --count 续传时，前面已有的页不变。
// - 经 `POST /api/folders/{id}/files`（M1-7 的 multipart：file_name、content_sha256、cover、file）入库。
// - 可以重跑：先列出文件夹里已有的文件，已有同名文件（不区分大小写）的页直接跳过，不生成、不上传；
//   所以改了 --low-dpi、--no-cover 再跑，已有的页也不会变成改名的重复页（要重造就换个文件夹）。
//   上一次传到一半断开的那一页，server 会按"之前已上传过"（同名同内容，200）处理。
//
// 账号：--user，或环境变量 INIT_ADMIN_USERNAME / INIT_ADMIN_PASSWORD，默认 admin / change-me-now（同 seed:demo）。
// 地址：--base，或环境变量 DOCLITE_BASE，默认 http://127.0.0.1:9080（server 本身，不经过 vite）。

import { createHash } from 'node:crypto'
import { parseArgs } from 'node:util'

import { makeScanPage } from './lib/scan-pages.mjs'

const { values: args } = parseArgs({
  options: {
    base: { type: 'string', default: process.env.DOCLITE_BASE ?? 'http://127.0.0.1:9080' },
    user: {
      type: 'string',
      default: `${process.env.INIT_ADMIN_USERNAME ?? 'admin'}:${process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'}`,
    },
    project: { type: 'string' },
    folder: { type: 'string', default: '' },
    remark: { type: 'string' },
    count: { type: 'string', default: '10' },
    'low-dpi': { type: 'string', default: '0' },
    'no-cover': { type: 'string', default: '0' },
    prefix: { type: 'string', default: 'IMG_' },
    start: { type: 'string', default: '1' },
  },
})

function fail(msg) {
  console.error(msg)
  process.exit(2)
}

if (!args.project) fail('缺少 --project（项目编码或 id），例如：npm run seed:files -- --project DEMO-A --folder 盒1 --count 20')
const count = Number(args.count)
const lowDpi = Number(args['low-dpi'])
const noCover = Number(args['no-cover'])
const start = Number(args.start)
for (const [name, v] of [['count', count], ['low-dpi', lowDpi], ['no-cover', noCover], ['start', start]]) {
  if (!Number.isInteger(v) || v < 0) fail(`--${name} 要是非负整数`)
}
if (lowDpi + noCover > count) fail('--low-dpi 和 --no-cover 加起来不能超过 --count')

const base = args.base.replace(/\/$/, '')
let cookie = ''

async function api(method, path, body, { allow = [] } = {}) {
  const res = await fetch(base + path, {
    method,
    headers: {
      Accept: 'application/json',
      ...(body !== undefined && !(body instanceof FormData) ? { 'Content-Type': 'application/json' } : {}),
      ...(cookie ? { Cookie: cookie } : {}),
    },
    body: body === undefined ? undefined : body instanceof FormData ? body : JSON.stringify(body),
  })
  const setCookie = res.headers.get('set-cookie')
  if (setCookie) cookie = setCookie.split(';')[0]
  const text = await res.text()
  const data = text ? JSON.parse(text) : undefined
  if (!res.ok && !allow.includes(res.status)) {
    const err = new Error(`${method} ${path} → ${res.status} ${data?.code}: ${data?.message}`)
    err.status = res.status
    err.body = data
    throw err
  }
  return { status: res.status, data }
}

// ---------- 登录、项目、文件夹 ----------

const [username, ...pw] = args.user.split(':')
await api('POST', '/api/auth/login', { username, password: pw.join(':') })

const { data: projects } = await api('GET', '/api/projects')
const project = projects.find((p) => p.project_code === args.project || p.id === args.project)
if (!project) fail(`账号 ${username} 进不了项目 ${args.project}（能进的：${projects.map((p) => p.project_code).join('、') || '无'}）`)

const segments = args.folder.split('/').map((s) => s.trim()).filter(Boolean)
let folderId = project.root_folder_id
let created = false
for (let i = 0; i < segments.length; i++) {
  const last = i === segments.length - 1
  const { status, data } = await api('POST', '/api/folders', {
    parent_id: folderId,
    folder_name: segments[i],
    ...(last && args.remark ? { remark: args.remark } : {}),
    if_exists: 'return',
  })
  folderId = data.id
  created = status === 201
  if (last && args.remark !== undefined && !created && (data.remark ?? '') !== args.remark) {
    await api('PATCH', `/api/folders/${folderId}`, { remark: args.remark })
  }
}
const folderPath = `/${segments.join('/')}`
console.log(`项目 ${project.project_code}「${project.project_name}」 文件夹 ${folderPath}${created ? '（新建）' : ''}  id=${folderId}`)

// ---------- 哪些页低 DPI、哪些页不带封面 ----------

/**
 * 从第 first 页（从 --start 数起，0 起）起每隔 step 取一个（跳过 taken 里的），取够 n 个为止。
 * 结果只和 n、first、step、taken 有关，和 --count 无关（--count 只决定取到哪一页为止）。
 */
function pick(n, first, step, taken) {
  const out = new Set()
  for (let k = first; out.size < n && k < count; k += step) if (!taken.has(k)) out.add(k)
  return out
}
const lowSet = pick(lowDpi, 1, 3, new Set()) // 第 2、5、8… 页
const noCoverSet = pick(noCover, 2, 4, lowSet) // 第 3、7、11… 页
if (lowSet.size < lowDpi) console.warn(`提示：${count} 页里只放得下 ${lowSet.size} 张低 DPI 页（第 2、5、8… 页）`)
if (noCoverSet.size < noCover) console.warn(`提示：${count} 页里只放得下 ${noCoverSet.size} 张无封面页（第 3、7、11… 页）`)

/** 种子：默认前缀不进种子（和已经造好的演示数据一致），别的前缀要进，免得不同前缀的页内容相同。 */
const seedBase = `${project.project_code}${folderPath}${args.prefix === 'IMG_' ? '' : `|${args.prefix}`}`

// 文件夹里已有的文件名（不区分大小写，和 server 判重名一致）：已有的页跳过。
const { data: existingChildren } = await api('GET', `/api/folders/${folderId}/children`)
const existingNames = new Set(existingChildren.files.map((f) => f.file_name.toLowerCase()))

// ---------- 上传 ----------

const stats = { created: 0, skipped: 0, existed: 0, duplicate: 0, failed: 0, lowDpi: 0, noCover: 0, bytes: 0 }
const startedAt = Date.now()
let lastReport = startedAt

for (let i = 0; i < count; i++) {
  const pageNo = start + i
  const fileName = `${args.prefix}${String(pageNo).padStart(4, '0')}.png`
  if (existingNames.has(fileName.toLowerCase())) {
    stats.skipped++
    continue
  }
  const low = lowSet.has(i)
  const withCover = !noCoverSet.has(i)
  const page = makeScanPage({
    seed: `${seedBase}#${pageNo}`,
    pageNo,
    dpi: low ? 150 : 200,
  })
  const sha = createHash('sha256').update(page.png).digest('hex')

  const form = new FormData()
  form.append('file_name', fileName)
  form.append('content_sha256', sha)
  if (withCover) form.append('cover', new Blob([page.cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([page.png], { type: 'image/png' }), 'page.png')

  try {
    const { status, data } = await api('POST', `/api/folders/${folderId}/files`, form, { allow: [409] })
    if (status === 201) {
      stats.created++
      stats.bytes += page.png.length
      if (data.warnings.includes('low_dpi')) stats.lowDpi++
      if (!data.has_cover) stats.noCover++
    } else if (status === 200) {
      stats.existed++
    } else {
      stats.duplicate++
      console.warn(`  ${fileName}：内容重复（已有 ${data?.details?.folder_path}/${data?.details?.file_name}），跳过`)
    }
  } catch (e) {
    stats.failed++
    console.error(`  ${fileName}：${e.message}`)
    if (stats.failed >= 5 && stats.created === 0) fail('连续失败，停止')
  }

  const now = Date.now()
  if (now - lastReport > 5000 || i === count - 1) {
    lastReport = now
    const rate = ((i + 1) / ((now - startedAt) / 1000)).toFixed(1)
    console.log(`  ${i + 1}/${count}  ${rate} 页/秒`)
  }
}

const secs = ((Date.now() - startedAt) / 1000).toFixed(1)
console.log(
  `完成：新入库 ${stats.created}（其中低 DPI ${stats.lowDpi}、无封面 ${stats.noCover}，原图共 ${(stats.bytes / 1048576).toFixed(1)} MiB），` +
    `已有同名文件跳过 ${stats.skipped}，之前已上传过 ${stats.existed}，内容重复 ${stats.duplicate}，失败 ${stats.failed}；` +
    `用时 ${secs} 秒`,
)
process.exit(stats.failed ? 1 : 0)
