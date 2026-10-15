<script setup lang="ts">
// 挂接工作台的待归类池（M3-6，归类页的第三栏，参考 plan.html p-catalog Mockup 的右栏；用户决定 Q4）。
//
// - **范围**：只看选中的文件夹里直接放的文件（子文件夹在上面的目录树里选），数据复用 children 接口（files 或 scan 菜单，
//   内置归类员有 files）；文件夹记在地址栏 `?pool=`（页面管），换文件夹后目录树收起，页头点文件夹名再展开。
// - **筛选**：默认"仅未归类"、全部审核状态（Q4）；可以关掉"仅未归类"、按审核状态筛。
// - **列表**：FileGrid 的 dense 图标视图（虚拟滚动，2000 页的文件夹 DOM 里也只有可见的几行），名字前是显示位置"#n"；
//   已归类的下面写"在 0012 第 3 页"（本条目的写"本条目第 3 页"，悬停看归类节点、档号、题名）：只给看得到的已归类文件
//   取 catalog-placements（每批最多 500 个，去抖 120 ms，按 file_id 缓存）。
// - **选择**：单击、Ctrl/⌘ 加选、Shift 区间（按显示位置）、Ctrl/⌘+A；另有"第 a–b 张"输入（同样按显示位置）。
// - **操作**（按钮的可用与否、文字由页面给）：挂到当前条目末尾、插到页序条选中的页之后、用选中的文件新建条目；
//   也可以把选中的文件拖进页序条（拖没选中的文件就只拖它）。双击看原图（页面打开 OriginalViewerDialog）。
// - **实时**（页面转过来）：`files.changed` 涉及当前文件夹或列表里的文件时重新取列表（已归类、审核状态、版本），并作废
//   这些文件的归类位置；`folders.changed` 刷新目录树，涉及当前文件夹或它的祖先时重新打开（被删了回到没有选中）；
//   `catalog.changed`：条目变了的、或者只有节点变了（`item_ids` 为空或没带：路径显示可能变了，design §7）时重取看得到的
//   归类位置。同一个文件夹重新加载时保留选择（不在了的自动去掉），换文件夹才清空。
// - **文件夹用不了**（已删除、不在当前项目里、进不了、地址栏里的不是文件夹）：池在本地回到空闲，发 `invalid` 让页面清掉
//   地址栏和记住的这一项。取文件夹用 probeFolder（403 forbidden 不走全局处理，M3-6 审查第 1 轮）。
// - 截图脚本用的属性：根 data-testid=catalog-pool（data-folder-id、data-state、data-count、data-selected、data-range、
//   data-only-uncataloged）；格子沿用 FileGrid 的 file-cell（data-file-id、data-position）；"在哪个条目"那一行
//   data-testid=file-note（data-item-id、data-page-no、data-path）。
import { ChevronDownIcon, ChevronRightIcon, FolderIcon, FolderOpenIcon, ListPlusIcon, LoaderCircleIcon, PlusIcon } from '@lucide/vue'
import { computed, onBeforeUnmount, ref, shallowReactive, watch } from 'vue'
import { toast } from 'vue-sonner'

import { getCatalogPlacements } from '@/api/catalog'
import { probeFolder } from '@/api/folders'
import { errorMessage, isApiError } from '@/api/http'
import type { CatalogPlacement, FileEntry, FolderItem, ReviewStatus } from '@/api/types'
import GuardedButton from '@/components/app/GuardedButton.vue'
import FolderTree from '@/components/files/FolderTree.vue'
import { Button } from '@/components/ui/button'
import { useFolderTree } from '@/composables/useFolderTree'
import { useSelection } from '@/composables/useSelection'
import { formatItemNo, placementNo } from '@/lib/catalog'
import { REVIEW_STATUS } from '@/lib/files'
import { useImageInfoStore } from '@/stores/image-info'
import { type Entry, fileKey, type FileItemEntry, toEntries } from '@/views/files/entries'
import FileGrid, { type GridNote } from '@/views/files/FileGrid.vue'

import { dragPayload, POOL_MIME, setDragLabel } from './drag'

/** 池底部按钮的状态（页面按当前条目、页序条的选择算好）。`reason` 不为空时按钮禁用、悬停看原因。 */
export interface PoolAction {
  label: string
  reason: string | null
}

const props = defineProps<{
  projectId: string
  rootFolderId: string
  projectName: string
  /** 当前文件夹（地址栏 `?pool=`）。 */
  folderId: string | null
  /** 当前打开的条目（"本条目第 n 页"）。 */
  currentItemId: string | null
  itemNoWidth: number
  linkAction: PoolAction
  insertAction: PoolAction
  createAction: PoolAction
  /** 有写操作在路上。 */
  busy: boolean
}>()

const emit = defineEmits<{
  'update:folderId': [id: string | null]
  /** 这个文件夹用不了（已删除、不在这个项目里、进不了）：页面清掉地址栏和记住的这一项。 */
  invalid: [id: string]
  link: [files: FileEntry[], mode: 'end' | 'after']
  create: [files: FileEntry[]]
  open: [files: FileEntry[], fileId: string]
}>()

const images = useImageInfoStore()
const tree = useFolderTree()

// ---------- 目录树 ----------

const treeOpen = ref(!props.folderId)
let treeProject: string | null = null

function resetTree() {
  treeProject = props.projectId
  tree.reset({ id: props.rootFolderId, folder_name: props.projectName })
  void tree.load(props.rootFolderId).catch(() => null)
}

function pickFolder(id: string) {
  emit('update:folderId', id)
  treeOpen.value = false
}

// ---------- 当前文件夹和列表 ----------

const folder = ref<FolderItem | null>(null)
const files = ref<FileEntry[] | null>(null)
const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const loadError = ref('')
let openSeq = 0
let listSeq = 0
let alive = true

/** 当前文件夹的路径（从项目根文件夹的下一级起）。 */
const folderPath = computed(() => {
  const f = folder.value
  if (!f) return ''
  if (!f.parent_id) return props.projectName
  return [...f.ancestors.slice(1).map((a) => a.folder_name), f.folder_name].join(' / ')
})

/** 回到"没有选文件夹"（目录树展开）。 */
function toIdle() {
  openSeq++
  listSeq++
  folder.value = null
  files.value = null
  state.value = 'idle'
  treeOpen.value = true
  selection.clear()
}

/**
 * 当前文件夹用不了了（已删除、不在这个项目里、进不了）：池先在本地回到空闲（M3-6 审查：原来要等页面改地址，
 * 文件夹来自 localStorage 时地址不变，池就一直停在已删的文件夹上），再告诉页面清掉记住的这一项。
 */
function folderUnusable(id: string, message: string) {
  toIdle()
  toast.info(message)
  emit('invalid', id)
}

async function openFolder(id: string | null) {
  const my = ++openSeq
  if (!id) {
    toIdle()
    return
  }
  // 换了文件夹才清掉选择；同一个文件夹重新打开（folders.changed、断线重连）时保留，已经不在的由 prune 去掉
  // （M3-6 审查）。
  if (folder.value?.id !== id) {
    selection.clear()
    folder.value = null
    files.value = null
  }
  state.value = 'loading'
  try {
    // 地址栏的 ?pool= 可能是别的项目（进不了）的文件夹：用不走全局 403 处理的探测接口，进不了就当作没有。
    const detail = await probeFolder(id, ['not_found', 'forbidden', 'invalid_input'])
    if (my !== openSeq) return
    if (detail.project_id !== props.projectId) {
      folderUnusable(id, '待归类池的文件夹不在当前项目里，请重新选一个')
      return
    }
    folder.value = detail
    void tree.revealPath(detail).catch(() => null)
    await reload()
  } catch (e) {
    if (my !== openSeq) return
    if (isApiError(e, 'not_found') || isApiError(e, 'forbidden') || isApiError(e, 'invalid_input')) {
      folderUnusable(id, '待归类池的文件夹已不存在或进不了（可能刚被删除），请重新选一个')
      return
    }
    state.value = 'error'
    loadError.value = errorMessage(e)
  }
}

/** 重新取当前文件夹里的文件（同时更新树上这一层）。 */
async function reload() {
  const id = folder.value?.id
  if (!id) return
  const my = ++listSeq
  try {
    const res = await tree.load(id)
    if (my !== listSeq || folder.value?.id !== id) return
    if (!res) {
      folderUnusable(id, '待归类池的文件夹已被删除，请重新选一个')
      return
    }
    files.value = res.files
    state.value = 'ready'
  } catch (e) {
    if (my !== listSeq) return
    if (files.value) state.value = 'ready'
    else {
      state.value = 'error'
      loadError.value = errorMessage(e)
    }
  }
}

// ---------- 筛选 ----------

const onlyUncataloged = ref(true)
type StatusFilter = 'all' | ReviewStatus
const statusFilter = ref<StatusFilter>('all')
const STATUS_OPTIONS: ReviewStatus[] = ['unassigned', 'pending', 'approved', 'rejected']

/** batch 说不存在、无权访问、已删除的文件从列表里移除（design §4.4）。 */
const shown = computed(() =>
  (files.value ?? []).filter(
    (f) =>
      !images.isMissing(f.file_id) &&
      (!onlyUncataloged.value || !f.cataloged) &&
      (statusFilter.value === 'all' || f.review_status === statusFilter.value),
  ),
)
const entries = computed<Entry[]>(() => toEntries([], shown.value))
const total = computed(() => (files.value ?? []).length)
const catalogedCount = computed(() => (files.value ?? []).filter((f) => f.cataloged).length)

// ---------- 选择、区间 ----------

const selection = useSelection(() => entries.value.map((e) => e.key))
watch(entries, () => selection.prune())
const selectedFiles = computed(() => shown.value.filter((f) => selection.isSelected(fileKey(f.file_id))))
/** 选中的连续一段（按显示位置），不连续时为 null。 */
const selectedRange = computed(() => {
  const keys = entries.value.map((e) => e.key)
  const pos = keys.flatMap((k, i) => (selection.isSelected(k) ? [i + 1] : []))
  if (!pos.length) return null
  const a = pos[0]!
  const b = pos[pos.length - 1]!
  return b - a + 1 === pos.length ? { a, b } : null
})

/** 没选文件时按钮禁用的原因。 */
const noSelection = computed(() => (selection.count.value ? null : '先在上面选文件（单击、Shift 连续选，或者输入"第 a–b 张"）'))

const rangeA = ref('')
const rangeB = ref('')
const rangeError = ref('')
const gridRef = ref<InstanceType<typeof FileGrid> | null>(null)

function applyRange() {
  const n = entries.value.length
  const a = Number(rangeA.value.trim())
  const b = rangeB.value.trim() ? Number(rangeB.value.trim()) : a
  if (!Number.isInteger(a) || !Number.isInteger(b) || a < 1 || b < a || b > n) {
    rangeError.value = n ? `要是 1–${n} 之间的两个整数，前一个不大于后一个` : '列表是空的'
    return
  }
  rangeError.value = ''
  const keys = entries.value.slice(a - 1, b).map((e) => e.key)
  selection.set(keys)
  gridRef.value?.scrollToKey(keys[0]!)
}
watch([rangeA, rangeB], () => (rangeError.value = ''))

// ---------- 归类位置（只取看得到的已归类文件） ----------

const placements = shallowReactive(new Map<string, CatalogPlacement | null>())
let visibleIds: string[] = []
let placementTimer: ReturnType<typeof setTimeout> | null = null
/** 作废时加一：路上的结果作废（回来后不写缓存，再取一次）。 */
let placementGen = 0
const placementInflight = new Set<string>()

function onVisible(ids: string[]) {
  visibleIds = ids
  schedulePlacements()
}

function schedulePlacements() {
  if (placementTimer) clearTimeout(placementTimer)
  placementTimer = setTimeout(() => void fetchPlacements(), 120)
}

async function fetchPlacements() {
  placementTimer = null
  const byId = new Map((files.value ?? []).map((f) => [f.file_id, f]))
  const want = visibleIds.filter((id) => byId.get(id)?.cataloged && !placements.has(id) && !placementInflight.has(id))
  if (!want.length) return
  const gen = placementGen
  const pid = props.projectId
  want.forEach((id) => placementInflight.add(id))
  try {
    for (let i = 0; i < want.length; i += 500) {
      const chunk = want.slice(i, i + 500)
      const res = await getCatalogPlacements(pid, chunk, true)
      if (!alive || gen !== placementGen || pid !== props.projectId) continue
      for (const it of res.items) placements.set(it.file_id, it.placement)
      for (const id of res.missing) placements.set(id, null)
    }
  } catch {
    // 取不到就不显示"在哪个条目"，下一次可见范围变化时再取。
  } finally {
    want.forEach((id) => placementInflight.delete(id))
    if (alive && gen !== placementGen) schedulePlacements()
  }
}

/** 作废归类位置：给了 file_id 只作废这些，否则全部；然后重取看得到的。 */
function invalidatePlacements(fileIds?: Iterable<string>) {
  if (fileIds) for (const id of fileIds) placements.delete(id)
  else placements.clear()
  placementGen++
  schedulePlacements()
}

function placementTitle(p: CatalogPlacement): string {
  return [p.category_names.join(' / '), placementNo(p), p.title || '（还没填题名）', `第 ${p.page_no} 页`].join(' · ')
}

function note(f: FileEntry): GridNote | null {
  if (!f.cataloged) return null
  const p = placements.get(f.file_id)
  if (!p) return { text: placements.has(f.file_id) ? '已归类' : '已归类…', tone: 'violet' }
  const mine = p.item_id === props.currentItemId
  return {
    text: mine ? `本条目第 ${p.page_no} 页` : `在 ${formatItemNo(p.item_no, props.itemNoWidth)} 第 ${p.page_no} 页`,
    title: placementTitle(p),
    tone: mine ? 'info' : 'violet',
    data: { 'data-item-id': p.item_id, 'data-page-no': p.page_no, 'data-path': p.category_names.join('/') },
  }
}

// ---------- 拖出去、看原图 ----------

function onDragStart(entry: FileItemEntry, ev: DragEvent) {
  if (!ev.dataTransfer) return
  if (!selection.isSelected(entry.key)) selection.click(entry.key)
  const list = selectedFiles.value.length ? selectedFiles.value : [entry.file]
  dragPayload.value = {
    kind: 'pool',
    files: list.map((f) => ({
      file_id: f.file_id,
      file_name: f.file_name,
      folder_id: folder.value?.id,
      review_status: f.review_status,
      review_round: f.review_round,
      version_no: f.version_no,
    })),
  }
  ev.dataTransfer.effectAllowed = 'copy'
  ev.dataTransfer.setData(POOL_MIME, list.map((f) => f.file_id).join(','))
  ev.dataTransfer.setData('text/plain', list.map((f) => f.file_name).join('\n'))
  setDragLabel(ev, list.length > 1 ? `${list.length} 个文件` : list[0]!.file_name)
}

function onDragEnd() {
  dragPayload.value = null
}

function onOpen(entry: Entry) {
  if (entry.kind === 'file') emit('open', shown.value, entry.file.file_id)
}

// ---------- 对外 ----------

/** files.changed：涉及当前文件夹（folderIds）或列表里的文件时重新取列表；没带 folder_ids、file_ids 时（不知道范围）也取。 */
function onFilesChanged(folderIds: ReadonlySet<string> | null, fileIds: ReadonlySet<string>) {
  if (fileIds.size) invalidatePlacements(fileIds)
  const id = folder.value?.id
  if (!id) return
  const mine = !folderIds || folderIds.has(id) || (files.value ?? []).some((f) => fileIds.has(f.file_id))
  if (mine) void reload()
}

/** folders.changed：刷新目录树；当前文件夹可能改名、移动、被删，重新打开一次。 */
function onFoldersChanged(folderIds: ReadonlySet<string> | null) {
  if (folderIds?.size) void tree.refresh(folderIds)
  else void tree.refreshAll()
  const f = folder.value
  // 事件可能只带被删、被移动的文件夹的上级：当前文件夹或它的祖先在里面就重新打开（被删了会回到空闲）。
  if (f && (!folderIds || folderIds.has(f.id) || f.ancestors.some((a) => folderIds.has(a.id)))) void openFolder(f.id)
}

/**
 * catalog.changed：`itemIds` 非空时作废挂在这些条目里的文件的位置（页号、档号、题名可能变了）；空数组或没带（只有节点
 * 变了，或者超过 500 个条目）时归类路径可能变了，全部作废。
 */
function onCatalogChanged(itemIds: ReadonlySet<string> | null) {
  if (!itemIds || itemIds.size === 0) {
    if (placements.size) invalidatePlacements()
    return
  }
  const hit = [...placements.entries()].filter(([, p]) => p && itemIds.has(p.item_id)).map(([id]) => id)
  if (hit.length) invalidatePlacements(hit)
}

/** 挂接成功之后：这些文件马上标成已归类（"仅未归类"时从列表里消失），等 files.changed 再取一次。 */
function markCataloged(fileIds: string[]) {
  const set = new Set(fileIds)
  if (files.value) files.value = files.value.map((f) => (set.has(f.file_id) ? { ...f, cataloged: true } : f))
  invalidatePlacements(fileIds)
  selection.prune()
}

// ---------- 项目、文件夹（放在最后：immediate 的 watch 要用到上面声明的状态） ----------

watch(
  () => [props.projectId, props.rootFolderId] as const,
  () => {
    if (treeProject !== props.projectId) {
      resetTree()
      placements.clear()
    }
  },
  { immediate: true },
)
watch(
  () => props.folderId,
  (id) => void openFolder(id),
  { immediate: true },
)

defineExpose({
  reload,
  refresh: async () => {
    invalidatePlacements()
    void tree.refreshAll()
    if (folder.value) await openFolder(folder.value.id)
  },
  onFilesChanged,
  onFoldersChanged,
  onCatalogChanged,
  markCataloged,
  clearSelection: () => selection.clear(),
})

onBeforeUnmount(() => {
  alive = false
  openSeq++
  listSeq++
  if (placementTimer) clearTimeout(placementTimer)
})
</script>

<template>
  <aside
    class="flex w-[340px] shrink-0 flex-col border-l bg-sidebar/40"
    data-testid="catalog-pool"
    :data-folder-id="folder?.id ?? undefined"
    :data-state="state"
    :data-count="entries.length"
    :data-selected="selection.count.value"
    :data-range="selectedRange ? `${selectedRange.a}-${selectedRange.b}` : undefined"
    :data-only-uncataloged="onlyUncataloged ? 'true' : 'false'"
  >
    <!-- 页头：文件夹（点开目录树换） -->
    <div class="flex h-9 shrink-0 items-center gap-1.5 border-b px-3 pt-0.5">
      <span class="shrink-0 text-xs font-medium text-muted-foreground">待归类</span>
      <button
        type="button"
        class="flex min-w-0 flex-1 items-center gap-1 rounded-md px-1.5 py-0.5 text-left text-xs hover:bg-muted"
        :title="folder ? `${folderPath}${folder.remark ? `（${folder.remark}）` : ''}：点这里换文件夹` : '选一个文件夹'"
        data-testid="pool-folder-toggle"
        :data-open="treeOpen ? 'true' : 'false'"
        @click="treeOpen = !treeOpen"
      >
        <component :is="treeOpen ? FolderOpenIcon : FolderIcon" class="size-3.5 shrink-0 fill-amber-300/60 text-amber-600" />
        <span class="min-w-0 flex-1 truncate font-medium text-foreground">{{ folder ? folderPath : '选一个文件夹' }}</span>
        <component :is="treeOpen ? ChevronDownIcon : ChevronRightIcon" class="size-3.5 shrink-0 text-muted-foreground" />
      </button>
    </div>

    <div v-if="treeOpen" class="max-h-[40%] min-h-[120px] shrink-0 overflow-y-auto border-b px-2 py-1.5" data-testid="pool-tree">
      <FolderTree :tree="tree" :selected-id="folder?.id ?? null" @select="pickFolder" @activate="pickFolder" />
    </div>

    <template v-if="folder">
      <!-- 筛选 -->
      <div class="flex shrink-0 flex-wrap items-center gap-1.5 px-3 pt-2 text-xs">
        <button
          type="button"
          class="rounded-full px-2 leading-6 transition-colors"
          :class="onlyUncataloged ? 'bg-foreground text-background' : 'bg-muted text-muted-foreground hover:text-foreground'"
          :aria-pressed="onlyUncataloged"
          :title="onlyUncataloged ? `已归类的 ${catalogedCount} 张没有显示，点一下全部显示` : '只看还没归类的'"
          data-testid="pool-only-uncataloged"
          @click="onlyUncataloged = !onlyUncataloged"
        >仅未归类</button>
        <select
          v-model="statusFilter"
          class="h-6 rounded-full border-0 bg-muted px-2 text-xs text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          data-testid="pool-status-filter"
        >
          <option value="all">全部审核状态</option>
          <option v-for="s in STATUS_OPTIONS" :key="s" :value="s">{{ REVIEW_STATUS[s].label }}</option>
        </select>
        <span class="ml-auto text-muted-foreground tabular-nums" data-testid="pool-count">
          {{ entries.length === total ? `共 ${total} 张` : `${entries.length} / ${total} 张` }}
        </span>
      </div>
      <!-- 区间 -->
      <form class="flex shrink-0 items-center gap-1 px-3 pt-1.5 text-xs" data-testid="pool-range" @submit.prevent="applyRange">
        <span class="text-muted-foreground">第</span>
        <input
          v-model="rangeA"
          class="h-6 w-12 rounded border border-input bg-background px-1.5 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          inputmode="numeric"
          aria-label="从第几张"
          data-testid="pool-range-from"
        >
        <span class="text-muted-foreground">–</span>
        <input
          v-model="rangeB"
          class="h-6 w-12 rounded border border-input bg-background px-1.5 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          inputmode="numeric"
          aria-label="到第几张"
          data-testid="pool-range-to"
        >
        <span class="text-muted-foreground">张</span>
        <Button type="submit" size="xs" variant="outline" :disabled="!entries.length" data-testid="pool-range-apply">选中</Button>
        <span class="ml-auto truncate text-muted-foreground" data-testid="pool-selection-text">
          <template v-if="selection.count.value">
            {{ selectedRange && selection.count.value > 1 ? `#${selectedRange.a}–#${selectedRange.b} · ` : '已选 ' }}{{ selection.count.value }} 张
          </template>
          <template v-else>Shift 连续选</template>
        </span>
      </form>
      <p v-if="rangeError" class="shrink-0 px-3 pt-1 text-[11px] text-destructive" data-testid="pool-range-error">{{ rangeError }}</p>

      <!-- 列表 -->
      <div v-if="state === 'loading' && !files" class="flex flex-1 items-center justify-center gap-2 text-xs text-muted-foreground">
        <LoaderCircleIcon class="size-3.5 animate-spin" />正在加载文件…
      </div>
      <div v-else-if="state === 'error' && !files" class="flex flex-1 flex-col items-center justify-center gap-2 px-6 text-center text-xs">
        <p class="text-destructive">{{ loadError }}</p>
        <Button variant="outline" size="xs" @click="openFolder(folder.id)">重试</Button>
      </div>
      <div
        v-else-if="!entries.length"
        class="flex flex-1 flex-col items-center justify-center gap-1 px-6 text-center text-xs text-muted-foreground"
        data-testid="pool-empty"
      >
        <span>{{ total ? '没有符合筛选的文件' : '这个文件夹里没有直接放的文件' }}</span>
        <span v-if="total && onlyUncataloged && catalogedCount === total">这里的 {{ total }} 张都已归类</span>
        <span v-else-if="!total">子文件夹里的文件在上面的目录树里选</span>
      </div>
      <div v-else class="mt-1.5 flex min-h-0 flex-1 flex-col border-t">
        <FileGrid
          ref="gridRef"
          :entries="entries"
          view="icons"
          :selection="selection"
          dense
          :note="note"
          draggable-files
          visible-owner="catalog-pool"
          :overscan="1"
          @open="onOpen"
          @visible="onVisible"
          @dragstart="onDragStart"
          @dragend="onDragEnd"
        />
      </div>

      <!-- 操作 -->
      <div class="flex shrink-0 flex-col gap-1.5 border-t px-3 py-2.5" data-testid="pool-actions">
        <GuardedButton
          size="sm"
          :reason="props.linkAction.reason ?? noSelection"
          :disabled="props.busy"
          class="w-full min-w-0 justify-center"
          :title="props.linkAction.label"
          data-testid="pool-link-end"
          @click="emit('link', selectedFiles, 'end')"
        >
          <ListPlusIcon /><span class="min-w-0 truncate">{{ props.linkAction.label }}</span><span v-if="selection.count.value" class="shrink-0">（{{ selection.count.value }}）</span>
        </GuardedButton>
        <div class="flex gap-1.5">
          <GuardedButton
            size="sm"
            variant="outline"
            :reason="props.insertAction.reason ?? noSelection"
            :disabled="props.busy"
            class="min-w-0 flex-1 justify-center"
            data-testid="pool-link-after"
            @click="emit('link', selectedFiles, 'after')"
          >
            <span class="truncate">{{ props.insertAction.label }}</span>
          </GuardedButton>
          <GuardedButton
            size="sm"
            variant="outline"
            :reason="props.createAction.reason ?? noSelection"
            :disabled="props.busy"
            class="min-w-0 flex-1 justify-center"
            data-testid="pool-create-item"
            @click="emit('create', selectedFiles)"
          >
            <PlusIcon /><span class="truncate">{{ props.createAction.label }}</span>
          </GuardedButton>
        </div>
      </div>
    </template>
    <div v-else-if="!treeOpen" class="flex flex-1 items-center justify-center px-6 text-center text-xs text-muted-foreground">
      点上面选一个文件夹，挑出要挂接的文件
    </div>
    <div v-else class="flex flex-1 items-start justify-center px-6 pt-4 text-center text-xs text-muted-foreground">
      在上面的目录树里选一个文件夹（只看它里面直接放的文件）
    </div>
  </aside>
</template>
