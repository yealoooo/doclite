// M3-8 验收（Mac）：审核和归类在同一个文件夹上并行——负责人分 100 页给审核员；审核员全键盘审核的同时，归类员在 /catalog
// 建条目、区间挂接、拖拽排序、著录、标记完成，把一页从一个条目移到另一个条目；驳回的页在浏览器和打包的壳里重扫，条目里
// 自动显示 v2、第 2 轮；看板实时变化；定稿 → 定稿后重扫（"定稿后有新版本"）→ 负责人解锁 → 再定稿；整理文件目录（移动、
// 改名、复制文件，移动文件夹，删除含已归类文件的文件夹被拒）前后用 SQL 对比挂接和档号；最后还原 3 个文件的全过程。
//
//   INIT_ADMIN_PASSWORD=admin123456 SHOTS_TMP=<临时目录> \
//   M3_ACCEPT_SHELL=<打包的壳>/Doclite.app/Contents/MacOS/Doclite \
//     npm run screenshots -- --spec scripts/shots/m3-accept.mjs --base http://127.0.0.1:<web 端口> [--out <目录>]
//
// --base 是 web 的入口（vite preview 或 dev，/api 代理到 server）。壳的几步（M3_ACCEPT_SHELL）用打包的 release 壳跑 `--smoke` 的 rescan 一步：
// 数据目录用 SHOTS_TMP 下的隔离目录（DOCLITE_DATA_DIR，另设隔离的 HOME），不碰 ~/Library/Application Support/Doclite；服务器地址是 M3_ACCEPT_SERVER（默认同
// --base 的 origin；壳只调 /api，也可以给 server 本身的地址）。tiles、viewer 两步有意让它们立即失败（viewer 要求窗口看得见，锁屏时会卡住整个
// 自检，见 runShell），其余各步都要通过。
// 没给 M3_ACCEPT_SHELL 时，壳的重扫改用接口模拟（带 content_sha256，同壳的字段）。核对审计和"整理前后的挂接"时
// 直接读库（psql，DATABASE_URL，默认本机 doclite_dev；写脚本时还没有审计的读接口）。
//
// 演示数据（脚本自己建；只能走一遍，再跑会换一个新的文件夹，全宗编码已被占用时失败）：
// - DEMO-A 根下「M3 验收演示 <日期>」（M3_ACCEPT_FOLDER 指定名字）：盒 01、盒 02 各 30 页，盒 03、盒 04 各 20 页，
//   demo_scanner 经上传接口传（合成 PNG + 封面），全部待分配；已有同名文件夹时要求正好 100 页、全部待分配。
// - 归类树（demo_cataloger 经接口建，默认模板，著录项沿用 DEMO-A 现有的 5 项）：全宗 Z308「M3 验收演示」（M3_ACCEPT_FONDS
//   换编码）/ 门类 WS「文书档案」/ 年度 2025「2025 年」/ 保管期限 Y「永久」/ 机构 BGS「办公室」、RSC「人事处」。
// - M3_ACCEPT_TRIAL=1 是试跑：文件夹叫「M3 验收试跑 <时间>」、全宗编码 T<时间>，最后一张的 after 里删掉条目（定稿的先解锁）、
//   归类节点，软删除文件夹。
// 要求 demo_manager、demo_reviewer、demo_scanner、demo_cataloger 能进 DEMO-A（密码 demo123456，DEMO_PASSWORD 可覆盖）。
// 别的演示文件夹（M1、M2、M3-5 到 M3-7）和别的全宗（Z109、Z201、Z206）不改：第一张记下它们的计数、挂接和档号，最后核对没变；
// 「M2-5 分配演示」要一直是 40 个待分配。
//
// 截图（按顺序执行，后面的用前面的结果；文件名都以 m3-accept- 开头）：
//  1. m3-accept-setup：建演示文件夹和归类树，demo_cataloger 打开空的"办公室"节点、池里是盒 01。
//  2. m3-accept-assign：demo_manager 在 /review-assign 按文件夹把 100 页分给 demo_reviewer（确认框"含子文件夹，共 100 个
//     待分配"）；接口核对每个文件一行 open、assign_review 日志。
//  3. m3-accept-parallel：同时进行（两个浏览器上下文，另开一个 demo_manager 的看板）——
//     - demo_reviewer 在 /review 只用键盘审 100 页（A 通过；R → 数字 → Tab 说明 → Enter 驳回，驳回 5 页），每页停 PACE 毫秒；
//     - demo_cataloger 在 /catalog 只用界面：办公室"新建"第 1 件 → 池里"第 1–10 张"挂到末尾 → 把第 2 页拖到第 5、6 页
//       之间 → 填著录、保存 → 标记完成；"第 1–10 张"（IMG_0011–0020）"用选中的文件新建条目"第 2 件 → 著录、保存 →
//       关掉"仅未归类"把第 1 件里的 IMG_0010 挂过来（409 file_cataloged，对话框里"从原条目移出"后自动重试，预期的冲突）→
//       "移到第 1 页"→ 标记完成；切到人事处、池换到盒 02，"第 1–15 张"新建第 1 件、著录、保存（留作草稿）；切回办公室、
//       池换到盒 03，"第 1–20 张"新建第 3 件、著录、保存、标记完成。
//     两边每个写请求记耗时（浏览器里从发出到收完，另记 TTFB），按操作类型给出中位数和最慢；除了那一次预期的 409 之外，
//     任何 409、5xx、请求失败或超过 5 秒都算失败；前后各读一次 pg_stat_database 的 deadlocks，不能变。结束后看板 3 秒内
//     和接口一致。另存 m3-accept-parallel-catalog（归类员那边）、m3-accept-move-dialog（"已在别的条目里"对话框）、
//     m3-accept-parallel-dashboard。
//  4. m3-accept-rescan-browser：demo_scanner 在 /scan 的待重扫列表里重扫 3 页（A、盒 02/IMG_0005、盒 03/IMG_0012）；接口
//     核对 v2、第 2 轮、自动分回 demo_reviewer。
//  5. m3-accept-rescan-shell：demo_cataloger 开着第 2 件；打包的壳 `--smoke` 重扫 C（盒 01/IMG_0016）→ 快照的 sha 等于本地
//     文件，3 秒内页序条上 C 变成 v2、第 2 轮、待审（不用重新挂接）。截图是这个页面。
//  6. m3-accept-v2：第 1 件里 A 的那一页：v2、"第 2 轮"、待审；条目的 row_version 没被重扫改动。
//  7. m3-accept-lock-cycle（demo_cataloger 的 /catalog，另开 demo_manager 的 /catalog 和看板）：审核员通过第 2 轮的 4 页 →
//     归类员定稿第 1 件（另存 m3-accept-lock）→ 审核员撤回 A 的通过、驳回 → 壳重扫 A（v3）→ 第 1 件标出"定稿后有新版本"
//     （另存 m3-accept-new-version）→ 负责人解锁（另存 m3-accept-unlock）→ 审核员通过 v3 → 归类员再定稿，标记消失；
//     再批量定稿第 2、3 件。每一步之后看板 3 秒内和接口一致（另存 m3-accept-dashboard）。
//  8. m3-accept-organize（demo_scanner 的 /files）：SQL 导出这个全宗的挂接（archive_item_id、file_id、page_no）和条目（id、
//     件号、档号、状态、row_version）→ 在演示文件夹下建「已整理」，把 B 移进去，改名第 1 件里的 IMG_0005，把 IMG_0006 复制
//     进去，把盒 03 整个移进去（接口）→ 在界面上删除盒 02 → 409 file_cataloged 和提示"文件夹里有已归类的文件，不能删除"→
//     再导出一次，逐行相同。
//  9. m3-accept-restore：用 `/logs`、`/reviews`、`/snapshots`、条目详情和 SQL 还原 A、B、C 的全过程并断言，打印一行
//     `ACCEPT_IDS {...}` 给外面的 SQL 核对。截图是 B 在 /files 详情里的日志。
// 10. m3-accept-final：demo_manager 的看板；别的演示文件夹、别的全宗没变，deadlocks 没变；打印 `STATE {...}`（耗时统计）。

import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { apiLogin, deleteItemsForCleanup, lit, runSql, stamp, uploadPage } from '../lib/m3-demo.mjs'
import { makeScanPage } from '../lib/scan-pages.mjs'
import { shotsTmp } from '../lib/viewer-shots.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'
const PROJECT = 'DEMO-A'
const MANAGER = 'demo_manager'
const REVIEWER = 'demo_reviewer'
const SCANNER = 'demo_scanner'
const CATALOGER = 'demo_cataloger'
const IDLE = 'networkidle2'
const OUT = '../docs/screenshots/m3'
const outArg = process.argv.indexOf('--out')
const OUT_DIR = path.resolve(outArg > 0 ? process.argv[outArg + 1] : OUT)
const SHELL = process.env.M3_ACCEPT_SHELL ?? ''
const TRIAL = process.env.M3_ACCEPT_TRIAL === '1'
const RUN_STAMP = stamp()
const TODAY = new Date().toLocaleDateString('sv-SE')
const FOLDER_NAME = process.env.M3_ACCEPT_FOLDER ?? (TRIAL ? `M3 验收试跑 ${RUN_STAMP}` : `M3 验收演示 ${TODAY}`)
const FONDS_CODE = process.env.M3_ACCEPT_FONDS ?? (TRIAL ? `T${RUN_STAMP.slice(4)}` : 'Z308')
const FONDS_NAME = TRIAL ? 'M3 验收试跑' : 'M3 验收演示'
/** 别的演示全宗（只核对没变）。 */
const OTHER_FONDS = ['Z109', 'Z201', 'Z206']
/** 页面之外改了数据后，页面要在这么久之内跟上。 */
const LIVE_MS = 3000
/** 一个写请求超过这么久算超时。 */
const SLOW_MS = 5000
/** 审核员每页停多久（毫秒），让审核和归类在时间上真正重叠。 */
const PACE_MS = Number(process.env.M3_ACCEPT_PACE_MS ?? 200)
/** 归类员每做完一步停多久（毫秒，看一眼、想一下），让归类的操作分布在整个审核过程里。 */
const THINK_MS = Number(process.env.M3_ACCEPT_THINK_MS ?? 2500)
const BOXES = [
  { name: '盒 01', count: 30, remark: '4 号库 A 架 1 层 · 盒 01（1–30 页）' },
  { name: '盒 02', count: 30, remark: '4 号库 A 架 1 层 · 盒 02（31–60 页）' },
  { name: '盒 03', count: 20, remark: '4 号库 A 架 2 层 · 盒 03（61–80 页）' },
  { name: '盒 04', count: 20, remark: '4 号库 A 架 2 层 · 盒 04（81–100 页）' },
]

/** 驳回：键（1–9 按项目原因列表的顺序：模糊、倾斜、裁切过度、污渍、缺页、重页、方向、其他）、原因代码、说明。 */
const REJECTS = {
  '盒 01/IMG_0003.png': { keys: ['1'], reasons: ['blur'], comment: '字迹发虚，请按 300 dpi 重扫' },
  '盒 01/IMG_0016.png': { keys: ['2'], reasons: ['skew'], comment: null },
  '盒 02/IMG_0005.png': { keys: ['4'], reasons: ['stain'], comment: '右下角有水渍，挡住了落款' },
  '盒 03/IMG_0012.png': { keys: ['3', '8'], reasons: ['cut', 'other'], comment: '装订线被裁掉，重扫时整页放平' },
  '盒 04/IMG_0007.png': { keys: ['7'], reasons: ['orient'], comment: null },
}
/** A：第 1 件；驳回 → 浏览器重扫 v2 → 通过 → 定稿 → 撤回、驳回 → 壳重扫 v3（定稿后有新版本）→ 解锁 → 通过 → 再定稿。 */
const FILE_A = '盒 01/IMG_0003.png'
/** B：第 1 件 → 从原条目移出、挂到第 2 件 → 整理时移进「已整理」。 */
const FILE_B = '盒 01/IMG_0010.png'
/** C：第 2 件；驳回 → 壳重扫 v2 → 通过。 */
const FILE_C = '盒 01/IMG_0016.png'
const RESCAN_BROWSER = [FILE_A, '盒 02/IMG_0005.png', '盒 03/IMG_0012.png']
const RENAME_FILE = '盒 01/IMG_0005.png'
const COPY_FILE = '盒 01/IMG_0006.png'
const ORGANIZED = '已整理'

/** 4 个条目的著录（归类员在界面上填）。 */
const ITEMS = {
  i1: { node: 'bgs', no: 1, title: '关于印发 2025 年档案数字化验收方案的通知', fields: { author: '市档案馆办公室', doc_no: '档办〔2025〕31 号', doc_date: '2025-10-09', secret: '内部' } },
  i2: { node: 'bgs', no: 2, title: '办公室 2025 年第三季度工作总结', fields: { author: '办公室', doc_date: '2025-10' } },
  i3: { node: 'rsc', no: 1, title: '关于 2025 年度职称评审工作的通知', fields: { author: '人事处', doc_no: '人〔2025〕12 号', doc_date: '2025-09-20', secret: '公开' } },
  i4: { node: 'bgs', no: 3, title: '2025 年档案库房安全检查记录', fields: { author: '保管部', doc_date: '2025-08' } },
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

function assert(cond, msg) {
  if (!cond) throw new Error(`断言失败：${msg}`)
}

function sameList(a, b) {
  return a.length === b.length && a.every((x, i) => x === b[i])
}

// ---------- 共用状态 ----------

let ctxPromise = null
const state = {
  timings: {},
  live: {},
  shellRuns: [],
  rescanned: {},
  items: {},
  parallel: null,
  windows: [],
}

/** 登录几个账号，找到 DEMO-A 和用户 id（只做一次）。演示文件夹、归类树在第一张里建。 */
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const adminApi = await apiLogin(origin, admin, adminPassword)
    const project = (await adminApi('GET', '/api/projects')).find((p) => p.project_code === PROJECT)
    assert(project, `找不到项目 ${PROJECT}，先跑 seed:demo`)
    const users = {}
    for (const name of [MANAGER, REVIEWER, SCANNER, CATALOGER]) {
      const res = await adminApi('GET', `/api/system/users?keyword=${encodeURIComponent(name)}&page_size=100`)
      const u = res.items.find((x) => x.username === name)
      assert(u && u.status === 'active', `找不到启用的用户 ${name}`)
      users[name] = u
    }
    state.origin = origin
    return {
      origin,
      project,
      pid: project.id,
      admin: adminApi,
      mgr: await apiLogin(origin, MANAGER, demoPassword),
      rev: await apiLogin(origin, REVIEWER, demoPassword),
      scanner: await apiLogin(origin, SCANNER, demoPassword),
      cat: await apiLogin(origin, CATALOGER, demoPassword),
      id: (name) => users[name].id,
      displayName: (name) => users[name].display_name,
      // 下面这些在 m3-accept-setup 里填上。
      demo: null,
      boxes: {},
      files: {},
      cats: {},
    }
  })()
  return ctxPromise
}

function file(ctx, key) {
  const f = ctx.files[key]
  assert(f, `演示文件夹里没有 ${key}`)
  return f
}

/** 一个盒子里的文件（按 sort_no，即上传顺序）。 */
function boxFiles(ctx, box) {
  return Object.values(ctx.files)
    .filter((f) => f.box === box)
    .sort((a, b) => a.sort_no - b.sort_no)
}

function keyOf(ctx, fid) {
  return Object.keys(ctx.files).find((k) => ctx.files[k].file_id === fid) ?? fid.slice(-6)
}

const reviewsOf = (ctx, fid) => ctx.admin('GET', `/api/files/${fid}/reviews`)
const logsOf = async (ctx, fid) => (await ctx.admin('GET', `/api/files/${fid}/logs`)).logs
const snapshotsOf = async (ctx, fid) => (await ctx.admin('GET', `/api/files/${fid}/snapshots`)).snapshots
const getItem = (ctx, id) => ctx.admin('GET', `/api/archive-items/${id}`)
const pageIds = (it) => it.pages.map((p) => p.file_id)

/** 这个文件夹子树的四种状态计数（review-summary）。 */
async function summary(ctx, folderId) {
  return (await ctx.mgr('GET', `/api/projects/${ctx.pid}/review-summary?folder_id=${folderId}`)).counts
}

/** DEMO-A 根下别的演示文件夹的状态计数（核对没被改动）。 */
async function otherFolders(ctx) {
  const root = await ctx.admin('GET', `/api/folders/${ctx.project.root_folder_id}/children`)
  const out = {}
  for (const f of root.folders) {
    if (f.id === ctx.demo?.id) continue
    if (f.folder_name.startsWith('M3 亲手演示') || f.folder_name.startsWith('M3 验收')) continue
    const c = await summary(ctx, f.id)
    out[f.folder_name] = `${c.unassigned}/${c.pending}/${c.approved}/${c.rejected}`
  }
  return out
}

/** 别的全宗里的条目和挂接（SQL，按档号、页号排好的文本，前后逐行比较）。 */
function otherFondsSnapshot(ctx) {
  return runSql(`
    SELECT fo.category_code, a.id, a.item_no, coalesce(a.archive_no, '-'), a.status, a.row_version,
           coalesce(string_agg(af.file_id::text || '#' || af.page_no, ',' ORDER BY af.page_no), '')
    FROM archive_categories fo
    JOIN archive_categories c ON c.project_id = fo.project_id AND c.path_code >= fo.path_code
         AND c.path_code < left(fo.path_code, -1) || '/' COLLATE "C"
    JOIN archive_items a ON a.category_id = c.id AND a.deleted_at IS NULL
    LEFT JOIN archive_item_files af ON af.archive_item_id = a.id
    WHERE fo.project_id = ${lit(ctx.pid)} AND fo.deleted_at IS NULL AND fo.category_code IN (${OTHER_FONDS.map(lit).join(',')})
      AND fo.parent_id = (SELECT id FROM archive_categories WHERE project_id = ${lit(ctx.pid)} AND parent_id IS NULL)
    GROUP BY fo.category_code, a.id ORDER BY fo.category_code, a.id;`)
}

/** 全库的死锁计数（pg_stat_database）。 */
function deadlocks() {
  return Number(runSql(`SELECT deadlocks FROM pg_stat_database WHERE datname = current_database();`).trim())
}

/** 一页新的扫描件（PNG + 封面），内容由种子决定。 */
function newPage(label, pageNo, dpi = 300) {
  const p = makeScanPage({ seed: `M3-8 ${label} ${stamp()} ${Math.random()}`, pageNo, dpi })
  return { png: Buffer.from(p.png), cover: Buffer.from(p.cover) }
}

// ---------- 写请求的耗时 ----------

/** 每个写请求一行：{side, op, method, path, status, code, ms, ttfb, at, end, expected}。 */
const writes = []
/** 预期的冲突（只认一次）：{side, code, path}。 */
let expected409 = null
/** 只读的 POST（不算写操作，但 409、5xx、失败照样算）。 */
const READ_POSTS = [/\/api\/files\/batch$/, /\/catalog-placements$/]

function opOf(method, p, body) {
  const m = (re) => re.test(p)
  if (method === 'POST' && m(/^\/api\/files\/[^/]+\/review$/)) return body?.result === 'rejected' ? '驳回' : '通过'
  if (method === 'POST' && m(/\/reviews:approve$/)) return '批量通过'
  if (method === 'POST' && m(/^\/api\/files\/[^/]+\/reopen$/)) return '撤回通过'
  if (method === 'POST' && m(/\/review-assignments$/)) return '分配'
  if (method === 'POST' && m(/^\/api\/archive-categories\/[^/]+\/items$/)) return body?.file_ids?.length ? '新建条目（带页）' : '新建条目'
  if (method === 'PATCH' && m(/^\/api\/archive-items\/[^/]+$/)) return '保存著录'
  if (method === 'PUT' && m(/^\/api\/archive-items\/[^/]+\/files$/)) return '页序（挂接、排序、移出）'
  if (method === 'POST' && m(/^\/api\/archive-items\/[^/]+\/mark-done$/)) return '标记完成'
  if (method === 'POST' && m(/^\/api\/archive-items\/[^/]+\/lock$/)) return '定稿'
  if (method === 'POST' && m(/^\/api\/archive-items\/[^/]+\/unlock$/)) return '解锁'
  if (method === 'POST' && m(/\/archive-items:lock$/)) return '批量定稿'
  if (method === 'POST' && m(/^\/api\/files\/[^/]+\/rescan$/)) return '重扫'
  if (method === 'DELETE' && m(/^\/api\/folders\/[^/]+$/)) return '删除文件夹'
  if (READ_POSTS.some((re) => re.test(p))) return '（只读）' + p.split('/').at(-1)
  return `${method} ${p.replace(/[0-9a-f]{8}-[0-9a-f-]{27}/g, '…')}`
}

/** 记下这个页面发出的写请求（GET 和登录以外的 /api 请求）。同一个页面只挂一次（截图脚本按账号复用页面）。 */
const tracked = new WeakSet()
function trackWrites(page, side) {
  if (tracked.has(page)) return
  tracked.add(page)
  const started = new Map()
  page.on('request', (req) => {
    let u
    try {
      u = new URL(req.url())
    } catch {
      return
    }
    if (!u.pathname.startsWith('/api/') || ['GET', 'HEAD', 'OPTIONS'].includes(req.method()) || u.pathname.startsWith('/api/auth/')) return
    let body = null
    try {
      body = JSON.parse(req.postData() ?? 'null')
    } catch {
      body = null
    }
    started.set(req, { side, method: req.method(), path: u.pathname, op: opOf(req.method(), u.pathname, body), at: Date.now(), t0: performance.now() })
  })
  const done = async (req, failed) => {
    const r = started.get(req)
    if (!r) return
    started.delete(req)
    r.ms = Math.round(performance.now() - r.t0)
    r.end = Date.now()
    delete r.t0
    const res = req.response()
    r.status = failed ? 0 : (res?.status() ?? 0)
    if (failed) r.error = req.failure()?.errorText ?? 'failed'
    const t = res?.timing?.()
    if (t && t.receiveHeadersEnd > 0 && t.sendEnd >= 0) r.ttfb = Math.round(t.receiveHeadersEnd - t.sendEnd)
    if (r.status >= 400) {
      try {
        r.code = JSON.parse(await res.text()).code
      } catch {
        r.code = '?'
      }
    }
    if (r.status === 409 && expected409 && !expected409.hit && expected409.side === side && r.code === expected409.code && r.path.includes(expected409.path)) {
      r.expected = true
      expected409.hit = true
    }
    writes.push(r)
  }
  page.on('requestfinished', (req) => void done(req, false))
  page.on('requestfailed', (req) => void done(req, true))
}

/** 不正常的写请求：预期之外的 409、其他 4xx、5xx、失败、超过 SLOW_MS。 */
function badWrites(list = writes) {
  return list.filter((w) => (w.status >= 400 && !w.expected) || w.status === 0 || w.ms > SLOW_MS)
}

function median(xs) {
  const s = [...xs].sort((a, b) => a - b)
  if (!s.length) return null
  const mid = Math.floor(s.length / 2)
  return s.length % 2 ? s[mid] : Math.round((s[mid - 1] + s[mid]) / 2)
}

/** 按 side、操作类型汇总：次数、中位数、最慢（毫秒，浏览器里从发出到收完），另给 TTFB 的中位数和最慢。 */
function writeStats(list) {
  const groups = new Map()
  for (const w of list) {
    if (w.op.startsWith('（只读）')) continue
    const k = `${w.side}｜${w.op}`
    if (!groups.has(k)) groups.set(k, [])
    groups.get(k).push(w)
  }
  const out = {}
  for (const [k, ws] of groups) {
    const ok = ws.filter((w) => w.status > 0 && w.status < 400)
    const slowest = ok.reduce((a, w) => (!a || w.ms > a.ms ? w : a), null)
    out[k] = {
      n: ws.length,
      median: median(ok.map((w) => w.ms)),
      max: slowest?.ms ?? null,
      ttfb_median: median(ok.filter((w) => w.ttfb !== undefined).map((w) => w.ttfb)),
      ttfb_max: Math.max(...ok.filter((w) => w.ttfb !== undefined).map((w) => w.ttfb), 0),
      expected_409: ws.filter((w) => w.expected).length,
    }
  }
  return out
}

function printStats(title, stats) {
  console.log(`  ${title}（浏览器里从发出到收完，毫秒；括号里是 TTFB）：`)
  for (const [k, s] of Object.entries(stats)) {
    const t = s.median === null ? '（只有预期的错误响应）' : `中位 ${s.median} 最慢 ${s.max}（TTFB 中位 ${s.ttfb_median} 最慢 ${s.ttfb_max}）`
    console.log(`    ${k.padEnd(22)} ${String(s.n).padStart(3)} 次  ${t}${s.expected_409 ? `  预期的 409 ×${s.expected_409}` : ''}`)
  }
}

// ---------- 页面：通用 ----------

async function waitToast(page, text, timeout = 10000) {
  await page.waitForFunction(
    (t) => [...document.querySelectorAll('[data-sonner-toast]')].some((el) => el.textContent?.includes(t)),
    { timeout },
    text,
  )
}

async function waitNoToast(page, timeout = 10000) {
  await page.waitForFunction(() => !document.querySelector('[data-sonner-toast]'), { timeout }).catch(() => {})
}

async function toastText(page, text) {
  return page.$$eval('[data-sonner-toast]', (els, t) => els.map((e) => e.textContent?.replace(/\s+/g, ' ').trim() ?? '').find((x) => x.includes(t)) ?? '', text)
}

/** 把鼠标移开（悬停效果不进截图），焦点还给页面。 */
async function rest(page) {
  await page.mouse.move(5, 895)
  await page.evaluate(() => (document.activeElement instanceof HTMLElement ? document.activeElement.blur() : null))
}

async function waitCovers(page, timeout = 15000) {
  await page.waitForFunction(
    () => [...document.querySelectorAll('[data-testid=file-cover]')].every((c) => !['loading', 'loading-image'].includes(c.dataset.coverState)),
    { timeout },
  )
}

async function alertText(page) {
  await page.waitForSelector('[role=alertdialog]', { visible: true, timeout: 5000 })
  await sleep(250)
  return page.$eval('[role=alertdialog]', (el) => el.textContent ?? '')
}

async function confirmAlert(page) {
  await page.waitForSelector('[role=alertdialog]', { visible: true, timeout: 5000 })
  await sleep(200)
  const buttons = await page.$$('[role=alertdialog] button')
  await buttons[buttons.length - 1].click()
  await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 10000 })
}

/** 截图里有未保存的修改时离开页面会弹 beforeunload：一律接受。 */
const dialogHooked = new WeakSet()
function acceptDialogs(page) {
  if (dialogHooked.has(page)) return
  dialogHooked.add(page)
  page.on('dialog', (d) => void d.accept().catch(() => {}))
}

/** 另一个浏览器上下文（独立的 Cookie）登录一个账号，一个页面。 */
async function openWindow(page, username, password = demoPassword) {
  const ctx = await context(page)
  const bc = await page.browser().createBrowserContext()
  const first = await bc.newPage()
  await first.setViewport({ width: 1440, height: 900 })
  acceptDialogs(first)
  await first.goto(`${ctx.origin}/login`, { waitUntil: 'networkidle0' })
  await first.type('#username', username)
  await first.type('#password', password)
  await Promise.all([first.waitForFunction(() => !location.pathname.startsWith('/login'), { timeout: 15000 }), first.click('[data-testid=login-submit]')])
  const win = { page: first, close: () => bc.close().catch(() => {}) }
  state.windows.push(win)
  return win
}

async function closeWindows() {
  for (const w of state.windows.splice(0)) await w.close()
}

async function shot(page, name) {
  await sleep(300)
  await page.screenshot({ path: path.join(OUT_DIR, `${name}.png`) })
}

// ---------- /review-assign ----------

async function openAssign(page, folderId) {
  const ctx = await context(page)
  await page.goto(`${ctx.origin}/review-assign?folder=${folderId}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=review-assign-page][data-list=ready][data-stream=open]', { timeout: 15000 })
  await page.waitForSelector('[data-testid=reviewer-row]', { timeout: 10000 })
  await waitCovers(page)
}

// ---------- /review ----------

async function openReview(page, folderId, { tab } = {}) {
  const ctx = await context(page)
  await page.goto(`${ctx.origin}/review?folder=${folderId}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=review-page][data-list=ready][data-stream=open]', { timeout: 15000 })
  if (tab) {
    await page.click(`[data-testid=review-tab][data-state=${tab}]`)
    await page.waitForSelector(`[data-testid=review-tab][data-state=${tab}][aria-checked=true]`, { timeout: 3000 })
    await page.waitForSelector('[data-testid=review-page][data-list=ready]', { timeout: 10000 })
  }
}

/** 等当前文件（或指定的文件）的原图状态是 ready。 */
async function waitReady(page, fileId, timeout = 30000) {
  try {
    await page.waitForFunction(
      (fid) => {
        const el = document.querySelector('[data-testid=review-page]')
        if (!el || !el.dataset.current) return false
        if (fid && el.dataset.current !== fid) return false
        return el.dataset.phase === 'ready'
      },
      { timeout, polling: 50 },
      fileId ?? null,
    )
  } catch (e) {
    const st = await page.evaluate(() => ({ ...document.querySelector('[data-testid=review-page]')?.dataset }))
    throw new Error(`等 ${fileId ?? '当前文件'} 的原图超时：${JSON.stringify(st)}（${e.message}）`)
  }
}

/**
 * 只用键盘审完 items（按列表顺序，每次审完自动跳到下一张待审）：rejects 里的按 R → 数字 → [Tab → 说明] → Enter，
 * 其余按 A。每张原图显示出来之后停 pace 毫秒再按键（人的节奏）。返回开始、结束的时间。
 */
async function keyboardReview(page, items, rejects, pace = 0) {
  const started = Date.now()
  for (let i = 0; i < items.length; i++) {
    const f = items[i]
    await waitReady(page, f.file_id)
    if (pace) await sleep(pace)
    const rj = rejects[f.key]
    if (rj) {
      await page.keyboard.press('r')
      await page.waitForSelector('[data-testid=review-actions][data-panel=open]', { timeout: 3000 })
      for (const k of rj.keys) await page.keyboard.press(k)
      if (rj.comment) {
        await page.keyboard.press('Tab')
        await page.keyboard.type(rj.comment)
      }
      await page.keyboard.press('Enter')
    } else {
      await page.keyboard.press('a')
    }
    if (i < items.length - 1) {
      await page.waitForSelector(`[data-testid=review-page][data-current="${items[i + 1].file_id}"]`, { timeout: 8000 })
    } else {
      await waitToast(page, '都审完了')
    }
  }
  return { started, ended: Date.now() }
}

/** 我的审核（接口）里这个文件夹子树的 open 项，按列表顺序。 */
async function openItems(ctx, call, folderId) {
  const r = await call('GET', `/api/me/reviews?project_id=${ctx.pid}&folder_id=${folderId}&state=open&limit=2000`)
  return r.items.map((it) => {
    const key = Object.keys(ctx.files).find((k) => ctx.files[k].file_id === it.file_id)
    assert(key, `待办里有演示文件夹之外的文件 ${it.file_name}`)
    return ctx.files[key]
  })
}

// ---------- /scan 的待重扫列表 ----------

async function waitRescanList(page) {
  await page.waitForSelector('[data-testid=rescan-list][data-state=ready]', { timeout: 15000 })
}

/** 在待重扫列表上重扫一页（注入一张新扫的 PNG，和选择框同一段代码），返回对话框的结果。 */
async function rescanOnPage(page, f, name, png) {
  await waitRescanList(page)
  const item = `[data-testid=rescan-item][data-file-id="${f.file_id}"]`
  await page.waitForSelector(item, { timeout: 8000 })
  await page.$eval(item, (el) => el.scrollIntoView({ block: 'center' }))
  await page.click(`${item} [data-testid=rescan-item-upload]`)
  await page.waitForSelector(`[data-testid=rescan-dialog][data-file-id="${f.file_id}"] [data-testid=rescan-input]`, { timeout: 5000 })
  await page.evaluate(
    (n, b64) => {
      const bin = atob(b64)
      const bytes = new Uint8Array(bin.length)
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
      document.querySelector('[data-testid=rescan-input]').dispatchEvent(new CustomEvent('doclite-inject-files', { detail: [new File([bytes], n, { type: 'image/png' })] }))
    },
    name,
    png.toString('base64'),
  )
  await page.waitForSelector('[data-testid=rescan-dialog] [data-testid=rescan-result]', { timeout: 30000 })
  return page.$eval('[data-testid=rescan-dialog]', (d) => ({
    result: d.querySelector('[data-testid=rescan-result]')?.dataset.result,
    title: d.querySelector('[data-testid=rescan-result-title]')?.textContent.replace(/\s+/g, ' ').trim(),
    reassign: d.querySelector('[data-testid=rescan-reassign]')?.textContent.trim() ?? null,
  }))
}

async function closeRescanDialog(page) {
  await page.click('[data-testid=rescan-dialog] [data-testid=rescan-close]')
  await page.waitForFunction(() => !document.querySelector('[data-testid=rescan-dialog]') && document.body.style.pointerEvents !== 'none', { timeout: 5000 })
  await sleep(200)
}

/** 重扫之后核对：第 version 个快照是 rescan、upload_name、（给了时）sha；第 round 轮、自动分回审核员、rescan 日志。 */
async function verifyRescan(ctx, f, { uploadName, version, sha = null }) {
  const snaps = await snapshotsOf(ctx, f.file_id)
  const last = snaps.at(-1)
  assert(snaps.length === version && last.version_no === version && last.snapshot_type === 'rescan', `${f.key} 的快照：${snaps.map((s) => `v${s.version_no} ${s.snapshot_type}`)}`)
  assert(last.upload_name === uploadName, `${f.key} v${version} 的 upload_name：${last.upload_name}`)
  assert(!!last.cover_url, `${f.key} v${version} 没有封面`)
  if (sha) assert(last.content_sha256 === sha, `${f.key} v${version} 的 sha 不等于本地文件：${last.content_sha256}`)
  const r = await reviewsOf(ctx, f.file_id)
  const open = r.reviews.filter((x) => x.status === 'open')
  assert(r.review_round === version && r.version_no === version, `${f.key}：第 ${r.review_round} 轮、v${r.version_no}`)
  assert(r.review_status === 'pending' && open.length === 1 && open[0].reviewer_id === ctx.id(REVIEWER), `${f.key} 应该自动分回 ${REVIEWER}：${r.review_status} ${JSON.stringify(open)}`)
  const log = (await logsOf(ctx, f.file_id))[0]
  assert(log.action === 'rescan' && log.detail.version_no === version && log.detail.review_round === version && log.detail.upload_name === uploadName, `${f.key} rescan 日志：${JSON.stringify(log)}`)
  return last
}

// ---------- 打包的壳（--smoke） ----------

/** 跑一次打包的壳 `--smoke`（隔离 HOME），返回 {code, outcome, ms, steps}。 */
async function runShell(label, env) {
  assert(SHELL, '没有设置 M3_ACCEPT_SHELL')
  const home = shotsTmp('m3-accept-shell-home')
  const child = spawn(SHELL, ['--smoke'], {
    env: {
      ...process.env,
      HOME: home,
      DOCLITE_DATA_DIR: path.join(home, 'Doclite'),
      // 后台模式：不激活、不进 Dock，不抢用户的焦点。
      DOCLITE_BACKGROUND: '1',
      DOCLITE_SMOKE_SERVER: process.env.M3_ACCEPT_SERVER ?? state.origin,
      DOCLITE_SMOKE_USERNAME: SCANNER,
      DOCLITE_SMOKE_PASSWORD: demoPassword,
      DOCLITE_SMOKE_TIMEOUT_SECS: '300',
      DOCLITE_SMOKE_IMG_PROJECT: PROJECT,
      // 上一版的「M1-11 缓存冒烟」Go 版的库里没有，取图缓存一步用 M2-3 的样本。
      DOCLITE_SMOKE_IMG_FOLDER: 'M2-3 分块冒烟',
      // tiles、viewer 两步这里不需要（M2-8 验过）：viewer 要求窗口看得见，窗口被挡住或锁屏时 WebKit 停掉页面的定时器，
      // 整个自检卡到超时。--smoke 没有跳过某一步的开关，所以给一个没有 TIFF 的文件夹，让 tiles 立即失败、viewer 没有
      // 目标也立即失败（两步都只记下来、不中断），后面的 ui、scan_page、rescan 照常跑。
      DOCLITE_SMOKE_TILES_FOLDER: 'M1-11 缓存冒烟',
      ...env,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  let out = ''
  let err = ''
  child.stdout.on('data', (b) => (out += b))
  child.stderr.on('data', (b) => (err += b))
  const t0 = Date.now()
  const code = await new Promise((resolve) => child.on('exit', (c) => resolve(c)))
  const line = out.trim().split('\n').filter((l) => l.startsWith('{')).at(-1)
  writeFileSync(path.join(shotsTmp('m3-accept-shell-out'), `${label.replace(/[^\p{L}\p{N}.-]+/gu, '_')}.json`), `${out}\n---- stderr ----\n${err}`)
  let report = null
  try {
    report = JSON.parse(line)
  } catch {
    throw new Error(`壳 --smoke 没有输出结果（退出码 ${code}）：${err.slice(-500)}`)
  }
  const run = { label, code, outcome: report.outcome, ms: Date.now() - t0, steps: report.steps }
  state.shellRuns.push(run)
  const failed = (report.steps ?? []).filter((s) => !s.ok).map((s) => s.name)
  console.log(`  壳 --smoke「${label}」：退出码 ${code}（${report.outcome}），${(run.ms / 1000).toFixed(1)} 秒${failed.length ? `；没通过的步骤：${failed.join('、')}（tiles、viewer 是有意跳过的，见 runShell）` : ''}`)
  assert(report.steps, `壳 --smoke 没有跑完：${report.error}`)
  const unexpected = failed.filter((n) => n !== 'tiles' && n !== 'viewer')
  assert(!unexpected.length, `壳 --smoke 有别的步骤没通过：${JSON.stringify(report.steps.filter((s) => unexpected.includes(s.name)))}`)
  return run
}

/** 用壳重扫一页（没给 M3_ACCEPT_SHELL 时照壳的字段用接口重扫），返回 {name, sha, ms, where}。 */
async function shellRescan(ctx, f, label, version) {
  const name = `${f.file_name.replace('.png', '')} 壳重扫 v${version}.png`
  const p = newPage(`壳重扫 ${f.key} v${version}`, Number(f.file_name.slice(4, 8)))
  const local = path.join(shotsTmp('m3-accept-rescan-files'), name)
  writeFileSync(local, p.png)
  const sha = createHash('sha256').update(p.png).digest('hex')
  if (SHELL) {
    const run = await runShell(label, {
      DOCLITE_SMOKE_RESCAN_PROJECT: PROJECT,
      DOCLITE_SMOKE_RESCAN_FILE_ID: f.file_id,
      DOCLITE_SMOKE_RESCAN_PATH: local,
      DOCLITE_SMOKE_RESCAN_EXPECT: 'done',
    })
    const s = run.steps.find((x) => x.name === 'rescan')
    assert(s?.ok, `壳的 rescan 一步没通过：${JSON.stringify(s)}`)
    console.log(`    rescan 一步（${s.ms} ms）：${JSON.stringify(s.detail).slice(0, 300)}`)
    return { name, sha, ms: s.ms, where: '壳 --smoke' }
  }
  const form = new FormData()
  form.append('file_name', name)
  form.append('content_sha256', sha)
  form.append('cover', new Blob([p.cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([p.png], { type: 'image/png' }), 'page.png')
  const t0 = Date.now()
  await ctx.scanner('POST', `/api/files/${f.file_id}/rescan`, form)
  console.log(`  （没有设置 M3_ACCEPT_SHELL：${f.key} 改用接口按壳的字段重扫）`)
  return { name, sha, ms: Date.now() - t0, where: '接口（模拟壳）' }
}

// ---------- /catalog ----------

async function openWorkbench(page, { category, item, pool } = {}) {
  acceptDialogs(page)
  const ctx = await context(page)
  const q = new URLSearchParams()
  if (category) q.set('category', category)
  if (item) q.set('item', item)
  if (pool) q.set('pool', pool)
  await page.goto(`${ctx.origin}/catalog?${q}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=catalog-page][data-tree=ready]', { timeout: 10000 })
  if (category) await page.waitForSelector('[data-testid=catalog-page][data-list=ready]', { timeout: 10000 })
  if (item) await waitDetail(page, item)
  if (pool) await waitPool(page, pool)
  await page.waitForFunction(() => document.querySelector('[data-testid=catalog-page]')?.dataset.stream === 'open', { timeout: 10000 })
}

async function waitDetail(page, itemId) {
  await page.waitForSelector(`[data-testid=item-detail][data-item-id="${itemId}"][data-load=ready]`, { timeout: 10000 })
}

/** 等详情换成一个新的条目（不在 exclude 里），返回它的 id。 */
async function waitNewDetail(page, exclude) {
  const h = await page.waitForFunction(
    (ex) => {
      const el = document.querySelector('[data-testid=item-detail][data-load=ready]')
      return el && el.dataset.itemId && !ex.includes(el.dataset.itemId) ? el.dataset.itemId : null
    },
    { timeout: 10000 },
    exclude,
  )
  return h.jsonValue()
}

async function waitPool(page, folderId) {
  await page.waitForSelector(`[data-testid=catalog-pool][data-folder-id="${folderId}"][data-state=ready]`, { timeout: 10000 })
}

const stripSel = (fid) => `[data-testid=item-pages] [data-testid=item-page][data-file-id="${fid}"]`

/** 页序条上渲染出来的页（按页号）。 */
function stripCells(page) {
  return page.$$eval('[data-testid=item-pages] [data-testid=item-page]', (els) =>
    els
      .map((e) => ({
        fileId: e.dataset.fileId,
        pageNo: Number(e.dataset.pageNo),
        status: e.dataset.reviewStatus,
        version: Number(e.dataset.version),
        round: Number(e.dataset.round),
        newVersion: e.dataset.newVersion === 'true',
        text: e.textContent.replace(/\s+/g, ' ').trim(),
      }))
      .sort((a, b) => a.pageNo - b.pageNo),
  )
}

/** 等页序条的页（按页号）正好是 ids。 */
async function waitStrip(page, ids, timeout = 5000) {
  await page.waitForFunction(
    (want) => {
      const box = document.querySelector('[data-testid=item-pages]')
      if (!box || box.dataset.busy === 'true' || Number(box.dataset.count) !== want.length) return false
      const got = [...box.querySelectorAll('[data-testid=item-page]')].filter((e) => e.dataset.mark !== 'removed')
      if (!got.length) return want.length === 0
      return got.every((e) => want[Number(e.dataset.pageNo) - 1] === e.dataset.fileId)
    },
    { timeout, polling: 50 },
    ids,
  )
}

/** 等页序条上某一页的属性（version、round、status、newVersion）符合 want，返回用了多少毫秒。 */
async function waitStripPage(page, fid, want, timeout = LIVE_MS) {
  const t0 = Date.now()
  await page.waitForFunction(
    (sel, w) => {
      const e = document.querySelector(sel)
      if (!e) return false
      if (w.version !== undefined && Number(e.dataset.version) !== w.version) return false
      if (w.round !== undefined && Number(e.dataset.round) !== w.round) return false
      if (w.status !== undefined && e.dataset.reviewStatus !== w.status) return false
      if (w.newVersion !== undefined && (e.dataset.newVersion === 'true') !== w.newVersion) return false
      return true
    },
    { timeout, polling: 50 },
    stripSel(fid),
    want,
  )
  return Date.now() - t0
}

async function rangeSelect(page, a, b) {
  const set = async (sel, v) =>
    page.$eval(
      sel,
      (el, val) => {
        el.value = val
        el.dispatchEvent(new Event('input', { bubbles: true }))
      },
      String(v),
    )
  await set('[data-testid=pool-range-from]', a)
  await set('[data-testid=pool-range-to]', b)
  await page.click('[data-testid=pool-range-apply]')
  await page.waitForSelector(`[data-testid=catalog-pool][data-range="${a}-${b}"][data-selected="${b - a + 1}"]`, { timeout: 3000 })
}

async function clickStripPage(page, fileId) {
  await page.waitForSelector(stripSel(fileId), { timeout: 5000 })
  await page.click(stripSel(fileId))
}

async function rowVersion(page) {
  return page.$eval('[data-testid=item-detail]', (el) => Number(el.dataset.rowVersion))
}

/** 给表单项设值：input、textarea 发 input 事件，select 用 page.select。 */
async function setField(page, key, value) {
  const sel = key === 'title' ? '[data-testid=item-title]' : `[data-testid=item-field][data-key="${key}"] [data-testid=item-field-input]`
  const tag = await page.$eval(sel, (el) => el.tagName)
  if (tag === 'SELECT') {
    await page.select(sel, value)
    return
  }
  await page.$eval(
    sel,
    (el, v) => {
      el.value = v
      el.dispatchEvent(new Event('input', { bubbles: true }))
    },
    value,
  )
}

/** 在表单里填题名和著录项，点"保存著录"，等保存完（row_version 前进、没有未保存的修改）。 */
async function fillAndSave(page, spec) {
  await setField(page, 'title', spec.title)
  for (const [k, v] of Object.entries(spec.fields)) await setField(page, k, v)
  const rv = await rowVersion(page)
  await page.click('[data-testid=item-save]')
  await page.waitForFunction(
    (v) => {
      const d = document.querySelector('[data-testid=item-detail]')
      return d && Number(d.dataset.rowVersion) > v && !d.dataset.dirty
    },
    { timeout: 8000 },
    rv,
  )
}

async function markDone(page) {
  await page.click('[data-testid=item-mark-done]')
  await page.waitForSelector('[data-testid=item-detail][data-status=done]', { timeout: 8000 })
}

async function lockUi(page) {
  await page.waitForSelector('[data-testid=item-lock]:not([disabled])', { timeout: 5000 })
  await page.click('[data-testid=item-lock]')
  await confirmAlert(page)
  await page.waitForSelector('[data-testid=item-detail][data-status=locked]', { timeout: 8000 })
}

/** 点归类树上的节点（同一层的兄弟在树上看得见），等条目列表出来。 */
async function clickCategory(page, categoryId) {
  const sel = `[data-testid=tree-category][data-category-id="${categoryId}"]`
  await page.waitForSelector(sel, { visible: true, timeout: 5000 })
  await page.click(sel)
  await page.waitForFunction((id) => new URL(location.href).searchParams.get('category') === id, { timeout: 5000 }, categoryId)
  await page.waitForSelector('[data-testid=catalog-page][data-list=ready]', { timeout: 10000 })
}

/** 池换一个文件夹：点页头的文件夹名展开目录树，点目标文件夹（同一层的兄弟）。 */
async function switchPool(page, folderId) {
  await page.click('[data-testid=pool-folder-toggle]')
  const sel = `[data-testid=pool-tree] [data-testid=tree-folder][data-folder-id="${folderId}"]`
  await page.waitForSelector(sel, { visible: true, timeout: 8000 })
  await sleep(150)
  await page.click(sel)
  await waitPool(page, folderId)
}

async function setOnlyUncataloged(page, on, count) {
  const cur = await page.$eval('[data-testid=catalog-pool]', (el) => el.dataset.onlyUncataloged)
  if ((cur === 'true') !== on) await page.click('[data-testid=pool-only-uncataloged]')
  await page.waitForSelector(`[data-testid=catalog-pool][data-only-uncataloged="${on}"][data-count="${count}"][data-state=ready]`, { timeout: 5000 })
}

/** 元素中心点（先滚进可见范围）。 */
async function centerOf(page, sel) {
  const el = await page.waitForSelector(sel, { timeout: 5000 })
  await el.evaluate((e) => e.scrollIntoView({ block: 'nearest' }))
  const box = await el.boundingBox()
  return { x: box.x + box.width / 2, y: box.y + box.height / 2, box }
}

/** 浏览器原生拖放（puppeteer 的鼠标在 headless Chrome 里走真正的 dragstart → dragover → drop → dragend）。 */
async function dragAndDrop(page, from, to) {
  await page.mouse.move(from.x, from.y)
  await page.mouse.down()
  await page.mouse.move(from.x + 8, from.y + 8, { steps: 4 })
  await page.mouse.move(to.x, to.y, { steps: 12 })
  await sleep(150)
  await page.mouse.move(to.x + 1, to.y, { steps: 2 })
  await sleep(150)
  await page.mouse.up()
}

/** 页序条上第 gap 个空隙（第 gap 页和第 gap+1 页之间）的坐标：取第 gap+1 页的左边缘。 */
async function gapPoint(page, ids, gap) {
  if (gap < ids.length) {
    const c = await centerOf(page, stripSel(ids[gap]))
    return { x: c.box.x + 2, y: c.y }
  }
  const c = await centerOf(page, stripSel(ids[ids.length - 1]))
  return { x: c.box.x + c.box.width + 4, y: c.y }
}

/**
 * 把页序条上第 from 页（0 起）拖到第 gap 个空隙，等页序变成 want。审核那边的事件会让页序条重新渲染，拖到一半元素可能
 * 换掉：没变化（接口里还是原来的顺序）就再拖一次，最多 3 次；变成了别的顺序直接失败。返回拖了几次。
 */
async function dragPage(page, ctx, itemId, ids, from, gap, want) {
  for (let attempt = 1; ; attempt++) {
    // 先单击这一页只选中它（刚挂上的页全是选中的，拖选中的页会把选中的一起拖走）。
    await clickStripPage(page, ids[from])
    await page.waitForSelector('[data-testid=item-pages][data-selected="1"]', { timeout: 3000 })
    await dragAndDrop(page, await centerOf(page, stripSel(ids[from])), await gapPoint(page, ids, gap))
    try {
      await waitStrip(page, want, 5000)
      return attempt
    } catch (e) {
      const now = pageIds(await getItem(ctx, itemId))
      if (sameList(now, want)) {
        await waitStrip(page, want, 5000)
        return attempt
      }
      if (!sameList(now, ids) || attempt >= 3) throw new Error(`拖拽后页序不对（第 ${attempt} 次）：${now.map((x) => keyOf(ctx, x)).join(',')}（${e.message}）`)
      console.log(`    第 ${attempt} 次拖拽没有生效（页序条刚好重新渲染），再拖一次`)
      await sleep(300)
    }
  }
}

// ---------- /dashboard ----------

async function openDashboard(page) {
  const ctx = await context(page)
  await page.goto(`${ctx.origin}/dashboard`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=dashboard-page][data-state=ready][data-stream=open]', { timeout: 15000 })
  await page.waitForSelector('[data-testid=kpi]')
}

function readDashboard(page) {
  return page.evaluate(() => {
    const kpi = {}
    for (const el of document.querySelectorAll('[data-testid=kpi]')) {
      kpi[el.dataset.kind] = Number(el.dataset.value)
      if (el.dataset.total !== undefined) kpi[`${el.dataset.kind}_total`] = Number(el.dataset.total)
    }
    const matrix = {}
    for (const el of document.querySelectorAll('[data-testid=matrix-cell]')) matrix[`${el.dataset.review}.${el.dataset.catalog}`] = Number(el.dataset.value)
    return { kpi, matrix }
  })
}

/** 接口的 counts 应该在看板上显示成什么样（同 m3-dashboard.mjs）。 */
function expectedDashboard(c) {
  const kpi = {
    total: c.total,
    approved: c.review.approved,
    rejected: c.review.rejected,
    cataloged: c.catalog.cataloged,
    locked: c.catalog.locked,
    exportable: c.exportable.approved,
    exportable_total: c.exportable.total,
  }
  const matrix = {}
  for (const r of ['unassigned', 'pending', 'approved', 'rejected']) {
    for (const col of ['uncataloged', 'unlocked', 'locked', 'total']) matrix[`${r}.${col}`] = c.matrix[r][col]
    matrix[`${r}.exportable`] = c.exportable[r]
  }
  for (const col of ['uncataloged', 'unlocked', 'locked']) matrix[`total.${col}`] = c.catalog[col]
  matrix['total.total'] = c.total
  matrix['total.exportable'] = c.exportable.total
  return { kpi, matrix }
}

/** 等看板（整个项目）和接口 catalog-summary 一致，返回用了多少毫秒；超过 timeout 报出差异。 */
async function waitDashboard(page, ctx, label, timeout = LIVE_MS) {
  const t0 = Date.now()
  let want = null
  let wantAt = 0
  let last = []
  for (;;) {
    if (!want || Date.now() - wantAt > 500) {
      want = expectedDashboard((await ctx.mgr('GET', `/api/projects/${ctx.pid}/catalog-summary`)).counts)
      wantAt = Date.now()
    }
    const got = await readDashboard(page)
    last = []
    for (const [k, v] of Object.entries(want.kpi)) if (got.kpi[k] !== v) last.push(`KPI ${k}: 页面 ${got.kpi[k]}，接口 ${v}`)
    for (const [k, v] of Object.entries(want.matrix)) if (got.matrix[k] !== v) last.push(`矩阵 ${k}: 页面 ${got.matrix[k]}，接口 ${v}`)
    if (!last.length) {
      const ms = Date.now() - t0
      state.live[label] = ms
      return ms
    }
    if (Date.now() - t0 > timeout) throw new Error(`「${label}」之后 ${timeout} ms 内看板没有和接口一致：${last.slice(0, 6).join('；')}`)
    await sleep(100)
  }
}

// ---------- 归类员在 /catalog 的整套操作（和审核同时进行） ----------

async function catalogFlow(cp, ctx) {
  const box1 = boxFiles(ctx, '盒 01').map((f) => f.file_id)
  const box2 = boxFiles(ctx, '盒 02').map((f) => f.file_id)
  const box3 = boxFiles(ctx, '盒 03').map((f) => f.file_id)
  const B = file(ctx, FILE_B).file_id
  const log = async (s) => {
    console.log(`    [归类 ${((Date.now() - state.parallel.t0) / 1000).toFixed(1)}s] ${s}`)
    await sleep(THINK_MS)
  }
  const out = { started: Date.now() }

  // ---- 第 1 件：新建 → 挂第 1–10 张 → 拖第 2 页到第 5、6 页之间 → 著录、保存 → 标记完成 ----
  await cp.click('[data-testid=item-new]')
  const i1 = await waitNewDetail(cp, [])
  out.i1 = i1
  await log(`办公室"新建"第 1 件（${i1.slice(-6)}）`)
  await rangeSelect(cp, 1, 10)
  await cp.click('[data-testid=pool-link-end]')
  const p1 = box1.slice(0, 10)
  await waitStrip(cp, p1, 10000)
  await log('"第 1–10 张"→"挂到末尾"：页序条 10 页')
  const want1 = [p1[0], p1[2], p1[3], p1[4], p1[1], ...p1.slice(5)]
  out.dragAttempts = await dragPage(cp, ctx, i1, p1, 1, 5, want1)
  await log(`把第 2 页拖到第 5、6 页之间（${out.dragAttempts} 次）`)
  await fillAndSave(cp, ITEMS.i1)
  await markDone(cp)
  await log('著录、保存、标记完成')

  // ---- 第 2 件："第 1–10 张"（IMG_0011–0020）新建 → 著录、保存 → 把 IMG_0010 从第 1 件移过来 → 移到第 1 页 → 标记完成 ----
  await rangeSelect(cp, 1, 10)
  await cp.click('[data-testid=pool-create-item]')
  const i2 = await waitNewDetail(cp, [i1])
  out.i2 = i2
  const p2 = box1.slice(10, 20)
  await waitStrip(cp, p2, 10000)
  await log(`"第 1–10 张"→"用选中的文件新建条目"：第 2 件（${i2.slice(-6)}）10 页`)
  await fillAndSave(cp, ITEMS.i2)
  // 关掉"仅未归类"，把第 1 件里的 IMG_0010 挂过来：409 file_cataloged（预期的冲突）→ 对话框"从原条目移出"→ 自动重试。
  await setOnlyUncataloged(cp, false, 30)
  expected409 = { side: '归类', code: 'file_cataloged', path: `/api/archive-items/${i2}/files` }
  await cp.click(`[data-testid=catalog-pool] [data-testid=file-cell][data-file-id="${B}"]`)
  await cp.waitForSelector('[data-testid=catalog-pool][data-selected="1"]', { timeout: 3000 })
  await cp.click('[data-testid=pool-link-end]')
  await cp.waitForSelector('[data-testid=file-cataloged-dialog]', { visible: true, timeout: 5000 })
  const group = await cp.$eval(`[data-testid=cataloged-group][data-item-id="${i1}"]`, (e) => e.textContent.replace(/\s+/g, ' ').trim())
  out.dialog = group
  await shot(cp, 'm3-accept-move-dialog')
  await cp.click(`[data-testid=cataloged-group][data-item-id="${i1}"] [data-testid=cataloged-move-out]`)
  await cp.waitForSelector('[data-testid=file-cataloged-dialog]', { hidden: true, timeout: 10000 })
  await waitStrip(cp, [...p2, B], 8000)
  await log(`关掉"仅未归类"把 IMG_0010 挂过来：对话框「${group.slice(0, 60)}」→"从原条目移出"→ 自动重试，第 2 件 11 页`)
  await setOnlyUncataloged(cp, true, 10)
  await clickStripPage(cp, B)
  await cp.$eval('[data-testid=page-move-to-input]', (el) => {
    el.value = '1'
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await cp.click('[data-testid=page-move-to-submit]')
  await waitStrip(cp, [B, ...p2], 5000)
  await markDone(cp)
  await log('IMG_0010"移到第 1 页"、标记完成')

  // ---- 人事处第 1 件：池换到盒 02，"第 1–15 张"新建 → 著录、保存（留作草稿） ----
  await clickCategory(cp, ctx.cats.rsc.id)
  await switchPool(cp, ctx.boxes['盒 02'].id)
  await rangeSelect(cp, 1, 15)
  await cp.click('[data-testid=pool-create-item]')
  const i3 = await waitNewDetail(cp, [i1, i2])
  out.i3 = i3
  await waitStrip(cp, box2.slice(0, 15), 10000)
  await fillAndSave(cp, ITEMS.i3)
  await log(`人事处、池换到盒 02："第 1–15 张"新建第 1 件（${i3.slice(-6)}）、著录、保存（草稿）`)

  // ---- 办公室第 3 件：池换到盒 03，"第 1–20 张"新建 → 著录、保存 → 标记完成 ----
  await clickCategory(cp, ctx.cats.bgs.id)
  await switchPool(cp, ctx.boxes['盒 03'].id)
  await rangeSelect(cp, 1, 20)
  await cp.click('[data-testid=pool-create-item]')
  const i4 = await waitNewDetail(cp, [i1, i2, i3])
  out.i4 = i4
  await waitStrip(cp, box3, 10000)
  await fillAndSave(cp, ITEMS.i4)
  await markDone(cp)
  await log(`办公室、池换到盒 03："第 1–20 张"新建第 3 件（${i4.slice(-6)}）、著录、保存、标记完成`)
  out.ended = Date.now()
  return out
}

/** 按 ITEMS 核对一个条目（接口）：节点、件号、档号、题名、著录、状态、页序。 */
async function verifyItem(ctx, key, { status, pages }) {
  const spec = ITEMS[key]
  const it = await getItem(ctx, state.items[key])
  const node = ctx.cats[spec.node]
  const no = `${FONDS_CODE}-WS·2025-Y-${node.category_code}-${String(spec.no).padStart(4, '0')}`
  assert(it.category_id === node.id && it.item_no === spec.no && it.archive_no === no, `${key} 的节点、件号、档号：${it.category_id} ${it.item_no} ${it.archive_no}（应为 ${no}）`)
  assert(it.title === spec.title, `${key} 的题名：${it.title}`)
  for (const [k, v] of Object.entries(spec.fields)) assert(it.extra_fields[k] === v, `${key} 的 ${k}：${it.extra_fields[k]}（应为 ${v}）`)
  assert(it.status === status, `${key} 的状态：${it.status}（应为 ${status}）`)
  assert(sameList(pageIds(it), pages), `${key} 的页序：${pageIds(it).map((x) => keyOf(ctx, x)).join(',')}`)
  return it
}

/** 条目的审计（直接读库）。 */
function itemAudits(itemId) {
  return runSql(`SELECT json_build_object('action', action, 'detail', detail, 'at', created_at)::text FROM audit_logs WHERE target_type = 'archive_item' AND target_id = ${lit(itemId)} ORDER BY id;`)
    .split('\n')
    .filter(Boolean)
    .map((l) => JSON.parse(l))
}

/** 这个全宗里的挂接（archive_item_id、file_id、page_no）和条目（id、件号、档号、状态、row_version），整理前后逐行比较。 */
function fondsLinks(ctx) {
  const range = `c.path_code >= ${lit(ctx.cats.fonds.path_code)} AND c.path_code < ${lit(ctx.cats.fonds.path_code.slice(0, -1) + '/')} COLLATE "C"`
  const links = runSql(`
    SELECT af.archive_item_id, af.file_id, af.page_no
    FROM archive_item_files af JOIN archive_items a ON a.id = af.archive_item_id JOIN archive_categories c ON c.id = a.category_id
    WHERE a.project_id = ${lit(ctx.pid)} AND ${range}
    ORDER BY af.archive_item_id, af.page_no;`)
  const items = runSql(`
    SELECT a.id, a.item_no, coalesce(a.archive_no, '-'), a.status, a.row_version
    FROM archive_items a JOIN archive_categories c ON c.id = a.category_id
    WHERE a.project_id = ${lit(ctx.pid)} AND a.deleted_at IS NULL AND ${range}
    ORDER BY a.archive_no, a.id;`)
  return { links: links.trim().split('\n').filter(Boolean), items: items.trim().split('\n').filter(Boolean) }
}

// ---------- 截图 ----------

export default {
  out: OUT,
  accounts: { [admin]: adminPassword, [MANAGER]: demoPassword, [REVIEWER]: demoPassword, [SCANNER]: demoPassword, [CATALOGER]: demoPassword },
  waitUntil: IDLE,
  shots: [
    // ---------------------------------------------------------------- 1. 演示数据
    {
      name: 'm3-accept-setup',
      user: CATALOGER,
      path: '/catalog',
      waitFor: '[data-testid=catalog-page]',
      async prepare(page) {
        mkdirSync(OUT_DIR, { recursive: true })
        const ctx = await context(page)
        // 项目授权：四个演示账号都能进 DEMO-A。
        const access = await ctx.admin('GET', `/api/projects/${ctx.pid}/access`)
        for (const n of [MANAGER, REVIEWER, SCANNER, CATALOGER]) {
          assert(access.users.some((u) => u.id === ctx.id(n)), `${n} 没有 DEMO-A 的个人授权`)
        }
        const settings = await ctx.cat('GET', `/api/projects/${ctx.pid}/catalog-settings`)
        assert(settings.item_fields.map((f) => f.key).join() === 'author,doc_no,doc_date,secret,remark', `DEMO-A 的著录项：${settings.item_fields.map((f) => f.key)}`)
        assert(settings.lock_requires_approved === true, '要求 catalog.lock_requires_approved 为 true（定稿前全部页通过）')
        state.template = settings.archive_no_template
        // 基线：别的演示文件夹、M2-5 的 40 个待分配、别的全宗、死锁计数。
        const root = await ctx.admin('GET', `/api/folders/${ctx.project.root_folder_id}/children`)
        const m25 = root.folders.find((f) => f.folder_name === 'M2-5 分配演示')
        assert(m25, '找不到「M2-5 分配演示」')
        const c25 = await summary(ctx, m25.id)
        assert(c25.unassigned === 40, `「M2-5 分配演示」应是 40 个待分配：${JSON.stringify(c25)}`)
        state.m25 = m25.id
        state.baseline = await otherFolders(ctx)
        state.otherFonds = otherFondsSnapshot(ctx)
        state.deadlocks0 = deadlocks()
        state.dbRows = runSql(`SELECT count(*) FILTER (WHERE deleted_at IS NULL), count(*) FILTER (WHERE deleted_at IS NOT NULL) FROM archive_items WHERE project_id = ${lit(ctx.pid)};`).trim()
        console.log(`  别的演示文件夹（待分配/待审/通过/驳回）${Object.keys(state.baseline).length} 个；别的全宗 ${OTHER_FONDS.join('、')} 的条目 ${state.otherFonds.trim().split('\n').length} 个；DEMO-A 的条目（未删除|已删除）${state.dbRows}；死锁计数 ${state.deadlocks0}`)

        // 演示文件夹：已有时要求 100 页、全部待分配；没有就建，demo_scanner 经上传接口传 100 页。
        let demo = root.folders.find((f) => f.folder_name === FOLDER_NAME)
        if (!demo) {
          const t0 = Date.now()
          demo = await ctx.scanner('POST', '/api/folders', {
            parent_id: ctx.project.root_folder_id,
            folder_name: FOLDER_NAME,
            remark: TRIAL ? 'M3-8 验收脚本试跑，跑完软删除' : 'M3-8 验收：审核和归类并行（脚本建的）',
          })
          for (const b of BOXES) {
            const box = await ctx.scanner('POST', '/api/folders', { parent_id: demo.id, folder_name: b.name, remark: b.remark })
            for (let i = 1; i <= b.count; i++) {
              await uploadPage(ctx.scanner, box.id, `IMG_${String(i).padStart(4, '0')}.png`, `M3-8 ${FOLDER_NAME} ${b.name} ${RUN_STAMP}#${i}`, i)
            }
          }
          console.log(`  建了「${FOLDER_NAME}」：${BOXES.map((b) => `${b.name} ${b.count} 页`).join('、')}（demo_scanner 经上传接口，${((Date.now() - t0) / 1000).toFixed(1)} 秒）`)
        }
        ctx.demo = demo
        const sub = await ctx.admin('GET', `/api/folders/${demo.id}/children`)
        for (const b of BOXES) {
          const f = sub.folders.find((x) => x.folder_name === b.name)
          assert(f, `${demo.folder_name} 里没有 ${b.name}`)
          ctx.boxes[b.name] = f
          const ch = await ctx.admin('GET', `/api/folders/${f.id}/children`)
          assert(ch.files.length === b.count, `${b.name} 应有 ${b.count} 页：${ch.files.length}`)
          for (const x of ch.files) ctx.files[`${b.name}/${x.file_name}`] = { ...x, box: b.name, folder_id: f.id, key: `${b.name}/${x.file_name}` }
        }
        const c = await summary(ctx, demo.id)
        assert(c.total === 100 && c.unassigned === 100, `「${demo.folder_name}」应该是 100 页全部待分配（只能走一遍）：${JSON.stringify(c)}`)

        // 归类树（demo_cataloger 经接口建）。
        const top = (await ctx.cat('GET', `/api/projects/${ctx.pid}/archive-categories`)).items
        assert(!top.some((x) => x.category_code === FONDS_CODE), `全宗编码 ${FONDS_CODE} 已被占用（只能走一遍；试跑用 M3_ACCEPT_TRIAL=1，或用 M3_ACCEPT_FONDS 换一个）`)
        const mk = (parent, type, code, name) =>
          ctx.cat('POST', `/api/projects/${ctx.pid}/archive-categories`, { parent_id: parent?.id ?? null, category_type: type, category_code: code, category_name: name })
        const fonds = await mk(null, 'fonds', FONDS_CODE, FONDS_NAME)
        const ws = await mk(fonds, 'category', 'WS', '文书档案')
        const year = await mk(ws, 'year', '2025', '2025 年')
        const y = await mk(year, 'retention', 'Y', '永久')
        const bgs = await mk(y, 'org', 'BGS', '办公室')
        const rsc = await mk(y, 'org', 'RSC', '人事处')
        ctx.cats = { fonds, ws, year, y, bgs, rsc }
        console.log(`  归类树：${FONDS_CODE}「${FONDS_NAME}」/ WS 文书档案 / 2025 / Y 永久 / BGS 办公室、RSC 人事处；模板 ${state.template}；著录项 ${settings.item_fields.map((f) => `${f.label}${f.required ? '*' : ''}`).join('、')}`)
        console.log(`  演示文件夹 id ${demo.id}，全宗 id ${fonds.id}`)

        await openWorkbench(page, { category: bgs.id, pool: ctx.boxes['盒 01'].id })
        await page.waitForSelector('[data-testid=catalog-pool][data-count="30"]', { timeout: 5000 })
        await waitCovers(page).catch(() => {})
        await rest(page)
      },
    },

    // ---------------------------------------------------------------- 2. 分配
    {
      name: 'm3-accept-assign',
      user: MANAGER,
      path: '/review-assign',
      waitFor: '[data-testid=review-assign-page]',
      async prepare(page) {
        const ctx = await context(page)
        assert(ctx.demo, '先跑 m3-accept-setup')
        trackWrites(page, '负责人')
        await openAssign(page, ctx.demo.id)
        await page.click(`[data-testid=reviewer-row][data-username="${REVIEWER}"]`)
        await page.waitForSelector(`[data-testid=reviewer-row][data-username="${REVIEWER}"][aria-checked=true]`, { timeout: 3000 })
        const label = await page.$eval('[data-testid=assign-button]', (el) => `${el.dataset.mode} ${el.textContent?.trim()}`)
        assert(label === `folder 分给${ctx.displayName(REVIEWER)}（100 个）`, `分配按钮是「${label}」`)
        await page.click('[data-testid=assign-button]')
        const text = await alertText(page)
        assert(text.includes('含子文件夹，共 100 个待分配'), `确认框：${text}`)
        const t0 = Date.now()
        await confirmAlert(page)
        await waitToast(page, `已分配 100 个给 ${ctx.displayName(REVIEWER)}`)
        state.timings.assign = Date.now() - t0
        await page.waitForSelector('[data-testid=folder-unassigned][data-count="0"]', { timeout: 5000 })
        for (const f of Object.values(ctx.files)) {
          const r = await reviewsOf(ctx, f.file_id)
          assert(r.review_status === 'pending' && r.reviews.length === 1 && r.reviews[0].status === 'open' && r.reviews[0].reviewer_id === ctx.id(REVIEWER), `${f.key} 没分给 ${REVIEWER}`)
          const logs = await logsOf(ctx, f.file_id)
          assert(logs.length === 2 && logs[0].action === 'assign_review' && logs[1].action === 'upload', `${f.key} 的日志：${logs.map((l) => l.action)}`)
        }
        const w = writes.find((x) => x.op === '分配')
        console.log(`  ✓ demo_manager 按文件夹分配 100 页给 ${REVIEWER}（点确认到提示出现 ${state.timings.assign} ms，请求 ${w?.ms} ms）；接口核对 100 个文件各一行 open、assign_review 日志`)
        await waitNoToast(page)
        await rest(page)
      },
    },

    // ---------------------------------------------------------------- 3. 审核和归类同时进行
    {
      name: 'm3-accept-parallel',
      user: REVIEWER,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        const items = await openItems(ctx, ctx.rev, ctx.demo.id)
        assert(items.length === 100, `${REVIEWER} 在演示文件夹里应有 100 个待审：${items.length}`)
        trackWrites(page, '审核')
        const cat = await openWindow(page, CATALOGER)
        trackWrites(cat.page, '归类')
        const dash = await openWindow(page, MANAGER)
        await openDashboard(dash.page)
        await openReview(page, ctx.demo.id, { tab: 'open' })
        await rest(page)
        await openWorkbench(cat.page, { category: ctx.cats.bgs.id, pool: ctx.boxes['盒 01'].id })
        const deadlocks0 = deadlocks()
        const before = writes.length
        state.parallel = { t0: Date.now() }
        console.log(`  开始：审核员每页停 ${PACE_MS} ms；归类员同时在 /catalog 操作，每一步之后停 ${THINK_MS} ms`)
        const [rv, cg] = await Promise.allSettled([keyboardReview(page, items, REJECTS, PACE_MS), catalogFlow(cat.page, ctx)])
        if (rv.status === 'rejected') throw new Error(`审核那边失败：${rv.reason?.message}`)
        if (cg.status === 'rejected') {
          await shot(cat.page, 'm3-accept-parallel-catalog-failed').catch(() => {})
          throw new Error(`归类那边失败：${cg.reason?.message}`)
        }
        const review = rv.value
        const catalog = cg.value
        Object.assign(state.items, { i1: catalog.i1, i2: catalog.i2, i3: catalog.i3, i4: catalog.i4 })
        // 重叠：归类的写请求有多少发生在审核进行期间。
        const ws = writes.slice(before)
        const catWrites = ws.filter((w) => w.side === '归类' && !w.op.startsWith('（只读）'))
        const revWrites = ws.filter((w) => w.side === '审核' && !w.op.startsWith('（只读）'))
        const overlapped = catWrites.filter((w) => w.at >= review.started && w.at <= review.ended).length
        state.parallel = {
          review: { seconds: (review.ended - review.started) / 1000, writes: revWrites.length },
          catalog: { seconds: (catalog.ended - catalog.started) / 1000, writes: catWrites.length, overlapped },
          stats: writeStats(ws),
          dragAttempts: catalog.dragAttempts,
        }
        console.log(`  审核：${(review.ended - review.started) / 1000} 秒、${revWrites.length} 个写请求；归类：${(catalog.ended - catalog.started) / 1000} 秒、${catWrites.length} 个写请求，其中 ${overlapped} 个发生在审核进行期间`)
        printStats('两边的写请求', state.parallel.stats)
        // 互不阻塞：除了那一次预期的 409，没有 409、其他 4xx、5xx、失败或超时；死锁计数不变。
        const bad = badWrites(ws)
        assert(!bad.length, `有不正常的写请求：${JSON.stringify(bad.slice(0, 5))}`)
        assert(expected409?.hit, '"从原条目移出"那一步应得到一次 409 file_cataloged')
        expected409 = null
        assert(overlapped >= Math.ceil(catWrites.length * 0.8), `归类的写请求只有 ${overlapped}/${catWrites.length} 个和审核重叠（调大 M3_ACCEPT_PACE_MS）`)
        const deadlocks1 = deadlocks()
        assert(deadlocks1 === deadlocks0, `死锁计数变了：${deadlocks0} → ${deadlocks1}`)
        console.log(`  ✓ 除了一次预期的 409 file_cataloged（"从原条目移出"前），没有 409、5xx、失败或超过 ${SLOW_MS} ms 的写请求；死锁计数 ${deadlocks1} 不变`)

        // 审核：95 通过、5 驳回，原因和说明对。
        const s = await summary(ctx, ctx.demo.id)
        assert(s.approved === 95 && s.rejected === 5 && s.pending === 0, `审完后的计数：${JSON.stringify(s)}`)
        for (const [key, rj] of Object.entries(REJECTS)) {
          const row = (await reviewsOf(ctx, file(ctx, key).file_id)).reviews.at(-1)
          assert(row.result === 'rejected' && JSON.stringify(row.reject_reasons) === JSON.stringify(rj.reasons) && (row.comment ?? null) === rj.comment, `${key} 的驳回：${JSON.stringify(row)}`)
        }
        console.log(`  ✓ 审核：通过 95、驳回 5（${Object.keys(REJECTS).join('、')}），原因、说明对`)
        // 归类：4 个条目的著录、状态、页序。
        const box1 = boxFiles(ctx, '盒 01').map((f) => f.file_id)
        const B = file(ctx, FILE_B).file_id
        const p1 = box1.slice(0, 10)
        const it1 = await verifyItem(ctx, 'i1', { status: 'done', pages: [p1[0], p1[2], p1[3], p1[4], p1[1], p1[5], p1[6], p1[7], p1[8]] })
        await verifyItem(ctx, 'i2', { status: 'done', pages: [B, ...box1.slice(10, 20)] })
        await verifyItem(ctx, 'i3', { status: 'draft', pages: boxFiles(ctx, '盒 02').slice(0, 15).map((f) => f.file_id) })
        await verifyItem(ctx, 'i4', { status: 'done', pages: boxFiles(ctx, '盒 03').map((f) => f.file_id) })
        // 第 1 件：一条 reorder（第 2 页 → 第 5 页）；B：link 第 1 件 → unlink 第 1 件 → link 第 2 件。
        const reorder = itemAudits(state.items.i1).filter((a) => a.action === 'reorder')
        assert(reorder.length === 1 && reorder[0].detail.moved?.length === 1 && reorder[0].detail.moved[0].file_id === p1[1] && reorder[0].detail.moved[0].from_page === 2 && reorder[0].detail.moved[0].to_page === 5, `第 1 件的 reorder 审计：${JSON.stringify(reorder)}`)
        const bl = (await logsOf(ctx, B)).filter((l) => l.action === 'link' || l.action === 'unlink').reverse()
        assert(
          bl.length === 3 && bl[0].action === 'link' && bl[0].detail.item_id === state.items.i1 && bl[1].action === 'unlink' && bl[1].detail.item_id === state.items.i1 && bl[2].action === 'link' && bl[2].detail.item_id === state.items.i2,
          `B 的挂接日志：${JSON.stringify(bl.map((l) => [l.action, l.detail.item_id?.slice(-6), l.detail.page_no]))}`,
        )
        console.log(`  ✓ 归类：办公室第 1 件 ${it1.archive_no}（9 页，第 2 页拖到第 5 页，一条 reorder 审计）、第 2 件（11 页，IMG_0010 从第 1 件移来、在第 1 页）、第 3 件（盒 03 的 20 页），人事处第 1 件（盒 02 的 15 页，草稿）；著录、状态、页序都和界面上做的一致`)
        console.log(`    "已在别的条目里"对话框：${catalog.dialog}`)
        // 看板：3 秒内和接口一致。
        const ms = await waitDashboard(dash.page, ctx, '并行结束后')
        console.log(`  ✓ 看板（demo_manager 一直开着）在并行结束后 ${ms} ms 内和接口一致`)
        await shot(dash.page, 'm3-accept-parallel-dashboard')
        await rest(cat.page)
        await shot(cat.page, 'm3-accept-parallel-catalog')
        await waitNoToast(page)
        await rest(page)
      },
      async after() {
        await closeWindows()
      },
    },

    // ---------------------------------------------------------------- 4. 浏览器重扫
    {
      name: 'm3-accept-rescan-browser',
      user: SCANNER,
      path: '/scan',
      waitFor: '[data-testid=rescan-list]',
      async prepare(page) {
        const ctx = await context(page)
        trackWrites(page, '扫描')
        await waitRescanList(page)
        for (const key of RESCAN_BROWSER) {
          const f = file(ctx, key)
          const name = `${f.file_name.replace('.png', '')} 重扫.png`
          const p = newPage(`浏览器重扫 ${key}`, Number(f.file_name.slice(4, 8)))
          const t0 = Date.now()
          const r = await rescanOnPage(page, f, name, p.png)
          const ms = Date.now() - t0
          assert(r.result === 'done', `${key} 重扫结果：${JSON.stringify(r)}`)
          assert(r.reassign === `自动分给 ${ctx.displayName(REVIEWER)}`, `${key} 的说明：${r.reassign}`)
          await verifyRescan(ctx, f, { uploadName: name, version: 2 })
          state.rescanned[`${key} v2`] = { where: '浏览器', name, ms }
          console.log(`  ✓ 浏览器重扫 ${key}（${ms} ms）：${r.title}；${r.reassign}；v2、第 2 轮`)
          await closeRescanDialog(page)
        }
        await waitCovers(page)
        await rest(page)
      },
    },

    // ---------------------------------------------------------------- 5. 壳重扫；条目里自动显示 v2、第 2 轮
    {
      name: 'm3-accept-rescan-shell',
      user: CATALOGER,
      path: '/catalog',
      waitFor: '[data-testid=catalog-page]',
      async prepare(page) {
        const ctx = await context(page)
        const c = file(ctx, FILE_C)
        await openWorkbench(page, { category: ctx.cats.bgs.id, item: state.items.i2 })
        await waitStripPage(page, c.file_id, { version: 1, status: 'rejected' }, 5000)
        const rv0 = (await getItem(ctx, state.items.i2)).row_version
        const r = await shellRescan(ctx, c, `rescan ${FILE_C}`, 2)
        const t0 = Date.now()
        await verifyRescan(ctx, c, { uploadName: r.name, version: 2, sha: r.sha })
        const ms = await waitStripPage(page, c.file_id, { version: 2, round: 2, status: 'pending' })
        const cell = (await stripCells(page)).find((x) => x.fileId === c.file_id)
        assert(cell.text.includes('第 2 轮') && cell.text.includes('v2'), `页序条上 C 应写 v2、第 2 轮：${cell.text}`)
        const it = await getItem(ctx, state.items.i2)
        const pc = it.pages.find((p) => p.file_id === c.file_id)
        assert(it.row_version === rv0 && pc.version_no === 2 && pc.review_round === 2 && pc.review_status === 'pending', `重扫后第 2 件：row_version ${rv0} → ${it.row_version}，C 那一页 ${JSON.stringify(pc)}`)
        state.rescanned[`${FILE_C} v2`] = { where: r.where, name: r.name, ms: r.ms }
        console.log(`  ✓ ${r.where} 重扫 ${FILE_C}：v2 的 sha 等于本地文件、自动分回 ${REVIEWER}；开着的第 2 件页序条 ${Date.now() - t0 > ms ? ms : Date.now() - t0} ms 内显示「${cell.text}」（不用重新挂接，row_version 仍是 ${rv0}）`)
        await clickStripPage(page, c.file_id)
        await rest(page)
      },
    },

    // ---------------------------------------------------------------- 6. 条目里的 v2、第 2 轮
    {
      name: 'm3-accept-v2',
      user: CATALOGER,
      path: '/catalog',
      waitFor: '[data-testid=catalog-page]',
      async prepare(page) {
        const ctx = await context(page)
        const a = file(ctx, FILE_A)
        await openWorkbench(page, { category: ctx.cats.bgs.id, item: state.items.i1 })
        await waitStripPage(page, a.file_id, { version: 2, round: 2, status: 'pending' }, 5000)
        const cell = (await stripCells(page)).find((x) => x.fileId === a.file_id)
        assert(cell.text.includes('第 2 轮') && cell.text.includes('v2'), `页序条上 A 应写 v2、第 2 轮：${cell.text}`)
        const it = await getItem(ctx, state.items.i1)
        const pa = it.pages.find((p) => p.file_id === a.file_id)
        assert(pa.version_no === 2 && pa.review_round === 2 && pa.review_status === 'pending', `接口里 A 的页：${JSON.stringify(pa)}`)
        for (const key of ['盒 02/IMG_0005.png', '盒 03/IMG_0012.png']) {
          const pl = (await ctx.cat('GET', `/api/files/${file(ctx, key).file_id}/catalog`)).placement
          const owner = await getItem(ctx, pl.item_id)
          const pg = owner.pages.find((p) => p.file_id === file(ctx, key).file_id)
          assert(pg.version_no === 2 && pg.review_round === 2, `${key} 在 ${owner.archive_no} 里应是 v2、第 2 轮：${JSON.stringify(pg)}`)
        }
        console.log(`  ✓ 第 1 件第 ${pa.page_no} 页（A）：页序条「${cell.text}」，接口 v2、第 2 轮、待审；盒 02/IMG_0005、盒 03/IMG_0012 所在条目里也是 v2、第 2 轮`)
        await clickStripPage(page, a.file_id)
        await rest(page)
      },
    },

    // ---------------------------------------------------------------- 7. 定稿 → 定稿后有新版本 → 解锁 → 再定稿；看板实时
    {
      name: 'm3-accept-lock-cycle',
      user: CATALOGER,
      path: '/catalog',
      waitFor: '[data-testid=catalog-page]',
      async prepare(page) {
        const ctx = await context(page)
        trackWrites(page, '归类')
        const a = file(ctx, FILE_A)
        const i1 = state.items.i1
        // 负责人两个窗口（各自一个上下文：同一个上下文里的第二个页面在后台，requestAnimationFrame 停了，等待会卡住）。
        const mgr = await openWindow(page, MANAGER)
        trackWrites(mgr.page, '负责人')
        const dash = (await openWindow(page, MANAGER)).page
        await openDashboard(dash)
        await waitDashboard(dash, ctx, '打开看板')
        await openWorkbench(page, { category: ctx.cats.bgs.id, item: i1 })

        // 审核员通过第 2 轮的 4 页（页面之外，经接口）。
        const round2 = [FILE_A, FILE_C, '盒 02/IMG_0005.png', '盒 03/IMG_0012.png']
        for (const key of round2) {
          const r = await ctx.rev('POST', `/api/files/${file(ctx, key).file_id}/review`, { result: 'approved' })
          assert(r.review_status === 'approved' && r.version_no === 2 && r.review_round === 2, `${key} 第 2 轮通过：${JSON.stringify(r)}`)
        }
        console.log(`  审核员通过第 2 轮的 4 页（v2）；看板 ${await waitDashboard(dash, ctx, '第 2 轮通过 4 页')} ms 后和接口一致`)
        await waitStripPage(page, a.file_id, { status: 'approved' })

        // 归类员定稿第 1 件。
        await lockUi(page)
        let it = await getItem(ctx, i1)
        assert(it.status === 'locked' && !it.new_version_after_lock, `定稿后：${it.status} ${it.new_version_after_lock}`)
        console.log(`  ✓ 归类员定稿第 1 件 ${it.archive_no}；看板 ${await waitDashboard(dash, ctx, '定稿第 1 件')} ms 后和接口一致（已定稿 +${it.page_count}）`)
        await rest(page)
        await shot(page, 'm3-accept-lock')

        // 定稿后：审核员撤回 A 的通过、驳回 → 壳重扫 v3 → 第 1 件标出"定稿后有新版本"。
        await ctx.rev('POST', `/api/files/${a.file_id}/reopen`)
        await ctx.rev('POST', `/api/files/${a.file_id}/review`, { result: 'rejected', reject_reasons: ['blur'], comment: '定稿后复查：右下角仍模糊' })
        await waitStripPage(page, a.file_id, { status: 'rejected' })
        console.log(`  审核员撤回 A 的通过、驳回（定稿后复查）；看板 ${await waitDashboard(dash, ctx, '定稿后驳回 A')} ms 后和接口一致`)
        const r3 = await shellRescan(ctx, a, `rescan ${FILE_A} v3`, 3)
        await verifyRescan(ctx, a, { uploadName: r3.name, version: 3, sha: r3.sha })
        state.rescanned[`${FILE_A} v3`] = { where: r3.where, name: r3.name, ms: r3.ms }
        const msNew = await waitStripPage(page, a.file_id, { version: 3, round: 3, status: 'pending', newVersion: true })
        await page.waitForSelector('[data-testid=item-new-version-after-lock]', { timeout: LIVE_MS })
        await page.waitForSelector('[data-testid=item-pages-new-version][data-count="1"]', { timeout: LIVE_MS })
        it = await getItem(ctx, i1)
        assert(it.status === 'locked' && it.new_version_after_lock && it.pages.find((p) => p.file_id === a.file_id).new_version_after_lock, `重扫后第 1 件应标出定稿后有新版本：${JSON.stringify({ s: it.status, n: it.new_version_after_lock })}`)
        const listFlag = (await ctx.cat('GET', `/api/archive-categories/${ctx.cats.bgs.id}/items?limit=100`)).items.find((x) => x.id === i1)
        assert(listFlag.new_version_after_lock === true, '条目列表里第 1 件也应标出定稿后有新版本')
        console.log(`  ✓ ${r3.where} 重扫 A（v3、第 3 轮）：开着的第 1 件 ${msNew} ms 内标出"定稿后有新版本"（页序条那一页、详情、列表），仍是定稿；看板 ${await waitDashboard(dash, ctx, '定稿后重扫 A')} ms`)
        await clickStripPage(page, a.file_id)
        await rest(page)
        await shot(page, 'm3-accept-new-version')

        // 负责人解锁。
        const mp = mgr.page
        await openWorkbench(mp, { category: ctx.cats.bgs.id, item: i1 })
        await mp.waitForSelector('[data-testid=item-unlock]', { timeout: 5000 })
        await mp.click('[data-testid=item-unlock]')
        await confirmAlert(mp)
        await mp.waitForSelector('[data-testid=item-detail][data-status=done]', { timeout: 8000 })
        await waitToast(mp, '已解锁')
        const t0 = Date.now()
        await page.waitForSelector('[data-testid=item-detail][data-status=done]', { timeout: LIVE_MS })
        const msUnlock = Date.now() - t0
        assert(!(await page.$('[data-testid=item-unlock]')), '归类员（只有 catalog 菜单）不应看到"解锁"')
        console.log(`  ✓ 负责人解锁第 1 件：归类员那边 ${msUnlock} ms 内变成"已完成"；看板 ${await waitDashboard(dash, ctx, '解锁第 1 件')} ms`)
        await rest(mp)
        await shot(mp, 'm3-accept-unlock')

        // 审核员通过 v3 → 归类员再定稿：标记消失。
        const r = await ctx.rev('POST', `/api/files/${a.file_id}/review`, { result: 'approved' })
        assert(r.version_no === 3 && r.review_round === 3, `A 第 3 轮通过：${JSON.stringify(r)}`)
        await waitStripPage(page, a.file_id, { status: 'approved' })
        console.log(`  审核员通过 A 的 v3；看板 ${await waitDashboard(dash, ctx, '通过 A 的 v3')} ms`)
        await lockUi(page)
        it = await getItem(ctx, i1)
        assert(it.status === 'locked' && !it.new_version_after_lock && !it.pages.some((p) => p.new_version_after_lock), `再定稿后不应再标"定稿后有新版本"：${it.new_version_after_lock}`)
        await page.waitForFunction(() => !document.querySelector('[data-testid=item-new-version-after-lock]'), { timeout: LIVE_MS })
        const audits = itemAudits(i1).map((x) => x.action)
        assert(audits.filter((x) => x === 'lock').length === 2 && audits.includes('unlock'), `第 1 件的审计：${audits}`)
        console.log(`  ✓ 归类员再定稿第 1 件：标记消失；审计 ${audits.join(' → ')}；看板 ${await waitDashboard(dash, ctx, '再定稿第 1 件')} ms`)

        // 批量定稿第 2、3 件（办公室）。
        for (const id of [state.items.i2, state.items.i4]) await page.click(`[data-testid=item-row][data-item-id="${id}"] [data-testid=item-check]`)
        await page.waitForFunction(() => document.querySelector('[data-testid=batch-lock]')?.textContent?.includes('（2）'), { timeout: 3000 })
        await page.click('[data-testid=batch-lock]')
        await confirmAlert(page)
        for (const id of [state.items.i2, state.items.i4]) await page.waitForSelector(`[data-testid=item-row][data-item-id="${id}"][data-status=locked]`, { timeout: 5000 })
        console.log(`  ✓ 批量定稿办公室第 2、3 件；看板 ${await waitDashboard(dash, ctx, '批量定稿 2 件')} ms`)
        await rest(dash)
        await shot(dash, 'm3-accept-dashboard')
        const bad = badWrites()
        assert(!bad.length, `有不正常的写请求：${JSON.stringify(bad.slice(0, 5))}`)
        await page.click(`[data-testid=item-row][data-item-id="${i1}"]`)
        await waitDetail(page, i1)
        await rest(page)
      },
      async after() {
        await closeWindows()
      },
    },

    // ---------------------------------------------------------------- 8. 整理不影响归类
    {
      name: 'm3-accept-organize',
      user: SCANNER,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await context(page)
        trackWrites(page, '扫描')
        const before = fondsLinks(ctx)
        assert(before.links.length === 55 && before.items.length === 4, `整理前：${before.links.length} 个挂接、${before.items.length} 个条目`)
        const B = file(ctx, FILE_B)
        const rn = file(ctx, RENAME_FILE)
        const cp = file(ctx, COPY_FILE)
        // 接口（demo_scanner，files 菜单）：建「已整理」→ 移动 B → 改名 → 复制 → 移动盒 03。
        const org = await ctx.scanner('POST', '/api/folders', { parent_id: ctx.demo.id, folder_name: ORGANIZED, remark: 'M3-8 整理演示：移进来的文件和文件夹' })
        ctx.organized = org
        await ctx.scanner('POST', '/api/folder-files:move', { ids: [B.folder_file_id], folder_id: org.id })
        const newName = 'IMG_0005（改名）.png'
        await ctx.scanner('PATCH', `/api/folder-files/${rn.folder_file_id}`, { file_name: newName })
        const copied = await ctx.scanner('POST', `/api/folder-files/${cp.folder_file_id}/copy`, { folder_id: org.id })
        await ctx.scanner('PATCH', `/api/folders/${ctx.boxes['盒 03'].id}`, { parent_id: org.id })
        state.organize = { org: org.id, copied }
        console.log(`  demo_scanner：建「${ORGANIZED}」，把 ${FILE_B}（在第 2 件）移进去，${RENAME_FILE}（在已定稿的第 1 件）改名为「${newName}」，${COPY_FILE} 复制进去，盒 03（整个第 3 件）移进去`)
        // 界面：删除盒 02（人事处第 1 件挂着它的 15 页）→ 409 file_cataloged 和提示。
        expected409 = { side: '扫描', code: 'file_cataloged', path: `/api/folders/${ctx.boxes['盒 02'].id}` }
        await page.goto(`${ctx.origin}/files?folder=${ctx.demo.id}`, { waitUntil: IDLE })
        await page.waitForSelector('[data-testid=files-page][data-list=ready]', { timeout: 15000 })
        const box2Sel = `[data-testid=files-page] [data-folder-id="${ctx.boxes['盒 02'].id}"]`
        await page.waitForSelector(box2Sel, { visible: true, timeout: 5000 })
        await page.click(box2Sel, { button: 'right' })
        await page.waitForSelector('[data-testid=context-menu] [data-testid=menu-delete]', { visible: true, timeout: 5000 })
        await sleep(150)
        await page.click('[data-testid=context-menu] [data-testid=menu-delete]')
        const confirmText = await alertText(page)
        // 删除失败时确认框留着（只有成功才关），提示是一条错误通知。
        const buttons = await page.$$('[role=alertdialog] button')
        await buttons[buttons.length - 1].click()
        await waitToast(page, '文件夹里有已归类的文件，不能删除')
        const toast = await toastText(page, '不能删除')
        await page.mouse.move(5, 895)
        await shot(page, 'm3-accept-delete-409')
        await page.keyboard.press('Escape')
        await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 5000 })
        assert(expected409.hit, '删除盒 02 应得到 409 file_cataloged')
        expected409 = null
        const still = await ctx.admin('GET', `/api/folders/${ctx.boxes['盒 02'].id}/children`)
        assert(still.files.length === 30, `盒 02 不应被删：${still.files.length}`)
        console.log(`  ✓ 界面上删除盒 02（确认框「${confirmText.replace(/\s+/g, ' ').trim().slice(0, 40)}…」）→ 409 file_cataloged，提示「${toast.slice(0, 80)}」，盒 02 还在`)
        // SQL 对比。
        const after = fondsLinks(ctx)
        assert(sameList(after.links, before.links), `整理后 archive_item_files 变了：\n${before.links.join('\n')}\n→\n${after.links.join('\n')}`)
        assert(sameList(after.items, before.items), `整理后条目（档号、状态、row_version）变了：\n${before.items.join('\n')}\n→\n${after.items.join('\n')}`)
        const md5 = (lines) => createHash('md5').update(lines.join('\n')).digest('hex').slice(0, 12)
        state.organizeSql = { links: before.links.length, items: before.items.length, md5_links: md5(before.links), md5_items: md5(before.items) }
        console.log(`    整理前后的导出：archive_item_files md5 ${md5(before.links)} → ${md5(after.links)}，条目 md5 ${md5(before.items)} → ${md5(after.items)}`)
        for (const l of after.items) console.log(`    条目 ${l}`)
        // 条目详情里的位置跟着变（只是显示）；复制出来的是新文件、没归类。
        const i2 = await getItem(ctx, state.items.i2)
        const pb = i2.pages.find((p) => p.file_id === B.file_id)
        assert(pb.folder_path.endsWith(`${ctx.demo.folder_name}/${ORGANIZED}`), `第 2 件里 B 的位置：${pb.folder_path}`)
        const i1 = await getItem(ctx, state.items.i1)
        assert(i1.pages.find((p) => p.file_id === rn.file_id).file_name === newName, '第 1 件里改名的那一页应显示新名字')
        const pc = (await ctx.cat('POST', `/api/projects/${ctx.pid}/catalog-placements`, { file_ids: [copied.file_id, cp.file_id] })).items
        assert(pc[0].placement === null && pc[1].placement?.item_id === state.items.i1, `复制出来的不应归类、原文件还在第 1 件：${JSON.stringify(pc)}`)
        console.log(`  ✓ SQL 前后逐行相同：archive_item_files ${after.links.length} 行（archive_item_id、file_id、page_no），条目 ${after.items.length} 个（id、件号、档号、状态、row_version）；详情里 B 的位置变成「…/${ORGANIZED}」，改名的页显示新名字，复制出来的新文件未归类`)
        await rest(page)
      },
    },

    // ---------------------------------------------------------------- 9. 还原 3 个文件的全过程
    {
      name: 'm3-accept-restore',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await context(page)
        const nameOf = (id) => Object.entries(state.items).find(([, v]) => v === id)?.[0] ?? id?.slice(-6)
        // 审核和重扫的顺序是确定的；挂接（link/unlink）和审核同时发生，单独核对它们之间的顺序。
        const expect = {
          [FILE_A]: { review: ['upload', 'assign_review', 'reject', 'rescan', 'approve', 'reopen', 'reject', 'rescan', 'approve'], links: [['link', 'i1']], versions: 3 },
          [FILE_B]: { review: ['upload', 'assign_review', 'approve', 'move'], links: [['link', 'i1'], ['unlink', 'i1'], ['link', 'i2']], versions: 1 },
          [FILE_C]: { review: ['upload', 'assign_review', 'reject', 'rescan', 'approve'], links: [['link', 'i2']], versions: 2 },
        }
        const ids = {}
        for (const [key, want] of Object.entries(expect)) {
          const f = file(ctx, key)
          ids[key] = f.file_id
          const logs = (await logsOf(ctx, f.file_id)).slice().reverse()
          const reviews = await reviewsOf(ctx, f.file_id)
          const snaps = await snapshotsOf(ctx, f.file_id)
          const placement = (await ctx.admin('GET', `/api/files/${f.file_id}/catalog`)).placement
          const owner = placement ? await getItem(ctx, placement.item_id) : null
          const pg = owner?.pages.find((p) => p.file_id === f.file_id)
          const nonLink = logs.filter((l) => l.action !== 'link' && l.action !== 'unlink').map((l) => l.action)
          const links = logs.filter((l) => l.action === 'link' || l.action === 'unlink').map((l) => [l.action, nameOf(l.detail.item_id)])
          assert(JSON.stringify(nonLink) === JSON.stringify(want.review), `${key} 的日志（挂接以外）：${nonLink}`)
          assert(JSON.stringify(links) === JSON.stringify(want.links), `${key} 的挂接日志：${JSON.stringify(links)}`)
          assert(snaps.length === want.versions && snaps[0].snapshot_type === 'original' && snaps.slice(1).every((s) => s.snapshot_type === 'rescan'), `${key} 的快照：${snaps.map((s) => `v${s.version_no} ${s.snapshot_type}`)}`)
          assert(logs[0].action === 'upload' && logs[1].action === 'assign_review', `${key} 应先上传、再分配`)
          console.log(`  ${key}（file_id ${f.file_id}）现在在 ${placement ? `${placement.archive_no} 第 ${placement.page_no} 页（${placement.item_status}）` : '（未归类）'}，${reviews.review_status}、第 ${reviews.review_round} 轮、v${reviews.version_no}`)
          if (pg) console.log(`    条目详情里这一页：第 ${pg.page_no} 页 ${pg.file_name} @ ${pg.folder_path}，${pg.review_status}、第 ${pg.review_round} 轮、v${pg.version_no}，定稿后有新版本 ${pg.new_version_after_lock}`)
          console.log(`    快照：${snaps.map((s) => `v${s.version_no} ${s.snapshot_type} ${s.created_by_name ?? ''} ${s.upload_name ?? ''}`.trim()).join('；')}`)
          for (const l of logs) {
            const d = l.detail
            const bits = [
              d.version_no !== undefined ? `v${d.version_no}` : null,
              d.review_round !== undefined ? `第 ${d.review_round} 轮` : null,
              d.item_id ? `条目 ${nameOf(d.item_id)} ${d.archive_no ?? ''} 第 ${d.page_no} 页` : null,
              d.reasons ? `reasons=${d.reasons.join(',')}` : null,
              d.comment ? `comment=${d.comment}` : null,
              d.by ? `by=${d.by}` : null,
              d.review_status && !d.item_id ? `→ ${d.review_status}` : null,
              d.upload_name && l.action === 'rescan' ? `upload_name=${d.upload_name}` : null,
              l.action === 'move' ? `${d.from ?? d.from_folder ?? ''} → ${d.to ?? d.to_folder ?? ''}` : null,
            ].filter(Boolean)
            console.log(`    ${l.created_at.slice(11, 23)} ${l.action.padEnd(15)} ${l.operator_name ?? l.operator_id}  ${bits.join(' ')}`)
          }
          for (const r of reviews.reviews) {
            console.log(`    file_reviews ${r.assignment_id.slice(-8)} ${r.reviewer_name} by ${r.assigned_by_name} ${r.status}${r.result ? ` ${r.result} v${r.version_no}` : ''}${r.reject_reasons?.length ? ` [${r.reject_reasons}]` : ''}`)
          }
        }
        // 关键分支：B 从第 1 件移出时的页号、挂到第 2 件的页号；A 的第 3 版在定稿之后。
        const bl = (await logsOf(ctx, ids[FILE_B])).filter((l) => l.action === 'unlink' || l.action === 'link')
        assert(bl[1].detail.page_no === 10 && bl[0].detail.page_no === 11, `B 移出第 1 件时是第 10 页、挂到第 2 件时是第 11 页：${JSON.stringify(bl.map((l) => l.detail.page_no))}`)
        const lockAt = itemAudits(state.items.i1).filter((x) => x.action === 'lock')
        const v3 = (await snapshotsOf(ctx, ids[FILE_A])).at(-1)
        assert(new Date(v3.created_at) > new Date(lockAt[0].at) && new Date(v3.created_at) < new Date(lockAt[1].at), `A 的 v3 应在第一次定稿之后、再定稿之前：${v3.created_at} ${lockAt.map((x) => x.at)}`)
        console.log(`  第 1 件的审计：${itemAudits(state.items.i1).map((x) => `${x.at.slice(11, 19)} ${x.action}`).join(' → ')}`)
        console.log(`ACCEPT_IDS ${JSON.stringify({ demo: ctx.demo.id, fonds: ctx.cats.fonds.id, A: ids[FILE_A], B: ids[FILE_B], C: ids[FILE_C], ...state.items })}`)
        // SQL 还原（精简版；完整的几条见 docs/sql/key-queries.sql 的"M3-8 验收的核对 SQL"）。
        for (const [key, fid] of Object.entries(ids)) {
          const out = runSql(`
            SELECT to_char(l.created_at, 'HH24:MI:SS.MS') || ' ' || rpad(l.action, 15) || ' ' || u.username || ' ' ||
                   concat_ws(' ', 'v' || (l.detail->>'version_no'), '第' || (l.detail->>'review_round') || '轮',
                             l.detail->>'archive_no', '第' || (l.detail->>'page_no') || '页', l.detail->>'folder_path')
            FROM file_logs l JOIN users u ON u.id = l.operator_id WHERE l.file_id = ${lit(fid)} ORDER BY l.id;`)
          const now = runSql(`
            SELECT ff.file_name || ' @ ' || f.path_code || ' | ' || ff.review_status || ' 第' || ff.review_round || '轮 v' ||
                   (SELECT max(version_no) FROM file_snapshots s WHERE s.file_id = ff.file_id) || ' | ' ||
                   coalesce((SELECT a.archive_no || ' 第' || af.page_no || '页 ' || a.status FROM archive_item_files af
                             JOIN archive_items a ON a.id = af.archive_item_id WHERE af.file_id = ff.file_id), '未归类')
            FROM folder_files ff JOIN folders f ON f.id = ff.folder_id WHERE ff.file_id = ${lit(fid)};`)
          console.log(`  SQL ${key}：${now.trim()}\n    ${out.trim().split('\n').join('\n    ')}`)
        }
        // 截图：B 在 /files 详情里的日志。
        const b = file(ctx, FILE_B)
        await page.goto(`${ctx.origin}/files?folder=${ctx.organized.id}`, { waitUntil: IDLE })
        await page.waitForSelector('[data-testid=files-page][data-list=ready]', { timeout: 15000 })
        const cell = await page.evaluateHandle(
          (n) => [...document.querySelectorAll('[data-testid=file-cell],[data-testid=file-row]')].find((el) => [...el.querySelectorAll('[title]')].some((t) => t.getAttribute('title') === n)),
          b.file_name,
        )
        assert(cell.asElement(), `「${ORGANIZED}」里找不到 ${b.file_name}`)
        await cell.asElement().click()
        await page.waitForSelector('[data-testid=file-log]', { timeout: 8000 }).catch(() => {})
        await waitCovers(page).catch(() => {})
        await rest(page)
      },
    },

    // ---------------------------------------------------------------- 10. 最后核对
    {
      name: 'm3-accept-final',
      user: MANAGER,
      path: '/dashboard',
      waitFor: '[data-testid=dashboard-page]',
      async prepare(page) {
        const ctx = await context(page)
        await page.waitForSelector('[data-testid=dashboard-page][data-state=ready][data-stream=open]', { timeout: 15000 })
        await waitDashboard(page, ctx, '最后')
        const s = await summary(ctx, ctx.demo.id)
        // 100 页：通过 99、驳回 1（盒 04/IMG_0007 留着待重扫）；另有整理时复制出来的 1 页（新文件，待分配）。
        assert(s.approved === 99 && s.rejected === 1 && s.pending === 0 && s.unassigned === 1 && s.total === 101, `演示文件夹最后的计数：${JSON.stringify(s)}`)
        const c25 = await summary(ctx, state.m25)
        assert(c25.unassigned === 40, `「M2-5 分配演示」应还是 40 个待分配：${JSON.stringify(c25)}`)
        const now = await otherFolders(ctx)
        assert(JSON.stringify(now) === JSON.stringify(state.baseline), `别的演示文件夹的计数变了：${JSON.stringify(state.baseline)} → ${JSON.stringify(now)}`)
        assert(otherFondsSnapshot(ctx) === state.otherFonds, '别的全宗（Z109、Z201、Z206）的条目或挂接变了')
        const d = deadlocks()
        assert(d === state.deadlocks0, `死锁计数变了：${state.deadlocks0} → ${d}`)
        const bad = badWrites()
        assert(!bad.length, `有不正常的写请求：${JSON.stringify(bad.slice(0, 5))}`)
        console.log(`  ✓ 别的演示文件夹 ${Object.keys(now).length} 个的计数没变、「M2-5 分配演示」40 个待分配；${OTHER_FONDS.join('、')} 的条目和挂接没变；死锁计数 ${d} 没变；整套 ${writes.length} 个写请求没有预期外的 409、5xx、失败或超时`)
        printStats('整套的写请求', writeStats(writes))
        console.log(`STATE ${JSON.stringify({ folder: ctx.demo.id, fonds: ctx.cats.fonds.id, items: state.items, timings: state.timings, live: state.live, parallel: state.parallel, rescanned: state.rescanned, shellRuns: state.shellRuns.map(({ steps, ...r }) => r), all: writeStats(writes) })}`)
      },
      async after(page) {
        if (!TRIAL || !ctxPromise) return
        // 试跑：删掉条目（定稿的先解锁；前面失败时按节点找）、归类节点，软删除演示文件夹。
        const ctx = await ctxPromise
        const ids = new Set(Object.values(state.items))
        for (const node of [ctx.cats.bgs, ctx.cats.rsc].filter(Boolean)) {
          for (const it of (await ctx.admin('GET', `/api/archive-categories/${node.id}/items?limit=2000`)).items) ids.add(it.id)
        }
        await deleteItemsForCleanup(ctx.admin, [...ids])
        const r1 = ctx.cats.fonds ? await ctx.admin('DELETE', `/api/projects/${ctx.pid}/archive-categories/${ctx.cats.fonds.id}`, undefined, { allow: [404, 409] }) : null
        const r2 = ctx.demo ? await ctx.admin('DELETE', `/api/folders/${ctx.demo.id}`, undefined, { allow: [404, 409] }) : null
        console.log(`  试跑数据清理：条目 ${ids.size} 个（删除）、全宗 ${FONDS_CODE}（${r1?.status ?? '已删除'}）、「${FOLDER_NAME}」（${r2?.status ?? '已软删除'}）`)
        void page
      },
    },
  ],
}

