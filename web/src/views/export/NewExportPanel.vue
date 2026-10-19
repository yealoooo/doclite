<script setup lang="ts">
// 新建导出（M4-5，参考 plan.html 的 p-export Mockup 右栏）：选范围 → 选格式和选项 → 预检 → 显示结果 → 建任务。
//
// - **范围**（硬性规则 6：只能从归类树选节点或条目）：
//   - 归类节点：CategoryPicker 选（可以选"顶层"＝整个项目），可以加几个；
//   - 条目：ExportItemPicker（左边归类树、右边节点里的条目，勾选）；
//   - 补交（`resubmit_of`）：范围由 server 按那个任务的交付后变更算（有变更、没删除的条目），不把 id 列表传进
//     `scope_ids`（不受 5000 个的限制）；可以改为手选范围。
//   - 选范围要用归类树（catalog 菜单）；没有这个菜单的人只能用带进来的范围（任务的"重新导出"、补交）。
// - **格式**：PDF、原图包；双层 PDF、OFD 只显示为"以后的版本"。"只导出审核通过的"默认勾选（用户决定 Q7）。
// - **预检**：范围、格式、选项一变就重新预检（去抖 250 ms，旧的响应作废）；页面收到 `files.changed`、`catalog.changed`
//   时（`refreshToken` 加一）静默重跑（两次之间至少 20 秒，标签页在后台时不跑、回到前台补跑一次；期间保留上一次的
//   结果、按钮照常可用，审查第 1 轮）。输入变了而预检没跑完、和现在的输入对不上时不能建任务。
//   - 不就绪的条目：汇总每种原因的件数，先列 3 件，"查看全部问题"展开全部（server 最多列 500 件，`not_ready_truncated`）；
//   - 有不就绪的时候按钮是"只导出就绪的 n 件"（`only_ready: true`）；全部就绪是"开始导出"；一件都没有时不能建；
//   - 超过 20 万页（`page_limit_exceeded`）：显示 `page_limit_message`，禁用建任务，提示按分类分批导出。
// - **建任务**：201 → 交给页面（插进列表）；409 `invalid_state` 的 `details` 和预检一样 → 用它更新预检结果并显示 message
//   （建任务之前别人改了数据）；其他错误在按钮上方显示。
import {
  CheckIcon,
  CircleAlertIcon,
  FolderTreeIcon,
  ListChecksIcon,
  LoaderCircleIcon,
  PlusIcon,
  RefreshCwIcon,
  TriangleAlertIcon,
  XIcon,
} from '@lucide/vue'
import { useDocumentVisibility } from '@vueuse/core'
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import { probeArchiveItem } from '@/api/catalog'
import { createExportTask, MAX_SCOPE_IDS, precheckExport } from '@/api/export'
import { ApiError, errorMessage, HANDLE_ALL, isApiError } from '@/api/http'
import type { ExportFormat, ExportNotReadyItem, ExportPrecheck, ExportPrecheckBody, ExportTask } from '@/api/types'
import CategoryPicker, { type PickedCategory } from '@/components/catalog/CategoryPicker.vue'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { useCategoryTree } from '@/composables/useCategoryTree'
import {
  FORMAT_OPTIONS,
  itemName,
  NOT_READY_LABEL,
  NOT_READY_ORDER,
  type NewExportDraft,
} from '@/lib/export'
import { reviewStatusOf } from '@/lib/files'

import ExportItemPicker, { type PickedItem } from './ExportItemPicker.vue'

export interface ScopeEntry {
  id: string
  /** 节点：从顶层起的名称路径（根节点是"整个项目"）；条目：档号或"第 n 件"。还没取到时为 null。 */
  label: string | null
  /** 条目的题名和所在节点。 */
  sub?: string
}

const props = defineProps<{
  projectId: string
  draft: NewExportDraft
  /** 已知的名称（任务的"重新导出"带进来的节点名称链）。 */
  labels?: Record<string, string>
  /** 补交的源任务的范围标题（列表里有它时）。 */
  resubmitTitle?: string | null
  /** 能用归类树选范围（catalog 菜单）。 */
  canPickScope: boolean
  /** 页面收到 files.changed、catalog.changed 时加一：重新预检。 */
  refreshToken: number
}>()

const emit = defineEmits<{
  close: []
  created: [task: ExportTask]
}>()

// ---------- 输入 ----------

const mode = ref<'category' | 'item' | 'resubmit'>(props.draft.mode)
const resubmitOf = ref<string | null>(props.draft.resubmitOf)
const categories = ref<ScopeEntry[]>(props.draft.mode === 'category' ? props.draft.ids.map((id) => ({ id, label: props.labels?.[id] ?? null })) : [])
const items = ref<ScopeEntry[]>(props.draft.mode === 'item' ? props.draft.ids.map((id) => ({ id, label: props.labels?.[id] ?? null })) : [])
const format = ref<ExportFormat>(props.draft.format)
const onlyApproved = ref(props.draft.onlyApproved)
/** 预检 404 时范围里已经不存在的 id。 */
const missing = ref<Set<string>>(new Set())
const creating = ref(false)
/** 建任务失败的说明（显示在按钮上方）。 */
const createError = ref('')
/** 范围的提示（补交改手选时截断到 5000 件）。 */
const scopeNotice = ref('')

const entries = computed(() => (mode.value === 'item' ? items.value : categories.value))

const body = computed<ExportPrecheckBody | null>(() => {
  if (mode.value === 'resubmit') {
    return resubmitOf.value ? { resubmit_of: resubmitOf.value, export_format: format.value, only_approved: onlyApproved.value } : null
  }
  const ids = entries.value.map((e) => e.id)
  if (!ids.length) return null
  return { scope_type: mode.value, scope_ids: ids, export_format: format.value, only_approved: onlyApproved.value }
})
const bodyKey = computed(() => (body.value ? JSON.stringify(body.value) : ''))

// ---------- 名称（地址栏、重新导出带进来的范围） ----------

const tree = useCategoryTree()
let alive = true
let labelSeq = 0
/** 逐个取名称最多取这么多个（每个一两次请求）；更多的只显示 id 的前几位，预检照常按全部的 id。 */
const MAX_LABEL_LOOKUPS = 30

function placeholder(list: ScopeEntry[], what: string): ScopeEntry[] {
  return list.map((e, i) => (e.label === null && (i >= MAX_LABEL_LOOKUPS || !props.canPickScope) ? { ...e, label: `${what} ${e.id.slice(0, 8)}…` } : e))
}

async function resolveLabels() {
  const my = ++labelSeq
  if (mode.value === 'category') {
    categories.value = placeholder(categories.value, '归类节点')
    const todo = categories.value.filter((e) => e.label === null)
    if (!todo.length) return
    try {
      const root = await tree.init(props.projectId)
      if (!root || my !== labelSeq || !alive) return
      for (const e of todo) {
        const node = e.id === root.id ? root : await tree.reveal(e.id).catch(() => null)
        if (my !== labelSeq || !alive) return
        const label = !node ? '（不存在或已删除）' : node.id === root.id ? '整个项目（全部）' : tree.pathText(node.id)
        categories.value = categories.value.map((x) => (x.id === e.id ? { ...x, label } : x))
        if (!node) missing.value = new Set([...missing.value, e.id])
      }
    } catch {
      // 取不到名称不影响预检和导出：显示 id 的前几位。
      if (my === labelSeq && alive) {
        categories.value = categories.value.map((e) => (e.label === null ? { ...e, label: `归类节点 ${e.id.slice(0, 8)}…` } : e))
      }
    }
  } else if (mode.value === 'item') {
    items.value = placeholder(items.value, '条目')
    const todo = items.value.filter((e) => e.label === null)
    for (const e of todo) {
      let label = '（不存在或已删除）'
      let sub: string | undefined
      try {
        const it = await probeArchiveItem(e.id, HANDLE_ALL)
        if (it.project_id === props.projectId) {
          label = itemName(it.archive_no, it.item_no)
          sub = `${it.title || '（未填题名）'} · ${it.category_path.split('/').slice(1).join(' / ')}`
        }
      } catch {
        // 不存在、进不了：当作没有这个条目，预检会报 404。
      }
      if (my !== labelSeq || !alive) return
      items.value = items.value.map((x) => (x.id === e.id ? { ...x, label, sub } : x))
    }
  }
}
void resolveLabels()

// ---------- 选范围 ----------

const categoryPickerOpen = ref(false)
const itemPickerOpen = ref(false)

function switchMode(m: 'category' | 'item') {
  if (mode.value === m) return
  mode.value = m
  missing.value = new Set()
}

function onPickCategory(t: PickedCategory) {
  if (categories.value.some((e) => e.id === t.id)) return
  const label = t.path ? t.path : '整个项目（全部）'
  categories.value = [...categories.value, { id: t.id, label }]
}

function onPickItems(picked: PickedItem[]) {
  const have = new Set(items.value.map((e) => e.id))
  const add = picked.filter((p) => !have.has(p.id)).map((p) => ({ id: p.id, label: p.label, sub: p.sub }))
  items.value = [...items.value, ...add].slice(0, MAX_SCOPE_IDS)
}

function removeEntry(id: string) {
  if (mode.value === 'item') items.value = items.value.filter((e) => e.id !== id)
  else categories.value = categories.value.filter((e) => e.id !== id)
  if (missing.value.has(id)) {
    const next = new Set(missing.value)
    next.delete(id)
    missing.value = next
  }
}

function removeMissing() {
  const gone = missing.value
  if (mode.value === 'item') items.value = items.value.filter((e) => !gone.has(e.id))
  else categories.value = categories.value.filter((e) => !gone.has(e.id))
  missing.value = new Set()
}

/** 补交改为手选范围：用预检算出来的条目（有的话）作为起点。 */
function toManual() {
  const ids = precheck.value?.resubmit_of ? precheck.value.scope_ids : []
  mode.value = 'item'
  resubmitOf.value = null
  items.value = ids.slice(0, MAX_SCOPE_IDS).map((id) => ({ id, label: null }))
  scopeNotice.value =
    ids.length > MAX_SCOPE_IDS
      ? `补交的条目有 ${ids.length.toLocaleString()} 件，手选范围最多 ${MAX_SCOPE_IDS.toLocaleString()} 件，只带上了前 ${MAX_SCOPE_IDS.toLocaleString()} 件；全部补交请关掉右栏，在原任务的交付后变更里点"只补交有变更的"。`
      : ''
  void resolveLabels()
}

// ---------- 预检 ----------

const precheck = ref<ExportPrecheck | null>(null)
/** 预检结果对应的输入（和 bodyKey 不一样说明输入变了，要重新预检）。 */
const precheckKey = ref('')
const precheckState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const precheckError = ref('')
let precheckSeq = 0
let precheckTimer: ReturnType<typeof setTimeout> | null = null
/** 上一次发出预检的时间（事件触发的重跑按它限流）。 */
let lastPrecheckAt = 0
/** 静默重跑在路上（结果照常显示）。 */
const refreshing = ref(false)
/** 静默重跑失败的说明（结果保留）。 */
const refreshError = ref('')
const showAll = ref(false)

/**
 * 跑一次预检。`quiet`：只是项目里有了变化、输入没变的重跑（审查第 1 轮）：期间保留上一次的结果，"开始导出"照常可用、
 * 结果区不闪；失败也保留上一次的结果，只在旁边标一句。输入变了的（或者还没有能用的结果时）照常跑。
 */
async function runPrecheck(opts: { quiet?: boolean } = {}) {
  precheckTimer = null
  lastPrecheckAt = Date.now()
  eventDirty = false
  const b = body.value
  const key = bodyKey.value
  const my = ++precheckSeq
  if (!b) {
    precheck.value = null
    precheckKey.value = ''
    precheckState.value = 'idle'
    return
  }
  const quiet = !!opts.quiet && fresh.value
  if (quiet) refreshing.value = true
  else {
    precheckState.value = 'loading'
    precheckError.value = ''
  }
  refreshError.value = ''
  try {
    const res = await precheckExport(props.projectId, b, HANDLE_ALL)
    if (my !== precheckSeq || !alive) return
    precheck.value = res
    precheckKey.value = key
    precheckState.value = 'ready'
    missing.value = new Set()
  } catch (e) {
    if (my !== precheckSeq || !alive) return
    if (quiet) {
      refreshError.value = errorMessage(e)
      return
    }
    precheckState.value = 'error'
    precheckError.value = precheckErrorText(e)
    precheck.value = null
    precheckKey.value = ''
  } finally {
    if (my === precheckSeq) refreshing.value = false
  }
}

function precheckErrorText(e: unknown): string {
  if (isApiError(e, 'not_found')) {
    const ids = (e.details as { ids?: string[] } | undefined)?.ids
    if (ids?.length) {
      missing.value = new Set(ids)
      return `${e.message}（下面标出来了），移除后重新预检`
    }
    return e.message
  }
  return errorMessage(e)
}

function schedulePrecheck(delay = 250) {
  if (precheckTimer) clearTimeout(precheckTimer)
  precheckTimer = setTimeout(() => void runPrecheck(), delay)
}

watch(bodyKey, () => {
  createError.value = ''
  schedulePrecheck()
}, { immediate: true })

// 项目里的审核、挂接、著录变了：预检结果可能过时，静默重跑一次（只在已经有结果时；审查第 1 轮）。事件很密时两次之间
// 至少隔 20 秒；标签页在后台时不跑，回到前台时补跑一次。建任务时数据又变了的，server 409 带回最新的预检。
const EVENT_PRECHECK_MIN_INTERVAL_MS = 20_000
let eventTimer: ReturnType<typeof setTimeout> | null = null
/** 上次预检之后项目里有了变化。 */
let eventDirty = false
const visibility = useDocumentVisibility()
const pageVisible = computed(() => visibility.value !== 'hidden')

function scheduleEventPrecheck() {
  if (eventTimer || !eventDirty || !pageVisible.value) return
  const wait = Math.max(300, lastPrecheckAt + EVENT_PRECHECK_MIN_INTERVAL_MS - Date.now())
  eventTimer = setTimeout(() => {
    eventTimer = null
    if (!eventDirty || !pageVisible.value) return
    if (precheckState.value === 'ready' || precheckState.value === 'error') void runPrecheck({ quiet: true })
  }, wait)
}

watch(
  () => props.refreshToken,
  () => {
    eventDirty = true
    scheduleEventPrecheck()
  },
)

watch(pageVisible, (v) => {
  if (eventTimer) clearTimeout(eventTimer)
  eventTimer = null
  if (!v || !eventDirty) return
  // 回到前台：后台期间有过变化，补跑一次（不受 20 秒限制）。
  if (precheckState.value === 'ready' || precheckState.value === 'error') void runPrecheck({ quiet: true })
})

const fresh = computed(() => precheckState.value === 'ready' && !!precheck.value && precheckKey.value === bodyKey.value)

const reasonSummary = computed(() => {
  const p = precheck.value
  if (!p) return []
  return NOT_READY_ORDER.filter((k) => p.reason_counts[k] > 0).map((k) => ({ code: k, text: `${p.reason_counts[k]} 件${NOT_READY_LABEL[k]}` }))
})

/** 一件的问题写成一行（Mockup 的"0012 第 3 页：待审 · 第 5 页：驳回"）。 */
function shortIssue(it: ExportNotReadyItem): string {
  const parts = it.reasons.map((r) => {
    if (r.code === 'pages_not_approved' && r.pages?.length) {
      const shown = r.pages.slice(0, 5).map((p) => `第 ${p.page_no} 页：${reviewStatusOf(p.review_status).label}`)
      const more = (r.count ?? 0) > shown.length ? ` 等 ${r.count} 页` : ''
      return shown.join(' · ') + more
    }
    return NOT_READY_LABEL[r.code] ?? r.message
  })
  return parts.join(' · ')
}

const shownIssues = computed(() => {
  const list = precheck.value?.not_ready ?? []
  return showAll.value ? list : list.slice(0, 3)
})

// ---------- 建任务 ----------

const submit = computed(() => {
  const p = precheck.value
  if (!fresh.value || !p) return { label: precheckState.value === 'loading' ? '正在预检…' : '先预检', disabled: true, onlyReady: false }
  if (p.item_count === 0) return { label: '范围里没有条目', disabled: true, onlyReady: false }
  if (p.ready_item_count === 0) return { label: '没有可以导出的条目', disabled: true, onlyReady: false }
  if (p.page_limit_exceeded) return { label: '超过单个任务的页数上限', disabled: true, onlyReady: false }
  if (p.not_ready_count > 0) return { label: `只导出就绪的 ${p.ready_item_count.toLocaleString()} 件`, disabled: false, onlyReady: true }
  return { label: `开始导出（${p.ready_item_count.toLocaleString()} 件 · ${p.ready_page_count.toLocaleString()} 页）`, disabled: false, onlyReady: false }
})

function looksLikePrecheck(d: unknown): d is ExportPrecheck {
  return !!d && typeof d === 'object' && 'ready_item_count' in d && 'not_ready' in d
}

async function create() {
  const b = body.value
  if (!b || submit.value.disabled || creating.value) return
  creating.value = true
  createError.value = ''
  const key = bodyKey.value
  try {
    const task = await createExportTask(props.projectId, { ...b, only_ready: submit.value.onlyReady }, HANDLE_ALL)
    if (!alive) return
    emit('created', task)
  } catch (e) {
    if (!alive) return
    if (e instanceof ApiError && e.code === 'invalid_state' && looksLikePrecheck(e.details)) {
      // 建任务之前数据变了：用 details（同预检）更新结果，让用户看到现在的情况再决定。
      if (key === bodyKey.value) {
        precheck.value = e.details
        precheckKey.value = key
        precheckState.value = 'ready'
      }
      createError.value = `没有建任务：${e.message}`
    } else {
      createError.value = `没有建任务：${errorMessage(e)}`
    }
  } finally {
    creating.value = false
  }
}

onBeforeUnmount(() => {
  alive = false
  precheckSeq++
  labelSeq++
  if (precheckTimer) clearTimeout(precheckTimer)
  if (eventTimer) clearTimeout(eventTimer)
  eventTimer = null
})

const scopeHint = computed(() => {
  if (mode.value === 'resubmit') return ''
  return mode.value === 'item' ? '选中的条目' : '节点子树里的全部条目（含下级节点）'
})
</script>

<template>
  <aside
    class="flex w-[460px] shrink-0 flex-col border-l bg-background"
    data-testid="export-new"
    :data-mode="mode"
    :data-precheck-state="precheckState"
    :data-fresh="fresh ? 'true' : undefined"
  >
    <div class="flex h-11 shrink-0 items-center gap-2 border-b px-4">
      <span class="text-sm font-semibold text-foreground">新建导出</span>
      <span class="text-2xs text-muted-foreground">只包含已归档的文件</span>
      <span class="flex-1" />
      <Button variant="ghost" size="icon-xs" aria-label="关闭" data-testid="export-new-close" @click="emit('close')"><XIcon /></Button>
    </div>

    <div class="min-h-0 flex-1 space-y-5 overflow-y-auto px-4 py-4">
      <!-- 范围 -->
      <section>
        <div class="mb-1.5 flex items-center gap-2">
          <span class="text-xs font-medium text-muted-foreground">范围（只能从归类树选节点或条目）</span>
        </div>

        <div
          v-if="mode === 'resubmit'"
          class="rounded-lg border bg-info-soft/50 px-3 py-2.5 text-xs"
          data-testid="scope-resubmit"
          :data-resubmit-of="resubmitOf ?? ''"
        >
          <div class="font-medium text-foreground">补交{{ props.resubmitTitle ? `「${props.resubmitTitle}」` : '一个已完成的导出' }}的交付后变更</div>
          <div class="mt-0.5 text-muted-foreground">
            范围是那次导出里有变更、没删除的条目，由服务器按交付后变更算<template v-if="precheck?.resubmit_of">（现在 {{ precheck.item_count.toLocaleString() }} 件）</template>。
          </div>
          <button
            v-if="props.canPickScope"
            type="button"
            class="mt-1.5 text-info hover:underline"
            data-testid="scope-resubmit-manual"
            @click="toManual"
          >改为手选范围</button>
        </div>

        <template v-else>
          <div class="mb-2 inline-flex rounded-md border p-0.5 text-xs" role="tablist">
            <button
              type="button"
              class="rounded px-2.5 py-1"
              :class="mode === 'category' ? 'bg-muted font-medium text-foreground' : 'text-muted-foreground hover:text-foreground'"
              data-testid="scope-mode-category"
              @click="switchMode('category')"
            >按归类节点</button>
            <button
              type="button"
              class="rounded px-2.5 py-1"
              :class="mode === 'item' ? 'bg-muted font-medium text-foreground' : 'text-muted-foreground hover:text-foreground'"
              data-testid="scope-mode-item"
              @click="switchMode('item')"
            >按条目</button>
          </div>

          <div class="rounded-lg border" data-testid="scope-entries" :data-count="entries.length">
            <div v-if="!entries.length" class="px-3 py-3 text-xs text-muted-foreground">
              还没选范围。{{ mode === 'item' ? '从归类树里挑条目。' : '选一个或几个归类节点，导出它们子树里的全部条目。' }}
            </div>
            <ul v-else class="max-h-48 divide-y overflow-y-auto">
              <li
                v-for="e in entries.slice(0, 200)"
                :key="e.id"
                class="flex items-start gap-2 px-3 py-1.5 text-xs"
                :class="missing.has(e.id) && 'bg-destructive-soft/60'"
                data-testid="scope-entry"
                :data-id="e.id"
                :data-missing="missing.has(e.id) ? 'true' : undefined"
              >
                <FolderTreeIcon v-if="mode === 'category'" class="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
                <ListChecksIcon v-else class="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
                <div class="min-w-0 flex-1">
                  <div class="truncate font-medium text-foreground" :title="e.label ?? undefined">
                    <span v-if="e.label === null" class="text-muted-foreground">正在取名称…</span>
                    <template v-else>{{ e.label }}</template>
                  </div>
                  <div v-if="e.sub" class="truncate text-2xs text-muted-foreground" :title="e.sub">{{ e.sub }}</div>
                  <div v-if="missing.has(e.id)" class="text-2xs text-destructive">不存在、已删除或不在这个项目里</div>
                </div>
                <button type="button" class="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground" aria-label="移除" data-testid="scope-entry-remove" @click="removeEntry(e.id)">
                  <XIcon class="size-3.5" />
                </button>
              </li>
              <li v-if="entries.length > 200" class="px-3 py-1.5 text-2xs text-muted-foreground">另外 {{ (entries.length - 200).toLocaleString() }} 个没有列出</li>
            </ul>
          </div>
          <div class="mt-1.5 flex flex-wrap items-center gap-2">
            <Button
              v-if="mode === 'category'"
              size="xs"
              variant="outline"
              :disabled="!props.canPickScope"
              data-testid="scope-add-category"
              @click="categoryPickerOpen = true"
            ><PlusIcon />添加节点…</Button>
            <Button
              v-else
              size="xs"
              variant="outline"
              :disabled="!props.canPickScope"
              data-testid="scope-add-item"
              @click="itemPickerOpen = true"
            ><PlusIcon />从归类树选条目…</Button>
            <span class="text-2xs text-muted-foreground">{{ scopeHint }}</span>
          </div>
          <p v-if="scopeNotice && mode === 'item'" class="mt-1.5 rounded-md bg-warn-soft px-2.5 py-1.5 text-2xs text-warn" data-testid="scope-notice">
            {{ scopeNotice }}
          </p>
          <p v-if="!props.canPickScope" class="mt-1.5 text-2xs text-muted-foreground">
            选范围要用归类树（归类菜单），你的账号没有这个菜单；可以用任务列表里的"按原来的范围重新导出"或补交交付后变更。
          </p>
        </template>
      </section>

      <!-- 格式 -->
      <section>
        <div class="mb-1.5 text-xs font-medium text-muted-foreground">格式</div>
        <div class="grid grid-cols-2 gap-2">
          <button
            v-for="f in FORMAT_OPTIONS"
            :key="f.value"
            type="button"
            class="rounded-lg border px-3 py-2 text-left text-xs transition-colors"
            :class="[
              f.future ? 'cursor-not-allowed opacity-50' : 'hover:border-foreground/30',
              format === f.value ? 'border-foreground bg-muted/60 ring-1 ring-foreground/20' : '',
            ]"
            :disabled="f.future"
            data-testid="export-format"
            :data-value="f.value"
            :data-selected="format === f.value ? 'true' : undefined"
            :data-future="f.future ? 'true' : undefined"
            @click="!f.future && (format = f.value as ExportFormat)"
          >
            <div class="flex items-center gap-1.5 font-medium text-foreground">
              {{ f.label }}
              <span v-if="f.future" class="rounded bg-muted px-1 text-2xs font-normal text-muted-foreground">以后的版本</span>
              <CheckIcon v-else-if="format === f.value" class="ml-auto size-3.5" />
            </div>
            <div class="mt-0.5 text-2xs text-muted-foreground">{{ f.description }}</div>
          </button>
        </div>
        <p class="mt-1.5 text-2xs text-muted-foreground">两种都附目录.xlsx（每件一行）和清单.csv（每页一行，可以按 file_id、snapshot_id 反查）。</p>
      </section>

      <!-- 选项 -->
      <section>
        <div class="mb-1.5 text-xs font-medium text-muted-foreground">选项</div>
        <label class="flex cursor-pointer items-center gap-2 text-xs text-foreground" data-testid="export-only-approved" :data-checked="onlyApproved ? 'true' : 'false'">
          <Checkbox :model-value="onlyApproved" @update:model-value="(v) => (onlyApproved = v === true)" />
          只导出审核通过的文件
          <span class="text-2xs text-muted-foreground">（不勾选就包含所有已归档的文件）</span>
        </label>
        <p class="mt-1 text-2xs text-muted-foreground">
          每页用建任务那一刻的最新版本，明细里记下用的是哪一版；之后的重扫、改页序不影响这次导出，会出现在"交付后变更"里。
          <template v-if="precheck?.require_locked_item">项目设置要求导出的件已锁定。</template>
        </p>
      </section>

      <!-- 预检 -->
      <section
        data-testid="export-precheck"
        :data-state="precheckState"
        :data-fresh="fresh ? 'true' : undefined"
        :data-item-count="precheck?.item_count"
        :data-page-count="precheck?.page_count"
        :data-ready-items="precheck?.ready_item_count"
        :data-ready-pages="precheck?.ready_page_count"
        :data-not-ready="precheck?.not_ready_count"
        :data-not-ready-pages="precheck?.not_ready_page_count"
        :data-limit-exceeded="precheck?.page_limit_exceeded ? 'true' : undefined"
        :data-truncated="precheck?.not_ready_truncated ? 'true' : undefined"
      >
        <div class="mb-1.5 flex items-center gap-2">
          <span class="text-xs font-medium text-muted-foreground">预检</span>
          <LoaderCircleIcon v-if="precheckState === 'loading' || refreshing" class="size-3.5 animate-spin text-muted-foreground" />
          <span v-if="refreshing" class="text-2xs text-muted-foreground" data-testid="precheck-refreshing">项目里有变化，正在重新预检</span>
          <span class="flex-1" />
          <button
            v-if="body"
            type="button"
            class="inline-flex items-center gap-1 text-2xs text-muted-foreground hover:text-foreground"
            data-testid="precheck-rerun"
            @click="runPrecheck()"
          ><RefreshCwIcon class="size-3" />重新预检</button>
        </div>

        <div v-if="!body" class="rounded-lg border border-dashed px-3 py-3 text-xs text-muted-foreground">选好范围后自动预检。</div>
        <div v-else-if="precheckState === 'error'" class="rounded-lg bg-destructive-soft px-3 py-2 text-xs text-destructive" data-testid="precheck-error">
          {{ precheckError }}
          <button v-if="missing.size" type="button" class="ml-1 underline" data-testid="scope-remove-missing" @click="removeMissing">移除这些</button>
        </div>
        <div v-else-if="!precheck" class="flex items-center gap-2 rounded-lg border px-3 py-3 text-xs text-muted-foreground">
          <LoaderCircleIcon class="size-3.5 animate-spin" />正在预检…
        </div>
        <div v-else class="space-y-2" :class="!fresh && 'opacity-60'">
          <p v-if="refreshError" class="text-2xs text-muted-foreground" data-testid="precheck-refresh-error">
            重新预检失败（{{ refreshError }}），下面是上一次的结果；建任务时数据变了会提示。
          </p>
          <div class="grid grid-cols-3 gap-2 text-xs">
            <div class="rounded-lg border px-2.5 py-2">
              <div class="text-2xs text-muted-foreground">范围</div>
              <div class="mt-0.5 font-medium text-foreground tabular-nums" data-testid="precheck-scope">{{ precheck.item_count.toLocaleString() }} 件 · {{ precheck.page_count.toLocaleString() }} 页</div>
            </div>
            <div class="rounded-lg border px-2.5 py-2">
              <div class="text-2xs text-muted-foreground">就绪</div>
              <div class="mt-0.5 font-medium text-ok tabular-nums" data-testid="precheck-ready">{{ precheck.ready_item_count.toLocaleString() }} 件 · {{ precheck.ready_page_count.toLocaleString() }} 页</div>
            </div>
            <div class="rounded-lg border px-2.5 py-2">
              <div class="text-2xs text-muted-foreground">不就绪</div>
              <div class="mt-0.5 font-medium tabular-nums" :class="precheck.not_ready_count ? 'text-warn' : 'text-foreground'" data-testid="precheck-not-ready">
                {{ precheck.not_ready_count.toLocaleString() }} 件
              </div>
            </div>
          </div>

          <div
            v-if="precheck.page_limit_exceeded"
            class="flex items-start gap-2 rounded-lg bg-destructive-soft px-3 py-2 text-xs text-destructive"
            data-testid="precheck-page-limit"
          >
            <CircleAlertIcon class="mt-px size-3.5 shrink-0" />
            <div>
              {{ precheck.page_limit_message }}
              <div class="mt-0.5 text-destructive">在范围里分别选下级节点，分几次导出（每次最多 {{ precheck.max_page_count.toLocaleString() }} 页）。</div>
            </div>
          </div>

          <div v-if="precheck.item_count === 0" class="rounded-lg border px-3 py-2 text-xs text-muted-foreground" data-testid="precheck-empty">
            范围里没有条目（下级节点里也没有）。
          </div>
          <div v-else-if="precheck.not_ready_count === 0" class="flex items-center gap-2 rounded-lg bg-ok-soft px-3 py-2 text-xs text-ok" data-testid="precheck-all-ready">
            <CheckIcon class="size-3.5" />全部 {{ precheck.item_count.toLocaleString() }} 件都可以导出
          </div>
          <div v-else class="rounded-lg bg-warn-soft/70 px-3 py-2 text-xs" data-testid="precheck-issues" :data-shown="shownIssues.length">
            <div class="flex items-center gap-1.5 font-medium text-warn">
              <TriangleAlertIcon class="size-3.5" />预检：{{ precheck.not_ready_count.toLocaleString() }} 个条目不满足条件
            </div>
            <div class="mt-1 flex flex-wrap gap-1">
              <span v-for="r in reasonSummary" :key="r.code" class="rounded bg-background/70 px-1.5 leading-5 text-foreground" :data-reason="r.code">{{ r.text }}</span>
            </div>
            <ul class="mt-1.5 space-y-1" :class="showAll && 'max-h-72 overflow-y-auto pr-1'">
              <li
                v-for="it in shownIssues"
                :key="it.item_id"
                class="text-foreground"
                data-testid="precheck-issue"
                :data-item-id="it.item_id"
                :data-reasons="it.reasons.map((r) => r.code).join(',')"
              >
                <span class="font-mono font-medium">{{ itemName(it.archive_no, it.item_no) }}</span>
                <span class="text-muted-foreground">：{{ shortIssue(it) }}</span>
                <template v-if="showAll">
                  <div class="truncate pl-3 text-2xs text-muted-foreground" :title="`${it.title} · ${it.category_names.join(' / ')}`">
                    {{ it.title || '（未填题名）' }} · {{ it.category_names.join(' / ') }} · {{ it.page_count }} 页
                  </div>
                  <div v-for="r in it.reasons" :key="r.code" class="pl-3 text-2xs text-warn">{{ r.message }}</div>
                </template>
              </li>
            </ul>
            <p v-if="showAll && precheck.not_ready_truncated" class="mt-1.5 text-2xs text-muted-foreground" data-testid="precheck-truncated">
              只列出了前 {{ precheck.not_ready.length }} 件；上面的件数是全部的。
            </p>
            <button
              v-if="precheck.not_ready.length"
              type="button"
              class="mt-1.5 text-info hover:underline"
              data-testid="precheck-show-all"
              @click="showAll = !showAll"
            >{{ showAll ? '收起' : `查看全部问题（${precheck.not_ready_count.toLocaleString()} 件）` }}</button>
            <p class="mt-1.5 text-2xs text-muted-foreground">可以只导出就绪的，也可以先回归类里处理这些条目再导出。</p>
          </div>
        </div>
      </section>
    </div>

    <div class="shrink-0 border-t px-4 py-3">
      <p v-if="createError" class="mb-2 rounded-md bg-destructive-soft px-2.5 py-1.5 text-xs text-destructive" data-testid="export-create-error">{{ createError }}</p>
      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" data-testid="export-new-cancel" @click="emit('close')">取消</Button>
        <Button
          size="sm"
          class="flex-1"
          :disabled="submit.disabled || creating"
          data-testid="export-submit"
          :data-only-ready="submit.onlyReady ? 'true' : 'false'"
          @click="create"
        >
          <LoaderCircleIcon v-if="creating" class="animate-spin" />{{ submit.label }}
        </Button>
      </div>
    </div>

    <CategoryPicker
      v-model:open="categoryPickerOpen"
      :project-id="props.projectId"
      allow-top
      :disabled-ids="categories.map((e) => e.id)"
      title="选择要导出的归类节点"
      description="导出这个节点子树里的全部条目（含下级节点）；选“顶层”是整个项目。"
      confirm-text="加入导出范围"
      @confirm="onPickCategory"
    />
    <ExportItemPicker
      v-model:open="itemPickerOpen"
      :project-id="props.projectId"
      :chosen="items.map((e) => e.id)"
      @confirm="onPickItems"
    />
  </aside>
</template>
