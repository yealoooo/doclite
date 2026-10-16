// 和桌面客户端（壳）打交道的最底层（只在 isShell() 时调用）：命令 `invoke()`、事件 `listen()`、流 `openShellSocket()`。
// 上层（lib/shell.ts、lib/scan-queue.ts……）只用这三个，不直接碰壳的运行时。约定见 client/BRIDGE.md。
//
// - 运行时由壳提供：`import('/wails/runtime.js')`（壳的资产服务器给的，和壳里的 Wails 同一个版本），web 不打包
//   Wails 的运行时，也就不跟客户端的 Wails 版本绑在一起。浏览器里不加载。
// - 壳只放行 Call.ByName（Bridge.Invoke）和取消调用（client/BRIDGE.md 的"桥的白名单"），运行时的其他对象
//   （Browser、Clipboard、Window……）在壳里一律 403，不要用。
// - 命令：壳只绑定了一个方法 `Bridge.Invoke(命令, 参数)`，按命令名分发：`invoke('probe_server', { url })`。
// - 命令失败时 reject 的值是 `{kind, message}`（ShellError，见 lib/shell.ts）：壳返回的错误在运行时的 RuntimeError
//   的 cause 里，这里取出来；调用本身出错（运行时没加载）转成 kind `internal`。客户端没有这个命令（比页面旧）时
//   kind 是 `unsupported`；页面已经知道客户端没有这项能力时（shell_info 的 capabilities）不发出去，直接 reject
//   `unsupported`（见 lib/client.ts 的 commandCapability）。
// - 事件：壳用 `app.Event.Emit(名字, 数据)` 发，这里收到的是数据本身。

import { CLIENT_TOO_OLD, commandCapability, shellCapabilitiesKnown, shellHas } from './client'

/** 壳的运行时里用到的部分（Wails v3 的 @wailsio/runtime）。 */
export interface JSONSocket {
  onopen: ((ev: Event) => unknown) | null
  onmessage: ((ev: MessageEvent) => unknown) | null
  onclose: ((ev: CloseEvent) => unknown) | null
  send(data: unknown): void
  close(code?: number, reason?: string): void
}

interface Runtime {
  Call: { ByName(name: string, ...args: unknown[]): Promise<unknown> }
  Events: { On(name: string, cb: (ev: { name: string; data: unknown }) => void): () => void }
  JSONStream(name: string): JSONSocket
}

/** 壳的运行时的地址（壳的资产服务器提供；截图脚本用的 devshell 也提供）。 */
const RUNTIME_URL = '/wails/runtime.js'

let runtime: Promise<Runtime> | null = null

function loadRuntime(): Promise<Runtime> {
  runtime ??= import(/* @vite-ignore */ RUNTIME_URL) as Promise<Runtime>
  return runtime
}

/** 壳绑定的方法（Go 的包路径 + 类型名 + 方法名）。 */
const INVOKE = 'doclite/client/internal/shell.Bridge.Invoke'

interface ShellErrorLike {
  kind: string
  message: string
}

function isShellErrorLike(v: unknown): v is ShellErrorLike {
  return (
    typeof v === 'object' &&
    v !== null &&
    typeof (v as ShellErrorLike).kind === 'string' &&
    typeof (v as ShellErrorLike).message === 'string'
  )
}

function toShellError(e: unknown): ShellErrorLike {
  const cause = (e as { cause?: unknown } | null)?.cause
  if (isShellErrorLike(cause)) return { kind: cause.kind, message: cause.message }
  if (isShellErrorLike(e)) return { kind: e.kind, message: e.message }
  return { kind: 'internal', message: e instanceof Error ? e.message : String(e) }
}

/** 调壳的命令。失败时 reject `{kind, message}`。 */
export async function invoke<T>(cmd: string, args: Record<string, unknown> = {}): Promise<T> {
  const cap = commandCapability(cmd)
  if (cap && shellCapabilitiesKnown() && !shellHas(cap)) {
    throw { kind: 'unsupported', message: CLIENT_TOO_OLD } satisfies ShellErrorLike
  }
  let rt: Runtime
  try {
    rt = await loadRuntime()
  } catch (e) {
    throw toShellError(e)
  }
  try {
    return (await rt.Call.ByName(INVOKE, cmd, args)) as T
  } catch (e) {
    throw toShellError(e)
  }
}

/** 订阅壳发的事件，返回取消订阅的函数。 */
export async function listen<T>(event: string, handler: (payload: T) => void): Promise<() => void> {
  const rt = await loadRuntime()
  return rt.Events.On(event, (ev) => handler(ev.data as T))
}

/** 打开一个壳的流（Go 端 `app.HandleStream(name, …)`），收发的都是 JSON。 */
export async function openShellSocket(name: string): Promise<JSONSocket> {
  const rt = await loadRuntime()
  return rt.JSONStream(name)
}
