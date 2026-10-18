// M3-7 的截图清单和断言：项目看板 /dashboard（demo_manager 看）。
//
//   INIT_ADMIN_PASSWORD=admin123456 \
//     npm run screenshots -- --spec scripts/shots/m3-dashboard.mjs --base http://localhost:<preview 端口>
//
// 前端地址是 vite preview 或 dev（/api 代理到 server）。要先有 npm run seed 的演示数据（M9-3）：DEMO-A 的
// 「2025 文书档案 / 盒 WS-2025-002 / 附件（含下级）」、全宗 Z001「文书档案」；demo_manager（有 dashboard 菜单）、
// demo_reviewer 能进 DEMO-A（密码 demo123456，可用 DEMO_PASSWORD 覆盖）。
// 每张会建的临时数据（「截图验证 M3-7 …」文件夹、临时归类节点和条目）都在 after 里删掉，前面失败了也删，可以反复跑；
// 不改演示数据的审核、归档状态。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - m3-dashboard：KPI（已上传、审核通过、待重扫、已归档、已锁定、可导出的主数字和次要数字）和矩阵的 4×3 格、每行合计、
//   可导出列、合计行，都和接口 catalog-summary（不带 folder_id）一致。
// - m3-dashboard-expand：展开「2025 文书档案」再展开「盒 WS-2025-002」：每行的文件数、通过、驳回、已归档、已锁定、可导出
//   和接口（这两个文件夹的 catalog-summary 的 children、direct_counts）一致，盒 002 下多一行"直接放在这里的文件"。
// - m3-dashboard-live：页面之外建临时文件夹（含一个子文件夹）、传 4 页 → 分给 demo_reviewer → 驳回子文件夹里的 1 页
//   （子文件夹那一层没加载，要按路径补祖先链）→ 展开临时文件夹后通过 2 页：每一步之后 3 秒内 KPI、矩阵和临时文件夹
//   （展开后还有子文件夹、"直接放在这里的文件"）的行都和接口一致，打印每一步用了多久。
// - m3-dashboard-link（M9-3 起按整个文件夹归档）：临时文件夹传 3 页，在全宗 Z001 下经接口建一条完整的临时链（门类 /
//   年度 / 保管期限 / 机构，档号才完整）→ 把临时文件夹整个归档（items:from-folders，发 files.changed、folders.changed
//   和 catalog.changed）→ 3 秒内已归档 +3 且和接口一致；填必填著录、这 3 页分给 demo_reviewer 并审核通过（锁定要求
//   全部页通过）→ 没锁定时"可导出"不变；再锁定 → 3 秒内已锁定 +3、可导出的主数字（通过且可导出）和次要数字都 +3。
//   after 里条目解锁、撤销归档，临时链删掉。
// - m3-dashboard-requests（审查第 1 轮：收起的文件夹不再刷新、一轮刷新不叠加）：拦截页面的 catalog-summary 请求计数。
//   展开「2025 文书档案」「盒 WS-2025-002」后发一条 catalog.changed（经接口给全宗节点改名）→ 正好取 3 次（根、2025、
//   盒 002）；收起「2025 文书档案」（盒 002 一起收起）后再发一条 → 只取根 1 次；再展开 → 重新取 2025、盒 002 是收起的；
//   收起后把 catalog-summary 推迟 1 秒放行，每 400 ms 发一条事件、共 5 条 → 同时在途最多 1 个、一共不超过 3 次；
//   最后看板和接口一致，全宗名改回原样。

import { apiLogin, interceptRequests, requiredFieldValues, stamp, uploadPage } from '../lib/api.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'
const MANAGER = 'demo_manager'
const IDLE = 'networkidle2'
const OUT = '../docs/screenshots/m3'
/** 页面之外改了数据后，看板要在这么久之内和接口一致。 */
const LIVE_MS = 3000
/** npm run seed 的演示数据（M9-3）：展开的两层文件夹、第 3 层的子文件夹、全宗。 */
const M37_FOLDER = '2025 文书档案'
const BOX = '盒 WS-2025-002'
const EXTRA = '附件（含下级）'
const FONDS_CODE = 'Z001'
const M37_FONDS_NAME = '文书档案'

/** 找到 npm run seed 建的文件夹和全宗，返回 {project, folders: {top, box3, extra}, categories: {fonds}}。 */
async function seedData(admin) {
  const project = (await admin('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-A')
  if (!project) throw new Error('找不到 DEMO-A，先跑 npm run seed')
  const child = async (id, name) => {
    const f = (await admin('GET', `/api/folders/${id}/children`)).folders.find((x) => x.folder_name === name)
    if (!f) throw new Error(`找不到文件夹「${name}」，先跑 npm run seed`)
    return f
  }
  const top = await child(project.root_folder_id, M37_FOLDER)
  const box3 = await child(top.id, BOX)
  const extra = await child(box3.id, EXTRA)
  const fonds = (await admin('GET', `/api/projects/${project.id}/archive-categories`)).items.find((c) => c.category_code === FONDS_CODE)
  if (!fonds) throw new Error(`找不到全宗 ${FONDS_CODE}，先跑 npm run seed`)
  return { project, folders: { top, box3, extra }, categories: { fonds } }
}

/** 删掉临时条目（已锁定的先解锁；撤销归档时页一并移出）。 */
async function deleteItem(call, id) {
  let it = await call('GET', `/api/archive-items/${id}`, undefined, { allow: [404] })
  if (typeof it.status === 'number') return
  if (it.status === 'locked') it = await call('POST', `/api/archive-items/${id}/unlock`, { row_version: it.row_version })
  await call('DELETE', `/api/archive-items/${id}?row_version=${it.row_version}`)
}

let ctxPromise = null
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const adminApi = await apiLogin(origin, admin, adminPassword)
    const demo = await seedData(adminApi)
    const mgr = await apiLogin(origin, MANAGER, demoPassword)
    const rev = await apiLogin(origin, 'demo_reviewer', demoPassword)
    const reviewer = (await adminApi('GET', '/api/system/users?keyword=demo_reviewer&page_size=100')).items.find(
      (u) => u.username === 'demo_reviewer',
    )
    return { origin, admin: adminApi, mgr, rev, reviewerId: reviewer.id, demo, pid: demo.project.id }
  })()
  return ctxPromise
}

function assert(cond, message) {
  if (!cond) throw new Error(`断言失败：${message}`)
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function summary(ctx, folderId) {
  return ctx.mgr('GET', `/api/projects/${ctx.pid}/catalog-summary${folderId ? `?folder_id=${folderId}` : ''}`)
}

/** 页面上的 KPI、矩阵、文件夹行（都从 data-* 读）。 */
function readPage(page) {
  return page.evaluate(() => {
    const kpi = {}
    for (const el of document.querySelectorAll('[data-testid=kpi]')) {
      kpi[el.dataset.kind] = Number(el.dataset.value)
      if (el.dataset.total !== undefined) kpi[`${el.dataset.kind}_total`] = Number(el.dataset.total)
    }
    const matrix = {}
    for (const el of document.querySelectorAll('[data-testid=matrix-cell]')) matrix[`${el.dataset.review}.${el.dataset.catalog}`] = Number(el.dataset.value)
    const rows = {}
    for (const el of document.querySelectorAll('[data-testid=folder-row]')) {
      rows[`${el.dataset.kind}:${el.dataset.folderId}`] = {
        depth: Number(el.dataset.depth),
        total: Number(el.dataset.total),
        approved: Number(el.dataset.approved),
        rejected: Number(el.dataset.rejected),
        cataloged: Number(el.dataset.cataloged),
        locked: Number(el.dataset.locked),
        exportable: Number(el.dataset.exportable),
      }
    }
    return { kpi, matrix, rows }
  })
}

/** 接口的 counts 应该在页面上显示成什么样。 */
function expectedTop(c) {
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

function rowOf(c) {
  return {
    total: c.total,
    approved: c.review.approved,
    rejected: c.review.rejected,
    cataloged: c.catalog.cataloged,
    locked: c.catalog.locked,
    exportable: c.exportable.approved,
  }
}

/** 页面和期望的差异（空数组表示一致）。`rows` 是 {行键: 期望的行}。 */
function diff(actual, want) {
  const out = []
  for (const [k, v] of Object.entries(want.kpi ?? {})) if (actual.kpi[k] !== v) out.push(`KPI ${k}: 页面 ${actual.kpi[k]}，接口 ${v}`)
  for (const [k, v] of Object.entries(want.matrix ?? {})) if (actual.matrix[k] !== v) out.push(`矩阵 ${k}: 页面 ${actual.matrix[k]}，接口 ${v}`)
  for (const [k, v] of Object.entries(want.rows ?? {})) {
    const a = actual.rows[k]
    if (!a) {
      out.push(`没有行 ${k}`)
      continue
    }
    for (const [f, n] of Object.entries(v)) if (a[f] !== n) out.push(`行 ${k}.${f}: 页面 ${a[f]}，接口 ${n}`)
  }
  return out
}

/**
 * 等页面和接口一致（每 100 ms 重新读页面；接口每 500 ms 重新取一次），返回用了多少毫秒。`rowsFor(ctx)` 返回
 * 要核对的行（键 → 期望）。超时时报出差异。
 */
async function waitConsistent(page, ctx, { timeout = LIVE_MS, rowsFor = async () => ({}) } = {}) {
  const t0 = Date.now()
  let want = null
  let wantAt = 0
  let last = []
  while (true) {
    if (!want || Date.now() - wantAt > 500) {
      const root = await summary(ctx, null)
      want = { ...expectedTop(root.counts), rows: await rowsFor(ctx) }
      wantAt = Date.now()
    }
    last = diff(await readPage(page), want)
    if (last.length === 0) return Date.now() - t0
    if (Date.now() - t0 > timeout) throw new Error(`${timeout} ms 内看板没有和接口一致：${last.slice(0, 6).join('；')}`)
    await sleep(100)
  }
}

async function openDashboard(page) {
  await page.waitForSelector('[data-testid=dashboard-page][data-state=ready][data-stream=open]', { timeout: 15000 })
  await page.waitForSelector('[data-testid=kpi]')
}

async function toggleRow(page, folderId, want) {
  const sel = `[data-testid=folder-row][data-kind=folder][data-folder-id="${folderId}"]`
  await page.waitForSelector(sel, { timeout: 5000 })
  const expanded = await page.$eval(sel, (el) => el.dataset.expanded === 'true')
  if (expanded !== want) await page.click(`${sel} [data-testid=folder-toggle]`)
  await page.waitForSelector(`${sel}[data-expanded="${want}"]`, { timeout: 5000 })
}

/** 在 DEMO-A 根下建临时文件夹（可以带一个子文件夹），各传几页。返回 {id, name, sub, files, subFiles}。 */
async function makeTemp(ctx, label, { pages = 0, subPages = 0 } = {}) {
  const name = `截图验证 M3-7 ${label} ${stamp()}`
  const top = await ctx.admin('POST', '/api/folders', {
    parent_id: ctx.demo.project.root_folder_id,
    folder_name: name,
    remark: 'M3-7 截图脚本临时建的，做完会删掉',
  })
  const seed = `M3-7 ${label} ${stamp()} ${Math.random()}`
  const files = []
  for (let i = 1; i <= pages; i++) files.push(await uploadPage(ctx.admin, top.id, `P_${String(i).padStart(2, '0')}.png`, `${seed}#${i}`, i))
  let sub = null
  const subFiles = []
  if (subPages) {
    sub = await ctx.admin('POST', '/api/folders', { parent_id: top.id, folder_name: '子文件夹' })
    for (let i = 1; i <= subPages; i++) subFiles.push(await uploadPage(ctx.admin, sub.id, `S_${String(i).padStart(2, '0')}.png`, `${seed}|sub#${i}`, i))
  }
  return { id: top.id, name, sub, files, subFiles }
}

const temps = []
async function dropTemps() {
  if (!ctxPromise) return
  const ctx = await ctxPromise
  while (temps.length) {
    const t = temps.pop()
    await ctx.admin('DELETE', `/api/folders/${t.id}`, undefined, { allow: [404, 409] }).then((r) => {
      if (r?.status === 409) console.log(`  临时文件夹「${t.name}」删不掉：${r.message}`)
      else console.log(`  已删除临时文件夹「${t.name}」`)
    })
  }
}

let tempCategory = null
let tempItem = null
async function dropCatalogTemps() {
  if (!ctxPromise) return
  const ctx = await ctxPromise
  const step = async (what, fn) => {
    try {
      await fn()
    } catch (e) {
      console.warn(`  清理 ${what} 出错：${e.message}`)
    }
  }
  if (tempItem) {
    // 经接口：已锁定的先解锁，再撤销归档（页一并移出，文件夹回到未归档）。
    const id = tempItem
    await step('临时条目', () => deleteItem(ctx.admin, id))
    console.log('  已撤销临时条目的归档（一并移出了它的页）')
    tempItem = null
  }
  if (tempCategory) {
    const node = tempCategory
    await step('临时归类节点', () => ctx.admin('DELETE', `/api/projects/${ctx.pid}/archive-categories/${node.id}`, undefined, { allow: [404] }))
    console.log(`  已删除临时归类节点「${node.category_name}」（含下级）`)
    tempCategory = null
  }
}

export default {
  out: OUT,
  accounts: {
    [admin]: adminPassword,
    [MANAGER]: demoPassword,
  },
  waitUntil: IDLE,
  shots: [
    {
      name: 'm3-dashboard',
      user: MANAGER,
      path: '/dashboard',
      prepare: async (page) => {
        const ctx = await context(page)
        await openDashboard(page)
        const ms = await waitConsistent(page, ctx, { timeout: 5000 })
        const root = await summary(ctx, null)
        console.log(
          `  KPI 和矩阵和接口一致（${ms} ms）：已上传 ${root.counts.total}、通过 ${root.counts.review.approved}、驳回 ${root.counts.review.rejected}、` +
            `已归档 ${root.counts.catalog.cataloged}、已锁定 ${root.counts.catalog.locked}、可导出 ${root.counts.exportable.approved} / ${root.counts.exportable.total}`,
        )
        // 第一层每个文件夹的行也对。
        const rows = Object.fromEntries(root.children.map((c) => [`folder:${c.folder_id}`, rowOf(c.counts)]))
        const d = diff(await readPage(page), { rows })
        assert(d.length === 0, `第一层文件夹的行和接口不一致：${d.slice(0, 5).join('；')}`)
        await page.evaluate(() => document.querySelector('[data-testid=dashboard-page] section')?.scrollTo(0, 0))
      },
    },
    {
      name: 'm3-dashboard-expand',
      user: MANAGER,
      path: '/dashboard',
      prepare: async (page) => {
        const ctx = await context(page)
        await openDashboard(page)
        const top = ctx.demo.folders.top
        const box3 = ctx.demo.folders.box3
        await toggleRow(page, top.id, true)
        await toggleRow(page, box3.id, true)
        const s1 = await summary(ctx, top.id)
        const s3 = await summary(ctx, box3.id)
        const rows = {
          [`folder:${top.id}`]: rowOf(s1.counts),
          ...Object.fromEntries(s1.children.map((c) => [`folder:${c.folder_id}`, rowOf(c.counts)])),
          ...Object.fromEntries(s3.children.map((c) => [`folder:${c.folder_id}`, rowOf(c.counts)])),
          [`direct:${box3.id}`]: rowOf(s3.direct_counts),
        }
        await page.waitForSelector(`[data-testid=folder-row][data-kind=direct][data-folder-id="${box3.id}"]`, { timeout: 5000 })
        const actual = await readPage(page)
        const d = diff(actual, { rows })
        assert(d.length === 0, `展开后的行和接口不一致：${d.slice(0, 5).join('；')}`)
        assert(actual.rows[`folder:${ctx.demo.folders.extra.id}`]?.depth === 2, `「${EXTRA}」应该在第 3 层（depth 2）`)
        console.log(
          `  展开「${M37_FOLDER}」「${BOX}」：${Object.keys(rows).length} 行和接口一致；${M37_FOLDER} 共 ${s1.counts.total} 页，` +
            `已归档 ${s1.counts.catalog.cataloged}、已锁定 ${s1.counts.catalog.locked}`,
        )
      },
    },
    {
      name: 'm3-dashboard-live',
      user: MANAGER,
      path: '/dashboard',
      prepare: async (page) => {
        const ctx = await context(page)
        await openDashboard(page)
        const temp = await makeTemp(ctx, '看板实时', { pages: 2, subPages: 2 })
        temps.push(temp)
        const tempRow = async (c) => ({ [`folder:${temp.id}`]: rowOf((await summary(c, temp.id)).counts) })
        let ms = await waitConsistent(page, ctx, { rowsFor: tempRow })
        console.log(`  页面之外建临时文件夹、传 4 页：${ms} ms 后看板和接口一致`)

        const all = [...temp.files, ...temp.subFiles].map((f) => f.file_id)
        await ctx.admin('POST', `/api/projects/${ctx.pid}/review-assignments`, { reviewer_id: ctx.reviewerId, file_ids: all })
        ms = await waitConsistent(page, ctx, { rowsFor: tempRow })
        console.log(`  分给 demo_reviewer：${ms} ms`)

        // 子文件夹那一层还没加载过（临时文件夹没展开）时驳回子文件夹里的 1 页：事件只带子文件夹，要按路径补祖先链。
        await ctx.rev('POST', `/api/files/${temp.subFiles[1].file_id}/review`, {
          result: 'rejected',
          reject_reasons: ['blur'],
          comment: 'M3-7 截图脚本：看板实时刷新',
        })
        ms = await waitConsistent(page, ctx, { rowsFor: tempRow })
        assert(ms <= LIVE_MS, `驳回之后 ${ms} ms 才更新`)
        console.log(`  页面之外驳回子文件夹里的 1 页（子文件夹那一层没加载）：${ms} ms 后 KPI、矩阵、临时文件夹那一行和接口一致`)

        // 展开临时文件夹后通过 2 页（一页在顶层、一页在子文件夹里）：展开的各行也要跟上。
        await toggleRow(page, temp.id, true)
        await ctx.rev('POST', `/api/projects/${ctx.pid}/reviews:approve`, { file_ids: [temp.files[0].file_id, temp.subFiles[0].file_id] })
        const rowsExpanded = async (c) => ({
          ...(await tempRow(c)),
          [`folder:${temp.sub.id}`]: rowOf((await summary(c, temp.sub.id)).counts),
          [`direct:${temp.id}`]: rowOf((await summary(c, temp.id)).direct_counts),
        })
        ms = await waitConsistent(page, ctx, { rowsFor: rowsExpanded })
        assert(ms <= LIVE_MS, `通过之后 ${ms} ms 才更新`)
        console.log(`  页面之外通过 2 页（展开着）：${ms} ms 后 KPI、矩阵、临时文件夹、子文件夹和"直接放在这里的文件"的行都和接口一致`)
        const t = (await readPage(page)).rows[`folder:${temp.id}`]
        assert(t.approved === 2 && t.rejected === 1 && t.total === 4, `临时文件夹那一行应为 4 页、通过 2、驳回 1，页面是 ${JSON.stringify(t)}`)
        await page.evaluate(() => document.querySelector('[data-testid=dashboard-page] section')?.scrollTo(0, 0))
      },
      after: dropTemps,
    },
    {
      name: 'm3-dashboard-link',
      user: MANAGER,
      path: '/dashboard',
      prepare: async (page) => {
        const ctx = await context(page)
        await openDashboard(page)
        const temp = await makeTemp(ctx, '看板归档', { pages: 3 })
        temps.push(temp)
        const tempRow = async (c) => ({ [`folder:${temp.id}`]: rowOf((await summary(c, temp.id)).counts) })
        // 一条完整的临时链（门类 / 年度 / 保管期限 / 机构）：档号完整才能锁定。
        const node = (parentId, type, code, name) =>
          ctx.admin('POST', `/api/projects/${ctx.pid}/archive-categories`, { parent_id: parentId, category_type: type, category_code: code, category_name: name })
        tempCategory = await node(ctx.demo.categories.fonds.id, 'category', `T${stamp()}`, '截图验证 M3-7 归档')
        const y = await node(tempCategory.id, 'year', '2025', '2025 年')
        const r = await node(y.id, 'retention', 'Y', '永久')
        const org = await node(r.id, 'org', 'BGS', '办公室')
        await waitConsistent(page, ctx, { rowsFor: tempRow })
        const before = (await summary(ctx, null)).counts

        // 经接口把临时文件夹整个归档（它没有子文件夹，自己成一件，3 页）。
        const res = await ctx.admin('POST', `/api/archive-categories/${org.id}/items:from-folders`, { folder_ids: [temp.id] })
        assert(res.items.length === 1 && res.items[0].page_count === 3, `应归档成 1 件 3 页：${JSON.stringify(res.items)}`)
        let it = await ctx.admin('GET', `/api/archive-items/${res.items[0].item_id}`)
        tempItem = it.id
        let ms = await waitConsistent(page, ctx, { rowsFor: tempRow })
        assert(ms <= LIVE_MS, `归档之后 ${ms} ms 才更新`)
        const after1 = (await summary(ctx, null)).counts
        assert(after1.catalog.cataloged === before.catalog.cataloged + 3, `已归档应该 +3（${before.catalog.cataloged} → ${after1.catalog.cataloged}）`)
        console.log(`  经接口把临时文件夹整个归档（3 页）：${ms} ms 后已归档 ${before.catalog.cataloged} → ${after1.catalog.cataloged}，和接口一致`)

        // 锁定要求必填著录合格、全部页审核通过（DEMO-A 的 lock_requires_approved 是默认 true）。
        it = await ctx.admin('PATCH', `/api/archive-items/${it.id}`, { row_version: it.row_version, extra_fields: await requiredFieldValues(ctx.admin, ctx.pid) })
        const three = temp.files.map((f) => f.file_id)
        await ctx.admin('POST', `/api/projects/${ctx.pid}/review-assignments`, { reviewer_id: ctx.reviewerId, file_ids: three })
        await ctx.rev('POST', `/api/projects/${ctx.pid}/reviews:approve`, { file_ids: three })
        ms = await waitConsistent(page, ctx, { rowsFor: tempRow })
        const beforeLock = (await summary(ctx, null)).counts
        assert(beforeLock.exportable.approved === before.exportable.approved, '没锁定时审核通过不改"可导出"（要求锁定）')
        console.log(`  这 3 页分给 demo_reviewer 并通过：${ms} ms 后和接口一致；审核通过 ${before.review.approved} → ${beforeLock.review.approved}`)

        it = await ctx.admin('POST', `/api/archive-items/${it.id}/lock`, { row_version: it.row_version })
        assert(it.status === 'locked', `条目应已锁定，现在是 ${it.status}`)
        ms = await waitConsistent(page, ctx, { rowsFor: tempRow })
        assert(ms <= LIVE_MS, `锁定之后 ${ms} ms 才更新`)
        const after2 = (await summary(ctx, null)).counts
        assert(after2.catalog.locked === before.catalog.locked + 3, '已锁定应该 +3')
        assert(after2.exportable.total === before.exportable.total + 3, '全部可导出应该 +3')
        assert(after2.exportable.approved === before.exportable.approved + 3, '通过且可导出应该 +3（这 3 页审核通过了）')
        console.log(
          `  锁定 + catalog.changed：${ms} ms；已锁定 ${before.catalog.locked} → ${after2.catalog.locked}，` +
            `可导出 ${before.exportable.approved} / ${before.exportable.total} → ${after2.exportable.approved} / ${after2.exportable.total}`,
        )
        await page.evaluate(() => document.querySelector('[data-testid=dashboard-page] section')?.scrollTo(0, 0))
      },
      after: async () => {
        await dropCatalogTemps()
        await dropTemps()
      },
    },
    {
      name: 'm3-dashboard-requests',
      user: MANAGER,
      path: '/dashboard',
      prepare: async (page) => {
        const ctx = await context(page)
        await openDashboard(page)
        const top = ctx.demo.folders.top
        const box3 = ctx.demo.folders.box3
        const fondsId = ctx.demo.categories.fonds.id
        const rename = (name) => ctx.admin('PATCH', `/api/projects/${ctx.pid}/archive-categories/${fondsId}`, { category_name: name })
        /** 记下的 catalog-summary 请求各取的是哪个文件夹（不带 folder_id 的是根）。 */
        const targets = (log) => log.started.map((r) => new URL(r.url).searchParams.get('folder_id') ?? 'root')
        const name = (id) => (id === 'root' ? '根' : id === top.id ? M37_FOLDER : id === box3.id ? BOX : id)
        await interceptRequests(page, (u) => u.includes('/catalog-summary'), async (log) => {
          await toggleRow(page, top.id, true)
          await toggleRow(page, box3.id, true)
          await page.waitForSelector(`[data-testid=folder-row][data-kind=direct][data-folder-id="${box3.id}"]`, { timeout: 5000 })
          await log.waitQuiet(500)
          log.reset()

          // 展开着两层：一条 catalog.changed 取根和两个展开的文件夹，各一次。
          await rename(`${M37_FONDS_NAME}（请求计数 1）`)
          await log.waitQuiet()
          let got = targets(log)
          assert(
            JSON.stringify([...got].sort()) === JSON.stringify(['root', top.id, box3.id].sort()),
            `展开两层时一条事件应取 3 次（根、${M37_FOLDER}、${BOX}），实际 ${got.map(name).join('、')}`,
          )
          console.log(`  展开两层时一条 catalog.changed：取了 ${got.length} 次（${got.map(name).join('、')}）`)

          // 收起「2025 文书档案」：它下面展开的盒 002 一起收起，都不再刷新。
          await toggleRow(page, top.id, false)
          assert(!(await page.$(`[data-testid=folder-row][data-folder-id="${box3.id}"]`)), `收起后不应再显示${BOX}`)
          await log.waitQuiet(300)
          log.reset()
          await rename(`${M37_FONDS_NAME}（请求计数 2）`)
          await log.waitQuiet()
          got = targets(log)
          assert(JSON.stringify(got) === JSON.stringify(['root']), `收起后一条事件应只取根 1 次，实际 ${got.map(name).join('、') || '0 次'}`)
          console.log('  收起后一条 catalog.changed：只取了根 1 次')

          // 再展开：重新取 2025 文书档案；盒 002 是收起的（收起上一层时一起收起了）。
          log.reset()
          await toggleRow(page, top.id, true)
          await page.waitForSelector(`[data-testid=folder-row][data-folder-id="${box3.id}"][data-expanded=false]`, { timeout: 5000 })
          await log.waitQuiet(500)
          got = targets(log)
          assert(JSON.stringify(got) === JSON.stringify([top.id]), `再展开应只取 ${M37_FOLDER} 1 次，实际 ${got.map(name).join('、')}`)
          await toggleRow(page, top.id, false)
          await log.waitQuiet(300)

          // 慢接口（catalog-summary 推迟 1 秒放行）+ 每 400 ms 一条事件：一轮没跑完时不叠加，跑完再补一轮。
          log.reset()
          log.delayMs = 1000
          const names = ['（请求计数 3）', '（请求计数 4）', '（请求计数 5）', '（请求计数 6）', ''].map((s) => `${M37_FONDS_NAME}${s}`)
          // 改名不等响应（远程库上一次改名要 0.6–1 秒，等着就不是每 400 ms 一条了），发完再一起等。
          const sent = []
          for (const n of names) {
            sent.push(rename(n))
            await sleep(400)
          }
          await Promise.all(sent)
          await log.waitQuiet(1500, 20000)
          log.delayMs = 0
          assert(log.maxInflight === 1, `同时在途的 catalog-summary 应最多 1 个，实际 ${log.maxInflight} 个`)
          assert(log.started.length <= 3, `5 条事件（每条间隔 400 ms、接口 1 秒）应合成不超过 3 轮，实际取了 ${log.started.length} 次`)
          console.log(`  接口慢 1 秒、每 400 ms 一条事件共 5 条：取了 ${log.started.length} 次，同时在途最多 ${log.maxInflight} 个`)
        })
        const ms = await waitConsistent(page, ctx)
        console.log(`  最后看板和接口一致（${ms} ms）`)
        await page.evaluate(() => document.querySelector('[data-testid=dashboard-page] section')?.scrollTo(0, 0))
      },
      after: async () => {
        if (!ctxPromise) return
        const ctx = await ctxPromise
        await ctx.admin('PATCH', `/api/projects/${ctx.pid}/archive-categories/${ctx.demo.categories.fonds.id}`, { category_name: M37_FONDS_NAME })
      },
    },
  ],
}
