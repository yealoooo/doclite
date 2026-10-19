// M1-13 扫描上传页（/scan）的截图和断言：
//
//   INIT_ADMIN_PASSWORD=admin123456 npm run screenshots -- --spec scripts/shots/m1-scan.mjs --base <devshell 的地址>
//
// 壳模式的几张和 m1-shell.mjs、m1-cache.mjs 一样，以壳模式的地址打开、注入 Wails 桥的模拟（scripts/lib/wails-mock.mjs）：
// - shell_info / check_server：已配置服务器、连得上（Windows 的数据目录）；api_fetch 转给 devshell 的 /api（真实 server）；
// - 上传队列和热文件夹的命令（upload_status、upload_recent、upload_retry_failed、upload_resume、hotfolder_*）在页面里
//   模拟一份队列；脚本用 `window.__mock.emit(事件, 数据)` 推壳的事件（upload:item、upload:progress、upload:state……，
//   即 Wails 的 _wails.dispatchWailsEvent）；
// 登录：脚本在页面之外登录，把会话 Cookie 放进浏览器上下文（壳模式的地址），直接打开 /scan。
// 账号：demo_scanner / demo123456（只能进 DEMO-A）；新建文件夹那张用 admin（INIT_ADMIN_USERNAME / INIT_ADMIN_PASSWORD），
// 在 DEMO-B 里建一个临时文件夹（上一版用 M1-9 的 HOTFOLDER-TEST 项目，Go 版的库里没有），截图后删掉。
// 目标文件夹用 DEMO-A 的 /2025 文书档案/盒 WS-2025-002（只读，npm run seed 建的，备注"3 号库 A 架 12 层"）。
//
// 截图：
// - scan-shell-uploading：热文件夹运行中，上传中（进度 64%），含内容重复、低 DPI、无封面、失败的行；之后点失败行的
//   "重试"，断言页面调了 upload_retry_failed 且这一行变成"等待写入完成"；
// - scan-shell-race：页面读状态、读最近文件、调命令的期间壳又发了事件（模拟壳的命令慢 150 毫秒）：后到的旧快照、
//   命令的返回值不能盖掉已经收到的新事件——上传中的那行变成已入库、被删的行不再出现、计数和热文件夹的出错提示
//   都是事件里的；"再试一次"（upload_resume）、"暂停"（hotfolder_pause）的返回值也不盖掉期间收到的事件；
// - scan-shell-backlog：积压 300 个排队的文件（列表只留最近 200 个），id 最小的那个开始上传、另一个失败：这两行
//   照样留在列表里；之后到文件目录页，壳发 tray:error，全局弹出提示并带"去扫描上传"，点了回到 /scan；
// - scan-shell-auth-paused：队列因 401 暂停，提示重新登录；
// - scan-shell-auth-resumed：点"重新登录"→ 登录页 → 登录后回到 /scan，暂停解除（模拟壳在登录成功后自动继续）；
// - scan-shell-upgrade-paused：队列因 426 暂停，显示升级提示；
// - scan-shell-new-folder：没设置热文件夹时选目标文件夹，在选择框里新建文件夹（带备注）；之后确认，卡片显示
//   选的时候所在的项目、路径和备注；在项目切换器里换到 DEMO-A，还没保存的目标被清掉；换回来重新选、开始监听，
//   进入运行状态；再换到 DEMO-A，已保存的目标仍显示它自己的项目名（不是当前项目）；
// - scan-browser：浏览器里打开 /scan，只提示"请在客户端中使用"（M10-2）。

import { installWailsMock, useShellSession } from '../lib/wails-mock.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const scanner = 'demo_scanner'
const scannerPassword = 'demo123456'

const BASE = (() => {
  const i = process.argv.indexOf('--base')
  const v = i > 0 ? process.argv[i + 1] : (process.env.DOCLITE_WEB ?? 'http://127.0.0.1:5173')
  return v.replace(/\/$/, '')
})()
const SERVER = 'http://127.0.0.1:9080'
const WATCH = 'D:\\Scan\\Out'
const TARGET_PATH = ['2025 文书档案', '盒 WS-2025-002']
/** 新建文件夹那张在哪个项目里建临时文件夹（要和 DEMO-A 不同，用来看换项目）。 */
const NEW_FOLDER_PROJECT = 'DEMO-B'
const IDLE = 'networkidle2'

// ---------- 页面之外调接口 ----------

async function login(username, password) {
  const res = await fetch(`${BASE}/api/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  if (!res.ok) throw new Error(`登录 ${username} 失败：HTTP ${res.status}`)
  const cookie = res.headers.get('set-cookie')?.split(';')[0]
  if (!cookie) throw new Error('登录响应没有 Set-Cookie')
  return { cookie, me: await res.json() }
}

async function api(cookie, method, path, body) {
  const res = await fetch(BASE + path, {
    method,
    headers: {
      Accept: 'application/json',
      Cookie: cookie,
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await res.text()
  const data = text ? JSON.parse(text) : undefined
  if (!res.ok) throw new Error(`${method} ${path} → ${res.status} ${data?.code}: ${data?.message}`)
  return data
}

async function findFolder(cookie, rootId, names) {
  let id = rootId
  for (const name of names) {
    const ch = await api(cookie, 'GET', `/api/folders/${id}/children`)
    const f = ch.folders.find((x) => x.folder_name === name)
    if (!f) throw new Error(`找不到文件夹 ${names.join('/')}（没有「${name}」）`)
    id = f.id
  }
  return api(cookie, 'GET', `/api/folders/${id}`)
}

/** 把会话 Cookie 放进浏览器上下文（壳模式的地址；页面经模拟的 api_fetch 用同源 fetch 访问 /api）。 */
async function useSession(page, cookie) {
  await useShellSession(page, BASE, cookie)
}

let scannerCtx = null
/** demo_scanner 的会话和目标文件夹（只取一次）。 */
function scannerContext() {
  scannerCtx ??= (async () => {
    const { cookie } = await login(scanner, scannerPassword)
    const projects = await api(cookie, 'GET', '/api/projects')
    const project = projects.find((p) => p.project_code === 'DEMO-A')
    if (!project) throw new Error('demo_scanner 进不了 DEMO-A（先跑 npm run seed）')
    const target = await findFolder(cookie, project.root_folder_id, TARGET_PATH)
    return { cookie, project, target }
  })()
  return scannerCtx
}

// ---------- 模拟的队列数据 ----------

const MB = 1024 * 1024
const NOW = Date.UTC(2026, 9, 5, 2, 30, 0)

function item(id, over) {
  const fileName = over.file_name
  return {
    id,
    source: 'hotfolder',
    server_url: SERVER,
    local_path: `${WATCH}\\${over.rel_dir ? `${over.rel_dir.replaceAll('/', '\\')}\\` : ''}${fileName}`,
    file_name: fileName,
    rel_dir: '',
    target_folder_id: over.target,
    folder_id: null,
    size: Math.round(2.2 * MB),
    mtime_ns: (NOW - 60_000) * 1e6,
    sha256: null,
    status: 'done',
    attempts: 0,
    next_attempt_at: null,
    file_id: null,
    server_file_name: fileName,
    sort_no: null,
    cover_status: 'included',
    cover_reason: null,
    error_code: null,
    error_message: null,
    warnings: [],
    duplicate_of: null,
    created_at: NOW - (200 - id) * 4000,
    updated_at: NOW - (200 - id) * 3000,
    finished_at: null,
    ...over,
  }
}

/** 上传中的队列：两个分组（根目录 → 盒 WS-2025-002，子文件夹"附件（信封）"）在并行上传。 */
function uploadingItems(target) {
  const t = { target }
  const list = [
    item(161, { ...t, file_name: 'IMG_0146.jpg', status: 'waiting', cover_status: null, size: Math.round(2.1 * MB) }),
    item(160, { ...t, file_name: 'IMG_0003.jpg', rel_dir: '附件（信封）', status: 'uploading', cover_status: null, size: Math.round(1.6 * MB) }),
    item(159, { ...t, file_name: 'IMG_0145.jpg', status: 'uploading', cover_status: null, size: Math.round(2.3 * MB) }),
    item(158, { ...t, file_name: 'IMG_0002.jpg', rel_dir: '附件（信封）', sort_no: 2 }),
    item(157, { ...t, file_name: 'IMG_0144.jpg', sort_no: 140, warnings: ['low_dpi'], size: Math.round(1.1 * MB) }),
    item(156, {
      ...t,
      file_name: 'IMG_0143.jpg',
      status: 'duplicate',
      cover_status: null,
      server_file_name: null,
      size: Math.round(2.0 * MB),
      duplicate_of: { file_id: '01a107f5-0000-7000-8000-000000000001', folder_path: '演示项目 A · 文书档案/2025 文书档案/盒 WS-2025-002', file_name: 'IMG_0140.jpg' },
    }),
    item(155, {
      ...t,
      file_name: 'IMG_0142.jpg',
      status: 'failed',
      cover_status: null,
      server_file_name: null,
      size: Math.round(0.1 * MB),
      error_code: 'invalid_image',
      error_message: '不是有效的图片：JPEG 文件头损坏',
    }),
    item(154, {
      ...t,
      file_name: 'IMG_0141.tif',
      sort_no: 139,
      size: Math.round(0.9 * MB),
      cover_status: 'not_generated',
      cover_reason: 'unsupported：调色板 TIFF 不支持生成封面',
    }),
    item(153, { ...t, file_name: 'IMG_0140.jpg', sort_no: 138 }),
    item(152, { ...t, file_name: 'IMG_0001.jpg', rel_dir: '附件（信封）', sort_no: 1 }),
    item(151, { ...t, file_name: 'IMG_0139.jpg', sort_no: 137 }),
    item(150, { ...t, file_name: 'IMG_0138.jpg', sort_no: 136 }),
    item(149, { ...t, file_name: 'IMG_0137.jpg', sort_no: 135 }),
    item(148, { ...t, file_name: 'IMG_0136.jpg', sort_no: 134 }),
    item(147, { ...t, file_name: 'IMG_0135.jpg', sort_no: 133 }),
  ]
  return list
}

const UPLOADING_STATS = { waiting: 1, queued: 37, uploading: 2, done: 140, duplicate: 1, failed: 1, retrying: 0, total: 182 }

/** 竞态：9 已入库、10 上传中、11 排队。 */
function raceItems(target) {
  const t = { target }
  return [
    item(11, { ...t, file_name: 'IMG_0011.jpg', status: 'queued', cover_status: null }),
    item(10, { ...t, file_name: 'IMG_0010.jpg', status: 'uploading', cover_status: null }),
    item(9, { ...t, file_name: 'IMG_0009.jpg', sort_no: 9 }),
  ]
}

/** 积压：300 个排队的文件（id 越大越新）。 */
function backlogItems(target) {
  return Array.from({ length: 300 }, (_, i) =>
    item(i + 1, { target, file_name: `IMG_${i + 1}.jpg`, status: 'queued', cover_status: null, server_file_name: null }),
  )
}

function hotfolderStatus(target, running) {
  return {
    config: { watch_dir: WATCH, target_folder_id: target, server_url: SERVER, scan_interval_secs: 15, running },
    running,
    watching: running,
    error: null,
    last_scan_at: running ? Date.now() - 6000 : null,
    scans: running ? 12 : 0,
  }
}

// ---------- 页面里的模拟（会被序列化，不能引用外部变量） ----------

function scanMock(scenario) {
  const shell = window.__shell
  const state = {
    status: scenario.status,
    hotfolder: scenario.hotfolder,
    items: scenario.items,
    // 页面调过的壳命令（不含 api_fetch）。
    get calls() {
      return shell.calls.filter((c) => c.cmd !== 'api_fetch')
    },
  }

  function emit(event, payload) {
    shell.emit(event, payload)
  }

  function emitState() {
    emit('upload:state', state.status)
  }

  // 模拟"壳读状态的期间队列又变了"（scenario.slow：命令 → {delay, actions}，只生效一次）：命令先取快照，
  // 10 毫秒后执行 actions（改模拟的队列并发事件），过 delay 毫秒才把旧快照返回给页面。
  function act(a) {
    if (a.type === 'item') {
      const it = state.items.find((i) => i.id === a.id)
      Object.assign(it, a.patch, { updated_at: it.updated_at + 5000 })
      emit('upload:item', structuredClone(it))
    } else if (a.type === 'remove') {
      state.items = state.items.filter((i) => i.id !== a.id)
      emit('upload:item-removed', { id: a.id })
    } else if (a.type === 'state') {
      state.status = { ...state.status, ...a.patch, stats: { ...state.status.stats, ...(a.patch.stats ?? {}) } }
      emit('upload:state', structuredClone(state.status))
    } else if (a.type === 'hotfolder') {
      state.hotfolder = { ...state.hotfolder, ...a.patch }
      emit('hotfolder:state', structuredClone(state.hotfolder))
    }
  }

  async function slowly(cmd, snapshot) {
    const slow = scenario.slow?.[cmd]
    if (!slow) return snapshot
    delete scenario.slow[cmd]
    const snap = structuredClone(snapshot)
    setTimeout(() => slow.actions.forEach(act), 10)
    await new Promise((r) => setTimeout(r, slow.delay))
    return snap
  }

  window.__mock = { emit, state }
  const apiFetch = shell.commands.api_fetch
  Object.assign(shell.commands, {
    async api_fetch(args) {
      const req = args.req
      const res = await apiFetch(args)
      // 壳在登录成功后自动解除"需要重新登录"的暂停（client/internal/shell/upload.go 的 observeUploads）。
      if (req.method === 'POST' && req.path === '/api/auth/login' && res.status < 300 && state.status.paused?.reason === 'auth_required') {
        state.status = { ...state.status, paused: null }
        setTimeout(emitState, 0)
      }
      return res
    },
    upload_status: (args) => slowly('upload_status', state.status),
    upload_recent(args) {
      const want = args.status
      const list = state.items
        .filter((i) => !want || want.includes(i.status))
        .sort((a, b) => b.updated_at - a.updated_at)
        .slice(0, args.limit ?? 100)
      return slowly('upload_recent', list)
    },
    upload_retry_failed(args) {
      const ids = args.ids
      let n = 0
      for (const i of state.items) {
        if (i.status !== 'failed' || (ids && !ids.includes(i.id))) continue
        Object.assign(i, { status: 'waiting', error_code: null, error_message: null, updated_at: Date.now() })
        n++
        setTimeout(() => emit('upload:item', { ...i }), 50)
      }
      const s = state.status.stats
      state.status = { ...state.status, stats: { ...s, failed: s.failed - n, waiting: s.waiting + n } }
      setTimeout(emitState, 60)
      return n
    },
    upload_resume() {
      state.status = { ...state.status, paused: null }
      setTimeout(emitState, 0)
      return slowly('upload_resume', state.status)
    },
    hotfolder_status: () => slowly('hotfolder_status', state.hotfolder),
    hotfolder_pick_dir: () => 'D:\\Scan\\Out',
    hotfolder_set(args) {
      state.hotfolder = {
        ...state.hotfolder,
        config: {
          watch_dir: args.watchDir,
          target_folder_id: args.targetFolderId,
          server_url: scenario.serverUrl,
          scan_interval_secs: 15,
          running: state.hotfolder.running,
        },
      }
      setTimeout(() => emit('hotfolder:state', state.hotfolder), 0)
      return state.hotfolder
    },
    hotfolder_start() {
      state.hotfolder = { ...state.hotfolder, running: true, watching: true, last_scan_at: Date.now(), scans: 1, config: { ...state.hotfolder.config, running: true } }
      state.status = { ...state.status, paused_sources: [] }
      setTimeout(() => emit('hotfolder:state', state.hotfolder), 0)
      return state.hotfolder
    },
    hotfolder_pause() {
      state.hotfolder = { ...state.hotfolder, running: false, watching: false, config: { ...state.hotfolder.config, running: false } }
      setTimeout(() => emit('hotfolder:state', state.hotfolder), 0)
      return slowly('hotfolder_pause', state.hotfolder)
    },
  })
}

/** 壳模式：登录 demo_scanner，注入队列的模拟。 */
function shellScenario(build) {
  return async (page) => {
    const ctx = await scannerContext()
    await useSession(page, ctx.cookie)
    await installWailsMock(page, { serverUrl: SERVER }, scanMock, { serverUrl: SERVER, ...build(ctx) })
  }
}

function status(stats, paused = null) {
  return { server_url: SERVER, paused, paused_sources: [], stats, running: stats.uploading > 0 ? 2 : 0 }
}

/** 等页面读完队列。 */
async function waitReady(page) {
  await page.waitForSelector('[data-testid=scan-page][data-state=ready]', { timeout: 15000 })
  await page.waitForSelector('[data-testid=hotfolder-remark]', { timeout: 15000 })
}

async function texts(page, selector) {
  return page.$$eval(selector, (els) => els.map((e) => e.textContent.trim()))
}

function expect(cond, message) {
  if (!cond) throw new Error(message)
}

export default {
  out: '../docs/screenshots/m1',
  accounts: { [scanner]: scannerPassword },
  shots: [
    {
      name: 'scan-shell-uploading',
      path: '/scan',
      shell: true,
      waitUntil: IDLE,
      init: shellScenario(({ target }) => ({
        status: status(UPLOADING_STATS),
        hotfolder: hotfolderStatus(target.id, true),
        items: uploadingItems(target.id),
      })),
      async prepare(page) {
        await waitReady(page)
        // 壳推上传进度（约 250 毫秒一次）：一个 64%，一个 31%。
        await page.evaluate(() => {
          const size = (id) => window.__mock.state.items.find((i) => i.id === id).size
          window.__mock.emit('upload:progress', { id: 159, sent: Math.round(size(159) * 0.64), size: size(159) })
          window.__mock.emit('upload:progress', { id: 160, sent: Math.round(size(160) * 0.31), size: size(160) })
        })
        await page.waitForFunction(
          () => document.querySelector('[data-testid=queue-row][data-id="159"] [data-testid=queue-status]')?.textContent.includes('64%'),
          { timeout: 5000 },
        )
        const rows = await page.$$eval('[data-testid=queue-row]', (els) => els.length)
        expect(rows === 15, `应该有 15 行，实际 ${rows}`)
        const order = await page.$$eval('[data-testid=queue-row]', (els) => els.map((e) => Number(e.dataset.id)))
        expect(order.every((id, i) => i === 0 || order[i - 1] > id), `列表应该按发现的先后倒序：${order}`)
        expect((await texts(page, '[data-testid=badge-low-dpi]')).length === 1, '应该有 1 个"DPI 低于 200"')
        expect((await texts(page, '[data-testid=badge-no-cover]')).length === 1, '应该有 1 个"无封面"')
        const dup = await texts(page, '[data-testid=queue-row][data-status=duplicate] [data-testid=duplicate-of]')
        expect(dup[0]?.includes('盒 WS-2025-002/IMG_0140.jpg'), `重复行要写出已有文件：${dup}`)
        const failed = await texts(page, '[data-testid=queue-row][data-status=failed] [data-testid=queue-status]')
        expect(failed[0]?.includes('JPEG 文件头损坏'), `失败行要写出原因：${failed}`)
        expect(await page.$('[data-testid=queue-row][data-status=failed] [data-testid=queue-retry]'), '失败行要有"重试"')
        const width = await page.$eval('[data-testid=queue-row][data-id="159"] [data-testid=queue-bar]', (el) => el.style.width)
        expect(width === '64%', `进度条应该是 64%，实际 ${width}`)
        const tiles = await texts(page, '[data-testid=queue-stats] [data-testid=tile-count]')
        expect(tiles.join(',') === '1,37,2,140,1,1', `计数不对：${tiles}`)
        // M7-11：头部不再有本机图片缓存的摘要（缓存只在设置页看）。
        expect(!(await page.$('[data-testid=image-cache-summary]')), '扫描上传页头部不该再有缓存摘要')
        const remark = await texts(page, '[data-testid=hotfolder-remark]')
        expect(remark[0]?.includes('3 号库 A 架 12 层'), `要显示目标文件夹的备注：${remark}`)
        const headline = await texts(page, '[data-testid=hotfolder-headline]')
        expect(headline[0]?.includes(TARGET_PATH.join('/')), `标题要写出目标：${headline}`)
      },
      // 截图之后：点失败行的"重试"，页面调 upload_retry_failed，这一行变成"等待写入完成"，失败数变成 0。
      async after(page) {
        await page.click('[data-testid=queue-row][data-status=failed] [data-testid=queue-retry]')
        await page.waitForSelector('[data-testid=queue-row][data-id="155"][data-status=waiting]', { timeout: 5000 })
        const calls = await page.evaluate(() => window.__mock.state.calls.filter((c) => c.cmd === 'upload_retry_failed'))
        expect(calls.length === 1 && JSON.stringify(calls[0].args.ids) === '[155]', `重试的参数不对：${JSON.stringify(calls)}`)
        await page.waitForFunction(
          () => document.querySelector('[data-testid=tile-failed] [data-testid=tile-count]')?.textContent.trim() === '0',
          { timeout: 5000 },
        )
        // 点"失败"格子：只看失败项（现在没有）。
        await page.click('[data-testid=tile-failed]')
        await page.waitForSelector('[data-testid=queue-empty]', { timeout: 5000 })
        const recent = await page.evaluate(() => window.__mock.state.calls.filter((c) => c.cmd === 'upload_recent').map((c) => c.args.status))
        expect(JSON.stringify(recent.at(-1)) === '["failed"]', `筛选要按状态取：${JSON.stringify(recent)}`)
      },
      delay: 500,
    },
    {
      name: 'scan-shell-race',
      path: '/scan',
      shell: true,
      waitUntil: IDLE,
      init: shellScenario(({ target }) => ({
        status: status(
          { waiting: 0, queued: 1, uploading: 1, done: 1, duplicate: 0, failed: 0, retrying: 0, total: 3 },
          {
            reason: 'upgrade_required',
            message: '客户端版本过低，请更新',
            details: { client_version: '0.1.0', min_version: '0.2.0', latest_version: '0.2.0', download_url: null },
            since: Date.now(),
          },
        ),
        hotfolder: hotfolderStatus(target.id, true),
        items: raceItems(target.id),
        slow: {
          // 读最近文件的期间：10 传完、11 被删（热文件夹里的文件上传前不见了）。
          upload_recent: {
            delay: 150,
            actions: [
              { type: 'item', id: 10, patch: { status: 'done', sort_no: 10, cover_status: 'included', server_file_name: 'IMG_0010.jpg' } },
              { type: 'remove', id: 11 },
            ],
          },
          // 读整体状态的期间：计数变了。
          upload_status: {
            delay: 150,
            actions: [{ type: 'state', patch: { running: 0, stats: { queued: 0, uploading: 0, done: 2, total: 2 } } }],
          },
          // 读热文件夹状态的期间：出了问题。
          hotfolder_status: { delay: 150, actions: [{ type: 'hotfolder', patch: { error: '监控目录读不了（测试）' } }] },
          // "再试一次"的期间：又传完一个。
          upload_resume: { delay: 150, actions: [{ type: 'state', patch: { stats: { done: 3, total: 3 } } }] },
          // "暂停"的期间：热文件夹又报了问题。
          hotfolder_pause: { delay: 150, actions: [{ type: 'hotfolder', patch: { error: '暂停后监控目录被删除（测试）' } }] },
        },
      })),
      async prepare(page) {
        await waitReady(page)
        await new Promise((r) => setTimeout(r, 400))
        const rows = await page.$$eval('[data-testid=queue-row]', (els) => els.map((e) => `${e.dataset.id}:${e.dataset.status}`))
        expect(rows.join(',') === '10:done,9:done', `读的期间收到的事件被旧快照盖掉了：${rows}`)
        const tiles = await texts(page, '[data-testid=queue-stats] [data-testid=tile-count]')
        expect(tiles.join(',') === '0,0,0,2,0,0', `计数应该是事件里的：${tiles}`)
        const err = await texts(page, '[data-testid=hotfolder-error]')
        expect(err[0]?.includes('监控目录读不了'), `热文件夹的出错提示应该是事件里的：${err}`)
        // "再试一次"：命令的返回值（已入库 2）不能盖掉期间收到的事件（已入库 3）。
        await page.click('[data-testid=paused-upgrade] button:last-of-type')
        await page.waitForFunction(() => !document.querySelector('[data-testid=paused-upgrade]'), { timeout: 5000 })
        await new Promise((r) => setTimeout(r, 400))
        const done = await texts(page, '[data-testid=tile-done] [data-testid=tile-count]')
        expect(done[0] === '3', `"再试一次"的返回值盖掉了事件：已入库 ${done}`)
        // "暂停"：命令的返回值（没有出错）不能盖掉期间收到的事件（出错）。
        await page.click('[data-testid=hotfolder-pause]')
        await page.waitForSelector('[data-testid=hotfolder-card][data-running=false]', { timeout: 5000 })
        await new Promise((r) => setTimeout(r, 400))
        const err2 = await texts(page, '[data-testid=hotfolder-error]')
        expect(err2[0]?.includes('暂停后监控目录被删除'), `"暂停"的返回值盖掉了事件：${err2}`)
      },
      delay: 300,
    },
    {
      name: 'scan-shell-backlog',
      path: '/scan',
      shell: true,
      waitUntil: IDLE,
      init: shellScenario(({ target }) => ({
        status: status({ waiting: 0, queued: 300, uploading: 0, done: 0, duplicate: 0, failed: 0, retrying: 0, total: 300 }),
        hotfolder: hotfolderStatus(target.id, true),
        items: backlogItems(target.id),
      })),
      async prepare(page) {
        await waitReady(page)
        const before = await page.$$eval('[data-testid=queue-row]', (els) => els.length)
        expect(before === 200, `积压 300 个时列表应该留 200 行，实际 ${before}`)
        // 最早的 1 开始上传（带进度），2 失败。
        await page.evaluate(() => {
          const m = window.__mock
          const up = m.state.items.find((i) => i.id === 1)
          Object.assign(up, { status: 'uploading', updated_at: Date.now() })
          m.emit('upload:item', structuredClone(up))
          m.emit('upload:progress', { id: 1, sent: Math.round(up.size / 2), size: up.size })
          const bad = m.state.items.find((i) => i.id === 2)
          Object.assign(bad, { status: 'failed', updated_at: Date.now(), error_code: 'invalid_image', error_message: '不是有效的图片（测试）' })
          m.emit('upload:item', structuredClone(bad))
        })
        await page.waitForSelector('[data-testid=queue-row][data-id="1"][data-status=uploading]', { timeout: 5000 })
        await page.waitForSelector('[data-testid=queue-row][data-id="2"][data-status=failed]', { timeout: 5000 })
        const after = await page.$$eval('[data-testid=queue-row]', (els) => els.map((e) => Number(e.dataset.id)))
        expect(after.length === 202, `上传中、失败的两行另外留着，应该 202 行，实际 ${after.length}`)
        expect(after.includes(300) && after.includes(101) && !after.includes(100), `其余的应该是最近的 200 个：${after.slice(0, 3)}…${after.slice(-4)}`)
        const width = await page.$eval('[data-testid=queue-row][data-id="1"] [data-testid=queue-bar]', (el) => el.style.width)
        expect(width === '50%', `上传中那行的进度应该是 50%，实际 ${width}`)
        await page.$eval('[data-testid=queue-row][data-id="2"]', (el) => el.scrollIntoView({ block: 'center' }))
      },
      // 截图之后：到文件目录页，壳发 tray:error（托盘菜单"继续热文件夹"失败），全局弹出提示，点"去扫描上传"回到 /scan。
      async after(page) {
        await page.click('[data-testid=side-nav] [data-menu=files]')
        await page.waitForFunction(() => location.pathname === '/files', { timeout: 5000 })
        await page.evaluate(() => window.__mock.emit('tray:error', { message: '热文件夹没能继续：监控目录不存在（测试）' }))
        const action = await page.waitForSelector('[data-sonner-toast] [data-button]', { timeout: 5000 })
        const toast = await texts(page, '[data-sonner-toast]')
        expect(toast.some((t) => t.includes('监控目录不存在')), `应该弹出托盘的出错提示：${toast}`)
        expect((await action.evaluate((el) => el.textContent.trim())) === '去扫描上传', '提示上应该有"去扫描上传"')
        // 提示从顶上滑进来，等它停在视口里再点（滑到一半时按钮还在视口外，点不到）。
        await page.waitForFunction(() => (document.querySelector('[data-sonner-toast] [data-button]')?.getBoundingClientRect().top ?? -1) >= 0, { timeout: 5000 })
        await action.click()
        await page.waitForSelector('[data-testid=scan-page][data-state=ready]', { timeout: 5000 })
      },
      delay: 300,
    },
    {
      name: 'scan-shell-auth-paused',
      path: '/scan',
      shell: true,
      waitUntil: IDLE,
      init: shellScenario(({ target }) => ({
        status: status(
          { waiting: 0, queued: 58, uploading: 0, done: 84, duplicate: 0, failed: 0, retrying: 0, total: 142 },
          { reason: 'auth_required', message: '登录已失效（HTTP 401）', details: null, since: Date.now() - 30_000 },
        ),
        hotfolder: hotfolderStatus(target.id, true),
        items: uploadingItems(target.id)
          .filter((i) => i.status === 'done' || i.status === 'waiting')
          .map((i) => (i.status === 'waiting' ? { ...i, status: 'queued' } : i)),
      })),
      async prepare(page) {
        await waitReady(page)
        await page.waitForSelector('[data-testid=paused-auth]')
        const text = await texts(page, '[data-testid=paused-auth]')
        expect(text[0].includes('重新登录'), `暂停提示不对：${text}`)
      },
      delay: 500,
    },
    {
      name: 'scan-shell-auth-resumed',
      path: '/scan',
      shell: true,
      waitUntil: IDLE,
      init: shellScenario(({ target }) => ({
        status: status(
          { waiting: 0, queued: 58, uploading: 0, done: 84, duplicate: 0, failed: 0, retrying: 0, total: 142 },
          { reason: 'auth_required', message: '登录已失效（HTTP 401）', details: null, since: Date.now() - 30_000 },
        ),
        hotfolder: hotfolderStatus(target.id, true),
        items: uploadingItems(target.id).filter((i) => i.status === 'done'),
      })),
      async prepare(page) {
        await waitReady(page)
        await page.click('[data-testid=paused-relogin]')
        await page.waitForSelector('#username')
        const url = new URL(page.url())
        expect(url.pathname === '/login' && url.searchParams.get('redirect') === '/scan', `应该去登录页并带回跳地址：${page.url()}`)
        await page.type('#username', scanner)
        await page.type('#password', scannerPassword)
        await page.click('[data-testid=login-submit]')
        await page.waitForFunction(() => location.pathname === '/scan', { timeout: 10000 })
        await waitReady(page)
        await page.waitForFunction(() => !document.querySelector('[data-testid=paused-auth]'), { timeout: 5000 })
      },
      delay: 500,
    },
    {
      name: 'scan-shell-upgrade-paused',
      path: '/scan',
      shell: true,
      waitUntil: IDLE,
      init: shellScenario(({ target }) => ({
        status: status(
          { waiting: 0, queued: 12, uploading: 0, done: 140, duplicate: 1, failed: 0, retrying: 0, total: 153 },
          {
            reason: 'upgrade_required',
            message: '客户端版本过低，请更新',
            details: {
              client_version: '0.1.0',
              min_version: '0.2.0',
              latest_version: '0.2.1',
              download_url: 'http://127.0.0.1:9080/downloads/Doclite_0.2.1_x64-setup.exe',
            },
            since: Date.now() - 5000,
          },
        ),
        hotfolder: hotfolderStatus(target.id, true),
        items: uploadingItems(target.id).filter((i) => i.status === 'done' || i.status === 'duplicate'),
      })),
      async prepare(page) {
        await waitReady(page)
        await page.waitForSelector('[data-testid=paused-upgrade]')
        const text = await texts(page, '[data-testid=paused-upgrade]')
        expect(text[0].includes('0.2.0') && text[0].includes('0.1.0'), `升级提示要写出版本：${text}`)
      },
      delay: 500,
    },
    {
      // 还没设置热文件夹（admin，在 DEMO-B 里新建一个临时文件夹，截图后删掉）。
      name: 'scan-shell-new-folder',
      path: '/scan',
      shell: true,
      waitUntil: IDLE,
      async init(page) {
        const { cookie, me } = await login(admin, adminPassword)
        const projects = await api(cookie, 'GET', '/api/projects')
        const project = projects.find((p) => p.project_code === NEW_FOLDER_PROJECT)
        if (!project) throw new Error(`找不到项目 ${NEW_FOLDER_PROJECT}（先跑 npm run seed）`)
        newFolderCtx = { cookie, project, name: `M1-13 截图 ${Date.now()}` }
        await useSession(page, cookie)
        // 项目切换器选中 DEMO-B（stores/project.ts 记在 localStorage 的上次所选项目）。
        await page.evaluateOnNewDocument(
          (key, id) => {
            try {
              localStorage.setItem(key, id)
            } catch {
              // 截图的上下文里一般都能写。
            }
          },
          `doclite.lastProject.${me.id}`,
          project.id,
        )
        await installWailsMock(page, { serverUrl: SERVER }, scanMock, {
          serverUrl: SERVER,
          status: status({ waiting: 0, queued: 0, uploading: 0, done: 0, duplicate: 0, failed: 0, retrying: 0, total: 0 }),
          hotfolder: { config: null, running: false, watching: false, error: null, last_scan_at: null, scans: 0 },
          items: [],
        })
      },
      async prepare(page) {
        await page.waitForSelector('[data-testid=scan-page][data-state=ready]')
        await page.waitForSelector('[data-testid=hotfolder-card][data-configured=false]')
        await page.click('[data-testid=hotfolder-pick-dir]')
        await page.waitForFunction(() => document.querySelector('[data-testid=hotfolder-dir]')?.textContent.includes('D:\\Scan\\Out'))
        await page.click('[data-testid=hotfolder-pick-target]')
        await page.waitForSelector('[data-testid=folder-picker] [data-testid=picker-new-folder]')
        await page.click('[data-testid=picker-new-folder]')
        await page.waitForSelector('[data-testid=new-folder-dialog]')
        await page.type('[data-testid=new-folder-name]', newFolderCtx.name)
        await page.type('[data-testid=new-folder-remark]', '3 号库 B 架 2 层')
        await page.click('[data-testid=new-folder-submit]')
        await page.waitForFunction(() => !document.querySelector('[data-testid=new-folder-dialog]'), { timeout: 5000 })
        // 新建的文件夹被选中，选择框底部显示它的路径和备注。
        await page.waitForFunction(
          (name) => {
            const t = document.querySelector('[data-testid=folder-picker-selected]')?.textContent ?? ''
            return t.includes(name) && t.includes('3 号库 B 架 2 层')
          },
          { timeout: 5000 },
          newFolderCtx.name,
        )
        newFolderCtx.id = await page.$eval('[data-testid=folder-picker] [data-testid=tree-folder][data-selected-folder]', (el) => el.dataset.folderId)
        expect(/^[0-9a-f-]{36}$/.test(newFolderCtx.id ?? ''), `新建的文件夹应该被选中：${newFolderCtx.id}`)
      },
      // 截图之后：确认 → 换项目清掉没保存的目标 → 换回来重新选、开始监听 → 再换项目，已保存的目标显示它自己的项目。
      // 最后删掉临时文件夹。
      async after(page) {
        const targetText = () => page.$eval('[data-testid=hotfolder-target]', (el) => el.textContent.trim())
        const switchTo = async (code) => {
          await page.click('[data-testid=project-switcher-trigger]')
          await page.click(`[data-testid=project-option][data-code="${code}"]`)
          await page.waitForFunction(
            (c) => document.querySelector('[data-testid=project-switcher-trigger]')?.textContent.includes(c),
            { timeout: 5000 },
            code,
          )
          await new Promise((r) => setTimeout(r, 300))
        }
        const waitPickerGone = () =>
          page.waitForFunction(
            () => !document.querySelector('[data-testid=folder-picker]') && getComputedStyle(document.body).pointerEvents !== 'none',
            { timeout: 5000 },
          )
        try {
          const projectName = newFolderCtx.project.project_name
          await page.click('[data-testid=folder-picker-confirm]')
          await page.waitForFunction(
            (name) => document.querySelector('[data-testid=hotfolder-target]')?.textContent.includes(name),
            { timeout: 5000 },
            newFolderCtx.name,
          )
          const pending = await targetText()
          expect(pending.startsWith(`${projectName} · `), `没保存的目标要显示选的时候所在的项目：${pending}`)
          const remark = await texts(page, '[data-testid=hotfolder-remark]')
          expect(remark[0]?.includes('3 号库 B 架 2 层'), `卡片要显示新文件夹的备注：${remark}`)
          await waitPickerGone()

          // 换到 DEMO-A：还没保存的目标属于 DEMO-B，清掉。
          await switchTo('DEMO-A')
          const cleared = await targetText()
          expect(!cleared.includes(newFolderCtx.name) && cleared.includes('还没选'), `换项目后没保存的目标应该清掉：${cleared}`)

          // 换回来，重新选这个文件夹，开始监听。
          await switchTo(NEW_FOLDER_PROJECT)
          await page.click('[data-testid=hotfolder-pick-target]')
          const row = await page.waitForSelector(`[data-testid=folder-picker] [data-testid=tree-folder][data-folder-id="${newFolderCtx.id}"]`, { timeout: 5000 })
          await row.click()
          await page.click('[data-testid=folder-picker-confirm]')
          await waitPickerGone()
          await page.click('[data-testid=hotfolder-start]')
          await page.waitForSelector('[data-testid=hotfolder-card][data-running=true]', { timeout: 5000 })
          const calls = await page.evaluate(() => window.__mock.state.calls.map((c) => c.cmd))
          const set = calls.indexOf('hotfolder_set')
          expect(set >= 0 && calls.indexOf('hotfolder_start') > set, `应该先保存再开始：${calls}`)
          const args = await page.evaluate(() => window.__mock.state.calls.find((c) => c.cmd === 'hotfolder_set').args)
          expect(args.watchDir === 'D:\\Scan\\Out', `监控目录不对：${JSON.stringify(args)}`)
          expect(args.targetFolderId === newFolderCtx.id, `目标文件夹 id 不对：${JSON.stringify(args)}`)

          // 已保存的目标：换到 DEMO-A 后仍显示它自己的项目（GET /api/folders/{id} 的根文件夹名）。
          await switchTo('DEMO-A')
          await page.waitForFunction(
            (name) => document.querySelector('[data-testid=hotfolder-target]')?.textContent.includes(name),
            { timeout: 5000 },
            newFolderCtx.name,
          )
          const saved = await targetText()
          expect(saved.startsWith(`${projectName} · `), `已保存的目标要显示它自己的项目：${saved}`)
        } finally {
          const ch = await api(newFolderCtx.cookie, 'GET', `/api/folders/${newFolderCtx.project.root_folder_id}/children`)
          const f = ch.folders.find((x) => x.folder_name === newFolderCtx.name)
          if (f) await api(newFolderCtx.cookie, 'DELETE', `/api/folders/${f.id}`)
        }
      },
      delay: 500,
    },
    {
      name: 'scan-browser',
      user: scanner,
      path: '/scan',
      // M2-7 起 /scan 右侧有待重扫列表，常开一条 SSE 连接。
      waitUntil: IDLE,
      waitFor: '[data-testid=scan-browser-notice]',
      async prepare(page) {
        expect(!(await page.$('[data-testid=hotfolder-card]')), '浏览器里不应该有热文件夹')
        const text = await page.$eval('[data-testid=scan-browser-notice]', (el) => el.textContent ?? '')
        expect(text.includes('请在客户端中使用'), `浏览器里只提示"请在客户端中使用"：${text}`)
      },
    },
  ],
}

let newFolderCtx = null
