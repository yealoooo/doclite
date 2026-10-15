<script setup lang="ts">
// 项目看板 /dashboard（M3-7，参考 plan.html 的 p-dash Mockup；菜单码 dashboard；用户决定 Q6）。
//
// - 数据：`GET /api/projects/{pid}/catalog-summary?folder_id=`（M3-4，每次实时聚合）。根文件夹（不传 folder_id）是
//   整个项目：KPI（KpiTiles）和"审核状态 × 归类状态"矩阵（StatusMatrix）；它的 children 是文件夹进度表的第一层，
//   点箭头逐层展开（FolderProgressTable，展开时取那个文件夹的 catalog-summary）。
// - 计数用 `useFolderSummaries`（和 /review-assign 的 useReviewCounts 同一套）：每个加载过的文件夹记下 parent 关系，
//   `files.changed` 只带文件直接所在的文件夹，沿 parent 链刷新祖先里已加载的（不认识的文件夹用
//   `GET /api/folders/{id}` 补祖先链，要 files 或 scan 菜单；没有时刷新全部已加载的）。
// - 实时（useProjectStream，去抖：最后一条事件后 300 ms，最多等 1 秒）：`files.changed` 按路径刷新；
//   `catalog.changed`（条目、挂接、定稿……不带文件夹）、`folders.changed`（文件夹移动、删除、改名）、`resync`、断线
//   重连之后刷新全部跟踪着的。只跟踪根和看得见的展开链上的文件夹（syncTracked：收起时连同下面展开的一起收起、
//   不再跟踪，再展开时重新取），所以一批事件最多取"根 + 展开着的"这么多次；一轮刷新还在跑时新到的事件等它跑完
//   再补一轮，不叠加（审查第 1 轮）。展开的文件夹被删掉（404）、移到收起的地方后自动收起。
// - 人员统计、"今天 / 本周"不做（Q6）。
// 截图脚本用的属性：data-testid=dashboard-page（data-stream、data-state：idle、loading、ready、error，
// data-updates：根文件夹的结果更新了几次）；kpi、matrix-cell、folder-row 见各组件。
import { LoaderCircleIcon, RefreshCwIcon } from '@lucide/vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { toast } from 'vue-sonner'

import { getCatalogSummary } from '@/api/catalog'
import { errorMessage } from '@/api/http'
import type { CatalogCounts, CatalogSummary, ProjectEvent } from '@/api/types'
import { Button } from '@/components/ui/button'
import { useFolderSummaries } from '@/composables/useFolderSummaries'
import { useProjectStream } from '@/composables/useProjectStream'
import { REVIEW_STATUS } from '@/lib/files'
import { useProjectStore } from '@/stores/project'
import { useSessionStore } from '@/stores/session'

import {
  CATALOG_COLOR,
  CATALOG_COLUMN_LABEL,
  catalogSegments,
  num,
  percentText,
  REVIEW_COLOR,
  reviewSegments,
} from './dashboard'
import FolderProgressTable, { type ProgressRow } from './FolderProgressTable.vue'
import KpiTiles from './KpiTiles.vue'
import StackedBar from './StackedBar.vue'
import StatusMatrix from './StatusMatrix.vue'

const project = useProjectStore()
const session = useSessionStore()
/** 按路径补祖先链（`GET /api/folders/{id}`）要 files 或 scan 菜单。 */
const canLookup = computed(() => session.menuCodes.has('files') || session.menuCodes.has('scan'))

/** 最近一次刷新根文件夹失败的说明（成功后清掉）。 */
const refreshError = ref<string | null>(null)
const rootId = computed(() => project.current?.root_folder_id ?? null)

const sums = useFolderSummaries<CatalogCounts, CatalogSummary>(
  () => project.currentId,
  async (pid, folderId) => {
    // 错误由页面自己显示（根文件夹在页头，展开失败在 toast），不弹默认提示；404 由 useFolderSummaries 处理。
    try {
      const s = await getCatalogSummary(pid, folderId, true)
      if (folderId === null) refreshError.value = null
      return s
    } catch (e) {
      if (folderId === null) refreshError.value = errorMessage(e)
      throw e
    }
  },
  { canLookup: () => canLookup.value },
)

const rootState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const rootSummary = computed(() => sums.summaryOf(rootId.value))
const counts = computed(() => rootSummary.value?.counts ?? null)
const requireLocked = computed(() => rootSummary.value?.require_locked_item ?? true)
const updatedAt = ref<Date | null>(null)
const updates = ref(0)
watch(rootSummary, (s) => {
  if (!s) return
  updatedAt.value = new Date()
  updates.value++
})

/** 展开的文件夹。 */
const expanded = reactive(new Set<string>())
let alive = true

async function loadRoot() {
  const id = rootId.value
  if (!id) return
  if (rootState.value !== 'ready') rootState.value = 'loading'
  try {
    await sums.load(id)
    if (rootId.value === id) rootState.value = 'ready'
  } catch {
    if (rootId.value === id && rootState.value !== 'ready') rootState.value = 'error'
  }
}

watch(
  () => project.current?.id ?? null,
  (pid) => {
    expanded.clear()
    refreshError.value = null
    updatedAt.value = null
    rootState.value = 'idle'
    sums.reset(rootId.value)
    if (pid) void loadRoot()
  },
  { immediate: true },
)

async function toggle(folderId: string) {
  if (expanded.has(folderId)) {
    expanded.delete(folderId)
    syncTracked()
    return
  }
  expanded.add(folderId)
  if (sums.summaryOf(folderId)) return
  try {
    const s = await sums.load(folderId)
    if (!s) {
      expanded.delete(folderId)
      toast.info('这个文件夹已经不在了（被删除或移走），已刷新')
      void sums.refreshAll()
    }
  } catch (e) {
    expanded.delete(folderId)
    sums.forget([folderId])
    toast.error(`取不到这个文件夹的进度：${errorMessage(e)}`)
  }
}

function collapseAll() {
  expanded.clear()
  syncTracked()
}

/** 看得见的展开的文件夹：从根往下，父文件夹展开着（根总是"展开"的）才看得见。 */
function visibleExpanded(): Set<string> {
  const vis = new Set<string>()
  const walk = (parentId: string) => {
    for (const c of sums.summaryOf(parentId)?.children ?? []) {
      if (!expanded.has(c.folder_id)) continue
      vis.add(c.folder_id)
      walk(c.folder_id)
    }
  }
  if (rootId.value) walk(rootId.value)
  return vis
}

/**
 * 只跟踪看得见的文件夹（审查第 1 轮：收起的文件夹原来还留在"已加载"里，每批 catalog.changed 都整批重取）：
 * 看不见的展开标记去掉（收起一层时它下面展开的一起收起；展开的文件夹被删、被移到收起的地方时也收起），
 * 不在根和看得见的展开链上的文件夹从 useFolderSummaries 里 forget，以后的刷新只取根和展开着的；再展开时重新取。
 */
function syncTracked() {
  const vis = visibleExpanded()
  for (const id of [...expanded]) if (!vis.has(id)) expanded.delete(id)
  sums.forget(sums.loadedIds().filter((id) => id !== rootId.value && !vis.has(id)))
}

const rows = computed<ProgressRow[]>(() => {
  const out: ProgressRow[] = []
  const walk = (parentId: string, depth: number) => {
    const s = sums.summaryOf(parentId)
    if (!s) return
    for (const c of s.children) {
      const own = sums.summaryOf(c.folder_id)
      const isOpen = expanded.has(c.folder_id)
      out.push({
        key: c.folder_id,
        kind: 'folder',
        folderId: c.folder_id,
        name: c.folder_name,
        depth,
        counts: sums.get(c.folder_id) ?? c.counts,
        expanded: isOpen,
        loading: isOpen && !own,
        leaf: !!own && own.children.length === 0,
      })
      if (isOpen && own) walk(c.folder_id, depth + 1)
    }
    if (s.children.length > 0 && s.direct_counts.total > 0) {
      out.push({
        key: `direct:${parentId}`,
        kind: 'direct',
        folderId: parentId,
        name: s.folder_name,
        depth,
        counts: s.direct_counts,
        expanded: false,
        loading: false,
        leaf: true,
      })
    }
  }
  if (rootId.value) walk(rootId.value, 0)
  return out
})

async function refreshNow() {
  await sums.refreshAll()
  syncTracked()
}

// ---------- 实时事件 ----------

const pendingFolders = new Set<string>()
let pendingAll = false
let pendingStructure = false
let applyTimer: ReturnType<typeof setTimeout> | null = null
let firstPendingAt = 0
const EVENT_DEBOUNCE_MS = 300
const EVENT_MAX_WAIT_MS = 1000

function scheduleApply() {
  const now = Date.now()
  if (!firstPendingAt) firstPendingAt = now
  if (applyTimer) clearTimeout(applyTimer)
  const wait = Math.min(EVENT_DEBOUNCE_MS, Math.max(0, firstPendingAt + EVENT_MAX_WAIT_MS - now))
  applyTimer = setTimeout(() => void applyEvents(), wait)
}

function onEvent(e: ProjectEvent) {
  if (e.type === 'resync' || e.type === 'catalog.changed') pendingAll = true
  else if (e.type === 'folders.changed') pendingStructure = true
  else if (e.type === 'files.changed') {
    if (e.folder_ids?.length) e.folder_ids.forEach((id) => pendingFolders.add(id))
    else pendingAll = true
  } else return
  scheduleApply()
}

/** 一轮刷新正在跑（审查第 1 轮：一轮超过去抖时间时不叠加，跑完再补一次）。 */
let applying = false
/** 跑的期间又到了该刷新的时候：跑完马上再来一轮（这期间攒下的事件一起处理）。 */
let applyAgain = false

async function applyEvents() {
  applyTimer = null
  firstPendingAt = 0
  if (!alive) return
  if (applying) {
    applyAgain = true
    return
  }
  applying = true
  try {
    const folders = new Set(pendingFolders)
    const structure = pendingStructure
    const all = pendingAll || structure
    pendingFolders.clear()
    pendingAll = false
    pendingStructure = false
    if (rootState.value !== 'ready') {
      // 第一次还没取到（或失败了）：重新取根文件夹。
      await loadRoot()
      return
    }
    // "全部"只是根和看得见的展开链上的文件夹（syncTracked 之后 useFolderSummaries 只跟踪这些）。
    if (structure) sums.forgetUnloadedParents()
    if (all) await sums.refreshAll()
    else await sums.refreshForFolders(folders)
    syncTracked()
  } finally {
    applying = false
    if (applyAgain && alive) {
      applyAgain = false
      void applyEvents()
    }
  }
}

const stream = useProjectStream(() => project.currentId, {
  onEvent,
  onOpen: (first) => {
    if (!first || rootState.value === 'ready') {
      // 重连（断开期间的事件不会补发），或第一次连上时已经取好了（加载和连接建立之间可能漏了事件）：全部刷新。
      pendingAll = true
      scheduleApply()
    }
  },
  onStopped: (err) => {
    if (err.status === 403 || err.status === 404) void project.refresh()
  },
})

onMounted(() => {
  void project.ensureLoaded()
})

onBeforeUnmount(() => {
  alive = false
  if (applyTimer) clearTimeout(applyTimer)
  applyTimer = null
})

function timeText(d: Date | null): string {
  if (!d) return ''
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
</script>

<template>
  <div
    class="flex min-h-0 flex-1 flex-col"
    data-testid="dashboard-page"
    :data-stream="stream.state.value"
    :data-state="rootState"
    :data-updates="updates"
  >
    <header class="flex h-14 shrink-0 items-center gap-3 border-b px-6">
      <h1 class="shrink-0 text-base font-semibold tracking-tight text-foreground">项目看板</h1>
      <span v-if="project.current" class="min-w-0 truncate border-l pl-3 text-sm text-muted-foreground">
        {{ project.current.project_name }} · 整个项目
      </span>
      <div class="flex-1" />
      <span v-if="refreshError && rootState === 'ready'" class="truncate text-xs text-destructive" :title="refreshError">
        刷新失败：{{ refreshError }}
      </span>
      <span v-if="updatedAt" class="shrink-0 text-xs text-muted-foreground tabular-nums" data-testid="dashboard-updated">
        更新于 {{ timeText(updatedAt) }}
      </span>
      <span
        v-if="project.current"
        class="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground"
        :title="stream.state.value === 'open' ? '已连接实时更新' : '实时更新已断开，正在重连'"
      >
        <span
          class="size-1.5 rounded-full"
          :class="stream.state.value === 'open' ? 'bg-ok' : stream.state.value === 'stopped' ? 'bg-destructive' : 'animate-pulse bg-warn'"
        />
        {{ stream.state.value === 'open' ? '实时' : stream.state.value === 'stopped' ? '已断开' : '连接中…' }}
      </span>
      <Button v-if="project.current" variant="outline" size="sm" data-testid="dashboard-refresh" @click="refreshNow">
        <RefreshCwIcon />刷新
      </Button>
    </header>

    <!-- 项目还在加载 / 没有能进的项目 -->
    <div v-if="!project.current" class="flex flex-1 items-center justify-center p-6 text-sm text-muted-foreground">
      <span v-if="project.state === 'loading' || project.state === 'idle'" class="flex items-center gap-2">
        <LoaderCircleIcon class="size-4 animate-spin" />正在加载项目…
      </span>
      <div v-else class="max-w-sm text-center">
        <div class="text-base font-medium text-foreground">没有可进入的项目</div>
        <p class="mt-1">请联系管理员在「系统管理 › 项目」里给你授权，或在左上角选择一个项目。</p>
      </div>
    </div>

    <div v-else-if="!counts" class="flex flex-1 items-center justify-center p-6 text-sm text-muted-foreground">
      <span v-if="rootState !== 'error'" class="flex items-center gap-2">
        <LoaderCircleIcon class="size-4 animate-spin" />正在统计…
      </span>
      <div v-else class="text-center" data-testid="dashboard-error">
        <p class="text-destructive">{{ refreshError ?? '取不到看板数据' }}</p>
        <Button size="sm" variant="outline" class="mt-3" @click="loadRoot">重试</Button>
      </div>
    </div>

    <section v-else class="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-6 py-5">
      <KpiTiles :counts="counts" :require-locked="requireLocked" />

      <div class="grid grid-cols-[minmax(0,600px)_minmax(0,1fr)] items-start gap-5">
        <!-- 状态矩阵 -->
        <div class="rounded-lg p-4 shadow-[0_0_0_1px_rgba(0,0,0,0.08)]">
          <div class="mb-3 flex items-baseline gap-2">
            <h2 class="text-sm font-semibold text-foreground">状态矩阵：审核 × 归类</h2>
            <span class="text-xs text-muted-foreground">文件数，整个项目</span>
          </div>
          <StatusMatrix :counts="counts" :require-locked="requireLocked" />
          <p class="mt-3 text-[11.5px] leading-relaxed text-muted-foreground">
            审核和归类互不限制，可以先归类再审。可导出按项目设置算：{{
              requireLocked ? '只算已定稿的条目里的文件' : '已归类的文件都算'
            }}；绿框是"通过且可导出"，导出时默认只导出审核通过的。
          </p>
        </div>

        <!-- 总体进度 -->
        <div class="flex flex-col gap-5 rounded-lg p-4 shadow-[0_0_0_1px_rgba(0,0,0,0.08)]" data-testid="overall-progress">
          <div>
            <div class="mb-2 flex items-baseline gap-2">
              <h2 class="text-sm font-semibold text-foreground">审核进度</h2>
              <span class="text-xs text-muted-foreground">通过 {{ percentText(counts.review.approved, counts.total) }}</span>
            </div>
            <StackedBar :segments="reviewSegments(counts)" :total="counts.total" :height="12" />
            <ul class="mt-2.5 grid grid-cols-2 gap-x-6 gap-y-1 text-xs">
              <li v-for="s in reviewSegments(counts)" :key="s.key" class="flex items-center gap-1.5">
                <span class="size-2 rounded-[2px]" :class="s.color" />
                <span class="text-muted-foreground">{{ s.label }}</span>
                <span class="ml-auto tabular-nums text-foreground">{{ num(s.value) }}</span>
                <span class="w-9 text-right tabular-nums text-muted-foreground">{{ percentText(s.value, counts.total) }}</span>
              </li>
            </ul>
          </div>
          <div>
            <div class="mb-2 flex items-baseline gap-2">
              <h2 class="text-sm font-semibold text-foreground">归类进度</h2>
              <span class="text-xs text-muted-foreground">已归类 {{ percentText(counts.catalog.cataloged, counts.total) }}</span>
            </div>
            <StackedBar :segments="catalogSegments(counts)" :total="counts.total" :height="12" />
            <ul class="mt-2.5 grid grid-cols-2 gap-x-6 gap-y-1 text-xs">
              <li v-for="k in (['locked', 'unlocked', 'uncataloged'] as const)" :key="k" class="flex items-center gap-1.5">
                <span class="size-2 rounded-[2px]" :class="k === 'uncataloged' ? 'bg-muted shadow-[inset_0_0_0_1px_rgba(0,0,0,0.12)]' : CATALOG_COLOR[k]" />
                <span class="text-muted-foreground">{{ CATALOG_COLUMN_LABEL[k] }}</span>
                <span class="ml-auto tabular-nums text-foreground">{{ num(counts.catalog[k]) }}</span>
                <span class="w-9 text-right tabular-nums text-muted-foreground">{{ percentText(counts.catalog[k], counts.total) }}</span>
              </li>
            </ul>
          </div>
        </div>
      </div>

      <!-- 文件夹进度 -->
      <div class="rounded-lg shadow-[0_0_0_1px_rgba(0,0,0,0.08)]">
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3">
          <h2 class="text-sm font-semibold text-foreground">文件夹进度</h2>
          <span class="text-xs text-muted-foreground">点箭头逐层展开</span>
          <div class="flex-1" />
          <div class="flex items-center gap-3 text-[11px] text-muted-foreground">
            <span>审核</span>
            <span v-for="s in (['approved', 'pending', 'rejected', 'unassigned'] as const)" :key="s" class="flex items-center gap-1">
              <span class="size-2 rounded-[2px]" :class="REVIEW_COLOR[s]" />{{ REVIEW_STATUS[s].label }}
            </span>
            <span class="ml-2">归类</span>
            <span v-for="k in (['locked', 'unlocked'] as const)" :key="k" class="flex items-center gap-1">
              <span class="size-2 rounded-[2px]" :class="CATALOG_COLOR[k]" />{{ CATALOG_COLUMN_LABEL[k] }}
            </span>
          </div>
          <Button v-if="expanded.size" variant="ghost" size="xs" data-testid="collapse-all" @click="collapseAll">全部收起</Button>
        </div>
        <FolderProgressTable class="border-t" :rows="rows" :require-locked="requireLocked" @toggle="toggle" />
      </div>
    </section>
  </div>
</template>
