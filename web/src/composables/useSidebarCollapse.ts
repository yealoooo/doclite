// 侧栏收起（M7-11）：展开是原来的样子，收起是一条窄栏（只有图标，悬停显示名字）。
// - 状态按本机记住（localStorage，读写失败时按展开、只在这一次有效）；
// - 快捷键 ⌘B（macOS）/ Ctrl+B（其他系统）切换；焦点在输入框、文本框、下拉框、可编辑区域里时不触发（留给打字）。
// 只在登录后的外框（AppLayout）里用；状态是模块级的，几个地方取到的是同一个。

import { useLocalStorage } from '@vueuse/core'
import { onBeforeUnmount, onMounted } from 'vue'

import { FLAG_SERIALIZER, isEditable } from '@/lib/utils'

const collapsed = useLocalStorage('doclite.sidebar.collapsed', false, { serializer: FLAG_SERIALIZER })

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/i.test(navigator.platform || navigator.userAgent)

/** 快捷键的写法（提示里用）。 */
export const SIDEBAR_SHORTCUT = isMac ? '⌘B' : 'Ctrl+B'

function onKeydown(e: KeyboardEvent) {
  if (e.defaultPrevented || e.repeat || e.altKey || e.shiftKey) return
  const mod = isMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey
  if (!mod || e.key.toLowerCase() !== 'b') return
  if (isEditable(e.target) || isEditable(document.activeElement)) return
  e.preventDefault()
  collapsed.value = !collapsed.value
}

export function useSidebarCollapse(opts: { shortcut?: boolean } = {}) {
  if (opts.shortcut) {
    onMounted(() => window.addEventListener('keydown', onKeydown))
    onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
  }
  function toggle() {
    collapsed.value = !collapsed.value
  }
  return { collapsed, toggle }
}
