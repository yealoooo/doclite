// M1-10 的截图清单和断言：文件目录页（浏览和看图）。
//
//   INIT_ADMIN_PASSWORD=admin123456 npm run screenshots -- --spec scripts/shots/m1-files.mjs --base <前端地址>
//
// 前端地址是 vite preview / dev（/api 代理到 server），或者托管了 web/dist 的 server 本身。脚本在页面之外调接口时
// 也走这个地址。
//
// 数据来自 seed:demo 和 seed:files，用到 DEMO-A 里的：
//   /2025 文书档案/盒 WS-2025-001（2000 页）、盒 WS-2025-002（48 页，含低 DPI 和无封面）、
//   盒 WS-2025-003（24 页，两个子文件夹）。换库重建时（S=--base <server> --user demo_scanner:demo123456 --project DEMO-A）：
//   npm run seed:files -- $S --folder "2025 文书档案/盒 WS-2025-001" --remark "3 号库 A 架 11 层" --count 2000 --low-dpi 3 --no-cover 3
//   npm run seed:files -- $S --folder "2025 文书档案/盒 WS-2025-002" --remark "3 号库 A 架 12 层" --count 48 --low-dpi 4 --no-cover 4
//   npm run seed:files -- $S --folder "2025 文书档案/盒 WS-2025-003" --remark "3 号库 A 架 13 层" --count 24
//   npm run seed:files -- $S --folder "2025 文书档案/盒 WS-2025-003/补扫" --count 6 --start 101
//   npm run seed:files -- $S --folder "2025 文书档案/盒 WS-2025-003/附件（封面、信封）" --count 4 --start 201 --no-cover 1
//   （按这个顺序建，文件夹的 sort_no 才是 001、002、003。）
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - 2000 页的文件夹滚到中间时，DOM 里的封面节点少于 200 个；
// - 每次 batch 请求不超过 500 个 id（全程监听；在 2000 页的文件夹里 Ctrl+A，多选汇总要取全部 2000 个的
//   图片信息，验证会拆成每批最多 500 个）；
// - 在页面之外用接口移动、删除一个文件，页面在 3 秒内更新（SSE），被删的文件从列表中消失。
//   这一步在根文件夹下临时建一个"截图验证 <时间>"文件夹、传 4 页，做完后把这个文件夹删掉。
// "无封面"占位用 --no-cover 造的文件看；url_error 的效果由脚本改写 batch 的响应来造（页面里包一层 fetch）。

import { makeScanPage } from '../lib/scan-pages.mjs'

const demo = 'demo123456'
const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const PROJECT = 'DEMO-A'
const BOX1 = '2025 文书档案/盒 WS-2025-001'
const BOX2 = '2025 文书档案/盒 WS-2025-002'
const BOX3 = '2025 文书档案/盒 WS-2025-003'
const IDLE = 'networkidle2'

// ---------- 页面之外调接口（admin） ----------

let apiState = null

async function api(origin, method, path, body) {
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
  if (!res.ok) throw new Error(`${method} ${path} → ${res.status} ${data?.code}: ${data?.message}`)
  return data
}

let ctxPromise = null
/** 登录 admin，取 DEMO-A 和几个文件夹的 id（只做一次）。 */
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    await api(origin, 'POST', '/api/auth/login', { username: admin, password: adminPassword })
    const projects = await api(origin, 'GET', '/api/projects')
    const project = projects.find((p) => p.project_code === PROJECT)
    if (!project) throw new Error(`找不到项目 ${PROJECT}，先跑 seed:demo`)
    const folders = {}
    for (const path of [BOX1, BOX2, BOX3]) {
      let id = project.root_folder_id
      for (const name of path.split('/')) {
        const ch = await api(origin, 'GET', `/api/folders/${id}/children`)
        const f = ch.folders.find((x) => x.folder_name === name)
        if (!f) throw new Error(`找不到文件夹 ${path}，先按 roadmap 的命令跑 seed:files`)
        id = f.id
      }
      folders[path] = { id, children: await api(origin, 'GET', `/api/folders/${id}/children`) }
    }
    return { origin, project, folders }
  })()
  return ctxPromise
}

// ---------- 观察 batch 请求 ----------

const batchSizes = []
const watched = new WeakSet()
function watchBatches(page) {
  if (watched.has(page)) return
  watched.add(page)
  page.on('request', (req) => {
    if (req.method() === 'POST' && req.url().endsWith('/api/files/batch')) {
      try {
        batchSizes.push(JSON.parse(req.postData() ?? '{}').file_ids?.length ?? 0)
      } catch {
        batchSizes.push(-1)
      }
    }
  })
}

function assertBatches(label) {
  const max = Math.max(0, ...batchSizes)
  if (batchSizes.some((n) => n > 500 || n < 0)) throw new Error(`batch 请求超过 500 个 id：${batchSizes.join(',')}`)
  console.log(`  断言 ${label}：已发 ${batchSizes.length} 次 batch，最多 ${max} 个 id（≤ 500）`)
}

// ---------- 页面操作 ----------

async function openFolder(page, path, view = 'icons') {
  watchBatches(page)
  const { origin, folders } = await context(page)
  await page.goto(`${origin}/files?folder=${folders[path].id}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=files-page][data-list=ready]', { timeout: 15000 })
  await setView(page, view)
  await waitCovers(page)
}

async function setView(page, view) {
  await page.click(`[data-testid=view-${view}]`)
  await page.waitForSelector(`[data-testid=file-scroll][data-view=${view}]`, { timeout: 5000 })
}

/** 等可见的封面都加载完（或确定是占位）。 */
async function waitCovers(page, timeout = 15000) {
  await page.waitForFunction(
    () => {
      const covers = [...document.querySelectorAll('[data-testid=file-cover]')]
      return covers.length > 0 && covers.every((c) => !['loading', 'loading-image'].includes(c.dataset.coverState))
    },
    { timeout },
  )
}

async function clickFile(page, fileName, mods = {}) {
  const handle = await page.evaluateHandle((name) => {
    return [...document.querySelectorAll('[data-testid=file-cell],[data-testid=file-row]')].find(
      (el) => el.querySelector('[title]')?.getAttribute('title') === name || el.textContent?.includes(name),
    )
  }, fileName)
  const el = handle.asElement()
  if (!el) throw new Error(`页面上找不到文件 ${fileName}`)
  if (mods.ctrl) await page.keyboard.down('Meta')
  await el.click()
  if (mods.ctrl) await page.keyboard.up('Meta')
}

function fileId(ctx, path, name) {
  const f = ctx.folders[path].children.files.find((x) => x.file_name === name)
  if (!f) throw new Error(`${path} 里没有 ${name}`)
  return f
}

// ---------- url_error：改写 batch 响应 ----------

let rewriteScript = null
async function injectUrlErrors(page, ids) {
  rewriteScript = await page.evaluateOnNewDocument((errorIds) => {
    const orig = window.fetch.bind(window)
    window.fetch = async (input, init) => {
      const res = await orig(input, init)
      const url = typeof input === 'string' ? input : input.url
      if (!url.endsWith('/api/files/batch') || !res.ok) return res
      const data = await res.clone().json()
      for (const it of data.items) {
        if (!errorIds.includes(it.file_id)) continue
        it.cover_url = null
        it.original_url = null
        it.url_expires_at = null
        it.url_error = { code: 'storage_unavailable', message: '存储「旧 NAS 存储」已停用，图片暂时取不到' }
      }
      return new Response(JSON.stringify(data), { status: res.status, headers: res.headers })
    }
  }, ids)
}

// ---------- SSE 断言用的临时文件夹 ----------

let sseTemp = null
/** files-2000-middle 的 prepare 走完了（after 里才做 Ctrl+A 的断言）。 */
let middleReady = false

/**
 * 在页面里包一层 fetch：统计每个 file_id 出现在几次 batch 请求里（`window.__batchHits`）；
 * `window.__delayBatchId` 设了值时，含这个 id 的 batch 响应晚 2 秒才交给页面（让请求"在路上"久一点）。
 */
let batchProbe = null
async function injectBatchProbe(page) {
  batchProbe = await page.evaluateOnNewDocument(() => {
    window.__batchHits = {}
    window.__delayBatchId = null
    const orig = window.fetch.bind(window)
    window.fetch = async (input, init) => {
      const url = typeof input === 'string' ? input : input.url
      if (!url.endsWith('/api/files/batch') || typeof init?.body !== 'string') return orig(input, init)
      const ids = JSON.parse(init.body).file_ids ?? []
      for (const id of ids) window.__batchHits[id] = (window.__batchHits[id] ?? 0) + 1
      const delay = window.__delayBatchId && ids.includes(window.__delayBatchId)
      const res = await orig(input, init)
      if (delay) await new Promise((r) => setTimeout(r, 2000))
      return res
    }
  })
}

/** 一个 file_id 出现在几次 batch 请求里。 */
function batchHits(page, id) {
  return page.evaluate((x) => window.__batchHits?.[x] ?? 0, id)
}

async function uploadPage(origin, folderId, name, seed, pageNo) {
  const p = makeScanPage({ seed, pageNo, dpi: 200 })
  const form = new FormData()
  form.append('file_name', name)
  form.append('cover', new Blob([p.cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([p.png], { type: 'image/png' }), 'page.png')
  return api(origin, 'POST', `/api/folders/${folderId}/files`, form)
}

export default {
  out: '../docs/screenshots/m1',
  // /files 常开一条 SSE 连接，等不到 networkidle0。
  waitUntil: IDLE,
  accounts: {
    [admin]: adminPassword,
    demo_scanner: demo,
  },
  shots: [
    // admin：盒 003（有子文件夹）图标视图；盒 002 列表视图（审核状态、规格、低 DPI）。
    {
      name: 'files-admin-icons',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      prepare: (page) => openFolder(page, BOX3, 'icons'),
    },
    {
      name: 'files-admin-list',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      prepare: (page) => openFolder(page, BOX2, 'list'),
    },
    // demo_scanner（只能进 DEMO-A）：盒 002 图标视图（低 DPI 标记、无封面占位）；盒 003 列表视图。
    {
      name: 'files-scanner-icons',
      user: 'demo_scanner',
      path: '/files',
      waitFor: '[data-testid=files-page]',
      prepare: (page) => openFolder(page, BOX2, 'icons'),
    },
    {
      name: 'files-scanner-list',
      user: 'demo_scanner',
      path: '/files',
      waitFor: '[data-testid=files-page]',
      prepare: (page) => openFolder(page, BOX3, 'list'),
    },
    // 详情面板：选中一张低 DPI 的页。
    {
      name: 'files-detail',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        await openFolder(page, BOX2, 'icons')
        await clickFile(page, 'IMG_0005.png')
        await page.waitForSelector('[data-testid=file-logs] li', { timeout: 10000 })
        await page.waitForSelector('[data-testid=detail-storage-key]', { timeout: 10000 })
      },
    },
    // 快照历史对话框。
    {
      name: 'files-snapshots',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        await openFolder(page, BOX2, 'icons')
        await clickFile(page, 'IMG_0017.png')
        await page.waitForSelector('[data-testid=open-snapshots]')
        await page.click('[data-testid=open-snapshots]')
        await page.waitForSelector('[data-testid=snapshot-item]', { timeout: 10000 })
        await page.waitForFunction(
          () => [...document.querySelectorAll('[data-testid=snapshot-dialog] [data-testid=file-cover]')].every(
            (c) => !['loading', 'loading-image'].includes(c.dataset.coverState),
          ),
          { timeout: 10000 },
        )
      },
      delay: 500,
    },
    // 占位图：--no-cover 造的"无封面"，加上改写 batch 响应造的 url_error（存储不可用）。
    {
      name: 'files-placeholders',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await context(page)
        const ids = ['IMG_0009.png', 'IMG_0010.png'].map((n) => fileId(ctx, BOX2, n).file_id)
        await injectUrlErrors(page, ids)
        await openFolder(page, BOX2, 'icons')
        const states = await page.$$eval('[data-testid=file-cover]', (els) => els.map((e) => e.dataset.coverState))
        const none = states.filter((s) => s === 'none').length
        const error = states.filter((s) => s === 'error').length
        if (none < 1 || error < 2) throw new Error(`占位图不对：none=${none} error=${error}`)
        console.log(`  占位图：无封面 ${none} 个，存储不可用（url_error）${error} 个`)
        await clickFile(page, 'IMG_0009.png')
        await page.waitForSelector('[data-testid=file-logs] li', { timeout: 10000 })
      },
      async after(page) {
        if (rewriteScript) await page.removeScriptToEvaluateOnNewDocument(rewriteScript.identifier)
        rewriteScript = null
      },
    },
    // 2000 页的文件夹滚到中间：DOM 里的封面节点少于 200 个；之后 Ctrl+A，汇总要取全部 2000 个的图片信息。
    {
      name: 'files-2000-middle',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        await openFolder(page, BOX1, 'icons')
        const total = await page.$eval('[data-testid=folder-summary]', (el) => el.textContent)
        if (!total.includes('2000 个文件')) throw new Error(`盒 001 不是 2000 页：${total}`)
        await page.$eval('[data-testid=file-scroll]', (el) => {
          el.scrollTop = (el.scrollHeight - el.clientHeight) / 2
        })
        await new Promise((r) => setTimeout(r, 400))
        await waitCovers(page)
        const covers = await page.$$eval('[data-testid=file-cover]', (els) => els.length)
        const names = await page.$$eval('[data-testid=file-cell]', (els) =>
          els.map((e) => e.textContent?.match(/IMG_\d+/)?.[0]).filter(Boolean),
        )
        if (covers >= 200) throw new Error(`封面节点 ${covers} 个，应少于 200`)
        console.log(`  断言 2000 页滚到中间：DOM 里有 ${covers} 个封面节点（< 200），渲染的是 ${names[0]}…${names[names.length - 1]}`)
        assertBatches('滚动之后')
        middleReady = true
      },
      async after(page) {
        // after 在前面失败时也会执行（screenshots.mjs 放在 finally 里）：前面没走完就不做全选的断言。
        if (!middleReady) return
        middleReady = false
        const before = batchSizes.length
        await page.focus('[data-testid=file-scroll]')
        await page.keyboard.down('Meta')
        await page.keyboard.press('a')
        await page.keyboard.up('Meta')
        await page.waitForFunction(
          () => {
            const s = document.querySelector('[data-testid=selection-summary]')?.closest('[data-testid=detail-panel]')
            return s && !s.textContent?.includes('已统计')
          },
          { timeout: 20000 },
        )
        const sizes = batchSizes.slice(before)
        if (!sizes.includes(500)) throw new Error(`Ctrl+A 之后应有 500 个 id 的 batch，实际：${sizes.join(',')}`)
        console.log(`  Ctrl+A 全选 2000 页：汇总取图片信息发了 ${sizes.length} 次 batch（${sizes.join('、')} 个 id）`)
        assertBatches('全程')
      },
    },
    // SSE：页面之外移动、删除文件，页面 3 秒内更新。
    {
      name: 'files-sse',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        watchBatches(page)
        const ctx = await context(page)
        const stamp = new Date().toISOString().replace(/[-:T]/g, '').slice(0, 14)
        const tmp = await api(ctx.origin, 'POST', '/api/folders', {
          parent_id: ctx.project.root_folder_id,
          folder_name: `截图验证 ${stamp}`,
          remark: 'M1-10 截图脚本临时建的，做完会删掉',
        })
        sseTemp = tmp
        const target = await api(ctx.origin, 'POST', '/api/folders', { parent_id: tmp.id, folder_name: '移入目标' })
        const files = []
        for (let i = 1; i <= 4; i++) {
          files.push(await uploadPage(ctx.origin, tmp.id, `SSE_${i}.png`, `sse-check ${stamp} #${i}`, i))
        }
        await injectBatchProbe(page)
        await page.goto(`${ctx.origin}/files?folder=${tmp.id}`, { waitUntil: IDLE })
        await page.waitForSelector('[data-testid=files-page][data-list=ready][data-stream=open]', { timeout: 15000 })
        await page.waitForFunction(() => document.querySelectorAll('[data-testid=file-cell]').length === 4, { timeout: 10000 })
        await waitCovers(page)

        // 图片信息请求还在路上时收到 files.changed：结果回来后要再取一次（M1-10 审查第 3 条）。
        // 改名两次：第一次触发一个 batch（让它在路上停 2 秒），第二次的事件正好落在它在路上的时候。
        const renamed = files[2]
        const hits0 = await batchHits(page, renamed.file_id)
        await page.evaluate((id) => {
          window.__delayBatchId = id
        }, renamed.file_id)
        await api(ctx.origin, 'PATCH', `/api/folder-files/${renamed.folder_file_id}`, { file_name: 'SSE_3a.png' })
        await page.waitForFunction((id, n) => (window.__batchHits[id] ?? 0) >= n, { timeout: 3000 }, renamed.file_id, hits0 + 1)
        await api(ctx.origin, 'PATCH', `/api/folder-files/${renamed.folder_file_id}`, { file_name: 'SSE_3b.png' })
        try {
          await page.waitForFunction((id, n) => (window.__batchHits[id] ?? 0) >= n, { timeout: 6000 }, renamed.file_id, hits0 + 2)
        } catch {
          throw new Error('请求在路上时收到的 files.changed 丢了：结果回来后没有再取图片信息')
        }
        await page.evaluate(() => {
          window.__delayBatchId = null
        })
        console.log(
          `  断言 在途失效：batch 在路上时又收到这个文件的 files.changed，结果回来后又取了一次（共 ${await batchHits(page, renamed.file_id)} 次）`,
        )

        const [moved, deleted] = files
        const t0 = Date.now()
        await api(ctx.origin, 'PATCH', `/api/folder-files/${moved.folder_file_id}`, { folder_id: target.id })
        await api(ctx.origin, 'DELETE', `/api/folder-files/${deleted.folder_file_id}`)
        await page.waitForFunction(
          (a, b, folderId) => {
            const gone = !document.querySelector(`[data-file-id="${a}"]`) && !document.querySelector(`[data-file-id="${b}"]`)
            const sub = document.querySelector(`[data-testid=folder-cell][data-folder-id="${folderId}"]`)
            return gone && sub?.textContent?.includes('1 个文件')
          },
          { timeout: 3000 },
          moved.file_id,
          deleted.file_id,
          target.id,
        )
        const ms = Date.now() - t0
        const left = await page.$$eval('[data-testid=file-cell]', (els) => els.length)
        if (left !== 2) throw new Error(`应剩 2 个文件，实际 ${left}`)
        console.log(`  断言 SSE：页面之外移动 1 个、删除 1 个文件后 ${ms} ms 页面更新（≤ 3000），列表剩 ${left} 个，子文件夹显示 1 个文件`)
      },
      async after(page) {
        if (batchProbe) await page.removeScriptToEvaluateOnNewDocument(batchProbe.identifier)
        batchProbe = null
        if (!sseTemp) return
        const ctx = await ctxPromise
        await api(ctx.origin, 'DELETE', `/api/folders/${sseTemp.id}`)
        console.log(`  已删除临时文件夹「${sseTemp.folder_name}」`)
        sseTemp = null
      },
    },
    // FolderPicker（"转到…"）：定位到 盒 003/补扫。
    {
      name: 'folder-picker',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        await openFolder(page, BOX3, 'icons')
        await page.click('[data-testid=goto-folder]')
        await page.waitForSelector('[data-testid=folder-picker] [data-testid=tree-folder]')
        // 展开并选中"补扫"。
        await page.waitForFunction(
          () => [...document.querySelectorAll('[data-testid=folder-picker] [data-testid=tree-folder]')].some((el) => el.textContent?.includes('补扫')),
          { timeout: 10000 },
        )
        // 等对话框的缩放动画结束再点，否则点到的位置会偏。
        await new Promise((r) => setTimeout(r, 400))
        const row = async (name) => {
          for (const it of await page.$$('[data-testid=folder-picker] [data-testid=tree-folder]')) {
            if ((await it.evaluate((el) => el.textContent?.trim())) === name) return it
          }
          throw new Error(`选择框里找不到「${name}」`)
        }

        // 审查第 1 条：先单击选中盒 001，再双击盒 003 的展开箭头，不能把盒 001 当成确认。
        const urlBefore = page.url()
        await (await row('盒 WS-2025-001')).click()
        await page.waitForFunction(
          () => document.querySelector('[data-testid=folder-picker-selected]')?.textContent?.includes('盒 WS-2025-001'),
          { timeout: 5000 },
        )
        await (await (await row('盒 WS-2025-003')).$('[data-chevron]')).click({ count: 2 })
        await new Promise((r) => setTimeout(r, 800))
        const stillOpen = !!(await page.$('[data-testid=folder-picker]'))
        if (!stillOpen || page.url() !== urlBefore) {
          throw new Error(`双击展开箭头把之前选中的文件夹当成了确认（对话框${stillOpen ? '还开着' : '已关闭'}，地址 ${page.url()}）`)
        }
        console.log('  断言 选择框：双击展开箭头不确认，对话框仍开着，地址没变')

        await (await row('补扫')).click()
        await page.waitForFunction(
          () => document.querySelector('[data-testid=folder-picker-selected]')?.textContent?.includes('补扫'),
          { timeout: 5000 },
        )
      },
      delay: 400,
    },
  ],
}
