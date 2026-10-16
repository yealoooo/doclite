// 运行环境：桌面客户端（壳，Wails v3）还是普通浏览器；壳里还有客户端的能力（约定见 client/BRIDGE.md）。

/**
 * 是否运行在桌面客户端里。壳里的页面由壳的资产服务器给出（客户端嵌入的 web 产物）：macOS 上是 `wails://localhost`，
 * Windows 上是 `http://wails.localhost`（WebView2）。浏览器里是 web 服务的地址。
 */
export function isShell(): boolean {
  if (typeof window === 'undefined') return false
  const { protocol, hostname } = window.location
  return protocol === 'wails:' || hostname === 'wails.localhost'
}

/** 旧名字（上一版的壳是 Tauri），等同于 `isShell()`。各页面还在用它，新代码用 `isShell()`。 */
export const isTauri = isShell

/**
 * 客户端的能力（shell_info 的 capabilities）。客户端可能比网页旧：页面按能力决定显示哪些本机功能，没有的隐藏或
 * 提示升级客户端，不报错。每一项对应哪些命令、事件、地址见 client/BRIDGE.md。
 */
export type ShellCapability =
  | 'api'
  | 'server'
  | 'pages'
  | 'external'
  | 'tray'
  | 'upload'
  | 'rescan'
  | 'images'
  | 'smoke'

let capabilities: Set<string> | null = null

/** 记下客户端的能力（读到 shell_info 之后，lib/shell.ts）。 */
export function setShellCapabilities(list: readonly string[] | undefined) {
  capabilities = new Set(list ?? [])
}

/** 已经读到客户端的能力了。 */
export function shellCapabilitiesKnown(): boolean {
  return capabilities !== null
}

/** 在壳里、并且客户端有这项能力。浏览器里总是 false。 */
export function shellHas(cap: ShellCapability): boolean {
  return isShell() && capabilities !== null && capabilities.has(cap)
}

/** 命令属于哪项能力（客户端没有这项能力时 invoke 不发出去，直接 reject unsupported）。 */
export function commandCapability(cmd: string): ShellCapability | null {
  if (cmd === 'shell_info') return null
  if (cmd === 'api_fetch') return 'api'
  if (cmd === 'probe_server' || cmd === 'check_server' || cmd === 'set_server') return 'server'
  if (cmd === 'open_external') return 'external'
  if (cmd === 'tray_status') return 'tray'
  if (cmd === 'pick_and_rescan') return 'rescan'
  if (cmd === 'prepare_tiles' || cmd.startsWith('cache_')) return 'images'
  if (cmd.startsWith('upload_') || cmd.startsWith('hotfolder_') || cmd === 'pick_and_enqueue_upload') return 'upload'
  if (cmd.startsWith('smoke_')) return 'smoke'
  return null
}

/** 在壳里、但客户端没有这项能力（比界面旧）：相应功能提示升级客户端。 */
export function shellLacks(cap: ShellCapability): boolean {
  return isShell() && !shellHas(cap)
}

/** 客户端没有某项能力时给人看的话。 */
export const CLIENT_TOO_OLD = '这个版本的客户端没有这项功能，请升级客户端'

/** 看图要不要走壳（取图地址、本机缓存、切块）。壳里的 CSP 不许页面直连存储，所以壳里总是走它。 */
export function shellImages(): boolean {
  return shellHas('images')
}
