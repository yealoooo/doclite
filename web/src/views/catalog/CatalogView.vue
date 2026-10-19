<script setup lang="ts">
// 归类页 /catalog（M3-5；M9-2 按 requirements 5.9"界面 /catalog"和 Mockup v3.1 重写；菜单码 catalog）：左右两个资源
// 管理器，中间"归档 →"。只能整个文件夹归档：没有待归类池、页序条、按页挂接、新建空条目、标记完成 / 退回草稿。
//
// - **左：文件目录**（FolderPane，要 files 或 scan 菜单，没有时给说明）：目录树 + 当前文件夹（地址栏 `?folder=`）；
//   子文件夹显示归档状态；打开已归档的文件夹时顶部紫色（已封存）/ 橙色（修改中）横条。选中的文件夹拖到右边树上的节点、
//   件列表，或者点中间的"归档 →"（放到右边选中的节点），打开归档预览（ArchiveDialog）。
// - **右：归档目录**：归类树（CategoryTree；工具栏和右键：新建、改名称编码类型、移动、删除、重新生成档号、导出）+
//   选中节点的件列表（ItemList：件号、题名、页数、封存和锁定状态；勾选后批量锁定）；双击打开一件（ItemDetail，替换列表，
//   "‹ 返回"回到列表）。根下直接放的件单列一行。
// - **解除封存**（components/catalog/UnsealDialog）、**重新归档**（ResealDialog，先给对比）从左栏横条、件详情、
//   "修改中 n 件"清单打开。
// - **不让人忘**：顶部常驻"✎ 修改中 n 件"（modifying.ts，`GET archive-items:modifying`），
//   点开列出件、文件夹、谁、原因（ModifyingDialog）。
// - 导出入口（M4-5）：节点（工具栏、右键、节点的空白页）和件详情上的"导出"，带着范围跳到 /export；只有 export 菜单的人看得到。
// - **地址栏**：`?folder=<左栏的文件夹>&category=<节点>&item=<件>`。
// - 著录表单有未保存的修改时，换件、换节点、离开页面、在切换器里换项目都先确认。著录设置改了不发事件：进入页面、切换
//   节点、写操作失败（4xx）时重取 catalog-settings。
// - **实时**（useProjectStream，去抖：最后一条事件后 300 ms、最多等 1 秒；同时最多跑一轮）：
//   - `catalog.changed {category_ids?, item_ids?}`：树按节点刷新它自己和祖先；item_ids 非空时当前节点在 category_ids 里或
//     列表里有这些件就重取列表，打开的件在里面就重取详情；空数组只有节点变了；没带时把 category_ids 当子树的根。
//   - `files.changed`：打开的件的页在里面就重取详情；列表里有页的件可能变了（"n 待审"）；左栏按 folder_ids 刷新。
//   - `folders.changed`：左栏（归档状态、名字、文件夹没了）；打开的件的来源路径可能变了。
//   - `resync`、断线重连：整体刷新。
//   catalog.changed、folders.changed 之后重取"修改中 n 件"（modifying.ts，专门的接口）。
import {
  ArrowRightIcon,
  ArrowRightLeftIcon,
  FileOutputIcon,
  FolderPlusIcon,
  LoaderCircleIcon,
  LockIcon,
  PencilIcon,
  PencilLineIcon,
  PlusIcon,
  RefreshCwIcon,
  Trash2Icon,
} from '@lucide/vue'
import { useEventListener } from '@vueuse/core'
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import {
  deleteArchiveCategory,
  getCatalogPlacements,
  getCatalogSettings,
  listArchiveItems,
  lockArchiveItems,
  MAX_ARCHIVE_FOLDERS,
  MAX_ITEMS_PAGE,
  MAX_LOCK_ITEMS,
  renumberArchiveItems,
  updateArchiveCategory,
} from '@/api/catalog'
import { ApiError, errorMessage, isApiError } from '@/api/http'
import type {
  ArchiveCategory,
  ArchiveItem,
  ArchiveItemSummary,
  ArchiveResult,
  CatalogSettings,
  FileEntry,
  FolderArchive,
  MyProject,
  ProjectEvent,
  SubfolderEntry,
  UpdatedArchiveCategory,
} from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import CategoryPicker, { type PickedCategory } from '@/components/catalog/CategoryPicker.vue'
import CategoryTree from '@/components/catalog/CategoryTree.vue'
import UnsealDialog from '@/components/catalog/UnsealDialog.vue'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from '@/components/ui/context-menu'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useCategoryTree } from '@/composables/useCategoryTree'
import { useProjectStream } from '@/composables/useProjectStream'
import { useSelection } from '@/composables/useSelection'
import {
  CATEGORY_TYPES,
  SAMPLE_CODES,
  categoryTypeLabel,
  describeArchiveNoTemplate,
  formatItemNo,
  itemShortLabel,
  parseArchiveNoTemplate,
  renderArchiveNo,
} from '@/lib/catalog'
import { newExportQuery } from '@/lib/export'
import { useImageInfoStore } from '@/stores/image-info'
import { useProjectStore } from '@/stores/project'
import { useSessionStore } from '@/stores/session'
import { handledGlobally } from '@/views/review-assign/review-errors'

import ArchiveDialog from './ArchiveDialog.vue'
import { categoryOpErrorText } from './catalog-errors'
import CategoryFormDialog from './CategoryFormDialog.vue'
import { folderDrag, isFolderDrag } from './drag'
import FolderPane from './FolderPane.vue'
import ItemDetail from './ItemDetail.vue'
import ItemList from './ItemList.vue'
import { loadModifyingItems, type ModifyingItem } from './modifying'
import ModifyingDialog from './ModifyingDialog.vue'
import ResealDialog from './ResealDialog.vue'

const OriginalViewerDialog = defineAsyncComponent(() => import('@/views/files/OriginalViewerDialog.vue'))

const project = useProjectStore()
const session = useSessionStore()
const images = useImageInfoStore()
const route = useRoute()
const router = useRouter()
const tree = useCategoryTree()

const categoryQuery = computed(() => (typeof route.query.category === 'string' ? route.query.category : null))
const itemQuery = computed(() => (typeof route.query.item === 'string' ? route.query.item : null))
/** 左栏的文件夹（M9）。 */
const folderQuery = computed(() => (typeof route.query.folder === 'string' ? route.query.folder : null))
/** 浏览文件目录（左栏用 children 接口）要 files 或 scan 菜单（内置归类员有 files）。 */
const canBrowse = computed(() => session.menuCodes.has('files') || session.menuCodes.has('scan'))
/** 能去「文件目录」页修改（files 菜单）。 */
const canFiles = computed(() => session.menuCodes.has('files'))
/** 导出入口（M4-5）：只有 export 菜单的人看得到（内置负责人和 admin）。 */
const canExport = computed(() => session.menuCodes.has('export'))

/** 带着范围跳到导出页的新建导出（M4-5）；有未保存的修改时由路由守卫先确认。 */
function goExport(mode: 'category' | 'item', id: string) {
  void router.push({ path: '/export', query: newExportQuery({ mode, ids: [id] }) })
}

let alive = true

// ---------- 著录设置 ----------

const settings = ref<CatalogSettings | null>(null)
let settingsSeq = 0

async function loadSettings() {
  const pid = project.currentId
  if (!pid) return
  const my = ++settingsSeq
  try {
    const s = await getCatalogSettings(pid, true)
    if (my !== settingsSeq || project.currentId !== pid) return
    settings.value = s
  } catch (e) {
    if (my === settingsSeq && !settings.value && !handledGlobally(e)) toast.error(`取不到著录设置：${errorMessage(e)}`)
  }
}

const parsedTemplate = computed(() => (settings.value ? parseArchiveNoTemplate(settings.value.archive_no_template) : null))
const itemNoWidth = computed(() => (parsedTemplate.value?.ok ? parsedTemplate.value.template.itemNoWidth : 0))

// ---------- 归类树 ----------

const treeState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const treeError = ref('')
const category = computed(() => (categoryQuery.value ? (tree.nodes.get(categoryQuery.value) ?? null) : null))
/** 打开的是根节点（"直接放在顶层的件"）。 */
const atRoot = computed(() => !!categoryQuery.value && categoryQuery.value === tree.rootId.value)
/** 节点操作（新建下级、修改、移动、删除、重新生成）针对的节点：根节点不能改，也不在树上显示。 */
const opNode = computed(() => (atRoot.value ? null : category.value))

let lastProjectId: string | null = null
let projectSeq = 0

async function resetForProject(p: MyProject) {
  const my = ++projectSeq
  settings.value = null
  items.value = []
  listState.value = 'idle'
  selection.clear()
  blockedIds.value = new Set()
  modifyingItems.value = []
  modifyingTotal.value = 0
  treeState.value = 'loading'
  void loadSettings()
  try {
    await tree.init(p.id)
    if (my !== projectSeq) return
    treeState.value = 'ready'
    void loadModifying()
  } catch (e) {
    if (my !== projectSeq) return
    treeState.value = 'error'
    treeError.value = errorMessage(e)
  }
}

let openSeq = 0
/** 进入一个节点：在树上展开到它，加载它的件。 */
async function openCategory(id: string | null) {
  const my = ++openSeq
  selection.clear()
  blockedIds.value = new Set()
  if (!id) {
    items.value = []
    listState.value = 'idle'
    return
  }
  if (treeState.value !== 'ready') return
  if (listCategoryId !== id) {
    items.value = []
    listState.value = 'loading'
  }
  const node = await tree.reveal(id).catch(() => null)
  if (my !== openSeq) return
  if (!node) {
    toast.error('归类节点不存在或已被删除')
    await setQuery({ category: null, item: null }, true)
    return
  }
  await loadList()
}

/** 改地址栏的一部分（其余的保留）。 */
function setQuery(patch: Record<string, string | null>, replace = false) {
  if (!alive) return Promise.resolve()
  const query: Record<string, string> = {}
  for (const [k, v] of Object.entries(route.query)) if (typeof v === 'string') query[k] = v
  for (const [k, v] of Object.entries(patch)) {
    if (v) query[k] = v
    else delete query[k]
  }
  if (!query.category) delete query.item
  return replace ? router.replace({ query }) : router.push({ query })
}

function go(categoryId: string | null, itemId: string | null = null, replace = false) {
  return setQuery({ category: categoryId, item: itemId }, replace)
}

function selectCategory(id: string) {
  if (id === categoryQuery.value && !itemQuery.value) return
  void go(id)
}

/** 新建节点时默认的类型：上级的下一级（全宗 → 门类 → 年度 → 保管期限 → 机构 → 自定义）。 */
function nextType(parent: ArchiveCategory | null): string {
  if (!parent) return 'fonds'
  const i = (CATEGORY_TYPES as readonly string[]).indexOf(parent.category_type)
  return CATEGORY_TYPES[Math.min(i + 1, CATEGORY_TYPES.length - 1)] ?? 'custom'
}

// ---------- 件列表 ----------

const PAGE = 500
const items = ref<ArchiveItemSummary[]>([])
const listTotal = ref(0)
const listCounts = ref({ draft: 0, locked: 0, modifying: 0 })
const nextCursor = ref<string | null>(null)
const listState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const listError = ref('')
const loadingMore = ref(false)
const loadMoreError = ref<string | null>(null)
/** 列表属于哪个节点。 */
let listCategoryId: string | null = null
let listSeq = 0
let listLoadedAt = 0

const selection = useSelection(() => items.value.map((i) => i.id))
watch(items, () => selection.prune())
/** 批量锁定时不满足条件的件（409 的 details.ids）。 */
const blockedIds = ref<ReadonlySet<string>>(new Set())
const itemListRef = ref<InstanceType<typeof ItemList> | null>(null)

/** 重新取列表；已经往下翻过的，取回同样多（滚动位置不变）。 */
async function loadList() {
  const cid = categoryQuery.value
  if (!cid) return
  const my = ++listSeq
  const want = listCategoryId === cid ? Math.max(PAGE, items.value.length) : PAGE
  try {
    const collected: ArchiveItemSummary[] = []
    let cursor: string | null = null
    let total = 0
    let counts = { draft: 0, locked: 0, modifying: 0 }
    for (;;) {
      const res = await listArchiveItems(cid, { limit: Math.min(MAX_ITEMS_PAGE, Math.max(PAGE, want - collected.length)), cursor }, ['not_found'])
      if (my !== listSeq || categoryQuery.value !== cid) return
      collected.push(...res.items)
      total = res.total
      counts = res.counts
      cursor = res.next_cursor
      if (!cursor || collected.length >= want) break
    }
    listCategoryId = cid
    items.value = collected
    listTotal.value = total
    listCounts.value = counts
    nextCursor.value = cursor
    listState.value = 'ready'
    loadMoreError.value = null
    listLoadedAt = Date.now()
  } catch (e) {
    if (my !== listSeq) return
    if (isApiError(e, 'not_found')) {
      categoryGone(cid)
      return
    }
    if (listCategoryId === cid && items.value.length) return
    listState.value = 'error'
    listError.value = errorMessage(e)
  }
}

async function loadMore() {
  const cid = categoryQuery.value
  const cursor = nextCursor.value
  if (!cid || !cursor || loadingMore.value || listCategoryId !== cid) return
  const my = listSeq
  loadingMore.value = true
  try {
    const res = await listArchiveItems(cid, { limit: PAGE, cursor }, true)
    if (my !== listSeq || categoryQuery.value !== cid) return
    const have = new Set(items.value.map((i) => i.id))
    items.value = [...items.value, ...res.items.filter((i) => !have.has(i.id))]
    nextCursor.value = res.next_cursor
    listTotal.value = res.total
    listCounts.value = res.counts
    loadMoreError.value = null
  } catch (e) {
    if (my === listSeq) loadMoreError.value = errorMessage(e)
  } finally {
    loadingMore.value = false
  }
}

let goneHandled: string | null = null
function categoryGone(id: string) {
  if (!alive || goneHandled === id || categoryQuery.value !== id) return
  goneHandled = id
  const parent = tree.nodes.get(id)?.parent_id ?? null
  toast.info('当前归类节点已被删除，已回到上一级')
  void tree.refreshShown()
  void go(parent && parent !== tree.rootId.value ? parent : null, null, true)
}

/** 用详情更新列表里的这一行（计数跟着改）；不在这个节点里了就去掉。 */
function patchRow(it: ArchiveItem) {
  const i = items.value.findIndex((x) => x.id === it.id)
  if (i < 0) return
  const old = items.value[i]!
  if (it.category_id !== listCategoryId) {
    items.value = items.value.filter((x) => x.id !== it.id)
    void loadList()
    return
  }
  const row: ArchiveItemSummary = {
    id: it.id,
    category_id: it.category_id,
    item_no: it.item_no,
    archive_no: it.archive_no,
    title: it.title,
    status: it.status,
    row_version: it.row_version,
    page_count: it.page_count,
    page_counts: it.page_counts,
    locked_at: it.locked_at,
    new_version_after_lock: it.new_version_after_lock,
    updated_at: it.updated_at,
    source_folder_id: it.source_folder_id,
    source_folder_name: it.source_folder_name,
    source_folder_path: it.source_folder_path,
    source_folder_deleted: it.source_folder_deleted,
    source_status: it.source_status,
    unsealed_by: it.unsealed_by,
    unsealed_by_name: it.unsealed_by_name,
    unsealed_at: it.unsealed_at,
    unseal_reason: it.unseal_reason,
  }
  const next = [...items.value]
  next[i] = row
  // 改了件号：按件号重新排。
  if (old.item_no !== it.item_no) next.sort((a, b) => a.item_no - b.item_no || (a.id < b.id ? -1 : 1))
  items.value = next
  if (old.status !== it.status || old.source_status !== it.source_status) {
    const c = { ...listCounts.value }
    c[old.status] = Math.max(0, c[old.status] - 1)
    c[it.status] += 1
    if (old.source_status === 'modifying') c.modifying = Math.max(0, c.modifying - 1)
    if (it.source_status === 'modifying') c.modifying += 1
    listCounts.value = c
  }
}

// ---------- 打开的件 ----------

const detailRef = ref<InstanceType<typeof ItemDetail> | null>(null)
const detailRefresh = ref(0)
const detailItem = ref<ArchiveItem | null>(null)
const detailDirty = ref(false)

function openItem(id: string) {
  if (id === itemQuery.value) return
  void go(categoryQuery.value, id)
}

function onDetailLoaded(it: ArchiveItem) {
  detailItem.value = it
  patchRow(it)
  syncTreePath(it)
}

function onDetailChanged(it: ArchiveItem) {
  detailItem.value = it
  patchRow(it)
  syncTreePath(it)
}

/** 树上这个节点从根起的名称链（和 server 的 category_path 同一个格式：根名/…/节点名）；树上不知道时为 null。 */
function treeCategoryPath(id: string): string | null {
  const rootId = tree.rootId.value
  const root = rootId ? tree.nodes.get(rootId) : undefined
  if (!root || !tree.nodes.has(id)) return null
  return `${root.category_name}${tree.pathOf(id)}`
}

/** 详情里显示的路径：树上的名称链和 server 的一致时用树上的（名称里可能有 /），否则交给 ItemDetail 用 server 的。 */
const detailCategoryLabel = computed(() => {
  const it = detailItem.value
  if (!it) return null
  if (it.category_id === tree.rootId.value) return '（直接放在顶层）'
  return treeCategoryPath(it.category_id) === it.category_path ? tree.pathText(it.category_id) : null
})

/** 树上的名称链和 server 的不一致：重新取这个节点和它现在的上级，再展开到它。每个路径只修一次。 */
let treeSyncedFor = ''
function syncTreePath(it: ArchiveItem) {
  if (it.category_id === tree.rootId.value || !tree.nodes.has(it.category_id)) return
  if (treeCategoryPath(it.category_id) === it.category_path) return
  const key = `${it.category_id}|${it.category_path}`
  if (treeSyncedFor === key) return
  treeSyncedFor = key
  void (async () => {
    const res = await tree.load(it.category_id).catch(() => null)
    const parent = res?.parent.parent_id
    if (parent) await tree.load(parent).catch(() => null)
    await tree.reveal(it.category_id).catch(() => null)
  })()
}

function onDetailMoved(it: ArchiveItem, from: string) {
  detailItem.value = it
  patchRow(it)
  void tree.refreshFor([from, it.category_id])
  const to = it.category_id
  toast.success(`已移到「${tree.nodes.get(to)?.category_name ?? '目标节点'}」，现在是第 ${it.item_no} 件`, {
    description: it.archive_no ? `档号 ${it.archive_no}` : '档号为空（原因见件详情）',
    action: { label: '查看', onClick: () => void go(to, it.id) },
  })
  void go(categoryQuery.value, null, true)
}

function onDetailGone(id: string) {
  items.value = items.value.filter((x) => x.id !== id)
  if (itemQuery.value === id) {
    toast.info('这一件已不存在（可能刚被撤销归档），已刷新')
    void go(categoryQuery.value, null, true)
  }
  void loadList()
}

/** 自己撤销了归档：从列表里去掉，回到节点，刷新树上的件数和左栏（文件夹回到未归档）。 */
function onDetailDeleted(it: ArchiveItem) {
  items.value = items.value.filter((x) => x.id !== it.id)
  toast.success(`已撤销归档 ${it.archive_no ?? `第 ${it.item_no} 件`}`, {
    description: `文件夹「${it.source_folder_name}」回到"未归档"`,
  })
  void tree.refreshFor([it.category_id])
  void go(categoryQuery.value, null, true)
  void loadList()
  paneRef.value?.refresh()
  void loadModifying()
}

watch(itemQuery, (id) => {
  if (!id) detailItem.value = null
  else void nextTick(() => itemListRef.value?.scrollToItem(id))
})

// 看原图（左栏的文件、件详情的页双击）
const viewer = reactive({ mounted: false, open: false, files: [] as FileEntry[], fileId: null as string | null })
function openViewer(files: FileEntry[], fileId: string) {
  viewer.files = files
  viewer.fileId = fileId
  viewer.mounted = true
  viewer.open = true
}

// ---------- 左栏和归档（M9） ----------

const paneRef = ref<InstanceType<typeof FolderPane> | null>(null)
/** 左栏选中的子文件夹。 */
const leftSelected = ref<SubfolderEntry[]>([])

function setPaneFolder(id: string | null) {
  void setQuery({ folder: id })
}

/** 归档要放到的节点：右边选中的节点（不能是根）。 */
const archiveTargetNode = computed(() => (opNode.value ? opNode.value : null))
const midTip = computed(() => {
  if (!leftSelected.value.length) return '左边选文件夹（只能整个文件夹）'
  if (!archiveTargetNode.value) return '右边先选一个节点'
  return `放到「${archiveTargetNode.value.category_name}」`
})

const archive = reactive({ open: false, categoryId: null as string | null, label: '', folderIds: [] as string[] })

function openArchive(categoryId: string, folderIds: string[]) {
  if (!folderIds.length) return
  if (categoryId === tree.rootId.value) {
    toast.info('不能归档到顶层，请放到一个归类节点上')
    return
  }
  if (folderIds.length > MAX_ARCHIVE_FOLDERS) {
    toast.error(`一次最多归档 ${MAX_ARCHIVE_FOLDERS} 个文件夹，选中了 ${folderIds.length} 个`)
    return
  }
  archive.categoryId = categoryId
  archive.label = tree.pathText(categoryId) || (tree.nodes.get(categoryId)?.category_name ?? '')
  archive.folderIds = folderIds
  archive.open = true
}

function archiveSelected() {
  const node = archiveTargetNode.value
  if (!node || !leftSelected.value.length) return
  openArchive(node.id, leftSelected.value.map((f) => f.id))
}

async function onArchived(res: ArchiveResult) {
  const cid = res.category_id
  const name = tree.nodes.get(cid)?.category_name ?? '目标节点'
  toast.success(`已在「${name}」新建 ${res.items.length} 件，文件夹已封存`, {
    description: res.skipped.length ? `跳过 ${res.skipped.length} 个文件夹` : undefined,
  })
  paneRef.value?.clearSelection()
  paneRef.value?.refresh()
  void tree.refreshFor([cid])
  if (categoryQuery.value !== cid || itemQuery.value) await go(cid)
  else await loadList()
}

// 拖放：左栏的文件夹 → 右边树上的节点或件列表
const dropTarget = ref<string | null>(null)
/** 拖动经过件列表（放到当前节点）。 */
const dropOnList = ref(false)

function onTreeDragOver(ev: DragEvent) {
  if (!isFolderDrag(ev)) return
  const el = (ev.target as HTMLElement | null)?.closest<HTMLElement>('[data-category-id]')
  const id = el?.dataset.categoryId ?? null
  dropTarget.value = id
  if (!id) return
  ev.preventDefault()
  if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'copy'
}
function onTreeDrop(ev: DragEvent) {
  const id = dropTarget.value
  const drag = folderDrag.value
  dropTarget.value = null
  if (!id || !drag || !isFolderDrag(ev)) return
  ev.preventDefault()
  openArchive(id, drag.ids)
}
function onListDragOver(ev: DragEvent) {
  if (!isFolderDrag(ev) || !opNode.value || itemQuery.value) return
  ev.preventDefault()
  dropOnList.value = true
  if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'copy'
}
function onListDrop(ev: DragEvent) {
  const drag = folderDrag.value
  dropOnList.value = false
  if (!drag || !opNode.value || !isFolderDrag(ev)) return
  ev.preventDefault()
  openArchive(opNode.value.id, drag.ids)
}
useEventListener(window, 'dragend', () => {
  dropTarget.value = null
  dropOnList.value = false
})

// ---------- 解除封存、重新归档（M9） ----------

const unseal = reactive({ open: false, itemId: null as string | null, label: '' })
const reseal = reactive({ open: false, itemId: null as string | null, label: '' })

function folderArchiveLabel(a: FolderArchive) {
  return itemShortLabel(a, itemNoWidth.value)
}
function itemLabel(it: { category_id: string; item_no: number }) {
  const node = tree.nodes.get(it.category_id)
  const no = formatItemNo(it.item_no, itemNoWidth.value)
  return node && it.category_id !== tree.rootId.value ? `${node.category_name} ${no}` : `第 ${no} 件`
}

function askUnseal(itemId: string, label: string) {
  unseal.itemId = itemId
  unseal.label = label
  unseal.open = true
}
function askReseal(itemId: string, label: string) {
  reseal.itemId = itemId
  reseal.label = label
  reseal.open = true
}

/** 解除封存、重新归档、解锁之后：更新打开的件、列表、左栏、"修改中 n 件"。 */
function afterSealChange(it: ArchiveItem) {
  if (detailItem.value?.id === it.id) detailRef.value?.apply(it)
  patchRow(it)
  paneRef.value?.refresh()
  void loadModifying()
}

function onUnsealed(it: ArchiveItem) {
  afterSealChange(it)
  toast.success('已解除封存：去文件目录里修改，改完点"重新归档"', {
    action: canFiles.value ? { label: '去文件目录', onClick: () => openFiles(it.source_folder_id) } : undefined,
  })
}
function onResealed(it: ArchiveItem) {
  afterSealChange(it)
  toast.success(`${unsealLabelOf(it)} 已重新归档并封存`, { description: `现在 ${it.page_count} 页` })
}
function unsealLabelOf(it: ArchiveItem) {
  return reseal.label || itemLabel(it)
}

function openFiles(folderId: string) {
  void router.push({ path: '/files', query: { folder: folderId } })
}

// "修改中 n 件"
const modifyingItems = ref<ModifyingItem[]>([])
/** 修改中的真实件数（清单最多 500 件）。 */
const modifyingTotal = ref(0)
const modifyingOpen = ref(false)
let modifyingSeq = 0
async function loadModifying() {
  const pid = project.currentId
  if (!pid) return
  const my = ++modifyingSeq
  try {
    const list = await loadModifyingItems(pid)
    if (my === modifyingSeq && project.currentId === pid) {
      modifyingItems.value = list.items
      modifyingTotal.value = list.total
    }
  } catch {
    // 取不到就保留上一次的。
  }
}
function onModifyingOpenItem(it: ModifyingItem) {
  modifyingOpen.value = false
  void go(it.category_id, it.item_id)
}
function onModifyingReseal(it: ModifyingItem) {
  const node = it.category_names[it.category_names.length - 1]
  askReseal(it.item_id, node ? `${node} ${formatItemNo(it.item_no, itemNoWidth.value)}` : `第 ${it.item_no} 件`)
}

// ---------- 有未保存的修改时先确认 ----------

const discardAsk = reactive({ open: false, resolve: null as ((ok: boolean) => void) | null })

function confirmDiscard(): Promise<boolean> {
  if (!detailRef.value?.isDirty()) return Promise.resolve(true)
  return new Promise((resolve) => {
    discardAsk.resolve?.(false)
    discardAsk.resolve = resolve
    discardAsk.open = true
  })
}
function resolveDiscard(ok: boolean) {
  const r = discardAsk.resolve
  discardAsk.resolve = null
  discardAsk.open = false
  r?.(ok)
}
watch(
  () => discardAsk.open,
  (v) => {
    if (!v) resolveDiscard(false)
  },
)

// 在切换器里换项目：有没保存的著录时先确认（确认放弃就先清掉，换项目之后不再问一次）。
const removeSwitchGuard = project.addSwitchGuard(async () => {
  const ok = await confirmDiscard()
  if (ok) detailRef.value?.discard()
  return ok
})

onBeforeRouteUpdate(async (to, from) => {
  if (to.query.item === from.query.item && to.query.category === from.query.category) return true
  return confirmDiscard()
})
onBeforeRouteLeave(() => confirmDiscard())
useEventListener(window, 'beforeunload', (e: BeforeUnloadEvent) => {
  if (detailRef.value?.isDirty()) e.preventDefault()
})

// ---------- 批量锁定 ----------

const selectedIds = computed(() => [...selection.selected.value])
const allChecked = computed(() =>
  items.value.length > 0 && selection.count.value === items.value.length ? true : selection.count.value > 0 ? 'indeterminate' : false,
)
function toggleAll() {
  if (selection.count.value === items.value.length) selection.clear()
  else selection.selectAll()
}

const confirmBatch = reactive({ open: false, loading: false, ids: [] as string[] })

function askBatchLock() {
  const ids = selectedIds.value
  if (!ids.length || ids.length > MAX_LOCK_ITEMS) return
  confirmBatch.ids = ids
  confirmBatch.open = true
}

async function doBatchLock() {
  const pid = project.currentId
  const ids = confirmBatch.ids
  if (!pid || !ids.length || confirmBatch.loading) return
  confirmBatch.loading = true
  try {
    const res = await lockArchiveItems(pid, ids, true)
    confirmBatch.open = false
    blockedIds.value = new Set()
    toast.success(`已锁定 ${res.locked} 件${res.skipped ? `，跳过 ${res.skipped} 件已锁定的` : ''}`)
    selection.clear()
  } catch (e) {
    if (handledGlobally(e)) return
    const bad = e instanceof ApiError ? (e.details as { ids?: string[] } | undefined)?.ids : undefined
    if (isApiError(e, 'invalid_state') && bad?.length) {
      blockedIds.value = new Set(bad)
      confirmBatch.open = false
      toast.error('有件不满足锁定条件，一件都没有锁定', { description: `${(e as ApiError).message}（已在列表里标红）` })
    } else {
      toast.error(errorMessage(e))
      if (e instanceof ApiError && e.status >= 400 && e.status < 500) confirmBatch.open = false
    }
  } finally {
    confirmBatch.loading = false
    void loadList()
    detailRefresh.value++
  }
}

// ---------- 归类节点的操作 ----------

const formOpen = ref(false)
const formParent = ref<ArchiveCategory | null>(null)
const formNode = ref<ArchiveCategory | null>(null)
const formDefaultType = ref('fonds')
const formFocus = ref<'type' | 'code' | 'name'>('code')

/** 右键菜单针对的节点（null：在空白处点的）。 */
const menuNode = ref<ArchiveCategory | null>(null)

function onTreeContextMenu(ev: MouseEvent) {
  const el = (ev.target as HTMLElement | null)?.closest<HTMLElement>('[data-category-id]')
  const id = el?.dataset.categoryId
  menuNode.value = id ? (tree.nodes.get(id) ?? null) : null
}

function askCreate(parent: ArchiveCategory | null) {
  formParent.value = parent
  formNode.value = null
  formDefaultType.value = nextType(parent)
  formFocus.value = 'code'
  formOpen.value = true
}

function askEdit(node: ArchiveCategory | null, focus: 'type' | 'code' | 'name' = 'name') {
  if (!node) return
  formParent.value = null
  formNode.value = node
  formFocus.value = focus
  formOpen.value = true
}

/** 重算之后的提示：重新生成了几个档号、几个因为撞号为空。 */
function renumberToast(head: string, renumbered: number, conflicts: number) {
  if (conflicts > 0) {
    toast.warning(`${head}；有 ${conflicts} 件因为撞号档号为空`, {
      description: `${renumbered ? `重新生成了 ${renumbered} 个档号。` : ''}撞号的件档号为空、不能锁定，打开它们可以看到和谁撞了。`,
    })
  } else toast.success(renumbered ? `${head}，重新生成了 ${renumbered} 个档号` : head)
}

async function onCategoryCreated(node: ArchiveCategory) {
  toast.success(`已新建「${node.category_name}」（${categoryTypeLabel(node.category_type)} ${node.category_code}）`)
  const parentId = node.parent_id ?? tree.rootId.value
  if (parentId) await tree.load(parentId).catch(() => null)
  await go(node.id)
}

/** 节点改了编码、类型、位置之后：刷新树、当前列表和详情（档号可能都变了）。 */
async function afterCategoryChanged(ids: (string | null | undefined)[]) {
  await tree.refreshFor(ids.filter((x): x is string => !!x))
  void loadList()
  detailRefresh.value++
}

function onCategoryUpdated(node: UpdatedArchiveCategory, before: ArchiveCategory) {
  const what = [
    node.category_name !== before.category_name && '名称',
    node.category_code !== before.category_code && '编码',
    node.category_type !== before.category_type && '类型',
  ].filter(Boolean)
  renumberToast(`已修改「${node.category_name}」的${what.join('、') || '信息'}`, node.renumbered, node.archive_no_conflicts)
  void afterCategoryChanged([node.id, node.parent_id ?? tree.rootId.value])
}

// 移动节点
const movePicker = reactive({ open: false, node: null as ArchiveCategory | null })
function askMove(node: ArchiveCategory | null) {
  if (!node) return
  movePicker.node = node
  movePicker.open = true
}
async function doMove(target: PickedCategory) {
  const pid = project.currentId
  const node = movePicker.node
  if (!pid || !node) return
  try {
    const res = await updateArchiveCategory(pid, node.id, { parent_id: target.id }, true)
    renumberToast(
      `已把「${node.category_name}」移到「${target.path || '顶层'}」`,
      res.renumbered,
      res.archive_no_conflicts,
    )
    // 先用响应更新树上的这个节点（新的位置），再刷新原来和现在的上级、定位到它。
    await tree.afterMove(res, node.path_code, node.parent_id)
    await tree.reveal(node.id).catch(() => null)
    void loadList()
    detailRefresh.value++
  } catch (e) {
    if (!handledGlobally(e)) toast.error(categoryOpErrorText(e))
    if (isApiError(e, 'not_found')) void tree.refreshShown()
  }
}

// 删除节点
const confirmDelete = reactive({ open: false, loading: false, node: null as ArchiveCategory | null })
function askDelete(node: ArchiveCategory | null) {
  if (!node) return
  if (node.item_count > 0) {
    // 有条目的节点 server 会拒绝（409 category_not_empty），不用再确认一次。
    toast.error(`「${node.category_name}」（含下级节点）下还有 ${node.item_count} 件，不能删除`, {
      description: '请先把件移到别的节点，或撤销归档',
    })
    return
  }
  confirmDelete.node = node
  confirmDelete.open = true
}
async function doDelete() {
  const pid = project.currentId
  const node = confirmDelete.node
  if (!pid || !node || confirmDelete.loading) return
  confirmDelete.loading = true
  try {
    await deleteArchiveCategory(pid, node.id, true)
    confirmDelete.open = false
    toast.success(`已删除「${node.category_name}」${node.child_count ? '（含下级节点）' : ''}`)
    const cur = categoryQuery.value
    const inside = !!cur && (cur === node.id || tree.isUnder(cur, node.id) === true)
    await tree.refreshFor([node.parent_id ?? tree.rootId.value!])
    if (inside) await go(node.parent_id && node.parent_id !== tree.rootId.value ? node.parent_id : null, null, true)
  } catch (e) {
    if (!handledGlobally(e)) toast.error(categoryOpErrorText(e))
    if (e instanceof ApiError && e.status >= 400 && e.status < 500) {
      confirmDelete.open = false
      void tree.refreshShown()
    }
  } finally {
    confirmDelete.loading = false
  }
}

// 按新模板重新生成档号
const confirmRenumber = reactive({ open: false, loading: false, categoryId: null as string | null, label: '' })
function askRenumber(categoryId: string | null) {
  confirmRenumber.categoryId = categoryId
  confirmRenumber.label = categoryId
    ? `「${tree.nodes.get(categoryId)?.category_name ?? '这个节点'}」（含下级节点）`
    : '整个项目'
  confirmRenumber.open = true
}
async function doRenumber() {
  const pid = project.currentId
  if (!pid || confirmRenumber.loading) return
  confirmRenumber.loading = true
  try {
    const res = await renumberArchiveItems(pid, confirmRenumber.categoryId, true)
    confirmRenumber.open = false
    renumberToast(
      `已按新模板重新生成${confirmRenumber.label}的档号${res.skipped_locked ? `（${res.skipped_locked} 个已锁定的没有动）` : ''}`,
      res.renumbered,
      res.archive_no_conflicts,
    )
    if (!res.renumbered && !res.archive_no_conflicts) toast.info('档号都和现在的模板一致，没有变化')
    void loadList()
    detailRefresh.value++
  } catch (e) {
    if (!handledGlobally(e)) toast.error(errorMessage(e))
    if (e instanceof ApiError && e.status >= 400 && e.status < 500) confirmRenumber.open = false
  } finally {
    confirmRenumber.loading = false
  }
}

// ---------- 项目、节点（放在最后：immediate 的 watch 要用到上面声明的状态） ----------

watch(
  [() => project.current?.id ?? null, categoryQuery],
  async ([pid, cid], [, oldCid]) => {
    const p = project.current
    if (!pid || !p) {
      lastProjectId = null
      return
    }
    if (pid !== lastProjectId) {
      const switched = lastProjectId !== null
      lastProjectId = pid
      if (switched && detailRef.value?.isDirty()) {
        detailRef.value.discard()
        toast.warning('换了项目，没有保存的著录修改已放弃')
      }
      await resetForProject(p)
      if (switched && (cid || itemQuery.value || folderQuery.value)) {
        // 用户在切换器里换了项目：清掉地址里的节点、件、左栏的文件夹（地址变了会再进来一次）。
        await router.replace({ query: {} })
        return
      }
    } else if (cid !== oldCid) {
      // 换了节点：著录设置可能被改过（改设置不发事件），重取一次。
      void loadSettings()
    }
    await openCategory(cid)
  },
  { immediate: true },
)

// ---------- 实时事件 ----------

const pendingCategories = new Set<string>()
/** 带了非空 item_ids 的事件涉及的节点。 */
const pendingItemCategories = new Set<string>()
/** 没带 item_ids 的事件：category_ids 当作子树的根。 */
const pendingSubtrees = new Set<string>()
const pendingItems = new Set<string>()
const pendingFiles = new Set<string>()
let pendingFilesAll = false
let pendingFolders = false
let pendingCatalog = false
let pendingFull = false
const pendingFileFolders = new Set<string>()
let pendingFileFoldersUnknown = false
const pendingFolderIds = new Set<string>()
let pendingFolderIdsUnknown = false
/** item_ids 为空或没带的事件（只有节点变了）里的 category_ids。 */
const pendingStructCats = new Set<string>()
let applyTimer: ReturnType<typeof setTimeout> | null = null
let firstPendingAt = 0
/** 正在跑一轮（同时最多一轮；跑的过程中新来的事件并到下一轮）。 */
let applying = false
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
  if (e.type === 'resync') pendingFull = true
  else if (e.type === 'catalog.changed') {
    pendingCatalog = true
    const cats = e.category_ids ?? []
    cats.forEach((id) => pendingCategories.add(id))
    if (e.item_ids === undefined) cats.forEach((id) => pendingSubtrees.add(id))
    else if (e.item_ids.length) {
      e.item_ids.forEach((id) => pendingItems.add(id))
      cats.forEach((id) => pendingItemCategories.add(id))
    }
    if (!e.item_ids?.length) cats.forEach((id) => pendingStructCats.add(id))
  } else if (e.type === 'files.changed') {
    if (e.file_ids?.length) e.file_ids.forEach((id) => pendingFiles.add(id))
    else pendingFilesAll = true
    if (e.folder_ids?.length) e.folder_ids.forEach((id) => pendingFileFolders.add(id))
    else pendingFileFoldersUnknown = true
  } else if (e.type === 'folders.changed') {
    pendingFolders = true
    if (e.folder_ids?.length) e.folder_ids.forEach((id) => pendingFolderIds.add(id))
    else pendingFolderIdsUnknown = true
  } else return
  scheduleApply()
}

function hasPending(): boolean {
  return pendingFull || pendingCatalog || pendingFilesAll || pendingFolders || pendingFiles.size > 0 || pendingFileFolders.size > 0
}

/** 定时器到了：跑一轮；上一轮还没跑完就等它跑完再说（事件都还在 pending 里）。 */
async function applyEvents() {
  applyTimer = null
  firstPendingAt = 0
  if (applying) return
  applying = true
  try {
    await applyOnce()
  } catch {
    // 单个请求的失败各自处理过了。
  } finally {
    applying = false
    if (alive && hasPending()) scheduleApply()
  }
}

async function applyOnce() {
  if (pendingFull) {
    pendingFull = false
    clearPending()
    await refreshAll()
    return
  }
  const cats = new Set(pendingCategories)
  const itemCats = new Set(pendingItemCategories)
  const subtrees = new Set(pendingSubtrees)
  const itemIds = new Set(pendingItems)
  const fileIds = new Set(pendingFiles)
  const filesAll = pendingFilesAll
  const folders = pendingFolders
  const catalog = pendingCatalog
  const fileFolders = pendingFileFoldersUnknown ? null : new Set(pendingFileFolders)
  const fileEvents = filesAll || fileIds.size > 0 || pendingFileFolders.size > 0
  const folderIds = pendingFolderIdsUnknown ? null : new Set(pendingFolderIds)
  const structCats = new Set(pendingStructCats)
  clearPending()

  const tasks: Promise<unknown>[] = []
  // 路径过期：节点改名、移动、改编码而档号没变时 item_ids 是 []，打开的件的 category_path 却变了（M3-5 遗留）。
  const own = detailItem.value?.category_id ?? null
  const pathBefore = own ? tree.pathText(own) : null
  let detailQueued = false
  if (own && itemQuery.value && (tree.ancestorsOf(own).some((a) => cats.has(a)) || structCats.has(own))) {
    detailQueued = true
    if (detailRef.value) tasks.push(detailRef.value.refresh())
  }
  // "修改中 n 件"：解除封存、重新归档、撤销归档都发 folders.changed 和 catalog.changed。
  if (catalog || folders) void loadModifying()
  if (catalog) {
    tasks.push(
      tree.refreshFor(cats).then(() => {
        if (!own || detailQueued || detailItem.value?.category_id !== own) return
        if (tree.pathText(own) !== pathBefore) return detailRef.value?.refresh()
      }),
    )
  }
  if (fileIds.size) images.invalidate(fileIds)

  // 左栏（M9）。
  const pane = paneRef.value
  if (pane) {
    if (folders) pane.onFoldersChanged(folderIds)
    if (fileEvents) pane.onFilesChanged(fileFolders)
  }

  const cur = categoryQuery.value
  const selected = itemQuery.value
  let listAffected = false
  let detailAffected = false
  if (cur) {
    if (itemCats.has(cur)) listAffected = true
    if (items.value.some((i) => itemIds.has(i.id))) listAffected = true
    for (const root of subtrees) {
      if (root === cur || tree.isUnder(cur, root) === true) {
        listAffected = true
        detailAffected = true
      }
    }
  }
  if (selected && itemIds.has(selected)) detailAffected = true
  const detailFiles = new Set(detailItem.value?.pages.map((p) => p.file_id) ?? [])
  if (selected && detailItem.value) {
    if (filesAll || [...fileIds].some((f) => detailFiles.has(f))) detailAffected = true
    // 来源文件夹改名、移动：路径显示变了。
    if (folders && (!folderIds || folderIds.has(detailItem.value.source_folder_id))) detailAffected = true
  }
  if (cur && folders && (!folderIds || items.value.some((i) => folderIds.has(i.source_folder_id)))) listAffected = true
  if (cur && filesAll && items.value.some((i) => i.page_count > 0)) listAffected = true
  if (detailAffected && detailRef.value && !detailQueued) {
    detailQueued = true
    tasks.push(detailRef.value.refresh())
  }
  if (listAffected) tasks.push(loadList())
  else if (cur && fileIds.size && items.value.some((i) => i.page_count > 0)) tasks.push(refreshListIfFilesShown(fileIds))
  await Promise.all(tasks)
}

/** 文件的审核状态变了：查这些文件在哪些件，列表里有就重新取（"n 待审"）。 */
async function refreshListIfFilesShown(fileIds: Set<string>) {
  const pid = project.currentId
  if (!pid) return
  const ids = [...fileIds]
  const shown = new Set(items.value.map((i) => i.id))
  try {
    for (let i = 0; i < ids.length; i += 500) {
      const res = await getCatalogPlacements(pid, ids.slice(i, i + 500), true)
      if (res.items.some((x) => x.placement && shown.has(x.placement.item_id))) {
        await loadList()
        return
      }
    }
  } catch {
    // 查不到就算了：列表的"n 待审"等下一次刷新。
  }
}

function clearPending() {
  pendingCategories.clear()
  pendingItemCategories.clear()
  pendingSubtrees.clear()
  pendingItems.clear()
  pendingFiles.clear()
  pendingFilesAll = false
  pendingFolders = false
  pendingCatalog = false
  pendingFileFolders.clear()
  pendingFileFoldersUnknown = false
  pendingFolderIds.clear()
  pendingFolderIdsUnknown = false
  pendingStructCats.clear()
}

/** 整体刷新：resync、断线重连之后（断开期间的事件不会补发）。树只刷新看得到的部分，收起的再展开时重新取。 */
async function refreshAll() {
  void loadSettings()
  void loadModifying()
  images.invalidate()
  paneRef.value?.refresh()
  if (treeState.value !== 'ready') {
    const p = project.current
    if (p) {
      await resetForProject(p)
      await openCategory(categoryQuery.value)
    }
    return
  }
  await Promise.all([
    tree.refreshShown(),
    categoryQuery.value ? loadList() : null,
    detailRef.value ? detailRef.value.refresh() : null,
  ])
}

const stream = useProjectStream(() => project.currentId, {
  onEvent,
  onOpen: (first) => {
    if (!first) {
      pendingFull = true
      scheduleApply()
    } else if (listLoadedAt > 0) {
      // 第一次连上时页面已经加载好了：补刷一次（加载和连接建立之间可能漏了事件）。
      void tree.refreshShown()
      void loadList()
      detailRefresh.value++
      paneRef.value?.refresh()
    }
  },
  onStopped: (err) => {
    if (err.status === 403 || err.status === 404) void project.refresh()
  },
})

void project.ensureLoaded()

onBeforeUnmount(() => {
  removeSwitchGuard()
  alive = false
  openSeq++
  listSeq++
  projectSeq++
  modifyingSeq++
  if (applyTimer) clearTimeout(applyTimer)
  applyTimer = null
  discardAsk.resolve?.(false)
})

const templateError = computed(() => settings.value?.archive_no_template_error ?? null)
// 顶栏的档号规则：模板的中文写法，悬停看示例和原始模板。解析不了时原样显示（下面另有错误提示）。
const templateLabel = computed(() =>
  parsedTemplate.value?.ok ? describeArchiveNoTemplate(parsedTemplate.value.template) : settings.value?.archive_no_template ?? '',
)
const templateHint = computed(() => {
  const st = settings.value
  if (!st) return ''
  const lines: string[] = []
  const p = parsedTemplate.value
  if (p?.ok) {
    lines.push(`档号规则：${describeArchiveNoTemplate(p.template)}`)
    lines.push(`例如：${renderArchiveNo(p.template, SAMPLE_CODES, 1).value}`)
    if (p.template.itemNoWidth) lines.push(`件号 ${p.template.itemNoWidth} 位，不足补 0`)
  }
  lines.push(`项目设置里的档号模板：${st.archive_no_template}`)
  if (st.lock_requires_approved) lines.push('锁定要求全部页审核通过')
  return lines.join('\n')
})
</script>

<template>
  <div
    class="flex min-h-0 flex-1 flex-col"
    data-testid="catalog-page"
    :data-stream="stream.state.value"
    :data-tree="treeState"
    :data-list="listState"
    :data-category-id="categoryQuery ?? undefined"
    :data-dirty="detailDirty ? 'true' : undefined"
    :data-modifying="modifyingTotal"
  >
    <header class="flex h-12 shrink-0 items-center gap-3 border-b px-4">
      <h1 class="shrink-0 text-base font-semibold tracking-tight text-foreground">归类</h1>
      <span
        v-if="settings"
        class="hidden min-w-0 truncate border-l pl-3 text-xs text-muted-foreground xl:inline"
        :title="templateHint"
        data-testid="catalog-template"
      >档号：{{ templateLabel }}</span>
      <button
        v-if="modifyingTotal"
        type="button"
        class="flex shrink-0 items-center gap-1 rounded-full border border-orange-300 bg-orange-50 px-2.5 py-0.5 text-xs text-orange-700 hover:bg-orange-100"
        title="点开看是哪几件"
        data-testid="modifying-chip"
        :data-count="modifyingTotal"
        @click="modifyingOpen = true"
      >
        <PencilLineIcon class="size-3.5" />修改中 {{ modifyingTotal }} 件，改完记得重新归档 ›
      </button>
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
        v-if="project.current && treeState === 'ready'"
        variant="outline"
        size="sm"
        data-testid="renumber-project"
        @click="askRenumber(null)"
      >
        <RefreshCwIcon />按新模板重新生成档号…
      </Button>
    </header>

    <div v-if="!project.current" class="flex flex-1 items-center justify-center p-6 text-sm text-muted-foreground">
      <span v-if="project.state === 'loading' || project.state === 'idle'" class="flex items-center gap-2">
        <LoaderCircleIcon class="size-4 animate-spin" />正在加载项目…
      </span>
      <div v-else class="max-w-sm text-center" data-testid="catalog-no-project">
        <div class="text-base font-medium text-foreground">没有可进入的项目</div>
        <p class="mt-1">请联系管理员在「系统管理 › 项目」里给你授权，或在左上角选择一个项目。</p>
      </div>
    </div>

    <div v-else class="flex min-h-0 flex-1">
      <!-- 左：文件目录 -->
      <FolderPane
        v-if="canBrowse"
        ref="paneRef"
        :project-id="project.current.id"
        :root-folder-id="project.current.root_folder_id"
        :project-name="project.current.project_name"
        :folder-id="folderQuery"
        :item-no-width="itemNoWidth"
        :can-files="canFiles"
        @update:folder-id="setPaneFolder"
        @selection="(list) => (leftSelected = list)"
        @unseal="(a) => askUnseal(a.item_id, folderArchiveLabel(a))"
        @reseal="(a) => askReseal(a.item_id, folderArchiveLabel(a))"
        @open-files="openFiles"
        @open-viewer="openViewer"
      />
      <div
        v-else
        class="flex min-w-0 flex-1 items-center justify-center px-8 text-center text-xs text-muted-foreground"
        data-testid="catalog-pane-forbidden"
      >
        归档要从文件目录里选文件夹（文件目录或扫描上传菜单）；你的账号没有这个菜单，请联系管理员。右边的归档目录照常可以用。
      </div>

      <!-- 中间：归档 → -->
      <div class="flex w-24 shrink-0 flex-col items-center justify-center gap-2.5 border-x bg-muted/30 px-2" data-testid="catalog-mid">
        <Button
          size="sm"
          class="w-full"
          :disabled="!leftSelected.length || !archiveTargetNode"
          data-testid="archive-button"
          @click="archiveSelected"
        >
          归档<ArrowRightIcon />
        </Button>
        <p class="text-center text-[11px] leading-snug text-muted-foreground" data-testid="archive-tip">{{ midTip }}</p>
        <p v-if="leftSelected.length" class="text-center text-[11px] text-info">已选 {{ leftSelected.length }} 个</p>
      </div>

      <!-- 右：归档目录 -->
      <section class="flex min-w-0 flex-[1.15] flex-col">
        <div class="flex h-10 shrink-0 items-center gap-2 border-b bg-muted/30 px-3">
          <span class="shrink-0 text-[13px] font-semibold text-foreground">归档目录</span>
          <span class="min-w-0 truncate text-xs text-muted-foreground" data-testid="catalog-crumb">
            <template v-if="atRoot">直接放在顶层的件</template>
            <template v-else-if="category">
              <button v-if="itemQuery" type="button" class="text-info hover:underline" @click="go(categoryQuery)">{{ tree.pathText(category.id) }}</button>
              <b v-else class="font-medium text-foreground">{{ tree.pathText(category.id) }}</b>
              <template v-if="itemQuery && detailItem"> / <b class="font-mono font-medium text-foreground">{{ formatItemNo(detailItem.item_no, itemNoWidth) }}</b></template>
            </template>
          </span>
        </div>
        <div
          v-if="templateError"
          class="shrink-0 border-b bg-destructive-soft px-4 py-2 text-xs text-destructive"
          data-testid="template-error"
        >
          项目的档号模板解析不了：{{ templateError }}。归档、移动、改件号都会失败，请到「系统管理 › 项目」里修改。
        </div>
        <div class="flex min-h-0 flex-1">
          <!-- 归类树 -->
          <aside class="flex w-48 shrink-0 flex-col border-r bg-sidebar/60 xl:w-56">
            <div class="flex h-8 shrink-0 items-center gap-0.5 px-2">
              <span class="text-xs font-medium text-muted-foreground">归类树</span>
              <span class="flex-1" />
              <Tooltip>
                <TooltipTrigger as-child>
                  <Button variant="ghost" size="icon-xs" aria-label="新建顶层节点" data-testid="category-new-top" @click="askCreate(null)">
                    <FolderPlusIcon />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>新建顶层节点（例如全宗）</TooltipContent>
              </Tooltip>
              <Tooltip>
                <TooltipTrigger as-child>
                  <Button variant="ghost" size="icon-xs" aria-label="新建下级节点" :disabled="!opNode" data-testid="category-new-child" @click="askCreate(opNode)">
                    <PlusIcon />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>在选中的节点下新建</TooltipContent>
              </Tooltip>
              <Tooltip>
                <TooltipTrigger as-child>
                  <Button variant="ghost" size="icon-xs" aria-label="修改节点" :disabled="!opNode" data-testid="category-edit" @click="askEdit(opNode)">
                    <PencilIcon />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>改名称、编码、类型</TooltipContent>
              </Tooltip>
              <Tooltip>
                <TooltipTrigger as-child>
                  <Button variant="ghost" size="icon-xs" aria-label="移动节点" :disabled="!opNode" data-testid="category-move" @click="askMove(opNode)">
                    <ArrowRightLeftIcon />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>移动到…</TooltipContent>
              </Tooltip>
              <Tooltip>
                <TooltipTrigger as-child>
                  <Button variant="ghost" size="icon-xs" aria-label="删除节点" :disabled="!opNode" data-testid="category-delete" @click="askDelete(opNode)">
                    <Trash2Icon />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>删除（含下级节点；有件时不能删）</TooltipContent>
              </Tooltip>
              <Tooltip v-if="canExport">
                <TooltipTrigger as-child>
                  <Button variant="ghost" size="icon-xs" aria-label="导出节点" :disabled="!opNode" data-testid="category-export" @click="opNode && goExport('category', opNode.id)">
                    <FileOutputIcon />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>导出这个节点（含下级）…</TooltipContent>
              </Tooltip>
            </div>
            <ContextMenu :modal="false">
              <ContextMenuTrigger as-child>
                <div
                  class="min-h-0 flex-1 overflow-y-auto px-1.5 pb-2"
                  data-testid="category-tree-area"
                  @contextmenu.capture="onTreeContextMenu"
                  @dragover="onTreeDragOver"
                  @dragleave.self="dropTarget = null"
                  @drop="onTreeDrop"
                >
                  <div v-if="treeState === 'loading'" class="flex items-center gap-2 p-2 text-xs text-muted-foreground">
                    <LoaderCircleIcon class="size-3.5 animate-spin" />正在加载归类树…
                  </div>
                  <div v-else-if="treeState === 'error'" class="p-2 text-xs text-destructive">
                    归类树加载失败：{{ treeError }}
                    <button type="button" class="ml-1 underline" @click="project.current && resetForProject(project.current)">重试</button>
                  </div>
                  <template v-else>
                    <button
                      v-if="tree.rootDirectCount.value > 0"
                      type="button"
                      class="mb-px flex w-full items-center gap-1.5 rounded-md py-1 pr-2 pl-[26px] text-left text-[13px] hover:bg-muted"
                      :class="atRoot ? 'bg-info-soft font-medium text-info hover:bg-info-soft' : 'text-sidebar-foreground'"
                      title="直接放在顶层（没有归到任何节点下）的件，建议移到合适的节点"
                      data-testid="tree-root-items"
                      :data-items="tree.rootDirectCount.value"
                      @click="tree.rootId.value && selectCategory(tree.rootId.value)"
                    >
                      <span class="min-w-0 flex-1 truncate">（直接放在顶层的件）</span>
                      <span class="shrink-0 font-mono text-[11px] text-warn tabular-nums">{{ tree.rootDirectCount.value.toLocaleString() }} 件</span>
                    </button>
                    <CategoryTree :tree="tree" :selected-id="categoryQuery" :drop-target-id="dropTarget" @select="selectCategory" />
                    <div v-if="!tree.topLevel.value.length" class="p-3 text-xs text-muted-foreground" data-testid="category-tree-empty">
                      还没有归类节点。点上面的 <FolderPlusIcon class="inline size-3.5" /> 新建全宗，再逐级建门类、年度、保管期限、机构。
                    </div>
                  </template>
                </div>
              </ContextMenuTrigger>
              <ContextMenuContent class="w-56" data-testid="category-menu" :data-node="menuNode?.id ?? ''">
                <template v-if="menuNode">
                  <ContextMenuItem data-testid="menu-category-new-child" @select="askCreate(menuNode)">
                    <PlusIcon />新建下级节点
                  </ContextMenuItem>
                  <ContextMenuItem data-testid="menu-category-rename" @select="askEdit(menuNode, 'name')">
                    <PencilIcon />改名称、编码、类型
                  </ContextMenuItem>
                  <ContextMenuItem data-testid="menu-category-move" @select="askMove(menuNode)">
                    <ArrowRightLeftIcon />移动到…
                  </ContextMenuItem>
                  <ContextMenuItem data-testid="menu-category-renumber" @select="askRenumber(menuNode.id)">
                    <RefreshCwIcon />按新模板重新生成档号
                  </ContextMenuItem>
                  <ContextMenuItem v-if="canExport" data-testid="menu-category-export" @select="goExport('category', menuNode.id)">
                    <FileOutputIcon />导出…
                  </ContextMenuItem>
                  <ContextMenuSeparator />
                  <ContextMenuItem variant="destructive" data-testid="menu-category-delete" @select="askDelete(menuNode)">
                    <Trash2Icon />删除
                  </ContextMenuItem>
                </template>
                <ContextMenuItem v-else data-testid="menu-category-new-top" @select="askCreate(null)">
                  <FolderPlusIcon />新建顶层节点
                </ContextMenuItem>
              </ContextMenuContent>
            </ContextMenu>
          </aside>

          <!-- 件详情 / 件列表 -->
          <div class="relative flex min-w-0 flex-1 flex-col">
            <ItemDetail
              v-if="itemQuery && project.currentId"
              :key="itemQuery"
              ref="detailRef"
              :item-id="itemQuery"
              :project-id="project.currentId"
              :settings="settings"
              :can-export="canExport"
              :item-no-width="itemNoWidth"
              :refresh-token="detailRefresh"
              :category-label="detailCategoryLabel"
              @loaded="onDetailLoaded"
              @changed="onDetailChanged"
              @moved="onDetailMoved"
              @gone="onDetailGone"
              @open-item="(c, i) => go(c, i)"
              @renumber="askRenumber"
              @settings-stale="loadSettings"
              @dirty="(d) => (detailDirty = d)"
              @deleted="onDetailDeleted"
              @open-viewer="openViewer"
              @export="(id) => goExport('item', id)"
              @back="go(categoryQuery)"
              @unseal="(it, label) => askUnseal(it.id, label)"
              @reseal="(it, label) => askReseal(it.id, label)"
              @open-folder="setPaneFolder"
            />
            <div
              v-else-if="category"
              class="flex min-h-0 flex-1 flex-col"
              :class="dropOnList && 'bg-info-soft/40 outline-2 -outline-offset-4 outline-info outline-dashed'"
              data-testid="item-list-area"
              @dragover="onListDragOver"
              @dragleave.self="dropOnList = false"
              @drop="onListDrop"
            >
              <div class="flex shrink-0 flex-wrap items-center gap-1.5 border-b px-3 py-1.5 text-xs">
                <span class="flex shrink-0 items-center pr-1" title="全选已加载的件" @click="toggleAll">
                  <Checkbox
                    :model-value="allChecked"
                    aria-label="全选已加载的件"
                    class="pointer-events-none"
                    :disabled="!items.length"
                    data-testid="item-check-all"
                  />
                </span>
                <span class="shrink-0 text-muted-foreground" data-testid="item-total" :data-count="listTotal">{{ listTotal.toLocaleString() }} 件</span>
                <span class="shrink-0 rounded-full bg-muted px-1.5 leading-5 text-muted-foreground tabular-nums" data-testid="item-count-draft" :data-count="listCounts.draft">未锁定 {{ listCounts.draft }}</span>
                <span class="shrink-0 rounded-full bg-violet-100 px-1.5 leading-5 text-violet-700 tabular-nums" data-testid="item-count-locked" :data-count="listCounts.locked">已锁定 {{ listCounts.locked }}</span>
                <span v-if="listCounts.modifying" class="shrink-0 rounded-full border border-orange-300 bg-orange-50 px-1.5 leading-4 text-orange-700 tabular-nums" data-testid="item-count-modifying" :data-count="listCounts.modifying">修改中 {{ listCounts.modifying }}</span>
                <span class="flex-1" />
                <Button
                  size="xs"
                  variant="outline"
                  :disabled="!selection.count.value || selection.count.value > MAX_LOCK_ITEMS"
                  :title="selection.count.value > MAX_LOCK_ITEMS ? `一次最多锁定 ${MAX_LOCK_ITEMS} 件` : '把勾选的件一起锁定'"
                  data-testid="batch-lock"
                  @click="askBatchLock"
                >
                  <LockIcon />锁定{{ selection.count.value ? `（${selection.count.value}）` : '' }}
                </Button>
                <Button v-if="canExport && opNode" size="xs" variant="ghost" title="导出这个节点（含下级）…" data-testid="category-export-main" @click="goExport('category', opNode.id)">
                  <FileOutputIcon /><span class="hidden 2xl:inline">导出…</span>
                </Button>
              </div>
              <div v-if="listState === 'loading' && !items.length" class="flex flex-1 items-center justify-center gap-2 text-xs text-muted-foreground">
                <LoaderCircleIcon class="size-3.5 animate-spin" />正在加载…
              </div>
              <div v-else-if="listState === 'error' && !items.length" class="flex flex-1 flex-col items-center justify-center gap-2 text-xs">
                <p class="text-destructive">{{ listError }}</p>
                <Button variant="outline" size="xs" @click="loadList">重试</Button>
              </div>
              <div
                v-else-if="listState === 'ready' && !items.length"
                class="flex flex-1 flex-col items-center justify-center gap-1 px-6 text-center text-xs text-muted-foreground"
                data-testid="item-list-empty"
              >
                <template v-if="atRoot">顶层没有直接放的件</template>
                <template v-else-if="category.child_count && category.item_count">
                  <span>「{{ category.category_name }}」下面是节点，含下级共 {{ category.item_count }} 件，在各自的节点里看</span>
                  <span>也可以把左边的文件夹拖到这里，直接归到这个节点</span>
                </template>
                <template v-else>
                  <span>「{{ category.category_name }}」里还没有件</span>
                  <span>把左边的文件夹拖到这里（或者树上的节点），或者选好后点中间的"归档 →"</span>
                </template>
              </div>
              <ItemList
                v-else
                ref="itemListRef"
                :items="items"
                :current-id="itemQuery"
                :selection="selection"
                :item-no-width="itemNoWidth"
                :has-more="!!nextCursor"
                :loading-more="loadingMore"
                :load-more-error="loadMoreError"
                :blocked-ids="blockedIds"
                @activate="openItem"
                @load-more="loadMore"
                @retry-more="loadMore"
              />
              <p v-if="items.length" class="shrink-0 border-t px-3 py-1 text-[11px] text-muted-foreground">双击打开一件 · 勾选后可以批量锁定</p>
            </div>
            <div v-else class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center text-sm text-muted-foreground" data-testid="catalog-empty">
              <p>在归类树里选一个节点，看它里面的件。</p>
              <p class="text-xs">归档：把左边的文件夹拖到树上的节点。</p>
            </div>
          </div>
        </div>
      </section>
    </div>

    <!-- 对话框 -->
    <ArchiveDialog
      v-model:open="archive.open"
      :category-id="archive.categoryId"
      :category-label="archive.label"
      :folder-ids="archive.folderIds"
      :item-no-width="itemNoWidth"
      @archived="onArchived"
      @stale="paneRef?.refresh()"
    />
    <UnsealDialog v-model:open="unseal.open" :item-id="unseal.itemId" :label="unseal.label" @unsealed="onUnsealed" @changed="afterSealChange" />
    <ResealDialog v-model:open="reseal.open" :item-id="reseal.itemId" :label="reseal.label" @resealed="onResealed" />
    <ModifyingDialog
      v-model:open="modifyingOpen"
      :items="modifyingItems"
      :total="modifyingTotal"
      :item-no-width="itemNoWidth"
      :can-files="canFiles"
      @open-files="openFiles"
      @open-item="onModifyingOpenItem"
      @reseal="onModifyingReseal"
    />
    <CategoryFormDialog
      v-if="project.currentId"
      v-model:open="formOpen"
      :project-id="project.currentId"
      :settings="settings"
      :parent="formParent"
      :node="formNode"
      :default-type="formDefaultType"
      :focus="formFocus"
      @created="onCategoryCreated"
      @updated="onCategoryUpdated"
    />
    <CategoryPicker
      v-if="project.currentId"
      v-model:open="movePicker.open"
      :project-id="project.currentId"
      :initial-id="movePicker.node?.parent_id ?? null"
      :exclude="movePicker.node ? [{ path_code: movePicker.node.path_code }] : []"
      :disabled-ids="[movePicker.node?.parent_id ?? null]"
      allow-top
      :title="`移动「${movePicker.node?.category_name ?? ''}」到…`"
      description="连同下级节点和里面的件一起移动；未锁定的件的档号按新位置重新生成。子树里有已锁定的件时不能移动。"
      confirm-text="移到这里"
      @confirm="doMove"
    />
    <ConfirmDialog
      v-model:open="confirmDelete.open"
      :title="`删除「${confirmDelete.node?.category_name ?? ''}」？`"
      description="连同下级节点一起删除（软删除，同一层的编码以后可以再用）。"
      confirm-text="删除"
      destructive
      :loading="confirmDelete.loading"
      @confirm="doDelete"
    />
    <ConfirmDialog
      v-model:open="confirmBatch.open"
      :title="`锁定选中的 ${confirmBatch.ids.length} 件？`"
      description="一起锁定：任何一件不满足锁定条件（不在修改中、必填著录项已填、至少 1 页、档号不为空、项目要求时全部页审核通过）就一件都不锁定，并标出不满足的。已锁定的会跳过。"
      confirm-text="锁定"
      :loading="confirmBatch.loading"
      @confirm="doBatchLock"
    />
    <ConfirmDialog
      v-model:open="confirmRenumber.open"
      :title="`按新模板重新生成${confirmRenumber.label}的档号？`"
      description="按项目现在的档号模板，重新生成范围内所有未锁定的件的档号（已锁定的不动）。生成出来的档号和别的件重复时，那一件的档号为空。"
      confirm-text="重新生成"
      :loading="confirmRenumber.loading"
      @confirm="doRenumber"
    />
    <ConfirmDialog
      v-model:open="discardAsk.open"
      title="著录还没有保存"
      description="离开这一件会放弃没有保存的修改。"
      confirm-text="放弃修改"
      destructive
      @confirm="resolveDiscard(true)"
    />
    <OriginalViewerDialog
      v-if="viewer.mounted"
      v-model:open="viewer.open"
      :files="viewer.files"
      :file-id="viewer.fileId"
      :can-read-logs="session.menuCodes.has('files')"
    />
  </div>
</template>
