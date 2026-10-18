// 归类页的拖放（M9，浏览器原生拖放，不加依赖）：左栏（文件目录）的文件夹拖到右栏（归档目录）树上的节点或件列表，
// 松开后打开归档预览。只能拖文件夹（只能整个文件夹归档，requirements 硬性规则 4），文件不能拖。
//
// dragover 时读不到 dataTransfer 里的数据（只能看类型），所以拖的是什么记在这里；dataTransfer 只放类型标记（判断能不能
// 放下）和一段纯文本（拖到别的程序里时看得懂）。拖动结束（dragend，放下或取消都会发）时清掉。

import { shallowRef } from 'vue'

/** 从左栏拖出来的文件夹。 */
export const FOLDERS_MIME = 'application/x-doclite-folders'

export interface FolderDrag {
  ids: string[]
  names: string[]
}

export const folderDrag = shallowRef<FolderDrag | null>(null)

/** 拖动时跟着鼠标的小标签（"3 个文件夹"）；放在屏幕外，setDragImage 截它的图。 */
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

/** 这次拖动是不是从左栏拖出来的文件夹（dragover 用：看 dataTransfer 的类型，再核对 folderDrag）。 */
export function isFolderDrag(ev: DragEvent): boolean {
  const types = ev.dataTransfer?.types
  return !!folderDrag.value && !!types && Array.from(types).includes(FOLDERS_MIME)
}
