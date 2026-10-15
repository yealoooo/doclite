// 和 Wails 壳打交道的最底层（只在 isShell() 时调用）：命令 `invoke()`、事件 `listen()`、流 `openShellSocket()`。
// 上层（lib/shell.ts、lib/scan-queue.ts……）只用这三个，不直接碰 Wails 的运行时。
//
// - Wails 的前端运行时（npm 包 @wailsio/runtime）按需加载：浏览器里不加载（它会挂全局的右键菜单、拖动处理）。
// - 命令：壳只绑定了一个方法 `Bridge.Invoke(命令, 参数)`（client/internal/shell/bridge.go），按命令名分发，所以
//   命令名和参数沿用上一版的写法：`invoke('probe_server', { url })`。
// - 命令失败时 reject 的值是 `{kind, message}`（ShellError，见 lib/shell.ts）：壳返回的错误在 Wails 的 RuntimeError
//   的 cause 里，这里取出来；调用本身出错（运行时没加载、方法不存在）转成 kind `internal`。
// - 事件：壳用 `app.Event.Emit(名字, 数据)` 发，这里收到的是数据本身。

import type * as WailsRuntime from '@wailsio/runtime'

type Runtime = typeof WailsRuntime

let runtime: Promise<Runtime> | null = null

function loadRuntime(): Promise<Runtime> {
  runtime ??= import('@wailsio/runtime')
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
  if (isShellErrorLike(e)) return e
  const cause = (e as { cause?: unknown } | null)?.cause
  if (isShellErrorLike(cause)) return { kind: cause.kind, message: cause.message }
  return { kind: 'internal', message: e instanceof Error ? e.message : String(e) }
}

/** 调壳的命令。失败时 reject `{kind, message}`。 */
export async function invoke<T>(cmd: string, args: Record<string, unknown> = {}): Promise<T> {
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

/** 打开一个 Wails 流（Go 端 `app.HandleStream(name, …)`），收发的都是 JSON。 */
export async function openShellSocket(name: string): Promise<WailsRuntime.JSONSocket> {
  const rt = await loadRuntime()
  return rt.JSONStream(name)
}
