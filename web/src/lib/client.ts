// 运行环境：桌面客户端（壳，Wails v3）还是普通浏览器。

/**
 * 是否运行在桌面客户端里。壳里的页面由壳自己的资产服务器提供：macOS 上是 `wails://localhost`，
 * Windows 上是 `http://wails.localhost`（WebView2）。浏览器里是 server 的地址。
 */
export function isShell(): boolean {
  if (typeof window === 'undefined') return false
  const { protocol, hostname } = window.location
  return protocol === 'wails:' || hostname === 'wails.localhost'
}

/** 旧名字（上一版的壳是 Tauri），等同于 `isShell()`。各页面还在用它，新代码用 `isShell()`。 */
export const isTauri = isShell

/**
 * 壳的取图协议、本机图片缓存和大图切块（M6-3）是否可用。为 false 时壳里看图和浏览器一样：`<img>` 直接用
 * 预签名 URL 连存储，原图查看器走浏览器的那条路（M6-1 的壳是这样；壳的 CSP 打开后这条路走不通）。
 */
const SHELL_IMAGE_PROTOCOL = true as boolean

/** 看图要不要走壳（取图协议、缓存、切块）。 */
export function shellImages(): boolean {
  return SHELL_IMAGE_PROTOCOL && isShell()
}
