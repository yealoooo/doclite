<script setup lang="ts">
// 导出页 /export（M4-5，参考 plan.html 的 p-export Mockup；菜单码 export，内置只有负责人和 admin）。
//
// - **任务列表**（左，ExportTaskRow）：范围、格式、件数和页数、状态和进度、下载、明细、交付后变更 n、取消、按原来的
//   范围重新导出。最新的在前，滚到底加载更多。
// - **新建导出**（右栏，NewExportPanel）：选范围 → 选格式和选项 → 预检 → 建任务。地址栏带着范围进来（/catalog 的入口）：
//   `?new=category&ids=…`、`?new=item&ids=…`、`?resubmit=<任务>`（见 lib/export.ts），读完就从地址栏去掉。
// - **明细**（TaskFilesDialog，按件、按页）、**交付后变更**（TaskChangesDialog，"只补交有变更的 n 件"用 `resubmit_of`）。
// - **下载**：先 `GET /download` 拿预签名地址（409 时提示 message），再打开它，直接从 S3 下载（lib/download.ts）：
//   壳里交给系统浏览器（壳的导航保护拦下所有外部地址）；浏览器里用隐藏的 iframe（不触发 beforeunload，存储出错时
//   不整页跳走，提示"下载没有开始"）。
// - **取消**：排队中、生成中的可以取消；生成中的取消后显示"正在清理"，执行器删掉已上传的部分后发事件。
// - **实时**（useProjectStream）：
//   - `export.changed {task_ids}`：只重拉这些任务（在途合并，同一个任务一秒最多一次，失败退避重试；见
//     useExportTasks），这些任务的交付后变更按对话框的速度重取；
//   - `files.changed`、`catalog.changed`：交付后变更标成"可能过时"（对话框开着的那个去抖后重取，列表行至少隔 20 秒），
//     新建导出的预检静默重跑（至少隔 20 秒）；标签页在后台时都暂停，回到前台补取一次（审查第 1 轮）；
//   - `resync`、断线重连：整页重新加载。
import { LoaderCircleIcon, PlusIcon } from '@lucide/vue'
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import { cancelExportTask, getExportDownload, MAX_SCOPE_IDS, probeExportTask } from '@/api/export'
import { ApiError, HANDLE_ALL, isApiError } from '@/api/http'
import type { ExportFormat, ExportTask, ProjectEvent } from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import { Button } from '@/components/ui/button'
import StreamStatus from '@/components/app/StreamStatus.vue'
import { useProjectStream } from '@/composables/useProjectStream'
import { openDownload } from '@/lib/download'
import { formatBytes } from '@/lib/files'
import { shellErrorMessage } from '@/lib/shell'
import { type NewExportDraft, parseNewExportQuery, scopeTitle } from '@/lib/export'
import { useProjectStore } from '@/stores/project'
import { useSessionStore } from '@/stores/session'

import ExportTaskRow from './ExportTaskRow.vue'
import NewExportPanel from './NewExportPanel.vue'
import TaskChangesDialog from './TaskChangesDialog.vue'
import TaskFilesDialog from './TaskFilesDialog.vue'
import { useExportTasks } from './useExportTasks'

const project = useProjectStore()
const session = useSessionStore()
const route = useRoute()
const router = useRouter()

/** 选范围要用归类树（catalog 菜单）。 */
const canPickScope = computed(() => session.menuCodes.has('catalog'))

const list = useExportTasks(() => project.currentId)
const { tasks, changes, labels } = list

const now = ref(Date.now())
const clock = setInterval(() => (now.value = Date.now()), 30_000)

const byId = computed(() => new Map(tasks.value.map((t) => [t.id, t])))

function titleOf(t: ExportTask): string {
  return scopeTitle(t, labels.labels.value.get(t.id))
}

// ---------- 可视区里的行：按需取交付后变更、条目范围的第一件档号 ----------

const visibleIds = reactive(new Set<string>())
const releases = new Map<string, () => void>()

function onRowVisible(id: string, v: boolean) {
  if (v) visibleIds.add(id)
  else visibleIds.delete(id)
  syncWants()
}

function syncWants() {
  const map = byId.value
  for (const [id, release] of releases) {
    const t = map.get(id)
    if (!t || t.status !== 'done' || !visibleIds.has(id)) {
      release()
      releases.delete(id)
    }
  }
  for (const id of visibleIds) {
    const t = map.get(id)
    if (!t) continue
    if (t.status === 'done' && !releases.has(id)) releases.set(id, changes.want(id, 'row'))
    if (t.scope_type === 'item' && !t.resubmit_of) labels.request(id)
  }
}

// 任务状态变了（例如变成 done）、列表重新加载之后，按可视区重新登记。
watch(() => tasks.value.map((t) => `${t.id}:${t.status}`).join(','), syncWants)

// ---------- 新建导出（右栏） ----------

const panel = ref<{ key: number; draft: NewExportDraft; labels?: Record<string, string> } | null>(null)
let panelKey = 0
const highlightId = ref<string | null>(null)
/** files.changed、catalog.changed 时加一（新建导出的预检重跑）。 */
const precheckToken = ref(0)

function openNew(draft: NewExportDraft, labelMap?: Record<string, string>) {
  panel.value = { key: ++panelKey, draft, labels: labelMap }
}

function openBlank() {
  openNew({ mode: 'category', ids: [], resubmitOf: null, format: 'pdf', onlyApproved: true })
}

/** 补交的源任务（列表里没有时，冷启动取到的）。 */
const resubmitSources = new Map<string, ExportTask>()

// 地址栏带进来的新建导出（/catalog 的入口、补交）：读完就去掉，刷新页面不会再打开。
watch(
  () => route.query,
  (q) => {
    const draft = parseNewExportQuery(q)
    if (!draft) return
    void router.replace({ query: {} })
    if (draft.mode === 'resubmit' && draft.resubmitOf) void openResubmitFromQuery(draft)
    else openNew(draft)
  },
  { immediate: true },
)

/**
 * `?resubmit=<任务>`：格式和"只导出审核通过的"沿用源任务（审查第 1 轮：冷启动时列表还没加载，先取源任务再打开）。
 * 取不到（不存在、属于别的项目）时按地址栏的参数打开，预检会报 404。
 */
async function openResubmitFromQuery(draft: NewExportDraft) {
  const id = draft.resubmitOf!
  await project.ensureLoaded()
  let src: ExportTask | null = byId.value.get(id) ?? null
  if (!src) {
    try {
      src = await probeExportTask(id, HANDLE_ALL)
    } catch {
      src = null
    }
  }
  if (src && src.project_id === project.currentId) {
    draft.format = src.export_format === 'images' ? 'images' : 'pdf'
    draft.onlyApproved = src.only_approved
    resubmitSources.set(src.id, src)
  }
  openNew(draft)
}

const resubmitTitle = computed(() => {
  const id = panel.value?.draft.resubmitOf
  const src = id ? (byId.value.get(id) ?? resubmitSources.get(id)) : undefined
  return src ? titleOf(src) : null
})

function onCreated(task: ExportTask) {
  panel.value = null
  list.upsert(task)
  highlightId.value = task.id
  setTimeout(() => {
    if (highlightId.value === task.id) highlightId.value = null
  }, 6000)
  toast.success(`已建导出任务：${task.item_count.toLocaleString()} 件 · ${task.page_count.toLocaleString()} 页`, {
    description: task.skipped_item_count ? `跳过了 ${task.skipped_item_count} 件不就绪的。` : '排队等执行，进度在列表里实时更新。',
  })
}

/** 按原来的范围重新导出（过期、失败、取消的）。 */
function reexport(t: ExportTask) {
  if (t.scope_ids.length > MAX_SCOPE_IDS) {
    // 只有补交的任务会超过（server 按交付后变更算，不受 5000 个的限制）；行上的按钮已禁用，这里兜底。
    toast.error(`这次补交有 ${t.scope_ids.length.toLocaleString()} 件，超过手选范围的上限 ${MAX_SCOPE_IDS.toLocaleString()} 件`, {
      description: '请在原任务的交付后变更里点"只补交有变更的"重新补交。',
    })
    return
  }
  const format: ExportFormat = t.export_format === 'images' ? 'images' : 'pdf'
  if (t.scope_type === 'category') {
    const names: Record<string, string> = {}
    for (const c of t.scope_categories) names[c.id] = c.category_names.length ? c.category_names.join(' / ') : '整个项目（全部）'
    openNew({ mode: 'category', ids: t.scope_ids, resubmitOf: null, format, onlyApproved: t.only_approved }, names)
  } else {
    openNew({ mode: 'item', ids: t.scope_ids, resubmitOf: null, format, onlyApproved: t.only_approved })
  }
}

function resubmit(t: ExportTask) {
  changesDialog.open = false
  const format: ExportFormat = t.export_format === 'images' ? 'images' : 'pdf'
  openNew({ mode: 'resubmit', ids: [], resubmitOf: t.id, format, onlyApproved: t.only_approved })
}

// ---------- 明细、交付后变更 ----------

const filesDialog = reactive({ open: false, taskId: null as string | null })
const changesDialog = reactive({ open: false, taskId: null as string | null })
let changesRelease: (() => void) | null = null

const filesTask = computed(() => (filesDialog.taskId ? (byId.value.get(filesDialog.taskId) ?? null) : null))
const changesTask = computed(() => (changesDialog.taskId ? (byId.value.get(changesDialog.taskId) ?? null) : null))

function openFiles(t: ExportTask) {
  filesDialog.taskId = t.id
  filesDialog.open = true
}

function openChanges(t: ExportTask) {
  changesDialog.taskId = t.id
  changesDialog.open = true
}

// 对话框开着时这个任务的交付后变更算"要用的"（作废后自动重取）。
watch(
  () => [changesDialog.open, changesDialog.taskId] as const,
  ([open, id]) => {
    changesRelease?.()
    changesRelease = open && id ? changes.want(id, 'dialog') : null
  },
)

// ---------- 下载 ----------

const downloading = reactive(new Set<string>())

async function download(t: ExportTask) {
  if (downloading.has(t.id)) return
  downloading.add(t.id)
  try {
    const d = await getExportDownload(t.id, ['invalid_state', 'storage_unavailable'])
    // 预签名地址带 response-content-disposition（attachment）。壳里交给系统浏览器；浏览器里用隐藏的 iframe，
    // 不离开页面、不触发 beforeunload（lib/download.ts）。
    const via = await openDownload(d.url, () => {
      toast.error('下载没有开始，请检查存储是否可以访问', {
        description: '打开下载地址时得到的是一个页面而不是文件：存储连不上，或者返回了错误。',
      })
    })
    if (via === 'shell') {
      toast.success(`已在系统浏览器中开始下载 ${d.file_name}`, {
        description: `${formatBytes(d.size)} · 下载进度和保存位置在系统浏览器的下载列表里`,
      })
    } else {
      toast.success(`开始下载 ${d.file_name}`, { description: `${formatBytes(d.size)} · 浏览器直接从存储下载` })
    }
  } catch (e) {
    if (isApiError(e, 'invalid_state') || isApiError(e, 'storage_unavailable')) {
      toast.error(e.message)
      list.refreshTasks([t.id])
    } else if (!(e instanceof ApiError)) {
      // 地址不对、壳打不开系统浏览器（接口的其他错误请求封装已经提示过）。
      toast.error(`下载没有开始：${shellErrorMessage(e)}`)
    }
  } finally {
    downloading.delete(t.id)
  }
}

// ---------- 取消 ----------

const cancelAsk = reactive({ open: false, task: null as ExportTask | null, loading: false })

function askCancel(t: ExportTask) {
  cancelAsk.task = t
  cancelAsk.open = true
}

const cancelText = computed(() => {
  const t = cancelAsk.task
  if (!t) return ''
  return t.status === 'running'
    ? '正在生成，取消后会停下并删掉已生成的部分（清理要几秒，期间显示"正在清理"）。'
    : '还没开始执行，取消后不会生成导出物。任务和明细会保留。'
})

async function doCancel() {
  const t = cancelAsk.task
  if (!t) return
  cancelAsk.loading = true
  try {
    const res = await cancelExportTask(t.id, ['invalid_state'])
    list.upsert(res)
    cancelAsk.open = false
    toast.success(res.cleaning_up ? '已取消，正在清理已生成的部分' : '已取消')
  } catch (e) {
    if (isApiError(e, 'invalid_state')) {
      toast.error(e.message)
      cancelAsk.open = false
      list.refreshTasks([t.id])
    }
  } finally {
    cancelAsk.loading = false
  }
}

// ---------- 项目 ----------

let lastProjectId: string | null = null
watch(
  () => project.currentId,
  (pid) => {
    const changedProject = lastProjectId !== null && pid !== lastProjectId
    lastProjectId = pid
    for (const r of releases.values()) r()
    releases.clear()
    visibleIds.clear()
    list.reset()
    if (changedProject) {
      // 右栏、对话框里的范围和任务属于原来的项目。
      panel.value = null
      filesDialog.open = false
      changesDialog.open = false
    }
    if (pid) void list.load()
  },
  { immediate: true },
)
void project.ensureLoaded()

// ---------- 实时 ----------

function onEvent(e: ProjectEvent) {
  if (e.type === 'export.changed') {
    const ids = e.task_ids ?? []
    if (ids.length) {
      list.refreshTasks(ids)
      changes.invalidate(ids, true)
    } else void list.load()
  } else if (e.type === 'files.changed' || e.type === 'catalog.changed') {
    changes.invalidate()
    precheckToken.value++
  } else if (e.type === 'resync') {
    void refreshAll()
  }
}

async function refreshAll() {
  changes.invalidate()
  precheckToken.value++
  await list.load()
}

const stream = useProjectStream(() => project.currentId, {
  onEvent,
  onOpen: (first) => {
    // 重连：断开期间的事件不补发，整页刷新（列表、交付后变更、新建导出的预检）。第一次连上而列表已经加载好了：加载和
    // 连接建立之间可能漏了事件，补拉一次列表（交付后变更、预检刚取过，不重取）。
    if (!first) void refreshAll()
    else if (list.state.value === 'ready') void list.load()
  },
})

onBeforeUnmount(() => {
  clearInterval(clock)
  for (const r of releases.values()) r()
  releases.clear()
  changesRelease?.()
  list.dispose()
})

const listEl = ref<HTMLElement | null>(null)

function onListScroll() {
  const el = listEl.value
  if (!el || !list.nextCursor.value || list.loadingMore.value) return
  if (el.scrollTop + el.clientHeight >= el.scrollHeight - 200) void list.loadMore()
}

const counts = computed(() => {
  const c = { running: 0, queued: 0 }
  for (const t of tasks.value) {
    if (t.status === 'running') c.running++
    else if (t.status === 'queued') c.queued++
  }
  return c
})
</script>

<template>
  <div
    class="flex min-h-0 flex-1 flex-col"
    data-testid="export-page"
    :data-stream="stream.state.value"
    :data-list="list.state.value"
    :data-rows="tasks.length"
    :data-changes-requests="changes.requests.value"
  >
    <header class="flex h-14 shrink-0 items-center gap-3 border-b px-5">
      <h1 class="shrink-0 text-base font-semibold tracking-tight text-foreground">导出交付</h1>
      <span class="shrink-0 rounded-full bg-muted px-2 text-2xs leading-5 text-muted-foreground">只导出已归档的文件</span>
      <div class="flex-1" />
      <StreamStatus v-if="project.current" :state="stream.state.value" />
      <Button v-if="project.current" size="sm" data-testid="export-new-button" @click="openBlank">
        <PlusIcon />新建导出
      </Button>
    </header>

    <div v-if="!project.current" class="flex flex-1 items-center justify-center p-6 text-sm text-muted-foreground">
      <span v-if="project.state === 'loading' || project.state === 'idle'" class="flex items-center gap-2">
        <LoaderCircleIcon class="size-4 animate-spin" />正在加载项目…
      </span>
      <div v-else class="max-w-sm text-center" data-testid="export-no-project">
        <div class="text-base font-medium text-foreground">没有可进入的项目</div>
        <p class="mt-1">请联系管理员在「系统管理 › 项目」里给你授权，或在左上角选择一个项目。</p>
      </div>
    </div>

    <div v-else class="flex min-h-0 flex-1">
      <!-- 任务列表 -->
      <section class="flex min-w-0 flex-1 flex-col">
        <div class="flex h-10 shrink-0 items-center gap-3 border-b px-5 text-xs">
          <span class="shrink-0 font-medium text-foreground">导出任务</span>
          <span v-if="counts.running || counts.queued" class="shrink-0 text-muted-foreground" data-testid="export-counts">
            生成中 {{ counts.running }} · 排队 {{ counts.queued }}
          </span>
          <span class="flex-1" />
          <span class="hidden min-w-0 truncate text-muted-foreground xl:inline">
            导出入口在归类这一侧；建任务时冻结每页的版本，之后的重扫、撤回、改页序会出现在"交付后变更"里，只补交那几件。
          </span>
        </div>
        <div class="grid shrink-0 grid-cols-[minmax(0,1fr)_64px_104px_minmax(150px,210px)] gap-x-4 border-b bg-muted/40 px-5 py-1.5 text-2xs font-medium text-muted-foreground">
          <span>范围（归类树）</span><span>格式</span><span>件数 · 页数</span><span>状态</span>
        </div>
        <div ref="listEl" class="min-h-0 flex-1 overflow-y-auto" data-testid="export-task-list" @scroll.passive="onListScroll">
          <div v-if="list.state.value === 'loading'" class="flex items-center gap-2 p-5 text-xs text-muted-foreground">
            <LoaderCircleIcon class="size-3.5 animate-spin" />正在加载导出任务…
          </div>
          <div v-else-if="list.state.value === 'error'" class="p-5 text-xs text-destructive">
            导出任务加载失败：{{ list.error.value }}
            <button type="button" class="ml-1 underline" @click="list.load()">重试</button>
          </div>
          <div
            v-else-if="list.state.value === 'ready' && !tasks.length"
            class="flex flex-col items-center gap-2 px-6 py-16 text-center text-sm text-muted-foreground"
            data-testid="export-empty"
          >
            <div class="text-base font-medium text-foreground">还没有导出任务</div>
            <p>在归类页选一个节点或条目点"导出"，或者点右上角的"新建导出"。</p>
          </div>
          <template v-else>
            <ExportTaskRow
              v-for="t in tasks"
              :key="t.id"
              :task="t"
              :now="now"
              :changes="changes.entries.get(t.id)"
              :first-item="labels.labels.value.get(t.id)"
              :resubmit-source="t.resubmit_of && byId.get(t.resubmit_of) ? titleOf(byId.get(t.resubmit_of)!) : null"
              :downloading="downloading.has(t.id)"
              :highlighted="highlightId === t.id"
              @visible="(v) => onRowVisible(t.id, v)"
              @download="download(t)"
              @files="openFiles(t)"
              @changes="openChanges(t)"
              @cancel="askCancel(t)"
              @reexport="reexport(t)"
            />
            <div v-if="list.loadingMore.value" class="py-3 text-center text-xs text-muted-foreground">正在加载更多…</div>
            <div v-else-if="list.loadMoreError.value" class="py-3 text-center text-xs text-destructive">
              加载更多失败：{{ list.loadMoreError.value }}
              <button type="button" class="ml-1 underline" @click="list.loadMore()">重试</button>
            </div>
            <div v-else-if="list.nextCursor.value" class="py-3 text-center">
              <Button size="xs" variant="outline" @click="list.loadMore()">加载更多</Button>
            </div>
          </template>
        </div>
      </section>

      <!-- 新建导出 -->
      <NewExportPanel
        v-if="panel && project.currentId"
        :key="panel.key"
        :project-id="project.currentId"
        :draft="panel.draft"
        :labels="panel.labels"
        :resubmit-title="resubmitTitle"
        :can-pick-scope="canPickScope"
        :refresh-token="precheckToken"
        @close="panel = null"
        @created="onCreated"
      />
    </div>

    <TaskFilesDialog v-model:open="filesDialog.open" :task="filesTask" :scope-title="filesTask ? titleOf(filesTask) : ''" />
    <TaskChangesDialog
      v-model:open="changesDialog.open"
      :task="changesTask"
      :scope-title="changesTask ? titleOf(changesTask) : ''"
      :entry="changesDialog.taskId ? changes.entries.get(changesDialog.taskId) : undefined"
      @resubmit="resubmit"
      @refresh="changesDialog.taskId && changes.reload(changesDialog.taskId)"
    />
    <ConfirmDialog
      v-model:open="cancelAsk.open"
      :title="`取消导出「${cancelAsk.task ? titleOf(cancelAsk.task) : ''}」？`"
      :description="cancelText"
      confirm-text="取消任务"
      destructive
      :loading="cancelAsk.loading"
      @confirm="doCancel"
    />
  </div>
</template>
