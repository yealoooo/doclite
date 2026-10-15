// 审核工作台的待办列表（M2-6）：`GET /api/me/reviews`（M2-2）的 keyset 分页，加上实时事件之后的局部更新。
//
// - 范围：当前项目（跟随左上角的项目切换，Q12）、可选的文件夹子树（地址栏 `?folder=`）、状态（全部 / 待审 / 通过 /
//   驳回）、"DPI 不足"。范围一变就重新从第一页取。
// - 分页：每页 PAGE_SIZE 个，跟着 `next_cursor` 往后加载（列表滚到快到底时 `autoLoadMore`，键盘翻到最后一个时
//   `loadMore`，返回有没有进展；在途时返回同一个 Promise）；追加时按 file_id 去重（翻页期间移动了文件，keyset 可能
//   重复，M2-2）；整体重拉期间不加载下一页，cursor 变了的那一页丢掉。加载失败记在 `loadMoreError`（列表底部显示、可以
//   重试），5 秒内自动加载不再发。计数、文件夹、其他项目的待审数取最近一次响应的。
// - 我的审核每次 75–140 ms 且每页都重算计数（M2-2 实测），所以实时事件不整体重拉：
//   - 事件里的 file_ids 在列表里的：逐个取 `GET /api/files/{fid}/reviews`（约 2 ms），按"最近一次分给我、没取消的那
//     一行"更新这一行；没有这样的行（被改派走了）、文件删了（404）、进不了项目了（403）就从列表里拿掉。计数按
//     这一行的前后变化在本地加减（`countsDelta`）。超过 REFRESH_FILES_MAX 个时整体重拉。
//   - 不在列表里的 file_ids、只带 folder_ids 的事件（超过 500 个文件）：可能有新分给我的、或者被筛掉的那几种状态变了。
//     节流（COUNTS_CHECK_MS 内最多一次）取一次只要 1 行的"我的审核"核对计数，和本地的计数对不上才整体重拉。
//   - 只带 folder_ids 的事件碰到列表里的文件夹：整体重拉（不知道是哪几个文件）。
//   整体重拉时一次取回已经加载的那么多行（最多 2000），正在看的文件（`keep`）不在里面就接着往后取（最多 5 页），
//   全部取完才替换列表；列表的位置由页面按 file_id 保持。逐个刷新时版本变了（重扫，dpi 可能变了）也整体重拉。
// - 本页自己的操作（通过、驳回、批量通过）成功后先按响应更新这一行和计数（`applyReviewed`），之后的事件再按上面的
//   规则核对一遍；在那之前发出的整体重拉，响应里的这一行用本地的（不会把刚通过的又显示成待审）。逐个刷新拿到的是
//   server 的新状态，比它早的本地记录作废；整体重拉替换完之后，对在它发出之后逐个刷新过的文件再刷新一次（验收第 1 轮：
//   两者互相覆盖）。
// - 核对计数时同时比较分文件夹的计数（一个文件夹进、一个文件夹出，总数会抵消）。
// - 整体重拉失败而列表里还有内容时，`error` 有值（页面显示提示和"重试"），5 秒后自动再试一次。
// - 文件改名不会反映到列表里（分配记录接口不带文件名），等下一次整体重拉。

import { computed, ref, shallowRef } from 'vue'

import { isApiError } from '@/api/http'
import { getFileReviews, listMyReviews } from '@/api/reviews'
import type {
  FileReviews,
  MyReviewCounts,
  MyReviewFolder,
  MyReviewItem,
  MyReviewState,
  MyReviews,
  OtherProjectOpen,
  ReviewedFile,
} from '@/api/types'

/** 每页多少个（M2-2 的默认值）。 */
export const PAGE_SIZE = 500
/** 整体重拉时一次最多取多少个（接口的上限）。 */
const RELOAD_MAX = 2000
/** 一批事件里在列表里的文件超过这么多就整体重拉（逐个取每个文件的分配记录不划算）。 */
export const REFRESH_FILES_MAX = 40
/** 同时取几个文件的分配记录。 */
const REFRESH_CONCURRENCY = 6
/** 核对计数的节流间隔。 */
export const COUNTS_CHECK_MS = 3000

export interface ReviewScope {
  projectId: string
  folderId: string | null
  state: MyReviewState
  lowDpi: boolean
}

export type ListState = 'idle' | 'loading' | 'ready' | 'error'

function emptyCounts(): MyReviewCounts {
  return { all: 0, open: 0, approved: 0, rejected: 0, low_dpi: 0 }
}

/** 这一行算进哪一种计数（open、approved、rejected）。 */
function bucketOf(item: Pick<MyReviewItem, 'status' | 'result'>): 'open' | 'approved' | 'rejected' | null {
  if (item.status === 'open') return 'open'
  return item.result
}

function sameCounts(a: MyReviewCounts, b: MyReviewCounts): boolean {
  return a.all === b.all && a.open === b.open && a.approved === b.approved && a.rejected === b.rejected && a.low_dpi === b.low_dpi
}

/** 分文件夹的计数也一样（补上"一个文件夹进、一个文件夹出，总数抵消"的盲区）。 */
function sameFolders(a: readonly MyReviewFolder[], b: readonly MyReviewFolder[]): boolean {
  if (a.length !== b.length) return false
  return a.every((f, i) => f.folder_id === b[i]!.folder_id && sameCounts(f.counts, b[i]!.counts))
}

/**
 * 从一个文件的全部分配（按 id 升序）里取"最近一次分给我、没有取消的那一行"，拼成我的审核的一行（同 M2-2 的规则）；
 * 没有这样的行返回 null（改派走了）。`base` 是列表里原来的这一行（文件名、路径、dpi 等 reviews 接口没有的字段）。
 */
export function myRowFrom(res: FileReviews, meId: string, base: MyReviewItem): MyReviewItem | null {
  let mine = null
  for (let i = res.reviews.length - 1; i >= 0; i--) {
    const r = res.reviews[i]!
    if (r.reviewer_id === meId && r.status !== 'cancelled') {
      mine = r
      break
    }
  }
  if (!mine) return null
  const last = res.reviews[res.reviews.length - 1]
  return {
    ...base,
    assignment_id: mine.assignment_id,
    folder_id: res.folder_id,
    review_round: res.review_round,
    review_status: res.review_status,
    version_no: res.version_no,
    assigned_at: mine.assigned_at,
    status: mine.status === 'open' ? 'open' : 'done',
    result: mine.status === 'open' ? null : ((mine.result as MyReviewItem['result']) ?? null),
    reviewed_at: mine.reviewed_at,
    is_current: last?.assignment_id === mine.assignment_id,
  }
}

/**
 * scope：当前范围（null 表示还没有项目）；meId：当前用户（判断"分给我的那一行"）；keep：正在看的文件（整体重拉时
 * 尽量保住它，不在第一批里就接着往后取）。
 */
export function useMyReviews(scope: () => ReviewScope | null, meId: () => string | null, keep: () => string | null = () => null) {
  const items = shallowRef<MyReviewItem[]>([])
  const counts = ref<MyReviewCounts | null>(null)
  const folders = shallowRef<MyReviewFolder[]>([])
  const otherOpen = ref(0)
  const otherProjects = shallowRef<OtherProjectOpen[]>([])
  const lowDpiThreshold = ref(200)
  const nextCursor = ref<string | null>(null)
  const state = ref<ListState>('idle')
  const error = ref<unknown>(null)
  const loadingMore = ref(false)
  /** 最近一次加载下一页失败的原因（列表底部显示，可以重试）；成功或换范围后清掉。 */
  const loadMoreError = ref<unknown>(null)
  /** 当前列表是按哪个文件夹取成功的（说明这个文件夹在当前项目里、能进；没有范围或还没取成功时为 null）。 */
  const loadedFolderId = ref<string | null>(null)
  /** 每次整体重拉、换范围加一（丢弃旧请求的结果）。 */
  let seq = 0
  /** 当前列表对应的范围（判断换没换）。 */
  let loadedScope: string | null = null
  /** 页面卸载后不再发请求、不再改状态。 */
  let disposed = false
  /** 正在整体重拉（这期间不加载下一页：旧的 cursor 接不上新的列表）。 */
  let fetching = false
  /** 在途的"下一页"（重复调用时返回同一个）。 */
  let moreInFlight: Promise<boolean> | null = null
  /** 加载下一页失败的时间：之后 LOAD_MORE_BACKOFF_MS 内自动加载不再发（滚到底时不会反复请求）。 */
  let loadMoreFailedAt = 0
  /**
   * 本页刚刚在本地改过的行（通过、驳回成功后按响应改的）和改的时间：在那之前发出的整体重拉，响应里这一行可能还是旧的，
   * 用本地的（审查第 1 轮：在途的重拉会把刚通过的又显示成待审）。
   */
  const localApplied = new Map<string, { at: number; item: MyReviewItem }>()
  /**
   * 逐个刷新过的文件和那次请求发出的时间：整体重拉的响应可能比它旧（请求更早发出），替换完之后对这些文件再刷新一次
   * （验收第 1 轮）。
   */
  const refreshedAt = new Map<string, number>()
  /** 整体重拉失败之后自动再试一次的计时器。 */
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  /** 这一串失败里已经自动再试过（成功后清掉）。 */
  let autoRetried = false
  /** 整体重拉失败（列表里还是原来的内容）后多久自动再试一次。 */
  const RELOAD_RETRY_MS = 5000

  const indexOf = computed(() => new Map(items.value.map((it, i) => [it.file_id, i])))
  const folderById = computed(() => new Map(folders.value.map((f) => [f.folder_id, f])))
  const hasMore = computed(() => nextCursor.value !== null)

  function scopeKey(s: ReviewScope | null): string | null {
    return s ? `${s.projectId}|${s.folderId ?? ''}|${s.state}|${s.lowDpi ? 1 : 0}` : null
  }

  function take(res: MyReviews) {
    counts.value = res.counts
    folders.value = res.folders
    otherOpen.value = res.other_projects_open
    otherProjects.value = res.other_projects
    lowDpiThreshold.value = res.low_dpi_threshold
  }

  /** 换了范围（或第一次）：清空，从第一页取。 */
  async function reset() {
    if (disposed) return
    const s = scope()
    loadedScope = scopeKey(s)
    seq++
    items.value = []
    counts.value = null
    folders.value = []
    nextCursor.value = null
    state.value = s ? 'loading' : 'idle'
    error.value = null
    loadMoreError.value = null
    loadMoreFailedAt = 0
    fetching = false
    loadedFolderId.value = null
    localApplied.clear()
    refreshedAt.clear()
    autoRetried = false
    if (retryTimer) clearTimeout(retryTimer)
    retryTimer = null
    if (s) await fetchFirst(s, PAGE_SIZE, null)
  }

  /** 响应里的一行：本地在请求发出之后改过的，用本地的。 */
  function withLocal(item: MyReviewItem, requestedAt: number): MyReviewItem {
    const loc = localApplied.get(item.file_id)
    return loc && loc.at > requestedAt ? loc.item : item
  }

  /**
   * 取第一页（整体替换列表）。keepId 不在第一页里而后面还有页时接着往后取（最多取到 RELOAD_MAX 行之后再加 5 页），
   * 免得整体重拉之后正在看的文件不见了（审查第 1 轮）。全部取完才替换列表。失败时记下错误（列表已经有内容时
   * 保留原来的），不抛出。
   */
  async function fetchFirst(s: ReviewScope, limit: number, keepId: string | null) {
    const my = ++seq
    const requestedAt = Date.now()
    fetching = true
    try {
      const q = { project_id: s.projectId, folder_id: s.folderId, state: s.state, low_dpi: s.lowDpi }
      let res = await listMyReviews({ ...q, limit }, true)
      if (my !== seq || disposed) return
      const list = [...res.items]
      const seen = new Set(list.map((i) => i.file_id))
      let pages = 0
      while (keepId && !seen.has(keepId) && res.next_cursor && pages < 5) {
        pages++
        res = await listMyReviews({ ...q, limit: PAGE_SIZE, cursor: res.next_cursor }, true)
        if (my !== seq || disposed) return
        for (const it of res.items) {
          if (seen.has(it.file_id)) continue
          seen.add(it.file_id)
          list.push(it)
        }
      }
      take(res)
      const merged = list.map((it) => withLocal(it, requestedAt))
      // 用了本地版本的行：计数按"响应里的 → 本地的"补上（响应的计数是在本地改之前算的）。
      list.forEach((it, i) => {
        if (merged[i] !== it) countsDelta(it, merged[i]!)
      })
      items.value = merged
      nextCursor.value = res.next_cursor
      state.value = 'ready'
      loadedFolderId.value = s.folderId
      error.value = null
      autoRetried = false
      loadMoreError.value = null
      loadMoreFailedAt = 0
      // 这次请求发出之后逐个刷新过的文件：响应里的可能比刷新的旧，再刷新一次；更早的记录不用再留。
      const redo: string[] = []
      for (const [fid, at] of refreshedAt) {
        if (at > requestedAt) redo.push(fid)
        else refreshedAt.delete(fid)
      }
      if (redo.length) void refreshFiles(redo)
    } catch (e) {
      if (my !== seq || disposed) return
      error.value = e
      if (state.value !== 'ready') state.value = 'error'
      else scheduleRetry()
    } finally {
      if (my === seq) fetching = false
    }
  }

  /**
   * 整体重拉失败、列表里还是原来的内容：过一会儿自动再试一次（页面同时显示提示和"重试"）。连着失败只自动试一次，
   * 之后等用户点"重试"或下一个事件。
   */
  function scheduleRetry() {
    if (retryTimer || disposed || autoRetried) return
    autoRetried = true
    retryTimer = setTimeout(() => {
      retryTimer = null
      if (!disposed && error.value && state.value === 'ready') void reload()
    }, RELOAD_RETRY_MS)
  }

  /**
   * 整体重拉：一次取回已经加载的那么多行（至少一页，最多 2000）；keepId（正在看的文件）不在里面时接着往后取。
   * 范围变了按 reset 处理。
   */
  async function reload(keepId: string | null = keep()) {
    if (disposed) return
    const s = scope()
    if (scopeKey(s) !== loadedScope || !s) return reset()
    const limit = Math.min(RELOAD_MAX, Math.max(PAGE_SIZE, items.value.length))
    await fetchFirst(s, limit, keepId)
  }

  /** 加载下一页失败之后多久内不自动再试。 */
  const LOAD_MORE_BACKOFF_MS = 5000

  /** 自动加载下一页（列表滚到快到底时）：刚失败过的等一会儿再试，免得反复请求。 */
  function autoLoadMore() {
    if (loadMoreError.value && Date.now() - loadMoreFailedAt < LOAD_MORE_BACKOFF_MS) return
    void loadMore()
  }

  /**
   * 加载下一页（追加，按 file_id 去重）。返回有没有进展（取到了下一页）；没有下一页、在整体重拉、失败时返回 false。
   * 已经在加载时返回在途的那一次（审查第 1 轮：调用方循环等下一页时不能空转）。
   */
  function loadMore(): Promise<boolean> {
    if (moreInFlight) return moreInFlight
    const s = scope()
    const cursor = nextCursor.value
    if (disposed || !s || !cursor || fetching || state.value !== 'ready') return Promise.resolve(false)
    const my = seq
    loadingMore.value = true
    moreInFlight = (async () => {
      try {
        const res = await listMyReviews(
          { project_id: s.projectId, folder_id: s.folderId, state: s.state, low_dpi: s.lowDpi, limit: PAGE_SIZE, cursor },
          true,
        )
        // 期间整体重拉过、或者 cursor 已经变了：这一页接不上现在的列表，丢掉。
        if (my !== seq || disposed || nextCursor.value !== cursor) return false
        take(res)
        const seen = new Set(items.value.map((i) => i.file_id))
        items.value = [...items.value, ...res.items.filter((i) => !seen.has(i.file_id))]
        nextCursor.value = res.next_cursor
        loadMoreError.value = null
        loadMoreFailedAt = 0
        return true
      } catch (e) {
        if (my === seq && !disposed) {
          loadMoreError.value = e
          loadMoreFailedAt = Date.now()
        }
        return false
      } finally {
        loadingMore.value = false
        moreInFlight = null
      }
    })()
    return moreInFlight
  }

  // ---------- 计数的本地加减 ----------

  function bump(c: MyReviewCounts, item: MyReviewItem, sign: 1 | -1) {
    c.all += sign
    const b = bucketOf(item)
    if (b) c[b] += sign
    if (item.dpi != null && item.dpi < lowDpiThreshold.value) c.low_dpi += sign
  }

  /** 一行从 before 变成 after（null 表示不在了）：总计数和所在文件夹的计数跟着加减。 */
  function countsDelta(before: MyReviewItem | null, after: MyReviewItem | null) {
    const c = counts.value
    if (!c) return
    const next = { ...c }
    const folderUpdates = new Map<string, MyReviewCounts>()
    const folderCounts = (fid: string) => {
      let fc = folderUpdates.get(fid)
      if (!fc) {
        const f = folderById.value.get(fid)
        if (!f) return null
        fc = { ...f.counts }
        folderUpdates.set(fid, fc)
      }
      return fc
    }
    if (before) {
      bump(next, before, -1)
      const fc = folderCounts(before.folder_id)
      if (fc) bump(fc, before, -1)
    }
    if (after) {
      bump(next, after, 1)
      const fc = folderCounts(after.folder_id)
      if (fc) bump(fc, after, 1)
    }
    counts.value = next
    if (folderUpdates.size) {
      folders.value = folders.value.map((f) => {
        const fc = folderUpdates.get(f.folder_id)
        return fc ? { ...f, counts: fc } : f
      })
    }
  }

  /** 换掉（或拿掉）列表里的一行，计数跟着变。 */
  function replace(fileId: string, next: MyReviewItem | null) {
    const i = indexOf.value.get(fileId)
    if (i === undefined) return
    const before = items.value[i]!
    countsDelta(before, next)
    const list = items.value.slice()
    if (next) list[i] = next
    else list.splice(i, 1)
    items.value = list
  }

  /** 本页的审核成功之后：按响应更新这一行（我的那一行变成 done，结果、时间、版本、轮次）。 */
  function applyReviewed(r: ReviewedFile) {
    const i = indexOf.value.get(r.file_id)
    if (i === undefined) return
    const before = items.value[i]!
    const after: MyReviewItem = {
      ...before,
      assignment_id: r.assignment_id,
      status: 'done',
      result: r.result,
      review_status: r.review_status,
      reviewed_at: r.reviewed_at,
      version_no: r.version_no,
      review_round: r.review_round,
      is_current: true,
    }
    localApplied.set(r.file_id, { at: Date.now(), item: after })
    if (localApplied.size > 5000) localApplied.delete(localApplied.keys().next().value!)
    replace(r.file_id, after)
  }

  function get(fileId: string | null | undefined): MyReviewItem | undefined {
    if (!fileId) return undefined
    const i = indexOf.value.get(fileId)
    return i === undefined ? undefined : items.value[i]
  }

  // ---------- 逐个文件刷新（实时事件、操作失败之后） ----------

  /**
   * 重新取这几个文件（都在列表里）的分配记录，更新或拿掉对应的行。返回需要整体重拉的原因，没有时返回 null：
   * moved（文件移到了别的文件夹，路径变了）、stale（版本变了，重扫之后 dpi 可能变了；分配记录接口不带 dpi）、
   * failed（有的没取到）。
   */
  async function refreshFiles(fileIds: Iterable<string>): Promise<'moved' | 'stale' | 'failed' | null> {
    const me = meId()
    const ids = [...new Set(fileIds)].filter((id) => indexOf.value.has(id))
    if (disposed || !me || !ids.length) return null
    const my = seq
    let need: 'moved' | 'stale' | 'failed' | null = null
    let next = 0
    const worker = async () => {
      while (next < ids.length) {
        const fid = ids[next++]!
        let res: FileReviews | null = null
        let gone = false
        const startedAt = Date.now()
        try {
          res = await getFileReviews(fid, true)
        } catch (e) {
          if (isApiError(e) && (e.status === 404 || e.status === 403)) gone = true
          else need ??= 'failed'
        }
        if (my !== seq || disposed) return
        const base = get(fid)
        if (!base) continue
        if (gone || res) {
          // 这是 server 上的新状态：比它早的本地记录作废；记下时间，给之后完成的整体重拉核对新旧。
          refreshedAt.set(fid, startedAt)
          const loc = localApplied.get(fid)
          if (loc && loc.at < startedAt) localApplied.delete(fid)
        }
        if (gone) {
          replace(fid, null)
          continue
        }
        if (!res) continue
        const row = myRowFrom(res, me, base)
        if (row && row.folder_id !== base.folder_id) need = 'moved'
        else if (row && row.version_no !== base.version_no && need !== 'moved') need = 'stale'
        replace(fid, row)
      }
    }
    await Promise.all(Array.from({ length: Math.min(REFRESH_CONCURRENCY, ids.length) }, worker))
    return my === seq ? need : null
  }

  // ---------- 核对计数（节流） ----------

  let lastCheckAt = 0
  let checkTimer: ReturnType<typeof setTimeout> | null = null
  let checking: Promise<void> | null = null

  /**
   * 取一次只要 1 行的"我的审核"，更新其他项目的待审数；计数和本地的对不上（有我们没看到的变化：新分给我的、
   * 被筛掉的那几种状态变了）就整体重拉。`COUNTS_CHECK_MS` 内最多一次，期间再要求的合并到之后的那一次。
   */
  function checkCounts() {
    if (checkTimer || disposed) return
    const wait = Math.max(0, lastCheckAt + COUNTS_CHECK_MS - Date.now())
    checkTimer = setTimeout(() => {
      checkTimer = null
      lastCheckAt = Date.now()
      checking ??= runCheck().finally(() => {
        checking = null
      })
    }, wait)
  }

  async function runCheck() {
    const s = scope()
    if (!s || scopeKey(s) !== loadedScope || state.value !== 'ready') return
    const my = seq
    let res: MyReviews
    try {
      res = await listMyReviews({ project_id: s.projectId, folder_id: s.folderId, state: s.state, low_dpi: s.lowDpi, limit: 1 }, true)
    } catch {
      return
    }
    if (my !== seq || disposed) return
    otherOpen.value = res.other_projects_open
    otherProjects.value = res.other_projects
    if (!counts.value || !sameCounts(counts.value, res.counts) || !sameFolders(folders.value, res.folders)) {
      await reload(keep())
    }
  }

  /** 页面卸载：之后的调用都不做（在途的请求回来也不改状态、不再发新的）。 */
  function dispose() {
    disposed = true
    seq++
    if (checkTimer) clearTimeout(checkTimer)
    checkTimer = null
    if (retryTimer) clearTimeout(retryTimer)
    retryTimer = null
  }

  return {
    items,
    counts,
    folders,
    folderById,
    otherOpen,
    otherProjects,
    lowDpiThreshold,
    nextCursor,
    hasMore,
    state,
    error,
    loadingMore,
    loadMoreError,
    loadedFolderId,
    indexOf,
    get,
    reset,
    reload,
    loadMore,
    autoLoadMore,
    applyReviewed,
    replace,
    refreshFiles,
    checkCounts,
    dispose,
    emptyCounts,
  }
}

export type MyReviewsList = ReturnType<typeof useMyReviews>
