// M3-7 的截图清单和断言：归类信息（只读）——/review 右栏、/files 的详情面板（用户决定 Q10：只给档号、题名、第几页）。
//
//   INIT_ADMIN_PASSWORD=admin123456 \
//     npm run screenshots -- --spec scripts/shots/m3-catalog-info.mjs --base http://localhost:<preview 端口>
//
// 要先有 DEMO-A 的「M3-7 看板演示」演示数据（见 scripts/lib/m3-demo.mjs，第一次跑时自动补上审核、归类树、条目和挂接；
// 条目和挂接在 M3-2、M3-3 合入前直接写库，要本机 psql 和 DATABASE_URL）。demo_reviewer 能进 DEMO-A。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - m3-review-uncataloged：demo_reviewer 打开「盒 01」的待审（IMG_0017，没挂接）→ 右栏"归类信息（只读）"写"未归类"，
//   和接口 GET /api/files/{fid}/catalog 一致。
// - m3-review-catalog-info：切到"全部"、点 IMG_0008（挂在 Z109-WS·2025-Y-BGS-0002 第 2 页）→ 档号、第几页、题名和接口
//   一致；页面之外改题名（直接写库，M3-2 之前没有接口）并给全宗节点改名（经接口，发 catalog.changed）→ 3 秒内右栏的
//   题名跟着变；最后改回去（after 里也改回去）。
// - m3-files-catalog-info：admin 在 /files 打开「盒 01」、选 IMG_0003 → 档号 Z109-WS·2025-Y-BGS-0001、第 3 页、题名和接口
//   一致；文件日志里的"挂接"说明是"挂到 Z109-WS·2025-Y-BGS-0001 第 3 页 · 题名：… · v1 · 当时审核：通过"（logSummary）。
//   同样页面之外改题名 + 发 catalog.changed，3 秒内详情面板跟着变。
// - m3-files-catalog-info-item-no：「盒 03」的 IMG_0001 挂在档号为空的条目里 → 显示"第 1 件 第 1 页"和"（还没填题名）"；
//   IMG_0005 没挂接 → "未归类"。

import { apiLogin, ensureM37Demo, lit, M37_FONDS_NAME, runSql } from '../lib/m3-demo.mjs'

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
    const demo = await ensureM37Demo({ origin, admin: adminApi, demoPassword })
    const rev = await apiLogin(origin, REVIEWER, demoPassword)
    const byName = (list, name) => {
      const f = list.find((x) => x.file_name === name)
      if (!f) throw new Error(`演示文件夹里找不到 ${name}`)
      return f
    }
    return { origin, admin: adminApi, rev, demo, pid: demo.project.id, byName }
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

/** 等归类信息和接口一致（placement 为 null 时应为"未归类"）。返回用了多少毫秒。 */
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
      if (want === null && info.placed === 'false' && info.text.includes('未归类')) return Date.now() - t0
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

// ---------- 页面之外改题名 + 发 catalog.changed（M3-2 之前没有改著录的接口） ----------

const originalTitles = new Map()
async function changeTitle(ctx, itemId, title) {
  if (!originalTitles.has(itemId)) {
    originalTitles.set(itemId, runSql(`SELECT title FROM archive_items WHERE id = ${lit(itemId)};`).replace(/\n$/, ''))
  }
  runSql(`UPDATE archive_items SET title = ${lit(title)}, row_version = row_version + 1, updated_at = now() WHERE id = ${lit(itemId)};`)
  await poke(ctx)
}

/** 发一条 catalog.changed：给全宗节点改名再改回来（两次 PATCH 都发事件）。 */
async function poke(ctx) {
  const fonds = ctx.demo.categories.fonds
  await ctx.admin('PATCH', `/api/projects/${ctx.pid}/archive-categories/${fonds.id}`, { category_name: `${M37_FONDS_NAME}（改名验证）` })
  await ctx.admin('PATCH', `/api/projects/${ctx.pid}/archive-categories/${fonds.id}`, { category_name: M37_FONDS_NAME })
}

async function restoreTitles() {
  if (!ctxPromise || originalTitles.size === 0) return
  const ctx = await ctxPromise
  for (const [id, title] of originalTitles) runSql(`UPDATE archive_items SET title = ${lit(title)} WHERE id = ${lit(id)};`)
  originalTitles.clear()
  await poke(ctx)
  console.log('  已把题名改回去')
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
        await openReview(page, ctx.demo.folders.box1.id)
        const f = ctx.byName(ctx.demo.files.box1, 'IMG_0017.png')
        await page.waitForFunction((fid) => document.querySelector('[data-testid=review-page]')?.dataset.current === fid, { timeout: 10000 }, f.file_id)
        const ms = await waitInfoMatches(page, ctx.rev, f.file_id, { scope: '[data-testid=review-right]' })
        console.log(`  IMG_0017（待审、没挂接）：右栏"未归类"，和接口一致（${ms} ms）`)
      },
    },
    {
      name: 'm3-review-catalog-info',
      user: REVIEWER,
      path: '/review',
      prepare: async (page) => {
        const ctx = await context(page)
        await openReview(page, ctx.demo.folders.box1.id)
        await page.click('[data-testid=review-tab][data-state=all]')
        await page.waitForSelector('[data-testid=review-tab][data-state=all][aria-checked=true]', { timeout: 3000 })
        await page.waitForSelector('[data-testid=review-page][data-list=ready]', { timeout: 10000 })
        const f = ctx.byName(ctx.demo.files.box1, 'IMG_0008.png')
        await page.click(`[data-testid=review-row][data-file-id="${f.file_id}"]`)
        await page.waitForFunction((fid) => document.querySelector('[data-testid=review-page]')?.dataset.current === fid, { timeout: 10000 }, f.file_id)
        let ms = await waitInfoMatches(page, ctx.rev, f.file_id, { scope: '[data-testid=review-right]' })
        const info = await readInfo(page, '[data-testid=review-right]')
        assert(info.no === 'Z109-WS·2025-Y-BGS-0002' && info.page === 2, `IMG_0008 应在 Z109-WS·2025-Y-BGS-0002 第 2 页，页面是 ${JSON.stringify(info)}`)
        console.log(`  IMG_0008：${info.no} 第 ${info.page} 页「${info.title}」，和接口一致（${ms} ms）`)

        const item = ctx.demo.items.find((i) => i.archive_no === 'Z109-WS·2025-Y-BGS-0002')
        await changeTitle(ctx, item.id, '办公室 2025 年上半年工作总结（改题名验证）')
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
        await openFiles(page, ctx.demo.folders.box1.id, 'IMG_0003.png')
        const f = ctx.byName(ctx.demo.files.box1, 'IMG_0003.png')
        let ms = await waitInfoMatches(page, ctx.admin, f.file_id, { scope: '[data-testid=detail-panel]' })
        const info = await readInfo(page, '[data-testid=detail-panel]')
        assert(info.no === 'Z109-WS·2025-Y-BGS-0001' && info.page === 3, `IMG_0003 应在 Z109-WS·2025-Y-BGS-0001 第 3 页，页面是 ${JSON.stringify(info)}`)
        console.log(`  IMG_0003：${info.no} 第 ${info.page} 页「${info.title}」，和接口一致（${ms} ms）`)

        await page.waitForSelector('[data-testid=file-logs] li', { timeout: 5000 })
        const logs = await page.$$eval('[data-testid=file-logs] li', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()))
        const link = logs.find((t) => t.startsWith('挂接'))
        const want = '挂到 Z109-WS·2025-Y-BGS-0001 第 3 页 · 题名：关于开展 2025 年档案数字化工作的通知 · v1 · 当时审核：通过'
        assert(link?.includes(want), `文件日志里"挂接"的说明不对：${link}`)
        console.log(`  文件日志：${want}`)

        const item = ctx.demo.items.find((i) => i.archive_no === 'Z109-WS·2025-Y-BGS-0001')
        await changeTitle(ctx, item.id, '关于开展 2025 年档案数字化工作的通知（改题名验证）')
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
      name: 'm3-files-catalog-info-item-no',
      user: admin,
      path: '/files',
      prepare: async (page) => {
        const ctx = await context(page)
        await openFiles(page, ctx.demo.folders.box3.id, 'IMG_0005.png')
        const f5 = ctx.byName(ctx.demo.files.box3, 'IMG_0005.png')
        await waitInfoMatches(page, ctx.admin, f5.file_id, { scope: '[data-testid=detail-panel]' })
        console.log('  盒 03 IMG_0005（没挂接）："未归类"，和接口一致')
        await openFiles(page, ctx.demo.folders.box3.id, 'IMG_0001.png')
        const f1 = ctx.byName(ctx.demo.files.box3, 'IMG_0001.png')
        await waitInfoMatches(page, ctx.admin, f1.file_id, { scope: '[data-testid=detail-panel]' })
        const info = await readInfo(page, '[data-testid=detail-panel]')
        assert(info.no === '第 1 件' && info.page === 1 && info.title === '（还没填题名）', `盒 03 IMG_0001 应显示"第 1 件 第 1 页（还没填题名）"，页面是 ${JSON.stringify(info)}`)
        console.log(`  盒 03 IMG_0001（档号为空的条目）：${info.no} 第 ${info.page} 页 ${info.title}`)
        await page.$eval('[data-testid=catalog-info]', (el) => el.scrollIntoView({ block: 'center' }))
      },
    },
  ],
}
