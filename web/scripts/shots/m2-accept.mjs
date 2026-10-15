// M2-8 验收（Mac）：在一个新的演示文件夹上把审核循环整个走一遍——分配、改派、两人全键盘审核并驳回、扫描员在浏览器
// 和壳里重扫、自动回到原审核人（第 2 轮）对比后通过、撤回一页的通过再通过、停用审核人后重扫变成待分配并由负责人重新
// 分配——再用接口还原 3 个文件的全过程，最后在浏览器和壳里各打开 G4 TIFF 和 200 MB TIFF，记下耗时和内存。
//
//   INIT_ADMIN_PASSWORD=admin123456 SHOTS_TMP=<临时目录> \
//   M2_ACCEPT_SHELL=<打包的壳>/Doclite.app/Contents/MacOS/doclite-shell \
//     npm run screenshots -- --spec scripts/shots/m2-accept.mjs --base http://127.0.0.1:<server 端口> [--out <目录>]
//
// --base 直接指向托管了 web/dist 的 server（也可以是 vite preview）。壳的几步（M2_ACCEPT_SHELL）用打包的壳跑 `--smoke`：
// 数据目录用 SHOTS_TMP 下的隔离 HOME，不碰 ~/Library/Application Support/Doclite；服务器地址是 M2_ACCEPT_SERVER（默认
// 同 --base 的 origin，必须是 server 本身）。没给 M2_ACCEPT_SHELL 时，壳的重扫改用接口模拟（带 content_sha256，同壳的
// 字段），壳的大图一步跳过。壳的 viewer 一步要求窗口看得见：被别的窗口挡住时这一步失败，记下来，不算这张截图失败。
//
// 演示数据（只能走一遍；再跑要换一个新的文件夹）：DEMO-A 下「M2 验收演示 <日期>」（M2_ACCEPT_FOLDER 指定名字，默认取
// 根下最新的一个），4 个盒共 100 页、全部待分配，demo_scanner 上传：
//   T="M2 验收演示 2026-10-08"; U="--base http://127.0.0.1:<端口> --user demo_scanner:demo123456 --project DEMO-A"
//   先用接口（或文件目录页）在 DEMO-A 根下建「$T」并写备注，再：
//   npm run seed:files -- $U --folder "$T/盒 01" --count 30 --low-dpi 2 --remark "3 号库 B 架 2 层 · 盒 01（1–30 页）"
//   npm run seed:files -- $U --folder "$T/盒 02" --count 30 --remark "3 号库 B 架 2 层 · 盒 02（31–60 页）"
//   npm run seed:files -- $U --folder "$T/盒 03" --count 20 --remark "3 号库 B 架 3 层 · 盒 03（61–80 页）"
//   npm run seed:files -- $U --folder "$T/盒 04" --count 20 --no-cover 1 --remark "3 号库 B 架 3 层 · 盒 04（81–100 页）"
// 要求 demo_manager、demo_reviewer、demo_reviewer2、demo_scanner 能进 DEMO-A（密码 demo123456，DEMO_PASSWORD 可覆盖）。
// 别的演示文件夹（M1、M2-3 到 M2-7）不改：第一张记下它们的状态计数，最后一张核对没变。
//
// 截图（按顺序执行，后面的用前面的结果；文件名都以 m2-accept- 开头）：
//  1. m2-accept-assign：demo_manager 在 /review-assign 按文件夹把 100 页分给 demo_reviewer（确认框"含子文件夹，共 100 个
//     待分配"，中途另存 m2-accept-assign-confirm）；接口核对 100 个文件各一行 open、assign_review 日志。
//  2. m2-accept-reassign：在盒 03 全选 20 页，批量改派给 demo_reviewer2；接口核对旧行 cancelled、新行 open、reassign_review。
//  3. m2-accept-review-browser：demo_reviewer 在浏览器的 /review 只用键盘审完 80 页（A 通过，R → 数字勾原因 → Tab 写
//     说明 → Enter 驳回），驳回 4 页；接口核对每个文件的结论、原因、说明、所审快照和日志。
//  4. m2-accept-review-shell：demo_reviewer2 在壳模式（Tauri IPC 和 img 协议模拟，分块由 tiles-bench 现场切）只用键盘审完
//     20 页，驳回 3 页；断言用的是分块、给下一张调了 prepare_tiles。打包的壳里的键盘操作没法自动化，写在给用户的演示步骤里。
//  5. m2-accept-rescan-browser：demo_scanner 在浏览器的 /scan 待重扫列表里重扫 3 页（2 页回到 demo_reviewer、1 页回到
//     demo_reviewer2）；接口核对 v2 快照、封面、自动再分配（assigned_by 沿用驳回那一行）和 rescan 日志。
//  6. m2-accept-rescan-shell：打包的壳 `--smoke` 的 rescan 一步各重扫 1 页（demo_reviewer、demo_reviewer2 各一页）；核对同上，
//     另核对快照的 sha 等于本地文件。
//  7. m2-accept-round2-browser：demo_reviewer 打开第 2 轮的 3 页：横幅"上一轮因…被驳回"，按 C 对比（左 v1 标驳回原因、
//     右 v2，中途另存 m2-accept-round2-compare），按 A 通过 v2；再在页面上撤回其中一页的通过、按 A 再通过。
//  8. m2-accept-round2-shell：demo_reviewer2 在壳模式里对比（两侧都是分块，中途另存 m2-accept-round2-shell-compare）后
//     通过他的 2 页。
//  9. m2-accept-disabled-rescan：admin 停用 demo_reviewer2，demo_scanner 在浏览器重扫他驳回的最后一页 → 对话框"…已不能再审
//     这个项目，改为待分配"；接口核对 unassigned、没有 open 分配、rescan 日志带 skipped_reviewer。
// 10. m2-accept-reassign-unassigned：demo_manager 在 /review-assign 把这一页分给 demo_reviewer（右栏日志看得到"已不能再审"
//     和新的分配）；after 里重新启用 demo_reviewer2（前面失败了也启用）。
// 11. m2-accept-final：demo_reviewer 对比后通过这一页。
// 12. m2-accept-restore：用 `GET /api/files/{fid}/logs`、`/reviews`、`/snapshots` 还原 3 个文件的全过程并断言（A：驳回 → 浏览器
//     重扫 → 通过 → 撤回 → 再通过；B：改派 → 驳回 → 壳重扫 → 通过；C：改派 → 驳回 → 审核人停用后重扫 → 负责人重新分配 → 通过），
//     打印一行 `ACCEPT_IDS {...}` 给外面的 SQL 核对；核对别的演示文件夹的计数没变。截图是 C 在 /review-assign 右栏的日志。
// 13. m2-accept-big-browser-g4、m2-accept-big-browser-200mb：浏览器（独立的上下文）打开 DEMO-A/M2-3 分块实测 的 a4-600-g4.tif、
//     a3-600-none-rgb.tif，记下到画完的耗时和渲染进程的内存峰值。
// 14. m2-accept-big-shell：打包的壳 `--smoke`（cold，tiles + viewer 两步只对这一个文件）各打开这两张，记下切块耗时、viewer
//     画完的耗时、壳进程和 WebKit 的 WebContent 进程的内存峰值。截图只是 M2-3 分块实测 文件夹。

import { spawn, execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { makeScanPage } from '../lib/scan-pages.mjs'
import {
  apiClient,
  assert,
  installShellMock,
  login as loginPage,
  openFolder,
  openViewer,
  sampleMemory,
  settle,
  shotsTmp,
  sleep,
  waitViewer,
} from '../lib/viewer-shots.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'
const PROJECT = 'DEMO-A'
const DEMO_PREFIX = 'M2 验收演示 '
const BOXES = ['盒 01', '盒 02', '盒 03', '盒 04']
const MANAGER = 'demo_manager'
const REV1 = 'demo_reviewer'
const REV2 = 'demo_reviewer2'
const SCANNER = 'demo_scanner'
const IDLE = 'networkidle2'
const OUT = '../docs/screenshots/m2'
const outArg = process.argv.indexOf('--out')
const OUT_DIR = path.resolve(outArg > 0 ? process.argv[outArg + 1] : OUT)
const SHELL = process.env.M2_ACCEPT_SHELL ?? ''
/**
 * 壳模式的两步（m2-accept-review-shell、m2-accept-round2-shell）注入的是 Tauri IPC 的模拟；M6-1 起页面按 wails: 协议判断
 * 是不是在壳里，这个模拟不再起作用（M6-4 换成 Wails 的写法）。M2_ACCEPT_SHELL_MOCK=1 才按原样跑；默认（M5-3）这两步改在
 * 浏览器里由同一个人做同样的键盘审核和接口核对，只是不核对分块和 prepare_tiles，后面的步骤照常接着做。
 */
const SHELL_MOCK = process.env.M2_ACCEPT_SHELL_MOCK === '1'
const BENCH_FOLDER = 'M2-3 分块实测'
// M2-3 分块实测 重建过时 G4 样本的名字可能不同（M5-3 时 doclite_dev 里是 300 DPI 的 a4-g4-1bit.tif），用 M2_ACCEPT_BIG_G4 指定。
const BIG_G4 = process.env.M2_ACCEPT_BIG_G4 ?? 'a4-600-g4.tif'
const BIG_200 = 'a3-600-none-rgb.tif'

/** 驳回：键（1–9 按项目原因列表的顺序：模糊、倾斜、裁切过度、污渍、缺页、重页、方向、其他）、原因代码、说明。 */
const REJECTS_REV1 = {
  '盒 01/IMG_0002.png': { keys: ['1'], reasons: ['blur'], comment: '150 dpi，字迹发虚，请按 300 dpi 重扫' },
  '盒 01/IMG_0011.png': { keys: ['2'], reasons: ['skew'], comment: null },
  '盒 02/IMG_0007.png': { keys: ['4'], reasons: ['stain'], comment: '右上角有水渍，挡住了文号' },
  '盒 04/IMG_0003.png': { keys: ['3', '8'], reasons: ['cut', 'other'], comment: '左侧装订线被裁掉，重扫时整页放平' },
}
const REJECTS_REV2 = {
  '盒 03/IMG_0004.png': { keys: ['3'], reasons: ['cut'], comment: null },
  '盒 03/IMG_0010.png': { keys: ['7', '8'], reasons: ['orient', 'other'], comment: '整页倒置，页码被夹子挡住' },
  '盒 03/IMG_0016.png': { keys: ['4'], reasons: ['stain'], comment: '中间一大块墨迹' },
}
/** 谁在哪里重扫：A、B、C 是最后还原过程的 3 个文件。盒 04/IMG_0003 留着待重扫（给用户亲手演示）。 */
const RESCAN_BROWSER = ['盒 01/IMG_0002.png', '盒 02/IMG_0007.png', '盒 03/IMG_0004.png']
const RESCAN_SHELL = ['盒 01/IMG_0011.png', '盒 03/IMG_0010.png']
const FILE_A = '盒 01/IMG_0002.png'
const FILE_B = '盒 03/IMG_0010.png'
const FILE_C = '盒 03/IMG_0016.png'
/** 内置驳回原因的中文（同 web/src/lib/reject-reasons.ts）。 */
const REASON_LABELS = { blur: '模糊', skew: '倾斜', cut: '裁切过度', stain: '污渍', missing: '缺页', dup: '重页', orient: '方向', other: '其他' }

// ---------- 接口 ----------

async function as(origin, username, password = demoPassword) {
  const { call } = apiClient(origin)
  await call('POST', '/api/auth/login', { username, password })
  return call
}

function stamp() {
  return new Date().toISOString().replace(/[-:T]/g, '').slice(0, 14)
}

let ctxPromise = null
/** 跨截图共用的状态（截图按清单顺序执行）。 */
const state = { timings: {}, shellRuns: [], rescanned: {} }

/** 登录几个账号，找到 DEMO-A、演示文件夹（4 个盒）、全部文件和用户 id（只做一次）。 */
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const adminApi = await as(origin, admin, adminPassword)
    const projects = await adminApi('GET', '/api/projects')
    const project = projects.find((p) => p.project_code === PROJECT)
    assert(project, `找不到项目 ${PROJECT}，先跑 seed:demo`)
    const users = {}
    for (const name of [MANAGER, REV1, REV2, SCANNER]) {
      const res = await adminApi('GET', `/api/system/users?keyword=${encodeURIComponent(name)}&page_size=100`)
      const u = res.items.find((x) => x.username === name)
      assert(u, `找不到用户 ${name}`)
      users[name] = u
    }
    // 上一次跑到一半失败时 demo_reviewer2 可能还是停用的：先恢复。
    if (users[REV2].status !== 'active') {
      await adminApi('POST', `/api/system/users/${users[REV2].id}/enable`)
      users[REV2].status = 'active'
      console.log(`  ${REV2} 是停用的（上次没跑完？），已重新启用`)
    }
    const root = await adminApi('GET', `/api/folders/${project.root_folder_id}/children`)
    const wanted = process.env.M2_ACCEPT_FOLDER
    const demo = wanted
      ? root.folders.find((f) => f.folder_name === wanted)
      : root.folders.filter((f) => f.folder_name.startsWith(DEMO_PREFIX)).sort((a, b) => a.path_code.localeCompare(b.path_code)).at(-1)
    assert(demo, `DEMO-A 根下找不到「${wanted ?? DEMO_PREFIX + '…'}」，先按脚本开头建好`)
    const boxes = {}
    const files = {}
    const sub = await adminApi('GET', `/api/folders/${demo.id}/children`)
    for (const b of BOXES) {
      const f = sub.folders.find((x) => x.folder_name === b)
      assert(f, `${demo.folder_name} 里没有 ${b}`)
      boxes[b] = f
      const ch = await adminApi('GET', `/api/folders/${f.id}/children`)
      for (const x of ch.files) files[`${b}/${x.file_name}`] = { ...x, folder_id: f.id, key: `${b}/${x.file_name}` }
    }
    state.origin = origin
    const ctx = {
      origin,
      project,
      demo,
      boxes,
      files,
      admin: adminApi,
      mgr: await as(origin, MANAGER),
      rev1: await as(origin, REV1),
      rev2: await as(origin, REV2),
      scanner: await as(origin, SCANNER),
      id: (name) => users[name].id,
      displayName: (name) => users[name].display_name,
      as: (name) => as(origin, name),
    }
    return ctx
  })()
  return ctxPromise
}

function file(ctx, key) {
  const f = ctx.files[key]
  assert(f, `演示文件夹里没有 ${key}`)
  return f
}

async function reviewsOf(ctx, fid) {
  return ctx.admin('GET', `/api/files/${fid}/reviews`)
}

async function logsOf(ctx, fid) {
  return (await ctx.admin('GET', `/api/files/${fid}/logs`)).logs
}

async function snapshotsOf(ctx, fid) {
  return (await ctx.admin('GET', `/api/files/${fid}/snapshots`)).snapshots
}

/** 这个文件夹子树的四种状态计数（review-summary）。 */
async function summary(ctx, folderId) {
  const r = await ctx.mgr('GET', `/api/projects/${ctx.project.id}/review-summary?folder_id=${folderId}`)
  return r.counts
}

/** DEMO-A 根下别的演示文件夹的状态计数（核对没被改动）。 */
async function otherFolders(ctx) {
  const root = await ctx.admin('GET', `/api/folders/${ctx.project.root_folder_id}/children`)
  const out = {}
  for (const f of root.folders) {
    if (f.id === ctx.demo.id) continue
    const c = await summary(ctx, f.id)
    out[f.folder_name] = `${c.unassigned}/${c.pending}/${c.approved}/${c.rejected}`
  }
  return out
}

/** 一页新的扫描件（PNG + 封面），内容由种子决定。 */
function newPage(label, pageNo, dpi = 200) {
  const p = makeScanPage({ seed: `M2-8 ${label} ${stamp()} ${Math.random()}`, pageNo, dpi })
  return { png: Buffer.from(p.png), cover: Buffer.from(p.cover) }
}

/**
 * 重扫之后核对：快照（v2 rescan、upload_name、sha、封面）、file_reviews（驳回那一行 + 新的 open，assigned_by 沿用）、
 * rescan 日志。reviewer 为 null 表示应该变成待分配（skipped_reviewer）。
 */
async function verifyRescan(ctx, f, { uploadName, reviewer, skipped = null, sha = null }) {
  const snaps = await snapshotsOf(ctx, f.file_id)
  const last = snaps.at(-1)
  assert(snaps.length === 2 && last.version_no === 2 && last.snapshot_type === 'rescan', `${f.key} 的快照：${snaps.map((s) => `v${s.version_no} ${s.snapshot_type}`)}`)
  assert(last.upload_name === uploadName, `${f.key} v2 的 upload_name：${last.upload_name}`)
  assert(last.storage_key.endsWith('/v2.png'), `${f.key} v2 的 key：${last.storage_key}`)
  assert(!!last.cover_url, `${f.key} v2 没有封面`)
  if (sha) assert(last.content_sha256 === sha, `${f.key} v2 的 sha 不等于本地文件：${last.content_sha256}`)
  const r = await reviewsOf(ctx, f.file_id)
  const rejectedRow = r.reviews.filter((x) => x.result === 'rejected').at(-1)
  const open = r.reviews.filter((x) => x.status === 'open')
  assert(r.review_round === 2 && r.version_no === 2, `${f.key}：第 ${r.review_round} 轮、v${r.version_no}`)
  if (reviewer) {
    assert(r.review_status === 'pending', `${f.key} 应该是待审：${r.review_status}`)
    assert(open.length === 1 && open[0].reviewer_id === ctx.id(reviewer), `${f.key} 应该自动分回 ${reviewer}：${JSON.stringify(open)}`)
    assert(open[0].assigned_by === rejectedRow.assigned_by, `${f.key} 新行的 assigned_by 应沿用驳回那一行`)
  } else {
    assert(r.review_status === 'unassigned' && open.length === 0, `${f.key} 应该是待分配、没有 open 分配：${r.review_status} ${open.length}`)
  }
  const log = (await logsOf(ctx, f.file_id))[0]
  assert(log.action === 'rescan', `${f.key} 最新日志是 ${log.action}`)
  const d = log.detail
  assert(d.version_no === 2 && d.review_round === 2 && d.upload_name === uploadName && d.file_name === f.file_name, `${f.key} rescan 日志：${JSON.stringify(d)}`)
  if (reviewer) assert(d.reviewer === ctx.displayName(reviewer) && d.review_status === 'pending' && !('skipped_reviewer' in d), `${f.key} rescan 日志：${JSON.stringify(d)}`)
  else assert(d.reviewer === null && d.review_status === 'unassigned' && d.skipped_reviewer === skipped, `${f.key} rescan 日志应带 skipped_reviewer：${JSON.stringify(d)}`)
  return { v2: last, reviews: r, log }
}

// ---------- 页面 ----------

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
  const buttons = await page.$$('[role=alertdialog] button')
  await buttons[buttons.length - 1].click()
  await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 10000 })
}

// /review-assign

async function openAssign(page, folderId) {
  const ctx = await context(page)
  await page.goto(`${ctx.origin}/review-assign?folder=${folderId}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=review-assign-page][data-list=ready][data-stream=open]', { timeout: 15000 })
  await page.waitForSelector('[data-testid=reviewer-row]', { timeout: 10000 })
  await waitCovers(page)
}

async function pickReviewer(page, username) {
  await page.click(`[data-testid=reviewer-row][data-username="${username}"]`)
  await page.waitForSelector(`[data-testid=reviewer-row][data-username="${username}"][aria-checked=true]`, { timeout: 3000 })
}

async function fileCell(page, name) {
  const handle = await page.evaluateHandle(
    (n) => [...document.querySelectorAll('[data-testid=file-cell],[data-testid=file-row]')].find((el) => [...el.querySelectorAll('[title]')].some((t) => t.getAttribute('title') === n)),
    name,
  )
  const el = handle.asElement()
  if (!el) throw new Error(`网格里找不到「${name}」`)
  return el
}

/** 右栏单个文件的日志（动作和说明），新的在前。 */
async function panelLogs(page, fileId, minCount, firstAction) {
  await page.waitForFunction(
    (fid, n, act) => {
      const panel = document.querySelector(`[data-testid=single-file][data-file-id="${fid}"]`)
      const logs = panel ? [...panel.querySelectorAll('[data-testid=file-log]')] : []
      return panel?.querySelector('[data-testid=file-log-panel][data-state=ready]') && logs.length >= n && (!act || logs[0].dataset.action === act)
    },
    { timeout: 8000 },
    fileId,
    minCount,
    firstAction ?? null,
  )
  return page.$$eval(`[data-testid=single-file][data-file-id="${fileId}"] [data-testid=file-log]`, (els) =>
    els.map((el) => ({ action: el.dataset.action, summary: el.querySelector('[data-testid=file-log-summary]')?.textContent?.trim() ?? '' })),
  )
}

// /review

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

/** 列表是虚拟滚动的：从头往下滚，直到这一行渲染出来，再点它。 */
async function clickRow(page, fileId) {
  const sel = `[data-testid=review-row][data-file-id="${fileId}"]`
  const found = await page.evaluate(async (s) => {
    const list = document.querySelector('[data-testid=review-list]')
    if (!list) return false
    const frame = () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))
    if (!document.querySelector(s)) {
      list.scrollTop = 0
      await frame()
    }
    for (let i = 0; i < 400 && !document.querySelector(s); i++) {
      const before = list.scrollTop
      list.scrollTop += Math.max(100, list.clientHeight * 0.7)
      await frame()
      if (list.scrollTop === before) break
    }
    const el = document.querySelector(s)
    if (!el) return false
    el.scrollIntoView({ block: 'nearest' })
    await frame()
    return true
  }, sel)
  assert(found, `列表里找不到文件 ${fileId}`)
  await page.click(sel)
}

/**
 * 只用键盘审完 items（按列表顺序，每次审完自动跳到下一张待审）：rejects 里的按 R → 数字 → [Tab → 说明] → Enter，
 * 其余按 A。返回耗时（毫秒）。
 */
async function keyboardReview(page, items, rejects) {
  const t0 = Date.now()
  for (let i = 0; i < items.length; i++) {
    const f = items[i]
    await waitReady(page, f.file_id)
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
  return Date.now() - t0
}

/** 接口核对一个人审完的文件：结论、原因、说明、所审快照、最新一条日志。 */
async function verifyReviewed(ctx, items, rejects, reviewer, round = 1) {
  let approved = 0
  let rejected = 0
  for (const f of items) {
    const r = await reviewsOf(ctx, f.file_id)
    const row = r.reviews.at(-1)
    const snaps = await snapshotsOf(ctx, f.file_id)
    const log = (await logsOf(ctx, f.file_id))[0]
    assert(row.status === 'done' && row.reviewer_id === ctx.id(reviewer), `${f.key} 的分配行 ${row.status} ${row.reviewer_name}`)
    assert(row.snapshot_id === snaps.at(-1).snapshot_id, `${f.key} 审的快照不是当前版本`)
    const rj = rejects[f.key]
    if (rj) {
      rejected++
      assert(r.review_status === 'rejected' && row.result === 'rejected', `${f.key} 应该驳回：${r.review_status}`)
      assert(JSON.stringify(row.reject_reasons) === JSON.stringify(rj.reasons) && (row.comment ?? null) === rj.comment, `${f.key} 原因、说明：${row.reject_reasons} ${row.comment}`)
      assert(log.action === 'reject' && JSON.stringify(log.detail.reasons) === JSON.stringify(rj.reasons) && (log.detail.comment ?? null) === rj.comment, `${f.key} 驳回日志：${JSON.stringify(log)}`)
    } else {
      approved++
      assert(r.review_status === 'approved' && row.result === 'approved', `${f.key} 应该通过：${r.review_status}`)
      assert(log.action === 'approve' && !log.detail.batch, `${f.key} 通过日志：${JSON.stringify(log)}`)
    }
    assert(log.detail.reviewer === ctx.displayName(reviewer) && log.detail.version_no === round && log.detail.review_round === round, `${f.key} 日志的审核人、版本、轮次：${JSON.stringify(log.detail)}`)
  }
  return { approved, rejected }
}

/** 我的审核（接口）里这个文件夹子树的 open 项，按列表顺序。 */
async function openItems(ctx, call, folderId) {
  const r = await call('GET', `/api/me/reviews?project_id=${ctx.project.id}&folder_id=${folderId}&state=open&limit=2000`)
  return r.items.map((it) => {
    const key = Object.keys(ctx.files).find((k) => ctx.files[k].file_id === it.file_id)
    assert(key, `待办里有演示文件夹之外的文件 ${it.file_name}`)
    return ctx.files[key]
  })
}

/**
 * 第 2 轮：看横幅"上一轮因…被驳回"，按 C 对比（左 v1 标驳回原因、右 v2），按 A 通过 v2。shot 给了文件名时中途另存。
 * mode 是 tiles（壳模式）时要求两侧都是分块。
 */
async function compareAndApprove(page, ctx, f, reviewer, rj, { mode, shot } = {}) {
  await clickRow(page, f.file_id)
  await waitReady(page, f.file_id)
  await page.waitForSelector('[data-testid=prev-reject][data-version="1"]', { timeout: 8000 })
  const banner = (await page.$eval('[data-testid=prev-reject]', (e) => e.textContent ?? '')).replace(/\s+/g, ' ').trim()
  for (const code of rj.reasons) assert(banner.includes(REASON_LABELS[code]), `横幅没写驳回原因「${REASON_LABELS[code]}」：${banner}`)
  if (rj.comment) assert(banner.includes(rj.comment), `横幅没写说明：${banner}`)
  await rest(page)
  await page.keyboard.press('c')
  await page.waitForSelector('[data-testid=compare-viewer]', { timeout: 8000 })
  await page.waitForFunction(
    (m) => {
      const v = [...document.querySelectorAll('[data-testid=compare-pane] [data-testid=image-viewer]')]
      return v.length === 2 && v.every((x) => x.dataset.state === 'ready' && (!m || x.dataset.mode === m))
    },
    { timeout: 30000 },
    mode ?? null,
  )
  const sides = await page.$$eval('[data-testid=compare-pane]', (els) =>
    els.map((e) => ({
      side: e.dataset.side,
      sid: e.querySelector('[data-testid=image-viewer]')?.dataset.snapshotId,
      label: e.querySelector('[data-testid=compare-label]')?.textContent?.replace(/\s+/g, ' ').trim() ?? '',
    })),
  )
  const snaps = await snapshotsOf(ctx, f.file_id)
  const left = sides.find((s) => s.side === 'left')
  const right = sides.find((s) => s.side === 'right')
  assert(left.sid === snaps[0].snapshot_id && left.label.includes('v1') && left.label.includes('上一轮'), `左侧：${JSON.stringify(left)}`)
  assert(right.sid === snaps[1].snapshot_id && right.label.includes('v2'), `右侧：${JSON.stringify(right)}`)
  if (shot) {
    await sleep(400)
    await page.screenshot({ path: path.join(OUT_DIR, `${shot}.png`) })
  }
  await page.keyboard.press('a')
  const t0 = Date.now()
  let r
  for (;;) {
    r = await reviewsOf(ctx, f.file_id)
    if (r.review_status === 'approved' || Date.now() - t0 > 8000) break
    await sleep(100)
  }
  const row = r.reviews.at(-1)
  assert(r.review_status === 'approved' && row.result === 'approved' && row.reviewer_id === ctx.id(reviewer) && row.snapshot_id === snaps[1].snapshot_id, `${f.key} 对比后通过的应该是 v2：${JSON.stringify(row)}`)
  const log = (await logsOf(ctx, f.file_id))[0]
  assert(log.action === 'approve' && log.detail.version_no === 2 && log.detail.review_round === 2, `${f.key} 第 2 轮的通过日志：${JSON.stringify(log)}`)
  return { banner, left: left.label, right: right.label }
}

// /scan 的待重扫列表

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

// ---------- 打包的壳（--smoke） ----------

const WEBKIT = /com\.apple\.WebKit\.(WebContent|GPU|Networking)/

function processes() {
  try {
    return execFileSync('ps', ['-A', '-o', 'pid=,ppid=,rss=,command='], { encoding: 'utf8', maxBuffer: 16 << 20 })
      .split('\n')
      .map((l) => l.trim().match(/^(\d+)\s+(\d+)\s+(\d+)\s+(.*)$/))
      .filter(Boolean)
      .map((m) => ({ pid: +m[1], ppid: +m[2], rss: +m[3] / 1024, cmd: m[4] }))
  } catch {
    return []
  }
}

/**
 * 跑一次打包的壳 `--smoke`（隔离 HOME），每 100 ms 记一次壳进程和这期间新出现的 WebKit 进程（WebContent、GPU、Networking，
 * 页面在 WebContent 里）的 RSS。返回 {code, report, peak: {shell, webcontent, gpu, networking}}（MiB）。
 */
async function runShell(label, env) {
  assert(SHELL, '没有设置 M2_ACCEPT_SHELL')
  const home = shotsTmp('m2-accept-shell-home')
  const before = new Set(processes().filter((p) => WEBKIT.test(p.cmd)).map((p) => p.pid))
  const peak = { shell: 0, webcontent: 0, gpu: 0, networking: 0 }
  const child = spawn(SHELL, ['--smoke'], {
    env: {
      ...process.env,
      HOME: home,
      DOCLITE_SMOKE_SERVER: process.env.M2_ACCEPT_SERVER ?? state.origin,
      DOCLITE_SMOKE_USERNAME: SCANNER,
      DOCLITE_SMOKE_PASSWORD: demoPassword,
      DOCLITE_SMOKE_TIMEOUT_SECS: '300',
      DOCLITE_SMOKE_IMG_PROJECT: PROJECT,
      DOCLITE_SMOKE_IMG_FOLDER: 'M1-11 缓存冒烟',
      DOCLITE_SMOKE_TILES_FOLDER: 'M2-3 分块冒烟',
      ...env,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  let out = ''
  let err = ''
  child.stdout.on('data', (b) => (out += b))
  child.stderr.on('data', (b) => (err += b))
  const tick = () => {
    for (const p of processes()) {
      if (p.pid === child.pid) peak.shell = Math.max(peak.shell, p.rss)
      else if (!before.has(p.pid) && WEBKIT.test(p.cmd)) {
        const k = p.cmd.includes('WebContent') ? 'webcontent' : p.cmd.includes('GPU') ? 'gpu' : 'networking'
        peak[k] = Math.max(peak[k], p.rss)
      }
    }
  }
  const timer = setInterval(tick, 100)
  const t0 = Date.now()
  const code = await new Promise((resolve) => child.on('exit', (c) => resolve(c)))
  clearInterval(timer)
  const line = out.trim().split('\n').filter((l) => l.startsWith('{')).at(-1)
  writeFileSync(path.join(shotsTmp('m2-accept-shell-out'), `${label.replace(/[^\p{L}\p{N}.-]+/gu, '_')}.json`), `${out}\n---- stderr ----\n${err}`)
  let report = null
  try {
    report = JSON.parse(line)
  } catch {
    throw new Error(`壳 --smoke 没有输出结果（退出码 ${code}）：${err.slice(-500)}`)
  }
  for (const k of Object.keys(peak)) peak[k] = Math.round(peak[k])
  const run = { label, code, outcome: report.outcome, ms: Date.now() - t0, peak, steps: report.steps }
  state.shellRuns.push(run)
  const failed = report.steps.filter((s) => !s.ok).map((s) => `${s.name}: ${s.error}`)
  console.log(`  壳 --smoke「${label}」：退出码 ${code}（${report.outcome}），${(run.ms / 1000).toFixed(1)} 秒，RSS 峰值 壳 ${peak.shell} MiB、WebContent ${peak.webcontent} MiB、GPU ${peak.gpu} MiB${failed.length ? `；没通过的步骤：${failed.join('；')}` : ''}`)
  return run
}

function step(run, name) {
  return run.steps.find((s) => s.name === name)
}

// ---------- 壳模式（IPC 和 img 协议模拟，同 m2-review.mjs 的 shellShot） ----------

function shellModeShot(name, user, run) {
  if (!SHELL_MOCK) {
    return {
      name,
      user,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        console.log('  （没有设置 M2_ACCEPT_SHELL_MOCK=1：壳模式这一步改在浏览器里做，不核对分块）')
        await run(page, await context(page), { browser: true })
      },
      delay: 400,
    }
  }
  return {
    name,
    path: '/login',
    waitFor: '#username',
    async init(page) {
      await installShellMock(page, { cacheDir: shotsTmp('m2-accept-shell-mock') })
      // 在 M2-4 的模拟之上：记下 prepare_tiles 的调用；api_stream（实时事件）转给同源的 /api，逐块经 Channel 推给页面。
      await page.evaluateOnNewDocument(() => {
        const t = window.__TAURI_INTERNALS__
        const invoke = t.invoke.bind(t)
        window.__prepareCalls = []
        const streams = new Map()
        let nextStream = 1
        t.invoke = async (cmd, args) => {
          if (cmd === 'prepare_tiles') window.__prepareCalls.push(args)
          if (cmd === 'api_stream') {
            const id = nextStream++
            const ctrl = new AbortController()
            streams.set(id, ctrl)
            const send = (m) => {
              if (!ctrl.signal.aborted) args.onEvent.onmessage(m)
            }
            const req = args.req
            const qs = new URLSearchParams(req.query).toString()
            void (async () => {
              try {
                const res = await fetch(qs ? `${req.path}?${qs}` : req.path, { headers: req.headers, signal: ctrl.signal })
                send({ event: 'open', status: res.status, headers: {} })
                const reader = res.body.getReader()
                const dec = new TextDecoder()
                for (;;) {
                  const { done, value } = await reader.read()
                  if (done) break
                  send({ event: 'chunk', data: dec.decode(value, { stream: true }) })
                }
                send({ event: 'end' })
              } catch (e) {
                send({ event: 'error', kind: 'network', message: String(e) })
              } finally {
                streams.delete(id)
              }
            })()
            return id
          }
          if (cmd === 'api_stream_cancel') {
            streams.get(args.id)?.abort()
            streams.delete(args.id)
            return null
          }
          return invoke(cmd, args)
        }
      })
    },
    async prepare(page) {
      const origin = new URL(page.url()).origin
      await loginPage(page, origin, user, demoPassword)
      await page.waitForSelector('[data-testid=side-nav]')
      const ctx = await context(page)
      await run(page, ctx, { browser: false })
    },
    delay: 400,
  }
}

/**
 * 壳模式里打开审核台：模拟的来源记录是 Node 这边收到 batch、snapshots 响应后才记下的，页面一打开就要的封面、分块描述
 * 可能抢在前面（404）。先打开一次让模拟记下，再重新打开（同 m2-review.mjs 的 shellOpenTwice）。
 */
async function shellOpenTwice(page, ctx, tab) {
  for (let i = 0; i < 2; i++) {
    await openReview(page, ctx.demo.id, { tab })
    await page.waitForFunction(
      () => [...document.querySelectorAll('[data-testid=file-cover]')].every((c) => !['loading', 'loading-image'].includes(c.dataset.coverState)),
      { timeout: 15000 },
    )
    await sleep(300)
  }
}

// ---------- 截图 ----------

let reassignUnassignedDone = false

export default {
  out: OUT,
  accounts: { [MANAGER]: demoPassword, [REV1]: demoPassword, [REV2]: demoPassword, [SCANNER]: demoPassword, [admin]: adminPassword },
  waitUntil: IDLE,
  shots: [
    {
      name: 'm2-accept-assign',
      user: MANAGER,
      path: '/review-assign',
      waitFor: '[data-testid=review-assign-page]',
      async prepare(page) {
        const ctx = await context(page)
        // 项目授权：四个演示账号都能进 DEMO-A（个人授权或角色授权）。
        const access = await ctx.admin('GET', `/api/projects/${ctx.project.id}/access`)
        for (const n of [MANAGER, REV1, REV2, SCANNER]) {
          const personal = (access.user_ids ?? access.users?.map((u) => u.id) ?? []).includes(ctx.id(n))
          assert(personal, `${n} 没有 DEMO-A 的个人授权：${JSON.stringify(access).slice(0, 300)}`)
        }
        const c = await summary(ctx, ctx.demo.id)
        assert(c.total === 100 && c.unassigned === 100, `「${ctx.demo.folder_name}」应该是 100 页全部待分配（只能走一遍）：${JSON.stringify(c)}`)
        state.baseline = await otherFolders(ctx)
        console.log(`  演示文件夹「${ctx.demo.folder_name}」：${Object.entries(ctx.boxes).map(([k, b]) => `${k} ${b.file_count} 页`).join('、')}，共 100 页待分配`)
        console.log(`  别的演示文件夹（待分配/待审/通过/驳回）：${JSON.stringify(state.baseline)}`)

        await openAssign(page, ctx.demo.id)
        await pickReviewer(page, REV1)
        const label = await page.$eval('[data-testid=assign-button]', (el) => `${el.dataset.mode} ${el.textContent?.trim()}`)
        assert(label === `folder 分给${ctx.displayName(REV1)}（100 个）`, `分配按钮是「${label}」`)
        await page.click('[data-testid=assign-button]')
        const text = await alertText(page)
        assert(text.includes('含子文件夹，共 100 个待分配'), `确认框：${text}`)
        await page.screenshot({ path: path.join(OUT_DIR, 'm2-accept-assign-confirm.png') })
        const t0 = Date.now()
        await confirmAlert(page)
        await waitToast(page, `已分配 100 个给 ${ctx.displayName(REV1)}`)
        state.timings.assign = Date.now() - t0
        await page.waitForSelector('[data-testid=folder-unassigned][data-count="0"]', { timeout: 5000 })
        for (const f of Object.values(ctx.files)) {
          const r = await reviewsOf(ctx, f.file_id)
          assert(r.review_status === 'pending' && r.reviews.length === 1 && r.reviews[0].status === 'open' && r.reviews[0].reviewer_id === ctx.id(REV1), `${f.key} 没分给 ${REV1}`)
          assert(r.reviews[0].assigned_by === ctx.id(MANAGER), `${f.key} 的分配人不是 ${MANAGER}`)
          const logs = await logsOf(ctx, f.file_id)
          assert(logs.length === 2 && logs[0].action === 'assign_review' && logs[1].action === 'upload', `${f.key} 的日志：${logs.map((l) => l.action)}`)
          const d = logs[0].detail
          assert(d.reviewer_id === ctx.id(REV1) && d.folder_path === `${ctx.project.project_name}/${ctx.demo.folder_name}/${f.key.split('/')[0]}` && d.version_no === 1 && d.review_round === 1, `${f.key} 分配日志：${JSON.stringify(d)}`)
        }
        const after = await summary(ctx, ctx.demo.id)
        assert(after.pending === 100 && after.unassigned === 0, `分配后的计数：${JSON.stringify(after)}`)
        console.log(`  ✓ 按文件夹分配 100 页给 ${REV1}（点确认到提示出现 ${state.timings.assign} ms）；接口核对 100 个文件各一行 open、assign_review 日志的 folder_path 对`)
        await waitNoToast(page)
        await rest(page)
      },
    },
    {
      name: 'm2-accept-reassign',
      user: MANAGER,
      path: '/review-assign',
      waitFor: '[data-testid=review-assign-page]',
      async prepare(page) {
        const ctx = await context(page)
        const box = ctx.boxes['盒 03']
        const list = Object.values(ctx.files).filter((f) => f.folder_id === box.id)
        assert(list.length === 20, `盒 03 应有 20 页：${list.length}`)
        await openAssign(page, box.id)
        await pickReviewer(page, REV2)
        await (await fileCell(page, 'IMG_0001.png')).click()
        await page.keyboard.down('Meta')
        await page.keyboard.press('a')
        await page.keyboard.up('Meta')
        await page.waitForSelector('[data-testid=bulk-reassign-button]', { timeout: 3000 })
        const bl = await page.$eval('[data-testid=bulk-reassign-button]', (el) => el.textContent?.trim())
        assert(bl === `改派给${ctx.displayName(REV2)}（20 个待审）`, `批量改派按钮是「${bl}」`)
        await page.click('[data-testid=bulk-reassign-button]')
        await waitToast(page, `已改派 20 个给 ${ctx.displayName(REV2)}`)
        for (const f of list) {
          const r = await reviewsOf(ctx, f.file_id)
          assert(r.reviews.length === 2 && r.reviews[0].status === 'cancelled' && r.reviews[0].reviewer_id === ctx.id(REV1), `${f.key} 旧行：${JSON.stringify(r.reviews[0])}`)
          assert(r.reviews[1].status === 'open' && r.reviews[1].reviewer_id === ctx.id(REV2) && r.reviews[1].assigned_by === ctx.id(MANAGER), `${f.key} 新行：${JSON.stringify(r.reviews[1])}`)
          const l = (await logsOf(ctx, f.file_id))[0]
          assert(l.action === 'reassign_review' && l.detail.from_id === ctx.id(REV1) && l.detail.to_id === ctx.id(REV2) && l.detail.review_round === 1, `${f.key} 改派日志：${JSON.stringify(l)}`)
        }
        console.log(`  ✓ 盒 03 全选 20 页批量改派给 ${REV2}；接口核对旧行 cancelled、新行 open、reassign_review 日志`)
        // 截图：看其中一页的右栏（分配记录：取消的一行 + 新的一行；日志）。
        const one = list[3]
        await (await fileCell(page, one.file_name)).click()
        await page.waitForSelector(`[data-testid=single-file][data-file-id="${one.file_id}"] [data-testid=review-row][data-status=cancelled]`, { timeout: 5000 })
        await panelLogs(page, one.file_id, 3, 'reassign_review')
        await waitNoToast(page)
        await rest(page)
      },
    },
    {
      name: 'm2-accept-review-browser',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        const items = await openItems(ctx, ctx.rev1, ctx.demo.id)
        assert(items.length === 80, `${REV1} 在演示文件夹里应有 80 个待审：${items.length}`)
        await openReview(page, ctx.demo.id, { tab: 'open' })
        await rest(page)
        const ms = await keyboardReview(page, items, REJECTS_REV1)
        state.timings.rev1 = ms
        const v = await verifyReviewed(ctx, items, REJECTS_REV1, REV1)
        assert(v.approved === 76 && v.rejected === 4, `通过 ${v.approved}、驳回 ${v.rejected}`)
        const tabs = await page.$$eval('[data-testid=review-tab]', (els) => Object.fromEntries(els.map((e) => [e.dataset.state, Number(e.dataset.count)])))
        assert(tabs.open === 0 && tabs.approved === 76 && tabs.rejected === 4, `标签计数 ${JSON.stringify(tabs)}`)
        console.log(`  ✓ ${REV1} 在浏览器里只用键盘审完 80 页（${(ms / 1000).toFixed(1)} 秒）：通过 76、驳回 4（${Object.keys(REJECTS_REV1).join('、')}）；接口核对结论、原因、说明、所审快照、日志`)
        await waitNoToast(page)
        await rest(page)
      },
    },
    shellModeShot('m2-accept-review-shell', REV2, async (page, ctx, { browser }) => {
      const items = await openItems(ctx, ctx.rev2, ctx.demo.id)
      assert(items.length === 20, `${REV2} 应有 20 个待审：${items.length}`)
      if (browser) await openReview(page, ctx.demo.id, { tab: 'open' })
      else await shellOpenTwice(page, ctx, 'open')
      await rest(page)
      const ms = await keyboardReview(page, items, REJECTS_REV2)
      state.timings.rev2 = ms
      const mode = await page.$eval('[data-testid=image-viewer]', (e) => e.dataset.mode).catch(() => null)
      let nextOnes = null
      if (!browser) {
        const calls = await page.evaluate(() => window.__prepareCalls)
        const prepared = new Set(calls.map((c) => c.fileId))
        nextOnes = items.slice(1).filter((f) => prepared.has(f.file_id)).length
        assert(nextOnes >= items.length - 3, `给下一张调 prepare_tiles 的只有 ${nextOnes} 次：${JSON.stringify(calls).slice(0, 300)}`)
      }
      const v = await verifyReviewed(ctx, items, REJECTS_REV2, REV2)
      assert(v.approved === 17 && v.rejected === 3, `通过 ${v.approved}、驳回 ${v.rejected}`)
      console.log(
        `  ✓ ${REV2} 在${browser ? '浏览器（代替壳模式）' : '壳模式'}里只用键盘审完 20 页（${(ms / 1000).toFixed(1)} 秒，查看器 ${mode} 模式` +
          `${browser ? '' : `，给下一张提前切块 ${nextOnes} 次`}）：通过 17、驳回 3；接口核对同上`,
      )
      await waitNoToast(page)
      await rest(page)
    }),
    {
      name: 'm2-accept-rescan-browser',
      user: SCANNER,
      path: '/scan',
      waitFor: '[data-testid=rescan-list]',
      async prepare(page) {
        const ctx = await context(page)
        await waitRescanList(page)
        const total = Number(await page.$eval('[data-testid=rescan-list]', (e) => e.dataset.total))
        const api = await ctx.scanner('GET', `/api/projects/${ctx.project.id}/rescans?limit=2000`)
        assert(total === api.total, `角标 ${total}、接口 ${api.total}`)
        const ours = api.items.filter((i) => Object.values(ctx.files).some((f) => f.file_id === i.file_id))
        assert(ours.length === 7, `演示文件夹应有 7 页待重扫：${ours.length}`)
        for (const key of RESCAN_BROWSER) {
          const f = file(ctx, key)
          const reviewer = key in REJECTS_REV1 ? REV1 : REV2
          const name = `${f.file_name.replace('.png', '')} 重扫.png`
          const p = newPage(`浏览器重扫 ${key}`, Number(f.file_name.slice(4, 8)), key === FILE_A ? 300 : 200)
          const t0 = Date.now()
          const r = await rescanOnPage(page, f, name, p.png)
          const ms = Date.now() - t0
          assert(r.result === 'done', `${key} 重扫结果：${JSON.stringify(r)}`)
          assert(r.reassign === `自动分给 ${ctx.displayName(reviewer)}`, `${key} 的说明：${r.reassign}`)
          await verifyRescan(ctx, f, { uploadName: name, reviewer })
          state.rescanned[key] = { where: '浏览器', name, ms }
          console.log(`  ✓ 浏览器重扫 ${key}（${ms} ms）：${r.title}；${r.reassign}`)
          await closeRescanDialog(page)
        }
        await page.waitForFunction((n) => document.querySelector('[data-testid=rescan-list]')?.dataset.total === String(n), { timeout: 5000 }, total - 3)
        await waitCovers(page)
        await rest(page)
      },
    },
    {
      name: 'm2-accept-rescan-shell',
      user: SCANNER,
      path: '/scan',
      waitFor: '[data-testid=rescan-list]',
      async prepare(page) {
        const ctx = await context(page)
        for (const key of RESCAN_SHELL) {
          const f = file(ctx, key)
          const reviewer = key in REJECTS_REV1 ? REV1 : REV2
          const name = `${f.file_name.replace('.png', '')} 壳重扫.png`
          const p = newPage(`壳重扫 ${key}`, Number(f.file_name.slice(4, 8)))
          const local = path.join(shotsTmp('m2-accept-rescan-files'), name)
          writeFileSync(local, p.png)
          const sha = createHash('sha256').update(p.png).digest('hex')
          if (SHELL) {
            const run = await runShell(`rescan ${key}`, {
              DOCLITE_SMOKE_RESCAN_PROJECT: PROJECT,
              DOCLITE_SMOKE_RESCAN_FILE_ID: f.file_id,
              DOCLITE_SMOKE_RESCAN_PATH: local,
              DOCLITE_SMOKE_RESCAN_EXPECT: 'done',
            })
            const s = step(run, 'rescan')
            assert(s?.ok, `壳的 rescan 一步没通过：${JSON.stringify(s)}`)
            console.log(`    rescan 一步：${JSON.stringify(s.detail)}`)
            state.rescanned[key] = { where: '壳', name, ms: s.ms, smoke: run.outcome }
          } else {
            // 没有打包的壳：照壳的字段（file_name、content_sha256、cover、file）用接口重扫。
            const form = new FormData()
            form.append('file_name', name)
            form.append('content_sha256', sha)
            form.append('cover', new Blob([p.cover], { type: 'image/jpeg' }), 'cover.jpg')
            form.append('file', new Blob([p.png], { type: 'image/png' }), 'page.png')
            await ctx.scanner('POST', `/api/files/${f.file_id}/rescan`, form)
            state.rescanned[key] = { where: '接口（模拟壳）', name }
            console.log(`  （没有设置 M2_ACCEPT_SHELL：${key} 改用接口按壳的字段重扫）`)
          }
          await verifyRescan(ctx, f, { uploadName: name, reviewer, sha })
          console.log(`  ✓ 壳重扫 ${key}：v2 的 sha 等于本地文件，自动分回 ${reviewer}，rescan 日志对`)
        }
        await page.reload({ waitUntil: IDLE })
        await waitRescanList(page)
        await waitCovers(page)
        await rest(page)
      },
    },
    {
      name: 'm2-accept-round2-browser',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        const items = await openItems(ctx, ctx.rev1, ctx.demo.id)
        const want = ['盒 01/IMG_0002.png', '盒 01/IMG_0011.png', '盒 02/IMG_0007.png']
        assert(JSON.stringify(items.map((f) => f.key)) === JSON.stringify(want), `${REV1} 的第 2 轮待审：${items.map((f) => f.key)}`)
        await openReview(page, ctx.demo.id, { tab: 'open' })
        for (const f of items) {
          const r = await compareAndApprove(page, ctx, f, REV1, REJECTS_REV1[f.key], { shot: f.key === FILE_A ? 'm2-accept-round2-compare' : null })
          console.log(`  ✓ ${f.key}：${r.banner}；C 对比 左「${r.left}」右「${r.right}」；A 通过 v2`)
        }
        // 撤回 A 的通过（审核员撤回自己通过的），再按 A 通过。
        const a = file(ctx, FILE_A)
        await openReview(page, ctx.demo.id, { tab: 'all' })
        await clickRow(page, a.file_id)
        await waitReady(page, a.file_id)
        await page.waitForSelector('[data-testid=review-reopen]', { timeout: 5000 })
        await page.click('[data-testid=review-reopen]')
        await alertText(page)
        await confirmAlert(page)
        await page.waitForSelector(`[data-testid=review-row][data-file-id="${a.file_id}"][data-status=open]`, { timeout: 5000 })
        const ro = await reviewsOf(ctx, a.file_id)
        assert(ro.review_status === 'pending' && ro.reviews.at(-1).status === 'open' && ro.reviews.at(-1).reviewer_id === ctx.id(REV1) && ro.review_round === 2, `撤回后：${JSON.stringify(ro.reviews.at(-1))}`)
        const rl = (await logsOf(ctx, a.file_id))[0]
        assert(rl.action === 'reopen' && rl.detail.by === ctx.displayName(REV1) && rl.detail.reviewer_id === ctx.id(REV1) && rl.detail.version_no === 2 && rl.detail.review_round === 2, `reopen 日志：${JSON.stringify(rl)}`)
        await waitReady(page, a.file_id)
        await rest(page)
        await page.keyboard.press('a')
        await page.waitForSelector(`[data-testid=review-row][data-file-id="${a.file_id}"][data-result=approved]`, { timeout: 5000 })
        const ra = await reviewsOf(ctx, a.file_id)
        assert(ra.review_status === 'approved' && ra.reviews.at(-1).result === 'approved', `再通过后：${ra.review_status}`)
        console.log(`  ✓ 撤回 ${FILE_A} 的通过（回到 ${REV1} 的待审，reopen 日志 v2 第 2 轮），再按 A 通过`)
        await clickRow(page, a.file_id)
        await waitReady(page, a.file_id)
        await page.waitForSelector('[data-testid=review-record]', { timeout: 5000 })
        await waitNoToast(page)
        await rest(page)
      },
    },
    shellModeShot('m2-accept-round2-shell', REV2, async (page, ctx, { browser }) => {
      const items = await openItems(ctx, ctx.rev2, ctx.demo.id)
      const want = ['盒 03/IMG_0004.png', '盒 03/IMG_0010.png']
      assert(JSON.stringify(items.map((f) => f.key)) === JSON.stringify(want), `${REV2} 的第 2 轮待审：${items.map((f) => f.key)}`)
      if (browser) await openReview(page, ctx.demo.id, { tab: 'open' })
      else await shellOpenTwice(page, ctx, 'open')
      for (const f of items) {
        const r = await compareAndApprove(page, ctx, f, REV2, REJECTS_REV2[f.key], {
          mode: browser ? undefined : 'tiles',
          shot: f.key === FILE_B ? 'm2-accept-round2-shell-compare' : null,
        })
        console.log(`  ✓ ${browser ? '浏览器（代替壳模式）' : '壳模式'} ${f.key}：${r.banner}；C 对比${browser ? '' : '（两侧分块）'}左「${r.left}」右「${r.right}」；A 通过 v2`)
      }
      await waitNoToast(page)
      await rest(page)
    }),
    {
      name: 'm2-accept-disabled-rescan',
      user: SCANNER,
      path: '/scan',
      waitFor: '[data-testid=rescan-list]',
      async prepare(page) {
        const ctx = await context(page)
        const c = file(ctx, FILE_C)
        await ctx.admin('POST', `/api/system/users/${ctx.id(REV2)}/disable`)
        state.rev2Disabled = true
        console.log(`  已停用 ${REV2}`)
        const name = `${c.file_name.replace('.png', '')} 重扫.png`
        const p = newPage(`停用后重扫 ${FILE_C}`, 16)
        const r = await rescanOnPage(page, c, name, p.png)
        assert(r.result === 'done', `重扫结果：${JSON.stringify(r)}`)
        assert(r.reassign === `${ctx.displayName(REV2)} 已不能再审这个项目，改为待分配`, `说明：${r.reassign}`)
        await verifyRescan(ctx, c, { uploadName: name, reviewer: null, skipped: ctx.displayName(REV2) })
        state.rescanned[FILE_C] = { where: '浏览器（审核人已停用）', name }
        console.log(`  ✓ 停用 ${REV2} 后在浏览器重扫 ${FILE_C}：${r.title}；${r.reassign}；接口核对 unassigned、没有 open 分配、rescan 日志带 skipped_reviewer`)
        await sleep(300)
        await rest(page)
      },
      async after(page) {
        await closeRescanDialog(page).catch(() => {})
      },
    },
    {
      name: 'm2-accept-reassign-unassigned',
      user: MANAGER,
      path: '/review-assign',
      waitFor: '[data-testid=review-assign-page]',
      async prepare(page) {
        const ctx = await context(page)
        const c = file(ctx, FILE_C)
        await openAssign(page, ctx.boxes['盒 03'].id)
        await page.waitForSelector('[data-testid=status-filter-unassigned][data-count="1"]', { timeout: 5000 })
        const rows = await page.$$eval('[data-testid=reviewer-row]', (els) => els.map((e) => e.dataset.username))
        assert(!rows.includes(REV2), `停用的 ${REV2} 不该出现在"分给谁"里`)
        await pickReviewer(page, REV1)
        await (await fileCell(page, c.file_name)).click()
        await page.waitForSelector('[data-testid=assign-button][data-mode=files]', { timeout: 3000 })
        const label = await page.$eval('[data-testid=assign-button]', (el) => `${el.dataset.mode} ${el.textContent?.trim()}`)
        assert(label === `files 分给${ctx.displayName(REV1)}（1 个）`, `分配按钮是「${label}」`)
        await page.click('[data-testid=assign-button]')
        await waitToast(page, `已分配 1 个给 ${ctx.displayName(REV1)}`)
        const r = await reviewsOf(ctx, c.file_id)
        assert(r.review_status === 'pending' && r.reviews.at(-1).status === 'open' && r.reviews.at(-1).reviewer_id === ctx.id(REV1) && r.reviews.at(-1).assigned_by === ctx.id(MANAGER), `重新分配后：${JSON.stringify(r.reviews.at(-1))}`)
        const l = (await logsOf(ctx, c.file_id))[0]
        assert(l.action === 'assign_review' && l.detail.reviewer_id === ctx.id(REV1) && l.detail.version_no === 2 && l.detail.review_round === 2, `分配日志：${JSON.stringify(l)}`)
        // 分配之后选择会清掉：再点一下这个文件，看右栏的分配记录和日志。
        await (await fileCell(page, c.file_name)).click()
        const logs = await panelLogs(page, c.file_id, 6, 'assign_review')
        const rescanLine = logs.find((x) => x.action === 'rescan')
        assert(rescanLine?.summary.includes('已不能再审这个项目'), `右栏 rescan 那一行：${JSON.stringify(rescanLine)}`)
        reassignUnassignedDone = true
        console.log(`  ✓ ${MANAGER} 在 /review-assign 把待分配的 ${FILE_C} 分给 ${REV1}（v2 第 2 轮）；右栏日志：${rescanLine.summary}`)
        await waitNoToast(page)
        await rest(page)
      },
      async after() {
        // 一定恢复 demo_reviewer2（前面失败了也恢复）。
        if (!ctxPromise) return
        const ctx = await ctxPromise
        const res = await ctx.admin('GET', `/api/system/users?keyword=${REV2}&page_size=100`)
        const u = res.items.find((x) => x.username === REV2)
        if (u && u.status !== 'active') {
          await ctx.admin('POST', `/api/system/users/${u.id}/enable`)
          console.log(`  已重新启用 ${REV2}`)
        }
        state.rev2Disabled = false
      },
    },
    {
      name: 'm2-accept-final',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        assert(reassignUnassignedDone, '上一张（重新分配）没成功')
        const c = file(ctx, FILE_C)
        const items = await openItems(ctx, ctx.rev1, ctx.demo.id)
        assert(items.length === 1 && items[0].file_id === c.file_id, `${REV1} 的待审：${items.map((f) => f.key)}`)
        await openReview(page, ctx.demo.id, { tab: 'open' })
        const r = await compareAndApprove(page, ctx, c, REV1, REJECTS_REV2[FILE_C])
        console.log(`  ✓ ${REV1} 审 ${FILE_C}：${r.banner}；C 对比后通过 v2`)
        const s = await summary(ctx, ctx.demo.id)
        assert(s.approved === 99 && s.rejected === 1 && s.pending === 0 && s.unassigned === 0, `演示文件夹最后的计数：${JSON.stringify(s)}`)
        console.log(`  ✓ 演示文件夹最后：通过 99、驳回 1（盒 04/IMG_0003.png 留着待重扫）`)
        await waitNoToast(page)
        await rest(page)
      },
    },
    {
      name: 'm2-accept-restore',
      user: MANAGER,
      path: '/review-assign',
      waitFor: '[data-testid=review-assign-page]',
      async prepare(page) {
        const ctx = await context(page)
        const expect = {
          [FILE_A]: ['upload', 'assign_review', 'reject', 'rescan', 'approve', 'reopen', 'approve'],
          [FILE_B]: ['upload', 'assign_review', 'reassign_review', 'reject', 'rescan', 'approve'],
          [FILE_C]: ['upload', 'assign_review', 'reassign_review', 'reject', 'rescan', 'assign_review', 'approve'],
        }
        const ids = {}
        for (const [key, actions] of Object.entries(expect)) {
          const f = file(ctx, key)
          ids[key] = f.file_id
          const logs = (await ctx.admin('GET', `/api/files/${f.file_id}/logs`)).logs.slice().reverse()
          const reviews = await reviewsOf(ctx, f.file_id)
          const snaps = await snapshotsOf(ctx, f.file_id)
          assert(JSON.stringify(logs.map((l) => l.action)) === JSON.stringify(actions), `${key} 的日志：${logs.map((l) => l.action)}`)
          assert(snaps.length === 2 && snaps[0].version_no === 1 && snaps[0].snapshot_type === 'original' && snaps[1].version_no === 2 && snaps[1].snapshot_type === 'rescan', `${key} 的快照：${snaps.map((s) => `v${s.version_no} ${s.snapshot_type}`)}`)
          console.log(`  ${key}（file_id ${f.file_id}，${state.rescanned?.[key]?.where ?? '?'}重扫）`)
          console.log(`    快照：${snaps.map((s) => `v${s.version_no} ${s.snapshot_type} ${s.created_by_name ?? ''} ${s.upload_name ?? ''}`.trim()).join('；')}`)
          for (const l of logs) {
            const d = l.detail
            const bits = [
              d.version_no !== undefined ? `v${d.version_no}` : null,
              d.review_round !== undefined ? `第 ${d.review_round} 轮` : null,
              d.reviewer !== undefined ? `reviewer=${d.reviewer}` : null,
              d.from ? `from=${d.from} to=${d.to}` : null,
              d.reasons ? `reasons=${d.reasons.join(',')}` : null,
              d.comment ? `comment=${d.comment}` : null,
              d.by ? `by=${d.by}` : null,
              d.review_status ? `→ ${d.review_status}` : null,
              'skipped_reviewer' in d ? `skipped_reviewer=${d.skipped_reviewer}` : null,
              d.upload_name && l.action === 'rescan' ? `upload_name=${d.upload_name}` : null,
            ].filter(Boolean)
            console.log(`    ${l.created_at.slice(11, 23)} ${l.action.padEnd(15)} ${l.operator_name ?? l.operator_id}  ${bits.join(' ')}`)
          }
          for (const r of reviews.reviews) {
            console.log(`    file_reviews ${r.assignment_id.slice(-8)} ${r.reviewer_name} by ${r.assigned_by_name} ${r.status}${r.result ? ` ${r.result} v${r.version_no}` : ''}${r.reject_reasons?.length ? ` [${r.reject_reasons}]` : ''}`)
          }
        }
        // 关键分支的断言。
        const lc = (await ctx.admin('GET', `/api/files/${ids[FILE_C]}/logs`)).logs.slice().reverse()
        assert(lc[4].detail.skipped_reviewer === ctx.displayName(REV2) && lc[4].detail.review_status === 'unassigned', `C 的 rescan：${JSON.stringify(lc[4].detail)}`)
        const la = (await ctx.admin('GET', `/api/files/${ids[FILE_A]}/logs`)).logs.slice().reverse()
        assert(la[5].detail.reviewer_id === ctx.id(REV1) && la[6].detail.version_no === 2 && la[6].detail.review_round === 2, `A 的 reopen、再通过：${JSON.stringify(la.slice(5))}`)
        console.log(
          'ACCEPT_IDS ' +
            JSON.stringify({ demo: ctx.demo.id, A: ids[FILE_A], B: ids[FILE_B], C: ids[FILE_C], rev1: ctx.id(REV1), rev2: ctx.id(REV2), mgr: ctx.id(MANAGER) }),
        )
        // 别的演示文件夹没被改动。
        const now = await otherFolders(ctx)
        assert(JSON.stringify(now) === JSON.stringify(state.baseline), `别的演示文件夹的计数变了：${JSON.stringify(state.baseline)} → ${JSON.stringify(now)}`)
        console.log(`  ✓ 别的演示文件夹的状态计数没变（${Object.keys(now).length} 个）`)
        // 截图：C 在 /review-assign 右栏的分配记录和日志。
        const c = file(ctx, FILE_C)
        await openAssign(page, ctx.boxes['盒 03'].id)
        await (await fileCell(page, c.file_name)).click()
        await panelLogs(page, c.file_id, 7, 'approve')
        await page.$eval('[data-testid=single-file]', (el) => el.scrollIntoView({ block: 'start' }))
        await sleep(300)
        await rest(page)
      },
    },
    ...[BIG_G4, BIG_200].map((name) => ({
      name: name === BIG_G4 ? 'm2-accept-big-browser-g4' : 'm2-accept-big-browser-200mb',
      path: '/login',
      waitFor: '#username',
      init: async () => {},
      async prepare(page) {
        const origin = new URL(page.url()).origin
        await loginPage(page, origin, admin, adminPassword)
        const ctx = await context(page)
        const root = await ctx.admin('GET', `/api/folders/${ctx.project.root_folder_id}/children`)
        const bench = root.folders.find((f) => f.folder_name === BENCH_FOLDER)
        assert(bench, `找不到 ${BENCH_FOLDER}`)
        const f = (await ctx.admin('GET', `/api/folders/${bench.id}/children`)).files.find((x) => x.file_name === name)
        assert(f, `${BENCH_FOLDER} 里没有 ${name}`)
        await openFolder(page, origin, bench.id)
        const mem = sampleMemory(page.browser(), 50)
        const t0 = Date.now()
        await openViewer(page, name)
        const d = await waitViewer(page, ['ready'], { timeout: 120000 })
        const ms = Date.now() - t0
        await settle(page)
        await sleep(2000)
        const peak = mem.stop()
        state[`browser ${name}`] = { ms, ...peak, mode: d.mode }
        console.log(`  浏览器打开 ${name}（${d.mode}）：${ms} ms 画完；渲染进程 RSS 打开前 ${peak.rendererBase} MiB、峰值 ${peak.renderer} MiB、画完 2 秒后 ${peak.rendererEnd} MiB；GPU 进程峰值 ${peak.gpu} MiB`)
      },
      delay: 300,
    })),
    {
      name: 'm2-accept-big-shell',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await context(page)
        const root = await ctx.admin('GET', `/api/folders/${ctx.project.root_folder_id}/children`)
        const bench = root.folders.find((f) => f.folder_name === BENCH_FOLDER)
        if (SHELL) {
          for (const name of [BIG_G4, BIG_200]) {
            const run = await runShell(`big ${name}`, {
              DOCLITE_SMOKE_CACHE: 'cold',
              DOCLITE_SMOKE_TILES_FOLDER: BENCH_FOLDER,
              DOCLITE_SMOKE_TILES_FILES: name,
            })
            const tiles = step(run, 'tiles')
            const viewer = step(run, 'viewer')
            assert(tiles?.ok, `tiles 一步没通过：${JSON.stringify(tiles)}`)
            const t = tiles.detail.files[0]
            console.log(
              `    tiles：描述 ${t.descriptor_ms} ms（切块 ${t.generate_ms} ms，${t.tile_count} 块，${(t.tiles_bytes / 1048576).toFixed(1)} MB，${t.streamed ? '按条带' : '整张解码'}）` +
                `；viewer：${viewer?.ok ? `画完 ${viewer.detail.ready_ms} ms（请求 ${viewer.detail.tile_requests}/${viewer.detail.tile_count} 块，命中缓存 ${viewer.detail.hit}）` : `没通过（${viewer?.error}）`}`,
            )
          }
        } else {
          console.log('  （没有设置 M2_ACCEPT_SHELL：跳过壳的大图）')
        }
        await openFolder(page, ctx.origin, bench.id)
        await waitCovers(page)
        console.log(`STATE ${JSON.stringify({ timings: state.timings, rescanned: state.rescanned, shellRuns: state.shellRuns.map(({ steps, ...r }) => r), browser: Object.fromEntries(Object.entries(state).filter(([k]) => k.startsWith('browser '))) })}`)
      },
    },
  ],
}
