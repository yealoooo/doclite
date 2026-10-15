<script setup lang="ts">
// 审核工作台的左栏列表（M2-6）：我的审核按文件夹分组，用 @tanstack/vue-virtual 虚拟滚动（分组头 30 px、每个文件
// 一行 52 px）。
// - 分组：列表按所在文件夹的 path_code、sort_no 排（M2-2），所在文件夹变了就插一个分组头，计数取 `folders`
//   （这个文件夹直接放的、分给我的：待审 / 全部）。分组头上"只看这里"把范围缩到这个文件夹（页面改 ?folder=）。
// - 点一行：看这个文件（页面的"当前文件"），同时只选中它；Ctrl/⌘ 点加选或取消、Shift 点选区间（给批量通过用），
//   这两种不换当前文件。键盘由页面统一绑定（方向键翻页等，Q3）。
// - 可见范围（含预渲染的几行）告诉 image-info store，封面经 FileCover → imageSrc()。
// - 滚到离末尾不到 LOAD_MORE_ROWS 行时要下一页（`loadMore`）。
// - 截图脚本用的属性：分组头 data-testid=review-group（data-folder-id）；文件行 data-testid=review-row（data-file-id、
//   data-status：open / done，data-result、data-review-status、data-round、data-history（不是当前结论）、
//   data-current、data-selected）。
import { useVirtualizer } from '@tanstack/vue-virtual'
import { FolderIcon, HistoryIcon } from '@lucide/vue'
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import type { MyReviewFolder, MyReviewItem } from '@/api/types'
import FileCover from '@/components/files/FileCover.vue'
import type { Selection } from '@/composables/useSelection'
import { useImageInfoStore } from '@/stores/image-info'

const props = defineProps<{
  items: MyReviewItem[]
  folders: Map<string, MyReviewFolder>
  currentId: string | null
  selection: Selection
  lowDpiThreshold: number
  hasMore: boolean
  loadingMore: boolean
  /** 加载下一页失败的原因（底部显示，带"重试"）。 */
  loadMoreError: string | null
  /** 正在提交审核的文件（行上显示"提交中"）。 */
  busyIds: ReadonlySet<string>
  /** 范围已经缩到某个文件夹时，分组头不再显示"只看这里"。 */
  scoped: boolean
}>()

const emit = defineEmits<{
  activate: [fileId: string]
  /** 滚到快到底了（页面自动加载下一页；刚失败过的会等一会儿）。 */
  loadMore: []
  /** 用户点了"重试"。 */
  retryMore: []
  scopeFolder: [folderId: string]
}>()

const images = useImageInfoStore()
const VISIBLE_OWNER = 'review-list'
const GROUP_H = 30
const ITEM_H = 52
/** 离末尾不到这么多行就加载下一页。 */
const LOAD_MORE_ROWS = 15

interface GroupRow {
  kind: 'group'
  key: string
  folderId: string
  name: string
  path: string
}
interface ItemRow {
  kind: 'item'
  key: string
  item: MyReviewItem
}
type Row = GroupRow | ItemRow

const rows = computed<Row[]>(() => {
  const out: Row[] = []
  let folder: string | null = null
  for (const item of props.items) {
    if (item.folder_id !== folder) {
      folder = item.folder_id
      const f = props.folders.get(folder)
      const path = f?.folder_path ?? item.folder_path
      out.push({
        kind: 'group',
        key: `g:${folder}:${out.length}`,
        folderId: folder,
        name: f?.folder_name ?? path.split('/').pop() ?? path,
        path,
      })
    }
    out.push({ kind: 'item', key: item.file_id, item })
  }
  return out
})

/** 各个分组的文件夹（按列表的顺序，逗号分隔；截图脚本核对分组用，列表是虚拟滚动的，看不到的分组头不在 DOM 里）。 */
const groupIds = computed(() => rows.value.flatMap((r) => (r.kind === 'group' ? [r.folderId] : [])).join(','))

const rowOfFile = computed(() => {
  const m = new Map<string, number>()
  rows.value.forEach((r, i) => {
    if (r.kind === 'item') m.set(r.key, i)
  })
  return m
})

const scrollEl = ref<HTMLElement | null>(null)

const virtualizer = useVirtualizer(
  computed(() => ({
    count: rows.value.length,
    getScrollElement: () => scrollEl.value,
    estimateSize: (i: number) => (rows.value[i]?.kind === 'group' ? GROUP_H : ITEM_H),
    getItemKey: (i: number) => rows.value[i]?.key ?? i,
    overscan: 6,
    paddingEnd: 8,
  })),
)
// 行的种类、个数变了（分组头插进来、拿掉一行）：重新量。
watch(rows, () => virtualizer.value.measure())

const virtualRows = computed(() => virtualizer.value.getVirtualItems())
/** 看得到的行（含预渲染的几行）和它们的内容。 */
const shown = computed(() =>
  virtualRows.value.flatMap((v) => {
    const row = rows.value[v.index]
    return row ? [{ v, row }] : []
  }),
)

const visibleFileIds = computed(() => shown.value.flatMap(({ row }) => (row.kind === 'item' ? [row.item.file_id] : [])))
watch(visibleFileIds, (ids) => images.setVisible(VISIBLE_OWNER, ids), { immediate: true })
onBeforeUnmount(() => images.clearVisible(VISIBLE_OWNER))

// 快到底了：要下一页。
watch(
  () => [virtualRows.value[virtualRows.value.length - 1]?.index ?? -1, rows.value.length, props.hasMore, props.loadingMore] as const,
  ([last, total, more, loading]) => {
    if (more && !loading && total > 0 && last >= total - LOAD_MORE_ROWS) emit('loadMore')
  },
)

function onRowClick(item: MyReviewItem, ev: MouseEvent) {
  const ctrl = ev.ctrlKey || ev.metaKey
  if (ctrl || ev.shiftKey) {
    props.selection.click(item.file_id, { ctrl, shift: ev.shiftKey })
    return
  }
  props.selection.click(item.file_id)
  emit('activate', item.file_id)
}

/** 滚到某个文件（键盘翻页、通过后跳到下一张时）。 */
function scrollToFile(fileId: string) {
  const i = rowOfFile.value.get(fileId)
  if (i === undefined) return
  // 第一个文件连同它的分组头一起露出来。
  const prev = rows.value[i - 1]
  virtualizer.value.scrollToIndex(prev?.kind === 'group' ? i - 1 : i, { align: 'auto' })
}

defineExpose({ scrollToFile })

function statusLabel(item: MyReviewItem): { text: string; cls: string } {
  if (item.status === 'open') return { text: '待审', cls: 'bg-warn-soft text-warn' }
  if (item.result === 'approved') return { text: '通过', cls: 'bg-ok-soft text-ok' }
  if (item.result === 'rejected') return { text: '驳回', cls: 'bg-destructive-soft text-destructive' }
  return { text: item.status, cls: 'bg-muted text-muted-foreground' }
}

function lowDpi(item: MyReviewItem): boolean {
  return item.dpi != null && item.dpi < props.lowDpiThreshold
}

function rowClass(it: MyReviewItem): string[] {
  const out = [
    it.file_id === props.currentId
      ? 'bg-info-soft'
      : props.selection.isSelected(it.file_id)
        ? 'bg-muted'
        : 'hover:bg-muted/60',
  ]
  if (!it.is_current) out.push('opacity-70')
  return out
}
</script>

<template>
  <div
    ref="scrollEl"
    class="min-h-0 flex-1 overflow-y-auto outline-none [scrollbar-gutter:stable]"
    tabindex="-1"
    data-testid="review-list"
    :data-rows="rows.length"
    :data-groups="groupIds"
  >
    <div class="relative w-full" :style="{ height: `${virtualizer.getTotalSize()}px` }">
      <div
        v-for="{ v, row } in shown"
        :key="row.key"
        class="absolute top-0 left-0 w-full"
        :style="{ height: `${v.size}px`, transform: `translateY(${v.start}px)` }"
      >
        <!-- 分组头：文件夹 -->
        <div
          v-if="row.kind === 'group'"
          class="group flex h-full items-center gap-1.5 border-b bg-muted/50 px-3 text-xs"
          data-testid="review-group"
          :data-folder-id="row.folderId"
          :title="`/${row.path}`"
        >
          <FolderIcon class="size-3.5 shrink-0 fill-amber-300/70 text-amber-600" :stroke-width="1.5" />
          <span class="min-w-0 truncate font-medium text-foreground">{{ row.name }}</span>
          <span
            v-if="props.folders.get(row.folderId)"
            class="shrink-0 font-mono text-[11px] text-muted-foreground tabular-nums"
            data-testid="review-group-counts"
            :data-open="props.folders.get(row.folderId)!.counts.open"
            :data-all="props.folders.get(row.folderId)!.counts.all"
          >待审 {{ props.folders.get(row.folderId)!.counts.open }} / {{ props.folders.get(row.folderId)!.counts.all }}</span>
          <span class="flex-1" />
          <button
            v-if="!props.scoped"
            type="button"
            class="hidden shrink-0 rounded px-1.5 leading-5 text-muted-foreground group-hover:inline hover:bg-background hover:text-foreground"
            data-testid="review-group-scope"
            @mousedown.prevent
            @click="emit('scopeFolder', row.folderId)"
          >只看这里</button>
        </div>

        <!-- 一个文件 -->
        <div
          v-else
          class="relative flex h-full cursor-default items-center gap-2.5 border-b border-border/60 pr-2.5 pl-3"
          :class="rowClass(row.item)"
          data-testid="review-row"
          :data-file-id="row.item.file_id"
          :data-status="row.item.status"
          :data-result="row.item.result ?? undefined"
          :data-review-status="row.item.review_status"
          :data-round="row.item.review_round"
          :data-history="row.item.is_current ? undefined : 'true'"
          :data-current="row.item.file_id === props.currentId ? 'true' : undefined"
          :data-selected="props.selection.isSelected(row.item.file_id) ? 'true' : undefined"
          :aria-selected="props.selection.isSelected(row.item.file_id)"
          @click="onRowClick(row.item, $event)"
        >
          <span v-if="row.item.file_id === props.currentId" class="absolute inset-y-0 left-0 w-0.5 bg-info" />
          <span
            v-if="props.selection.count.value > 1 && props.selection.isSelected(row.item.file_id)"
            class="absolute top-1.5 left-1 size-1.5 rounded-full bg-info"
          />
          <FileCover
            :file-id="row.item.file_id"
            :info="images.get(row.item.file_id)"
            compact
            class="w-[30px] shrink-0"
            @broken="images.reportBroken(row.item.file_id)"
          />
          <div class="min-w-0 flex-1">
            <div class="truncate text-[13px] text-foreground" :title="row.item.file_name">{{ row.item.file_name }}</div>
            <div class="mt-0.5 flex items-center gap-1.5 text-[11px] text-muted-foreground">
              <span class="font-mono">v{{ row.item.version_no }}</span>
              <span v-if="row.item.review_round > 1" class="rounded bg-warn-soft px-1 leading-4 text-warn">第 {{ row.item.review_round }} 轮</span>
              <span
                v-if="lowDpi(row.item)"
                class="rounded bg-warn-soft px-1 leading-4 text-warn"
                data-testid="review-row-low-dpi"
                :title="`DPI 低于 ${props.lowDpiThreshold}`"
              >{{ row.item.dpi }} dpi</span>
              <span
                v-if="!row.item.is_current"
                class="inline-flex items-center gap-0.5"
                title="这一行只是历史：文件后来被撤回或改派，现在的结论不是这一行"
              >
                <HistoryIcon class="size-3" />历史
              </span>
            </div>
          </div>
          <span v-if="props.busyIds.has(row.item.file_id)" class="shrink-0 text-[11px] text-muted-foreground">提交中…</span>
          <span v-else class="shrink-0 rounded-full px-1.5 text-[11px] leading-5" :class="statusLabel(row.item).cls">
            {{ statusLabel(row.item).text }}
          </span>
        </div>
      </div>
    </div>
    <div v-if="props.loadingMore" class="py-2 text-center text-xs text-muted-foreground" data-testid="review-list-loading-more">
      正在加载更多…
    </div>
    <div
      v-else-if="props.loadMoreError && props.hasMore"
      class="px-3 py-2 text-center text-xs text-destructive"
      data-testid="review-list-more-error"
    >
      加载更多失败：{{ props.loadMoreError }}
      <button type="button" class="ml-1 underline" @click="emit('retryMore')">重试</button>
    </div>
  </div>
</template>
