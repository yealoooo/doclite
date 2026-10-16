// M2-5 的截图清单和断言：审核分配页 /review-assign（demo_manager 操作）。
//
//   INIT_ADMIN_PASSWORD=admin123456 \
//     npm run screenshots -- --spec scripts/shots/m2-assign.mjs --base http://localhost:<preview 端口>
//
// 前端地址是 vite preview（/api 代理到 server）或托管了 web/dist 的 server。要先有：
// - DEMO-A 下的演示文件夹「M2-5 分配演示」（40 页、两层子文件夹，seed:files 建的，审核状态保持待分配）：
//     npm run seed:files -- --project DEMO-A --folder "M2-5 分配演示" --count 12 --remark "M2-5 审核分配演示"
//     npm run seed:files -- --project DEMO-A --folder "M2-5 分配演示/第一组" --count 10 --low-dpi 1
//     npm run seed:files -- --project DEMO-A --folder "M2-5 分配演示/第一组/补扫" --count 6
//     npm run seed:files -- --project DEMO-A --folder "M2-5 分配演示/第二组" --count 12 --no-cover 1
//   （都加 --base 和 --user，同 seed:files 的说明）。每张截图在它下面建一个
//   「截图验证 M2-5 … <时间>」临时文件夹、用接口上传几页，做完在 after 里删掉（软删除；前面失败了也删），
//   所以可以反复跑，不改 M1 的演示文件夹和「M2-5 分配演示」里原有文件的审核状态；
// - demo_manager、demo_reviewer、demo_reviewer2 能进 DEMO-A（密码 demo123456，可用 DEMO_PASSWORD 覆盖）。
//   改派、重扫那几张会临时停用 demo_reviewer2 造"不能再审的人"，after 里一定重新启用。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - m2-assign-folder-done：选子文件夹 A（含子文件夹 A1）→ 选 demo_reviewer → 分配 → 确认框写明
//   "含子文件夹，共 10 个待分配"（另存 m2-assign-folder.png）→ 确认。另一个窗口（同一文件夹）3 秒内树上的
//   待分配数、状态计数、页头计数都更新（另存 m2-assign-folder-other.png）；本窗口的树、页头、筛选计数、审核人的
//   待办数都对；接口核对 A、A1 每个文件 file_reviews 只有一行 open（审核人、分配人对），最新一条日志是
//   assign_review（detail 的 reviewer、reviewer_id、folder_path、version_no、review_round）；其他文件没动。
// - m2-assign-selected：8 个文件里 2 个先用接口分给 demo_reviewer；全选 8 个分给 demo_reviewer2 → 提示
//   "已分配 6 个……跳过了 2 个"；接口核对两组文件的 open 行和日志。
// - m2-reassign：子文件夹 C 的 5 个分给 demo_reviewer2 后停用他 → 右栏提醒、"改派…"对话框里他的标签和 5 行
//   高亮并写"已停用"（另存 m2-reassign.png）→ 单个改派一行、按子树改派他剩下的 4 个、重新启用后在网格里
//   批量改派 3 个；每一步接口核对 file_reviews（旧行 cancelled、新行 open）和 reassign_review 日志
//   （另存最终状态 m2-reassign-done.png）。M2-8 验收补的：之后再全选这 3 个、右栏仍选 demo_reviewer2，
//   "改派给他"是"（0 个待审）"、禁用、写"选中的都已经是…在审"，换成 demo_reviewer 时是 3 个；只选 1 个时也一样。
// - m2-file-log：一个文件依次分配 → 单个改派 → 通过（带说明）→ 在页面上撤回通过；一个驳回（模糊、其他 + 说明）
//   → 重扫（自动分回原审核人）；一个驳回后停用审核人再重扫（skipped_reviewer，改为待分配）。页面上的日志
//   说明（logSummary）逐条核对；接口核对撤回后的 file_reviews 和日志顺序（另存 m2-file-log-rescan.png）。
//   另一个通过后停用审核人、在页面上撤回通过：改为待分配，日志带 skipped_reviewer。
// - m2-assign-big-event：子文件夹放 510 页，页面之外按文件夹分配：同时在页面之外订阅 SSE，核对事件只带
//   folder_ids、没有 file_ids；页面 3 秒内更新树、筛选、页头计数和选中文件的分配记录、日志。上传 510 页约 20 秒。
// - m2-assign-guards（审查第 1 轮的回归）：按文件夹分配的确认框开着时当前文件夹被删、按后退，确认框都自动关闭、
//   上一级不被分配；改派对话框开着时当前文件夹被删，对话框关闭、上一级不被改派；文件夹移动到别处之后在页面之外
//   分配，新位置的计数 3 秒内更新；通过 → 撤回 → 驳回之后右栏写"驳回：某人"；拦截 /api/me/menus 模拟只有
//   review_assign 的人：显示提示、页头计数来自 review-summary 并随事件更新（另存 m2-assign-no-browse.png）；
//   拦截 stream 请求（实时连接断开）时按文件夹分配，下面已展开的文件夹的计数也刷新。

import { mkdirSync } from 'node:fs'

import { makeScanPage } from '../lib/scan-pages.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'
const PROJECT = 'DEMO-A'
const DEMO_FOLDER = 'M2-5 分配演示'
const MANAGER = 'demo_manager'
const REV1 = 'demo_reviewer'
const REV2 = 'demo_reviewer2'
const IDLE = 'networkidle2'
// 另存的截图和清单的截图放在同一个目录：--out，默认 docs/screenshots/m2。
const OUT = (() => {
  const i = process.argv.indexOf('--out')
  const dir = i > 0 ? process.argv[i + 1] : '../docs/screenshots/m2'
  mkdirSync(dir, { recursive: true })
  return dir
})()

// ---------- 页面之外调接口 ----------

/** 一个登录会话（各自的 Cookie）。 */
async function login(origin, username, password) {
  let cookie = ''
  const call = async (method, path, body, { allow = [] } = {}) => {
    const res = await fetch(origin + path, {
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

/**
 * 在页面之外订阅项目的实时事件（SSE，经前端地址的代理），收集 `files.changed` 等事件，用来核对事件的内容
 * （例如超过 500 个文件时没有 file_ids）。`stop()` 断开。
 */
function listenEvents(origin, call, projectId) {
  const events = []
  const ctrl = new AbortController()
  let resolveOpen
  const opened = new Promise((r) => (resolveOpen = r))
  const done = (async () => {
    const res = await fetch(`${origin}/api/projects/${projectId}/stream`, {
      headers: { Accept: 'text/event-stream', Cookie: call.cookie() },
      signal: ctrl.signal,
    })
    if (!res.ok) throw new Error(`订阅事件失败：${res.status}`)
    resolveOpen()
    const decoder = new TextDecoder()
    let buf = ''
    for await (const chunk of res.body) {
      buf += decoder.decode(chunk, { stream: true })
      let i
      while ((i = buf.indexOf('\n\n')) >= 0) {
        const block = buf.slice(0, i)
        buf = buf.slice(i + 2)
        const data = block
          .split('\n')
          .filter((l) => l.startsWith('data:'))
          .map((l) => l.slice(5).trim())
          .join('\n')
        if (data) events.push(JSON.parse(data))
      }
    }
  })().catch((e) => {
    if (e.name !== 'AbortError') throw e
  })
  return {
    events,
    opened,
    stop: async () => {
      ctrl.abort()
      await done.catch(() => {})
    },
  }
}

let ctxPromise = null
/** 登录 admin 和 demo_manager，取 DEMO-A、演示文件夹和几个演示用户（只做一次）。 */
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const adminApi = await login(origin, admin, adminPassword)
    const projects = await adminApi('GET', '/api/projects')
    const project = projects.find((p) => p.project_code === PROJECT)
    if (!project) throw new Error(`找不到项目 ${PROJECT}，先跑 seed:demo`)
    const root = await adminApi('GET', `/api/folders/${project.root_folder_id}/children`)
    const demoFolder = root.folders.find((f) => f.folder_name === DEMO_FOLDER)
    if (!demoFolder) throw new Error(`找不到 ${PROJECT}/${DEMO_FOLDER}，先按 roadmap M2-5 的须知用 seed:files 建好`)
    const users = {}
    for (const name of [MANAGER, REV1, REV2]) {
      const page = await adminApi('GET', `/api/system/users?keyword=${encodeURIComponent(name)}&page_size=100`)
      const u = page.items.find((x) => x.username === name)
      if (!u) throw new Error(`找不到用户 ${name}（demo_reviewer2 见 roadmap M2-5 的须知）`)
      users[name] = u
    }
    // 上一次跑到一半失败时 demo_reviewer2 可能还是停用的：先恢复。
    if (users[REV2].status !== 'active') {
      await adminApi('POST', `/api/system/users/${users[REV2].id}/enable`)
      console.log(`  ${REV2} 是停用的（上次没跑完？），已重新启用`)
    }
    const mgr = await login(origin, MANAGER, demoPassword)
    return {
      origin,
      project,
      demoFolder,
      admin: adminApi,
      mgr,
      id: (name) => users[name].id,
      displayName: (name) => users[name].display_name,
      /** 以某个演示用户登录（每次新的会话：停用会让旧会话失效）。 */
      as: (name) => login(origin, name, demoPassword),
    }
  })()
  return ctxPromise
}

function stamp() {
  return new Date().toISOString().replace(/[-:T]/g, '').slice(0, 14)
}

/** 用接口上传一页合成的 PNG（带封面），返回入库结果。 */
async function uploadPage(call, folderId, name, seed, pageNo) {
  const p = makeScanPage({ seed, pageNo })
  const form = new FormData()
  form.append('file_name', name)
  form.append('cover', new Blob([p.cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([p.png], { type: 'image/png' }), 'page.png')
  return call('POST', `/api/folders/${folderId}/files`, form)
}

/** 重扫一页（新内容）。 */
async function rescanPage(call, fileId, name, seed) {
  const p = makeScanPage({ seed, pageNo: 99 })
  const form = new FormData()
  form.append('file_name', name)
  form.append('cover', new Blob([p.cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([p.png], { type: 'image/png' }), 'page.png')
  return call('POST', `/api/files/${fileId}/rescan`, form)
}

/**
 * 在演示文件夹下建一个临时文件夹，按 layout（相对路径 → 页数，'' 是它自己）建子文件夹、上传页面。
 * 返回 {id, name, folders: {路径: id}, files: {路径: [{file_id, file_name}]}}。
 */
async function makeTemp(ctx, label, layout) {
  const name = `截图验证 M2-5 ${label} ${stamp()}`
  const top = await ctx.admin('POST', '/api/folders', {
    parent_id: ctx.demoFolder.id,
    folder_name: name,
    remark: 'M2-5 截图脚本临时建的，做完会删掉',
  })
  const folders = { '': top.id }
  const files = {}
  const seed = `M2-5 ${label} ${stamp()} ${Math.random()}`
  for (const [rel, count] of Object.entries(layout)) {
    if (rel && !folders[rel]) {
      const parts = rel.split('/')
      const parent = folders[parts.slice(0, -1).join('/')]
      const f = await ctx.admin('POST', '/api/folders', { parent_id: parent, folder_name: parts[parts.length - 1] })
      folders[rel] = f.id
    }
    files[rel] = []
    for (let i = 1; i <= count; i++) {
      const fileName = `${(rel.split('/').pop() || 'P')}_${String(i).padStart(2, '0')}.png`
      const r = await uploadPage(ctx.admin, folders[rel], fileName, `${seed}|${rel}#${i}`, i)
      files[rel].push({ file_id: r.file_id, file_name: r.file_name })
    }
  }
  return { id: top.id, name, folders, files }
}

async function dropTemp(temp) {
  if (!temp || !ctxPromise) return
  const ctx = await ctxPromise
  await ctx.admin('DELETE', `/api/folders/${temp.id}`, undefined, { allow: [404] })
  console.log(`  已删除临时文件夹「${temp.name}」`)
}

/** demo_reviewer2 一定是启用的（after 里调）。 */
async function ensureRev2Enabled() {
  if (!ctxPromise) return
  const ctx = await ctxPromise
  const page = await ctx.admin('GET', `/api/system/users?keyword=${REV2}&page_size=100`)
  const u = page.items.find((x) => x.username === REV2)
  if (u && u.status !== 'active') {
    await ctx.admin('POST', `/api/system/users/${u.id}/enable`)
    console.log(`  已重新启用 ${REV2}`)
  }
}

function assert(cond, message) {
  if (!cond) throw new Error(`断言失败：${message}`)
}

/** file_reviews 的行和文件状态（经接口）。 */
async function reviewsOf(ctx, fileId) {
  return ctx.mgr('GET', `/api/files/${fileId}/reviews`)
}

async function logsOf(ctx, fileId) {
  return (await ctx.mgr('GET', `/api/files/${fileId}/logs`)).logs
}

/** 核对一个文件：状态、只有一行 open 且审核人对；最新一条日志的动作和 detail。 */
async function expectOpen(ctx, fileId, reviewerName, { status = 'pending', openRows = 1 } = {}) {
  const r = await reviewsOf(ctx, fileId)
  const open = r.reviews.filter((x) => x.status === 'open')
  assert(r.review_status === status, `${fileId} 的状态是 ${r.review_status}，应为 ${status}`)
  assert(open.length === openRows, `${fileId} 有 ${open.length} 行 open，应为 ${openRows}`)
  if (openRows) assert(open[0].reviewer_id === ctx.id(reviewerName), `${fileId} 的 open 行审核人是 ${open[0].reviewer_name}，应为 ${ctx.displayName(reviewerName)}`)
  return r
}

// ---------- 页面操作 ----------

async function openAssign(page, folderId) {
  const { origin } = await context(page)
  await page.goto(`${origin}/review-assign?folder=${folderId}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=review-assign-page][data-list=ready][data-stream=open]', { timeout: 15000 })
  await page.waitForSelector('[data-testid=reviewer-row]', { timeout: 10000 })
  await waitCovers(page)
}

async function waitCovers(page, timeout = 15000) {
  await page.waitForFunction(
    () => [...document.querySelectorAll('[data-testid=file-cover]')].every((c) => !['loading', 'loading-image'].includes(c.dataset.coverState)),
    { timeout },
  )
}

async function pickReviewer(page, username) {
  await page.click(`[data-testid=reviewer-row][data-username="${username}"]`)
  await page.waitForSelector(`[data-testid=reviewer-row][data-username="${username}"][aria-checked=true]`, { timeout: 3000 })
}

/** 页面上的几个计数（树上某些文件夹的待分配数、页头、筛选）。 */
function readCounts(page, folderIds = []) {
  return page.evaluate((ids) => {
    const num = (el) => (el && el.dataset.count !== '' ? Number(el.dataset.count) : null)
    const tree = {}
    for (const id of ids) tree[id] = num(document.querySelector(`[data-testid=tree-folder][data-folder-id="${id}"] [data-testid=tree-unassigned]`))
    const head = {}
    for (const s of ['unassigned', 'pending', 'approved', 'rejected']) head[s] = num(document.querySelector(`[data-testid=count-${s}]`))
    const filter = {}
    for (const s of ['all', 'unassigned', 'pending', 'approved', 'rejected']) filter[s] = num(document.querySelector(`[data-testid=status-filter-${s}]`))
    const reviewers = {}
    for (const el of document.querySelectorAll('[data-testid=reviewer-row]')) reviewers[el.dataset.username] = Number(el.dataset.open)
    return { tree, head, filter, reviewers, folderUnassigned: num(document.querySelector('[data-testid=folder-unassigned]')) }
  }, folderIds)
}

/**
 * 等页面上的计数都等于期望值：`{tree: {文件夹 id: 待分配数}, head: {状态: 数}, filter: {状态: 数}, folder: 当前文件夹的待分配数}`。
 * 超时时报出页面上实际的计数。
 */
async function waitCounts(page, want, timeout = 5000) {
  try {
    await page.waitForFunction(
      (w) => {
        const num = (sel) => {
          const el = document.querySelector(sel)
          return el && el.dataset.count !== '' ? Number(el.dataset.count) : null
        }
        for (const [id, n] of Object.entries(w.tree ?? {}))
          if (num(`[data-testid=tree-folder][data-folder-id="${id}"] [data-testid=tree-unassigned]`) !== n) return false
        for (const [s, n] of Object.entries(w.head ?? {})) if (num(`[data-testid=count-${s}]`) !== n) return false
        for (const [s, n] of Object.entries(w.filter ?? {})) if (num(`[data-testid=status-filter-${s}]`) !== n) return false
        if (w.folder !== undefined && num('[data-testid=folder-unassigned]') !== w.folder) return false
        return true
      },
      { timeout, polling: 50 },
      want,
    )
  } catch (e) {
    const actual = await readCounts(page, Object.keys(want.tree ?? {}))
    throw new Error(`计数不对：期望 ${JSON.stringify(want)}，页面上是 ${JSON.stringify(actual)}（${e.message}）`)
  }
}

/** 网格里名字为 name 的文件单元格。 */
async function fileCell(page, name) {
  const handle = await page.evaluateHandle(
    (n) => [...document.querySelectorAll('[data-testid=file-cell],[data-testid=file-row]')].find((el) => [...el.querySelectorAll('[title]')].some((t) => t.getAttribute('title') === n)),
    name,
  )
  const el = handle.asElement()
  if (!el) throw new Error(`网格里找不到「${name}」`)
  return el
}

async function selectAll(page, firstName) {
  await (await fileCell(page, firstName)).click()
  await page.keyboard.down('Meta')
  await page.keyboard.press('a')
  await page.keyboard.up('Meta')
}

async function waitToast(page, text, timeout = 10000) {
  await page.waitForFunction(
    (t) => [...document.querySelectorAll('[data-sonner-toast]')].some((el) => el.textContent?.includes(t)),
    { timeout },
    text,
  )
}

/** 等提示都消失（截图时不挡住页头）。 */
async function waitNoToast(page, timeout = 10000) {
  await page.waitForFunction(() => !document.querySelector('[data-sonner-toast]'), { timeout })
}

/** 右栏滚到选中文件那一段（分配记录和日志）。 */
async function scrollPanelToFile(page) {
  await page.$eval('[data-testid=single-file]', (el) => el.scrollIntoView({ block: 'start' }))
  await new Promise((r) => setTimeout(r, 200))
}

/** 确认框（AlertDialog）出现，返回它的文字。 */
async function alertText(page) {
  await page.waitForSelector('[role=alertdialog]', { visible: true, timeout: 5000 })
  await new Promise((r) => setTimeout(r, 250))
  return page.$eval('[role=alertdialog]', (el) => el.textContent ?? '')
}

async function confirmAlert(page) {
  const buttons = await page.$$('[role=alertdialog] button')
  await buttons[buttons.length - 1].click()
  await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 10000 })
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

// ---------- 截图 ----------

let folderTemp = null
let selectedTemp = null
let reassignTemp = null
let logTemp = null
let bigTemp = null
let guardTemp = null

export default {
  out: OUT,
  accounts: { [MANAGER]: demoPassword },
  waitUntil: IDLE,
  shots: [
    {
      name: 'm2-assign-folder-done',
      user: MANAGER,
      path: '/review-assign',
      waitFor: '[data-testid=review-assign-page]',
      async prepare(page) {
        const ctx = await context(page)
        folderTemp = await makeTemp(ctx, '按文件夹', { '': 4, A: 6, 'A/A1': 4, B: 5 })
        const t = folderTemp
        const aFiles = [...t.files.A, ...t.files['A/A1']]
        // 另一个窗口（同一个登录会话里的另一个标签页）先打开同一个文件夹。
        const other = await page.browserContext().newPage()
        await other.setViewport({ width: 1440, height: 900 })
        try {
          await openAssign(other, t.folders.A)
          await page.bringToFront()
          await openAssign(page, t.folders.A)
          await waitCounts(page, { tree: { [t.folders.A]: 10, [t.id]: 19 }, filter: { unassigned: 6 }, folder: 10 })
          await waitCounts(other, { tree: { [t.folders.A]: 10, [t.id]: 19 } })
          const before = await readCounts(page)
          await pickReviewer(page, REV1)
          const label = await page.$eval('[data-testid=assign-button]', (el) => `${el.dataset.mode} ${el.textContent?.trim()}`)
          assert(label === `folder 分给${ctx.displayName(REV1)}（10 个）`, `分配按钮是「${label}」`)
          await page.click('[data-testid=assign-button]')
          const text = await alertText(page)
          assert(text.includes('含子文件夹，共 10 个待分配'), `确认框没写"含子文件夹，共 10 个待分配"：${text}`)
          await page.screenshot({ path: `${OUT}/m2-assign-folder.png` })
          console.log('  ✓ 确认框：含子文件夹，共 10 个待分配（另存 m2-assign-folder.png）')

          const t0 = Date.now()
          await confirmAlert(page)
          // 另一个窗口 3 秒内更新：树上 A 和临时文件夹的待分配数、筛选计数、页头。
          await other.waitForFunction(
            (a, top, beforeUnassigned) => {
              const num = (sel) => {
                const el = document.querySelector(sel)
                return el && el.dataset.count !== '' ? Number(el.dataset.count) : null
              }
              return (
                num(`[data-testid=tree-folder][data-folder-id="${a}"] [data-testid=tree-unassigned]`) === 0 &&
                num(`[data-testid=tree-folder][data-folder-id="${top}"] [data-testid=tree-unassigned]`) === 9 &&
                num('[data-testid=status-filter-pending]') === 6 &&
                num('[data-testid=count-unassigned]') === beforeUnassigned - 10
              )
            },
            { timeout: 3000, polling: 50 },
            t.folders.A,
            t.id,
            before.head.unassigned,
          )
          console.log(`  ✓ 另一个窗口 ${Date.now() - t0} ms 内更新（树上的待分配数、状态、页头计数）`)
          await waitCovers(other)
          await other.screenshot({ path: `${OUT}/m2-assign-folder-other.png` })

          await waitToast(page, `已分配 10 个给 ${ctx.displayName(REV1)}`)
          await waitCounts(page, {
            tree: { [t.folders.A]: 0, [t.id]: 9 },
            filter: { pending: 6, unassigned: 0 },
            head: { unassigned: before.head.unassigned - 10, pending: before.head.pending + 10 },
            folder: 0,
          })
          await page.waitForFunction(
            (n, want) => Number(document.querySelector(`[data-testid=reviewer-row][data-username="${n}"]`)?.dataset.open) === want,
            { timeout: 3000 },
            REV1,
            before.reviewers[REV1] + 10,
          )
          console.log(`  ✓ 本窗口：树 A 0、上一级 9；页头待分配 ${before.head.unassigned} → ${before.head.unassigned - 10}；${REV1} 待办 +10`)

          // 接口核对：A、A1 的 10 个文件各有一行 open（demo_reviewer，分配人 demo_manager），最新日志 assign_review。
          for (const [rel, list] of [['A', t.files.A], ['A/A1', t.files['A/A1']]]) {
            for (const f of list) {
              const r = await expectOpen(ctx, f.file_id, REV1)
              assert(r.reviews.length === 1, `${f.file_name} 有 ${r.reviews.length} 行 file_reviews`)
              assert(r.reviews[0].assigned_by === ctx.id(MANAGER), `${f.file_name} 的分配人不是 ${MANAGER}`)
              const logs = await logsOf(ctx, f.file_id)
              const d = logs[0].detail
              assert(logs[0].action === 'assign_review', `${f.file_name} 最新日志是 ${logs[0].action}`)
              assert(logs.length === 2 && logs[1].action === 'upload', `${f.file_name} 的日志条数 ${logs.length}`)
              assert(d.reviewer === ctx.displayName(REV1) && d.reviewer_id === ctx.id(REV1), `${f.file_name} 日志的审核人 ${d.reviewer}`)
              // folder_path 从项目根文件夹（名字就是项目名）起用 / 连接。
              const wantPath = `${ctx.project.project_name}/${DEMO_FOLDER}/${t.name}/${rel}`
              assert(d.folder_path === wantPath, `${f.file_name} 日志的 folder_path 是 ${d.folder_path}，应为 ${wantPath}`)
              assert(d.version_no === 1 && d.review_round === 1 && d.file_name === f.file_name, `${f.file_name} 日志的版本、轮次、文件名`)
            }
          }
          for (const f of [...t.files[''], ...t.files.B]) {
            const r = await reviewsOf(ctx, f.file_id)
            assert(r.review_status === 'unassigned' && r.reviews.length === 0, `${f.file_name} 不该被分配`)
          }
          console.log(`  ✓ 接口：${aFiles.length} 个文件各一行 open（${REV1}），日志 assign_review 的 detail 正确；另外 9 个没动`)
          await waitNoToast(page)
        } finally {
          await other.close()
        }
      },
      async after() {
        await dropTemp(folderTemp)
      },
    },
    {
      name: 'm2-assign-selected',
      user: MANAGER,
      path: '/review-assign',
      waitFor: '[data-testid=review-assign-page]',
      async prepare(page) {
        const ctx = await context(page)
        selectedTemp = await makeTemp(ctx, '按选中', { '': 8 })
        const files = selectedTemp.files['']
        const pre = files.slice(0, 2)
        const rest = files.slice(2)
        await ctx.mgr('POST', `/api/projects/${ctx.project.id}/review-assignments`, {
          reviewer_id: ctx.id(REV1),
          file_ids: pre.map((f) => f.file_id),
        })
        await openAssign(page, selectedTemp.id)
        await waitCounts(page, { filter: { pending: 2, unassigned: 6 } })
        await pickReviewer(page, REV2)
        await selectAll(page, files[0].file_name)
        await page.waitForSelector('[data-testid=assign-button][data-mode=files]', { timeout: 3000 })
        const label = await page.$eval('[data-testid=assign-button]', (el) => el.textContent?.trim())
        assert(label === `分给${ctx.displayName(REV2)}（8 个）`, `分配按钮是「${label}」`)
        await page.click('[data-testid=assign-button]')
        await waitToast(page, `已分配 6 个给 ${ctx.displayName(REV2)}，跳过了 2 个`)
        const result = await page.$eval('[data-testid=last-result]', (el) => el.textContent?.trim())
        assert(result.includes('已分配 6 个') && result.includes('跳过了 2 个'), `右栏的结果是「${result}」`)
        await waitCounts(page, { filter: { pending: 8, unassigned: 0 }, folder: 0 })
        console.log(`  ✓ 选中 8 个分配：提示"已分配 6 个，跳过了 2 个"，筛选计数待审 8`)
        for (const f of pre) {
          const r = await expectOpen(ctx, f.file_id, REV1)
          assert(r.reviews.length === 1, `${f.file_name} 被重复分配了`)
          const logs = await logsOf(ctx, f.file_id)
          assert(logs.filter((l) => l.action === 'assign_review').length === 1, `${f.file_name} 的分配日志不止一条`)
        }
        for (const f of rest) {
          await expectOpen(ctx, f.file_id, REV2)
          const logs = await logsOf(ctx, f.file_id)
          assert(logs[0].action === 'assign_review' && logs[0].detail.reviewer === ctx.displayName(REV2), `${f.file_name} 的分配日志不对`)
        }
        console.log(`  ✓ 接口：先分配的 2 个仍是 ${REV1}（一条分配日志），其余 6 个是 ${REV2}`)
        // 截图前把选择清掉一半，留着提示和右栏结果（选择保留也行；这里保留，能看到"已选 8 个"）。
        await waitCovers(page)
      },
      async after() {
        await dropTemp(selectedTemp)
      },
    },
    {
      name: 'm2-reassign-done',
      user: MANAGER,
      path: '/review-assign',
      waitFor: '[data-testid=review-assign-page]',
      async prepare(page) {
        const ctx = await context(page)
        reassignTemp = await makeTemp(ctx, '改派', { '': 3, C: 5 })
        const t = reassignTemp
        const top = t.files['']
        const cFiles = t.files.C
        await ctx.mgr('POST', `/api/projects/${ctx.project.id}/review-assignments`, { reviewer_id: ctx.id(REV2), folder_id: t.folders.C })
        await ctx.mgr('POST', `/api/projects/${ctx.project.id}/review-assignments`, { reviewer_id: ctx.id(REV1), file_ids: top.map((f) => f.file_id) })
        await ctx.admin('POST', `/api/system/users/${ctx.id(REV2)}/disable`)
        console.log(`  已停用 ${REV2}（造"不能再审的人"）`)

        await openAssign(page, t.id)
        await page.waitForSelector('[data-testid=problem-reviewers]', { timeout: 5000 })
        const warn = await page.$eval('[data-testid=problem-reviewers]', (el) => el.textContent ?? '')
        assert(warn.includes(ctx.displayName(REV2)) && warn.includes('已停用') && warn.includes('5 个'), `右栏提醒是「${warn}」`)
        const rows = await page.$$eval('[data-testid=reviewer-row]', (els) => els.map((e) => e.dataset.username))
        assert(!rows.includes(REV2), `停用的 ${REV2} 不该出现在"分给谁"里`)
        await pickReviewer(page, REV1)
        await page.click('[data-testid=open-reassign]')
        await page.waitForSelector('[data-testid=reassign-dialog][data-state=ready] [data-testid=assignment-row]', { timeout: 8000 })
        await new Promise((r) => setTimeout(r, 300))
        const dialog = await page.evaluate(() => ({
          rows: [...document.querySelectorAll('[data-testid=assignment-row]')].map((el) => ({
            file: el.dataset.fileId,
            reviewer: el.dataset.reviewerId,
            ok: el.dataset.canReview === 'true',
            problem: el.querySelector('[data-testid=assignment-problem]')?.textContent?.trim() ?? '',
          })),
          chips: [...document.querySelectorAll('[data-testid=assignment-reviewer]')].map((el) => ({
            id: el.dataset.reviewerId,
            ok: el.dataset.canReview === 'true',
            text: el.textContent?.trim(),
          })),
          problems: document.querySelector('[data-testid=assignment-problems]')?.textContent ?? '',
        }))
        assert(dialog.rows.length === 8, `对话框有 ${dialog.rows.length} 行`)
        const bad = dialog.rows.filter((r) => !r.ok)
        assert(bad.length === 5 && bad.every((r) => r.reviewer === ctx.id(REV2) && r.problem === '已停用'), `高亮的行不对：${JSON.stringify(bad)}`)
        const chip = dialog.chips.find((c) => c.id === ctx.id(REV2))
        assert(chip && !chip.ok && chip.text.includes('已停用'), `${REV2} 的标签：${JSON.stringify(chip)}`)
        assert(dialog.problems.includes('已停用'), '对话框没有"已停用"的提醒')
        await page.screenshot({ path: `${OUT}/m2-reassign.png` })
        console.log(`  ✓ 改派对话框：${REV2} 标"已停用"，他的 5 行高亮（另存 m2-reassign.png）`)

        // 1) 单个改派：第一行（C 的第一个文件）→ demo_reviewer。
        const first = cFiles[0]
        const firstBefore = await reviewsOf(ctx, first.file_id)
        await page.click(`[data-testid=assignment-row][data-file-id="${first.file_id}"] [data-testid=assignment-reassign]`)
        await waitToast(page, `已把「${first.file_name}」改派给 ${ctx.displayName(REV1)}`)
        const r1 = await expectOpen(ctx, first.file_id, REV1)
        assert(r1.reviews.length === 2 && r1.reviews[0].status === 'cancelled' && r1.reviews[0].assignment_id === firstBefore.reviews[0].assignment_id, '单个改派后旧行没变成 cancelled')
        const l1 = (await logsOf(ctx, first.file_id))[0]
        assert(
          l1.action === 'reassign_review' && l1.detail.from === ctx.displayName(REV2) && l1.detail.from_id === ctx.id(REV2) && l1.detail.to === ctx.displayName(REV1) && l1.detail.to_id === ctx.id(REV1),
          `单个改派的日志：${JSON.stringify(l1)}`,
        )
        await page.waitForFunction(() => document.querySelectorAll('[data-testid=assignment-row][data-can-review=false]').length === 4, { timeout: 5000 })
        console.log('  ✓ 单个改派：旧行 cancelled、新行 open，日志 reassign_review（from、to 正确）')

        // 2) 按子树改派：筛选 demo_reviewer2 → 把他在这里的全部 4 个改派给 demo_reviewer。
        await page.click(`[data-testid=assignment-reviewer][data-reviewer-id="${ctx.id(REV2)}"]`)
        await page.waitForFunction(() => document.querySelectorAll('[data-testid=assignment-row]').length === 4, { timeout: 5000 })
        await page.click('[data-testid=reassign-all-of]')
        const text = await alertText(page)
        assert(text.includes('全部 4 个'), `按子树改派的确认框：${text}`)
        await confirmAlert(page)
        await waitToast(page, `已把 ${ctx.displayName(REV2)} 的 4 个待审改派给 ${ctx.displayName(REV1)}`)
        for (const f of cFiles) await expectOpen(ctx, f.file_id, REV1)
        for (const f of cFiles.slice(1)) {
          const l = (await logsOf(ctx, f.file_id))[0]
          assert(l.action === 'reassign_review' && l.detail.from_id === ctx.id(REV2) && l.detail.to_id === ctx.id(REV1), `${f.file_name} 按子树改派的日志不对`)
        }
        await page.waitForFunction(() => !document.querySelector('[data-testid=assignment-row][data-can-review=false]'), { timeout: 5000 })
        console.log('  ✓ 按子树改派：剩下 4 个都到了 demo_reviewer，日志 reassign_review；对话框里不再有高亮行')
        await page.keyboard.press('Escape')
        await page.waitForSelector('[data-testid=reassign-dialog]', { hidden: true, timeout: 5000 })

        // 3) 重新启用 demo_reviewer2，在网格里全选这一层的 3 个（demo_reviewer 的待审）批量改派给他。
        await ctx.admin('POST', `/api/system/users/${ctx.id(REV2)}/enable`)
        await openAssign(page, t.id)
        await page.waitForSelector('[data-testid=problem-reviewers]', { hidden: true, timeout: 5000 })
        await pickReviewer(page, REV2)
        await selectAll(page, top[0].file_name)
        await page.waitForSelector('[data-testid=bulk-reassign-button]', { timeout: 3000 })
        const bl = await page.$eval('[data-testid=bulk-reassign-button]', (el) => el.textContent?.trim())
        assert(bl === `改派给${ctx.displayName(REV2)}（3 个待审）`, `批量改派按钮是「${bl}」`)
        await page.click('[data-testid=bulk-reassign-button]')
        await waitToast(page, `已改派 3 个给 ${ctx.displayName(REV2)}`)
        for (const f of top) {
          const r = await expectOpen(ctx, f.file_id, REV2)
          assert(r.reviews.length === 2 && r.reviews[0].status === 'cancelled', `${f.file_name} 批量改派后的 file_reviews 不对`)
          const l = (await logsOf(ctx, f.file_id))[0]
          assert(l.action === 'reassign_review' && l.detail.from_id === ctx.id(REV1) && l.detail.to_id === ctx.id(REV2), `${f.file_name} 批量改派的日志不对`)
        }
        console.log(`  ✓ 网格里批量改派 3 个给 ${REV2}：旧行 cancelled、新行 open，日志 reassign_review`)
        // M2-8 验收：选中的都已经是 demo_reviewer2 在审、右栏又选了他时，"改派给他"的个数是 0、按钮禁用、写明原因；
        // 换成 demo_reviewer 时 3 个都能改派。单个文件也一样。
        const bulkState = () =>
          page.evaluate(() => {
            const b = document.querySelector('[data-testid=bulk-reassign-button]')
            return {
              count: b?.dataset.count ?? null,
              disabled: b ? b.disabled : null,
              text: b?.textContent?.trim() ?? null,
              note: document.querySelector('[data-testid=bulk-reassign-note]')?.textContent?.trim() ?? null,
            }
          })
        const waitBulk = async (count, disabled) => {
          try {
            await page.waitForFunction(
              (c, d) => {
                const b = document.querySelector('[data-testid=bulk-reassign-button]')
                return b && b.dataset.count === String(c) && b.disabled === d
              },
              { timeout: 5000 },
              count,
              disabled,
            )
          } catch {
            throw new Error(`批量改派按钮应是 ${count} 个、${disabled ? '禁用' : '可点'}：${JSON.stringify(await bulkState())}`)
          }
          return bulkState()
        }
        await selectAll(page, top[0].file_name)
        let bs = await waitBulk(0, true)
        assert(bs.text === `改派给${ctx.displayName(REV2)}（0 个待审）` && bs.note === `选中的都已经是${ctx.displayName(REV2)}在审`, `都已经是他在审时：${JSON.stringify(bs)}`)
        await pickReviewer(page, REV1)
        bs = await waitBulk(3, false)
        assert(bs.text === `改派给${ctx.displayName(REV1)}（3 个待审）` && bs.note === null, `换成 ${REV1} 时：${JSON.stringify(bs)}`)
        await pickReviewer(page, REV2)
        await waitBulk(0, true)
        console.log(`  ✓ 选中的 3 个都已经是 ${REV2} 在审：按钮"（0 个待审）"禁用、写"选中的都已经是…在审"；换成 ${REV1} 时 3 个可改派`)
        await (await fileCell(page, top[0].file_name)).click()
        await page.waitForSelector(`[data-testid=single-file][data-file-id="${top[0].file_id}"] [data-testid=review-row][data-status=cancelled]`, { timeout: 5000 })
        bs = await waitBulk(0, true)
        assert(bs.note === `选中的都已经是${ctx.displayName(REV2)}在审`, `单个文件时：${JSON.stringify(bs)}`)
        assert(await page.$eval('[data-testid=single-reassign-button]', (el) => el.disabled), '单个文件时右栏的"改派给他"也应禁用')
        console.log(`  ✓ 只选 1 个（已经是 ${REV2} 在审）时也一样，右栏单个改派按钮也禁用`)
        await panelLogs(page, top[0].file_id, 3, 'reassign_review')
        await waitCovers(page)
        await waitNoToast(page)
      },
      async after() {
        await ensureRev2Enabled()
        await dropTemp(reassignTemp)
      },
    },
    {
      name: 'm2-file-log',
      user: MANAGER,
      path: '/review-assign',
      waitFor: '[data-testid=review-assign-page]',
      async prepare(page) {
        const ctx = await context(page)
        logTemp = await makeTemp(ctx, '日志', { '': 4 })
        const [x, y, z, w] = logTemp.files['']
        const pid = ctx.project.id
        const openId = async (fid) => (await reviewsOf(ctx, fid)).reviews.find((r) => r.status === 'open').assignment_id
        await ctx.mgr('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: ctx.id(REV1), file_ids: logTemp.files[''].map((f) => f.file_id) })
        // X：单个改派给 demo_reviewer2，他通过（带说明）；最后在页面上撤回通过（重新分给他）。
        await ctx.mgr('POST', `/api/review-assignments/${await openId(x.file_id)}/reassign`, { reviewer_id: ctx.id(REV2) })
        const rev2 = await ctx.as(REV2)
        await rev2('POST', `/api/files/${x.file_id}/review`, { result: 'approved', comment: '页面清楚，通过' })
        // Y：demo_reviewer 驳回（模糊、其他 + 说明）→ 重扫，自动分回 demo_reviewer（第 2 轮）。
        const rev1 = await ctx.as(REV1)
        await rev1('POST', `/api/files/${y.file_id}/review`, { result: 'rejected', reject_reasons: ['blur', 'other'], comment: '右下角模糊，重扫一张' })
        const seed = `M2-5 rescan ${stamp()} ${Math.random()}`
        const ry = await rescanPage(ctx.admin, y.file_id, y.file_name, `${seed}#y`)
        assert(ry.review_status === 'pending' && ry.reviewer_id === ctx.id(REV1) && ry.review_round === 2, `Y 重扫后：${JSON.stringify(ry)}`)
        // Z：改派给 demo_reviewer2，他驳回（倾斜）；W：改派给 demo_reviewer2，他通过。
        await ctx.mgr('POST', `/api/review-assignments/${await openId(z.file_id)}/reassign`, { reviewer_id: ctx.id(REV2) })
        await rev2('POST', `/api/files/${z.file_id}/review`, { result: 'rejected', reject_reasons: ['skew'] })
        await ctx.mgr('POST', `/api/review-assignments/${await openId(w.file_id)}/reassign`, { reviewer_id: ctx.id(REV2) })
        await rev2('POST', `/api/files/${w.file_id}/review`, { result: 'approved' })
        // 停用 demo_reviewer2 → 重扫 Z：没有分配（skipped_reviewer），改为待分配；W 稍后在页面上撤回通过，也改为待分配。
        await ctx.admin('POST', `/api/system/users/${ctx.id(REV2)}/disable`)
        console.log(`  已停用 ${REV2}（重扫、撤回通过时"原审核人不能再审"）`)
        const rz = await rescanPage(ctx.admin, z.file_id, z.file_name, `${seed}#z`)
        assert(rz.review_status === 'unassigned' && rz.reviewer_id === null, `Z 重扫后：${JSON.stringify(rz)}`)

        await openAssign(page, logTemp.id)
        await waitCounts(page, { filter: { approved: 2, pending: 1, unassigned: 1 } })

        // Y 的日志：重扫自动分回 demo_reviewer；驳回写原因的中文和说明。
        await (await fileCell(page, y.file_name)).click()
        const ly = await panelLogs(page, y.file_id, 4, 'rescan')
        assert(ly[0].summary === `新版本 v2，第 2 轮审核，自动分给 ${ctx.displayName(REV1)}`, `Y 的重扫说明：${ly[0].summary}`)
        assert(ly[1].action === 'reject' && ly[1].summary === '原因：模糊、其他 · 说明：右下角模糊，重扫一张 · v1 · 第 1 轮', `Y 的驳回说明：${ly[1].summary}`)
        assert(ly[2].action === 'assign_review' && ly[2].summary === `分配给 ${ctx.displayName(REV1)} · v1 · 第 1 轮`, `Y 的分配说明：${ly[2].summary}`)
        // W：页面上撤回通过；通过它的 demo_reviewer2 已停用 → 改为待分配（reopen 带 skipped_reviewer）。
        await (await fileCell(page, w.file_name)).click()
        await panelLogs(page, w.file_id, 4, 'approve')
        await page.waitForSelector('[data-testid=reopen-button]', { timeout: 5000 })
        await page.click('[data-testid=reopen-button]')
        await alertText(page)
        await confirmAlert(page)
        await waitToast(page, '文件改为待分配')
        const lw = await panelLogs(page, w.file_id, 5, 'reopen')
        assert(lw[0].summary === `${ctx.displayName(REV2)} 已不能再审这个项目，改为待分配 · v1 · 第 1 轮`, `W 的撤回说明：${lw[0].summary}`)
        const dw = (await logsOf(ctx, w.file_id))[0].detail
        assert(dw.skipped_reviewer === ctx.displayName(REV2) && dw.reviewer === null && dw.reviewer_id === null, `W 的 reopen detail：${JSON.stringify(dw)}`)
        await expectOpen(ctx, w.file_id, REV2, { status: 'unassigned', openRows: 0 })
        await page.waitForSelector('[data-testid=single-file-status][data-status=unassigned]', { timeout: 5000 })
        console.log('  ✓ 撤回通过时原审核人已停用：改为待分配，日志带 skipped_reviewer，说明"已不能再审这个项目，改为待分配"')
        // Z 的日志：重扫时原审核人已停用，改为待分配。
        await (await fileCell(page, z.file_name)).click()
        const lz = await panelLogs(page, z.file_id, 5, 'rescan')
        assert(lz[0].summary === `新版本 v2，第 2 轮审核，${ctx.displayName(REV2)} 已不能再审这个项目，改为待分配`, `Z 的重扫说明：${lz[0].summary}`)
        assert(lz[1].summary === '原因：倾斜 · v1 · 第 1 轮', `Z 的驳回说明：${lz[1].summary}`)
        assert(lz[2].action === 'reassign_review' && lz[2].summary === `从 ${ctx.displayName(REV1)} 改派给 ${ctx.displayName(REV2)} · v1 · 第 1 轮`, `Z 的改派说明：${lz[2].summary}`)
        const dz = (await logsOf(ctx, z.file_id))[0].detail
        assert(dz.skipped_reviewer === ctx.displayName(REV2) && dz.reviewer === null && dz.review_status === 'unassigned', `Z 的 rescan detail：${JSON.stringify(dz)}`)
        await waitCovers(page)
        await waitNoToast(page)
        await scrollPanelToFile(page)
        await page.screenshot({ path: `${OUT}/m2-file-log-rescan.png` })
        console.log('  ✓ 日志说明：驳回（原因中文、说明）、重扫自动分回、重扫时原审核人已停用改为待分配（另存 m2-file-log-rescan.png）')

        // 重新启用 demo_reviewer2；X：页面上撤回通过 → 重新分给他。
        await ctx.admin('POST', `/api/system/users/${ctx.id(REV2)}/enable`)
        await (await fileCell(page, x.file_name)).click()
        const lx0 = await panelLogs(page, x.file_id, 4, 'approve')
        assert(lx0[0].summary === 'v1 · 第 1 轮 · 说明：页面清楚，通过', `X 的通过说明：${lx0[0].summary}`)
        await page.waitForSelector('[data-testid=reopen-button]', { timeout: 5000 })
        await page.click('[data-testid=reopen-button]')
        const text = await alertText(page)
        assert(text.includes(`撤回「${x.file_name}」的通过`), `撤回的确认框：${text}`)
        await confirmAlert(page)
        await waitToast(page, `重新分给 ${ctx.displayName(REV2)}`)
        const lx = await panelLogs(page, x.file_id, 5, 'reopen')
        assert(lx[0].summary === `重新分给 ${ctx.displayName(REV2)} · v1 · 第 1 轮`, `X 的撤回说明：${lx[0].summary}`)
        assert(lx[2].summary === `从 ${ctx.displayName(REV1)} 改派给 ${ctx.displayName(REV2)} · v1 · 第 1 轮`, `X 的改派说明：${lx[2].summary}`)
        const rx = await expectOpen(ctx, x.file_id, REV2)
        assert(
          rx.reviews.map((r) => `${r.reviewer_id === ctx.id(REV1) ? 'r1' : 'r2'}:${r.status}:${r.result ?? ''}`).join(',') === 'r1:cancelled:,r2:done:approved,r2:open:',
          `X 的 file_reviews：${JSON.stringify(rx.reviews)}`,
        )
        const logs = await logsOf(ctx, x.file_id)
        assert(logs.map((l) => l.action).join(',') === 'reopen,approve,reassign_review,assign_review,upload', `X 的日志顺序：${logs.map((l) => l.action)}`)
        const d = logs[0].detail
        assert(d.by === ctx.displayName(MANAGER) && d.reviewer === ctx.displayName(REV2) && d.reviewer_id === ctx.id(REV2) && !('skipped_reviewer' in d), `reopen 的 detail：${JSON.stringify(d)}`)
        await page.waitForSelector(`[data-testid=single-file-status][data-status=pending]`, { timeout: 5000 })
        await waitCounts(page, { filter: { approved: 0, pending: 2, unassigned: 2 } })
        console.log('  ✓ 撤回通过：重新分给 demo_reviewer2；file_reviews r1 取消 → r2 通过 → r2 open；日志 reopen 的 detail 正确')
        await waitCovers(page)
        await waitNoToast(page)
        await scrollPanelToFile(page)
      },
      async after() {
        await ensureRev2Enabled()
        await dropTemp(logTemp)
      },
    },
    {
      name: 'm2-assign-guards',
      user: MANAGER,
      path: '/review-assign',
      waitFor: '[data-testid=review-assign-page]',
      async prepare(page) {
        const ctx = await context(page)
        guardTemp = await makeTemp(ctx, '审查回归', { '': 3, S: 2, S2: 2, S3: 2, R: 1, M: 0, 'M/X': 3, N: 0, 'N/C': 0, D: 0, 'D/D1': 2, 'D/D1/D2': 2 })
        const t = guardTemp
        const pid = ctx.project.id
        const directIds = t.files[''].map((f) => f.file_id)
        const statusOf = async (fid) => (await reviewsOf(ctx, fid)).review_status

        // 1) 按文件夹分配的确认框开着时，当前文件夹 S 被别人删掉（页面回到上一级）：确认框关掉，上一级不被分配。
        await openAssign(page, t.folders.S)
        await pickReviewer(page, REV1)
        await page.click('[data-testid=assign-button]')
        assert((await alertText(page)).includes('把「S」分给'), '确认框不是 S 的')
        await ctx.admin('DELETE', `/api/folders/${t.folders.S}`)
        await page.waitForFunction((id) => location.search.includes(id), { timeout: 8000 }, t.id)
        await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 5000 })
        await new Promise((r) => setTimeout(r, 800))
        for (const fid of directIds) assert((await statusOf(fid)) === 'unassigned', '确认框关掉后上一级的文件被分配了')
        console.log('  ✓ 确认框开着时当前文件夹被删：回到上一级、确认框自动关闭，上一级的文件没被分配')

        // 1b) 确认框开着时按浏览器"后退"：确认框关掉。
        await page.click(`[data-testid=tree-folder][data-folder-id="${t.id}"]`)
        await page.waitForSelector(`[data-testid=tree-folder][data-folder-id="${t.folders.S2}"]`, { timeout: 5000 })
        await page.click(`[data-testid=tree-folder][data-folder-id="${t.folders.S2}"]`)
        await page.waitForFunction((id) => location.search.includes(id), { timeout: 5000 }, t.folders.S2)
        await page.waitForSelector('[data-testid=review-assign-page][data-list=ready]')
        await waitCounts(page, { folder: 2 })
        await pickReviewer(page, REV1)
        await page.click('[data-testid=assign-button]')
        assert((await alertText(page)).includes('把「S2」分给'), '确认框不是 S2 的')
        await page.goBack()
        await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 5000 })
        await new Promise((r) => setTimeout(r, 800))
        for (const fid of [...directIds, ...t.files.S2.map((f) => f.file_id)]) assert((await statusOf(fid)) === 'unassigned', '后退之后有文件被分配了')
        console.log('  ✓ 确认框开着时后退：确认框自动关闭，没有分配')

        // 2) 改派对话框（筛选了某人）开着时，当前文件夹 S3 被删：对话框关掉，上一级的待审不被改派。
        await ctx.mgr('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: ctx.id(REV1), file_ids: directIds })
        await ctx.mgr('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: ctx.id(REV1), folder_id: t.folders.S3 })
        await openAssign(page, t.folders.S3)
        await pickReviewer(page, REV2)
        await page.click('[data-testid=open-reassign]')
        await page.waitForSelector('[data-testid=reassign-dialog][data-state=ready] [data-testid=assignment-row]', { timeout: 8000 })
        await page.click(`[data-testid=assignment-reviewer][data-reviewer-id="${ctx.id(REV1)}"]`)
        await page.waitForSelector('[data-testid=reassign-all-of]', { timeout: 5000 })
        await ctx.admin('DELETE', `/api/folders/${t.folders.S3}`)
        await page.waitForSelector('[data-testid=reassign-dialog]', { hidden: true, timeout: 8000 })
        await new Promise((r) => setTimeout(r, 800))
        for (const fid of directIds) await expectOpen(ctx, fid, REV1)
        console.log('  ✓ 改派对话框开着时当前文件夹被删：对话框自动关闭，上一级的待审没被改派')

        // 3) 文件夹 X 从 M 移到 N/C 之后，X 里的文件在页面之外被分配：N、C 的计数照样更新（parent 关系不留旧的）。
        await openAssign(page, t.id)
        await page.click(`[data-testid=tree-folder][data-folder-id="${t.id}"]`)
        for (const id of [t.folders.M, t.folders.N]) {
          await page.waitForSelector(`[data-testid=tree-folder][data-folder-id="${id}"]`, { timeout: 5000 })
          await page.click(`[data-testid=tree-folder][data-folder-id="${id}"]`)
          await page.waitForFunction((x) => location.search.includes(x), { timeout: 5000 }, id)
          await page.waitForSelector('[data-testid=review-assign-page][data-list=ready]')
          await new Promise((r) => setTimeout(r, 500))
        }
        const X = t.folders['M/X']
        const C = t.folders['N/C']
        await waitCounts(page, { tree: { [X]: 3, [C]: 0, [t.folders.N]: 0 } })
        await ctx.admin('PATCH', `/api/folders/${X}`, { parent_id: C })
        await waitCounts(page, { tree: { [C]: 3, [t.folders.N]: 3, [t.folders.M]: 0 } })
        await new Promise((r) => setTimeout(r, 500))
        await ctx.mgr('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: ctx.id(REV1), folder_id: X })
        await waitCounts(page, { tree: { [C]: 0, [t.folders.N]: 0 } }, 3000)
        console.log('  ✓ 文件夹移动后在页面之外分配：新位置 N/C 的待分配数 3 秒内变成 0')

        // 4) 通过 → 撤回 → 驳回之后，右栏写"驳回：某人"（不写"通过：某人"），没有撤回通过按钮。
        const r1 = t.files.R[0]
        await ctx.mgr('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: ctx.id(REV1), file_ids: [r1.file_id] })
        const rev1 = await ctx.as(REV1)
        await rev1('POST', `/api/files/${r1.file_id}/review`, { result: 'approved' })
        await ctx.mgr('POST', `/api/files/${r1.file_id}/reopen`)
        await rev1('POST', `/api/files/${r1.file_id}/review`, { result: 'rejected', reject_reasons: ['blur'] })
        await openAssign(page, t.folders.R)
        await (await fileCell(page, r1.file_name)).click()
        await page.waitForSelector(`[data-testid=single-file][data-file-id="${r1.file_id}"] [data-testid=review-history]`, { timeout: 5000 })
        const line = await page.$eval('[data-testid=single-file-reviewer]', (el) => el.textContent?.replace(/\s+/g, '').trim())
        assert(line === `驳回：${ctx.displayName(REV1)}`, `右栏审核人一栏是「${line}」`)
        assert(!(await page.$('[data-testid=reopen-button]')), '驳回的文件不该有撤回通过按钮')
        console.log(`  ✓ 通过 → 撤回 → 驳回之后右栏写"驳回：${ctx.displayName(REV1)}"`)

        // 5) 只有 review_assign、没有 files、scan 菜单的人（拦截 /api/me/menus 模拟）：显示提示，页头四个计数照样有，
        //    实时事件来了会更新。
        const menus = await ctx.mgr('GET', '/api/me/menus')
        const limited = menus.filter((m) => !['files', 'scan'].includes(m.menu_code))
        const other = await page.browserContext().newPage()
        await other.setViewport({ width: 1440, height: 900 })
        try {
          await other.setRequestInterception(true)
          other.on('request', (req) => {
            if (new URL(req.url()).pathname === '/api/me/menus') {
              void req.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(limited) })
            } else void req.continue()
          })
          await other.goto(`${ctx.origin}/review-assign`, { waitUntil: IDLE })
          await other.waitForSelector('[data-testid=review-assign-no-browse]', { timeout: 10000 })
          const root = await ctx.mgr('GET', `/api/projects/${pid}/review-summary`)
          await waitCounts(other, { head: { unassigned: root.counts.unassigned, pending: root.counts.pending } })
          await other.waitForSelector('[data-testid=review-assign-page][data-stream=open]', { timeout: 10000 })
          await ctx.mgr('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: ctx.id(REV1), file_ids: [t.files.S2[0].file_id] })
          await waitCounts(other, { head: { unassigned: root.counts.unassigned - 1, pending: root.counts.pending + 1 } }, 3000)
          console.log('  ✓ 没有 files、scan 菜单：显示提示，页头计数来自 review-summary，3 秒内随事件更新')
          await other.screenshot({ path: `${OUT}/m2-assign-no-browse.png` })
        } finally {
          await other.close()
        }

        // 6) 实时连接断开时（拦截 stream 请求），自己按文件夹分配整个临时文件夹：下面已展开的 D、D1 里的计数
        //    （D1、D2 的待分配数来自 D、D1 的 review-summary）也立即刷新，不只刷新当前文件夹到根这一条。
        const nos = await page.browserContext().newPage()
        await nos.setViewport({ width: 1440, height: 900 })
        try {
          await nos.setRequestInterception(true)
          nos.on('request', (req) => {
            if (/^\/api\/projects\/[^/]+\/stream$/.test(new URL(req.url()).pathname)) void req.abort('failed')
            else void req.continue()
          })
          const D1 = t.folders['D/D1']
          const D2 = t.folders['D/D1/D2']
          await nos.goto(`${ctx.origin}/review-assign?folder=${D1}`, { waitUntil: IDLE })
          await nos.waitForSelector('[data-testid=review-assign-page][data-list=ready]', { timeout: 15000 })
          await nos.click(`[data-testid=tree-folder][data-folder-id="${D1}"]`)
          await nos.waitForSelector(`[data-testid=tree-folder][data-folder-id="${D2}"]`, { timeout: 5000 })
          await nos.click(`[data-testid=tree-folder][data-folder-id="${t.id}"]`)
          await nos.waitForFunction((x) => location.search.includes(x), { timeout: 5000 }, t.id)
          await nos.waitForSelector('[data-testid=review-assign-page][data-list=ready]')
          await waitCounts(nos, { tree: { [t.folders.D]: 4, [D1]: 4, [D2]: 2 } })
          const state = await nos.$eval('[data-testid=review-assign-page]', (el) => el.dataset.stream)
          assert(state !== 'open', `实时连接应该是断开的（${state}）`)
          await pickReviewer(nos, REV2)
          await nos.click('[data-testid=assign-button]')
          await alertText(nos)
          await confirmAlert(nos)
          await waitCounts(nos, { tree: { [t.id]: 0, [t.folders.D]: 0, [D1]: 0, [D2]: 0 } }, 3000)
          console.log(`  ✓ 实时连接断开（${state}）时按文件夹分配：下面已展开的 D、D1、D2 的计数 3 秒内都刷新`)
        } finally {
          await nos.close()
        }
        await page.bringToFront()
        await waitNoToast(page)
        await scrollPanelToFile(page)
        await waitCovers(page)
      },
      async after() {
        await dropTemp(guardTemp)
      },
    },
    {
      name: 'm2-assign-big-event',
      user: MANAGER,
      path: '/review-assign',
      waitFor: '[data-testid=review-assign-page]',
      async prepare(page) {
        const ctx = await context(page)
        // 子文件夹"大"放 510 页：按文件夹分配后事件超过 500 个文件，只带 folder_ids（Q10）。
        const t0 = Date.now()
        bigTemp = await makeTemp(ctx, '大事件', { '': 1, 大: 510 })
        console.log(`  上传 511 页用了 ${((Date.now() - t0) / 1000).toFixed(1)} 秒`)
        const big = bigTemp.files['大']
        await openAssign(page, bigTemp.folders['大'])
        await waitCounts(page, { tree: { [bigTemp.folders['大']]: 510, [bigTemp.id]: 511 }, filter: { unassigned: 510 }, folder: 510 })
        // 选中一个文件：事件没有 file_ids 时，右栏的分配记录和日志按文件夹刷新。
        await (await fileCell(page, big[0].file_name)).click()
        await panelLogs(page, big[0].file_id, 1, 'upload')
        const before = await readCounts(page)
        const listener = listenEvents(ctx.origin, ctx.mgr, ctx.project.id)
        try {
          await listener.opened
          // 页面之外（别的负责人）按文件夹分配。
          const t1 = Date.now()
          const res = await ctx.mgr('POST', `/api/projects/${ctx.project.id}/review-assignments`, { reviewer_id: ctx.id(REV1), folder_id: bigTemp.folders['大'] })
          assert(res.assigned === 510, `分配了 ${res.assigned} 个`)
          await waitCounts(
            page,
            {
              tree: { [bigTemp.folders['大']]: 0, [bigTemp.id]: 1 },
              filter: { unassigned: 0, pending: 510 },
              head: { unassigned: before.head.unassigned - 510, pending: before.head.pending + 510 },
              folder: 0,
            },
            3000,
          )
          await page.waitForSelector(`[data-testid=single-file][data-file-id="${big[0].file_id}"] [data-testid=single-file-status][data-status=pending]`, { timeout: 3000 })
          await panelLogs(page, big[0].file_id, 2, 'assign_review')
          const ms = Date.now() - t1
          const ev = listener.events.find((e) => e.type === 'files.changed' && e.folder_ids?.includes(bigTemp.folders['大']))
          assert(ev, `没收到 files.changed：${JSON.stringify(listener.events)}`)
          assert(!ev.file_ids, `超过 500 个文件的事件不该带 file_ids（带了 ${ev.file_ids?.length} 个）`)
          console.log(`  ✓ 510 个文件的事件只带 folder_ids；页面 ${ms} ms 内更新树、筛选、页头计数和选中文件的分配记录、日志`)
        } finally {
          await listener.stop()
        }
        await waitCovers(page)
      },
      async after() {
        await dropTemp(bigTemp)
      },
    },
  ],
}
