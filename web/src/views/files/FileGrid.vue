<script setup lang="ts">
// 文件目录的主区（M1-10）：图标视图和列表视图，用 @tanstack/vue-virtual 按行虚拟化。
// - 图标视图：列数随容器宽度变化，每格大小固定，封面按 A4 比例占位；列表视图每行一条。
// - 只按 children 返回的条目渲染；图片信息（封面、规格）从 image-info store 按 file_id 取，
//   可见范围（含预渲染的几行）变化时告诉 store，由它去抖、合批、缓存。
// - 选择：单击单选、Ctrl/⌘ 加选、Shift 区间、Ctrl/⌘+A 全选、Esc 取消、方向键移动（带 Shift 扩展选择）；
//   双击或 Enter 打开（文件夹：进入；文件：由页面决定）。点空白处取消选择。
// - 整理的快捷键（M1-12）交给页面：F2 重命名、Delete（或 Backspace）删除、Ctrl/⌘+C 复制（`command` 事件）。
//   右键菜单由页面包在外面（按单元格上的 data-key 定位条目）。
// - M3-6 挂接工作台的待归类池另用几个可选的 props（不给时和原来完全一样）：`dense`（窄格子、小封面，名字前加显示
//   位置"#n"——区间选择按显示位置，用户决定 Q4）、`note`（每个文件多一行说明，例如"在 0012 第 3 页"）、
//   `draggableFiles`（文件可以拖出去：`dragstart`、`dragend` 事件，拖什么由页面决定）、`visibleOwner`（在 image-info
//   store 里登记可见范围用的名字）；可见的 file_id 变了发 `visible` 事件（页面按需取别的信息，例如归在哪个条目）。
import { useResizeObserver } from '@vueuse/core'
import { useVirtualizer } from '@tanstack/vue-virtual'
import { FolderIcon } from '@lucide/vue'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import type { FileEntry } from '@/api/types'
import FileCover from '@/components/files/FileCover.vue'
import type { Selection } from '@/composables/useSelection'
import { formatDateTime } from '@/lib/format'
import { formatBytes, formatSpec, isLowDpi, reviewStatusOf } from '@/lib/files'
import { useImageInfoStore } from '@/stores/image-info'

import type { Entry, FileItemEntry, ViewMode } from './entries'

/** 文件格子下面多显示的一行（M3-6）。`data` 原样放到这一行的元素上（截图脚本用）。 */
export interface GridNote {
  text: string
  title?: string
  tone?: 'muted' | 'info' | 'violet'
  data?: Record<string, string | number>
}

const props = defineProps<{
  entries: Entry[]
  view: ViewMode
  selection: Selection
  /** 窄格子、小封面，名字前显示位置"#n"（M3-6 待归类池）。只影响图标视图。 */
  dense?: boolean
  /** 文件格子下面多显示的一行；返回 null 不显示。 */
  note?: (file: FileEntry) => GridNote | null
  /** 文件可以拖出去（发 dragstart、dragend）。 */
  draggableFiles?: boolean
  /** 在 image-info store 里登记可见范围用的名字（同一页面有两个列表时区分），默认 files-grid。 */
  visibleOwner?: string
  /** 可见范围上下各多渲染几行，默认 3。 */
  overscan?: number
}>()

/** 整理的快捷键（M1-12）：F2 重命名、Delete 删除、Ctrl/⌘+C 复制。 */
export type GridCommand = 'rename' | 'delete' | 'copy'

const emit = defineEmits<{
  open: [entry: Entry]
  command: [command: GridCommand]
  dragstart: [entry: FileItemEntry, ev: DragEvent]
  dragend: [ev: DragEvent]
  visible: [fileIds: string[]]
}>()

const images = useImageInfoStore()
/** 在 image-info store 里登记可见范围用的名字。 */
const VISIBLE_OWNER = props.visibleOwner ?? 'files-grid'

// ---------- 尺寸 ----------

const PAD = 12
const COL_GAP = 8
/** 图标视图每格的最小宽度和每行的高度（封面 92×130 + 名字 + 状态）。 */
const CELL_MIN_W = props.dense ? 92 : 124
const ICON_ROW_H = props.dense ? 178 : 192
const LIST_ROW_H = 40

const scrollEl = ref<HTMLElement | null>(null)
const width = ref(0)
useResizeObserver(scrollEl, (entries) => {
  width.value = entries[0]?.contentRect.width ?? 0
})

const cols = computed(() =>
  props.view === 'icons'
    ? Math.max(1, Math.floor((width.value - 2 * PAD + COL_GAP) / (CELL_MIN_W + COL_GAP)))
    : 1,
)
const rowCount = computed(() => Math.ceil(props.entries.length / cols.value))
const rowHeight = computed(() => (props.view === 'icons' ? ICON_ROW_H : LIST_ROW_H))
/** 列表视图的"入库时间"列：容器够宽时才显示。 */
const showCreated = computed(() => width.value >= 680)
const listColumns = computed(
  () => `48px 30px minmax(140px,1fr) 64px 40px 136px 64px${showCreated.value ? ' 116px' : ''}`,
)

const virtualizer = useVirtualizer(
  computed(() => ({
    count: rowCount.value,
    getScrollElement: () => scrollEl.value,
    estimateSize: () => rowHeight.value,
    overscan: props.overscan ?? 3,
    paddingStart: props.view === 'icons' ? PAD : 0,
    paddingEnd: PAD,
  })),
)
watch([rowHeight, cols], () => virtualizer.value.measure())

const virtualRows = computed(() => virtualizer.value.getVirtualItems())

function rowEntries(index: number): Entry[] {
  const start = index * cols.value
  return props.entries.slice(start, start + cols.value)
}

// ---------- 可见范围 → 图片信息 ----------

const visibleFileIds = computed(() => {
  const ids: string[] = []
  for (const row of virtualRows.value) {
    for (const e of rowEntries(row.index)) if (e.kind === 'file') ids.push(e.file.file_id)
  }
  return ids
})
watch(
  visibleFileIds,
  (ids) => {
    images.setVisible(VISIBLE_OWNER, ids)
    emit('visible', ids)
  },
  { immediate: true },
)
onBeforeUnmount(() => images.clearVisible(VISIBLE_OWNER))

/** 显示位置（从 1 起，dense 时名字前的"#n"）。 */
function positionOf(entry: Entry): number {
  return (indexOfKey.value.get(entry.key) ?? 0) + 1
}

const NOTE_TONE: Record<NonNullable<GridNote['tone']>, string> = {
  muted: 'text-muted-foreground',
  info: 'text-info',
  violet: 'text-violet-700',
}

function onDragStart(entry: Entry, ev: DragEvent) {
  if (!props.draggableFiles || entry.kind !== 'file') {
    if (props.draggableFiles) ev.preventDefault()
    return
  }
  emit('dragstart', entry, ev)
}

// ---------- 选择和键盘 ----------

const indexOfKey = computed(() => new Map(props.entries.map((e, i) => [e.key, i])))

function onCellClick(entry: Entry, ev: MouseEvent) {
  props.selection.click(entry.key, { ctrl: ev.ctrlKey || ev.metaKey, shift: ev.shiftKey })
  scrollEl.value?.focus({ preventScroll: true })
}

function onBackgroundClick(ev: MouseEvent) {
  if (ev.target === ev.currentTarget && !ev.ctrlKey && !ev.metaKey && !ev.shiftKey) props.selection.clear()
}

function scrollToEntry(index: number) {
  virtualizer.value.scrollToIndex(Math.floor(index / cols.value), { align: 'auto' })
}

function onKeydown(ev: KeyboardEvent) {
  const mod = ev.ctrlKey || ev.metaKey
  if (mod && ev.key.toLowerCase() === 'a') {
    ev.preventDefault()
    props.selection.selectAll()
    return
  }
  if (ev.key === 'Escape') {
    props.selection.clear()
    return
  }
  if (ev.key === 'F2' || ev.key === 'Delete' || ev.key === 'Backspace' || (mod && ev.key.toLowerCase() === 'c')) {
    if (props.selection.count.value === 0) return
    ev.preventDefault()
    // 按住不放时的自动重复不算（否则按住 Ctrl/⌘+C 会复制很多份）。
    if (ev.repeat) return
    emit('command', ev.key === 'F2' ? 'rename' : ev.key.toLowerCase() === 'c' ? 'copy' : 'delete')
    return
  }
  const focusKey = props.selection.focus.value
  const cur = focusKey ? (indexOfKey.value.get(focusKey) ?? -1) : -1
  if (ev.key === 'Enter') {
    if (cur >= 0) emit('open', props.entries[cur]!)
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
  if (delta === undefined || props.entries.length === 0) return
  ev.preventDefault()
  const next = cur < 0 ? 0 : Math.min(props.entries.length - 1, Math.max(0, cur + delta))
  props.selection.click(props.entries[next]!.key, { shift: ev.shiftKey })
  scrollToEntry(next)
}

/** 滚到某个条目（页面定位用）。 */
function scrollToKey(key: string) {
  const i = indexOfKey.value.get(key)
  if (i !== undefined) scrollToEntry(i)
}

// 列表出来时，焦点没在别的控件上就把焦点给列表，Ctrl/⌘+A、方向键直接可用。
onMounted(() => {
  const active = document.activeElement
  if (!active || active === document.body) scrollEl.value?.focus({ preventScroll: true })
})

defineExpose({ scrollToKey })
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col">
    <!-- 列表视图的表头（不随内容滚动）。 -->
    <div
      v-if="props.view === 'list'"
      class="grid shrink-0 items-center gap-2 overflow-hidden border-b bg-muted/40 px-3 text-xs text-muted-foreground [scrollbar-gutter:stable]"
      :style="{ gridTemplateColumns: listColumns, height: '32px' }"
    >
      <span class="text-right">序号</span>
      <span />
      <span>名称</span>
      <span>审核</span>
      <span>版本</span>
      <span>规格</span>
      <span class="text-right">大小</span>
      <span v-if="showCreated">入库时间</span>
    </div>

    <div
      ref="scrollEl"
      class="min-h-0 flex-1 overflow-y-auto outline-none [scrollbar-gutter:stable]"
      tabindex="0"
      data-testid="file-scroll"
      :data-view="props.view"
      @keydown="onKeydown"
    >
      <div
        class="relative w-full"
        :style="{ height: `${virtualizer.getTotalSize()}px` }"
        @click="onBackgroundClick"
      >
        <div
          v-for="row in virtualRows"
          :key="`${props.view}-${row.index}`"
          class="absolute top-0 left-0 grid w-full"
          :class="props.view === 'icons' ? 'px-3' : ''"
          :style="{
            height: `${row.size}px`,
            transform: `translateY(${row.start}px)`,
            gridTemplateColumns: props.view === 'icons' ? `repeat(${cols}, minmax(0, 1fr))` : undefined,
            columnGap: props.view === 'icons' ? `${COL_GAP}px` : undefined,
          }"
          @click="onBackgroundClick"
        >
          <template v-for="entry in rowEntries(row.index)" :key="entry.key">
            <!-- ===== 图标视图 ===== -->
            <div
              v-if="props.view === 'icons'"
              class="flex cursor-default flex-col items-center gap-1 self-start rounded-lg px-1 pt-2 pb-1.5"
              :class="
                props.selection.isSelected(entry.key)
                  ? 'bg-info-soft shadow-[0_0_0_1px_var(--info)]'
                  : 'hover:bg-muted/70'
              "
              :data-testid="entry.kind === 'file' ? 'file-cell' : 'folder-cell'"
              :data-key="entry.key"
              :data-file-id="entry.kind === 'file' ? entry.file.file_id : undefined"
              :data-folder-id="entry.kind === 'folder' ? entry.folder.id : undefined"
              :data-position="props.dense ? positionOf(entry) : undefined"
              :aria-selected="props.selection.isSelected(entry.key)"
              :draggable="props.draggableFiles && entry.kind === 'file' ? 'true' : undefined"
              @click.stop="onCellClick(entry, $event)"
              @dblclick="emit('open', entry)"
              @dragstart="onDragStart(entry, $event)"
              @dragend="emit('dragend', $event)"
            >
              <template v-if="entry.kind === 'file'">
                <FileCover
                  :file-id="entry.file.file_id"
                  :info="images.get(entry.file.file_id)"
                  :class="props.dense ? 'w-[72px]' : 'w-[92px]'"
                  @broken="images.reportBroken(entry.file.file_id)"
                >
                  <span
                    v-if="entry.file.cataloged"
                    class="absolute top-1 left-1 rounded bg-violet-100 px-1 text-[10px] leading-4 text-violet-700"
                  >已归类</span>
                  <span
                    v-if="entry.file.version_no > 1"
                    class="absolute top-1 right-1 rounded bg-foreground/75 px-1 font-mono text-[10px] leading-4 text-background"
                  >v{{ entry.file.version_no }}</span>
                  <span
                    v-if="isLowDpi(images.get(entry.file.file_id)?.dpi)"
                    class="absolute bottom-1 left-1 rounded bg-warn-soft px-1 text-[10px] leading-4 text-warn"
                    title="DPI 低于 200"
                  >{{ images.get(entry.file.file_id)?.dpi }} dpi</span>
                </FileCover>
                <div
                  class="w-full truncate text-center text-foreground"
                  :class="props.dense ? 'text-[11px]' : 'text-xs'"
                  :title="entry.file.file_name"
                >
                  <span v-if="props.dense" class="mr-1 font-mono text-[11px] text-muted-foreground" data-testid="file-position">#{{ positionOf(entry) }}</span>{{ entry.file.file_name }}
                </div>
                <div class="flex items-center gap-1 text-[11px] text-muted-foreground">
                  <span class="size-1.5 rounded-full" :class="reviewStatusOf(entry.file.review_status).dot" />
                  {{ reviewStatusOf(entry.file.review_status).label }}
                  <span v-if="props.dense && entry.file.version_no > 1" class="font-mono">· v{{ entry.file.version_no }}</span>
                </div>
                <template v-if="props.note">
                  <div
                    v-for="n in [props.note(entry.file)]"
                    :key="n ? 'note' : 'none'"
                    class="h-4 w-full truncate text-center text-[11px] leading-4"
                    :class="n ? NOTE_TONE[n.tone ?? 'muted'] : ''"
                    :title="n?.title"
                    data-testid="file-note"
                    v-bind="n?.data ?? {}"
                  >{{ n?.text ?? '' }}</div>
                </template>
              </template>
              <template v-else>
                <div class="flex items-center justify-center" :class="props.dense ? 'h-[102px] w-[72px]' : 'h-[130px] w-[92px]'">
                  <FolderIcon class="size-16 fill-amber-300/70 text-amber-600" :stroke-width="1.25" />
                </div>
                <div class="w-full truncate text-center text-xs text-foreground" :title="entry.folder.folder_name">
                  {{ entry.folder.folder_name }}
                </div>
                <div class="truncate text-[11px] text-muted-foreground">
                  <template v-if="entry.folder.folder_count">{{ entry.folder.folder_count }} 个文件夹 · </template>
                  {{ entry.folder.file_count }} 个文件
                </div>
              </template>
            </div>

            <!-- ===== 列表视图 ===== -->
            <div
              v-else
              class="grid cursor-default items-center gap-2 border-b border-border/60 px-3 text-[13px]"
              :class="props.selection.isSelected(entry.key) ? 'bg-info-soft' : 'hover:bg-muted/60'"
              :style="{ gridTemplateColumns: listColumns, height: `${LIST_ROW_H}px` }"
              :data-testid="entry.kind === 'file' ? 'file-row' : 'folder-row'"
              :data-key="entry.key"
              :data-file-id="entry.kind === 'file' ? entry.file.file_id : undefined"
              :data-folder-id="entry.kind === 'folder' ? entry.folder.id : undefined"
              :aria-selected="props.selection.isSelected(entry.key)"
              @click.stop="onCellClick(entry, $event)"
              @dblclick="emit('open', entry)"
            >
              <template v-if="entry.kind === 'file'">
                <span class="text-right font-mono text-xs text-muted-foreground">{{ entry.file.sort_no }}</span>
                <FileCover
                  :file-id="entry.file.file_id"
                  :info="images.get(entry.file.file_id)"
                  compact
                  class="w-[22px]"
                  @broken="images.reportBroken(entry.file.file_id)"
                />
                <span class="flex min-w-0 items-center gap-1.5">
                  <span class="truncate text-foreground" :title="entry.file.file_name">{{ entry.file.file_name }}</span>
                  <span
                    v-if="entry.file.cataloged"
                    class="shrink-0 rounded bg-violet-100 px-1 text-[10px] leading-4 text-violet-700"
                  >已归类</span>
                </span>
                <span>
                  <span
                    class="inline-flex items-center gap-1 rounded-full px-1.5 text-[11px] leading-5"
                    :class="reviewStatusOf(entry.file.review_status).badge"
                    data-testid="review-status"
                  >
                    <span class="size-1.5 rounded-full" :class="reviewStatusOf(entry.file.review_status).dot" />
                    {{ reviewStatusOf(entry.file.review_status).label }}
                  </span>
                </span>
                <span class="font-mono text-xs text-muted-foreground">v{{ entry.file.version_no }}</span>
                <span
                  class="truncate text-xs"
                  :class="isLowDpi(images.get(entry.file.file_id)?.dpi) ? 'text-warn' : 'text-muted-foreground'"
                >
                  {{ images.get(entry.file.file_id) ? formatSpec(images.get(entry.file.file_id)!, false) : '' }}
                </span>
                <span class="text-right text-xs text-muted-foreground">
                  {{ images.get(entry.file.file_id) ? formatBytes(images.get(entry.file.file_id)!.file_size) : '' }}
                </span>
                <span v-if="showCreated" class="text-xs text-muted-foreground">
                  {{ formatDateTime(entry.file.created_at) }}
                </span>
              </template>
              <template v-else>
                <span />
                <FolderIcon class="size-5 fill-amber-300/70 text-amber-600" :stroke-width="1.5" />
                <span class="flex min-w-0 items-baseline gap-2">
                  <span class="truncate font-medium text-foreground" :title="entry.folder.folder_name">
                    {{ entry.folder.folder_name }}
                  </span>
                  <span v-if="entry.folder.remark" class="truncate text-xs text-muted-foreground" :title="entry.folder.remark">
                    {{ entry.folder.remark }}
                  </span>
                </span>
                <span class="text-xs text-muted-foreground">文件夹</span>
                <span />
                <span class="truncate text-xs text-muted-foreground">
                  <template v-if="entry.folder.folder_count">{{ entry.folder.folder_count }} 个文件夹 · </template>
                  {{ entry.folder.file_count }} 个文件
                </span>
                <span />
                <span v-if="showCreated" />
              </template>
            </div>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>
