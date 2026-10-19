<script setup lang="ts">
// 导出范围选条目（M4-5）：左边归类树（CategoryTree，按需加载，和 CategoryPicker 同一套），右边选中节点里直接放的条目
// （按件号，每页 500，滚到底加载更多），勾选；换节点时已勾的保留，确认时一起加进导出范围。
// 每次打开都重新加载（不和页面共用状态）。要 catalog 菜单（导出页只在有这个菜单时打开它）。
//
// 截图脚本用的属性：data-testid=export-item-picker（data-settled、data-category-id、data-list）；条目行
// data-testid=picker-item（data-item-id、data-checked）；确认按钮 data-testid=export-item-picker-confirm。
import { LoaderCircleIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import { listArchiveItems } from '@/api/catalog'
import { errorMessage, HANDLE_ALL } from '@/api/http'
import type { ArchiveItemSummary } from '@/api/types'
import CategoryTree from '@/components/catalog/CategoryTree.vue'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useCategoryTree } from '@/composables/useCategoryTree'
import { StatusBadge } from '@/components/ui/status-badge'
import { itemStatusOf } from '@/lib/catalog'
import { itemName } from '@/lib/export'

export interface PickedItem {
  id: string
  /** 档号或"第 n 件"。 */
  label: string
  /** 题名和所在节点。 */
  sub: string
}

const props = defineProps<{
  projectId: string
  /** 已经在范围里的条目（显示为已勾、不能取消）。 */
  chosen: string[]
}>()

const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ confirm: [items: PickedItem[]] }>()

const tree = useCategoryTree()
const treeState = ref<'loading' | 'ready' | 'error'>('loading')
const settled = ref(false)
const categoryId = ref<string | null>(null)
const items = ref<ArchiveItemSummary[]>([])
const nextCursor = ref<string | null>(null)
const listState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const listError = ref('')
/** 这次勾上的（id → 显示用的信息）。 */
const picked = ref(new Map<string, PickedItem>())
const chosenSet = computed(() => new Set(props.chosen))

let openSeq = 0
watch(
  open,
  async (v) => {
    if (!v) return
    const my = ++openSeq
    treeState.value = 'loading'
    settled.value = false
    categoryId.value = null
    items.value = []
    listState.value = 'idle'
    picked.value = new Map()
    try {
      const root = await tree.init(props.projectId)
      if (my !== openSeq || !root) return
      treeState.value = 'ready'
      settled.value = true
    } catch {
      if (my === openSeq) treeState.value = 'error'
    }
  },
  { immediate: true },
)

let listSeq = 0
async function selectCategory(id: string) {
  categoryId.value = id
  const my = ++listSeq
  items.value = []
  nextCursor.value = null
  listState.value = 'loading'
  try {
    const res = await listArchiveItems(id, { limit: 500 }, HANDLE_ALL)
    if (my !== listSeq) return
    items.value = res.items
    nextCursor.value = res.next_cursor
    listState.value = 'ready'
  } catch (e) {
    if (my !== listSeq) return
    listState.value = 'error'
    listError.value = errorMessage(e)
  }
}

async function loadMore() {
  const id = categoryId.value
  const cursor = nextCursor.value
  if (!id || !cursor) return
  const my = listSeq
  try {
    const res = await listArchiveItems(id, { limit: 500, cursor }, HANDLE_ALL)
    if (my !== listSeq) return
    items.value = [...items.value, ...res.items]
    nextCursor.value = res.next_cursor
  } catch (e) {
    listError.value = errorMessage(e)
  }
}

function infoOf(it: ArchiveItemSummary): PickedItem {
  const path = categoryId.value ? tree.pathText(categoryId.value) : ''
  return { id: it.id, label: itemName(it.archive_no, it.item_no), sub: `${it.title || '（未填题名）'} · ${path}` }
}

function toggle(it: ArchiveItemSummary) {
  if (chosenSet.value.has(it.id)) return
  const next = new Map(picked.value)
  if (next.has(it.id)) next.delete(it.id)
  else next.set(it.id, infoOf(it))
  picked.value = next
}

const selectableShown = computed(() => items.value.filter((it) => !chosenSet.value.has(it.id)))
const allShownPicked = computed(() => selectableShown.value.length > 0 && selectableShown.value.every((it) => picked.value.has(it.id)))

function toggleAll() {
  const next = new Map(picked.value)
  if (allShownPicked.value) selectableShown.value.forEach((it) => next.delete(it.id))
  else selectableShown.value.forEach((it) => next.set(it.id, infoOf(it)))
  picked.value = next
}

function confirm() {
  emit('confirm', [...picked.value.values()])
  open.value = false
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent
      class="gap-4 sm:max-w-3xl"
      data-testid="export-item-picker"
      :data-settled="settled ? 'true' : undefined"
      :data-category-id="categoryId ?? undefined"
      :data-list="listState"
    >
      <DialogHeader>
        <DialogTitle>从归类树选条目</DialogTitle>
        <DialogDescription>左边选节点，右边勾选它里面直接放的条目；换节点时已勾的会保留。</DialogDescription>
      </DialogHeader>

      <div class="grid h-96 grid-cols-[minmax(0,5fr)_minmax(0,7fr)] gap-3">
        <div class="overflow-auto rounded-lg border bg-background p-1.5">
          <p v-if="treeState === 'error'" class="p-3 text-sm text-destructive">归类树加载失败，请关掉重试</p>
          <div v-else-if="treeState === 'loading'" class="flex items-center gap-2 p-3 text-xs text-muted-foreground">
            <LoaderCircleIcon class="size-3.5 animate-spin" />正在加载归类树…
          </div>
          <CategoryTree v-else :tree="tree" :selected-id="categoryId" @select="selectCategory" />
        </div>
        <div class="flex min-h-0 flex-col rounded-lg border">
          <div class="flex h-8 shrink-0 items-center gap-2 border-b px-2.5 text-xs">
            <span class="flex items-center" @click="toggleAll">
              <Checkbox :model-value="allShownPicked" :disabled="!selectableShown.length" class="pointer-events-none" aria-label="全选这个节点里的条目" />
            </span>
            <span class="min-w-0 flex-1 truncate text-muted-foreground">
              {{ categoryId ? `${tree.pathText(categoryId)} · 直接放的 ${items.length.toLocaleString()}${nextCursor ? '+' : ''} 件` : '先在左边选一个节点' }}
            </span>
          </div>
          <div class="min-h-0 flex-1 overflow-y-auto">
            <div v-if="listState === 'loading'" class="flex items-center gap-2 p-3 text-xs text-muted-foreground">
              <LoaderCircleIcon class="size-3.5 animate-spin" />正在加载条目…
            </div>
            <p v-else-if="listState === 'error'" class="p-3 text-xs text-destructive">{{ listError }}</p>
            <p v-else-if="listState === 'ready' && !items.length" class="p-3 text-xs text-muted-foreground">这个节点里没有直接放的条目（下级节点里的在各自的节点里选）。</p>
            <button
              v-for="it in items"
              :key="it.id"
              type="button"
              class="flex w-full items-center gap-2 border-b border-border/60 px-2.5 py-1.5 text-left text-xs hover:bg-muted/60"
              :class="chosenSet.has(it.id) && 'opacity-60'"
              data-testid="picker-item"
              :data-item-id="it.id"
              :data-checked="picked.has(it.id) || chosenSet.has(it.id) ? 'true' : undefined"
              @click="toggle(it)"
            >
              <Checkbox :model-value="picked.has(it.id) || chosenSet.has(it.id)" :disabled="chosenSet.has(it.id)" class="pointer-events-none" tabindex="-1" />
              <span class="min-w-0 flex-1">
                <span class="block truncate font-mono font-medium text-foreground" :title="it.archive_no ?? undefined">{{ itemName(it.archive_no, it.item_no) }}</span>
                <span class="block truncate text-2xs text-muted-foreground" :class="it.title ? '' : 'italic'" :title="it.title || undefined">{{ it.title || '（未填题名）' }}</span>
              </span>
              <span class="shrink-0 text-muted-foreground tabular-nums">{{ it.page_count }} 页</span>
              <StatusBadge :status="itemStatusOf(it.status)" />
            </button>
            <button v-if="nextCursor" type="button" class="w-full py-2 text-xs text-info hover:underline" @click="loadMore">加载更多</button>
          </div>
        </div>
      </div>

      <DialogFooter class="items-center">
        <span class="mr-auto text-xs text-muted-foreground" data-testid="export-item-picker-count" :data-count="picked.size">已勾选 {{ picked.size }} 件</span>
        <Button variant="outline" @click="open = false">取消</Button>
        <Button :disabled="!picked.size" data-testid="export-item-picker-confirm" @click="confirm">加入导出范围</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
