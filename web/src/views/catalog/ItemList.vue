<script setup lang="ts">
// 归类页的条目列表（M3-5）：当前归类节点里直接放的条目，按件号排，@tanstack/vue-virtual 虚拟滚动（每行 50 px）。
// - 每行：勾选框、件号（按模板补零）、题名（没填时"（未填题名）"）、状态、页数、"n 待审"、档号为空、定稿后有新版本；
//   列表只显示档号是否为空，原因在详情里（M3-2 须知）；
// - 点一行：打开这个条目（页面的"当前条目"），同时只勾选它；Ctrl/⌘ 点、点勾选框：加选或取消；Shift 点：选区间
//   （给批量定稿用），这几种不换当前条目；
// - 滚到离末尾不到 LOAD_MORE_ROWS 行时要下一页（`loadMore`）；
// - 批量定稿被拒时（409 `invalid_state` 的 `details.ids`），不满足条件的行标红（`blockedIds`）。
// - 截图脚本用的属性：容器 data-testid=item-list（data-rows）；行 data-testid=item-row（data-item-id、data-item-no、
//   data-status、data-archive-no、data-pending、data-pages、data-current、data-selected、data-blocked）。
import { useVirtualizer } from '@tanstack/vue-virtual'
import { computed, ref, watch } from 'vue'

import type { ArchiveItemSummary } from '@/api/types'
import { Checkbox } from '@/components/ui/checkbox'
import type { Selection } from '@/composables/useSelection'
import { formatItemNo, itemStatusOf } from '@/lib/catalog'

const props = defineProps<{
  items: ArchiveItemSummary[]
  currentId: string | null
  selection: Selection
  /** 件号补零的位数（模板的 `{item_no:0N}`）。 */
  itemNoWidth: number
  hasMore: boolean
  loadingMore: boolean
  loadMoreError: string | null
  /** 批量定稿时不满足条件的条目。 */
  blockedIds: ReadonlySet<string>
}>()

const emit = defineEmits<{
  activate: [id: string]
  loadMore: []
  retryMore: []
}>()

const ROW_H = 50
const LOAD_MORE_ROWS = 20

const scrollEl = ref<HTMLElement | null>(null)

const virtualizer = useVirtualizer(
  computed(() => ({
    count: props.items.length,
    getScrollElement: () => scrollEl.value,
    estimateSize: () => ROW_H,
    getItemKey: (i: number) => props.items[i]?.id ?? i,
    overscan: 8,
    paddingEnd: 8,
  })),
)

const virtualRows = computed(() => virtualizer.value.getVirtualItems())
const shown = computed(() =>
  virtualRows.value.flatMap((v) => {
    const item = props.items[v.index]
    return item ? [{ v, item }] : []
  }),
)

watch(
  () => [virtualRows.value[virtualRows.value.length - 1]?.index ?? -1, props.items.length, props.hasMore, props.loadingMore] as const,
  ([last, total, more, loading]) => {
    if (more && !loading && last >= total - LOAD_MORE_ROWS) emit('loadMore')
  },
)

const indexOf = computed(() => {
  const m = new Map<string, number>()
  props.items.forEach((it, i) => m.set(it.id, i))
  return m
})

function onRowClick(item: ArchiveItemSummary, ev: MouseEvent) {
  const ctrl = ev.ctrlKey || ev.metaKey
  if (ctrl || ev.shiftKey) {
    props.selection.click(item.id, { ctrl, shift: ev.shiftKey })
    return
  }
  props.selection.click(item.id)
  emit('activate', item.id)
}

function onCheck(item: ArchiveItemSummary, ev: MouseEvent) {
  ev.stopPropagation()
  props.selection.click(item.id, { ctrl: true, shift: ev.shiftKey })
}

/** 滚到某个条目（打开深处的条目、新建之后）。 */
function scrollToItem(id: string) {
  const i = indexOf.value.get(id)
  if (i !== undefined) virtualizer.value.scrollToIndex(i, { align: 'auto' })
}

defineExpose({ scrollToItem })
</script>

<template>
  <div
    ref="scrollEl"
    class="min-h-0 flex-1 overflow-y-auto outline-none [scrollbar-gutter:stable]"
    tabindex="-1"
    data-testid="item-list"
    :data-rows="props.items.length"
  >
    <div class="relative w-full" :style="{ height: `${virtualizer.getTotalSize()}px` }">
      <div
        v-for="{ v, item } in shown"
        :key="item.id"
        class="absolute top-0 left-0 w-full"
        :style="{ height: `${v.size}px`, transform: `translateY(${v.start}px)` }"
      >
        <div
          class="relative flex h-full cursor-default items-center gap-2 border-b border-border/60 pr-2.5 pl-2"
          :class="[
            item.id === props.currentId
              ? 'bg-info-soft'
              : props.selection.isSelected(item.id)
                ? 'bg-muted'
                : 'hover:bg-muted/60',
            props.blockedIds.has(item.id) && 'bg-destructive-soft/60',
          ]"
          data-testid="item-row"
          :data-item-id="item.id"
          :data-item-no="item.item_no"
          :data-status="item.status"
          :data-archive-no="item.archive_no ?? ''"
          :data-pending="item.page_counts.pending"
          :data-pages="item.page_count"
          :data-current="item.id === props.currentId ? 'true' : undefined"
          :data-selected="props.selection.isSelected(item.id) ? 'true' : undefined"
          :data-blocked="props.blockedIds.has(item.id) ? 'true' : undefined"
          :aria-selected="props.selection.isSelected(item.id)"
          @click="onRowClick(item, $event)"
        >
          <span v-if="item.id === props.currentId" class="absolute inset-y-0 left-0 w-0.5 bg-info" />
          <span class="flex shrink-0 items-center" @click="onCheck(item, $event)">
            <Checkbox
              :model-value="props.selection.isSelected(item.id)"
              :aria-label="`选择第 ${item.item_no} 件`"
              tabindex="-1"
              class="pointer-events-none"
              data-testid="item-check"
            />
          </span>
          <span
            class="w-11 shrink-0 font-mono text-xs font-semibold text-foreground tabular-nums"
            :title="item.archive_no ?? '档号为空（原因见详情）'"
          >{{ formatItemNo(item.item_no, props.itemNoWidth) }}</span>
          <div class="min-w-0 flex-1">
            <div
              class="truncate text-[13px]"
              :class="item.title ? 'text-foreground' : 'text-muted-foreground'"
              :title="item.title || undefined"
            >{{ item.title || '（未填题名）' }}</div>
            <div class="mt-0.5 flex items-center gap-1.5 overflow-hidden text-[11px] whitespace-nowrap text-muted-foreground">
              <span class="tabular-nums">{{ item.page_count }} 页</span>
              <span
                v-if="item.page_counts.pending > 0"
                class="rounded bg-warn-soft px-1 leading-4 text-warn"
                data-testid="item-row-pending"
              >{{ item.page_counts.pending }} 待审</span>
              <span
                v-if="item.page_counts.rejected > 0"
                class="rounded bg-destructive-soft px-1 leading-4 text-destructive"
              >{{ item.page_counts.rejected }} 驳回</span>
              <span
                v-if="!item.archive_no"
                class="rounded bg-warn-soft px-1 leading-4 text-warn"
                data-testid="item-row-no-archive-no"
              >档号为空</span>
              <span
                v-if="item.new_version_after_lock"
                class="rounded bg-info-soft px-1 leading-4 text-info"
                title="定稿之后有页出了新版本（重扫）"
              >定稿后有新版本</span>
            </div>
          </div>
          <span
            class="shrink-0 rounded-full px-1.5 text-[11px] leading-5"
            :class="itemStatusOf(item.status).badge"
            data-testid="item-row-status"
          >{{ itemStatusOf(item.status).label }}</span>
        </div>
      </div>
    </div>
    <div v-if="props.loadingMore" class="py-2 text-center text-xs text-muted-foreground">正在加载更多…</div>
    <div v-else-if="props.loadMoreError && props.hasMore" class="px-3 py-2 text-center text-xs text-destructive">
      加载更多失败：{{ props.loadMoreError }}
      <button type="button" class="ml-1 underline" @click="emit('retryMore')">重试</button>
    </div>
  </div>
</template>
