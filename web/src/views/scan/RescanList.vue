<script setup lang="ts">
// 待重扫列表（M2-7，/scan 页的侧栏，壳和浏览器里都显示；参考 plan.html 的 p-upload Mockup 右栏）。
//
// - 数据来自 `GET /api/projects/{pid}/rescans`（当前项目，按文件夹 path_code、sort_no 排序），`total` 做角标；
//   一页 200 个，"加载更多"跟着 next_cursor 取（按 file_id 去重）。
// - 每一项：封面（当前版本，经 image-info store 的 batch 取）、文件夹 / 文件名、项目里的位置、文件夹备注（原件位置）、
//   审核人、驳回原因和说明、第几轮和版本号，"上传重扫件"打开 RescanDialog。
// - 提示"重扫件不要扫进热文件夹"（热文件夹会把它当成新的一页）。
// - 实时：订阅当前项目的 SSE，files.changed、folders.changed、resync 和重连后去抖（300 ms，最多等 1 秒）重新取一次
//   （取回已经加载的那么多，最多 2000 个）。重扫成功后对话框通知这里马上刷新。
import { LoaderCircleIcon, RefreshCwIcon, ScanLineIcon, TriangleAlertIcon } from '@lucide/vue'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import { listRescans, RESCAN_PAGE_SIZE } from '@/api/rescans'
import type { ProjectEvent, RescanItem } from '@/api/types'
import FileCover from '@/components/files/FileCover.vue'
import RescanDialog from '@/components/files/RescanDialog.vue'
import { Button } from '@/components/ui/button'
import { useProjectStream } from '@/composables/useProjectStream'
import { type RescanTarget, targetOfRescan, withinProject } from '@/lib/rescan'
import { rejectReasonLabel } from '@/lib/reviews'
import { useImageInfoStore } from '@/stores/image-info'
import { useProjectStore } from '@/stores/project'

const project = useProjectStore()
const images = useImageInfoStore()

/** 一次重新取最多多少个（server 的上限）。 */
const MAX_RELOAD = 2000

const items = ref<RescanItem[]>([])
const total = ref<number | null>(null)
const nextCursor = ref<string | null>(null)
const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const error = ref<string | null>(null)
const loadingMore = ref(false)
let seq = 0

async function reload() {
  const pid = project.currentId
  if (!pid) return
  const my = ++seq
  if (state.value !== 'ready') state.value = 'loading'
  try {
    const limit = Math.min(MAX_RELOAD, Math.max(RESCAN_PAGE_SIZE, items.value.length))
    const res = await listRescans(pid, { limit }, true)
    if (my !== seq) return
    items.value = res.items
    total.value = res.total
    nextCursor.value = res.next_cursor
    state.value = 'ready'
    error.value = null
  } catch (e) {
    if (my !== seq) return
    error.value = e instanceof Error ? e.message : String(e)
    if (state.value !== 'ready') state.value = 'error'
  }
}

async function loadMore() {
  const pid = project.currentId
  const cursor = nextCursor.value
  if (!pid || !cursor || loadingMore.value) return
  const my = seq
  loadingMore.value = true
  try {
    const res = await listRescans(pid, { limit: RESCAN_PAGE_SIZE, cursor }, true)
    if (my !== seq) return
    // 翻页期间文件被移动时可能和前一页重复：按 file_id 去重。
    const seen = new Set(items.value.map((i) => i.file_id))
    items.value = [...items.value, ...res.items.filter((i) => !seen.has(i.file_id))]
    total.value = res.total
    nextCursor.value = res.next_cursor
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loadingMore.value = false
  }
}

watch(
  () => project.currentId,
  (pid) => {
    seq++
    items.value = []
    total.value = null
    nextCursor.value = null
    state.value = 'idle'
    if (pid) void reload()
  },
  { immediate: true },
)

watch(items, (list) => images.setVisible('rescan-list', list.map((i) => i.file_id)))

onMounted(() => {
  void project.ensureLoaded()
})

// ---------- 实时 ----------

let timer: ReturnType<typeof setTimeout> | null = null
let firstAt = 0
const changed = new Set<string>()

function schedule() {
  const now = Date.now()
  if (!firstAt) firstAt = now
  if (timer) clearTimeout(timer)
  timer = setTimeout(
    () => {
      timer = null
      firstAt = 0
      if (changed.size) images.invalidate(changed)
      changed.clear()
      void reload()
    },
    Math.min(300, Math.max(0, firstAt + 1000 - now)),
  )
}

const stream = useProjectStream(() => project.currentId, {
  onEvent: (e: ProjectEvent) => {
    if (e.type !== 'files.changed' && e.type !== 'folders.changed' && e.type !== 'resync') return
    e.file_ids?.forEach((id) => changed.add(id))
    schedule()
  },
  onOpen: (first) => {
    if (!first) schedule()
  },
})

onBeforeUnmount(() => {
  seq++
  if (timer) clearTimeout(timer)
  images.clearVisible('rescan-list')
})

// ---------- 上传重扫件 ----------

const dialogOpen = ref(false)
const target = ref<RescanTarget | null>(null)

function openRescan(it: RescanItem) {
  target.value = targetOfRescan(it)
  dialogOpen.value = true
}

const badge = computed(() => (total.value === null ? '' : total.value > 999 ? '999+' : String(total.value)))
</script>

<template>
  <aside
    class="flex w-80 shrink-0 flex-col border-l bg-sidebar/40"
    data-testid="rescan-list"
    :data-state="state"
    :data-total="total ?? ''"
    :data-stream="stream.state.value"
  >
    <div class="flex h-11 shrink-0 items-center gap-2 border-b px-4">
      <span class="text-sm font-semibold text-foreground">待重扫</span>
      <span
        v-if="badge"
        class="rounded-full px-1.5 py-0.5 text-xs font-medium tabular-nums"
        :class="total ? 'bg-destructive text-white' : 'bg-muted text-muted-foreground'"
        data-testid="rescan-total"
      >{{ badge }}</span>
      <div class="flex-1" />
      <button
        type="button"
        class="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
        title="刷新"
        data-testid="rescan-refresh"
        @click="reload"
      >
        <RefreshCwIcon class="size-3.5" :class="state === 'loading' ? 'animate-spin' : ''" />
      </button>
    </div>

    <div class="flex shrink-0 gap-1.5 border-b bg-warn-soft/60 px-4 py-2 text-xs leading-relaxed text-foreground/85" data-testid="rescan-hint">
      <TriangleAlertIcon class="mt-0.5 size-3.5 shrink-0 text-warn" />
      <span><span class="font-medium text-warn">重扫件不要扫进热文件夹</span>（会被当成新的一页）。扫到别的目录，再点"上传重扫件"单独选择。</span>
    </div>

    <div class="min-h-0 flex-1 overflow-y-auto">
      <div v-if="!project.current" class="p-4 text-sm text-muted-foreground">请先在左上角选择项目</div>
      <div v-else-if="state === 'loading' || state === 'idle'" class="flex items-center gap-2 p-4 text-sm text-muted-foreground">
        <LoaderCircleIcon class="size-4 animate-spin" />正在读取待重扫的文件…
      </div>
      <div v-else-if="state === 'error'" class="space-y-2 p-4 text-sm">
        <p class="text-destructive">{{ error ?? '读取失败' }}</p>
        <Button size="xs" variant="outline" @click="reload">重试</Button>
      </div>
      <div v-else-if="items.length === 0" class="p-4 text-sm text-muted-foreground" data-testid="rescan-empty">
        <div class="font-medium text-foreground">没有待重扫的文件</div>
        <p class="mt-1 text-xs">审核员驳回的页会出现在这里，带着原件位置和驳回原因。</p>
      </div>
      <ul v-else class="divide-y">
        <li
          v-for="it in items"
          :key="it.file_id"
          class="flex gap-3 px-4 py-3"
          data-testid="rescan-item"
          :data-file-id="it.file_id"
        >
          <FileCover :file-id="it.file_id" :info="images.get(it.file_id)" class="w-14 shrink-0 self-start" compact />
          <div class="min-w-0 flex-1 space-y-0.5 text-xs">
            <div class="truncate text-sm font-medium text-foreground" :title="`${it.folder_path}/${it.file_name}`" data-testid="rescan-item-name">
              {{ it.folder_name }} / {{ it.file_name }}
            </div>
            <div v-if="withinProject(it.folder_path)" class="truncate text-muted-foreground" :title="it.folder_path">
              {{ withinProject(it.folder_path) }}
            </div>
            <div class="truncate text-foreground/80" :title="it.folder_remark ?? ''" data-testid="rescan-item-remark">
              <template v-if="it.folder_remark">{{ it.folder_remark }}</template>
              <span v-else class="text-muted-foreground">文件夹没有备注</span>
              <template v-if="it.last_reject"> · {{ it.last_reject.reviewer_name }}</template>
            </div>
            <div class="text-destructive" data-testid="rescan-item-reasons">
              驳回：{{ it.last_reject ? it.last_reject.reasons.map(rejectReasonLabel).join('、') : '（找不到驳回记录）' }}
            </div>
            <div
              v-if="it.last_reject?.comment"
              class="line-clamp-2 text-muted-foreground"
              :title="it.last_reject.comment"
              data-testid="rescan-item-comment"
            >
              说明：{{ it.last_reject.comment }}
            </div>
            <div class="flex items-center gap-2 pt-1">
              <Button size="xs" variant="outline" data-testid="rescan-item-upload" @click="openRescan(it)">
                <ScanLineIcon />上传重扫件
              </Button>
              <span class="text-[11px] text-muted-foreground">第 {{ it.review_round }} 轮 · v{{ it.version_no }}</span>
            </div>
          </div>
        </li>
      </ul>
      <div v-if="nextCursor" class="p-3 text-center">
        <Button size="xs" variant="ghost" :disabled="loadingMore" data-testid="rescan-more" @click="loadMore">
          {{ loadingMore ? '正在加载…' : `加载更多（已显示 ${items.length} / ${total ?? '—'}）` }}
        </Button>
      </div>
    </div>

    <div class="shrink-0 border-t px-4 py-2 text-[11px] leading-relaxed text-muted-foreground">
      重扫件追加到原来的文件（新版本），文件名、位置和归类都不变；上传后自动回到驳回它的审核人那里。
    </div>

    <RescanDialog v-model:open="dialogOpen" :target="target" :project-id="project.currentId" @finished="reload" />
  </aside>
</template>
