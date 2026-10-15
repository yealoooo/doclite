// 审核工作台的键盘（M2-6，用户决定 Q3）：在 document 上绑一个 keydown，按下面的键位调页面给的操作。
//
//   ← → ↑ ↓   上一张、下一张
//   A         通过并跳到下一张待审
//   R         打开驳回面板；面板里 1–9 勾选第几个原因、Tab 到说明框、Enter 确认、Esc 取消
//   空格       适合窗口和 100% 切换
//   + − 0     放大、缩小、适合窗口
//   [ ]       向左、向右旋转查看（不保存）
//   C         打开或关闭对比
//   V         对比时在同一位置切换看另一版（Q2 的"另给一个快捷键"，自定）：并排时第一次按直接显示左边（上一轮）
//   Esc       退出对比；没在对比时把多选收回到当前这一个
//
// 规则：
// - 焦点在输入框里时字母键不触发（驳回说明框里只认 Esc 取消、Enter 确认，Shift+Enter 换行）；
// - 按住不放的自动重复（e.repeat）一律忽略（按住 A 只通过一张）；按键本身照样 preventDefault（按住空格不滚动页面）；
// - 输入法组字时的按键（isComposing、keyCode 229）不算；带 Ctrl/⌘/Alt 的不处理（留给浏览器和系统）；
// - 别的对话框、弹出层开着时不处理；
// - 处理之前先让焦点离开按钮、勾选框，免得空格、回车松开时再"点"一次它。
// 面板打开时只认面板的键和查看器的键（空格、缩放、旋转、C、V），翻页、A、R 不认。

import { useEventListener } from '@vueuse/core'

export interface RejectPanelKeys {
  isOpen(): boolean
  /** 这个元素是不是驳回说明框。 */
  isComment(el: EventTarget | null): boolean
  cancel(): void
  confirm(): void
  focusComment(): void
  /** 勾选或取消第 index 个原因（从 0 数）。 */
  toggleIndex(index: number): void
}

export interface ViewerKeys {
  toggleFit(): void
  zoomIn(): void
  zoomOut(): void
  fit(): void
  rotateLeft(): void
  rotateRight(): void
}

export interface ReviewKeyHandlers {
  /** 现在要不要处理（例如还没有项目时不处理）。 */
  enabled(): boolean
  panel(): RejectPanelKeys | null
  viewer(): ViewerKeys | null
  step(delta: number): void
  approve(): void
  openReject(): void
  toggleCompare(): void
  toggleSide(): void
  /** Esc（面板没开时）要做的事（退出对比、收回多选）；没有要做的返回 null（不拦截）。 */
  escape(): (() => void) | null
}

/** 焦点在可以输入文字的地方（输入框、文本框、下拉框、可编辑区域）。 */
export function isEditable(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false
  if (el.isContentEditable) return true
  const tag = el.tagName
  if (tag === 'TEXTAREA' || tag === 'SELECT') return true
  if (tag === 'INPUT') {
    const type = (el as HTMLInputElement).type
    return !['checkbox', 'radio', 'button', 'submit', 'reset', 'range', 'color', 'file'].includes(type)
  }
  return false
}

/** 按 Enter、空格会"点"它的元素（按钮、链接、勾选框）。 */
function isActivatable(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false
  if (el.tagName === 'BUTTON' || el.tagName === 'A' || el.getAttribute('role') === 'button') return true
  return el.tagName === 'INPUT' && ['checkbox', 'radio', 'button', 'submit', 'reset'].includes((el as HTMLInputElement).type)
}

/** 别的对话框、弹出层、菜单开着（确认框、项目切换器……）。 */
function overlayOpen(): boolean {
  return !!document.querySelector(
    '[role=dialog][data-state=open], [role=alertdialog][data-state=open], [role=menu][data-state=open], [role=listbox][data-state=open]',
  )
}

export function useReviewKeys(h: ReviewKeyHandlers) {
  /** 这个键对应的操作（没有时返回 null，不拦截）。 */
  function shortcut(e: KeyboardEvent): (() => void) | null {
    const key = e.key
    const code = e.code
    // 字母键按字符认（大小写都行）；字符不是拉丁字母时（个别输入法开着）按物理键位认。
    const letter = /^[a-z]$/i.test(key) ? key.toLowerCase() : /^Key[A-Z]$/.test(code) ? code.slice(3).toLowerCase() : ''
    const viewerKeys = (): (() => void) | null => {
      const v = h.viewer()
      if (key === ' ' || code === 'Space') return () => v?.toggleFit()
      if (key === '+' || key === '=' || code === 'NumpadAdd') return () => v?.zoomIn()
      if (key === '-' || key === '_' || code === 'NumpadSubtract') return () => v?.zoomOut()
      if (key === '0' || code === 'Numpad0') return () => v?.fit()
      if (key === '[' || code === 'BracketLeft') return () => v?.rotateLeft()
      if (key === ']' || code === 'BracketRight') return () => v?.rotateRight()
      if (letter === 'c') return () => h.toggleCompare()
      if (letter === 'v') return () => h.toggleSide()
      return null
    }
    const panel = h.panel()
    if (panel?.isOpen()) {
      if (key === 'Escape') return () => panel.cancel()
      if (key === 'Enter') return () => panel.confirm()
      if (key === 'Tab' && !e.shiftKey) return () => panel.focusComment()
      if (/^[1-9]$/.test(key)) return () => panel.toggleIndex(Number(key) - 1)
      return viewerKeys()
    }
    switch (key) {
      case 'ArrowLeft':
      case 'ArrowUp':
        return () => h.step(-1)
      case 'ArrowRight':
      case 'ArrowDown':
        return () => h.step(1)
      case 'Escape':
        return h.escape()
    }
    if (letter === 'a') return () => h.approve()
    if (letter === 'r') return () => h.openReject()
    return viewerKeys()
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.defaultPrevented || !h.enabled()) return
    // 输入法组字时的按键不算（中文输入法用 Enter 上屏）。
    if (e.isComposing || e.keyCode === 229) return
    const panel = h.panel()
    // 驳回说明框里：只认 Esc（取消）和 Enter（确认，Shift+Enter 换行）。
    if (panel?.isOpen() && panel.isComment(e.target)) {
      if (e.key === 'Escape') {
        e.preventDefault()
        if (!e.repeat) panel.cancel()
      } else if (e.key === 'Enter' && !e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault()
        if (!e.repeat) panel.confirm()
      }
      return
    }
    if (isEditable(e.target)) return
    // 面板开着、焦点在面板里的按钮上（例如从说明框 Tab 到了"取消"）：Enter、空格交给这个按钮（审查第 1 轮：
    // 否则在"取消"上按 Enter 会变成确认驳回）。
    if (panel?.isOpen() && (e.key === 'Enter' || e.key === ' ') && isActivatable(e.target)) return
    if (e.ctrlKey || e.metaKey || e.altKey) return
    if (overlayOpen()) return
    const run = shortcut(e)
    if (!run) return
    e.preventDefault()
    // 按住不放的自动重复一律忽略（按住 A 只通过一张）。
    if (e.repeat) return
    // 焦点在按钮、勾选框上时先拿掉焦点：免得空格、回车松开时再"点"一次它（面板里的焦点留着）。
    const active = document.activeElement
    if (active instanceof HTMLElement && active !== document.body && !panel?.isOpen()) active.blur()
    run()
  }

  useEventListener(document, 'keydown', onKeydown)
}
