// 壳里的整页提示和网页里的壳入口：npm run screenshots -- --spec scripts/shots/m1-shell.mjs --base <devshell 的地址>
//
// M7-6 起连接服务器、连不上、需要升级是客户端自带的页面（client/internal/shellpages，devshell 的 /shell/ 给的是同一份），
// 界面只负责跳过去。升级页只信探测（check_server）的结果：探测说不用升级时不显示，下载地址也只用探测到的。页面以壳模式的地址打开（shell: true），在页面脚本运行前注入 Wails 桥的模拟
// （scripts/lib/wails-mock.mjs），客户端自带的页面和网页用的是同一个模拟：
// - shell_info / check_server / probe_server 按场景返回（没有服务器地址、连不上、版本够、版本过低）；
// - api_fetch 默认转给 devshell 的 /api（真实 server，要先启动），按场景拦截某些请求（例如 /api/me 返回 426）。
// 最后一张模拟比网页旧的客户端（shell_info 的能力只有 api、server、external）：网页里相应的本机功能隐藏、提示升级。
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
    // 登录后"关于"：网页版本、客户端版本、服务器地址、数据目录。
    {
      name: 'shell-about',
      path: '/login',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true }),
      waitFor: '#username',
      async prepare(page) {
        await login(page, admin, adminPassword)
        await page.click('[data-testid=about]')
        await page.waitForSelector('[data-testid=about-dialog]')
        await expectText(page, '[data-testid=about-dialog]', ['界面版本', '客户端版本', '0.2.0', SERVER, DATA_DIR, '修改服务器地址'])
        await expectText(page, '[data-testid=about-client-version]', ['0.2.0'])
      },
      delay: 500,
    },
    // 比网页旧的客户端（能力只有 api、server、external）：扫描上传页提示升级客户端，本机图片缓存、修改服务器地址的
    // 入口不出现，页面不报错。
    {
      name: 'shell-old-client',
      path: '/login',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true }, { capabilities: ['api', 'server', 'external'], version: '0.1.9' }),
      waitFor: '#username',
      async prepare(page) {
        if (await page.$('[data-testid=login-server] button')) throw new Error('客户端没有自带页面时不该出现"修改"')
        await login(page, scanner, scannerPassword)
        if (await page.$('[data-testid=image-cache]')) throw new Error('客户端没有 images 能力时不该出现本机图片缓存')
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
