<script setup lang="ts">
// 审核工作台 /review（M2-6，参考 plan.html 的 p-review Mockup；菜单码 review）。
//
// 三栏：
// - 左：我的待办（`GET /api/me/reviews`，useMyReviews）：按文件夹分组、按状态（全部 / 待审 / 驳回 / 通过）和"DPI 不足"
//   筛选、虚拟滚动（ReviewList），跟随左上角的项目切换（Q12），页头提示"其他项目还有 n 个待审"。范围可以缩到一个
//   文件夹子树（地址栏 `?folder=<id>`，分组头上"只看这里"）。is_current 为 false 的行显示成历史、不给撤回。
//   Ctrl/⌘、Shift 点选多个，底部"批量通过"（Q9：只发选中的里分给自己的待审，最多 500 个，确认框写明个数）。
// - 中：M2-4 的 ImageViewer（第 2 轮及以后按 C 换成 CompareViewer：左边是上一轮被驳回的版本，标出原因）。
//   浏览器里不预加载下一张原图（占内存预算）；壳里看当前这张时给下一张提前切块（prepareTiles，M2-3）。
// - 右：文件标题、"上一轮因为某原因被驳回"、快照时间线（可以勾两个版本对比）、审核记录（已取消的默认隐藏）、文件信息
//   （ReviewDetail）；底部是驳回原因面板、通过和驳回按钮、撤回通过（ReviewActions）。
//
// 全键盘（Q3，document 上绑，见 onKeydown）：← → ↑ ↓ 上一张、下一张；A 通过并跳到下一张待审；R 打开驳回面板
// （1–9 勾选原因、Tab 到说明框、Enter 确认、Esc 取消）；空格 适合窗口和 100% 切换；+ − 0 缩放；[ ] 向左、向右旋转
// （不保存）；C 打开或关闭对比；对比时 V 在同一位置切换看另一版（Q2 的"另给一个快捷键"，自定）；Esc 退出对比。
// 焦点在输入框时字母键不触发；按住不放的重复按键（e.repeat）一律忽略；带 Ctrl/⌘/Alt 的不处理（留给浏览器）。
// "同一张图的请求在途时不接受新的操作"：这个文件的审核请求在途时、原图还在加载时（M2-4 须知：status.phase 为
// loading），不能通过、驳回（翻页、缩放照常）。
// 审的必须是看到的那一版（验收第 1 轮，用户决定；design §4.4）：原图和封面都没显示出来时不能审；原图加载失败、
// 只显示了封面时先确认再审；原图太大或看不了、只显示了封面时可以审，画面上醒目标出"只看了封面"；对比时右边不是
// 当前版本、同位切换正在看左边（上一轮）时不能审（并排时第一次按 V 直接显示左边）。见下面的 seen、canAct。
//
// 提交审核带上看到的 snapshot_id（batch 的当前版本）。403 `not_assignee`、409 `invalid_state`、409 `conflict` 时提示、
// 重新取这个文件并整体刷新列表；`conflict`（刚重扫出新版本）另外重取图片信息。通过、驳回成功后跳到下一张待审。
//
// 实时（useProjectStream，去抖 300 ms、最多等 1 秒）：`files.changed` 的 file_ids 在列表里的逐个刷新（被改派走的
// 从待办里消失），不在列表里的、只带 folder_ids 的（超过 500 个文件）按 useMyReviews 的规则核对计数或按文件夹整体重拉；
// `folders.changed`、`resync`、断线重连整体重拉。M3-7：右栏的归类信息在当前这张的 `files.changed`、任何一条
// `catalog.changed` 和整体重拉时重新取（catalogRefresh）。
//
// 截图脚本用的属性：data-testid=review-page（data-list、data-stream、data-current、data-phase（当前原图的状态）、
// data-busy、data-compare、data-prepared（壳里最近一次提前切块的 file_id））；页头 other-projects（data-count）；
// 标签 review-tab（data-state、data-count、aria-checked），review-tab-low-dpi；范围 review-scope；批量通过 batch-approve。
import { Columns2Icon, DownloadIcon, LayersIcon, LoaderCircleIcon, RotateCwIcon, XIcon } from '@lucide/vue'
import { useEventListener } from '@vueuse/core'
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowReactive, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import { batchFiles, getFileSnapshots } from '@/api/files'
import { getFolder } from '@/api/folders'
import { errorMessage, isApiError } from '@/api/http'
import {
  approveMany,
  getFileReviews,
  getReviewSettings,
  MAX_APPROVE_FILE_IDS,
  reopenFile,
  submitReview,
} from '@/api/reviews'
import type { FileReviews, MyReviewItem, MyReviewState, ProjectEvent, ReviewSettings, SnapshotItem } from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import CompareViewer, { type CompareMode, type CompareSide, type CompareSideKey } from '@/components/viewer/CompareViewer.vue'
import ImageViewer, { type ViewerStatus, type ViewerView } from '@/components/viewer/ImageViewer.vue'
import ViewerToolbar from '@/components/viewer/ViewerToolbar.vue'
import { useProjectStream } from '@/composables/useProjectStream'
import { useSelection } from '@/composables/useSelection'
import { isTauri, shellImages } from '@/lib/client'
import { formatSpec } from '@/lib/files'
import { prepareTiles } from '@/lib/image-cache'
import { rejectReasonsText } from '@/lib/reject-reasons'
import { openExternal, shellErrorMessage } from '@/lib/shell'
import type { ViewerImageInfo } from '@/lib/viewer/load-original'
import { useImageInfoStore } from '@/stores/image-info'
import { useProjectStore } from '@/stores/project'
import { useSessionStore } from '@/stores/session'
import { toastOpError } from '@/views/review-assign/review-errors'

import ReviewActions from './ReviewActions.vue'
import ReviewDetail from './ReviewDetail.vue'
import ReviewList from './ReviewList.vue'
import { defaultComparePair, previousRejection, rejectionForSnapshot } from './review-file'
import { REFRESH_FILES_MAX, useMyReviews } from './useMyReviews'
import { useReviewKeys } from './useReviewKeys'

const project = useProjectStore()
const session = useSessionStore()
const images = useImageInfoStore()
const route = useRoute()
const router = useRouter()
const inShell = isTauri()

// ---------- 范围和列表 ----------

const folderScope = computed(() => (typeof route.query.folder === 'string' && route.query.folder ? route.query.folder : null))
const stateFilter = ref<MyReviewState>('open')
const lowDpi = ref(false)
/**
 * 刚在切换器里换了项目、地址栏里还是原来项目的文件夹（正在 router.replace 清掉）：这期间范围按整个项目算，免得拿
 * 新项目配旧文件夹去取，得到 404 和"文件夹不在当前项目里"的提示（审查第 1 轮）。
 */
const switchingProject = ref(false)

const list = useMyReviews(
  () =>
    project.current
      ? {
          projectId: project.current.id,
          folderId: switchingProject.value ? null : folderScope.value,
          state: stateFilter.value,
          lowDpi: lowDpi.value,
        }
      : null,
  () => session.me?.id ?? null,
  () => currentId.value,
)

const TABS: { state: MyReviewState; label: string }[] = [
  { state: 'all', label: '全部' },
  { state: 'open', label: '待审' },
  { state: 'rejected', label: '驳回' },
  { state: 'approved', label: '通过' },
]

/** 换了范围之后的第一批数据：当前文件不在里面时选第一个待审的（而不是"原来位置上的那个"）。 */
let pickFresh = true
let lastProjectId: string | null = null

// 范围里的文件夹不在这个项目里或已删除（404），或者地址栏里的 id 本身不对（400 invalid_input，验收第 1 轮）：
// 提示，回到整个项目。
watch(
  () => list.error.value,
  (e) => {
    if (!e || switchingProject.value) return
    if (folderScope.value && (isApiError(e, 'not_found') || isApiError(e, 'invalid_input'))) {
      toast.error(
        isApiError(e, 'invalid_input')
          ? '地址里的文件夹不对，已回到整个项目'
          : '这个文件夹不在当前项目里或已被删除，已回到整个项目',
      )
      void router.replace({ query: {} })
    }
  },
)

const items = computed(() => list.items.value)
const selection = useSelection(() => items.value.map((i) => i.file_id))
watch(items, () => selection.prune())

/**
 * 范围文件夹自己的名字和路径（验收第 1 轮：按各项路径的公共前缀算，范围是上层文件夹、文件都在同一个子文件夹里时会
 * 算成子文件夹）。有 files 或 scan 菜单时取 `GET /api/folders/{id}`（这个接口要这两个菜单之一；没有时调它会触发
 * 全局的 403 处理，所以不调）；否则这个文件夹里直接有分给我的文件时用 folders 里的名字，再不行写"所选文件夹"。
 */
const scopeFolderInfo = ref<{ id: string; name: string; path: string } | null>(null)
let scopeFolderSeq = 0
// 等"我的审核"按这个文件夹取成功了再取名字：说明它在当前项目里、能进（进不了项目的文件夹，folders 接口返回 403
// forbidden，会触发全局的无权处理）。
watch(
  () => list.loadedFolderId.value,
  async (id) => {
    const my = ++scopeFolderSeq
    if (!id) {
      scopeFolderInfo.value = null
      return
    }
    if (scopeFolderInfo.value?.id === id) return
    if (!(session.menuCodes.has('files') || session.menuCodes.has('scan'))) return
    try {
      const f = await getFolder(id, true)
      if (my !== scopeFolderSeq) return
      const name = f.parent_id ? f.folder_name : (project.current?.project_name ?? f.folder_name)
      const path = [...f.ancestors.map((a) => a.folder_name), f.folder_name].join('/')
      scopeFolderInfo.value = { id, name, path }
    } catch {
      // 只是名字，取不到就用下面的退路。
    }
  },
)

const scopeName = computed(() => {
  const id = folderScope.value
  if (!id) return null
  if (scopeFolderInfo.value?.id === id) return scopeFolderInfo.value.name
  return list.folderById.value.get(id)?.folder_name ?? '所选文件夹'
})

function scopeTo(folderId: string | null) {
  void router.push({ query: folderId ? { folder: folderId } : {} })
}

// ---------- 当前文件 ----------

const currentId = ref<string | null>(null)
/** 页面还在（卸载后在途的回调不再发请求）。 */
let alive = true
/** 审核请求在途的文件（同一张图的请求在途时不接受新的操作）。 */
const busyIds = shallowReactive(new Set<string>())
/** 当前文件在列表里的位置（它被拿掉时，选同一个位置上的那个，即原来的下一个）。 */
let lastIndex = 0
const current = computed(() => list.get(currentId.value) ?? null)
const currentIndex = computed(() => (currentId.value ? (list.indexOf.value.get(currentId.value) ?? -1) : -1))

watch(
  () => [items.value, list.state.value] as const,
  ([list_, state]) => {
    if (state !== 'ready') return
    const id = currentId.value
    const i = id ? list.indexOf.value.get(id) : undefined
    if (i !== undefined) {
      lastIndex = i
      pickFresh = false
      return
    }
    if (!list_.length) {
      currentId.value = null
      return
    }
    if (pickFresh || !id) {
      pickFresh = false
      const first = list_.find((it) => it.status === 'open') ?? list_[0]!
      goTo(first.file_id, false)
      return
    }
    // 当前文件不在了（改派走了、删了）：看原来位置上的那个。
    goTo(list_[Math.min(lastIndex, list_.length - 1)]!.file_id)
  },
)

const listRef = ref<InstanceType<typeof ReviewList> | null>(null)

function goTo(fileId: string, scroll = true) {
  currentId.value = fileId
  selection.set([fileId])
  const i = list.indexOf.value.get(fileId)
  if (i !== undefined) lastIndex = i
  if (scroll) void nextTick(() => listRef.value?.scrollToFile(fileId))
}

function activate(fileId: string) {
  currentId.value = fileId
  const i = list.indexOf.value.get(fileId)
  if (i !== undefined) lastIndex = i
}

/** 上一张、下一张（到底了先加载下一页）。 */
async function step(delta: number) {
  const n = items.value.length
  if (!n) return
  let i = currentIndex.value
  if (i < 0) {
    goTo(items.value[0]!.file_id)
    return
  }
  let j = i + delta
  if (j >= n && list.hasMore.value) {
    const from = currentId.value
    await list.loadMore()
    if (currentId.value !== from) return
    i = currentIndex.value
    j = i + delta
  }
  j = Math.max(0, Math.min(items.value.length - 1, j))
  if (j !== i) goTo(items.value[j]!.file_id)
}

function isPending(it: MyReviewItem) {
  return it.status === 'open'
}

function pendingIn(from: number, to: number): string | null {
  const list_ = items.value
  for (let k = from; k < to; k++) if (isPending(list_[k]!) && !busyIds.has(list_[k]!.file_id)) return list_[k]!.file_id
  return null
}

/**
 * 下一张待审：先从 fileId 之后找；后面没有而且也没有没加载的页了，才从头找跳过的（不含它自己）。后面还有页没加载时
 * 返回 null（由 jumpAfterReview 先加载，验收第 1 轮：不能先回绕到前面）。
 */
function nextPendingAfter(fileId: string): string | null {
  const i = list.indexOf.value.get(fileId) ?? -1
  const after = pendingIn(i + 1, items.value.length)
  if (after || list.hasMore.value) return after
  return pendingIn(0, Math.max(0, i))
}

/**
 * 通过、驳回之后跳到下一张待审；已加载的都审完了而后面还有页，先加载（没有进展——失败、正在整体重拉——就停下，
 * 不空转，审查第 1 轮）。
 */
async function jumpAfterReview(fileId: string) {
  if (currentId.value !== fileId) return
  let next = nextPendingAfter(fileId)
  while (!next && list.hasMore.value) {
    const progressed = await list.loadMore()
    if (currentId.value !== fileId) return
    next = nextPendingAfter(fileId)
    if (!progressed) break
  }
  if (next) goTo(next)
  else if (list.hasMore.value) toast.info('后面还有没加载出来的文件，请稍后在列表里往下翻', { id: 'all-done' })
  else toast.success(folderScope.value ? '这个文件夹里分给你的都审完了' : '分给你的都审完了', { id: 'all-done' })
}

/** 预取：当前这张的下一张（A 会跳到的那张；当前不是待审时是列表里的下一个）。 */
const nextTarget = computed(() => {
  const cur = current.value
  if (!cur) return null
  if (isPending(cur)) return nextPendingAfter(cur.file_id)
  return items.value[currentIndex.value + 1]?.file_id ?? null
})

// 当前这张和下一张的图片信息：登记成一个可见范围（和列表的可见范围分开），所以列表里这一行滚出视野之后，事件
// invalidate 时照样重取（重扫出新版本时查看器跟着换）、快过期时照样续、缓存满了也不淘汰（验收第 1 轮）。
// 下一张先取好信息，切过去时不用等 batch；只取信息，浏览器里不预加载原图。
const CURRENT_OWNER = 'review-current'
watch(
  () => [currentId.value, nextTarget.value] as const,
  ([cur, next]) => {
    images.setVisible(
      CURRENT_OWNER,
      [cur, next].filter((x): x is string => !!x),
    )
  },
  { immediate: true },
)

const info = computed(() => (currentId.value ? images.get(currentId.value) : undefined))
const missing = computed(() => !!currentId.value && images.isMissing(currentId.value))
// batch 说不存在（删了、进不了项目）：重新取这个文件，从列表里拿掉。
watch(missing, (m) => {
  if (m && currentId.value) void list.refreshFiles([currentId.value])
})

// ---------- 审核设置 ----------

const settings = ref<ReviewSettings | null>(null)
/** 取驳回原因列表失败的原因（操作区显示，带"重试"；打开驳回面板时也会再取一次）。 */
const settingsError = ref<string | null>(null)
let settingsSeq = 0
async function loadSettings(pid: string) {
  const my = ++settingsSeq
  settings.value = null
  settingsError.value = null
  try {
    const res = await getReviewSettings(pid, true)
    if (my === settingsSeq) settings.value = res
  } catch (e) {
    if (my === settingsSeq) settingsError.value = errorMessage(e)
  }
}

// ---------- 当前文件的版本和审核记录 ----------

const detail = reactive({
  fileId: null as string | null,
  snapshots: null as SnapshotItem[] | null,
  reviews: null as FileReviews | null,
  state: 'idle' as 'idle' | 'loading' | 'ready' | 'error',
  error: null as string | null,
})
let detailSeq = 0
let detailTimer: ReturnType<typeof setTimeout> | null = null
let detailPromise: Promise<void> | null = null
/** 换文件之后停多久再取（快速翻页时中间那几张不取）。 */
const DETAIL_SETTLE_MS = 120

function loadDetail(fileId: string, quiet = false): Promise<void> {
  if (!alive) return Promise.resolve()
  const my = ++detailSeq
  if (!quiet || detail.fileId !== fileId) {
    detail.fileId = fileId
    detail.snapshots = null
    detail.reviews = null
    detail.state = 'loading'
    detail.error = null
  }
  detailPromise = (async () => {
    try {
      const [snaps, revs] = await Promise.all([getFileSnapshots(fileId, true), getFileReviews(fileId, true)])
      if (my !== detailSeq) return
      detail.snapshots = snaps.snapshots
      detail.reviews = revs
      detail.state = 'ready'
      detail.error = null
    } catch (e) {
      if (my !== detailSeq) return
      if (detail.state !== 'ready') {
        detail.state = 'error'
        detail.error = errorMessage(e)
      }
      // 文件没了、不能看了：列表也刷新这个文件。
      if (isApiError(e) && (e.status === 404 || e.status === 403)) void list.refreshFiles([fileId])
    }
  })()
  return detailPromise
}

watch(currentId, (fid) => {
  if (detailTimer) clearTimeout(detailTimer)
  detailTimer = null
  detailSeq++
  detail.fileId = fid
  detail.snapshots = null
  detail.reviews = null
  detail.state = fid ? 'loading' : 'idle'
  detail.error = null
  detailPromise = null
  if (!fid) return
  detailTimer = setTimeout(() => {
    detailTimer = null
    void loadDetail(fid)
  }, DETAIL_SETTLE_MS)
})

/** 等当前文件的详情取好（按 C 时还没取到就马上取）。 */
async function ensureDetail(fileId: string) {
  if (detail.fileId === fileId && detail.state === 'ready') return
  if (detailTimer) {
    clearTimeout(detailTimer)
    detailTimer = null
    await loadDetail(fileId)
    return
  }
  if (detailPromise) await detailPromise
  if (detail.fileId === fileId && detail.state !== 'ready') await loadDetail(fileId)
}

const detailForCurrent = computed(() => detail.fileId === currentId.value)
const currentSnapshotId = computed(
  () => info.value?.snapshot_id ?? detail.snapshots?.[detail.snapshots.length - 1]?.snapshot_id ?? null,
)
const prevReject = computed(() =>
  detailForCurrent.value && detail.reviews ? previousRejection(detail.reviews.reviews, currentSnapshotId.value) : null,
)

// ---------- 查看器 ----------

const viewerRef = ref<InstanceType<typeof ImageViewer> | null>(null)
const compareRef = ref<InstanceType<typeof CompareViewer> | null>(null)
const view = ref<ViewerView | null>(null)
/** 当前文件的原图状态（对比时取右侧，即当前版本那一侧）。 */
const viewerStatus = ref<{ fileId: string; status: ViewerStatus } | null>(null)

// 换文件的那一刻清掉（之后的状态事件才算这个文件的）。
watch(
  currentId,
  () => {
    viewerStatus.value = null
  },
  { flush: 'sync' },
)

/** 这几种失败是图片信息过期了：原图地址过期（存储返回 403）、壳里没有来源记录（404）、取不到地址。 */
function staleInfo(s: ViewerStatus): boolean {
  return s.phase === 'failed' && ['http', 'not_found', 'no_url'].includes(s.code ?? '')
}

function onViewerStatus(s: ViewerStatus) {
  const fid = currentId.value
  if (!fid) return
  viewerStatus.value = { fileId: fid, status: s }
  if (staleInfo(s)) images.reportBroken(fid)
}

const phase = computed(() => (viewerStatus.value && viewerStatus.value.fileId === currentId.value ? viewerStatus.value.status.phase : null))
const step_ = computed(() => (viewerStatus.value && viewerStatus.value.fileId === currentId.value ? viewerStatus.value.status.step : null))

/** 工具栏的操作发给当前显示的查看器。 */
function target() {
  return compare.value ? compareRef.value : viewerRef.value
}

const modeLabel = computed(() => {
  switch (viewerStatus.value?.status.mode) {
    case 'tiles':
      return '客户端分块'
    case 'image':
      return '浏览器整图'
    case 'worker':
      return '浏览器解码 TIFF'
    default:
      return ''
  }
})

const statusText = computed(() => {
  const s = viewerStatus.value?.status
  if (!s || viewerStatus.value?.fileId !== currentId.value) return ''
  switch (s.phase) {
    case 'loading':
      return s.cover === 'loaded' ? '原图加载中 · 先显示封面' : '原图加载中'
    case 'ready':
      if (s.elapsedMs == null) return '原图'
      return s.elapsedMs < 1000 ? `原图 · ${s.elapsedMs} ms` : `原图 · ${(s.elapsedMs / 1000).toFixed(1)} 秒`
    case 'too_large':
      return '原图太大 · 只显示封面'
    case 'unsupported':
      return '看不了原图 · 只显示封面'
    case 'failed':
      return '原图加载失败'
    default:
      return ''
  }
})

// ---------- 对比（C） ----------

const compare = ref<{ fileId: string; left: CompareSide; right: CompareSide; sids: [string, string] } | null>(null)
const compareMode = ref<CompareMode>('side')
const compareShowing = ref<CompareSideKey>('right')
const compareLoading = ref(false)
let compareSeq = 0

/** 地址还能用多久以上才算新鲜。 */
const FRESH_MS = 10 * 60 * 1000
function fresh(expiresAt: string | null | undefined): boolean {
  return !!expiresAt && Date.parse(expiresAt) - Date.now() > FRESH_MS
}

function sideOf(s: SnapshotItem, label: string): CompareSide {
  const r = detail.reviews ? rejectionForSnapshot(detail.reviews.reviews, s.snapshot_id) : null
  return {
    info: s as ViewerImageInfo,
    versionNo: s.version_no,
    label,
    rejectReasons: r?.reject_reasons ?? null,
    rejectComment: r?.comment ?? null,
  }
}

/** 打开对比：不给 pair 时左边是上一轮被驳回的版本（没有时前一版），右边是当前版本。 */
async function openCompare(pair?: [string, string]) {
  const fid = currentId.value
  if (!fid) return
  const my = ++compareSeq
  compareLoading.value = true
  try {
    await ensureDetail(fid)
    if (my !== compareSeq || currentId.value !== fid) return
    // 地址快过期了（页面开了很久）：重取一次版本（壳因此重新记下来源）。
    if (detail.snapshots?.some((s) => !fresh(s.url_expires_at))) await loadDetail(fid, true)
    if (my !== compareSeq || currentId.value !== fid) return
    const snaps = detail.snapshots
    if (!snaps) {
      toast.error('取不到这个文件的各个版本')
      return
    }
    let left: SnapshotItem | undefined
    let right: SnapshotItem | undefined
    let leftLabel = '上一版'
    if (pair) {
      left = snaps.find((s) => s.snapshot_id === pair[0])
      right = snaps.find((s) => s.snapshot_id === pair[1])
    } else {
      const p = defaultComparePair(snaps, prevReject.value)
      left = p?.left
      right = p?.right
      leftLabel = p?.leftLabel ?? leftLabel
    }
    if (!left || !right) {
      toast.info('这个文件只有一个版本，没有可以对比的', { id: 'compare-single' })
      return
    }
    const latest = snaps[snaps.length - 1]!
    const labelOf = (s: SnapshotItem, fallback: string) => (s.snapshot_id === latest.snapshot_id ? '当前' : fallback)
    compare.value = {
      fileId: fid,
      left: sideOf(left, pair ? labelOf(left, `v${left.version_no}`) : leftLabel),
      right: sideOf(right, labelOf(right, `v${right.version_no}`)),
      sids: [left.snapshot_id, right.snapshot_id],
    }
    compareMode.value = 'side'
    compareShowing.value = 'right'
    viewerStatus.value = null
    sidesRefreshedAt = 0
  } finally {
    if (my === compareSeq) compareLoading.value = false
  }
}

function closeCompare() {
  compareSeq++
  compareLoading.value = false
  if (!compare.value) return
  compare.value = null
  viewerStatus.value = null
}

function toggleCompare() {
  if (compare.value || compareLoading.value) closeCompare()
  else void openCompare()
}

watch(currentId, () => closeCompare())

let sidesRefreshedAt = 0
const SIDES_RETRY_MS = 30 * 1000

/** 对比时某一侧的状态：右侧（当前版本）算作当前文件的原图状态；地址过期时重取版本（30 秒内最多一次）。 */
function onCompareStatus(side: CompareSideKey, s: ViewerStatus) {
  const c = compare.value
  if (!c || c.fileId !== currentId.value) return
  if (side === 'right') viewerStatus.value = { fileId: c.fileId, status: s }
  if (!staleInfo(s)) return
  images.reportBroken(c.fileId)
  if (Date.now() - sidesRefreshedAt < SIDES_RETRY_MS) return
  sidesRefreshedAt = Date.now()
  void loadDetail(c.fileId, true).then(() => {
    const cur = compare.value
    if (!cur || cur.fileId !== c.fileId || !detail.snapshots) return
    const pick = (sd: CompareSide): CompareSide => {
      const found = detail.snapshots!.find((x) => x.snapshot_id === sd.info?.snapshot_id)
      return found ? { ...sd, info: found as ViewerImageInfo } : sd
    }
    compare.value = { ...cur, left: pick(cur.left), right: pick(cur.right) }
  })
}

// ---------- 壳里提前切块（M2-3） ----------

const prepared = new Set<string>()
const lastPrepared = ref<string | null>(null)

watch(
  () => [currentId.value, phase.value, step_.value, nextTarget.value, nextTarget.value ? images.get(nextTarget.value) : undefined] as const,
  ([cur, ph, st, next, nextInfo]) => {
    if (!shellImages() || !cur || !next) return
    // 当前这张的分块描述取到了（开始画块，或已经画完）再给下一张切，不和当前这张抢切块名额。
    if (ph !== 'ready' && !(ph === 'loading' && st === 'render')) return
    if (!nextInfo) return
    if (!nextInfo.original_url || prepared.has(nextInfo.snapshot_id)) return
    if (prepared.size > 1000) prepared.clear()
    prepared.add(nextInfo.snapshot_id)
    prepareTiles({ file_id: next, snapshot_id: nextInfo.snapshot_id })
      .then(() => {
        lastPrepared.value = next
      })
      .catch(() => {
        // 不在来源记录里（换了用户、信息过期）之类：只是提前切块，看到它时照常切。
        prepared.delete(nextInfo.snapshot_id)
      })
  },
)

// ---------- 通过、驳回、撤回通过、批量通过 ----------

const rejectError = ref<string | null>(null)
const actionsRef = ref<InstanceType<typeof ReviewActions> | null>(null)

watch(currentId, () => {
  rejectError.value = null
})

/**
 * 对比时右边显示的不是当前版本（在快照时间线上挑了两个旧版本，或对比期间重扫出了新版本）：这时不能审，审的必须是
 * 看到的那一版（审查第 1 轮）。
 */
const compareHidesCurrent = computed(() => !!compare.value && compare.value.sids[1] !== info.value?.snapshot_id)
/** 对比的同位切换正在显示左边（旧版本）：当前版本没在看，不能审（验收第 1 轮）。 */
const compareShowsOld = computed(() => !!compare.value && compareMode.value === 'toggle' && compareShowing.value === 'left')

/**
 * 当前这一版看到了什么（验收第 1 轮，用户决定；design §4.4"审核台"）：
 * - original：原图画出来了；
 * - cover：原图太大或看不了（too_large、unsupported），封面显示出来了：可以审，画面上醒目标出"只看了封面"；
 * - cover-confirm：原图加载失败（网络、502、503、tiles_failed、地址取不到……），封面显示出来了：可以审，但先确认；
 * - none：原图和封面都没显示出来：不能审；
 * - loading：原图还在加载（或者状态还没到）：不能审。
 */
type Seen = 'loading' | 'original' | 'cover' | 'cover-confirm' | 'none'
const seen = computed<Seen>(() => {
  const vs = viewerStatus.value
  if (!vs || vs.fileId !== currentId.value) return 'loading'
  const s = vs.status
  const coverShown = s.cover === 'loaded'
  switch (s.phase) {
    case 'ready':
      return 'original'
    case 'too_large':
    case 'unsupported':
      return coverShown ? 'cover' : 'none'
    case 'failed':
      return coverShown ? 'cover-confirm' : 'none'
    default:
      return 'loading'
  }
})

/** 能不能通过、驳回当前文件（见文件开头"同一张图的请求在途时不接受新的操作"和上面的 seen）。 */
const canAct = computed(() => {
  const it = current.value
  if (!it || !isPending(it) || busyIds.has(it.file_id)) return false
  if (!info.value?.snapshot_id || compareHidesCurrent.value || compareShowsOld.value) return false
  return seen.value === 'original' || seen.value === 'cover' || seen.value === 'cover-confirm'
})

/** 操作区按钮上方的提示：不能审的原因，或者"只看了封面"。 */
const actionHint = computed(() => {
  const it = current.value
  if (!it || !isPending(it)) return null
  if (busyIds.has(it.file_id)) return '正在提交…'
  if (!info.value) return '正在取图片信息…'
  if (compareHidesCurrent.value) return '对比里右边不是当前版本：退出对比（C）后再审'
  if (compareShowsOld.value) return '正在看上一轮的版本：按 V 切回当前版本再审'
  switch (seen.value) {
    case 'loading':
      return '原图加载完才能审'
    case 'none':
      return '没看到这一版（原图和封面都没显示出来），不能审'
    case 'cover':
      return '原图太大或看不了，只看了封面'
    case 'cover-confirm':
      return '原图没加载出来，只看了封面；审之前会再确认一次'
    default:
      return null
  }
})

// 只看了封面（原图没显示出来）时，审之前先确认；同一个文件的同一版确认过一次就不再问。
const coverConfirm = reactive({ open: false, action: 'approve' as 'approve' | 'reject', key: '', reason: null as string | null })
let coverConfirmedKey = ''
const currentKey = () => `${currentId.value ?? ''}|${info.value?.snapshot_id ?? ''}`

function needCoverConfirm(): boolean {
  return seen.value === 'cover-confirm' && coverConfirmedKey !== currentKey()
}

/** A、"通过"按钮。 */
function requestApprove() {
  if (!canAct.value) return
  if (needCoverConfirm()) {
    Object.assign(coverConfirm, { open: true, action: 'approve', key: currentKey(), reason: null })
    return
  }
  void approve()
}

/** R、"驳回重扫"按钮、点原因（reason：点的是哪个原因，打开面板时勾上）。 */
function requestReject(reason?: string | null) {
  if (!canAct.value) return
  if (needCoverConfirm()) {
    Object.assign(coverConfirm, { open: true, action: 'reject', key: currentKey(), reason: reason ?? null })
    return
  }
  openRejectPanel(reason)
}

function onCoverConfirmed() {
  const { action, key, reason } = coverConfirm
  coverConfirm.open = false
  // 确认框开着的时候换了文件或版本：不做。
  if (key !== currentKey()) return
  coverConfirmedKey = key
  if (action === 'approve') void approve()
  else openRejectPanel(reason)
}

watch(currentKey, () => {
  if (coverConfirm.open) coverConfirm.open = false
})

// 对比期间当前版本变了（重扫出了新版本）：退出对比，看新版本。
watch(
  () => info.value?.snapshot_id ?? null,
  (sid, before) => {
    const c = compare.value
    if (!c || !sid || !before || sid === before) return
    if (c.fileId === currentId.value) {
      closeCompare()
      toast.info('这个文件刚有了新版本，已退出对比，请看新版本')
    }
  },
)

/** 审核错误：not_assignee、invalid_state、conflict 提示后刷新这个文件和整个列表。 */
async function onReviewError(e: unknown, it: MyReviewItem) {
  const name = `「${it.file_name}」`
  if (isApiError(e, 'not_assignee')) toast.error(`${name}已经不是分给你的了（可能刚被改派给别人），列表已刷新`, { id: `review-err-${it.file_id}` })
  else if (isApiError(e, 'invalid_state')) toast.error(`${name}的审核状态刚刚变了（可能已被审过、撤回或删除），列表已刷新`, { id: `review-err-${it.file_id}` })
  else if (isApiError(e, 'conflict')) {
    toast.error(`${name}刚有了新版本（重扫），请看过新版本再审`, { id: `review-err-${it.file_id}` })
    images.invalidate([it.file_id])
  } else if (isApiError(e, 'invalid_input')) {
    rejectError.value = e.message
    toast.error(e.message)
    return
  } else {
    toastOpError(e, errorMessage(e))
    return
  }
  await list.refreshFiles([it.file_id])
  await list.reload()
  if (currentId.value === it.file_id) void loadDetail(it.file_id, true)
}

async function approve() {
  const it = current.value
  const sid = info.value?.snapshot_id
  if (!it || !sid || !canAct.value || needCoverConfirm()) return
  const fid = it.file_id
  busyIds.add(fid)
  try {
    const res = await submitReview(fid, { result: 'approved', snapshot_id: sid }, true)
    list.applyReviewed(res)
    busyIds.delete(fid)
    await jumpAfterReview(fid)
  } catch (e) {
    await onReviewError(e, it)
  } finally {
    busyIds.delete(fid)
  }
}

async function reject(payload: { reasons: string[]; comment: string | null }) {
  const it = current.value
  const sid = info.value?.snapshot_id
  if (!it || !sid || !canAct.value || needCoverConfirm()) return
  const fid = it.file_id
  busyIds.add(fid)
  rejectError.value = null
  try {
    const res = await submitReview(
      fid,
      { result: 'rejected', reject_reasons: payload.reasons, comment: payload.comment, snapshot_id: sid },
      true,
    )
    list.applyReviewed(res)
    busyIds.delete(fid)
    // 提交成功：清空勾选和说明（这一行已经不是待审，面板也会因此清空；这里再明确清一次）。
    if (currentId.value === fid) actionsRef.value?.reset()
    await jumpAfterReview(fid)
  } catch (e) {
    await onReviewError(e, it)
  } finally {
    busyIds.delete(fid)
  }
}

function openRejectPanel(reason?: string | null) {
  if (!canAct.value) return
  if (settingsError.value && project.currentId) void loadSettings(project.currentId)
  actionsRef.value?.openPanel(reason)
}

// 撤回通过（Q8）：只对我通过的、现在仍是这一行结论的（is_current）。
const confirmReopen = reactive({ open: false, loading: false, item: null as MyReviewItem | null })

function askReopen() {
  const it = current.value
  if (!it || it.result !== 'approved' || !it.is_current) return
  confirmReopen.item = it
  confirmReopen.open = true
}

async function doReopen() {
  const it = confirmReopen.item
  if (!it || confirmReopen.loading) return
  confirmReopen.loading = true
  busyIds.add(it.file_id)
  try {
    const res = await reopenFile(it.file_id, true)
    confirmReopen.open = false
    if (res.reviewer_id === session.me?.id) toast.success(`已撤回「${it.file_name}」的通过，回到你的待审`)
    else toast.success(`已撤回「${it.file_name}」的通过`)
  } catch (e) {
    toastOpError(
      e,
      isApiError(e, 'invalid_state')
        ? '这个文件已经不是"通过"了（可能刚被撤回），已刷新'
        : isApiError(e, 'not_assignee')
          ? '只能撤回自己通过的'
          : errorMessage(e),
    )
    if (isApiError(e) && e.status >= 400 && e.status < 500) confirmReopen.open = false
  } finally {
    confirmReopen.loading = false
    busyIds.delete(it.file_id)
    await list.refreshFiles([it.file_id])
    if (currentId.value === it.file_id) void loadDetail(it.file_id, true)
  }
}

// 批量通过（Q9）：选中的里分给自己的待审，最多 500 个，一个事务。
const selectedItems = computed(() => items.value.filter((i) => selection.isSelected(i.file_id)))
const batchEligible = computed(() => selectedItems.value.filter((i) => isPending(i) && !busyIds.has(i.file_id)))
const confirmBatch = reactive({ open: false, loading: false, ids: [] as string[], skipped: 0 })

function askBatch() {
  const ids = batchEligible.value.map((i) => i.file_id)
  if (!ids.length) return
  if (ids.length > MAX_APPROVE_FILE_IDS) {
    toast.error(`一次最多批量通过 ${MAX_APPROVE_FILE_IDS} 个，现在选了 ${ids.length} 个待审的`)
    return
  }
  confirmBatch.ids = ids
  confirmBatch.skipped = selectedItems.value.length - ids.length
  confirmBatch.open = true
}

const BATCH_REASONS: Record<string, string> = {
  not_found: '已不存在（可能刚被删除或移走）',
  invalid_state: '已经不是待审了（可能刚被审过、改派或撤回）',
  not_assignee: '已经不是分给你的了（可能刚被改派）',
}

async function doBatch() {
  const pid = project.currentId
  const ids = confirmBatch.ids
  if (!pid || !ids.length || confirmBatch.loading) return
  confirmBatch.loading = true
  for (const id of ids) busyIds.add(id)
  try {
    const res = await approveMany(pid, ids, true)
    for (const r of res.items) list.applyReviewed(r)
    confirmBatch.open = false
    selection.clear()
    toast.success(`已批量通过 ${res.approved} 个文件`)
    const cur = currentId.value
    if (cur && ids.includes(cur)) {
      for (const id of ids) busyIds.delete(id)
      await jumpAfterReview(cur)
    }
  } catch (e) {
    if (isApiError(e) && BATCH_REASONS[e.code]) {
      const bad = (e.details as { ids?: string[] } | undefined)?.ids ?? []
      toast.error(`有 ${bad.length || '几'} 个文件${BATCH_REASONS[e.code]}，整批没有通过，列表已刷新`)
      confirmBatch.open = false
      await list.reload()
    } else {
      toastOpError(e, errorMessage(e))
      if (isApiError(e) && e.status >= 400 && e.status < 500) confirmBatch.open = false
    }
  } finally {
    confirmBatch.loading = false
    for (const id of ids) busyIds.delete(id)
  }
}

// ---------- 下载原图 ----------

/**
 * 打开原图地址（浏览器：新窗口；壳：系统浏览器）。不给 snapshotId 是当前版本（batch）；给了是对比里的某一侧
 * （snapshots）。地址剩不到 10 分钟就重新取。
 */
async function downloadOriginal(snapshotId?: string | null) {
  const fid = currentId.value
  if (!fid) return
  let url: string | null = null
  let urlError: string | undefined
  if (snapshotId) {
    let s = detail.snapshots?.find((x) => x.snapshot_id === snapshotId)
    if (!s || !fresh(s.url_expires_at)) {
      await loadDetail(fid, true)
      s = detail.snapshots?.find((x) => x.snapshot_id === snapshotId)
    }
    url = s?.original_url ?? null
    urlError = s ? s.url_error?.message : '取不到这个版本（重取各个版本失败）'
  } else {
    let item = images.get(fid)
    if (!item || !fresh(item.url_expires_at)) {
      try {
        item = (await batchFiles([fid], true)).items[0]
      } catch (e) {
        toast.error(`取不到原图地址：${errorMessage(e)}`)
        return
      }
    }
    url = item?.original_url ?? null
    urlError = item?.url_error?.message
  }
  if (!url) {
    toast.error('取不到原图地址', { description: urlError })
    return
  }
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    toast.error('原图地址格式不对')
    return
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    toast.error('原图地址不是 http 或 https，不能打开')
    return
  }
  if (inShell) {
    try {
      await openExternal(parsed.href)
      toast.success('已在系统浏览器里打开原图')
    } catch (e) {
      toast.error(shellErrorMessage(e))
    }
  } else {
    window.open(parsed.href, '_blank', 'noopener,noreferrer')
  }
}

// ---------- 键盘（Q3，键位和规则见 useReviewKeys.ts） ----------

useReviewKeys({
  enabled: () => !!project.current,
  panel: () => actionsRef.value,
  viewer: () => target(),
  step: (delta) => void step(delta),
  approve: () => requestApprove(),
  openReject: () => requestReject(),
  toggleCompare,
  // 并排时第一次按 V：切到同位切换，直接显示左边（上一轮）；之后在两边之间切换（验收第 1 轮）。
  toggleSide: () => {
    if (!compare.value) return
    if (compareMode.value === 'side') {
      compareMode.value = 'toggle'
      compareShowing.value = 'left'
    } else compareRef.value?.toggleSide()
  },
  escape: () => {
    if (compare.value || compareLoading.value) return () => closeCompare()
    if (selection.count.value > 1 && currentId.value) return () => selection.set([currentId.value!])
    return null
  },
})

// ---------- 实时事件 ----------

const pendingFiles = new Set<string>()
const pendingFolderOnly = new Set<string>()
let pendingFolders = false
let pendingFull = false
/** M3-7：收到 `catalog.changed`（档号、题名、页序可能变了），右栏的归类信息要重新取。 */
let pendingCatalog = false
/** M3-7：右栏"归类信息（只读）"的刷新令牌。 */
const catalogRefresh = ref(0)
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
  if (e.type === 'resync') pendingFull = true
  else if (e.type === 'files.changed') {
    if (e.file_ids?.length) e.file_ids.forEach((id) => pendingFiles.add(id))
    else e.folder_ids?.forEach((id) => pendingFolderOnly.add(id))
  } else if (e.type === 'folders.changed') pendingFolders = true
  else if (e.type === 'catalog.changed') pendingCatalog = true
  else return
  scheduleApply()
}

async function applyEvents() {
  applyTimer = null
  firstPendingAt = 0
  if (!alive) return
  const full = pendingFull || pendingFolders
  const fileIds = [...pendingFiles]
  const folderOnly = [...pendingFolderOnly]
  pendingFull = false
  pendingFolders = false
  pendingFiles.clear()
  pendingFolderOnly.clear()
  const cur = currentId.value
  // 归类信息：catalog.changed 不带文件，任何一条都重新取当前这张的（一次 1 ms 左右）；当前这张的 files.changed
  // （挂接、移出）在下面和版本、审核记录一起刷新。
  if (pendingCatalog || full) catalogRefresh.value++
  pendingCatalog = false

  if (full) {
    images.invalidate()
    await list.reload()
    if (cur && currentId.value === cur) void loadDetail(cur, true)
    return
  }

  if (fileIds.length) images.invalidate(fileIds)
  const inList = fileIds.filter((id) => list.indexOf.value.has(id))
  if (inList.length > REFRESH_FILES_MAX) {
    await list.reload()
  } else if (inList.length) {
    const before = new Map(inList.map((id) => [id, list.get(id)!]))
    const need = await list.refreshFiles(inList)
    const gone = [...before.values()].filter((it) => it.status === 'open' && !list.indexOf.value.has(it.file_id))
    if (gone.length === 1) toast.info(`「${gone[0]!.file_name}」已不再分给你（改派给别人或被删除），已从待办里移除`)
    else if (gone.length > 1) toast.info(`${gone.length} 个待审文件已不再分给你（改派给别人或被删除），已从待办里移除`)
    if (need) await list.reload()
  }
  // 不在列表里的文件、只带文件夹的事件：可能有新分给我的，或者被筛掉的那几种变了。
  const myFolders = new Set(items.value.map((i) => i.folder_id))
  if (folderOnly.some((id) => myFolders.has(id))) {
    // 超过 500 个文件的事件没有 file_ids：按文件夹整体重拉。
    if (cur) images.invalidate(items.value.filter((i) => folderOnly.includes(i.folder_id)).map((i) => i.file_id))
    await list.reload()
  } else if (fileIds.length > inList.length || folderOnly.length) {
    list.checkCounts()
  }
  const curItem = cur ? list.get(cur) : null
  if (cur && currentId.value === cur && (fileIds.includes(cur) || (curItem && folderOnly.includes(curItem.folder_id)))) {
    void loadDetail(cur, true)
    catalogRefresh.value++
  }
}

const stream = useProjectStream(() => project.currentId, {
  onEvent,
  onOpen: (first) => {
    if (!first) {
      pendingFull = true
      scheduleApply()
    } else if (list.state.value === 'ready') {
      // 第一次连上时列表已经取好：加载和连接建立之间可能漏了事件，核对一次计数。
      list.checkCounts()
    }
  },
  onStopped: (err) => {
    if (err.status === 403 || err.status === 404) void project.refresh()
  },
})

// 范围变了（项目、文件夹、状态标签、DPI 不足）：从第一页重新取。放在最后：immediate 的第一次调用要用到上面的变量。
watch(
  () => [project.currentId, folderScope.value, stateFilter.value, lowDpi.value] as const,
  async ([pid]) => {
    if (pid !== lastProjectId) {
      const switched = lastProjectId !== null
      lastProjectId = pid
      if (pid) void loadSettings(pid)
      if (switched) {
        currentId.value = null
        closeCompare()
        selection.clear()
        if (folderScope.value) {
          // 用户在切换器里换了项目：范围（文件夹）属于原来的项目，回到整个项目（地址变了会再进来一次）。
          // 换地址期间这个 watch 再进来时跳过（见下面），换完在这里取新项目的列表。
          switchingProject.value = true
          try {
            await router.replace({ query: {} })
          } finally {
            switchingProject.value = false
          }
          pickFresh = true
          await list.reset()
          return
        }
      }
    }
    if (switchingProject.value) return
    pickFresh = true
    await list.reset()
  },
  { immediate: true },
)

// 其他项目的待审数不随本项目的事件变：窗口重新拿到焦点时核对一次。
useEventListener(window, 'focus', () => {
  if (project.currentId) list.checkCounts()
})

onMounted(() => {
  void project.ensureLoaded()
})

onBeforeUnmount(() => {
  alive = false
  images.clearVisible(CURRENT_OWNER)
  list.dispose()
  if (applyTimer) clearTimeout(applyTimer)
  if (detailTimer) clearTimeout(detailTimer)
  detailSeq++
  compareSeq++
})

// ---------- 显示 ----------

const otherProjectRows = computed(() =>
  list.otherProjects.value.map((p) => {
    const pr = project.projects.find((x) => x.id === p.project_id)
    return { ...p, code: pr?.project_code ?? '', name: pr?.project_name ?? '（未知项目）' }
  }),
)
const otherOpen = ref(false)

const tabCount = (s: MyReviewState) => (list.counts.value ? list.counts.value[s] : null)
const spec = computed(() => (info.value ? formatSpec(info.value) : ''))
const positionText = computed(() => {
  if (currentIndex.value < 0) return ''
  return `${currentIndex.value + 1} / ${items.value.length}${list.hasMore.value ? '+' : ''}`
})

const emptyText = computed(() => {
  const tab = TABS.find((t) => t.state === stateFilter.value)?.label ?? ''
  const what = lowDpi.value ? `DPI 不足、${tab === '全部' ? '' : tab}` : tab === '全部' ? '' : tab
  return `没有${what ? `${what.replace(/、$/, '')}的` : '分给你的'}文件`
})
</script>

<template>
  <div
    class="flex min-h-0 flex-1 flex-col"
    data-testid="review-page"
    :data-list="list.state.value"
    :data-stream="stream.state.value"
    :data-current="currentId ?? undefined"
    :data-phase="phase ?? undefined"
    :data-seen="seen"
    :data-busy="busyIds.size ? 'true' : undefined"
    :data-compare="compare ? 'on' : 'off'"
    :data-prepared="lastPrepared ?? undefined"
  >
    <header class="flex h-14 shrink-0 items-center gap-3 border-b px-5">
      <h1 class="shrink-0 text-base font-semibold tracking-tight text-foreground">审核</h1>
      <span v-if="list.counts.value" class="shrink-0 text-xs text-muted-foreground" data-testid="review-head-counts">
        待审 {{ list.counts.value.open.toLocaleString() }} · 共 {{ list.counts.value.all.toLocaleString() }} 个分给我
      </span>
      <Popover v-if="list.otherOpen.value > 0" v-model:open="otherOpen">
        <PopoverTrigger as-child>
          <button
            type="button"
            class="inline-flex shrink-0 items-center gap-1 rounded-full bg-info-soft px-2.5 text-xs leading-6 text-info hover:bg-info-soft/70"
            data-testid="other-projects"
            :data-count="list.otherOpen.value"
          >
            其他项目还有 {{ list.otherOpen.value.toLocaleString() }} 个待审
          </button>
        </PopoverTrigger>
        <PopoverContent align="start" class="w-80 gap-2 p-2">
          <div class="px-2 pt-1 text-xs text-muted-foreground">点项目切过去（左上角的项目切换器也可以）</div>
          <button
            v-for="p in otherProjectRows"
            :key="p.project_id"
            type="button"
            class="flex items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-muted"
            data-testid="other-project"
            @click="
              otherOpen = false;
              project.select(p.project_id)
            "
          >
            <span class="font-mono text-xs text-muted-foreground">{{ p.code }}</span>
            <span class="min-w-0 flex-1 truncate">{{ p.name }}</span>
            <span v-if="p.status === 'archived'" class="shrink-0 rounded bg-muted px-1 text-[10px] text-muted-foreground">已归档</span>
            <span class="shrink-0 font-mono text-xs">{{ p.open.toLocaleString() }}</span>
          </button>
        </PopoverContent>
      </Popover>
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

    <div v-else class="flex min-h-0 flex-1">
      <!-- 左：我的待办 -->
      <aside class="flex w-72 shrink-0 flex-col border-r bg-sidebar/60" data-testid="review-left">
        <div class="shrink-0 border-b px-3 pt-2.5 pb-2">
          <div class="flex items-center gap-1.5" data-testid="review-scope" :data-folder-id="folderScope ?? undefined">
            <b class="min-w-0 truncate text-sm font-semibold text-foreground">{{ scopeName ?? project.current.project_name }}</b>
            <button
              v-if="folderScope"
              type="button"
              class="inline-flex shrink-0 items-center gap-0.5 rounded px-1 text-[11px] leading-5 text-muted-foreground hover:bg-muted hover:text-foreground"
              title="看整个项目"
              data-testid="review-scope-clear"
              @click="scopeTo(null)"
            >
              <XIcon class="size-3" />整个项目
            </button>
            <span class="flex-1" />
            <button
              type="button"
              class="shrink-0 rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
              title="刷新列表"
              data-testid="review-refresh"
              @click="list.reload()"
            >
              <RotateCwIcon class="size-3.5" />
            </button>
          </div>
          <div class="mt-0.5 text-xs text-muted-foreground">
            {{ folderScope ? '只看这个文件夹（含子文件夹）' : '整个项目' }} · 分给我
            {{ list.counts.value ? list.counts.value.all.toLocaleString() : '…' }} 个
          </div>
          <div class="mt-2 flex flex-wrap gap-1" role="radiogroup" aria-label="按状态筛选">
            <button
              v-for="t in TABS"
              :key="t.state"
              type="button"
              role="radio"
              :aria-checked="stateFilter === t.state"
              class="rounded-full px-2 text-xs leading-6"
              :class="stateFilter === t.state ? 'bg-foreground text-background' : 'bg-background text-foreground shadow-[0_0_0_1px_rgba(0,0,0,0.1)] hover:bg-muted'"
              data-testid="review-tab"
              :data-state="t.state"
              :data-count="tabCount(t.state) ?? ''"
              @click="stateFilter = t.state"
            >
              {{ t.label }} {{ tabCount(t.state) ?? '' }}
            </button>
            <button
              type="button"
              role="checkbox"
              :aria-checked="lowDpi"
              class="rounded-full px-2 text-xs leading-6"
              :class="lowDpi ? 'bg-warn text-white' : 'bg-background text-warn shadow-[0_0_0_1px_rgba(180,83,9,0.25)] hover:bg-warn-soft'"
              data-testid="review-tab-low-dpi"
              :data-count="list.counts.value?.low_dpi ?? ''"
              :title="`当前版本的 DPI 低于 ${list.lowDpiThreshold.value}（读不出的不算）`"
              @click="lowDpi = !lowDpi"
            >
              DPI 不足 {{ list.counts.value?.low_dpi ?? '' }}
            </button>
          </div>
        </div>

        <!-- 整体重拉失败（列表里还是原来的内容）：提示，带"重试"（另外会自动再试一次） -->
        <div
          v-if="list.state.value === 'ready' && list.error.value"
          class="flex shrink-0 items-center gap-1.5 border-b bg-destructive-soft px-3 py-1.5 text-xs text-destructive"
          data-testid="review-reload-error"
        >
          <span class="min-w-0 flex-1 truncate" :title="errorMessage(list.error.value)">列表刷新失败：{{ errorMessage(list.error.value) }}</span>
          <button type="button" class="shrink-0 underline" @mousedown.prevent @click="list.reload()">重试</button>
        </div>
        <div v-if="list.state.value === 'loading'" class="flex flex-1 items-center justify-center gap-2 text-sm text-muted-foreground">
          <LoaderCircleIcon class="size-4 animate-spin" />正在加载…
        </div>
        <div v-else-if="list.state.value === 'error'" class="flex flex-1 flex-col items-center justify-center gap-3 px-4 text-center text-sm">
          <p class="text-destructive">{{ errorMessage(list.error.value) }}</p>
          <Button variant="outline" size="sm" @click="list.reset()">重试</Button>
        </div>
        <div
          v-else-if="!items.length"
          class="flex flex-1 flex-col items-center justify-center gap-1 px-6 text-center text-sm text-muted-foreground"
          data-testid="review-empty"
        >
          <span>{{ emptyText }}</span>
          <span v-if="list.otherOpen.value" class="text-xs">其他项目还有 {{ list.otherOpen.value }} 个待审，见页头</span>
        </div>
        <ReviewList
          v-else
          ref="listRef"
          :items="items"
          :folders="list.folderById.value"
          :current-id="currentId"
          :selection="selection"
          :low-dpi-threshold="list.lowDpiThreshold.value"
          :has-more="list.hasMore.value"
          :loading-more="list.loadingMore.value"
          :load-more-error="list.loadMoreError.value ? errorMessage(list.loadMoreError.value) : null"
          :busy-ids="busyIds"
          :scoped="!!folderScope"
          @activate="activate"
          @load-more="list.autoLoadMore()"
          @retry-more="list.loadMore()"
          @scope-folder="scopeTo"
        />

        <div class="shrink-0 border-t px-3 py-2" data-testid="batch-bar" :data-selected="selection.count.value" :data-eligible="batchEligible.length">
          <div class="mb-1.5 text-[11px] text-muted-foreground">
            <template v-if="selection.count.value > 1">
              已选 {{ selection.count.value }} 个，其中待审 {{ batchEligible.length }} 个
              <button type="button" class="ml-1 underline hover:text-foreground" @click="currentId ? selection.set([currentId]) : selection.clear()">取消</button>
            </template>
            <template v-else>Ctrl/⌘ 点选多个、Shift 点选一段，再批量通过</template>
          </div>
          <Button
            variant="outline"
            size="sm"
            class="w-full"
            :disabled="batchEligible.length === 0 || selection.count.value < 2"
            data-testid="batch-approve"
            @mousedown.prevent
            @click="askBatch()"
          >
            批量通过选中的{{ selection.count.value > 1 ? ` ${batchEligible.length} 个` : '文件' }}
          </Button>
        </div>
      </aside>

      <!-- 中：查看器 -->
      <section class="relative flex min-w-0 flex-1 flex-col" data-testid="review-center">
        <ViewerToolbar
          :view="view"
          :disabled="!currentId"
          @fit="target()?.fit()"
          @actual="target()?.actualSize()"
          @zoom-in="target()?.zoomIn()"
          @zoom-out="target()?.zoomOut()"
          @rotate-left="target()?.rotateLeft()"
          @rotate-right="target()?.rotateRight()"
        >
          <template #end>
            <span class="mx-1 h-4 w-px bg-border" />
            <template v-if="!compare">
              <button
                type="button"
                class="viewer-tool"
                :disabled="!current || (current.version_no < 2 && (detail.snapshots?.length ?? 0) < 2)"
                data-testid="review-compare"
                title="和上一轮（被驳回的版本）对比（C）"
                @mousedown.prevent
                @click="toggleCompare()"
              >
                <LoaderCircleIcon v-if="compareLoading" class="size-3.5 animate-spin" />
                <Columns2Icon v-else class="size-3.5" />与上一轮对比
                <span class="font-mono text-[10px] text-muted-foreground">C</span>
              </button>
            </template>
            <template v-else>
              <button
                type="button"
                class="viewer-tool"
                :class="{ on: compareMode === 'side' }"
                data-testid="compare-mode-side"
                @mousedown.prevent
                @click="compareMode = 'side'"
              >
                <Columns2Icon class="size-3.5" />左右并排
              </button>
              <button
                type="button"
                class="viewer-tool"
                :class="{ on: compareMode === 'toggle' }"
                data-testid="compare-mode-toggle"
                title="两张叠在同一个位置，切换着看（V）"
                @mousedown.prevent
                @click="compareMode = 'toggle'"
              >
                <LayersIcon class="size-3.5" />同位切换<span class="font-mono text-[10px] text-muted-foreground">V</span>
              </button>
              <button type="button" class="viewer-tool" data-testid="compare-close" @mousedown.prevent @click="closeCompare()">
                退出对比<span class="font-mono text-[10px] text-muted-foreground">C</span>
              </button>
            </template>
            <div class="flex-1" />
            <button
              type="button"
              class="viewer-tool"
              :disabled="!currentId"
              title="下载原图（当前版本）"
              data-testid="viewer-download-original"
              @mousedown.prevent
              @click="downloadOriginal()"
            >
              <DownloadIcon class="size-3.5" />
            </button>
          </template>
        </ViewerToolbar>

        <div class="relative flex min-h-0 flex-1">
          <div v-if="!currentId" class="flex flex-1 items-center justify-center bg-zinc-200 text-sm text-muted-foreground">
            {{ list.state.value === 'ready' ? '没有要看的文件' : '' }}
          </div>
          <div v-else-if="missing" class="flex flex-1 items-center justify-center bg-zinc-200 text-sm text-muted-foreground">
            文件已不存在（可能刚被删除或移走）
          </div>
          <CompareViewer
            v-else-if="compare"
            ref="compareRef"
            v-model:mode="compareMode"
            v-model:showing="compareShowing"
            :file-id="compare.fileId"
            :left="compare.left"
            :right="compare.right"
            @status="onCompareStatus"
            @view="(v) => (view = v)"
            @download="(side) => downloadOriginal(compare?.sids[side === 'left' ? 0 : 1])"
          />
          <ImageViewer
            v-else
            ref="viewerRef"
            class="min-w-0 flex-1"
            :file-id="currentId"
            :info="info"
            @status="onViewerStatus"
            @view="(v) => (view = v)"
            @download="downloadOriginal()"
          >
            <!-- 第 2 轮及以后：上一轮因为什么被驳回（Q2），按 C 对比 -->
            <button
              v-if="prevReject && current && current.review_round > 1"
              type="button"
              class="absolute top-2 left-1/2 z-10 flex max-w-[calc(100%-2rem)] -translate-x-1/2 items-center gap-1.5 rounded-full bg-background/95 py-1 pr-1.5 pl-3 text-xs text-foreground shadow-[0_0_0_1px_rgba(180,83,9,0.25),0_2px_8px_rgba(0,0,0,0.08)] hover:bg-background"
              data-testid="viewer-prev-reject"
              @mousedown.prevent
              @click="openCompare()"
            >
              <span class="size-1.5 shrink-0 rounded-full bg-warn" />
              <span class="truncate">
                第 {{ current.review_round }} 轮 · 上一轮（v{{ prevReject.version_no }}）因“{{ rejectReasonsText(prevReject.reject_reasons) }}”被驳回
              </span>
              <span class="shrink-0 rounded-full bg-foreground px-2 leading-5 text-background">对比 C</span>
            </button>
          </ImageViewer>
          <div v-if="compareLoading && !compare" class="pointer-events-none absolute inset-x-0 top-3 flex justify-center">
            <span class="rounded-full bg-background/95 px-3 py-1 text-xs text-muted-foreground shadow">正在取各个版本…</span>
          </div>
          <!-- 原图没显示出来、只看了封面：醒目标出来（验收第 1 轮） -->
          <div
            v-if="currentId && (seen === 'cover' || seen === 'cover-confirm')"
            class="pointer-events-none absolute top-11 right-2 z-20 flex items-center gap-1.5 rounded-md bg-warn px-2.5 py-1 text-xs font-medium text-white shadow-[0_2px_8px_rgba(0,0,0,0.18)]"
            data-testid="review-cover-only"
            :data-seen="seen"
          >
            只看了封面 · {{ seen === 'cover' ? '原图太大或看不了' : '原图没加载出来' }}
          </div>
          <!-- 同位切换正在看上一轮的版本 -->
          <div
            v-if="compareShowsOld"
            class="pointer-events-none absolute bottom-3 left-1/2 z-20 -translate-x-1/2 rounded-full bg-foreground/85 px-3 py-1 text-xs text-background"
            data-testid="review-showing-old"
          >
            正在看上一轮的版本 · 按 V 切回当前版本再审
          </div>
        </div>

        <!-- 状态栏 -->
        <div
          class="flex h-7 shrink-0 items-center gap-3.5 overflow-hidden border-t bg-background px-3 font-mono text-[11px] whitespace-nowrap text-muted-foreground"
          data-testid="viewer-statusbar"
        >
          <span v-if="current" class="truncate">{{ current.file_name }}</span>
          <span v-if="positionText">{{ positionText }}</span>
          <span v-if="current">v{{ current.version_no }}</span>
          <span v-if="spec">{{ spec }}</span>
          <span v-if="!compare && modeLabel">{{ modeLabel }}</span>
          <span class="flex-1" />
          <span>{{ statusText }}</span>
          <span v-if="view?.zoom != null">{{ Math.round(view.zoom * 100) }}%</span>
        </div>
      </section>

      <!-- 右：快照、审核记录、文件信息、操作 -->
      <aside class="flex w-80 shrink-0 flex-col border-l" data-testid="review-right">
        <div class="min-h-0 flex-1 overflow-y-auto">
          <ReviewDetail
            :item="current"
            :info="info"
            :snapshots="detailForCurrent ? detail.snapshots : null"
            :reviews="detailForCurrent ? detail.reviews : null"
            :state="detailForCurrent ? detail.state : 'loading'"
            :error="detail.error"
            :prev-reject="prevReject"
            :low-dpi-threshold="list.lowDpiThreshold.value"
            :comparing="compare ? compare.sids : null"
            :catalog-refresh="catalogRefresh"
            @compare="(l, r) => openCompare([l, r])"
            @download="downloadOriginal()"
            @retry="currentId && loadDetail(currentId)"
          />
        </div>
        <ReviewActions
          ref="actionsRef"
          :item="current"
          :reasons="settings?.reject_reasons ?? null"
          :reasons-error="settingsError"
          :comment-max="settings?.comment_max_chars ?? 500"
          :can-act="canAct"
          :blocked-hint="actionHint"
          :busy="!!current && busyIds.has(current.file_id)"
          :server-error="rejectError"
          @approve="requestApprove()"
          @open-request="(reason) => requestReject(reason)"
          @reject="reject"
          @reopen="askReopen()"
          @retry-reasons="project.currentId && loadSettings(project.currentId)"
        />
      </aside>
    </div>

    <ConfirmDialog
      v-model:open="confirmBatch.open"
      :title="`批量通过 ${confirmBatch.ids.length} 个文件？`"
      :description="`只通过选中的里分给你、待审的 ${confirmBatch.ids.length} 个${
        confirmBatch.skipped ? `（另有 ${confirmBatch.skipped} 个不是待审，不处理）` : ''
      }。每个文件按它现在的版本记为通过，各记一条通过日志；任何一个已经不能通过（被改派、审过、删除）时整批都不做。之后可以逐个撤回通过。`"
      :confirm-text="`通过 ${confirmBatch.ids.length} 个`"
      :loading="confirmBatch.loading"
      @confirm="doBatch"
    />
    <ConfirmDialog
      v-model:open="coverConfirm.open"
      title="原图没加载出来，只看了封面，确定要审吗？"
      :description="`封面只有长边 400 像素，看不清细节。可以先在原图的提示里点“重试”，或者下载原图看过再审。确定要${
        coverConfirm.action === 'approve' ? '通过' : '驳回'
      }「${current?.file_name ?? ''}」吗？`"
      :confirm-text="coverConfirm.action === 'approve' ? '只看封面，确定通过' : '只看封面，去选驳回原因'"
      @confirm="onCoverConfirmed"
    />
    <ConfirmDialog
      v-model:open="confirmReopen.open"
      :title="`撤回「${confirmReopen.item?.file_name ?? ''}」的通过？`"
      description="文件回到待审核，重新分给你（你已不能再审这个项目时改为待分配）。撤回会记进文件的操作日志。"
      confirm-text="撤回通过"
      destructive
      :loading="confirmReopen.loading"
      @confirm="doReopen"
    />
  </div>
</template>
