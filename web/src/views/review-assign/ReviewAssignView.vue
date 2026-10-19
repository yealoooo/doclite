<script setup lang="ts">
// 审核分配页 /review-assign（M2-5，参考 plan.html 的 p-assign Mockup；菜单码 review_assign）。
//
// - 三栏：左边目录树（FolderTree，按需加载）带每个文件夹整棵子树的待分配数（review-summary：每加载一层树
//   调一次）；中间当前文件夹里直接放的文件（FileGrid，图标和列表两种视图），按审核状态筛选、多选；右边是
//   审核人和各自的待办数、分配和改派按钮、选中文件的分配记录和操作日志（AssignPanel）。
// - 页头：整个项目的"待分配 n、待审 n、通过 n、驳回 n"（不带 folder_id 的 review-summary）和"改派…"。
// - 分配（M2-1，Q5）：没选文件时把整个文件夹（含子文件夹）分给一个人，先确认"含子文件夹，共 n 个待分配"；
//   选了文件时按选中的分配，不是待分配的由 server 跳过，提示"跳过了 n 个"。选中的超过 500 个时分批发。
// - 改派（Q5）：右栏可以把选中的待审文件批量改派（已经是目标审核人在审的不算，M2-8）、把单个文件改派；"改派…"对话框（ReassignDialog）列出子树里
//   全部 open 分配，已不能再审的人（停用、没有审核菜单、失去项目授权）的行高亮并写明原因。不做取消分配。
// - 撤回通过（Q8）：选中一个已通过的文件，负责人可以撤回；回到待审、重新分给通过它的人（他不能再审时改为待分配）。
// - 当前文件夹在地址栏 `?folder=<id>`（没有就是根文件夹）；链接里的文件夹不在当前项目里时回到根文件夹。
// - 实时（useProjectStream）：`files.changed` 只带文件直接所在的文件夹，祖先的计数按路径刷新
//   （useFolderSummaries.refreshForFolders）；超过 500 个文件的事件没有 file_ids，按文件夹整体刷新列表和详情。
//   `folders.changed` 刷新树和全部已加载的计数；`resync`、断线重连后整体刷新。去抖：最后一条事件后 300 ms，
//   最多等 1 秒（和 /files 一样）。
//   停用、启用用户和项目授权不发项目事件："分给谁"在 files.changed 和窗口重新拿到焦点时刷新。
// - 所有图片都经 image-info store（batch）和 FileCover。
// - 浏览目录树和文件要 files 或 scan 菜单（children 接口），看日志要 files 菜单；内置的项目负责人都有。
import { LayoutGridIcon, ListIcon, LoaderCircleIcon, RotateCcwIcon } from '@lucide/vue'
import { useEventListener } from '@vueuse/core'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import { getFolder } from '@/api/folders'
import { errorMessage, HANDLE_ALL, isApiError, isClientError, toastOpError } from '@/api/http'
import {
  assignReviews,
  getFileReviews,
  getReviewSummary,
  listOpenAssignments,
  listReviewers,
  MAX_REVIEW_FILE_IDS,
  MAX_REVIEW_FOLDER_FILES,
  reassignMany,
  reassignOne,
  reopenFile,
} from '@/api/reviews'
import type {
  AssignmentReviewer,
  FileEntry,
  MyProject,
  ProjectEvent,
  ProjectReviewer,
  ReviewStatusCounts,
  ReviewSummary,
  ReviewStatus,
} from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import FolderTree from '@/components/files/FolderTree.vue'
import { Button } from '@/components/ui/button'
import StreamStatus from '@/components/app/StreamStatus.vue'
import { useFolderBrowser } from '@/composables/useFolderBrowser'
import { useFolderTree } from '@/composables/useFolderTree'
import { useEventBatch, useProjectStream } from '@/composables/useProjectStream'
import { useSelection } from '@/composables/useSelection'
import { REVIEW_STATUS } from '@/lib/files'
import { formatCount } from '@/lib/format'
import { useImageInfoStore } from '@/stores/image-info'
import { useProjectStore } from '@/stores/project'
import { useSessionStore } from '@/stores/session'
import { type Entry, toEntries, useViewMode } from '@/components/files/entries'
import FileGrid from '@/components/files/FileGrid.vue'
import { chunk } from '@/lib/utils'
import { useFolderSummaries } from '@/composables/useFolderSummaries'

import AssignPanel from './AssignPanel.vue'
import ReassignDialog from './ReassignDialog.vue'
import { reviewOpErrorText } from './review-errors'

const project = useProjectStore()
const session = useSessionStore()
const images = useImageInfoStore()
const route = useRoute()
const router = useRouter()
const tree = useFolderTree()
/** 浏览目录树和文件（children 接口）要 files 或 scan 菜单。 */
const canBrowse = computed(() => session.menuCodes.has('files') || session.menuCodes.has('scan'))
/** 按文件夹的子树计数（review-summary，M2-5；树每加载一层取一次）。 */
const counts = useFolderSummaries<ReviewStatusCounts, ReviewSummary>(() => project.currentId, getReviewSummary, {
  canLookup: () => canBrowse.value,
})
/** 文件日志要 files 菜单。 */
const canSeeLogs = computed(() => session.menuCodes.has('files'))

/** 视图（记在 localStorage，只是个人习惯，读写失败就用默认）。 */
const view = useViewMode('doclite.review-assign.view')

// ---------- 状态筛选 ----------

type StatusFilter = 'all' | ReviewStatus
const STATUS_ORDER: ReviewStatus[] = ['unassigned', 'pending', 'approved', 'rejected']
const statusFilter = ref<StatusFilter>('all')

// ---------- 当前文件夹和列表 ----------

const folderQuery = computed(() => (typeof route.query.folder === 'string' ? route.query.folder : null))
/** 组件还在（卸载后在途的回调不再改地址）。 */
let alive = true
onBeforeUnmount(() => {
  alive = false
})

const browser = useFolderBrowser({
  tree,
  rootId: () => project.current?.root_folder_id ?? null,
  fetchFolder: (id) => getFolder(id, ['not_found']),
  onSwitch() {
    selection.clear()
    problemReviewers.value = []
    openReviewers.value = null
  },
  async accept(detail) {
    if (detail.project_id === project.current?.id) return true
    // 别的项目里的文件夹：回到当前项目的根（审核分配页不自动切项目）。
    await router.replace({ query: {} })
    return false
  },
  onOpened(detail) {
    void counts.load(detail.id).catch(() => null)
    void loadProblems()
  },
  async onOpenFailed(e, id) {
    if (!isApiError(e, 'not_found') || id === project.current?.root_folder_id) return false
    toast.error('文件夹不存在或已被删除，已回到根文件夹')
    await router.replace({ query: {} })
    return true
  },
  onGone(parent) {
    toast.info('当前文件夹已被删除，已回到上一级')
    void navigate(parent, true)
  },
  onListLoaded: (id) => void loadOpenReviewers(id),
})
const { folder, state: listState, error: listError } = browser
/** 当前文件夹里直接放的文件（null：还没加载）。 */
const files = computed(() => browser.children.value?.files ?? null)

/** batch 说不存在、无权访问、已删除的文件从列表里移除（design §4.4）。 */
const presentFiles = computed(() => (files.value ?? []).filter((f) => !images.isMissing(f.file_id)))
const statusCounts = computed(() => {
  const c: Record<StatusFilter, number> = { all: 0, unassigned: 0, pending: 0, approved: 0, rejected: 0 }
  for (const f of presentFiles.value) {
    c.all++
    c[f.review_status]++
  }
  return c
})
const entries = computed<Entry[]>(() =>
  toEntries(
    [],
    statusFilter.value === 'all'
      ? presentFiles.value
      : presentFiles.value.filter((f) => f.review_status === statusFilter.value),
  ),
)
const selection = useSelection(() => entries.value.map((e) => e.key))
watch(entries, () => selection.prune())
const selectedFiles = computed(() =>
  entries.value.flatMap((e) => (e.kind === 'file' && selection.isSelected(e.key) ? [e.file] : [])),
)
/**
 * 当前文件夹里直接放的文件各自 open 分配的审核人（file_id → reviewer_id，loadOpenReviewers）；null：还没取到或取失败。
 * 给"改派给 X"去掉已经是 X 在审的（M2-8 验收：原来这些也算进按钮的个数，点了 server 只是跳过）。
 */
const openReviewers = ref<Map<string, string> | null>(null)
let openReviewersSeq = 0
/** 选中的里能改派给右栏选中的人的：待审、而且现在不是他在审（不知道审核人的照样算，server 会跳过已经分给他的）。 */
const reassignableFiles = computed(() => {
  const to = target.value
  const map = openReviewers.value
  return selectedFiles.value.filter((f) => f.review_status === 'pending' && !(to && map?.get(f.file_id) === to))
})

const rootId = computed(() => project.current?.root_folder_id ?? null)
/** 当前文件夹的名字（根文件夹的名字就是项目名）。 */
const folderName = computed(() => {
  const f = folder.value
  if (!f) return null
  return f.parent_id ? f.folder_name : (project.current?.project_name ?? f.folder_name)
})
const rootCounts = computed(() => counts.get(rootId.value))
const folderCounts = computed(() => counts.get(folder.value?.id))

/** 右栏的分配记录、日志重新取的信号。 */
const detailRefresh = ref(0)
/** 改派对话框重新加载的信号。 */
const reassignRefresh = ref(0)

/** 可以分给的人（null：还在加载）和右栏选中的那个（分配、改派的目标）。 */
const reviewers = ref<ProjectReviewer[] | null>(null)
const target = ref<string | null>(null)
let reviewersSeq = 0
/** 当前文件夹子树里有待审、但已不能再审的人（右栏提醒）。 */
const problemReviewers = ref<AssignmentReviewer[]>([])
let problemsSeq = 0
const busy = ref(false)
/** 最近一次分配、改派的结果（右栏显示，含"跳过了 n 个"）。 */
const lastResult = ref<string | null>(null)

function resetForProject(p: MyProject) {
  browser.reset()
  selection.clear()
  openReviewers.value = null
  lastResult.value = null
  reviewers.value = null
  problemReviewers.value = []
  tree.reset({ id: p.root_folder_id, folder_name: p.project_name })
  counts.reset(p.root_folder_id)
  void counts.load(p.root_folder_id).catch(() => null)
  void loadReviewers()
}

let lastProjectId: string | null = null
watch(
  [() => project.current?.id ?? null, folderQuery, canBrowse],
  async ([pid, q, browse]) => {
    const p = project.current
    if (!pid || !p || !browse) {
      lastProjectId = null
      return
    }
    if (pid !== lastProjectId) {
      const switched = lastProjectId !== null
      lastProjectId = pid
      resetForProject(p)
      if (switched && q) {
        // 用户在切换器里换了项目：回到新项目的根文件夹（地址变了会再进来一次）。
        await router.replace({ query: {} })
        return
      }
    }
    await browser.open(q ?? p.root_folder_id)
  },
  { immediate: true },
)

/**
 * 取当前文件夹里直接放的文件的 open 分配（审核人）。review-assignments 按所在文件夹的 path_code 排，文件夹自己的编码
 * 排在它的子文件夹前面，所以翻到出现别的文件夹就停，不把整棵子树翻完。取不到时为 null（按钮按全部待审计数）。
 */
async function loadOpenReviewers(id: string) {
  const pid = project.currentId
  if (!pid) return
  const my = ++openReviewersSeq
  try {
    const map = new Map<string, string>()
    let cursor: string | null = null
    for (;;) {
      const res = await listOpenAssignments(pid, { folder_id: id, limit: 2000, cursor }, HANDLE_ALL)
      if (my !== openReviewersSeq || folder.value?.id !== id) return
      let passed = false
      for (const it of res.items) {
        if (it.folder_id !== id) {
          passed = true
          break
        }
        map.set(it.file_id, it.reviewer_id)
      }
      if (passed || !res.next_cursor) break
      cursor = res.next_cursor
    }
    openReviewers.value = map
  } catch {
    if (my === openReviewersSeq) openReviewers.value = null
  }
}

function navigate(id: string | null, replace = false) {
  if (!alive) return Promise.resolve()
  const to = { query: id && id !== rootId.value ? { folder: id } : {} }
  return replace ? router.replace(to) : router.push(to)
}

// 没有 files、scan 菜单（只有 review_assign）：浏览不了目录，但页头的四个计数照样用 review-summary（只要
// review_assign）填上；实时事件来了刷新它（见 applyEvents）。
watch(
  [() => project.current?.id ?? null, canBrowse],
  ([pid, browse]) => {
    const p = project.current
    if (!pid || !p || browse) return
    counts.reset(p.root_folder_id)
    void counts.load(p.root_folder_id).catch(() => null)
  },
  { immediate: true },
)

// 树每加载一层（展开、定位），取这一层的计数（子文件夹的子树待分配数）。
watch(
  () => [...tree.childIds.keys()],
  (ids) => counts.ensure(ids),
)

// ---------- 审核人 ----------

async function loadReviewers() {
  const pid = project.currentId
  if (!pid) return
  const my = ++reviewersSeq
  try {
    const res = await listReviewers(pid, HANDLE_ALL)
    if (my !== reviewersSeq || project.currentId !== pid) return
    reviewers.value = res.reviewers
    // 选中的人已经不能再分配了（停用、失去授权）：取消选中。
    if (target.value && !res.reviewers.some((r) => r.id === target.value)) target.value = null
  } catch (e) {
    if (my !== reviewersSeq) return
    if (!reviewers.value) {
      reviewers.value = []
      toast.error(`取不到审核人列表：${errorMessage(e)}`)
    }
  }
}

const targetReviewer = computed(() => reviewers.value?.find((r) => r.id === target.value) ?? null)

/** 取当前文件夹子树里 open 分配的审核人汇总（只要 reviewers，items 只取 1 条）。 */
async function loadProblems() {
  const pid = project.currentId
  const fid = folder.value?.id
  if (!pid || !fid) return
  const my = ++problemsSeq
  try {
    const res = await listOpenAssignments(pid, { folder_id: fid, limit: 1 }, HANDLE_ALL)
    if (my !== problemsSeq || folder.value?.id !== fid) return
    problemReviewers.value = res.reviewers.filter((r) => !r.can_review)
  } catch {
    // 只是提醒，取不到就不显示。
  }
}

// ---------- 分配、改派、撤回通过 ----------

/**
 * 操作之后立即刷新受影响的部分（SSE 的事件随后也会到；实时连接断开时就靠这里）。
 * `subtree`：改动在这些文件夹的整棵子树里（按文件夹分配、按子树改派），下面已加载的文件夹的计数也刷新。
 */
function afterChange(folderIds: string[], opts: { subtree?: boolean } = {}) {
  if (opts.subtree) void counts.refreshSubtree(folderIds)
  else void counts.refreshForFolders(folderIds)
  void loadReviewers()
  void loadProblems()
  // 当前文件夹的列表总是重新取（按子树改派、分配时，改动可能在子文件夹里，也可能在这里）。
  if (folder.value) void browser.reload()
  detailRefresh.value++
}

/**
 * 按文件夹分配的确认框。打开时记下文件夹和审核人，确认时只用记下的（审查第 1 轮：确认框开着时当前文件夹被删、
 * 用户后退，不能变成分配别的文件夹）；当前文件夹一变就关掉它（见下面的 watch）。
 */
const confirmFolder = reactive({
  open: false,
  loading: false,
  count: 0,
  name: '',
  folderId: '',
  reviewerId: '',
  reviewer: '',
})

function askAssignFolder() {
  const to = targetReviewer.value
  if (!to || !folder.value) return
  confirmFolder.count = folderCounts.value?.unassigned ?? 0
  confirmFolder.name = folderName.value ?? ''
  confirmFolder.folderId = folder.value.id
  confirmFolder.reviewerId = to.id
  confirmFolder.reviewer = to.display_name
  confirmFolder.open = true
}

async function doAssignFolder() {
  const pid = project.currentId
  const { folderId, reviewerId, reviewer, name } = confirmFolder
  if (!pid || !folderId || !reviewerId || confirmFolder.loading) return
  if (folder.value?.id !== folderId) {
    // 当前文件夹已经变了（watch 还没来得及关掉确认框）：不发请求。
    confirmFolder.open = false
    return
  }
  confirmFolder.loading = true
  try {
    const res = await assignReviews(pid, { reviewer_id: reviewerId, folder_id: folderId }, HANDLE_ALL)
    confirmFolder.open = false
    lastResult.value = `已把「${name}」（含子文件夹）的 ${res.assigned} 个待分配文件分给 ${reviewer}${
      res.skipped ? `；另有 ${res.skipped} 个已分配或已审的没有改动` : ''
    }`
    toast.success(`已分配 ${res.assigned} 个给 ${reviewer}`)
    afterChange([folderId], { subtree: true })
  } catch (e) {
    toastOpError(e, reviewOpErrorText(e))
    // 400（计数过时、实际超过 1 万个，或审核人刚变得不合格）、404（文件夹没了）：前提已经不成立，
    // 关掉确认框、刷新计数；网络错误、5xx 留着确认框可以重试。
    if (isClientError(e)) {
      confirmFolder.open = false
      afterChange([folderId], { subtree: true })
    }
  } finally {
    confirmFolder.loading = false
  }
}

/** 按 500 个一批依次调用；中途出错时抛出（已经完成的批次由 onDone 记下）。 */
async function inChunks<T>(ids: string[], call: (chunk: string[]) => Promise<T>, onDone: (r: T, n: number) => void) {
  for (const part of chunk(ids, MAX_REVIEW_FILE_IDS)) onDone(await call(part), part.length)
}

async function assignSelected() {
  const to = targetReviewer.value
  const pid = project.currentId
  const cur = folder.value
  const ids = selectedFiles.value.map((f) => f.file_id)
  if (!to || !pid || !cur || !ids.length || busy.value) return
  busy.value = true
  let assigned = 0
  let skipped = 0
  let done = 0
  try {
    await inChunks(
      ids,
      (chunk) => assignReviews(pid, { reviewer_id: to.id, file_ids: chunk }, HANDLE_ALL),
      (r, n) => {
        assigned += r.assigned
        skipped += r.skipped
        done += n
      },
    )
    lastResult.value = `已分配 ${assigned} 个给 ${to.display_name}${skipped ? `，跳过了 ${skipped} 个（不是待分配的）` : ''}`
    toast.success(
      `已分配 ${assigned} 个给 ${to.display_name}${skipped ? `，跳过了 ${skipped} 个` : ''}`,
      skipped ? { description: '跳过的不是待分配的文件（已分配、已审，或刚被别人分配）；已分配的要改派' } : undefined,
    )
    selection.clear()
  } catch (e) {
    const partial = done ? `前 ${done} 个已处理（分配 ${assigned} 个、跳过 ${skipped} 个），后面的没有分配` : undefined
    lastResult.value = done
      ? `已处理 ${done} 个（分配给 ${to.display_name} ${assigned} 个、跳过 ${skipped} 个），之后失败：${reviewOpErrorText(e)}`
      : null
    toastOpError(e, reviewOpErrorText(e), partial)
  } finally {
    busy.value = false
    afterChange([cur.id])
  }
}

async function reassignSelected() {
  const to = targetReviewer.value
  const pid = project.currentId
  const cur = folder.value
  const ids = reassignableFiles.value.map((f) => f.file_id)
  if (!to || !pid || !cur || !ids.length || busy.value) return
  busy.value = true
  let reassigned = 0
  let skipped = 0
  let done = 0
  try {
    await inChunks(
      ids,
      (chunk) => reassignMany(pid, { to_reviewer_id: to.id, file_ids: chunk }, HANDLE_ALL),
      (r, n) => {
        reassigned += r.reassigned
        skipped += r.skipped
        done += n
      },
    )
    lastResult.value = `已改派 ${reassigned} 个给 ${to.display_name}${skipped ? `，跳过了 ${skipped} 个（已经分给他的，或刚被审完、改派）` : ''}`
    toast.success(`已改派 ${reassigned} 个给 ${to.display_name}${skipped ? `，跳过了 ${skipped} 个` : ''}`)
  } catch (e) {
    const partial = done ? `前 ${done} 个已处理（改派 ${reassigned} 个），后面的没有改派` : undefined
    lastResult.value = done
      ? `已处理 ${done} 个（改派给 ${to.display_name} ${reassigned} 个、跳过 ${skipped} 个），之后失败：${reviewOpErrorText(e)}`
      : null
    toastOpError(e, reviewOpErrorText(e), partial)
  } finally {
    busy.value = false
    afterChange([cur.id])
  }
}

async function reassignSingle(assignmentId: string, file: FileEntry) {
  const to = targetReviewer.value
  const cur = folder.value
  if (!to || !cur || busy.value) return
  busy.value = true
  try {
    const res = await reassignOne(assignmentId, to.id, HANDLE_ALL)
    if (res.changed) {
      lastResult.value = `已把「${file.file_name}」改派给 ${to.display_name}`
      toast.success(lastResult.value)
    } else toast.info(`「${file.file_name}」原来就分给 ${to.display_name}，没有改动`)
  } catch (e) {
    toastOpError(
      e,
      isApiError(e, 'invalid_state') ? '这个文件的分配刚刚变了（可能已审完或被改派），已刷新' : reviewOpErrorText(e),
    )
  } finally {
    busy.value = false
    afterChange([cur.id])
  }
}

const confirmReopen = reactive({ open: false, loading: false, file: null as FileEntry | null, folderId: '' })

function askReopen(file: FileEntry) {
  confirmReopen.file = file
  confirmReopen.folderId = folder.value?.id ?? ''
  confirmReopen.open = true
}

async function doReopen() {
  const file = confirmReopen.file
  const folderId = confirmReopen.folderId
  if (!file || !folderId || confirmReopen.loading) return
  confirmReopen.loading = true
  try {
    const res = await reopenFile(file.file_id, HANDLE_ALL)
    confirmReopen.open = false
    if (res.reviewer_id) {
      // 审核人的名字：右栏的列表可能是旧的（他刚被重新启用、授权），取不到时查这个文件的分配记录。
      let name = reviewers.value?.find((r) => r.id === res.reviewer_id)?.display_name
      if (!name) {
        try {
          name = (await getFileReviews(file.file_id, HANDLE_ALL)).reviews.find((r) => r.status === 'open')?.reviewer_name
        } catch {
          // 只是为了提示里的名字。
        }
      }
      name ??= '原审核人'
      lastResult.value = `已撤回「${file.file_name}」的通过，重新分给 ${name}（第 ${res.review_round} 轮）`
      toast.success(lastResult.value)
    } else {
      lastResult.value = `已撤回「${file.file_name}」的通过。通过它的审核人已不能再审这个项目，文件改为待分配`
      toast.warning(lastResult.value)
    }
  } catch (e) {
    toastOpError(
      e,
      isApiError(e, 'invalid_state') ? '这个文件已经不是"通过"了（可能刚被撤回），已刷新' : reviewOpErrorText(e),
    )
    // 409（已经不是通过）、404（文件没了）：关掉确认框；网络错误、5xx 留着可以重试。
    if (isClientError(e)) confirmReopen.open = false
  } finally {
    confirmReopen.loading = false
    afterChange([folderId])
  }
}

// 当前文件夹变了（点树、后退、当前文件夹被删回到上一级）：关掉针对原文件夹的确认框，清掉上一次操作的结果。
// 改派对话框自己跟着 folder 的 id 关闭（ReassignDialog）。
watch(
  () => folder.value?.id ?? null,
  (id) => {
    if (confirmFolder.open && !confirmFolder.loading && confirmFolder.folderId !== id) confirmFolder.open = false
    if (confirmReopen.open && !confirmReopen.loading && confirmReopen.folderId !== id) confirmReopen.open = false
    lastResult.value = null
  },
)

const reassignOpen = ref(false)

// ---------- 实时事件 ----------

const pendingFolders = new Set<string>()
const pendingFiles = new Set<string>()
/** folders.changed 带的文件夹（树要刷新）。 */
const pendingTreeFolders = new Set<string>()
let pendingFull = false
// 去抖：最后一条事件后 300 ms，最多等 1 秒；连续不断（上传、批量分配期间）时最多等 3 秒（children 只能整个文件夹重取）。
const batch = useEventBatch(applyEvents, { burstMaxWaitMs: 3000 })

function onEvent(e: ProjectEvent) {
  if (e.type === 'resync') pendingFull = true
  else if (e.type === 'files.changed') {
    e.folder_ids?.forEach((id) => pendingFolders.add(id))
    e.file_ids?.forEach((id) => pendingFiles.add(id))
  } else if (e.type === 'folders.changed') {
    e.folder_ids?.forEach((id) => {
      pendingFolders.add(id)
      pendingTreeFolders.add(id)
    })
  } else return
  batch.schedule()
}

async function applyEvents() {
  if (pendingFull) {
    pendingFull = false
    pendingFolders.clear()
    pendingFiles.clear()
    pendingTreeFolders.clear()
    await refreshAll()
    return
  }
  const folders = new Set(pendingFolders)
  const fileIds = new Set(pendingFiles)
  const treeFolders = new Set(pendingTreeFolders)
  pendingFolders.clear()
  pendingFiles.clear()
  pendingTreeFolders.clear()

  if (!canBrowse.value) {
    // 只有页头的计数（没有 files、scan 菜单）。
    void counts.refreshAll()
    return
  }
  if (fileIds.size) images.invalidate(fileIds)
  // 计数：文件夹结构变了（移动、删除文件夹）时丢掉可能过时的 parent 记录、整体刷新已加载的；否则沿路径刷新祖先。
  if (treeFolders.size) {
    counts.forgetUnloadedParents()
    void counts.refreshAll()
  } else void counts.refreshForFolders(folders)
  void loadReviewers()
  void loadProblems()
  reassignRefresh.value++

  const cur = folder.value
  const listAffected =
    !!cur && (folders.has(cur.id) || !!files.value?.some((f) => fileIds.has(f.file_id)))
  if (treeFolders.size) void tree.refresh(treeFolders, listAffected ? cur?.id : null)
  if (!cur) return
  if (listAffected) void browser.reload()
  // 右栏：事件带了 file_ids 时看有没有选中的那个文件；超过 500 个时只带 folder_ids，看当前文件夹。
  const single = selectedFiles.value.length === 1 ? selectedFiles.value[0]! : null
  if (single && (fileIds.has(single.file_id) || (fileIds.size === 0 && folders.has(cur.id)))) detailRefresh.value++
}

/** 整体刷新：resync、断线重连之后（断开期间的事件不会补发）。 */
async function refreshAll() {
  if (!canBrowse.value) {
    await counts.refreshAll()
    return
  }
  images.invalidate()
  detailRefresh.value++
  reassignRefresh.value++
  void loadReviewers()
  void loadProblems()
  if (!folder.value) {
    const p = project.current
    if (p && listState.value !== 'loading') void browser.open(folderQuery.value ?? p.root_folder_id)
    await Promise.all([tree.refreshAll(), counts.refreshAll()])
    return
  }
  await Promise.all([tree.refreshAll(folder.value.id), browser.reload(), counts.refreshAll()])
}

const stream = useProjectStream(() => project.currentId, {
  onEvent,
  onOpen: (first) => {
    if (!first) {
      pendingFull = true
      batch.schedule()
    } else if (!canBrowse.value) {
      void counts.refreshAll()
    } else if (browser.loaded()) {
      // 第一次连上时页面已经加载好了：补刷一次（加载和连接建立之间可能漏了事件）。
      void browser.reload()
      void counts.refreshAll()
      void loadReviewers()
    }
  },
})

onMounted(() => {
  void project.ensureLoaded()
})

// 停用、启用用户和项目授权不发项目事件：窗口重新拿到焦点时刷新一次"分给谁"和不能再审的人。
useEventListener(window, 'focus', () => {
  if (!project.currentId || !canBrowse.value) return
  void loadReviewers()
  void loadProblems()
})

</script>

<template>
  <div
    class="flex min-h-0 flex-1 flex-col"
    data-testid="review-assign-page"
    :data-stream="stream.state.value"
    :data-list="listState"
  >
    <header class="flex h-14 shrink-0 items-center gap-3 border-b px-5">
      <h1 class="shrink-0 text-base font-semibold tracking-tight text-foreground">审核分配</h1>
      <div v-if="project.current" class="flex min-w-0 items-center gap-1.5 border-l pl-3" data-testid="project-counts">
        <span
          v-for="s in STATUS_ORDER"
          :key="s"
          class="inline-flex items-center gap-1 rounded-full px-2 text-xs leading-6"
          :class="REVIEW_STATUS[s].badge"
          :data-testid="`count-${s}`"
          :data-count="rootCounts?.[s] ?? ''"
        >
          <span class="size-1.5 rounded-full" :class="REVIEW_STATUS[s].dot" />
          {{ REVIEW_STATUS[s].label }} {{ formatCount(rootCounts?.[s]) }}
        </span>
      </div>
      <div class="flex-1" />
      <StreamStatus v-if="project.current" :state="stream.state.value" />
      <Button
        v-if="project.current && canBrowse"
        variant="outline"
        size="sm"
        :disabled="!folder"
        data-testid="open-reassign"
        @click="reassignOpen = true"
      >
        <RotateCcwIcon />改派…
      </Button>
      <div v-if="canBrowse" class="flex shrink-0 rounded-md bg-muted p-0.5" role="radiogroup" aria-label="视图">
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

    <!-- 没有浏览文件目录的菜单 -->
    <div v-if="!canBrowse" class="flex flex-1 items-center justify-center p-6 text-sm text-muted-foreground">
      <div class="max-w-sm text-center" data-testid="review-assign-no-browse">
        <div class="text-base font-medium text-foreground">需要「文件目录」菜单</div>
        <p class="mt-1">审核分配要浏览文件夹和文件。请联系管理员给你的角色加上「文件目录」或「扫描上传」菜单。</p>
      </div>
    </div>

    <!-- 项目还在加载 / 没有能进的项目 -->
    <div v-else-if="!project.current" class="flex flex-1 items-center justify-center p-6 text-sm text-muted-foreground">
      <span v-if="project.state === 'loading' || project.state === 'idle'" class="flex items-center gap-2">
        <LoaderCircleIcon class="size-4 animate-spin" />正在加载项目…
      </span>
      <div v-else class="max-w-sm text-center">
        <div class="text-base font-medium text-foreground">没有可进入的项目</div>
        <p class="mt-1">请联系管理员在「系统管理 › 项目」里给你授权，或在左上角选择一个项目。</p>
      </div>
    </div>

    <div v-else class="flex min-h-0 flex-1">
      <!-- 左：目录树和每个文件夹（含子文件夹）的待分配数 -->
      <aside class="flex w-60 shrink-0 flex-col border-r bg-sidebar/60">
        <div class="px-3 pt-2.5 pb-1 text-xs font-medium text-muted-foreground">按文件夹看待分配</div>
        <div class="min-h-0 flex-1 overflow-y-auto px-2 pb-2.5">
          <FolderTree
            :tree="tree"
            :selected-id="folder?.id ?? folderQuery ?? project.current.root_folder_id"
            @select="navigate"
          >
            <template #suffix="{ folder: f }">
              <span
                v-if="counts.get(f.id)"
                class="shrink-0 font-mono text-[11px] tabular-nums"
                :class="counts.get(f.id)!.unassigned > 0 ? 'font-medium text-foreground' : 'text-muted-foreground/60'"
                :title="`含子文件夹：待分配 ${counts.get(f.id)!.unassigned}、待审 ${counts.get(f.id)!.pending}、通过 ${counts.get(f.id)!.approved}、驳回 ${counts.get(f.id)!.rejected}`"
                data-testid="tree-unassigned"
                :data-count="counts.get(f.id)!.unassigned"
              >{{ counts.get(f.id)!.unassigned.toLocaleString() }}</span>
            </template>
          </FolderTree>
        </div>
      </aside>

      <!-- 中：当前文件夹里的文件 -->
      <section class="relative flex min-w-0 flex-1 flex-col">
        <div class="flex h-10 shrink-0 items-center gap-2 border-b px-4 text-xs text-muted-foreground" data-testid="folder-summary">
          <span class="max-w-64 truncate font-medium text-foreground" :title="folderName ?? ''">{{ folderName ?? '…' }}</span>
          <span v-if="folderCounts" class="shrink-0" data-testid="folder-unassigned" :data-count="folderCounts.unassigned">
            · 含子文件夹 {{ folderCounts.unassigned.toLocaleString() }} 个待分配
          </span>
          <div class="flex-1" />
          <div class="flex shrink-0 items-center gap-0.5 rounded-md bg-muted p-0.5" role="radiogroup" aria-label="按状态筛选">
            <button
              v-for="s in (['all', ...STATUS_ORDER] as StatusFilter[])"
              :key="s"
              type="button"
              role="radio"
              :aria-checked="statusFilter === s"
              class="rounded px-2 py-0.5"
              :class="statusFilter === s ? 'bg-background font-medium text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'"
              :data-testid="`status-filter-${s}`"
              :data-count="statusCounts[s]"
              @click="statusFilter = s"
            >
              {{ s === 'all' ? '全部' : REVIEW_STATUS[s].label }} {{ statusCounts[s] }}
            </button>
          </div>
        </div>

        <div class="flex min-h-0 flex-1 flex-col" data-testid="file-area">
          <div v-if="listState === 'error' && !files" class="flex flex-1 flex-col items-center justify-center gap-3 text-sm">
            <p class="text-destructive">{{ listError ?? '加载失败' }}</p>
            <Button variant="outline" size="sm" @click="browser.open(folderQuery ?? project.current.root_folder_id)">重试</Button>
          </div>
          <div v-else-if="!files" class="flex flex-1 items-center justify-center gap-2 text-sm text-muted-foreground">
            <LoaderCircleIcon class="size-4 animate-spin" />正在加载…
          </div>
          <div
            v-else-if="entries.length === 0"
            class="flex flex-1 flex-col items-center justify-center gap-1 px-6 text-center text-sm text-muted-foreground"
            data-testid="folder-empty"
          >
            <template v-if="presentFiles.length === 0">
              <span>这个文件夹里没有直接放的文件</span>
              <span v-if="folderCounts && folderCounts.total > 0" class="text-xs">
                子文件夹里共 {{ folderCounts.total.toLocaleString() }} 个文件（{{ folderCounts.unassigned.toLocaleString() }} 个待分配）：
                在左边选子文件夹，或者在右边把整个文件夹分给一个人
              </span>
            </template>
            <span v-else>没有「{{ statusFilter === 'all' ? '全部' : REVIEW_STATUS[statusFilter].label }}」的文件</span>
          </div>
          <FileGrid v-else :entries="entries" :view="view" :selection="selection" />
        </div>

        <div
          v-if="selection.count.value > 0"
          class="flex h-9 shrink-0 items-center gap-2 border-t bg-muted/60 px-4 text-xs text-foreground"
          data-testid="selection-bar"
        >
          <span>已选 {{ selection.count.value }} 个文件</span>
          <span class="text-muted-foreground">Shift 选区间，Ctrl/⌘ 加选，Ctrl/⌘+A 全选</span>
          <div class="flex-1" />
          <button type="button" class="text-muted-foreground hover:text-foreground" @click="selection.clear()">
            取消选择（Esc）
          </button>
        </div>
      </section>

      <!-- 右：分给谁、操作、选中文件的分配记录和日志 -->
      <aside class="w-80 shrink-0 overflow-y-auto border-l">
        <AssignPanel
          v-model:target="target"
          :reviewers="reviewers"
          :folder-name="folderName"
          :folder-unassigned="folderCounts?.unassigned ?? null"
          :folder-limit="MAX_REVIEW_FOLDER_FILES"
          :selected-files="selectedFiles"
          :reassignable-count="reassignableFiles.length"
          :problem-reviewers="problemReviewers"
          :busy="busy"
          :last-result="lastResult"
          :refresh-token="detailRefresh"
          :can-see-logs="canSeeLogs"
          @assign-folder="askAssignFolder"
          @assign-selected="assignSelected"
          @reassign-selected="reassignSelected"
          @reassign-one="reassignSingle"
          @reopen="askReopen"
          @open-reassign="reassignOpen = true"
        />
      </aside>
    </div>

    <ConfirmDialog
      v-model:open="confirmFolder.open"
      :title="`把「${confirmFolder.name}」分给 ${confirmFolder.reviewer}？`"
      :description="`含子文件夹，共 ${confirmFolder.count.toLocaleString()} 个待分配。已分配、已审的文件不受影响；每个文件都会记一条分配日志。`"
      confirm-text="分配"
      :loading="confirmFolder.loading"
      @confirm="doAssignFolder"
    />
    <ConfirmDialog
      v-model:open="confirmReopen.open"
      :title="`撤回「${confirmReopen.file?.file_name ?? ''}」的通过？`"
      description="文件回到待审核，重新分给通过它的审核人；他已不能再审这个项目时改为待分配。撤回会记进文件的操作日志。"
      confirm-text="撤回通过"
      destructive
      :loading="confirmReopen.loading"
      @confirm="doReopen"
    />
    <ReassignDialog
      v-if="project.current"
      v-model:open="reassignOpen"
      v-model:target="target"
      :project-id="project.current.id"
      :folder="folder ? { id: folder.id, name: folderName ?? folder.folder_name } : null"
      :targets="reviewers"
      :refresh-token="reassignRefresh"
      @changed="(ids: string[], subtree: boolean) => afterChange(ids, { subtree })"
    />
  </div>
</template>
