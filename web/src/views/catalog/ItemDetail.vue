<script setup lang="ts">
// 归类页右栏的条目详情（M3-5；M9 改成整个文件夹归档，Mockup v3.1 的件详情）：档号、状态、著录表单、页（只读，来自来源
// 文件夹）和动作。在件列表里双击打开，"‹ 返回"回到列表。
//
// - **档号只读**，按模板自动生成；为空时显示原因（`archive_no_reason`）：缺级、撞号（可以跳到占用的条目）、未生成
//   （"按新模板重新生成"）、模板不对。件号未锁定时可以改（撞号、被占用 409 `conflict`，提示换一个号）。
// - **封存横条**（M9）：已封存（紫色）写来源文件夹、"页来自文件夹，这里不能改页"和"解除封存去修改…"；修改中（青色）写
//   谁、何时、原因，"去文件目录"（左栏打开来源文件夹）和"重新归档…"。解除封存、重新归档的对话框在页面里
//   （`unseal`、`reseal` 事件）。
// - **著录表单**按 catalog-settings 的 item_fields 动态生成：text（长的用多行框）、date、enum（下拉）。旧值（Q1）：库里
//   的值和现在的设置对不上（`field_issues`）时照样显示原值和提示；设置里已经删掉的著录项单独列出，只能保留或清空。保存时
//   **只提交改过的项**（PATCH 的 extra_fields 按 key 合并）。必填在锁定时才查（M9）。
// - **乐观锁**：保存、状态动作都带 row_version。409 `conflict` 时重新加载最新版本：没改过的项换成最新的值，改过的项保留，
//   两边都改了的项标出"别人改成了…"。SSE 告诉页面条目变了时，表单没改动就直接换成最新的；有改动就只提示。
// - **动作**（requirements 5.9 的状态 × 动作表）：锁定（修改中、没页、没档号、要求全部通过而没通过时置灰并写明原因；
//   必填项没填 server 400 `invalid_input` 标到对应的项）、解锁、移到别的节点、改件号、解除封存、撤销归档（DELETE）、
//   导出这件（有 export 菜单）。有未保存的修改时先保存再做。锁定、解锁、撤销归档都要确认。
// - 已锁定：表单只读、不能移动、改件号、撤销归档；锁定后有页重扫出新版本时标出。
// - 截图脚本用的属性：根 data-testid=item-detail（data-item-id、data-status、data-source、data-row-version、data-dirty、
//   data-load、data-busy）；档号 item-archive-no / item-archive-no-empty（data-code）；路径 item-category-path；表单项
//   data-testid=item-field（data-key、data-error、data-issue）；按钮 item-back、item-save、item-lock、item-unlock、
//   item-move、item-no-edit、item-unseal、item-reseal、item-open-folder、item-revoke、item-export；页 item-pages（data-count）。
import {
  ArrowRightLeftIcon,
  ChevronLeftIcon,
  CircleAlertIcon,
  ExternalLinkIcon,
  FileOutputIcon,
  LoaderCircleIcon,
  LockIcon,
  LockOpenIcon,
  PencilIcon,
  RefreshCwIcon,
  SaveIcon,
  Undo2Icon,
} from '@lucide/vue'
import { useEventListener } from '@vueuse/core'
import { computed, reactive, ref, watch } from 'vue'
import { toast } from 'vue-sonner'

import { deleteArchiveItem, getArchiveItem, type ItemAction, itemAction, updateArchiveItem } from '@/api/catalog'
import { ApiError, errorMessage, HANDLE_ALL, handledGlobally, isApiError } from '@/api/http'
import type { ArchiveItem, CatalogSettings, FieldIssue, FileEntry, ItemFieldDef, UpdateArchiveItemBody } from '@/api/types'
import CategoryPicker, { type PickedCategory } from '@/components/catalog/CategoryPicker.vue'
import StateBanner from '@/components/catalog/StateBanner.vue'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import GuardedButton from '@/components/app/GuardedButton.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { StatusBadge } from '@/components/ui/status-badge'
import { useSelection } from '@/composables/useSelection'
import {
  ARCHIVE_STATE,
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
import { type Entry, fileKey } from '@/components/files/entries'
import FileGrid from '@/components/files/FileGrid.vue'

import { itemOpErrorText } from './catalog-errors'
import ItemFieldsForm from './ItemFieldsForm.vue'

const props = defineProps<{
  itemId: string
  projectId: string
  settings: CatalogSettings | null
  /** 件号补零的位数。 */
  itemNoWidth: number
  /** 页面收到这个条目的事件（或要整体刷新）时加一，重新取详情。 */
  refreshToken: number
  /** 所在节点从顶层起的名称路径（页面的树上知道时给出；名称里可能有 /，比拆 category_path 准）。 */
  categoryLabel?: string | null
  /** 显示"导出这件"（有 export 菜单）。 */
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
  /** 撤销归档了（自己做的）。 */
  deleted: [item: ArchiveItem]
  /** 看原图（页面打开 OriginalViewerDialog）。 */
  openViewer: [files: FileEntry[], fileId: string]
  /** 导出这个条目（页面带着范围跳到 /export 的新建导出）。 */
  export: [itemId: string]
  /** 回到件列表。 */
  back: []
  /** 解除封存、重新归档（页面打开对话框；label 是"办公室 0003"）。 */
  unseal: [item: ArchiveItem, label: string]
  reseal: [item: ArchiveItem, label: string]
  /** 左栏打开来源文件夹。 */
  openFolder: [folderId: string]
}>()

// ---------- 状态 ----------

/** server 最新的条目（显示档号、状态、页都用它）。 */
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
const busy = ref<'' | 'save' | ItemAction | 'move' | 'item-no' | 'delete'>('')

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
watch(dirty, (d) => emit('dirty', d), { immediate: true })

/** 别人在你编辑期间改了这个条目（SSE 拿到的最新版本比表单开始编辑的版本新）。 */
const remoteChanged = computed(
  () =>
    !!item.value &&
    !!base.value &&
    dirty.value &&
    item.value.row_version !== base.value.row_version &&
    !sameFields(item.value, base.value),
)

/** 条目的题名、著录项和表单开始编辑时的一样（别人只动了状态、页、件号这些）：表单的修改照样能直接套在新版本上。 */
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
 * 别人把它锁定了：表单不能再改，未保存的修改作废（提示一次）。
 */
function applyFresh(it: ArchiveItem, opts: { force?: boolean } = {}) {
  // 慢回来的旧结果（比现在显示的版本旧，例如保存之前发出的 GET）不要盖掉刚保存的。版本相同照样用：页的审核状态、
  // 版本号变了不改 row_version。
  if (!opts.force && item.value && item.value.id === it.id && it.row_version < item.value.row_version) return
  const lostEdits = !opts.force && dirty.value && it.status === 'locked'
  item.value = it
  if (lostEdits) toast.warning('这一件刚被别人锁定，你没有保存的修改已作废', { description: '要修改请先解锁' })
  if (opts.force || lostEdits || !dirty.value || !base.value) {
    resetForm(it)
    mergeNote.value = null
  } else if (it.row_version > base.value.row_version && it.status !== 'locked' && sameFields(it, base.value)) {
    // 别人只动了状态、页这些（著录还是开始编辑时的样子）：修改留在表单里，基础版本跟着前进。
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
    void load({ force: true })
  },
  { immediate: true },
)
watch(
  () => props.refreshToken,
  () => void load(),
)
// 设置变了（重新取到 catalog-settings）：没改动时按新设置重排表单。保留各项上的错误标记：锁定被
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

/** 必填（含题名）为空的（锁定前查；M9 起必填在锁定时才查）。 */
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
    // 别人把它锁定了：修改保存不了，直接换成最新的。
    applyFresh(latest, { force: true })
    toast.warning('这一件已被别人锁定，你没有保存的修改已作废', { description: '要修改请先解锁' })
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
      saved = await updateArchiveItem(it.id, body, HANDLE_ALL)
    } catch (e) {
      if (!isApiError(e, 'conflict')) throw e
      // row_version 过期：读最新的。著录还是开始编辑时的样子（别人只动了页、状态这些，没有锁定）就用新的
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
      saved = await updateArchiveItem(it.id, { ...body, row_version: latest.row_version }, HANDLE_ALL)
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
  // item_locked、invalid_state（状态刚变了、锁定的前置条件不满足）：前提已经变了，重新取一次。
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

/** 锁定前在本地预判还差什么（requirements 5.9"锁定前置"）；最终以 server 为准。 */
const lockBlockers = computed(() => {
  const it = item.value
  if (!it) return []
  const out: string[] = []
  if (it.source_status === 'modifying') out.push('修改中，先重新归档')
  if (it.page_count === 0) out.push('没有页')
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
  if (action === 'lock') {
    const missing = { ...validateChanged(), ...missingRequired() }
    if (Object.keys(missing).length) {
      errors.value = missing
      toast.error('锁定前要把必填的著录项填好', { description: Object.values(missing).slice(0, 3).join('；') })
      return
    }
  }
  let current: ArchiveItem | null = it
  if (dirty.value) current = await save()
  if (!current) return
  busy.value = action
  try {
    const res = await itemAction(current.id, action, current.row_version, HANDLE_ALL)
    applyFresh(res, { force: true })
    emit('changed', res)
    toast.success(action === 'lock' ? `已锁定：${res.archive_no ?? ''}` : '已解锁，可以著录、解除封存了')
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
    const res = await updateArchiveItem(current.id, { row_version: sent, item_no: n }, HANDLE_ALL)
    applyFresh(res, { force: true })
    emit('changed', res)
    itemNoEditing.value = false
    toast.success(`已改为第 ${res.item_no} 件`, { description: res.archive_no ? `档号 ${res.archive_no}` : '档号为空，原因见页头' })
  } catch (e) {
    if (handledGlobally(e)) return
    if (isApiError(e, 'conflict')) {
      // 两种 409 conflict：件号被占用 / 档号撞号（换一个号），或者 row_version 过期（别人刚改过）。重新取一次来区分。
      const latest = await getArchiveItem(current.id, HANDLE_ALL).catch(() => null)
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
  let current: ArchiveItem | null = it
  if (dirty.value) current = await save()
  if (!current) return
  busy.value = 'move'
  const from = current.category_id
  try {
    const res = await updateArchiveItem(current.id, { row_version: current.row_version, category_id: target.id }, HANDLE_ALL)
    applyFresh(res, { force: true })
    emit('moved', res, from)
  } catch (e) {
    if (isApiError(e, 'not_found')) {
      // 404 可能是条目没了，也可能是目标节点刚被删掉：再取一次条目来区分（M3-5 审查）。
      const still = await getArchiveItem(current.id, HANDLE_ALL).catch(() => null)
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

// ---------- 撤销归档（DELETE，M9：未锁定的才能撤销；修改中的也可以） ----------

/**
 * 确认框打开时记下 row_version，DELETE 带它；期间这一件被改了 server 返回 409 conflict：重新读取，确认框不关，写明
 * "刚被修改"，请用户重新确认。
 */
const deleteAsk = reactive({ open: false, rowVersion: 0, pageCount: 0, changed: null as string | null })

function askDelete() {
  const it = item.value
  if (!it || busy.value || locked.value) return
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
    await deleteArchiveItem(it.id, deleteAsk.rowVersion, HANDLE_ALL)
    deleteAsk.open = false
    resetForm(it)
    emit('deleted', it)
  } catch (e) {
    if (handledGlobally(e)) {
      deleteAsk.open = false
      return
    }
    if (isApiError(e, 'conflict')) {
      // 确认框打开期间这一件被改了（可能刚重新归档）：重新读，不关确认框，请用户看清再确认。
      const latest = await getArchiveItem(it.id, ['not_found']).catch(() => null)
      if (!latest) {
        deleteAsk.open = false
        emit('gone', it.id)
        return
      }
      applyFresh(latest, { force: !dirty.value })
      if (latest.status === 'locked') {
        deleteAsk.open = false
        toast.error('这一件刚被锁定，不能撤销归档', { description: '要撤销先解锁' })
        return
      }
      deleteAsk.changed = `这一件刚被修改了（现在 ${latest.page_count} 页，打开确认框时 ${deleteAsk.pageCount} 页），没有撤销。请看清后重新确认。`
      deleteAsk.rowVersion = latest.row_version
      deleteAsk.pageCount = latest.page_count
      return
    }
    deleteAsk.open = false
    if (isApiError(e, 'item_locked')) {
      toast.error('这一件已锁定，不能撤销归档', { description: '要撤销先解锁' })
      void load({ force: !dirty.value })
    } else if (isApiError(e, 'not_found')) {
      toast.info('这一件已经不在了（可能刚被别人撤销归档）')
      emit('gone', it.id)
    } else toast.error(`撤销归档失败：${errorMessage(e)}`)
  } finally {
    busy.value = ''
  }
}

// ---------- 显示 ----------

const reason = computed(() => item.value?.archive_no_reason ?? null)
const statusInfo = computed(() => itemStatusOf(status.value))

function clearOrphan(key: string) {
  cleared.value = new Set([...cleared.value, key])
}
function keepOrphan(key: string) {
  const next = new Set(cleared.value)
  next.delete(key)
  cleared.value = next
}

/** 著录表单里一项改了：写进表单值，清掉它的错误。 */
function onField(key: string, value: string) {
  values[key] = value
  onInput(key)
}

function onInput(key: string) {
  if (errors.value[key]) {
    const next = { ...errors.value }
    delete next[key]
    errors.value = next
  }
}

/** 页（只读，来自来源文件夹）：给 FileGrid 的小格子用，"#n"就是页号。 */
const pageEntries = computed<Entry[]>(() =>
  (item.value?.pages ?? []).map((p) => ({
    kind: 'file',
    key: fileKey(p.file_id),
    file: {
      folder_file_id: '',
      file_id: p.file_id,
      file_name: p.file_name,
      sort_no: p.page_no,
      review_status: p.review_status,
      review_round: p.review_round,
      version_no: p.version_no,
      cataloged: false,
      created_at: '',
    },
  })),
)
const pageSelection = useSelection(() => pageEntries.value.map((e) => e.key))
const newAfterLock = computed(() => new Set((item.value?.pages ?? []).filter((p) => p.new_version_after_lock).map((p) => p.file_id)))

/** 看原图：这一件的页（按页序）当作一个文件夹给查看器左右切换。 */
function openViewer(entry: Entry) {
  if (entry.kind !== 'file') return
  emit(
    'openViewer',
    pageEntries.value.flatMap((e) => (e.kind === 'file' ? [e.file] : [])),
    entry.file.file_id,
  )
}

const modifying = computed(() => item.value?.source_status === 'modifying')
/** 件的简称（"办公室 0003"），对话框标题用。 */
const shortLabel = computed(() => {
  const it = item.value
  if (!it) return ''
  const node = (props.categoryLabel ?? it.category_path.split('/').slice(1).join(' / ')).split(' / ').pop()
  return node ? `${node} ${formatItemNo(it.item_no, props.itemNoWidth)}` : `第 ${formatItemNo(it.item_no, props.itemNoWidth)} 件`
})

defineExpose({
  /** 有没保存的著录修改。 */
  isDirty: () => dirty.value,
  save,
  discard: () => {
    if (item.value) resetForm(item.value)
  },
  /** 重新取详情（页面的实时事件用，可以 await）。 */
  refresh: () => load(),
  /** 页面自己做了解除封存、重新归档之后，用响应更新。 */
  apply: (it: ArchiveItem) => applyFresh(it, { force: !dirty.value }),
})
</script>

<template>
  <div
    class="flex min-h-0 flex-1 flex-col"
    data-testid="item-detail"
    :data-item-id="props.itemId"
    :data-status="item?.status"
    :data-source="item?.source_status"
    :data-row-version="item?.row_version"
    :data-dirty="dirty ? 'true' : undefined"
    :data-load="loadState"
    :data-busy="busy || undefined"
  >
    <div v-if="loadState === 'loading' && !item" class="flex flex-1 items-center justify-center gap-2 text-sm text-muted-foreground">
      <LoaderCircleIcon class="size-4 animate-spin" />正在加载…
    </div>
    <div v-else-if="loadState === 'error' && !item" class="flex flex-1 flex-col items-center justify-center gap-3 text-sm">
      <p class="text-destructive">{{ loadError || '加载失败' }}</p>
      <Button variant="outline" size="sm" @click="load({ force: true })">重试</Button>
    </div>

    <template v-else-if="item">
      <!-- 封存横条（M9；外框和 SealBanner 共用 StateBanner） -->
      <StateBanner v-if="modifying" tone="modifying" data-testid="item-banner" data-state="modifying">
        <b class="text-compact text-modifying">修改中</b>：来源文件夹已解除封存（{{ item.unsealed_by_name ?? '有人' }}
        {{ formatDateTime(item.unsealed_at) }}{{ item.unseal_reason ? `，${item.unseal_reason}` : '' }}）。改完后重新归档；在此之前不能锁定、不能导出。
        <span v-if="item.source_folder_deleted" class="block text-destructive">来源文件夹已被删除：不能重新归档，不要这一件了请"撤销归档"。</span>
        <template #actions>
          <Button v-if="!item.source_folder_deleted" size="xs" variant="outline" data-testid="item-open-folder" @click="emit('openFolder', item.source_folder_id)">
            去文件目录
          </Button>
          <Button size="xs" variant="modify" :disabled="busy !== ''" data-testid="item-reseal" @click="emit('reseal', item, shortLabel)">
            重新归档…
          </Button>
        </template>
      </StateBanner>
      <StateBanner v-else tone="sealed" data-testid="item-banner" data-state="sealed">
        <b class="text-compact text-sealed">来源：</b>
        <button type="button" class="text-left text-sealed hover:underline" :title="`在左边打开 ${item.source_folder_path}`" data-testid="item-source" @click="emit('openFolder', item.source_folder_id)">{{ item.source_folder_path }}</button>（已封存）
        <span class="block text-muted-foreground">页来自文件夹，这里不能改页。要改页 →</span>
        <template #actions>
          <Button size="xs" variant="outline" :disabled="busy !== ''" data-testid="item-unseal" @click="emit('unseal', item, shortLabel)">
            解除封存去修改…
          </Button>
        </template>
      </StateBanner>

      <!-- 页头：档号、状态、件号、位置 -->
      <div class="shrink-0 border-b px-4 pt-2.5 pb-2.5">
        <div class="flex items-start gap-2">
          <Button size="xs" variant="ghost" class="mt-0.5 -ml-1.5 shrink-0" data-testid="item-back" @click="emit('back')">
            <ChevronLeftIcon />返回
          </Button>
          <div class="min-w-0 flex-1">
            <div
              v-if="item.archive_no"
              class="truncate font-mono text-base font-semibold tracking-tight text-foreground"
              data-testid="item-archive-no"
              :title="`档号（按模板自动生成）：${item.archive_no}`"
            >{{ item.archive_no }}</div>
            <div
              v-else
              class="flex items-center gap-1.5 text-sm font-medium text-warn"
              data-testid="item-archive-no-empty"
              :data-code="reason?.code ?? ''"
            >
              <CircleAlertIcon class="size-4" />档号为空 · {{ archiveNoReasonShort(reason) }}
            </div>
          </div>
          <StatusBadge v-if="modifying" :status="ARCHIVE_STATE.modifying" size="md" class="mt-0.5" />
          <StatusBadge :status="statusInfo" size="md" class="mt-0.5" data-testid="item-status" />
        </div>

        <div class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
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
              <Input
                v-model="itemNoText"
                class="h-6 w-20 px-1.5 font-mono text-xs md:text-xs"
                inputmode="numeric"
                data-testid="item-no-input"
                @keydown.esc.prevent="itemNoEditing = false"
              />
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
          <span v-if="item.locked_at">锁定于 {{ formatDateTime(item.locked_at) }}</span>
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
            <p v-if="reason.code === 'incomplete'" class="mt-0.5 text-warn">在归类树里补上这几级节点，或者把这一件移到层级完整的节点下；档号为空的件可以著录，不能锁定。</p>
            <p v-else-if="reason.code === 'template_invalid'" class="mt-0.5 text-warn">请到「系统管理 › 项目」里修改档号模板。</p>
          </div>
          <Button
            v-if="reason.code === 'conflict' && reason.conflict_item_id && reason.conflict_category_id"
            size="xs"
            variant="outline"
            class="shrink-0"
            data-testid="archive-no-conflict-open"
            @click="emit('openItem', reason.conflict_category_id!, reason.conflict_item_id!)"
          >
            <ExternalLinkIcon />查看占用的件（第 {{ reason.conflict_item_no }} 件）
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
          class="mt-2 flex items-center gap-2 rounded-md bg-sealed-soft/50 px-3 py-2 text-xs text-sealed"
          data-testid="item-locked-note"
        >
          <LockIcon class="size-3.5 shrink-0" />
          <span class="min-w-0 flex-1">
            已锁定：著录、档号都已冻结，不能移动、改件号、撤销归档、解除封存。要修改先解锁。
            <span v-if="item.new_version_after_lock" class="ml-1 font-medium text-info" data-testid="item-new-version-after-lock">
              锁定之后有页重扫出了新版本（下面标出）。
            </span>
          </span>
        </div>
        <div
          v-if="remoteChanged"
          class="mt-2 flex items-center gap-2 rounded-md bg-info-soft px-3 py-2 text-xs text-info"
          data-testid="item-remote-changed"
        >
          <span class="min-w-0 flex-1">别人刚修改了这一件（现在是第 {{ item.row_version }} 版）。保存时会提示冲突，你的修改不会丢。</span>
          <Button size="xs" variant="outline" class="shrink-0" @click="mergeAfterConflict()">载入最新（保留我的修改）</Button>
        </div>
        <div
          v-if="mergeNote"
          class="mt-2 rounded-md bg-warn-soft px-3 py-2 text-xs text-warn"
          role="alert"
          data-testid="item-merge-note"
        >{{ mergeNote }}</div>
      </div>

      <!-- 著录表单 -->
      <ItemFieldsForm
        v-model:title="title"
        :defs="defs"
        :values="values"
        :initial="initial"
        :cleared="cleared"
        :errors="errors"
        :theirs="theirs"
        :issues="issues"
        :orphan-keys="orphanKeys"
        :locked="locked"
        @submit="save"
        @input="onInput"
        @field="onField"
        @clear="clearOrphan"
        @keep="keepOrphan"
      />

      <!-- 页（只读，来自来源文件夹） -->
      <div class="flex min-h-0 flex-1 flex-col" data-testid="item-pages" :data-count="item.pages.length">
        <div class="flex h-8 shrink-0 items-center gap-2 border-b px-4 text-xs text-muted-foreground">
          <span class="font-medium text-foreground">页（{{ item.page_count }}，只读，来自文件夹）</span>
          <span v-if="item.page_counts.approved" class="text-ok">通过 {{ item.page_counts.approved }}</span>
          <span v-if="item.page_counts.pending" class="text-warn">待审 {{ item.page_counts.pending }}</span>
          <span v-if="item.page_counts.rejected" class="text-destructive">驳回 {{ item.page_counts.rejected }}</span>
          <span v-if="item.page_counts.unassigned">待分配 {{ item.page_counts.unassigned }}</span>
          <span v-if="newAfterLock.size" class="text-info">锁定后有新版本 {{ newAfterLock.size }}</span>
          <span class="flex-1" />
          <span v-if="modifying" class="text-modifying">修改中：这里还是上次归档时的页</span>
          <span v-else>双击看原图</span>
        </div>
        <div v-if="!item.pages.length" class="flex flex-1 items-center justify-center text-xs text-muted-foreground">没有页</div>
        <div v-else class="flex min-h-0 flex-1 flex-col" :class="modifying && 'opacity-60'">
          <FileGrid
            :entries="pageEntries"
            view="icons"
            dense
            :selection="pageSelection"
            visible-owner="catalog-item-pages"
            @open="openViewer"
          />
        </div>
      </div>

      <!-- 动作 -->
      <div class="flex shrink-0 flex-wrap items-center gap-1.5 border-t px-4 py-2" data-testid="item-actions">
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
        <GuardedButton
          v-if="!locked"
          size="sm"
          :reason="lockBlockers.length ? lockBlockers.join('；') : null"
          :disabled="busy !== ''"
          data-testid="item-lock"
          @click="confirmLock = true"
        >
          <LockIcon />锁定
        </GuardedButton>
        <Button v-if="locked" size="sm" variant="outline" :disabled="busy !== ''" data-testid="item-unlock" @click="confirmUnlock = true">
          <LockOpenIcon />解锁
        </Button>
        <Button v-if="!locked" size="sm" variant="ghost" :disabled="busy !== ''" data-testid="item-move" @click="pickerOpen = true">
          <ArrowRightLeftIcon />移到别的节点…
        </Button>
        <Button v-if="props.canExport" size="sm" variant="ghost" title="带着这一件跳到导出页的新建导出（先预检）" data-testid="item-export" @click="emit('export', item.id)">
          <FileOutputIcon />导出这件…
        </Button>
        <span class="flex-1" />
        <Button
          v-if="!locked"
          size="sm"
          variant="ghost"
          class="text-destructive hover:text-destructive"
          :disabled="busy !== ''"
          title="删除这一件，文件夹回到未归档（只在真的不要这一件时用）"
          data-testid="item-revoke"
          @click="askDelete"
        >
          <Undo2Icon />撤销归档…
        </Button>
        <span v-if="dirty" class="w-full text-2xs text-muted-foreground">有未保存的修改（Ctrl 或 ⌘ + S 保存）</span>
      </div>

      <ConfirmDialog
        v-model:open="confirmLock"
        title="锁定这一件？"
        :description="`${item.archive_no ?? ''} 锁定后，著录、档号都冻结，不能移动、撤销归档、解除封存；要修改先解锁。`"
        confirm-text="锁定"
        :loading="busy === 'lock'"
        @confirm="doLock"
      />
      <ConfirmDialog
        v-model:open="confirmUnlock"
        title="解锁这一件？"
        :description="`${item.archive_no ?? `第 ${item.item_no} 件`} 解锁后回到「未锁定」，著录可以再改，也可以解除封存；要导出时需要重新锁定。`"
        confirm-text="解锁"
        :loading="busy === 'unlock'"
        @confirm="doUnlock"
      />
      <ConfirmDialog
        v-model:open="deleteAsk.open"
        :title="`撤销归档 ${shortLabel}？`"
        :description="`删除这一件（题名、著录一起删掉，件号 ${item.item_no} 不再使用），文件夹「${item.source_folder_name}」回到“未归档”，可以重新归到别处。只在真的不要这一件时用；只是改几页请用“解除封存”。${dirty ? '没有保存的著录修改也会丢掉。' : ''}`"
        confirm-text="撤销归档"
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
      </ConfirmDialog>
      <CategoryPicker
        v-model:open="pickerOpen"
        :project-id="props.projectId"
        :initial-id="item.category_id"
        :disabled-ids="[item.category_id]"
        title="把这一件移到别的归类节点"
        :description="`件号取目标节点的最大件号 + 1，档号按新位置重新生成（撞号时为空）。来源文件夹和封存不变。`"
        confirm-text="移到这里"
        @confirm="moveTo"
      />
    </template>
  </div>
</template>
