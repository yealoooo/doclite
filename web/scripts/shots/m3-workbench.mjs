// M3-6 的截图清单和断言：挂接工作台（/catalog 的页序条 + 待归类池）。
//
//   INIT_ADMIN_PASSWORD=admin123456 \
//     npm run screenshots -- --spec scripts/shots/m3-workbench.mjs --base http://localhost:<preview 端口> [--only …]
//
// 前端地址是 vite preview（/api 代理到 server）或托管了 web/dist 的 server。演示数据用 scripts/lib/m3-demo.mjs 的
// ensureM36Demo 建好（常驻，全部经接口：文件夹「M3-6 挂接演示」24 页、全宗 Z206 的归类树、办公室 BGS 的 2 个条目）；
// 2000 页的文件夹用 DEMO-A 已有的「2025 文书档案/盒 WS-2025-001」（只读，不挂接）。每张截图自己造的临时数据（文件夹、
// 节点、条目）在 after 里删掉（条目经删除接口，已定稿的先解锁；文件夹在条目删掉之后才删得掉），前面失败了也执行。
// 截图默认放 ../docs/screenshots/m3（--out 可改）。核对审计时直接读库（psql，DATABASE_URL；写脚本时还没有审计的读接口，
// M7-3 之后有 GET /api/system/audit-logs）。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - m3-workbench（demo_cataloger，工作台全貌）：打开演示条目第 1 件，页序条 6 页的顺序、审核状态、版本和接口一致；池里
//   「M3-6 挂接演示」"仅未归类"的张数和顺序和 children 接口（未归类的、按 sort_no）一致；关掉"仅未归类"后已归类的写
//   "在 0001 第 n 页"/"本条目第 n 页"，和 catalog-placements 一致。
// - m3-workbench-range（区间选择）：输入"第 3–8 张"→ 选中 6 张、正好是列表的第 3–8 张；再单击第 10 张、Shift 单击第
//   14 张 → 选中第 10–14 张（按显示位置）。
// - m3-workbench-link30（挂接 30 页）：临时文件夹 32 页、临时机构下的空条目；池里输入"第 1–30 张"→"挂到末尾"→ 用接口
//   核对页序（= 前 30 张，按 sort_no）、每页一条 link 日志（item_id、page_no 对）；池里只剩 2 张。
// - m3-workbench-drag（拖拽中另存 m3-workbench-dragging.png）：在上一张的条目里把第 2 页拖到第 6、7 页之间 → 用接口核对
//   页序；这几页没有新的 link/unlink 日志，审计多了正好一条 reorder（moved 是挪的那一页）、没有别的；再从池里把 2 个文件
//   拖到第 1 页前面 → 页序和 link 日志对。
// - m3-workbench-keyboard：空格拿起、→→ 放下（回车）；"移到第 1 页"；Alt+← ；Delete 移出 → 每一步用接口核对页序。
// - m3-workbench-review-sync：条目里有 1 页待审，另一个窗口（demo_reviewer，经接口）审核通过 → 3 秒内页序条的那一页
//   变成"通过"，条目 row_version 不变（接口和页面上都是）。
// - m3-workbench-conflict（冲突提示）：两个窗口（admin、demo_cataloger）打开同一个条目，本窗口断开实时连接（pagehide）；
//   另一个窗口用"移到第 1 页"改了页序 → 本窗口把一页 Alt+→ 后移 → 409 → 冲突横幅、重做在最新页序上的预览 → 点"保存这个
//   页序"→ 接口核对：两边的修改都在（数据不丢）。
// - m3-workbench-cataloged：池里关掉"仅未归类"，把已经挂在别的条目里的文件挂到当前条目 → 对话框写明"在 某档号 第 p 页"；
//   原条目定稿时"从原条目移出"置灰；没定稿的点"从原条目移出"→ 自动重试 → 文件到了当前条目、原条目里没了。
// - m3-workbench-404：池里选好一个文件，页面之外删掉它 → "挂到末尾"→ 提示文件已不存在、池刷新（那张消失）、条目没变。
// - m3-workbench-item-locked：断开实时连接后页面之外把条目定稿 → 拖动排序 → 409 item_locked → 提示，页序条变成只读。
// - m3-workbench-locked（定稿后只读）：3 页通过的条目经接口标记完成、定稿 → 撤回通过第 2 页（admin）→ 驳回（demo_reviewer）
//   → 重扫（v2）→ 页序条只读（不能拖、没有"移出"）、第 2 页标"定稿后新版本"，详情 new_version_after_lock 为 true。
//   顺序这样排就不用改 `lock_requires_approved`：定稿时 3 页都通过，之后审核状态照样能变（design §2.3）。
// - m3-workbench-delete：删除带 3 页的条目（确认框写明"将移出 3 页"）→ 那些文件回到"未归类"：池里重新出现，
//   catalog-placements 为 null，每页一条带 item_deleted 的 unlink 日志。
// - m3-workbench-path（M3-5 遗留）：另一个窗口（经接口）给条目所在节点改名 → 3 秒内详情里的路径（data-path 和文字）更新。
// - m3-workbench-create：池里选 3 个文件"用选中的文件新建条目"→ 新条目打开、3 页、顺序对。
// - m3-workbench-2000：池换到 2000 页的「盒 WS-2025-001」→ 滚到中间，池里的文件格少于 200 个（另打印池里全部 DOM 元素数）。

import { mkdirSync } from 'node:fs'

import {
  apiLogin,
  deleteItemsForCleanup,
  ensureM36Demo,
  interceptRequests,
  linkPages,
  lit,
  M36_FOLDER,
  requiredFieldValues,
  runSql,
  stamp,
  uploadPage,
} from '../lib/m3-demo.mjs'
import { makeScanPage } from '../lib/scan-pages.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'
const CATALOGER = 'demo_cataloger'
const IDLE = 'networkidle2'
// 另存的截图和清单的截图放在同一个目录：--out，默认 docs/screenshots/m3。
const OUT = (() => {
  const i = process.argv.indexOf('--out')
  return i > 0 ? process.argv[i + 1] : '../docs/screenshots/m3'
})()
const LIVE_MS = 3000

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

function assert(cond, message) {
  if (!cond) throw new Error(`断言失败：${message}`)
}

let ctxPromise = null
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const api = await apiLogin(origin, admin, adminPassword)
    const demo = await ensureM36Demo({ origin, admin: api, demoPassword })
    mkdirSync(OUT, { recursive: true })
    return { origin, api, demo, pid: demo.pid, c: demo.categories, cat: demo.users.cataloger, rev: demo.users.reviewer }
  })()
  return ctxPromise
}

/** 截图里有未保存的修改时离开页面会弹 beforeunload：一律接受。 */
const dialogHooked = new WeakSet()
function acceptDialogs(page) {
  if (dialogHooked.has(page)) return
  dialogHooked.add(page)
  page.on('dialog', (d) => void d.accept().catch(() => {}))
}

// ---------- 临时数据 ----------

const temps = { items: [], categories: [], folders: [] }
async function cleanupTemps(ctx) {
  const step = async (what, fn) => {
    try {
      await fn()
    } catch (e) {
      console.warn(`  清理 ${what} 出错：${e.message}`)
    }
  }
  const items = temps.items.splice(0)
  await step(`${items.length} 个条目`, () => deleteItemsForCleanup(ctx.api, items))
  for (const id of temps.folders.splice(0)) {
    await step(`文件夹 ${id}`, async () => {
      const r = await ctx.api('DELETE', `/api/folders/${id}`, undefined, { allow: [404, 409] })
      if (r?.status === 409) console.warn(`  文件夹 ${id} 删不掉：${r.message}`)
    })
  }
  for (const id of temps.categories.splice(0).reverse()) {
    await step(`节点 ${id}`, () => ctx.api('DELETE', `/api/projects/${ctx.pid}/archive-categories/${id}`, undefined, { allow: [404] }))
  }
}

/** 在 DEMO-A 根下建临时文件夹，传 n 页（名字 T_0001.png…，sort_no 按名字）。返回 {id, name, files}（按 sort_no）。 */
async function tempFolder(ctx, label, n) {
  const name = `截图验证 M3-6 ${label} ${stamp()}`
  const f = await ctx.api('POST', '/api/folders', {
    parent_id: ctx.demo.project.root_folder_id,
    folder_name: name,
    remark: 'M3-6 截图脚本临时建的，做完会删掉',
  })
  temps.folders.push(f.id)
  const seed = `M3-6 ${label} ${stamp()} ${Math.random()}`
  for (let i = 1; i <= n; i++) await uploadPage(ctx.api, f.id, `T_${String(i).padStart(4, '0')}.png`, `${seed}#${i}`, i)
  const files = (await ctx.api('GET', `/api/folders/${f.id}/children`)).files
  return { id: f.id, name, files }
}

/** 保管期限 Y 下的临时机构节点（档号完整：Z206-WS·2025-Y-<code>-0001）。 */
async function tempOrg(ctx, label) {
  const code = `T${stamp().slice(6)}${Math.floor(Math.random() * 90 + 10)}`
  const node = await ctx.api('POST', `/api/projects/${ctx.pid}/archive-categories`, {
    parent_id: ctx.c.y.id,
    category_type: 'org',
    category_code: code,
    category_name: `截图验证 M3-6 ${label}`,
  })
  temps.categories.push(node.id)
  return node
}

async function tempItem(ctx, categoryId, body = {}) {
  const it = await ctx.api('POST', `/api/archive-categories/${categoryId}/items`, body)
  temps.items.push(it.id)
  return it
}

// ---------- 接口核对 ----------

const getItem = (ctx, id) => ctx.api('GET', `/api/archive-items/${id}`)
const pageIds = (it) => it.pages.map((p) => p.file_id)
const fileLogs = async (ctx, fid) => (await ctx.api('GET', `/api/files/${fid}/logs`)).logs

/** 条目的审计（直接读库）。 */
function audits(itemId) {
  const out = runSql(
    `SELECT json_build_object('id', id, 'action', action, 'detail', detail)::text FROM audit_logs WHERE target_type = 'archive_item' AND target_id = ${lit(itemId)} ORDER BY id;`,
  )
  return out.split('\n').filter(Boolean).map((l) => JSON.parse(l))
}

/** 这些文件的 link、unlink 日志条数。 */
async function linkLogCount(ctx, fileIds) {
  let n = 0
  for (const fid of fileIds) n += (await fileLogs(ctx, fid)).filter((l) => l.action === 'link' || l.action === 'unlink').length
  return n
}

function sameList(a, b) {
  return a.length === b.length && a.every((x, i) => x === b[i])
}

function names(ctx, files, ids) {
  const m = new Map(files.map((f) => [f.file_id, f.file_name]))
  return ids.map((id) => m.get(id) ?? id.slice(-6)).join(',')
}

// ---------- 页面 ----------

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

async function waitPool(page, folderId) {
  await page.waitForSelector(`[data-testid=catalog-pool][data-folder-id="${folderId}"][data-state=ready]`, { timeout: 10000 })
}

/** 池里显示的文件（按显示位置）。 */
function poolCells(page) {
  return page.$$eval('[data-testid=catalog-pool] [data-testid=file-cell]', (els) =>
    els
      .map((e) => ({ fileId: e.dataset.fileId, position: Number(e.dataset.position), selected: e.getAttribute('aria-selected') === 'true' }))
      .sort((a, b) => a.position - b.position),
  )
}

/** 页序条上渲染出来的页（按页号）。 */
function stripCells(page) {
  return page.$$eval('[data-testid=item-pages] [data-testid=item-page]', (els) =>
    els
      .map((e) => ({
        fileId: e.dataset.fileId,
        pageNo: Number(e.dataset.pageNo),
        status: e.dataset.reviewStatus,
        version: Number(e.dataset.version),
        newVersion: e.dataset.newVersion === 'true',
        selected: e.dataset.selected === 'true',
        mark: e.dataset.mark ?? null,
      }))
      .sort((a, b) => a.pageNo - b.pageNo),
  )
}

/** 等页序条的页（按页号）正好是 ids（虚拟滚动：渲染出来的每一页都要在它的页号上）。 */
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
  await page.waitForSelector(`[data-testid=catalog-pool][data-range="${a}-${b}"]`, { timeout: 3000 })
}

async function clickStripPage(page, fileId, mods = {}) {
  const sel = `[data-testid=item-pages] [data-testid=item-page][data-file-id="${fileId}"]`
  await page.waitForSelector(sel, { timeout: 5000 })
  if (mods.shift) await page.keyboard.down('Shift')
  await page.click(sel)
  if (mods.shift) await page.keyboard.up('Shift')
}

async function waitToast(page, text, timeout = 8000) {
  await page.waitForFunction(
    (t) => [...document.querySelectorAll('[data-sonner-toast]')].some((el) => el.textContent?.includes(t)),
    { timeout },
    text,
  )
}

async function confirmAlert(page) {
  await page.waitForSelector('[role=alertdialog]', { visible: true, timeout: 5000 })
  await sleep(200)
  const buttons = await page.$$('[role=alertdialog] button')
  await buttons[buttons.length - 1].click()
  await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 10000 })
}

/** 断开页面的实时连接（派发 pagehide，和离开页面一样），恢复用 reconnect。 */
async function disconnect(page) {
  await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })))
  await page.waitForFunction(() => document.querySelector('[data-testid=catalog-page]')?.dataset.stream !== 'open', { timeout: 5000 })
}
async function reconnect(page) {
  await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })))
  await page.waitForFunction(() => document.querySelector('[data-testid=catalog-page]')?.dataset.stream === 'open', { timeout: 15000 })
}

/** 另一个浏览器上下文（独立的 Cookie）登录一个账号，返回页面。 */
async function otherWindow(page, username, password) {
  const ctx = await context(page)
  const bc = await page.browser().createBrowserContext()
  const other = await bc.newPage()
  await other.setViewport({ width: 1440, height: 900 })
  acceptDialogs(other)
  await other.goto(`${ctx.origin}/login`, { waitUntil: 'networkidle0' })
  await other.type('#username', username)
  await other.type('#password', password)
  await Promise.all([
    other.waitForFunction(() => !location.pathname.startsWith('/login'), { timeout: 15000 }),
    other.click('[data-testid=login-submit]'),
  ])
  return { other, close: () => bc.close() }
}

// ---------- 拖放 ----------

/** 元素中心点（先滚进可见范围）。 */
async function centerOf(page, sel) {
  const el = await page.waitForSelector(sel, { timeout: 5000 })
  await el.evaluate((e) => e.scrollIntoView({ block: 'nearest' }))
  const box = await el.boundingBox()
  return { x: box.x + box.width / 2, y: box.y + box.height / 2, box }
}

/**
 * 浏览器原生拖放：从 from 拖到 to（坐标），中途可以截一张"拖拽中"。用 puppeteer 的鼠标（headless Chrome 走真正的拖放
 * 流程：dragstart → dragover → drop → dragend）。
 */
async function dragAndDrop(page, from, to, { midShot } = {}) {
  await page.mouse.move(from.x, from.y)
  await page.mouse.down()
  await page.mouse.move(from.x + 8, from.y + 8, { steps: 4 })
  await page.mouse.move(to.x, to.y, { steps: 12 })
  await sleep(150)
  await page.mouse.move(to.x + 1, to.y, { steps: 2 })
  await sleep(150)
  if (midShot) await midShot()
  await page.mouse.up()
}

/** 页序条上第 gap 个空隙（第 gap 页和第 gap+1 页之间）的坐标：取第 gap+1 页的左边缘（同一行时）。 */
async function gapPoint(page, ids, gap) {
  if (gap < ids.length) {
    const c = await centerOf(page, `[data-testid=item-pages] [data-testid=item-page][data-file-id="${ids[gap]}"]`)
    return { x: c.box.x + 2, y: c.y }
  }
  const c = await centerOf(page, `[data-testid=item-pages] [data-testid=item-page][data-file-id="${ids[ids.length - 1]}"]`)
  return { x: c.box.x + c.box.width + 4, y: c.y }
}

export default {
  out: OUT,
  accounts: { [admin]: adminPassword, [CATALOGER]: demoPassword },
  waitUntil: IDLE,
  shots: [
    // ---------------------------------------------------------------- 工作台全貌
    {
      name: 'm3-workbench',
      user: CATALOGER,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it1 = ctx.demo.items.find((i) => i.item_no === 1)
        await openWorkbench(page, { category: ctx.c.bgs.id, item: it1.id, pool: ctx.demo.folder.id })
        // 页序条和接口一致。
        const api = await getItem(ctx, it1.id)
        await waitStrip(page, pageIds(api))
        const cells = await stripCells(page)
        for (const p of api.pages) {
          const c = cells.find((x) => x.fileId === p.file_id)
          assert(c && c.pageNo === p.page_no && c.status === p.review_status && c.version === p.version_no, `第 ${p.page_no} 页和接口不一致：${JSON.stringify(c)}`)
        }
        // 池："仅未归类"的和 children 接口一致（按 sort_no）。
        const children = (await ctx.api('GET', `/api/folders/${ctx.demo.folder.id}/children`)).files
        const want = children.filter((f) => !f.cataloged).map((f) => f.file_id)
        const shown = await poolCells(page)
        assert(sameList(shown.map((c) => c.fileId), want), `池里的文件和接口不一致：页面 ${shown.length} 张，接口 ${want.length} 张`)
        assert(shown.every((c, i) => c.position === i + 1), '池里的显示位置不是 1..n')
        // 关掉"仅未归类"：已归类的写"在 0001 第 n 页"/"本条目第 n 页"，和 placements 一致。
        await page.click('[data-testid=pool-only-uncataloged]')
        await page.waitForSelector(`[data-testid=catalog-pool][data-count="${children.length}"]`, { timeout: 3000 })
        const cataloged = children.filter((f) => f.cataloged).map((f) => f.file_id)
        const placements = (await ctx.api('POST', `/api/projects/${ctx.pid}/catalog-placements`, { file_ids: cataloged })).items
        await page.waitForFunction(
          (n) => [...document.querySelectorAll('[data-testid=catalog-pool] [data-testid=file-note][data-item-id]')].length >= n,
          { timeout: 5000 },
          Math.min(cataloged.length, 6),
        )
        const notes = await page.$$eval('[data-testid=catalog-pool] [data-testid=file-cell]', (els) =>
          els.map((e) => {
            const n = e.querySelector('[data-testid=file-note]')
            return { fileId: e.dataset.fileId, itemId: n?.dataset.itemId ?? null, pageNo: n?.dataset.pageNo ? Number(n.dataset.pageNo) : null, text: n?.textContent?.trim() ?? '' }
          }),
        )
        let checked = 0
        for (const p of placements) {
          const n = notes.find((x) => x.fileId === p.file_id)
          if (!n || !n.itemId) continue // 虚拟滚动：没渲染出来的不核对
          checked++
          assert(n.itemId === p.placement.item_id && n.pageNo === p.placement.page_no, `${p.file_id} 的"在哪个条目"和接口不一致：${JSON.stringify(n)}`)
          const mine = p.placement.item_id === it1.id
          const text = mine ? `本条目第 ${p.placement.page_no} 页` : `在 ${String(p.placement.item_no).padStart(4, '0')} 第 ${p.placement.page_no} 页`
          assert(n.text === text, `"在哪个条目"的文字应为「${text}」，实际「${n.text}」`)
        }
        assert(checked > 0, '没有核对到任何"在哪个条目"')
        console.log(`  页序条 ${api.pages.length} 页、池里未归类 ${want.length} 张和接口一致；关掉"仅未归类"后 ${checked} 个"在哪个条目"和 placements 一致`)
        // 截图：恢复"仅未归类"，页序条选第 3 页，池里选 3 张。
        await page.click('[data-testid=pool-only-uncataloged]')
        await page.waitForSelector(`[data-testid=catalog-pool][data-count="${want.length}"]`, { timeout: 3000 })
        await clickStripPage(page, api.pages[2].file_id)
        await rangeSelect(page, 2, 4)
        await page.mouse.move(5, 5)
        await sleep(500)
      },
    },

    // ---------------------------------------------------------------- 区间选择
    {
      name: 'm3-workbench-range',
      user: CATALOGER,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it1 = ctx.demo.items.find((i) => i.item_no === 1)
        await openWorkbench(page, { category: ctx.c.bgs.id, item: it1.id, pool: ctx.demo.folder.id })
        const children = (await ctx.api('GET', `/api/folders/${ctx.demo.folder.id}/children`)).files
        const want = children.filter((f) => !f.cataloged).map((f) => f.file_id)
        // "第 3–8 张"
        await rangeSelect(page, 3, 8)
        let sel = (await poolCells(page)).filter((c) => c.selected)
        assert(sameList(sel.map((c) => c.fileId), want.slice(2, 8)), `"第 3–8 张"选中的不对：${sel.map((c) => c.position).join(',')}`)
        // 单击第 10 张、Shift 单击第 14 张：按显示位置的区间。
        await page.click('[data-testid=catalog-pool] [data-testid=file-cell][data-position="10"]')
        await page.keyboard.down('Shift')
        await page.click('[data-testid=catalog-pool] [data-testid=file-cell][data-position="14"]')
        await page.keyboard.up('Shift')
        await page.waitForSelector('[data-testid=catalog-pool][data-range="10-14"][data-selected="5"]', { timeout: 3000 })
        sel = (await poolCells(page)).filter((c) => c.selected)
        assert(sameList(sel.map((c) => c.fileId), want.slice(9, 14)), `Shift 区间选中的不对：${sel.map((c) => c.position).join(',')}`)
        const text = await page.$eval('[data-testid=pool-selection-text]', (el) => el.textContent.trim())
        assert(text.includes('#10–#14') && text.includes('5 张'), `选择说明不对：${text}`)
        // 不合法的区间：提示，不改选择。
        await rangeSelectRaw(page, 9, 3)
        await page.waitForSelector('[data-testid=pool-range-error]', { timeout: 3000 })
        await page.waitForSelector('[data-testid=catalog-pool][data-range="10-14"]', { timeout: 1000 })
        await rangeSelectRaw(page, '', '')
        console.log(`  "第 3–8 张"选中 6 张、Shift 单击第 10、14 张选中第 10–14 张，都和列表的显示位置一致（${text}）；"第 9–3 张"提示不对`)
        await page.mouse.move(5, 5)
      },
    },

    // ---------------------------------------------------------------- 挂接 30 页
    {
      name: 'm3-workbench-link30',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '挂接 30 页', 32)
        const org = await tempOrg(ctx, '挂接 30 页')
        const it = await tempItem(ctx, org.id, { title: '挂接 30 页测试' })
        await openWorkbench(page, { category: org.id, item: it.id, pool: folder.id })
        await rangeSelect(page, 1, 30)
        const t0 = Date.now()
        await page.click('[data-testid=pool-link-end]')
        const want = folder.files.slice(0, 30).map((f) => f.file_id)
        await waitStrip(page, want, 10000)
        const ms = Date.now() - t0
        // 新挂上的页被选中（M3-6 审查第 1 轮：原来没选中、也没滚过去），第 1 个新页在看得到的地方。
        await page.waitForSelector('[data-testid=item-pages][data-selected="30"]', { timeout: 3000 })
        assert(await page.$(`[data-testid=item-pages] [data-testid=item-page][data-file-id="${want[0]}"][data-selected=true]`), '第 1 个新页应被选中并渲染出来')
        // 接口：页序、每页一条 link 日志。
        const api = await getItem(ctx, it.id)
        assert(sameList(pageIds(api), want), `接口里的页序不是前 30 张：${names(ctx, folder.files, pageIds(api))}`)
        assert(api.row_version === it.row_version + 1, `row_version 应 +1（${it.row_version} → ${api.row_version}）`)
        for (let i = 0; i < 30; i++) {
          const logs = (await fileLogs(ctx, want[i])).filter((l) => l.action === 'link' || l.action === 'unlink')
          assert(logs.length === 1 && logs[0].action === 'link', `${folder.files[i].file_name} 应正好一条 link 日志，实际 ${logs.map((l) => l.action).join(',')}`)
          const d = logs[0].detail
          assert(d.item_id === it.id && d.page_no === i + 1 && d.file_name === folder.files[i].file_name, `${folder.files[i].file_name} 的 link 日志不对：${JSON.stringify(d)}`)
        }
        // 池里只剩 2 张（"仅未归类"）。
        await page.waitForSelector('[data-testid=catalog-pool][data-count="2"]', { timeout: 5000 })
        console.log(`  "第 1–30 张"→"挂到末尾"：${ms} ms 后页序条 30 页；接口页序 = 前 30 张，30 条 link 日志的 item_id、page_no 都对；池里剩 2 张`)
        // 选第 25 页，把剩下的 2 张"插到第 25 页之后"：新页在第 26、27 页，被选中并滚到看得见的地方。
        await page.$eval('[data-testid=item-pages-scroll]', (el) => el.scrollTo(0, 0))
        await clickStripPage(page, want[24])
        await page.$eval('[data-testid=item-pages-scroll]', (el) => el.scrollTo(0, 0))
        await rangeSelect(page, 1, 2)
        await page.waitForSelector('[data-testid=pool-link-after]:not([disabled])', { timeout: 3000 })
        await page.click('[data-testid=pool-link-after]')
        const rest = folder.files.slice(30).map((f) => f.file_id)
        const want2 = [...want.slice(0, 25), ...rest, ...want.slice(25)]
        await waitStrip(page, want2, 5000)
        assert(sameList(pageIds(await getItem(ctx, it.id)), want2), '"插到第 25 页之后"的页序不对')
        await page.waitForSelector('[data-testid=item-pages][data-selected="2"]', { timeout: 3000 })
        const visible = await page.evaluate((fid) => {
          const box = document.querySelector('[data-testid=item-pages-scroll]').getBoundingClientRect()
          const el = document.querySelector(`[data-testid=item-page][data-file-id="${fid}"]`)
          if (!el || el.dataset.selected !== 'true') return false
          const r = el.getBoundingClientRect()
          return r.top >= box.top - 1 && r.bottom <= box.bottom + 1
        }, rest[0])
        assert(visible, '插入的第 1 个新页应被选中、滚到看得见的地方')
        console.log('  选第 25 页后"插到第 25 页之后"：2 个新页在第 26、27 页，被选中并滚到看得见的地方')
        await page.mouse.move(5, 5)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 拖拽重排、从池拖进页序条
    {
      name: 'm3-workbench-drag',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '拖拽', 12)
        const org = await tempOrg(ctx, '拖拽')
        const ids = folder.files.map((f) => f.file_id)
        const it = await tempItem(ctx, org.id, { title: '拖拽排序测试', file_ids: ids.slice(0, 8) })
        await openWorkbench(page, { category: org.id, item: it.id, pool: folder.id })
        await waitStrip(page, ids.slice(0, 8))
        const logsBefore = await linkLogCount(ctx, ids.slice(0, 8))
        const auditsBefore = audits(it.id).length

        // 把第 2 页拖到第 6、7 页之间（中途另存"拖拽中"）。
        const from = await centerOf(page, `[data-testid=item-pages] [data-testid=item-page][data-file-id="${ids[1]}"]`)
        const to = await gapPoint(page, ids.slice(0, 8), 6)
        await dragAndDrop(page, from, to, {
          midShot: async () => {
            await page.waitForSelector('[data-testid=page-drop-marker][data-gap="6"]', { timeout: 3000 })
            await page.screenshot({ path: `${OUT}/m3-workbench-dragging.png` })
          },
        })
        const want = [ids[0], ids[2], ids[3], ids[4], ids[5], ids[1], ids[6], ids[7]]
        await waitStrip(page, want, 5000)
        const api = await getItem(ctx, it.id)
        assert(sameList(pageIds(api), want), `拖动后接口里的页序不对：${names(ctx, folder.files, pageIds(api))}`)
        // 只有一条 reorder 审计，没有 link/unlink。
        const logsAfter = await linkLogCount(ctx, ids.slice(0, 8))
        assert(logsAfter === logsBefore, `重排后不应有新的 link/unlink 日志（${logsBefore} → ${logsAfter}）`)
        const newAudits = audits(it.id).slice(auditsBefore)
        assert(newAudits.length === 1 && newAudits[0].action === 'reorder', `重排后应正好一条 reorder 审计，实际 ${newAudits.map((a) => a.action).join(',') || '没有'}`)
        const moved = newAudits[0].detail.moved ?? []
        assert(moved.length === 1 && moved[0].file_id === ids[1] && moved[0].from_page === 2 && moved[0].to_page === 6, `reorder 的 moved 不对：${JSON.stringify(moved)}`)
        console.log('  第 2 页拖到第 6、7 页之间：接口页序对，没有新的 link/unlink 日志，正好一条 reorder 审计（moved 第 2 页 → 第 6 页）')

        // 从池里把 2 个文件（第 9、10 张）拖到第 1 页前面。
        await rangeSelect(page, 1, 2)
        const src = await centerOf(page, `[data-testid=catalog-pool] [data-testid=file-cell][data-file-id="${ids[8]}"]`)
        const dst = await gapPoint(page, want, 0)
        await dragAndDrop(page, src, dst)
        const want2 = [ids[8], ids[9], ...want]
        await waitStrip(page, want2, 5000)
        const api2 = await getItem(ctx, it.id)
        assert(sameList(pageIds(api2), want2), `从池里拖进来后接口里的页序不对：${names(ctx, folder.files, pageIds(api2))}`)
        for (const [i, fid] of [ids[8], ids[9]].entries()) {
          const link = (await fileLogs(ctx, fid)).find((l) => l.action === 'link')
          assert(link && link.detail.item_id === it.id && link.detail.page_no === i + 1, `${folder.files[8 + i].file_name} 的 link 日志不对`)
        }
        await page.waitForSelector('[data-testid=catalog-pool][data-count="2"]', { timeout: 5000 })
        console.log('  池里第 1–2 张拖到第 1 页前面：接口页序对，各一条 link 日志（第 1、2 页），池里剩 2 张')
        await page.mouse.move(5, 5)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 键盘
    {
      name: 'm3-workbench-keyboard',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '键盘', 6)
        const org = await tempOrg(ctx, '键盘')
        const [p1, p2, p3, p4, p5, p6] = folder.files.map((f) => f.file_id)
        const it = await tempItem(ctx, org.id, { title: '键盘排序测试', file_ids: [p1, p2, p3, p4, p5, p6] })
        await openWorkbench(page, { category: org.id, item: it.id, pool: folder.id })
        await waitStrip(page, [p1, p2, p3, p4, p5, p6])
        const check = async (want, what) => {
          await waitStrip(page, want, 5000)
          const api = await getItem(ctx, it.id)
          assert(sameList(pageIds(api), want), `${what}后接口里的页序不对：${names(ctx, folder.files, pageIds(api))}`)
        }
        // 1. 空格拿起第 1 页，→→ 移到第 2、3 页之间，回车放下。
        await clickStripPage(page, p1)
        await page.keyboard.press('Space')
        await page.waitForSelector('[data-testid=item-pages][data-picked=true]', { timeout: 2000 })
        await page.keyboard.press('ArrowRight')
        await page.keyboard.press('ArrowRight')
        await page.waitForSelector('[data-testid=page-drop-marker][data-gap="2"]', { timeout: 2000 })
        await page.keyboard.press('Enter')
        await check([p2, p1, p3, p4, p5, p6], '空格拿起、→→、回车放下')
        // 2. 选第 6 页"移到第 1 页"。
        await clickStripPage(page, p6)
        await page.$eval('[data-testid=page-move-to-input]', (el) => {
          el.value = '1'
          el.dispatchEvent(new Event('input', { bubbles: true }))
        })
        await page.click('[data-testid=page-move-to-submit]')
        await check([p6, p2, p1, p3, p4, p5], '"移到第 1 页"')
        // 3. Alt+→：选中的页（还是 p6）后移一页。
        await clickStripPage(page, p6)
        await page.keyboard.down('Alt')
        await page.keyboard.press('ArrowRight')
        await page.keyboard.up('Alt')
        await check([p2, p6, p1, p3, p4, p5], 'Alt+→')
        // 4. Delete 移出。
        await clickStripPage(page, p6)
        await page.keyboard.press('Delete')
        await check([p2, p1, p3, p4, p5], 'Delete 移出')
        const unlink = (await fileLogs(ctx, p6)).find((l) => l.action === 'unlink')
        assert(unlink && unlink.detail.item_id === it.id && unlink.detail.page_no === 2, `移出的页应有一条 unlink 日志（原第 2 页）：${JSON.stringify(unlink?.detail)}`)
        console.log('  空格拿起 →→ 回车、"移到第 1 页"、Alt+→、Delete 移出：每一步接口页序都对，移出有 unlink 日志')
        // 5. 一次移出 5 页以上（这里是全部）先确认；移出后提示里"撤销"放回原来的页序（M3-6 审查第 1 轮）。
        await clickStripPage(page, p2)
        await page.keyboard.down('Control')
        await page.keyboard.press('KeyA')
        await page.keyboard.up('Control')
        await page.waitForSelector('[data-testid=item-pages][data-selected="5"]', { timeout: 2000 })
        await page.keyboard.press('Delete')
        await page.waitForSelector('[role=alertdialog] [data-testid=page-remove-confirm][data-count="5"]', { timeout: 3000 })
        const ask = await page.$eval('[role=alertdialog]', (el) => el.textContent)
        assert(ask.includes('移出全部 5 页'), `确认框应写"移出全部 5 页"：${ask}`)
        assert((await getItem(ctx, it.id)).page_count === 5, '确认之前不应移出')
        await confirmAlert(page)
        await check([], '确认移出全部')
        await page.waitForFunction(
          () => !![...document.querySelectorAll('[data-sonner-toast]')].find((t) => t.textContent.includes('等 5 页'))?.querySelector('button[data-button]'),
          { timeout: 3000 },
        )
        const toastText = await page.evaluate(() => [...document.querySelectorAll('[data-sonner-toast]')].find((t) => t.textContent.includes('等 5 页'))?.textContent)
        assert(toastText.includes('已移出 T_0002.png 等 5 页') && toastText.includes('撤销'), `移出的提示应写文件名、带"撤销"：${toastText}`)
        await page.evaluate(() =>
          [...document.querySelectorAll('[data-sonner-toast]')].find((t) => t.textContent.includes('等 5 页')).querySelector('button[data-button]').click(),
        )
        await check([p2, p1, p3, p4, p5], '撤销移出')
        console.log('  全选 5 页按 Delete：先确认"移出全部 5 页"，确认后移出；点提示里的"撤销"放回原来的页序')
        await clickStripPage(page, p3)
        await page.mouse.move(5, 5)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 审核状态实时刷新
    {
      name: 'm3-workbench-review-sync',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '审核同步', 3)
        const ids = folder.files.map((f) => f.file_id)
        await ctx.api('POST', `/api/projects/${ctx.pid}/review-assignments`, { reviewer_id: ctx.demo.users.reviewerId, file_ids: ids })
        const org = await tempOrg(ctx, '审核同步')
        const it = await tempItem(ctx, org.id, { title: '审核状态同步测试', file_ids: ids })
        await openWorkbench(page, { category: org.id, item: it.id })
        const sel = `[data-testid=item-pages] [data-testid=item-page][data-file-id="${ids[1]}"]`
        await page.waitForSelector(`${sel}[data-review-status=pending]`, { timeout: 5000 })
        const rvBefore = await page.$eval('[data-testid=item-detail]', (el) => Number(el.dataset.rowVersion))
        // 另一个窗口（demo_reviewer，经接口）审核通过第 2 页。
        const t0 = Date.now()
        await ctx.rev('POST', `/api/files/${ids[1]}/review`, { result: 'approved' })
        await page.waitForSelector(`${sel}[data-review-status=approved]`, { timeout: LIVE_MS })
        const ms = Date.now() - t0
        await page.waitForSelector('[data-testid=item-pages-approved][data-count="1"]', { timeout: 2000 })
        const api = await getItem(ctx, it.id)
        const rvAfter = await page.$eval('[data-testid=item-detail]', (el) => Number(el.dataset.rowVersion))
        assert(api.row_version === it.row_version, `审核通过不应改条目的 row_version（${it.row_version} → ${api.row_version}）`)
        assert(rvAfter === rvBefore && rvAfter === api.row_version, `页面上的 row_version 应不变（${rvBefore} → ${rvAfter}）`)
        console.log(`  另一个窗口审核通过第 2 页：${ms} ms 后页序条变成"通过"；条目 row_version 仍是 ${api.row_version}（接口和页面）`)
        await page.mouse.move(5, 5)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 两个窗口改同一个条目：冲突
    {
      name: 'm3-workbench-conflict-resolved',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '冲突', 6)
        const org = await tempOrg(ctx, '冲突')
        const [p1, p2, p3, p4, p5, p6] = folder.files.map((f) => f.file_id)
        const it = await tempItem(ctx, org.id, { title: '两个窗口改页序', file_ids: [p1, p2, p3, p4, p5, p6] })
        await openWorkbench(page, { category: org.id, item: it.id })
        await waitStrip(page, [p1, p2, p3, p4, p5, p6])
        // 本窗口断开实时连接：收不到另一个窗口的修改（用来稳定地造出"后提交"）。
        await disconnect(page)
        const { other, close } = await otherWindow(page, CATALOGER, demoPassword)
        try {
          await other.goto(`${ctx.origin}/catalog?category=${org.id}&item=${it.id}`, { waitUntil: IDLE })
          await waitDetail(other, it.id)
          await waitStrip(other, [p1, p2, p3, p4, p5, p6])
          // 另一个窗口：第 5 页"移到第 1 页"。
          await clickStripPage(other, p5)
          await other.$eval('[data-testid=page-move-to-input]', (el) => {
            el.value = '1'
            el.dispatchEvent(new Event('input', { bubbles: true }))
          })
          await other.click('[data-testid=page-move-to-submit]')
          await waitStrip(other, [p5, p1, p2, p3, p4, p6])
          // 本窗口（还是旧的页序）：第 2 页 Alt+→ 后移一页 → 409 → 冲突横幅和预览。
          await clickStripPage(page, p2)
          await page.keyboard.down('Alt')
          await page.keyboard.press('ArrowRight')
          await page.keyboard.up('Alt')
          await page.waitForSelector('[data-testid=page-conflict][data-changed=true]', { timeout: 5000 })
          const preview = [p5, p1, p3, p2, p4, p6]
          await waitStrip(page, preview)
          const marked = (await stripCells(page)).filter((c) => c.mark === 'moved').map((c) => c.fileId)
          assert(sameList(marked, [p2]), `预览里应只标出挪动的第 2 页：${marked}`)
          assert((await page.$eval('[data-testid=item-detail]', (el) => el.dataset.pendingOp)) === 'move', '待确认的操作应算未保存的修改')
          // 这时接口里还是另一个窗口保存的页序（本窗口的操作没保存）。
          let api = await getItem(ctx, it.id)
          assert(sameList(pageIds(api), [p5, p1, p2, p3, p4, p6]), '冲突时接口里应是另一个窗口的页序')
          const banner = await page.$eval('[data-testid=page-conflict]', (el) => el.textContent.replace(/\s+/g, ' ').trim())
          assert(banner.includes('把 T_0002.png 移到 T_0004.png 前面'), `横幅应用文件名说明刚才的操作：${banner}`)
          // 有待确认的预览时，状态动作、移动、删除都先不让做（M3-6 审查第 1 轮）。
          for (const t of ['item-mark-done', 'item-move', 'item-delete']) {
            assert(await page.$eval(`[data-testid=${t}]`, (el) => el.disabled), `有待确认的预览时 ${t} 应禁用`)
          }
          assert(await page.$('[data-blocked] [data-testid=item-lock]'), '有待确认的预览时"定稿"应禁用并说明原因')
          await page.mouse.move(5, 5)
          await sleep(300)
          await page.screenshot({ path: `${OUT}/m3-workbench-conflict.png` })
          // 待确认的操作算未保存的修改：换节点先确认，取消就留在原处、预览还在。
          await page.click(`[data-testid=category-tree-area] [data-testid=tree-category][data-category-id="${ctx.c.bgs.id}"]`)
          await page.waitForSelector('[role=alertdialog]', { visible: true, timeout: 5000 })
          const ask = await page.$eval('[role=alertdialog]', (el) => el.textContent)
          assert(ask.includes('页序还有没确认的操作'), `换节点时应先确认：${ask}`)
          await sleep(200)
          const buttons = await page.$$('[role=alertdialog] button')
          await buttons[0].click()
          await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 5000 })
          await page.waitForSelector('[data-testid=item-detail][data-pending-op=move]', { timeout: 2000 })
          // 保存这个页序：两边的修改都在。
          await page.click('[data-testid=page-conflict-redo]')
          await page.waitForSelector('[data-testid=page-conflict]', { hidden: true, timeout: 5000 })
          await waitStrip(page, preview)
          api = await getItem(ctx, it.id)
          assert(sameList(pageIds(api), preview), `保存后接口里的页序不对：${names(ctx, folder.files, pageIds(api))}`)
          console.log(`  另一个窗口"移到第 1 页"后本窗口 Alt+→ → 409 冲突提示：「${banner.slice(0, 80)}…」；点"保存这个页序"后接口页序 = 5,1,3,2,4,6，两边的修改都在`)
        } finally {
          await close()
          await reconnect(page)
        }
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 别人只改了页序，我保存著录（M3-6 审查第 1 轮）
    {
      name: 'm3-workbench-save-after-pages',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '只改页序', 3)
        const ids = folder.files.map((f) => f.file_id)
        const org = await tempOrg(ctx, '只改页序')
        const it = await tempItem(ctx, org.id, { title: '原题名', file_ids: ids })
        await openWorkbench(page, { category: org.id, item: it.id })
        const setTitle = (v) =>
          page.$eval(
            '[data-testid=item-title]',
            (el, val) => {
              el.value = val
              el.dispatchEvent(new Event('input', { bubbles: true }))
            },
            v,
          )
        const rv = () => page.$eval('[data-testid=item-detail]', (el) => Number(el.dataset.rowVersion))
        let patches = 0
        const onReq = (req) => {
          if (req.method() === 'PATCH' && /\/api\/archive-items\/[^/]+$/.test(req.url())) patches++
        }
        page.on('request', onReq)
        try {
          // (a) 连着实时：A 改题名没保存 → B（demo_cataloger，经接口）只重排页 → A 收到事件，不提示"别人改过"，保存一次就成。
          await setTitle('A 的题名')
          await page.waitForSelector('[data-testid=item-detail][data-dirty=true]', { timeout: 2000 })
          let cur = await getItem(ctx, it.id)
          await ctx.cat('PUT', `/api/archive-items/${it.id}/files`, { row_version: cur.row_version, file_ids: [...ids].reverse() })
          await page.waitForFunction((v) => Number(document.querySelector('[data-testid=item-detail]')?.dataset.rowVersion) > v, { timeout: LIVE_MS }, cur.row_version)
          await waitStrip(page, [...ids].reverse())
          assert(!(await page.$('[data-testid=item-remote-changed]')), '别人只改了页序，不应提示"别人刚修改了这个条目"')
          assert((await page.$eval('[data-testid=item-title]', (el) => el.value)) === 'A 的题名', '表单里的修改应还在')
          patches = 0
          await page.click('[data-testid=item-save]')
          await page.waitForFunction(() => !document.querySelector('[data-testid=item-detail]')?.dataset.dirty, { timeout: 5000 })
          assert(!(await page.$('[data-testid=item-merge-note]')), '不应有合并提示')
          let api = await getItem(ctx, it.id)
          assert(api.title === 'A 的题名' && sameList(pageIds(api), [...ids].reverse()), `题名和 B 的页序都应在：${api.title}，${names(ctx, folder.files, pageIds(api))}`)
          assert(patches === 1, `应只保存一次，实际发了 ${patches} 次 PATCH`)
          console.log('  连着实时：B 只重排页后 A 保存，一次成功、没有冲突提示，B 的页序保留')

          // (b) 断开实时（A 不知道 B 改了）：保存 409 → 读最新的，著录没变 → 用新的 row_version 再发一次，不提示合并。
          await disconnect(page)
          await setTitle('A 的第二个题名')
          cur = await getItem(ctx, it.id)
          await ctx.cat('PUT', `/api/archive-items/${it.id}/files`, { row_version: cur.row_version, file_ids: ids })
          const stale = await rv()
          assert(stale === cur.row_version, '断开实时后页面不应知道 B 的修改')
          patches = 0
          await page.click('[data-testid=item-save]')
          await page.waitForFunction(() => !document.querySelector('[data-testid=item-detail]')?.dataset.dirty, { timeout: 5000 })
          assert(!(await page.$('[data-testid=item-merge-note]')), '著录没被别人改过时 409 后不应有合并提示')
          api = await getItem(ctx, it.id)
          assert(api.title === 'A 的第二个题名' && sameList(pageIds(api), ids), `题名和 B 的页序都应在：${api.title}`)
          assert(patches === 2, `应是 409 之后自动重发一次（共 2 次 PATCH），实际 ${patches} 次`)
          await reconnect(page)
          console.log('  断开实时：保存遇到 409，著录没被别人改过 → 自动用新的 row_version 重发一次成功，没有合并提示，B 的页序保留')
        } finally {
          page.off('request', onReq)
        }
        await page.mouse.move(5, 5)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 已在别的条目里：从原条目移出
    {
      name: 'm3-workbench-cataloged',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '已在别的条目', 4)
        const [f1, f2, f3, f4] = folder.files.map((f) => f.file_id)
        // f4 审核通过（定稿要求全部页通过）。
        await ctx.api('POST', `/api/projects/${ctx.pid}/review-assignments`, { reviewer_id: ctx.demo.users.reviewerId, file_ids: [f4] })
        await ctx.rev('POST', `/api/files/${f4}/review`, { result: 'approved' })
        const org = await tempOrg(ctx, '已在别的条目')
        const required = await requiredFieldValues(ctx.api, ctx.pid)
        const a = await tempItem(ctx, org.id, { title: '原条目 A', file_ids: [f1, f2] })
        const b = await tempItem(ctx, org.id, { title: '当前条目 B', file_ids: [f3] })
        let l = await tempItem(ctx, org.id, { title: '已定稿的原条目 L', extra_fields: required, file_ids: [f4] })
        l = await ctx.api('POST', `/api/archive-items/${l.id}/mark-done`, { row_version: l.row_version })
        l = await ctx.api('POST', `/api/archive-items/${l.id}/lock`, { row_version: l.row_version })
        await openWorkbench(page, { category: org.id, item: b.id, pool: folder.id })
        // 关掉"仅未归类"，选 f1（在 A 里）和 f4（在已定稿的 L 里），挂到末尾。
        await page.click('[data-testid=pool-only-uncataloged]')
        await page.waitForSelector('[data-testid=catalog-pool][data-count="4"]', { timeout: 3000 })
        await page.click(`[data-testid=catalog-pool] [data-testid=file-cell][data-file-id="${f1}"]`)
        await page.keyboard.down('Meta')
        await page.click(`[data-testid=catalog-pool] [data-testid=file-cell][data-file-id="${f4}"]`)
        await page.keyboard.up('Meta')
        await page.waitForSelector('[data-testid=catalog-pool][data-selected="2"]', { timeout: 2000 })
        await page.click('[data-testid=pool-link-end]')
        await page.waitForSelector('[data-testid=file-cataloged-dialog]', { visible: true, timeout: 5000 })
        const groups = await page.$$eval('[data-testid=cataloged-group]', (els) =>
          els.map((e) => ({
            itemId: e.dataset.itemId,
            locked: e.dataset.locked === 'true',
            disabled: e.querySelector('[data-testid=cataloged-move-out]')?.disabled ?? null,
            text: e.textContent.replace(/\s+/g, ' ').trim(),
          })),
        )
        const ga = groups.find((g) => g.itemId === a.id)
        const gl = groups.find((g) => g.itemId === l.id)
        assert(ga && !ga.locked && ga.disabled === false, `A 的"从原条目移出"应可用：${JSON.stringify(ga)}`)
        assert(gl && gl.locked && gl.disabled === true, `已定稿的 L 的"从原条目移出"应置灰：${JSON.stringify(gl)}`)
        assert(ga.text.includes(`${a.archive_no} 第 1 页`), `应写明"在 ${a.archive_no} 第 1 页"：${ga.text}`)
        assert(gl.text.includes(`${l.archive_no} 第 1 页`) && gl.text.includes('已定稿'), `应写明"在 ${l.archive_no} 第 1 页"和已定稿：${gl.text}`)
        assert((await getItem(ctx, b.id)).page_count === 1, '被拒时当前条目不应有变化')
        await sleep(300)
        await page.screenshot({ path: `${OUT}/m3-workbench-cataloged-dialog.png` })
        // 从 A 移出 → A 只剩 f2；L 还挡着 → "跳过还在别的条目里的，挂接其余的 1 个"。
        await page.click(`[data-testid=cataloged-group][data-item-id="${a.id}"] [data-testid=cataloged-move-out]`)
        await page.waitForSelector(`[data-testid=cataloged-group][data-item-id="${a.id}"][data-state=done]`, { timeout: 5000 })
        assert(sameList(pageIds(await getItem(ctx, a.id)), [f2]), '从原条目移出后 A 应只剩第 2 个文件')
        await page.click('[data-testid=cataloged-skip]')
        await page.waitForSelector('[data-testid=file-cataloged-dialog]', { hidden: true, timeout: 5000 })
        await waitStrip(page, [f3, f1], 5000)
        assert(sameList(pageIds(await getItem(ctx, b.id)), [f3, f1]), '跳过之后 B 应是 [f3, f1]')
        assert(sameList(pageIds(await getItem(ctx, l.id)), [f4]), '已定稿的 L 不应变')
        console.log('  f1（在 A）、f4（在已定稿的 L）挂到 B：对话框写明各在哪个档号第几页，L 的"从原条目移出"置灰；从 A 移出后跳过 L 的，B = [f3, f1]，L 不变')

        // 只有 A 的：从原条目移出后自动重试。移出进行中（把对 A 的 PUT 推迟 1.5 秒放行）按 Esc 关不掉、没有叉
        // （M3-6 审查第 1 轮：原来关掉对话框后仍会自动挂接）。
        await page.click(`[data-testid=catalog-pool] [data-testid=file-cell][data-file-id="${f2}"]`)
        await page.click('[data-testid=pool-link-end]')
        await page.waitForSelector('[data-testid=file-cataloged-dialog]', { visible: true, timeout: 5000 })
        await interceptRequests(
          page,
          (u) => u.includes(`/api/archive-items/${a.id}/files`),
          async (log) => {
            log.delayMs = 1500
            await page.click(`[data-testid=cataloged-group][data-item-id="${a.id}"] [data-testid=cataloged-move-out]`)
            await page.waitForSelector('[data-testid=file-cataloged-dialog][data-working=true]', { timeout: 3000 })
            await page.keyboard.press('Escape')
            await sleep(300)
            assert(await page.$('[data-testid=file-cataloged-dialog][data-working=true]'), '移出进行中按 Esc 不应关掉对话框')
            assert(!(await page.$('[data-testid=file-cataloged-dialog] [data-slot=dialog-close]')), '移出进行中不应有关闭的叉')
            assert(await page.$eval('[data-testid=cataloged-cancel]', (el) => el.disabled), '移出进行中"取消"应禁用')
            await page.waitForSelector('[data-testid=file-cataloged-dialog]', { hidden: true, timeout: 8000 })
          },
        )
        await waitStrip(page, [f3, f1, f2], 5000)
        assert(sameList(pageIds(await getItem(ctx, b.id)), [f3, f1, f2]), '自动重试后 B 应是 [f3, f1, f2]')
        assert((await getItem(ctx, a.id)).page_count === 0, 'A 应已没有页')
        console.log('  只有 A 的 f2：移出进行中 Esc、叉、取消都关不掉；移出完成后自动重试，B = [f3, f1, f2]，A 没有页了')
        await page.mouse.move(5, 5)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 404：要挂的文件已删除
    {
      name: 'm3-workbench-404',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '文件已删除', 3)
        const org = await tempOrg(ctx, '文件已删除')
        const it = await tempItem(ctx, org.id, { title: '要挂的文件已删除' })
        await openWorkbench(page, { category: org.id, item: it.id, pool: folder.id })
        await page.click(`[data-testid=catalog-pool] [data-testid=file-cell][data-file-id="${folder.files[1].file_id}"]`)
        // 断开实时连接，页面之外删掉这个文件（页面还以为它在）。
        await disconnect(page)
        await ctx.api('DELETE', `/api/folder-files/${folder.files[1].folder_file_id}`)
        await page.click('[data-testid=pool-link-end]')
        await waitToast(page, '个文件已不存在')
        await page.waitForSelector('[data-testid=catalog-pool][data-count="2"]', { timeout: 5000 })
        const shown = (await poolCells(page)).map((c) => c.fileId)
        assert(!shown.includes(folder.files[1].file_id), '池刷新后被删的文件应该不在了')
        assert((await getItem(ctx, it.id)).page_count === 0, '条目不应有变化')
        await reconnect(page)
        console.log('  页面之外删掉选中的文件后挂接：404 提示"文件已不存在"，池刷新（只剩 2 张），条目没变')
        await sleep(300)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 409 item_locked
    {
      name: 'm3-workbench-item-locked',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '刚被定稿', 3)
        const ids = folder.files.map((f) => f.file_id)
        await ctx.api('POST', `/api/projects/${ctx.pid}/review-assignments`, { reviewer_id: ctx.demo.users.reviewerId, file_ids: ids })
        await ctx.rev('POST', `/api/projects/${ctx.pid}/reviews:approve`, { file_ids: ids })
        const org = await tempOrg(ctx, '刚被定稿')
        const required = await requiredFieldValues(ctx.api, ctx.pid)
        let it = await tempItem(ctx, org.id, { title: '页序提交时刚被定稿', extra_fields: required, file_ids: ids })
        it = await ctx.api('POST', `/api/archive-items/${it.id}/mark-done`, { row_version: it.row_version })
        await openWorkbench(page, { category: org.id, item: it.id })
        await waitStrip(page, ids)
        await disconnect(page)
        await ctx.api('POST', `/api/archive-items/${it.id}/lock`, { row_version: it.row_version })
        await clickStripPage(page, ids[0])
        await page.keyboard.down('Alt')
        await page.keyboard.press('ArrowRight')
        await page.keyboard.up('Alt')
        await waitToast(page, '条目已定稿，页序不能再改')
        await page.waitForSelector('[data-testid=item-detail][data-status=locked]', { timeout: 5000 })
        await page.waitForSelector('[data-testid=item-pages][data-editable=false]', { timeout: 5000 })
        assert(sameList(pageIds(await getItem(ctx, it.id)), ids), '页序不应变')
        await reconnect(page)
        console.log('  页面之外定稿后再 Alt+→：409 item_locked 提示，重新读取后页序条只读，页序没变')
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 定稿后只读、定稿后有新版本
    {
      name: 'm3-workbench-locked',
      user: CATALOGER,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '定稿后新版本', 3)
        const ids = folder.files.map((f) => f.file_id)
        await ctx.api('POST', `/api/projects/${ctx.pid}/review-assignments`, { reviewer_id: ctx.demo.users.reviewerId, file_ids: ids })
        await ctx.rev('POST', `/api/projects/${ctx.pid}/reviews:approve`, { file_ids: ids })
        const org = await tempOrg(ctx, '定稿后新版本')
        const required = await requiredFieldValues(ctx.api, ctx.pid)
        let it = await tempItem(ctx, org.id, { title: '定稿后重扫的条目', extra_fields: required, file_ids: ids })
        it = await ctx.api('POST', `/api/archive-items/${it.id}/mark-done`, { row_version: it.row_version })
        it = await ctx.api('POST', `/api/archive-items/${it.id}/lock`, { row_version: it.row_version })
        assert(it.status === 'locked' && !it.new_version_after_lock, '刚定稿时不应有"定稿后有新版本"')
        // 定稿之后：撤回通过第 2 页（admin）→ 驳回（demo_reviewer）→ 重扫（v2）。重扫只接受驳回的页。
        await ctx.api('POST', `/api/files/${ids[1]}/reopen`)
        await ctx.rev('POST', `/api/files/${ids[1]}/review`, { result: 'rejected', reject_reasons: ['blur'], comment: 'M3-6 截图：定稿后重扫' })
        const p = makeScanPage({ seed: `M3-6 rescan ${stamp()} ${Math.random()}`, pageNo: 2 })
        const form = new FormData()
        form.append('file_name', folder.files[1].file_name)
        form.append('cover', new Blob([p.cover], { type: 'image/jpeg' }), 'cover.jpg')
        form.append('file', new Blob([p.png], { type: 'image/png' }), 'page.png')
        await ctx.api('POST', `/api/files/${ids[1]}/rescan`, form)
        const api = await getItem(ctx, it.id)
        assert(api.status === 'locked' && api.new_version_after_lock, '重扫后详情的 new_version_after_lock 应为 true')
        assert(api.pages[1].new_version_after_lock && api.pages[1].version_no === 2, '第 2 页应标出定稿后有新版本（v2）')
        assert(api.row_version === it.row_version, '重扫不应改条目的 row_version')
        await openWorkbench(page, { category: org.id, item: it.id })
        const cells = await stripCells(page)
        const c2 = cells.find((c) => c.fileId === ids[1])
        assert(c2?.newVersion && c2.version === 2, `页序条上第 2 页应标"定稿后新版本"、v2：${JSON.stringify(c2)}`)
        assert(cells.filter((c) => c.newVersion).length === 1, '只有第 2 页是定稿后的新版本')
        await page.waitForSelector('[data-testid=item-new-version-after-lock]', { timeout: 3000 })
        await page.waitForSelector('[data-testid=item-pages-new-version][data-count="1"]', { timeout: 3000 })
        // 只读：不能拖、没有"移出"、"移到第 n 页"，Delete 不移出。
        assert((await page.$eval('[data-testid=item-pages]', (el) => el.dataset.editable)) === 'false', '页序条应只读')
        const draggable = await page.$$eval('[data-testid=item-pages] [data-testid=item-page]', (els) => els.map((e) => e.getAttribute('draggable')))
        assert(draggable.every((d) => d !== 'true'), `定稿后页不能拖：${draggable}`)
        await clickStripPage(page, ids[0])
        assert(!(await page.$('[data-testid=page-remove]')) && !(await page.$('[data-testid=page-move-to]')), '定稿后不应有"移出"、"移到第 n 页"')
        await page.keyboard.press('Delete')
        await sleep(500)
        assert(sameList(pageIds(await getItem(ctx, it.id)), ids), 'Delete 不应移出')
        assert(!(await page.$('[data-testid=item-delete]')), '定稿后不应有"删除"')
        await page.waitForSelector('[data-testid=item-pages-frozen]', { timeout: 2000 })
        console.log('  定稿 → 撤回通过、驳回、重扫第 2 页：页序条第 2 页 v2、标"定稿后新版本"，详情写明；页序条只读（不能拖、没有移出、Delete 无效），条目 row_version 不变')
        // 双击看原图（定稿后也能看）：OriginalViewerDialog，左右切换在条目的页之间，v2 能"对比上一版"。
        const cell2 = await page.$(`[data-testid=item-pages] [data-testid=item-page][data-file-id="${ids[1]}"]`)
        await cell2.click({ count: 2 })
        await page.waitForSelector('[data-testid=original-viewer] [data-testid=image-viewer]', { visible: true, timeout: 10000 })
        const title = await page.$eval('[data-testid=viewer-title]', (el) => el.textContent.trim())
        assert(title === folder.files[1].file_name, `查看器应打开第 2 页（${folder.files[1].file_name}），实际 ${title}`)
        const head = await page.$eval('[data-testid=original-viewer]', (el) => el.textContent.replace(/\s+/g, ' '))
        assert(head.includes('2 / 3') && head.includes('v2'), `查看器应写"2 / 3"和 v2：${head.slice(0, 120)}`)
        assert(await page.$('[data-testid=viewer-compare]'), 'v2 应能"对比上一版"')
        await page.waitForFunction(() => document.querySelector('[data-testid=image-viewer]')?.dataset.state !== 'loading', { timeout: 15000 }).catch(() => {})
        await sleep(500)
        await page.screenshot({ path: `${OUT}/m3-workbench-viewer.png` })
        await page.click('[data-testid=viewer-close]')
        await page.waitForSelector('[data-testid=original-viewer]', { hidden: true, timeout: 5000 })
        console.log(`  双击第 2 页：查看器打开 ${title}（2 / 3 · v2，可以对比上一版）`)
        await page.evaluate(() => document.activeElement?.blur())
        await page.mouse.move(5, 5)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 删除带页的条目
    {
      name: 'm3-workbench-delete',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '删除条目', 3)
        const ids = folder.files.map((f) => f.file_id)
        const org = await tempOrg(ctx, '删除条目')
        const it = await tempItem(ctx, org.id, { title: '要删除的条目（带 3 页）', file_ids: ids })
        await openWorkbench(page, { category: org.id, item: it.id, pool: folder.id })
        await page.waitForSelector('[data-testid=catalog-pool] [data-testid=pool-empty]', { timeout: 5000 })
        await page.click('[data-testid=item-delete]')
        await page.waitForSelector('[role=alertdialog] [data-testid=item-delete-pages][data-count="3"]', { visible: true, timeout: 3000 })
        const desc = await page.$eval('[role=alertdialog]', (el) => el.textContent.replace(/\s+/g, ' ').trim())
        assert(desc.includes('3 页会一并移出'), `确认框应写明页数：${desc}`)
        await sleep(300)
        await page.screenshot({ path: `${OUT}/m3-workbench-delete-confirm.png` })
        await confirmAlert(page)
        await page.waitForSelector('[data-testid=item-detail]', { hidden: true, timeout: 5000 })
        // 那些文件回到"未归类"：池里重新出现（files.changed），placements 为 null，每页一条带 item_deleted 的 unlink。
        const t0 = Date.now()
        await page.waitForSelector('[data-testid=catalog-pool][data-count="3"]', { timeout: LIVE_MS })
        const ms = Date.now() - t0
        const placements = (await ctx.api('POST', `/api/projects/${ctx.pid}/catalog-placements`, { file_ids: ids })).items
        assert(placements.every((p) => p.placement === null), '删除后这些文件应未归类（placements 为 null）')
        for (const fid of ids) {
          const un = (await fileLogs(ctx, fid)).find((l) => l.action === 'unlink')
          assert(un && un.detail.item_deleted === true && un.detail.item_id === it.id, `应有带 item_deleted 的 unlink 日志：${JSON.stringify(un?.detail)}`)
        }
        const del = await ctx.api('GET', `/api/archive-items/${it.id}`, undefined, { allow: [404] })
        assert(del.status === 404, '条目应已删除')
        assert((await page.$$('[data-testid=item-row]')).length === 0, '列表里不应还有这个条目')
        temps.items = temps.items.filter((x) => x !== it.id)
        console.log(`  删除带 3 页的条目（确认框写明"3 页会一并移出"）：${ms} ms 内池里重新出现 3 张；placements 都是 null，每页一条 item_deleted 的 unlink`)
        // /files 的文件日志：unlink 写"条目已删除，从 … 移出"（lib/files.ts 的 logSummary）。
        await page.goto(`${ctx.origin}/files?folder=${folder.id}`, { waitUntil: IDLE })
        await page.waitForSelector(`[data-testid=file-cell][data-file-id="${ids[0]}"]`, { timeout: 10000 })
        await page.click(`[data-testid=file-cell][data-file-id="${ids[0]}"]`)
        await page.waitForFunction(
          () => [...document.querySelectorAll('[data-testid=file-logs] li')].some((e) => e.textContent.includes('条目已删除')),
          { timeout: 5000 },
        )
        const line = await page.$$eval('[data-testid=file-logs] li', (els) =>
          els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()).find((t) => t.includes('条目已删除')),
        )
        assert(line.includes(`条目已删除，从 ${it.archive_no} 移出（原第 1 页）`), `文件日志的说明不对：${line}`)
        console.log(`  /files 的文件日志：「${line}」`)
        await sleep(300)
        await page.screenshot({ path: `${OUT}/m3-workbench-delete-log.png` })
        // 回到归类页：池里这 3 张又是"未归类"（最后这张截图）。
        await openWorkbench(page, { category: org.id, pool: folder.id })
        await page.waitForSelector('[data-testid=catalog-pool][data-count="3"]', { timeout: 5000 })
        await page.mouse.move(5, 5)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 删除确认框打开期间条目被改了（M3-6 审查第 1 轮）
    {
      name: 'm3-workbench-delete-changed',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '删除时被改', 4)
        const ids = folder.files.map((f) => f.file_id)
        const org = await tempOrg(ctx, '删除时被改')
        const it = await tempItem(ctx, org.id, { title: '删除确认期间被别人挂了一页', file_ids: ids.slice(0, 3) })
        await openWorkbench(page, { category: org.id, item: it.id })
        await page.click('[data-testid=item-delete]')
        await page.waitForSelector('[role=alertdialog] [data-testid=item-delete-pages][data-count="3"]', { visible: true, timeout: 3000 })
        // 确认框开着的时候，另一个窗口（demo_cataloger，经接口）又挂了一页。
        await linkPages(ctx.cat, it.id, [ids[3]])
        await sleep(800)
        assert(
          (await page.$eval('[data-testid=item-delete-pages]', (el) => el.dataset.count)) === '3',
          '确认框里应还是打开时记下的页数',
        )
        const buttons = await page.$$('[role=alertdialog] button')
        await buttons[buttons.length - 1].click()
        await page.waitForSelector('[role=alertdialog] [data-testid=item-delete-changed]', { visible: true, timeout: 5000 })
        const changed = await page.$eval('[data-testid=item-delete-changed]', (el) => el.textContent)
        assert(changed.includes('现在 4 页') && changed.includes('打开确认框时 3 页'), `应提示条目刚被修改：${changed}`)
        assert((await page.$eval('[data-testid=item-delete-pages]', (el) => el.dataset.count)) === '4', '重新确认时应写现在的 4 页')
        assert((await getItem(ctx, it.id)).page_count === 4, '第一次确认不应删掉')
        await sleep(300)
        await page.screenshot({ path: `${OUT}/m3-workbench-delete-changed-confirm.png` })
        await confirmAlert(page)
        await page.waitForSelector('[data-testid=item-detail]', { hidden: true, timeout: 5000 })
        const gone = await ctx.api('GET', `/api/archive-items/${it.id}`, undefined, { allow: [404] })
        assert(gone.status === 404, '重新确认后应已删除')
        const placements = (await ctx.api('POST', `/api/projects/${ctx.pid}/catalog-placements`, { file_ids: ids })).items
        assert(placements.every((p) => p.placement === null), '4 页都应回到未归类')
        temps.items = temps.items.filter((x) => x !== it.id)
        console.log('  删除确认框开着时别人又挂了 1 页：确认 → 409，确认框不关，写明"现在 4 页（打开时 3 页）"；重新确认后删除，4 页都回到未归类')
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 移出操作的冲突预览（M3-6 审查第 1 轮）
    {
      name: 'm3-workbench-conflict-remove',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '移出冲突', 4)
        const [p1, p2, p3, p4] = folder.files.map((f) => f.file_id)
        const org = await tempOrg(ctx, '移出冲突')
        const it = await tempItem(ctx, org.id, { title: '移出时冲突', file_ids: [p1, p2, p3, p4] })
        await openWorkbench(page, { category: org.id, item: it.id })
        await waitStrip(page, [p1, p2, p3, p4])
        await disconnect(page)
        try {
          // 另一个人（demo_cataloger，经接口）把第 4 页挪到最前面；本窗口不知道，选第 2 页按 Delete → 409 → 预览。
          const cur = await getItem(ctx, it.id)
          await ctx.cat('PUT', `/api/archive-items/${it.id}/files`, { row_version: cur.row_version, file_ids: [p4, p1, p2, p3] })
          await clickStripPage(page, p2)
          await page.keyboard.press('Delete')
          await page.waitForSelector('[data-testid=page-conflict][data-changed=true]', { timeout: 5000 })
          const banner = await page.$eval('[data-testid=page-conflict]', (el) => el.textContent.replace(/\s+/g, ' ').trim())
          assert(banner.includes('移出 T_0002.png') && !banner.includes('第 2 页'), `横幅应用文件名、不用冲突前的页号：${banner}`)
          const cells = await stripCells(page)
          const order = (await page.$$eval('[data-testid=item-pages] [data-testid=item-page]', (els) => els.map((e) => e.dataset.fileId)))
          assert(sameList(order, [p4, p1, p2, p3]), '预览应按最新的页序显示，要移出的留在原位')
          const removed = cells.filter((c) => c.mark === 'removed').map((c) => c.fileId)
          assert(sameList(removed, [p2]), `要移出的那页应标成 removed（半透明）：${removed}`)
          const kept = cells.filter((c) => c.mark !== 'removed').sort((a, b) => a.pageNo - b.pageNo)
          assert(sameList(kept.map((c) => c.fileId), [p4, p1, p3]) && sameList(kept.map((c) => c.pageNo), [1, 2, 3]), '留下的页按移出之后重新编号')
          await page.mouse.move(5, 5)
          await sleep(300)
          await page.screenshot({ path: `${OUT}/m3-workbench-conflict-remove-preview.png` })
          await page.click('[data-testid=page-conflict-redo]')
          await page.waitForSelector('[data-testid=page-conflict]', { hidden: true, timeout: 5000 })
          await waitStrip(page, [p4, p1, p3])
          assert(sameList(pageIds(await getItem(ctx, it.id)), [p4, p1, p3]), '保存后应是别人的页序再移出第 2 个文件')
          console.log(`  移出时冲突：横幅「${banner.slice(0, 60)}…」用文件名；预览里要移出的页半透明留在原位，留下的重新编号；保存后 = 4,1,3`)
        } finally {
          await reconnect(page)
        }
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 池的边界情况（M3-6 审查第 1 轮）
    {
      name: 'm3-workbench-pool-edge',
      user: CATALOGER,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const key = `doclite.catalog.pool.${ctx.pid}`
        const urlHasPool = () => page.evaluate(() => new URL(location.href).searchParams.has('pool'))
        const idleSel = '[data-testid=catalog-pool][data-state=idle]:not([data-folder-id])'

        // (a) 同一个文件夹重新加载（files.changed、folders.changed）时保留选择。
        const f1 = await tempFolder(ctx, '池选择', 6)
        await openWorkbench(page, { pool: f1.id })
        await rangeSelect(page, 2, 5)
        await uploadPage(ctx.api, f1.id, 'T_0099.png', `M3-6 pool-edge ${stamp()} ${Math.random()}`, 99)
        await page.waitForSelector('[data-testid=catalog-pool][data-count="7"]', { timeout: LIVE_MS })
        assert(await page.$('[data-testid=catalog-pool][data-selected="4"][data-range="2-5"]'), 'files.changed 之后选择应还在')
        await ctx.api('POST', '/api/folders', { parent_id: f1.id, folder_name: '子文件夹' })
        await sleep(1500)
        assert(await page.$('[data-testid=catalog-pool][data-selected="4"][data-range="2-5"]'), 'folders.changed 之后选择应还在')
        console.log('  池里选好第 2–5 张：往这个文件夹传一页（files.changed）、建子文件夹（folders.changed）之后选择都还在')

        // (b) 地址栏里的池文件夹被删：池回到空闲，地址里的 ?pool= 去掉。
        await ctx.api('DELETE', `/api/folders/${f1.id}`)
        await page.waitForSelector(idleSel, { timeout: LIVE_MS })
        await page.waitForFunction(() => !new URL(location.href).searchParams.has('pool'), { timeout: 3000 })
        console.log('  地址栏里的池文件夹被删：池回到空闲（目录树展开），地址里的 ?pool= 去掉了')

        // (c) 记住的（localStorage）池文件夹被删：池回到空闲，记住的这一项清掉。
        const f2 = await tempFolder(ctx, '池记住', 2)
        await page.evaluate((k, v) => localStorage.setItem(k, v), key, f2.id)
        await openWorkbench(page, {})
        await waitPool(page, f2.id)
        assert(!(await urlHasPool()), '这次池的文件夹应来自记住的，不在地址栏')
        await ctx.api('DELETE', `/api/folders/${f2.id}`)
        await page.waitForSelector(idleSel, { timeout: LIVE_MS })
        assert((await page.evaluate((k) => localStorage.getItem(k), key)) === null, '记住的池文件夹应清掉')
        console.log('  记住的（localStorage）池文件夹被删：池回到空闲，记住的这一项清掉了')

        // (d) ?pool= 不是 UUID：提示、去掉，用记住的。
        await page.evaluate((k, v) => localStorage.setItem(k, v), key, ctx.demo.folder.id)
        await page.goto(`${ctx.origin}/catalog?pool=abc`, { waitUntil: IDLE })
        await waitPool(page, ctx.demo.folder.id)
        await page.waitForFunction(() => !new URL(location.href).searchParams.has('pool'), { timeout: 3000 })
        await waitToast(page, '已忽略')
        console.log('  ?pool=abc：提示"已忽略"、去掉，池用记住的「M3-6 挂接演示」')

        // (e) ?pool= 是进不了的项目（DEMO-B，demo_cataloger 没有授权）的文件夹：不走全局 403 处理（不重拉会话），去掉，
        //     不误删这个项目记住的池文件夹。和不带 ?pool= 打开时比较 /api/me 的请求数。
        const demoB = (await ctx.api('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-B')
        const countMe = async (url) => {
          let n = 0
          const onReq = (r) => {
            if (/\/api\/me(\/menus)?(\?|$)/.test(r.url())) n++
          }
          page.on('request', onReq)
          try {
            await page.goto(url, { waitUntil: IDLE })
            await waitPool(page, ctx.demo.folder.id)
            await sleep(1000)
          } finally {
            page.off('request', onReq)
          }
          return n
        }
        const base = await countMe(`${ctx.origin}/catalog`)
        const forbidden = await countMe(`${ctx.origin}/catalog?pool=${demoB.root_folder_id}`)
        assert(forbidden === base, `进不了的 ?pool= 不应重拉会话：/api/me 请求 ${forbidden} 次，平常 ${base} 次`)
        assert(!(await urlHasPool()), '进不了的 ?pool= 应去掉')
        assert((await page.evaluate((k) => localStorage.getItem(k), key)) === ctx.demo.folder.id, '不应误删这个项目记住的池文件夹')
        assert(await page.$('[data-testid=catalog-page]'), '应还在归类页')
        console.log(`  ?pool= 是进不了的 DEMO-B 的文件夹：没有重拉会话（/api/me ${forbidden} 次，和平常一样），去掉后用记住的，记住的没被误删`)
        await page.mouse.move(5, 5)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 只带 ?pool= 时换项目（M3-6 审查第 1 轮）
    {
      name: 'm3-workbench-pool-switch',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const demoB = (await ctx.api('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-B')
        const bFolder = (await ctx.api('GET', `/api/folders/${demoB.root_folder_id}/children`)).folders[0]
        assert(bFolder, 'DEMO-B 下应有文件夹')
        const keyB = `doclite.catalog.pool.${demoB.id}`
        await openWorkbench(page, { pool: ctx.demo.folder.id })
        await page.evaluate((k, v) => localStorage.setItem(k, v), keyB, bFolder.id)
        await page.click('[data-testid=project-switcher-trigger]')
        await page.waitForSelector('[data-testid=project-option][data-code="DEMO-B"]', { visible: true })
        await page.click('[data-testid=project-option][data-code="DEMO-B"]')
        await page.waitForFunction(() => !new URL(location.href).searchParams.has('pool'), { timeout: 5000 })
        await waitPool(page, bFolder.id)
        assert((await page.evaluate((k) => localStorage.getItem(k), keyB)) === bFolder.id, '换项目时不应误删新项目记住的池文件夹')
        console.log(`  地址只带 DEMO-A 的 ?pool= 时换到 DEMO-B：?pool= 去掉，池打开 DEMO-B 记住的「${bFolder.folder_name}」，没被误删`)
      },
      async after(page) {
        const ctx = await context(page)
        const demoB = (await ctx.api('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-B')
        await page.evaluate((k) => localStorage.removeItem(k), `doclite.catalog.pool.${demoB.id}`).catch(() => {})
        const cur = await page.$eval('[data-testid=project-current]', (el) => el.textContent.trim()).catch(() => '')
        if (!cur.includes('文书档案')) {
          await page.click('[data-testid=project-switcher-trigger]')
          await page.waitForSelector('[data-testid=project-option][data-code="DEMO-A"]', { visible: true })
          await page.click('[data-testid=project-option][data-code="DEMO-A"]')
          await sleep(800)
        }
      },
    },

    // ---------------------------------------------------------------- 路径过期（M3-5 遗留）
    {
      name: 'm3-workbench-path',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        // 临时链：保管期限 R… / 机构 O1（档号完整）。
        const ret = await ctx.api('POST', `/api/projects/${ctx.pid}/archive-categories`, {
          parent_id: ctx.c.year.id,
          category_type: 'retention',
          category_code: `R${stamp().slice(8)}`,
          category_name: '截图验证 M3-6 路径',
        })
        temps.categories.push(ret.id)
        const org = await ctx.api('POST', `/api/projects/${ctx.pid}/archive-categories`, {
          parent_id: ret.id,
          category_type: 'org',
          category_code: 'O1',
          category_name: '路径测试机构',
        })
        // 两个收起着的节点（各有一个下级，树上显示为可展开、没展开）：后面把上级、所在节点分别移到它们下面（M3-6 审查第 1 轮）。
        const node = async (parentId, type, code, name) => {
          const n = await ctx.api('POST', `/api/projects/${ctx.pid}/archive-categories`, { parent_id: parentId, category_type: type, category_code: code, category_name: name })
          if (parentId === ctx.c.year.id) temps.categories.push(n.id)
          return n
        }
        const boxC = await node(ctx.c.year.id, 'custom', `C${stamp().slice(8)}`, '截图验证 M3-6 收起的甲')
        const boxC1 = await node(boxC.id, 'custom', 'C1', '甲的下级')
        // 乙放在条目所在节点的上级里，自定义类型（档号不取它）：所在节点移进去档号不变，事件的 item_ids 是 []。
        const boxR = await node(ret.id, 'custom', `Q${stamp().slice(8)}`, '截图验证 M3-6 收起的乙')
        const boxR1 = await node(boxR.id, 'custom', 'X1', '乙的下级')
        const folder = await tempFolder(ctx, '路径', 2)
        const it = await tempItem(ctx, org.id, { title: '路径会变的条目', file_ids: [folder.files[0].file_id] })
        await openWorkbench(page, { category: org.id, item: it.id, pool: folder.id })
        // 池里关掉"仅未归类"：挂着的那张写"本条目第 1 页"，悬停的路径（data-path）也要跟着节点改名更新。
        await page.click('[data-testid=pool-only-uncataloged]')
        const noteSel = `[data-testid=catalog-pool] [data-testid=file-cell][data-file-id="${folder.files[0].file_id}"] [data-testid=file-note][data-item-id]`
        await page.waitForSelector(noteSel, { timeout: 5000 })
        const notePath = () => page.$eval(noteSel, (el) => el.dataset.path)
        assert((await notePath()).endsWith('截图验证 M3-6 路径/路径测试机构'), `池里的路径不对：${await notePath()}`)
        const pathNow = () => page.$eval('[data-testid=item-category-path]', (el) => ({ path: el.dataset.path, text: el.textContent.trim() }))
        const before = await pathNow()
        assert(before.path.endsWith('截图验证 M3-6 路径/路径测试机构'), `路径不对：${before.path}`)
        // 另一个窗口（demo_cataloger，经接口）给条目所在节点改名：档号不变，事件 item_ids 为 []。
        let t0 = Date.now()
        await ctx.cat('PATCH', `/api/projects/${ctx.pid}/archive-categories/${org.id}`, { category_name: '路径测试机构（改名）' })
        await page.waitForFunction(
          () => {
            const el = document.querySelector('[data-testid=item-category-path]')
            return el?.dataset.path?.endsWith('/路径测试机构（改名）') && el.textContent.includes('路径测试机构（改名）')
          },
          { timeout: LIVE_MS, polling: 50 },
        )
        const ms1 = Date.now() - t0
        // 祖先节点改名。
        t0 = Date.now()
        await ctx.cat('PATCH', `/api/projects/${ctx.pid}/archive-categories/${ret.id}`, { category_name: '截图验证 M3-6 路径（上级改名）' })
        await page.waitForFunction(
          () => {
            const el = document.querySelector('[data-testid=item-category-path]')
            return el?.dataset.path?.includes('/截图验证 M3-6 路径（上级改名）/') && el.textContent.includes('截图验证 M3-6 路径（上级改名）')
          },
          { timeout: LIVE_MS, polling: 50 },
        )
        const ms2 = Date.now() - t0
        const api = await getItem(ctx, it.id)
        const after = await pathNow()
        assert(after.path === api.category_path, `详情的路径应和接口一致：${after.path} / ${api.category_path}`)
        assert(api.row_version === it.row_version, '改名不应改条目的 row_version')
        await page.waitForFunction(
          (sel) => document.querySelector(sel)?.dataset.path?.endsWith('截图验证 M3-6 路径（上级改名）/路径测试机构（改名）'),
          { timeout: LIVE_MS, polling: 50 },
          noteSel,
        )
        console.log(`  另一个窗口给条目所在节点改名：${ms1} ms 后详情路径更新；给上级节点改名：${ms2} ms 后更新（和接口的 category_path 一致）；池里"本条目第 1 页"的路径也更新了`)

        // 移到收起着的节点下面（树上目标的下级没加载，父子关系要重新取）：先移上级、再移所在节点本身。
        const shownText = (p) => p.split('/').slice(1).join(' / ')
        const shown = async (id) => !!(await page.$(`[data-testid=category-tree-area] [data-testid=tree-category][data-category-id="${id}"]`))
        assert((await shown(boxC.id)) && (await shown(boxR.id)), '目标节点应在树上')
        assert(!(await shown(boxC1.id)) && !(await shown(boxR1.id)), '移动之前目标节点应是收起着的（下级没显示）')
        const waitPathIs = async (what, fn) => {
          const t = Date.now()
          await fn()
          const want = (await getItem(ctx, it.id)).category_path
          await page
            .waitForFunction(
              (w, txt) => {
                const el = document.querySelector('[data-testid=item-category-path]')
                return el?.dataset.path === w && el.textContent.trim() === txt
              },
              { timeout: LIVE_MS, polling: 50 },
              want,
              shownText(want),
            )
            .catch(async () => {
              throw new Error(`${what}：${LIVE_MS} ms 内详情的路径没有更新成「${want}」，页面是 ${JSON.stringify(await pathNow())}`)
            })
          return { ms: Date.now() - t, want }
        }
        const m1 = await waitPathIs('上级移到收起的节点下面', () =>
          ctx.cat('PATCH', `/api/projects/${ctx.pid}/archive-categories/${ret.id}`, { parent_id: boxC.id }),
        )
        assert(m1.want.includes('/截图验证 M3-6 收起的甲/截图验证 M3-6 路径（上级改名）/'), `接口路径不对：${m1.want}`)
        const m2 = await waitPathIs('所在节点移到收起的节点下面', () =>
          ctx.cat('PATCH', `/api/projects/${ctx.pid}/archive-categories/${org.id}`, { parent_id: boxR.id }),
        )
        assert(m2.want.endsWith('/截图验证 M3-6 收起的乙/路径测试机构（改名）'), `接口路径不对：${m2.want}`)
        const final = await getItem(ctx, it.id)
        assert(final.archive_no === it.archive_no && final.row_version === it.row_version, '这两次移动都不应改条目的档号和 row_version（事件 item_ids 为 []）')
        console.log(`  另一个窗口把上级移到收起着的节点下面：${m1.ms} ms 后详情路径更新；把所在节点本身移过去：${m2.ms} ms 后更新（data-path 和文字都和接口一致）`)
        await page.mouse.move(5, 5)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 用选中的文件新建条目
    {
      name: 'm3-workbench-create',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const folder = await tempFolder(ctx, '新建条目', 4)
        const ids = folder.files.map((f) => f.file_id)
        const org = await tempOrg(ctx, '新建条目')
        await openWorkbench(page, { category: org.id, pool: folder.id })
        await rangeSelect(page, 2, 4)
        await page.click('[data-testid=pool-create-item]')
        await page.waitForSelector('[data-testid=item-detail][data-load=ready]', { timeout: 8000 })
        const itemId = await page.$eval('[data-testid=item-detail]', (el) => el.dataset.itemId)
        temps.items.push(itemId)
        await waitStrip(page, ids.slice(1), 5000)
        const api = await getItem(ctx, itemId)
        assert(sameList(pageIds(api), ids.slice(1)) && api.item_no === 1 && api.row_version === 1, `新条目不对：${JSON.stringify({ pages: names(ctx, folder.files, pageIds(api)), no: api.item_no, rv: api.row_version })}`)
        await page.waitForSelector('[data-testid=catalog-pool][data-count="1"]', { timeout: 5000 })
        console.log(`  "第 2–4 张"→"用选中的文件新建条目"：第 ${api.item_no} 件（${api.archive_no}）3 页、顺序对，池里剩 1 张`)
        await page.mouse.move(5, 5)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 2000 页的文件夹
    {
      name: 'm3-workbench-2000',
      user: CATALOGER,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const root = (await ctx.api('GET', `/api/folders/${ctx.demo.project.root_folder_id}/children`)).folders.find((f) => f.folder_name === '2025 文书档案')
        const big = root && (await ctx.api('GET', `/api/folders/${root.id}/children`)).folders.find((f) => f.folder_name === '盒 WS-2025-001')
        assert(big && big.file_count >= 2000, '找不到 2000 页的「2025 文书档案/盒 WS-2025-001」')
        const it1 = ctx.demo.items.find((i) => i.item_no === 1)
        await openWorkbench(page, { category: ctx.c.bgs.id, item: it1.id, pool: big.id })
        await page.waitForFunction(() => Number(document.querySelector('[data-testid=catalog-pool]')?.dataset.count) >= 2000, { timeout: 10000 })
        const count = await page.$eval('[data-testid=catalog-pool]', (el) => Number(el.dataset.count))
        await page.$eval('[data-testid=catalog-pool] [data-testid=file-scroll]', (el) => {
          el.scrollTop = el.scrollHeight / 2
        })
        await sleep(800)
        const stats = await page.$eval('[data-testid=catalog-pool]', (el) => ({
          cells: el.querySelectorAll('[data-testid=file-cell]').length,
          grid: el.querySelector('[data-testid=file-scroll]').querySelectorAll('*').length,
          all: el.querySelectorAll('*').length,
          positions: [...el.querySelectorAll('[data-testid=file-cell]')].map((c) => Number(c.dataset.position)),
        }))
        assert(stats.cells > 0 && stats.cells < 200, `池里的文件格应少于 200 个，实际 ${stats.cells}`)
        assert(stats.grid < 200, `池的列表里的 DOM 元素应少于 200 个，实际 ${stats.grid}`)
        const mid = Math.min(...stats.positions)
        assert(mid > 500, `应已滚到中间，现在最前面渲染的是第 ${mid} 张`)
        console.log(
          `  2000 页的文件夹（"仅未归类" ${count} 张）滚到中间：池里的文件格 ${stats.cells} 个（第 ${mid}–${Math.max(...stats.positions)} 张），` +
            `池的列表里 DOM 元素 ${stats.grid} 个（整个池含页头、筛选、按钮的图标共 ${stats.all} 个）`,
        )
        await page.mouse.move(5, 5)
      },
    },
  ],
}

/** 填"第 a–b 张"再点"选中"（不等结果）。 */
async function rangeSelectRaw(page, a, b) {
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
  if (a !== '') await page.click('[data-testid=pool-range-apply]')
}
