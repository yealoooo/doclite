// M1-2 壳里的整页提示截图：npm run screenshots -- --spec scripts/shots/m1-shell.mjs --base http://localhost:<preview 端口>
//
// 用无头 Chrome 打开 vite preview 的页面，并在页面脚本运行前注入 Tauri IPC 的模拟（window.__TAURI_INTERNALS__），
// 让页面以为自己在壳里：
// - shell_info / check_server / probe_server 按场景返回（没有服务器地址、连不上、版本够）；
// - api_fetch 默认转给 preview 的 /api 代理（真实 server，要先启动），按场景拦截某些请求（例如 /api/me 返回 426）；
// - convertFileSrc 按 Windows 的形式生成 http://<协议>.localhost/…。
// 管理员账号同 m0.mjs（INIT_ADMIN_USERNAME / INIT_ADMIN_PASSWORD，默认 admin / change-me-now）。

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'

const SERVER = 'http://192.168.1.10:9080'

/** 在页面里执行（会被序列化，不能引用外部变量）。 */
function installTauriMock(scenario) {
  const version = '0.1.0'
  const reachable = (url) => ({
    server_url: url,
    reachable: true,
    error: null,
    server_version: '0.1.0',
    min_version: null,
    latest_version: null,
    download_url: null,
    client_version: version,
    upgrade_required: false,
  })
  const unreachable = (url) => ({ ...reachable(url), reachable: false, server_version: null, error: '无法连接服务器' })
  const normalize = (url) => {
    const s = url.trim().replace(/\/+$/, '')
    return s.includes('://') ? s : `http://${s}`
  }

  let nextId = 1
  window.__TAURI_INTERNALS__ = {
    transformCallback(callback) {
      const id = nextId++
      window[`_${id}`] = callback
      return id
    },
    unregisterCallback(id) {
      delete window[`_${id}`]
    },
    convertFileSrc(path, protocol = 'asset') {
      return `http://${protocol}.localhost/${encodeURIComponent(path)}`
    },
    async invoke(cmd, args) {
      switch (cmd) {
        case 'shell_info':
          return {
            version,
            server_url: scenario.serverUrl,
            data_dir: 'C:\\Users\\scan01\\AppData\\Local\\Doclite',
            platform: 'windows',
            smoke: false,
          }
        case 'check_server':
          return scenario.checkReachable ? reachable(scenario.serverUrl) : unreachable(scenario.serverUrl)
        case 'probe_server':
          // 慢一点，看得到"正在连接…"。
          await new Promise((r) => setTimeout(r, 300))
          return unreachable(normalize(args.url))
        case 'set_server':
          return normalize(args.url)
        case 'open_external':
          return null
        case 'api_fetch': {
          const req = args.req
          if (scenario.upgradeOn === req.path) {
            return {
              status: 426,
              headers: { 'content-type': 'application/json' },
              body: JSON.stringify({
                code: 'client_upgrade_required',
                message: '客户端版本过低，请更新',
                details: {
                  client_version: version,
                  min_version: '0.2.0',
                  latest_version: '0.2.1',
                  download_url: 'http://192.168.1.10:9080/downloads/Doclite_0.2.1_x64-setup.exe',
                },
              }),
            }
          }
          // 其余请求转给 preview 的 /api 代理（真实 server）。
          const qs = new URLSearchParams(req.query).toString()
          const res = await fetch(qs ? `${req.path}?${qs}` : req.path, {
            method: req.method,
            headers: req.headers,
            body: req.body,
          })
          return { status: res.status, headers: {}, body: await res.text() }
        }
        default:
          throw { kind: 'internal', message: `截图模拟里没有命令 ${cmd}` }
      }
    },
  }
}

const mock = (scenario) => (page) => page.evaluateOnNewDocument(installTauriMock, scenario)

export default {
  out: '../docs/screenshots/m1',
  accounts: {},
  shots: [
    // 第一次启动：还没有服务器地址。
    {
      name: 'shell-connect',
      path: '/',
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
      init: mock({ serverUrl: SERVER, checkReachable: false }),
      waitFor: '[data-testid=server-unreachable]',
    },
    // 请求返回 426：整页提示更新。
    {
      name: 'shell-upgrade',
      path: '/',
      init: mock({ serverUrl: SERVER, checkReachable: true, upgradeOn: '/api/me' }),
      waitFor: '[data-testid=upgrade-required]',
    },
    // 壳里的登录页：显示服务器地址，可以修改。
    {
      name: 'shell-login',
      path: '/login',
      init: mock({ serverUrl: SERVER, checkReachable: true }),
      waitFor: '[data-testid=login-server]',
    },
    // 登录后"关于"：版本号、服务器地址、数据目录。
    {
      name: 'shell-about',
      path: '/login',
      init: mock({ serverUrl: SERVER, checkReachable: true }),
      waitFor: '#username',
      async prepare(page) {
        await page.type('#username', admin)
        await page.type('#password', adminPassword)
        await page.click('[data-testid=login-submit]')
        await page.waitForSelector('[data-testid=side-nav]')
        await page.click('[data-testid=about]')
        await page.waitForSelector('[data-testid=about-dialog]')
      },
      delay: 500,
    },
  ],
}
