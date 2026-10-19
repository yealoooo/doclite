<script setup lang="ts">
// 导出明细（M4-5）：`GET /api/export-tasks/{id}/files`，建任务时冻结的每一页（件号、档号、题名、快照的 id、版本号、
// sha256、大小、原文件名，和文件现在的名字）。
// - 按件：每件一行（序号、档号、题名、页数、大小、用到的版本）；点一行跳到"按页"里这一件的第一页。
// - 按页：每页一行（档号、页号、文件名、版本、snapshot_id、sha256、大小）。两种都用虚拟滚动。
// - 打开后按每次 2000 页接着取，最多自动取 2 万页（大任务再点"继续加载"）；还没取完时最后一件的页数标"≥"。
// - 截图脚本用的属性：data-testid=task-files-dialog（data-task-id、data-loaded、data-total、data-items、data-state、
//   data-view）；按件的行 task-file-item（data-item-id、data-seq、data-archive-no、data-pages）；按页的行 task-file-page
//   （data-seq、data-page-no、data-file-id、data-snapshot-id、data-version、data-sha256）。
import { useVirtualizer } from '@tanstack/vue-virtual'
import { LoaderCircleIcon } from '@lucide/vue'
import { computed, nextTick, ref, watch } from 'vue'

import { listExportTaskFiles, MAX_FILES_PAGE } from '@/api/export'
import { errorMessage, HANDLE_ALL } from '@/api/http'
import type { ExportTask, ExportTaskFile } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { formatBytes } from '@/lib/files'
import { formatLabel, itemName } from '@/lib/export'

const props = defineProps<{
  task: ExportTask | null
  /** 范围的标题（列表上显示的那个）。 */
  scopeTitle: string
}>()
const open = defineModel<boolean>('open', { required: true })

/** 自动取到这么多页为止，更多的点"继续加载"。 */
const AUTO_LOAD_PAGES = 20_000

const files = ref<ExportTaskFile[]>([])
const total = ref(0)
const totalItems = ref(0)
const cursor = ref<string | null>(null)
const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const error = ref('')
const view = ref<'items' | 'pages'>('items')
let seq = 0

async function loadFrom(taskId: string, my: number, limitPages: number) {
  state.value = 'loading'
  try {
    let loadedThisRound = 0
    do {
      const res = await listExportTaskFiles(taskId, { limit: MAX_FILES_PAGE, cursor: cursor.value }, HANDLE_ALL)
      if (my !== seq) return
      files.value = files.value.concat(res.items)
      total.value = res.page_count
      totalItems.value = res.item_count
      cursor.value = res.next_cursor
      loadedThisRound += res.items.length
    } while (cursor.value && loadedThisRound < limitPages)
    state.value = 'ready'
  } catch (e) {
    if (my !== seq) return
    state.value = 'error'
    error.value = errorMessage(e)
  }
}

watch(
  [open, () => props.task?.id],
  ([v, id]) => {
    const my = ++seq
    if (!v || !id) return
    files.value = []
    cursor.value = null
    total.value = props.task?.page_count ?? 0
    totalItems.value = props.task?.item_count ?? 0
    view.value = 'items'
    void loadFrom(id, my, AUTO_LOAD_PAGES)
  },
  { immediate: true },
)

function loadRest() {
  const id = props.task?.id
  if (id && cursor.value) void loadFrom(id, seq, AUTO_LOAD_PAGES)
}

// ---------- 按件 ----------

interface ItemRow {
  seq: number
  item_id: string
  item_no: number
  archive_no: string | null
  title: string
  pages: number
  bytes: number
  versions: string
  firstIndex: number
  partial: boolean
}

const itemRows = computed<ItemRow[]>(() => {
  const rows: ItemRow[] = []
  const versions = new Map<number, Map<number, number>>()
  files.value.forEach((f, i) => {
    let r = rows[rows.length - 1]
    if (!r || r.seq !== f.seq) {
      r = { seq: f.seq, item_id: f.item_id, item_no: f.item_no, archive_no: f.archive_no, title: f.title, pages: 0, bytes: 0, versions: '', firstIndex: i, partial: false }
      rows.push(r)
      versions.set(f.seq, new Map())
    }
    r.pages++
    r.bytes += f.file_size
    const v = versions.get(f.seq)!
    v.set(f.version_no, (v.get(f.version_no) ?? 0) + 1)
  })
  for (const r of rows) {
    const v = versions.get(r.seq)!
    r.versions = [...v.entries()].sort((a, b) => a[0] - b[0]).map(([no, n]) => (v.size === 1 ? `v${no}` : `v${no}×${n}`)).join(' ')
  }
  const last = rows[rows.length - 1]
  if (last && cursor.value) last.partial = true
  return rows
})

const itemsEl = ref<HTMLElement | null>(null)
const itemsVirtual = useVirtualizer(
  computed(() => ({
    count: itemRows.value.length,
    getScrollElement: () => itemsEl.value,
    estimateSize: () => 44,
    overscan: 10,
  })),
)

// ---------- 按页 ----------

const pagesEl = ref<HTMLElement | null>(null)
const pagesVirtual = useVirtualizer(
  computed(() => ({
    count: files.value.length,
    getScrollElement: () => pagesEl.value,
    estimateSize: () => 36,
    overscan: 12,
  })),
)

async function openItem(r: ItemRow) {
  view.value = 'pages'
  await nextTick()
  pagesVirtual.value.scrollToIndex(r.firstIndex, { align: 'start' })
}

const totalBytes = computed(() => files.value.reduce((n, f) => n + f.file_size, 0))
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent
      class="flex h-[82vh] flex-col gap-3 sm:max-w-5xl"
      data-testid="task-files-dialog"
      :data-task-id="props.task?.id"
      :data-state="state"
      :data-loaded="files.length"
      :data-total="total"
      :data-items="itemRows.length"
      :data-view="view"
    >
      <DialogHeader>
        <DialogTitle>明细：{{ props.scopeTitle }}</DialogTitle>
        <DialogDescription>
          {{ formatLabel(props.task?.export_format ?? '') }} · {{ totalItems.toLocaleString() }} 件 · {{ total.toLocaleString() }} 页。
          建任务时冻结：每页用的是那一刻的最新版本，之后的重扫、改页序、移出不影响这次导出（会出现在"交付后变更"里）。
        </DialogDescription>
      </DialogHeader>

      <div class="flex shrink-0 items-center gap-2 text-xs">
        <div class="inline-flex rounded-md border p-0.5" role="tablist">
          <button
            type="button"
            class="rounded px-2.5 py-1"
            :class="view === 'items' ? 'bg-muted font-medium text-foreground' : 'text-muted-foreground hover:text-foreground'"
            data-testid="task-files-view-items"
            @click="view = 'items'"
          >按件</button>
          <button
            type="button"
            class="rounded px-2.5 py-1"
            :class="view === 'pages' ? 'bg-muted font-medium text-foreground' : 'text-muted-foreground hover:text-foreground'"
            data-testid="task-files-view-pages"
            @click="view = 'pages'"
          >按页</button>
        </div>
        <span class="text-muted-foreground tabular-nums">
          已加载 {{ files.length.toLocaleString() }} / {{ total.toLocaleString() }} 页 · 原图共 {{ formatBytes(totalBytes) }}
        </span>
        <LoaderCircleIcon v-if="state === 'loading'" class="size-3.5 animate-spin text-muted-foreground" />
        <Button v-if="state === 'ready' && cursor" size="xs" variant="outline" data-testid="task-files-more" @click="loadRest">继续加载</Button>
        <span v-if="state === 'error'" class="text-destructive">{{ error }}</span>
      </div>

      <!-- 按件 -->
      <div v-show="view === 'items'" class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border">
        <div class="grid shrink-0 grid-cols-[48px_minmax(0,1.3fr)_minmax(0,2fr)_72px_80px_96px] gap-3 border-b bg-muted/50 px-3 py-1.5 text-[11px] font-medium text-muted-foreground">
          <span>序号</span><span>档号</span><span>题名</span><span class="text-right">页数</span><span class="text-right">原图</span><span>版本</span>
        </div>
        <div ref="itemsEl" class="min-h-0 flex-1 overflow-y-auto">
          <div class="relative w-full" :style="{ height: `${itemsVirtual.getTotalSize()}px` }">
            <button
              v-for="v in itemsVirtual.getVirtualItems()"
              :key="itemRows[v.index]!.seq"
              type="button"
              class="absolute top-0 left-0 grid w-full grid-cols-[48px_minmax(0,1.3fr)_minmax(0,2fr)_72px_80px_96px] items-center gap-3 border-b border-border/60 px-3 text-left text-xs hover:bg-muted/60"
              :style="{ height: `${v.size}px`, transform: `translateY(${v.start}px)` }"
              title="看这一件的每一页"
              data-testid="task-file-item"
              :data-item-id="itemRows[v.index]!.item_id"
              :data-seq="itemRows[v.index]!.seq"
              :data-archive-no="itemRows[v.index]!.archive_no ?? ''"
              :data-pages="itemRows[v.index]!.pages"
              @click="openItem(itemRows[v.index]!)"
            >
              <span class="text-muted-foreground tabular-nums">{{ itemRows[v.index]!.seq }}</span>
              <span class="truncate font-mono font-medium text-foreground">{{ itemName(itemRows[v.index]!.archive_no, itemRows[v.index]!.item_no) }}</span>
              <span class="truncate text-foreground" :title="itemRows[v.index]!.title">{{ itemRows[v.index]!.title || '（未填题名）' }}</span>
              <span class="text-right text-foreground tabular-nums">{{ itemRows[v.index]!.partial ? '≥' : '' }}{{ itemRows[v.index]!.pages }}</span>
              <span class="text-right text-muted-foreground tabular-nums">{{ formatBytes(itemRows[v.index]!.bytes) }}</span>
              <span class="truncate font-mono text-[11px] text-muted-foreground">{{ itemRows[v.index]!.versions }}</span>
            </button>
          </div>
        </div>
      </div>

      <!-- 按页 -->
      <div v-show="view === 'pages'" class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border">
        <div class="grid shrink-0 grid-cols-[minmax(0,1.2fr)_48px_minmax(0,1.4fr)_40px_minmax(0,1fr)_minmax(0,1fr)_72px] gap-3 border-b bg-muted/50 px-3 py-1.5 text-[11px] font-medium text-muted-foreground">
          <span>档号</span><span class="text-right">页号</span><span>文件名</span><span>版本</span><span>snapshot_id</span><span>sha256</span><span class="text-right">大小</span>
        </div>
        <div ref="pagesEl" class="min-h-0 flex-1 overflow-y-auto">
          <div class="relative w-full" :style="{ height: `${pagesVirtual.getTotalSize()}px` }">
            <div
              v-for="v in pagesVirtual.getVirtualItems()"
              :key="`${files[v.index]!.seq}_${files[v.index]!.page_no}`"
              class="absolute top-0 left-0 grid w-full grid-cols-[minmax(0,1.2fr)_48px_minmax(0,1.4fr)_40px_minmax(0,1fr)_minmax(0,1fr)_72px] items-center gap-3 border-b border-border/60 px-3 text-xs"
              :style="{ height: `${v.size}px`, transform: `translateY(${v.start}px)` }"
              data-testid="task-file-page"
              :data-seq="files[v.index]!.seq"
              :data-page-no="files[v.index]!.page_no"
              :data-file-id="files[v.index]!.file_id"
              :data-snapshot-id="files[v.index]!.snapshot_id"
              :data-version="files[v.index]!.version_no"
              :data-sha256="files[v.index]!.content_sha256"
            >
              <span class="truncate font-mono text-foreground">{{ itemName(files[v.index]!.archive_no, files[v.index]!.item_no) }}</span>
              <span class="text-right text-foreground tabular-nums">{{ files[v.index]!.page_no }}</span>
              <span
                class="truncate text-foreground"
                :title="files[v.index]!.upload_name && files[v.index]!.upload_name !== files[v.index]!.file_name ? `原文件名：${files[v.index]!.upload_name}` : (files[v.index]!.file_name ?? undefined)"
              >{{ files[v.index]!.file_name ?? files[v.index]!.upload_name ?? '—' }}</span>
              <span class="font-mono text-muted-foreground">v{{ files[v.index]!.version_no }}</span>
              <span class="truncate font-mono text-[11px] text-muted-foreground" :title="files[v.index]!.snapshot_id">{{ files[v.index]!.snapshot_id }}</span>
              <span class="truncate font-mono text-[11px] text-muted-foreground" :title="files[v.index]!.content_sha256">{{ files[v.index]!.content_sha256 }}</span>
              <span class="text-right text-muted-foreground tabular-nums">{{ formatBytes(files[v.index]!.file_size) }}</span>
            </div>
          </div>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
