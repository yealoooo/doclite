// 把元素的 title 换成和 Tooltip 组件一样的气泡。
//
// 原生的 title 提示要停一秒多才出来，样式跟系统走，在客户端的 WebView 里有时不出现。各处照常写 title，这里在
// document 上统一接管：鼠标移到带 title 的元素上时先把 title 换成空的（不让原生提示出来，祖先的也不出来），停 300 ms
// 显示气泡；移开时还是空的才放回去（期间 Vue 改了 title 就用新的，去掉了就不放回）。气泡不接收鼠标事件，按下鼠标、
// 按键、滚动时收起（title 等移开再放回，免得原生提示接着冒出来）。

const DELAY = 300
const GAP = 6
const MARGIN = 8

let bubble: HTMLDivElement | null = null
let current: HTMLElement | null = null
let saved = ''
let timer = 0

function ensureBubble(): HTMLDivElement {
  if (bubble) return bubble
  bubble = document.createElement('div')
  bubble.setAttribute('role', 'tooltip')
  bubble.className =
    'pointer-events-none fixed z-[100] hidden w-fit max-w-xs rounded-md bg-foreground px-3 py-1.5 text-xs whitespace-pre-line break-words text-background'
  document.body.appendChild(bubble)
  return bubble
}

function show() {
  if (!current?.isConnected) return
  const b = ensureBubble()
  b.textContent = saved
  b.classList.remove('hidden')
  const r = current.getBoundingClientRect()
  const w = b.offsetWidth
  const h = b.offsetHeight
  const left = Math.min(Math.max(r.left + r.width / 2 - w / 2, MARGIN), window.innerWidth - w - MARGIN)
  // 上面放得下放上面，否则放下面。
  const top = r.top - h - GAP >= MARGIN ? r.top - h - GAP : r.bottom + GAP
  b.style.left = `${left}px`
  b.style.top = `${top}px`
}

function hide() {
  window.clearTimeout(timer)
  bubble?.classList.add('hidden')
}

function release() {
  hide()
  if (current?.getAttribute('title') === '') current.setAttribute('title', saved)
  current = null
}

function onOver(e: PointerEvent) {
  const target = e.target as Element | null
  const el = target?.closest<HTMLElement>('[title]')
  // 还在当前元素里面（它的 title 是空的，closest 找到的就是它），除非移到了它里面另一个带 title 的元素上。
  if (current && el === current) return
  if (current && target && current.contains(target) && !(el && current.contains(el))) return
  if (!el) return release()
  release()
  const text = el.getAttribute('title')?.trim()
  if (!text) return
  current = el
  saved = el.getAttribute('title') ?? ''
  el.setAttribute('title', '')
  timer = window.setTimeout(show, DELAY)
}

function onOut(e: PointerEvent) {
  if (!current) return
  const to = e.relatedTarget as Node | null
  if (to && current.contains(to)) return
  release()
}

export function installTitleTips() {
  document.addEventListener('pointerover', onOver, true)
  document.addEventListener('pointerout', onOut, true)
  for (const type of ['pointerdown', 'keydown', 'scroll']) window.addEventListener(type, hide, true)
  window.addEventListener('blur', release)
}
