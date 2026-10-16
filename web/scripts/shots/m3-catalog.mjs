// M3-5 的截图清单和断言：归类页 /catalog（归类树和条目著录）。
//
//   INIT_ADMIN_PASSWORD=admin123456 \
//     npm run screenshots -- --spec scripts/shots/m3-catalog.mjs --base http://localhost:<preview 端口> [--only …]
//
// 前端地址是 vite preview（/api 代理到 server）或托管了 web/dist 的 server。演示数据用 scripts/lib/m3-demo.mjs 的
// ensureM35Demo 建好（常驻，没有就建：DEMO-A 的著录项、demo_cataloger 的授权、文件夹「M3-5 归类演示」12 页、全宗
// Z201 的归类树和 8 个条目）。挂接在 M3-3 合入前是直接写库造的，M3-6 起一律经 PUT 接口（m3-demo 的 linkPages）。每张
// 截图自己造的临时数据（节点、条目、设置的改动）在 after 里还原或删掉：临时条目经 M3-3 的删除接口（已定稿的先解锁；
// M3-6 去掉了直接写库的兜底），每一步各自兜住错误，前面失败了也执行。
// 截图默认放 ../docs/screenshots/m3（--out 可改）。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - m3-catalog-tree（demo_cataloger）：归类树每个节点显示编码和子树里的条目数，和接口一致；办公室 BGS 的条目列表
//   （件号、题名、状态、页数、"1 待审"）和接口一致；打开第 3 件，表单按 catalog-settings 动态生成（5 个著录项、类型对）。
// - m3-catalog-form-errors（admin）：草稿条目里把日期改成 2025-02-30、题名清空，点"标记完成" → 本地标出日期和题名，
//   不发请求。
// - m3-catalog-form-server-error（admin）：页面打开之后在页面之外把"文号"的最大长度改成 4（改设置不发事件，页面上的设置
//   是旧的）→ 填 10 个字的文号保存 → server 400 `invalid_input` 的 details.fields 标到"文号"一项；保存失败后页面重取了
//   catalog-settings；条目没有变。
// - m3-catalog-old-values（admin）：第 5 件存了密级"秘密"和备注，之后在设置里把"秘密"从选项里删掉、把"备注"整项删掉
//   → 表单的下拉里显示"秘密（旧值，不在选项里）"和提示，"项目设置里已经删掉的著录项"一栏列出备注 → 只改题名保存 →
//   接口核对密级、备注原样保留（不悄悄丢掉，用户决定 Q1）。
// - m3-catalog-locked（demo_cataloger，只有 catalog 菜单）：已定稿的第 1 件表单只读、没有"解锁"按钮、页头写"联系项目
//   负责人解锁"；接口直接调解锁是 403。另核对 admin 能看到"解锁"。
// - m3-catalog-unlock（demo_manager）：看得到"解锁"→ 确认 → 回到"已完成"，接口核对；after 里重新定稿。
// - m3-catalog-code-change（admin）：在界面上把"人事处"的编码 RSC 改成 RS2 → 提示重新生成了 2 个档号；3 秒内详情、
//   列表、树上的档号和编码都更新，和接口一致；after 里改回 RSC。
// - m3-catalog-conflict（admin）：临时机构 M35TMP 下的条目 A（第 1 件）和它下面自定义节点 C1 里的条目 B（也是第 1 件，
//   模板里没有自定义这一级）撞号 → B 的档号为空、原因"撞号"，"查看占用的条目"跳到 A；A 改件号 9 之后 B 的原因变成
//   "未生成"，点"按新模板重新生成"后 B 有了档号；在 A 上把件号改回 1 → 409 提示换一个号；再建 C2 里的条目 E（撞 B），
//   在界面上改 C2 的编码 → 提示"有 1 个条目因为撞号档号为空"。临时节点、条目最后删掉。
// - m3-catalog-sync（admin + 另一个窗口 demo_manager）：另一个窗口改第 3 件的题名并保存 → 本窗口 3 秒内列表和详情
//   都更新；另一个窗口新建条目 → 本窗口树上"人事处"的条目数 3 秒内加 1；本窗口改着文号时另一个窗口改了题名 →
//   本窗口出现"别人刚修改了"，保存得到 409 → 重新加载、文号保留、题名换成别人的 → 再保存成功，两边的修改都在。
// - m3-catalog-batch-lock（admin）：勾选第 2 件（1 页待审）和第 3 件（草稿）批量定稿 → 409，两行标红，状态都没变。
// - m3-catalog-category-ops（admin）：有条目的全宗 Z201 点删除 → 直接提示"不能删除"；在界面上新建顶层全宗 M35OPS、
//   它下面的门类（编码含模板分隔符时本地提示），改名、改类型，移到顶层，删除（工具栏和右键菜单各一次）；每一步接口核对。
// - m3-catalog-files-event（admin）：临时文件夹里传 2 页，挂到临时条目上（M3-6 起经 PUT 接口），1 页分给
//   demo_reviewer。页面上列表显示"1 待审"、页序是"待审"；demo_reviewer 在页面之外通过 → 3 秒内列表的"1 待审"消失、
//   页序变成"通过"（files.changed）。最后移出、删掉临时条目和文件夹。
// - m3-catalog-reconnect（admin）：实时连接断开（派发 pagehide）期间在页面之外改题名、建条目，页面不变 → 重新连上
//   （派发 persisted 的 pageshow，和断线重连一样走 onOpen(false)）后整体刷新：详情、列表、树上的条目数都更新。
// 审查第 1 轮补的：
// - m3-catalog-old-values 里：密级改成"公开"之后旧值"秘密"还留在下拉里，改回去就没有修改。
// - m3-catalog-events（admin）：不相干的分支展开再收起，在它的下级上改名 5 次 → 不重取列表、详情，树请求不超过 2 个；
//   树请求推迟 800 ms、第一轮在路上时又来两次事件 → 同时在途最多 1 个、一共 2 轮（在途合并）；另记下页面之外新建
//   顶层节点（树上不认识）时的请求数。
// - m3-catalog-required-stale（admin）：页面打开后在页面之外把"文号"改成必填 → 标记完成被 400 拒绝 → 文号上的错误
//   标记在页面重取设置之后仍在。
// - m3-catalog-root-items（admin）：用接口在根节点下建条目 → 树顶出现"（直接放在顶层的条目）n 件"，能打开、看详情，
//   移到临时节点后那一行消失（另存 m3-catalog-root-items-open.png）。
// - m3-catalog-move-reveal（admin）：没展开过的分支下的节点移到另一个没展开过的分支 → 树展开到新位置、能看到它。
// - m3-catalog-move-gone（admin）："移到别的节点"选好目标后目标被删 → 提示"目标节点已不存在"，详情保留、条目没动。
// - m3-catalog-switch-guard（admin）：有未保存的著录时在切换器里换项目 → 先确认（另存 m3-catalog-switch-guard-confirm.png）；
//   取消留在原项目、修改还在；确认放弃后换过去；after 里换回 DEMO-A。
// - m3-catalog-virtual（admin）：常驻节点 10 年 / VS「M3-5 虚拟滚动（常驻）」（不够 1200 个条目时补足，审查第 1 轮改的，
//   原来每次建 1200 个再软删除）→ 列表第一页 500 个，滚到中间时 DOM 里的条目行少于 200 个，滚到底自动加载到 1200 个。

import { mkdirSync } from 'node:fs'

import {
  apiLogin,
  deleteItemsForCleanup,
  ensureM35Demo,
  ensureM35Virtual,
  interceptRequests,
  linkPages,
  M35_ITEM_FIELDS,
  M35_VIRTUAL_COUNT,
  stamp,
  storedSettings,
  unlinkAllForCleanup,
  uploadPage,
} from '../lib/m3-demo.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'
const CATALOGER = 'demo_cataloger'
const MANAGER = 'demo_manager'
const IDLE = 'networkidle2'
// 另存的截图和清单的截图放在同一个目录：--out，默认 docs/screenshots/m3。
const OUT = (() => {
  const i = process.argv.indexOf('--out')
  return i > 0 ? process.argv[i + 1] : '../docs/screenshots/m3'
})()

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

function assert(cond, message) {
  if (!cond) throw new Error(`断言失败：${message}`)
}

let ctxPromise = null
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const api = await apiLogin(origin, admin, adminPassword)
    const demo = await ensureM35Demo({ origin, admin: api, demoPassword })
    mkdirSync(OUT, { recursive: true })
    const item = (category, no) => demo.items.find((i) => i.spec.category.id === category.id && i.item_no === no)
    return { origin, api, demo, pid: demo.pid, c: demo.categories, item }
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

async function openCatalog(page, categoryId, itemId) {
  acceptDialogs(page)
  const ctx = await context(page)
  const q = new URLSearchParams()
  if (categoryId) q.set('category', categoryId)
  if (itemId) q.set('item', itemId)
  await page.goto(`${ctx.origin}/catalog?${q}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=catalog-page][data-tree=ready]', { timeout: 10000 })
  if (categoryId) await page.waitForSelector('[data-testid=catalog-page][data-list=ready]', { timeout: 10000 })
  if (itemId) await waitDetail(page, itemId)
  await page.waitForFunction(() => document.querySelector('[data-testid=catalog-page]')?.dataset.stream === 'open', { timeout: 10000 })
}

async function waitDetail(page, itemId) {
  await page.waitForSelector(`[data-testid=item-detail][data-item-id="${itemId}"][data-load=ready]`, { timeout: 10000 })
}

/** 给表单项设值：input、textarea 发 input 事件，select 用 page.select。 */
async function setField(page, key, value) {
  const sel = key === 'title' ? '[data-testid=item-title]' : `[data-testid=item-field][data-key="${key}"] [data-testid=item-field-input]`
  const tag = await page.$eval(sel, (el) => el.tagName)
  if (tag === 'SELECT') {
    await page.select(sel, value)
    return
  }
  await page.$eval(
    sel,
    (el, v) => {
      el.value = v
      el.dispatchEvent(new Event('input', { bubbles: true }))
    },
    value,
  )
}

async function waitToast(page, text, timeout = 8000) {
  await page.waitForFunction(
    (t) => [...document.querySelectorAll('[data-sonner-toast]')].some((el) => el.textContent?.includes(t)),
    { timeout },
    text,
  )
}

async function toastsText(page) {
  return page.$$eval('[data-sonner-toast]', (els) => els.map((e) => e.textContent?.trim() ?? '').join(' | '))
}

async function confirmAlert(page) {
  await page.waitForSelector('[role=alertdialog]', { visible: true, timeout: 5000 })
  await sleep(200)
  const buttons = await page.$$('[role=alertdialog] button')
  await buttons[buttons.length - 1].click()
  await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 10000 })
}

async function rowVersion(page) {
  return page.$eval('[data-testid=item-detail]', (el) => Number(el.dataset.rowVersion))
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

async function getItem(ctx, id) {
  return ctx.api('GET', `/api/archive-items/${id}`)
}

async function listItems(ctx, categoryId) {
  return (await ctx.api('GET', `/api/archive-categories/${categoryId}/items?limit=2000`)).items
}

async function setItemFields(ctx, fields) {
  const stored = storedSettings(ctx.pid)
  await ctx.api('PATCH', `/api/system/projects/${ctx.pid}`, { settings: { ...stored, item_fields: fields } })
}

/** 临时节点、条目、文件夹（after 里删掉）。每一步各自兜住错误，一步失败不影响后面的（不留下临时节点）。 */
const temps = { items: [], categories: [], folders: [], linked: [] }
async function cleanupTemps(ctx) {
  const step = async (what, fn) => {
    try {
      await fn()
    } catch (e) {
      console.warn(`  清理 ${what} 出错：${e.message}`)
    }
  }
  for (const id of temps.linked.splice(0)) await step(`条目 ${id} 的页`, () => unlinkAllForCleanup(ctx.api, id))
  const items = temps.items.splice(0)
  await step(`${items.length} 个条目`, () => deleteItemsForCleanup(ctx.api, items))
  for (const id of temps.folders.splice(0)) {
    await step(`文件夹 ${id}`, () => ctx.api('DELETE', `/api/folders/${id}`, undefined, { allow: [404] }))
  }
  // 子节点先删（倒序）；已经随父节点删掉的 404 不算错。
  for (const id of temps.categories.splice(0).reverse()) {
    await step(`节点 ${id}`, () => ctx.api('DELETE', `/api/projects/${ctx.pid}/archive-categories/${id}`, undefined, { allow: [404] }))
  }
}

async function tempCategory(ctx, parentId, type, code, name) {
  const node = await ctx.api('POST', `/api/projects/${ctx.pid}/archive-categories`, {
    parent_id: parentId,
    category_type: type,
    category_code: code,
    category_name: name,
  })
  temps.categories.push(node.id)
  return node
}

async function tempItem(ctx, categoryId, body = {}) {
  const it = await ctx.api('POST', `/api/archive-categories/${categoryId}/items`, body)
  temps.items.push(it.id)
  return it
}

export default {
  out: OUT,
  accounts: { [admin]: adminPassword, [CATALOGER]: demoPassword, [MANAGER]: demoPassword },
  waitUntil: IDLE,
  shots: [
    // ---------------------------------------------------------------- 归类树
    {
      name: 'm3-catalog-tree',
      user: CATALOGER,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it3 = ctx.item(ctx.c.bgs, 3)
        await openCatalog(page, ctx.c.bgs.id, it3.id)
        // 树：每个已显示的节点的编码、条目数和接口一致。
        const rows = await page.$$eval('[data-testid=tree-category]', (els) =>
          els.map((e) => ({ id: e.dataset.categoryId, code: e.dataset.code, items: Number(e.dataset.items), text: e.textContent })),
        )
        const want = new Map()
        for (const parent of [null, ctx.c.fonds.id, ctx.c.ws.id, ctx.c.year.id, ctx.c.y.id]) {
          const q = parent ? `?parent_id=${parent}` : ''
          for (const n of (await ctx.api('GET', `/api/projects/${ctx.pid}/archive-categories${q}`)).items) want.set(n.id, n)
        }
        for (const r of rows) {
          const n = want.get(r.id)
          assert(n, `树上的节点 ${r.code} 不在接口返回里`)
          assert(r.code === n.category_code && r.items === n.item_count, `节点 ${n.category_name}：编码 ${r.code}/${n.category_code}，条目数 ${r.items}/${n.item_count}`)
          assert(r.text.includes(n.category_code) && (n.item_count === 0 || r.text.includes(`${n.item_count.toLocaleString('zh-CN')} 件`)), `节点 ${n.category_name} 没显示编码或条目数：${r.text}`)
        }
        for (const id of [ctx.c.fonds.id, ctx.c.ws.id, ctx.c.year.id, ctx.c.y.id, ctx.c.bgs.id, ctx.c.rsc.id, ctx.c.d30.id, ctx.c.d10.id]) {
          assert(rows.some((r) => r.id === id), `树上没展开到演示节点 ${want.get(id)?.category_name}`)
        }
        // 列表：件号、题名、状态、页数、待审，和接口一致。
        const apiItems = await listItems(ctx, ctx.c.bgs.id)
        const listRows = await page.$$eval('[data-testid=item-row]', (els) =>
          els.map((e) => ({ id: e.dataset.itemId, no: Number(e.dataset.itemNo), status: e.dataset.status, pages: Number(e.dataset.pages), pending: Number(e.dataset.pending), text: e.textContent })),
        )
        assert(listRows.length === apiItems.length, `列表 ${listRows.length} 行，接口 ${apiItems.length} 个`)
        for (const a of apiItems) {
          const r = listRows.find((x) => x.id === a.id)
          assert(r && r.no === a.item_no && r.status === a.status && r.pages === a.page_count && r.pending === a.page_counts.pending, `第 ${a.item_no} 件和接口不一致`)
          assert(r.text.includes(a.title || '（未填题名）'), `第 ${a.item_no} 件没显示题名`)
          if (a.page_counts.pending) assert(r.text.includes(`${a.page_counts.pending} 待审`), `第 ${a.item_no} 件没显示"n 待审"`)
        }
        // 表单：按 catalog-settings 动态生成。
        const settings = await ctx.api('GET', `/api/projects/${ctx.pid}/catalog-settings`)
        const fields = await page.$$eval('[data-testid=item-field]', (els) => els.map((e) => ({ key: e.dataset.key, type: e.dataset.type ?? null })))
        const expect = [{ key: 'title', type: null }, ...settings.item_fields.map((f) => ({ key: f.key, type: f.type }))]
        assert(JSON.stringify(fields) === JSON.stringify(expect), `表单项不对：${JSON.stringify(fields)}`)
        const archiveNo = await page.$eval('[data-testid=item-archive-no]', (el) => el.textContent.trim())
        assert(archiveNo === it3.archive_no, `档号显示 ${archiveNo}，应为 ${it3.archive_no}`)
        assert(await page.$('[data-testid=item-pages] [data-testid=item-page]'), '页序区没有页')
        console.log(`  树 ${rows.length} 个节点、列表 ${listRows.length} 行和接口一致；表单 ${fields.length} 项（${fields.map((f) => f.key).join('、')}）`)
        await sleep(600)
      },
    },

    // ---------------------------------------------------------------- 动态表单和校验错误
    {
      name: 'm3-catalog-form-errors',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it3 = ctx.item(ctx.c.bgs, 3)
        await openCatalog(page, ctx.c.bgs.id, it3.id)
        const before = await getItem(ctx, it3.id)
        let writes = 0
        const onReq = (req) => {
          if (/\/api\/archive-items\//.test(req.url()) && req.method() !== 'GET') writes++
        }
        page.on('request', onReq)
        try {
          await setField(page, 'doc_date', '2025-02-30')
          await setField(page, 'title', '')
          await page.click('[data-testid=item-mark-done]')
          await page.waitForSelector('[data-testid=item-field][data-key=doc_date][data-error]', { timeout: 3000 })
          await page.waitForSelector('[data-testid=item-field][data-key=title][data-error]', { timeout: 3000 })
          const errs = await page.$$eval('[data-testid=item-field][data-error]', (els) => Object.fromEntries(els.map((e) => [e.dataset.key, e.dataset.error])))
          assert(errs.doc_date?.includes('不是合法的日期'), `日期的提示不对：${errs.doc_date}`)
          assert(errs.title?.includes('题名不能为空'), `题名的提示不对：${errs.title}`)
          await sleep(500)
          assert(writes === 0, `本地校验不过还发了 ${writes} 个写请求`)
        } finally {
          page.off('request', onReq)
        }
        const after = await getItem(ctx, it3.id)
        assert(after.row_version === before.row_version, '条目被改了')
        console.log('  本地标出：日期 2025-02-30 不是合法的日期、题名不能为空；没有发请求')
      },
    },
    {
      name: 'm3-catalog-form-server-error',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it3 = ctx.item(ctx.c.bgs, 3)
        await openCatalog(page, ctx.c.bgs.id, it3.id)
        const before = await getItem(ctx, it3.id)
        // 页面打开之后改设置（不发事件）：文号最长 4 个字。
        await setItemFields(ctx, M35_ITEM_FIELDS.map((f) => (f.key === 'doc_no' ? { ...f, max_length: 4 } : f)))
        let settingsFetches = 0
        const onReq = (req) => {
          if (req.url().includes('/catalog-settings')) settingsFetches++
        }
        page.on('request', onReq)
        try {
          await setField(page, 'doc_no', '档办〔2025〕99 号')
          await page.click('[data-testid=item-save]')
          await page.waitForSelector('[data-testid=item-field][data-key=doc_no][data-error]', { timeout: 5000 })
          const err = await page.$eval('[data-testid=item-field][data-key=doc_no]', (el) => el.dataset.error)
          assert(err.includes('文号最长 4 个字符'), `文号的提示应来自 server（最长 4 个字符），是：${err}`)
          await waitToast(page, '著录内容不对')
          await page.waitForFunction(() => true)
          await sleep(800)
          assert(settingsFetches >= 1, '保存失败（400）后没有重取 catalog-settings')
        } finally {
          page.off('request', onReq)
        }
        const after = await getItem(ctx, it3.id)
        assert(after.row_version === before.row_version && after.extra_fields.doc_no === before.extra_fields.doc_no, '条目被改了')
        console.log(`  server 400 标到"文号"：${await page.$eval('[data-testid=item-field][data-key=doc_no]', (el) => el.dataset.error)}；之后重取了 ${settingsFetches} 次设置`)
      },
      async after(page) {
        const ctx = await context(page)
        await setItemFields(ctx, M35_ITEM_FIELDS)
      },
    },

    // ---------------------------------------------------------------- 旧值（Q1）
    {
      name: 'm3-catalog-old-values',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it5 = ctx.item(ctx.c.bgs, 5)
        let cur = await getItem(ctx, it5.id)
        cur = await ctx.api('PATCH', `/api/archive-items/${it5.id}`, {
          row_version: cur.row_version,
          extra_fields: { secret: '秘密', remark: '原备注：第 12 期会议纪要（旧）' },
        })
        // 改设置：密级去掉"秘密"，备注整项删掉。
        await setItemFields(
          ctx,
          M35_ITEM_FIELDS.filter((f) => f.key !== 'remark').map((f) => (f.key === 'secret' ? { ...f, options: ['公开', '内部'] } : f)),
        )
        await openCatalog(page, ctx.c.bgs.id, it5.id)
        const sel = '[data-testid=item-field][data-key=secret] select'
        const shown = await page.$eval(sel, (el) => ({ value: el.value, text: el.selectedOptions[0]?.textContent ?? '' }))
        assert(shown.value === '秘密' && shown.text.includes('旧值'), `密级应显示旧值"秘密"：${JSON.stringify(shown)}`)
        assert(await page.$('[data-testid=item-field][data-key=secret] [data-testid=field-issue]'), '密级没有旧值提示')
        const orphan = await page.$eval('[data-testid=item-orphan-field][data-key=remark]', (el) => el.textContent)
        assert(orphan.includes('原备注：第 12 期会议纪要（旧）'), `已删除的著录项"备注"没显示原值：${orphan}`)
        await page.screenshot({ path: `${OUT}/m3-catalog-old-values-form.png` })
        // 改成"公开"之后旧值还留在下拉里（能改回来）；改回"秘密"就没有修改（M3-5 审查）。
        await page.select(sel, '公开')
        const kept = await page.$eval(sel, (el) => [...el.options].some((o) => o.value === '秘密' && o.textContent.includes('旧值')))
        assert(kept, '改走以后旧值"秘密"从下拉里消失了')
        await page.select(sel, '秘密')
        await page.waitForFunction(() => !document.querySelector('[data-testid=item-detail]')?.dataset.dirty, { timeout: 2000 })
        // 只改题名保存：旧值原样保留。
        const v0 = await rowVersion(page)
        await setField(page, 'title', `${it5.spec.title}（修订）`)
        await page.click('[data-testid=item-save]')
        await page.waitForFunction((v) => Number(document.querySelector('[data-testid=item-detail]')?.dataset.rowVersion) > v, { timeout: 5000 }, v0)
        const saved = await getItem(ctx, it5.id)
        assert(saved.title === `${it5.spec.title}（修订）`, `题名没保存：${saved.title}`)
        assert(saved.extra_fields.secret === '秘密', `密级的旧值丢了：${saved.extra_fields.secret}`)
        assert(saved.extra_fields.remark === '原备注：第 12 期会议纪要（旧）', `备注的旧值丢了：${saved.extra_fields.remark}`)
        assert(saved.field_issues.some((i) => i.key === 'secret') && saved.field_issues.some((i) => i.key === 'remark'), 'field_issues 应列出密级、备注')
        console.log('  旧值显示：密级"秘密（旧值，不在选项里）"、已删除的"备注"；只改题名保存后两者原样保留')
        await sleep(400)
      },
      async after(page) {
        const ctx = await context(page)
        const it5 = ctx.item(ctx.c.bgs, 5)
        await setItemFields(ctx, M35_ITEM_FIELDS)
        const cur = await getItem(ctx, it5.id)
        await ctx.api('PATCH', `/api/archive-items/${it5.id}`, {
          row_version: cur.row_version,
          title: it5.spec.title,
          extra_fields: { secret: '内部', remark: null },
        })
      },
    },

    // ---------------------------------------------------------------- 定稿后只读、解锁
    {
      name: 'm3-catalog-locked',
      user: CATALOGER,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it1 = ctx.item(ctx.c.bgs, 1)
        await openCatalog(page, ctx.c.bgs.id, it1.id)
        const st = await page.$eval('[data-testid=item-detail]', (el) => el.dataset.status)
        assert(st === 'locked', `第 1 件应是已定稿，是 ${st}`)
        assert(!(await page.$('[data-testid=item-unlock]')), '只有 catalog 菜单的人看到了"解锁"')
        assert(await page.$eval('[data-testid=item-title]', (el) => el.disabled), '定稿后题名应只读')
        const disabled = await page.$$eval('[data-testid=item-field-input]', (els) => els.every((e) => e.disabled))
        assert(disabled, '定稿后著录项应只读')
        for (const t of ['item-save', 'item-move', 'item-no-edit', 'item-mark-done', 'item-lock']) {
          assert(!(await page.$(`[data-testid=${t}]`)), `定稿后不应有 ${t}`)
        }
        const note = await page.$eval('[data-testid=item-locked-note]', (el) => el.textContent)
        assert(note.includes('联系项目负责人解锁'), `页头没写"联系项目负责人解锁"：${note}`)
        const cat = await apiLogin(ctx.origin, CATALOGER, demoPassword)
        const r = await cat('POST', `/api/archive-items/${it1.id}/unlock`, { row_version: (await getItem(ctx, it1.id)).row_version }, { allow: [403] })
        assert(r?.status === 403, `只有 catalog 菜单的人调解锁应是 403，是 ${JSON.stringify(r)}`)
        console.log('  demo_cataloger：表单只读、没有"解锁"，接口解锁 403')
      },
    },
    {
      name: 'm3-catalog-unlock',
      user: MANAGER,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it1 = ctx.item(ctx.c.bgs, 1)
        await openCatalog(page, ctx.c.bgs.id, it1.id)
        assert(await page.$('[data-testid=item-unlock]'), '负责人看不到"解锁"')
        await page.click('[data-testid=item-unlock]')
        await confirmAlert(page)
        await page.waitForSelector('[data-testid=item-detail][data-status=done]', { timeout: 5000 })
        await waitToast(page, '已解锁')
        const after = await getItem(ctx, it1.id)
        assert(after.status === 'done', `解锁后应是 done，是 ${after.status}`)
        await page.waitForSelector(`[data-testid=item-row][data-item-id="${it1.id}"][data-status=done]`, { timeout: 3000 })
        assert(!(await page.$eval('[data-testid=item-title]', (el) => el.disabled)), '解锁后题名应能改')
        console.log('  demo_manager 解锁：第 1 件回到"已完成"，表单可以编辑')
        await sleep(300)
      },
      async after(page) {
        const ctx = await context(page)
        const it1 = ctx.item(ctx.c.bgs, 1)
        const cur = await getItem(ctx, it1.id)
        if (cur.status === 'done') await ctx.api('POST', `/api/archive-items/${it1.id}/lock`, { row_version: cur.row_version })
      },
    },

    // ---------------------------------------------------------------- 改编码后档号更新
    {
      name: 'm3-catalog-code-change',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        // admin 能看到"解锁"（admin 有全部菜单）。
        const it1 = ctx.item(ctx.c.bgs, 1)
        await openCatalog(page, ctx.c.bgs.id, it1.id)
        assert(await page.$('[data-testid=item-unlock]'), 'admin 看不到"解锁"')
        console.log('  admin 能看到"解锁"')

        const r1 = ctx.item(ctx.c.rsc, 1)
        await openCatalog(page, ctx.c.rsc.id, r1.id)
        await page.click('[data-testid=category-edit]')
        await page.waitForSelector('[data-testid=category-form]', { visible: true })
        await page.$eval('[data-testid=category-form-code]', (el) => {
          el.value = 'RS2'
          el.dispatchEvent(new Event('input', { bubbles: true }))
        })
        await page.waitForSelector('[data-testid=category-form-renumber-hint]')
        const t0 = Date.now()
        await page.click('[data-testid=category-form-submit]')
        await waitToast(page, '重新生成了 2 个档号')
        const expectNo = 'Z201-WS·2025-Y-RS2-0001'
        await page.waitForFunction(
          (id, no) =>
            document.querySelector('[data-testid=item-archive-no]')?.textContent?.trim() === no &&
            document.querySelector(`[data-testid=item-row][data-item-id="${id}"]`)?.dataset.archiveNo === no &&
            document.querySelector(`[data-testid=tree-category][data-category-id]`) &&
            [...document.querySelectorAll('[data-testid=tree-category]')].some((e) => e.dataset.code === 'RS2'),
          { timeout: 3000, polling: 50 },
          r1.id,
          expectNo,
        )
        const ms = Date.now() - t0
        const api = await listItems(ctx, ctx.c.rsc.id)
        assert(api.every((i) => i.archive_no === `Z201-WS·2025-Y-RS2-${String(i.item_no).padStart(4, '0')}`), `接口里的档号没更新：${api.map((i) => i.archive_no)}`)
        const rows = await page.$$eval('[data-testid=item-row]', (els) => els.map((e) => e.dataset.archiveNo))
        assert(JSON.stringify(rows) === JSON.stringify(api.map((i) => i.archive_no)), `列表档号和接口不一致：${rows}`)
        console.log(`  改编码 RSC → RS2：${ms} ms 内详情、列表、树都更新；接口档号 ${api.map((i) => i.archive_no).join('、')}`)
        await sleep(300)
      },
      async after(page) {
        const ctx = await context(page)
        await ctx.api('PATCH', `/api/projects/${ctx.pid}/archive-categories/${ctx.c.rsc.id}`, { category_code: 'RSC' })
      },
    },

    // ---------------------------------------------------------------- 撞号、未生成、改件号 409、节点 PATCH 的撞号提示
    {
      name: 'm3-catalog-conflict',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const org = await tempCategory(ctx, ctx.c.y.id, 'org', 'M35TMP', '临时机构（截图）')
        const c1 = await tempCategory(ctx, org.id, 'custom', 'C1', '补充件 1')
        const a = await tempItem(ctx, org.id, { title: '临时条目 A（占用者）' })
        const b = await tempItem(ctx, c1.id, { title: '临时条目 B（撞号）' })
        assert(a.archive_no === 'Z201-WS·2025-Y-M35TMP-0001', `A 的档号 ${a.archive_no}`)
        assert(b.archive_no === null && b.archive_no_reason?.code === 'conflict', `B 应撞号：${JSON.stringify(b.archive_no_reason)}`)
        await openCatalog(page, c1.id, b.id)
        await page.waitForSelector('[data-testid=archive-no-reason][data-code=conflict]')
        const text = await page.$eval('[data-testid=archive-no-reason]', (el) => el.textContent)
        assert(text.includes('Z201-WS·2025-Y-M35TMP-0001'), `撞号说明没写档号：${text}`)
        await page.screenshot({ path: `${OUT}/m3-catalog-conflict-reason.png` })
        // 跳到占用的条目。
        await page.click('[data-testid=archive-no-conflict-open]')
        await waitDetail(page, a.id)
        const url = new URL(page.url())
        assert(url.searchParams.get('item') === a.id && url.searchParams.get('category') === org.id, `没跳到占用的条目：${url}`)
        console.log('  B 撞号（和 A 的 Z201-WS·2025-Y-M35TMP-0001），"查看占用的条目"跳到 A')

        // A 改件号 9（界面上）→ B 变成"未生成"。
        await page.click('[data-testid=item-no-edit]')
        await page.$eval('[data-testid=item-no-input]', (el) => {
          el.value = '9'
          el.dispatchEvent(new Event('input', { bubbles: true }))
        })
        await page.click('[data-testid=item-no-submit]')
        await page.waitForFunction(() => document.querySelector('[data-testid=item-no]')?.textContent?.trim() === '0009', { timeout: 5000 })
        await openCatalog(page, c1.id, b.id)
        await page.waitForSelector('[data-testid=archive-no-reason][data-code=not_generated]')
        await page.click('[data-testid=archive-no-regenerate]')
        await confirmAlert(page)
        await page.waitForFunction(() => document.querySelector('[data-testid=item-archive-no]')?.textContent?.trim() === 'Z201-WS·2025-Y-M35TMP-0001', { timeout: 5000 })
        const bNow = await getItem(ctx, b.id)
        assert(bNow.archive_no === 'Z201-WS·2025-Y-M35TMP-0001', `重新生成后 B 的档号 ${bNow.archive_no}`)
        console.log('  A 改件号 9 后 B 是"未生成"，点"按新模板重新生成"后 B 有了档号')

        // A 把件号改回 1：档号会和 B 重复 → 409，提示换一个号。
        await openCatalog(page, org.id, a.id)
        await page.click('[data-testid=item-no-edit]')
        await page.$eval('[data-testid=item-no-input]', (el) => {
          el.value = '1'
          el.dispatchEvent(new Event('input', { bubbles: true }))
        })
        await page.click('[data-testid=item-no-submit]')
        await page.waitForSelector('[data-testid=item-no-error]', { timeout: 5000 })
        const noErr = await page.$eval('[data-testid=item-no-error]', (el) => el.textContent.trim())
        assert(noErr.includes('请换一个件号'), `改件号撞号的提示不对：${noErr}`)
        assert((await getItem(ctx, a.id)).item_no === 9, 'A 的件号不应变')
        console.log(`  A 改回第 1 件：${noErr}`)

        // C2 里的条目 E 撞 B；在界面上改 C2 的编码 → 提示"有 1 个条目因为撞号档号为空"。
        const c2 = await tempCategory(ctx, org.id, 'custom', 'C2', '补充件 2')
        const e = await tempItem(ctx, c2.id, { title: '临时条目 E（撞号）' })
        assert(e.archive_no === null, 'E 应撞号')
        await openCatalog(page, c2.id, e.id)
        await page.click('[data-testid=category-edit]')
        await page.waitForSelector('[data-testid=category-form]', { visible: true })
        await page.$eval('[data-testid=category-form-code]', (el) => {
          el.value = 'C3'
          el.dispatchEvent(new Event('input', { bubbles: true }))
        })
        await page.click('[data-testid=category-form-submit]')
        await waitToast(page, '有 1 个条目因为撞号档号为空')
        console.log('  改 C2 的编码：提示"有 1 个条目因为撞号档号为空"')
        await sleep(300)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 另一个窗口同步、409 合并
    {
      name: 'm3-catalog-sync',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it3 = ctx.item(ctx.c.bgs, 3)
        const original = await getItem(ctx, it3.id)
        await openCatalog(page, ctx.c.bgs.id, it3.id)
        const { other, close } = await otherWindow(page, MANAGER, demoPassword)
        try {
          await openCatalog(other, ctx.c.bgs.id, it3.id)
          await page.bringToFront()
          // 1. 另一个窗口改题名 → 本窗口 3 秒内列表和详情更新。
          const newTitle = `${original.title}（另一窗口）`
          await setField(other, 'title', newTitle)
          const t0 = Date.now()
          await other.click('[data-testid=item-save]')
          await page.waitForFunction(
            (id, t) =>
              document.querySelector(`[data-testid=item-row][data-item-id="${id}"]`)?.textContent?.includes(t) &&
              document.querySelector('[data-testid=item-title]')?.value === t,
            { timeout: 3000, polling: 50 },
            it3.id,
            newTitle,
          )
          const ms1 = Date.now() - t0
          console.log(`  另一个窗口改题名：本窗口 ${ms1} ms 内列表和详情更新`)

          // 2. 另一个窗口在"人事处"新建条目 → 本窗口树上人事处的条目数 3 秒内加 1。
          const rscBefore = await page.$eval(`[data-testid=tree-category][data-category-id="${ctx.c.rsc.id}"]`, (el) => Number(el.dataset.items))
          await openCatalog(other, ctx.c.rsc.id)
          const t1 = Date.now()
          await other.click('[data-testid=item-new]')
          await other.waitForFunction(() => new URL(location.href).searchParams.get('item'), { timeout: 5000 })
          temps.items.push(new URL(other.url()).searchParams.get('item'))
          await page.waitForFunction(
            (id, n) => Number(document.querySelector(`[data-testid=tree-category][data-category-id="${id}"]`)?.dataset.items) === n,
            { timeout: 3000, polling: 50 },
            ctx.c.rsc.id,
            rscBefore + 1,
          )
          console.log(`  另一个窗口新建条目：本窗口树上"人事处" ${Date.now() - t1} ms 内从 ${rscBefore} 件变成 ${rscBefore + 1} 件`)

          // 3. 本窗口改着文号，另一个窗口改了题名 → 本窗口保存 409 → 合并（文号保留、题名换成别人的）→ 再保存成功。
          await openCatalog(other, ctx.c.bgs.id, it3.id)
          await setField(page, 'doc_no', '档办〔2025〕18 号')
          const theirTitle = `${original.title}（别人刚改）`
          await setField(other, 'title', theirTitle)
          await other.click('[data-testid=item-save]')
          await page.waitForSelector('[data-testid=item-remote-changed]', { timeout: 3000 })
          await page.click('[data-testid=item-save]')
          await page.waitForSelector('[data-testid=item-merge-note]', { timeout: 5000 })
          await waitToast(page, '条目已被别人修改')
          const form = await page.evaluate(() => ({
            title: document.querySelector('[data-testid=item-title]').value,
            docNo: document.querySelector('[data-testid=item-field][data-key=doc_no] input').value,
            dirty: document.querySelector('[data-testid=item-detail]').dataset.dirty,
          }))
          assert(form.title === theirTitle, `合并后题名应是别人改的：${form.title}`)
          assert(form.docNo === '档办〔2025〕18 号' && form.dirty === 'true', `合并后文号应保留我改的：${JSON.stringify(form)}`)
          await page.screenshot({ path: `${OUT}/m3-catalog-conflict-merge.png` })
          await page.click('[data-testid=item-save]')
          await page.waitForFunction(() => !document.querySelector('[data-testid=item-detail]')?.dataset.dirty, { timeout: 5000 })
          const merged = await getItem(ctx, it3.id)
          assert(merged.title === theirTitle && merged.extra_fields.doc_no === '档办〔2025〕18 号', `两边的修改应都在：${merged.title} / ${merged.extra_fields.doc_no}`)
          console.log('  两个窗口改同一个条目：后保存的 409 → 重新加载，自己的修改保留 → 再保存，两边的修改都在')
          await sleep(300)
        } finally {
          await close()
        }
      },
      async after(page) {
        const ctx = await context(page)
        const it3 = ctx.item(ctx.c.bgs, 3)
        const cur = await getItem(ctx, it3.id)
        await ctx.api('PATCH', `/api/archive-items/${it3.id}`, {
          row_version: cur.row_version,
          title: it3.spec.title,
          extra_fields: { doc_no: it3.spec.fields.doc_no },
        })
        await cleanupTemps(ctx)
      },
    },

    // ---------------------------------------------------------------- 批量定稿被拒
    {
      name: 'm3-catalog-batch-lock',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it2 = ctx.item(ctx.c.bgs, 2)
        const it3 = ctx.item(ctx.c.bgs, 3)
        await openCatalog(page, ctx.c.bgs.id)
        await page.click(`[data-testid=item-row][data-item-id="${it2.id}"] [data-testid=item-check]`)
        await page.click(`[data-testid=item-row][data-item-id="${it3.id}"] [data-testid=item-check]`)
        await page.waitForFunction(() => document.querySelector('[data-testid=batch-lock]')?.textContent?.includes('（2）'), { timeout: 3000 })
        await page.click('[data-testid=batch-lock]')
        await confirmAlert(page)
        await waitToast(page, '有条目不满足定稿条件')
        await page.waitForSelector(`[data-testid=item-row][data-item-id="${it2.id}"][data-blocked]`, { timeout: 3000 })
        await page.waitForSelector(`[data-testid=item-row][data-item-id="${it3.id}"][data-blocked]`, { timeout: 3000 })
        const s2 = (await getItem(ctx, it2.id)).status
        const s3 = (await getItem(ctx, it3.id)).status
        assert(s2 === 'done' && s3 === 'draft', `批量定稿被拒后状态变了：${s2}、${s3}`)
        console.log(`  批量定稿被拒（${(await toastsText(page)).slice(0, 80)}…），两行标红，状态没变`)
      },
    },

    // ---------------------------------------------------------------- 归类节点：新建、改名、改类型、移动、删除
    {
      name: 'm3-catalog-category-ops',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        await openCatalog(page, null)
        const top = async () => (await ctx.api('GET', `/api/projects/${ctx.pid}/archive-categories`)).items
        // 有条目的节点不能删：直接提示，不弹确认框。
        await page.click(`[data-testid=tree-category][data-category-id="${ctx.c.fonds.id}"]`, { button: 'right' })
        await page.waitForSelector('[data-testid=menu-category-delete]', { visible: true })
        await page.click('[data-testid=menu-category-delete]')
        await waitToast(page, '不能删除')
        assert(!(await page.$('[role=alertdialog]')), '有条目的节点不应弹删除确认框')
        // 新建顶层全宗。
        await page.click('[data-testid=category-new-top]')
        await page.waitForSelector('[data-testid=category-form]', { visible: true })
        assert((await page.$eval('[data-testid=category-form-type]', (el) => el.value)) === 'fonds', '顶层默认类型应是全宗')
        await page.type('[data-testid=category-form-code]', 'M35OPS')
        await page.type('[data-testid=category-form-name]', '临时全宗（截图）')
        await page.click('[data-testid=category-form-submit]')
        await page.waitForSelector('[data-testid=category-form]', { hidden: true, timeout: 5000 })
        await page.waitForFunction(() => new URL(location.href).searchParams.get('category'), { timeout: 5000 })
        const fonds = (await top()).find((c) => c.category_code === 'M35OPS')
        assert(fonds, '顶层没有 M35OPS')
        temps.categories.push(fonds.id)
        await page.waitForSelector(`[data-testid=tree-category][data-category-id="${fonds.id}"][data-selected-category]`, { timeout: 3000 })
        // 在它下面新建（默认类型是门类）；编码里有模板的分隔符时本地提示。
        await page.click('[data-testid=category-new-child]')
        await page.waitForSelector('[data-testid=category-form]', { visible: true })
        assert((await page.$eval('[data-testid=category-form-type]', (el) => el.value)) === 'category', '全宗下默认类型应是门类')
        await page.type('[data-testid=category-form-code]', 'W-S')
        await page.waitForSelector('[data-testid=category-form-code-problem]')
        const problem = await page.$eval('[data-testid=category-form-code-problem]', (el) => el.textContent)
        assert(problem.includes('-'), `编码含分隔符 - 应提示：${problem}`)
        await page.screenshot({ path: `${OUT}/m3-catalog-category-form.png` })
        await page.$eval('[data-testid=category-form-code]', (el) => {
          el.value = 'WS'
          el.dispatchEvent(new Event('input', { bubbles: true }))
        })
        await page.type('[data-testid=category-form-name]', '文书')
        await page.click('[data-testid=category-form-submit]')
        await page.waitForSelector('[data-testid=category-form]', { hidden: true, timeout: 5000 })
        await page.waitForFunction((f) => new URL(location.href).searchParams.get('category') !== f, { timeout: 5000 }, fonds.id)
        const child = (await ctx.api('GET', `/api/projects/${ctx.pid}/archive-categories?parent_id=${fonds.id}`)).items[0]
        assert(child?.category_code === 'WS' && child.category_type === 'category', `下级节点不对：${JSON.stringify(child)}`)
        temps.categories.push(child.id)
        // 右键菜单 → 改名称、类型。
        await page.click(`[data-testid=tree-category][data-category-id="${child.id}"]`, { button: 'right' })
        await page.waitForSelector('[data-testid=category-menu]', { visible: true })
        await page.screenshot({ path: `${OUT}/m3-catalog-category-menu.png` })
        await page.click('[data-testid=menu-category-rename]')
        await page.waitForSelector('[data-testid=category-form]', { visible: true })
        await page.$eval('[data-testid=category-form-name]', (el) => {
          el.value = '文书（改名）'
          el.dispatchEvent(new Event('input', { bubbles: true }))
        })
        await page.select('[data-testid=category-form-type]', 'custom')
        await page.click('[data-testid=category-form-submit]')
        await waitToast(page, '已修改「文书（改名）」的名称、类型')
        await page.waitForSelector('[data-testid=category-form]', { hidden: true, timeout: 5000 })
        const renamed = (await ctx.api('GET', `/api/projects/${ctx.pid}/archive-categories?parent_id=${fonds.id}`)).items[0]
        assert(renamed.category_name === '文书（改名）' && renamed.category_type === 'custom', `改名、改类型没生效：${JSON.stringify(renamed)}`)
        // 移到顶层。
        await page.click('[data-testid=category-move]')
        await page.waitForSelector('[data-testid=category-picker][data-settled=true]', { visible: true })
        await page.waitForSelector('[data-testid=category-picker-top]')
        await page.click('[data-testid=category-picker-top]')
        await page.click('[data-testid=category-picker-confirm]')
        await waitToast(page, '移到「顶层」')
        assert((await top()).some((c) => c.id === child.id), '移到顶层后接口里没有')
        // 删除两个。
        // 一个用工具栏的删除按钮，一个用右键菜单。
        for (const [id, via] of [[child.id, 'toolbar'], [fonds.id, 'menu']]) {
          if (via === 'toolbar') {
            await page.click(`[data-testid=tree-category][data-category-id="${id}"]`)
            await page.waitForFunction((x) => new URL(location.href).searchParams.get('category') === x, { timeout: 3000 }, id)
            await page.$eval('[data-testid=category-delete]', (el) => el.click())
          } else {
            await page.click(`[data-testid=tree-category][data-category-id="${id}"]`, { button: 'right' })
            await page.waitForSelector('[data-testid=menu-category-delete]', { visible: true })
            await page.click('[data-testid=menu-category-delete]')
          }
          await confirmAlert(page)
          await page.waitForFunction((x) => !document.querySelector(`[data-testid=tree-category][data-category-id="${x}"]`), { timeout: 5000 }, id)
        }
        const left = await top()
        assert(!left.some((c) => c.id === child.id || c.id === fonds.id), '删除后接口里还有')
        temps.categories.length = 0
        console.log('  界面上新建顶层全宗、下级门类（编码含分隔符本地提示）、改名改类型、移到顶层、删除，接口逐步核对')
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- files.changed：页的审核状态
    {
      name: 'm3-catalog-files-event',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const project = ctx.demo.project
        const folder = await ctx.api('POST', '/api/folders', {
          parent_id: project.root_folder_id,
          folder_name: `截图验证 M3-5 审核状态 ${stamp()}`,
          remark: 'M3-5 截图脚本临时建的，做完会删掉',
        })
        temps.folders.push(folder.id)
        const seed = `M3-5 files-event ${stamp()} ${Math.random()}`
        const f1 = await uploadPage(ctx.api, folder.id, 'EV_01.png', `${seed}#1`, 1)
        const f2 = await uploadPage(ctx.api, folder.id, 'EV_02.png', `${seed}#2`, 2)
        const reviewer = (await ctx.api('GET', `/api/projects/${ctx.pid}/reviewers`)).reviewers.find((r) => r.username === 'demo_reviewer')
        await ctx.api('POST', `/api/projects/${ctx.pid}/review-assignments`, { reviewer_id: reviewer.id, file_ids: [f1.file_id] })
        const org = await tempCategory(ctx, ctx.c.y.id, 'org', 'M35EV', '审核状态测试（截图）')
        const it = await tempItem(ctx, org.id, { title: '审核状态变化测试' })
        const files = (await ctx.api('GET', `/api/folders/${folder.id}/children`)).files
        await linkPages(ctx.api, it.id, files)
        temps.linked.push(it.id)
        await openCatalog(page, org.id, it.id)
        await page.waitForSelector(`[data-testid=item-row][data-item-id="${it.id}"][data-pending="1"]`, { timeout: 5000 })
        await page.waitForSelector(`[data-testid=item-page][data-file-id="${f1.file_id}"][data-review-status=pending]`, { timeout: 5000 })
        const rev = await apiLogin(ctx.origin, 'demo_reviewer', demoPassword)
        const t0 = Date.now()
        await rev('POST', `/api/files/${f1.file_id}/review`, { result: 'approved' })
        await page.waitForFunction(
          (id, fid) =>
            document.querySelector(`[data-testid=item-row][data-item-id="${id}"]`)?.dataset.pending === '0' &&
            document.querySelector(`[data-testid=item-page][data-file-id="${fid}"]`)?.dataset.reviewStatus === 'approved',
          { timeout: 3000, polling: 50 },
          it.id,
          f1.file_id,
        )
        console.log(`  页面之外通过 1 页：${Date.now() - t0} ms 内列表的"1 待审"消失、页序变成"通过"`)
        await sleep(300)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 断线重连后整体刷新
    {
      name: 'm3-catalog-reconnect',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const org = await tempCategory(ctx, ctx.c.y.id, 'org', 'M35RC', '重连测试（截图）')
        const it = await tempItem(ctx, org.id, { title: '重连之前的题名' })
        await openCatalog(page, org.id, it.id)
        // 断开实时连接：派发 pagehide（useProjectStream 主动断开，和离开页面一样）；之后派发 persisted 的 pageshow，
        // 按"重新连上"处理（onOpen(false)，和断线重连走同一条路）。setOfflineMode 断不开已经建立的流式请求，不用它。
        await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })))
        await page.waitForFunction(() => document.querySelector('[data-testid=catalog-page]')?.dataset.stream !== 'open', { timeout: 5000 })
        // 断开期间的改动（事件不会补发）。
        const cur = await getItem(ctx, it.id)
        await ctx.api('PATCH', `/api/archive-items/${it.id}`, { row_version: cur.row_version, title: '断线期间改的题名' })
        await tempItem(ctx, org.id, { title: '断线期间建的条目' })
        await sleep(1500)
        const stale = await page.evaluate(() => document.querySelector('[data-testid=item-title]')?.value)
        assert(stale === '重连之前的题名', `断开期间页面不应收到事件：${stale}`)
        await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })))
        const t0 = Date.now()
        await page.waitForFunction(
          (id, cid) =>
            document.querySelector('[data-testid=catalog-page]')?.dataset.stream === 'open' &&
            document.querySelector('[data-testid=item-title]')?.value === '断线期间改的题名' &&
            document.querySelectorAll('[data-testid=item-row]').length === 2 &&
            document.querySelector(`[data-testid=tree-category][data-category-id="${cid}"]`)?.dataset.items === '2',
          { timeout: 15000, polling: 100 },
          it.id,
          org.id,
        )
        console.log(`  重新连上后 ${Date.now() - t0} ms 内整体刷新：详情题名、列表 2 行、树上 2 件`)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 事件放大、在途合并（M3-5 审查）
    {
      name: 'm3-catalog-events',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const x = await tempCategory(ctx, null, 'fonds', 'M35X', '不相干的分支（截图）')
        const xc = await tempCategory(ctx, x.id, 'category', 'X1', '不相干的门类')
        const it3 = ctx.item(ctx.c.bgs, 3)
        await openCatalog(page, ctx.c.bgs.id, it3.id)
        // 展开再收起不相干的分支：它的下级成了树认识、但收起的节点。
        const row = (id) => `[data-testid=category-tree-area] [data-testid=tree-category][data-category-id="${id}"]`
        await page.click(`${row(x.id)} [data-chevron]`)
        await page.waitForSelector(row(xc.id), { timeout: 5000 })
        await page.click(`${row(x.id)} [data-chevron]`)
        await page.waitForSelector(row(xc.id), { hidden: true, timeout: 5000 })
        await sleep(800)
        const kind = (u) =>
          /\/archive-categories\/[^/?]+\/items/.test(u)
            ? 'list'
            : /\/api\/archive-items\/[^/?]+$/.test(u)
              ? 'detail'
              : u.includes('/archive-categories')
                ? 'tree'
                : u.includes('/catalog-placements')
                  ? 'placements'
                  : 'other'
        const countDuring = async (fn, wait = 2500) => {
          const seen = []
          const onReq = (req) => {
            const u = req.url()
            if (u.includes('/api/') && !u.includes('/stream')) seen.push(kind(u))
          }
          page.on('request', onReq)
          try {
            await fn()
            await sleep(wait)
          } finally {
            page.off('request', onReq)
          }
          const n = (k) => seen.filter((v) => v === k).length
          return { tree: n('tree'), list: n('list'), detail: n('detail'), placements: n('placements'), other: n('other') }
        }
        // 1. 收起的不相干分支上改名 5 次：不重取列表、详情；树只刷新看得到的（根），收起的不发请求。
        const c1 = await countDuring(async () => {
          for (let i = 0; i < 5; i++) {
            await ctx.api('PATCH', `/api/projects/${ctx.pid}/archive-categories/${xc.id}`, { category_name: `不相干的门类 ${i}` })
          }
        })
        assert(c1.list === 0 && c1.detail === 0, `不相干的改名不应重取列表、详情：${JSON.stringify(c1)}`)
        assert(c1.tree <= 2, `收起的分支改名 5 次，树请求应不超过 2 个：${JSON.stringify(c1)}`)
        console.log(`  收起的不相干分支改名 5 次：树 ${c1.tree} 个请求，列表 ${c1.list}、详情 ${c1.detail}、其他 ${c1.other + c1.placements}`)
        // 2. 在途合并：树请求推迟 800 ms，第一轮在路上时又来两次事件 → 同时最多一轮，后两次并成一轮。
        const merged = await interceptRequests(
          page,
          (u) => u.includes('/archive-categories') && !u.includes('/items'),
          async (log) => {
            log.delayMs = 800
            log.reset()
            await ctx.api('PATCH', `/api/projects/${ctx.pid}/archive-categories/${xc.id}`, { category_name: '不相干的门类 a' })
            await sleep(550)
            await ctx.api('PATCH', `/api/projects/${ctx.pid}/archive-categories/${xc.id}`, { category_name: '不相干的门类 b' })
            await sleep(200)
            await ctx.api('PATCH', `/api/projects/${ctx.pid}/archive-categories/${xc.id}`, { category_name: '不相干的门类 c' })
            await log.waitQuiet(1500, 15000)
            return { started: log.started.length, maxInflight: log.maxInflight }
          },
        )
        assert(merged.maxInflight === 1, `同时最多一轮：树请求同时在途的最多 ${merged.maxInflight} 个`)
        assert(merged.started === 2, `3 次事件（第 2、3 次在第一轮途中）应合并成 2 轮，实际 ${merged.started} 个树请求`)
        console.log(`  在途合并：3 次事件（后两次在第一轮途中）→ ${merged.started} 轮，同时在途最多 ${merged.maxInflight} 个`)
        // 3. 参考：树上不认识的节点（页面之外新建顶层节点）位置不明，刷新全部看得到的展开节点。
        const c3 = await countDuring(async () => {
          await tempCategory(ctx, null, 'fonds', 'M35Y', '页面之外新建的节点（截图）')
        })
        // M3-3 合入前节点事件不带 item_ids，新建顶层节点的事件里有根节点 → 当作整个项目的子树变了，列表、详情也重取
        // （按审查约定的兼容语义，只是多发请求）；M3-3 起带 item_ids: [] 就不会了。这里只记数，不断言。
        console.log(`  参考：页面之外新建顶层节点（树上不认识）：树 ${c3.tree} 个请求（根和展开着的 Z201 链），列表 ${c3.list}、详情 ${c3.detail}（M3-3 合入前的旧事件语义）`)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 400 之后重取设置，错误标记保留（M3-5 审查）
    {
      name: 'm3-catalog-required-stale',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it5 = ctx.item(ctx.c.bgs, 5)
        await openCatalog(page, ctx.c.bgs.id, it5.id)
        const before = await getItem(ctx, it5.id)
        assert(!before.extra_fields.doc_no && before.status === 'draft', `第 5 件应是没有文号的草稿：${JSON.stringify(before.extra_fields)}`)
        // 页面打开之后把"文号"改成必填（不发事件，页面上的设置是旧的，本地检查放行）。
        await setItemFields(ctx, M35_ITEM_FIELDS.map((f) => (f.key === 'doc_no' ? { ...f, required: true } : f)))
        await page.click('[data-testid=item-mark-done]')
        await page.waitForSelector('[data-testid=item-field][data-key=doc_no][data-error]', { timeout: 5000 })
        // 400 之后页面重取设置（文号出现必填的 *），错误标记要还在。
        await page.waitForFunction(
          () => document.querySelector('[data-testid=item-field][data-key=doc_no] span')?.textContent?.includes('*'),
          { timeout: 5000 },
        )
        await sleep(800)
        const err = await page.$eval('[data-testid=item-field][data-key=doc_no]', (el) => el.dataset.error ?? null)
        assert(err?.includes('文号不能为空'), `重取设置之后文号的错误标记没了：${err}`)
        const after = await getItem(ctx, it5.id)
        assert(after.status === 'draft' && after.row_version === before.row_version, '条目被改了')
        console.log(`  标记完成被 400 拒绝：文号标出"${err}"，重取设置之后仍在`)
      },
      async after(page) {
        await setItemFields(await context(page), M35_ITEM_FIELDS)
      },
    },

    // ---------------------------------------------------------------- 直接放在根节点下的条目（M3-5 审查）
    {
      name: 'm3-catalog-root-items',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const rootId = (await ctx.api('GET', `/api/projects/${ctx.pid}/archive-categories`)).parent.id
        const it = await tempItem(ctx, rootId, { title: '直接放在顶层的临时条目（截图）' })
        const target = await tempCategory(ctx, ctx.c.y.id, 'org', 'M35RT', '顶层条目的去处（截图）')
        await openCatalog(page, null)
        await page.waitForSelector('[data-testid=tree-root-items]', { timeout: 5000 })
        const n = await page.$eval('[data-testid=tree-root-items]', (el) => Number(el.dataset.items))
        assert(n >= 1, `"直接放在顶层的条目"应至少 1 件，是 ${n}`)
        await page.click('[data-testid=tree-root-items]')
        await page.waitForFunction((r) => new URL(location.href).searchParams.get('category') === r, { timeout: 5000 }, rootId)
        await page.waitForSelector(`[data-testid=item-row][data-item-id="${it.id}"]`, { timeout: 5000 })
        await page.click(`[data-testid=item-row][data-item-id="${it.id}"]`)
        await waitDetail(page, it.id)
        const label = await page.$eval('[data-testid=item-category-path]', (el) => el.textContent.trim())
        assert(label.includes('直接放在顶层'), `位置应写"直接放在顶层"：${label}`)
        await sleep(300)
        await page.screenshot({ path: `${OUT}/m3-catalog-root-items-open.png` })
        // 移走：移到别的节点…
        await page.click('[data-testid=item-move]')
        await page.waitForSelector('[data-testid=category-picker][data-settled=true]', { visible: true })
        for (const id of [ctx.c.fonds.id, ctx.c.ws.id, ctx.c.year.id, ctx.c.y.id, target.id]) {
          const sel = `[data-testid=category-picker] [data-testid=tree-category][data-category-id="${id}"]`
          await page.waitForSelector(sel, { timeout: 5000 })
          await page.click(sel)
        }
        await page.click('[data-testid=category-picker-confirm]')
        await waitToast(page, '已移到')
        const moved = await getItem(ctx, it.id)
        assert(moved.category_id === target.id, `没移到目标节点：${moved.category_id}`)
        if (n === 1) await page.waitForSelector('[data-testid=tree-root-items]', { hidden: true, timeout: 5000 })
        console.log(`  顶层有 ${n} 件直接放的条目：能打开、能看详情，移到「${target.category_name}」后${n === 1 ? '那一行消失' : '少一件'}`)
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 移动没加载过的节点后树上能定位到（M3-5 审查）
    {
      name: 'm3-catalog-move-reveal',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const a = await tempCategory(ctx, null, 'fonds', 'M35MA', '移动测试 A（截图）')
        const b = await tempCategory(ctx, null, 'fonds', 'M35MB', '移动测试 B（截图）')
        const b1 = await tempCategory(ctx, b.id, 'category', 'MB1', '移动测试 B 的门类')
        const c = await tempCategory(ctx, a.id, 'category', 'MA1', '要移动的节点')
        await openCatalog(page, null)
        const row = (id) => `[data-testid=category-tree-area] [data-testid=tree-category][data-category-id="${id}"]`
        await page.click(`${row(a.id)} [data-chevron]`)
        await page.waitForSelector(row(c.id), { timeout: 5000 })
        await page.click(row(c.id), { button: 'right' })
        await page.waitForSelector('[data-testid=menu-category-move]', { visible: true })
        await page.click('[data-testid=menu-category-move]')
        await page.waitForSelector('[data-testid=category-picker][data-settled=true]', { visible: true })
        await page.click(`[data-testid=category-picker] [data-testid=tree-category][data-category-id="${b.id}"]`)
        await page.waitForSelector(`[data-testid=category-picker] [data-testid=tree-category][data-category-id="${b1.id}"]`, { timeout: 5000 })
        await page.click(`[data-testid=category-picker] [data-testid=tree-category][data-category-id="${b1.id}"]`)
        await page.click('[data-testid=category-picker-confirm]')
        await waitToast(page, '已把「要移动的节点」移到')
        // 树上定位到它：在 MB1 下面（缩进比 MB1 深），B、MB1 都展开了。
        await page.waitForSelector(row(c.id), { timeout: 5000 })
        const pad = (id) => page.$eval(row(id), (el) => parseFloat(el.style.paddingLeft))
        const [pc, pb1] = [await pad(c.id), await pad(b1.id)]
        assert(pc > pb1, `移动后的节点应在 MB1 下面：缩进 ${pc} / ${pb1}`)
        const shown = await page.$$eval('[data-testid=category-tree-area] [data-testid=tree-category]', (els) => els.map((e) => e.dataset.code))
        assert(shown.indexOf('MA1') === shown.indexOf('MB1') + 1, `MA1 应紧跟在 MB1 下面：${shown}`)
        console.log('  把没加载过的分支下的节点移到另一个没展开过的分支：树上展开到新位置、能看到它')
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 移动条目时目标节点刚被删（M3-5 审查）
    {
      name: 'm3-catalog-move-gone',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it3 = ctx.item(ctx.c.bgs, 3)
        const before = await getItem(ctx, it3.id)
        const t = await tempCategory(ctx, null, 'fonds', 'M35MG', '马上被删的节点（截图）')
        await openCatalog(page, ctx.c.bgs.id, it3.id)
        await page.click('[data-testid=item-move]')
        await page.waitForSelector('[data-testid=category-picker][data-settled=true]', { visible: true })
        await page.click(`[data-testid=category-picker] [data-testid=tree-category][data-category-id="${t.id}"]`)
        await page.waitForSelector(`[data-testid=category-picker] [data-testid=tree-category][data-category-id="${t.id}"][data-selected-category]`, { timeout: 3000 })
        await ctx.api('DELETE', `/api/projects/${ctx.pid}/archive-categories/${t.id}`)
        await page.click('[data-testid=category-picker-confirm]')
        await waitToast(page, '目标节点已不存在').catch(async (e) => {
          throw new Error(`${e.message}；提示：${await toastsText(page)}；地址 ${page.url()}`)
        })
        await sleep(800)
        const st = await page.evaluate(() => ({
          item: new URL(location.href).searchParams.get('item'),
          detail: document.querySelector('[data-testid=item-detail]')?.dataset.itemId ?? null,
          gone: [...document.querySelectorAll('[data-sonner-toast]')].some((e) => e.textContent?.includes('条目已不存在')),
        }))
        assert(st.item === it3.id && st.detail === it3.id && !st.gone, `条目被当成不存在了：${JSON.stringify(st)}`)
        const after = await getItem(ctx, it3.id)
        assert(after.category_id === before.category_id && after.row_version === before.row_version, '条目被改了')
        console.log('  目标节点刚被删：提示"目标节点已不存在"，详情保留，条目没动')
      },
      async after(page) {
        await cleanupTemps(await context(page))
      },
    },

    // ---------------------------------------------------------------- 有未保存的著录时换项目先确认（M3-5 审查）
    {
      name: 'm3-catalog-switch-guard',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        const it3 = ctx.item(ctx.c.bgs, 3)
        const before = await getItem(ctx, it3.id)
        const projects = await ctx.api('GET', '/api/projects')
        const nameOf = (code) => projects.find((p) => p.project_code === code).project_name
        const current = () => page.$eval('[data-testid=project-current]', (el) => el.textContent.trim())
        const choose = async (code) => {
          await page.click('[data-testid=project-switcher-trigger]')
          await page.waitForSelector(`[data-testid=project-option][data-code="${code}"]`, { visible: true })
          await page.click(`[data-testid=project-option][data-code="${code}"]`)
        }
        await openCatalog(page, ctx.c.bgs.id, it3.id)
        await setField(page, 'doc_no', '档办〔2025〕77 号（没保存）')
        await choose('DEMO-B')
        await page.waitForSelector('[role=alertdialog]', { visible: true, timeout: 5000 })
        const text = await page.$eval('[role=alertdialog]', (el) => el.textContent)
        assert(text.includes('著录还没有保存'), `换项目时应先确认：${text}`)
        await sleep(200)
        await page.screenshot({ path: `${OUT}/m3-catalog-switch-guard-confirm.png` })
        // 取消：还在 DEMO-A，修改还在。
        const buttons = await page.$$('[role=alertdialog] button')
        await buttons[0].click()
        await page.waitForSelector('[role=alertdialog]', { hidden: true, timeout: 5000 })
        await sleep(300)
        assert((await current()) === nameOf('DEMO-A'), `取消后应还在 DEMO-A：${await current()}`)
        const kept = await page.evaluate(() => ({
          dirty: document.querySelector('[data-testid=item-detail]')?.dataset.dirty,
          docNo: document.querySelector('[data-testid=item-field][data-key=doc_no] input')?.value,
        }))
        assert(kept.dirty === 'true' && kept.docNo === '档办〔2025〕77 号（没保存）', `取消后修改应还在：${JSON.stringify(kept)}`)
        // 再换一次，确认放弃：到了 DEMO-B，没有再问第二次。
        await choose('DEMO-B')
        await confirmAlert(page)
        await page.waitForFunction((n) => document.querySelector('[data-testid=project-current]')?.textContent?.trim() === n, { timeout: 5000 }, nameOf('DEMO-B'))
        await sleep(500)
        assert(!(await page.$('[role=alertdialog]')), '确认放弃之后不应再问一次')
        const after = await getItem(ctx, it3.id)
        assert(after.row_version === before.row_version && after.extra_fields.doc_no === before.extra_fields.doc_no, '放弃的修改不应保存')
        console.log('  有未保存的著录时换项目：先确认；取消留在原项目、修改还在；确认放弃后换过去，没有保存')
      },
      async after(page) {
        // 换回 DEMO-A（切换器记住上次选的项目，后面的截图要在 DEMO-A）。
        const cur = await page.$eval('[data-testid=project-current]', (el) => el.textContent.trim()).catch(() => '')
        if (!cur.includes('文书档案')) {
          await page.click('[data-testid=project-switcher-trigger]')
          await page.waitForSelector('[data-testid=project-option][data-code="DEMO-A"]', { visible: true })
          await page.click('[data-testid=project-option][data-code="DEMO-A"]')
          await sleep(800)
        }
      },
    },

    // ---------------------------------------------------------------- 虚拟滚动
    {
      name: 'm3-catalog-virtual',
      user: admin,
      path: '/catalog',
      async prepare(page) {
        const ctx = await context(page)
        // 常驻节点「M3-5 虚拟滚动（常驻）」，条目不够时补足（M3-5 审查：不再每次建、删 1200 个）。
        const node = await ensureM35Virtual(ctx.api, ctx.demo)
        const N = M35_VIRTUAL_COUNT
        await openCatalog(page, node.id)
        const first = await page.$eval('[data-testid=item-list]', (el) => Number(el.dataset.rows))
        assert(first === 500, `第一页应是 500 个，是 ${first}`)
        await page.$eval('[data-testid=item-list]', (el) => {
          el.scrollTop = el.scrollHeight / 2
        })
        await sleep(600)
        const mid = await page.evaluate(() => ({
          dom: document.querySelectorAll('[data-testid=item-row]').length,
          rows: Number(document.querySelector('[data-testid=item-list]').dataset.rows),
        }))
        assert(mid.dom < 200, `滚到中间时 DOM 里有 ${mid.dom} 行，应少于 200`)
        await page.screenshot({ path: `${OUT}/m3-catalog-virtual.png` })
        for (let k = 0; k < 10; k++) {
          await page.$eval('[data-testid=item-list]', (el) => {
            el.scrollTop = el.scrollHeight
          })
          await sleep(500)
          const n = await page.$eval('[data-testid=item-list]', (el) => Number(el.dataset.rows))
          if (n >= N) break
        }
        const all = await page.evaluate(() => ({
          dom: document.querySelectorAll('[data-testid=item-row]').length,
          rows: Number(document.querySelector('[data-testid=item-list]').dataset.rows),
          total: Number(document.querySelector('[data-testid=item-total]').dataset.count),
        }))
        assert(all.rows === N && all.total === N, `滚到底应加载到 ${N} 个：${JSON.stringify(all)}`)
        assert(all.dom < 200, `滚到底时 DOM 里有 ${all.dom} 行`)
        console.log(`  ${N} 个条目：第一页 500；滚到中间 DOM ${mid.dom} 行（已加载 ${mid.rows}）；滚到底加载到 ${all.rows}，DOM ${all.dom} 行`)
      },
    },
  ],
}
