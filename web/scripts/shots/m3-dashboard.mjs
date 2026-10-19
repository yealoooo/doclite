// M3-7 的截图清单和断言：项目看板 /dashboard（demo_manager 看；2026-10-19 简化成"一行五个数 + 按文件夹的表"，M9-5）。
//
//   INIT_ADMIN_PASSWORD=admin123456 \
//     npm run screenshots -- --spec scripts/shots/m3-dashboard.mjs --base http://localhost:<preview 端口>
//
// 前端地址是 vite preview 或 dev（/api 代理到 server）。要先有 npm run seed 的演示数据（M9-3）：DEMO-A 的
// 「2025 文书档案 / 盒 WS-2025-001、盒 WS-2025-002 / 附件（含下级）」、全宗 Z001「文书档案」；demo_manager（有 dashboard 菜单）、
// demo_reviewer 能进 DEMO-A（密码 demo123456，可用 DEMO_PASSWORD 覆盖）。
// 每张会建的临时数据（「截图验证 M3-7 …」文件夹、临时归类节点和条目）都在 after 里删掉，前面失败了也删，可以反复跑；
// 不改演示数据的审核、归档状态。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - m3-dashboard：一行五个数（已上传 N 页、审核通过 x / N 页和待分配、待审、驳回、已归档 n 件 · m 页、已锁定 a / n 件、
//   可导出 e 件 · p 页）和接口 catalog-summary（不带 folder_id）的 counts 一致，可导出的小字按 require_locked_item；
//   第一层文件夹的行（页数、审核通过、已归档件数和页数、已锁定件数、自己的归档信息）和接口的 children 一致。打印五个数
//   （seed 的 DEMO-A 应是：44；10（待分配 27、待审 5、驳回 2）；3 件 9 页；1 / 3 件；1 件 3 页）。
// - m3-dashboard-expand：展开「2025 文书档案」「盒 WS-2025-001」「盒 WS-2025-002」：每行和接口（这几个文件夹的
//   catalog-summary 的 children、direct_counts）一致；盒 001 下归档了的 3 个文件夹写档号、其中 1 个"已锁定"，盒 002 下
//   多一行"直接放在这里的文件"。截图收起盒 002（和 Mockup v2 一样展开「2025 文书档案」「盒 WS-2025-001」）。
// - m3-dashboard-live：页面之外建临时文件夹（含一个子文件夹）、传 4 页 → 分给 demo_reviewer → 驳回子文件夹里的 1 页
//   （子文件夹那一层没加载，要按路径补祖先链）→ 展开临时文件夹后通过 2 页：每一步之后 3 秒内五个数和临时文件夹
//   （展开后还有子文件夹、"直接放在这里的文件"）的行都和接口一致，打印每一步用了多久。
// - m3-dashboard-link（M9-3 起按整个文件夹归档）：临时文件夹传 3 页，在全宗 Z001 下经接口建一条完整的临时链（门类 /
//   年度 / 保管期限 / 机构，档号才完整）→ 把临时文件夹整个归档（items:from-folders，发 files.changed、folders.changed
//   和 catalog.changed）→ 3 秒内已归档 +1 件 +3 页、临时文件夹那一行写档号，和接口一致；填必填著录、这 3 页分给
//   demo_reviewer 并审核通过（锁定要求全部页通过）→ 没锁定时"可导出"不变；再锁定 → 3 秒内已锁定 +1 件、可导出 +1 件
//   +3 页，那一行"已锁定"；再解锁、解除封存 → 那一行带"修改中"标记，可导出回到原样。
//   after 里撤销归档（已锁定的先解锁），临时链删掉。
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
const BOX1 = '盒 WS-2025-001'
const BOX = '盒 WS-2025-002'
const EXTRA = '附件（含下级）'
const FONDS_CODE = 'Z001'
const M37_FONDS_NAME = '文书档案'

/** 找到 npm run seed 建的文件夹和全宗，返回 {project, folders: {top, box1, box3, extra}, categories: {fonds}}。 */
async function seedData(admin) {
  const project = (await admin('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-A')
  if (!project) throw new Error('找不到 DEMO-A，先跑 npm run seed')
  const child = async (id, name) => {
    const f = (await admin('GET', `/api/folders/${id}/children`)).folders.find((x) => x.folder_name === name)
    if (!f) throw new Error(`找不到文件夹「${name}」，先跑 npm run seed`)
    return f
  }
  const top = await child(project.root_folder_id, M37_FOLDER)
  const box1 = await child(top.id, BOX1)
  const box3 = await child(top.id, BOX)
  const extra = await child(box3.id, EXTRA)
  const fonds = (await admin('GET', `/api/projects/${project.id}/archive-categories`)).items.find((c) => c.category_code === FONDS_CODE)
  if (!fonds) throw new Error(`找不到全宗 ${FONDS_CODE}，先跑 npm run seed`)
  return { project, folders: { top, box1, box3, extra }, categories: { fonds } }
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

/** 页面上的五个数、文件夹行（都从 data-* 读）。 */
function readPage(page) {
  return page.evaluate(() => {
    const flow = {}
    for (const el of document.querySelectorAll('[data-testid=flow-step]')) {
      for (const [k, v] of Object.entries(el.dataset)) {
        if (k === 'testid' || k === 'kind') continue
        flow[k === 'value' ? el.dataset.kind : `${el.dataset.kind}.${k}`] = Number(v)
      }
    }
    const rows = {}
    for (const el of document.querySelectorAll('[data-testid=folder-row]')) {
      const d = el.dataset
      rows[`${d.kind}:${d.folderId}`] = {
        depth: Number(d.depth),
        total: Number(d.total),
        approved: Number(d.approved),
        items: Number(d.items),
        cataloged: Number(d.cataloged),
        lockedItems: Number(d.lockedItems),
        archiveNo: d.archiveNo ?? null,
        archiveLocked: d.archiveLocked ?? null,
        archiveModifying: d.archiveModifying ?? null,
      }
    }
    return { flow, rows }
  })
}

/** 接口的 counts 应该在页面上显示成什么样（五个数）。 */
function expectedTop(c) {
  return {
    flow: {
      uploaded: c.total,
      approved: c.review.approved,
      'approved.total': c.total,
      'approved.unassigned': c.review.unassigned,
      'approved.pending': c.review.pending,
      'approved.rejected': c.review.rejected,
      archived: c.items.total,
      'archived.pages': c.catalog.cataloged,
      'archived.modifying': c.items.modifying,
      locked: c.items.locked,
      'locked.total': c.items.total,
      exportable: c.items.exportable,
      'exportable.pages': c.items.exportable_pages,
    },
  }
}

/** 一行（counts 是这一行的计数，archive 是文件夹自己的归档信息，直接放的文件为 null）。 */
function rowOf(c, archive = null) {
  return {
    total: c.total,
    approved: c.review.approved,
    items: c.items.total,
    cataloged: c.catalog.cataloged,
    lockedItems: c.items.locked,
    archiveNo: archive ? (archive.archive_no ?? `第 ${archive.item_no} 件`) : null,
    archiveLocked: archive ? String(archive.locked) : null,
    archiveModifying: archive ? String(archive.source_status === 'modifying') : null,
  }
}

/** children 里的一个子文件夹的行。 */
const childRow = (ch) => rowOf(ch.counts, ch.archive)

/** 父文件夹（null 是根）的 children 里 folderId 那一行（键 → 期望）。 */
async function childRowOf(ctx, parentId, folderId) {
  const ch = (await summary(ctx, parentId)).children.find((x) => x.folder_id === folderId)
  if (!ch) throw new Error(`接口的 children 里没有 ${folderId}`)
  return { [`folder:${folderId}`]: childRow(ch) }
}

/** 页面和期望的差异（空数组表示一致）。`rows` 是 {行键: 期望的行}。 */
function diff(actual, want) {
  const out = []
  for (const [k, v] of Object.entries(want.flow ?? {})) if (actual.flow[k] !== v) out.push(`总数 ${k}: 页面 ${actual.flow[k]}，接口 ${v}`)
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
  await page.waitForSelector('[data-testid=flow-step]')
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
        const c = root.counts
        console.log(
          `  五个数和接口一致（${ms} ms）：已上传 ${c.total} 页；通过 ${c.review.approved}（待分配 ${c.review.unassigned}、待审 ${c.review.pending}、` +
            `驳回 ${c.review.rejected}）；已归档 ${c.items.total} 件 ${c.catalog.cataloged} 页；已锁定 ${c.items.locked} / ${c.items.total} 件；` +
            `可导出 ${c.items.exportable} 件 ${c.items.exportable_pages} 页`,
        )
        // 可导出的小字按 require_locked_item。
        const note = await page.$eval('[data-testid=flow-step][data-kind=exportable]', (el) => el.textContent)
        const want = root.require_locked_item ? '已锁定且全部页审核通过' : '已归档且全部页审核通过'
        assert(note.includes(want), `可导出的小字应是"${want}"，页面是：${note}`)
        // 第一层每个文件夹的行也对。
        const rows = Object.fromEntries(root.children.map((ch) => [`folder:${ch.folder_id}`, childRow(ch)]))
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
        const { top, box1, box3 } = ctx.demo.folders
        await toggleRow(page, top.id, true)
        await toggleRow(page, box1.id, true)
        await toggleRow(page, box3.id, true)
        const s1 = await summary(ctx, top.id)
        const sb1 = await summary(ctx, box1.id)
        const s3 = await summary(ctx, box3.id)
        const rows = {
          ...(await childRowOf(ctx, null, top.id)),
          ...Object.fromEntries([...s1.children, ...sb1.children, ...s3.children].map((ch) => [`folder:${ch.folder_id}`, childRow(ch)])),
          [`direct:${box3.id}`]: rowOf(s3.direct_counts),
        }
        await page.waitForSelector(`[data-testid=folder-row][data-kind=direct][data-folder-id="${box3.id}"]`, { timeout: 5000 })
        const actual = await readPage(page)
        const d = diff(actual, { rows })
        assert(d.length === 0, `展开后的行和接口不一致：${d.slice(0, 5).join('；')}`)
        assert(actual.rows[`folder:${ctx.demo.folders.extra.id}`]?.depth === 2, `「${EXTRA}」应该在第 3 层（depth 2）`)
        const archivedRows = sb1.children.map((ch) => actual.rows[`folder:${ch.folder_id}`]).filter((r) => r?.archiveNo)
        assert(archivedRows.length > 0 && archivedRows.some((r) => r.archiveLocked === 'true'), `${BOX1} 下应有写档号的行、其中有已锁定的`)
        console.log(
          `  展开「${M37_FOLDER}」「${BOX1}」「${BOX}」：${Object.keys(rows).length} 行和接口一致；${M37_FOLDER} 共 ${s1.counts.total} 页，` +
            `已归档 ${s1.counts.items.total} 件 ${s1.counts.catalog.cataloged} 页、已锁定 ${s1.counts.items.locked} 件；` +
            `${BOX1} 下写档号的 ${archivedRows.map((r) => `${r.archiveNo}${r.archiveLocked === 'true' ? '（已锁定）' : ''}`).join('、')}`,
        )
        // 截图和 Mockup v2 一样：展开「2025 文书档案」「盒 WS-2025-001」。
        await toggleRow(page, box3.id, false)
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
        const tempRow = (c) => childRowOf(c, null, temp.id)
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
        console.log(`  页面之外驳回子文件夹里的 1 页（子文件夹那一层没加载）：${ms} ms 后五个数、临时文件夹那一行和接口一致`)

        // 展开临时文件夹后通过 2 页（一页在顶层、一页在子文件夹里）：展开的各行也要跟上。
        await toggleRow(page, temp.id, true)
        await ctx.rev('POST', `/api/projects/${ctx.pid}/reviews:approve`, { file_ids: [temp.files[0].file_id, temp.subFiles[0].file_id] })
        const rowsExpanded = async (c) => ({
          ...(await tempRow(c)),
          ...(await childRowOf(c, temp.id, temp.sub.id)),
          [`direct:${temp.id}`]: rowOf((await summary(c, temp.id)).direct_counts),
        })
        ms = await waitConsistent(page, ctx, { rowsFor: rowsExpanded })
        assert(ms <= LIVE_MS, `通过之后 ${ms} ms 才更新`)
        console.log(`  页面之外通过 2 页（展开着）：${ms} ms 后五个数、临时文件夹、子文件夹和"直接放在这里的文件"的行都和接口一致`)
        const t = (await readPage(page)).rows[`folder:${temp.id}`]
        assert(t.approved === 2 && t.total === 4 && t.items === 0, `临时文件夹那一行应为 4 页、通过 2、没有归档，页面是 ${JSON.stringify(t)}`)
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
        const tempRow = (c) => childRowOf(c, null, temp.id)
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
        assert(
          after1.items.total === before.items.total + 1 && after1.catalog.cataloged === before.catalog.cataloged + 3,
          `已归档应该 +1 件 +3 页（${before.items.total} 件 ${before.catalog.cataloged} 页 → ${after1.items.total} 件 ${after1.catalog.cataloged} 页）`,
        )
        const row1 = (await readPage(page)).rows[`folder:${temp.id}`]
        assert(row1.archiveNo === it.archive_no && row1.archiveLocked === 'false', `临时文件夹那一行应写档号 ${it.archive_no}：${JSON.stringify(row1)}`)
        console.log(
          `  经接口把临时文件夹整个归档（3 页）：${ms} ms 后已归档 ${before.items.total} 件 ${before.catalog.cataloged} 页 → ` +
            `${after1.items.total} 件 ${after1.catalog.cataloged} 页，那一行写档号 ${row1.archiveNo}，和接口一致`,
        )

        // 锁定要求必填著录合格、全部页审核通过（DEMO-A 的 lock_requires_approved 是默认 true）。
        it = await ctx.admin('PATCH', `/api/archive-items/${it.id}`, { row_version: it.row_version, extra_fields: await requiredFieldValues(ctx.admin, ctx.pid) })
        const three = temp.files.map((f) => f.file_id)
        await ctx.admin('POST', `/api/projects/${ctx.pid}/review-assignments`, { reviewer_id: ctx.reviewerId, file_ids: three })
        await ctx.rev('POST', `/api/projects/${ctx.pid}/reviews:approve`, { file_ids: three })
        ms = await waitConsistent(page, ctx, { rowsFor: tempRow })
        const beforeLock = (await summary(ctx, null)).counts
        assert(beforeLock.items.exportable === before.items.exportable, '没锁定时审核通过不改"可导出"（要求锁定）')
        console.log(`  这 3 页分给 demo_reviewer 并通过：${ms} ms 后和接口一致；审核通过 ${before.review.approved} → ${beforeLock.review.approved}`)

        it = await ctx.admin('POST', `/api/archive-items/${it.id}/lock`, { row_version: it.row_version })
        assert(it.status === 'locked', `条目应已锁定，现在是 ${it.status}`)
        ms = await waitConsistent(page, ctx, { rowsFor: tempRow })
        assert(ms <= LIVE_MS, `锁定之后 ${ms} ms 才更新`)
        const after2 = (await summary(ctx, null)).counts
        assert(after2.items.locked === before.items.locked + 1, '已锁定应该 +1 件')
        assert(
          after2.items.exportable === before.items.exportable + 1 && after2.items.exportable_pages === before.items.exportable_pages + 3,
          '可导出应该 +1 件 +3 页（这 3 页审核通过了）',
        )
        assert((await readPage(page)).rows[`folder:${temp.id}`].archiveLocked === 'true', '临时文件夹那一行应写"已锁定"')
        console.log(
          `  锁定 + catalog.changed：${ms} ms；已锁定 ${before.items.locked} → ${after2.items.locked} 件，` +
            `可导出 ${before.items.exportable} 件 ${before.items.exportable_pages} 页 → ${after2.items.exportable} 件 ${after2.items.exportable_pages} 页`,
        )

        // 解锁、解除封存：那一行带"修改中"标记，可导出回到原样（修改中的导出预检不就绪）。
        it = await ctx.admin('POST', `/api/archive-items/${it.id}/unlock`, { row_version: it.row_version })
        it = await ctx.admin('POST', `/api/archive-items/${it.id}/unseal`, { row_version: it.row_version, reason: 'M3-7 截图脚本：看板的修改中标记' })
        assert(it.source_status === 'modifying', `条目应在修改中，现在是 ${it.source_status}`)
        ms = await waitConsistent(page, ctx, { rowsFor: tempRow })
        assert(ms <= LIVE_MS, `解除封存之后 ${ms} ms 才更新`)
        const after3 = (await summary(ctx, null)).counts
        assert(after3.items.modifying === before.items.modifying + 1 && after3.items.exportable === before.items.exportable, '修改中 +1 件、可导出回到原样')
        assert(await page.$(`[data-testid=folder-row][data-folder-id="${temp.id}"] [data-testid=archive-modifying]`), '临时文件夹那一行应带"修改中"标记')
        console.log(`  解锁、解除封存：${ms} ms；那一行带"修改中"标记，修改中 ${after3.items.modifying} 件，可导出回到 ${after3.items.exportable} 件`)
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
