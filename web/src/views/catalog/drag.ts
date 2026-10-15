// 挂接工作台的拖放（M3-6，浏览器原生拖放，用户决定 Q3：不加依赖）：页序条里拖页重排、从待归类池拖文件进页序条。
//
// dragover 时读不到 dataTransfer 里的数据（只能看类型），所以拖的是什么记在这里；dataTransfer 只放类型标记（判断能不能
// 放下）和一段纯文本（拖到别的程序里时看得懂）。拖动结束（dragend，放下或取消都会发）时清掉。

import { shallowRef } from 'vue'

import type { OpFile } from './page-ops'

/** 从待归类池拖出来的文件。 */
export const POOL_MIME = 'application/x-doclite-pool-files'
/** 页序条里拖的页。 */
export const PAGES_MIME = 'application/x-doclite-item-pages'

export type DragPayload = { kind: 'pool'; files: OpFile[] } | { kind: 'pages'; itemId: string; fileIds: string[] }

export const dragPayload = shallowRef<DragPayload | null>(null)

/** 拖动时跟着鼠标的小标签（"3 页"、"12 个文件"）；放在屏幕外，setDragImage 截它的图。 */
export function setDragLabel(ev: DragEvent, text: string) {
  const dt = ev.dataTransfer
  if (!dt || typeof document === 'undefined') return
  const el = document.createElement('div')
  el.textContent = text
  el.style.cssText =
    'position:fixed;top:-100px;left:-100px;padding:4px 10px;border-radius:6px;font:500 12px system-ui,sans-serif;' +
    'background:#171717;color:#fff;box-shadow:0 2px 8px rgba(0,0,0,.2);white-space:nowrap;pointer-events:none'
  document.body.appendChild(el)
  try {
    dt.setDragImage(el, -12, -12)
  } catch {
    // 不支持就用浏览器默认的拖动图。
  }
  setTimeout(() => el.remove(), 0)
}

/** 这次拖动带的是不是某一类（dragover 用：看 dataTransfer 的类型，再核对 dragPayload）。 */
export function dragHas(ev: DragEvent, mime: string): boolean {
  const types = ev.dataTransfer?.types
  return !!types && Array.from(types).includes(mime)
}
