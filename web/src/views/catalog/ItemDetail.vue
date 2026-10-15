<script setup lang="ts">
// 归类页右边的条目详情（M3-5）：档号、著录表单、页序（只读）和状态动作。规则见 design §2.3"档案条目"。
//
// - **档号只读**，按模板自动生成；为空时显示原因（`archive_no_reason`）：缺级（缺哪几级）、撞号（和谁撞，可以跳到占用的
//   条目）、未生成（"按新模板重新生成"）、模板不对。件号未定稿时可以改（撞号、被占用 409 `conflict`，提示换一个号）。
// - **著录表单**按 catalog-settings 的 item_fields 动态生成：text（长的用多行框）、date（YYYY / YYYY-MM / YYYY-MM-DD）、
//   enum（下拉）。旧值（用户决定 Q1）：库里的值和现在的设置对不上（`field_issues`）时照样显示原值和提示——不在选项里的
//   enum 值作为额外的选项留在下拉里；设置里已经删掉的著录项单独列出，只能保留或清空。保存时**只提交改过的项**
//   （PATCH 的 extra_fields 按 key 合并），没改的旧值原样保留，不会被悄悄丢掉。
// - **校验**：前端用 lib/catalog 的 checkTitle、checkFieldValue（isValidDateValue、checkTextValue）只校验改过的项；
//   "标记完成"、"定稿"前另查必填。server 的 400 `invalid_input` 按 `details.fields [{key, message}]` 标到对应的项
//   （`title` 是题名），同时让页面重取一次 catalog-settings（设置可能刚被改过，改设置不发事件）。
// - **乐观锁**：保存、状态动作都带 row_version。409 `conflict`（别人刚改过）时重新加载最新版本：没改过的项换成最新的
//   值，改过的项保留在表单里，两边都改了的项标出"别人改成了…"，提示核对后再保存（数据不丢）。SSE 告诉页面条目变了
//   时，表单没改动就直接换成最新的；有改动就只提示"别人刚修改了这个条目"。
// - **状态动作**（design §2.3 的状态 × 动作表）：草稿 →"标记完成"；已完成 →"退回草稿"、"定稿"；已定稿 →"解锁"（只有
//   同时有 catalog 和 review_assign 两个菜单的人看得到，内置只有负责人和 admin）。有未保存的修改时先保存再做。
//   定稿前在本地按 Q5 预判（已完成、至少 1 页、档号不为空、项目要求时全部页审核通过），按钮旁写明还差什么；最终以
//   server 为准。定稿、解锁都要确认。
// - **移到别的节点**：CategoryPicker 选目标，PATCH category_id（件号取那里的 max+1，档号按新位置重算，撞号留空）。
// - 已定稿：表单只读、页序条只读、不能移动、改件号、删除，页头写明；定稿后有页重扫出新版本时页头和页序条都标出。
// - 著录项可以收起（M3-6，只留题名，给页序条腾地方；记在 localStorage，收起的项有错误时自动展开）。
// - **页序**（M3-6 挂接工作台，PageStrip）：挂接、重排、移出都算出整份新的有序列表，经
//   `PUT /api/archive-items/{id}/files {row_version, file_ids}` 一次提交（M3-3），响应就是新的详情。
//   **即时提交**：每个完成的动作（放下拖动的页、键盘放下、Alt+方向键、"移到第 n 页"、移出、从池里挂接）发一次 PUT，
//   不另设"保存页序"。理由：挂接是"占用"文件（一个文件只能在一个条目里），马上提交别人才能马上看到它已归类，少撞
//   409 `file_cataloged`；一个动作一条审计、一条事件，日志读得懂；拖动、键盘"拿起"的过程只在本地，放下才提交，
//   不会按一下键发一次请求；也不会出现"著录没保存、页序也没保存"两种未保存状态叠在一起。提交期间 `busy = 'pages'`，
//   不接受新的动作。页序的提交不改著录：表单有没保存的修改时照样保留（表单的基础版本跟着前进）。
// - **409 conflict**（别人刚改过这个条目）：重新读取最新的详情，把刚才的动作（按"挨着哪个文件"记的位置，page-ops）
//   重做在最新的页序上，作为**预览**显示在页序条里（新挂上的绿框、挪动的黄框），横幅写明"你刚才的操作没有保存：…"，
//   用户点"保存这个页序"才提交，或者"放弃"。这个待确认的动作算未保存的修改（`isDirty()`：换条目、换节点、离开页面、
//   切换项目都先确认）。
// - **409 file_cataloged**：FileCatalogedDialog 列出"在 某节点 / 档号 第 p 页"，可以"从原条目移出"后自动重试
//   （原条目已定稿时置灰），或者跳过这些文件。**404 带 details.ids**：文件已删除或不在本项目，提示并让页面刷新待归类池
//   （可以只挂其余的）。**409 item_locked**：条目刚被定稿，重新读取后变成只读。
// - **删除条目**（M3-3 的接口，M3-5 没做界面）：未定稿的才能删；确认框写明"n 页会一并移出、回到未归类"，带 row_version；
//   定稿的 409 `item_locked`。
// - 截图脚本用的属性：根 data-testid=item-detail（data-item-id、data-status、data-row-version、data-dirty、data-load、
//   data-busy、data-pending-op）；档号 item-archive-no / item-archive-no-empty（data-code）；路径 item-category-path
//   （data-path）；表单项 data-testid=item-field（data-key、data-error、data-issue）；各按钮 item-save、item-mark-done、
//   item-back-to-draft、item-lock、item-unlock、item-move、item-no-edit、item-delete、item-export（M4-5，有 export 菜单时）；
//   冲突横幅 page-conflict（按钮 page-conflict-redo、page-conflict-discard）。
import {
  ArrowRightLeftIcon,
  CheckCheckIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  CircleAlertIcon,
  ExternalLinkIcon,
  FileOutputIcon,
  LoaderCircleIcon,
  LockIcon,
  LockOpenIcon,
  PencilIcon,
  RefreshCwIcon,
  SaveIcon,
  Trash2Icon,
  Undo2Icon,
} from '@lucide/vue'
import { useEventListener } from '@vueuse/core'
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { toast } from 'vue-sonner'

import {
  deleteArchiveItem,
  getArchiveItem,
  type ItemAction,
  itemAction,
  MAX_ITEM_PAGES,
  putArchiveItemFiles,
  updateArchiveItem,
} from '@/api/catalog'
import { ApiError, errorMessage, isApiError } from '@/api/http'
import type {
  ArchiveItem,
  CatalogedElsewhere,
  CatalogSettings,
  FieldIssue,
  FileCatalogedDetails,
  FileEntry,
  ItemFieldDef,
  UpdateArchiveItemBody,
} from '@/api/types'
import CategoryPicker, { type PickedCategory } from '@/components/catalog/CategoryPicker.vue'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import GuardedButton from '@/components/app/GuardedButton.vue'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import {
  archiveNoReasonShort,
  checkFieldValue,
  checkTitle,
  fieldIssuesOf,
  formatItemNo,
  itemStatusOf,
  MAX_ITEM_NO,
  storedFieldText,
  TITLE_KEY,
} from '@/lib/catalog'
import { formatDateTime } from '@/lib/format'
import { handledGlobally } from '@/views/review-assign/review-errors'

import { itemOpErrorText } from './catalog-errors'
import FileCatalogedDialog from './FileCatalogedDialog.vue'
import { applyOp, filesText, type OpFile, type PageOp, type PageTarget } from './page-ops'
import PageStrip, { type StripPage } from './PageStrip.vue'

/** 页序条的状态（给待归类池的按钮用）。 */
export interface StripState {
  itemId: string
  /** 档号（为空时"第 n 件"）。 */
  label: string
  locked: boolean
  busy: boolean
  /** 有待确认的冲突预览。 */
  pending: boolean
  pageCount: number
  /** 页序条选中的最后一页的页号（"插到第 p 页之后"）；没选中为 null。 */
  insertAfterPageNo: number | null
}

const props = defineProps<{
  itemId: string
  projectId: string
  settings: CatalogSettings | null
  /** 能解锁（同时有 catalog 和 review_assign 两个菜单）。 */
  canUnlock: boolean
  /** 件号补零的位数。 */
  itemNoWidth: number
  /** 页面收到这个条目的事件（或要整体刷新）时加一，重新取详情。 */
  refreshToken: number
  /** 所在节点从顶层起的名称路径（页面的树上知道时给出；名称里可能有 /，比拆 category_path 准）。 */
  categoryLabel?: string | null
  /** 显示"导出这件"（M4-5：有 export 菜单）。 */
  canExport?: boolean
}>()

const emit = defineEmits<{
  /** 取到（或刷新了）详情：页面用它更新列表里的这一行。 */
  loaded: [item: ArchiveItem]
  /** 自己保存、状态动作成功之后。 */
  changed: [item: ArchiveItem]
  /** 移到了别的节点。 */
  moved: [item: ArchiveItem, fromCategoryId: string]
  /** 条目已不存在（404）。 */
  gone: [id: string]
  /** 跳到另一个条目（撞号时"查看占用的条目"）。 */
  openItem: [categoryId: string, itemId: string]
  /** 按新模板重新生成这个节点（含下级）的档号（页面确认后调接口）。 */
  renumber: [categoryId: string]
  /** catalog-settings 可能过时了（400 之后），请页面重取。 */
  settingsStale: []
  dirty: [dirty: boolean]
  /** 条目已删除（自己删的）。 */
  deleted: [item: ArchiveItem]
  /** 这些文件刚挂到了这个条目上（待归类池马上标成已归类）。 */
  linked: [fileIds: string[]]
  /** 有要挂的文件已不存在或不在本项目（404 details.ids）：请页面刷新待归类池。 */
  poolStale: []
  /** 页序条的状态变了（待归类池的按钮用）。 */
  stripState: [state: StripState | null]
  /** 看原图（页面打开 OriginalViewerDialog）。 */
  openViewer: [files: FileEntry[], fileId: string]
  /** 导出这个条目（M4-5：页面带着范围跳到 /export 的新建导出）。 */
  export: [itemId: string]
}>()

// ---------- 状态 ----------

/** server 最新的条目（显示档号、状态、页序都用它）。 */
const item = ref<ArchiveItem | null>(null)
/** 表单是从哪个版本开始编辑的（row_version、题名、著录项）；保存时带这个 row_version。 */
const base = ref<{ row_version: number; title: string; extra: Record<string, unknown> } | null>(null)
const loadState = ref<'loading' | 'ready' | 'error'>('loading')
const loadError = ref('')
const title = ref('')
/** 著录项的表单值（设置里的和设置里已经没有、库里还存着的都在这里）。 */
const values = reactive<Record<string, string>>({})
/** 每一项开始编辑时的表单值（判断改没改过）。 */
const initial = ref<Record<string, string>>({})
/** 设置里已经没有、用户点了"清空"的著录项。 */
const cleared = ref<Set<string>>(new Set())
/** 每一项的错误（本地校验和 server 的 details.fields；`title` 是题名）。 */
const errors = ref<Record<string, string>>({})
/** 409 之后合并时两边都改了的项：key → 别人改成的值。 */
const theirs = ref<Record<string, string>>({})
const mergeNote = ref<string | null>(null)
const busy = ref<'' | 'save' | ItemAction | 'move' | 'item-no' | 'pages' | 'delete'>('')

const defs = computed<ItemFieldDef[]>(() => props.settings?.item_fields ?? [])
const status = computed(() => item.value?.status ?? 'draft')
const locked = computed(() => status.value === 'locked')
/** 库里存着、设置里已经没有的著录项（按库里的顺序）。 */
const orphanKeys = computed(() => {
  const known = new Set(defs.value.map((d) => d.key))
  return Object.keys(base.value?.extra ?? {}).filter((k) => !known.has(k))
})
const issues = computed(() => {
  const m = new Map<string, string>()
  for (const i of item.value?.field_issues ?? []) m.set(i.key, i.message)
  return m
})

/** 改过的著录项（只提交这些）：key → 新值（空串、清空为 null）。 */
const changedExtra = computed(() => {
  const out: Record<string, string | null> = {}
  for (const d of defs.value) {
    const v = (values[d.key] ?? '').trim()
    if (v !== (initial.value[d.key] ?? '').trim()) out[d.key] = v || null
  }
  for (const k of cleared.value) out[k] = null
  return out
})
const titleChanged = computed(() => !!base.value && title.value.trim() !== base.value.title.trim())
const dirty = computed(() => !locked.value && (titleChanged.value || Object.keys(changedExtra.value).length > 0))
/** 409 conflict 之后待确认的页序动作（在最新的页序上重做的预览，见"页序"一节）。 */
const pendingOp = ref<PageOp | null>(null)
/** 有没保存的修改：著录表单改过，或者有待确认的页序动作。 */
const unsaved = computed(() => dirty.value || !!pendingOp.value)
/**
 * 有待确认的冲突预览时，状态动作（标记完成、退回草稿、定稿、解锁）、移动、改件号、删除都先不让做（M3-6 审查：原来
 * 能直接定稿，定稿后预览作废时提示还说成"别人定稿"）。保存著录不受影响。
 */
const PENDING_REASON = '页序条上有一个因为冲突没保存的操作，先"保存这个页序"或"放弃"'
const actionsBlocked = computed(() => busy.value !== '' || !!pendingOp.value)
watch(unsaved, (d) => emit('dirty', d), { immediate: true })

/** 别人在你编辑期间改了这个条目（SSE 拿到的最新版本比表单开始编辑的版本新）。 */
const remoteChanged = computed(
  () =>
    !!item.value &&
    !!base.value &&
    dirty.value &&
    item.value.row_version !== base.value.row_version &&
    !sameFields(item.value, base.value),
)

/**
 * 条目的题名、著录项和表单开始编辑时的一样（别人只动了页序、状态、件号这些，或者是自己刚提交的页序）。这时表单的
 * 修改照样能直接套在新版本上：基础版本跟着前进，保存不会 409，也不用提示"别人改过"（M3-6 审查：页序即时提交之后，
 * "别人只改了页序"会很常见）。
 */
function sameFields(it: ArchiveItem, b: { title: string; extra: Record<string, unknown> }): boolean {
  return it.title === b.title && JSON.stringify(it.extra_fields) === JSON.stringify(b.extra)
}

// ---------- 加载 ----------

let loadSeq = 0

function resetForm(it: ArchiveItem, opts: { keepErrors?: boolean } = {}) {
  base.value = { row_version: it.row_version, title: it.title, extra: { ...it.extra_fields } }
  title.value = it.title
  for (const k of Object.keys(values)) delete values[k]
  const init: Record<string, string> = {}
  for (const d of defs.value) init[d.key] = storedFieldText(it.extra_fields[d.key])
  for (const k of Object.keys(it.extra_fields)) if (!(k in init)) init[k] = storedFieldText(it.extra_fields[k])
  Object.assign(values, init)
  initial.value = init
  cleared.value = new Set()
  if (!opts.keepErrors) errors.value = {}
  theirs.value = {}
}

/**
 * 拿到 server 的新版本：表单没改动时整个换掉，有改动时只更新显示的部分（表单保留，提示别人改过）。
 * 别人把它定稿了：表单不能再改，未保存的修改作废（提示一次）。
 */
function applyFresh(it: ArchiveItem, opts: { force?: boolean } = {}) {
  // 慢回来的旧结果（比现在显示的版本旧，例如保存之前发出的 GET）不要盖掉刚保存的。版本相同照样用：页的审核状态、
  // 版本号变了不改 row_version。
  if (!opts.force && item.value && item.value.id === it.id && it.row_version < item.value.row_version) return
  const lostEdits = !opts.force && dirty.value && it.status === 'locked'
  item.value = it
  if (lostEdits) toast.warning('条目刚被别人定稿，你没有保存的修改已作废', { description: '要修改请先解锁' })
  if (it.status === 'locked' && pendingOp.value) {
    pendingOp.value = null
    // force 是自己的动作（有待确认的预览时状态动作是禁用的，按说走不到这里）。
    toast.warning(opts.force ? '已定稿，没有确认的页序操作已作废' : '条目刚被别人定稿，没有确认的页序操作已作废', {
      description: '要修改请先解锁',
    })
  }
  if (opts.force || lostEdits || !dirty.value || !base.value) {
    resetForm(it)
    mergeNote.value = null
  } else if (it.row_version > base.value.row_version && it.status !== 'locked' && sameFields(it, base.value)) {
    // 别人只动了页序、状态这些（著录还是开始编辑时的样子）：修改留在表单里，基础版本跟着前进。
    base.value = { ...base.value, row_version: it.row_version }
  }
  loadState.value = 'ready'
  emit('loaded', it)
}

async function load(opts: { force?: boolean } = {}) {
  const id = props.itemId
  const my = ++loadSeq
  if (!item.value || item.value.id !== id) loadState.value = 'loading'
  try {
    const it = await getArchiveItem(id, ['not_found'])
    if (my !== loadSeq || props.itemId !== id) return
    applyFresh(it, opts)
  } catch (e) {
    if (my !== loadSeq) return
    if (isApiError(e, 'not_found')) {
      emit('gone', id)
      return
    }
    if (!item.value) {
      loadState.value = 'error'
      loadError.value = errorMessage(e)
    }
  }
}

watch(
  () => props.itemId,
  () => {
    item.value = null
    base.value = null
    mergeNote.value = null
    pendingOp.value = null
    void load({ force: true })
  },
  { immediate: true },
)
watch(
  () => props.refreshToken,
  () => void load(),
)
// 设置变了（重新取到 catalog-settings）：没改动时按新设置重排表单。保留各项上的错误标记：标记完成、定稿被
// 400 拒绝时页面会重取设置（M3-5 审查：原来这里把 server 刚标出的错误清掉了）。
watch(defs, () => {
  if (item.value && !dirty.value) resetForm(item.value, { keepErrors: true })
})

// ---------- 校验 ----------

function defOf(key: string): ItemFieldDef | undefined {
  return defs.value.find((d) => d.key === key)
}

/** 本地校验改过的项（草稿规则：类型和格式）。 */
function validateChanged(): Record<string, string> {
  const out: Record<string, string> = {}
  if (titleChanged.value) {
    const why = checkTitle(title.value.trim())
    if (why) out[TITLE_KEY] = why
  }
  for (const [k, v] of Object.entries(changedExtra.value)) {
    const d = defOf(k)
    if (!d || v === null) continue
    const why = checkFieldValue(d, v)
    if (why) out[k] = why
  }
  return out
}

/** 必填（含题名）为空的（"标记完成"、"定稿"前查）。 */
function missingRequired(): Record<string, string> {
  const out: Record<string, string> = {}
  if (!title.value.trim()) out[TITLE_KEY] = '题名不能为空'
  for (const d of defs.value) if (d.required && !(values[d.key] ?? '').trim()) out[d.key] = `${d.label}不能为空`
  return out
}

function labelOf(key: string): string {
  return key === TITLE_KEY ? '题名' : (defOf(key)?.label ?? key)
}

function showFieldErrors(list: FieldIssue[], head: string) {
  const map: Record<string, string> = {}
  for (const f of list) map[f.key] ??= f.message
  errors.value = map
  toast.error(head, { description: list.slice(0, 3).map((f) => f.message).join('；') })
}

// ---------- 保存 ----------

/** 别人改过（409 conflict）：取最新的，没改过的项换成最新的，改过的保留，两边都改了的标出来。 */
async function mergeAfterConflict(fresh?: ArchiveItem) {
  const id = props.itemId
  let latest: ArchiveItem
  if (fresh) latest = fresh
  else {
    try {
      latest = await getArchiveItem(id, ['not_found'])
    } catch (e) {
      if (isApiError(e, 'not_found')) emit('gone', id)
      return
    }
  }
  if (props.itemId !== id) return
  if (latest.status === 'locked') {
    // 别人把它定稿了：修改保存不了，直接换成最新的。
    applyFresh(latest, { force: true })
    toast.warning('条目已被别人定稿，你没有保存的修改已作废', { description: '要修改请先解锁' })
    return
  }
  const mineTitle = title.value
  const mine = { ...values }
  const startTitle = base.value?.title ?? ''
  const start = { ...initial.value }
  const userChangedTitle = titleChanged.value
  const userChanged = new Set(Object.keys(changedExtra.value))
  const keepCleared = new Set(cleared.value)
  item.value = latest
  resetForm(latest)
  const both: Record<string, string> = {}
  if (userChangedTitle) {
    title.value = mineTitle
    if (latest.title.trim() !== startTitle.trim() && latest.title.trim() !== mineTitle.trim()) both[TITLE_KEY] = latest.title
  }
  for (const k of userChanged) {
    if (keepCleared.has(k)) continue
    const now = storedFieldText(latest.extra_fields[k])
    values[k] = mine[k] ?? ''
    if (now.trim() !== (start[k] ?? '').trim() && now.trim() !== (mine[k] ?? '').trim()) both[k] = now
  }
  cleared.value = new Set([...keepCleared].filter((k) => k in latest.extra_fields))
  theirs.value = both
  const kept = [...(userChangedTitle ? [TITLE_KEY] : []), ...userChanged].map(labelOf)
  mergeNote.value =
    `别人在你编辑时修改了这个条目，已载入最新的第 ${latest.row_version} 版。` +
    (kept.length ? `你改过的${kept.join('、')}还在表单里，` : '') +
    (Object.keys(both).length ? `其中${Object.keys(both).map(labelOf).join('、')}别人也改了（见各项下面），` : '') +
    '请核对后再保存。'
  emit('loaded', latest)
  toast.warning('条目已被别人修改，已重新加载', { description: '你的修改保留在表单里，请核对后再保存' })
}

/** 保存改过的项。成功返回保存后的条目；没有改动返回现在的条目；失败返回 null（已经提示过）。 */
async function save(): Promise<ArchiveItem | null> {
  const it = item.value
  const b = base.value
  // 在途保护：上一次保存、状态动作还没回来（例如连按 Ctrl+S）。
  if (busy.value) return null
  if (!it || !b || locked.value) return null
  if (!dirty.value) return it
  const local = validateChanged()
  if (Object.keys(local).length) {
    errors.value = local
    toast.error('著录内容不对', { description: Object.values(local).slice(0, 3).join('；') })
    return null
  }
  const body: UpdateArchiveItemBody = { row_version: b.row_version }
  if (titleChanged.value) body.title = title.value.trim()
  if (Object.keys(changedExtra.value).length) body.extra_fields = { ...changedExtra.value }
  busy.value = 'save'
  try {
    let saved: ArchiveItem
    try {
      saved = await updateArchiveItem(it.id, body, true)
    } catch (e) {
      if (!isApiError(e, 'conflict')) throw e
      // row_version 过期：读最新的。著录还是开始编辑时的样子（别人只动了页序、状态这些，没有定稿）就用新的
      // row_version 直接再发一次，不打扰用户；否则按原来的合并处理（保留修改、标出两边都改了的项）。
      const latest = await getArchiveItem(it.id, ['not_found']).catch((err) => {
        if (isApiError(err, 'not_found')) emit('gone', it.id)
        return null
      })
      if (!latest || props.itemId !== it.id) return null
      if (latest.status === 'locked' || !sameFields(latest, b)) {
        await mergeAfterConflict(latest)
        return null
      }
      saved = await updateArchiveItem(it.id, { ...body, row_version: latest.row_version }, true)
    }
    applyFresh(saved, { force: true })
    emit('changed', saved)
    return saved
  } catch (e) {
    await onWriteError(e)
    return null
  } finally {
    busy.value = ''
  }
}

/** 保存、状态动作、移动的错误：按错误码处理（全局已经提示过的不再提示）。 */
async function onWriteError(e: unknown) {
  if (handledGlobally(e)) return
  // 著录设置改了不发事件：写失败（4xx）时让页面重取一次 catalog-settings。
  if (e instanceof ApiError && e.status >= 400 && e.status < 500) emit('settingsStale')
  const fields = fieldIssuesOf(e)
  if (fields?.length) {
    showFieldErrors(fields, e instanceof ApiError ? e.message.split('：')[0] || '著录内容不对' : '著录内容不对')
    return
  }
  if (isApiError(e, 'conflict')) {
    if (dirty.value) await mergeAfterConflict()
    else {
      // 状态动作、移动（没有未保存的修改）：直接换成最新的，请用户再做一次。
      toast.warning('条目刚被别人修改过，已重新加载，请再试一次')
      await load({ force: true })
    }
    return
  }
  if (isApiError(e, 'not_found')) {
    toast.error(itemOpErrorText(e))
    emit('gone', props.itemId)
    return
  }
  toast.error(itemOpErrorText(e))
  // item_locked、invalid_state（状态刚变了、定稿的前置条件不满足）：前提已经变了，重新取一次。
  if (isApiError(e, 'item_locked') || isApiError(e, 'invalid_state')) void load()
}

// Ctrl/⌘+S 保存。
useEventListener(window, 'keydown', (ev: KeyboardEvent) => {
  if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 's' && item.value) {
    ev.preventDefault()
    void save()
  }
})

// ---------- 状态动作 ----------

/** 定稿前在本地预判还差什么（Q5）；最终以 server 为准。 */
const lockBlockers = computed(() => {
  const it = item.value
  if (!it) return []
  const out: string[] = []
  if (it.status !== 'done') out.push('要先"标记完成"')
  if (it.page_count === 0) out.push('还没有挂接任何页')
  if (!it.archive_no) out.push('没有档号')
  const notApproved = it.page_count - it.page_counts.approved
  if ((props.settings?.lock_requires_approved ?? true) && notApproved > 0) {
    out.push(`还有 ${notApproved} 页没有审核通过（项目设置要求全部页通过）`)
  }
  return out
})

async function act(action: ItemAction) {
  const it = item.value
  if (!it || busy.value) return
  if (pendingOp.value) {
    toast.info(PENDING_REASON)
    return
  }
  if (action === 'mark-done' || action === 'lock') {
    const missing = { ...validateChanged(), ...missingRequired() }
    if (Object.keys(missing).length) {
      errors.value = missing
      toast.error(action === 'lock' ? '定稿前要把著录填完整' : '标记完成前要把著录填完整', {
        description: Object.values(missing).slice(0, 3).join('；'),
      })
      return
    }
  }
  let current: ArchiveItem | null = it
  if (dirty.value) current = await save()
  if (!current) return
  busy.value = action
  try {
    const res = await itemAction(current.id, action, current.row_version, true)
    applyFresh(res, { force: true })
    emit('changed', res)
    const done: Record<ItemAction, string> = {
      'mark-done': '已标记完成',
      'back-to-draft': '已退回草稿',
      lock: `已定稿：${res.archive_no ?? ''}`,
      unlock: '已解锁，条目回到"已完成"',
    }
    toast.success(done[action])
  } catch (e) {
    await onWriteError(e)
  } finally {
    busy.value = ''
  }
}

const confirmLock = ref(false)
const confirmUnlock = ref(false)

async function doLock() {
  await act('lock')
  confirmLock.value = false
}
async function doUnlock() {
  await act('unlock')
  confirmUnlock.value = false
}

// ---------- 件号 ----------

const itemNoEditing = ref(false)
const itemNoText = ref('')
const itemNoError = ref('')

function startItemNo() {
  if (!item.value || locked.value) return
  if (pendingOp.value) {
    toast.info(PENDING_REASON)
    return
  }
  itemNoText.value = String(item.value.item_no)
  itemNoError.value = ''
  itemNoEditing.value = true
}

async function submitItemNo() {
  const it = item.value
  if (!it || busy.value) return
  const n = Number(itemNoText.value.trim())
  if (!Number.isInteger(n) || n < 1 || n > MAX_ITEM_NO) {
    itemNoError.value = `件号要是 1–${MAX_ITEM_NO} 的整数`
    return
  }
  if (n === it.item_no) {
    itemNoEditing.value = false
    return
  }
  let current: ArchiveItem | null = it
  if (dirty.value) current = await save()
  if (!current) return
  busy.value = 'item-no'
  const sent = current.row_version
  try {
    const res = await updateArchiveItem(current.id, { row_version: sent, item_no: n }, true)
    applyFresh(res, { force: true })
    emit('changed', res)
    itemNoEditing.value = false
    toast.success(`已改为第 ${res.item_no} 件`, { description: res.archive_no ? `档号 ${res.archive_no}` : '档号为空，原因见页头' })
  } catch (e) {
    if (handledGlobally(e)) return
    if (isApiError(e, 'conflict')) {
      // 两种 409 conflict：件号被占用 / 档号撞号（换一个号），或者 row_version 过期（别人刚改过）。重新取一次来区分。
      const latest = await getArchiveItem(current.id, true).catch(() => null)
      if (latest && latest.row_version === sent) {
        itemNoError.value = (e as ApiError).message
        return
      }
      if (latest) applyFresh(latest, { force: true })
      itemNoError.value = '条目刚被别人修改过，已重新加载，请再试一次'
      return
    }
    itemNoError.value = itemOpErrorText(e)
    if (isApiError(e, 'not_found')) emit('gone', current.id)
    else if (isApiError(e, 'item_locked')) void load({ force: true })
  } finally {
    busy.value = ''
  }
}

// ---------- 移到别的节点 ----------

const pickerOpen = ref(false)

async function moveTo(target: PickedCategory) {
  const it = item.value
  if (!it || busy.value || target.id === it.category_id) return
  if (pendingOp.value) {
    toast.info(PENDING_REASON)
    return
  }
  let current: ArchiveItem | null = it
  if (dirty.value) current = await save()
  if (!current) return
  busy.value = 'move'
  const from = current.category_id
  try {
    const res = await updateArchiveItem(current.id, { row_version: current.row_version, category_id: target.id }, true)
    applyFresh(res, { force: true })
    emit('moved', res, from)
  } catch (e) {
    if (isApiError(e, 'not_found')) {
      // 404 可能是条目没了，也可能是目标节点刚被删掉：再取一次条目来区分（M3-5 审查）。
      const still = await getArchiveItem(current.id, true).catch(() => null)
      if (still) {
        applyFresh(still)
        toast.error('目标节点已不存在（可能刚被别人删除），条目没有移动', { description: '请重新选择目标节点' })
        return
      }
    }
    await onWriteError(e)
  } finally {
    busy.value = ''
  }
}

// ---------- 页序：挂接、重排、移出（M3-6，即时提交，理由见文件开头） ----------

const stripRef = ref<InstanceType<typeof PageStrip> | null>(null)
/** 页序条选中的页（按页序）。 */
const stripSelected = ref<string[]>([])

/**
 * 自己的写操作（页序、删除前的重读）成功之后用响应更新：页序的提交不改著录，表单有没保存的修改、而且是基于刚提交的
 * 那一版开始改的，就保留修改、基础版本跟着前进（不然会误报"别人刚修改了"）；否则同 applyFresh。
 */
function applyOwnPages(res: ArchiveItem) {
  // 表单有修改时不重置；著录内容和开始编辑时的一样（sameFields）就让基础版本跟着前进（见 applyFresh）。
  applyFresh(res, { force: !dirty.value })
}

/**
 * 提交一个页序动作：在现在的页序上算出整份新列表，PUT 一次。成功返回 true。没有变化（例如放回原处）不发请求。
 * 失败按错误码处理（见 onPagesError）。
 */
/** 一次移出这么多页以上（或者全部移出）先确认（M3-6 审查：Delete、Backspace 是立即提交的）。 */
const REMOVE_CONFIRM_PAGES = 5

async function applyPageOp(op: PageOp, opts: { confirmed?: boolean } = {}): Promise<boolean> {
  const it = item.value
  if (!it || locked.value || busy.value) return false
  const before = it.pages.map((p) => p.file_id)
  const r = applyOp(op, before)
  if (op.kind === 'insert' && r.skipped.length) {
    toast.info(`${r.skipped.length} 个文件已经在这个条目里，跳过了`)
  }
  if (!r.changed) {
    if (pendingOp.value === op) pendingOp.value = null
    return false
  }
  if (r.list.length > MAX_ITEM_PAGES) {
    toast.error(`一个条目最多 ${MAX_ITEM_PAGES} 页，这样会有 ${r.list.length} 页`, { description: '请少挂一些，或者分成几个条目' })
    return false
  }
  if (op.kind === 'remove' && !opts.confirmed && (r.affected.length >= REMOVE_CONFIRM_PAGES || r.list.length === 0)) {
    confirmRemove.op = op
    confirmRemove.count = r.affected.length
    confirmRemove.all = r.list.length === 0
    confirmRemove.open = true
    return false
  }
  busy.value = 'pages'
  const sent = it.row_version
  try {
    const res = await putArchiveItemFiles(it.id, sent, r.list, true)
    pendingOp.value = null
    applyOwnPages(res)
    emit('changed', res)
    if (op.kind === 'insert') emit('linked', r.affected)
    // 等页序条拿到新的页（下一拍）再选中刚挪、刚挂上的页并滚过去，接着操作（M3-6 审查）；移出的给"撤销"。
    await nextTick()
    if (op.kind === 'remove') {
      stripRef.value?.clear()
      toast.success(`已${op.label}`, {
        description: '文件本身没删，回到了"未归类"',
        action: { label: '撤销', onClick: () => void undoRemove(before, res.row_version, r.affected) },
      })
    } else stripRef.value?.select(r.affected)
    return true
  } catch (e) {
    await onPagesError(e, op)
    return false
  } finally {
    busy.value = ''
  }
}

// 移出很多页、全部移出前的确认
const confirmRemove = reactive({ open: false, op: null as PageOp | null, count: 0, all: false })
async function doConfirmRemove() {
  const op = confirmRemove.op
  confirmRemove.open = false
  if (op) await applyPageOp(op, { confirmed: true })
}

/**
 * 撤销移出：条目从那以后没再变过（row_version 还是移出后的那一版）时，用移出前的整份列表再 PUT 一次（带新的
 * row_version）；变过就不撤销（避免盖掉别人的修改）。
 */
async function undoRemove(before: string[], rowVersion: number, removed: string[]) {
  const it = item.value
  if (!it || busy.value || locked.value) return
  if (it.row_version !== rowVersion) {
    toast.warning('页序之后又改过了，不能撤销', { description: '要放回去，请从待归类池重新挂接' })
    return
  }
  busy.value = 'pages'
  try {
    const res = await putArchiveItemFiles(it.id, rowVersion, before, true)
    applyOwnPages(res)
    emit('changed', res)
    emit('linked', removed)
    toast.success('已撤销移出')
    await nextTick()
    stripRef.value?.select(removed)
  } catch (e) {
    if (handledGlobally(e)) return
    if (isApiError(e, 'conflict')) {
      toast.warning('页序刚被别人改过，不能撤销', { description: '要放回去，请从待归类池重新挂接' })
      void load({ force: !dirty.value })
    } else if (isApiError(e, 'file_cataloged')) {
      toast.warning('移出的文件已经挂到了别的条目里，不能撤销')
    } else toast.error(`没有撤销：${errorMessage(e)}`)
  } finally {
    busy.value = ''
  }
}

/** 页序动作的错误。 */
async function onPagesError(e: unknown, op: PageOp) {
  if (handledGlobally(e)) return
  if (isApiError(e, 'conflict')) {
    // 别人刚改过：读最新的，把这个动作作为预览重做在最新的页序上（用户确认后再提交）。
    const latest = await getArchiveItem(props.itemId, ['not_found']).catch((err) => {
      if (isApiError(err, 'not_found')) emit('gone', props.itemId)
      return null
    })
    if (!latest) return
    applyFresh(latest, { force: !dirty.value })
    if (latest.status === 'locked') {
      toast.warning('条目刚被别人定稿，页序不能再改', { description: `你刚才的操作没有保存：${op.label}` })
      return
    }
    pendingOp.value = op
    toast.warning('条目刚被别人修改过，你的页序操作没有保存', { description: '页序条里是把它重做在最新页序上的结果，确认后再保存' })
    return
  }
  if (isApiError(e, 'item_locked')) {
    toast.error('条目已定稿，页序不能再改', { description: `你刚才的操作没有保存：${op.label}` })
    void load({ force: !dirty.value })
    return
  }
  if (isApiError(e, 'file_cataloged') && op.kind === 'insert') {
    const d = (e as ApiError).details as FileCatalogedDetails | undefined
    if (d?.items?.length) {
      openCataloged(op, d.items)
      return
    }
  }
  if (isApiError(e, 'not_found')) {
    const ids = ((e as ApiError).details as { ids?: string[] } | undefined)?.ids
    if (ids?.length) {
      // 要挂的文件已删除或不在本项目：刷新待归类池；还有别的文件时可以只挂它们。
      emit('poolStale')
      const rest = op.kind === 'insert' ? op.files.filter((f) => !ids.includes(f.file_id)) : []
      toast.error(`有 ${ids.length} 个文件已不存在（可能刚被删除或移到了别的项目），没有挂接`, {
        description: '待归类池已刷新',
        action: rest.length && op.kind === 'insert'
          ? { label: `只挂其余的 ${rest.length} 个`, onClick: () => void applyPageOp({ ...op, files: rest, label: `${op.label}（去掉不存在的）` }) }
          : undefined,
      })
      return
    }
    toast.error(itemOpErrorText(e))
    emit('gone', props.itemId)
    return
  }
  toast.error(`页序没有保存：${errorMessage(e)}`)
}

// 已经挂在别的条目里（409 file_cataloged）
const cataloged = reactive({
  open: false,
  op: null as (PageOp & { kind: 'insert' }) | null,
  items: [] as CatalogedElsewhere[],
  names: {} as Record<string, string>,
})
function openCataloged(op: PageOp & { kind: 'insert' }, items: CatalogedElsewhere[]) {
  cataloged.op = op
  cataloged.items = items
  cataloged.names = Object.fromEntries(op.files.map((f) => [f.file_id, f.file_name]))
  cataloged.open = true
}

/** 从原条目移出之后：在最新的页序上重做原来的动作。 */
async function catalogedRetry() {
  const op = cataloged.op
  cataloged.open = false
  if (!op) return
  await load({ force: !dirty.value })
  await applyPageOp(op)
}

/** 跳过还在别的条目里的（对话框里已经移出的不算），在最新的页序上挂接其余的。 */
async function catalogedSkip(blocked: string[]) {
  const op = cataloged.op
  cataloged.open = false
  if (!op) return
  const bad = new Set(blocked)
  const files = op.files.filter((f) => !bad.has(f.file_id))
  if (!files.length) return
  await load({ force: !dirty.value })
  await applyPageOp({ ...op, files, label: `${op.label}（跳过还在别的条目里的）` })
}

/** 待归类池的"挂到末尾"、"插到选中页之后"（页面调）。成功返回 true。 */
function linkFiles(files: OpFile[], mode: 'end' | 'after'): Promise<boolean> {
  const it = item.value
  if (!it || !files.length) return Promise.resolve(false)
  const names = files.map((f) => f.file_name)
  let target: PageTarget = { at: 'end' }
  let where = '挂到末尾'
  if (mode === 'after') {
    const last = stripSelected.value[stripSelected.value.length - 1]
    const page = last ? it.pages.find((p) => p.file_id === last) : undefined
    if (!page) {
      toast.info('先在页序条里选一页，再"插到选中页之后"')
      return Promise.resolve(false)
    }
    // 备用下标是这一页现在的位置（0 起）：它要是被别人移出了，新文件就放在它原来的位置（M3-6 审查：原来多了一位）。
    target = { after: page.file_id, index: page.page_no - 1 }
    where = `插在 ${page.file_name} 后面`
  }
  return applyPageOp({ kind: 'insert', files, target, label: `挂上 ${filesText(names)}（${where}）` })
}

/** 从待归类池拖进页序条。 */
function onDropFiles(files: OpFile[], target: PageTarget) {
  const it = item.value
  if (!it) return
  const anchor = 'before' in target ? it.pages.find((p) => p.file_id === target.before) : undefined
  const where = anchor ? `插在 ${anchor.file_name} 前面` : '挂到末尾'
  void applyPageOp({ kind: 'insert', files, target, label: `挂上 ${filesText(files.map((f) => f.file_name))}（${where}）` })
}

/**
 * 冲突预览：把待确认的动作重做在最新的页序上。新挂上的标 new、挪动的标 moved；要移出的页留在原位、标 removed
 * （半透明，页号不算它们），让用户看清移出的是哪几页。
 */
const preview = computed(() => {
  const op = pendingOp.value
  const it = item.value
  if (!op || !it) return null
  const result = applyOp(op, it.pages.map((p) => p.file_id))
  const byId = new Map(it.pages.map((p) => [p.file_id, p]))
  if (op.kind === 'remove') {
    const removing = new Set(result.affected)
    let n = 0
    const pages: StripPage[] = it.pages.map((p) =>
      removing.has(p.file_id) ? { ...p, mark: 'removed' } : { ...p, page_no: ++n },
    )
    return { result, pages }
  }
  const added = new Map(op.kind === 'insert' ? op.files.map((f) => [f.file_id, f]) : [])
  const affected = new Set(result.affected)
  const pages: StripPage[] = result.list.map((id, i) => {
    const p = byId.get(id)
    if (p) return { ...p, page_no: i + 1, mark: affected.has(id) && op.kind === 'move' ? 'moved' : undefined }
    const f = added.get(id)!
    return {
      file_id: id,
      page_no: i + 1,
      file_name: f.file_name,
      folder_id: f.folder_id ?? '',
      folder_path: f.folder_path ?? '',
      review_status: f.review_status as StripPage['review_status'],
      review_round: f.review_round,
      version_no: f.version_no,
      new_version_after_lock: false,
      mark: 'new',
    }
  })
  return { result, pages }
})

async function redoPending() {
  const op = pendingOp.value
  if (!op) return
  // 移出很多页时第一次已经确认过了，这里不再问。
  const ok = await applyPageOp(op, { confirmed: true })
  if (ok && op.kind !== 'remove') toast.success('已在最新的页序上重做并保存')
}

function dropPending() {
  pendingOp.value = null
}

const stripPages = computed<StripPage[]>(() => preview.value?.pages ?? item.value?.pages ?? [])

/** 看原图：页序条里的页（按页序）当作一个文件夹给查看器左右切换。 */
function openViewer(fileId: string) {
  const files: FileEntry[] = stripPages.value.map((p) => ({
    folder_file_id: '',
    file_id: p.file_id,
    file_name: p.file_name,
    sort_no: p.page_no,
    review_status: p.review_status,
    review_round: p.review_round,
    version_no: p.version_no,
    cataloged: true,
    created_at: '',
  }))
  emit('openViewer', files, fileId)
}

// 页序条的状态告诉页面（待归类池的按钮）。
watch(
  () => {
    const it = item.value
    if (!it) return null
    const last = stripSelected.value[stripSelected.value.length - 1]
    const state: StripState = {
      itemId: it.id,
      label: it.archive_no ?? `第 ${formatItemNo(it.item_no, props.itemNoWidth)} 件`,
      locked: it.status === 'locked',
      busy: busy.value !== '',
      pending: !!pendingOp.value,
      pageCount: it.page_count,
      insertAfterPageNo: last ? (it.pages.find((p) => p.file_id === last)?.page_no ?? null) : null,
    }
    return JSON.stringify(state)
  },
  (s) => emit('stripState', s ? (JSON.parse(s) as StripState) : null),
  { immediate: true },
)

// ---------- 删除条目（M3-3 的接口；M3-5 没做界面） ----------

/**
 * 删除确认框：打开时记下 row_version 和页数，确认框写的是记下的页数，DELETE 带记下的 row_version（M3-6 审查：原来
 * 用的是确认时最新的版本，确认框打开期间条目被改了也照删）。期间条目被改了 server 返回 409 conflict：重新读取，
 * 确认框不关，写明"条目刚被修改（现在 n 页）"，请用户重新确认。
 */
const deleteAsk = reactive({ open: false, rowVersion: 0, pageCount: 0, changed: null as string | null })

function askDelete() {
  const it = item.value
  if (!it || busy.value || locked.value) return
  if (pendingOp.value) {
    toast.info(PENDING_REASON)
    return
  }
  deleteAsk.rowVersion = it.row_version
  deleteAsk.pageCount = it.page_count
  deleteAsk.changed = null
  deleteAsk.open = true
}

async function doDelete() {
  const it = item.value
  if (!it || busy.value || locked.value) return
  busy.value = 'delete'
  try {
    await deleteArchiveItem(it.id, deleteAsk.rowVersion, true)
    deleteAsk.open = false
    pendingOp.value = null
    resetForm(it)
    emit('deleted', it)
  } catch (e) {
    if (handledGlobally(e)) {
      deleteAsk.open = false
      return
    }
    if (isApiError(e, 'conflict')) {
      // 确认框打开期间条目被改了（可能挂接、移出了页）：重新读，不关确认框，请用户看清页数再确认。
      const latest = await getArchiveItem(it.id, ['not_found']).catch(() => null)
      if (!latest) {
        deleteAsk.open = false
        emit('gone', it.id)
        return
      }
      applyFresh(latest, { force: !dirty.value })
      if (latest.status === 'locked') {
        deleteAsk.open = false
        toast.error('条目刚被定稿，不能删除', { description: '要删除请先由项目负责人解锁' })
        return
      }
      deleteAsk.changed = `条目刚被修改了（现在 ${latest.page_count} 页，打开确认框时 ${deleteAsk.pageCount} 页），没有删除。请看清后重新确认。`
      deleteAsk.rowVersion = latest.row_version
      deleteAsk.pageCount = latest.page_count
      return
    }
    deleteAsk.open = false
    if (isApiError(e, 'item_locked')) {
      toast.error('条目已定稿，不能删除', { description: '要删除请先由项目负责人解锁' })
      void load({ force: !dirty.value })
    } else if (isApiError(e, 'not_found')) {
      toast.info('条目已经不在了（可能刚被别人删除）')
      emit('gone', it.id)
    } else toast.error(`删除失败：${errorMessage(e)}`)
  } finally {
    busy.value = ''
  }
}

// ---------- 显示 ----------

// 著录项收起（M3-6：挂接时给页序条腾地方）：只留题名；记在 localStorage（个人习惯，读写失败就展开）。
const FORM_COLLAPSED_KEY = 'doclite.catalog.form-collapsed'
function readCollapsed(): boolean {
  try {
    return window.localStorage.getItem(FORM_COLLAPSED_KEY) === '1'
  } catch {
    return false
  }
}
const formCollapsed = ref(readCollapsed())
watch(formCollapsed, (v) => {
  try {
    window.localStorage.setItem(FORM_COLLAPSED_KEY, v ? '1' : '0')
  } catch {
    // 记不住就算了。
  }
})
// 收起着的项有错误（本地校验、server 的 400）：展开，让用户看得到。
watch(errors, (e) => {
  if (formCollapsed.value && Object.keys(e).some((k) => k !== TITLE_KEY)) formCollapsed.value = false
})
/** 收起时的一行摘要：各项的值（空的不写）。 */
const collapsedSummary = computed(() =>
  defs.value
    .map((d) => [d.label, (values[d.key] ?? '').trim()] as const)
    .filter(([, v]) => v)
    .map(([l, v]) => `${l}：${v}`)
    .join(' · '),
)

const reason = computed(() => item.value?.archive_no_reason ?? null)
const statusInfo = computed(() => itemStatusOf(status.value))

function fieldError(key: string): string | undefined {
  return errors.value[key]
}

/** 库里存的 enum 值不在现在的选项里（旧值）：一直留在下拉里，改走以后还能改回来。 */
function oldEnumValue(d: ItemFieldDef): string | null {
  const v = initial.value[d.key] ?? ''
  return v && !d.options.includes(v) ? v : null
}

function isLong(d: ItemFieldDef): boolean {
  return d.type === 'text' && (d.max_length ?? 500) > 200
}

function clearOrphan(key: string) {
  cleared.value = new Set([...cleared.value, key])
}
function keepOrphan(key: string) {
  const next = new Set(cleared.value)
  next.delete(key)
  cleared.value = next
}

function onInput(key: string) {
  if (errors.value[key]) {
    const next = { ...errors.value }
    delete next[key]
    errors.value = next
  }
}

const inputCls =
  'h-8 w-full min-w-0 rounded-md border border-input bg-background px-2 text-[13px] text-foreground shadow-xs outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:bg-muted/50 disabled:text-foreground aria-invalid:border-destructive'

defineExpose({
  /** 有没保存的修改：著录表单，或者 409 之后待确认的页序动作。 */
  isDirty: () => unsaved.value,
  /** 没保存的是什么（确认框的说法）。 */
  unsavedWhat: (): 'form' | 'pages' | 'both' | null =>
    dirty.value && pendingOp.value ? 'both' : dirty.value ? 'form' : pendingOp.value ? 'pages' : null,
  save,
  discard: () => {
    pendingOp.value = null
    if (item.value) resetForm(item.value)
  },
  /** 重新取详情（页面的实时事件用，可以 await）。 */
  refresh: () => load(),
  /** 待归类池的"挂到末尾"、"插到选中页之后"。 */
  linkFiles,
})
</script>

<template>
  <div
    class="flex min-h-0 flex-1 flex-col"
    data-testid="item-detail"
    :data-item-id="props.itemId"
    :data-status="item?.status"
    :data-row-version="item?.row_version"
    :data-dirty="dirty ? 'true' : undefined"
    :data-load="loadState"
    :data-busy="busy || undefined"
    :data-pending-op="pendingOp ? pendingOp.kind : undefined"
  >
    <div v-if="loadState === 'loading' && !item" class="flex flex-1 items-center justify-center gap-2 text-sm text-muted-foreground">
      <LoaderCircleIcon class="size-4 animate-spin" />正在加载条目…
    </div>
    <div v-else-if="loadState === 'error' && !item" class="flex flex-1 flex-col items-center justify-center gap-3 text-sm">
      <p class="text-destructive">{{ loadError || '加载失败' }}</p>
      <Button variant="outline" size="sm" @click="load({ force: true })">重试</Button>
    </div>

    <template v-else-if="item">
      <!-- 页头：档号、状态、件号、位置 -->
      <div class="shrink-0 border-b px-5 pt-3 pb-3">
        <div class="flex items-start gap-3">
          <div class="min-w-0 flex-1">
            <div class="text-[11px] text-muted-foreground">档号（按模板自动生成）</div>
            <div
              v-if="item.archive_no"
              class="truncate font-mono text-lg font-semibold tracking-tight text-foreground"
              data-testid="item-archive-no"
              :title="item.archive_no"
            >{{ item.archive_no }}</div>
            <div
              v-else
              class="flex items-center gap-1.5 text-base font-medium text-warn"
              data-testid="item-archive-no-empty"
              :data-code="reason?.code ?? ''"
            >
              <CircleAlertIcon class="size-4" />档号为空 · {{ archiveNoReasonShort(reason) }}
            </div>
          </div>
          <Button
            v-if="props.canExport"
            size="xs"
            variant="outline"
            class="mt-1 shrink-0"
            title="带着这个条目跳到导出页的新建导出（先预检）"
            data-testid="item-export"
            @click="emit('export', item.id)"
          >
            <FileOutputIcon />导出这件…
          </Button>
          <span
            class="mt-1 shrink-0 rounded-full px-2.5 text-xs leading-6 font-medium"
            :class="statusInfo.badge"
            data-testid="item-status"
          >{{ statusInfo.label }}</span>
        </div>

        <div class="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
          <span class="flex items-center gap-1">
            <template v-if="!itemNoEditing">
              件号 <span class="font-mono font-medium text-foreground" data-testid="item-no">{{ formatItemNo(item.item_no, props.itemNoWidth) }}</span>
              <button
                v-if="!locked"
                type="button"
                class="rounded p-0.5 hover:bg-muted hover:text-foreground"
                title="改件号"
                data-testid="item-no-edit"
                @click="startItemNo"
              ><PencilIcon class="size-3" /></button>
            </template>
            <form v-else class="flex items-center gap-1" @submit.prevent="submitItemNo">
              件号
              <input
                v-model="itemNoText"
                class="h-6 w-20 rounded border border-input bg-background px-1.5 font-mono text-xs text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                inputmode="numeric"
                data-testid="item-no-input"
                @keydown.esc.prevent="itemNoEditing = false"
              >
              <Button type="submit" size="xs" :disabled="busy !== ''" data-testid="item-no-submit">改</Button>
              <Button type="button" size="xs" variant="ghost" @click="itemNoEditing = false">取消</Button>
            </form>
          </span>
          <span
            class="min-w-0 truncate"
            :title="item.category_path"
            data-testid="item-category-path"
            :data-path="item.category_path"
          >{{ props.categoryLabel || item.category_path.split('/').slice(1).join(' / ') }}</span>
          <span>{{ item.page_count }} 页</span>
          <span>第 {{ item.row_version }} 版 · {{ formatDateTime(item.updated_at) }}</span>
          <span v-if="item.locked_at">定稿于 {{ formatDateTime(item.locked_at) }}</span>
        </div>
        <p v-if="itemNoError" class="mt-1.5 rounded-md bg-destructive-soft px-2.5 py-1.5 text-xs text-destructive" data-testid="item-no-error">
          {{ itemNoError }}
        </p>

        <!-- 档号为空的原因 -->
        <div
          v-if="!item.archive_no && reason"
          class="mt-2 flex items-start gap-2 rounded-md bg-warn-soft px-3 py-2 text-xs text-warn"
          data-testid="archive-no-reason"
          :data-code="reason.code"
        >
          <div class="min-w-0 flex-1">
            <p>{{ reason.message }}</p>
            <p v-if="reason.code === 'incomplete'" class="mt-0.5 text-warn/80">在归类树里补上这几级节点，或者把条目移到层级完整的节点下；档号为空的条目可以著录，不能定稿。</p>
            <p v-else-if="reason.code === 'template_invalid'" class="mt-0.5 text-warn/80">请到「系统管理 › 项目」里修改档号模板。</p>
          </div>
          <Button
            v-if="reason.code === 'conflict' && reason.conflict_item_id && reason.conflict_category_id"
            size="xs"
            variant="outline"
            class="shrink-0"
            data-testid="archive-no-conflict-open"
            @click="emit('openItem', reason.conflict_category_id!, reason.conflict_item_id!)"
          >
            <ExternalLinkIcon />查看占用的条目（第 {{ reason.conflict_item_no }} 件）
          </Button>
          <Button
            v-else-if="reason.code === 'not_generated'"
            size="xs"
            variant="outline"
            class="shrink-0"
            data-testid="archive-no-regenerate"
            @click="emit('renumber', item.category_id)"
          >
            <RefreshCwIcon />按新模板重新生成
          </Button>
        </div>

        <div
          v-if="locked"
          class="mt-2 flex items-center gap-2 rounded-md bg-ok-soft px-3 py-2 text-xs text-ok"
          data-testid="item-locked-note"
        >
          <LockIcon class="size-3.5 shrink-0" />
          <span class="min-w-0 flex-1">
            已定稿：著录、页序、档号都已冻结，不能移动、改件号、删除。{{ props.canUnlock ? '要修改先解锁。' : '要修改请联系项目负责人解锁。' }}
            <span v-if="item.new_version_after_lock" class="ml-1 font-medium text-info" data-testid="item-new-version-after-lock">
              定稿之后有页重扫出了新版本（页序条里标出）。
            </span>
          </span>
        </div>
        <div
          v-if="remoteChanged"
          class="mt-2 flex items-center gap-2 rounded-md bg-info-soft px-3 py-2 text-xs text-info"
          data-testid="item-remote-changed"
        >
          <span class="min-w-0 flex-1">别人刚修改了这个条目（现在是第 {{ item.row_version }} 版）。保存时会提示冲突，你的修改不会丢。</span>
          <Button size="xs" variant="outline" class="shrink-0" @click="mergeAfterConflict">载入最新（保留我的修改）</Button>
        </div>
        <div
          v-if="mergeNote"
          class="mt-2 rounded-md bg-warn-soft px-3 py-2 text-xs text-warn"
          role="alert"
          data-testid="item-merge-note"
        >{{ mergeNote }}</div>
      </div>

      <!-- 著录表单 -->
      <form
        class="relative max-h-[48%] shrink-0 overflow-y-auto border-b px-5 py-3"
        data-testid="item-form"
        :data-collapsed="formCollapsed ? 'true' : undefined"
        @submit.prevent="save"
      >
        <!-- 挂接时给页序条腾地方（M3-6）：只留题名，其他著录项收起（v-show，表单项还在） -->
        <button
          v-if="defs.length || orphanKeys.length"
          type="button"
          class="absolute top-2.5 right-5 z-10 flex items-center gap-0.5 rounded px-1.5 text-[11px] leading-5 text-muted-foreground hover:bg-muted hover:text-foreground"
          :title="formCollapsed ? '展开全部著录项' : '只留题名，给页序条腾地方'"
          data-testid="item-form-toggle"
          @click="formCollapsed = !formCollapsed"
        >
          <template v-if="formCollapsed">
            展开著录项（{{ defs.length + orphanKeys.length }}）<ChevronDownIcon class="size-3" />
          </template>
          <template v-else>收起著录项<ChevronUpIcon class="size-3" /></template>
        </button>
        <div class="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-x-4 gap-y-2.5">
          <!-- 题名（固定列） -->
          <label
            class="col-span-full grid gap-1"
            data-testid="item-field"
            :data-key="TITLE_KEY"
            :data-error="fieldError(TITLE_KEY) ?? undefined"
          >
            <span class="text-xs font-medium text-muted-foreground">题名 <span class="text-destructive">*</span></span>
            <input
              v-model="title"
              :class="inputCls"
              :disabled="locked"
              :aria-invalid="!!fieldError(TITLE_KEY) || undefined"
              placeholder="（未填题名）"
              data-testid="item-title"
              @input="onInput(TITLE_KEY)"
            >
            <span v-if="fieldError(TITLE_KEY)" class="text-[11px] text-destructive" data-testid="field-error">{{ fieldError(TITLE_KEY) }}</span>
            <span v-if="theirs[TITLE_KEY] !== undefined" class="text-[11px] text-warn">别人改成了：{{ theirs[TITLE_KEY] || '（空）' }}</span>
          </label>

          <!-- 设置里的著录项 -->
          <label
            v-for="d in defs"
            v-show="!formCollapsed"
            :key="d.key"
            class="grid content-start gap-1"
            :class="isLong(d) && 'col-span-2'"
            data-testid="item-field"
            :data-key="d.key"
            :data-type="d.type"
            :data-error="fieldError(d.key) ?? undefined"
            :data-issue="issues.get(d.key) ?? undefined"
          >
            <span class="truncate text-xs font-medium text-muted-foreground" :title="d.key">
              {{ d.label }}<span v-if="d.required" class="text-destructive"> *</span>
            </span>
            <select
              v-if="d.type === 'enum'"
              v-model="values[d.key]"
              :class="inputCls"
              :disabled="locked"
              :aria-invalid="!!fieldError(d.key) || undefined"
              data-testid="item-field-input"
              @change="onInput(d.key)"
            >
              <option value="">（不填）</option>
              <option v-if="oldEnumValue(d)" :value="oldEnumValue(d)!">{{ oldEnumValue(d) }}（旧值，不在选项里）</option>
              <option v-for="o in d.options" :key="o" :value="o">{{ o }}</option>
            </select>
            <Textarea
              v-else-if="isLong(d)"
              v-model="values[d.key]"
              class="min-h-14 px-2 py-1 text-[13px] md:text-[13px]"
              rows="2"
              :disabled="locked"
              :aria-invalid="!!fieldError(d.key) || undefined"
              data-testid="item-field-input"
              @input="onInput(d.key)"
            />
            <input
              v-else
              v-model="values[d.key]"
              :class="[inputCls, d.type === 'date' && 'font-mono']"
              :disabled="locked"
              :aria-invalid="!!fieldError(d.key) || undefined"
              :placeholder="d.type === 'date' ? 'YYYY、YYYY-MM 或 YYYY-MM-DD' : ''"
              data-testid="item-field-input"
              @input="onInput(d.key)"
            >
            <span v-if="fieldError(d.key)" class="text-[11px] text-destructive" data-testid="field-error">{{ fieldError(d.key) }}</span>
            <span
              v-else-if="issues.get(d.key) && (values[d.key] ?? '').trim() === (initial[d.key] ?? '').trim()"
              class="text-[11px] text-warn"
              data-testid="field-issue"
            >旧值：{{ issues.get(d.key) }}。不改它照样能保存，标记完成、定稿前要改掉。</span>
            <span v-if="theirs[d.key] !== undefined" class="text-[11px] text-warn">别人改成了：{{ theirs[d.key] || '（空）' }}</span>
          </label>
        </div>

        <!-- 设置里已经没有的著录项（旧值） -->
        <div
          v-if="orphanKeys.length"
          v-show="!formCollapsed"
          class="mt-3 rounded-md border border-dashed px-3 py-2"
          data-testid="item-orphan-fields"
        >
          <div class="text-xs font-medium text-muted-foreground">项目设置里已经删掉的著录项（原值保留，保存时不会丢；也可以清空）</div>
          <div
            v-for="k in orphanKeys"
            :key="k"
            class="mt-1.5 flex items-center gap-2 text-[13px]"
            data-testid="item-orphan-field"
            :data-key="k"
            :data-cleared="cleared.has(k) ? 'true' : undefined"
          >
            <span class="w-28 shrink-0 truncate font-mono text-xs text-muted-foreground" :title="k">{{ k }}</span>
            <span class="min-w-0 flex-1 truncate" :class="cleared.has(k) ? 'text-muted-foreground line-through' : 'text-foreground'">
              {{ initial[k] || '（空）' }}
            </span>
            <template v-if="!locked">
              <Button v-if="!cleared.has(k)" type="button" size="xs" variant="ghost" @click="clearOrphan(k)">清空</Button>
              <Button v-else type="button" size="xs" variant="ghost" @click="keepOrphan(k)">保留</Button>
            </template>
          </div>
        </div>
        <p v-if="!defs.length" class="mt-2 text-xs text-muted-foreground">
          项目还没有设置其他著录项（在「系统管理 › 项目」的"著录项和档号"里设置）。
        </p>
        <p v-else-if="formCollapsed && collapsedSummary" class="mt-1.5 truncate text-xs text-muted-foreground" data-testid="item-form-summary">
          {{ collapsedSummary }}
        </p>
      </form>

      <!-- 页序条（M3-6）：挂接、拖动排序、移出，即时提交 -->
      <PageStrip
        ref="stripRef"
        :item-id="item.id"
        :pages="stripPages"
        :counts="item.page_counts"
        :locked="locked"
        :busy="busy !== ''"
        :preview="!!pendingOp"
        @op="applyPageOp"
        @drop-files="onDropFiles"
        @open="openViewer"
        @selection="(ids) => (stripSelected = ids)"
      >
        <template #banner>
          <div
            v-if="pendingOp && preview"
            class="flex shrink-0 flex-wrap items-center gap-2 border-b bg-warn-soft px-4 py-2 text-xs text-warn"
            role="alert"
            data-testid="page-conflict"
            :data-changed="preview.result.changed ? 'true' : 'false'"
          >
            <div class="min-w-0 flex-1">
              <p>
                别人在你操作时修改了这个条目（现在是第 {{ item.row_version }} 版），你刚才的操作没有保存：<b>{{ pendingOp.label }}</b>。
              </p>
              <p v-if="preview.result.changed" class="mt-0.5 text-warn/85">
                下面是把它重做在最新页序上的结果（还没保存；绿框是要挂上的，黄框是要挪动的，半透明的是要移出的）。
              </p>
              <p v-else class="mt-0.5 text-warn/85">在最新的页序上，这个操作已经不需要了（涉及的页已经不在或已经是这个顺序）。</p>
            </div>
            <Button
              v-if="preview.result.changed"
              size="xs"
              :disabled="busy !== ''"
              data-testid="page-conflict-redo"
              @click="redoPending"
            >
              <LoaderCircleIcon v-if="busy === 'pages'" class="animate-spin" />保存这个页序
            </Button>
            <Button size="xs" variant="outline" :disabled="busy !== ''" data-testid="page-conflict-discard" @click="dropPending">
              {{ preview.result.changed ? '放弃，看最新的' : '知道了' }}
            </Button>
          </div>
        </template>
      </PageStrip>

      <!-- 动作 -->
      <div v-if="!locked || props.canUnlock" class="flex shrink-0 flex-wrap items-center gap-2 border-t px-5 py-2.5" data-testid="item-actions">
        <Button
          v-if="!locked"
          size="sm"
          :variant="dirty ? 'default' : 'outline'"
          :disabled="!dirty || busy !== ''"
          data-testid="item-save"
          @click="save"
        >
          <LoaderCircleIcon v-if="busy === 'save'" class="animate-spin" /><SaveIcon v-else />保存著录
        </Button>
        <Button
          v-if="status === 'draft'"
          size="sm"
          variant="outline"
          :disabled="actionsBlocked"
          :title="pendingOp ? PENDING_REASON : undefined"
          data-testid="item-mark-done"
          @click="act('mark-done')"
        >
          <CheckCheckIcon />标记完成
        </Button>
        <Button
          v-if="status === 'done'"
          size="sm"
          variant="outline"
          :disabled="actionsBlocked"
          :title="pendingOp ? PENDING_REASON : undefined"
          data-testid="item-back-to-draft"
          @click="act('back-to-draft')"
        >
          <Undo2Icon />退回草稿
        </Button>
        <GuardedButton
          v-if="status !== 'locked'"
          size="sm"
          :reason="
            pendingOp
              ? PENDING_REASON
              : status === 'done' && lockBlockers.length
                ? lockBlockers.join('；')
                : status !== 'done'
                  ? '要先标记完成'
                  : null
          "
          :disabled="actionsBlocked"
          data-testid="item-lock"
          @click="confirmLock = true"
        >
          <LockIcon />定稿
        </GuardedButton>
        <Button
          v-if="locked && props.canUnlock"
          size="sm"
          variant="outline"
          :disabled="actionsBlocked"
          data-testid="item-unlock"
          @click="confirmUnlock = true"
        >
          <LockOpenIcon />解锁
        </Button>
        <Button
          v-if="!locked"
          size="sm"
          variant="ghost"
          :disabled="actionsBlocked"
          :title="pendingOp ? PENDING_REASON : undefined"
          data-testid="item-move"
          @click="pickerOpen = true"
        >
          <ArrowRightLeftIcon />移到别的节点…
        </Button>
        <Button
          v-if="!locked"
          size="sm"
          variant="ghost"
          class="text-destructive hover:text-destructive"
          :disabled="actionsBlocked"
          :title="pendingOp ? PENDING_REASON : '删除这个条目（未定稿的才能删）'"
          data-testid="item-delete"
          @click="askDelete"
        >
          <Trash2Icon />删除…
        </Button>
        <span
          v-if="status === 'done' && lockBlockers.length"
          class="min-w-0 flex-1 truncate text-[11px] text-muted-foreground"
          data-testid="item-lock-blockers"
          :title="lockBlockers.join('；')"
        >定稿还差：{{ lockBlockers.join('；') }}</span>
        <span v-else-if="dirty" class="text-[11px] text-muted-foreground">有未保存的修改（Ctrl 或 ⌘ + S 保存）</span>
      </div>

      <ConfirmDialog
        v-model:open="confirmLock"
        title="定稿这个条目？"
        :description="`${item.archive_no ?? ''} 定稿后，著录、页序、档号都冻结，不能移动、删除；只有项目负责人能解锁。`"
        confirm-text="定稿"
        :loading="busy === 'lock'"
        @confirm="doLock"
      />
      <ConfirmDialog
        v-model:open="confirmUnlock"
        title="解锁这个条目？"
        :description="`${item.archive_no ?? `第 ${item.item_no} 件`} 解锁后回到「已完成」，著录、页序可以再改；要导出时需要重新定稿。`"
        confirm-text="解锁"
        :loading="busy === 'unlock'"
        @confirm="doUnlock"
      />
      <ConfirmDialog
        v-model:open="deleteAsk.open"
        :title="`删除${item.archive_no ?? `第 ${formatItemNo(item.item_no, props.itemNoWidth)} 件`}${item.title ? `「${item.title}」` : ''}？`"
        :description="
          (deleteAsk.pageCount > 0
            ? `条目里的 ${deleteAsk.pageCount} 页会一并移出（文件本身不删，回到“未归类”，每页记一条移出日志）。`
            : '条目里没有页。') +
          `件号 ${item.item_no} 删除后不再使用（不回收）。` +
          (dirty ? '没有保存的著录修改也会丢掉。' : '')
        "
        confirm-text="删除"
        destructive
        :loading="busy === 'delete'"
        @confirm="doDelete"
      >
        <p
          v-if="deleteAsk.changed"
          class="rounded-md bg-destructive-soft px-3 py-2 text-xs text-destructive"
          role="alert"
          data-testid="item-delete-changed"
        >{{ deleteAsk.changed }}</p>
        <p
          v-if="deleteAsk.pageCount > 0"
          class="rounded-md bg-warn-soft px-3 py-2 text-xs text-warn"
          data-testid="item-delete-pages"
          :data-count="deleteAsk.pageCount"
        >将移出 {{ deleteAsk.pageCount }} 页</p>
      </ConfirmDialog>
      <ConfirmDialog
        v-model:open="confirmRemove.open"
        :title="confirmRemove.all ? `移出全部 ${confirmRemove.count} 页？` : `移出 ${confirmRemove.count} 页？`"
        :description="`${confirmRemove.op?.label ?? ''}。文件本身不删，回到“未归类”；移出后可以在提示里撤销。`"
        confirm-text="移出"
        destructive
        :loading="busy === 'pages'"
        @confirm="doConfirmRemove"
      >
        <p class="hidden" data-testid="page-remove-confirm" :data-count="confirmRemove.count" />
      </ConfirmDialog>
      <FileCatalogedDialog
        v-model:open="cataloged.open"
        :items="cataloged.items"
        :names="cataloged.names"
        :total="cataloged.op?.files.length ?? 0"
        :action="cataloged.op ? `“${cataloged.op.label}”` : '这次挂接'"
        :item-no-width="props.itemNoWidth"
        @retry="catalogedRetry"
        @skip="catalogedSkip"
      />
      <CategoryPicker
        v-model:open="pickerOpen"
        :project-id="props.projectId"
        :initial-id="item.category_id"
        :disabled-ids="[item.category_id]"
        title="把条目移到别的归类节点"
        :description="`件号取目标节点的最大件号 + 1，档号按新位置重新生成（撞号时为空）。`"
        confirm-text="移到这里"
        @confirm="moveTo"
      />
    </template>
  </div>
</template>
