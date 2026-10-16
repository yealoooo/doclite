// 壳里的整页提示和网页里的壳入口：npm run screenshots -- --spec scripts/shots/m1-shell.mjs --base <devshell 的地址>
//
// M7-6 起连接服务器、连不上、需要升级是客户端自带的页面（client/internal/shellpages，devshell 的 /shell/ 给的是同一份），
// 界面只负责跳过去。升级页只信探测（check_server）的结果：探测说不用升级时不显示，下载地址也只用探测到的。页面以壳模式的地址打开（shell: true），在页面脚本运行前注入 Wails 桥的模拟
// （scripts/lib/wails-mock.mjs），客户端自带的页面和网页用的是同一个模拟：
// - shell_info / check_server / probe_server 按场景返回（没有服务器地址、连不上、版本够、版本过低）；
// - api_fetch 默认转给 devshell 的 /api（真实 server，要先启动），按场景拦截某些请求（例如 /api/me 返回 426）。
// 设置页（M7-11，侧栏用户区的"设置"）：服务器（地址、连接状态和响应时间、服务端版本、修改地址）、账号、本机存储
// （数据目录、本机图片缓存、上传队列、日志目录，打开目录只给名字）、热文件夹、关于（复制诊断信息，不带密码和会话）；
// 缓存的清空确认。侧栏收起（M7-11）：业务界面、系统管理各一张（只有图标、悬停提示名字、当前菜单高亮、项目编码、
// 头像点开是用户菜单；⌘B / Ctrl+B 切换，输入框里不触发，刷新后保持）。
// 最后一张模拟比网页旧的客户端（shell_info 的能力只有 api、server、external）：网页里相应的本机功能隐藏、提示升级，
// 设置页里没有的分组隐藏。
// 管理员账号同 m0.mjs（INIT_ADMIN_USERNAME / INIT_ADMIN_PASSWORD，默认 admin / change-me-now）；扫描员 demo_scanner。

import { installWailsMock } from '../lib/wails-mock.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const scanner = 'demo_scanner'
const scannerPassword = process.env.DEMO_PASSWORD ?? 'demo123456'

const SERVER = 'http://192.168.1.10:9080'
const DATA_DIR = 'C:\\Users\\scan01\\AppData\\Local\\Doclite'
const UPGRADE = {
  client_version: '0.2.0',
  min_version: '0.3.0',
  latest_version: '0.3.1',
  download_url: 'http://192.168.1.10:9080/downloads/Doclite_0.3.1_x64-setup.exe',
}

/** 在页面里执行（会被序列化，不能引用外部变量）：按场景覆盖通用模拟的几个命令。 */
function scenarioMock(scenario) {
  const shell = window.__shell
  const probe = shell.commands.probe_server
  const unreachable = (url) => ({ ...probe({ url }), reachable: false, server_version: null, error: '无法连接服务器' })
  shell.commands.check_server = () => {
    if (!shell.serverUrl) throw { kind: 'not_configured', message: '还没有设置服务器地址' }
    if (!scenario.checkReachable) return unreachable(shell.serverUrl)
    const p = probe({ url: shell.serverUrl })
    return scenario.upgrade ? { ...p, ...scenario.upgrade, upgrade_required: true } : p
  }
  if (scenario.probeFails) {
    shell.commands.probe_server = async (args) => {
      // 慢一点，看得到"正在连接…"。
      await new Promise((r) => setTimeout(r, 300))
      return unreachable(String(args.url).includes('://') ? args.url : `http://${args.url}`)
    }
  }
  const apiFetch = shell.commands.api_fetch
  shell.commands.api_fetch = async (args) => {
    if (scenario.upgrade && scenario.upgradeOn === args.req.path) {
      return {
        status: 426,
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ code: 'client_upgrade_required', message: '客户端版本过低，请更新', details: scenario.upgrade }),
      }
    }
    // 其余请求转给 devshell 的 /api（真实 server）。
    return apiFetch(args)
  }
}

const mock = (scenario, options = {}) => (page) =>
  installWailsMock(page, { serverUrl: scenario.serverUrl, ...options }, scenarioMock, scenario)

/**
 * 设置页用的模拟（在页面里执行，会被序列化）：上传队列有几个失败的、热文件夹在运行（目标是 DEMO-A 的根文件夹，
 * 取自真实的 /api/projects）、本机图片缓存有两个项目。
 */
function settingsMock() {
  const MB = 1024 * 1024
  const shell = window.__shell
  const stats = { waiting: 1, queued: 2, uploading: 1, done: 140, duplicate: 1, failed: 2, retrying: 0, total: 147 }
  let projects = null
  const loadProjects = async () => {
    if (!projects) {
      try {
        const res = await fetch('/api/projects')
        projects = res.ok ? await res.json() : []
      } catch {
        projects = []
      }
    }
    return projects
  }
  let cache = null
  const buildCache = async () => {
    const list = await loadProjects()
    const a = list.find((p) => p.project_code === 'DEMO-A') ?? list[0]
    const b = list.find((p) => p.project_code === 'DEMO-B') ?? list[1]
    const rows = []
    if (a) rows.push({ project_id: a.id, total_bytes: Math.round(1180.4 * MB), file_count: 13900, cover_count: 13850, original_count: 50, tiles_count: 3 })
    if (b && b !== a) rows.push({ project_id: b.id, total_bytes: Math.round(150.8 * MB), file_count: 1330, cover_count: 1320, original_count: 10, tiles_count: 0 })
    return { projects: rows, limit_bytes: 2 * 1024 * MB }
  }
  const withTotals = (c) => ({
    dir: 'C:\\Users\\scan01\\AppData\\Local\\Doclite\\image-cache',
    available: true,
    error: null,
    limit_bytes: c.limit_bytes,
    projects: c.projects,
    total_bytes: c.projects.reduce((n, p) => n + p.total_bytes, 0),
    file_count: c.projects.reduce((n, p) => n + p.file_count, 0),
    counters: { hits: 0, misses: 0, downloads: 0, download_bytes: 0, rejected: 0, errors: 0, tilings: 0, tiling_failures: 0 },
  })
  Object.assign(shell.commands, {
    upload_status: () => ({ server_url: shell.serverUrl, paused: null, paused_sources: [], stats: { ...stats }, running: 1 }),
    upload_retry_failed: () => {
      const n = stats.failed
      stats.queued += n
      stats.failed = 0
      setTimeout(() => shell.emit('upload:state', { server_url: shell.serverUrl, paused: null, paused_sources: [], stats: { ...stats }, running: 1 }), 0)
      return n
    },
    async hotfolder_status() {
      const list = await loadProjects()
      const a = list.find((p) => p.project_code === 'DEMO-A') ?? list[0]
      return {
        config: { watch_dir: 'D:\\扫描\\输出', target_folder_id: a?.root_folder_id ?? '0199b6f2-1c3e-7a10-9f00-5a1e2c3d4e5f', server_url: shell.serverUrl, scan_interval_secs: 15, running: true },
        running: true,
        watching: true,
        error: null,
        last_scan_at: Date.now() - 42_000,
        scans: 12,
      }
    },
    async cache_stats() {
      cache ??= await buildCache()
      return withTotals(cache)
    },
    async cache_clear(args) {
      cache ??= await buildCache()
      const removed = cache.projects.filter((p) => args.projectId == null || p.project_id === args.projectId)
      cache.projects = cache.projects.filter((p) => !removed.includes(p))
      return { removed_files: removed.reduce((n, p) => n + p.file_count, 0), removed_bytes: removed.reduce((n, p) => n + p.total_bytes, 0), failed_files: 0 }
    },
    async cache_set_limit(args) {
      cache ??= await buildCache()
      cache.limit_bytes = args.limitBytes
      return withTotals(cache)
    },
  })
}

const settingsInit = (options = {}) => async (page) => {
  await mock({ serverUrl: SERVER, checkReachable: true }, options)(page)
  await page.evaluateOnNewDocument(settingsMock)
}

/** 登录后从侧栏用户区进设置页，等各组读出来。 */
async function openSettings(page, user = admin, password = adminPassword) {
  await login(page, user, password)
  await page.click('[data-testid=user-area] [data-testid=open-settings]')
  await page.waitForSelector('[data-testid=settings-page][data-state=ready]', { timeout: 15000 })
}

/** 壳发事件（托盘菜单的"设置…"），直到页面有反应（页面订阅事件是异步的，刚打开时可能还没订上）。 */
async function emitUntil(page, name, done) {
  for (let i = 0; i < 50; i++) {
    await page.evaluate((n) => window.__shell.emit(n, null), name)
    await new Promise((r) => setTimeout(r, 100))
    if (await page.evaluate(done)) return
  }
  throw new Error(`发了 ${name}，页面没有反应：${page.url()}`)
}

/** 截下"复制诊断信息"复制的文字（剪贴板和 execCommand 的兜底都换掉）。 */
async function captureCopies(page) {
  await page.evaluate(() => {
    window.__copied = []
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (t) => void window.__copied.push(t) } })
    document.execCommand = (cmd) => {
      if (cmd === 'copy') window.__copied.push(document.activeElement?.value ?? '')
      return true
    }
  })
}

/** 当前的修饰键（侧栏的快捷键：macOS ⌘B，其他 Ctrl+B）。 */
async function pressSidebarShortcut(page) {
  const mac = await page.evaluate(() => /Mac|iPhone|iPad/i.test(navigator.platform || navigator.userAgent))
  const mod = mac ? 'Meta' : 'Control'
  await page.keyboard.down(mod)
  await page.keyboard.press('KeyB')
  await page.keyboard.up(mod)
}

const sidebarState = (page) => page.$eval('[data-testid=side-nav]', (el) => el.dataset.collapsed)

async function expectCollapsedLayout(page) {
  const width = await page.$eval('[data-testid=side-nav]', (el) => el.getBoundingClientRect().width)
  if (width > 60) throw new Error(`收起的侧栏应该约 56px，实际 ${width}px`)
  const texts = await page.$$eval('[data-testid=side-nav] nav a', (els) => els.map((e) => e.textContent.trim()).filter(Boolean))
  if (texts.length > 0) throw new Error(`收起时菜单不该显示名字：${texts.join('、')}`)
  const icons = await page.$$eval('[data-testid=side-nav] nav a', (els) => els.filter((e) => e.querySelector('svg')).length)
  const links = await page.$$eval('[data-testid=side-nav] nav a', (els) => els.length)
  if (links === 0 || icons !== links) throw new Error(`每个菜单都要有图标：${icons}/${links}`)
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
  if (overflow > 0) throw new Error(`出现了横向滚动（多出 ${overflow}px）`)
}

/** 客户端自带的页面里，当前显示的是哪一块。 */
const view = (name) => `#view-${name}:not([hidden])`

async function expectText(page, selector, wants) {
  const text = await page.$eval(selector, (el) => el.textContent)
  for (const want of wants) {
    if (!text.includes(want)) throw new Error(`${selector} 里没有「${want}」：${text}`)
  }
}

async function login(page, user, password) {
  await page.waitForSelector('#username')
  await page.type('#username', user)
  await page.type('#password', password)
  await page.click('[data-testid=login-submit]')
  await page.waitForSelector('[data-testid=side-nav]')
}

export default {
  out: '../docs/screenshots/m1',
  accounts: {},
  waitUntil: 'networkidle2',
  shots: [
    // 第一次启动：还没有服务器地址，客户端自带的连接页（不需要服务器）。
    {
      name: 'shell-connect',
      path: '/shell/',
      shell: true,
      init: mock({ serverUrl: null }),
      waitFor: view('connect'),
      async prepare(page) {
        await expectText(page, '#connect-title', ['连接服务器'])
        await expectText(page, '[data-testid=shell-version]', ['0.2.0'])
        await page.type('#server-address', '192.168.1.10:9080')
      },
    },
    // 连接页：探测失败的提示，地址不保存。
    {
      name: 'shell-connect-failed',
      path: '/shell/',
      shell: true,
      init: mock({ serverUrl: null, probeFails: true }),
      waitFor: view('connect'),
      async prepare(page) {
        await page.type('#server-address', '192.168.1.10:9080')
        await page.click('[data-testid=connect-submit]')
        await page.waitForSelector('[data-testid=connect-error]:not([hidden])')
        await expectText(page, '[data-testid=connect-error]', ['连不上 http://192.168.1.10:9080'])
        const saved = await page.evaluate(() => window.__shell.calls.some((c) => c.cmd === 'set_server'))
        if (saved) throw new Error('连不上的地址也保存了')
      },
    },
    // 连接页：连得上就保存、回到关口，关口探测通过后进网页（登录页）。
    {
      name: 'shell-connect-ok',
      path: '/shell/',
      shell: true,
      init: mock({ serverUrl: null, checkReachable: true }),
      waitFor: view('connect'),
      async prepare(page) {
        await page.type('#server-address', SERVER)
        await Promise.all([page.waitForNavigation({ waitUntil: 'networkidle2' }), page.click('[data-testid=connect-submit]')])
        await page.waitForSelector('[data-testid=login-server]', { timeout: 15000 })
        // 每次换页面模拟会重装（新的 window），这里只核对落在了网页的登录页上。
        if (!new URL(page.url()).pathname.startsWith('/login')) throw new Error(`没有进到登录页：${page.url()}`)
      },
    },
    // 启动时连不上已配置的服务器：重试、修改地址。
    {
      name: 'shell-unreachable',
      path: '/shell/',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: false }),
      waitFor: view('unreachable'),
      async prepare(page) {
        await expectText(page, '#unreachable-server', [SERVER])
        await expectText(page, '#unreachable-reason', ['无法连接服务器'])
        await page.click('[data-testid=retry]')
        await page.waitForSelector(view('unreachable'))
      },
    },
    // 界面里的请求返回 426：跳到客户端自带的升级页（探测确认要升级，显示探测到的版本和下载地址）。
    {
      name: 'shell-upgrade',
      path: '/',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true, upgrade: UPGRADE, upgradeOn: '/api/me' }),
      waitFor: view('upgrade'),
      async prepare(page) {
        if (!page.url().includes('/shell/?upgrade')) throw new Error(`没有跳到升级页：${page.url()}`)
        await expectText(page, '[data-testid=upgrade-min]', ['0.3.0'])
        await expectText(page, '#view-upgrade', ['0.2.0', '0.3.0', '0.3.1', UPGRADE.download_url])
        await page.click('[data-testid=upgrade-download]')
        const opened = await page.evaluate(() => window.__shell.externals)
        if (!opened.includes('http://192.168.1.10:9080/downloads/Doclite_0.3.1_x64-setup.exe')) {
          throw new Error(`下载地址没有交给系统浏览器：${JSON.stringify(opened)}`)
        }
      },
    },
    // 同源的链接把人带到 /shell/?upgrade 并在查询串里塞下载地址：探测说不用升级，就不显示升级页，回到界面；
    // 查询串里的下载地址不用。
    {
      name: 'shell-upgrade-spoofed',
      path: '/shell/?upgrade&min_version=9.9.9&download_url=http%3A%2F%2Fevil.example%2Fsetup.exe',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true }),
      waitFor: '#username',
      async prepare(page) {
        if (new URL(page.url()).pathname.startsWith('/shell/')) throw new Error(`停在了客户端的页面：${page.url()}`)
        const opened = await page.evaluate(() => window.__shell.externals)
        if (opened.length > 0) throw new Error(`不该打开外部地址：${JSON.stringify(opened)}`)
      },
    },
    // 壳里的登录页：显示服务器地址，"修改"去客户端自带的修改地址页（带上原地址，能取消）。
    {
      name: 'shell-login',
      path: '/login',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true }),
      waitFor: '[data-testid=login-server]',
      async prepare(page) {
        await expectText(page, '[data-testid=login-server]', [SERVER, '修改'])
      },
    },
    {
      name: 'shell-edit-server',
      path: '/login',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true }),
      waitFor: '[data-testid=login-server] button',
      async prepare(page) {
        await Promise.all([page.waitForNavigation(), page.click('[data-testid=login-server] button')])
        await page.waitForSelector(view('connect'))
        await expectText(page, '#connect-title', ['修改服务器地址'])
        const value = await page.$eval('#server-address', (el) => el.value)
        if (value !== SERVER) throw new Error(`修改地址页没有带上原地址：${value}`)
        if (await page.$eval('#connect-cancel', (el) => el.hidden)) throw new Error('修改地址页没有"取消"')
      },
    },
    // 设置页（M7-11）：整页。各组按能力显示，连接状态是真实的 /api/health，打开目录只给名字，诊断信息不带密码和会话。
    // 从托盘菜单的"设置…"进来（壳发 tray:open-settings）：还没登录时先到登录页，登录后到设置页。
    {
      name: 'shell-settings',
      path: '/login',
      shell: true,
      init: settingsInit(),
      waitFor: '#username',
      async prepare(page) {
        await page.setViewport({ width: 1440, height: 1900 })
        await emitUntil(page, 'tray:open-settings', () => new URLSearchParams(location.search).get('redirect') === '/settings')
        await login(page, admin, adminPassword)
        await page.waitForSelector('[data-testid=settings-page][data-state=ready]', { timeout: 15000 })
        if (new URL(page.url()).pathname !== '/settings') throw new Error(`没有到设置页：${page.url()}`)
        // 已登录时在别的页面收到 tray:open-settings 也跳到设置页。
        await page.click('[data-testid=side-nav] [data-menu=files]')
        await page.waitForFunction(() => location.pathname === '/files')
        await emitUntil(page, 'tray:open-settings', () => location.pathname === '/settings')
        await page.waitForSelector('[data-testid=settings-page][data-state=ready]', { timeout: 15000 })
        await expectText(page, '[data-testid=settings-server]', [SERVER, '正常', '响应', '修改服务器地址'])
        const version = await page.$eval('[data-testid=settings-server-version]', (el) => el.textContent.trim())
        if (!/^\d+\.\d+\.\d+/.test(version)) throw new Error(`服务端版本不对：${version}`)
        await expectText(page, '[data-testid=settings-account]', ['系统管理员', admin, '退出登录'])
        await expectText(page, '[data-testid=settings-storage]', [DATA_DIR, '1.3 GB', '在资源管理器中打开', `${DATA_DIR}\\logs`, '14 个文件'])
        await page.waitForFunction(() => document.querySelectorAll('[data-testid=settings-cache] [data-testid=cache-project-row]').length === 2, { timeout: 10000 })
        const counts = await page.$$eval('[data-testid=settings-queue-stats] [data-testid=queue-count]', (els) => els.map((e) => e.textContent.trim()))
        if (counts.join(',') !== '3,1,2,141') throw new Error(`上传队列的计数不对：${counts}`)
        await expectText(page, '[data-testid=settings-hotfolder]', ['运行中', 'D:\\扫描\\输出', '实时监控目录', '去扫描上传设置'])
        await page.waitForFunction(() => document.querySelector('[data-testid=settings-hotfolder-target]')?.textContent.includes('/'), { timeout: 10000 })
        await expectText(page, '[data-testid=settings-about]', ['0.2.0', '0.1.0', 'Windows 10 Pro', 'amd64', '复制诊断信息'])

        // 打开目录：只把名字交给客户端。
        await page.click('[data-testid=open-data-dir]')
        await page.click('[data-testid=open-logs-dir]')
        await page.click('[data-testid=open-cache-dir]')
        const opened = await page.evaluate(() => window.__shell.openedDirs)
        if (opened.join(',') !== 'data,logs,cache') throw new Error(`打开的目录不对：${opened}`)
        const sent = await page.evaluate(() => window.__shell.calls.filter((c) => c.cmd === 'open_local_dir').map((c) => Object.keys(c.args).join('+')))
        if (sent.some((k) => k !== 'dir')) throw new Error(`open_local_dir 只该带 dir：${sent}`)

        // 重试失败的：调 upload_retry_failed，失败数变 0（随 upload:state 更新）。
        await page.click('[data-testid=queue-retry-failed]')
        await page.waitForFunction(() => document.querySelector('[data-testid=queue-failed] [data-testid=queue-count]')?.textContent.trim() === '0', { timeout: 5000 })

        // 复制诊断信息：有版本、服务器、账号、目录、队列，没有密码和会话。
        await captureCopies(page)
        await page.click('[data-testid=settings-copy-diagnostics]')
        await page.waitForFunction(() => window.__copied.length > 0, { timeout: 5000 })
        const text = await page.evaluate(() => window.__copied.at(-1))
        for (const want of ['客户端版本：0.2.0', '界面版本：0.1.0', `服务器：${SERVER}`, '连接：正常', `（${admin}，`, DATA_DIR, '上传队列：', '热文件夹：运行中', '客户端的能力：']) {
          if (!text.includes(want)) throw new Error(`诊断信息里没有「${want}」：${text}`)
        }
        for (const bad of [adminPassword, 'doclite_session', 'Cookie', 'cookie', 'token', 'password']) {
          if (text.includes(bad)) throw new Error(`诊断信息里不该有「${bad}」`)
        }
        await page.evaluate(() => document.querySelector('[data-testid=settings-page]').scrollTo(0, 0))
      },
      delay: 600,
    },
    // 设置页：本机图片缓存的"全部清空"先确认（说清删多少个、多大，服务器上的不受影响）。
    {
      name: 'shell-settings-cache-clear',
      path: '/login',
      shell: true,
      init: settingsInit(),
      waitFor: '#username',
      async prepare(page) {
        await openSettings(page)
        await page.waitForFunction(() => document.querySelectorAll('[data-testid=settings-cache] [data-testid=cache-project-row]').length === 2, { timeout: 10000 })
        await page.$eval('[data-testid=settings-cache]', (el) => el.scrollIntoView({ block: 'center' }))
        await page.click('[data-testid=settings-cache] [data-testid=cache-clear-all]')
        await page.waitForSelector('[role=alertdialog]')
        await expectText(page, '[role=alertdialog]', ['清空全部缓存', '15230 个图片文件', '服务器上的图片不受影响'])
      },
      delay: 500,
    },
    // 设置页在比网页旧的客户端里（能力只有 api、server、external，shell_info 没有架构和系统版本）：服务器、账号、
    // 数据目录、关于照常；没有的能力对应的分组和按钮不出现（改地址、打开目录、图片缓存、上传队列、日志目录、热文件夹），
    // 相应的命令不发出去，页面不报错。
    {
      name: 'shell-settings-old-client',
      path: '/login',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true }, { capabilities: ['api', 'server', 'external'], version: '0.1.9', arch: null, osVersion: null }),
      waitFor: '#username',
      async prepare(page) {
        await openSettings(page, scanner, scannerPassword)
        await expectText(page, '[data-testid=settings-server]', [SERVER, '正常'])
        await expectText(page, '[data-testid=settings-account]', ['demo_scanner'])
        await expectText(page, '[data-testid=settings-about]', ['0.1.9'])
        await expectText(page, '[data-testid=settings-storage]', [DATA_DIR])
        for (const sel of ['settings-edit-server', 'open-data-dir', 'settings-data-size', 'settings-cache', 'settings-queue', 'settings-logs-dir', 'settings-hotfolder']) {
          if (await page.$(`[data-testid=${sel}]`)) throw new Error(`客户端没有相应能力时不该出现 ${sel}`)
        }
        const runtime = await page.$eval('[data-testid=settings-runtime]', (el) => el.textContent.trim())
        if (runtime !== 'Windows') throw new Error(`旧客户端没有架构和系统版本，运行环境应为 Windows：${runtime}`)
        const calls = await page.evaluate(() => window.__shell.calls.map((c) => c.cmd))
        const sent = calls.filter((c) => /^(upload_|hotfolder_|cache_)/.test(c) || c === 'local_dirs' || c === 'open_local_dir')
        if (sent.length > 0) throw new Error(`客户端没有的能力的命令也发出去了：${sent.join('、')}`)
        const toasts = await page.$$eval('[data-sonner-toast][data-type=error]', (els) => els.map((e) => e.textContent))
        if (toasts.length > 0) throw new Error(`页面报错了：${toasts.join(' | ')}`)
      },
      delay: 500,
    },
    // 侧栏收起（M7-11）：业务界面。只有图标，悬停提示名字，当前菜单高亮，项目切换器只显示项目编码；刷新后保持。
    {
      name: 'shell-sidebar-collapsed',
      path: '/login',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true }),
      waitFor: '#username',
      async prepare(page) {
        await login(page, admin, adminPassword)
        if ((await sidebarState(page)) !== 'false') throw new Error('一开始应该是展开的')
        await page.click('[data-testid=sidebar-toggle]')
        await page.waitForFunction(() => document.querySelector('[data-testid=side-nav]').dataset.collapsed === 'true')
        await page.click('[data-testid=side-nav] [data-menu=files]')
        await page.waitForFunction(() => location.pathname === '/files')
        await new Promise((r) => setTimeout(r, 300))
        await expectCollapsedLayout(page)
        const active = await page.$eval('[data-testid=side-nav] [data-active=true]', (el) => el.dataset.menu)
        if (active !== 'files') throw new Error(`当前菜单没有高亮：${active}`)
        const code = await page.$eval('[data-testid=project-current-code]', (el) => el.textContent.trim())
        if (!code) throw new Error('收起时项目切换器要显示项目编码')
        // 刷新后仍是收起的（按本机记住）。
        await page.reload({ waitUntil: 'networkidle2' })
        await page.waitForSelector('[data-testid=side-nav]')
        if ((await sidebarState(page)) !== 'true') throw new Error('刷新后没有保持收起')
        // 项目切换器点开照样能切换。
        await page.click('[data-testid=project-switcher-trigger]')
        await page.waitForSelector('[data-testid=project-options] [data-testid=project-option]')
        await page.keyboard.press('Escape')
        await page.hover('[data-testid=side-nav] [data-menu=review]')
        await page.waitForSelector('[role=tooltip]')
        await expectText(page, '[role=tooltip]', ['我的审核'])
      },
      delay: 500,
    },
    // 侧栏收起（M7-11）：系统管理。头像点开是用户菜单（设置、退出登录）；快捷键在输入框里不触发。
    {
      name: 'shell-sidebar-collapsed-system',
      path: '/login',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true }),
      waitFor: '#username',
      async prepare(page) {
        await login(page, admin, adminPassword)
        await page.click('[data-testid=nav-system]')
        await page.waitForFunction(() => location.pathname.startsWith('/system/'))
        await page.waitForSelector('[data-testid=side-nav] [data-menu^="system."]')
        // 快捷键收起；焦点在输入框里时不触发。
        await pressSidebarShortcut(page)
        await page.waitForFunction(() => document.querySelector('[data-testid=side-nav]').dataset.collapsed === 'true')
        const input = await page.$('main input')
        if (!input) throw new Error('系统管理页上没有输入框，测不了快捷键')
        await input.focus()
        await pressSidebarShortcut(page)
        await new Promise((r) => setTimeout(r, 200))
        if ((await sidebarState(page)) !== 'true') throw new Error('在输入框里按快捷键不该切换侧栏')
        await page.evaluate(() => document.activeElement?.blur())
        await pressSidebarShortcut(page)
        await page.waitForFunction(() => document.querySelector('[data-testid=side-nav]').dataset.collapsed === 'false')
        await pressSidebarShortcut(page)
        await page.waitForFunction(() => document.querySelector('[data-testid=side-nav]').dataset.collapsed === 'true')
        await new Promise((r) => setTimeout(r, 300))
        await expectCollapsedLayout(page)
        if (!(await page.$('[data-testid=nav-back]'))) throw new Error('收起时要有"返回业务界面"')
        await page.click('[data-testid=user-menu]')
        await page.waitForSelector('[data-testid=user-menu-content]')
        await expectText(page, '[data-testid=user-menu-content]', ['系统管理员', '设置', '退出登录'])
      },
      delay: 500,
    },
    // 比网页旧的客户端（能力只有 api、server、external）：扫描上传页提示升级客户端，登录页"修改服务器地址"不出现，
    // 页面不报错（设置页见 shell-settings-old-client）。
    {
      name: 'shell-old-client',
      path: '/login',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true }, { capabilities: ['api', 'server', 'external'], version: '0.1.9' }),
      waitFor: '#username',
      async prepare(page) {
        if (await page.$('[data-testid=login-server] button')) throw new Error('客户端没有自带页面时不该出现"修改"')
        await login(page, scanner, scannerPassword)
        await page.goto(new URL('/scan', page.url()).href, { waitUntil: 'networkidle2' })
        await page.waitForSelector('[data-testid=scan-client-old]')
        await expectText(page, '[data-testid=scan-client-old]', ['请升级桌面客户端', '0.1.9'])
        const calls = await page.evaluate(() => window.__shell.calls.map((c) => c.cmd))
        const sent = calls.filter((c) => c.startsWith('upload_') || c.startsWith('hotfolder_') || c.startsWith('cache_'))
        if (sent.length > 0) throw new Error(`客户端没有的能力的命令也发出去了：${sent.join('、')}`)
        const toasts = await page.$$eval('[data-sonner-toast][data-type=error]', (els) => els.map((e) => e.textContent))
        if (toasts.length > 0) throw new Error(`页面报错了：${toasts.join(' | ')}`)
      },
      delay: 500,
    },
  ],
}
