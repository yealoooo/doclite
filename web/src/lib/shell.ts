// 壳（Wails v3 桌面客户端，client/）提供的命令，只在 isShell() 时调用。Go 端见 client/internal/shell/bridge.go。
//
// - 页面和 server 之间的请求由壳的 Go 端代理（requirements 第 6 节"会话"）：普通请求走 `api_fetch`，SSE 走
//   `api_stream`（Wails 的流逐块推送）。只能访问已配置服务器上 /api/ 下的路径；X-Client-Type、X-Client-Version 和
//   会话 Cookie 都由 Go 端加，页面拿不到会话令牌。
// - 命令失败时 reject 的值是 `{kind, message}`（ShellError）。
// - 命令、事件、流的底层调用见 lib/wails.ts。

import { invoke, openShellSocket } from './wails'

export interface ShellInfo {
  /** 壳的版本（和 web/package.json 的 version 相同）。 */
  version: string
  /** 已配置的服务器地址（origin，例如 `http://192.168.1.10:9080`）；还没配置时为 null。 */
  server_url: string | null
  /** 本机数据目录（设置、Cookie 罐、日志）。 */
  data_dir: string
  /** `windows`、`macos`…… */
  platform: string
  /** 是否在跑 `--smoke` 自检。 */
  smoke: boolean
}

/** 服务器探测结果（`GET /api/health` + `GET /api/shell/latest`）。 */
export interface ProbeResult {
  server_url: string
  reachable: boolean
  /** 连不上时给人看的原因。 */
  error: string | null
  server_version: string | null
  /** `/api/shell/latest` 返回 404（server 还没有这个接口）时三项都为 null，视为不限制版本。 */
  min_version: string | null
  latest_version: string | null
  download_url: string | null
  client_version: string
  /** 壳的版本低于 min_version。 */
  upgrade_required: boolean
}

/** unsupported：这个版本的客户端还没有这个功能（例如 M6-1 的壳还没有上传队列）。 */
export type ShellErrorKind = 'not_configured' | 'invalid_request' | 'network' | 'internal' | 'unsupported'

export interface ShellError {
  kind: ShellErrorKind
  message: string
}

export function isShellError(e: unknown): e is ShellError {
  return (
    typeof e === 'object' &&
    e !== null &&
    typeof (e as ShellError).kind === 'string' &&
    typeof (e as ShellError).message === 'string'
  )
}

/** 命令失败时给人看的一句话。 */
export function shellErrorMessage(e: unknown): string {
  if (isShellError(e)) return e.message
  if (e instanceof Error) return e.message
  return String(e)
}

export function shellInfo() {
  return invoke<ShellInfo>('shell_info')
}

/** 探测已配置的服务器。还没配置时 reject `not_configured`。 */
export function checkServer() {
  return invoke<ProbeResult>('check_server')
}

/** 探测一个还没保存的地址。格式不对时 reject `invalid_request`。 */
export function probeServer(url: string) {
  return invoke<ProbeResult>('probe_server', { url })
}

/** 保存服务器地址，返回规整后的地址。地址变了会清空本机保存的会话。 */
export function setServer(url: string) {
  return invoke<string>('set_server', { url })
}

/** 用系统浏览器打开 http(s) 地址。 */
export function openExternal(url: string) {
  return invoke<void>('open_external', { url })
}

// ---- 传输层（http.ts、stream.ts 用） ----

export interface ShellRequest {
  method: string
  /** 以 /api/ 开头，不带查询串。 */
  path: string
  query: [string, string][]
  /** 只能是 accept、content-type、if-match、if-none-match、last-event-id。 */
  headers: Record<string, string>
  body?: string
}

export interface ShellResponse {
  status: number
  /** 白名单内的响应头，名字小写。 */
  headers: Record<string, string>
  body: string
}

export function shellFetch(req: ShellRequest) {
  return invoke<ShellResponse>('api_fetch', { req })
}

export type ShellStreamEvent =
  | { event: 'open'; status: number; headers: Record<string, string> }
  | { event: 'chunk'; data: string }
  | { event: 'end' }
  | { event: 'error'; kind: ShellErrorKind; message: string }

/** 流式请求在壳里的名字（client/internal/shell/stream.go 的 apiStreamName）。 */
const API_STREAM = 'doclite.api_stream'

let nextStreamId = 1
/** 进行中的流式请求：id → 关掉它。 */
const openStreams = new Map<number, () => void>()

/**
 * 发起流式请求，返回流的 id。事件顺序：open → chunk… → end；连不上或中途断开时是一条 error。
 * 4xx、5xx 也是 open → chunk（错误体）→ end。
 *
 * 壳里是一个 Wails 流（`app.HandleStream`）：连上后先发一帧 `{req}`，壳把事件逐条发回来，发完 end 或 error
 * 就关掉。页面重载时 Wails 会关掉所有流，壳随之断开到 server 的连接。
 */
export async function shellStream(req: ShellRequest, onEvent: (e: ShellStreamEvent) => void): Promise<number> {
  const socket = await openShellSocket(API_STREAM)
  const id = nextStreamId++
  let finished = false
  const finish = () => {
    finished = true
    openStreams.delete(id)
  }
  socket.onopen = () => socket.send({ req })
  socket.onmessage = (ev) => {
    if (finished) return
    const m = ev.data as ShellStreamEvent
    if (m.event === 'end' || m.event === 'error') {
      finish()
      socket.close()
    }
    onEvent(m)
  }
  socket.onclose = () => {
    if (finished) return
    finish()
    onEvent({ event: 'error', kind: 'network', message: '和客户端的连接中断了' })
  }
  openStreams.set(id, () => {
    if (finished) return
    finish()
    socket.close()
  })
  return id
}

/** 取消流式请求：断开连接，之后不再有事件。 */
export async function shellStreamCancel(id: number): Promise<void> {
  openStreams.get(id)?.()
}

// ---- --smoke 自检 ----

export interface SmokeConfig {
  server_url: string
  username: string
  password: string
  /** 图片缓存一步（M1-11）的参数（M6-3 起用，见 client/internal/shell/smoke.go）。 */
  image_cache: {
    /** cold：先清空自检的缓存，要求全部未命中；warm：不清空，要求全部命中、不下载。 */
    mode: 'cold' | 'warm'
    /** 项目编码；null 表示第一个能进的项目。 */
    project: string | null
    /** 从项目根起的文件夹路径（/ 分隔）；null 表示按层找第一个有文件的文件夹。 */
    folder: string | null
    count: number
  }
  /** 原图分块一步（M2-3）的参数。 */
  tiles: {
    /** 项目编码；null 表示和 image_cache 一样。 */
    project: string | null
    /** 从项目根起的文件夹路径；null 表示按层找第一个同时有 G4、LZW TIFF（文件名含 g4、lzw）的文件夹。 */
    folder: string | null
    /** 实测用：指定这个文件夹里的几个文件（不按 g4、lzw 找，也不核对格式）；空数组表示按 g4、lzw 找。 */
    files: string[]
  }
  /**
   * 重扫一步（M2-7）的参数。给了 DOCLITE_SMOKE_RESCAN_PATH 才跑：自检里的 `pick_and_rescan` 不弹对话框，用那个
   * 本机文件（路径不给页面）。旧版壳没有这一项。
   */
  rescan?: {
    enabled: boolean
    /** 重扫件的文件名（不含目录）。 */
    file_name: string | null
    /** 要重扫的文件；null 表示取项目待重扫列表的第一个。 */
    file_id: string | null
    /** 项目编码；null 表示第一个能进的项目。 */
    project: string | null
    /** done：要求重扫成功；incomplete：给的是写了一半的文件，要求壳不发请求、提示不完整。 */
    expect?: 'done' | 'incomplete'
  }
  /**
   * 这个壳有哪些功能（M6-1 起）：没有的步骤跳过并记为 skipped。images：取图协议、本机缓存、切块（M6-3）；
   * upload_queue：上传队列、热文件夹、重扫（M6-2）；csp：页面不能直连服务器（M6-3）；nav_guard：导航保护装上了。
   * 旧版壳没有这一项，视为前三项都有。
   */
  capabilities?: { images: boolean; upload_queue: boolean; csp: boolean; nav_guard: boolean }
  /** 另外逐个打开账号菜单里的页面（DOCLITE_SMOKE_PAGES=1）。 */
  pages?: boolean
}

export function smokeConfig() {
  return invoke<SmokeConfig>('smoke_config')
}

/** 交回自检结果，壳随即输出并退出。 */
export function smokeReport(report: Record<string, unknown>) {
  return invoke<void>('smoke_report', { report })
}
