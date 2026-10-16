// 截图脚本里的"壳模式"（M6-4）：让页面以为自己在 Wails 壳里，正式前端代码不用改。
//
// 1. 地址：页面靠地址判断自己在不在壳里（web/src/lib/client.ts 的 isShell：macOS 的壳是 wails://localhost，Windows 的
//    WebView2 是 http://wails.localhost）。脚本用 `shellOrigin(base)` = `http://wails.localhost:<端口>` 打开 devshell
//    （client/cmd/devshell；Chrome 把 *.localhost 解析到本机，不用改 hosts）。devshell 做打包的壳的资产服务器做的事，
//    只是界面从 vite 现取（截图测的是 web 的源码）：给这个地址的页面加 CSP、/shell/ 是客户端自带的页面（连接服务器、
//    连不上、需要升级）、/wails/runtime.js 是 Wails 的运行时、/api/ 转给 vite、/shell/img/（取图、本机缓存、切块）用
//    客户端的 Go 代码现场做——和打包的壳是同一份实现。所以 --base 要指向 devshell，不是 vite 本身
//    （见 README"跑截图脚本"）。
// 2. Wails 的桥在页面里模拟（`installWailsMock`，页面脚本运行前注入）。页面（web/src/lib/wails.ts）和客户端自带的页面
//    都用 devshell 给的 /wails/runtime.js：
//    - 命令：运行时的 Call.ByName 发 `POST /wails/runtime`，body `{object: 0, method: 0, args: {methodName, args: [命令, 参数]}}`。
//      这里包住 window.fetch 把它拦下，交给 `window.__shell.commands[命令](参数)`；抛出 `{kind, message}` 时像 Wails
//      一样回 500 `{message, cause: {kind, message}}`（页面的 invoke 从 cause 里取出 ShellError）。每次调用记在
//      `window.__shell.calls`（{cmd, args}）。没登记的命令回 `unsupported`（和客户端一样）。
//    - 事件：壳的 app.Event.Emit 到页面是 `_wails.dispatchWailsEvent({name, data})`；脚本用 `window.__shell.emit(名字, 数据)`。
//    - 流（api_stream，实时通道 SSE）：Wails 的 JSONStream。运行时留了 `window._wails.streamFactory`（Wails 的 server 构建
//      用它换成 WebSocket），这里给它一个模拟的连接：收到 `{req}` 后用同源 fetch 转给 devshell 的 /api，逐块发回
//      open、chunk、end（和壳的 handleAPIStream 一样）。`window.__shell.stream = async (req, send, signal) => …` 可以换掉。
//    - 默认的命令（脚本可以覆盖）：shell_info（含能力 capabilities，options.capabilities 可换）、check_server、
//      probe_server、set_server、api_fetch（同源 fetch，响应头按壳的白名单）、open_external（只记下）、tray_status、
//      上传队列和热文件夹的空状态；prepare_tiles、cache_stats、cache_clear 转给 devshell 的真实实现；设置页的
//      local_dirs（数据目录下的 logs、image-cache，大小是假的）、open_local_dir（和壳一样只认 data、logs、cache，
//      记在 `window.__shell.openedDirs`，不打开任何东西）。
// 3. 写法：
//      import { installWailsMock, shellOrigin } from '../lib/wails-mock.mjs'
//      init: (page) => installWailsMock(page, { serverUrl: 'http://192.168.1.10:9080' }, (scenario) => {
//        // 在页面里执行（会被序列化，不能引用外部变量）：覆盖或补充命令
//        Object.assign(window.__shell.commands, { upload_status: () => scenario.status })
//      }, scenario)
//    清单里的这一张写 `shell: true`，screenshots.mjs 就用 shellOrigin(base) 打开 path；脚本自己 goto 的地址也要用它。
//    页面之外登录拿到的 Cookie 要设在 shellOrigin(base) 上（`useShellSession`）。
// 以后 Windows 上（M8）照样能用：只要本机有 Chrome、node 和 devshell。

/** 当前客户端的能力，见 client/BRIDGE.md（settings 是 M7-11 加的）。 */
export const ALL_CAPABILITIES = ['api', 'server', 'pages', 'external', 'tray', 'upload', 'rescan', 'images', 'settings']

/** 壳模式的地址：把 base 的主机换成 wails.localhost（端口不变）。 */
export function shellOrigin(base) {
  const u = new URL(base)
  u.hostname = 'wails.localhost'
  return u.origin
}

/** 把页面之外登录拿到的会话 Cookie（`name=value`）放进浏览器上下文，壳模式的地址用。 */
export async function useShellSession(page, base, cookie) {
  const i = cookie.indexOf('=')
  await page.setCookie({ name: cookie.slice(0, i), value: cookie.slice(i + 1), url: shellOrigin(base) })
}

/**
 * 在页面脚本运行前装上 Wails 桥的模拟。options：{serverUrl, dataDir, platform, arch, osVersion, version, webVersion,
 * capabilities}（shell_info 用；arch、osVersion 给 null 时 shell_info 里没有这两项，像 M7-11 之前的客户端）。
 * extra(arg) 在通用的模拟之后执行（同样在页面里、会被序列化），用来覆盖或补充 window.__shell.commands。
 */
export async function installWailsMock(page, options = {}, extra = null, arg = undefined) {
  await page.evaluateOnNewDocument(wailsBridgeMock, { capabilities: ALL_CAPABILITIES, ...options })
  if (extra) await page.evaluateOnNewDocument(extra, arg)
}

/** 在页面里执行（会被序列化，不能引用外部变量）。 */
function wailsBridgeMock(options) {
  const INVOKE = 'doclite/client/internal/shell.Bridge.Invoke'
  const API_STREAM = 'doclite.api_stream'
  const RESPONSE_HEADERS = ['content-type', 'content-disposition', 'etag', 'last-modified', 'cache-control', 'retry-after']
  const version = options.version ?? '0.2.0'
  // serverUrl 为 null 表示还没配置服务器（第一次启动）。set_server 改过的记在 sessionStorage 里：客户端自带的页面
  // 保存地址后会整页跳转，模拟随新页面重装，要接着用新地址（和壳的设置一样）。
  const SAVED = '__doclite_mock_server_url'
  const saved = (() => {
    try {
      return sessionStorage.getItem(SAVED)
    } catch {
      return null
    }
  })()
  const serverUrl = saved ?? (options.serverUrl === undefined ? 'http://127.0.0.1:9080' : options.serverUrl)
  const rawFetch = window.fetch.bind(window)

  const probe = (url, reachable = true) => ({
    server_url: url,
    reachable,
    error: reachable ? null : '无法连接服务器',
    server_version: reachable ? version : null,
    min_version: null,
    latest_version: null,
    download_url: null,
    client_version: version,
    upgrade_required: false,
  })
  const normalize = (url) => {
    const s = String(url).trim().replace(/\/+$/, '')
    return s.includes('://') ? s : `http://${s}`
  }
  const devshell = async (path, body) => {
    const res = await rawFetch(`/__devshell/${path}`, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
    const data = await res.json()
    if (!res.ok) throw data
    return data
  }
  const emptyStats = { waiting: 0, queued: 0, uploading: 0, done: 0, duplicate: 0, failed: 0, retrying: 0, total: 0 }
  const dataDir = options.dataDir ?? 'C:\\Users\\scan01\\AppData\\Local\\Doclite'
  const sep = dataDir.includes('\\') ? '\\' : '/'
  const MB = 1024 * 1024

  const shell = {
    serverUrl,
    calls: [],
    externals: [],
    stream: null,
    emit(name, data) {
      const dispatch = window._wails?.dispatchWailsEvent
      if (dispatch) dispatch({ name, data: data ?? null })
    },
    commands: {
      shell_info: () => ({
        version,
        web_version: options.webVersion ?? '0.1.0',
        server_url: shell.serverUrl,
        data_dir: dataDir,
        platform: options.platform ?? 'windows',
        // null：没有这一项（M7-11 之前的客户端）。
        arch: options.arch === null ? undefined : (options.arch ?? 'amd64'),
        os_version: options.osVersion === null ? undefined : (options.osVersion ?? 'Windows 10 Pro 2009 (Build: 19045)'),
        smoke: false,
        capabilities: options.capabilities,
      }),
      check_server: () => {
        if (!shell.serverUrl) throw { kind: 'not_configured', message: '还没有设置服务器地址' }
        return probe(shell.serverUrl)
      },
      probe_server: (args) => probe(normalize(args.url)),
      set_server: (args) => {
        shell.serverUrl = normalize(args.url)
        try {
          sessionStorage.setItem(SAVED, shell.serverUrl)
        } catch {
          // 存不下就只在这一页有效。
        }
        return shell.serverUrl
      },
      async api_fetch(args) {
        const req = args.req
        const qs = new URLSearchParams(req.query).toString()
        const res = await rawFetch(qs ? `${req.path}?${qs}` : req.path, { method: req.method, headers: req.headers, body: req.body, cache: 'no-store' })
        const headers = {}
        for (const name of RESPONSE_HEADERS) {
          const v = res.headers.get(name)
          if (v) headers[name] = v
        }
        return { status: res.status, headers, body: await res.text() }
      },
      open_external: (args) => {
        shell.externals.push(args.url)
        return null
      },
      tray_status: () => ({
        created: true,
        error: null,
        close_to_tray: true,
        tooltip: 'Doclite',
        status_line: 'Doclite · 已登录',
        counts_line: '排队 0 · 上传中 0 · 已入库 0 · 重复 0 · 失败 0',
        toggle: 'unavailable',
        close_check: null,
      }),
      upload_status: () => ({ server_url: shell.serverUrl, paused: null, paused_sources: ['hotfolder'], stats: { ...emptyStats }, running: 0 }),
      upload_recent: () => [],
      hotfolder_status: () => ({ config: null, running: false, watching: false, error: null, last_scan_at: null, scans: 0 }),
      prepare_tiles: (args) => devshell('prepare_tiles', { fileId: args.fileId, snapshotId: args.snapshotId }),
      cache_stats: () => devshell('cache_stats'),
      cache_clear: (args) => devshell('cache_clear', { projectId: args.projectId ?? null }),
      local_dirs: () => ({
        data: { path: dataDir, size_bytes: Math.round(1342.6 * MB), file_count: 15262 },
        logs: { path: `${dataDir}${sep}logs`, size_bytes: Math.round(3.4 * MB), file_count: 14 },
        cache: { path: `${dataDir}${sep}image-cache`, size_bytes: Math.round(1331.2 * MB), file_count: 15230 },
      }),
      open_local_dir: (args) => {
        if (!['data', 'logs', 'cache'].includes(args.dir)) throw { kind: 'invalid_request', message: 'dir 只能是 data、logs、cache' }
        shell.openedDirs.push(args.dir)
        return null
      },
    },
  }
  shell.openedDirs = []
  window.__shell = shell

  async function invoke(cmd, args) {
    shell.calls.push({ cmd, args })
    const fn = shell.commands[cmd]
    if (!fn) throw { kind: 'unsupported', message: `这个版本的客户端没有命令 ${cmd}，请升级客户端（截图模拟）` }
    return fn(args ?? {})
  }

  const json = (status, body) =>
    new Response(JSON.stringify(body ?? null), { status, headers: { 'Content-Type': 'application/json' } })

  async function runtimeCall(init) {
    let body = {}
    try {
      body = JSON.parse(typeof init?.body === 'string' ? init.body : new TextDecoder().decode(init?.body))
    } catch {
      return json(400, { kind: 'TypeError', message: '截图模拟只认 JSON 的运行时调用' })
    }
    // object 0 = Call（绑定方法），其余（取消调用、窗口、右键菜单……）一律成功、什么都不做。
    if (body.object !== 0) return json(200, null)
    const call = body.args ?? {}
    if (call.methodName !== INVOKE) return json(500, { kind: 'ReferenceError', message: `没有绑定方法 ${call.methodName}` })
    const [cmd, args] = call.args ?? []
    try {
      return json(200, await invoke(cmd, args))
    } catch (e) {
      const cause = e && typeof e.kind === 'string' ? { kind: e.kind, message: String(e.message) } : { kind: 'internal', message: String(e?.message ?? e) }
      return json(500, { kind: 'RuntimeError', message: cause.message, cause })
    }
  }

  window.fetch = (input, init) => {
    const url = new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url, location.href)
    if (url.origin === location.origin && url.pathname === '/wails/runtime') return runtimeCall(init)
    return rawFetch(input, init)
  }

  // ---- 流：JSONStream 的模拟连接 ----

  /** 默认：同源 fetch 转给 devshell 的 /api（真实 server），逐块发回（和壳的 handleAPIStream、proxy.Stream 一样）。 */
  async function proxyStream(req, send, signal) {
    const qs = new URLSearchParams(req.query).toString()
    let res
    try {
      res = await rawFetch(qs ? `${req.path}?${qs}` : req.path, { method: req.method, headers: req.headers, body: req.body, cache: 'no-store', signal })
    } catch {
      if (!signal.aborted) send({ event: 'error', kind: 'network', message: '连不上服务器' })
      return
    }
    const headers = {}
    for (const name of RESPONSE_HEADERS) {
      const v = res.headers.get(name)
      if (v) headers[name] = v
    }
    send({ event: 'open', status: res.status, headers })
    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        const text = decoder.decode(value, { stream: true })
        if (text) send({ event: 'chunk', data: text })
      }
      const tail = decoder.decode()
      if (tail) send({ event: 'chunk', data: tail })
      send({ event: 'end' })
    } catch {
      if (!signal.aborted) send({ event: 'error', kind: 'network', message: '和服务器的连接中断了' })
    }
  }

  class MockSocket extends EventTarget {
    constructor(name) {
      super()
      this.name = name
      this.url = `${location.origin}/wails/stream/poll#${encodeURIComponent(name)}`
      this.readyState = 0
      this.binaryType = 'arraybuffer'
      this.bufferedAmount = 0
      this.protocol = ''
      this.extensions = ''
      this.onopen = null
      this.onclose = null
      this.onerror = null
      this.onmessage = null
      this.abort = new AbortController()
      setTimeout(() => {
        if (this.readyState !== 0) return
        this.readyState = 1
        this.fire('open', new Event('open'))
      }, 0)
    }
    fire(type, ev) {
      const h = this[`on${type}`]
      // onmessage 由 JSONStream 改成了 addEventListener（解析 JSON 后再回调），其余的这里直接调。
      if (type !== 'message' && typeof h === 'function') h.call(this, ev)
      this.dispatchEvent(ev)
    }
    push(obj) {
      if (this.readyState !== 1) return
      this.fire('message', new MessageEvent('message', { data: JSON.stringify(obj) }))
    }
    send(data) {
      if (this.readyState !== 1) throw new Error('连接还没打开')
      if (this.name !== API_STREAM || this.started) return
      this.started = true
      let first
      try {
        first = JSON.parse(String(data))
      } catch {
        this.push({ event: 'error', kind: 'invalid_request', message: '缺少 req' })
        return
      }
      const run = shell.stream ?? proxyStream
      Promise.resolve(run(first.req, (ev) => this.push(ev), this.abort.signal)).catch((e) =>
        this.push({ event: 'error', kind: 'internal', message: String(e?.message ?? e) }),
      )
    }
    close(code = 1000, reason = '') {
      if (this.readyState >= 2) return
      this.readyState = 3
      this.abort.abort()
      this.fire('close', new CloseEvent('close', { code, reason, wasClean: true }))
    }
  }

  window._wails = window._wails || {}
  window._wails.streamFactory = (name) => new MockSocket(name)
}
