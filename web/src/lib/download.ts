// 打开一个预签名的下载地址（M4-5 审查第 1 轮：导出物的下载）。
//
// - **壳里**（Wails，WebView2 / WKWebView）：壳的导航保护会拦下所有跳到外部地址的导航，`location.href = url` 什么都
//   不会发生。交给系统浏览器下载（`open_external`，能力 external），和文件目录的"下载原图"一样；客户端没有这项能力时
//   提示升级客户端（iframe 在壳里也打不开：CSP 的 frame-src 'none'）。
// - **浏览器里**（只是开发工具）：新标签页打开，和"查看原图"（lib/original.ts）一样。

import { CLIENT_TOO_OLD, isShell, shellHas } from '@/lib/client'
import { openExternal } from '@/lib/shell'

/** 核对地址只是 http、https（不让页面借此打开别的协议），返回规整后的地址；不对时抛错（`what` 是错误里的说法）。 */
export function httpHref(url: string, what = '下载地址'): string {
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    throw new Error(`${what}格式不对`)
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') throw new Error(`${what}不是 http 或 https，不能打开`)
  return parsed.href
}

/** 壳里交给系统浏览器打开（能力 external）；客户端没有这项能力时抛错。 */
export async function openInSystemBrowser(href: string) {
  if (!shellHas('external')) throw new Error(CLIENT_TOO_OLD)
  await openExternal(href)
}

/**
 * 打开下载地址。只接受 http、https，否则抛错。
 * 壳里 `openExternal` 失败时抛出（壳的错误，用 `shellErrorMessage` 转成文字）。
 */
export async function openDownload(url: string) {
  const href = httpHref(url)
  if (isShell()) await openInSystemBrowser(href)
  else window.open(href, '_blank', 'noopener,noreferrer')
}
