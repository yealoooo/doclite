<script setup lang="ts">
// 条目的页序条（M3-6 挂接工作台；取代 M3-5 的只读页序区）：按页号排的封面网格，@tanstack/vue-virtual 按行虚拟化
// （一个条目最多 2000 页）。
//
// - **每页**：封面（image-info store + FileCover）、第几页、审核状态、版本号和第几轮、"定稿后有新版本"的标记；悬停看
//   文件名和所在文件夹；双击（或选中后回车）用 OriginalViewerDialog 看原图（`open` 事件，页面打开）。
// - **选择**：单击单选、Ctrl/⌘ 加选、Shift 区间、Ctrl/⌘+A 全选、Esc 取消、方向键移动（Shift 扩展）。
// - **排序**（浏览器原生拖放，用户决定 Q3）：拖选中的页（没选中的就拖它自己）放到两页之间，插入位置画一条竖线；靠近
//   上下边缘时自动滚动。**键盘**：空格"拿起"选中的页，方向键移动插入位置（↑↓ 按行），回车或空格放下，Esc 放回；
//   Alt+←/→（↑/↓）把选中的页前移、后移一页（一行）。另有"移到第 n 页"。
// - **移出**：选中后点"移出"或按 Delete。
// - **从待归类池拖进来**：池里的文件拖到两页之间（或空白处 = 末尾），`dropFiles` 事件由页面挂接。
// - 每个完成的动作发一个 `op` 事件（PageOp：挪、移出），由 ItemDetail 算出整份新列表、经 PUT 一次提交（即时提交，见
//   ItemDetail 的说明）。拿起、拖动的过程只在本地，不发请求。
// - **只读**：已定稿（不能拖、不能放、不能移出，页头写明）；正在提交（`busy`）；预览冲突后重做的结果（`preview`）。
// - 截图脚本用的属性：容器 data-testid=item-pages（data-count、data-editable、data-busy、data-preview、data-drop-gap、
//   data-picked、data-selected）；每页 data-testid=item-page（data-file-id、data-page-no、data-review-status、
//   data-version、data-round、data-new-version、data-selected、data-mark）；插入位置 data-testid=page-drop-marker。
import { GripVerticalIcon, LoaderCircleIcon, LockIcon, Trash2Icon } from '@lucide/vue'
import { useResizeObserver } from '@vueuse/core'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import type { ArchiveItemPage, ItemPageCounts, ReviewStatus } from '@/api/types'
import FileCover from '@/components/files/FileCover.vue'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useSelection } from '@/composables/useSelection'
import { REVIEW_STATUS, reviewStatusOf } from '@/lib/files'
import { useImageInfoStore } from '@/stores/image-info'

import { dragHas, dragPayload, PAGES_MIME, POOL_MIME, setDragLabel } from './drag'
import { type OpFile, type PageOp, type PageTarget, targetAtGap } from './page-ops'

/**
 * 页序条上的一页：预览冲突后的重做结果时，`mark` 标出新挂上的（new）、挪动过的（moved）和要移出的（removed：半透明，
 * 留在原位，页号不算它）。
 */
export type StripPage = ArchiveItemPage & { mark?: 'new' | 'moved' | 'removed' }

const props = defineProps<{
  itemId: string
  pages: StripPage[]
  counts: ItemPageCounts
  /** 条目已定稿：页序冻结。 */
  locked: boolean
  /** 正在提交（PUT 在路上）：不接受新的操作。 */
  busy: boolean
  /** 正在预览冲突后重做的结果（还没保存）：只读。 */
  preview: boolean
}>()

const emit = defineEmits<{
  /** 一个完成的动作（挪、移出）。 */
  op: [op: PageOp]
  /** 从待归类池拖进来的文件放在了 target。 */
  dropFiles: [files: OpFile[], target: PageTarget]
  /** 看原图。 */
  open: [fileId: string]
  /** 选中的页变了（按页序）。 */
  selection: [fileIds: string[]]
}>()

const images = useImageInfoStore()
const VISIBLE_OWNER = 'catalog-item-pages'

const PAD = 10
const GAP = 8
const CELL_W = 80
const ROW_H = 160
/** 拖到离上下边缘这么近时自动滚动。 */
const EDGE = 48

const editable = computed(() => !props.locked && !props.preview && !props.busy)
const ids = computed(() => props.pages.map((p) => p.file_id))
const indexOf = computed(() => new Map(ids.value.map((id, i) => [id, i])))
const pageById = computed(() => new Map(props.pages.map((p) => [p.file_id, p])))

// ---------- 布局和虚拟滚动 ----------

const scrollEl = ref<HTMLElement | null>(null)
const width = ref(0)
useResizeObserver(scrollEl, (entries) => {
  width.value = entries[0]?.contentRect.width ?? 0
})
const cols = computed(() => Math.max(1, Math.floor((width.value - 2 * PAD + GAP) / (CELL_W + GAP))))
const rowCount = computed(() => Math.ceil(props.pages.length / cols.value))

const virtualizer = useVirtualizer(
  computed(() => ({
    count: rowCount.value,
    getScrollElement: () => scrollEl.value,
    estimateSize: () => ROW_H,
    overscan: 2,
    paddingStart: PAD,
    paddingEnd: PAD,
  })),
)
watch(cols, () => virtualizer.value.measure())
const virtualRows = computed(() => virtualizer.value.getVirtualItems())

function rowPages(index: number): StripPage[] {
  const start = index * cols.value
  return props.pages.slice(start, start + cols.value)
}

const visibleFileIds = computed(() => virtualRows.value.flatMap((r) => rowPages(r.index).map((p) => p.file_id)))
watch(visibleFileIds, (v) => images.setVisible(VISIBLE_OWNER, v), { immediate: true })
onBeforeUnmount(() => images.clearVisible(VISIBLE_OWNER))

function scrollToIndex(i: number) {
  if (i < 0) return
  virtualizer.value.scrollToIndex(Math.floor(i / cols.value), { align: 'auto' })
}

// ---------- 选择 ----------

const selection = useSelection(() => ids.value)
const selectedIds = computed(() => ids.value.filter((id) => selection.selected.value.has(id)))
watch(ids, () => selection.prune())
watch(selectedIds, (v) => emit('selection', v), { immediate: true })

function onCellClick(p: StripPage, ev: MouseEvent) {
  selection.click(p.file_id, { ctrl: ev.ctrlKey || ev.metaKey, shift: ev.shiftKey })
  scrollEl.value?.focus({ preventScroll: true })
}

function onBackgroundClick(ev: MouseEvent) {
  if (ev.target === ev.currentTarget && !ev.ctrlKey && !ev.metaKey && !ev.shiftKey) selection.clear()
}


// ---------- 插入位置（拖放和键盘"拿起"共用） ----------

/** 拖动时的插入位置（第几个空隙：0 = 第 1 页前面，pages.length = 最后）；null 不显示。 */
const dropGap = ref<number | null>(null)
/** 键盘"拿起"了选中的页。 */
const picked = ref(false)
const pickGap = ref(0)
const markerGap = computed(() => (picked.value ? pickGap.value : dropGap.value))

const markerStyle = computed(() => {
  const g = markerGap.value
  if (g === null) return null
  const n = props.pages.length
  const c = cols.value
  let row: number
  let col: number
  if (n === 0) {
    row = 0
    col = 0
  } else if (g >= n) {
    row = Math.floor((n - 1) / c)
    col = ((n - 1) % c) + 1
  } else {
    row = Math.floor(g / c)
    col = g % c
  }
  const left = PAD + col * (CELL_W + GAP) - GAP / 2 - 1.5
  return { transform: `translate(${left}px, ${PAD + row * ROW_H}px)`, height: `${ROW_H - 12}px` }
})

/** 鼠标位置对应的空隙。 */
function gapAt(ev: DragEvent): number {
  const el = scrollEl.value
  const n = props.pages.length
  if (!el || n === 0) return 0
  const rect = el.getBoundingClientRect()
  const x = ev.clientX - rect.left
  const y = ev.clientY - rect.top + el.scrollTop - PAD
  if (y < 0) return 0
  const row = Math.floor(y / ROW_H)
  if (row >= rowCount.value) return n
  const col = Math.max(0, Math.min(cols.value, Math.round((x - PAD) / (CELL_W + GAP))))
  return Math.min(n, row * cols.value + col)
}

// ---------- 拖放 ----------

/** 正在拖的是这个条目里的这些页（变淡显示）。 */
const draggingIds = computed(() => {
  const p = dragPayload.value
  return p?.kind === 'pages' && p.itemId === props.itemId ? new Set(p.fileIds) : null
})

function onDragStart(p: StripPage, ev: DragEvent) {
  if (!editable.value || !ev.dataTransfer) {
    ev.preventDefault()
    return
  }
  if (!selection.isSelected(p.file_id)) selection.click(p.file_id)
  const moving = selectedIds.value.length ? selectedIds.value : [p.file_id]
  dragPayload.value = { kind: 'pages', itemId: props.itemId, fileIds: moving }
  ev.dataTransfer.effectAllowed = 'move'
  ev.dataTransfer.setData(PAGES_MIME, moving.join(','))
  ev.dataTransfer.setData('text/plain', moving.map((id) => pageById.value.get(id)?.file_name ?? id).join('\n'))
  setDragLabel(ev, moving.length > 1 ? `${moving.length} 页` : `第 ${p.page_no} 页`)
}

function onDragEnd() {
  dragPayload.value = null
  dropGap.value = null
}

/** 这次拖动能不能放在页序条上：这个条目里的页，或者待归类池的文件；已定稿、预览、提交中都不行。 */
function acceptable(ev: DragEvent): 'move' | 'copy' | null {
  if (props.locked || props.preview || props.busy) return null
  const p = dragPayload.value
  if (dragHas(ev, PAGES_MIME) && p?.kind === 'pages' && p.itemId === props.itemId) return 'move'
  if (dragHas(ev, POOL_MIME) && p?.kind === 'pool') return 'copy'
  return null
}

function autoScroll(ev: DragEvent) {
  const el = scrollEl.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  if (ev.clientY < rect.top + EDGE) el.scrollTop -= 14
  else if (ev.clientY > rect.bottom - EDGE) el.scrollTop += 14
}

function onDragOver(ev: DragEvent) {
  const kind = acceptable(ev)
  if (!kind) {
    if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'none'
    dropGap.value = null
    return
  }
  ev.preventDefault()
  if (ev.dataTransfer) ev.dataTransfer.dropEffect = kind
  dropGap.value = gapAt(ev)
  autoScroll(ev)
}

function onDragLeave(ev: DragEvent) {
  const to = ev.relatedTarget as Node | null
  if (to && (ev.currentTarget as HTMLElement).contains(to)) return
  dropGap.value = null
}

function onDrop(ev: DragEvent) {
  const kind = acceptable(ev)
  const gap = dropGap.value ?? gapAt(ev)
  dropGap.value = null
  const p = dragPayload.value
  dragPayload.value = null
  if (!kind || !p) return
  ev.preventDefault()
  if (p.kind === 'pages') moveTo(p.fileIds, gap)
  else emit('dropFiles', p.files, targetAtGap(ids.value, gap))
}

// ---------- 动作 ----------

// 操作的说明（冲突横幅、撤销提示里显示）用文件名，不用页号：冲突之后页号已经变了（M3-6 审查）。

/** 一组页的说明：`IMG_0003.png` / `IMG_0003.png 等 3 页`（按页序的第一页）。 */
function pageNamesText(fileIds: string[]): string {
  const names = ids.value.filter((id) => fileIds.includes(id)).map((id) => pageById.value.get(id)?.file_name ?? id)
  if (!names.length) return '0 页'
  return names.length === 1 ? names[0]! : `${names[0]} 等 ${names.length} 页`
}

function whereText(target: PageTarget): string {
  if ('at' in target) return target.at === 'end' ? '最后' : '最前面'
  if ('position' in target) return `第 ${target.position + 1} 页`
  const anchorId = 'before' in target ? target.before : target.after
  const name = pageById.value.get(anchorId)?.file_name
  if (!name) return '原来的位置'
  return 'before' in target ? ` ${name} 前面` : ` ${name} 后面`
}

/** 把这些页挪到第 gap 个空隙。没有变化（放回原处）不发事件。 */
function moveTo(fileIds: string[], gap: number) {
  const moving = new Set(fileIds)
  const target = targetAtGap(ids.value, gap, moving)
  emit('op', { kind: 'move', fileIds, target, label: `把 ${pageNamesText(fileIds)} 移到${whereText(target)}` })
}

function removeSelected() {
  if (!editable.value || !selectedIds.value.length) return
  emit('op', { kind: 'remove', fileIds: [...selectedIds.value], label: `移出 ${pageNamesText(selectedIds.value)}` })
}

/** Alt+方向键：选中的页（合在一起）前移、后移 `step` 个位置。 */
function stepMove(step: number) {
  const sel = selectedIds.value
  if (!editable.value || !sel.length) return
  const first = indexOf.value.get(sel[0]!) ?? 0
  const last = indexOf.value.get(sel[sel.length - 1]!) ?? 0
  const gap = step < 0 ? Math.max(0, first + step) : Math.min(ids.value.length, last + 1 + step)
  if (step < 0 && first === 0) return
  if (step > 0 && last === ids.value.length - 1) return
  moveTo(sel, gap)
}

// "移到第 n 页"
const moveToText = ref('')
const moveToError = ref('')
function submitMoveTo() {
  const sel = selectedIds.value
  if (!editable.value || !sel.length) return
  const n = Number(moveToText.value.trim())
  if (!Number.isInteger(n) || n < 1 || n > props.pages.length) {
    moveToError.value = `要是 1–${props.pages.length} 的整数`
    return
  }
  moveToError.value = ''
  emit('op', {
    kind: 'move',
    fileIds: [...sel],
    target: { position: n - 1 },
    label: `把 ${pageNamesText(sel)} 移到第 ${n} 页`,
  })
}
watch(moveToText, () => (moveToError.value = ''))

// ---------- 键盘 ----------

function onKeydown(ev: KeyboardEvent) {
  const mod = ev.ctrlKey || ev.metaKey
  const n = ids.value.length
  if (picked.value) {
    const step: Record<string, number> = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -cols.value, ArrowDown: cols.value }
    if (ev.key in step) {
      ev.preventDefault()
      pickGap.value = Math.max(0, Math.min(n, pickGap.value + step[ev.key]!))
      scrollToIndex(Math.min(pickGap.value, n - 1))
    } else if (ev.key === 'Home' || ev.key === 'End') {
      ev.preventDefault()
      pickGap.value = ev.key === 'Home' ? 0 : n
      scrollToIndex(Math.min(pickGap.value, n - 1))
    } else if (ev.key === 'Enter' || ev.key === ' ') {
      ev.preventDefault()
      if (ev.repeat) return
      picked.value = false
      if (editable.value) moveTo([...selectedIds.value], pickGap.value)
    } else if (ev.key === 'Escape') {
      ev.preventDefault()
      picked.value = false
    }
    return
  }
  if (mod && ev.key.toLowerCase() === 'a') {
    ev.preventDefault()
    selection.selectAll()
    return
  }
  if (ev.key === 'Escape') {
    selection.clear()
    return
  }
  if (ev.key === 'Delete' || ev.key === 'Backspace') {
    if (!selectedIds.value.length) return
    ev.preventDefault()
    if (!ev.repeat) removeSelected()
    return
  }
  if (ev.key === ' ') {
    ev.preventDefault()
    if (ev.repeat || !editable.value || !selectedIds.value.length) return
    picked.value = true
    pickGap.value = indexOf.value.get(selectedIds.value[0]!) ?? 0
    return
  }
  if (ev.altKey && ['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(ev.key)) {
    ev.preventDefault()
    if (ev.repeat) return
    const s = ev.key === 'ArrowLeft' ? -1 : ev.key === 'ArrowRight' ? 1 : ev.key === 'ArrowUp' ? -cols.value : cols.value
    stepMove(s)
    return
  }
  const focusId = selection.focus.value
  const cur = focusId ? (indexOf.value.get(focusId) ?? -1) : -1
  if (ev.key === 'Enter') {
    if (cur >= 0) emit('open', ids.value[cur]!)
    return
  }
  const step: Record<string, number> = {
    ArrowLeft: -1,
    ArrowRight: 1,
    ArrowUp: -cols.value,
    ArrowDown: cols.value,
    Home: -Infinity,
    End: Infinity,
  }
  const delta = step[ev.key]
  if (delta === undefined || n === 0) return
  ev.preventDefault()
  const next = cur < 0 ? 0 : Math.min(n - 1, Math.max(0, cur + delta))
  selection.click(ids.value[next]!, { shift: ev.shiftKey })
  scrollToIndex(next)
}

// 提交开始、预览、定稿时放下"拿起"的页。
watch(editable, (v) => {
  if (!v) {
    picked.value = false
    dropGap.value = null
  }
})

// ---------- 对外 ----------

/** 只选这些页（提交成功后选中刚挪、刚挂上的页，接着操作），滚到第一个。 */
function select(fileIds: string[]) {
  const present = fileIds.filter((id) => indexOf.value.has(id))
  selection.set(present)
  if (present.length) scrollToIndex(indexOf.value.get(present[0]!) ?? 0)
}

defineExpose({
  select,
  clear: () => selection.clear(),
  focus: () => scrollEl.value?.focus({ preventScroll: true }),
})

const STATUS_ORDER: ReviewStatus[] = ['approved', 'pending', 'rejected', 'unassigned']
const newVersionCount = computed(() => props.pages.filter((p) => p.new_version_after_lock).length)
const KEY_HINT =
  '拖动页排序；空格拿起选中的页，方向键选位置，回车放下（Esc 放回）；Alt + 方向键前移、后移；Delete 移出；双击或回车看原图'
</script>

<template>
  <section
    class="flex min-h-0 flex-1 flex-col"
    data-testid="item-pages"
    :data-count="props.pages.length"
    :data-editable="editable ? 'true' : 'false'"
    :data-busy="props.busy ? 'true' : undefined"
    :data-preview="props.preview ? 'true' : undefined"
    :data-drop-gap="dropGap ?? undefined"
    :data-picked="picked ? 'true' : undefined"
    :data-selected="selectedIds.length"
  >
    <div class="flex min-h-9 shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b px-4 py-1 text-xs">
      <span class="font-medium text-foreground">页序</span>
      <span class="text-muted-foreground">
        共 {{ props.pages.length }} 页<template v-if="selectedIds.length"> · 已选 {{ selectedIds.length }} 页</template>
      </span>
      <span v-if="props.locked" class="inline-flex items-center gap-1 text-ok" data-testid="item-pages-frozen">
        <LockIcon class="size-3" />已定稿，页序冻结
      </span>
      <span v-if="props.busy" class="inline-flex items-center gap-1 text-muted-foreground">
        <LoaderCircleIcon class="size-3 animate-spin" />正在保存…
      </span>
      <template v-for="s in STATUS_ORDER" :key="s">
        <span
          v-if="props.counts[s] > 0"
          class="inline-flex items-center gap-1 rounded-full px-2 leading-5"
          :class="REVIEW_STATUS[s].badge"
          :data-testid="`item-pages-${s}`"
          :data-count="props.counts[s]"
        >{{ props.counts[s] }} {{ REVIEW_STATUS[s].label }}</span>
      </template>
      <span
        v-if="newVersionCount"
        class="inline-flex items-center rounded-full bg-info-soft px-2 leading-5 text-info"
        data-testid="item-pages-new-version"
        :data-count="newVersionCount"
        title="条目定稿之后，这些页重扫出了新版本"
      >{{ newVersionCount }} 页定稿后有新版本</span>
      <span class="flex-1" />
      <template v-if="!props.locked && !props.preview">
        <form
          v-if="selectedIds.length"
          class="flex items-center gap-1"
          data-testid="page-move-to"
          @submit.prevent="submitMoveTo"
        >
          <span class="text-muted-foreground">移到第</span>
          <input
            v-model="moveToText"
            class="h-6 w-12 rounded border border-input bg-background px-1.5 font-mono text-xs text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50 aria-invalid:border-destructive"
            inputmode="numeric"
            :aria-invalid="!!moveToError || undefined"
            :title="moveToError || `1–${props.pages.length}`"
            data-testid="page-move-to-input"
          >
          <span class="text-muted-foreground">页</span>
          <Button type="submit" size="xs" variant="outline" :disabled="!editable" data-testid="page-move-to-submit">移动</Button>
        </form>
        <Button
          v-if="selectedIds.length"
          size="xs"
          variant="ghost"
          class="text-destructive hover:text-destructive"
          :disabled="!editable"
          title="把选中的页移出这个条目（文件本身不删，回到未归类）"
          data-testid="page-remove"
          @click="removeSelected"
        >
          <Trash2Icon />移出（{{ selectedIds.length }}）
        </Button>
        <Tooltip v-else>
          <TooltipTrigger as-child>
            <span class="inline-flex cursor-help items-center gap-0.5 text-muted-foreground" data-testid="page-key-hint">
              <GripVerticalIcon class="size-3" />拖动排序 · 双击看原图
            </span>
          </TooltipTrigger>
          <TooltipContent side="top" class="max-w-80">{{ KEY_HINT }}</TooltipContent>
        </Tooltip>
      </template>
    </div>
    <p v-if="moveToError" class="shrink-0 border-b bg-destructive-soft px-4 py-1 text-xs text-destructive">移到第几页：{{ moveToError }}</p>
    <p v-if="picked" class="shrink-0 border-b bg-info-soft px-4 py-1 text-xs text-info" data-testid="page-picked-note">
      已拿起 {{ selectedIds.length }} 页：方向键选位置，回车或空格放下，Esc 放回。
    </p>
    <slot name="banner" />

    <div
      ref="scrollEl"
      class="relative min-h-0 flex-1 overflow-y-auto outline-none [scrollbar-gutter:stable] focus-visible:ring-2 focus-visible:ring-ring/40 focus-visible:ring-inset"
      :class="dropGap !== null && 'bg-info-soft/40'"
      tabindex="0"
      data-testid="item-pages-scroll"
      @keydown="onKeydown"
      @dragover="onDragOver"
      @dragleave="onDragLeave"
      @drop="onDrop"
    >
      <div
        v-if="props.pages.length === 0"
        class="m-3 flex h-[calc(100%-24px)] min-h-32 flex-col items-center justify-center gap-1 rounded-lg border-[1.5px] border-dashed text-sm text-muted-foreground"
        :class="dropGap !== null ? 'border-info text-info' : 'border-border'"
        data-testid="item-pages-empty"
      >
        <span>还没有挂接任何页</span>
        <span v-if="!props.locked" class="text-xs">把右边待归类池里的文件拖到这里，或者选好后点"挂到末尾"</span>
        <span v-else class="text-xs">挂接页之后才能定稿</span>
      </div>
      <div v-else class="relative w-full" :style="{ height: `${virtualizer.getTotalSize()}px` }" @click="onBackgroundClick">
        <div
          v-for="row in virtualRows"
          :key="row.index"
          class="absolute top-0 left-0 flex w-full"
          :style="{ height: `${row.size}px`, transform: `translateY(${row.start}px)`, gap: `${GAP}px`, paddingLeft: `${PAD}px`, paddingRight: `${PAD}px` }"
          @click="onBackgroundClick"
        >
          <div
            v-for="p in rowPages(row.index)"
            :key="p.file_id"
            class="flex shrink-0 cursor-default flex-col gap-1 self-start rounded-md p-0.5 transition-opacity"
            :class="[
              selection.isSelected(p.file_id) ? 'bg-info-soft shadow-[0_0_0_1.5px_var(--info)]' : 'hover:bg-muted/70',
              draggingIds?.has(p.file_id) && 'opacity-40',
              picked && selection.isSelected(p.file_id) && 'outline-2 outline-info outline-dashed',
              p.mark === 'new' && 'shadow-[0_0_0_1.5px_var(--ok)]',
              p.mark === 'moved' && !selection.isSelected(p.file_id) && 'shadow-[0_0_0_1.5px_var(--warn)]',
              p.mark === 'removed' && 'opacity-40',
            ]"
            :style="{ width: `${CELL_W}px` }"
            :title="`第 ${p.page_no} 页 · ${p.file_name}\n${p.folder_path}`"
            :draggable="editable"
            data-testid="item-page"
            :data-file-id="p.file_id"
            :data-page-no="p.page_no"
            :data-review-status="p.review_status"
            :data-version="p.version_no"
            :data-round="p.review_round"
            :data-new-version="p.new_version_after_lock ? 'true' : undefined"
            :data-selected="selection.isSelected(p.file_id) ? 'true' : undefined"
            :data-mark="p.mark"
            :aria-selected="selection.isSelected(p.file_id)"
            @click.stop="onCellClick(p, $event)"
            @dblclick="emit('open', p.file_id)"
            @dragstart="onDragStart(p, $event)"
            @dragend="onDragEnd"
          >
            <div class="relative">
              <FileCover
                :file-id="p.file_id"
                :info="images.get(p.file_id)"
                class="w-full rounded-md ring-1 ring-black/8"
                @broken="images.reportBroken(p.file_id)"
              />
              <span
                v-if="p.mark === 'removed'"
                class="absolute top-1 left-1 rounded bg-destructive px-1 text-[10px] leading-4 text-white"
              >要移出</span>
              <span
                v-else
                class="absolute top-1 left-1 rounded bg-background/90 px-1 font-mono text-[10px] leading-4 text-foreground shadow-xs"
              >{{ p.page_no }}</span>
              <span
                v-if="p.mark === 'new'"
                class="absolute top-1 right-1 rounded bg-ok px-1 text-[10px] leading-4 text-white"
              >新挂上</span>
              <span
                v-else-if="p.mark === 'moved'"
                class="absolute top-1 right-1 rounded bg-warn px-1 text-[10px] leading-4 text-white"
              >挪到这里</span>
              <span
                v-if="p.new_version_after_lock"
                class="absolute right-1 bottom-1 rounded bg-info px-1 text-[10px] leading-4 text-white"
                title="定稿之后这一页出了新版本（重扫）"
                data-testid="page-new-version"
              >定稿后新版本</span>
            </div>
            <div class="flex items-center gap-1 px-0.5 text-[11px] leading-4">
              <span class="size-1.5 shrink-0 rounded-full" :class="reviewStatusOf(p.review_status).dot" />
              <span class="truncate text-foreground">{{ reviewStatusOf(p.review_status).label }}</span>
              <span class="flex-1" />
              <span class="shrink-0 font-mono text-muted-foreground" data-testid="page-version">v{{ p.version_no }}</span>
            </div>
            <div class="truncate px-0.5 text-[11px] leading-4 text-muted-foreground">
              {{ p.review_round > 1 ? `第 ${p.review_round} 轮 · ` : '' }}{{ p.file_name }}
            </div>
          </div>
        </div>
        <div
          v-if="markerStyle"
          class="pointer-events-none absolute top-0 left-0 w-[3px] rounded-full bg-info shadow-[0_0_0_2px_var(--background)]"
          :style="markerStyle"
          data-testid="page-drop-marker"
          :data-gap="markerGap"
        />
      </div>
    </div>
  </section>
</template>
