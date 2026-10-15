// M4-5 的截图清单和断言：导出页 /export（任务列表和进度、新建导出和预检、明细、交付后变更、补交）和 /catalog 上的导出入口。
//
//   INIT_ADMIN_PASSWORD=admin123456 \
//     npm run screenshots -- --spec scripts/shots/m4-export.mjs --base http://localhost:<preview 端口> [--only …]
//
// 前端地址是 vite preview（/api 代理到 server，server 要开着执行器：JOB_CONCURRENCY 不是 0）或托管了 web/dist 的 server。
// 演示数据用 scripts/lib/m4-demo.mjs 的 ensureM45Demo 建好（常驻，没有就建）：DEMO-A 的文件夹「M4-5 导出演示」16 页、
// 「M4-5 导出演示（大页）」160 页（带透明的 PNG，导出 PDF 要转换，一次约 10 秒，用来截进度），全宗 Z405 / WS / 2025 / Y /
// BGS（5 件：3 件定稿就绪，1 件没定稿且有页待审，1 件没有页）、DY（2 件大页，定稿）。截图写死到 ../docs/screenshots/m4。
// 各张按顺序依赖前面建的任务（D 建的原图包任务给 E、G 用），用 --only 单独跑后面几张时前面的会缺。
// 每跑一次新建 5–6 个导出任务（原图包、PDF 各约 9 页 3 MB，大页 PDF 约 9 MB，取消的两个不留导出物），导出物 7 天后
// 自动删除；下载到的 zip 放在系统临时目录，核对完删掉。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - m4-export-catalog-entry（demo_manager）：/catalog 的节点工具栏有"导出"、右键菜单有"导出…"、条目详情页头有"导出这件"。
// - m4-export-new-precheck（demo_manager）：在 BGS 的右键菜单点"导出…"→ 跳到 /export，右栏的范围是 BGS（名称路径）、
//   地址栏的参数已去掉；预检的件数、页数、就绪、不就绪、各原因件数、问题清单的条目和原因和接口（同样的参数直接调预检）
//   一致；去掉"只导出审核通过的"、用 CategoryPicker 加上 DY 节点后都重新预检，数字仍和接口一致；展开"查看全部问题"。
// - m4-export-item-picker（demo_manager）：带条目进来（`?new=item&ids=第 1 件`），从归类树再勾第 4 件 → 预检和接口一致
//   （1 件就绪、1 件不就绪）。
// - m4-export-download（demo_manager）：BGS 选原图包、"只导出就绪的 3 件" → 新任务出现在列表最上面（3 件 9 页、跳过 2 件），
//   界面上变成"完成"；点"下载"（先调 /download，再用隐藏的 iframe 打开预签名地址；审查第 1 轮）→ 浏览器下载到的 zip 里
//   的文件正好是明细的每一页（`<档号>/0001.png`）加目录.xlsx、清单.csv；清单每一行（档号、页号、zip 内文件名、file_id、
//   snapshot_id、版本号、sha256）和明细接口逐行一致，每个文件的 sha256 和清单一致；页面没有离开、没有触发 beforeunload。
//   再把 /download 返回的地址改成签名不对的（存储返回 403）、连不上的端口 → 提示"下载没有开始"，页面不跳走。
//   壳里（交给系统浏览器）headless 验证不了，M4-6 在 Mac 壳里亲手验证。
// - m4-export-files（-pages）（demo_manager）：明细对话框按件 3 行（档号、页数）、按页 9 行（file_id、snapshot_id、版本、
//   sha256）和明细接口、zip 里的清单一致。
// - m4-export-progress-end（demo_manager）：DY 在界面上建 PDF 导出 A，另用接口建 B、C（同时最多执行 2 个，C 排队）→ A 生成中
//   的进度条和接口的进度一致（另存 m4-export-progress.png）；在界面上取消排队的 C（已取消）和生成中的 B（另存
//   m4-export-cleaning.png，"正在清理"）→ 接口核对；A 完成。期间（按方法和路径数）不整页重拉列表、每个任务每秒最多
//   重拉一次、交付后变更最多随完成的任务各取一次。
// - m4-export-changes（demo_manager + 另一个窗口 demo_scanner）：用接口建一个 BGS 的 PDF 导出 T（不勾"只导出审核通过的"）
//   并等完成，列表上"交付后变更 0"；在页面之外撤回、驳回第 1 件第 1 页 → D 的原图包任务（勾了只导出通过的）的行马上标
//   "可能已过时"；打开 T 的对话框，驳回之后取的仍是空（没勾只导出通过的，审核状态不算）；另一个窗口 demo_scanner 在
//   /scan 的待重扫列表里重扫这一页 → 3 秒内 T 的对话框里出现"有新版本"，T 的行同时变成 1；没打开对话框的 D 在驳回后
//   25 秒内变成 1（审查第 1 轮：列表行最少隔 20 秒重取，3 秒内只要求对话框）。
// - m4-export-volume（demo_manager，新建导出开着）：前台每秒上传 1 页、连续 20 页 → /changes 不超过可视区里已完成的任务数
//   + 2（列表行 20 秒最多一轮），预检不超过 2 次，期间"开始导出"一直可用、预检结果区不闪；模拟切到后台再传 6 页 →
//   /changes、预检都是 0 次；回到前台 → 补跑一次预检、补取一轮交付后变更，"可能已过时"消失。临时文件夹跑完删掉。
// - m4-export-robustness（demo_manager，拦截请求模拟出错）：/changes 取失败 → 行上保留旧值并写"可能已过时，点开重试"、
//   不弹提示，对话框标出错（另存 m4-export-changes-error.png），恢复后约 5 秒自动重试成功；单个任务重拉一直 502 →
//   恢复后退避重试到"完成"；重连时整页重拉的响应扣住、期间建的任务先出现，旧响应回来后它还在。
// - m4-export-list（demo_manager）：T 的交付后变更对话框里点"只补交有变更的 1 件"→ 右栏是补交（resubmit_of），预检和接口
//   一致（另存 m4-export-resubmit.png）→ 建任务 → 新任务 resubmit_of 是 T、只含第 1 件（scope_ids、明细都是），第 1 页是
//   重扫后的版本；列表上显示"补交自"。after 里把重扫的那一页通过（演示数据复原）。
// - m4-export-states（demo_manager，**模拟的列表响应**）：把列表接口返回的前 4 个任务改写成失败（36 行问题页清单）、
//   等待重试、取消后正在清理、已过期 → 各自的显示、能做的操作对（过期的不能下载、可以按原来的范围重新导出；清理中的
//   不能再取消）；失败的原因能展开看全部；点过期的"按原来的范围重新导出"→ 右栏带着原来的范围、预检。
// - m4-export-no-entry（demo_cataloger，只有 catalog 菜单）：/catalog 上没有导出入口（工具栏、右键菜单、条目详情、导航）；
//   打开 /export 被挡到无权提示页；接口直接调预检、列表、任务详情、下载都是 403。

import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { apiLogin, stamp, uploadPage } from '../lib/m3-demo.mjs'
import { ensureApproved, ensureM45Demo, parseCsv, readZip } from '../lib/m4-demo.mjs'
import { makeScanPage } from '../lib/scan-pages.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'
const MANAGER = 'demo_manager'
const CATALOGER = 'demo_cataloger'
const SCANNER = 'demo_scanner'
const IDLE = 'networkidle2'
const OUT = '../docs/screenshots/m4'

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

function assert(cond, message) {
  if (!cond) throw new Error(`断言失败：${message}`)
}

function same(a, b) {
  return JSON.stringify(a) === JSON.stringify(b)
}

let ctxPromise = null
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const api = await apiLogin(origin, admin, adminPassword)
    const demo = await ensureM45Demo({ origin, admin: api, demoPassword })
    const mgr = await apiLogin(origin, MANAGER, demoPassword)
    const cat = await apiLogin(origin, CATALOGER, demoPassword)
    mkdirSync(OUT, { recursive: true })
    const bgs = demo.items.filter((i) => i.spec.category.id === demo.categories.bgs.id)
    const item = (n) => bgs.find((i) => i.spec.no === n)
    return { origin, api, mgr, cat, demo, pid: demo.pid, c: demo.categories, item, state: { created: [] } }
  })()
  return ctxPromise
}

async function shot(page, name) {
  await sleep(250)
  await page.screenshot({ path: path.join(OUT, `${name}.png`) })
  console.log(`  另存 ${name}.png`)
}

const dialogHooked = new WeakSet()
function acceptDialogs(page) {
  if (dialogHooked.has(page)) return
  dialogHooked.add(page)
  page.on('dialog', (d) => void d.accept().catch(() => {}))
}

async function waitToast(page, text, timeout = 8000) {
  await page.waitForFunction(
    (t) => [...document.querySelectorAll('[data-sonner-toast]')].some((el) => el.textContent?.includes(t)),
    { timeout },
    text,
  )
}

/** 等到没有含这段文字的提示（上一个提示消失后再触发同样的提示）。 */
async function waitNoToast(page, text, timeout = 12000) {
  await page.waitForFunction(
    (t) => ![...document.querySelectorAll('[data-sonner-toast]')].some((el) => el.textContent?.includes(t)),
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

async function closeDialog(page) {
  await page.keyboard.press('Escape')
  await page.waitForFunction(() => !document.querySelector('[role=dialog]') && document.body.style.pointerEvents !== 'none', { timeout: 5000 })
  await sleep(150)
}

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

// ---------- 导出页 ----------

async function waitExportPage(page) {
  await page.waitForSelector('[data-testid=export-page][data-list=ready]', { timeout: 15000 })
  await page.waitForFunction(() => document.querySelector('[data-testid=export-page]')?.dataset.stream === 'open', { timeout: 10000 })
}

async function waitPrecheckFresh(page, timeout = 10000) {
  await page.waitForSelector('[data-testid=export-precheck][data-state=ready][data-fresh=true]', { timeout })
}

/** 界面上的预检：数字（data-*）、汇总文字、问题清单。 */
async function readPrecheck(page) {
  return page.$eval('[data-testid=export-precheck]', (el) => ({
    item_count: Number(el.dataset.itemCount),
    page_count: Number(el.dataset.pageCount),
    ready_item_count: Number(el.dataset.readyItems),
    ready_page_count: Number(el.dataset.readyPages),
    not_ready_count: Number(el.dataset.notReady),
    not_ready_page_count: Number(el.dataset.notReadyPages),
    scopeText: el.querySelector('[data-testid=precheck-scope]')?.textContent.trim(),
    readyText: el.querySelector('[data-testid=precheck-ready]')?.textContent.trim(),
    reasons: Object.fromEntries([...el.querySelectorAll('[data-reason]')].map((r) => [r.dataset.reason, r.textContent.trim()])),
    issues: [...el.querySelectorAll('[data-testid=precheck-issue]')].map((i) => ({ id: i.dataset.itemId, reasons: i.dataset.reasons })),
  }))
}

async function apiPrecheck(ctx, body) {
  return ctx.mgr('POST', `/api/projects/${ctx.pid}/export-tasks:precheck`, body)
}

/** 界面的预检和接口的一致（allIssues：问题清单已展开，要逐条对上）。 */
function comparePrecheck(ui, api, label, allIssues) {
  for (const k of ['item_count', 'page_count', 'ready_item_count', 'ready_page_count', 'not_ready_count', 'not_ready_page_count']) {
    assert(ui[k] === api[k], `${label}：${k} 界面 ${ui[k]}，接口 ${api[k]}`)
  }
  assert(ui.scopeText === `${api.item_count.toLocaleString()} 件 · ${api.page_count.toLocaleString()} 页`, `${label}：范围的文字 ${ui.scopeText}`)
  assert(ui.readyText === `${api.ready_item_count} 件 · ${api.ready_page_count} 页`, `${label}：就绪的文字 ${ui.readyText}`)
  for (const [code, n] of Object.entries(api.reason_counts)) {
    if (n > 0) assert(ui.reasons[code]?.startsWith(`${n} 件`), `${label}：原因 ${code} 界面「${ui.reasons[code]}」，接口 ${n} 件`)
    else assert(!(code in ui.reasons), `${label}：原因 ${code} 接口是 0，界面不该显示`)
  }
  const want = api.not_ready.map((i) => ({ id: i.item_id, reasons: i.reasons.map((r) => r.code).join(',') }))
  const shown = allIssues ? want : want.slice(0, 3)
  assert(same(ui.issues, shown), `${label}：问题清单 界面 ${JSON.stringify(ui.issues)}，接口 ${JSON.stringify(shown)}`)
}

/** 在一个对话框里的归类树上依次点开节点（点一行会选中并展开）。 */
async function clickTreePath(page, scope, ids) {
  for (const id of ids) {
    const sel = `${scope} [data-testid=tree-category][data-category-id="${id}"]`
    await page.waitForSelector(sel, { timeout: 8000 })
    await page.$eval(sel, (el) => el.scrollIntoView({ block: 'center' }))
    await page.click(sel)
    await sleep(250)
  }
}

/** 模拟标签页切到后台（true）、回到前台（false）；null 去掉模拟。派发 visibilitychange。 */
async function setHidden(page, hidden) {
  await page.evaluate((h) => {
    if (h === null) {
      delete document.visibilityState
      delete document.hidden
    } else {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => (h ? 'hidden' : 'visible') })
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => h })
    }
    document.dispatchEvent(new Event('visibilitychange'))
  }, hidden)
}

function rowSel(id) {
  return `[data-testid=export-task][data-task-id="${id}"]`
}

async function rowInfo(page, id) {
  return page.$eval(rowSel(id), (el) => ({
    status: el.dataset.status,
    done: Number(el.dataset.progressDone),
    total: Number(el.dataset.progressTotal),
    cleaning: el.dataset.cleaningUp === 'true',
    changes: el.dataset.changes === undefined ? null : Number(el.dataset.changes),
    changesState: el.dataset.changesState,
    scope: el.dataset.scope,
    resubmitOf: el.dataset.resubmitOf ?? null,
  }))
}

async function waitRow(page, id, attrs, timeout = 30000) {
  const sel = rowSel(id) + Object.entries(attrs).map(([k, v]) => `[data-${k}="${v}"]`).join('')
  await page.waitForSelector(sel, { timeout })
}

async function apiWaitTask(ctx, id, statuses, timeout = 60000) {
  const t0 = Date.now()
  for (;;) {
    const t = await ctx.mgr('GET', `/api/export-tasks/${id}`)
    if (statuses.includes(t.status) && !t.cleaning_up) return t
    if (Date.now() - t0 > timeout) throw new Error(`任务 ${id} ${timeout} ms 内没有变成 ${statuses}（现在 ${t.status}）`)
    await sleep(300)
  }
}

async function apiFiles(ctx, id) {
  const out = []
  let cursor = null
  do {
    const q = new URLSearchParams({ limit: '2000' })
    if (cursor) q.set('cursor', cursor)
    const r = await ctx.mgr('GET', `/api/export-tasks/${id}/files?${q}`)
    out.push(...r.items)
    cursor = r.next_cursor
  } while (cursor)
  return out
}

/** 等列表最上面出现一个不在 known 里的任务，返回它的 id。 */
async function waitNewTask(page, known, timeout = 10000) {
  const handle = await page.waitForFunction(
    (k) => {
      const first = document.querySelector('[data-testid=export-task]')
      const id = first?.dataset.taskId
      return id && !k.includes(id) ? id : null
    },
    { timeout },
    known,
  )
  return handle.jsonValue()
}

async function taskIds(page) {
  return page.$$eval('[data-testid=export-task]', (els) => els.map((e) => e.dataset.taskId))
}

// ---------- 下载 ----------

async function allowDownloads(page, dir) {
  const client = await page.createCDPSession()
  const contextId = page.browserContext().id
  try {
    await client.send('Browser.setDownloadBehavior', { behavior: 'allow', downloadPath: dir, ...(contextId ? { browserContextId: contextId } : {}) })
  } catch {
    await client.send('Page.setDownloadBehavior', { behavior: 'allow', downloadPath: dir })
  }
}

/** 等下载目录里出现一个下载完的文件（没有 .crdownload，大小稳定）。 */
async function waitDownloaded(dir, timeout = 30000) {
  const t0 = Date.now()
  let last = ''
  for (;;) {
    const files = readdirSync(dir).filter((f) => !f.endsWith('.crdownload') && !f.startsWith('.'))
    const partial = readdirSync(dir).some((f) => f.endsWith('.crdownload'))
    if (files.length && !partial) {
      const f = path.join(dir, files[0])
      const sig = `${files[0]}:${statSync(f).size}`
      if (sig === last) return f
      last = sig
    }
    if (Date.now() - t0 > timeout) throw new Error(`${timeout} ms 内没有下载完（目录里：${readdirSync(dir).join(', ') || '空'}）`)
    await sleep(300)
  }
}

const sha256 = (buf) => createHash('sha256').update(buf).digest('hex')

// ---------- /scan 的待重扫列表（另一个窗口） ----------

async function rescanOnPage(page, fileId, name, png) {
  await page.waitForSelector('[data-testid=rescan-list][data-state=ready]', { timeout: 15000 })
  const item = `[data-testid=rescan-item][data-file-id="${fileId}"]`
  await page.waitForSelector(item, { timeout: 10000 })
  await page.$eval(item, (el) => el.scrollIntoView({ block: 'center' }))
  await page.click(`${item} [data-testid=rescan-item-upload]`)
  await page.waitForSelector(`[data-testid=rescan-dialog][data-file-id="${fileId}"] [data-testid=rescan-input]`, { timeout: 5000 })
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
  return page.$eval('[data-testid=rescan-dialog] [data-testid=rescan-result]', (el) => el.dataset.result)
}

export default {
  out: OUT,
  accounts: { [admin]: adminPassword, [MANAGER]: demoPassword, [CATALOGER]: demoPassword },
  waitUntil: IDLE,
  shots: [
    // ---------------------------------------------------------------- /catalog 上的入口
    {
      name: 'm4-export-catalog-entry',
      user: MANAGER,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        acceptDialogs(page)
        const it1 = ctx.item(1)
        await page.goto(`${ctx.origin}/catalog?category=${ctx.c.bgs.id}&item=${it1.id}`, { waitUntil: IDLE })
        await page.waitForSelector(`[data-testid=item-detail][data-item-id="${it1.id}"][data-load=ready]`, { timeout: 10000 })
        await page.waitForSelector('[data-testid=item-export]', { visible: true, timeout: 3000 })
        const tb = await page.$eval('[data-testid=category-export]', (b) => !b.disabled)
        assert(tb, '节点工具栏的"导出"应可用')
        const node = `[data-testid=category-tree-area] [data-testid=tree-category][data-category-id="${ctx.c.bgs.id}"]`
        await page.waitForSelector(node, { timeout: 8000 })
        await page.click(node, { button: 'right' })
        await page.waitForSelector('[data-testid=menu-category-export]', { visible: true, timeout: 3000 })
        await sleep(200)
      },
    },

    // ---------------------------------------------------------------- 新建导出和预检
    {
      name: 'm4-export-new-precheck',
      user: MANAGER,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        acceptDialogs(page)
        await page.goto(`${ctx.origin}/catalog?category=${ctx.c.bgs.id}`, { waitUntil: IDLE })
        const node = `[data-testid=category-tree-area] [data-testid=tree-category][data-category-id="${ctx.c.bgs.id}"]`
        await page.waitForSelector(node, { timeout: 8000 })
        await page.click(node, { button: 'right' })
        await page.waitForSelector('[data-testid=menu-category-export]', { visible: true, timeout: 3000 })
        await page.click('[data-testid=menu-category-export]')
        await page.waitForFunction(() => location.pathname === '/export' && !location.search, { timeout: 8000 })
        await waitExportPage(page)
        await page.waitForSelector(`[data-testid=export-new][data-mode=category] [data-testid=scope-entry][data-id="${ctx.c.bgs.id}"]`, { timeout: 5000 })
        await page.waitForFunction(
          (id) => !document.querySelector(`[data-testid=scope-entry][data-id="${id}"]`)?.textContent.includes('正在取名称'),
          { timeout: 8000 },
          ctx.c.bgs.id,
        )
        const label = await page.$eval(`[data-testid=scope-entry][data-id="${ctx.c.bgs.id}"]`, (e) => e.textContent.trim())
        assert(label.includes('M4-5 导出演示 / 文书档案 / 2025 年 / 永久 / 办公室'), `范围应显示节点的名称路径：${label}`)

        // 1. BGS、PDF、只导出通过的（默认勾选）。
        await waitPrecheckFresh(page)
        const checked = await page.$eval('[data-testid=export-only-approved]', (e) => e.dataset.checked)
        assert(checked === 'true', '"只导出审核通过的"应默认勾选')
        const base = { scope_type: 'category', scope_ids: [ctx.c.bgs.id], export_format: 'pdf', only_approved: true }
        const api1 = await apiPrecheck(ctx, base)
        comparePrecheck(await readPrecheck(page), api1, 'BGS 预检', false)
        assert(api1.item_count === 5 && api1.ready_item_count === 3 && api1.not_ready_count === 2, `BGS 应 5 件、3 件就绪：${JSON.stringify(api1)}`)
        const submit1 = await page.$eval('[data-testid=export-submit]', (b) => ({ text: b.textContent.trim(), onlyReady: b.dataset.onlyReady, disabled: b.disabled }))
        assert(submit1.text === '只导出就绪的 3 件' && submit1.onlyReady === 'true' && !submit1.disabled, `按钮应是"只导出就绪的 3 件"：${JSON.stringify(submit1)}`)

        // 2. 去掉"只导出审核通过的" → 重新预检。
        await page.click('[data-testid=export-only-approved]')
        await page.waitForFunction(() => document.querySelector('[data-testid=export-only-approved]')?.dataset.checked === 'false')
        await sleep(100)
        await waitPrecheckFresh(page)
        const api2 = await apiPrecheck(ctx, { ...base, only_approved: false })
        comparePrecheck(await readPrecheck(page), api2, '不勾只导出通过的', false)
        assert(api2.reason_counts.pages_not_approved === 0, '不勾时不该有"有页不是审核通过"')
        await page.click('[data-testid=export-only-approved]')
        await sleep(100)
        await waitPrecheckFresh(page)

        // 3. CategoryPicker 加上 DY → 两个节点一起预检。
        await page.click('[data-testid=scope-add-category]')
        await page.waitForSelector('[data-testid=category-picker][data-settled=true]', { timeout: 8000 })
        await clickTreePath(page, '[data-testid=category-picker]', [ctx.c.fonds.id, ctx.c.ws.id, ctx.c.year.id, ctx.c.y.id, ctx.c.dy.id])
        await page.click('[data-testid=category-picker-confirm]')
        await page.waitForSelector(`[data-testid=scope-entry][data-id="${ctx.c.dy.id}"]`, { timeout: 5000 })
        await sleep(100)
        await waitPrecheckFresh(page)
        const api3 = await apiPrecheck(ctx, { ...base, scope_ids: [ctx.c.bgs.id, ctx.c.dy.id] })
        comparePrecheck(await readPrecheck(page), api3, 'BGS + DY 预检', false)
        assert(api3.item_count === 7 && api3.ready_item_count === 5, `BGS + DY 应 7 件、5 件就绪：${api3.item_count}/${api3.ready_item_count}`)
        await page.click(`[data-testid=scope-entry][data-id="${ctx.c.dy.id}"] [data-testid=scope-entry-remove]`)
        await sleep(100)
        await waitPrecheckFresh(page)
        comparePrecheck(await readPrecheck(page), api1, '移除 DY 后', false)

        // 4. 展开全部问题。
        await page.click('[data-testid=precheck-show-all]')
        await sleep(150)
        comparePrecheck(await readPrecheck(page), api1, '展开全部问题', true)
        await page.$eval('[data-testid=export-precheck]', (el) => el.scrollIntoView({ block: 'end' }))
      },
    },

    // ---------------------------------------------------------------- 按条目选范围
    {
      name: 'm4-export-item-picker',
      user: MANAGER,
      path: '/export',
      async prepare(page) {
        const ctx = await context(page)
        const it1 = ctx.item(1)
        const it4 = ctx.item(4)
        await page.goto(`${ctx.origin}/export?new=item&ids=${it1.id}`, { waitUntil: IDLE })
        await waitExportPage(page)
        await page.waitForSelector(`[data-testid=export-new][data-mode=item] [data-testid=scope-entry][data-id="${it1.id}"]`, { timeout: 5000 })
        await waitPrecheckFresh(page)
        await page.waitForFunction((id) => document.querySelector(`[data-testid=scope-entry][data-id="${id}"]`)?.textContent.includes('Z405-WS·2025-Y-BGS-0001'), { timeout: 8000 }, it1.id)
        const one = await apiPrecheck(ctx, { scope_type: 'item', scope_ids: [it1.id], export_format: 'pdf', only_approved: true })
        comparePrecheck(await readPrecheck(page), one, '第 1 件', false)
        assert(one.ready_item_count === 1 && one.not_ready_count === 0, '第 1 件应就绪')

        await page.click('[data-testid=scope-add-item]')
        await page.waitForSelector('[data-testid=export-item-picker][data-settled=true]', { timeout: 8000 })
        await clickTreePath(page, '[data-testid=export-item-picker]', [ctx.c.fonds.id, ctx.c.ws.id, ctx.c.year.id, ctx.c.y.id, ctx.c.bgs.id])
        await page.waitForSelector('[data-testid=export-item-picker][data-list=ready]', { timeout: 8000 })
        const pre = await page.$eval(`[data-testid=picker-item][data-item-id="${it1.id}"]`, (e) => e.dataset.checked)
        assert(pre === 'true', '已经在范围里的第 1 件应显示为已勾')
        await page.click(`[data-testid=picker-item][data-item-id="${it4.id}"]`)
        await page.waitForSelector('[data-testid=export-item-picker-count][data-count="1"]', { timeout: 3000 })
        await shot(page, 'm4-export-item-picker-open')
        await page.click('[data-testid=export-item-picker-confirm]')
        await page.waitForSelector(`[data-testid=scope-entry][data-id="${it4.id}"]`, { timeout: 5000 })
        await sleep(100)
        await waitPrecheckFresh(page)
        const two = await apiPrecheck(ctx, { scope_type: 'item', scope_ids: [it1.id, it4.id], export_format: 'pdf', only_approved: true })
        comparePrecheck(await readPrecheck(page), two, '第 1、4 件', false)
        assert(two.ready_item_count === 1 && two.not_ready_count === 1, `第 1、4 件应 1 件就绪、1 件不就绪：${two.ready_item_count}/${two.not_ready_count}`)
        await page.click('[data-testid=precheck-show-all]')
        await sleep(150)
      },
    },

    // ---------------------------------------------------------------- 建任务、下载、核对 zip
    {
      name: 'm4-export-download',
      user: MANAGER,
      path: '/export',
      async prepare(page) {
        const ctx = await context(page)
        await page.goto(`${ctx.origin}/export?new=category&ids=${ctx.c.bgs.id}&format=images`, { waitUntil: IDLE })
        await waitExportPage(page)
        await waitPrecheckFresh(page)
        const fmt = await page.$eval('[data-testid=export-format][data-selected=true]', (e) => e.dataset.value)
        assert(fmt === 'images', `地址栏带 format=images 时应选中原图包：${fmt}`)
        const known = await taskIds(page)
        await page.click('[data-testid=export-submit]')
        const id = await waitNewTask(page, known)
        ctx.state.created.push(id)
        ctx.state.images = id
        await page.waitForSelector('[data-testid=export-new]', { hidden: true, timeout: 5000 })
        const t = await ctx.mgr('GET', `/api/export-tasks/${id}`)
        assert(t.export_format === 'images' && t.item_count === 3 && t.page_count === 9 && t.skipped_item_count === 2 && t.only_ready && t.only_approved,
          `新任务应是原图包、3 件 9 页、跳过 2 件：${JSON.stringify({ f: t.export_format, i: t.item_count, p: t.page_count, s: t.skipped_item_count })}`)
        const t0 = Date.now()
        await waitRow(page, id, { status: 'done' }, 60000)
        console.log(`  原图包任务在界面上 ${Date.now() - t0} ms 后变成完成`)

        // 下载：浏览器里用隐藏的 iframe 直接从预签名地址下载（审查第 1 轮：不用 location.href，不触发 beforeunload、
        // 不离开页面）。
        const dir = mkdtempSync(path.join(tmpdir(), 'doclite-m45-'))
        ctx.state.downloadDir = dir
        await allowDownloads(page, dir)
        await page.evaluate(() => {
          window.__beforeunload = 0
          window.addEventListener('beforeunload', () => window.__beforeunload++)
        })
        await page.click(`${rowSel(id)} [data-testid=task-download]`)
        await waitToast(page, '开始下载')
        await page.waitForSelector('iframe[data-testid=download-frame]', { timeout: 3000 })
        const file = await waitDownloaded(dir)
        assert(new URL(page.url()).pathname === '/export', `下载不应离开页面：${page.url()}`)
        assert((await page.evaluate(() => window.__beforeunload)) === 0, '下载不应触发 beforeunload')
        const dl = await ctx.mgr('GET', `/api/export-tasks/${id}/download`)
        assert(path.basename(file) === dl.file_name, `下载的文件名 ${path.basename(file)} 应是 ${dl.file_name}`)
        const zip = readZip(readFileSync(file))
        const rows = await apiFiles(ctx, id)
        assert(rows.length === 9, `明细应 9 页：${rows.length}`)
        const expectNames = rows.map((r) => `${r.archive_no}/${String(r.page_no).padStart(4, '0')}.png`)
        // 文件夹条目（`<档号>/`）单独核对：每件一个。
        const names = zip.filter((e) => !e.name.endsWith('/')).map((e) => e.name)
        const dirs = zip.filter((e) => e.name.endsWith('/')).map((e) => e.name)
        assert(same([...names].sort(), [...expectNames, '清单.csv', '目录.xlsx'].sort()), `zip 里的文件 ${JSON.stringify(names)} 应是明细的每一页加目录、清单`)
        assert(same([...dirs].sort(), [...new Set(rows.map((r) => `${r.archive_no}/`))].sort()), `zip 里的文件夹 ${JSON.stringify(dirs)} 应是每件一个`)
        const csv = parseCsv(zip.find((e) => e.name === '清单.csv').data.toString('utf8'))
        assert(same(csv[0], ['档号', '页号', 'zip 内文件名', 'file_id', 'snapshot_id', '版本号', 'sha256', '原文件名', '审核状态']), `清单的表头不对：${csv[0]}`)
        const body = csv.slice(1)
        assert(body.length === rows.length, `清单 ${body.length} 行，明细 ${rows.length} 页`)
        rows.forEach((r, i) => {
          const m = body[i]
          const want = [r.archive_no, String(r.page_no), expectNames[i], r.file_id, r.snapshot_id, String(r.version_no), r.content_sha256]
          assert(same(m.slice(0, 7), want), `清单第 ${i + 1} 行 ${JSON.stringify(m.slice(0, 7))} 和明细 ${JSON.stringify(want)} 不一致`)
          const entry = zip.find((e) => e.name === expectNames[i])
          assert(sha256(entry.data) === r.content_sha256, `${expectNames[i]} 的 sha256 和清单不一致`)
        })
        ctx.state.imagesRows = rows
        ctx.state.manifest = body
        console.log(`  下载 ${dl.file_name}（${statSync(file).size} 字节）：${names.length} 个文件、${dirs.length} 个文件夹，清单 ${body.length} 行和明细一致，sha256 都对`)
        rmSync(file, { force: true })

        // 存储返回错误、连不上：iframe 里显示的是一个页面 → 提示"下载没有开始"，页面不跳走（把 /download 返回的地址
        // 改成签名不对的、连不上的端口）。
        for (const [what, bad] of [
          ['签名不对（存储返回 403）', (u) => u.replace(/X-Amz-Signature=[0-9a-f]+/, 'X-Amz-Signature=' + '0'.repeat(64))],
          ['存储连不上', () => 'http://127.0.0.1:9/doclite-m45-unreachable.zip'],
        ]) {
          const rewrite = async (req) => {
            const u = new URL(req.url())
            if (req.method() !== 'GET' || u.pathname !== `/api/export-tasks/${id}/download`) return void req.continue().catch(() => {})
            const real = await ctx.mgr('GET', u.pathname)
            await req.respond({ status: 200, contentType: 'application/json', body: JSON.stringify({ ...real, url: bad(real.url) }) })
          }
          await page.setRequestInterception(true)
          page.on('request', rewrite)
          try {
            await waitNoToast(page, '下载没有开始')
            await page.click(`${rowSel(id)} [data-testid=task-download]`)
            await waitToast(page, '下载没有开始', 10000)
            assert(new URL(page.url()).pathname === '/export', `${what}：页面不应跳走`)
            assert((await page.evaluate(() => window.__beforeunload)) === 0, `${what}：不应触发 beforeunload`)
            console.log(`  ${what}：提示"下载没有开始"，页面没有跳走`)
          } finally {
            page.off('request', rewrite)
            await page.setRequestInterception(false)
          }
        }
        await waitNoToast(page, '开始下载')
        await page.click(`${rowSel(id)} [data-testid=task-download]`)
        await waitToast(page, '开始下载')
      },
      async after(page) {
        // 最后那一次下载放在临时目录里，等一下删掉。
        await sleep(1500)
        const ctx = await ctxPromise
        if (ctx?.state.downloadDir) rmSync(ctx.state.downloadDir, { recursive: true, force: true })
      },
    },

    // ---------------------------------------------------------------- 明细
    {
      name: 'm4-export-files-pages',
      user: MANAGER,
      path: '/export',
      async prepare(page) {
        const ctx = await context(page)
        const id = ctx.state.images
        assert(id, '要先跑 m4-export-download（建原图包任务）')
        await waitExportPage(page)
        await page.click(`${rowSel(id)} [data-testid=task-files]`)
        await page.waitForSelector(`[data-testid=task-files-dialog][data-task-id="${id}"][data-state=ready][data-loaded="9"]`, { timeout: 10000 })
        await sleep(300)
        const rows = ctx.state.imagesRows
        const items = await page.$$eval('[data-testid=task-file-item]', (els) => els.map((e) => ({ id: e.dataset.itemId, no: e.dataset.archiveNo, pages: Number(e.dataset.pages) })))
        const want = []
        for (const r of rows) {
          const last = want[want.length - 1]
          if (last && last.id === r.item_id) last.pages++
          else want.push({ id: r.item_id, no: r.archive_no, pages: 1 })
        }
        assert(same(items, want), `按件 ${JSON.stringify(items)} 和明细接口 ${JSON.stringify(want)} 不一致`)
        const manifestItems = [...new Set(ctx.state.manifest.map((m) => m[0]))]
        assert(same(items.map((i) => i.no), manifestItems), '按件的档号和 zip 里的清单不一致')
        await shot(page, 'm4-export-files')
        await page.click('[data-testid=task-files-view-pages]')
        await page.waitForSelector('[data-testid=task-files-dialog][data-view=pages] [data-testid=task-file-page]', { timeout: 5000 })
        await sleep(200)
        const pages = await page.$$eval('[data-testid=task-file-page]', (els) =>
          els.map((e) => [e.dataset.fileId, e.dataset.snapshotId, e.dataset.version, e.dataset.sha256, e.dataset.pageNo]),
        )
        const wantPages = ctx.state.manifest.map((m) => [m[3], m[4], m[5], m[6], m[1]])
        assert(same(pages, wantPages), `按页 ${pages.length} 行和 zip 里的清单不一致`)
      },
      async after(page) {
        await closeDialog(page).catch(() => {})
      },
    },

    // ---------------------------------------------------------------- 进度、排队、取消
    {
      name: 'm4-export-progress-end',
      user: MANAGER,
      path: '/export',
      async prepare(page) {
        const ctx = await context(page)
        // 按方法和路径数请求（不拦截，只记录）。
        const counts = { list: 0, task: new Map(), changes: 0 }
        const onRequest = (req) => {
          if (req.method() !== 'GET') return
          const u = new URL(req.url())
          if (/^\/api\/projects\/[^/]+\/export-tasks$/.test(u.pathname)) counts.list++
          const m = u.pathname.match(/^\/api\/export-tasks\/([^/]+)$/)
          if (m) counts.task.set(m[1], (counts.task.get(m[1]) ?? 0) + 1)
          if (/^\/api\/export-tasks\/[^/]+\/changes$/.test(u.pathname)) counts.changes++
        }
        await page.goto(`${ctx.origin}/export?new=category&ids=${ctx.c.dy.id}`, { waitUntil: IDLE })
        await waitExportPage(page)
        await waitPrecheckFresh(page)
        const api = await apiPrecheck(ctx, { scope_type: 'category', scope_ids: [ctx.c.dy.id], export_format: 'pdf', only_approved: true })
        comparePrecheck(await readPrecheck(page), api, 'DY 预检', false)
        page.on('request', onRequest)
        try {
          const known = await taskIds(page)
          const tStart = Date.now()
          await page.click('[data-testid=export-submit]')
          const a = await waitNewTask(page, known)
          ctx.state.created.push(a)
          const body = { scope_type: 'category', scope_ids: [ctx.c.dy.id], export_format: 'pdf', only_ready: true }
          const b = (await ctx.mgr('POST', `/api/projects/${ctx.pid}/export-tasks`, body)).id
          const c = (await ctx.mgr('POST', `/api/projects/${ctx.pid}/export-tasks`, body)).id
          ctx.state.created.push(b, c)
          // 别的窗口建的任务 3 秒内出现在列表里。
          await page.waitForSelector(rowSel(c), { timeout: 3000 })

          // A 生成中、进度在 15%–85% 之间；C 排队。
          await page.waitForFunction(
            (sel) => {
              const el = document.querySelector(sel)
              if (!el || el.dataset.status !== 'running') return false
              const d = Number(el.dataset.progressDone)
              const t = Number(el.dataset.progressTotal)
              return t > 0 && d >= t * 0.15 && d <= t * 0.85
            },
            { timeout: 40000, polling: 100 },
            rowSel(a),
          )
          const ui = await rowInfo(page, a)
          const apiA = await ctx.mgr('GET', `/api/export-tasks/${a}`)
          assert(apiA.progress_done >= ui.done && apiA.progress_done - ui.done <= 40, `进度：界面 ${ui.done}，接口 ${apiA.progress_done}（界面最多落后约 2 秒）`)
          assert(ui.total === apiA.progress_total, '进度的总页数应和接口一致')
          const cInfo = await rowInfo(page, c)
          assert(cInfo.status === 'queued', `C 应在排队（同时最多执行 2 个）：${cInfo.status}`)
          const text = await page.$eval(`${rowSel(a)} [data-testid=export-task-status]`, (e) => e.textContent.replace(/\s+/g, ' ').trim())
          assert(text.includes(`${ui.done} / ${ui.total} 页`), `进度文字：${text}`)
          await shot(page, 'm4-export-progress')

          // 取消排队的 C。
          await page.click(`${rowSel(c)} [data-testid=task-cancel]`)
          await confirmAlert(page)
          await waitRow(page, c, { status: 'canceled' }, 5000)
          const apiC = await ctx.mgr('GET', `/api/export-tasks/${c}`)
          assert(apiC.status === 'canceled' && !apiC.cleaning_up, `C 应已取消：${apiC.status} ${apiC.cleaning_up}`)
          assert(!(await page.$(`${rowSel(c)} [data-testid=task-cancel]`)), '已取消的不能再取消')

          // 取消生成中的 B：先显示"正在清理"。
          await waitRow(page, b, { status: 'running' }, 20000)
          await page.click(`${rowSel(b)} [data-testid=task-cancel]`)
          await confirmAlert(page)
          await waitRow(page, b, { status: 'canceled' }, 5000)
          const sawCleaning = await page.$(`${rowSel(b)}[data-cleaning-up=true]`)
          if (sawCleaning) await shot(page, 'm4-export-cleaning')
          console.log(`  取消生成中的任务：${sawCleaning ? '界面显示了"正在清理"' : '清理在界面刷新前就完成了'}`)
          await page.waitForSelector(`${rowSel(b)}[data-status=canceled]:not([data-cleaning-up])`, { timeout: 15000 })
          const apiB = await apiWaitTask(ctx, b, ['canceled'], 15000)
          assert(apiB.status === 'canceled' && !apiB.output_size, 'B 应已取消、没有导出物')

          // A 完成。
          await waitRow(page, a, { status: 'done' }, 60000)
          const elapsed = (Date.now() - tStart) / 1000
          await sleep(1500)
          console.log(`  A 用了 ${elapsed.toFixed(1)} 秒；请求：列表 ${counts.list} 次，A ${counts.task.get(a) ?? 0} 次，B ${counts.task.get(b) ?? 0} 次，C ${counts.task.get(c) ?? 0} 次，交付后变更 ${counts.changes} 次`)
          assert(counts.list === 0, `收到 export.changed 不应整页重拉列表：${counts.list} 次`)
          for (const [id, n] of counts.task) {
            assert(n <= Math.ceil(elapsed) + 3, `任务 ${id} 重拉了 ${n} 次（${elapsed.toFixed(1)} 秒，每秒最多一次）`)
          }
          assert(counts.changes <= 2, `交付后变更只该在 A 完成后取一次：${counts.changes} 次`)
          const doneA = await ctx.mgr('GET', `/api/export-tasks/${a}`)
          const rowA = await rowInfo(page, a)
          assert(doneA.status === 'done' && rowA.done === doneA.progress_done, '完成后界面和接口一致')
        } finally {
          page.off('request', onRequest)
        }
      },
    },

    // ---------------------------------------------------------------- 交付后变更（另一个窗口重扫）
    {
      name: 'm4-export-changes',
      user: MANAGER,
      path: '/export',
      async prepare(page) {
        const ctx = await context(page)
        const imagesId = ctx.state.images
        assert(imagesId, '要先跑 m4-export-download（建原图包任务）')
        // T：BGS 的 PDF，不勾"只导出审核通过的"。
        const t = await ctx.mgr('POST', `/api/projects/${ctx.pid}/export-tasks`, {
          scope_type: 'category',
          scope_ids: [ctx.c.bgs.id],
          export_format: 'pdf',
          only_approved: false,
          only_ready: true,
        })
        ctx.state.created.push(t.id)
        ctx.state.pdf = t.id
        await apiWaitTask(ctx, t.id, ['done'])
        await page.goto(`${ctx.origin}/export`, { waitUntil: IDLE })
        await waitExportPage(page)
        await waitRow(page, t.id, { status: 'done', changes: 0 }, 10000)
        await waitRow(page, imagesId, { changes: 0 }, 10000)

        // 在页面之外撤回、驳回第 1 件第 1 页。
        const fileId = ctx.item(1).pages[0].file_id
        ctx.state.rescanFile = fileId
        await ctx.api('POST', `/api/files/${fileId}/reopen`)
        await ctx.demo.users.reviewer('POST', `/api/files/${fileId}/review`, { result: 'rejected', reject_reasons: ['blur'], comment: 'M4-5 截图：交付后变更演示' })
        const rejectAt = Date.now()
        // 列表行（审查第 1 轮的规则）：马上标"可能已过时"，离上次取过 20 秒后才重取。
        await waitRow(page, imagesId, { 'changes-state': 'stale' }, 3000)
        assert(await page.$(`${rowSel(imagesId)} [data-testid=task-changes-stale]`), 'D 的行上应标"可能已过时"')

        // 打开 T 的对话框：按对话框的速度重取（去抖后马上），T 没勾"只导出审核通过的"，驳回不算变更。
        await page.click(`${rowSel(t.id)} [data-testid=task-changes]`)
        await page.waitForFunction(
          (id, at) => {
            const d = document.querySelector(`[data-testid=task-changes-dialog][data-task-id="${id}"]`)
            return d?.dataset.state === 'ready' && Number(d.dataset.fetchedAt) > at
          },
          { timeout: 5000 },
          t.id,
          rejectAt,
        )
        assert(await page.$('[data-testid=task-changes-dialog] [data-testid=task-changes-empty]'), 'T 没勾"只导出审核通过的"，驳回不算变更')
        const tRow = await rowInfo(page, t.id)
        assert(tRow.changes === 0 && tRow.changesState === 'ready', `对话框取到之后 T 的行也是最新的 0：${JSON.stringify(tRow)}`)

        // 另一个窗口：demo_scanner 在 /scan 的待重扫列表里重扫。
        const { other, close } = await otherWindow(page, SCANNER, demoPassword)
        ctx.state.closeOther = close
        await other.goto(`${ctx.origin}/scan`, { waitUntil: IDLE })
        const png = makeScanPage({ seed: `M4-5 rescan ${stamp()} ${Math.random()}`, pageNo: 1 }).png
        const name = ctx.item(1).pages[0].file_name
        const result = await rescanOnPage(other, fileId, name, png)
        const t0 = Date.now()
        assert(result === 'done', `重扫应成功：${result}`)
        await page.waitForSelector(`[data-testid=task-changes-dialog][data-task-id="${t.id}"] [data-testid=task-change-item][data-reasons*="new_version"]`, { timeout: 3000 })
        const inDialog = Date.now() - t0
        // 对话框开着的那个任务，列表行和对话框用同一份，马上一起变。
        await waitRow(page, t.id, { changes: 1 }, 500)
        const inRow = Date.now() - t0
        console.log(`  重扫后 ${inDialog} ms 对话框里出现"有新版本"，${inRow} ms T 的行变成 1`)
        assert(inDialog <= 3000, `对话框里交付后变更应在 3 秒内出现：${inDialog} ms`)
        const api = await ctx.mgr('GET', `/api/export-tasks/${t.id}/changes`)
        const ui = await page.$eval('[data-testid=task-changes-dialog]', (d) => ({
          changed: Number(d.dataset.changedCount),
          resubmit: Number(d.dataset.resubmitCount),
          items: [...d.querySelectorAll('[data-testid=task-change-item]')].map((e) => ({ id: e.dataset.itemId, reasons: e.dataset.reasons })),
        }))
        assert(ui.changed === api.changed_count && ui.resubmit === api.resubmit_item_ids.length, `对话框的计数和接口不一致：${JSON.stringify(ui)}`)
        assert(same(ui.items, api.items.map((i) => ({ id: i.item_id, reasons: i.reasons.map((r) => r.code).join(',') }))), '对话框的条目和原因和接口不一致')
        assert(same(api.resubmit_item_ids, [ctx.item(1).id]), `只有第 1 件要补交：${api.resubmit_item_ids}`)
        await close()
        ctx.state.closeOther = null
        // 没打开对话框的 D（勾了只导出通过的）：列表行在驳回后 25 秒内变成 1（行最少隔 20 秒重取）。
        await waitRow(page, imagesId, { changes: 1, 'changes-state': 'ready' }, Math.max(1000, rejectAt + 25_000 - Date.now()))
        console.log(`  没打开对话框的 D：驳回后 ${((Date.now() - rejectAt) / 1000).toFixed(1)} 秒列表行变成 1`)
      },
      async after() {
        const ctx = await ctxPromise
        await ctx?.state.closeOther?.().catch(() => {})
      },
    },

    // ---------------------------------------------------------------- 补交有变更的
    {
      name: 'm4-export-list',
      user: MANAGER,
      path: '/export',
      async prepare(page) {
        const ctx = await context(page)
        const src = ctx.state.pdf
        assert(src, '要先跑 m4-export-changes')
        await waitExportPage(page)
        await waitRow(page, src, { changes: 1 }, 5000)
        await page.click(`${rowSel(src)} [data-testid=task-changes]`)
        await page.waitForSelector(`[data-testid=task-changes-dialog][data-task-id="${src}"][data-state=ready] [data-testid=task-changes-resubmit][data-count="1"]`, { timeout: 5000 })
        await page.click('[data-testid=task-changes-resubmit]')
        await page.waitForSelector(`[data-testid=export-new][data-mode=resubmit] [data-testid=scope-resubmit][data-resubmit-of="${src}"]`, { timeout: 5000 })
        await waitPrecheckFresh(page)
        const onlyApproved = await page.$eval('[data-testid=export-only-approved]', (e) => e.dataset.checked)
        assert(onlyApproved === 'false', '补交沿用源任务的"只导出审核通过的"（没勾）')
        const api = await apiPrecheck(ctx, { resubmit_of: src, export_format: 'pdf', only_approved: false })
        comparePrecheck(await readPrecheck(page), api, '补交预检', false)
        assert(api.item_count === 1 && api.ready_item_count === 1 && same(api.scope_ids, [ctx.item(1).id]), `补交应是第 1 件：${JSON.stringify(api.scope_ids)}`)
        await shot(page, 'm4-export-resubmit')
        const known = await taskIds(page)
        await page.click('[data-testid=export-submit]')
        const id = await waitNewTask(page, known)
        ctx.state.created.push(id)
        const t = await ctx.mgr('GET', `/api/export-tasks/${id}`)
        assert(t.resubmit_of === src && t.scope_type === 'item' && same(t.scope_ids, [ctx.item(1).id]) && t.item_count === 1, `补交的任务不对：${JSON.stringify({ r: t.resubmit_of, s: t.scope_ids, n: t.item_count })}`)
        const files = await apiFiles(ctx, id)
        assert(files.every((f) => f.item_id === ctx.item(1).id) && files.length === 3, `补交的明细应只有第 1 件的 3 页：${files.length}`)
        const snaps = await ctx.mgr('GET', `/api/files/${ctx.state.rescanFile}/snapshots`)
        const latest = Math.max(...snaps.snapshots.map((s) => s.version_no))
        assert(files[0].version_no === latest, `第 1 页应是重扫后的版本 v${latest}：v${files[0].version_no}`)
        await waitRow(page, id, { status: 'done' }, 30000)
        const row = await rowInfo(page, id)
        assert(row.resubmitOf === src && row.scope === '补交 1 件', `列表上应显示补交：${JSON.stringify(row)}`)
        const tag = await page.$eval(rowSel(id), (e) => e.textContent)
        assert(tag.includes('补交自'), '列表上应写"补交自"')
        // 冷启动 `?resubmit=<T>`：先取源任务，沿用它的"只导出审核通过的"（T 没勾）和格式（审查第 1 轮）。
        await page.goto(`${ctx.origin}/export?resubmit=${src}`, { waitUntil: IDLE })
        await waitExportPage(page)
        await page.waitForSelector(`[data-testid=export-new][data-mode=resubmit] [data-testid=scope-resubmit][data-resubmit-of="${src}"]`, { timeout: 5000 })
        await waitPrecheckFresh(page)
        const cold = await page.$eval('[data-testid=export-only-approved]', (e) => e.dataset.checked)
        assert(cold === 'false', `冷启动的补交应沿用源任务的"只导出审核通过的"（没勾）：${cold}`)
        const coldFmt = await page.$eval('[data-testid=export-format][data-selected=true]', (e) => e.dataset.value)
        assert(coldFmt === 'pdf', `冷启动的补交应沿用源任务的格式：${coldFmt}`)
        await page.click('[data-testid=export-new-close]')
        await page.waitForSelector('[data-testid=export-new]', { hidden: true, timeout: 3000 })
        await page.$eval('[data-testid=export-task-list]', (el) => el.scrollTo(0, 0))
      },
      async after() {
        const ctx = await ctxPromise
        if (ctx?.state.rescanFile) await ensureApproved({ admin: ctx.api, rev: ctx.demo.users.reviewer, pid: ctx.pid, reviewerId: ctx.demo.users.reviewerId }, [ctx.state.rescanFile])
      },
    },

    // ---------------------------------------------------------------- 请求量（审查第 1 轮）
    {
      name: 'm4-export-volume',
      user: MANAGER,
      path: '/export',
      async prepare(page) {
        const ctx = await context(page)
        await page.goto(`${ctx.origin}/export?new=category&ids=${ctx.c.bgs.id}`, { waitUntil: IDLE })
        await waitExportPage(page)
        await waitPrecheckFresh(page)
        await sleep(1500)
        const visibleDone = await page.evaluate(() => {
          const box = document.querySelector('[data-testid=export-task-list]').getBoundingClientRect()
          return [...document.querySelectorAll('[data-testid=export-task][data-status=done]')].filter((e) => {
            const r = e.getBoundingClientRect()
            return r.bottom > box.top && r.top < box.bottom
          }).length
        })
        const counts = { changes: 0, precheck: 0 }
        const onRequest = (req) => {
          const u = new URL(req.url())
          if (/^\/api\/export-tasks\/[^/]+\/changes$/.test(u.pathname)) counts.changes++
          if (u.pathname.endsWith('/export-tasks:precheck')) counts.precheck++
        }
        // 期间"开始导出"按钮有没有被禁用过、预检有没有进过 loading（静默重跑不该有）。
        await page.evaluate(() => {
          window.__flicker = 0
          window.__flickerTimer = setInterval(() => {
            const b = document.querySelector('[data-testid=export-submit]')
            const p = document.querySelector('[data-testid=export-precheck]')
            if (b?.disabled || p?.dataset.state !== 'ready') window.__flicker++
          }, 50)
        })
        page.on('request', onRequest)
        const folder = await ctx.api('POST', '/api/folders', {
          parent_id: ctx.demo.project.root_folder_id,
          folder_name: `M4-5 请求量（临时）${stamp()}`,
          remark: 'M4-5 截图脚本：连续上传时导出页的请求量，跑完删除',
        })
        const upload = async (n, t0) => {
          await uploadPage(ctx.api, folder.id, `VOL_${String(n).padStart(3, '0')}.png`, `m45-volume ${t0}`, n)
        }
        try {
          // 1. 前台：每秒上传 1 页，连续 20 页。
          const t0 = Date.now()
          for (let i = 1; i <= 20; i++) {
            await upload(i, t0)
            await sleep(Math.max(0, t0 + i * 1000 - Date.now()))
          }
          await sleep(2000)
          const a = { ...counts }
          const secs = ((Date.now() - t0) / 1000).toFixed(0)
          console.log(`  前台连续上传 20 页（${secs} 秒）：/changes ${a.changes} 次（可视区里 ${visibleDone} 个已完成的任务），预检 ${a.precheck} 次`)
          assert(a.changes <= visibleDone + 2, `20 秒里列表行最多重取一轮：/changes ${a.changes} 次，可视区 ${visibleDone} 个`)
          assert(a.precheck <= 2, `事件触发的预检至少隔 20 秒：${a.precheck} 次`)
          const flicker = await page.evaluate(() => window.__flicker)
          assert(flicker === 0, `静默重跑预检期间按钮不该禁用、结果区不该变成加载中：${flicker} 次`)
          assert(await page.$('[data-testid=task-changes-stale]'), '上传期间列表行应标"可能已过时"')

          // 2. 切到后台：暂停所有重取。
          await setHidden(page, true)
          const b0 = { ...counts }
          const t1 = Date.now()
          for (let i = 21; i <= 26; i++) {
            await upload(i, t0)
            await sleep(Math.max(0, t1 + (i - 20) * 1000 - Date.now()))
          }
          await sleep(1500)
          assert(counts.changes === b0.changes && counts.precheck === b0.precheck, `后台时不该重取：/changes ${counts.changes - b0.changes} 次，预检 ${counts.precheck - b0.precheck} 次`)

          // 3. 回到前台：补取一次。
          await setHidden(page, false)
          await sleep(3000)
          const dc = counts.changes - b0.changes
          const dp = counts.precheck - b0.precheck
          console.log(`  后台上传 6 页：/changes 0 次、预检 0 次；回到前台补取：/changes ${dc} 次，预检 ${dp} 次`)
          assert(dp === 1, `回到前台应补跑一次预检：${dp} 次`)
          assert(dc >= 1 && dc <= visibleDone + 1, `回到前台应补取一轮交付后变更：${dc} 次`)
          assert(!(await page.$('[data-testid=task-changes-stale]')), '补取之后不该还有"可能已过时"')
        } finally {
          page.off('request', onRequest)
          await page.evaluate(() => clearInterval(window.__flickerTimer)).catch(() => {})
          await setHidden(page, null).catch(() => {})
          await ctx.api('DELETE', `/api/folders/${folder.id}`, undefined, { allow: [404, 409] }).catch((e) => console.warn(`  删临时文件夹出错：${e.message}`))
        }
      },
    },

    // ---------------------------------------------------------------- 取失败、重试、列表竞态（审查第 1 轮）
    {
      name: 'm4-export-robustness',
      user: MANAGER,
      path: '/export',
      async prepare(page) {
        const ctx = await context(page)
        await waitExportPage(page)
        const it2 = ctx.item(2)
        let failChanges = null
        let failTask = null
        let holdList = false
        let held = null
        const onRequest = async (req) => {
          const u = new URL(req.url())
          if (failChanges && u.pathname === `/api/export-tasks/${failChanges}/changes`) {
            return void req.respond({ status: 500, contentType: 'application/json', body: JSON.stringify({ code: 'internal', message: 'M4-5 截图：模拟交付后变更取失败' }) })
          }
          if (failTask && req.method() === 'GET' && u.pathname === `/api/export-tasks/${failTask}`) {
            return void req.respond({ status: 502, contentType: 'text/plain', body: 'Bad Gateway' })
          }
          if (holdList && req.method() === 'GET' && u.pathname === `/api/projects/${ctx.pid}/export-tasks`) {
            holdList = false
            // server 现在就处理（建任务之前），响应在路上耽搁。
            held = { req, body: await ctx.mgr('GET', `${u.pathname}${u.search}`) }
            return
          }
          req.continue().catch(() => {})
        }
        await page.setRequestInterception(true)
        page.on('request', onRequest)
        try {
          // 1. 交付后变更取失败：保留旧值、行上标出来、不弹提示；对话框里标出错；5 秒后自动重试。
          const id = await page.$eval('[data-testid=export-task][data-status=done][data-changes-state=ready]', (e) => e.dataset.taskId)
          const before = await rowInfo(page, id)
          failChanges = id
          await page.click(`${rowSel(id)} [data-testid=task-changes]`)
          await page.waitForSelector(`[data-testid=task-changes-dialog][data-task-id="${id}"]`, { timeout: 5000 })
          await page.click('[data-testid=task-changes-refresh]')
          await page.waitForSelector(`[data-testid=task-changes-dialog][data-state=error] [data-testid=task-changes-failed]`, { timeout: 5000 })
          await sleep(300)
          await shot(page, 'm4-export-changes-error')
          await closeDialog(page)
          const row = await rowInfo(page, id)
          assert(row.changesState === 'error' && row.changes === before.changes, `取失败后行上保留旧值并标出错：${JSON.stringify(row)}`)
          assert(await page.$(`${rowSel(id)} [data-testid=task-changes-error]`), '行上应写"可能已过时，点开重试"')
          const toasts = await page.$$eval('[data-sonner-toast]', (els) => els.filter((e) => e.textContent?.includes('模拟交付后变更取失败')).length)
          assert(toasts === 0, `取交付后变更失败不该弹提示：${toasts} 个`)
          failChanges = null
          const t0 = Date.now()
          await waitRow(page, id, { 'changes-state': 'ready' }, 9000)
          console.log(`  交付后变更取失败：行上保留旧值并标出来、没有提示；恢复后 ${Date.now() - t0} ms 自动重试成功`)

          // 2. 单个任务重拉失败（网关抖动）：退避后重试，行不会停在旧状态。
          const body = { scope_type: 'item', scope_ids: [it2.id], export_format: 'pdf', only_approved: true, only_ready: true }
          const t = await ctx.mgr('POST', `/api/projects/${ctx.pid}/export-tasks`, body)
          ctx.state.created.push(t.id)
          await page.waitForSelector(rowSel(t.id), { timeout: 5000 })
          failTask = t.id
          await apiWaitTask(ctx, t.id, ['done'])
          await sleep(1500)
          const stuck = await rowInfo(page, t.id)
          failTask = null
          const t1 = Date.now()
          await waitRow(page, t.id, { status: 'done' }, 15000)
          console.log(`  单个任务重拉失败期间行停在"${stuck.status}"；恢复后 ${Date.now() - t1} ms 重试到"完成"`)

          // 3. 整页重拉的响应比单个取回来的旧：列表请求发出之后建的任务不会被丢掉。
          holdList = true
          await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })))
          await sleep(200)
          await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })))
          for (let i = 0; i < 50 && !held; i++) await sleep(100)
          assert(held, '重连后应整页重拉列表')
          await page.waitForFunction(() => document.querySelector('[data-testid=export-page]')?.dataset.stream === 'open', { timeout: 10000 })
          const t2 = await ctx.mgr('POST', `/api/projects/${ctx.pid}/export-tasks`, body)
          ctx.state.created.push(t2.id)
          await page.waitForSelector(rowSel(t2.id), { timeout: 10000 })
          held.done = true
          await held.req.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(held.body) })
          await sleep(1000)
          assert(await page.$(rowSel(t2.id)), '列表请求发出之后建的任务，旧的列表响应回来后应还在')
          await waitRow(page, t2.id, { status: 'done' }, 30000)
          console.log('  整页重拉的旧响应回来后，之后建的任务还在列表里')
        } finally {
          failChanges = null
          failTask = null
          if (held && !held.done) await held.req.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(held.body) }).catch(() => {})
          page.off('request', onRequest)
          await page.setRequestInterception(false)
        }
      },
    },

    // ---------------------------------------------------------------- 各种状态的样子（模拟的列表响应）
    {
      name: 'm4-export-states',
      user: MANAGER,
      path: '/export',
      async prepare(page) {
        // 等待重试、失败（几十行的问题页清单）、已过期、正在清理在本机很难真实造出来（要坏掉的原图、等 7 天、卡住的
        // 清理），这一张把列表接口的响应改写成这几种状态，只看界面怎么显示；任务 id 是真的，明细、交付后变更照常取。
        const ctx = await context(page)
        const pathRe = new RegExp(`^/api/projects/${ctx.pid}/export-tasks$`)
        const onRequest = async (req) => {
          const u = new URL(req.url())
          if (req.method() !== 'GET' || !pathRe.test(u.pathname)) return void req.continue().catch(() => {})
          try {
            const real = await ctx.mgr('GET', `${u.pathname}${u.search}`)
            // 过期的那个用一个真的已完成的任务（交付后变更照常能取）。
            const di = real.items.findIndex((t, i) => i >= 3 && t.status === 'done')
            if (di > 3) real.items.splice(3, 0, ...real.items.splice(di, 1))
            const [a, b, c, d] = real.items
            const lines = Array.from({ length: 36 }, (_, i) => `${i + 1}. 档号 Z405-WS·2025-Y-BGS-000${(i % 3) + 1} 第 ${i + 2} 页（file_id 01a13fb5-c548-7695-81e0-${String(i).padStart(12, '0')}）原图和快照对不上：sha256 不一致`)
            Object.assign(a, { status: 'failed', attempts: 3, progress_done: 4, error_message: `已经执行了 3 次，都没有成功。最后一次：有 36 页原图有问题，重试没用。请重扫这些页，或者把它们移出条目后重新导出：\n${lines.join('\n')}`, output_size: null, expires_at: null, expired: false })
            Object.assign(b, { status: 'queued', attempts: 1, progress_done: 0, error_message: '第 1 次执行失败，30 秒后自动重试：读取原图失败：连接存储超时（127.0.0.1:8333）', output_size: null, expires_at: null, expired: false })
            Object.assign(c, { status: 'canceled', cleaning_up: true, output_size: null, expires_at: null, expired: false })
            Object.assign(d, { status: 'done', expired: true, expires_at: '2026-10-08T13:00:00Z', progress_done: d.progress_total })
            await req.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(real) })
          } catch (e) {
            await req.continue().catch(() => {})
            console.warn(`  改写列表响应失败：${e.message}`)
          }
        }
        await page.setRequestInterception(true)
        page.on('request', onRequest)
        try {
          await page.goto(`${ctx.origin}/export`, { waitUntil: IDLE })
          await waitExportPage(page)
          const rows = await page.$$eval('[data-testid=export-task]', (els) => els.slice(0, 4).map((e) => e.dataset.taskId))
          const [a, b, c, d] = rows
          await page.waitForSelector(`${rowSel(a)}[data-status=failed] [data-testid=export-task-error]`, { timeout: 5000 })
          assert(await page.$(`${rowSel(b)}[data-retrying=true] [data-testid=export-task-error]`), '排队中、失败过的应显示"等待重试"和原因')
          assert(await page.$(`${rowSel(c)}[data-cleaning-up=true] [data-testid=export-task-cleaning]`), '取消后清理中应显示"正在清理"')
          assert(await page.$(`${rowSel(d)}[data-expired=true] [data-testid=export-task-expired]`), '过期的应显示"已过期"')
          assert(!(await page.$(`${rowSel(d)} [data-testid=task-download]`)), '过期的不能下载')
          assert(await page.$(`${rowSel(d)} [data-testid=task-reexport]`), '过期的可以按原来的范围重新导出')
          assert(!(await page.$(`${rowSel(c)} [data-testid=task-cancel]`)), '清理中的不能再取消')
          assert(await page.$(`${rowSel(b)} [data-testid=task-cancel]`), '等待重试的可以取消')
          await page.click(`${rowSel(a)} [data-testid=export-task-error-toggle]`)
          await page.waitForSelector(`${rowSel(a)} [data-testid=export-task-error-full]`, { timeout: 3000 })
          const full = await page.$eval(`${rowSel(a)} [data-testid=export-task-error-full]`, (e) => e.textContent.split('\n').length)
          assert(full >= 37, `展开后应显示全部 ${37} 行：${full}`)
          // 过期的"按原来的范围重新导出"：右栏带着原来的范围和格式。
          await page.click(`${rowSel(d)} [data-testid=task-reexport]`)
          await page.waitForSelector('[data-testid=export-new] [data-testid=scope-entry]', { timeout: 5000 })
          await waitPrecheckFresh(page)
        } finally {
          page.off('request', onRequest)
          await page.setRequestInterception(false)
        }
      },
    },

    // ---------------------------------------------------------------- 没有 export 菜单的人
    {
      name: 'm4-export-no-entry',
      user: CATALOGER,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        acceptDialogs(page)
        const it1 = ctx.item(1)
        await page.goto(`${ctx.origin}/catalog?category=${ctx.c.bgs.id}&item=${it1.id}`, { waitUntil: IDLE })
        await page.waitForSelector(`[data-testid=item-detail][data-item-id="${it1.id}"][data-load=ready]`, { timeout: 10000 })
        assert(!(await page.$('[data-testid=item-export]')), '归类员的条目详情不应有"导出这件"')
        assert(!(await page.$('[data-testid=category-export]')), '归类员的节点工具栏不应有"导出"')
        assert(!(await page.$('a[href="/export"]')), '归类员的导航里不应有导出')
        const node = `[data-testid=category-tree-area] [data-testid=tree-category][data-category-id="${ctx.c.bgs.id}"]`
        await page.click(node, { button: 'right' })
        await page.waitForSelector('[data-testid=menu-category-renumber]', { visible: true, timeout: 3000 })
        assert(!(await page.$('[data-testid=menu-category-export]')), '归类员的右键菜单不应有"导出…"')
        await sleep(200)
        await shot(page, 'm4-export-no-entry-menu')
        await page.keyboard.press('Escape')
        // 直接打开 /export：挡到无权提示页。
        await page.goto(`${ctx.origin}/export`, { waitUntil: IDLE })
        await page.waitForFunction(() => location.pathname === '/forbidden', { timeout: 5000 })
        // 接口：403。
        const id = ctx.state.pdf ?? ctx.state.images
        const calls = [
          ['POST', `/api/projects/${ctx.pid}/export-tasks:precheck`, { scope_type: 'category', scope_ids: [ctx.c.bgs.id], export_format: 'pdf' }],
          ['GET', `/api/projects/${ctx.pid}/export-tasks`],
          ...(id ? [['GET', `/api/export-tasks/${id}`], ['GET', `/api/export-tasks/${id}/download`], ['GET', `/api/export-tasks/${id}/changes`]] : []),
        ]
        for (const [method, p, b] of calls) {
          const r = await ctx.cat(method, p, b, { allow: [403] })
          assert(r?.status === 403, `归类员 ${method} ${p} 应是 403：${JSON.stringify(r)?.slice(0, 120)}`)
        }
        await page.goto(`${ctx.origin}/catalog?category=${ctx.c.bgs.id}&item=${it1.id}`, { waitUntil: IDLE })
        await page.waitForSelector(`[data-testid=item-detail][data-item-id="${it1.id}"][data-load=ready]`, { timeout: 10000 })
      },
      async after() {
        // 收尾：脚本建的任务里还没结束的取消掉（正常不会有）。
        const ctx = await ctxPromise
        if (!ctx) return
        for (const id of ctx.state.created) {
          const t = await ctx.mgr('GET', `/api/export-tasks/${id}`)
          if (t.status === 'queued' || t.status === 'running') await ctx.mgr('POST', `/api/export-tasks/${id}/cancel`, undefined, { allow: [409] })
        }
        if (ctx.state.downloadDir && existsSync(ctx.state.downloadDir)) rmSync(ctx.state.downloadDir, { recursive: true, force: true })
      },
    },
  ],
}
