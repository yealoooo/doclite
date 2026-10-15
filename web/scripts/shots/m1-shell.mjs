// M1-2 壳里的整页提示截图：npm run screenshots -- --spec scripts/shots/m1-shell.mjs --base <devshell 的地址>
//
// 页面以壳模式的地址打开（shell: true），在页面脚本运行前注入 Wails 桥的模拟（scripts/lib/wails-mock.mjs），让页面
// 以为自己在壳里：
// - shell_info / check_server / probe_server 按场景返回（没有服务器地址、连不上、版本够）；
// - api_fetch 默认转给 devshell 的 /api（真实 server，要先启动），按场景拦截某些请求（例如 /api/me 返回 426）。
// 管理员账号同 m0.mjs（INIT_ADMIN_USERNAME / INIT_ADMIN_PASSWORD，默认 admin / change-me-now）。

import { installWailsMock } from '../lib/wails-mock.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'

const SERVER = 'http://192.168.1.10:9080'

/** 在页面里执行（会被序列化，不能引用外部变量）：按场景覆盖通用模拟的几个命令。 */
function scenarioMock(scenario) {
  const shell = window.__shell
  const probe = shell.commands.probe_server
  const unreachable = (url) => ({ ...probe({ url }), reachable: false, server_version: null, error: '无法连接服务器' })
  shell.commands.check_server = () => (scenario.checkReachable ? probe({ url: scenario.serverUrl }) : unreachable(scenario.serverUrl))
  shell.commands.probe_server = async (args) => {
    // 慢一点，看得到"正在连接…"。
    await new Promise((r) => setTimeout(r, 300))
    return unreachable(String(args.url).includes('://') ? args.url : `http://${args.url}`)
  }
  const apiFetch = shell.commands.api_fetch
  shell.commands.api_fetch = async (args) => {
    if (scenario.upgradeOn === args.req.path) {
      return {
        status: 426,
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          code: 'client_upgrade_required',
          message: '客户端版本过低，请更新',
          details: {
            client_version: '0.1.0',
            min_version: '0.2.0',
            latest_version: '0.2.1',
            download_url: 'http://192.168.1.10:9080/downloads/Doclite_0.2.1_x64-setup.exe',
          },
        }),
      }
    }
    // 其余请求转给 devshell 的 /api（真实 server）。
    return apiFetch(args)
  }
}

const mock = (scenario) => (page) => installWailsMock(page, { serverUrl: scenario.serverUrl }, scenarioMock, scenario)

export default {
  out: '../docs/screenshots/m1',
  accounts: {},
  shots: [
    // 第一次启动：还没有服务器地址。
    {
      name: 'shell-connect',
      path: '/',
      shell: true,
      init: mock({ serverUrl: null }),
      waitFor: '[data-testid=connect-server]',
      async prepare(page) {
        await page.type('#server-address', '192.168.1.10:9080')
      },
    },
    // 连接服务器页：探测失败的提示。
    {
      name: 'shell-connect-failed',
      path: '/',
      shell: true,
      init: mock({ serverUrl: null }),
      waitFor: '[data-testid=connect-server]',
      async prepare(page) {
        await page.type('#server-address', '192.168.1.10:9080')
        await page.click('[data-testid=connect-submit]')
        await page.waitForSelector('[data-testid=connect-error]')
      },
    },
    // 启动时连不上已配置的服务器。
    {
      name: 'shell-unreachable',
      path: '/',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: false }),
      waitFor: '[data-testid=server-unreachable]',
    },
    // 请求返回 426：整页提示更新。
    {
      name: 'shell-upgrade',
      path: '/',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true, upgradeOn: '/api/me' }),
      waitFor: '[data-testid=upgrade-required]',
    },
    // 壳里的登录页：显示服务器地址，可以修改。
    {
      name: 'shell-login',
      path: '/login',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true }),
      waitFor: '[data-testid=login-server]',
    },
    // 登录后"关于"：版本号、服务器地址、数据目录。
    {
      name: 'shell-about',
      path: '/login',
      shell: true,
      init: mock({ serverUrl: SERVER, checkReachable: true }),
      waitFor: '#username',
      async prepare(page) {
        await page.type('#username', admin)
        await page.type('#password', adminPassword)
        await page.click('[data-testid=login-submit]')
        await page.waitForSelector('[data-testid=side-nav]')
        await page.click('[data-testid=about]')
        await page.waitForSelector('[data-testid=about-dialog]')
        // 关于对话框里的版本、服务器、数据目录来自壳（shell_info）。
        const text = await page.$eval('[data-testid=about-dialog]', (el) => el.textContent)
        for (const want of ['0.1.0', SERVER, 'C:\\Users\\scan01\\AppData\\Local\\Doclite']) {
          if (!text.includes(want)) throw new Error(`关于对话框里没有 ${want}：${text}`)
        }
      },
      delay: 500,
    },
  ],
}
