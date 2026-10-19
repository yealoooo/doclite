// M1-12 的截图清单和断言：文件目录页的整理（上传只在客户端里，M10-2 起样本用接口传）。
//
//   INIT_ADMIN_PASSWORD=admin123456 [SHOTS_TMP=<临时目录>] \
//     npm run screenshots -- --spec scripts/shots/m1-organize.mjs --base http://localhost:<preview 端口>
//
// 前端地址是 vite preview 或 dev（/api 代理到 server）。JPEG、TIFF 样本读 web 自己的 `scripts/samples/`，见
// scripts/lib/upload-samples.mjs。
//
// 只看界面的截图（右键菜单、移动对话框、多选、删除确认、错误提示）用 npm run seed 的演示文件夹（BOX2、BOX3），不提交改动；
// 会改数据的（整理流程、上传结果、两个页面的实时刷新）各自在 DEMO-A 根下建一个"截图验证 M1-12 …"临时文件夹，
// 做完在 after 里删掉（软删除；前面失败了也删）。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - files-organize：界面上新建文件夹 → 用接口传一个带两层子文件夹的样本目录 → 多选移动 3 个文件 → F2 改名
//   → 删除一个子文件夹；然后用接口核对文件夹结构、各文件的位置和顺序、文件日志条数。
// - files-realtime：两个页面打开同一个文件夹，在一个页面里删除、移动、复制（⌘C）文件，另一个页面 3 秒内更新
//   （另存一张页面 B 的截图 files-realtime-other.png）。
// - files-folder-ops：文件夹的 F2 改名（重名、保留名的提示）、改备注、移动（选择框里不能选它自己的子树），
//   接口核对；根文件夹只能改备注。
// - files-context-menu 截图后点"下载原图"：新窗口打开预签名的 original_url；files-shell-upload 截图后点"下载原图"：
//   调 `open_external`。
// - files-shell-upload：壳里（壳模式的地址 + Wails 桥的模拟，scripts/lib/wails-mock.mjs；--base 要指向 devshell）两个上传
//   按钮调 `pick_and_enqueue_upload`，提示"已加入上传队列"。

import path from 'node:path'

import { sample, scanPng } from '../lib/upload-samples.mjs'
import { installWailsMock, shellOrigin } from '../lib/wails-mock.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const PROJECT = 'DEMO-A'
// npm run seed 建的盒 002 里两个没归档的子文件夹（各 6 页 IMG_0001–0006.png；M9-3 起不再有 M1-10 的盒 002、003）。
const BOX2 = '2025 文书档案/盒 WS-2025-002/2025年1月财务收支报表'
const BOX3 = '2025 文书档案/盒 WS-2025-002/关于2025年度部门预算的批复'
const IDLE = 'networkidle2'

// ---------- 页面之外调接口（admin） ----------

let apiState = null

async function api(origin, method, path, body, { allow = [] } = {}) {
  apiState ??= { cookie: '' }
  const res = await fetch(origin + path, {
    method,
    headers: {
      Accept: 'application/json',
      ...(body !== undefined && !(body instanceof FormData) ? { 'Content-Type': 'application/json' } : {}),
      ...(apiState.cookie ? { Cookie: apiState.cookie } : {}),
    },
    body: body === undefined ? undefined : body instanceof FormData ? body : JSON.stringify(body),
  })
  const sc = res.headers.get('set-cookie')
  if (sc) apiState.cookie = sc.split(';')[0]
  const text = await res.text()
  const data = text ? JSON.parse(text) : undefined
  if (!res.ok && !allow.includes(res.status)) throw new Error(`${method} ${path} → ${res.status} ${data?.code}: ${data?.message}`)
  return data
}

let ctxPromise = null
/** 登录 admin，取 DEMO-A 和演示文件夹（只做一次）。 */
export function context(page) {
  ctxPromise ??= (async () => {
    // 壳模式的页面（wails.localhost）也从同一个 devshell 调接口，用 127.0.0.1（Windows 上 node 不一定解析 *.localhost）。
    const u = new URL(page.url())
    if (u.hostname === 'wails.localhost') u.hostname = '127.0.0.1'
    const origin = u.origin
    await api(origin, 'POST', '/api/auth/login', { username: admin, password: adminPassword })
    const projects = await api(origin, 'GET', '/api/projects')
    const project = projects.find((p) => p.project_code === PROJECT)
    if (!project) throw new Error(`找不到项目 ${PROJECT}，先跑 npm run seed`)
    const call = (method, path, body, opts) => api(origin, method, path, body, opts)
    const folders = {}
    for (const path of [BOX2, BOX3]) folders[path] = await resolvePath(call, project.root_folder_id, path)
    return { origin, project, folders, api: call }
  })()
  return ctxPromise
}

/** 按路径（从某个文件夹起，用 / 分隔）找文件夹，返回 {id, children}。 */
async function resolvePath(call, fromId, path) {
  let id = fromId
  for (const name of path.split('/').filter(Boolean)) {
    const ch = await call('GET', `/api/folders/${id}/children`)
    const f = ch.folders.find((x) => x.folder_name === name)
    if (!f) throw new Error(`找不到文件夹 ${path}（缺「${name}」）`)
    id = f.id
  }
  return { id, children: await call('GET', `/api/folders/${id}/children`) }
}

export function stamp() {
  return new Date().toISOString().replace(/[-:T]/g, '').slice(0, 14)
}

/** 在根文件夹下建一个临时文件夹（接口）。 */
export async function makeTemp(ctx, label) {
  return ctx.api('POST', '/api/folders', {
    parent_id: ctx.project.root_folder_id,
    folder_name: `截图验证 M1-12 ${label} ${stamp()}`,
    remark: 'M1-12 截图脚本临时建的，做完会删掉',
  })
}

export async function dropTemp(folder) {
  if (!folder || !ctxPromise) return
  const ctx = await ctxPromise
  await ctx.api('DELETE', `/api/folders/${folder.id}`, undefined, { allow: [404] })
  console.log(`  已删除临时文件夹「${folder.folder_name}」`)
}

/** 用接口上传一页合成的 PNG（带封面）。 */
async function apiUploadPng(ctx, folderId, name, page) {
  const form = new FormData()
  form.append('file_name', name)
  form.append('cover', new Blob([page.cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([page.png], { type: 'image/png' }), 'page.png')
  return ctx.api('POST', `/api/folders/${folderId}/files`, form)
}

// ---------- 页面操作 ----------

export async function openFolderId(page, id, view = 'icons') {
  const { origin } = await context(page)
  await page.goto(`${origin}/files?folder=${id}`, { waitUntil: IDLE })
  await page.waitForSelector(`[data-testid=files-page][data-list=ready]`, { timeout: 15000 })
  await page.click(`[data-testid=view-${view}]`)
  await page.waitForSelector(`[data-testid=file-scroll][data-view=${view}],[data-testid=folder-empty]`, { timeout: 5000 })
}

async function waitCovers(page, timeout = 15000) {
  await page.waitForFunction(
    () => [...document.querySelectorAll('[data-testid=file-cover]')].every((c) => !['loading', 'loading-image'].includes(c.dataset.coverState)),
    { timeout },
  )
}

/** 主区里名字为 name 的文件或文件夹单元格。 */
async function cell(page, name, kind = 'file') {
  const handle = await page.evaluateHandle(
    (name, kind) =>
      [...document.querySelectorAll(kind === 'file' ? '[data-testid=file-cell],[data-testid=file-row]' : '[data-testid=folder-cell],[data-testid=folder-row]')].find(
        (el) => [...el.querySelectorAll('[title]')].some((t) => t.getAttribute('title') === name),
      ),
    name,
    kind,
  )
  const el = handle.asElement()
  if (!el) throw new Error(`页面上找不到${kind === 'file' ? '文件' : '文件夹'}「${name}」`)
  return el
}

async function clickCell(page, name, { kind = 'file', meta = false, button = 'left' } = {}) {
  const el = await cell(page, name, kind)
  if (meta) await page.keyboard.down('Meta')
  await el.click({ button })
  if (meta) await page.keyboard.up('Meta')
}

async function menu(page, testid) {
  await page.waitForSelector(`[data-testid=context-menu] [data-testid=${testid}]`, { visible: true, timeout: 5000 })
  // 菜单有 100 ms 的进场动画。
  await new Promise((r) => setTimeout(r, 150))
  await page.click(`[data-testid=context-menu] [data-testid=${testid}]`)
}

/** FolderPicker 里点选某一行（按名字）。 */
async function pickFolder(page, name) {
  await page.waitForSelector('[data-testid=folder-picker] [data-testid=tree-folder]', { timeout: 10000 })
  await new Promise((r) => setTimeout(r, 400))
  await page.waitForFunction(
    (n) => [...document.querySelectorAll('[data-testid=folder-picker] [data-testid=tree-folder]')].some((el) => el.textContent?.trim() === n),
    { timeout: 10000 },
    name,
  )
  for (const it of await page.$$('[data-testid=folder-picker] [data-testid=tree-folder]')) {
    if ((await it.evaluate((el) => el.textContent?.trim())) === name) {
      await it.click()
      await page.waitForFunction(
        (n) => document.querySelector('[data-testid=folder-picker-selected]')?.textContent?.includes(n),
        { timeout: 5000 },
        name,
      )
      return
    }
  }
  throw new Error(`选择框里找不到「${name}」`)
}

async function waitToast(page, text, timeout = 10000) {
  await page.waitForFunction(
    (t) => [...document.querySelectorAll('[data-sonner-toast]')].some((el) => el.textContent?.includes(t)),
    { timeout },
    text,
  )
}

/** 等列表里出现（或不再有）某个名字。 */
async function waitName(page, name, present = true, timeout = 10000) {
  await page.waitForFunction(
    (n, p) => {
      const has = [...document.querySelectorAll('[data-testid=file-cell] [title],[data-testid=folder-cell] [title],[data-testid=file-row] [title],[data-testid=folder-row] [title]')].some(
        (el) => el.getAttribute('title') === n,
      )
      return has === p
    },
    { timeout },
    name,
    present,
  )
}

export function assert(cond, msg) {
  if (!cond) throw new Error(msg)
}

/**
 * 用接口在 `parentId` 下建一个带两层子文件夹的样本目录并上传（M10-2 起浏览器里不能上传）：PNG 带合成的封面，
 * JPEG、TIFF 不带。同一个文件夹里按文件名的自然顺序传（和客户端的"上传文件夹"一样）。返回 {top}。
 */
export async function uploadSampleFolder(ctx, parentId, prefix = '  ') {
  const seed = `M1-12 upload ${stamp()} ${Math.random()}`
  const top = '扫描批次'
  const mk = (parent, name) => ctx.api('POST', '/api/folders', { parent_id: parent, folder_name: name })
  const put = async (folderId, name, file) => {
    const form = new FormData()
    form.append('file_name', name)
    form.append('file', new Blob([await sample(file)]), 'original')
    return ctx.api('POST', `/api/folders/${folderId}/files`, form)
  }
  const t = await mk(parentId, top)
  const box1 = await mk(t.id, '盒 001')
  const inner = await mk(box1.id, '卷内')
  const box2 = await mk(t.id, '盒 002')
  await put(t.id, 'JPEG_A4.jpg', 'jpeg-gray.jpg')
  for (const n of [1, 2, 10]) await apiUploadPng(ctx, box1.id, `IMG_${n}.png`, scanPng(`${seed}#${n}`, n))
  await put(box1.id, 'TIFF_none.tif', 'tiff-none-gray8.tif')
  await put(inner.id, 'JPEG_exif6.jpg', 'jpeg-exif6.jpg')
  await put(inner.id, 'TIFF_g4.tif', 'tiff-g4-1bit.tif')
  await put(inner.id, 'TIFF_lzw.tif', 'tiff-lzw-rgb.tif')
  for (const n of [1, 2]) await apiUploadPng(ctx, box2.id, `IMG_${n}.png`, scanPng(`${seed}#b${n}`, n))
  console.log(`${prefix}用接口上传样本目录：10 个文件（${top}/{盒 001/卷内, 盒 002}）`)
  return { top }
}

// ---------- 壳的模拟（files-shell-upload） ----------

/** 在页面里执行（会被序列化，不能引用外部变量）。只补文件目录页用到的命令；window.__shellCalls 是每次调用（api_fetch 只记路径）。 */
function organizeShellMock() {
  const shell = window.__shell
  Object.defineProperty(window, '__shellCalls', {
    get: () => shell.calls.map((c) => ({ cmd: c.cmd, args: c.cmd === 'api_fetch' ? { path: c.args.req.path } : c.args })),
  })
  shell.commands.pick_and_enqueue_upload = async (args) => {
    // 模拟用户在系统对话框里选了 3 个文件（文件夹模式另有 1 个不支持的被跳过）。
    await new Promise((r) => setTimeout(r, 200))
    return args.mode === 'folder'
      ? { cancelled: false, added: [21, 22, 23, 24, 25], skipped: [{ path: 'D:\\扫描\\盒 006\\说明.txt', reason: '不支持的扩展名' }] }
      : { cancelled: false, added: [11, 12, 13], skipped: [] }
  }
}

// ---------- 各张截图 ----------

let organizeTemp = null
let realtimeTemp = null
let realtimePage = null
let folderOpsTemp = null

export default {
  out: '../docs/screenshots/m1',
  waitUntil: IDLE,
  accounts: { [admin]: adminPassword },
  shots: [
    // 右键菜单（一个文件）：打开、重命名 F2、移动到…、复制、下载原图、查看快照历史、删除 Del。
    {
      name: 'files-context-menu',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await context(page)
        await openFolderId(page, ctx.folders[BOX3].id)
        await waitCovers(page)
        await clickCell(page, 'IMG_0002.png', { button: 'right' })
        await page.waitForSelector('[data-testid=context-menu][data-kind=file]', { visible: true })
        const items = await page.$$eval('[data-testid=context-menu] [role=menuitem]', (els) => els.map((e) => e.textContent?.replace(/\s+/g, '')))
        const want = ['打开', '重命名F2', '移动到…', '复制', '下载原图', '查看快照历史', '删除Del']
        for (const w of want) assert(items.some((t) => t.startsWith(w)), `右键菜单缺「${w}」：${items.join('、')}`)
        console.log(`  断言 右键菜单：${items.join('、')}`)
      },
      async after(page) {
        // 截完图再点"下载原图"：浏览器里在新窗口打开 original_url（预签名的 http 地址）。
        const ctx = await ctxPromise
        const file = ctx.folders[BOX3].children.files.find((f) => f.file_name === 'IMG_0002.png')
        const popup = page.browser().waitForTarget((t) => t.url().includes(file.file_id), { timeout: 10000 })
        await menu(page, 'menu-download')
        const target = await popup
        const url = new URL(target.url())
        assert(url.protocol === 'http:' && url.pathname.endsWith('/v1.png') && url.searchParams.has('X-Amz-Signature'), `下载原图打开的地址不对：${url}`)
        console.log(`  断言 下载原图：新窗口打开 ${url.origin}${url.pathname}（预签名 URL）`)
        await (await target.page())?.close()
      },
      delay: 400,
    },
    // 移动对话框：选中一个文件 → 右键"移动到…" → 选目标（不确认）。
    {
      name: 'files-move-dialog',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await context(page)
        await openFolderId(page, ctx.folders[BOX3].id)
        await waitCovers(page)
        await clickCell(page, 'IMG_0003.png', { button: 'right' })
        await menu(page, 'menu-move')
        await pickFolder(page, '格式样本')
        const disabled = await page.$eval('[data-testid=folder-picker-confirm]', (b) => b.disabled)
        assert(!disabled, '选了别的文件夹，确认按钮应该能点')
      },
      async after(page) {
        await page.keyboard.press('Escape')
      },
      delay: 500,
    },
    // 多选：Shift 选 5 个 → 右键 → 批量操作的菜单；底部的选择栏。
    {
      name: 'files-batch-select',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await context(page)
        await openFolderId(page, ctx.folders[BOX2].id)
        await waitCovers(page)
        await clickCell(page, 'IMG_0002.png')
        await page.keyboard.down('Shift')
        await (await cell(page, 'IMG_0006.png')).click()
        await page.keyboard.up('Shift')
        await page.waitForSelector('[data-testid=selection-bar][data-kind=files]')
        await clickCell(page, 'IMG_0004.png', { button: 'right' })
        await page.waitForSelector('[data-testid=context-menu][data-kind=files]', { visible: true })
        const label = await page.$eval('[data-testid=context-menu]', (el) => el.textContent)
        assert(label.includes('已选 5 个文件'), `多选菜单应显示"已选 5 个文件"：${label}`)
        const bar = await page.$eval('[data-testid=selection-bar]', (el) => el.textContent)
        assert(bar.includes('已选 5 项') && bar.includes('移动到') && bar.includes('删除'), `选择栏不对：${bar}`)
        console.log('  断言 多选：右键菜单是批量的（已选 5 个文件：移动到…、复制、删除），选择栏有批量操作')
      },
      async after(page) {
        await page.keyboard.press('Escape')
      },
      delay: 400,
    },
    // 删除确认：选中一个文件按 Delete → 确认框（取消，不删）。
    {
      name: 'files-delete-confirm',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await context(page)
        await openFolderId(page, ctx.folders[BOX3].id)
        await waitCovers(page)
        await clickCell(page, 'IMG_0004.png')
        await page.keyboard.press('Delete')
        await page.waitForSelector('[role=alertdialog]', { visible: true })
        const text = await page.$eval('[role=alertdialog]', (el) => el.textContent)
        assert(text.includes('删除「IMG_0004.png」'), `确认框标题不对：${text}`)
      },
      async after(page) {
        // 取消：不删。
        const cancel = await page.$('[role=alertdialog] button')
        if (cancel) await cancel.click()
        const ctx = await ctxPromise
        const ch = await ctx.api('GET', `/api/folders/${ctx.folders[BOX3].id}/children`)
        assert(ch.files.some((f) => f.file_name === 'IMG_0004.png'), '取消删除后文件应该还在')
      },
      delay: 400,
    },
    // 错误提示：改名撞名（409 conflict）在对话框里明确提示，名字不变。
    {
      name: 'files-error',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await context(page)
        await openFolderId(page, ctx.folders[BOX3].id)
        await waitCovers(page)
        await clickCell(page, 'IMG_0005.png')
        await page.keyboard.press('F2')
        await page.waitForSelector('[data-testid=rename-file-name]', { visible: true })
        await page.$eval('[data-testid=rename-file-name]', (el) => el.select())
        await page.keyboard.type('img_0006.png')
        await page.click('[data-testid=rename-file-submit]')
        await page.waitForSelector('[data-testid=rename-file-error]', { visible: true, timeout: 10000 })
        const msg = await page.$eval('[data-testid=rename-file-error]', (el) => el.textContent.trim())
        assert(msg.includes('已有名为「img_0006.png」的文件'), `撞名的提示不对：${msg}`)
        console.log(`  断言 错误提示：${msg}`)
      },
      async after(page) {
        await page.keyboard.press('Escape')
        const ctx = await ctxPromise
        const ch = await ctx.api('GET', `/api/folders/${ctx.folders[BOX3].id}/children`)
        assert(ch.files.some((f) => f.file_name === 'IMG_0005.png'), '撞名失败后名字应该不变')
      },
      delay: 300,
    },
    // 整理流程：新建 → 上传带两层子文件夹的目录 → 移动 3 个 → 改名 → 删除子文件夹 → 用接口核对。
    {
      name: 'files-organize',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await context(page)
        // 1. 新建文件夹（界面上）。
        await openFolderId(page, ctx.project.root_folder_id)
        const name = `截图验证 M1-12 整理 ${stamp()}`
        await page.click('[data-testid=new-folder]')
        await page.waitForSelector('[data-testid=folder-form-name]', { visible: true })
        await page.type('[data-testid=folder-form-name]', name)
        await page.type('[data-testid=folder-form-remark]', '截图脚本临时建的，做完会删掉')
        await page.click('[data-testid=folder-form-submit]')
        await waitToast(page, `已新建文件夹「${name}」`)
        const rootCh = await ctx.api('GET', `/api/folders/${ctx.project.root_folder_id}/children`)
        organizeTemp = rootCh.folders.find((f) => f.folder_name === name)
        assert(organizeTemp, '新建的文件夹在接口里找不到')
        assert(organizeTemp.remark === '截图脚本临时建的，做完会删掉', `备注不对：${organizeTemp.remark}`)
        console.log(`  1. 新建文件夹「${name}」（带备注）`)

        // 2. 用接口传一个带两层子文件夹的样本目录。
        const { top } = await uploadSampleFolder(ctx, organizeTemp.id, '  2. ')
        // 结构和顺序（接口）。
        const batch = await resolvePath(ctx.api, organizeTemp.id, top)
        assert(batch.children.folders.map((f) => f.folder_name).join('|') === '盒 001|盒 002', `扫描批次的子文件夹不对：${batch.children.folders.map((f) => f.folder_name)}`)
        const box1 = await resolvePath(ctx.api, organizeTemp.id, `${top}/盒 001`)
        const box2 = await resolvePath(ctx.api, organizeTemp.id, `${top}/盒 002`)
        const inner = await resolvePath(ctx.api, organizeTemp.id, `${top}/盒 001/卷内`)
        const order = box1.children.files.map((f) => f.file_name).join('|')
        assert(order === 'IMG_1.png|IMG_2.png|IMG_10.png|TIFF_none.tif', `盒 001 里的顺序应该是文件名的自然顺序：${order}`)
        assert(inner.children.files.length === 3, `卷内应有 3 个文件：${inner.children.files.length}`)
        console.log(`     结构：${top}/{盒 001/卷内, 盒 002}；盒 001 的 sort_no 顺序 ${order}（IMG_2 在 IMG_10 前）`)
        // 3. 移动 3 个文件（多选 → 右键 → 移动到… → 盒 002）。
        await openFolderId(page, box1.id)
        await waitCovers(page)
        await clickCell(page, 'IMG_1.png')
        await clickCell(page, 'IMG_2.png', { meta: true })
        await clickCell(page, 'IMG_10.png', { meta: true })
        await clickCell(page, 'IMG_10.png', { button: 'right' })
        await menu(page, 'menu-move')
        await pickFolder(page, '盒 002')
        await page.click('[data-testid=folder-picker-confirm]')
        await waitToast(page, '已移动 3 个文件')
        await page.waitForSelector('[data-testid=folder-picker]', { hidden: true })
        await waitName(page, 'IMG_1.png', false)
        console.log('  3. 多选移动 3 个文件到 盒 002（其中 2 个重名自动改名）')

        // 4. 改名（F2）。
        await clickCell(page, 'TIFF_none.tif')
        await page.waitForSelector('[data-testid=selection-bar][data-kind=file]')
        await page.keyboard.press('F2')
        await page.waitForSelector('[data-testid=rename-file-name]', { visible: true })
        await page.$eval('[data-testid=rename-file-name]', (el) => el.select())
        await page.keyboard.type('TIFF_无压缩.tif')
        await page.keyboard.press('Enter')
        await page.waitForSelector('[data-testid=rename-file]', { hidden: true })
        await waitName(page, 'TIFF_无压缩.tif', true)
        console.log('  4. F2 把 TIFF_none.tif 改名为 TIFF_无压缩.tif')

        // 5. 删除子文件夹（右键 → 删除 → 确认）。
        await clickCell(page, '卷内', { kind: 'folder', button: 'right' })
        await menu(page, 'menu-delete')
        await page.waitForSelector('[role=alertdialog]', { visible: true })
        const confirmText = await page.$eval('[role=alertdialog]', (el) => el.textContent)
        assert(confirmText.includes('3 个文件'), `删除文件夹的确认框应说明里面有 3 个文件：${confirmText}`)
        const buttons = await page.$$('[role=alertdialog] button')
        await buttons[buttons.length - 1].click()
        await waitToast(page, '已删除文件夹「卷内」')
        await waitName(page, '卷内', false)
        console.log('  5. 删除子文件夹 卷内（3 个文件）')

        // 核对（接口）。
        const after1 = await ctx.api('GET', `/api/folders/${box1.id}/children`)
        const after2 = await ctx.api('GET', `/api/folders/${box2.id}/children`)
        const afterTop = await ctx.api('GET', `/api/folders/${batch.id}/children`)
        assert(after1.folders.length === 0, `盒 001 下不应再有子文件夹：${after1.folders.map((f) => f.folder_name)}`)
        assert(after1.files.map((f) => f.file_name).join('|') === 'TIFF_无压缩.tif', `盒 001 的文件不对：${after1.files.map((f) => f.file_name)}`)
        const names2 = after2.files.map((f) => f.file_name).join('|')
        assert(names2 === 'IMG_1.png|IMG_2.png|IMG_1 (1).png|IMG_2 (1).png|IMG_10.png', `盒 002 的文件和顺序不对：${names2}`)
        assert(afterTop.files.map((f) => f.file_name).join('|') === 'JPEG_A4.jpg', '扫描批次里应只剩 JPEG_A4.jpg')
        const gone = inner.children.files.map((f) => f.file_id)
        const missing = (await ctx.api('POST', '/api/files/batch', { file_ids: gone })).missing
        assert(missing.length === 3, `卷内的 3 个文件应在 batch 的 missing 里：${missing.length}`)
        // 文件日志：剩下的每个文件 upload 1 条，移动过的再加 move，改名的再加 rename。
        let logTotal = 0
        const expectLogs = {
          'IMG_1.png': ['upload'],
          'IMG_2.png': ['upload'],
          'IMG_1 (1).png': ['move', 'upload'],
          'IMG_2 (1).png': ['move', 'upload'],
          'IMG_10.png': ['move', 'upload'],
          'TIFF_无压缩.tif': ['rename', 'upload'],
          'JPEG_A4.jpg': ['upload'],
        }
        for (const f of [...after2.files, ...after1.files, ...afterTop.files]) {
          const logs = (await ctx.api('GET', `/api/files/${f.file_id}/logs`)).logs
          const actions = logs.map((l) => l.action).join(',')
          assert(actions === expectLogs[f.file_name].join(','), `${f.file_name} 的日志应为 ${expectLogs[f.file_name]}：${actions}`)
          if (f.file_name === 'IMG_1 (1).png') {
            assert(logs[0].detail.renamed_from === 'IMG_1.png', `移动时自动改名的日志应有 renamed_from：${JSON.stringify(logs[0].detail)}`)
          }
          logTotal += logs.length
        }
        assert(logTotal === 11, `剩下 7 个文件的日志共应 11 条：${logTotal}`)
        console.log(
          `  断言 接口核对：结构 ${top}/{盒 001, 盒 002}（卷内已删）；盒 001 = TIFF_无压缩.tif；盒 002 = ${names2.replaceAll('|', '、')}；` +
            `卷内 3 个文件在 batch 的 missing 里；剩下 7 个文件的日志共 ${logTotal} 条（upload 7、move 3、rename 1）`,
        )
        // 截图：盒 002（移动进来的、自动改名的）。
        await openFolderId(page, box2.id)
        await waitCovers(page)
      },
      async after() {
        await dropTemp(organizeTemp)
        organizeTemp = null
      },
      delay: 500,
    },
    // 实时：两个页面打开同一个文件夹，一个页面里删除、移动文件，另一个页面 3 秒内更新。
    {
      name: 'files-realtime',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await context(page)
        const seed = `M1-12 realtime ${stamp()} ${Math.random()}`
        realtimeTemp = await makeTemp(ctx, '实时')
        const sub = await ctx.api('POST', '/api/folders', { parent_id: realtimeTemp.id, folder_name: '移入目标' })
        for (let i = 1; i <= 4; i++) await apiUploadPng(ctx, realtimeTemp.id, `RT_${i}.png`, scanPng(`${seed}#${i}`, i))
        // 页面 B（同一个登录会话里的另一个标签页）先打开；之后页面 A 在前台操作，B 在后台用定时轮询检查
        // （后台标签页没有 requestAnimationFrame；screenshots.mjs 关掉了后台标签页的定时器节流）。
        realtimePage = await page.browserContext().newPage()
        await realtimePage.setViewport({ width: 1440, height: 900 })
        await openFolderId(realtimePage, realtimeTemp.id)
        await realtimePage.waitForSelector('[data-testid=files-page][data-stream=open]', { timeout: 15000 })
        await waitCovers(realtimePage)
        await page.bringToFront()
        await openFolderId(page, realtimeTemp.id)
        await page.waitForSelector('[data-testid=files-page][data-stream=open]', { timeout: 15000 })
        await waitCovers(page)
        const fileNames = (p) =>
          p.$$eval('[data-testid=file-cell] [title]', (els) => els.map((e) => e.getAttribute('title')).filter((t) => t?.startsWith('RT_')))

        // 页面 A 删除 RT_1（右键 → 删除 → 确认），页面 B 3 秒内不再显示它。
        await clickCell(page, 'RT_1.png', { button: 'right' })
        await menu(page, 'menu-delete')
        await page.waitForSelector('[role=alertdialog]', { visible: true })
        const buttons = await page.$$('[role=alertdialog] button')
        const t0 = Date.now()
        await buttons[buttons.length - 1].click()
        await realtimePage.waitForFunction(
          () => ![...document.querySelectorAll('[data-testid=file-cell] [title]')].some((el) => el.getAttribute('title') === 'RT_1.png'),
          { timeout: 3000, polling: 50 },
        )
        const msDelete = Date.now() - t0
        await page.waitForSelector('[role=alertdialog]', { hidden: true })

        // 页面 A 移动 RT_2 到子文件夹（选择栏 → 移动到…），页面 B 3 秒内不再显示它、子文件夹显示 1 个文件。
        await clickCell(page, 'RT_2.png')
        await page.waitForSelector('[data-testid=selection-bar][data-kind=file]')
        await page.click('[data-testid=bar-move]')
        await pickFolder(page, '移入目标')
        const t1 = Date.now()
        await page.click('[data-testid=folder-picker-confirm]')
        await realtimePage.waitForFunction(
          (folderId) => {
            const gone = ![...document.querySelectorAll('[data-testid=file-cell] [title]')].some((el) => el.getAttribute('title') === 'RT_2.png')
            const subCell = document.querySelector(`[data-testid=folder-cell][data-folder-id="${folderId}"]`)
            return gone && subCell?.textContent?.includes('1 个文件')
          },
          { timeout: 3000, polling: 50 },
          sub.id,
        )
        const msMove = Date.now() - t1
        await page.waitForSelector('[data-testid=folder-picker]', { hidden: true })

        // 页面 A 复制 RT_3（⌘/Ctrl+C，复制到当前文件夹，自动改名为 RT_3 (1).png），页面 B 3 秒内出现副本。
        await clickCell(page, 'RT_3.png')
        await page.waitForSelector('[data-testid=selection-bar][data-kind=file]')
        const t2 = Date.now()
        await page.keyboard.down('Meta')
        await page.keyboard.press('c')
        await page.keyboard.up('Meta')
        await realtimePage.waitForFunction(
          () => [...document.querySelectorAll('[data-testid=file-cell] [title]')].some((el) => el.getAttribute('title') === 'RT_3 (1).png'),
          { timeout: 3000, polling: 50 },
        )
        const msCopy = Date.now() - t2
        const left = await fileNames(realtimePage)
        assert(left.join('|') === 'RT_3.png|RT_4.png|RT_3 (1).png', `另一个页面应是 RT_3、RT_4、RT_3 (1)：${left}`)
        console.log(
          `  断言 实时：页面 A 删除后 ${msDelete} ms、移动后 ${msMove} ms、复制（⌘C）后 ${msCopy} ms，页面 B 都已更新（≤ 3000 ms）；` +
            `B 现在是 ${left.join('、')}，子文件夹显示 1 个文件`,
        )
        await realtimePage.bringToFront()
        await new Promise((r) => setTimeout(r, 300))
        // 另存一张页面 B 的截图（和其他截图放在同一个目录：--out，默认清单的 out）。
        const outArg = process.argv.indexOf('--out')
        const outDir = outArg > 0 ? path.resolve(process.argv[outArg + 1]) : path.resolve('../docs/screenshots/m1')
        await realtimePage.screenshot({ path: path.join(outDir, 'files-realtime-other.png') })
        await page.bringToFront()
      },
      async after() {
        await realtimePage?.close()
        realtimePage = null
        await dropTemp(realtimeTemp)
        realtimeTemp = null
      },
      delay: 300,
    },
    // 文件夹：F2 改名、右键改备注、移动到…（选择框里不能选它自己和它的子树）、重名提示；根文件夹只能改备注。
    {
      name: 'files-folder-ops',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await context(page)
        folderOpsTemp = await makeTemp(ctx, '文件夹')
        const a = await ctx.api('POST', '/api/folders', { parent_id: folderOpsTemp.id, folder_name: '盒 A' })
        const b = await ctx.api('POST', '/api/folders', { parent_id: folderOpsTemp.id, folder_name: '盒 B' })
        await ctx.api('POST', '/api/folders', { parent_id: a.id, folder_name: '卷 1' })
        await openFolderId(page, folderOpsTemp.id)

        // F2 改名；先试一个重名的（只差大小写）：对话框里提示，不改。
        await clickCell(page, '盒 A', { kind: 'folder' })
        await page.waitForSelector('[data-testid=selection-bar][data-kind=folder]')
        await page.keyboard.press('F2')
        await page.waitForSelector('[data-testid=folder-form-name]', { visible: true })
        await page.$eval('[data-testid=folder-form-name]', (el) => el.select())
        await page.keyboard.type('盒 b')
        await page.click('[data-testid=folder-form-submit]')
        await page.waitForSelector('[data-testid=folder-form-error]', { visible: true })
        const conflict = await page.$eval('[data-testid=folder-form-error]', (el) => el.textContent.trim())
        assert(conflict.includes('已有名为「盒 b」的文件夹'), `文件夹重名的提示不对：${conflict}`)
        // 保留名：本地就拦下，文案同 server。
        await page.$eval('[data-testid=folder-form-name]', (el) => el.select())
        await page.keyboard.type('NUL')
        await page.click('[data-testid=folder-form-submit]')
        await page.waitForFunction(() => document.querySelector('[data-testid=folder-form-error]')?.textContent?.includes('Windows 保留名'))
        const reserved = await page.$eval('[data-testid=folder-form-error]', (el) => el.textContent.trim())
        await page.$eval('[data-testid=folder-form-name]', (el) => el.select())
        await page.keyboard.type('盒 A-1')
        await page.click('[data-testid=folder-form-submit]')
        await page.waitForSelector('[data-testid=folder-form]', { hidden: true })
        await waitName(page, '盒 A-1', true)

        // 右键 → 修改备注。
        await clickCell(page, '盒 A-1', { kind: 'folder', button: 'right' })
        await menu(page, 'menu-remark')
        await page.waitForSelector('[data-testid=folder-form-remark]', { visible: true })
        await page.type('[data-testid=folder-form-remark]', '3 号库 B 架 1 层')
        await page.click('[data-testid=folder-form-submit]')
        await page.waitForSelector('[data-testid=folder-form]', { hidden: true })

        // 右键 → 移动到… → 盒 B；选择框里它自己（和子文件夹）是禁用的。
        await clickCell(page, '盒 A-1', { kind: 'folder', button: 'right' })
        await menu(page, 'menu-move')
        await page.waitForSelector(`[data-testid=folder-picker] [data-testid=tree-folder][data-folder-id="${a.id}"]`, { timeout: 10000 })
        const selfDisabled = await page.$eval(`[data-testid=folder-picker] [data-testid=tree-folder][data-folder-id="${a.id}"]`, (el) =>
          el.hasAttribute('data-disabled'),
        )
        assert(selfDisabled, '移动文件夹时，选择框里它自己应该是禁用的（不能成环）')
        await pickFolder(page, '盒 B')
        await page.click('[data-testid=folder-picker-confirm]')
        await waitToast(page, '已把文件夹「盒 A-1」移动到')
        await waitName(page, '盒 A-1', false)

        // 接口核对。
        const top = await ctx.api('GET', `/api/folders/${folderOpsTemp.id}/children`)
        const inB = await ctx.api('GET', `/api/folders/${b.id}/children`)
        const moved = await ctx.api('GET', `/api/folders/${a.id}`)
        assert(top.folders.map((f) => f.folder_name).join('|') === '盒 B', `临时文件夹下应只剩 盒 B：${top.folders.map((f) => f.folder_name)}`)
        assert(inB.folders[0]?.folder_name === '盒 A-1' && inB.folders[0]?.remark === '3 号库 B 架 1 层', `盒 B 里应是改过名和备注的 盒 A-1：${JSON.stringify(inB.folders)}`)
        const bItem = await ctx.api('GET', `/api/folders/${b.id}`)
        assert(
          moved.parent_id === b.id && moved.path_code.startsWith(bItem.path_code) && moved.path_code.length === bItem.path_code.length + 6,
          `盒 A-1 的位置不对：${moved.path_code}（盒 B 是 ${bItem.path_code}）`,
        )
        assert(inB.folders[0].folder_count === 1, '盒 A-1 下的 卷 1 应跟着移动')

        // 根文件夹：编辑时名称不能改（跟着项目走），只能改备注。
        await openFolderId(page, ctx.project.root_folder_id)
        await page.click('[data-testid=edit-current-folder]')
        await page.waitForSelector('[data-testid=folder-form-name]', { visible: true })
        const nameDisabled = await page.$eval('[data-testid=folder-form-name]', (el) => el.disabled)
        assert(nameDisabled, '根文件夹的名称输入框应该是禁用的')
        console.log(
          `  断言 文件夹：重名提示「${conflict}」；保留名提示「${reserved}」；F2 改名、右键改备注、移动到 盒 B（选择框里它自己禁用）后接口核对正确，` +
            `子文件夹跟着移动，路径编码 ${moved.path_code}；根文件夹的编辑框里名称禁用、只能改备注`,
        )
      },
      async after(page) {
        await page.keyboard.press('Escape')
        await dropTemp(folderOpsTemp)
        folderOpsTemp = null
      },
      delay: 400,
    },
    // 壳里（Wails 桥的模拟）：上传按钮调 pick_and_enqueue_upload，提示"已加入上传队列"。
    {
      name: 'files-shell-upload',
      path: '/login',
      shell: true,
      init: (page) => installWailsMock(page, { serverUrl: 'http://192.168.1.10:9080' }, organizeShellMock),
      waitFor: '#username',
      async prepare(page) {
        await page.type('#username', admin)
        await page.type('#password', adminPassword)
        await page.click('[data-testid=login-submit]')
        await page.waitForSelector('[data-testid=side-nav]', { timeout: 15000 })
        const ctx = await context(page)
        const folderId = ctx.folders[BOX3].id
        await page.goto(`${shellOrigin(ctx.origin)}/files?folder=${folderId}`, { waitUntil: IDLE })
        await page.waitForSelector('[data-testid=files-page][data-list=ready]', { timeout: 15000 })
        await page.click('[data-testid=upload-folder]')
        await waitToast(page, '已加入上传队列：5 个文件')
        await page.click('[data-testid=upload-files]')
        await waitToast(page, '已加入上传队列：3 个文件')
        const calls = await page.evaluate(() => window.__shellCalls.filter((c) => c.cmd === 'pick_and_enqueue_upload').map((c) => c.args))
        assert(calls.length === 2, `应调用 2 次 pick_and_enqueue_upload：${JSON.stringify(calls)}`)
        assert(calls[0].folderId === folderId && calls[0].mode === 'folder', `第 1 次参数不对：${JSON.stringify(calls[0])}`)
        assert(calls[1].folderId === folderId && calls[1].mode === 'files', `第 2 次参数不对：${JSON.stringify(calls[1])}`)
        const uploads = await page.evaluate(() => window.__shellCalls.filter((c) => c.cmd === 'api_fetch' && /\/files$/.test(c.args.path)).length)
        assert(uploads === 0, '壳里不应从页面发上传请求')
        console.log(`  断言 壳：上传文件夹、上传文件各调 1 次 pick_and_enqueue_upload（folderId=BOX3，mode=folder/files），提示"已加入上传队列"，页面没有自己发上传请求`)
      },
      async after(page) {
        // 截完图再试"下载原图"：壳里调 open_external（系统浏览器打开 original_url）。
        await clickCell(page, 'IMG_0002.png', { button: 'right' })
        await menu(page, 'menu-download')
        await page.waitForFunction(() => window.__shellCalls.some((c) => c.cmd === 'open_external'), { timeout: 10000 })
        const url = new URL(await page.evaluate(() => window.__shellCalls.find((c) => c.cmd === 'open_external').args.url))
        assert(url.protocol === 'http:' && url.pathname.endsWith('/v1.png') && url.searchParams.has('X-Amz-Signature'), `open_external 的地址不对：${url}`)
        console.log(`  断言 壳里下载原图：调 open_external 打开 ${url.origin}${url.pathname}（预签名 URL）`)
      },
      delay: 300,
    },
  ],
}
