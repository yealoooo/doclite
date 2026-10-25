<script setup lang="ts">
// 文件目录页（M1-10：浏览和看图；整理和上传在 M1-12）。参考 plan.html 的 p-files Mockup。
//
// - 当前项目取自项目切换器（stores/project.ts）；当前文件夹在地址栏 `?folder=<id>`（没有就是根文件夹），
//   前进后退能在文件夹之间切换。打开别的项目里的文件夹链接时，能进那个项目就自动切过去。
// - 左侧目录树（FolderTree，按需加载）；顶部面包屑（`GET /api/folders/{id}`）；主区图标和列表两种视图
//   （FileGrid，按行虚拟化）；右侧详情（FileDetailPanel）；快照历史对话框。
// - 排列（M11-4）：文件夹摘要那一行的下拉框——扫描顺序（默认，children 接口的顺序 sort_no、名称）、文件名、入库时间，
//   只在本页排，记在 localStorage。
// - 文件名搜索（M11-4）：顶栏的搜索框（去抖 300 ms）搜当前文件夹和它的子文件夹（FileSearchResults 替换主区），点一个
//   结果跳到它的文件夹并选中、滚到它；Esc 或清空回到列表。
// - 调序（M11-4）：选中文件的"移到第…页"（右键菜单、选择栏；MoveToPositionDialog）。只在按扫描顺序排、没在搜索、
//   文件夹没封存时可用，否则置灰并写原因。
// - 实时：订阅当前项目的 SSE（useProjectStream）。`folders.changed`、`files.changed` 去抖后只刷新受影响的
//   文件夹（当前文件夹、它的子文件夹、面包屑上的、树上已加载的）；`resync` 或断线重连后整体刷新。
//   当前文件夹被删掉时回到上一级。
// - 整理（M1-12，useFileActions）：新建文件夹、改名、改备注、移动、复制、删除、下载原图；右键菜单包在主区外面，
//   按单元格的 data-key 定位条目（右键没选中的条目时先单选它）；底部的选择栏放同样的操作；F2、Delete、Ctrl/⌘+C。
// - 上传（M1-12）：只在客户端里，调 `pick_and_enqueue_upload`（上传进度在 /scan 页看）；浏览器里提示"请在客户端中使用"。
// - 查看原图（M2-4，Q11）：文件的右键菜单"查看原图"打开全屏的 OriginalViewerDialog（OpenSeadragon），可以在当前文件夹的
//   文件之间切换、对比上一版。
// - 重扫件（M2-7）：文件夹摘要那一行的"只看已驳回"筛选（只筛当前文件夹直接放的文件，子文件夹照常显示）；一个已驳回的
//   文件在右键菜单和选择栏里有"上传重扫件"（RescanDialog：客户端里走 `pick_and_rescan`）。
// - 封存（M9，requirements 5.5"封存"、5.9"/files 也显示同样的横条和状态"）：打开的文件夹已归档时顶部横条（SealBanner：
//   紫色已封存、青色修改中）。已封存的文件夹里新建文件夹、上传、复制、改名、移动、删除的按钮和右键菜单置灰并写原因
//   （重扫、看图、下载、改备注照常）；子文件夹本身封存时它的改名、移动、删除置灰。有 catalog 菜单的人横条上能直接
//   "解除封存去修改…"（UnsealDialog），修改中的给"改完了，去归类页重新归档"；没有的写"请联系归类员"。子文件夹的格子、
//   行上显示归档状态（FileGrid）；树上修改中的文件夹带青点。任何写操作碰到 409 `folder_archived`（例如把文件移进
//   封存的文件夹）给 server 的说明和"去归类页查看"（useFileActions）。
import {
  ChevronRightIcon,
  CopyIcon,
  DownloadIcon,
  EyeIcon,
  FilterIcon,
  FolderInputIcon,
  FolderOpenIcon,
  FolderPlusIcon,
  FolderUpIcon,
  HistoryIcon,
  ImageIcon,
  ListOrderedIcon,
  LoaderCircleIcon,
  NotebookPenIcon,
  PencilIcon,
  PencilLineIcon,
  ScanLineIcon as RescanIcon,
  SearchIcon,
  Trash2Icon,
  UploadIcon,
  XIcon,
} from '@lucide/vue'
import { watchDebounced } from '@vueuse/core'
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import { getFolder } from '@/api/folders'
import { isApiError } from '@/api/http'
import type { ArchiveItem, FileEntry, FolderSearchHit, ProjectEvent } from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import ErrorState from '@/components/app/ErrorState.vue'
import GuardedButton from '@/components/app/GuardedButton.vue'
import PageHeader from '@/components/app/PageHeader.vue'
import StreamStatus from '@/components/app/StreamStatus.vue'
import SealBanner from '@/components/catalog/SealBanner.vue'
import UnsealDialog from '@/components/catalog/UnsealDialog.vue'
import { type Entry, fileKey, sortFiles, toEntries, useSortMode, useViewMode } from '@/components/files/entries'
import FileGrid, { type GridCommand } from '@/components/files/FileGrid.vue'
import FolderPicker, { type PickedFolder } from '@/components/files/FolderPicker.vue'
import FolderTree from '@/components/files/FolderTree.vue'
import RescanDialog from '@/components/files/RescanDialog.vue'
import SnapshotHistoryDialog from '@/components/files/SnapshotHistoryDialog.vue'
import ViewModeToggle from '@/components/files/ViewModeToggle.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuShortcut,
  ContextMenuTrigger,
} from '@/components/ui/context-menu'
import { useFolderBrowser } from '@/composables/useFolderBrowser'
import { useFolderTree } from '@/composables/useFolderTree'
import { useEventBatch, useProjectStream } from '@/composables/useProjectStream'
import { useSelection } from '@/composables/useSelection'
import { treeFolderModifying } from '@/lib/catalog'
import type { RescanTarget } from '@/lib/rescan'
import { useImageInfoStore } from '@/stores/image-info'
import { useProjectStore } from '@/stores/project'
import { useSessionStore } from '@/stores/session'

import FileDetailPanel from './FileDetailPanel.vue'
import FileSearchResults from './FileSearchResults.vue'
import FolderFormDialog from './FolderFormDialog.vue'
import MoveToPositionDialog from './MoveToPositionDialog.vue'
import RenameFileDialog from './RenameFileDialog.vue'
import { useFileActions } from './useFileActions'

// 查看原图的对话框（带 OpenSeadragon）第一次打开时才加载。
const OriginalViewerDialog = defineAsyncComponent(() => import('./OriginalViewerDialog.vue'))

const project = useProjectStore()
const session = useSessionStore()
const images = useImageInfoStore()
const route = useRoute()
const router = useRouter()
const tree = useFolderTree()

/** 视图（记在 localStorage，只是个人习惯，读写失败就用默认）。 */
const view = useViewMode('doclite.files.view')
/** 文件的排列（M11-4，同样记在 localStorage）。 */
const sortMode = useSortMode('doclite.files.sort')

// ---------- 当前文件夹和列表 ----------

const folderQuery = computed(() => (typeof route.query.folder === 'string' ? route.query.folder : null))
/** 组件还在（卸载后在途的回调不再切项目、改地址）。 */
let alive = true
onBeforeUnmount(() => {
  alive = false
})

/** 切到另一个项目是因为打开了那个项目里的文件夹（不是用户在切换器里换的）。 */
let adopting = false
let lastProjectId: string | null = null

const browser = useFolderBrowser({
  tree,
  rootId: () => project.current?.root_folder_id ?? null,
  fetchFolder: (id) => getFolder(id, ['not_found', 'forbidden']),
  onSwitch: () => selection.clear(),
  async accept(detail, current) {
    if (detail.project_id === project.current?.id) return true
    // 别的项目里的文件夹：能进那个项目就切过去（列表里没有时先刷新一次项目列表，可能刚被授权）。
    let other = project.projects.find((x) => x.id === detail.project_id)
    if (!other) {
      await project.refresh()
      if (!current()) return false
      other = project.projects.find((x) => x.id === detail.project_id)
    }
    if (other) {
      adopting = true
      project.select(other.id)
    } else {
      await router.replace({ query: {} })
    }
    return false
  },
  async onOpenFailed(e, id) {
    // 不存在、已删除、进不了它所在的项目（403）：都回到当前项目的根文件夹。
    // 403 的提示由请求封装的全局处理弹出（它会先刷新菜单），这里不再重复提示。
    const gone = isApiError(e, 'not_found') || isApiError(e, 'forbidden')
    if (!gone || id === project.current?.root_folder_id) return false
    if (isApiError(e, 'not_found')) toast.error('文件夹不存在或已被删除，已回到根文件夹')
    await router.replace({ query: {} })
    return true
  },
  onGone(parent) {
    toast.info('当前文件夹已被删除，已回到上一级')
    void navigate(parent, true)
  },
})
const { folder, children, crumbs, state: listState, error: listError } = browser

/** M2-7：只看已驳回的文件（换项目时关掉，换文件夹时保留）。 */
const rejectedOnly = ref(false)

const entries = computed<Entry[]>(() =>
  children.value
    ? toEntries(
        children.value.folders,
        // batch 说不存在、无权访问、已删除的文件，从列表里移除（design §4.4）。
        // M2-7："只看已驳回"时只留已驳回的文件（文件夹照常显示，可以往下走）。
        sortFiles(children.value.files, sortMode.value).filter(
          (f) => !images.isMissing(f.file_id) && (!rejectedOnly.value || f.review_status === 'rejected'),
        ),
      )
    : [],
)
// 第几页：按扫描顺序（children 接口的顺序）数，和"移到第…页"的页码一致；sort_no 删除、移出后会有空号。
const pageOf = computed(() => new Map((children.value?.files ?? []).map((f, i) => [f.file_id, i + 1])))
const entryByKey = computed(() => new Map(entries.value.map((e) => [e.key, e])))
const counts = computed(() => ({
  folders: children.value?.folders.length ?? 0,
  files: entries.value.length - (children.value?.folders.length ?? 0),
}))

const selection = useSelection(() => entries.value.map((e) => e.key))
watch(entries, () => selection.prune())
const selectedEntries = computed(() => entries.value.filter((e) => selection.isSelected(e.key)))
const focusEntry = computed(() => {
  const k = selection.focus.value
  return (k && entryByKey.value.get(k)) || selectedEntries.value[selectedEntries.value.length - 1] || null
})

/** 当前文件夹的位置（不含根）：`/2025 文书档案/盒 001`；根是 `/`。 */
const location = computed(() => `/${crumbs.value.slice(1).map((c) => c.folder_name).join('/')}`)

/** 详情面板重新取快照和日志的信号。 */
const detailRefresh = ref(0)

watch(
  [() => project.current?.id ?? null, folderQuery],
  async ([pid, q]) => {
    const p = project.current
    if (!pid || !p) {
      lastProjectId = null
      return
    }
    if (pid !== lastProjectId) {
      const switchedByUser = lastProjectId !== null && !adopting
      lastProjectId = pid
      adopting = false
      browser.reset()
      selection.clear()
      tree.reset({ id: p.root_folder_id, folder_name: p.project_name })
      if (switchedByUser && q) {
        // 用户换了项目：回到新项目的根文件夹（地址变了会再进来一次）。
        await router.replace({ query: {} })
        return
      }
    }
    await browser.open(q ?? p.root_folder_id)
  },
  { immediate: true },
)

function navigate(id: string | null, replace = false) {
  if (!alive) return Promise.resolve()
  const to = { query: id && id !== project.current?.root_folder_id ? { folder: id } : {} }
  return replace ? router.replace(to) : router.push(to)
}

function onOpen(entry: Entry) {
  if (entry.kind === 'folder') void navigate(entry.folder.id)
}

// ---------- 实时事件 ----------

const pendingFolders = new Set<string>()
const pendingFiles = new Set<string>()
let pendingFull = false
// 去抖：最后一条事件后 300 ms 刷新，但从第一条起最多等 1 秒；连续不断（上传、批量操作期间）时最多等 3 秒。
// children 接口只能整个文件夹重取（没有按 file_id 取行的接口），所以事件多时少取几次。
const batch = useEventBatch(applyEvents, { burstMaxWaitMs: 3000 })

/**
 * M3-7：详情面板的归类信息。`catalog.changed` 不带文件（档号、题名、页序可能变了），收到就让面板重新取当前这个文件的
 * （面板自己去抖 150 ms）；归档、移出另有这个文件的 `files.changed`，走 detailRefresh。
 */
const catalogRefresh = ref(0)

function onEvent(e: ProjectEvent) {
  if (e.type === 'resync') pendingFull = true
  else if (e.type === 'folders.changed' || e.type === 'files.changed') {
    e.folder_ids?.forEach((id) => pendingFolders.add(id))
    e.file_ids?.forEach((id) => pendingFiles.add(id))
  } else if (e.type === 'catalog.changed') {
    catalogRefresh.value++
    return
  } else return
  batch.schedule()
}

async function applyEvents() {
  if (pendingFull) {
    pendingFull = false
    pendingFolders.clear()
    pendingFiles.clear()
    await refreshAll()
    return
  }
  const folders = new Set(pendingFolders)
  const files = new Set(pendingFiles)
  pendingFolders.clear()
  pendingFiles.clear()

  if (files.size) images.invalidate(files)
  // 详情面板的日志：事件带了 file_ids 时看有没有当前这个文件；一次涉及超过 500 个文件时事件只带 folder_ids
  // （M2-1 的批量分配、改派），这时看当前文件所在的文件夹（就是正在看的文件夹）。
  const focused = focusEntry.value
  const cur = folder.value
  if (
    focused?.kind === 'file' &&
    (files.has(focused.file.file_id) || (files.size === 0 && !!cur && folders.has(cur.id)))
  )
    detailRefresh.value++
  browser.applyChanges(folders, files)
}

/** 当前文件夹还没打开成功（首次加载失败）：重新打开它；正在加载时什么也不做。 */
function reopenIfFailed(): boolean {
  if (folder.value) return false
  const p = project.current
  if (p && listState.value !== 'loading') void browser.open(folderQuery.value ?? p.root_folder_id)
  return true
}

/** 整体刷新：resync、断线重连之后（断开期间的事件不会补发）。 */
async function refreshAll() {
  images.invalidate()
  detailRefresh.value++
  if (reopenIfFailed()) {
    await tree.refreshShown()
    return
  }
  await Promise.all([tree.refreshShown(folder.value?.id), browser.reload(), browser.reloadCrumb()])
}

const stream = useProjectStream(() => project.currentId, {
  onEvent,
  onOpen: (first) => {
    if (!first) {
      // 重连成功（含从往返缓存回来）：断开期间的事件不会补发，整体刷新。
      pendingFull = true
      batch.schedule()
    } else if (browser.loaded()) {
      // 第一次连上时列表已经加载好了：补刷列表和树（加载和连接建立之间可能漏了事件）。
      void browser.reload()
      void tree.refreshShown(folder.value?.id)
    } else if (listState.value === 'error') {
      // 首次加载失败（例如 server 刚恢复）：重新打开。
      reopenIfFailed()
    }
  },
})

// ---------- 对话框 ----------

const pickerOpen = ref(false)
function onPicked(target: PickedFolder) {
  void navigate(target.id)
}

const snapshotsOpen = ref(false)
const snapshotsFile = ref<FileEntry | null>(null)
function openSnapshots(file: FileEntry) {
  snapshotsFile.value = file
  snapshotsOpen.value = true
}

onMounted(() => {
  void project.ensureLoaded()
})

// ---------- 整理和上传（M1-12） ----------

/** 操作成功后立即刷新受影响的文件夹（列表、树、面包屑）；SSE 的事件随后也会到。 */
function afterChange(ids: string[]) {
  browser.applyChanges(new Set(ids))
}

const actions = useFileActions({
  folder: () => folder.value,
  selected: () => selectedEntries.value,
  selection,
  afterChange,
  openSnapshots,
  navigate: (id) => void navigate(id),
})

/** 快捷键提示：macOS 上是 ⌘。 */
const modKey = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform) ? '⌘' : 'Ctrl'

/** 右键菜单针对的是什么（右键时按选择算好）。 */
const menuKind = ref<'blank' | 'file' | 'files' | 'folder' | 'folders' | 'mixed'>('blank')
const menuEntry = ref<Entry | null>(null)

/** 右键：点在条目上时，没选中的先单选它；点在空白处取消选择。菜单按选择显示。 */
function onAreaContextMenu(ev: MouseEvent) {
  const el = (ev.target as HTMLElement | null)?.closest<HTMLElement>('[data-key]')
  const key = el?.dataset.key
  const entry = key ? entryByKey.value.get(key) : undefined
  if (entry) {
    if (!selection.isSelected(entry.key)) selection.click(entry.key)
    menuEntry.value = entry
  } else {
    selection.clear()
    menuEntry.value = null
  }
  menuKind.value = actions.kind.value === 'none' ? 'blank' : actions.kind.value
}

function onGridCommand(cmd: GridCommand) {
  if (sealedCommandBlocked(cmd)) return
  if (cmd === 'rename') actions.renameSelected()
  else if (cmd === 'delete') actions.deleteSelected()
  else void actions.copySelected()
}

/** 移动对话框选好了目标。 */
function onMovePicked(target: PickedFolder) {
  void actions.onMoveTarget(target)
}

const moveDescription = computed(() =>
  actions.move.what === 'files'
    ? '目标文件夹里有同名文件时自动改名（加 (1)），排在目标文件夹的末尾。'
    : '文件夹连同里面的所有内容一起移动；不能移进它自己和它下面的子文件夹。',
)

function editSelectedFolderRemark() {
  const f = actions.folders.value[0]
  if (f) actions.editFolder(f, 'remark')
}

// ---------- 查看原图（M2-4） ----------

const viewerOpen = ref(false)
/** 第一次打开之后才渲染对话框（之后一直留着，关掉只是隐藏）。 */
const viewerMounted = ref(false)
const viewerFileId = ref<string | null>(null)
/** 当前文件夹里的文件（查看器里左右切换用，顺序同列表）。 */
const viewerFiles = computed(() =>
  entries.value.filter((e): e is Extract<Entry, { kind: 'file' }> => e.kind === 'file').map((e) => e.file),
)
function viewOriginal(entry: Entry | null) {
  if (!entry || entry.kind !== 'file') return
  viewerFileId.value = entry.file.file_id
  viewerMounted.value = true
  viewerOpen.value = true
}

// ---------- 重扫件（M2-7） ----------

/** 当前文件夹里直接放的已驳回文件数（筛选按钮上的数字）。 */
const rejectedCount = computed(
  () => children.value?.files.filter((f) => f.review_status === 'rejected' && !images.isMissing(f.file_id)).length ?? 0,
)
watch(
  () => project.current?.id ?? null,
  () => (rejectedOnly.value = false),
)

/** 选中的是一个已驳回的文件：右键菜单和选择栏里给"上传重扫件"。 */
const rescanFile = computed(() => {
  const f = actions.kind.value === 'file' ? actions.files.value[0] : undefined
  return f?.review_status === 'rejected' ? f : null
})

const rescanOpen = ref(false)
const rescanTarget = ref<RescanTarget | null>(null)

function openRescan(file: FileEntry) {
  const cur = folder.value
  if (!cur) return
  rescanTarget.value = {
    file_id: file.file_id,
    file_name: file.file_name,
    folder_id: cur.id,
    folder_path: location.value === '/' ? '' : location.value.slice(1),
    folder_remark: cur.remark,
    version_no: file.version_no,
    review_round: file.review_round,
    // 驳回原因由对话框按所在文件夹去待重扫列表里找。
    last_reject: undefined,
  }
  rescanOpen.value = true
}

// ---------- 封存（M9） ----------

const canCatalog = computed(() => session.menuCodes.has('catalog'))
/** 打开的文件夹已封存：里面不能放、拿、改文件，不能建子文件夹，自己不能改名、移动、删除。 */
const sealed = computed(() => folder.value?.archive?.source_status === 'sealed')
const SEALED_REASON = '这个文件夹已归档并封存：要修改先解除封存（可以重扫、看图、改备注）'
const sealReason = computed(() => (sealed.value ? SEALED_REASON : null))
/** 选中的子文件夹已封存（它的改名、移动、删除不能做）。 */
const selectedFolderSealed = computed(
  () => actions.kind.value === 'folder' && actions.folders.value[0]?.archive?.source_status === 'sealed',
)
const FOLDER_SEALED_REASON = '这个文件夹已归档并封存：不能改名、移动、删除（可以改备注）；要修改先解除封存'

const unsealOpen = ref(false)
const unsealItemId = ref<string | null>(null)
function askUnseal() {
  const a = folder.value?.archive
  if (!a) return
  unsealItemId.value = a.item_id
  unsealOpen.value = true
}
function onUnsealed(_it: ArchiveItem) {
  toast.success('已解除封存：现在可以在这里修改，改完到归类页"重新归档"')
  const cur = folder.value
  if (cur) afterChange([cur.id])
  void browser.reloadCrumb()
}
function openCatalog() {
  const a = folder.value?.archive
  const cur = folder.value
  if (!a || !cur) return
  void router.push({ path: '/catalog', query: { folder: cur.id, category: a.category_id, item: a.item_id } })
}

/** 快捷键（F2、Delete、Ctrl/⌘+C）碰到封存时提示，不发请求。 */
function sealedCommandBlocked(cmd: GridCommand): boolean {
  if (sealed.value && (actions.kind.value === 'file' || actions.kind.value === 'files')) {
    toast.info(SEALED_REASON)
    return true
  }
  if (cmd !== 'copy' && selectedFolderSealed.value) {
    toast.info(FOLDER_SEALED_REASON)
    return true
  }
  return false
}

// ---------- 文件名搜索（M11-4） ----------

const grid = ref<InstanceType<typeof FileGrid> | null>(null)
/** 搜索框里的字；去抖 300 ms 后（去掉首尾空白）成为 searchQuery，清空时立即回到列表。 */
const searchInput = ref('')
const searchQuery = ref('')
watchDebounced(searchInput, (v) => (searchQuery.value = v.trim().slice(0, 100)), { debounce: 300 })
watch(searchInput, (v) => {
  if (!v.trim()) searchQuery.value = ''
})
const searching = computed(() => !!searchQuery.value && !!folder.value)
watch(searching, (v) => v && selection.clear())
function clearSearch() {
  searchInput.value = ''
  searchQuery.value = ''
}

/** 点了搜索结果：跳到它的文件夹，列表出来后选中、滚到这个文件。 */
let pendingFocus: { folderId: string; key: string } | null = null
function onPickHit(hit: FolderSearchHit) {
  clearSearch()
  rejectedOnly.value = false
  pendingFocus = { folderId: hit.folder_id, key: fileKey(hit.file_id) }
  if (folder.value?.id === hit.folder_id) applyPendingFocus()
  else void navigate(hit.folder_id)
}
function applyPendingFocus() {
  const p = pendingFocus
  if (!p || folder.value?.id !== p.folderId || !children.value) return
  pendingFocus = null
  if (!entryByKey.value.has(p.key)) return
  selection.set([p.key])
  void nextTick(() => grid.value?.scrollToKey(p.key))
}
watch(entries, applyPendingFocus)

// ---------- 调序（M11-4） ----------

/** "移到第…页"不能用的原因（按钮置灰并显示）。 */
const reorderReason = computed(() => {
  if (sealed.value) return SEALED_REASON
  if (searching.value) return '搜索时不能调顺序'
  if (sortMode.value !== 'scan') return '按扫描顺序排列时才能调顺序（归档按扫描顺序排页）'
  return null
})
const positionOpen = ref(false)
const positionFiles = ref<FileEntry[]>([])
function openPosition() {
  if (reorderReason.value || !actions.files.value.length) return
  positionFiles.value = actions.files.value.slice()
  positionOpen.value = true
}
function onPositionSaved(n: number) {
  const k = positionFiles.value.length
  toast.success(k === 1 ? `已把「${positionFiles.value[0]!.file_name}」移到第 ${n} 页` : `已把 ${k} 个文件移到第 ${n}–${n + k - 1} 页`)
  const cur = folder.value
  if (cur) afterChange([cur.id])
}
function onPositionConflict(message: string) {
  toast.error(message)
  const cur = folder.value
  if (cur) afterChange([cur.id])
}

/** 重扫结束（成功或状态已变）：刷新列表和详情（SSE 的 files.changed 随后也会到）。 */
function onRescanFinished() {
  const cur = folder.value
  if (cur) afterChange([cur.id])
  detailRefresh.value++
}
</script>

<template>
  <div
    class="flex min-h-0 flex-1 flex-col"
    data-testid="files-page"
    :data-stream="stream.state.value"
    :data-list="listState"
  >
    <PageHeader>
      <template #meta>
        <nav
          v-if="crumbs.length"
          class="flex min-w-0 items-center gap-0.5 border-l pl-3 text-sm"
          aria-label="当前位置"
          data-testid="breadcrumb"
        >
          <template v-for="(c, i) in crumbs" :key="c.id">
            <ChevronRightIcon v-if="i > 0" class="size-3.5 shrink-0 text-muted-foreground" />
            <span
              v-if="i === crumbs.length - 1"
              class="truncate font-medium text-foreground"
              :class="i === 0 ? 'max-w-64' : 'max-w-56'"
              data-testid="breadcrumb-current"
            >{{ c.folder_name }}</span>
            <button
              v-else
              type="button"
              class="max-w-40 truncate rounded px-1 text-muted-foreground hover:bg-muted hover:text-foreground"
              :title="c.folder_name"
              @click="navigate(c.id)"
            >
              {{ c.folder_name }}
            </button>
          </template>
        </nav>
      </template>
      <template #actions>
        <StreamStatus v-if="project.current" :state="stream.state.value" />
        <!-- M11-4：在当前文件夹和它的子文件夹里按文件名搜。 -->
        <div v-if="project.current" class="relative w-56">
          <SearchIcon class="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            v-model="searchInput"
            class="h-8 pr-7 pl-7"
            placeholder="搜索文件名…"
            maxlength="100"
            aria-label="在当前文件夹和子文件夹里搜索文件名"
            data-testid="file-search"
            @keydown.esc.prevent="clearSearch()"
          />
          <button
            v-if="searchInput"
            type="button"
            class="absolute top-1/2 right-1.5 -translate-y-1/2 rounded p-0.5 text-muted-foreground hover:text-foreground"
            aria-label="清空搜索"
            @click="clearSearch()"
          >
            <XIcon class="size-3.5" />
          </button>
        </div>
        <Button
          v-if="project.current"
          variant="outline"
          size="sm"
          data-testid="goto-folder"
          @click="pickerOpen = true"
        >
          <FolderInputIcon />转到…
        </Button>
        <ViewModeToggle v-model="view" />
      </template>
    </PageHeader>

    <!-- 项目还在加载 / 没有能进的项目 -->
    <div v-if="!project.current" class="flex flex-1 items-center justify-center p-6 text-sm text-muted-foreground">
      <span v-if="project.state === 'loading' || project.state === 'idle'" class="flex items-center gap-2">
        <LoaderCircleIcon class="size-4 animate-spin" />正在加载项目…
      </span>
      <div v-else class="max-w-sm text-center" data-testid="files-no-project">
        <div class="text-base font-medium text-foreground">没有可进入的项目</div>
        <p class="mt-1">请联系管理员在「系统管理 › 项目」里给你授权，或在左上角选择一个项目。</p>
      </div>
    </div>

    <div v-else class="flex min-h-0 flex-1">
      <aside class="w-56 shrink-0 overflow-y-auto border-r bg-sidebar/60 px-2 py-2.5">
        <FolderTree :tree="tree" :selected-id="folder?.id ?? folderQuery ?? project.current.root_folder_id" @select="navigate">
          <template #suffix="{ folder: f }">
            <span
              v-if="treeFolderModifying(f, tree.childrenOf(f))"
              class="flex shrink-0 text-modifying"
              title="里面有修改中的文件夹（改完要重新归档）"
              data-testid="tree-modifying-dot"
            ><PencilLineIcon class="size-3" aria-hidden="true" /><span class="sr-only">修改中</span></span>
          </template>
        </FolderTree>
      </aside>

      <section class="relative flex min-w-0 flex-1 flex-col">
        <div class="flex h-10 shrink-0 items-center gap-2 border-b px-4 text-xs text-muted-foreground" data-testid="folder-summary">
          <template v-if="folder && children">
            <span class="shrink-0">{{ counts.folders }} 个文件夹 · {{ counts.files }} 个文件</span>
            <template v-if="folder.remark">
              <span>·</span>
              <span class="truncate" :title="folder.remark" data-testid="folder-remark">原件：{{ folder.remark }}</span>
            </template>
          </template>
          <!-- M2-7：只看已驳回（当前文件夹直接放的文件）。 -->
          <button
            v-if="folder && children && (rejectedCount > 0 || rejectedOnly)"
            type="button"
            class="ml-1 flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-xs transition-colors"
            :class="rejectedOnly ? 'bg-destructive text-white' : 'bg-destructive-soft text-destructive hover:bg-destructive/15'"
            :aria-pressed="rejectedOnly"
            :title="rejectedOnly ? '显示全部文件' : '只看这个文件夹里已驳回、等重扫的文件'"
            data-testid="filter-rejected"
            :data-active="rejectedOnly"
            @click="rejectedOnly = !rejectedOnly"
          >
            <FilterIcon class="size-3" />已驳回 {{ rejectedCount }}
          </button>
          <div class="flex-1" />
          <!-- 整理和上传（M1-12）：作用于当前文件夹。 -->
          <div class="flex shrink-0 items-center gap-1.5">
            <GuardedButton variant="outline" size="xs" :reason="sealReason" :disabled="!folder" data-testid="new-folder" @click="actions.newFolder()">
              <FolderPlusIcon />新建文件夹
            </GuardedButton>
            <GuardedButton variant="outline" size="xs" :reason="sealReason" :disabled="!folder" data-testid="upload-files" @click="actions.startUpload('files')">
              <UploadIcon />上传文件
            </GuardedButton>
            <GuardedButton variant="outline" size="xs" :reason="sealReason" :disabled="!folder" data-testid="upload-folder" @click="actions.startUpload('folder')">
              <FolderUpIcon />上传文件夹
            </GuardedButton>
          </div>
          <!-- M11-4：排列（只在本页排；归档按扫描顺序）。 -->
          <NativeSelect
            v-model="sortMode"
            size="xs"
            class="shrink-0"
            aria-label="文件的排列方式"
            title="只影响这里的显示；归档按扫描顺序排页"
            data-testid="file-sort"
          >
            <option value="scan">按扫描顺序</option>
            <option value="name">按文件名</option>
            <option value="created">按入库时间</option>
          </NativeSelect>
        </div>

        <!-- 封存横条（M9） -->
        <SealBanner
          v-if="folder?.archive"
          :archive="folder.archive"
          where="files"
          :can-catalog="canCatalog"
          @unseal="askUnseal"
          @open-catalog="openCatalog"
        />

        <!-- M11-4：搜索结果替换文件列表。 -->
        <FileSearchResults
          v-if="searching && folder"
          :folder-id="folder.id"
          :folder-name="folder.folder_name"
          :query="searchQuery"
          @pick="onPickHit"
        />
        <ContextMenu v-else :modal="false">
          <ContextMenuTrigger as-child>
            <div class="flex min-h-0 flex-1 flex-col" data-testid="file-area" @contextmenu.capture="onAreaContextMenu">
              <ErrorState
                v-if="listState === 'error' && !children"
                class="flex-1"
                :message="listError"
                @retry="browser.open(folderQuery ?? project.current.root_folder_id)"
              />
              <div v-else-if="!children" class="flex flex-1 items-center justify-center gap-2 text-sm text-muted-foreground">
                <LoaderCircleIcon class="size-4 animate-spin" />正在加载…
              </div>
              <!-- M2-7：筛选已驳回、这里没有 -->
              <div
                v-else-if="rejectedOnly && entries.length === 0"
                class="flex flex-1 flex-col items-center justify-center gap-1 text-sm text-muted-foreground"
                data-testid="rejected-empty"
              >
                <span>这个文件夹里没有已驳回的文件</span>
                <span class="text-xs">子文件夹里的不在这里显示；全部待重扫的文件在「扫描上传」页的待重扫列表里</span>
              </div>
              <div
                v-else-if="entries.length === 0"
                class="flex flex-1 flex-col items-center justify-center gap-1 text-sm text-muted-foreground"
                data-testid="folder-empty"
              >
                <span>这个文件夹是空的</span>
                <span class="text-xs">可以新建子文件夹，或上传文件、文件夹（也可以在这里点右键）</span>
              </div>
              <FileGrid
                v-else
                ref="grid"
                :entries="entries"
                :view="view"
                :selection="selection"
                :page-of="pageOf"
                @open="onOpen"
                @command="onGridCommand"
              />
            </div>
          </ContextMenuTrigger>
          <ContextMenuContent class="w-56" data-testid="context-menu" :data-kind="menuKind">
            <!-- 一个文件 -->
            <ContextMenuLabel
              v-if="sealed && menuKind !== 'folder' && menuKind !== 'folders'"
              class="max-w-56 text-xs leading-relaxed font-normal text-sealed"
              data-testid="menu-sealed-note"
            >{{ SEALED_REASON }}</ContextMenuLabel>
            <ContextMenuLabel
              v-else-if="menuKind === 'folder' && selectedFolderSealed"
              class="max-w-56 text-xs leading-relaxed font-normal text-sealed"
              data-testid="menu-sealed-note"
            >{{ FOLDER_SEALED_REASON }}</ContextMenuLabel>
            <template v-if="menuKind === 'file'">
              <ContextMenuItem data-testid="menu-open" @select="menuEntry && actions.openEntry(menuEntry)">
                <EyeIcon />打开
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-view-original" @select="viewOriginal(menuEntry)">
                <ImageIcon />查看原图
              </ContextMenuItem>
              <ContextMenuItem :disabled="sealed" data-testid="menu-rename" @select="actions.renameSelected()">
                <PencilIcon />重命名<ContextMenuShortcut>F2</ContextMenuShortcut>
              </ContextMenuItem>
              <ContextMenuItem :disabled="sealed" data-testid="menu-move" @select="actions.moveSelected()">
                <FolderInputIcon />移动到…
              </ContextMenuItem>
              <ContextMenuItem :disabled="!!reorderReason" data-testid="menu-position" @select="openPosition()">
                <ListOrderedIcon />移到第…页
                <span v-if="reorderReason && !sealed" class="ml-auto text-2xs text-muted-foreground">按扫描顺序时可用</span>
              </ContextMenuItem>
              <ContextMenuItem :disabled="sealed" data-testid="menu-copy" @select="actions.copySelected()">
                <CopyIcon />复制<ContextMenuShortcut>{{ modKey }}+C</ContextMenuShortcut>
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-download" @select="actions.downloadOriginal()">
                <DownloadIcon />下载原图
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-snapshots" @select="actions.snapshotsOfSelected()">
                <HistoryIcon />查看快照历史
              </ContextMenuItem>
              <!-- M2-7：已驳回的文件上传重扫件。 -->
              <ContextMenuItem v-if="rescanFile" data-testid="menu-rescan" @select="rescanFile && openRescan(rescanFile)">
                <RescanIcon />上传重扫件
              </ContextMenuItem>
              <ContextMenuSeparator />
              <ContextMenuItem :disabled="sealed" variant="destructive" data-testid="menu-delete" @select="actions.deleteSelected()">
                <Trash2Icon />删除<ContextMenuShortcut>Del</ContextMenuShortcut>
              </ContextMenuItem>
            </template>
            <!-- 多个文件 -->
            <template v-else-if="menuKind === 'files'">
              <ContextMenuLabel class="text-xs text-muted-foreground">已选 {{ actions.files.value.length }} 个文件</ContextMenuLabel>
              <ContextMenuItem :disabled="sealed" data-testid="menu-move" @select="actions.moveSelected()">
                <FolderInputIcon />移动到…
              </ContextMenuItem>
              <ContextMenuItem :disabled="!!reorderReason" data-testid="menu-position" @select="openPosition()">
                <ListOrderedIcon />移到第…页
                <span v-if="reorderReason && !sealed" class="ml-auto text-2xs text-muted-foreground">按扫描顺序时可用</span>
              </ContextMenuItem>
              <ContextMenuItem :disabled="sealed" data-testid="menu-copy" @select="actions.copySelected()">
                <CopyIcon />复制<ContextMenuShortcut>{{ modKey }}+C</ContextMenuShortcut>
              </ContextMenuItem>
              <ContextMenuSeparator />
              <ContextMenuItem :disabled="sealed" variant="destructive" data-testid="menu-delete" @select="actions.deleteSelected()">
                <Trash2Icon />删除<ContextMenuShortcut>Del</ContextMenuShortcut>
              </ContextMenuItem>
            </template>
            <!-- 一个文件夹 -->
            <template v-else-if="menuKind === 'folder'">
              <ContextMenuItem data-testid="menu-open" @select="menuEntry && actions.openEntry(menuEntry)">
                <FolderOpenIcon />打开
              </ContextMenuItem>
              <ContextMenuItem :disabled="selectedFolderSealed" data-testid="menu-rename" @select="actions.renameSelected()">
                <PencilIcon />重命名<ContextMenuShortcut>F2</ContextMenuShortcut>
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-remark" @select="editSelectedFolderRemark()">
                <NotebookPenIcon />修改备注
              </ContextMenuItem>
              <ContextMenuItem :disabled="selectedFolderSealed" data-testid="menu-move" @select="actions.moveSelected()">
                <FolderInputIcon />移动到…
              </ContextMenuItem>
              <ContextMenuSeparator />
              <ContextMenuItem :disabled="selectedFolderSealed" variant="destructive" data-testid="menu-delete" @select="actions.deleteSelected()">
                <Trash2Icon />删除<ContextMenuShortcut>Del</ContextMenuShortcut>
              </ContextMenuItem>
            </template>
            <!-- 几个文件夹 -->
            <template v-else-if="menuKind === 'folders'">
              <ContextMenuLabel class="text-xs leading-relaxed font-normal text-muted-foreground">
                已选 {{ actions.folders.value.length }} 个文件夹。文件夹一次只能改名、移动或删除一个，请只选一个。
              </ContextMenuLabel>
            </template>
            <!-- 文件夹和文件混在一起 -->
            <template v-else-if="menuKind === 'mixed'">
              <ContextMenuLabel class="text-xs leading-relaxed font-normal text-muted-foreground">
                选中的有文件夹也有文件。文件可以多选一起移动、删除，文件夹一次操作一个。
              </ContextMenuLabel>
            </template>
            <!-- 空白处 -->
            <template v-else>
              <ContextMenuItem :disabled="sealed" data-testid="menu-new-folder" @select="actions.newFolder()">
                <FolderPlusIcon />新建文件夹
              </ContextMenuItem>
              <ContextMenuItem :disabled="sealed" data-testid="menu-upload-files" @select="actions.startUpload('files')">
                <UploadIcon />上传文件
              </ContextMenuItem>
              <ContextMenuItem :disabled="sealed" data-testid="menu-upload-folder" @select="actions.startUpload('folder')">
                <FolderUpIcon />上传文件夹
              </ContextMenuItem>
              <ContextMenuSeparator />
              <ContextMenuItem data-testid="menu-edit-current" @select="actions.editCurrentFolder()">
                <NotebookPenIcon />{{ folder?.parent_id ? '当前文件夹的名称和备注…' : '当前文件夹的备注…' }}
              </ContextMenuItem>
            </template>
          </ContextMenuContent>
        </ContextMenu>

        <div
          v-if="selection.count.value > 0"
          class="flex h-9 shrink-0 items-center gap-1 border-t bg-muted/60 px-4 text-xs text-foreground"
          data-testid="selection-bar"
          :data-kind="actions.kind.value"
        >
          <span class="mr-2">已选 {{ selection.count.value }} 项</span>
          <template v-if="actions.kind.value === 'file' || actions.kind.value === 'files'">
            <GuardedButton variant="ghost" size="xs" :reason="sealReason" data-testid="bar-move" @click="actions.moveSelected()">
              <FolderInputIcon />移动到…
            </GuardedButton>
            <GuardedButton variant="ghost" size="xs" :reason="reorderReason" data-testid="bar-position" @click="openPosition()">
              <ListOrderedIcon />移到第…页
            </GuardedButton>
            <GuardedButton variant="ghost" size="xs" :reason="sealReason" data-testid="bar-copy" @click="actions.copySelected()">
              <CopyIcon />复制
            </GuardedButton>
            <Button
              v-if="actions.kind.value === 'file'"
              variant="ghost"
              size="xs"
              data-testid="bar-download"
              @click="actions.downloadOriginal()"
            >
              <DownloadIcon />下载原图
            </Button>
            <!-- M2-7 -->
            <Button v-if="rescanFile" variant="ghost" size="xs" data-testid="bar-rescan" @click="openRescan(rescanFile)">
              <RescanIcon />上传重扫件
            </Button>
            <GuardedButton
              variant="ghost"
              size="xs"
              class="text-destructive hover:text-destructive"
              :reason="sealReason"
              data-testid="bar-delete"
              @click="actions.deleteSelected()"
            >
              <Trash2Icon />删除
            </GuardedButton>
          </template>
          <template v-else-if="actions.kind.value === 'folder'">
            <GuardedButton variant="ghost" size="xs" :reason="selectedFolderSealed ? FOLDER_SEALED_REASON : null" data-testid="bar-rename" @click="actions.renameSelected()">
              <PencilIcon />重命名
            </GuardedButton>
            <GuardedButton variant="ghost" size="xs" :reason="selectedFolderSealed ? FOLDER_SEALED_REASON : null" data-testid="bar-move" @click="actions.moveSelected()">
              <FolderInputIcon />移动到…
            </GuardedButton>
            <GuardedButton
              variant="ghost"
              size="xs"
              class="text-destructive hover:text-destructive"
              :reason="selectedFolderSealed ? FOLDER_SEALED_REASON : null"
              data-testid="bar-delete"
              @click="actions.deleteSelected()"
            >
              <Trash2Icon />删除
            </GuardedButton>
          </template>
          <span v-else class="text-muted-foreground">{{ actions.kind.value === 'folders' ? '文件夹一次只能操作一个' : '文件夹和文件要分开操作' }}</span>
          <div class="flex-1" />
          <button type="button" class="text-muted-foreground hover:text-foreground" @click="selection.clear()">
            取消选择（Esc）
          </button>
        </div>
      </section>

      <aside class="w-72 shrink-0 overflow-y-auto border-l">
        <FileDetailPanel
          :focus="focusEntry"
          :selected="selectedEntries"
          :folder="folder"
          :location="location"
          :counts="counts"
          :refresh-token="detailRefresh"
          :catalog-refresh-token="catalogRefresh"
          :page-of="pageOf"
          @snapshots="openSnapshots"
          @edit-folder="actions.editCurrentFolder()"
        />
      </aside>
    </div>

    <FolderPicker
      v-if="project.current"
      v-model:open="pickerOpen"
      :root-id="project.current.root_folder_id"
      :root-name="project.current.project_name"
      :initial-folder-id="folder?.id ?? null"
      title="转到文件夹"
      confirm-text="打开"
      @confirm="onPicked"
    />
    <SnapshotHistoryDialog
      v-model:open="snapshotsOpen"
      :file-id="snapshotsFile?.file_id ?? null"
      :file-name="snapshotsFile?.file_name"
    />

    <!-- 整理（M1-12） -->
    <FolderFormDialog
      v-model:open="actions.folderForm.open"
      :parent="actions.folderForm.parent"
      :folder="actions.folderForm.folder"
      :focus="actions.folderForm.focus"
      @saved="actions.onFolderSaved"
    />
    <RenameFileDialog v-model:open="actions.rename.open" :file="actions.rename.file" @saved="actions.onFileRenamed()" />
    <FolderPicker
      v-if="project.current"
      v-model:open="actions.move.open"
      :root-id="project.current.root_folder_id"
      :root-name="project.current.project_name"
      :initial-folder-id="folder?.id ?? null"
      :title="actions.move.title"
      :description="moveDescription"
      confirm-text="移动到这里"
      :exclude="actions.move.exclude"
      :disabled-ids="actions.move.disabledIds"
      @confirm="onMovePicked"
    />
    <ConfirmDialog
      v-model:open="actions.remove.open"
      :title="actions.remove.title"
      :description="actions.remove.description"
      confirm-text="删除"
      destructive
      :loading="actions.remove.loading"
      @confirm="actions.confirmDelete()"
    />

    <!-- 调序（M11-4） -->
    <MoveToPositionDialog
      v-model:open="positionOpen"
      :folder-id="folder?.id ?? null"
      :files="children?.files ?? []"
      :selected="positionFiles"
      @saved="onPositionSaved"
      @conflict="onPositionConflict"
    />

    <!-- 查看原图（M2-4） -->
    <OriginalViewerDialog v-if="viewerMounted" v-model:open="viewerOpen" :files="viewerFiles" :file-id="viewerFileId" />

    <!-- 解除封存（M9） -->
    <UnsealDialog v-if="canCatalog" v-model:open="unsealOpen" :item-id="unsealItemId" @unsealed="onUnsealed" />

    <!-- 上传重扫件（M2-7） -->
    <RescanDialog v-model:open="rescanOpen" :target="rescanTarget" :project-id="project.currentId" @finished="onRescanFinished" />
  </div>
</template>
