// 打开一个预签名的下载地址（M4-5 审查第 1 轮：导出物的下载）。
//
// - **壳里**（Wails，WebView2 / WKWebView）：壳的导航保护会拦下所有跳到外部地址的导航，`location.href = url` 什么都
//   不会发生。交给系统浏览器下载（`open_external`，能力 external），和文件目录的"下载原图"一样；客户端没有这项能力时
//   提示升级客户端（iframe 在壳里也打不开：CSP 的 frame-src 'none'）。
// - **浏览器里**：用一个隐藏的 iframe 打开。不用 `location.href`：它会触发 beforeunload（上传进行中时弹"离开此网站？"，
//   选"留下"就取消了下载），存储连不上、返回 403/404 时还会整页跳到错误页，没建完的导出会丢。
//   下载（`Content-Disposition: attachment`）不会在 iframe 里显示页面，也就不触发 iframe 的 load；触发了 load 说明 iframe
//   里显示的是一个页面：存储返回的错误（403、404 的 XML），或者浏览器的"无法访问"页。这时回调 `onFailed`。
//   检测不到的情况：存储一直不响应（既不下载也不报错，iframe 60 秒后移除，什么都不提示）。

import { CLIENT_TOO_OLD, isShell, shellHas } from '@/lib/client'
import { openExternal } from '@/lib/shell'

export type DownloadVia = 'shell' | 'iframe'

/** iframe 留多久（下载开始后就交给浏览器的下载管理了，移除 iframe 不影响下载）。 */
const IFRAME_TTL_MS = 60_000

/**
 * 打开下载地址。只接受 http、https（不让页面借此打开别的协议），否则抛错。
 * 壳里 `openExternal` 失败时抛出（壳的错误，用 `shellErrorMessage` 转成文字）。
 */
export async function openDownload(url: string, onFailed: () => void): Promise<DownloadVia> {
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    throw new Error('下载地址格式不对')
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') throw new Error('下载地址不是 http 或 https，不能打开')
  if (isShell()) {
    if (!shellHas('external')) throw new Error(CLIENT_TOO_OLD)
    await openExternal(parsed.href)
    return 'shell'
  }
  downloadInIframe(parsed.href, onFailed)
  return 'iframe'
}

function downloadInIframe(url: string, onFailed: () => void) {
  const iframe = document.createElement('iframe')
  iframe.style.display = 'none'
  iframe.setAttribute('aria-hidden', 'true')
  iframe.setAttribute('tabindex', '-1')
  iframe.dataset.testid = 'download-frame'
  let finished = false
  const remove = () => {
    if (finished) return
    finished = true
    iframe.remove()
  }
  iframe.addEventListener('load', () => {
    if (finished) return
    // 插进页面时的 about:blank（同源，读得到）不算；存储的错误页、浏览器的错误页是跨源的，读地址会抛错。
    let blank = false
    try {
      blank = iframe.contentWindow?.location.href === 'about:blank'
    } catch {
      blank = false
    }
    if (blank) return
    remove()
    onFailed()
  })
  iframe.src = url
  document.body.appendChild(iframe)
  setTimeout(remove, IFRAME_TTL_MS)
}
