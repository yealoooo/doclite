// M3-7 的截图清单和断言：项目看板 /dashboard（demo_manager 看）。
//
//   INIT_ADMIN_PASSWORD=admin123456 \
//     npm run screenshots -- --spec scripts/shots/m3-dashboard.mjs --base http://localhost:<preview 端口>
//
// 前端地址是 vite preview 或 dev（/api 代理到 server）。要先有：
// - DEMO-A 的「M3-7 看板演示」演示数据（scripts/lib/m3-demo.mjs 的 ensureM37Demo：文件夹要先用 seed:files 传好，
//   审核、归类树、条目和挂接第一次跑时自动建好，M3-6 起全部经接口，不再用 psql）；
// - demo_manager（有 dashboard 菜单）、demo_reviewer 能进 DEMO-A（密码 demo123456，可用 DEMO_PASSWORD 覆盖）。
// 每张会建的临时数据（「截图验证 M3-7 …」文件夹、临时归类节点和条目）都在 after 里删掉，前面失败了也删，可以反复跑；
// 不改 M1、M2 演示文件夹的审核状态。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - m3-dashboard：KPI（已上传、审核通过、待重扫、已归类、已定稿、可导出的主数字和次要数字）和矩阵的 4×3 格、每行合计、
//   可导出列、合计行，都和接口 catalog-summary（不带 folder_id）一致。
// - m3-dashboard-expand：展开「M3-7 看板演示」再展开「盒 03」：每行的文件数、通过、驳回、已归类、已定稿、可导出和
//   接口（这两个文件夹的 catalog-summary 的 children、direct_counts）一致，「盒 03」下多一行"直接放在这里的文件"。
// - m3-dashboard-live：页面之外建临时文件夹（含一个子文件夹）、传 4 页 → 分给 demo_reviewer → 驳回子文件夹里的 1 页
//   （子文件夹那一层没加载，要按路径补祖先链）→ 展开临时文件夹后通过 2 页：每一步之后 3 秒内 KPI、矩阵和临时文件夹
//   （展开后还有子文件夹、"直接放在这里的文件"）的行都和接口一致，打印每一步用了多久。
// - m3-dashboard-link（M3-6 起全部经接口；原来 M3-3 合入前是直接写库再改名发事件）：临时文件夹传 4 页，在全宗 Z109 下
//   经接口建一条完整的临时链（门类 / 年度 / 保管期限 / 机构，档号才完整）→ 带 file_ids 新建条目、挂 3 页（M3-3，自己发
//   files.changed 和 catalog.changed）→ 3 秒内已归类 +3 且和接口一致；这 3 页分给 demo_reviewer 并审核通过（定稿要求
//   全部页通过；M3-6 审查第 1 轮：不再临时改 `lock_requires_approved`）→ 没定稿时"可导出"不变；再标记完成、定稿 →
//   3 秒内已定稿 +3、可导出的主数字（通过且可导出）和次要数字都 +3。after 里条目解锁、删除（一并移出），临时链删掉。
// - m3-dashboard-requests（审查第 1 轮：收起的文件夹不再刷新、一轮刷新不叠加）：拦截页面的 catalog-summary 请求计数。
//   展开「M3-7 看板演示」「盒 03」后发一条 catalog.changed（经接口给全宗节点改名）→ 正好取 3 次（根、M3-7、盒 03）；
//   收起「M3-7 看板演示」（盒 03 一起收起）后再发一条 → 只取根 1 次；再展开 → 重新取 M3-7、盒 03 是收起的；
//   收起后把 catalog-summary 推迟 1 秒放行，每 400 ms 发一条事件、共 5 条 → 同时在途最多 1 个、一共不超过 3 次；
//   最后看板和接口一致，全宗名改回原样。

import {
  apiLogin,
  deleteItemsForCleanup,
  ensureM37Demo,
  interceptRequests,
  M37_FOLDER,
  M37_FONDS_NAME,
  requiredFieldValues,
  stamp,
  uploadPage,
} from '../lib/m3-demo.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'
const MANAGER = 'demo_manager'
const IDLE = 'networkidle2'
const OUT = '../docs/screenshots/m3'
/** 页面之外改了数据后，看板要在这么久之内和接口一致。 */
const LIVE_MS = 3000

let ctxPromise = null
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const adminApi = await apiLogin(origin, admin, adminPassword)
    const demo = await ensureM37Demo({ origin, admin: adminApi, demoPassword })
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
    // 经接口：已定稿的先解锁，再删除（有页时一并移出）。
    const id = tempItem
    await step('临时条目', () => deleteItemsForCleanup(ctx.admin, [id]))
    console.log('  已删除临时条目（一并移出了它的页）')
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
            `已归类 ${root.counts.catalog.cataloged}、已定稿 ${root.counts.catalog.locked}、可导出 ${root.counts.exportable.approved} / ${root.counts.exportable.total}`,
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
        assert(actual.rows[`folder:${ctx.demo.folders.extra.id}`]?.depth === 2, '「补扫」应该在第 3 层（depth 2）')
        console.log(
          `  展开「${M37_FOLDER}」「盒 03」：${Object.keys(rows).length} 行和接口一致；${M37_FOLDER} 共 ${s1.counts.total} 页，` +
            `已归类 ${s1.counts.catalog.cataloged}、已定稿 ${s1.counts.catalog.locked}`,
        )
        // 「M3-7 看板演示」是第一层最后一个文件夹：滚到底，展开的几行都在画面里。
        await page.evaluate(() => {
          const el = document.querySelector('[data-testid=dashboard-page] section')
          el?.scrollTo(0, el.scrollHeight)
        })
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
        const temp = await makeTemp(ctx, '看板挂接', { pages: 4 })
        temps.push(temp)
        const tempRow = async (c) => ({ [`folder:${temp.id}`]: rowOf((await summary(c, temp.id)).counts) })
        // 一条完整的临时链（门类 / 年度 / 保管期限 / 机构）：档号完整才能定稿。
        const node = (parentId, type, code, name) =>
          ctx.admin('POST', `/api/projects/${ctx.pid}/archive-categories`, { parent_id: parentId, category_type: type, category_code: code, category_name: name })
        tempCategory = await node(ctx.demo.categories.fonds.id, 'category', `T${stamp()}`, '截图验证 M3-7 挂接')
        const y = await node(tempCategory.id, 'year', '2025', '2025 年')
        const r = await node(y.id, 'retention', 'Y', '永久')
        const org = await node(r.id, 'org', 'BGS', '办公室')
        await waitConsistent(page, ctx, { rowsFor: tempRow })
        const before = (await summary(ctx, null)).counts

        // 经接口：带 file_ids 新建条目、挂 3 页（M3-3，自己发 files.changed、catalog.changed）。
        const required = await requiredFieldValues(ctx.admin, ctx.pid)
        let it = await ctx.admin('POST', `/api/archive-categories/${org.id}/items`, {
          title: '截图验证 M3-7',
          extra_fields: required,
          file_ids: temp.files.slice(0, 3).map((f) => f.file_id),
        })
        tempItem = it.id
        let ms = await waitConsistent(page, ctx, { rowsFor: tempRow })
        assert(ms <= LIVE_MS, `挂接之后 ${ms} ms 才更新`)
        const after1 = (await summary(ctx, null)).counts
        assert(after1.catalog.cataloged === before.catalog.cataloged + 3, `已归类应该 +3（${before.catalog.cataloged} → ${after1.catalog.cataloged}）`)
        console.log(`  经接口挂 3 页（POST 带 file_ids）：${ms} ms 后已归类 ${before.catalog.cataloged} → ${after1.catalog.cataloged}，和接口一致`)

        // 定稿要求全部页审核通过（DEMO-A 的 lock_requires_approved 是默认 true）：先把这 3 页分给 demo_reviewer 并通过
        // （M3-6 审查第 1 轮：不再临时改项目设置，进程被杀时会把 false 留在 DEMO-A 里，修改期间也对所有人生效）。
        const three = temp.files.slice(0, 3).map((f) => f.file_id)
        await ctx.admin('POST', `/api/projects/${ctx.pid}/review-assignments`, { reviewer_id: ctx.reviewerId, file_ids: three })
        await ctx.rev('POST', `/api/projects/${ctx.pid}/reviews:approve`, { file_ids: three })
        ms = await waitConsistent(page, ctx, { rowsFor: tempRow })
        const beforeLock = (await summary(ctx, null)).counts
        assert(beforeLock.exportable.approved === before.exportable.approved, '没定稿时审核通过不改"可导出"（要求定稿）')
        console.log(`  这 3 页分给 demo_reviewer 并通过：${ms} ms 后和接口一致；审核通过 ${before.review.approved} → ${beforeLock.review.approved}`)

        // 标记完成、定稿。
        it = await ctx.admin('POST', `/api/archive-items/${it.id}/mark-done`, { row_version: it.row_version })
        it = await ctx.admin('POST', `/api/archive-items/${it.id}/lock`, { row_version: it.row_version })
        assert(it.status === 'locked', `条目应已定稿，现在是 ${it.status}`)
        ms = await waitConsistent(page, ctx, { rowsFor: tempRow })
        assert(ms <= LIVE_MS, `定稿之后 ${ms} ms 才更新`)
        const after2 = (await summary(ctx, null)).counts
        assert(after2.catalog.locked === before.catalog.locked + 3, '已定稿应该 +3')
        assert(after2.exportable.total === before.exportable.total + 3, '全部可导出应该 +3')
        assert(after2.exportable.approved === before.exportable.approved + 3, '通过且可导出应该 +3（这 3 页审核通过了）')
        console.log(
          `  定稿 + catalog.changed：${ms} ms；已定稿 ${before.catalog.locked} → ${after2.catalog.locked}，` +
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
        const name = (id) => (id === 'root' ? '根' : id === top.id ? M37_FOLDER : id === box3.id ? '盒 03' : id)
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
            `展开两层时一条事件应取 3 次（根、${M37_FOLDER}、盒 03），实际 ${got.map(name).join('、')}`,
          )
          console.log(`  展开两层时一条 catalog.changed：取了 ${got.length} 次（${got.map(name).join('、')}）`)

          // 收起「M3-7 看板演示」：它下面展开的盒 03 一起收起，都不再刷新。
          await toggleRow(page, top.id, false)
          assert(!(await page.$(`[data-testid=folder-row][data-folder-id="${box3.id}"]`)), '收起后不应再显示盒 03')
          await log.waitQuiet(300)
          log.reset()
          await rename(`${M37_FONDS_NAME}（请求计数 2）`)
          await log.waitQuiet()
          got = targets(log)
          assert(JSON.stringify(got) === JSON.stringify(['root']), `收起后一条事件应只取根 1 次，实际 ${got.map(name).join('、') || '0 次'}`)
          console.log('  收起后一条 catalog.changed：只取了根 1 次')

          // 再展开：重新取 M3-7；盒 03 是收起的（收起上一层时一起收起了）。
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
          for (const n of names) {
            await rename(n)
            await sleep(400)
          }
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
