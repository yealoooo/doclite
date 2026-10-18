// M3-7 的截图清单和断言：归类信息（只读）——/review 右栏、/files 的详情面板（用户决定 Q10：只给档号、题名、第几页）。
//
//   INIT_ADMIN_PASSWORD=admin123456 \
//     npm run screenshots -- --spec scripts/shots/m3-catalog-info.mjs --base http://localhost:<preview 端口>
//
// 要先有 npm run seed 的演示数据（M9-3）：DEMO-A「2025 文书档案 / 盒 WS-2025-001」的第 2、3 个子文件夹已归档为
// Z001-WS·2025-Y-BGS-0002、0003（未锁定），第 4 个没归档、有待审的页；demo_reviewer 能进 DEMO-A。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - m3-review-uncataloged：demo_reviewer 打开第 4 个子文件夹的待审（IMG_0004，没归档）→ 右栏"归类信息（只读）"写
//   "未归档"，和接口 GET /api/files/{fid}/catalog 一致。
// - m3-review-catalog-info：第 2 个子文件夹切到"全部"、点 IMG_0002（Z001-WS·2025-Y-BGS-0002 第 2 页）→ 档号、第几页、
//   题名和接口一致；页面之外经接口改题名（PATCH，发 catalog.changed）→ 3 秒内右栏的题名跟着变；最后改回去（after 里也改回去）。
// - m3-files-catalog-info：admin 在 /files 打开第 3 个子文件夹、选 IMG_0002 → 档号 Z001-WS·2025-Y-BGS-0003、第 2 页、题名
//   和接口一致；文件日志里"归档"的说明是"挂到 Z001-WS·2025-Y-BGS-0003 第 2 页 · 题名：… · v1 · 当时审核：通过"
//   （logSummary）。同样页面之外改题名，3 秒内详情面板跟着变。
// - m3-files-uncataloged：盒 002 里没归档的 IMG_0001 → "未归档"。（M9-3 起演示数据里没有档号为空的条目，原来
//   "第 1 件 第 1 页（还没填题名）"那一张去掉了。）

import { apiLogin } from '../lib/api.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'
const REVIEWER = 'demo_reviewer'
const IDLE = 'networkidle2'
const OUT = '../docs/screenshots/m3'
const LIVE_MS = 3000

let ctxPromise = null
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const adminApi = await apiLogin(origin, admin, adminPassword)
    const project = (await adminApi('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-A')
    if (!project) throw new Error('找不到 DEMO-A，先跑 npm run seed')
    const child = async (id, name) => {
      const f = (await adminApi('GET', `/api/folders/${id}/children`)).folders.find((x) => x.folder_name === name)
      if (!f) throw new Error(`找不到文件夹「${name}」，先跑 npm run seed`)
      return f
    }
    const top = await child(project.root_folder_id, '2025 文书档案')
    const box1 = await child(top.id, '盒 WS-2025-001')
    const box2 = await child(top.id, '盒 WS-2025-002')
    const folders = {
      s2: await child(box1.id, '办公室2024年度工作总结'),
      s3: await child(box1.id, '关于调整2025年春节值班安排的通知'),
      s4: await child(box1.id, '2025年第一季度办公会议纪要'),
      b2: await child(box2.id, '差旅费报销管理办法'),
    }
    const files = {}
    for (const [k, f] of Object.entries(folders)) files[k] = (await adminApi('GET', `/api/folders/${f.id}/children`)).files
    const rev = await apiLogin(origin, REVIEWER, demoPassword)
    const byName = (list, name) => {
      const f = list.find((x) => x.file_name === name)
      if (!f) throw new Error(`演示文件夹里找不到 ${name}`)
      return f
    }
    return { origin, admin: adminApi, rev, demo: { project, folders, files }, pid: project.id, byName }
  })()
  return ctxPromise
}

function assert(cond, message) {
  if (!cond) throw new Error(`断言失败：${message}`)
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

/** 页面上的归类信息（data-* 和文字）。 */
function readInfo(page, scope = 'body') {
  return page.evaluate((s) => {
    const el = document.querySelector(`${s} [data-testid=catalog-info]`)
    if (!el) return null
    return {
      state: el.dataset.state,
      placed: el.dataset.placed,
      no: el.dataset.archiveNo ?? null,
      page: el.dataset.pageNo ? Number(el.dataset.pageNo) : null,
      itemId: el.dataset.itemId ?? null,
      title: el.querySelector('[data-testid=catalog-info-title]')?.textContent?.trim() ?? null,
      text: el.textContent.replace(/\s+/g, ' ').trim(),
    }
  }, scope)
}

/** 等归类信息和接口一致（placement 为 null 时应为"未归档"）。返回用了多少毫秒。 */
async function waitInfoMatches(page, call, fileId, { timeout = 5000, scope = 'body' } = {}) {
  const t0 = Date.now()
  let want = null
  let wantAt = 0
  let info = null
  while (true) {
    if (!want || Date.now() - wantAt > 500) {
      want = (await call('GET', `/api/files/${fileId}/catalog`)).placement
      wantAt = Date.now()
    }
    info = await readInfo(page, scope)
    if (info?.state === 'ready') {
      if (want === null && info.placed === 'false' && info.text.includes('未归档')) return Date.now() - t0
      if (
        want &&
        info.placed === 'true' &&
        info.no === (want.archive_no ?? `第 ${want.item_no} 件`) &&
        info.page === want.page_no &&
        info.itemId === want.item_id &&
        info.title === (want.title || '（还没填题名）')
      )
        return Date.now() - t0
    }
    if (Date.now() - t0 > timeout) {
      throw new Error(`${timeout} ms 内归类信息没有和接口一致：页面 ${JSON.stringify(info)}，接口 ${JSON.stringify(want)}`)
    }
    await sleep(100)
  }
}

// ---------- 页面之外经接口改题名（PATCH 自己发 catalog.changed） ----------

const originalTitles = new Map()
async function patchTitle(call, itemId, title) {
  const it = await call('GET', `/api/archive-items/${itemId}`)
  if (!originalTitles.has(itemId)) originalTitles.set(itemId, it.title)
  await call('PATCH', `/api/archive-items/${itemId}`, { row_version: it.row_version, title })
}
async function changeTitle(ctx, itemId, title) {
  await patchTitle(ctx.admin, itemId, title)
}

async function restoreTitles() {
  if (!ctxPromise || originalTitles.size === 0) return
  const ctx = await ctxPromise
  for (const [id, title] of [...originalTitles]) await patchTitle(ctx.admin, id, title)
  originalTitles.clear()
  console.log('  已把题名改回去')
}

/** 文件所在的条目（接口的归类信息）。 */
async function itemOf(ctx, fileId) {
  const p = (await ctx.admin('GET', `/api/files/${fileId}/catalog`)).placement
  if (!p) throw new Error(`${fileId} 没有归档`)
  return p
}

async function openReview(page, folderId) {
  const ctx = await context(page)
  await page.goto(`${ctx.origin}/review?folder=${folderId}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=review-page][data-list=ready][data-stream=open]', { timeout: 15000 })
}

async function openFiles(page, folderId, fileName) {
  const ctx = await context(page)
  await page.goto(`${ctx.origin}/files?folder=${folderId}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=file-cell]', { timeout: 15000 })
  await page.waitForFunction(() => document.querySelector('[data-stream]')?.dataset.stream === 'open', { timeout: 15000 })
  const cells = await page.$$('[data-testid=file-cell]')
  for (const c of cells) {
    if ((await c.evaluate((el) => el.textContent)).includes(fileName)) {
      await c.click()
      await page.waitForFunction((n) => document.querySelector('[data-testid=detail-file-name]')?.textContent?.trim() === n, { timeout: 5000 }, fileName)
      return
    }
  }
  throw new Error(`文件目录里找不到 ${fileName}`)
}

export default {
  out: OUT,
  accounts: {
    [admin]: adminPassword,
    [REVIEWER]: demoPassword,
  },
  waitUntil: IDLE,
  shots: [
    {
      name: 'm3-review-uncataloged',
      user: REVIEWER,
      path: '/review',
      prepare: async (page) => {
        const ctx = await context(page)
        await openReview(page, ctx.demo.folders.s4.id)
        const f = ctx.byName(ctx.demo.files.s4, 'IMG_0004.png')
        await page.waitForFunction((fid) => document.querySelector('[data-testid=review-page]')?.dataset.current === fid, { timeout: 10000 }, f.file_id)
        const ms = await waitInfoMatches(page, ctx.rev, f.file_id, { scope: '[data-testid=review-right]' })
        console.log(`  IMG_0004（待审、没归档）：右栏"未归档"，和接口一致（${ms} ms）`)
      },
    },
    {
      name: 'm3-review-catalog-info',
      user: REVIEWER,
      path: '/review',
      prepare: async (page) => {
        const ctx = await context(page)
        await openReview(page, ctx.demo.folders.s2.id)
        await page.click('[data-testid=review-tab][data-state=all]')
        await page.waitForSelector('[data-testid=review-tab][data-state=all][aria-checked=true]', { timeout: 3000 })
        await page.waitForSelector('[data-testid=review-page][data-list=ready]', { timeout: 10000 })
        const f = ctx.byName(ctx.demo.files.s2, 'IMG_0002.png')
        await page.click(`[data-testid=review-row][data-file-id="${f.file_id}"]`)
        await page.waitForFunction((fid) => document.querySelector('[data-testid=review-page]')?.dataset.current === fid, { timeout: 10000 }, f.file_id)
        let ms = await waitInfoMatches(page, ctx.rev, f.file_id, { scope: '[data-testid=review-right]' })
        const info = await readInfo(page, '[data-testid=review-right]')
        assert(info.no === 'Z001-WS·2025-Y-BGS-0002' && info.page === 2, `IMG_0002 应在 Z001-WS·2025-Y-BGS-0002 第 2 页，页面是 ${JSON.stringify(info)}`)
        console.log(`  IMG_0002：${info.no} 第 ${info.page} 页「${info.title}」，和接口一致（${ms} ms）`)

        const item = await itemOf(ctx, f.file_id)
        await changeTitle(ctx, item.item_id, '办公室2024年度工作总结（改题名验证）')
        ms = await waitInfoMatches(page, ctx.rev, f.file_id, { timeout: LIVE_MS, scope: '[data-testid=review-right]' })
        console.log(`  页面之外改题名 + catalog.changed：${ms} ms 后右栏的题名跟着变`)
        await restoreTitles()
        ms = await waitInfoMatches(page, ctx.rev, f.file_id, { timeout: LIVE_MS, scope: '[data-testid=review-right]' })
        console.log(`  改回去：${ms} ms`)
      },
      after: restoreTitles,
    },
    {
      name: 'm3-files-catalog-info',
      user: admin,
      path: '/files',
      prepare: async (page) => {
        const ctx = await context(page)
        await openFiles(page, ctx.demo.folders.s3.id, 'IMG_0002.png')
        const f = ctx.byName(ctx.demo.files.s3, 'IMG_0002.png')
        let ms = await waitInfoMatches(page, ctx.admin, f.file_id, { scope: '[data-testid=detail-panel]' })
        const info = await readInfo(page, '[data-testid=detail-panel]')
        assert(info.no === 'Z001-WS·2025-Y-BGS-0003' && info.page === 2, `IMG_0002 应在 Z001-WS·2025-Y-BGS-0003 第 2 页，页面是 ${JSON.stringify(info)}`)
        console.log(`  IMG_0002：${info.no} 第 ${info.page} 页「${info.title}」，和接口一致（${ms} ms）`)

        await page.waitForSelector('[data-testid=file-logs] li', { timeout: 5000 })
        const logs = await page.$$eval('[data-testid=file-logs] li', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()))
        const link = logs.find((t) => t.startsWith('归档'))
        const want = '挂到 Z001-WS·2025-Y-BGS-0003 第 2 页 · 题名：关于调整2025年春节值班安排的通知 · v1 · 当时审核：通过'
        assert(link?.includes(want), `文件日志里"归档"的说明不对：${link}`)
        console.log(`  文件日志：${want}`)

        const item = await itemOf(ctx, f.file_id)
        await changeTitle(ctx, item.item_id, '关于调整2025年春节值班安排的通知（改题名验证）')
        ms = await waitInfoMatches(page, ctx.admin, f.file_id, { timeout: LIVE_MS, scope: '[data-testid=detail-panel]' })
        console.log(`  页面之外改题名 + catalog.changed：${ms} ms 后详情面板跟着变`)
        await restoreTitles()
        ms = await waitInfoMatches(page, ctx.admin, f.file_id, { timeout: LIVE_MS, scope: '[data-testid=detail-panel]' })
        console.log(`  改回去：${ms} ms`)
        await page.$eval('[data-testid=catalog-info]', (el) => el.scrollIntoView({ block: 'center' }))
      },
      after: restoreTitles,
    },
    {
      name: 'm3-files-uncataloged',
      user: admin,
      path: '/files',
      prepare: async (page) => {
        const ctx = await context(page)
        await openFiles(page, ctx.demo.folders.b2.id, 'IMG_0001.png')
        const f = ctx.byName(ctx.demo.files.b2, 'IMG_0001.png')
        await waitInfoMatches(page, ctx.admin, f.file_id, { scope: '[data-testid=detail-panel]' })
        console.log('  盒 002「差旅费报销管理办法」IMG_0001（没归档）："未归档"，和接口一致')
        await page.$eval('[data-testid=catalog-info]', (el) => el.scrollIntoView({ block: 'center' }))
      },
    },
  ],
}
