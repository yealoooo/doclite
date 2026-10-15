// M2-7 的截图清单和断言：待重扫列表（/scan 侧栏）和上传重扫件（/scan、/files 的 RescanDialog）。
//
//   INIT_ADMIN_PASSWORD=admin123456 \
//     npm run screenshots -- --spec scripts/shots/m2-rescan.mjs --base <devshell 的地址>
//
// 前端地址是 devshell（client/cmd/devshell：/api 转给 server，壳模式那张要它的取图地址），浏览器的几张也可以用
// vite preview 或托管了 web/dist 的 server。要先有：
// - DEMO-A 下的演示文件夹「M2-7 重扫演示」（盒 003 6 页、盒 001 4 页，seed:files 建的；分给 demo_reviewer，驳回了 5 页），
//   见 roadmap M2-7 的须知。每张截图在它下面建一个「截图验证 M2-7 … <时间>」临时文件夹、用接口上传几页，分给
//   demo_reviewer 并驳回几页，做完在 after 里删掉（软删除；前面失败了也删），所以可以反复跑，不动演示文件夹里原有的页；
// - demo_scanner（重扫）、demo_manager（分配）、demo_reviewer（驳回）能进 DEMO-A（密码 demo123456，可用 DEMO_PASSWORD 覆盖）。
//
// 截图和断言（失败时这一张算失败，脚本退出码 1）：
// - m2-rescan-list-browser：浏览器里打开 /scan，右侧待重扫列表：角标等于接口的 total；临时文件夹的 2 页在列表里，带
//   原件位置、审核人、驳回原因（中文）和说明，封面都显示出来；提示"重扫件不要扫进热文件夹"。页面之外再驳回一页、重扫
//   一页，3 秒内列表和角标跟着变（SSE）。
// - m2-rescan-shell-done（另存 m2-rescan-list-shell.png）：壳模式（壳模式的地址 + Wails 桥的模拟，scripts/lib/wails-mock.mjs；
//   封面经 devshell 的取图地址，即客户端的本机缓存代码）：/scan 左边是热文件夹和上传队列，右边是同样的待重扫列表。点一页的"上传重扫件"→"选择重扫件…"，
//   页面调 `pick_and_rescan {fileId}`（模拟的命令像壳一样带 sha 和封面发 multipart 到真实 server，返回壳的结果），
//   对话框写"已重扫：现在是 v2，待审核（第 2 轮）""自动分给 演示审核员"，列表里这一页消失；接口核对 v2 快照、封面、
//   自动再分配和 rescan 日志。
// - m2-rescan-files-done（另存 m2-rescan-files-menu.png、m2-rescan-dialog-done.png）：/files 打开临时文件夹，"已驳回 2"
//   筛选后只剩 2 页；右键一页有"上传重扫件"；对话框补出驳回原因；注入一张新扫的 PNG（和选择框同一段代码）→ 成功；
//   关掉对话框后筛选里只剩 1 页，详情面板写"待审 第 2 轮"，日志第一条是"重扫 … 新版本 v2，第 2 轮审核，自动分给 …"；
//   接口核对 v2 快照（rescan、upload_name、带封面）、封面是长边 400 的 JPEG、file_reviews（驳回那一行 + 新的 open，
//   assigned_by 沿用驳回那一行）、rescan 日志。
// - m2-rescan-wrong-file（另存 m2-rescan-wrong-file-self.png、m2-rescan-already.png）：注入一张和同文件夹另一页相同的图 →
//   "可能选错了文件"并写出那一页的位置；再选它自己现在的那张 →"一模一样"；接口核对没有新快照、仍是已驳回。另一页在
//   页面之外先被重扫了，再在页面上传 →"已经不是已驳回了"（上一次已经成功），列表刷新。
// - m2-rescan-retries：拦截请求造 409 conflict（第一次）→ 页面自动再发一次并成功（2 个请求）；造 400 invalid_cover →
//   去掉封面重传一次并成功，对话框写"无封面（服务器没收封面…）"，batch 里 cover_url 为 null。
// - m2-rescan-local-checks（M2-7 审查第 1 轮；另存 m2-rescan-lookup.png）：浏览器里注入写了一半的 PNG →"选的文件不完整或
//   还在写入"，没有发重扫请求；扩展名是 .gif →"只能选 jpg…"，也没发；/files 打开对话框时驳回原因要翻页才找得到（拦截
//   rescans 把每页改成 1 个）→ 跟着 next_cursor 找到；rescans 里没有它 →"已经不在待重扫列表里"；rescans 出错 →"读不到
//   驳回原因"（中性说法，仍可上传）；上传期间（拦住响应 1.5 秒）派发 beforeunload 会被拦下，传完后不拦；成功后
//   "已自动分给审核人"之类的说明在没拿到驳回记录时用中性的说法。
//   壳模式那张（m2-rescan-shell-done）另断言 `rescan:progress` 事件：对话框依次显示核对文件、生成封面、"正在上传 n%"
//   （1–99 之间，另存 m2-rescan-shell-progress.png）、100%。
// - m2-rescan-e2e-browser（只在设置了 M27_E2E=1 时跑，不删数据）：浏览器里在 /scan 的列表上重扫演示文件夹
//   「M2-7 重扫演示/盒 003/IMG_0002.png」，接口核对 v2 快照、封面、自动再分配和 rescan 日志（给端到端核对用）。

import { createHash } from 'node:crypto'
import { mkdirSync } from 'node:fs'

import { makeScanPage } from '../lib/scan-pages.mjs'
import { installWailsMock, useShellSession } from '../lib/wails-mock.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'
const PROJECT = 'DEMO-A'
const DEMO_FOLDER = 'M2-7 重扫演示'
const SCANNER = 'demo_scanner'
const MANAGER = 'demo_manager'
const REV = 'demo_reviewer'
const IDLE = 'networkidle2'
// 另存的截图放在 --out（和清单的截图同一个目录；默认 docs/screenshots/m2）。
const OUT = (() => {
  const i = process.argv.indexOf('--out')
  const dir = i > 0 ? process.argv[i + 1] : '../docs/screenshots/m2'
  mkdirSync(dir, { recursive: true })
  return dir
})()
const TEMP_REMARK = '3 号库 A 架 14 层（M2-7 截图脚本临时建的，做完会删掉）'
const SERVER = 'http://127.0.0.1:9080'

const BASE = (() => {
  const i = process.argv.indexOf('--base')
  const v = i > 0 ? process.argv[i + 1] : (process.env.DOCLITE_WEB ?? 'http://127.0.0.1:5173')
  return v.replace(/\/$/, '')
})()

// ---------- 页面之外调接口 ----------

async function login(username, password) {
  let cookie = ''
  const call = async (method, path, body, { allow = [] } = {}) => {
    const res = await fetch(BASE + path, {
      method,
      headers: {
        Accept: 'application/json',
        ...(body !== undefined && !(body instanceof FormData) ? { 'Content-Type': 'application/json' } : {}),
        ...(cookie ? { Cookie: cookie } : {}),
      },
      body: body === undefined ? undefined : body instanceof FormData ? body : JSON.stringify(body),
    })
    const sc = res.headers.get('set-cookie')
    if (sc) cookie = sc.split(';')[0]
    const text = await res.text()
    const data = text ? JSON.parse(text) : undefined
    if (!res.ok && !allow.includes(res.status)) throw new Error(`${username}: ${method} ${path} → ${res.status} ${data?.code}: ${data?.message}`)
    return data
  }
  await call('POST', '/api/auth/login', { username, password })
  call.cookie = () => cookie
  return call
}

let ctxPromise = null
/** 登录几个账号，取 DEMO-A、演示文件夹和审核人（只做一次）。 */
function context() {
  ctxPromise ??= (async () => {
    const adminApi = await login(admin, adminPassword)
    const projects = await adminApi('GET', '/api/projects')
    const project = projects.find((p) => p.project_code === PROJECT)
    if (!project) throw new Error(`找不到项目 ${PROJECT}，先跑 seed:demo`)
    const root = await adminApi('GET', `/api/folders/${project.root_folder_id}/children`)
    const demoFolder = root.folders.find((f) => f.folder_name === DEMO_FOLDER)
    if (!demoFolder) throw new Error(`找不到 ${PROJECT}/${DEMO_FOLDER}，先按 roadmap M2-7 的须知建好`)
    const mgr = await login(MANAGER, demoPassword)
    const rev = await login(REV, demoPassword)
    const scanner = await login(SCANNER, demoPassword)
    const reviewers = await mgr('GET', `/api/projects/${project.id}/reviewers`)
    const r = reviewers.reviewers.find((x) => x.username === REV)
    if (!r) throw new Error(`${REV} 不在 ${PROJECT} 的可分配审核人里`)
    return { project, demoFolder, admin: adminApi, mgr, rev, scanner, revId: r.id, revName: r.display_name }
  })()
  return ctxPromise
}

function stamp() {
  return new Date().toISOString().replace(/[-:T]/g, '').slice(0, 14)
}

function sha256(buf) {
  return createHash('sha256').update(buf).digest('hex')
}

/** 一页合成的扫描件（PNG + 封面），内容由种子和页码决定。 */
function pageBytes(seed, pageNo) {
  const p = makeScanPage({ seed, pageNo })
  return { png: Buffer.from(p.png), cover: Buffer.from(p.cover) }
}

async function uploadPage(call, folderId, name, bytes) {
  const form = new FormData()
  form.append('file_name', name)
  form.append('cover', new Blob([bytes.cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([bytes.png], { type: 'image/png' }), 'page.png')
  return call('POST', `/api/folders/${folderId}/files`, form)
}

/** 页面之外重扫一页（像另一个扫描员）。 */
async function rescanPage(call, fileId, name, bytes) {
  const form = new FormData()
  form.append('file_name', name)
  form.append('cover', new Blob([bytes.cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([bytes.png], { type: 'image/png' }), 'page.png')
  return call('POST', `/api/files/${fileId}/rescan`, form)
}

const REJECTS = [
  { reasons: ['blur'], comment: null, text: '模糊' },
  { reasons: ['cut', 'other'], comment: '右边缺了一截，重扫时把纸往左放', text: '裁切过度、其他' },
  { reasons: ['orient'], comment: '倒置', text: '方向' },
  { reasons: ['stain'], comment: null, text: '污渍' },
]

/**
 * 在演示文件夹下建一个临时文件夹，上传 count 页（P_01.png …），全部分给 demo_reviewer，驳回 rejected 里的几页
 * （页码从 1 起，原因按 REJECTS 轮流）。返回 {id, name, seed, files: [{file_id, file_name, pageNo, bytes, reject}]}。
 */
async function makeTemp(ctx, label, count, rejected) {
  const name = `截图验证 M2-7 ${label} ${stamp()}`
  const top = await ctx.admin('POST', '/api/folders', { parent_id: ctx.demoFolder.id, folder_name: name, remark: TEMP_REMARK })
  const seed = `M2-7 ${label} ${stamp()} ${Math.random()}`
  const files = []
  try {
    for (let i = 1; i <= count; i++) {
      const bytes = pageBytes(seed, i)
      const fileName = `P_${String(i).padStart(2, '0')}.png`
      const r = await uploadPage(ctx.admin, top.id, fileName, bytes)
      files.push({ file_id: r.file_id, file_name: r.file_name, pageNo: i, bytes, reject: null })
    }
    const a = await ctx.mgr('POST', `/api/projects/${ctx.project.id}/review-assignments`, { reviewer_id: ctx.revId, folder_id: top.id })
    assert(a.assigned === count, `临时文件夹应该分配 ${count} 个，实际 ${a.assigned}`)
    rejected.forEach((pageNo, k) => {
      files[pageNo - 1].reject = REJECTS[k % REJECTS.length]
    })
    for (const f of files.filter((x) => x.reject)) {
      await ctx.rev('POST', `/api/files/${f.file_id}/review`, {
        result: 'rejected',
        reject_reasons: f.reject.reasons,
        ...(f.reject.comment ? { comment: f.reject.comment } : {}),
      })
    }
  } catch (e) {
    // 造数据中途失败（例如 S3 一时 502）：临时文件夹还没交给调用方，在这里删掉。
    await ctx.admin('DELETE', `/api/folders/${top.id}`, undefined, { allow: [404] }).catch(() => {})
    console.log(`  造数据失败，已删除临时文件夹「${name}」`)
    throw e
  }
  return { id: top.id, name, seed, files }
}

async function dropTemp(temp) {
  if (!temp || !ctxPromise) return
  const ctx = await ctxPromise
  await ctx.admin('DELETE', `/api/folders/${temp.id}`, undefined, { allow: [404] })
  console.log(`  已删除临时文件夹「${temp.name}」`)
}

function assert(cond, message) {
  if (!cond) throw new Error(`断言失败：${message}`)
}

/** 接口核对一次成功的重扫：快照、封面、file_reviews、rescan 日志。 */
async function verifyRescan(ctx, f, { version = 2, round = 2, uploadName, hasCover = true }) {
  const snaps = await ctx.scanner('GET', `/api/files/${f.file_id}/snapshots`)
  const last = snaps.snapshots[snaps.snapshots.length - 1]
  assert(snaps.snapshots.length === version, `${f.file_name} 应该有 ${version} 个快照，实际 ${snaps.snapshots.length}`)
  assert(last.version_no === version && last.snapshot_type === 'rescan', `最后一个快照应该是 v${version} rescan：v${last.version_no} ${last.snapshot_type}`)
  assert(last.upload_name === uploadName, `upload_name 应该是 ${uploadName}：${last.upload_name}`)
  assert(last.storage_key.endsWith(`/v${version}.png`), `原图 key：${last.storage_key}`)
  assert(hasCover ? !!last.cover_url : last.cover_url === null, `封面：${last.cover_url}`)
  let cover = null
  if (hasCover) {
    const res = await fetch(last.cover_url)
    const buf = Buffer.from(await res.arrayBuffer())
    assert(res.ok && buf[0] === 0xff && buf[1] === 0xd8, '封面应该是 JPEG')
    cover = jpegSize(buf)
    assert(cover && Math.max(cover.width, cover.height) === 400 && cover.height > cover.width, `封面应该是竖的、长边 400：${JSON.stringify(cover)}`)
  }
  const reviews = await ctx.mgr('GET', `/api/files/${f.file_id}/reviews`)
  assert(reviews.review_status === 'pending' && reviews.review_round === round, `状态 ${reviews.review_status}，第 ${reviews.review_round} 轮`)
  const rejectedRow = reviews.reviews.filter((r) => r.result === 'rejected').pop()
  const open = reviews.reviews.filter((r) => r.status === 'open')
  assert(open.length === 1 && open[0].reviewer_id === ctx.revId, `应该自动分回 ${ctx.revName}：${JSON.stringify(open)}`)
  assert(open[0].assigned_by === rejectedRow.assigned_by, 'assigned_by 应该沿用驳回那一行的')
  const logs = (await ctx.scanner('GET', `/api/files/${f.file_id}/logs`)).logs
  const log = logs[0]
  assert(log.action === 'rescan', `最新一条日志应该是 rescan：${log.action}`)
  assert(
    log.detail.version_no === version && log.detail.review_round === round && log.detail.reviewer === ctx.revName && log.detail.review_status === 'pending',
    `rescan 日志的 detail：${JSON.stringify(log.detail)}`,
  )
  assert(log.detail.upload_name === uploadName && log.detail.file_name === f.file_name, `rescan 日志的文件名：${JSON.stringify(log.detail)}`)
  return { snapshot: `v${last.version_no} ${last.snapshot_type}`, cover, reviewer: open[0].reviewer_name, log: log.action }
}

/** JPEG 的宽高（读 SOF）。 */
function jpegSize(buf) {
  let i = 2
  while (i + 9 < buf.length) {
    if (buf[i] !== 0xff) return null
    const m = buf[i + 1]
    const len = buf.readUInt16BE(i + 2)
    if (m >= 0xc0 && m <= 0xcf && m !== 0xc4 && m !== 0xc8 && m !== 0xcc) {
      return { height: buf.readUInt16BE(i + 5), width: buf.readUInt16BE(i + 7) }
    }
    i += 2 + len
  }
  return null
}

// ---------- 页面操作 ----------

async function waitCovers(page, scope = '', timeout = 15000) {
  await page.waitForFunction(
    (s) => [...document.querySelectorAll(`${s} [data-testid=file-cover]`)].every((c) => !['loading', 'loading-image'].includes(c.dataset.coverState)),
    { timeout },
    scope,
  )
}

async function waitList(page) {
  await page.waitForSelector('[data-testid=rescan-list][data-state=ready]', { timeout: 15000 })
}

/** 待重扫列表里的项（file_id → 文字）。 */
function listItems(page) {
  return page.$$eval('[data-testid=rescan-item]', (els) =>
    els.map((el) => ({
      file_id: el.dataset.fileId,
      name: el.querySelector('[data-testid=rescan-item-name]')?.textContent.trim(),
      remark: el.querySelector('[data-testid=rescan-item-remark]')?.textContent.replace(/\s+/g, ' ').trim(),
      reasons: el.querySelector('[data-testid=rescan-item-reasons]')?.textContent.trim(),
      comment: el.querySelector('[data-testid=rescan-item-comment]')?.textContent.trim() ?? null,
      cover: el.querySelector('[data-testid=file-cover]')?.dataset.coverState,
    })),
  )
}

async function waitTotal(page, n, timeout = 3000) {
  await page.waitForFunction((n) => document.querySelector('[data-testid=rescan-list]')?.dataset.total === String(n), { timeout }, n)
}

/** 把一张图当作在选择框里选中的文件注入对话框（和选择框同一段代码）。 */
async function injectRescan(page, name, png) {
  await page.waitForSelector('[data-testid=rescan-dialog] [data-testid=rescan-input]', { timeout: 5000 })
  await page.evaluate(
    (name, b64) => {
      const bin = atob(b64)
      const bytes = new Uint8Array(bin.length)
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
      const file = new File([bytes], name, { type: 'image/png' })
      document.querySelector('[data-testid=rescan-input]').dispatchEvent(new CustomEvent('doclite-inject-files', { detail: [file] }))
    },
    name,
    Buffer.from(png).toString('base64'),
  )
}

/** 等对话框出结果，返回 {result, title, location, reassign}。 */
async function dialogResult(page, timeout = 30000) {
  await page.waitForSelector('[data-testid=rescan-dialog] [data-testid=rescan-result]', { timeout })
  return page.$eval('[data-testid=rescan-dialog]', (d) => ({
    state: d.dataset.state,
    result: d.querySelector('[data-testid=rescan-result]')?.dataset.result,
    title: d.querySelector('[data-testid=rescan-result-title]')?.textContent.replace(/\s+/g, ' ').trim(),
    location: d.querySelector('[data-testid=rescan-duplicate-of]')?.textContent.replace(/\s+/g, ' ').trim() ?? null,
    reassign: d.querySelector('[data-testid=rescan-reassign]')?.textContent.trim() ?? null,
    text: d.textContent.replace(/\s+/g, ' '),
  }))
}

async function closeDialog(page) {
  await page.click('[data-testid=rescan-dialog] [data-testid=rescan-close]')
  await dialogGone(page)
}

/** 对话框关掉了（动画结束、body 的 pointer-events 恢复）。 */
async function dialogGone(page) {
  await page.waitForFunction(() => !document.querySelector('[data-testid=rescan-dialog]') && document.body.style.pointerEvents !== 'none', {
    timeout: 5000,
  })
  await new Promise((r) => setTimeout(r, 200))
}

/** 主区里名字为 name 的文件单元格。 */
async function fileCell(page, name) {
  const handle = await page.evaluateHandle(
    (name) =>
      [...document.querySelectorAll('[data-testid=file-cell],[data-testid=file-row]')].find((el) =>
        [...el.querySelectorAll('[title]')].some((t) => t.getAttribute('title') === name),
      ),
    name,
  )
  const el = handle.asElement()
  if (!el) throw new Error(`页面上找不到文件「${name}」`)
  return el
}

async function menu(page, testid) {
  await page.waitForSelector(`[data-testid=context-menu] [data-testid=${testid}]`, { visible: true, timeout: 5000 })
  await new Promise((r) => setTimeout(r, 150))
  await page.click(`[data-testid=context-menu] [data-testid=${testid}]`)
}

async function openFiles(page, folderId) {
  await page.goto(`${BASE}/files?folder=${folderId}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=files-page][data-list=ready][data-stream=open]', { timeout: 15000 })
  await waitCovers(page, '[data-testid=file-area]')
}

/** 打开某个文件的重扫对话框（/files 右键菜单），等驳回原因读出来。 */
async function openRescanFromFiles(page, f) {
  await (await fileCell(page, f.file_name)).click({ button: 'right' })
  await menu(page, 'menu-rescan')
  await page.waitForSelector(`[data-testid=rescan-dialog][data-file-id="${f.file_id}"] [data-testid=rescan-reasons]`, { timeout: 5000 })
}

/** 选择框挡不住对话框外的提示（toast），等它们消失再截图。 */
async function waitNoToast(page) {
  await page.waitForFunction(() => document.querySelectorAll('[data-sonner-toast]').length === 0, { timeout: 8000 }).catch(() => {})
}

// ---------- 壳模式：Wails 桥的模拟 ----------

/** 页面里的模拟（会被序列化，不能引用外部变量）。 */
function rescanShellMock(scenario) {
  const GB = 1024 ** 3
  const shell = window.__shell
  // 页面调过的壳命令（不含 api_fetch）。
  const state = {
    get calls() {
      return shell.calls.filter((c) => c.cmd !== 'api_fetch')
    },
  }
  const b64 = (s) => {
    const bin = atob(s)
    const bytes = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
    return bytes
  }
  window.__mock = { state }
  Object.assign(shell.commands, {
    cache_stats: () => ({
      dir: 'C:\\Users\\scan01\\AppData\\Local\\Doclite\\image-cache',
      available: true,
      error: null,
      total_bytes: Math.round(1.3 * GB),
      file_count: 15230,
      limit_bytes: 2 * GB,
      projects: [],
      counters: { hits: 0, misses: 0, downloads: 0, download_bytes: 0, rejected: 0, errors: 0 },
    }),
    upload_status: () => scenario.status,
    upload_recent: () => scenario.items,
    hotfolder_status: () => scenario.hotfolder,
    async pick_and_rescan(args) {
      // 像壳一样：（系统对话框选好了 scenario.pick）→ 带 sha 和封面，按 file_name、content_sha256、cover、file 的顺序
      // 发 multipart 到重扫接口（这里经 devshell 的 /api 到真实 server），同步返回壳的结果。
      const pick = scenario.pick
      if (!pick) return { cancelled: true, local_name: null, size: null, sha256: null, cover: null, attempts: 0, result: null, error: null, elapsed_ms: 0 }
      const started = performance.now()
      // 像壳一样发 rescan:progress（核对文件 → 生成封面 → 上传 0、一半……传完）。
      const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
      const progress = (phase, sent) => shell.emit('rescan:progress', { file_id: args.fileId, phase, sent, size: pick.size })
      progress('hashing', 0)
      await sleep(200)
      progress('cover', 0)
      await sleep(200)
      progress('uploading', 0)
      await sleep(100)
      progress('uploading', Math.floor(pick.size / 2))
      await sleep(scenario.uploadHold ?? 1200)
      const form = new FormData()
      form.append('file_name', pick.name)
      form.append('content_sha256', pick.sha256)
      form.append('cover', new Blob([b64(pick.cover)], { type: 'image/jpeg' }), 'cover.jpg')
      form.append('file', new Blob([b64(pick.png)], { type: 'application/octet-stream' }), 'file')
      const res = await fetch(`/api/files/${args.fileId}/rescan`, { method: 'POST', body: form })
      const body = await res.json()
      progress('uploading', pick.size)
      const ok = res.status === 201
      return {
        cancelled: false,
        local_name: pick.name,
        size: pick.size,
        sha256: pick.sha256,
        cover: { status: 'included', reason: null, ms: 38 },
        attempts: 1,
        result: ok ? body : null,
        error: ok ? null : { status: res.status, code: body.code, message: body.message, details: body.details ?? null },
        elapsed_ms: Math.round(performance.now() - started),
      }
    },
  })
}

// ---------- 截图 ----------

let listTemp = null
let shellTemp = null
let filesTemp = null
let wrongTemp = null
let retryTemp = null

const shots = [
  {
    name: 'm2-rescan-list-browser',
    user: SCANNER,
    path: '/scan',
    waitUntil: IDLE,
    waitFor: '[data-testid=rescan-list]',
    async prepare(page) {
      const ctx = await context()
      listTemp = await makeTemp(ctx, '列表', 4, [1, 2])
      await page.reload({ waitUntil: IDLE })
      await waitList(page)
      await page.waitForSelector('[data-testid=rescan-list][data-stream=open]', { timeout: 10000 })
      const api = await ctx.scanner('GET', `/api/projects/${ctx.project.id}/rescans?limit=500`)
      await waitTotal(page, api.total)
      const badge = await page.$eval('[data-testid=rescan-total]', (el) => el.textContent.trim())
      assert(badge === String(api.total), `角标 ${badge}，接口 total ${api.total}`)
      await waitCovers(page, '[data-testid=rescan-list]')
      let items = await listItems(page)
      assert(items.length === api.items.length, `列表 ${items.length} 项，接口 ${api.items.length} 项`)
      for (const f of listTemp.files.filter((x) => x.reject)) {
        const it = items.find((i) => i.file_id === f.file_id)
        assert(it, `列表里没有 ${f.file_name}`)
        assert(it.name === `${listTemp.name} / ${f.file_name}`, `名字：${it.name}`)
        assert(it.remark.includes(TEMP_REMARK) && it.remark.includes(ctx.revName), `原件位置和审核人：${it.remark}`)
        assert(it.reasons === `驳回：${f.reject.text}`, `驳回原因：${it.reasons}`)
        assert(f.reject.comment ? it.comment === `说明：${f.reject.comment}` : it.comment === null, `说明：${it.comment}`)
        assert(it.cover === 'loaded', `封面没显示出来：${it.cover}`)
      }
      const hint = await page.$eval('[data-testid=rescan-hint]', (el) => el.textContent)
      assert(hint.includes('重扫件不要扫进热文件夹'), `提示：${hint}`)
      console.log(`  ✓ 列表 ${items.length} 项、角标 ${badge}，和接口一致；原件位置、审核人、驳回原因和说明、封面都在`)

      // 页面之外驳回一页、重扫一页：3 秒内列表和角标跟着变（SSE）。
      const third = listTemp.files[2]
      await ctx.rev('POST', `/api/files/${third.file_id}/review`, { result: 'rejected', reject_reasons: ['dup'], comment: '和上一页重了' })
      await waitTotal(page, api.total + 1)
      await page.waitForSelector(`[data-testid=rescan-item][data-file-id="${third.file_id}"]`, { timeout: 3000 })
      const first = listTemp.files[0]
      await rescanPage(ctx.scanner, first.file_id, 'P_01 重扫.png', pageBytes(listTemp.seed, 101))
      await waitTotal(page, api.total)
      await page.waitForFunction((id) => !document.querySelector(`[data-testid=rescan-item][data-file-id="${id}"]`), { timeout: 3000 }, first.file_id)
      items = await listItems(page)
      assert(items.some((i) => i.file_id === third.file_id && i.reasons === '驳回：重页'), '新驳回的一页应该出现在列表里')
      console.log('  ✓ 页面之外驳回一页、重扫一页：3 秒内列表和角标都更新')
      await waitCovers(page, '[data-testid=rescan-list]')
    },
    async after() {
      await dropTemp(listTemp)
    },
  },
  {
    name: 'm2-rescan-shell-done',
    path: '/scan',
    shell: true,
    waitUntil: IDLE,
    async init(page) {
      const ctx = await context()
      shellTemp = await makeTemp(ctx, '壳', 3, [2, 3])
      const pick = pageBytes(shellTemp.seed, 102)
      await useShellSession(page, BASE, ctx.scanner.cookie())
      const MB = 1024 * 1024
      const now = Date.now()
      const item = (id, file_name, over = {}) => ({
        id,
        source: 'hotfolder',
        server_url: SERVER,
        local_path: `D:\\Scan\\Out\\${file_name}`,
        file_name,
        rel_dir: '',
        target_folder_id: ctx.demoFolder.id,
        folder_id: null,
        size: Math.round(2.2 * MB),
        mtime_ns: (now - 60_000) * 1e6,
        sha256: null,
        status: 'done',
        attempts: 0,
        next_attempt_at: null,
        file_id: null,
        server_file_name: file_name,
        sort_no: id,
        cover_status: 'included',
        cover_reason: null,
        error_code: null,
        error_message: null,
        warnings: [],
        duplicate_of: null,
        created_at: now - (40 - id) * 4000,
        updated_at: now - (40 - id) * 3000,
        finished_at: null,
        ...over,
      })
      await installWailsMock(page, { serverUrl: SERVER }, rescanShellMock, {
        serverUrl: SERVER,
        status: {
          server_url: SERVER,
          paused: null,
          paused_sources: [],
          stats: { waiting: 0, queued: 0, uploading: 0, done: 36, duplicate: 0, failed: 0, retrying: 0, total: 36 },
          running: 0,
        },
        hotfolder: {
          config: { watch_dir: 'D:\\Scan\\Out', target_folder_id: ctx.demoFolder.id, server_url: SERVER, scan_interval_secs: 15, running: true },
          running: true,
          watching: true,
          error: null,
          last_scan_at: now - 6000,
          scans: 12,
        },
        items: [36, 35, 34, 33, 32].map((id) => item(id, `IMG_00${id}.jpg`)),
        pick: {
          name: 'IMG_0231 重扫.png',
          png: pick.png.toString('base64'),
          cover: pick.cover.toString('base64'),
          sha256: sha256(pick.png),
          size: pick.png.length,
        },
      })
    },
    async prepare(page) {
      const ctx = await context()
      await page.waitForSelector('[data-testid=scan-page][data-mode=shell][data-state=ready]', { timeout: 15000 })
      await waitList(page)
      const api = await ctx.scanner('GET', `/api/projects/${ctx.project.id}/rescans?limit=500`)
      await waitTotal(page, api.total)
      await waitCovers(page, '[data-testid=rescan-list]')
      const items = await listItems(page)
      const target = shellTemp.files[1]
      assert(items.some((i) => i.file_id === target.file_id && i.cover === 'loaded'), '壳模式的列表里要有临时文件夹的页，封面经 img 协议显示')
      await page.screenshot({ path: `${OUT}/m2-rescan-list-shell.png` })
      console.log(`  ✓ 壳模式：左边热文件夹和上传队列，右边待重扫 ${api.total} 项（封面走壳的取图地址）`)

      await page.click(`[data-testid=rescan-item][data-file-id="${target.file_id}"] [data-testid=rescan-item-upload]`)
      await page.waitForSelector(`[data-testid=rescan-dialog][data-file-id="${target.file_id}"]`, { timeout: 5000 })
      const inputs = await page.$$('[data-testid=rescan-dialog] [data-testid=rescan-input]')
      assert(inputs.length === 0, '壳里不该有页面的文件选择框（文件由壳的系统对话框选）')
      // 记下对话框里显示过的进度文字（壳的 rescan:progress 事件）。
      await page.evaluate(() => {
        window.__progressSeen = []
        new MutationObserver(() => {
          const el = document.querySelector('[data-testid=rescan-progress-text]')
          const t = el?.textContent.trim()
          if (t && window.__progressSeen.at(-1)?.text !== t) window.__progressSeen.push({ phase: el.dataset.phase, text: t })
        }).observe(document.body, { subtree: true, childList: true, characterData: true, attributes: true })
      })
      await page.click('[data-testid=rescan-dialog] [data-testid=rescan-pick]')
      // 模拟发的"传了一半"是 floor(size / 2)：大小是奇数时显示 49%，所以只要求是 1–99% 之间的一个数。
      const MID = /^正在上传 ([1-9]\d?)%（.+ \/ .+）$/
      await page.waitForFunction(
        (src) => new RegExp(src).test(document.querySelector('[data-testid=rescan-progress-text]')?.textContent.trim() ?? ''),
        { timeout: 5000 },
        MID.source,
      )
      await page.screenshot({ path: `${OUT}/m2-rescan-shell-progress.png` })
      const r = await dialogResult(page)
      const seenProgress = await page.evaluate(() => window.__progressSeen)
      const seenPhases = [...new Set(seenProgress.map((x) => x.phase))]
      assert(['hashing', 'cover', 'uploading'].every((x) => seenPhases.includes(x)), `进度阶段：${JSON.stringify(seenProgress)}`)
      assert(seenProgress.some((x) => MID.test(x.text)), `上传百分比：${JSON.stringify(seenProgress)}`)
      assert(seenProgress.some((x) => /^正在上传 100%/.test(x.text)), `传完应该显示 100%：${JSON.stringify(seenProgress)}`)
      console.log(`  ✓ 壳的 rescan:progress：对话框依次显示 ${seenProgress.map((x) => x.text).join(' → ')}`)
      assert(r.result === 'done', `结果：${JSON.stringify(r)}`)
      assert(r.title === '已重扫：现在是 v2，待审核（第 2 轮）', `标题：${r.title}`)
      assert(r.reassign === `自动分给 ${ctx.revName}`, `自动分配：${r.reassign}`)
      const calls = await page.evaluate(() => window.__mock.state.calls.filter((c) => c.cmd === 'pick_and_rescan'))
      assert(calls.length === 1 && calls[0].args.fileId === target.file_id && Object.keys(calls[0].args).join() === 'fileId', `pick_and_rescan 的参数：${JSON.stringify(calls)}`)
      await page.waitForFunction((id) => !document.querySelector(`[data-testid=rescan-item][data-file-id="${id}"]`), { timeout: 3000 }, target.file_id)
      await waitTotal(page, api.total - 1)
      const v = await verifyRescan(ctx, target, { uploadName: 'IMG_0231 重扫.png' })
      console.log(`  ✓ 壳模式上传重扫件：pick_and_rescan {fileId} 只带 file_id；对话框写 v2 第 2 轮、自动分给 ${ctx.revName}；列表少了这一页；接口：${JSON.stringify(v)}`)
    },
    async after() {
      await dropTemp(shellTemp)
    },
  },
  {
    name: 'm2-rescan-files-done',
    user: SCANNER,
    path: '/files',
    waitUntil: IDLE,
    waitFor: '[data-testid=files-page]',
    async prepare(page) {
      const ctx = await context()
      filesTemp = await makeTemp(ctx, '文件目录', 4, [2, 4])
      await openFiles(page, filesTemp.id)
      const [, p2, , p4] = filesTemp.files
      const chip = await page.$eval('[data-testid=filter-rejected]', (el) => el.textContent.trim())
      assert(chip === '已驳回 2', `筛选按钮：${chip}`)
      await page.click('[data-testid=filter-rejected]')
      await page.waitForFunction(() => document.querySelectorAll('[data-testid=file-cell]').length === 2, { timeout: 3000 })
      await waitCovers(page, '[data-testid=file-area]')
      await (await fileCell(page, p2.file_name)).click({ button: 'right' })
      await page.waitForSelector('[data-testid=context-menu] [data-testid=menu-rescan]', { visible: true, timeout: 5000 })
      await new Promise((r) => setTimeout(r, 200))
      await page.screenshot({ path: `${OUT}/m2-rescan-files-menu.png` })
      await menu(page, 'menu-rescan')
      await page.waitForSelector(`[data-testid=rescan-dialog][data-file-id="${p2.file_id}"] [data-testid=rescan-reasons]`, { timeout: 5000 })
      const reasons = await page.$eval('[data-testid=rescan-reasons]', (el) => el.textContent.replace(/\s+/g, ' ').trim())
      assert(reasons.startsWith(`驳回：${p2.reject.text}`) && (!p2.reject.comment || reasons.includes(p2.reject.comment)), `对话框的驳回原因：${reasons}`)
      const remark = await page.$eval('[data-testid=rescan-remark]', (el) => el.textContent.trim())
      assert(remark.includes(TEMP_REMARK), `原件位置：${remark}`)
      const newScan = pageBytes(filesTemp.seed, 102)
      await injectRescan(page, 'P_02 重扫.png', newScan.png)
      const r = await dialogResult(page)
      assert(r.result === 'done' && r.title === '已重扫：现在是 v2，待审核（第 2 轮）', `结果：${JSON.stringify(r)}`)
      assert(r.reassign === `自动分给 ${ctx.revName}`, `自动分配：${r.reassign}`)
      assert(r.text.includes('带封面'), `应该带封面：${r.text}`)
      await waitNoToast(page)
      await page.screenshot({ path: `${OUT}/m2-rescan-dialog-done.png` })
      await closeDialog(page)
      // 筛选里只剩 P_04；关掉筛选，选中 P_02：详情写"待审 第 2 轮"，日志第一条是重扫。
      await page.waitForFunction(() => document.querySelectorAll('[data-testid=file-cell]').length === 1, { timeout: 3000 })
      const chip2 = await page.$eval('[data-testid=filter-rejected]', (el) => el.textContent.trim())
      assert(chip2 === '已驳回 1', `重扫后筛选按钮：${chip2}`)
      await page.click('[data-testid=filter-rejected]')
      await page.waitForFunction(() => document.querySelectorAll('[data-testid=file-cell]').length === 4, { timeout: 3000 })
      await (await fileCell(page, p2.file_name)).click()
      await page.waitForSelector('[data-testid=detail-panel] [data-testid=file-logs]', { timeout: 5000 })
      await page.waitForFunction(
        () => document.querySelector('[data-testid=detail-panel]')?.textContent.includes('第 2 轮'),
        { timeout: 5000 },
      )
      const detail = await page.$eval('[data-testid=detail-panel]', (el) => el.textContent.replace(/\s+/g, ' '))
      assert(detail.includes('待审') && detail.includes('第 2 轮'), `详情：${detail.slice(0, 300)}`)
      // 详情面板的日志（新的在前）：第一条是重扫。
      await page.waitForFunction(() => document.querySelector('[data-testid=file-logs] li')?.textContent.includes('重扫'), { timeout: 5000 })
      const firstLog = await page.$eval('[data-testid=file-logs] li', (el) => ({ text: el.textContent.replace(/\s+/g, ' ').trim() }))
      assert(firstLog.text.startsWith('重扫') && firstLog.text.includes('新版本 v2，第 2 轮审核') && firstLog.text.includes(`自动分给 ${ctx.revName}`), `日志：${JSON.stringify(firstLog)}`)
      const v = await verifyRescan(ctx, p2, { uploadName: 'P_02 重扫.png' })
      // 另一页没动。
      const r4 = await ctx.mgr('GET', `/api/files/${p4.file_id}/reviews`)
      assert(r4.review_status === 'rejected' && r4.version_no === 1, 'P_04 应该仍是已驳回 v1')
      console.log(`  ✓ /files：筛选"已驳回 2"→ 右键"上传重扫件"→ 成功；筛选剩 1 页；详情"待审 第 2 轮"；日志"${firstLog.text}"；接口：${JSON.stringify(v)}`)
      await waitCovers(page, '[data-testid=file-area]')
    },
    async after() {
      await dropTemp(filesTemp)
    },
  },
  {
    name: 'm2-rescan-wrong-file',
    user: SCANNER,
    path: '/files',
    waitUntil: IDLE,
    waitFor: '[data-testid=files-page]',
    async prepare(page) {
      const ctx = await context()
      wrongTemp = await makeTemp(ctx, '选错文件', 3, [2, 3])
      const [p1, p2, p3] = wrongTemp.files
      await openFiles(page, wrongTemp.id)

      // 1) 选成了另一页（P_01）的图：可能选错了文件，写出 P_01 的位置。
      await openRescanFromFiles(page, p2)
      await injectRescan(page, 'IMG_扫错了.png', p1.bytes.png)
      let r = await dialogResult(page)
      assert(r.result === 'duplicate_content', `结果：${JSON.stringify(r)}`)
      assert(r.title === '选的图片和项目里另一个文件内容相同，可能选错了文件', `标题：${r.title}`)
      assert(r.location === `内容相同的文件：${DEMO_FOLDER}/${wrongTemp.name}/${p1.file_name}`, `位置：${r.location}`)
      await waitNoToast(page)
      await page.screenshot({ path: `${OUT}/m2-rescan-wrong-file.png` })

      // 2) 重新选择，选成了它自己现在的那张：一模一样。
      await page.click('[data-testid=rescan-dialog] [data-testid=rescan-pick]').catch(() => {})
      await injectRescan(page, 'P_02.png', p2.bytes.png)
      await page.waitForFunction(
        () => document.querySelector('[data-testid=rescan-result-title]')?.textContent.includes('一模一样'),
        { timeout: 15000 },
      )
      r = await dialogResult(page)
      assert(r.result === 'duplicate_content' && r.location === null, `结果：${JSON.stringify(r)}`)
      assert(r.title === '选的图片和这个文件现在的版本一模一样，可能选错了文件', `标题：${r.title}`)
      assert(r.text.includes('和现在的 v1 相同'), `说明：${r.text}`)
      await page.screenshot({ path: `${OUT}/m2-rescan-wrong-file-self.png` })
      const snaps = await ctx.scanner('GET', `/api/files/${p2.file_id}/snapshots`)
      const st = await ctx.mgr('GET', `/api/files/${p2.file_id}/reviews`)
      assert(snaps.snapshots.length === 1 && st.review_status === 'rejected', `选错文件后不该有新快照：${snaps.snapshots.length} 个，${st.review_status}`)
      console.log(`  ✓ 选成别的页：提示"${r.title.replace('这个文件现在的版本一模一样', '…')}"并写出位置；选成它自己：一模一样；接口：仍是 v1、已驳回`)
      await page.keyboard.press('Escape')
      await dialogGone(page)

      // 3) 对话框开着时，这一页在页面之外已经被重扫了（或者上一次其实已经成功）：invalid_state，按"已经成功"提示。
      await openRescanFromFiles(page, p3)
      await rescanPage(ctx.scanner, p3.file_id, 'P_03 别人重扫.png', pageBytes(wrongTemp.seed, 103))
      await injectRescan(page, 'P_03 重扫.png', pageBytes(wrongTemp.seed, 203).png)
      r = await dialogResult(page)
      assert(r.result === 'invalid_state' && r.state === 'done', `结果：${JSON.stringify(r)}`)
      assert(r.title === '这个文件已经不是"已驳回"了', `标题：${r.title}`)
      const snaps3 = await ctx.scanner('GET', `/api/files/${p3.file_id}/snapshots`)
      assert(snaps3.snapshots.length === 2, `P_03 只该有页面之外那一次重扫：${snaps3.snapshots.length} 个快照`)
      // 列表刷新：P_03 已经是待审（第 2 轮）。
      await page.waitForFunction(
        (name) =>
          [...document.querySelectorAll('[data-testid=file-cell]')].some(
            (el) => [...el.querySelectorAll('[title]')].some((t) => t.getAttribute('title') === name) && el.textContent.includes('待审'),
          ),
        { timeout: 3000 },
        p3.file_name,
      )
      await waitNoToast(page)
      await page.screenshot({ path: `${OUT}/m2-rescan-already.png` })
      console.log('  ✓ 已经被重扫过的页再传：409 invalid_state，提示"已经不是已驳回了"（不当作失败），列表刷新成待审')
      await closeDialog(page)
      // 截图回到"选错文件"的样子。
      await openRescanFromFiles(page, p2)
      await injectRescan(page, 'IMG_扫错了.png', p1.bytes.png)
      await dialogResult(page)
      await waitNoToast(page)
    },
    async after() {
      await dropTemp(wrongTemp)
    },
  },
  {
    name: 'm2-rescan-retries',
    user: SCANNER,
    path: '/files',
    waitUntil: IDLE,
    waitFor: '[data-testid=files-page]',
    async prepare(page) {
      const ctx = await context()
      retryTemp = await makeTemp(ctx, '重试', 2, [1, 2])
      const [p1, p2] = retryTemp.files
      await openFiles(page, retryTemp.id)
      const plan = []
      const seen = []
      await page.setRequestInterception(true)
      const onRequest = (req) => {
        if (req.method() === 'POST' && /\/api\/files\/[^/]+\/rescan$/.test(new URL(req.url()).pathname)) {
          seen.push(req.url())
          const fault = plan.shift()
          if (fault) {
            void req.respond({ status: fault.status, contentType: 'application/json', body: JSON.stringify({ code: fault.code, message: fault.message }) })
            return
          }
        }
        void req.continue()
      }
      page.on('request', onRequest)
      try {
        // 1) 409 conflict：自动再发一次，第二次成功。
        plan.push({ status: 409, code: 'conflict', message: '文件的版本刚被改了，请刷新后重试' })
        await openRescanFromFiles(page, p1)
        await injectRescan(page, 'P_01 重扫.png', pageBytes(retryTemp.seed, 101).png)
        let r = await dialogResult(page)
        assert(r.result === 'done' && seen.length === 2, `conflict 之后应该自动再发一次并成功：${JSON.stringify(r)}，请求 ${seen.length} 次`)
        await verifyRescan(ctx, p1, { uploadName: 'P_01 重扫.png' })
        console.log('  ✓ 409 conflict：页面自动再发一次，第二次成功（共 2 个请求）')
        await closeDialog(page)

        // 2) 400 invalid_cover：去掉封面重传一次，成功，没有封面。
        seen.length = 0
        plan.push({ status: 400, code: 'invalid_cover', message: '封面的长宽比和原图不一致' })
        await openRescanFromFiles(page, p2)
        await injectRescan(page, 'P_02 重扫.png', pageBytes(retryTemp.seed, 102).png)
        r = await dialogResult(page)
        assert(r.result === 'done' && seen.length === 2, `invalid_cover 之后应该去掉封面重传并成功：${JSON.stringify(r)}，请求 ${seen.length} 次`)
        assert(r.text.includes('无封面（服务器没收封面：封面的长宽比和原图不一致）'), `对话框应该写无封面：${r.text}`)
        const v = await verifyRescan(ctx, p2, { uploadName: 'P_02 重扫.png', hasCover: false })
        const batch = await ctx.scanner('POST', '/api/files/batch', { file_ids: [p2.file_id] })
        assert(batch.items[0].cover_url === null && batch.items[0].version_no === 2, 'batch 里 v2 应该没有封面')
        console.log(`  ✓ 400 invalid_cover：去掉封面重传一次并成功，v2 没有封面（${JSON.stringify(v)}）`)
      } finally {
        page.off('request', onRequest)
        await page.setRequestInterception(false)
      }
      await waitNoToast(page)
    },
    async after() {
      await dropTemp(retryTemp)
    },
  },
]

let localTemp = null
shots.push({
  name: 'm2-rescan-local-checks',
  user: SCANNER,
  path: '/files',
  waitUntil: IDLE,
  waitFor: '[data-testid=files-page]',
  async prepare(page) {
    const ctx = await context()
    localTemp = await makeTemp(ctx, '本地核对', 4, [1, 2, 3])
    const [p1, p2, p3] = localTemp.files
    await openFiles(page, localTemp.id)
    const rescanPosts = []
    const rule = { rescansLimit: null, rescansRespond: null, holdRescanMs: 0 }
    const rescansGets = []
    await page.setRequestInterception(true)
    const onRequest = (req) => {
      const url = new URL(req.url())
      if (req.method() === 'POST' && /\/api\/files\/[^/]+\/rescan$/.test(url.pathname)) {
        rescanPosts.push(url.pathname)
        if (rule.holdRescanMs) {
          setTimeout(() => void req.continue(), rule.holdRescanMs)
          return
        }
      }
      if (req.method() === 'GET' && /\/api\/projects\/[^/]+\/rescans$/.test(url.pathname) && url.searchParams.get('folder_id') === localTemp.id) {
        rescansGets.push(url.search)
        if (rule.rescansRespond) {
          void req.respond(rule.rescansRespond)
          return
        }
        if (rule.rescansLimit) {
          url.searchParams.set('limit', String(rule.rescansLimit))
          void req.continue({ url: url.toString() })
          return
        }
      }
      void req.continue()
    }
    page.on('request', onRequest)
    try {
      // 1) 写了一半的 PNG：不发请求，提示不完整。
      await openRescanFromFiles(page, p1)
      const half = p1.bytes.png.subarray(0, Math.floor(p1.bytes.png.length / 2))
      await injectRescan(page, 'P_01 扫了一半.png', pageBytes(localTemp.seed, 101).png.subarray(0, half.length))
      let r = await dialogResult(page)
      assert(r.result === 'local_file' && r.title === '没有上传', `结果：${JSON.stringify(r)}`)
      assert(r.text.includes('选的文件不完整或还在写入，请等扫描软件写完再选'), `说明：${r.text}`)
      assert(rescanPosts.length === 0, `写了一半的文件不该发重扫请求（发了 ${rescanPosts.length} 次）`)
      await waitNoToast(page)
      await page.screenshot({ path: `${OUT}/m2-rescan-local-checks.png` })
      // 2) 扩展名不对：不发请求。
      await injectRescan(page, 'P_01.gif', pageBytes(localTemp.seed, 102).png)
      await page.waitForFunction(() => document.querySelector('[data-testid=rescan-dialog]')?.textContent.includes('只能选 jpg、jpeg、tif、tiff、png 的图片'), { timeout: 5000 })
      assert(rescanPosts.length === 0, '扩展名不对也不该发请求')
      const s1 = await ctx.scanner('GET', `/api/files/${p1.file_id}/snapshots`)
      assert(s1.snapshots.length === 1, 'P_01 应该还是 1 个快照')
      console.log('  ✓ 写了一半的 PNG、.gif：对话框直接提示，没有发重扫请求，P_01 仍是 v1')
      await page.keyboard.press('Escape')
      await dialogGone(page)

      // 3) 驳回原因要翻页才找得到：每页 1 个，P_03 在第 3 页。
      rule.rescansLimit = 1
      rescansGets.length = 0
      await openRescanFromFiles(page, p3)
      const reasons = await page.$eval('[data-testid=rescan-reasons]', (el) => el.textContent.replace(/\s+/g, ' ').trim())
      assert(reasons.startsWith(`驳回：${p3.reject.text}`), `第 3 页的驳回原因：${reasons}`)
      assert(rescansGets.length === 3 && rescansGets.every((q) => q.includes('limit=500')), `应该跟着 next_cursor 翻 3 页：${JSON.stringify(rescansGets)}`)
      await page.screenshot({ path: `${OUT}/m2-rescan-lookup.png` })
      console.log(`  ✓ 驳回原因在第 3 页：跟着 next_cursor 翻了 ${rescansGets.length} 页找到「${reasons}」`)
      await page.keyboard.press('Escape')
      await dialogGone(page)
      rule.rescansLimit = null

      // 4) 待重扫列表里没有它：已经不在列表里；5) 查失败：读不到驳回原因（中性说法）。
      rule.rescansRespond = { status: 200, contentType: 'application/json', body: JSON.stringify({ total: 0, items: [], next_cursor: null }) }
      await (await fileCell(page, p2.file_name)).click({ button: 'right' })
      await menu(page, 'menu-rescan')
      await page.waitForSelector('[data-testid=rescan-dialog] [data-testid=rescan-not-rejected]', { timeout: 5000 })
      await page.keyboard.press('Escape')
      await dialogGone(page)
      rule.rescansRespond = { status: 500, contentType: 'application/json', body: JSON.stringify({ code: 'internal', message: '服务器内部错误' }) }
      await (await fileCell(page, p2.file_name)).click({ button: 'right' })
      await menu(page, 'menu-rescan')
      await page.waitForSelector('[data-testid=rescan-dialog] [data-testid=rescan-lookup-error]', { timeout: 5000 })
      const errText = await page.$eval('[data-testid=rescan-lookup-error]', (el) => el.textContent.trim())
      assert(errText.includes('读不到驳回原因') && errText.includes('仍然可以上传'), `查失败：${errText}`)
      assert(!(await page.$('[data-testid=rescan-dialog] [data-testid=rescan-not-rejected]')), '查失败时不该说"已经不在待重扫列表里"')
      console.log('  ✓ rescans 里没有它：已经不在待重扫列表里；rescans 出错：读不到驳回原因，仍然可以上传')

      // 6) 查失败时照样上传；上传期间（响应拦 1.5 秒）关闭、刷新页面要确认，传完不拦；成功的说明用中性说法。
      rule.holdRescanMs = 1500
      await injectRescan(page, 'P_02 重扫.png', pageBytes(localTemp.seed, 202).png)
      await page.waitForFunction(() => document.querySelector('[data-testid=rescan-dialog]')?.dataset.state === 'working', { timeout: 5000 })
      await page.waitForFunction(() => document.querySelector('[data-testid=rescan-progress-text]')?.dataset.phase === 'uploading', { timeout: 10000 })
      const blocked = await page.evaluate(() => {
        const e = new Event('beforeunload', { cancelable: true })
        window.dispatchEvent(e)
        return e.defaultPrevented
      })
      assert(blocked, '上传期间 beforeunload 应该被拦下')
      r = await dialogResult(page)
      assert(r.result === 'done', `结果：${JSON.stringify(r)}`)
      assert(r.reassign === '已自动分给审核人', `没拿到驳回记录时的说明：${r.reassign}`)
      const after = await page.evaluate(() => {
        const e = new Event('beforeunload', { cancelable: true })
        window.dispatchEvent(e)
        return e.defaultPrevented
      })
      assert(!after, '传完后 beforeunload 不该再拦')
      await verifyRescan(ctx, p2, { uploadName: 'P_02 重扫.png' })
      console.log('  ✓ 上传期间 beforeunload 被拦下、传完不拦；没拿到驳回记录时成功说明写"已自动分给审核人"')
      rule.holdRescanMs = 0
      rule.rescansRespond = null
      await closeDialog(page)
      // 截图回到"写了一半"的样子。
      await openRescanFromFiles(page, p1)
      await injectRescan(page, 'P_01 扫了一半.png', half)
      await dialogResult(page)
      await waitNoToast(page)
    } finally {
      page.off('request', onRequest)
      await page.setRequestInterception(false)
    }
  },
  async after() {
    await dropTemp(localTemp)
  },
})

// 端到端（不删数据）：浏览器里在 /scan 的列表上重扫演示文件夹的一页。
if (process.env.M27_E2E === '1') {
  shots.push({
    name: 'm2-rescan-e2e-browser',
    user: SCANNER,
    path: '/scan',
    waitUntil: IDLE,
    waitFor: '[data-testid=rescan-list]',
    async prepare(page) {
      const ctx = await context()
      const box = (await ctx.admin('GET', `/api/folders/${ctx.demoFolder.id}/children`)).folders.find((f) => f.folder_name === '盒 003')
      const ch = await ctx.admin('GET', `/api/folders/${box.id}/children`)
      const f = ch.files.find((x) => x.file_name === (process.env.M27_E2E_FILE ?? 'IMG_0002.png'))
      assert(f && f.review_status === 'rejected', `演示文件夹的这一页应该是已驳回：${f?.review_status}`)
      await waitList(page)
      await page.click(`[data-testid=rescan-item][data-file-id="${f.file_id}"] [data-testid=rescan-item-upload]`)
      await page.waitForSelector(`[data-testid=rescan-dialog][data-file-id="${f.file_id}"]`, { timeout: 5000 })
      const name = `${f.file_name.replace('.png', '')} 重扫 浏览器.png`
      await injectRescan(page, name, pageBytes(`M2-7 端到端 浏览器 ${stamp()} ${Math.random()}`, 2).png)
      const r = await dialogResult(page)
      assert(r.result === 'done', `结果：${JSON.stringify(r)}`)
      const v = await verifyRescan(ctx, { file_id: f.file_id, file_name: f.file_name }, { uploadName: name })
      console.log(`  ✓ 浏览器端到端：盒 003/${f.file_name} file_id=${f.file_id}；${r.title}；${r.reassign}；接口：${JSON.stringify(v)}`)
    },
  })
}

export default {
  out: OUT,
  accounts: { [SCANNER]: demoPassword },
  shots,
}
