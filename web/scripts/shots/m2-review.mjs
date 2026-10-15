// M2-6 审核工作台 /review 的截图和断言（demo_reviewer 操作，浏览器真实数据 + 壳的 IPC 和 img 协议模拟）。
//
//   cd shell/src-tauri && cargo build --release --example tiles-bench     # 壳模式的模拟要用
//   INIT_ADMIN_PASSWORD=admin123456 [SHOTS_TMP=<临时目录>] \
//     npm run screenshots -- --spec scripts/shots/m2-review.mjs --base http://localhost:<preview 端口> [--only …]
//
// 前端地址是 vite preview（/api 代理到 server）。要先有：
// - DEMO-A 下的演示文件夹「M2-6 审核演示」（30 页，seed:files 建的，都加 --base 和 --user）：
//     npm run seed:files -- --project DEMO-A --folder "M2-6 审核演示/盒 01" --count 12 --low-dpi 2 --remark "M2-6 审核工作台演示：库房 A 架 3 层"
//     npm run seed:files -- --project DEMO-A --folder "M2-6 审核演示/盒 02" --count 10
//     npm run seed:files -- --project DEMO-A --folder "M2-6 审核演示/盒 03" --count 8 --no-cover 1
//   第一次运行时脚本（ensureDemo）在里面做好演示用的审核状态，之后复用、不再改：全部分给 demo_reviewer；盒 02 另传
//   两页"重扫样本"（v1 150 DPI，被 demo_reviewer 驳回后由 demo_scanner 重扫成 v2 300 DPI，第 2 轮待审）；盒 01 前两页
//   通过；盒 03 第 3 页驳回（等重扫）；盒 03 第 5 页通过后被负责人撤回、改派给 demo_reviewer2（demo_reviewer 那里是
//   "历史"）。截图只读这个文件夹（地址栏 ?folder=）。
// - 会改状态的断言各自在项目根下建「截图验证 M2-6 … <时间>」临时文件夹、用接口上传几页、分给 demo_reviewer，做完在
//   after 里删掉（软删除；前面失败了也删）。所以可以反复跑，不动 M1、M2-5 的演示数据。
// - demo_manager、demo_reviewer、demo_reviewer2、demo_scanner 能进 DEMO-A（密码 demo123456，可用 DEMO_PASSWORD 覆盖）。
//
// 截图（docs/screenshots/m2/）：
// - 浏览器：m2-review-list（待办列表，全部标签，按文件夹分组）、m2-review-round2（带"上一轮因为模糊被驳回"的第 2 轮文件）、
//   m2-review-compare（按 C 对比，左边上一轮）、m2-review-reject-panel（驳回面板：键盘勾了原因、写了说明）、
//   m2-review-keyboard-done（只用键盘审完 20 页之后）、m2-review-hold-a（按住 A 之后）、m2-review-reassigned（被改派走的
//   文件消失之后）、m2-review-not-assignee（提交时 403 not_assignee 的提示）、m2-review-big-event（505 个文件的事件之后）、
//   m2-review-batch-confirm（批量通过的确认框，中途另存）、m2-review-batch-done（批量通过、撤回其中一个之后）、
//   m2-review-other-projects（页头"其他项目还有 n 个待审"的弹出层，中途另存）、m2-review-other-projects-switched（点它切到
//   DEMO-B 之后）、m2-review-stale-guards（验收第 1 轮第 1、2、4 条之后；中途另存 m2-review-showing-old：同位切换正在看
//   上一轮时不能审）、m2-review-unseen（验收第 1 轮第 3 条：只看了封面的标记；中途另存 m2-review-cover-confirm：原图没加载
//   出来时的确认框）。
// - 壳（IPC 和 img 协议模拟，分块用 tiles-bench 现场切；另外模拟了 api_stream，实时事件照常）：shell-review（客户端分块
//   显示，给下一张提前切块）、shell-review-compare（第 2 轮按 C 对比，两侧都是分块）。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - m2-review-list：分组头和接口的 folders 一致（3 个文件夹）、标签上的数字等于接口的 counts、"历史"行没有撤回按钮。
// - m2-review-round2 / m2-review-compare：第 2 轮文件显示"上一轮（v1）因‘模糊’被驳回"；按 C 进入对比：左边 v1、标"上一轮"
//   和驳回原因，右边 v2；左侧的快照就是 files/{fid}/reviews 里最近一条 rejected 行的 snapshot_id。
// - m2-review-reject-panel：R 打开面板，1–9 勾选，Tab 进说明框，Esc 取消后什么都没提交（接口核对仍是待审）。
// - m2-review-keyboard-done：20 页只用键盘：18 页按 A，第 5、13 页按 R → 数字勾原因 → Tab → 写说明 → Enter；每次通过、
//   驳回后自动跳到下一张待审。接口核对 20 个文件的 file_reviews（结果、原因、说明、所审快照）和最新一条日志
//   （approve / reject 的 detail：reviewer、reasons、comment、version_no、review_round）。
// - m2-review-hold-a：按住 A 不放（之后的 keydown 都是自动重复），只通过一张，跳到下一张。
// - m2-review-reassigned：负责人在页面之外把两页（含正在看的那页）改派给 demo_reviewer2，3 秒内从待办里消失，
//   当前文件换成下一张。
// - m2-review-not-assignee：拦掉页面的实时连接，负责人在页面之外改派正在看的那页，页面上按 A：提示"已经不是分给你的了"，
//   列表刷新（那一页消失）。
// - m2-review-stale-guards（验收第 1 轮）：
//   第 1 条：当前文件那一行滚出列表的可见范围后，扫描员重扫出 v2：查看器 5 秒内换成 v2，按 A 审的是 v2（不再 409）；
//   第 2 条：最后一张待审驳回后（停在它上面）被重扫回来：按 R 面板里没有上一轮的原因和说明，直接按 Enter 被拦下；
//   第 4 条：第 2 轮文件按 C 对比后，并排时第一次按 V 直接显示左边（上一轮）：操作区不能审、提示"按 V 切回"，按 A 不通过；
//   再按 V 切回当前版本，按 A 通过的是 v2。
// - m2-review-unseen（验收第 1 轮第 3 条，CDP 改 batch 的响应）：原图、封面都没显示出来时 A、R 都不响应、提示"没看到
//   这一版"；原图加载失败、封面显示了时 A 先弹"只看了封面"的确认框，取消不通过、确认后通过；原图超出浏览器预算、只显示
//   封面时 A 直接通过，画面上标出"只看了封面"。
// - m2-review-big-event：505 页按文件夹分给 demo_reviewer、再按子树改派走：页面之外订阅 SSE 核对两次事件都只带
//   folder_ids（Q10）；分来的靠核对计数发现、整体重拉后出现，改派走的按文件夹整体重拉后消失（都在 5 秒内）。
// - m2-review-batch-done：Shift、⌘ 点选 5 个（其中 1 个已通过）→ 确认框写"批量通过 4 个文件"、"另有 1 个不是待审"
//   → 确认后接口核对 4 个通过、日志带 batch:true；再在页面上撤回其中一个的通过（Q8），回到待审。
// - m2-review-other-projects-switched：DEMO-B 有 2 页分给 demo_reviewer 时，页头"其他项目还有 n 个待审"和接口的
//   other_projects_open 一致；点弹出层里的 DEMO-B 切过去（范围清掉），待办换成 DEMO-B 的（Q12）。
// - shell-review：界面用客户端分块（data-mode=tiles），当前这张画出来后对下一张（A 会跳到的那张）调了 prepare_tiles。
// - shell-review-compare：壳里按 C，两侧都用分块显示，左边 v1。

import path from 'node:path'

import {
  apiClient,
  assert,
  installShellMock,
  login as loginPage,
  shotsTmp,
  sleep,
} from '../lib/viewer-shots.mjs'
import { makeScanPage } from '../lib/scan-pages.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'
const PROJECT = 'DEMO-A'
const DEMO_FOLDER = 'M2-6 审核演示'
const BOXES = ['盒 01', '盒 02', '盒 03']
const MANAGER = 'demo_manager'
const REV1 = 'demo_reviewer'
const REV2 = 'demo_reviewer2'
const SCANNER = 'demo_scanner'
const IDLE = 'networkidle2'
const OUT = '../docs/screenshots/m2'
/** 中途另存的截图（批量通过的确认框）：和截图同一个目录（命令行的 --out，默认 docs/screenshots/m2）。 */
const outArg = process.argv.indexOf('--out')
const OUT_DIR = path.resolve(outArg > 0 ? process.argv[outArg + 1] : OUT)

/** 盒 02 里的两页"重扫样本"：v1 150 DPI 被驳回，重扫成 v2 300 DPI。 */
const ROUND2 = [
  { name: '重扫样本 01.png', reasons: ['blur'], comment: '150 dpi，字迹发虚，请按 300 dpi 重扫' },
  { name: '重扫样本 02.png', reasons: ['skew', 'other'], comment: '右下角折角，整页向右倾斜约 3°' },
]

// ---------- 接口 ----------

async function as(origin, username, password = demoPassword) {
  const { call } = apiClient(origin)
  await call('POST', '/api/auth/login', { username, password })
  return call
}

let ctxPromise = null

/** 登录 admin、demo_manager、demo_reviewer、demo_scanner，找到 DEMO-A、演示文件夹和用户 id（只做一次）。 */
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
      assert(u.status === 'active', `${name} 是停用的`)
      users[name] = u
    }
    const ctx = {
      origin,
      project,
      admin: adminApi,
      mgr: await as(origin, MANAGER),
      rev1: await as(origin, REV1),
      scanner: await as(origin, SCANNER),
      id: (name) => users[name].id,
      displayName: (name) => users[name].display_name,
    }
    ctx.demo = await ensureDemo(ctx)
    return ctx
  })()
  return ctxPromise
}

async function childFolder(call, parentId, name) {
  const ch = await call('GET', `/api/folders/${parentId}/children`)
  return ch.folders.find((f) => f.folder_name === name) ?? null
}

function pageForm(name, p) {
  const form = new FormData()
  form.append('file_name', name)
  form.append('cover', new Blob([p.cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([p.png], { type: 'image/png' }), 'page.png')
  return form
}

async function lastRow(ctx, fileId) {
  const r = await ctx.mgr('GET', `/api/files/${fileId}/reviews`)
  return { file: r, last: r.reviews[r.reviews.length - 1] ?? null }
}

/** 演示文件夹的审核状态（见文件开头）；已经做过的步骤跳过。返回 {id, boxes: {名字: id}, files: {盒/文件名: file}}。 */
async function ensureDemo(ctx) {
  const root = ctx.project.root_folder_id
  const demo = await childFolder(ctx.admin, root, DEMO_FOLDER)
  assert(demo, `找不到 ${PROJECT}/${DEMO_FOLDER}，先按脚本开头的 seed:files 命令建好`)
  const boxes = {}
  for (const b of BOXES) {
    const f = await childFolder(ctx.admin, demo.id, b)
    assert(f, `找不到 ${DEMO_FOLDER}/${b}，先按脚本开头的 seed:files 命令建好`)
    boxes[b] = f.id
  }
  // 盒 02 的重扫样本（v1 150 DPI）。
  let box2 = await ctx.admin('GET', `/api/folders/${boxes['盒 02']}/children`)
  for (const s of ROUND2) {
    if (!box2.files.some((f) => f.file_name === s.name)) {
      const p = makeScanPage({ seed: `M2-6 ${s.name}`, pageNo: 3, dpi: 150, color: true })
      await ctx.admin('POST', `/api/folders/${boxes['盒 02']}/files`, pageForm(s.name, p))
      console.log(`  上传了 ${s.name} v1（150 DPI）`)
    }
  }
  const files = {}
  for (const b of BOXES) {
    const ch = await ctx.admin('GET', `/api/folders/${boxes[b]}/children`)
    for (const f of ch.files) files[`${b}/${f.file_name}`] = f
  }
  // 待分配的全部分给 demo_reviewer。
  const unassigned = Object.values(files).filter((f) => f.review_status === 'unassigned').map((f) => f.file_id)
  if (unassigned.length) {
    await ctx.mgr('POST', `/api/projects/${ctx.project.id}/review-assignments`, { reviewer_id: ctx.id(REV1), file_ids: unassigned })
    console.log(`  分给 ${REV1} ${unassigned.length} 页`)
  }
  // 重扫样本：驳回 → 重扫（v2 300 DPI，第 2 轮，自动分回 demo_reviewer）。
  for (const s of ROUND2) {
    const f = files[`盒 02/${s.name}`]
    let { file } = await lastRow(ctx, f.file_id)
    if (file.version_no === 1 && file.review_status === 'pending') {
      await ctx.rev1('POST', `/api/files/${f.file_id}/review`, { result: 'rejected', reject_reasons: s.reasons, comment: s.comment })
      file = (await lastRow(ctx, f.file_id)).file
      console.log(`  ${s.name} 已驳回`)
    }
    if (file.version_no === 1 && file.review_status === 'rejected') {
      const p = makeScanPage({ seed: `M2-6 ${s.name}`, pageNo: 3, dpi: 300, color: true })
      let rescanner = ctx.scanner
      try {
        await rescanner('POST', `/api/files/${f.file_id}/rescan`, pageForm(s.name, p))
      } catch (e) {
        console.log(`  ${SCANNER} 重扫失败（${e.message}），改用 admin`)
        rescanner = ctx.admin
        await rescanner('POST', `/api/files/${f.file_id}/rescan`, pageForm(s.name, p))
      }
      console.log(`  ${s.name} 重扫了 v2（300 DPI）`)
    }
  }
  // 盒 01 前两页通过。
  for (const n of ['IMG_0001.png', 'IMG_0002.png']) {
    const f = files[`盒 01/${n}`]
    const { file } = await lastRow(ctx, f.file_id)
    if (file.review_status === 'pending') await ctx.rev1('POST', `/api/files/${f.file_id}/review`, { result: 'approved' })
  }
  // 盒 03 第 3 页驳回（等重扫）。
  {
    const f = files['盒 03/IMG_0003.png']
    const { file } = await lastRow(ctx, f.file_id)
    if (file.review_status === 'pending' && file.version_no === 1) {
      await ctx.rev1('POST', `/api/files/${f.file_id}/review`, {
        result: 'rejected',
        reject_reasons: ['stain'],
        comment: '左上角有一块水渍，挡住了标题',
      })
    }
  }
  // 盒 03 第 5 页：通过 → 负责人撤回 → 改派给 demo_reviewer2（demo_reviewer 那里成了"历史"）。
  {
    const f = files['盒 03/IMG_0005.png']
    let { file, last } = await lastRow(ctx, f.file_id)
    if (file.review_status === 'pending' && last?.reviewer_id === ctx.id(REV1) && !file.reviews.some((r) => r.result === 'approved')) {
      await ctx.rev1('POST', `/api/files/${f.file_id}/review`, { result: 'approved' })
      ;({ file, last } = await lastRow(ctx, f.file_id))
    }
    if (file.review_status === 'approved' && last?.reviewer_id === ctx.id(REV1)) {
      await ctx.mgr('POST', `/api/files/${f.file_id}/reopen`)
      ;({ file, last } = await lastRow(ctx, f.file_id))
    }
    if (file.review_status === 'pending' && last?.status === 'open' && last.reviewer_id === ctx.id(REV1) && file.reviews.some((r) => r.result === 'approved')) {
      await ctx.mgr('POST', `/api/review-assignments/${last.assignment_id}/reassign`, { reviewer_id: ctx.id(REV2) })
      console.log('  盒 03/IMG_0005.png：通过 → 撤回 → 改派给 demo_reviewer2（历史行）')
    }
  }
  return { id: demo.id, boxes, files }
}

function stamp() {
  return new Date().toISOString().replace(/[-:T]/g, '').slice(0, 14)
}

/**
 * 在项目根下建临时文件夹，上传 count 页、分给 demo_reviewer（assign 为 false 时不分）。
 * 返回 {id, name, files: [{file_id, file_name}]}（按 sort_no）。
 */
async function makeTemp(ctx, label, count, { assign = true } = {}) {
  const name = `截图验证 M2-6 ${label} ${stamp()}`
  const top = await ctx.admin('POST', '/api/folders', {
    parent_id: ctx.project.root_folder_id,
    folder_name: name,
    remark: 'M2-6 截图脚本临时建的，做完会删掉',
  })
  const seed = `M2-6 ${label} ${stamp()} ${Math.random()}`
  const files = []
  for (let i = 1; i <= count; i++) {
    const fileName = `P_${String(i).padStart(2, '0')}.png`
    const r = await ctx.admin('POST', `/api/folders/${top.id}/files`, pageForm(fileName, makeScanPage({ seed: `${seed}#${i}`, pageNo: i })))
    files.push({ file_id: r.file_id, file_name: r.file_name })
  }
  if (assign) await ctx.mgr('POST', `/api/projects/${ctx.project.id}/review-assignments`, { reviewer_id: ctx.id(REV1), folder_id: top.id })
  return { id: top.id, name, files }
}

/** 登录拿会话 Cookie（订阅事件用）。 */
async function sessionCookie(origin, username, password) {
  const res = await fetch(`${origin}/api/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  assert(res.ok, `${username} 登录失败：${res.status}`)
  return (res.headers.get('set-cookie') ?? '').split(';')[0]
}

/** 在页面之外订阅项目的实时事件（经前端地址的代理），收集事件，用来核对内容（同 m2-assign.mjs）。 */
function listenEvents(origin, cookie, projectId) {
  const events = []
  const ctrl = new AbortController()
  let resolveOpen
  const opened = new Promise((r) => (resolveOpen = r))
  const done = (async () => {
    const res = await fetch(`${origin}/api/projects/${projectId}/stream`, {
      headers: { Accept: 'text/event-stream', Cookie: cookie },
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

async function dropTemp(temp) {
  if (!temp || !ctxPromise) return
  const ctx = await ctxPromise
  await ctx.admin('DELETE', `/api/folders/${temp.id}`, undefined, { allow: [404] })
  console.log(`  已删除临时文件夹「${temp.name}」`)
}

// ---------- 页面 ----------

/** 打开审核台（范围是 folderId），等列表取好、实时连接建立、当前这张的原图画完。 */
async function openReview(page, folderId, { tab, stream = true } = {}) {
  const ctx = await context(page)
  await page.goto(`${ctx.origin}/review?folder=${folderId}`, { waitUntil: IDLE })
  await page.waitForSelector(`[data-testid=review-page][data-list=ready]${stream ? '[data-stream=open]' : ''}`, { timeout: 15000 })
  if (tab) await setTab(page, tab)
  await waitReady(page)
}

async function setTab(page, state) {
  await page.click(`[data-testid=review-tab][data-state=${state}]`)
  await page.waitForSelector(`[data-testid=review-tab][data-state=${state}][aria-checked=true]`, { timeout: 3000 })
  await page.waitForSelector('[data-testid=review-page][data-list=ready]', { timeout: 10000 })
}

/** 等当前文件（或指定的文件）的原图状态是 ready（或 states 之一）。 */
async function waitReady(page, fileId, states = ['ready'], timeout = 20000) {
  try {
    await page.waitForFunction(
      (fid, st) => {
        const el = document.querySelector('[data-testid=review-page]')
        if (!el || !el.dataset.current) return false
        if (fid && el.dataset.current !== fid) return false
        return st.includes(el.dataset.phase)
      },
      { timeout, polling: 50 },
      fileId ?? null,
      states,
    )
  } catch (e) {
    const state = await page.evaluate(() => ({
      page: { ...document.querySelector('[data-testid=review-page]')?.dataset },
      viewers: [...document.querySelectorAll('[data-testid=image-viewer]')].map((v) => ({
        ...v.dataset,
        problem: v.querySelector('[data-testid=viewer-problem]')?.textContent?.replace(/\s+/g, ' ').trim(),
      })),
    }))
    throw new Error(`等 ${fileId ?? '当前文件'} 的原图（${states}）超时：${JSON.stringify(state)}（${e.message}）`)
  }
  return page.$eval('[data-testid=review-page]', (el) => el.dataset.current)
}

async function current(page) {
  return page.$eval('[data-testid=review-page]', (el) => el.dataset.current)
}

/** 列表是虚拟滚动的：从头往下滚，直到这一行渲染出来，再把它滚进视野。 */
async function revealRow(page, fileId) {
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
  return sel
}

/** 点列表里的某个文件（看它）。 */
async function clickRow(page, fileId, mods = []) {
  const sel = await revealRow(page, fileId)
  for (const m of mods) await page.keyboard.down(m)
  await page.click(sel)
  for (const m of [...mods].reverse()) await page.keyboard.up(m)
}

async function rowIds(page) {
  return page.$$eval('[data-testid=review-row]', (els) => els.map((e) => e.dataset.fileId))
}

async function waitToast(page, text, timeout = 8000) {
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

// ---------- 截图 ----------

let tempKeyboard = null
let tempHold = null
let tempReassign = null
let tempNotAssignee = null
let tempBatch = null
let tempBig = null
let tempOther = null
let tempStale = null
let tempStale2 = null
let tempUnseen = null
let unseenCdp = null
let notAssigneeHandler = null

function demoFile(ctx, key) {
  const f = ctx.demo.files[key]
  assert(f, `演示文件夹里没有 ${key}`)
  return f
}

export default {
  out: OUT,
  accounts: { [REV1]: demoPassword },
  waitUntil: IDLE,
  shots: [
    {
      name: 'm2-review-list',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        await openReview(page, ctx.demo.id, { tab: 'all' })
        const api = await ctx.rev1('GET', `/api/me/reviews?project_id=${ctx.project.id}&folder_id=${ctx.demo.id}&limit=500`)
        // 列表是虚拟滚动的，看不到的分组头不在 DOM 里：取列表上记下的全部分组。
        const groups = await page.$eval('[data-testid=review-list]', (e) => (e.dataset.groups ? e.dataset.groups.split(',') : []))
        assert(
          JSON.stringify(groups) === JSON.stringify(api.folders.map((f) => f.folder_id)),
          `分组头 ${JSON.stringify(groups)} 和接口的 folders 不一致`,
        )
        assert(groups.length === 3, `应该有 3 个文件夹分组，实际 ${groups.length}`)
        const tabs = await page.$$eval('[data-testid=review-tab]', (els) => Object.fromEntries(els.map((e) => [e.dataset.state, Number(e.dataset.count)])))
        for (const s of ['all', 'open', 'approved', 'rejected']) assert(tabs[s] === api.counts[s], `标签「${s}」是 ${tabs[s]}，接口是 ${api.counts[s]}`)
        const lowDpi = await page.$eval('[data-testid=review-tab-low-dpi]', (e) => Number(e.dataset.count))
        assert(lowDpi === api.counts.low_dpi, `DPI 不足是 ${lowDpi}，接口是 ${api.counts.low_dpi}`)
        // 历史行（盒 03/IMG_0005.png）：没有撤回按钮。
        const hist = demoFile(ctx, '盒 03/IMG_0005.png')
        await revealRow(page, hist.file_id)
        await page.waitForSelector(`[data-testid=review-row][data-file-id="${hist.file_id}"][data-history=true]`, { timeout: 3000 })
        await clickRow(page, hist.file_id)
        await page.waitForSelector(`[data-testid=review-actions][data-file-id="${hist.file_id}"] [data-testid=review-state-note]`, { timeout: 3000 })
        assert(!(await page.$('[data-testid=review-reopen]')), '历史行不该有"撤回通过"')
        // 截图时看一页待审的（盒 01 第 3 页，低 DPI 的在它旁边）。
        const f = demoFile(ctx, '盒 01/IMG_0003.png')
        await clickRow(page, f.file_id)
        await waitReady(page, f.file_id)
        await rest(page)
        console.log(`  ✓ 分组 ${groups.length} 个、标签计数 ${JSON.stringify(tabs)}、DPI 不足 ${lowDpi}，和接口一致；历史行没有撤回按钮`)
      },
      delay: 500,
    },
    {
      name: 'm2-review-round2',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        await openReview(page, ctx.demo.id, { tab: 'open' })
        const f = demoFile(ctx, `盒 02/${ROUND2[0].name}`)
        await clickRow(page, f.file_id)
        await waitReady(page, f.file_id)
        await page.waitForSelector('[data-testid=prev-reject][data-version="1"]', { timeout: 5000 })
        await page.waitForSelector('[data-testid=viewer-prev-reject]', { timeout: 5000 })
        const text = await page.$eval('[data-testid=prev-reject]', (e) => e.textContent ?? '')
        assert(text.includes('模糊') && text.includes(ROUND2[0].comment), `上一轮的提示不对：${text}`)
        await rest(page)
        console.log(`  ✓ 第 2 轮：${text.replace(/\s+/g, ' ').trim()}`)
      },
      delay: 500,
    },
    {
      name: 'm2-review-compare',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        await openReview(page, ctx.demo.id, { tab: 'open' })
        const f = demoFile(ctx, `盒 02/${ROUND2[0].name}`)
        await clickRow(page, f.file_id)
        await waitReady(page, f.file_id)
        await page.waitForSelector('[data-testid=prev-reject]', { timeout: 5000 })
        await rest(page)
        await page.keyboard.press('c')
        await page.waitForSelector('[data-testid=compare-viewer]', { timeout: 5000 })
        await page.waitForFunction(
          () => [...document.querySelectorAll('[data-testid=compare-pane] [data-testid=image-viewer]')].every((v) => v.dataset.state === 'ready'),
          { timeout: 20000 },
        )
        const reviews = await ctx.rev1('GET', `/api/files/${f.file_id}/reviews`)
        const rej = [...reviews.reviews].reverse().find((r) => r.result === 'rejected' && r.status !== 'cancelled')
        const sides = await page.$$eval('[data-testid=compare-pane]', (els) =>
          els.map((e) => ({
            side: e.dataset.side,
            sid: e.querySelector('[data-testid=image-viewer]')?.dataset.snapshotId,
            label: e.querySelector('[data-testid=compare-label]')?.textContent?.replace(/\s+/g, ' ').trim(),
          })),
        )
        const left = sides.find((s) => s.side === 'left')
        const right = sides.find((s) => s.side === 'right')
        assert(left.sid === rej.snapshot_id, `左侧的快照 ${left.sid} 不是上一轮驳回的 ${rej.snapshot_id}`)
        assert(left.label.includes('v1') && left.label.includes('上一轮') && left.label.includes('模糊'), `左侧角标不对：${left.label}`)
        assert(right.label.includes('v2'), `右侧角标不对：${right.label}`)
        // 联动：放大两次，两侧的视口一致。
        await page.keyboard.press('=')
        await page.keyboard.press('=')
        await sleep(800)
        const vz = await page.$$eval('[data-testid=compare-pane] [data-testid=image-viewer]', (els) => els.map((e) => e.dataset.viewZoom))
        assert(vz[0] === vz[1], `两侧的缩放不一致：${vz}`)
        await page.keyboard.press('0')
        await sleep(600)
        await rest(page)
        console.log(`  ✓ C 进入对比：左 ${left.label}（${left.sid}）、右 ${right.label}；放大两侧联动`)
      },
      delay: 500,
    },
    {
      name: 'm2-review-reject-panel',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        await openReview(page, ctx.demo.id, { tab: 'open' })
        const f = demoFile(ctx, '盒 01/IMG_0005.png')
        await clickRow(page, f.file_id)
        await waitReady(page, f.file_id)
        await rest(page)
        await page.keyboard.press('r')
        await page.waitForSelector('[data-testid=review-actions][data-panel=open]', { timeout: 3000 })
        await page.keyboard.press('2')
        await page.keyboard.press('8')
        // 选了"其他"不写说明：Enter 被拦下，提示要写说明。
        await page.keyboard.press('Enter')
        await page.waitForSelector('[data-testid=reject-error]', { timeout: 3000 })
        const err = await page.$eval('[data-testid=reject-error]', (e) => e.textContent ?? '')
        assert(err.includes('说明'), `选了其他不写说明的提示不对：${err}`)
        await page.keyboard.press('Tab')
        const focused = await page.evaluate(() => document.activeElement?.dataset?.testid)
        assert(focused === 'reject-comment', `Tab 之后焦点在 ${focused}`)
        await page.keyboard.type('页面向右倾斜约 3°，右下角折角挡住了页码')
        const checked = await page.$$eval('[data-testid=reject-reason][data-checked=true]', (els) => els.map((e) => e.dataset.code))
        assert(JSON.stringify(checked) === JSON.stringify(['skew', 'other']), `勾选的原因 ${checked}`)
        // Esc 取消：什么都不提交。
        await page.keyboard.press('Escape')
        await page.waitForSelector('[data-testid=review-actions][data-panel=closed]', { timeout: 3000 })
        await sleep(300)
        const r = await ctx.mgr('GET', `/api/files/${f.file_id}/reviews`)
        assert(r.review_status === 'pending', `Esc 取消之后文件是 ${r.review_status}`)
        console.log('  ✓ R 打开面板、2/8 勾倾斜和其他、没写说明被拦下、Tab 进说明框、Esc 取消后没有提交')
        // 从说明框再 Tab 到"取消"按钮，按 Enter：是取消，不是确认驳回（审查第 1 轮）。
        await page.keyboard.press('r')
        await page.waitForSelector('[data-testid=review-actions][data-panel=open]', { timeout: 3000 })
        await page.keyboard.press('1')
        await page.keyboard.press('Tab')
        await page.keyboard.press('Tab')
        const onCancel = await page.evaluate(() => document.activeElement?.dataset?.testid)
        assert(onCancel === 'reject-cancel', `第二次 Tab 之后焦点在 ${onCancel}`)
        await page.keyboard.press('Enter')
        await page.waitForSelector('[data-testid=review-actions][data-panel=closed]', { timeout: 3000 })
        await sleep(300)
        const r2 = await ctx.mgr('GET', `/api/files/${f.file_id}/reviews`)
        assert(r2.review_status === 'pending', `在"取消"上按 Enter 之后文件是 ${r2.review_status}`)
        console.log('  ✓ Tab 到"取消"按钮上按 Enter 是取消，没有提交')
        // 截图：重新打开面板（Esc 已经丢掉了上次的勾选和说明）、勾好、写好说明（截完在 after 里 Esc，不提交）。
        await page.keyboard.press('r')
        await page.waitForSelector('[data-testid=review-actions][data-panel=open]', { timeout: 3000 })
        assert(!(await page.$('[data-testid=reject-reason][data-checked=true]')), 'Esc 之后再打开，不该还勾着原因')
        assert((await page.$eval('[data-testid=reject-comment]', (e) => e.value)) === '', 'Esc 之后再打开，说明应该是空的')
        await page.keyboard.press('2')
        await page.keyboard.press('8')
        await page.keyboard.press('Tab')
        await page.keyboard.type('页面向右倾斜约 3°，右下角折角挡住了页码')
        await page.mouse.move(5, 895)
      },
      async after(page) {
        // 关掉面板，不提交。
        await page.keyboard.press('Escape').catch(() => {})
      },
      delay: 300,
    },
    {
      name: 'm2-review-keyboard-done',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        tempKeyboard = await makeTemp(ctx, '键盘 20 页', 20)
        const files = tempKeyboard.files
        await openReview(page, tempKeyboard.id, { tab: 'open' })
        await rest(page)
        const rejects = {
          4: { keys: ['1', '8'], reasons: ['blur', 'other'], comment: '第 5 页字迹发虚，装订线一侧有阴影' },
          12: { keys: ['2', '4'], reasons: ['skew', 'stain'], comment: '倾斜约 3°，右上角有污渍' },
        }
        const t0 = Date.now()
        for (let i = 0; i < files.length; i++) {
          const f = files[i]
          const cur = await waitReady(page, f.file_id)
          assert(cur === f.file_id, `第 ${i + 1} 页时当前文件是 ${cur}`)
          const rj = rejects[i]
          if (rj) {
            await page.keyboard.press('r')
            await page.waitForSelector('[data-testid=review-actions][data-panel=open]', { timeout: 3000 })
            for (const k of rj.keys) await page.keyboard.press(k)
            await page.keyboard.press('Tab')
            await page.keyboard.type(rj.comment)
            await page.keyboard.press('Enter')
          } else {
            await page.keyboard.press('a')
          }
          // 每次审完自动跳到下一张待审（最后一张审完提示"都审完了"）。
          if (i < files.length - 1) {
            await page.waitForSelector(`[data-testid=review-page][data-current="${files[i + 1].file_id}"]`, { timeout: 5000 })
          } else {
            await waitToast(page, '都审完了')
          }
        }
        const ms = Date.now() - t0
        // 接口核对。
        let approved = 0
        let rejected = 0
        for (let i = 0; i < files.length; i++) {
          const f = files[i]
          const r = await ctx.mgr('GET', `/api/files/${f.file_id}/reviews`)
          const row = r.reviews[r.reviews.length - 1]
          const logs = (await ctx.mgr('GET', `/api/files/${f.file_id}/logs`)).logs
          const log = logs[0]
          const snaps = await ctx.mgr('GET', `/api/files/${f.file_id}/snapshots`)
          const sid = snaps.snapshots[snaps.snapshots.length - 1].snapshot_id
          assert(row.status === 'done' && row.reviewer_id === ctx.id(REV1), `${f.file_name} 的分配行是 ${row.status}`)
          assert(row.snapshot_id === sid, `${f.file_name} 审的快照不是当前版本`)
          const rj = rejects[i]
          if (rj) {
            rejected++
            assert(r.review_status === 'rejected' && row.result === 'rejected', `${f.file_name} 应该是驳回，实际 ${r.review_status}`)
            assert(JSON.stringify(row.reject_reasons) === JSON.stringify(rj.reasons), `${f.file_name} 的原因 ${row.reject_reasons}`)
            assert(row.comment === rj.comment, `${f.file_name} 的说明「${row.comment}」`)
            assert(log.action === 'reject', `${f.file_name} 最新日志是 ${log.action}`)
            assert(JSON.stringify(log.detail.reasons) === JSON.stringify(rj.reasons) && log.detail.comment === rj.comment, `${f.file_name} 的驳回日志 ${JSON.stringify(log.detail)}`)
          } else {
            approved++
            assert(r.review_status === 'approved' && row.result === 'approved', `${f.file_name} 应该是通过，实际 ${r.review_status}`)
            assert(log.action === 'approve', `${f.file_name} 最新日志是 ${log.action}`)
            assert(!log.detail.batch, `${f.file_name} 不该是批量通过`)
          }
          assert(log.detail.reviewer === ctx.displayName(REV1), `${f.file_name} 日志的审核人 ${log.detail.reviewer}`)
          assert(log.detail.version_no === 1 && log.detail.review_round === 1, `${f.file_name} 日志的版本、轮次 ${JSON.stringify(log.detail)}`)
          assert(log.detail.file_name === f.file_name, `${f.file_name} 日志的文件名 ${log.detail.file_name}`)
          assert(logs.filter((l) => l.action === 'approve' || l.action === 'reject').length === 1, `${f.file_name} 有多条审核日志`)
        }
        assert(approved === 18 && rejected === 2, `通过 ${approved}、驳回 ${rejected}`)
        const tabs = await page.$$eval('[data-testid=review-tab]', (els) => Object.fromEntries(els.map((e) => [e.dataset.state, Number(e.dataset.count)])))
        assert(tabs.open === 0 && tabs.approved === 18 && tabs.rejected === 2, `标签计数 ${JSON.stringify(tabs)}`)
        await waitNoToast(page)
        await rest(page)
        console.log(`  ✓ 只用键盘审完 20 页（${(ms / 1000).toFixed(1)} 秒）：通过 18、驳回 2（原因、说明、快照、日志都对）`)
      },
      async after() {
        await dropTemp(tempKeyboard)
      },
      delay: 300,
    },
    {
      name: 'm2-review-hold-a',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        tempHold = await makeTemp(ctx, '按住 A', 3)
        const [f1, f2, f3] = tempHold.files
        await openReview(page, tempHold.id, { tab: 'open' })
        await rest(page)
        await waitReady(page, f1.file_id)
        // 按下 A，然后一直不松：之后的 keydown 都是自动重复（puppeteer 对已按下的键再 down 时 autoRepeat=true）。
        await page.keyboard.down('a')
        for (let i = 0; i < 5; i++) {
          await sleep(40)
          await page.keyboard.down('a')
        }
        await waitReady(page, f2.file_id)
        for (let i = 0; i < 10; i++) {
          await sleep(60)
          await page.keyboard.down('a')
        }
        await page.keyboard.up('a')
        await sleep(800)
        const states = []
        for (const f of [f1, f2, f3]) states.push((await ctx.mgr('GET', `/api/files/${f.file_id}/reviews`)).review_status)
        assert(JSON.stringify(states) === JSON.stringify(['approved', 'pending', 'pending']), `按住 A 之后三页是 ${states}`)
        assert((await current(page)) === f2.file_id, '应该停在第 2 页')
        console.log(`  ✓ 按住 A 不放（15 次自动重复）只通过了 1 页：${states.join('、')}`)
      },
      async after() {
        await dropTemp(tempHold)
      },
    },
    {
      name: 'm2-review-reassigned',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        tempReassign = await makeTemp(ctx, '改派走', 5)
        const files = tempReassign.files
        await openReview(page, tempReassign.id, { tab: 'open' })
        await clickRow(page, files[1].file_id)
        await waitReady(page, files[1].file_id)
        await rest(page)
        const t0 = Date.now()
        // 负责人在页面之外把正在看的第 2 页和第 4 页改派给 demo_reviewer2。
        const res = await ctx.mgr('POST', `/api/projects/${ctx.project.id}/review-assignments:reassign`, {
          to_reviewer_id: ctx.id(REV2),
          file_ids: [files[1].file_id, files[3].file_id],
        })
        assert(res.reassigned === 2, `改派了 ${res.reassigned} 个`)
        await page.waitForFunction(
          (a, b) => !document.querySelector(`[data-testid=review-row][data-file-id="${a}"]`) && !document.querySelector(`[data-testid=review-row][data-file-id="${b}"]`),
          { timeout: 3000, polling: 50 },
          files[1].file_id,
          files[3].file_id,
        )
        const ms = Date.now() - t0
        const ids = await rowIds(page)
        assert(JSON.stringify(ids) === JSON.stringify([files[0].file_id, files[2].file_id, files[4].file_id]), `剩下的行 ${ids}`)
        await page.waitForSelector(`[data-testid=review-page][data-current="${files[2].file_id}"]`, { timeout: 3000 })
        const tabs = await page.$$eval('[data-testid=review-tab]', (els) => Object.fromEntries(els.map((e) => [e.dataset.state, Number(e.dataset.count)])))
        assert(tabs.open === 3 && tabs.all === 3, `改派后的标签计数 ${JSON.stringify(tabs)}`)
        await waitToast(page, '已从待办里移除')
        await waitReady(page, files[2].file_id)
        console.log(`  ✓ 改派走的 2 页 ${ms} ms 内从待办里消失，当前换成下一页，计数 ${JSON.stringify(tabs)}`)
      },
      async after() {
        await dropTemp(tempReassign)
      },
      delay: 200,
    },
    {
      name: 'm2-review-not-assignee',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        tempNotAssignee = await makeTemp(ctx, '不是分给你的', 3)
        const files = tempNotAssignee.files
        // 拦掉实时连接：页面收不到改派的事件，按 A 时才从 server 知道。
        await page.setRequestInterception(true)
        notAssigneeHandler = (req) => {
          if (req.isInterceptResolutionHandled()) return
          if (req.url().includes('/stream')) req.abort().catch(() => {})
          else req.continue().catch(() => {})
        }
        page.on('request', notAssigneeHandler)
        await openReview(page, tempNotAssignee.id, { tab: 'open', stream: false })
        const st = await page.$eval('[data-testid=review-page]', (e) => e.dataset.stream)
        assert(st !== 'open', `实时连接应该断着，实际 ${st}`)
        await waitReady(page, files[0].file_id)
        await rest(page)
        const reviews = await ctx.mgr('GET', `/api/files/${files[0].file_id}/reviews`)
        const open = reviews.reviews.find((r) => r.status === 'open')
        await ctx.mgr('POST', `/api/review-assignments/${open.assignment_id}/reassign`, { reviewer_id: ctx.id(REV2) })
        await sleep(300)
        assert(await page.$(`[data-testid=review-row][data-file-id="${files[0].file_id}"]`), '断了实时连接，页面不该已经知道')
        await page.keyboard.press('a')
        await waitToast(page, '已经不是分给你的了')
        await page.waitForFunction(
          (fid) => !document.querySelector(`[data-testid=review-row][data-file-id="${fid}"]`),
          { timeout: 5000 },
          files[0].file_id,
        )
        const after = await ctx.mgr('GET', `/api/files/${files[0].file_id}/reviews`)
        assert(after.review_status === 'pending' && after.reviews.at(-1).reviewer_id === ctx.id(REV2), '这一页应该仍是待审、在 demo_reviewer2 那里')
        await page.waitForSelector(`[data-testid=review-page][data-current="${files[1].file_id}"]`, { timeout: 5000 })
        await rest(page)
        console.log('  ✓ 403 not_assignee：提示"已经不是分给你的了"，列表刷新（那一页消失），当前换成下一页')
      },
      async after(page) {
        if (notAssigneeHandler) page.off('request', notAssigneeHandler)
        notAssigneeHandler = null
        await page.setRequestInterception(false).catch(() => {})
        await dropTemp(tempNotAssignee)
      },
      delay: 200,
    },
    {
      // 验收第 1 轮的第 1、2、4 条（同位切换显示左边那一张中途另存 m2-review-showing-old.png）。
      name: 'm2-review-stale-guards',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        // ---- 第 1 条：当前文件那一行滚出可见范围后重扫出 v2，查看器跟着换成 v2，按 A 审的是 v2 ----
        tempStale = await makeTemp(ctx, '滚出视野重扫', 30)
        const f1 = tempStale.files[0]
        await ctx.rev1('POST', `/api/files/${f1.file_id}/review`, { result: 'rejected', reject_reasons: ['blur'], comment: '截图脚本：先驳回' })
        await openReview(page, tempStale.id, { tab: 'all' })
        await clickRow(page, f1.file_id)
        await waitReady(page, f1.file_id)
        await page.$eval('[data-testid=review-list]', (l) => (l.scrollTop = l.scrollHeight))
        await sleep(400)
        assert(!(await page.$(`[data-testid=review-row][data-file-id="${f1.file_id}"]`)), '当前文件那一行应该已经滚出可见范围')
        const rs = await ctx.scanner('POST', `/api/files/${f1.file_id}/rescan`, pageForm(f1.file_name, makeScanPage({ seed: `M2-6 滚出视野 ${stamp()}`, pageNo: 1, dpi: 300 })))
        const t0 = Date.now()
        await page.waitForSelector(`[data-testid=image-viewer][data-file-id="${f1.file_id}"][data-snapshot-id="${rs.snapshot_id}"]`, { timeout: 5000 })
        await waitReady(page, f1.file_id)
        const msNew = Date.now() - t0
        await rest(page)
        await page.keyboard.press('a')
        let r = null
        for (let i = 0; i < 30 && r?.review_status !== 'approved'; i++) {
          await sleep(200)
          r = await ctx.mgr('GET', `/api/files/${f1.file_id}/reviews`)
        }
        assert(r.review_status === 'approved' && r.reviews.at(-1).snapshot_id === rs.snapshot_id, `按 A 之后 ${r.review_status}，审的快照 ${r.reviews.at(-1).snapshot_id}（v2 是 ${rs.snapshot_id}）`)
        console.log(`  ✓ 第 1 条：那一行滚出视野后重扫，查看器 ${msNew} ms 内换成 v2，按 A 审的是 v2`)

        // ---- 第 2 条：最后一张待审驳回后重扫回来（同一个文件、新的一行分配），驳回面板是空的，Enter 不会再驳回 ----
        tempStale2 = await makeTemp(ctx, '驳回面板清空', 2)
        const [g1, g2] = tempStale2.files
        await ctx.rev1('POST', `/api/files/${g1.file_id}/review`, { result: 'approved' })
        await openReview(page, tempStale2.id, { tab: 'open' })
        await waitReady(page, g2.file_id)
        await rest(page)
        await page.keyboard.press('r')
        await page.waitForSelector('[data-testid=review-actions][data-panel=open]', { timeout: 3000 })
        await page.keyboard.press('1')
        await page.keyboard.press('Tab')
        await page.keyboard.type('第一次驳回时写的说明')
        await page.keyboard.press('Enter')
        await waitToast(page, '都审完了')
        assert((await current(page)) === g2.file_id, '没有下一张待审，应该停在第 2 页')
        await page.waitForSelector(`[data-testid=review-row][data-file-id="${g2.file_id}"][data-result=rejected]`, { timeout: 3000 })
        const rs2 = await ctx.scanner('POST', `/api/files/${g2.file_id}/rescan`, pageForm(g2.file_name, makeScanPage({ seed: `M2-6 面板清空 ${stamp()}`, pageNo: 2, dpi: 300 })))
        await page.waitForSelector(`[data-testid=review-row][data-file-id="${g2.file_id}"][data-status=open][data-round="2"]`, { timeout: 5000 })
        await page.waitForSelector(`[data-testid=image-viewer][data-snapshot-id="${rs2.snapshot_id}"]`, { timeout: 5000 })
        await waitReady(page, g2.file_id)
        await rest(page)
        await page.keyboard.press('r')
        await page.waitForSelector('[data-testid=review-actions][data-panel=open]', { timeout: 3000 })
        const checked = await page.$$eval('[data-testid=reject-reason][data-checked=true]', (els) => els.map((e) => e.dataset.code))
        const comment = await page.$eval('[data-testid=reject-comment]', (e) => e.value)
        assert(checked.length === 0 && comment === '', `重扫回来后面板里还留着上一轮的：原因 ${checked}，说明「${comment}」`)
        await page.keyboard.press('Enter')
        await sleep(800)
        const r2 = await ctx.mgr('GET', `/api/files/${g2.file_id}/reviews`)
        assert(r2.review_status === 'pending', `在空面板上按 Enter 之后文件是 ${r2.review_status}`)
        await page.keyboard.press('Escape')
        console.log('  ✓ 第 2 条：驳回后重扫回来，面板是空的，直接按 Enter 被拦下，没有再驳回')

        // ---- 第 4 条：同位切换正在看左边（上一轮）时不能审；并排时第一次按 V 直接显示左边 ----
        await page.keyboard.press('c')
        await page.waitForFunction(
          () => [...document.querySelectorAll('[data-testid=compare-pane] [data-testid=image-viewer]')].every((v) => v.dataset.state === 'ready'),
          { timeout: 20000 },
        )
        await page.waitForSelector('[data-testid=review-actions][data-can-act=true]', { timeout: 5000 })
        await page.keyboard.press('v')
        await page.waitForSelector('[data-testid=compare-viewer][data-mode=toggle][data-showing=left]', { timeout: 3000 })
        await page.waitForSelector('[data-testid=review-actions][data-can-act=false]', { timeout: 3000 })
        await page.waitForSelector('[data-testid=review-showing-old]', { timeout: 3000 })
        const hint = await page.$eval('[data-testid=review-blocked]', (e) => e.textContent ?? '')
        assert(hint.includes('按 V 切回'), `看左边时的提示：${hint}`)
        await page.mouse.move(5, 895)
        await sleep(300)
        await page.screenshot({ path: path.join(OUT_DIR, 'm2-review-showing-old.png') })
        await page.keyboard.press('a')
        await sleep(800)
        assert((await ctx.mgr('GET', `/api/files/${g2.file_id}/reviews`)).review_status === 'pending', '看着上一轮的版本按 A 不该通过')
        await page.keyboard.press('v')
        await page.waitForSelector('[data-testid=compare-viewer][data-mode=toggle][data-showing=right]', { timeout: 3000 })
        await page.waitForSelector('[data-testid=review-actions][data-can-act=true]', { timeout: 5000 })
        await page.keyboard.press('a')
        let r3 = null
        for (let i = 0; i < 30 && r3?.review_status !== 'approved'; i++) {
          await sleep(200)
          r3 = await ctx.mgr('GET', `/api/files/${g2.file_id}/reviews`)
        }
        assert(r3.review_status === 'approved' && r3.reviews.at(-1).snapshot_id === rs2.snapshot_id, `切回当前版本按 A 之后 ${r3.review_status}`)
        await rest(page)
        console.log('  ✓ 第 4 条：并排时按 V 直接显示上一轮，这时 A 被拦下（提示按 V 切回）；切回当前版本后 A 通过的是 v2')
      },
      async after() {
        await dropTemp(tempStale)
        await dropTemp(tempStale2)
      },
    },
    {
      // 验收第 1 轮的第 3 条：审的必须是看到的那一版（确认框那一张中途另存 m2-review-cover-confirm.png）。
      // 用 CDP 在响应阶段改 batch 的结果来造三种情况（不动真实存储）：P_01 两个地址都为 null（原图、封面都看不到）；
      // P_02 原图地址连不上（加载失败、封面照常）；P_03 宽高改成 2 万 × 2 万（超出浏览器预算，只显示封面）。
      name: 'm2-review-unseen',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        tempUnseen = await makeTemp(ctx, '没看到这一版', 4)
        const [u1, u2, u3, u4] = tempUnseen.files
        unseenCdp = await page.createCDPSession()
        await unseenCdp.send('Fetch.enable', { patterns: [{ urlPattern: '*/api/files/batch*', requestStage: 'Response' }] })
        unseenCdp.on('Fetch.requestPaused', async (ev) => {
          try {
            const { body, base64Encoded } = await unseenCdp.send('Fetch.getResponseBody', { requestId: ev.requestId })
            const data = JSON.parse(base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body)
            for (const it of data.items ?? []) {
              if (it.file_id === u1.file_id) Object.assign(it, { original_url: null, cover_url: null, url_error: { code: 'storage_unavailable', message: '存储已停用（截图模拟）' } })
              if (it.file_id === u2.file_id) it.original_url = 'http://127.0.0.1:9/m2-6-unreachable.png'
              if (it.file_id === u3.file_id) Object.assign(it, { width_px: 20000, height_px: 20000 })
            }
            await unseenCdp.send('Fetch.fulfillRequest', {
              requestId: ev.requestId,
              responseCode: ev.responseStatusCode ?? 200,
              responseHeaders: (ev.responseHeaders ?? []).filter((h) => h.name.toLowerCase() !== 'content-length'),
              body: Buffer.from(JSON.stringify(data)).toString('base64'),
            })
          } catch {
            await unseenCdp.send('Fetch.continueRequest', { requestId: ev.requestId }).catch(() => {})
          }
        })
        await page.goto(`${ctx.origin}/review?folder=${tempUnseen.id}`, { waitUntil: IDLE })
        await page.waitForSelector('[data-testid=review-page][data-list=ready][data-stream=open]', { timeout: 15000 })
        await rest(page)
        // P_01：原图、封面都没显示出来：不能审。
        await page.waitForSelector(`[data-testid=review-page][data-current="${u1.file_id}"][data-seen=none]`, { timeout: 10000 })
        await page.waitForSelector('[data-testid=review-actions][data-can-act=false]', { timeout: 3000 })
        const hint1 = await page.$eval('[data-testid=review-blocked]', (e) => e.textContent ?? '')
        assert(hint1.includes('没看到这一版'), `原图、封面都没显示时的提示：${hint1}`)
        await page.keyboard.press('a')
        await page.keyboard.press('r')
        await sleep(800)
        assert((await ctx.mgr('GET', `/api/files/${u1.file_id}/reviews`)).review_status === 'pending', '原图、封面都没显示出来时不该能审')
        assert(!(await page.$('[data-testid=review-actions][data-panel=open]')), '原图、封面都没显示出来时 R 不该打开面板')
        console.log('  ✓ 原图、封面都没显示出来：A、R 都不响应，提示"没看到这一版"')
        // P_02：原图加载失败、封面显示了：先确认。
        await clickRow(page, u2.file_id)
        await page.waitForSelector(`[data-testid=review-page][data-current="${u2.file_id}"][data-seen=cover-confirm]`, { timeout: 10000 })
        await page.waitForSelector('[data-testid=review-cover-only][data-seen=cover-confirm]', { timeout: 3000 })
        await rest(page)
        await page.keyboard.press('a')
        await page.waitForSelector('[role=alertdialog]', { visible: true, timeout: 5000 })
        await sleep(300)
        const dlg = await page.$eval('[role=alertdialog]', (el) => el.textContent ?? '')
        assert(dlg.includes('只看了封面'), `确认框文字：${dlg}`)
        await page.mouse.move(5, 895)
        await page.screenshot({ path: path.join(OUT_DIR, 'm2-review-cover-confirm.png') })
        await page.keyboard.press('Escape')
        await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 5000 })
        await sleep(300)
        assert((await ctx.mgr('GET', `/api/files/${u2.file_id}/reviews`)).review_status === 'pending', '确认框取消后不该通过')
        await page.keyboard.press('a')
        await page.waitForSelector('[role=alertdialog]', { visible: true, timeout: 5000 })
        await sleep(250)
        const buttons = await page.$$('[role=alertdialog] button')
        await buttons[buttons.length - 1].click()
        await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 5000 })
        let r2 = null
        for (let i = 0; i < 30 && r2?.review_status !== 'approved'; i++) {
          await sleep(200)
          r2 = await ctx.mgr('GET', `/api/files/${u2.file_id}/reviews`)
        }
        assert(r2.review_status === 'approved', `确认后应该通过，实际 ${r2.review_status}`)
        console.log('  ✓ 原图加载失败、只显示了封面：按 A 先弹"只看了封面"确认框，取消不通过，确认后通过')
        // P_03：原图太大（超出浏览器预算），只显示封面：可以直接审，画面上标出"只看了封面"。
        await page.waitForSelector(`[data-testid=review-page][data-current="${u3.file_id}"][data-seen=cover]`, { timeout: 10000 })
        await page.waitForSelector('[data-testid=review-cover-only][data-seen=cover]', { timeout: 3000 })
        await rest(page)
        await page.keyboard.press('a')
        let r3 = null
        for (let i = 0; i < 30 && r3?.review_status !== 'approved'; i++) {
          await sleep(200)
          r3 = await ctx.mgr('GET', `/api/files/${u3.file_id}/reviews`)
        }
        assert(r3.review_status === 'approved', `原图太大只看封面时按 A 应该直接通过，实际 ${r3.review_status}`)
        assert(!(await page.$('[role=alertdialog]')), '原图太大只看封面时不该弹确认框')
        await waitReady(page, u4.file_id)
        // 截图：回到 P_02 那种"只看了封面"的样子（已通过，看标记）。
        await clickRow(page, u2.file_id)
        await page.waitForSelector('[data-testid=review-cover-only]', { timeout: 10000 })
        await waitNoToast(page)
        await rest(page)
        console.log('  ✓ 原图太大、只显示封面：A 直接通过（不弹确认框），画面上标出"只看了封面"')
      },
      async after() {
        if (unseenCdp) {
          await unseenCdp.send('Fetch.disable').catch(() => {})
          await unseenCdp.detach().catch(() => {})
          unseenCdp = null
        }
        await dropTemp(tempUnseen)
      },
    },
    {
      // 超过 500 个文件的事件只带 folder_ids（Q10）：按文件夹处理。上传 505 页约 20 秒。
      name: 'm2-review-big-event',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        const t0 = Date.now()
        tempBig = await makeTemp(ctx, '大事件', 505, { assign: false })
        console.log(`  上传 505 页用了 ${((Date.now() - t0) / 1000).toFixed(1)} 秒`)
        await page.goto(`${ctx.origin}/review?folder=${tempBig.id}`, { waitUntil: IDLE })
        await page.waitForSelector('[data-testid=review-page][data-list=ready][data-stream=open]', { timeout: 15000 })
        await page.waitForSelector('[data-testid=review-empty]', { timeout: 5000 })
        const listener = listenEvents(ctx.origin, await sessionCookie(ctx.origin, MANAGER, demoPassword), ctx.project.id)
        try {
          await listener.opened
          // 页面之外按文件夹分给 demo_reviewer：列表里还没有这个文件夹，靠核对计数发现、整体重拉。
          let t1 = Date.now()
          const res = await ctx.mgr('POST', `/api/projects/${ctx.project.id}/review-assignments`, { reviewer_id: ctx.id(REV1), folder_id: tempBig.id })
          assert(res.assigned === 505, `分配了 ${res.assigned} 个`)
          await page.waitForSelector('[data-testid=review-tab][data-state=open][data-count="505"]', { timeout: 5000 })
          await page.waitForSelector(`[data-testid=review-row][data-file-id="${tempBig.files[0].file_id}"]`, { timeout: 5000 })
          const msIn = Date.now() - t1
          let ev = listener.events.find((e) => e.type === 'files.changed' && e.folder_ids?.includes(tempBig.id))
          assert(ev && !ev.file_ids, `分配 505 个的事件应该只带 folder_ids：${JSON.stringify(ev)}`)
          await waitReady(page)
          // 第一页 500 个：滚到底自动加载下一页，第 505 个出现。
          const lastId = tempBig.files[504].file_id
          await page.waitForFunction(
            (fid) => {
              const l = document.querySelector('[data-testid=review-list]')
              if (l) l.scrollTop = l.scrollHeight
              return !!document.querySelector(`[data-testid=review-row][data-file-id="${fid}"]`)
            },
            { timeout: 8000, polling: 200 },
            lastId,
          )
          await page.$eval('[data-testid=review-list]', (l) => (l.scrollTop = 0))
          // 按子树改派给 demo_reviewer2：列表里有这个文件夹，按文件夹整体重拉，全部消失。
          listener.events.length = 0
          t1 = Date.now()
          const re = await ctx.mgr('POST', `/api/projects/${ctx.project.id}/review-assignments:reassign`, {
            to_reviewer_id: ctx.id(REV2),
            from_reviewer_id: ctx.id(REV1),
            folder_id: tempBig.id,
          })
          assert(re.reassigned === 505, `改派了 ${re.reassigned} 个`)
          await page.waitForSelector('[data-testid=review-empty]', { timeout: 3000 })
          const msOut = Date.now() - t1
          ev = listener.events.find((e) => e.type === 'files.changed' && e.folder_ids?.includes(tempBig.id))
          assert(ev && !ev.file_ids, `改派 505 个的事件应该只带 folder_ids：${JSON.stringify(ev)}`)
          const tabs = await page.$$eval('[data-testid=review-tab]', (els) => Object.fromEntries(els.map((e) => [e.dataset.state, Number(e.dataset.count)])))
          assert(tabs.all === 0, `改派走之后的计数 ${JSON.stringify(tabs)}`)
          console.log(`  ✓ 505 个文件的事件只带 folder_ids：分给我的 ${msIn} ms 内出现（核对计数后整体重拉，滚到底加载第 2 页），按子树改派走的 ${msOut} ms 内消失`)
        } finally {
          await listener.stop()
        }
        await waitNoToast(page)
      },
      async after() {
        await dropTemp(tempBig)
      },
    },
    {
      // 确认框那一张在中途另存（m2-review-batch-confirm.png），这一张是批量通过、撤回其中一个之后的样子。
      name: 'm2-review-batch-done',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        tempBatch = await makeTemp(ctx, '批量通过', 7)
        const files = tempBatch.files
        // 第 3 页先用接口通过（选中的里有一个不是待审）。
        await ctx.rev1('POST', `/api/files/${files[2].file_id}/review`, { result: 'approved' })
        await openReview(page, tempBatch.id, { tab: 'all' })
        await clickRow(page, files[0].file_id)
        await clickRow(page, files[3].file_id, ['Shift'])
        await clickRow(page, files[5].file_id, ['Meta'])
        await page.waitForSelector('[data-testid=batch-bar][data-selected="5"][data-eligible="4"]', { timeout: 3000 })
        await page.click('[data-testid=batch-approve]')
        await page.waitForSelector('[role=alertdialog]', { visible: true, timeout: 5000 })
        await sleep(300)
        const text = await page.$eval('[role=alertdialog]', (el) => el.textContent ?? '')
        assert(text.includes('批量通过 4 个文件') && text.includes('另有 1 个不是待审'), `确认框文字：${text}`)
        await page.mouse.move(5, 895)
        await page.screenshot({ path: path.join(OUT_DIR, 'm2-review-batch-confirm.png') })
        const buttons = await page.$$('[role=alertdialog] button')
        await buttons[buttons.length - 1].click()
        await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 10000 })
        await waitToast(page, '已批量通过 4 个')
        const picked = [0, 1, 3, 5].map((i) => files[i])
        for (const f of picked) {
          const r = await ctx.mgr('GET', `/api/files/${f.file_id}/reviews`)
          assert(r.review_status === 'approved', `${f.file_name} 是 ${r.review_status}`)
          const log = (await ctx.mgr('GET', `/api/files/${f.file_id}/logs`)).logs[0]
          assert(log.action === 'approve' && log.detail.batch === true, `${f.file_name} 的日志 ${JSON.stringify(log)}`)
        }
        for (const i of [4, 6]) assert((await ctx.mgr('GET', `/api/files/${files[i].file_id}/reviews`)).review_status === 'pending', `${files[i].file_name} 不该被通过`)
        // 撤回其中一个的通过（Q8）。
        await clickRow(page, files[1].file_id)
        await page.waitForSelector('[data-testid=review-reopen]', { timeout: 5000 })
        await page.click('[data-testid=review-reopen]')
        await page.waitForSelector('[role=alertdialog]', { visible: true, timeout: 5000 })
        await sleep(250)
        const rb = await page.$$('[role=alertdialog] button')
        await rb[rb.length - 1].click()
        await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 10000 })
        await page.waitForSelector(`[data-testid=review-row][data-file-id="${files[1].file_id}"][data-status=open]`, { timeout: 5000 })
        const ro = await ctx.mgr('GET', `/api/files/${files[1].file_id}/reviews`)
        assert(ro.review_status === 'pending' && ro.reviews.at(-1).status === 'open' && ro.reviews.at(-1).reviewer_id === ctx.id(REV1), '撤回通过之后应该回到 demo_reviewer 的待审')
        await waitReady(page, files[1].file_id)
        await waitNoToast(page)
        await rest(page)
        console.log('  ✓ 批量通过：确认框写"4 个"和"另有 1 个不是待审"，接口核对 4 个通过、日志 batch:true；撤回其中一个回到待审')
      },
      async after() {
        await dropTemp(tempBatch)
      },
    },
    {
      // Q12：待办跟随项目切换，页头提示其他项目的待审数。在 DEMO-B 根下建临时文件夹、传 2 页分给 demo_reviewer。
      // 弹出层那一张中途另存（m2-review-other-projects.png），这一张是点它切到 DEMO-B 之后的样子。
      name: 'm2-review-other-projects-switched',
      user: REV1,
      path: '/review',
      waitFor: '[data-testid=review-page]',
      async prepare(page) {
        const ctx = await context(page)
        const projects = await ctx.admin('GET', '/api/projects')
        const other = projects.find((p) => p.project_code === 'DEMO-B')
        assert(other, '找不到项目 DEMO-B')
        const top = await ctx.admin('POST', '/api/folders', {
          parent_id: other.root_folder_id,
          folder_name: `截图验证 M2-6 其他项目 ${stamp()}`,
          remark: 'M2-6 截图脚本临时建的，做完会删掉',
        })
        tempOther = { id: top.id, name: top.folder_name }
        const ids = []
        for (let i = 1; i <= 2; i++) {
          const r = await ctx.admin('POST', `/api/folders/${top.id}/files`, pageForm(`B_${i}.png`, makeScanPage({ seed: `M2-6 other ${stamp()} ${i}`, pageNo: i })))
          ids.push(r.file_id)
        }
        await ctx.admin('POST', `/api/projects/${other.id}/review-assignments`, { reviewer_id: ctx.id(REV1), file_ids: ids })
        const mine = await ctx.rev1('GET', `/api/me/reviews?project_id=${ctx.project.id}&limit=1`)
        await openReview(page, ctx.demo.id, { tab: 'open' })
        await page.waitForSelector(`[data-testid=other-projects][data-count="${mine.other_projects_open}"]`, { timeout: 5000 })
        assert(mine.other_projects.some((p) => p.project_id === other.id && p.open >= 2), `接口的 other_projects ${JSON.stringify(mine.other_projects)}`)
        await page.click('[data-testid=other-projects]')
        await page.waitForSelector('[data-testid=other-project]', { visible: true, timeout: 3000 })
        await sleep(300)
        await page.screenshot({ path: path.join(OUT_DIR, 'm2-review-other-projects.png') })
        // 点 DEMO-B：切过去，范围（DEMO-A 的文件夹）清掉，列表换成 DEMO-B 的。
        const entries = await page.$$('[data-testid=other-project]')
        let clicked = false
        for (const el of entries) {
          if ((await el.evaluate((e) => e.textContent ?? '')).includes('DEMO-B')) {
            await el.click()
            clicked = true
            break
          }
        }
        assert(clicked, '弹出层里没有 DEMO-B')
        await page.waitForFunction(() => !location.search.includes('folder='), { timeout: 5000 })
        await page.waitForSelector(`[data-testid=review-row][data-file-id="${ids[0]}"]`, { timeout: 8000 })
        await waitReady(page)
        await rest(page)
        console.log(`  ✓ 页头"其他项目还有 ${mine.other_projects_open} 个待审"；点 DEMO-B 切过去，待办换成 DEMO-B 的`)
      },
      async after(page) {
        // 下次打开时回到 DEMO-A（项目切换器记在 localStorage 里）。
        if (ctxPromise) {
          const ctx = await ctxPromise
          await page.evaluate((k, v) => localStorage.setItem(k, v), `doclite.lastProject.${ctx.id(REV1)}`, ctx.project.id).catch(() => {})
        }
        await dropTemp(tempOther)
      },
    },
    // ---------- 壳（IPC 和 img 协议模拟） ----------
    shellShot('shell-review', async (page, { ctx }) => {
      await shellOpenTwice(page, ctx)
      // 打开时自动看第一张待审的（盒 01 第 3 页）。
      const f = demoFile(ctx, '盒 01/IMG_0003.png')
      await waitReady(page, f.file_id, ['ready'], 30000)
      const mode = await page.$eval('[data-testid=image-viewer]', (e) => e.dataset.mode)
      assert(mode === 'tiles', `壳里应该用分块，实际 ${mode}`)
      const next = demoFile(ctx, '盒 01/IMG_0004.png')
      await page.waitForSelector(`[data-testid=review-page][data-prepared="${next.file_id}"]`, { timeout: 5000 })
      const calls = await page.evaluate(() => window.__prepareCalls)
      assert(calls.some((c) => c.fileId === next.file_id), `没有给下一张调 prepare_tiles：${JSON.stringify(calls)}`)
      assert(!calls.some((c) => c.fileId === f.file_id), '不该给当前这张调 prepare_tiles')
      await rest(page)
      console.log(`  ✓ 壳里用分块显示；当前这张画完后给下一张（${next.file_name}）调了 prepare_tiles（共 ${calls.length} 次）`)
    }),
    shellShot('shell-review-compare', async (page, { ctx }) => {
      await shellOpenTwice(page, ctx)
      const f = demoFile(ctx, `盒 02/${ROUND2[1].name}`)
      await clickRow(page, f.file_id)
      await waitReady(page, f.file_id, ['ready'], 30000)
      await page.waitForSelector('[data-testid=prev-reject]', { timeout: 5000 })
      await rest(page)
      await page.keyboard.press('c')
      await page.waitForFunction(
        () => {
          const v = [...document.querySelectorAll('[data-testid=compare-pane] [data-testid=image-viewer]')]
          return v.length === 2 && v.every((x) => x.dataset.state === 'ready' && x.dataset.mode === 'tiles')
        },
        { timeout: 30000 },
      )
      const left = await page.$eval('[data-testid=compare-pane][data-side=left] [data-testid=compare-label]', (e) => e.textContent ?? '')
      assert(left.includes('v1') && left.includes('倾斜') && left.includes('其他'), `左侧角标：${left}`)
      await rest(page)
      console.log(`  ✓ 壳里 C 对比：两侧都是分块，左侧 ${left.replace(/\s+/g, ' ').trim()}`)
    }),
  ],
}

/**
 * 壳里打开审核台（范围是演示文件夹）。截图模拟的来源记录是 Node 这边收到 batch、snapshots 响应后异步记下的（真壳在
 * Rust 代理里转发响应之前就记好了），页面一打开就要的封面、分块描述可能抢在记下之前（404，显示"加载失败"）。所以先打开
 * 一次让模拟记下这些快照，再重新打开（快照不变，第二次都在记录里）。
 */
async function shellOpenTwice(page, ctx) {
  for (let i = 0; i < 2; i++) {
    await page.goto(`${ctx.origin}/review?folder=${ctx.demo.id}`, { waitUntil: IDLE })
    await page.waitForSelector('[data-testid=review-page][data-list=ready][data-stream=open]', { timeout: 15000 })
    await page.waitForFunction(
      () => [...document.querySelectorAll('[data-testid=file-cover]')].every((c) => !['loading', 'loading-image'].includes(c.dataset.coverState)),
      { timeout: 15000 },
    )
    await sleep(300)
  }
}

/** 壳里的一张：注入 IPC、拦截 img 协议（同 m2-viewer.mjs），记下 prepare_tiles 的调用，以 demo_reviewer 登录。 */
function shellShot(name, run) {
  return {
    name,
    path: '/login',
    waitFor: '#username',
    async init(page) {
      await installShellMock(page, { cacheDir: shotsTmp('m2-review-shell') })
      // 在 M2-4 的模拟之上：记下 prepare_tiles 的调用；api_stream（实时事件）转给 preview 的 /api 代理，逐块经 Channel
      // 的 onmessage 推给页面（open → chunk… → end，同 lib/shell.ts 的 ShellStreamEvent）。
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
      await loginPage(page, origin, REV1, demoPassword)
      await page.waitForSelector('[data-testid=side-nav]')
      const ctx = await context(page)
      await run(page, { ctx })
    },
    delay: 400,
  }
}
