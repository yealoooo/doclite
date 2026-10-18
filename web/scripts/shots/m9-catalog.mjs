// M9-2 的截图清单和断言：归类页双栏、整个文件夹归档、文件目录的封存提示（requirements 5.5"封存"、5.9"界面 /catalog"，
// Mockup doclite-catalog-redesign.html v3.1）。一个主流程，自己造数据（每跑一次一套新的，名字带时间戳）：
//
//   INIT_ADMIN_PASSWORD=<管理员密码> \
//     npm run screenshots -- --spec scripts/shots/m9-catalog.mjs --base http://127.0.0.1:<vite 端口>
//
// 要有 seed:demo 建好的 DEMO-A、demo_scanner（只有 files、scan 菜单，能进 DEMO-A）。数据（经接口）：
// - 文件夹「M9 归档演示 <时间>/盒 WS-2025-001」：4 个子文件夹（3、4、2、3 页）、1 个带下级文件夹的「附件（含下级）」
//   （演示"跳过"）、盒子里直接放 1 页（演示"直接放的文件不会归档"）；全部分给 admin 并通过；
// - 归类树：全宗 Z<时间> / 门类 WS / 年度 2025 / 保管期限 Y / 机构 BGS「办公室」。
//
// 流程和断言（失败时这一张算失败，脚本退出码 1）：
// 1. m9-catalog-select：/catalog 左栏打开演示文件夹（盒子一行"未归档"），右边选中「办公室」，选中盒子 → "归档 →"可点。
// 2. m9-archive-dialog：把盒子拖到右边树上的「办公室」（合成 HTML5 拖放事件）→ 预览：4 件、跳过 1 个（含下级文件夹），
//    改一件的题名；截图后"归档并封存"→ 右边列表 4 件、都是"未锁定"，件号 1–4。
// 3. m9-catalog-sealed：左栏打开第 1 个子文件夹 → 紫色横条"已归档为 办公室 0001…已封存"。
// 4. m9-files-sealed：/files 打开同一个文件夹 → 紫色横条、"上传文件"等按钮置灰；把盒子里直接放的那页移进这个文件夹
//    （接口）→ 409 folder_archived；再在页面里"移动到…"走一遍，提示是 server 的说明、带"去归类页查看"。
// 5. m9-files-sealed-scanner：demo_scanner 打开同一个文件夹 → 横条写"要修改请联系归类员解除封存"，没有解除按钮。
// 6. m9-unseal-dialog：/catalog 左栏横条"解除封存去修改…"→ 对话框，选原因"漏扫了页"；确认 → 橙色横条（三步进度），
//    顶部"修改中 1 件"。
// 7. m9-files-modifying：/files 打开这个文件夹 → 橙色横条；页面里上传一页（走上传按钮的代码）、接口删一页 → 文件数对。
// 8. m9-modifying-dialog：/catalog 点"修改中 1 件"→ 清单一行。
// 9. m9-reseal-dialog：右边双击打开这一件 → 橙色横条；"重新归档…"→ 原来 3 页 → 现在 3 页，新增 1、去掉 1；确认 →
//    封存（紫色横条），页 = 文件夹现在的样子。
// 10. m9-item-locked：新传的那页通过后"锁定"→ 已锁定；左栏横条写"已锁定"。
// 11. m9-batch-lock：回到列表，勾选另外 3 件批量锁定 → 4 件都已锁定。
// 12. m9-catalog-1366、m9-catalog-1920：两种宽度下打开一件看布局（截图人工看）。

import { mkdirSync } from 'node:fs'
import path from 'node:path'

import { apiLogin, stamp, uploadPage } from '../lib/m3-demo.mjs'
import { injectFiles, scanPng, waitUploadDone } from '../lib/upload-samples.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'
const SCANNER = 'demo_scanner'
const IDLE = 'networkidle2'
const OUT = '../docs/screenshots/m9'

const RUN = stamp()
const TOP = `M9 归档演示 ${RUN}`
const BOX = '盒 WS-2025-001'
const SUBS = [
  ['关于印发2025年度工作要点的通知', 3],
  ['办公室2024年度工作总结', 4],
  ['关于调整值班安排的通知', 2],
  ['2025年第一季度会议纪要', 3],
]

/** 流程中途补截一张（和清单的截图放在同一个目录：命令行的 --out，没有时用 OUT）。 */
async function extraShot(page, name) {
  const i = process.argv.indexOf('--out')
  const dir = path.resolve(i > 0 ? process.argv[i + 1] : OUT)
  mkdirSync(dir, { recursive: true })
  await page.screenshot({ path: path.join(dir, `${name}.png`) })
}

function assert(cond, message) {
  if (!cond) throw new Error(`断言失败：${message}`)
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

// ---------- 数据 ----------

let ctxPromise = null
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const api = await apiLogin(origin, admin, adminPassword)
    const project = (await api('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-A')
    if (!project) throw new Error('找不到 DEMO-A，先跑 seed:demo')
    const pid = project.id
    const me = await api('GET', '/api/me')
    const mk = (parent, name) => api('POST', '/api/folders', { parent_id: parent, folder_name: name, if_exists: 'return' })
    const top = await mk(project.root_folder_id, TOP)
    const box = await mk(top.id, BOX)
    const subs = []
    let seed = 0
    for (const [name, n] of SUBS) {
      const f = await mk(box.id, name)
      for (let i = 1; i <= n; i++) await uploadPage(api, f.id, `${String(i).padStart(4, '0')}.png`, `m9-${RUN}-${seed}`, i)
      seed++
      subs.push(f)
    }
    const att = await mk(box.id, '附件（含下级）')
    const deep = await mk(att.id, '扫描件')
    await uploadPage(api, deep.id, '0001.png', `m9-${RUN}-deep`, 1)
    const loose = await uploadPage(api, box.id, '散页.png', `m9-${RUN}-loose`, 1)
    // 全部分给 admin 并通过（锁定要求全部页通过）。
    const all = []
    for (const f of [...subs, deep]) all.push(...(await api('GET', `/api/folders/${f.id}/children`)).files.map((x) => x.file_id))
    all.push(loose.file_id)
    await approve(api, pid, me.id, all)

    const cat = (parent, type, code, name) =>
      api('POST', `/api/projects/${pid}/archive-categories`, { parent_id: parent, category_type: type, category_code: code, category_name: name })
    const fonds = await cat(null, 'fonds', `Z${RUN.slice(-8)}`, `M9 演示 ${RUN.slice(8, 12)}`)
    const ws = await cat(fonds.id, 'category', 'WS', '文书')
    const year = await cat(ws.id, 'year', '2025', '2025')
    const y = await cat(year.id, 'retention', 'Y', '永久')
    const bgs = await cat(y.id, 'org', 'BGS', '办公室')
    console.log(`  演示数据：DEMO-A/${TOP}/${BOX}（${SUBS.length} 个子文件夹），归类节点 ${fonds.category_code}/WS/2025/Y/BGS`)
    return { origin, api, pid, me, project, top, box, subs, att, loose, bgs, items: [] }
  })()
  return ctxPromise
}

async function approve(api, pid, reviewerId, fileIds) {
  if (!fileIds.length) return
  await api('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: reviewerId, file_ids: fileIds })
  await api('POST', `/api/projects/${pid}/reviews:approve`, { file_ids: fileIds })
}

// ---------- 页面 ----------

async function openCatalog(page, query) {
  const ctx = await context(page)
  const qs = new URLSearchParams(query).toString()
  await page.goto(`${ctx.origin}/catalog?${qs}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=catalog-page][data-tree=ready][data-stream=open]', { timeout: 15000 })
  await page.waitForSelector('[data-testid=folder-pane][data-state=ready]', { timeout: 15000 })
  if (query.category) {
    await page.waitForSelector(`[data-testid=tree-category][data-category-id="${query.category}"]`, { timeout: 15000 })
    if (!query.item) await page.waitForSelector('[data-testid=catalog-page][data-list=ready]', { timeout: 15000 })
  }
}

async function openFilesPage(page, folderId) {
  const ctx = await context(page)
  await page.goto(`${ctx.origin}/files?folder=${folderId}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=files-page][data-list=ready][data-stream=open]', { timeout: 15000 })
}

/** 等一个条件成立（page.waitForFunction 的简写）。 */
function waitFor(page, fn, arg, timeout = 10000) {
  return page.waitForFunction(fn, { timeout }, arg)
}

async function clickText(page, selector, text) {
  const ok = await page.evaluate(
    (sel, t) => {
      const el = [...document.querySelectorAll(sel)].find((e) => e.textContent.includes(t))
      el?.click()
      return !!el
    },
    selector,
    text,
  )
  assert(ok, `找不到「${text}」（${selector}）`)
}

/** 合成 HTML5 拖放：从 from 拖到 to（浏览器原生拖放在无头 Chrome 里不好驱动，事件和真实的一样走页面的处理函数）。 */
async function dragTo(page, fromSel, toSel) {
  const ok = await page.evaluate(
    (a, b) => {
      const from = document.querySelector(a)
      const to = document.querySelector(b)
      if (!from || !to) return false
      const dt = new DataTransfer()
      const fire = (el, type) => el.dispatchEvent(new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: dt }))
      fire(from, 'dragstart')
      fire(to, 'dragenter')
      fire(to, 'dragover')
      fire(to, 'drop')
      fire(from, 'dragend')
      return true
    },
    fromSel,
    toSel,
  )
  assert(ok, `拖放的起点或终点不在页面上：${fromSel} → ${toSel}`)
}

async function itemRows(page) {
  return page.$$eval('[data-testid=item-row]', (els) =>
    els.map((e) => ({ id: e.dataset.itemId, no: Number(e.dataset.itemNo), status: e.dataset.status, source: e.dataset.source })),
  )
}

async function bannerState(page, scope = '[data-testid=folder-pane]') {
  return page.evaluate((s) => {
    const el = document.querySelector(`${s} [data-testid=seal-banner]`)
    return el ? { state: el.dataset.state, text: el.textContent.replace(/\s+/g, ' ').trim() } : null
  }, scope)
}

async function folderFiles(ctx, folderId) {
  return (await ctx.api('GET', `/api/folders/${folderId}/children`)).files
}

export default {
  out: OUT,
  accounts: { [admin]: adminPassword, [SCANNER]: demoPassword },
  waitUntil: IDLE,
  shots: [
    {
      name: 'm9-catalog-select',
      user: admin,
      path: '/catalog',
      prepare: async (page) => {
        const ctx = await context(page)
        await openCatalog(page, { folder: ctx.top.id, category: ctx.bgs.id })
        await page.waitForSelector(`[data-testid=pane-folder][data-folder-id="${ctx.box.id}"]`)
        const mid = await page.$eval('[data-testid=archive-button]', (b) => b.disabled)
        assert(mid, '没选文件夹时"归档 →"应该是灰的')
        await page.click(`[data-testid=pane-folder][data-folder-id="${ctx.box.id}"]`)
        await waitFor(page, () => !document.querySelector('[data-testid=archive-button]').disabled)
        const tip = await page.$eval('[data-testid=archive-tip]', (e) => e.textContent)
        assert(tip.includes('办公室'), `中间的提示应写"放到「办公室」"：${tip}`)
        console.log(`  选中盒子，中间：${tip}`)
      },
    },
    {
      name: 'm9-archive-dialog',
      user: admin,
      path: '/catalog',
      prepare: async (page) => {
        const ctx = await context(page)
        await openCatalog(page, { folder: ctx.top.id, category: ctx.bgs.id })
        await page.click(`[data-testid=pane-folder][data-folder-id="${ctx.box.id}"]`)
        await dragTo(page, `[data-testid=pane-folder][data-folder-id="${ctx.box.id}"]`, `[data-testid=tree-category][data-category-id="${ctx.bgs.id}"]`)
        await page.waitForSelector('[data-testid=archive-dialog][data-state=ready]', { timeout: 10000 })
        const d = await page.$eval('[data-testid=archive-dialog]', (e) => ({ items: Number(e.dataset.items), skipped: Number(e.dataset.skipped) }))
        assert(d.items === SUBS.length && d.skipped === 1, `预览应是 ${SUBS.length} 件、跳过 1 个：${JSON.stringify(d)}`)
        const reason = await page.$eval('[data-testid=archive-skipped]', (e) => e.dataset.reason)
        assert(reason === 'has_subfolders', `跳过的原因应是 has_subfolders：${reason}`)
        const loose = await page.$eval('[data-testid=archive-loose-files]', (e) => e.textContent).catch(() => '')
        assert(loose.includes('1 个文件不会归档'), `应提示盒子里直接放的 1 个文件不会归档：${loose}`)
        await page.evaluate(() => {
          const input = document.querySelectorAll('[data-testid=archive-title]')[2]
          input.value = '关于调整2025年值班安排的通知'
          input.dispatchEvent(new Event('input', { bubbles: true }))
        })
        console.log(`  预览：${d.items} 件，跳过 1 个（含下级文件夹），提示盒子里直接放的 1 页不会归档，改了第 3 件的题名（预览里的第 3 行）`)
      },
      after: async (page) => {
        // 截图之后确认归档。
        if (!(await page.$('[data-testid=archive-confirm]'))) return
        const ctx = await context(page)
        await page.click('[data-testid=archive-confirm]')
        await waitFor(page, () => !document.querySelector('[data-testid=archive-dialog]'))
        await waitFor(page, (n) => document.querySelectorAll('[data-testid=item-row]').length === n, SUBS.length)
        const rows = await itemRows(page)
        assert(rows.map((r) => r.no).join() === '1,2,3,4' && rows.every((r) => r.status === 'draft' && r.source === 'sealed'), `列表不对：${JSON.stringify(rows)}`)
        const list = await ctx.api('GET', `/api/archive-categories/${ctx.bgs.id}/items`)
        ctx.items = list.items
        assert(list.items.some((i) => i.title === '关于调整2025年值班安排的通知'), '改过的题名没有用上')
        console.log('  已归档 4 件（未锁定、已封存），改过的题名用上了')
      },
    },
    {
      name: 'm9-catalog-sealed',
      user: admin,
      path: '/catalog',
      prepare: async (page) => {
        const ctx = await context(page)
        await openCatalog(page, { folder: ctx.subs[0].id, category: ctx.bgs.id })
        await page.waitForSelector('[data-testid=folder-pane] [data-testid=seal-banner]')
        const b = await bannerState(page)
        assert(b.state === 'sealed' && b.text.includes('办公室 0001') && b.text.includes('已封存'), `横条不对：${JSON.stringify(b)}`)
        console.log(`  左栏横条：${b.text.slice(0, 40)}…`)
      },
    },
    {
      name: 'm9-files-sealed',
      user: admin,
      path: '/files',
      prepare: async (page) => {
        const ctx = await context(page)
        // 接口：把盒子里直接放的那页移进封存的文件夹 → 409 folder_archived。
        const r = await ctx.api('PATCH', `/api/folder-files/${ctx.loose.folder_file_id}`, { folder_id: ctx.subs[0].id }, { allow: [409] })
        assert(r.code === 'folder_archived', `移进封存的文件夹应 409 folder_archived：${JSON.stringify(r)}`)
        console.log(`  接口：${r.message}`)
        // 页面：盒子里选中那一页，"移动到…"选封存的文件夹 → 提示 server 的说明。
        await openFilesPage(page, ctx.box.id)
        const badges = await page.$$eval('[data-testid=folder-archive-badge]', (els) => els.map((e) => e.dataset.state))
        assert(badges.filter((s) => s === 'sealed').length === SUBS.length, `子文件夹应有 ${SUBS.length} 个封存徽标：${badges}`)
        await page.click(`[data-testid=file-cell][data-file-id="${ctx.loose.file_id}"]`)
        await page.click('[data-testid=bar-move]')
        await page.waitForSelector('[role=dialog] [data-testid=folder-tree]')
        // 在选择框里展开到盒子、选第一个子文件夹（树按名称显示）。
        await waitFor(page, (name) => [...document.querySelectorAll('[role=dialog] [data-testid=tree-folder]')].some((e) => e.textContent.includes(name)), SUBS[0][0], 15000).catch(async () => {
          await clickText(page, '[role=dialog] [data-testid=tree-folder]', TOP)
          await sleep(500)
          await clickText(page, '[role=dialog] [data-testid=tree-folder]', BOX)
          await waitFor(page, (name) => [...document.querySelectorAll('[role=dialog] [data-testid=tree-folder]')].some((e) => e.textContent.includes(name)), SUBS[0][0])
        })
        await clickText(page, '[role=dialog] [data-testid=tree-folder]', SUBS[0][0])
        await clickText(page, '[role=dialog] button', '移动到这里')
        await page.waitForSelector('[data-sonner-toast][data-type=error]', { timeout: 10000 })
        const toast = await page.$eval('[data-sonner-toast][data-type=error]', (e) => e.textContent.replace(/\s+/g, ' '))
        assert(toast.includes('已归档为') && toast.includes('去归类页查看'), `提示应是 server 的说明并带"去归类页查看"：${toast}`)
        console.log(`  页面提示：${toast.slice(0, 60)}…`)
        await sleep(800)
        await extraShot(page, 'm9-files-archived-error')
        // 打开封存的文件夹：横条、按钮置灰。
        await openFilesPage(page, ctx.subs[0].id)
        const b = await bannerState(page, '[data-testid=files-page]')
        assert(b?.state === 'sealed' && b.text.includes('解除封存去修改'), `横条不对：${JSON.stringify(b)}`)
        const blocked = await page.$$eval('[data-blocked]', (els) => els.map((e) => e.querySelector('button')?.dataset.testid))
        assert(['new-folder', 'upload-files', 'upload-folder'].every((t) => blocked.includes(t)), `新建、上传应置灰：${blocked}`)
        console.log('  /files：紫色横条，新建文件夹、上传文件、上传文件夹置灰')
      },
    },
    {
      name: 'm9-files-sealed-scanner',
      user: SCANNER,
      path: '/files',
      prepare: async (page) => {
        const ctx = await context(page)
        await openFilesPage(page, ctx.subs[0].id)
        const b = await bannerState(page, '[data-testid=files-page]')
        assert(b?.state === 'sealed' && b.text.includes('联系归类员') && !(await page.$('[data-testid=seal-banner-unseal]')), `扫描员看到的横条不对：${JSON.stringify(b)}`)
        console.log('  demo_scanner：横条写"要修改请联系归类员解除封存"，没有解除按钮')
      },
    },
    {
      name: 'm9-unseal-dialog',
      user: admin,
      path: '/catalog',
      prepare: async (page) => {
        const ctx = await context(page)
        await openCatalog(page, { folder: ctx.subs[0].id, category: ctx.bgs.id })
        await page.click('[data-testid=folder-pane] [data-testid=seal-banner-unseal]')
        await page.waitForSelector('[data-testid=unseal-dialog][data-state=ready]')
        await page.select('[data-testid=unseal-reason]', '漏扫了页')
      },
      after: async (page) => {
        if (!(await page.$('[data-testid=unseal-confirm]'))) return
        await page.click('[data-testid=unseal-confirm]')
        await waitFor(page, () => document.querySelector('[data-testid=folder-pane] [data-testid=seal-banner]')?.dataset.state === 'modifying')
        await page.waitForSelector('[data-testid=modifying-chip][data-count="1"]', { timeout: 15000 })
        const b = await bannerState(page)
        assert(b.text.includes('漏扫了页') && b.text.includes('重新归档'), `修改中的横条不对：${b.text}`)
        await extraShot(page, 'm9-catalog-modifying')
        console.log('  已解除封存：左栏橙色横条（三步进度），顶部"修改中 1 件"（另存 m9-catalog-modifying.png）')
      },
    },
    {
      name: 'm9-files-modifying',
      user: admin,
      path: '/files',
      prepare: async (page) => {
        const ctx = await context(page)
        await openFilesPage(page, ctx.subs[0].id)
        const b = await bannerState(page, '[data-testid=files-page]')
        assert(b?.state === 'modifying' && b.text.includes('去归类页重新归档'), `横条不对：${JSON.stringify(b)}`)
        const before = await folderFiles(ctx, ctx.subs[0].id)
        // 页面里上传一页（漏扫的），接口删掉第 2 页。
        const p = scanPng(`m9-${RUN}-new`, 9)
        await injectFiles(page, 'files', [{ name: '0004.png', data: p.png, type: 'image/png' }])
        const done = await waitUploadDone(page)
        assert(done.every((x) => x.state === 'done'), `上传没成功：${JSON.stringify(done)}`)
        await ctx.api('DELETE', `/api/folder-files/${before[1].folder_file_id}`)
        const after = await folderFiles(ctx, ctx.subs[0].id)
        assert(after.length === before.length, `应还是 ${before.length} 个文件：${after.length}`)
        ctx.newFile = after.find((f) => f.file_name === '0004.png')
        await waitFor(page, (n) => document.querySelectorAll('[data-testid=file-cell], [data-testid=file-row]').length === n, after.length)
        console.log('  /files 修改中：橙色横条；上传了 0004.png，删掉了 0002.png')
      },
    },
    {
      name: 'm9-modifying-dialog',
      user: admin,
      path: '/catalog',
      prepare: async (page) => {
        const ctx = await context(page)
        await openCatalog(page, { folder: ctx.box.id, category: ctx.bgs.id })
        await page.waitForSelector('[data-testid=modifying-chip]', { timeout: 15000 })
        await page.click('[data-testid=modifying-chip]')
        await page.waitForSelector('[data-testid=modifying-dialog]')
        const rows = await page.$$('[data-testid=modifying-row]')
        assert(rows.length === 1, `清单应有 1 行：${rows.length}`)
        const pane = await page.$$eval('[data-testid=pane-folder][data-archive=modifying]', (els) => els.length)
        assert(pane === 1, '左栏应有 1 个修改中的子文件夹')
      },
    },
    {
      name: 'm9-reseal-dialog',
      user: admin,
      path: '/catalog',
      prepare: async (page) => {
        const ctx = await context(page)
        await openCatalog(page, { folder: ctx.box.id, category: ctx.bgs.id })
        await page.waitForSelector('[data-testid=item-row][data-source=modifying]')
        await page.evaluate(() => document.querySelector('[data-testid=item-row][data-source=modifying]').dispatchEvent(new MouseEvent('dblclick', { bubbles: true })))
        await page.waitForSelector('[data-testid=item-detail][data-load=ready][data-source=modifying]')
        await page.click('[data-testid=item-reseal]')
        await page.waitForSelector('[data-testid=reseal-dialog][data-state=ready]')
        const d = await page.$eval('[data-testid=reseal-dialog]', (e) => ({ ...e.dataset }))
        assert(d.old === '3' && d.new === '3' && d.added === '1' && d.removed === '1', `对比不对：${JSON.stringify(d)}`)
        console.log(`  对比：原来 ${d.old} 页 → 现在 ${d.new} 页，新增 ${d.added}、去掉 ${d.removed}、调序 ${d.moved}`)
      },
      after: async (page) => {
        if (!(await page.$('[data-testid=reseal-confirm]'))) return
        const ctx = await context(page)
        await page.click('[data-testid=reseal-confirm]')
        await page.waitForSelector('[data-testid=item-detail][data-source=sealed]', { timeout: 10000 })
        const now = (await folderFiles(ctx, ctx.subs[0].id)).map((f) => f.file_id)
        const item = await ctx.api('GET', `/api/archive-items/${ctx.items[0].id}`)
        assert(item.source_status === 'sealed' && item.pages.map((p) => p.file_id).join() === now.join(), '重新归档后页应和文件夹一样')
        await page.waitForSelector('[data-testid=modifying-chip]', { hidden: true, timeout: 15000 })
        console.log('  已重新归档：页 = 文件夹现在的样子，再次封存，"修改中"提醒消失')
      },
    },
    {
      name: 'm9-item-locked',
      user: admin,
      path: '/catalog',
      prepare: async (page) => {
        const ctx = await context(page)
        await approve(ctx.api, ctx.pid, ctx.me.id, [ctx.newFile.file_id])
        await openCatalog(page, { folder: ctx.subs[0].id, category: ctx.bgs.id, item: ctx.items[0].id })
        await page.waitForSelector('[data-testid=item-detail][data-load=ready]')
        await waitFor(page, () => !document.querySelector('[data-testid=item-lock]')?.disabled && !document.querySelector('[data-testid=item-lock]')?.closest('[data-blocked]'), undefined, 15000)
        await page.click('[data-testid=item-lock]')
        await clickText(page, '[role=alertdialog] button', '锁定')
        await page.waitForSelector('[data-testid=item-detail][data-status=locked]', { timeout: 10000 })
        await waitFor(page, () => document.querySelector('[data-testid=folder-pane] [data-testid=seal-banner]')?.textContent.includes('已锁定'))
        console.log('  已锁定；左栏横条写"已锁定"')
      },
    },
    {
      name: 'm9-batch-lock',
      user: admin,
      path: '/catalog',
      prepare: async (page) => {
        const ctx = await context(page)
        await openCatalog(page, { folder: ctx.box.id, category: ctx.bgs.id })
        await page.waitForSelector('[data-testid=item-row]')
        const boxes = await page.$$('[data-testid=item-row][data-status=draft] [data-testid=item-check]')
        assert(boxes.length === 3, `应有 3 件未锁定：${boxes.length}`)
        for (const b of boxes) await b.click()
        await page.click('[data-testid=batch-lock]')
        await clickText(page, '[role=alertdialog] button', '锁定')
        await waitFor(page, () => [...document.querySelectorAll('[data-testid=item-row]')].every((e) => e.dataset.status === 'locked'), undefined, 15000)
        console.log('  批量锁定：4 件都已锁定')
      },
    },
    {
      name: 'm9-catalog-1366',
      user: admin,
      path: '/catalog',
      prepare: async (page) => {
        const ctx = await context(page)
        await page.setViewport({ width: 1366, height: 768 })
        await openCatalog(page, { folder: ctx.box.id, category: ctx.bgs.id, item: ctx.items[1].id })
        await page.waitForSelector('[data-testid=item-detail][data-load=ready]')
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)
        assert(!overflow, '1366 宽时页面横向溢出')
      },
      after: (page) => page.setViewport({ width: 1440, height: 900 }),
    },
    {
      name: 'm9-catalog-1920',
      user: admin,
      path: '/catalog',
      prepare: async (page) => {
        const ctx = await context(page)
        await page.setViewport({ width: 1920, height: 1080 })
        await openCatalog(page, { folder: ctx.subs[1].id, category: ctx.bgs.id, item: ctx.items[1].id })
        await page.waitForSelector('[data-testid=item-detail][data-load=ready]')
      },
      after: (page) => page.setViewport({ width: 1440, height: 900 }),
    },
  ],
}
