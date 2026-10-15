<script setup lang="ts">
// 归类页 /catalog（M3-5，参考 plan.html 的 p-catalog Mockup；菜单码 catalog）：归类树和条目著录；M3-6 加上挂接工作台
// （右边第三栏的待归类池 CatalogPool、详情里可编辑的页序条 PageStrip）。著录设置在「系统管理 › 项目」里编辑（M3-7）。
//
// M4-5：节点（工具栏、右键菜单、节点的空白页）和条目（详情页头）上的"导出"入口，带着范围跳到 /export 的新建导出
// （`?new=category|item&ids=<id>`，lib/export.ts）；只有 export 菜单的人看得到。
//
// M3-6：
// - **待归类池**（第三栏，要 files 或 scan 菜单才能浏览目录）：文件夹记在地址栏 `?pool=`（没有时用这个项目上次用的，
//   localStorage，只是个人习惯）；"挂到末尾"、"插到选中页之后"交给条目详情（ItemDetail.linkFiles，经 PUT 提交整份
//   列表）；"用选中的文件新建条目"在这里：POST 带 file_ids（M3-3），已在别的条目里时同样给"从原条目移出"。
// - **看原图**：页序条双击、池里双击，都用 OriginalViewerDialog（左右切换在同一组文件之间）。
// - **删除条目**在详情里（确认框写明页数），删掉后回到节点、刷新列表和树。
// - **路径过期**（M3-5 遗留，design §7）：节点改名、移动、改编码而档号没变时事件是 `catalog.changed {category_ids,
//   item_ids: []}`，条目的路径显示（详情的 category_path）却变了：刷新树之后，打开着的条目所在节点的路径（树上的名称链）
//   变了就重取详情；待归类池的"在哪个条目"按同一条事件重取。
//
// - **左栏上半：归类树**（CategoryTree，按需加载，根节点不显示）：每个节点显示编码和子树里的条目数；工具栏和右键菜单
//   提供新建（顶层、下级）、修改（名称、编码、类型）、移动、删除、"按新模板重新生成档号"。改编码、改类型、移动的
//   响应里 `archive_no_conflicts > 0` 时提示"有 n 个条目因为撞号档号为空"。server 允许条目直接放在根节点下：
//   有这样的条目时树顶多一行"（直接放在顶层的条目）n 件"，能打开（根节点当作当前节点）、看详情、移走；根节点本身
//   不能改、不能在那里新建条目。
// - **左栏下半：条目列表**（ItemList，虚拟滚动，keyset 分页每页 500）：当前节点直接放的条目；新建条目、勾选后批量
//   定稿（≤500 个；被拒时标出不满足条件的条目）。
// - **右边：条目详情**（ItemDetail）：档号（只读，为空时显示原因）、著录表单（按 catalog-settings 动态生成）、
//   页序（只读）、标记完成 / 退回草稿 / 定稿 / 解锁（只有同时有 catalog 和 review_assign 的人看得到）、移到别的节点、
//   改件号。表单有未保存的修改时，换条目、换节点、离开页面、在切换器里换项目（project store 的 addSwitchGuard）
//   都先确认。
// - **地址栏**：`?category=<节点>&item=<条目>`；链接里的节点不在当前项目里（404）时回到没有选中的状态。
// - **著录设置**改了不发事件：进入页面、切换节点、写操作失败（4xx）时重取 catalog-settings。
// - **实时**（useProjectStream，去抖：最后一条事件后 300 ms、最多等 1 秒，和 /files 一样；同时最多跑一轮，跑的过程中
//   新来的事件并到下一轮）：
//   - `catalog.changed {category_ids?, item_ids?}`：树按节点刷新它自己和祖先（条目数沿祖先链变），只为看得到的展开节点
//     发请求，收起的丢掉子节点、再展开时重新取；树上不认识的节点位置不明，刷新全部看得到的展开节点。item_ids 的
//     语义见下面"实时事件"一节：非空时当前节点在 category_ids 里或列表里有这些条目就重新取列表，选中的条目在里面
//     就重新取详情；空数组只有节点变了，不重取列表、详情；没带时（超过 500 个；M3-3 合入前节点的事件也不带）把
//     category_ids 当作子树的根，当前节点在这些子树里才重新取列表和详情（当前节点的祖先链一定在树上，不认识的节点
//     不会是它的祖先）；
//   - `files.changed`：页的审核状态、版本会变。带 file_ids 时，选中条目的页在里面就重新取详情；再用
//     catalog-placements 查这些文件在哪些条目，列表里有就重新取列表（"n 待审"）；没带 file_ids 时都重新取。
//     封面缓存按 file_id 作废（重扫出了新版本）；
//   - `folders.changed`：页所在的文件夹路径可能变了，选中的条目有页就重新取详情；
//   - `resync`、断线重连：整体刷新（设置、树里看得到的部分、列表、详情）。
import {
  ArrowRightLeftIcon,
  FileOutputIcon,
  FolderPlusIcon,
  ListPlusIcon,
  LoaderCircleIcon,
  LockIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  Trash2Icon,
} from '@lucide/vue'
import { useEventListener } from '@vueuse/core'
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import {
  createArchiveItem,
  deleteArchiveCategory,
  getCatalogPlacements,
  getCatalogSettings,
  listArchiveItems,
  lockArchiveItems,
  MAX_ITEM_PAGES,
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
  CatalogedElsewhere,
  CatalogSettings,
  FileCatalogedDetails,
  FileEntry,
  ItemStatus,
  MyProject,
  ProjectEvent,
  UpdatedArchiveCategory,
} from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import CategoryPicker, { type PickedCategory } from '@/components/catalog/CategoryPicker.vue'
import CategoryTree from '@/components/catalog/CategoryTree.vue'
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
import { CATEGORY_TYPES, categoryTypeLabel, ITEM_STATUS, ITEM_STATUS_ORDER, parseArchiveNoTemplate } from '@/lib/catalog'
import { newExportQuery } from '@/lib/export'
import { useImageInfoStore } from '@/stores/image-info'
import { useProjectStore } from '@/stores/project'
import { useSessionStore } from '@/stores/session'
import { handledGlobally } from '@/views/review-assign/review-errors'

import { categoryOpErrorText } from './catalog-errors'
import CatalogPool, { type PoolAction } from './CatalogPool.vue'
import CategoryFormDialog from './CategoryFormDialog.vue'
import FileCatalogedDialog from './FileCatalogedDialog.vue'
import ItemDetail, { type StripState } from './ItemDetail.vue'
import ItemList from './ItemList.vue'
import { filesText } from './page-ops'

const OriginalViewerDialog = defineAsyncComponent(() => import('@/views/files/OriginalViewerDialog.vue'))

const project = useProjectStore()
const session = useSessionStore()
const images = useImageInfoStore()
const route = useRoute()
const router = useRouter()
const tree = useCategoryTree()

/** 解锁要同时有 catalog 和 review_assign 两个菜单（用户决定 Q5；内置只有负责人和 admin）。 */
const canUnlock = computed(() => session.menuCodes.has('catalog') && session.menuCodes.has('review_assign'))

const categoryQuery = computed(() => (typeof route.query.category === 'string' ? route.query.category : null))
const itemQuery = computed(() => (typeof route.query.item === 'string' ? route.query.item : null))
/** 待归类池的文件夹（M3-6）。 */
const poolQuery = computed(() => (typeof route.query.pool === 'string' ? route.query.pool : null))
/** 浏览文件目录（待归类池用 children 接口）要 files 或 scan 菜单（内置归类员有 files）。 */
const canBrowse = computed(() => session.menuCodes.has('files') || session.menuCodes.has('scan'))
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

const itemNoWidth = computed(() => {
  const t = settings.value ? parseArchiveNoTemplate(settings.value.archive_no_template) : null
  return t?.ok ? t.template.itemNoWidth : 0
})

// ---------- 归类树 ----------

const treeState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const treeError = ref('')
const category = computed(() => (categoryQuery.value ? (tree.nodes.get(categoryQuery.value) ?? null) : null))
/** 打开的是根节点（"直接放在顶层的条目"）。 */
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
  treeState.value = 'loading'
  void loadSettings()
  try {
    await tree.init(p.id)
    if (my !== projectSeq) return
    treeState.value = 'ready'
  } catch (e) {
    if (my !== projectSeq) return
    treeState.value = 'error'
    treeError.value = errorMessage(e)
  }
}

let openSeq = 0
/** 进入一个节点：在树上展开到它，加载它的条目。 */
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
  // 根节点也能打开：直接放在根节点下的条目（server 允许）要能看到、能移走。
  const node = await tree.reveal(id).catch(() => null)
  if (my !== openSeq) return
  if (!node) {
    toast.error('归类节点不存在或已被删除')
    await router.replace({ query: {} })
    return
  }
  await loadList()
}

function go(categoryId: string | null, itemId: string | null = null, replace = false) {
  if (!alive) return Promise.resolve()
  const query: Record<string, string> = {}
  if (categoryId) query.category = categoryId
  if (categoryId && itemId) query.item = itemId
  // 待归类池的文件夹（M3-6）跟着走。
  if (poolQuery.value) query.pool = poolQuery.value
  return replace ? router.replace({ query }) : router.push({ query })
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

// ---------- 条目列表 ----------

const PAGE = 500
const items = ref<ArchiveItemSummary[]>([])
const listTotal = ref(0)
const listCounts = ref<Record<ItemStatus, number>>({ draft: 0, done: 0, locked: 0 })
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
/** 批量定稿时不满足条件的条目（409 的 details.ids）。 */
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
    let counts = { draft: 0, done: 0, locked: 0 }
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

/** 用详情更新列表里的这一行（状态计数跟着改）；不在这个节点里了就去掉。 */
function patchRow(it: ArchiveItem) {
  const i = items.value.findIndex((x) => x.id === it.id)
  if (i < 0) return
  const old = items.value[i]!
  if (it.category_id !== listCategoryId) {
    items.value = items.value.filter((x) => x.id !== it.id)
    listTotal.value = Math.max(0, listTotal.value - 1)
    listCounts.value = { ...listCounts.value, [old.status]: Math.max(0, listCounts.value[old.status] - 1) }
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
  }
  const next = [...items.value]
  next[i] = row
  // 改了件号：按件号重新排。
  if (old.item_no !== it.item_no) next.sort((a, b) => a.item_no - b.item_no || (a.id < b.id ? -1 : 1))
  items.value = next
  if (old.status !== it.status) {
    listCounts.value = {
      ...listCounts.value,
      [old.status]: Math.max(0, listCounts.value[old.status] - 1),
      [it.status]: listCounts.value[it.status] + 1,
    }
  }
}

// ---------- 当前条目 ----------

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

/**
 * 详情里显示的路径：树上的名称链和 server 的 category_path 一致时用树上的（名称里可能有 /，比拆 category_path 准）；
 * 不一致（例如节点刚被移到收起着的节点下面，树上的父子关系还是旧的）时交给 ItemDetail 用 server 的 category_path。
 */
const detailCategoryLabel = computed(() => {
  const it = detailItem.value
  if (!it) return null
  if (it.category_id === tree.rootId.value) return '（直接放在顶层）'
  return treeCategoryPath(it.category_id) === it.category_path ? tree.pathText(it.category_id) : null
})

/** 树上的名称链和 server 的不一致：重新取这个节点（新的 parent_id）和它现在的上级，再展开到它。每个路径只修一次。 */
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
    description: it.archive_no ? `档号 ${it.archive_no}` : '档号为空（原因见条目详情）',
    action: { label: '查看', onClick: () => void go(to, it.id) },
  })
  void go(categoryQuery.value, null, true)
}

function onDetailGone(id: string) {
  items.value = items.value.filter((x) => x.id !== id)
  if (itemQuery.value === id) {
    toast.info('条目已不存在（可能刚被删除），已刷新')
    void go(categoryQuery.value, null, true)
  }
  void loadList()
}

/** 自己删掉了条目（M3-6）：从列表里去掉，回到节点，刷新树上的条目数。页回到"未归类"由 files.changed 刷新待归类池。 */
function onDetailDeleted(it: ArchiveItem) {
  const i = items.value.findIndex((x) => x.id === it.id)
  if (i >= 0) {
    const old = items.value[i]!
    items.value = items.value.filter((x) => x.id !== it.id)
    listTotal.value = Math.max(0, listTotal.value - 1)
    listCounts.value = { ...listCounts.value, [old.status]: Math.max(0, listCounts.value[old.status] - 1) }
  }
  toast.success(it.archive_no ? `已删除 ${it.archive_no}` : `已删除第 ${it.item_no} 件`, {
    description: it.page_count ? `${it.page_count} 页已移出，回到"未归类"` : undefined,
  })
  void tree.refreshFor([it.category_id])
  void go(categoryQuery.value, null, true)
  void loadList()
}

watch(itemQuery, (id) => {
  if (!id) {
    detailItem.value = null
    stripState.value = null
  } else void nextTick(() => itemListRef.value?.scrollToItem(id))
})

// ---------- 待归类池和挂接（M3-6） ----------

const poolRef = ref<InstanceType<typeof CatalogPool> | null>(null)
/** 页序条的状态（ItemDetail 告诉的）。 */
const stripState = ref<StripState | null>(null)
const linking = ref(false)

const POOL_KEY = 'doclite.catalog.pool.'
function recallPool(pid: string): string | null {
  try {
    return window.localStorage.getItem(POOL_KEY + pid)
  } catch {
    return null
  }
}
function rememberPool(pid: string, id: string | null) {
  try {
    if (id) window.localStorage.setItem(POOL_KEY + pid, id)
    else window.localStorage.removeItem(POOL_KEY + pid)
  } catch {
    // 记不住就算了。
  }
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/**
 * 这个项目上次用的池文件夹（localStorage）。用 ref：清掉之后 poolFolderId 能重新算（M3-6 审查第 1 轮：原来池的文件夹
 * 来自 localStorage 时被删了，地址不变，池一直停在已删的文件夹上）。
 */
const rememberedPool = ref<string | null>(null)
watch(
  () => project.currentId,
  (pid) => {
    const id = pid ? recallPool(pid) : null
    rememberedPool.value = id && UUID_RE.test(id) ? id : null
  },
  { immediate: true },
)

/** 地址栏的 ?pool= 不是 UUID（手改坏了的链接）：去掉，用记住的或者回到空闲。 */
watch(
  poolQuery,
  (q) => {
    if (q && !UUID_RE.test(q)) {
      toast.info('地址里的待归类池文件夹不对，已忽略')
      dropPoolQuery()
    }
  },
  { immediate: true },
)

/** 池里的文件夹：地址栏的，没有就是这个项目上次用的。 */
const poolFolderId = computed(() =>
  poolQuery.value && UUID_RE.test(poolQuery.value) ? poolQuery.value : rememberedPool.value,
)

function dropPoolQuery() {
  if (!alive || route.query.pool === undefined) return
  const query = { ...route.query }
  delete query.pool
  void router.replace({ query })
}

function setPoolFolder(id: string | null) {
  const pid = project.currentId
  if (pid) rememberPool(pid, id)
  rememberedPool.value = id
  const query = { ...route.query }
  if (id) query.pool = id
  else delete query.pool
  void router.replace({ query })
}

/**
 * 池的文件夹用不了（已删除、不在这个项目里、进不了）：地址栏里是它就去掉；记住的是它才清掉记住的（地址栏带着别的
 * 项目的文件夹时，不能误删这个项目记住的）。
 */
function onPoolInvalid(id: string) {
  const pid = project.currentId
  if (pid && rememberedPool.value === id) {
    rememberPool(pid, null)
    rememberedPool.value = null
  }
  if (poolQuery.value === id) dropPoolQuery()
}

const ITEM_REASON = '先在左边打开一个条目'
const linkAction = computed<PoolAction>(() => {
  const s = stripState.value
  if (!s) return { label: '挂到条目末尾', reason: ITEM_REASON }
  const label = `挂到 ${s.label} 末尾`
  if (s.locked) return { label, reason: '条目已定稿，页序冻结' }
  if (s.pending) return { label, reason: '先处理页序条上的冲突提示' }
  return { label, reason: null }
})
const insertAction = computed<PoolAction>(() => {
  const s = stripState.value
  if (!s) return { label: '插到选中页之后', reason: ITEM_REASON }
  if (s.locked) return { label: '插到选中页之后', reason: '条目已定稿，页序冻结' }
  if (s.pending) return { label: '插到选中页之后', reason: '先处理页序条上的冲突提示' }
  if (s.insertAfterPageNo === null) return { label: '插到选中页之后', reason: '先在页序条里选一页' }
  return { label: `插到第 ${s.insertAfterPageNo} 页之后`, reason: null }
})
const createAction = computed<PoolAction>(() => {
  if (!category.value) return { label: '用选中的文件新建条目', reason: '先在归类树里选一个节点' }
  if (atRoot.value) return { label: '用选中的文件新建条目', reason: '顶层不新建条目，请先在归类树里选一个节点' }
  return { label: '用选中的文件新建条目', reason: null }
})
const poolBusy = computed(() => linking.value || creating.value || !!stripState.value?.busy)

function opFiles(files: FileEntry[]) {
  return files.map((f) => ({
    file_id: f.file_id,
    file_name: f.file_name,
    folder_id: poolFolderId.value ?? undefined,
    review_status: f.review_status,
    review_round: f.review_round,
    version_no: f.version_no,
  }))
}

async function linkFromPool(files: FileEntry[], mode: 'end' | 'after') {
  const d = detailRef.value
  if (!d || !files.length || linking.value) return
  linking.value = true
  try {
    await d.linkFiles(opFiles(files), mode)
  } finally {
    linking.value = false
  }
}

/** ItemDetail 挂接成功：池里马上标成已归类（"仅未归类"时消失），清掉池的选择。 */
function onLinked(fileIds: string[]) {
  poolRef.value?.markCataloged(fileIds)
  poolRef.value?.clearSelection()
}

// 用选中的文件新建条目（POST 带 file_ids，M3-3）
const createCataloged = reactive({
  open: false,
  files: [] as FileEntry[],
  items: [] as CatalogedElsewhere[],
  names: {} as Record<string, string>,
})
async function createFromPool(files: FileEntry[]) {
  const cid = categoryQuery.value
  if (!cid || atRoot.value || creating.value || !files.length) return
  if (files.length > MAX_ITEM_PAGES) {
    toast.error(`一个条目最多 ${MAX_ITEM_PAGES} 页，选中了 ${files.length} 个文件`)
    return
  }
  if (!(await confirmDiscard())) return
  creating.value = true
  try {
    const it = await createArchiveItem(cid, { file_ids: files.map((f) => f.file_id) }, true)
    toast.success(`已用 ${filesText(files.map((f) => f.file_name))} 新建第 ${it.item_no} 件`, {
      description: it.archive_no ? `档号 ${it.archive_no}，${it.page_count} 页` : `档号为空：${it.archive_no_reason?.message ?? ''}`,
    })
    onLinked(it.pages.map((p) => p.file_id))
    void tree.refreshFor([cid])
    await loadList()
    detailRef.value?.discard()
    await go(cid, it.id)
    await nextTick()
    document.querySelector<HTMLInputElement>('[data-testid=item-title]')?.focus()
  } catch (e) {
    if (handledGlobally(e)) return
    if (isApiError(e, 'file_cataloged')) {
      const d = (e as ApiError).details as FileCatalogedDetails | undefined
      if (d?.items?.length) {
        createCataloged.files = files
        createCataloged.items = d.items
        createCataloged.names = Object.fromEntries(files.map((f) => [f.file_id, f.file_name]))
        createCataloged.open = true
        return
      }
    }
    if (isApiError(e, 'not_found') && ((e as ApiError).details as { ids?: string[] } | undefined)?.ids?.length) {
      toast.error('有文件已不存在（可能刚被删除或移到了别的项目），没有新建条目', { description: '待归类池已刷新，请重新选择' })
      void poolRef.value?.reload()
      return
    }
    toast.error(errorMessage(e))
  } finally {
    creating.value = false
  }
}

function createCatalogedRetry() {
  createCataloged.open = false
  void createFromPool(createCataloged.files)
}
function createCatalogedSkip(blocked: string[]) {
  createCataloged.open = false
  const bad = new Set(blocked)
  const rest = createCataloged.files.filter((f) => !bad.has(f.file_id))
  if (rest.length) void createFromPool(rest)
}

// 看原图（页序条、池里双击）
const viewer = reactive({ mounted: false, open: false, files: [] as FileEntry[], fileId: null as string | null })
function openViewer(files: FileEntry[], fileId: string) {
  viewer.files = files
  viewer.fileId = fileId
  viewer.mounted = true
  viewer.open = true
}

// ---------- 有未保存的修改时先确认 ----------

const discardAsk = reactive({
  open: false,
  resolve: null as ((ok: boolean) => void) | null,
  what: null as 'form' | 'pages' | 'both' | null,
})
const DISCARD_TEXT = {
  form: { title: '著录还没有保存', description: '离开这个条目会放弃没有保存的修改。' },
  pages: {
    title: '页序还有没确认的操作',
    description: '页序条上有一个因为冲突没保存的操作（预览），离开这个条目会放弃它。',
  },
  both: {
    title: '著录还没有保存，页序也有没确认的操作',
    description: '离开这个条目会放弃没有保存的著录修改，以及页序条上因为冲突没保存的操作。',
  },
} as const

function confirmDiscard(): Promise<boolean> {
  if (!detailRef.value?.isDirty()) return Promise.resolve(true)
  return new Promise((resolve) => {
    discardAsk.resolve?.(false)
    discardAsk.resolve = resolve
    discardAsk.what = detailRef.value?.unsavedWhat() ?? 'form'
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

// ---------- 新建条目、批量定稿 ----------

const creating = ref(false)
async function newItem() {
  const cid = categoryQuery.value
  if (!cid || creating.value) return
  if (!(await confirmDiscard())) return
  creating.value = true
  try {
    const it = await createArchiveItem(cid, {}, true)
    toast.success(`已新建第 ${it.item_no} 件`, {
      description: it.archive_no ? `档号 ${it.archive_no}` : `档号为空：${it.archive_no_reason?.message ?? ''}`,
    })
    void tree.refreshFor([cid])
    await loadList()
    // 已经确认过放弃修改：直接换（不再走一遍确认）。
    detailRef.value?.discard()
    await go(cid, it.id)
    await nextTick()
    document.querySelector<HTMLInputElement>('[data-testid=item-title]')?.focus()
  } catch (e) {
    if (!handledGlobally(e)) toast.error(errorMessage(e))
  } finally {
    creating.value = false
  }
}

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
    toast.success(`已定稿 ${res.locked} 个${res.skipped ? `，跳过 ${res.skipped} 个已定稿的` : ''}`)
    selection.clear()
  } catch (e) {
    if (handledGlobally(e)) return
    const bad = e instanceof ApiError ? (e.details as { ids?: string[] } | undefined)?.ids : undefined
    if (isApiError(e, 'invalid_state') && bad?.length) {
      blockedIds.value = new Set(bad)
      confirmBatch.open = false
      toast.error('有条目不满足定稿条件，一个都没有定稿', { description: `${(e as ApiError).message}（已在列表里标红）` })
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
    toast.warning(`${head}；有 ${conflicts} 个条目因为撞号档号为空`, {
      description: `${renumbered ? `重新生成了 ${renumbered} 个档号。` : ''}撞号的条目档号为空、不能定稿，打开它们可以看到和谁撞了。`,
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
    toast.error(`「${node.category_name}」（含下级节点）下还有 ${node.item_count} 个条目，不能删除`, {
      description: '请先把条目移到别的节点',
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
      `已按新模板重新生成${confirmRenumber.label}的档号${res.skipped_locked ? `（${res.skipped_locked} 个已定稿的没有动）` : ''}`,
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
      // 换了项目：切换器已经确认过（addSwitchGuard）；别的途径换的（例如项目被取消授权）还有没保存的修改时提示一次。
      if (switched && detailRef.value?.isDirty()) {
        detailRef.value.discard()
        toast.warning('换了项目，条目没有保存的修改已放弃')
      }
      await resetForProject(p)
      if (switched && (cid || itemQuery.value || poolQuery.value)) {
        // 用户在切换器里换了项目：清掉地址里的节点、条目、池的文件夹（地址变了会再进来一次；池换成新项目记住的）。
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
//
// catalog.changed 的 item_ids（M3-3 起 server 一律带上）：
// - 带了、非空：这些条目变了，category_ids 是它们所在的节点（新建、移动时当前节点的列表也要重取）；
// - 带了、空数组：只有节点变了（新建、改名、移动、删除节点），只刷新树，不重取列表和详情；
// - 没带：超过 500 个条目，把 category_ids 当作子树的根，当前节点在这些子树里就重取列表和详情。M3-3 合入前
//   节点的事件也不带 item_ids，照这一条处理（多发一些请求，但结果对）。

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
// 待归类池用的（M3-6）：files.changed 的 folder_ids（没带时为 null 表示不知道范围）、folders.changed 的 folder_ids、
// catalog.changed 里 item_ids 为空或没带（只有节点变了，或者超过 500 个条目：归类路径可能变了）。
const pendingFileFolders = new Set<string>()
let pendingFileFoldersUnknown = false
const pendingFolderIds = new Set<string>()
let pendingFolderIdsUnknown = false
let pendingCatalogStructure = false
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
    if (!e.item_ids?.length) {
      pendingCatalogStructure = true
      cats.forEach((id) => pendingStructCats.add(id))
    }
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
  return pendingFull || pendingCatalog || pendingFilesAll || pendingFolders || pendingFiles.size > 0
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
  const structure = pendingCatalogStructure
  const structCats = new Set(pendingStructCats)
  clearPending()

  const tasks: Promise<unknown>[] = []
  // 路径过期（M3-5 遗留，design §7）：节点改名、移动、改编码而档号没变时 item_ids 是 []，条目详情的 category_path
  // 却变了。刷新树之前取打开着的条目所在节点和它的祖先（不含根）：
  // - 祖先出现在任何一条事件的 category_ids 里 → 重取详情（条目自己的事件只带它所在的节点，不会误中祖先）；
  // - 所在节点本身出现在"只有节点变了"的事件里（item_ids 为空或没带）→ 重取详情。所在节点里别的条目变了的事件
  //   （item_ids 非空）不算，免得同一个节点里改一个条目就重取一次详情（M3-5 审查的事件放大）。
  // 不依赖刷新后的树：节点被移到收起着的节点下面时，树上的父子关系还是旧的（M3-6 审查第 1 轮）。刷新树之后名称链
  // 变了也补一次（兜底）。
  const own = detailItem.value?.category_id ?? null
  const pathBefore = own ? tree.pathText(own) : null
  let detailQueued = false
  if (own && itemQuery.value && (tree.ancestorsOf(own).some((a) => cats.has(a)) || structCats.has(own))) {
    detailQueued = true
    if (detailRef.value) tasks.push(detailRef.value.refresh())
  }
  if (catalog) {
    tasks.push(
      tree.refreshFor(cats).then(() => {
        if (!own || detailQueued || detailItem.value?.category_id !== own) return
        if (tree.pathText(own) !== pathBefore) return detailRef.value?.refresh()
      }),
    )
  }
  if (fileIds.size) images.invalidate(fileIds)

  // 待归类池（M3-6）。
  const pool = poolRef.value
  if (pool) {
    if (fileEvents) pool.onFilesChanged(fileFolders, fileIds)
    if (folders) pool.onFoldersChanged(folderIds)
    if (catalog) pool.onCatalogChanged(structure ? null : itemIds)
  }

  const cur = categoryQuery.value
  const selected = itemQuery.value
  let listAffected = false
  let detailAffected = false
  if (cur) {
    if (itemCats.has(cur)) listAffected = true
    if (items.value.some((i) => itemIds.has(i.id))) listAffected = true
    // 没带 item_ids：当前节点在这些子树里。当前节点的祖先链由 reveal 保证都在树上，所以树上不认识的节点不可能是
    // 它的祖先（isUnder 不知道时按不在处理）。
    for (const root of subtrees) {
      if (root === cur || tree.isUnder(cur, root) === true) {
        listAffected = true
        detailAffected = true
      }
    }
  }
  if (selected && itemIds.has(selected)) detailAffected = true
  const detailFiles = new Set(detailItem.value?.pages.map((p) => p.file_id) ?? [])
  if (selected && detailFiles.size) {
    if (filesAll || folders || [...fileIds].some((f) => detailFiles.has(f))) detailAffected = true
  }
  if (cur && filesAll && items.value.some((i) => i.page_count > 0)) listAffected = true
  if (detailAffected && detailRef.value && !detailQueued) {
    detailQueued = true
    tasks.push(detailRef.value.refresh())
  }
  if (listAffected) tasks.push(loadList())
  else if (cur && fileIds.size && items.value.some((i) => i.page_count > 0)) tasks.push(refreshListIfFilesShown(fileIds))
  await Promise.all(tasks)
}

/** 文件的审核状态变了：查这些文件在哪些条目，列表里有就重新取（"n 待审"）。 */
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
  pendingCatalogStructure = false
  pendingStructCats.clear()
}

/** 整体刷新：resync、断线重连之后（断开期间的事件不会补发）。树只刷新看得到的部分，收起的再展开时重新取。 */
async function refreshAll() {
  void loadSettings()
  images.invalidate()
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
    poolRef.value ? poolRef.value.refresh() : null,
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
      void poolRef.value?.reload()
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
  if (applyTimer) clearTimeout(applyTimer)
  applyTimer = null
  discardAsk.resolve?.(false)
})

const templateError = computed(() => settings.value?.archive_no_template_error ?? null)
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
  >
    <header class="flex h-14 shrink-0 items-center gap-3 border-b px-5">
      <h1 class="shrink-0 text-base font-semibold tracking-tight text-foreground">归类</h1>
      <span
        v-if="settings"
        class="min-w-0 truncate border-l pl-3 font-mono text-xs text-muted-foreground"
        :title="`档号模板：${settings.archive_no_template}${settings.lock_requires_approved ? '；定稿要求全部页审核通过' : ''}`"
        data-testid="catalog-template"
      >{{ settings.archive_no_template }}</span>
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
      <!-- 左栏：归类树 + 条目列表 -->
      <aside class="flex w-[300px] shrink-0 flex-col border-r bg-sidebar/60">
        <!-- 归类树 -->
        <div class="flex h-9 shrink-0 items-center gap-1 px-3 pt-1">
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
            <TooltipContent>删除（含下级节点；有条目时不能删）</TooltipContent>
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
              class="h-[38%] min-h-[150px] shrink-0 overflow-y-auto px-2 pb-2"
              data-testid="category-tree-area"
              @contextmenu.capture="onTreeContextMenu"
            >
              <div v-if="treeState === 'loading'" class="flex items-center gap-2 p-2 text-xs text-muted-foreground">
                <LoaderCircleIcon class="size-3.5 animate-spin" />正在加载归类树…
              </div>
              <div v-else-if="treeState === 'error'" class="p-2 text-xs text-destructive">
                归类树加载失败：{{ treeError }}
                <button type="button" class="ml-1 underline" @click="project.current && resetForProject(project.current)">重试</button>
              </div>
              <template v-else>
                <!-- 直接放在根节点下的条目（server 允许；界面上不显示根节点，这里单独给一行，能看到、能移走） -->
                <button
                  v-if="tree.rootDirectCount.value > 0"
                  type="button"
                  class="mb-px flex w-full items-center gap-1.5 rounded-md py-1 pr-2 pl-[26px] text-left text-[13px] hover:bg-muted"
                  :class="atRoot ? 'bg-info-soft font-medium text-info hover:bg-info-soft' : 'text-sidebar-foreground'"
                  title="直接放在顶层（没有归到任何节点下）的条目，建议移到合适的节点"
                  data-testid="tree-root-items"
                  :data-items="tree.rootDirectCount.value"
                  @click="tree.rootId.value && selectCategory(tree.rootId.value)"
                >
                  <span class="min-w-0 flex-1 truncate">（直接放在顶层的条目）</span>
                  <span class="shrink-0 font-mono text-[11px] text-warn tabular-nums">{{ tree.rootDirectCount.value.toLocaleString() }} 件</span>
                </button>
                <CategoryTree :tree="tree" :selected-id="categoryQuery" @select="selectCategory" />
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

        <!-- 条目列表 -->
        <div class="flex min-h-0 flex-1 flex-col border-t">
          <template v-if="category">
            <div class="flex shrink-0 flex-col gap-1.5 px-3 pt-2 pb-2">
              <div class="flex items-center gap-1.5 text-xs">
                <span class="min-w-0 truncate font-medium text-foreground" :title="atRoot ? '直接放在顶层的条目' : tree.pathText(category.id)">
                  {{ atRoot ? '直接放在顶层的条目' : category.category_name }}
                </span>
                <span class="shrink-0 text-muted-foreground" data-testid="item-total" :data-count="listTotal">
                  · 直接放的 {{ listTotal.toLocaleString() }} 件
                </span>
              </div>
              <div class="flex flex-wrap items-center gap-1">
                <span class="flex shrink-0 items-center pr-1 pl-2" title="全选已加载的条目" @click="toggleAll">
                  <Checkbox
                    :model-value="allChecked"
                    aria-label="全选已加载的条目"
                    class="pointer-events-none"
                    :disabled="!items.length"
                    data-testid="item-check-all"
                  />
                </span>
                <span
                  v-for="s in ITEM_STATUS_ORDER"
                  :key="s"
                  class="shrink-0 rounded-full px-1.5 text-[11px] leading-5 tabular-nums"
                  :class="ITEM_STATUS[s].badge"
                  :data-testid="`item-count-${s}`"
                  :data-count="listCounts[s]"
                >{{ ITEM_STATUS[s].label }} {{ listCounts[s].toLocaleString() }}</span>
                <span class="ml-auto flex shrink-0 items-center gap-1">
                <Button
                  size="xs"
                  variant="outline"
                  :disabled="!selection.count.value || selection.count.value > MAX_LOCK_ITEMS"
                  :title="selection.count.value > MAX_LOCK_ITEMS ? `一次最多定稿 ${MAX_LOCK_ITEMS} 个` : '把勾选的条目一起定稿'"
                  data-testid="batch-lock"
                  @click="askBatchLock"
                >
                  <LockIcon />定稿{{ selection.count.value ? `（${selection.count.value}）` : '' }}
                </Button>
                <Button
                  size="xs"
                  :disabled="creating || atRoot"
                  :title="atRoot ? '顶层不新建条目，请先在归类树里选一个节点' : '在这个节点里新建条目'"
                  data-testid="item-new"
                  @click="newItem"
                >
                  <ListPlusIcon />新建
                </Button>
                </span>
              </div>
            </div>
            <div v-if="listState === 'loading' && !items.length" class="flex flex-1 items-center justify-center gap-2 text-xs text-muted-foreground">
              <LoaderCircleIcon class="size-3.5 animate-spin" />正在加载条目…
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
              <span>这个节点里还没有条目</span>
              <span>下级节点里的条目在各自的节点里看</span>
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
          </template>
          <div v-else class="flex flex-1 items-center justify-center px-6 text-center text-xs text-muted-foreground">
            在上面的归类树里选一个节点，看它里面的条目
          </div>
        </div>
      </aside>

      <!-- 右边：条目详情 -->
      <section class="relative flex min-w-0 flex-1 flex-col">
        <div
          v-if="templateError"
          class="shrink-0 border-b bg-destructive-soft px-5 py-2 text-xs text-destructive"
          data-testid="template-error"
        >
          项目的档号模板解析不了：{{ templateError }}。新建条目、移动、改件号都会失败，请到「系统管理 › 项目」里修改。
        </div>
        <ItemDetail
          v-if="itemQuery && project.currentId"
          :key="itemQuery"
          ref="detailRef"
          :item-id="itemQuery"
          :project-id="project.currentId"
          :settings="settings"
          :can-unlock="canUnlock"
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
          @linked="onLinked"
          @pool-stale="poolRef?.reload()"
          @strip-state="(s) => (stripState = s)"
          @open-viewer="openViewer"
          @export="(id) => goExport('item', id)"
        />
        <div v-else class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center text-sm text-muted-foreground" data-testid="catalog-empty">
          <template v-if="atRoot">
            <div class="text-base font-medium text-foreground">直接放在顶层的条目</div>
            <p class="mt-1">这些条目没有归到任何节点下，档号不完整；打开后用"移到别的节点…"移到合适的节点。</p>
          </template>
          <template v-else-if="category">
            <div class="text-base font-medium text-foreground">{{ category.category_name }}</div>
            <div class="font-mono text-xs">{{ categoryTypeLabel(category.category_type) }} · {{ category.category_code }} · 含下级 {{ category.item_count }} 件</div>
            <p class="mt-1">在左边选一个条目著录，或者新建条目。</p>
            <Button v-if="canExport" size="sm" variant="outline" class="mt-2" data-testid="category-export-main" @click="goExport('category', category.id)">
              <FileOutputIcon />导出这个节点（含下级 {{ category.item_count }} 件）…
            </Button>
          </template>
          <p v-else>在左边的归类树里选一个节点。</p>
        </div>
      </section>

      <!-- 第三栏：待归类池（M3-6） -->
      <CatalogPool
        v-if="canBrowse && project.current"
        ref="poolRef"
        :project-id="project.current.id"
        :root-folder-id="project.current.root_folder_id"
        :project-name="project.current.project_name"
        :folder-id="poolFolderId"
        :current-item-id="itemQuery"
        :item-no-width="itemNoWidth"
        :link-action="linkAction"
        :insert-action="insertAction"
        :create-action="createAction"
        :busy="poolBusy"
        @update:folder-id="setPoolFolder"
        @invalid="onPoolInvalid"
        @link="linkFromPool"
        @create="createFromPool"
        @open="openViewer"
      />
      <aside
        v-else-if="project.current"
        class="flex w-[300px] shrink-0 items-center justify-center border-l px-6 text-center text-xs text-muted-foreground"
        data-testid="catalog-pool-forbidden"
      >
        待归类池要浏览文件目录（文件目录菜单）；你的账号没有这个菜单，请联系管理员。
      </aside>
    </div>

    <!-- 对话框 -->
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
      description="连同下级节点和里面的条目一起移动；未定稿条目的档号按新位置重新生成。子树里有已定稿的条目时不能移动。"
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
      :title="`定稿选中的 ${confirmBatch.ids.length} 个条目？`"
      description="一起定稿：任何一个不满足定稿条件（已完成、至少 1 页、档号不为空、项目要求时全部页审核通过）就一个都不定稿，并标出不满足的条目。已定稿的会跳过。"
      confirm-text="定稿"
      :loading="confirmBatch.loading"
      @confirm="doBatchLock"
    />
    <ConfirmDialog
      v-model:open="confirmRenumber.open"
      :title="`按新模板重新生成${confirmRenumber.label}的档号？`"
      description="按项目现在的档号模板，重新生成范围内所有未定稿条目的档号（已定稿的不动）。生成出来的档号和别的条目重复时，那个条目的档号为空。"
      confirm-text="重新生成"
      :loading="confirmRenumber.loading"
      @confirm="doRenumber"
    />
    <ConfirmDialog
      v-model:open="discardAsk.open"
      :title="DISCARD_TEXT[discardAsk.what ?? 'form'].title"
      :description="DISCARD_TEXT[discardAsk.what ?? 'form'].description"
      confirm-text="放弃修改"
      destructive
      @confirm="resolveDiscard(true)"
    />
    <FileCatalogedDialog
      v-model:open="createCataloged.open"
      :items="createCataloged.items"
      :names="createCataloged.names"
      :total="createCataloged.files.length"
      action="新建条目"
      :item-no-width="itemNoWidth"
      @retry="createCatalogedRetry"
      @skip="createCatalogedSkip"
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
