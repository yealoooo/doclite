// 侧栏收起（M7-11）：展开是原来的样子，收起是一条窄栏（只有图标，悬停显示名字）。
// - 状态按本机记住（localStorage；读写都可能抛错，例如存储被禁用，失败时按展开、只在这一次有效）；
// - 快捷键 ⌘B（macOS）/ Ctrl+B（其他系统）切换；焦点在输入框、文本框、下拉框、可编辑区域里时不触发（留给打字）。
// 只在登录后的外框（AppLayout）里用；状态是模块级的，几个地方取到的是同一个。

import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

const STORAGE_KEY = 'doclite.sidebar.collapsed'

function load(): boolean {
  try {
    return window.localStorage.getItem(STORAGE_KEY) === '1'
  } catch {
    return false
  }
}

const collapsed = ref(load())

watch(collapsed, (v) => {
  try {
    window.localStorage.setItem(STORAGE_KEY, v ? '1' : '0')
  } catch {
    // 存不下就只在这一次有效。
  }
})

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/i.test(navigator.platform || navigator.userAgent)

/** 快捷键的写法（提示里用）。 */
export const SIDEBAR_SHORTCUT = isMac ? '⌘B' : 'Ctrl+B'

/** 焦点在能打字的地方（输入框、文本框、下拉框、可编辑区域）。 */
function typingIn(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  if (target.isContentEditable) return true
  const tag = target.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT'
}

function onKeydown(e: KeyboardEvent) {
  if (e.defaultPrevented || e.repeat || e.altKey || e.shiftKey) return
  const mod = isMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey
  if (!mod || e.key.toLowerCase() !== 'b') return
  if (typingIn(e.target) || typingIn(document.activeElement)) return
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
