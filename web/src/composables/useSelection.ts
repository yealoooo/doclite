// 列表的多选（M1-10）：单击单选、Ctrl/⌘ 单击加选或取消、Shift 单击区间选择（Ctrl+Shift 在已选的基础上加一段）、
// 全选、清空。条目用字符串键标识，顺序取自 `keys()`（列表当前的顺序）；列表变了用 `prune()` 去掉已经不在的。
//
//   const sel = useSelection(() => entries.value.map((e) => e.key))
//   sel.click(key, { ctrl: ev.ctrlKey || ev.metaKey, shift: ev.shiftKey })
//   sel.selected.value   // ReadonlySet<string>
//   sel.focus.value      // 最后点的那一个（详情面板显示它）

import { computed, ref, shallowRef } from 'vue'

export interface ClickModifiers {
  /** Ctrl（Windows）或 ⌘（macOS）。 */
  ctrl?: boolean
  shift?: boolean
}

export function useSelection(keys: () => readonly string[]) {
  const selected = shallowRef<ReadonlySet<string>>(new Set())
  /** 区间选择的起点（最近一次不带 Shift 点击的条目）。 */
  const anchor = ref<string | null>(null)
  /** 最后点的条目；多选时详情面板显示它。 */
  const focus = ref<string | null>(null)
  const count = computed(() => selected.value.size)

  function rangeKeys(from: string, to: string): string[] {
    const list = keys()
    let a = list.indexOf(from)
    const b = list.indexOf(to)
    if (b < 0) return []
    if (a < 0) a = b
    return list.slice(Math.min(a, b), Math.max(a, b) + 1)
  }

  function click(key: string, mods: ClickModifiers = {}) {
    if (mods.shift) {
      const range = rangeKeys(anchor.value ?? key, key)
      const next = mods.ctrl ? new Set(selected.value) : new Set<string>()
      for (const k of range) next.add(k)
      selected.value = next
      anchor.value ??= key
      focus.value = key
      return
    }
    if (mods.ctrl) {
      const next = new Set(selected.value)
      if (next.has(key)) {
        next.delete(key)
        if (focus.value === key) focus.value = null
      } else {
        next.add(key)
        focus.value = key
      }
      selected.value = next
      anchor.value = key
      return
    }
    selected.value = new Set([key])
    anchor.value = key
    focus.value = key
  }

  function selectAll() {
    const list = keys()
    selected.value = new Set(list)
    if (!anchor.value || !selected.value.has(anchor.value)) anchor.value = list[0] ?? null
  }

  function clear() {
    selected.value = new Set()
    anchor.value = null
    focus.value = null
  }

  /** 只选这几个（例如程序里定位到某个文件）。 */
  function set(list: string[]) {
    selected.value = new Set(list)
    anchor.value = list[0] ?? null
    focus.value = list[list.length - 1] ?? null
  }

  /** 列表变了：去掉已经不在列表里的。 */
  function prune() {
    const present = new Set(keys())
    let changed = false
    const next = new Set<string>()
    for (const k of selected.value) {
      if (present.has(k)) next.add(k)
      else changed = true
    }
    if (changed) selected.value = next
    if (anchor.value && !present.has(anchor.value)) anchor.value = null
    if (focus.value && !present.has(focus.value)) focus.value = null
  }

  function isSelected(key: string): boolean {
    return selected.value.has(key)
  }

  return { selected, anchor, focus, count, click, selectAll, clear, set, prune, isSelected }
}

export type Selection = ReturnType<typeof useSelection>
