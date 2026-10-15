<script setup lang="ts">
// 文件目录页（M1-10：浏览和看图；整理和上传在 M1-12）。参考 plan.html 的 p-files Mockup。
//
// - 当前项目取自项目切换器（stores/project.ts）；当前文件夹在地址栏 `?folder=<id>`（没有就是根文件夹），
//   前进后退能在文件夹之间切换。打开别的项目里的文件夹链接时，能进那个项目就自动切过去。
// - 左侧目录树（FolderTree，按需加载）；顶部面包屑（`GET /api/folders/{id}`）；主区图标和列表两种视图
//   （FileGrid，按行虚拟化）；右侧详情（FileDetailPanel）；快照历史对话框。
// - 固定按 sort_no、名称排序（children 接口的顺序）；不做文件名搜索和排序下拉。
// - 实时：订阅当前项目的 SSE（useProjectStream）。`folders.changed`、`files.changed` 去抖后只刷新受影响的
//   文件夹（当前文件夹、它的子文件夹、面包屑上的、树上已加载的）；`resync` 或断线重连后整体刷新。
//   当前文件夹被删掉时回到上一级。
// - 整理（M1-12，useFileActions）：新建文件夹、改名、改备注、移动、复制、删除、下载原图；右键菜单包在主区外面，
//   按单元格的 data-key 定位条目（右键没选中的条目时先单选它）；底部的选择栏放同样的操作；F2、Delete、Ctrl/⌘+C。
// - 上传（M1-12）：浏览器里两个按钮打开文件选择框（上传文件夹用 webkitdirectory），交给 stores/uploads.ts，
//   结果列表浮在主区右下角（UploadPanel）；壳里改调 `pick_and_enqueue_upload`（上传进度在 /scan 页看）。
//   截图脚本可以在两个隐藏的 <input> 上派发 `doclite-inject-files` 事件（detail 是 File[]，上传文件夹的要带
//   webkitRelativePath），走和选择框完全相同的代码。
// - 查看原图（M2-4，Q11）：文件的右键菜单"查看原图"打开全屏的 OriginalViewerDialog（OpenSeadragon），可以在当前文件夹的
//   文件之间切换、对比上一版。
// - 重扫件（M2-7）：文件夹摘要那一行的"只看已驳回"筛选（只筛当前文件夹直接放的文件，子文件夹照常显示）；一个已驳回的
//   文件在右键菜单和选择栏里有"上传重扫件"（RescanDialog：浏览器里生成封面后 upload()，壳里走 `pick_and_rescan`）。
import {
  ChevronRightIcon,
  CopyIcon,
  DownloadIcon,
  EyeIcon,
  FolderInputIcon,
  FolderOpenIcon,
  FolderPlusIcon,
  FolderUpIcon,
  HistoryIcon,
  LayoutGridIcon,
  ListIcon,
  LoaderCircleIcon,
  NotebookPenIcon,
  PencilIcon,
  Trash2Icon,
  UploadIcon,
} from '@lucide/vue'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import { getFolder } from '@/api/folders'
import { errorMessage, isApiError } from '@/api/http'
import type { FileEntry, FolderChildren, FolderItem, MyProject, ProjectEvent } from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import FolderPicker, { type PickedFolder } from '@/components/files/FolderPicker.vue'
import FolderTree from '@/components/files/FolderTree.vue'
import SnapshotHistoryDialog from '@/components/files/SnapshotHistoryDialog.vue'
import { Button } from '@/components/ui/button'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuShortcut,
  ContextMenuTrigger,
} from '@/components/ui/context-menu'
import { useFolderTree } from '@/composables/useFolderTree'
import { useProjectStream } from '@/composables/useProjectStream'
import { useSelection } from '@/composables/useSelection'
import { isTauri } from '@/lib/client'
import { UPLOAD_ACCEPT } from '@/lib/upload-plan'
import { useImageInfoStore } from '@/stores/image-info'
import { useProjectStore } from '@/stores/project'

import { type Entry, toEntries, type ViewMode } from './entries'
import FileDetailPanel from './FileDetailPanel.vue'
import FileGrid, { type GridCommand } from './FileGrid.vue'
import FolderFormDialog from './FolderFormDialog.vue'
import RenameFileDialog from './RenameFileDialog.vue'
import UploadPanel from './UploadPanel.vue'
import { useFileActions } from './useFileActions'
// ---- 查看原图（M2-4）用到的，单独引入（不改上面已有的引入） ----
import { ImageIcon } from '@lucide/vue'
import { defineAsyncComponent } from 'vue'
// 查看原图的对话框（带 OpenSeadragon）第一次打开时才加载。
const OriginalViewerDialog = defineAsyncComponent(() => import('./OriginalViewerDialog.vue'))
// M2-7：已驳回的筛选和上传重扫件。
import { FilterIcon, ScanLineIcon as RescanIcon } from '@lucide/vue'
import RescanDialog from '@/components/files/RescanDialog.vue'
import type { RescanTarget } from '@/lib/rescan'

const project = useProjectStore()
const images = useImageInfoStore()
const route = useRoute()
const router = useRouter()
const tree = useFolderTree()

// ---------- 视图（记在 localStorage，只是个人习惯，读写失败就用默认） ----------

const VIEW_KEY = 'doclite.files.view'
function readView(): ViewMode {
  try {
    return window.localStorage.getItem(VIEW_KEY) === 'list' ? 'list' : 'icons'
  } catch {
    return 'icons'
  }
}
const view = ref<ViewMode>(readView())
watch(view, (v) => {
  try {
    window.localStorage.setItem(VIEW_KEY, v)
  } catch {
    // 记不住就算了。
  }
})

// ---------- 当前文件夹和列表 ----------

const folderQuery = computed(() => (typeof route.query.folder === 'string' ? route.query.folder : null))
const folder = ref<FolderItem | null>(null)
const children = ref<FolderChildren | null>(null)
const listState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const listError = ref<string | null>(null)
/** 列表最近一次加载完成的时间（0 表示还没加载过）。 */
let listLoadedAt = 0
let openSeq = 0
let listSeq = 0
/** 组件还在（卸载后在途的回调不再切项目、改地址；卸载时 openSeq、listSeq 也会加一，让在途的结果作废）。 */
let alive = true
/** 打开文件夹的过程中（面包屑还没取到）收到了事件。 */
let eventsWhileOpening = false

/** M2-7：只看已驳回的文件（换项目时关掉，换文件夹时保留）。 */
const rejectedOnly = ref(false)

const entries = computed<Entry[]>(() =>
  children.value
    ? toEntries(
        children.value.folders,
        // batch 说不存在、无权访问、已删除的文件，从列表里移除（design §4.4）。
        // M2-7："只看已驳回"时只留已驳回的文件（文件夹照常显示，可以往下走）。
        children.value.files.filter((f) => !images.isMissing(f.file_id) && (!rejectedOnly.value || f.review_status === 'rejected')),
      )
    : [],
)
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

/** 面包屑：根 → … → 当前。 */
const crumbs = computed(() =>
  folder.value
    ? [...folder.value.ancestors, { id: folder.value.id, folder_name: folder.value.folder_name, path_code: folder.value.path_code }]
    : [],
)
/** 当前文件夹的位置（不含根）：`/2025 文书档案/盒 001`；根是 `/`。 */
const location = computed(() => `/${crumbs.value.slice(1).map((c) => c.folder_name).join('/')}`)

/** 详情面板重新取快照和日志的信号。 */
const detailRefresh = ref(0)

function resetForProject(p: MyProject) {
  openSeq++
  listSeq++
  folder.value = null
  children.value = null
  listState.value = 'idle'
  listLoadedAt = 0
  selection.clear()
  tree.reset({ id: p.root_folder_id, folder_name: p.project_name })
}

/** 切到另一个项目是因为打开了那个项目里的文件夹（不是用户在切换器里换的）。 */
let adopting = false
let lastProjectId: string | null = null

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
      resetForProject(p)
      if (switchedByUser && q) {
        // 用户换了项目：回到新项目的根文件夹（地址变了会再进来一次）。
        await router.replace({ query: {} })
        return
      }
    }
    await openFolder(q ?? p.root_folder_id)
  },
  { immediate: true },
)

/** 进入一个文件夹：取它本身和祖先链（面包屑），展开树，加载列表。 */
async function openFolder(id: string) {
  const my = ++openSeq
  const p = project.current
  if (!p) return
  if (folder.value?.id !== id) {
    folder.value = null
    children.value = null
    // 列表清空后 FileGrid 会重新挂载，滚动位置自然回到顶部。
    selection.clear()
  }
  listState.value = 'loading'
  listError.value = null
  eventsWhileOpening = false
  try {
    const detail = await getFolder(id, ['not_found', 'forbidden'])
    if (my !== openSeq) return
    if (detail.project_id !== p.id) {
      // 别的项目里的文件夹：能进那个项目就切过去（列表里没有时先刷新一次项目列表，可能刚被授权）。
      let other = project.projects.find((x) => x.id === detail.project_id)
      if (!other) {
        await project.refresh()
        if (my !== openSeq) return
        other = project.projects.find((x) => x.id === detail.project_id)
      }
      if (other) {
        adopting = true
        project.select(other.id)
      } else {
        await router.replace({ query: {} })
      }
      return
    }
    folder.value = detail
    tree.rename(p.root_folder_id, detail.ancestors[0]?.folder_name ?? detail.folder_name)
    void tree.revealPath(detail)
    await reloadList()
    if (my === openSeq && eventsWhileOpening) {
      eventsWhileOpening = false
      void reloadList()
      void reloadBreadcrumb()
    }
  } catch (e) {
    if (my !== openSeq) return
    // 不存在、已删除、进不了它所在的项目（403）：都回到当前项目的根文件夹。
    // 403 的提示由请求封装的全局处理弹出（它会先刷新菜单），这里不再重复提示。
    const gone = isApiError(e, 'not_found') || isApiError(e, 'forbidden')
    if (gone && id !== p.root_folder_id) {
      if (isApiError(e, 'not_found')) toast.error('文件夹不存在或已被删除，已回到根文件夹')
      await router.replace({ query: {} })
      return
    }
    listState.value = 'error'
    listError.value = errorMessage(e)
  }
}

/** 重新加载当前文件夹的子项（同时更新树上这一层）。 */
async function reloadList() {
  const id = folder.value?.id
  if (!id) return
  const my = ++listSeq
  try {
    const res = await tree.load(id)
    if (my !== listSeq || folder.value?.id !== id) return
    if (!res) {
      folderGone(id)
      return
    }
    children.value = res
    listState.value = 'ready'
    listLoadedAt = Date.now()
  } catch (e) {
    if (my !== listSeq) return
    // 已经有列表时（刷新失败）保留旧的，错误已由请求封装提示。
    if (children.value) listState.value = 'ready'
    else {
      listState.value = 'error'
      listError.value = errorMessage(e)
    }
  }
}

/** 重新取面包屑（当前文件夹或祖先改名、移动）。 */
async function reloadBreadcrumb() {
  const id = folder.value?.id
  if (!id) return
  try {
    const detail = await getFolder(id, ['not_found'])
    if (folder.value?.id !== id) return
    folder.value = detail
    if (project.current) tree.rename(project.current.root_folder_id, detail.ancestors[0]?.folder_name ?? detail.folder_name)
  } catch (e) {
    if (isApiError(e, 'not_found')) folderGone(id)
  }
}

let goneHandled: string | null = null
/** 当前文件夹被删掉了：回到上一级（上一级也没了会再回到根）。 */
function folderGone(id: string) {
  if (!alive || goneHandled === id || folder.value?.id !== id) return
  goneHandled = id
  const parent = folder.value?.parent_id ?? null
  toast.info('当前文件夹已被删除，已回到上一级')
  void navigate(parent && parent !== project.current?.root_folder_id ? parent : null, true)
}

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
let applyTimer: ReturnType<typeof setTimeout> | null = null
let firstPendingAt = 0
/** 去抖：最后一条事件后 300 ms 刷新，但从第一条起最多等 1 秒。 */
const EVENT_DEBOUNCE_MS = 300
const EVENT_MAX_WAIT_MS = 1000

function scheduleApply() {
  const now = Date.now()
  if (!firstPendingAt) firstPendingAt = now
  if (applyTimer) clearTimeout(applyTimer)
  const wait = Math.min(EVENT_DEBOUNCE_MS, Math.max(0, firstPendingAt + EVENT_MAX_WAIT_MS - now))
  applyTimer = setTimeout(() => void applyEvents(), wait)
}

/**
 * M3-7：详情面板的归类信息。`catalog.changed` 不带文件（档号、题名、页序可能变了），收到就让面板重新取当前这个文件的
 * （面板自己去抖 150 ms）；挂接、移出另有这个文件的 `files.changed`，走 detailRefresh。
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
  scheduleApply()
}

async function applyEvents() {
  applyTimer = null
  firstPendingAt = 0
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
  const listAffected =
    !!cur &&
    (folders.has(cur.id) ||
      !!children.value?.folders.some((f) => folders.has(f.id)) ||
      !!children.value?.files.some((f) => files.has(f.file_id)))
  // 当前文件夹由 reloadList 重新加载（它同时更新树上这一层），树上不重复请求。
  void tree.refresh(folders, listAffected ? cur?.id : null)
  if (!cur) {
    // 当前文件夹正在打开（面包屑还没取到）：打开完成后补刷一次。
    if (listState.value === 'loading') eventsWhileOpening = true
    return
  }
  if (listAffected) void reloadList()
  if (crumbs.value.some((c) => folders.has(c.id))) void reloadBreadcrumb()
}

/** 当前文件夹还没打开成功（首次加载失败）：重新打开它；正在加载时什么也不做。 */
function reopenIfFailed(): boolean {
  if (folder.value) return false
  const p = project.current
  if (p && listState.value !== 'loading') void openFolder(folderQuery.value ?? p.root_folder_id)
  return true
}

/** 整体刷新：resync、断线重连之后（断开期间的事件不会补发）。 */
async function refreshAll() {
  images.invalidate()
  detailRefresh.value++
  if (reopenIfFailed()) {
    await tree.refreshAll()
    return
  }
  await Promise.all([tree.refreshAll(folder.value?.id), reloadList(), reloadBreadcrumb()])
}

const stream = useProjectStream(() => project.currentId, {
  onEvent,
  onOpen: (first) => {
    if (!first) {
      // 重连成功（含从往返缓存回来）：断开期间的事件不会补发，整体刷新。
      pendingFull = true
      scheduleApply()
    } else if (listLoadedAt > 0) {
      // 第一次连上时列表已经加载好了：补刷列表和树（加载和连接建立之间可能漏了事件）。
      void reloadList()
      void tree.refreshAll(folder.value?.id)
    } else if (listState.value === 'error') {
      // 首次加载失败（例如 server 刚恢复）：重新打开。
      reopenIfFailed()
    }
  },
  // 403、404：这个项目已经进不去了。刷新项目列表，切换器会换到还能进的项目（页面随之重置）。
  onStopped: (err) => {
    if (err.status === 403 || err.status === 404) void project.refresh()
  },
})

onBeforeUnmount(() => {
  alive = false
  openSeq++
  listSeq++
  if (applyTimer) clearTimeout(applyTimer)
  applyTimer = null
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
  const cur = folder.value
  const set = new Set(ids)
  const listToo = !!cur && set.has(cur.id)
  void tree.refresh(set, listToo ? cur.id : null)
  if (listToo) void reloadList()
  if (crumbs.value.some((c) => set.has(c.id))) void reloadBreadcrumb()
}

const actions = useFileActions({
  project: () => project.current,
  folder: () => folder.value,
  selected: () => selectedEntries.value,
  selection,
  afterChange,
  openSnapshots,
  navigate: (id) => void navigate(id),
})

const inShell = isTauri()
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
  if (cmd === 'rename') actions.renameSelected()
  else if (cmd === 'delete') actions.deleteSelected()
  else void actions.copySelected()
}

// 上传：浏览器里用两个隐藏的文件选择框；壳里直接调上传队列的命令。
const filesInput = ref<HTMLInputElement | null>(null)
const folderInput = ref<HTMLInputElement | null>(null)

function pickUpload(mode: 'files' | 'folder') {
  if (inShell) {
    actions.startUpload(mode)
    return
  }
  ;(mode === 'folder' ? folderInput : filesInput).value?.click()
}

function onUploadPicked(mode: 'files' | 'folder', ev: Event) {
  const input = ev.target as HTMLInputElement
  const list = Array.from(input.files ?? [])
  // 清空，下次选同样的文件也会触发 change。
  input.value = ''
  actions.startUpload(mode, list)
}

/** 截图脚本注入合成的文件（走和选择框相同的代码）。 */
function onInjected(mode: 'files' | 'folder', ev: Event) {
  const list = (ev as CustomEvent<File[]>).detail
  if (Array.isArray(list)) actions.startUpload(mode, list)
}
const injectFiles = (ev: Event) => onInjected('files', ev)
const injectFolder = (ev: Event) => onInjected('folder', ev)
watch(filesInput, (el, old) => {
  old?.removeEventListener('doclite-inject-files', injectFiles)
  el?.addEventListener('doclite-inject-files', injectFiles)
})
watch(folderInput, (el, old) => {
  old?.removeEventListener('doclite-inject-files', injectFolder)
  el?.addEventListener('doclite-inject-files', injectFolder)
})

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
    <header class="flex h-14 shrink-0 items-center gap-3 border-b px-5">
      <h1 class="shrink-0 text-base font-semibold tracking-tight text-foreground">文件目录</h1>
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
      <div class="flex-1" />
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
      <Button
        v-if="project.current"
        variant="outline"
        size="sm"
        data-testid="goto-folder"
        @click="pickerOpen = true"
      >
        <FolderInputIcon />转到…
      </Button>
      <div class="flex shrink-0 rounded-md bg-muted p-0.5" role="radiogroup" aria-label="视图">
        <button
          type="button"
          role="radio"
          :aria-checked="view === 'icons'"
          class="flex items-center gap-1 rounded px-2 py-1 text-xs"
          :class="view === 'icons' ? 'bg-background font-medium text-foreground shadow-sm' : 'text-muted-foreground'"
          data-testid="view-icons"
          @click="view = 'icons'"
        >
          <LayoutGridIcon class="size-3.5" />图标
        </button>
        <button
          type="button"
          role="radio"
          :aria-checked="view === 'list'"
          class="flex items-center gap-1 rounded px-2 py-1 text-xs"
          :class="view === 'list' ? 'bg-background font-medium text-foreground shadow-sm' : 'text-muted-foreground'"
          data-testid="view-list"
          @click="view = 'list'"
        >
          <ListIcon class="size-3.5" />列表
        </button>
      </div>
    </header>

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
        <FolderTree :tree="tree" :selected-id="folder?.id ?? folderQuery ?? project.current.root_folder_id" @select="navigate" />
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
            <Button variant="outline" size="xs" :disabled="!folder" data-testid="new-folder" @click="actions.newFolder()">
              <FolderPlusIcon />新建文件夹
            </Button>
            <Button variant="outline" size="xs" :disabled="!folder" data-testid="upload-files" @click="pickUpload('files')">
              <UploadIcon />上传文件
            </Button>
            <Button variant="outline" size="xs" :disabled="!folder" data-testid="upload-folder" @click="pickUpload('folder')">
              <FolderUpIcon />上传文件夹
            </Button>
            <!-- 浏览器上传用的选择框（壳里不用）。 -->
            <input
              ref="filesInput"
              type="file"
              multiple
              :accept="UPLOAD_ACCEPT"
              class="hidden"
              data-testid="upload-files-input"
              @change="onUploadPicked('files', $event)"
            >
            <input
              ref="folderInput"
              type="file"
              multiple
              webkitdirectory
              class="hidden"
              data-testid="upload-folder-input"
              @change="onUploadPicked('folder', $event)"
            >
          </div>
          <span class="shrink-0 border-l pl-2">按扫描顺序</span>
        </div>

        <ContextMenu :modal="false">
          <ContextMenuTrigger as-child>
            <div class="flex min-h-0 flex-1 flex-col" data-testid="file-area" @contextmenu.capture="onAreaContextMenu">
              <div v-if="listState === 'error' && !children" class="flex flex-1 flex-col items-center justify-center gap-3 text-sm">
                <p class="text-destructive">{{ listError ?? '加载失败' }}</p>
                <Button variant="outline" size="sm" @click="openFolder(folderQuery ?? project.current.root_folder_id)">重试</Button>
              </div>
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
                :entries="entries"
                :view="view"
                :selection="selection"
                @open="onOpen"
                @command="onGridCommand"
              />
            </div>
          </ContextMenuTrigger>
          <ContextMenuContent class="w-56" data-testid="context-menu" :data-kind="menuKind">
            <!-- 一个文件 -->
            <template v-if="menuKind === 'file'">
              <ContextMenuItem data-testid="menu-open" @select="menuEntry && actions.openEntry(menuEntry)">
                <EyeIcon />打开
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-view-original" @select="viewOriginal(menuEntry)">
                <ImageIcon />查看原图
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-rename" @select="actions.renameSelected()">
                <PencilIcon />重命名<ContextMenuShortcut>F2</ContextMenuShortcut>
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-move" @select="actions.moveSelected()">
                <FolderInputIcon />移动到…
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-copy" @select="actions.copySelected()">
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
              <ContextMenuItem variant="destructive" data-testid="menu-delete" @select="actions.deleteSelected()">
                <Trash2Icon />删除<ContextMenuShortcut>Del</ContextMenuShortcut>
              </ContextMenuItem>
            </template>
            <!-- 多个文件 -->
            <template v-else-if="menuKind === 'files'">
              <ContextMenuLabel class="text-xs text-muted-foreground">已选 {{ actions.files.value.length }} 个文件</ContextMenuLabel>
              <ContextMenuItem data-testid="menu-move" @select="actions.moveSelected()">
                <FolderInputIcon />移动到…
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-copy" @select="actions.copySelected()">
                <CopyIcon />复制<ContextMenuShortcut>{{ modKey }}+C</ContextMenuShortcut>
              </ContextMenuItem>
              <ContextMenuSeparator />
              <ContextMenuItem variant="destructive" data-testid="menu-delete" @select="actions.deleteSelected()">
                <Trash2Icon />删除<ContextMenuShortcut>Del</ContextMenuShortcut>
              </ContextMenuItem>
            </template>
            <!-- 一个文件夹 -->
            <template v-else-if="menuKind === 'folder'">
              <ContextMenuItem data-testid="menu-open" @select="menuEntry && actions.openEntry(menuEntry)">
                <FolderOpenIcon />打开
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-rename" @select="actions.renameSelected()">
                <PencilIcon />重命名<ContextMenuShortcut>F2</ContextMenuShortcut>
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-remark" @select="editSelectedFolderRemark()">
                <NotebookPenIcon />修改备注
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-move" @select="actions.moveSelected()">
                <FolderInputIcon />移动到…
              </ContextMenuItem>
              <ContextMenuSeparator />
              <ContextMenuItem variant="destructive" data-testid="menu-delete" @select="actions.deleteSelected()">
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
              <ContextMenuItem data-testid="menu-new-folder" @select="actions.newFolder()">
                <FolderPlusIcon />新建文件夹
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-upload-files" @select="pickUpload('files')">
                <UploadIcon />上传文件
              </ContextMenuItem>
              <ContextMenuItem data-testid="menu-upload-folder" @select="pickUpload('folder')">
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
            <Button variant="ghost" size="xs" data-testid="bar-move" @click="actions.moveSelected()">
              <FolderInputIcon />移动到…
            </Button>
            <Button variant="ghost" size="xs" data-testid="bar-copy" @click="actions.copySelected()">
              <CopyIcon />复制
            </Button>
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
            <Button
              variant="ghost"
              size="xs"
              class="text-destructive hover:text-destructive"
              data-testid="bar-delete"
              @click="actions.deleteSelected()"
            >
              <Trash2Icon />删除
            </Button>
          </template>
          <template v-else-if="actions.kind.value === 'folder'">
            <Button variant="ghost" size="xs" data-testid="bar-rename" @click="actions.renameSelected()">
              <PencilIcon />重命名
            </Button>
            <Button variant="ghost" size="xs" data-testid="bar-move" @click="actions.moveSelected()">
              <FolderInputIcon />移动到…
            </Button>
            <Button
              variant="ghost"
              size="xs"
              class="text-destructive hover:text-destructive"
              data-testid="bar-delete"
              @click="actions.deleteSelected()"
            >
              <Trash2Icon />删除
            </Button>
          </template>
          <span v-else class="text-muted-foreground">{{ actions.kind.value === 'folders' ? '文件夹一次只能操作一个' : '文件夹和文件要分开操作' }}</span>
          <div class="flex-1" />
          <button type="button" class="text-muted-foreground hover:text-foreground" @click="selection.clear()">
            取消选择（Esc）
          </button>
        </div>

        <UploadPanel :project-id="project.current.id" />
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

    <!-- 查看原图（M2-4） -->
    <OriginalViewerDialog v-if="viewerMounted" v-model:open="viewerOpen" :files="viewerFiles" :file-id="viewerFileId" />

    <!-- 上传重扫件（M2-7） -->
    <RescanDialog v-model:open="rescanOpen" :target="rescanTarget" :project-id="project.currentId" @finished="onRescanFinished" />
  </div>
</template>
