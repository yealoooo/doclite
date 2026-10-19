// 导出页（M4-5）的数据：任务列表、按事件只重拉变了的任务、"交付后变更 n"的按需计数、条目范围的第一件档号。
//
// - **列表**：`GET /api/projects/{pid}/export-tasks`，最新的在前，keyset 分页（每页 50，滚到底加载更多）。
// - **按任务重拉**（`export.changed {task_ids}`）：只取这些任务（`GET /api/export-tasks/{id}`）。同时最多一轮，跑的过程中
//   新来的 id 并到下一轮；同一个任务一秒最多取一次（进度事件每秒最多一次，再多也只是重复）。不认识的任务（别的窗口
//   刚建的）按建立时间插进列表。
//   - 取失败（不是 404）的放回去按退避重试：2、5、10 秒，最多 3 次（审查第 1 轮）；错误不弹提示。
//   - 和整页重新加载的先后（审查第 1 轮）：列表响应回来时，比列表请求晚发出的单个结果优先，不在响应里的（列表请求
//     之后才建的）插回去；比最近一次已应用的列表请求发出得早的单个结果不再用（列表更新）。
// - **交付后变更 n**（列表接口不带，每个任务单算一次约 130 ms；规则是审查第 1 轮总控定的）：
//   - 只对"要用的" done 任务调 `/changes`：行在可视区里（`want(id, 'row')`），或者对话框开着（`want(id, 'dialog')`）。
//     结果缓存；
//   - 项目里有 `files.changed`、`catalog.changed` 时，缓存标成"可能过时"（`stale`）：
//     - 对话框开着的那个任务：去抖后重取（最后一条事件后 400 ms、最多等 1.5 秒，两轮至少隔 2 秒），"3 秒内出现"靠它；
//     - 列表行：离上次取过至少 20 秒才重取（看得到的才取），行上标"可能已过时"；
//   - 这个任务自己的事件（`export.changed`）按对话框的速度重取；
//   - 取失败的保留旧值、行上标出来，按退避重试（5、15、30 秒），进入可视区、打开对话框、回到前台时马上重试；
//     错误不弹提示（显示在行内、对话框里）；
//   - 标签页在后台（`document.hidden`）时暂停所有重取，回到前台时补取一次（不受 20 秒限制）。
//   同时最多一轮、每轮最多 2 个并发。
// - **条目范围的标题**：任务接口不带条目的档号，看得到的条目范围的任务取一次明细的第一页（limit 1）拿第一件的档号，
//   冻结的数据不会变，取到就一直用。

import { useDocumentVisibility } from '@vueuse/core'
import { computed, reactive, ref, shallowRef, watch } from 'vue'

import { getExportTask, getExportTaskChanges, listExportTaskFiles, listExportTasks, MAX_TASKS_PAGE } from '@/api/export'
import { errorMessage, HANDLE_ALL, isApiError } from '@/api/http'
import type { ExportChanges, ExportTask } from '@/api/types'
import { itemName } from '@/lib/export'

const PAGE_SIZE = 50
/** 同一个任务两次重拉之间至少隔多久。 */
const TASK_MIN_INTERVAL_MS = 1000
/** 单个任务重拉失败后的重试间隔（最多这么多次）。 */
const TASK_RETRY_MS = [2000, 5000, 10_000]
/** 一轮里同时最多取几个任务。 */
const TASK_CONCURRENCY = 4

/** 新的在前：建立时间倒序，同一时刻按 id 倒序（和 server 的 keyset 一致）。a 比 b 新时返回负数。 */
function newerFirst(a: ExportTask, b: ExportTask): number {
  const ta = Date.parse(a.created_at)
  const tb = Date.parse(b.created_at)
  if (ta !== tb) return tb - ta
  return a.id < b.id ? 1 : a.id > b.id ? -1 : 0
}

/** 按并发上限跑完一组异步任务。 */
async function pool<T>(items: T[], limit: number, fn: (item: T) => Promise<void>) {
  let next = 0
  const workers = Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (next < items.length) await fn(items[next++]!)
  })
  await Promise.all(workers)
}

export function useExportTasks(projectId: () => string | null) {
  const tasks = ref<ExportTask[]>([])
  const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
  const error = ref('')
  const nextCursor = ref<string | null>(null)
  const loadingMore = ref(false)
  const loadMoreError = ref<string | null>(null)
  /** 列表的代次：换项目、整页重新加载时加一，旧的响应作废。 */
  let gen = 0
  let alive = true
  // 标签页在前台：切到后台时暂停按事件的重取，回到前台时补取一次（M4-5 审查第 1 轮）。
  const visibility = useDocumentVisibility()
  const pageVisible = computed(() => visibility.value !== 'hidden')

  /** 单个取回来的任务和发出请求的时间（整页重新加载时，比列表请求晚发出的以它为准）。 */
  const fetched = new Map<string, { task: ExportTask; startedAt: number }>()
  /** 最近一次已应用的列表请求是什么时候发出的。 */
  let appliedListAt = 0

  // 按任务重拉的状态（下面用）。
  const pending = new Set<string>()
  const lastFetch = new Map<string, number>()
  const retries = new Map<string, { n: number; at: number }>()
  let timer: ReturnType<typeof setTimeout> | null = null
  let running = false

  function reset() {
    gen++
    tasks.value = []
    nextCursor.value = null
    state.value = 'idle'
    error.value = ''
    loadMoreError.value = null
    pending.clear()
    lastFetch.clear()
    retries.clear()
    fetched.clear()
    appliedListAt = 0
    if (timer) clearTimeout(timer)
    timer = null
  }

  /** 第一页（整页重新加载时，取回已经加载过的个数，最多 200）。 */
  async function load(): Promise<void> {
    const pid = projectId()
    if (!pid) {
      reset()
      return
    }
    const my = ++gen
    if (!tasks.value.length) state.value = 'loading'
    const startedAt = Date.now()
    const limit = Math.min(MAX_TASKS_PAGE, Math.max(PAGE_SIZE, tasks.value.length))
    try {
      const res = await listExportTasks(pid, { limit }, HANDLE_ALL)
      if (my !== gen || !alive) return
      appliedListAt = startedAt
      const inResponse = new Set(res.items.map((t) => t.id))
      tasks.value = res.items.map((t) => {
        const f = fetched.get(t.id)
        return f && f.startedAt > startedAt ? f.task : t
      })
      nextCursor.value = res.next_cursor
      loadMoreError.value = null
      state.value = 'ready'
      error.value = ''
      // 列表请求发出之后才建的（单个取回来的），响应里没有：插回去。
      for (const [id, f] of fetched) {
        if (!inResponse.has(id) && f.startedAt > startedAt && f.task.project_id === pid) upsert(f.task)
      }
    } catch (e) {
      if (my !== gen || !alive) return
      error.value = errorMessage(e)
      if (!tasks.value.length) state.value = 'error'
    }
  }

  async function loadMore() {
    const pid = projectId()
    const cursor = nextCursor.value
    if (!pid || !cursor || loadingMore.value) return
    const my = gen
    loadingMore.value = true
    try {
      const res = await listExportTasks(pid, { limit: PAGE_SIZE, cursor }, HANDLE_ALL)
      if (my !== gen || !alive) return
      const have = new Set(tasks.value.map((t) => t.id))
      tasks.value = [...tasks.value, ...res.items.filter((t) => !have.has(t.id))]
      nextCursor.value = res.next_cursor
      loadMoreError.value = null
    } catch (e) {
      if (my === gen) loadMoreError.value = errorMessage(e)
    } finally {
      if (my === gen) loadingMore.value = false
    }
  }

  /** 用接口返回的任务更新列表（建任务、取消的响应、单个重拉）。 */
  function upsert(t: ExportTask) {
    if (t.project_id !== projectId()) return
    const list = tasks.value
    const i = list.findIndex((x) => x.id === t.id)
    if (i >= 0) {
      const copy = list.slice()
      copy[i] = t
      tasks.value = copy
      return
    }
    // 不认识的（别的窗口刚建的）：按建立时间插进去；比已加载的最后一个还早而后面还有没加载的，等"加载更多"。
    const last = list[list.length - 1]
    if (nextCursor.value && last && newerFirst(t, last) > 0) return
    const at = list.findIndex((x) => newerFirst(t, x) < 0)
    const copy = list.slice()
    copy.splice(at < 0 ? copy.length : at, 0, t)
    tasks.value = copy
  }

  function remove(id: string) {
    tasks.value = tasks.value.filter((t) => t.id !== id)
  }

  // ---------- 按任务重拉（export.changed） ----------

  function refreshTasks(ids: Iterable<string>) {
    for (const id of ids) pending.add(id)
    schedule(100)
  }

  function dueAt(id: string): number {
    return Math.max((lastFetch.get(id) ?? 0) + TASK_MIN_INTERVAL_MS, retries.get(id)?.at ?? 0)
  }

  function schedule(delay: number) {
    if (timer || running || !pending.size) return
    timer = setTimeout(() => void runRound(), delay)
  }

  async function runRound() {
    timer = null
    if (running) return
    const pid = projectId()
    if (!pid || !alive) {
      pending.clear()
      return
    }
    const now = Date.now()
    const ready = [...pending].filter((id) => dueAt(id) <= now)
    if (!ready.length) {
      const soonest = Math.min(...[...pending].map(dueAt))
      if (soonest < Infinity) timer = setTimeout(() => void runRound(), Math.max(50, soonest - now))
      return
    }
    ready.forEach((id) => pending.delete(id))
    running = true
    try {
      await pool(ready, TASK_CONCURRENCY, async (id) => {
        const startedAt = Date.now()
        lastFetch.set(id, startedAt)
        try {
          const t = await getExportTask(id, HANDLE_ALL)
          if (!alive || projectId() !== pid) return
          retries.delete(id)
          // 比已经用上的列表请求还早发出的，列表里的更新，不用它。
          if (startedAt < appliedListAt) return
          fetched.set(id, { task: t, startedAt })
          upsert(t)
        } catch (e) {
          if (!alive || projectId() !== pid) return
          if (isApiError(e, 'not_found')) {
            retries.delete(id)
            remove(id)
            return
          }
          // 网关抖动、server 重启：退避后再取，最多 3 次；再不行等下一条事件或重连时的整页刷新。
          const r = retries.get(id) ?? { n: 0, at: 0 }
          if (r.n < TASK_RETRY_MS.length) {
            retries.set(id, { n: r.n + 1, at: Date.now() + TASK_RETRY_MS[r.n]! })
            pending.add(id)
          } else retries.delete(id)
        }
      })
    } finally {
      running = false
      if (alive && pending.size) {
        const soonest = Math.min(...[...pending].map(dueAt))
        schedule(Math.max(100, soonest - Date.now()))
      }
    }
  }

  function dispose() {
    alive = false
    gen++
    if (timer) clearTimeout(timer)
    timer = null
    changes.dispose()
    labels.dispose()
  }

  const changes = useChangeCounts(projectId, pageVisible)
  const labels = useFirstItemLabels(projectId)

  return {
    tasks,
    state,
    error,
    nextCursor,
    loadingMore,
    loadMoreError,
    pageVisible,
    load,
    loadMore,
    reset: () => {
      reset()
      changes.clear()
      labels.clear()
    },
    upsert,
    refreshTasks,
    changes,
    labels,
    dispose,
  }
}

// ---------- 交付后变更的计数（按需、缓存） ----------

export interface ChangesEntry {
  /** 最近一次取到的（取失败、过时的时候也保留）。 */
  data: ExportChanges | null
  /** loading：取着；ready：取到了；error：最近一次取失败了（data 可能是旧的）。 */
  state: 'loading' | 'ready' | 'error'
  /** 项目里有了变化，data 可能已过时，等重取。 */
  stale: boolean
  error: string
  /** 最近一次取（成功或失败）结束的时间。 */
  fetchedAt: number
  /** 标成过时的时间。 */
  staleAt: number
  /** 这个任务自己有事件：按对话框的速度重取。 */
  urgent: boolean
  /** 连续失败次数（成功清零）和下次重试的时间。 */
  failures: number
  retryAt: number
}

/** 对话框：最后一条事件过去多久再重取。 */
const CHANGES_DEBOUNCE_MS = 400
/** 对话框：标成过时后最多等多久（连续的事件不会让它一直推迟）。 */
const CHANGES_MAX_WAIT_MS = 1500
/** 对话框：两轮之间至少隔多久。 */
const CHANGES_FAST_MIN_INTERVAL_MS = 2000
/** 列表行：离上次取过至少多久才重取（审查第 1 轮，总控定的 20 秒）。 */
export const CHANGES_ROW_MIN_INTERVAL_MS = 20_000
/** 取失败后的重试间隔。 */
const CHANGES_RETRY_MS = [5000, 15_000, 30_000]
const CHANGES_CONCURRENCY = 2

interface Want {
  rows: number
  dialog: number
}

function useChangeCounts(projectId: () => string | null, pageVisible: { value: boolean }) {
  const entries = reactive(new Map<string, ChangesEntry>())
  /** 要用的任务：行在可视区里、对话框开着（引用计数，同一个任务可能同时被行和对话框用）。 */
  const wanted = new Map<string, Want>()
  let timer: ReturnType<typeof setTimeout> | null = null
  /** 最近一条作废事件的时间（对话框的去抖）。 */
  let lastEventAt = 0
  /** 上一轮对话框速度的重取开始的时间。 */
  let lastFastRoundAt = 0
  /** 回到前台：下一轮把过时、出错的都取一次（不受间隔限制）。 */
  let catchUp = false
  let running = false
  let gen = 0
  let alive = true
  /** 测试用：总共发了几次 /changes。 */
  const requests = ref(0)

  function fast(id: string): boolean {
    return (wanted.get(id)?.dialog ?? 0) > 0 || !!entries.get(id)?.urgent
  }

  /** 这个要用的任务什么时候该取（null：现在不用取）。 */
  function dueAt(id: string, now: number): number | null {
    const e = entries.get(id)
    if (!e) return now
    if (e.state === 'loading') return null
    if (e.state === 'error') return catchUp ? now : e.retryAt
    if (!e.stale) return null
    if (catchUp) return now
    if (fast(id)) {
      const debounced = Math.min(lastEventAt + CHANGES_DEBOUNCE_MS, e.staleAt + CHANGES_MAX_WAIT_MS)
      return Math.max(debounced, lastFastRoundAt + CHANGES_FAST_MIN_INTERVAL_MS)
    }
    return Math.max(e.fetchedAt + CHANGES_ROW_MIN_INTERVAL_MS, e.staleAt + CHANGES_DEBOUNCE_MS)
  }

  function reschedule() {
    if (timer) clearTimeout(timer)
    timer = null
    if (!alive || running || !pageVisible.value) return
    const now = Date.now()
    let next = Infinity
    for (const id of wanted.keys()) {
      const d = dueAt(id, now)
      if (d !== null) next = Math.min(next, d)
    }
    if (next < Infinity) timer = setTimeout(() => void run(), Math.max(30, next - now))
  }

  /** 开始用这个任务的计数（行进入可视区、对话框打开）。取失败过的马上重试。返回释放函数。 */
  function want(id: string, kind: 'row' | 'dialog'): () => void {
    const w = wanted.get(id) ?? { rows: 0, dialog: 0 }
    if (kind === 'dialog') w.dialog++
    else w.rows++
    wanted.set(id, w)
    const e = entries.get(id)
    if (e?.state === 'error') e.retryAt = Date.now()
    reschedule()
    let released = false
    return () => {
      if (released) return
      released = true
      const cur = wanted.get(id)
      if (!cur) return
      if (kind === 'dialog') cur.dialog--
      else cur.rows--
      if (cur.dialog <= 0 && cur.rows <= 0) wanted.delete(id)
      reschedule()
    }
  }

  /**
   * 标成"可能过时"：给了 ids 只标这些（这个任务自己的事件，`urgent`：按对话框的速度重取），不给是全部
   * （files.changed、catalog.changed、重连）。
   */
  function invalidate(ids?: Iterable<string>, urgent = false) {
    const now = Date.now()
    lastEventAt = now
    for (const id of ids ?? [...entries.keys()]) {
      const e = entries.get(id)
      if (!e) continue
      if (!e.stale) {
        e.stale = true
        e.staleAt = now
      }
      if (urgent) e.urgent = true
    }
    reschedule()
  }

  /** 对话框里的"刷新"：马上重取。 */
  function reload(id: string) {
    const e = entries.get(id)
    if (e?.state === 'loading') return
    void fetchOne(id).finally(reschedule)
  }

  async function fetchOne(id: string) {
    const my = gen
    let e = entries.get(id)
    if (!e) {
      e = { data: null, state: 'loading', stale: false, error: '', fetchedAt: 0, staleAt: 0, urgent: false, failures: 0, retryAt: 0 }
      entries.set(id, e)
    } else {
      // 取的过程中又被标成过时的，回来之后仍是过时，下一轮再取。
      e.state = 'loading'
      e.stale = false
      e.urgent = false
    }
    requests.value++
    try {
      const data = await getExportTaskChanges(id, HANDLE_ALL)
      if (my !== gen) return
      const cur = entries.get(id)!
      cur.data = data
      cur.state = 'ready'
      cur.error = ''
      cur.failures = 0
      cur.fetchedAt = Date.now()
    } catch (err) {
      if (my !== gen) return
      const cur = entries.get(id)!
      cur.state = 'error'
      cur.error = errorMessage(err)
      cur.failures++
      cur.fetchedAt = Date.now()
      cur.retryAt = cur.fetchedAt + CHANGES_RETRY_MS[Math.min(cur.failures, CHANGES_RETRY_MS.length) - 1]!
    }
  }

  async function run() {
    timer = null
    if (running || !alive || !pageVisible.value || !projectId()) return
    const now = Date.now()
    const ids = [...wanted.keys()].filter((id) => {
      const d = dueAt(id, now)
      return d !== null && d <= now
    })
    catchUp = false
    if (!ids.length) return reschedule()
    if (ids.some(fast)) lastFastRoundAt = now
    running = true
    const my = gen
    try {
      await pool(ids, CHANGES_CONCURRENCY, fetchOne)
    } finally {
      if (my === gen) {
        running = false
        reschedule()
      }
    }
  }

  // 后台暂停（reschedule 不安排），回到前台补取一次。
  watch(
    () => pageVisible.value,
    (v) => {
      if (v) {
        catchUp = true
        reschedule()
      } else if (timer) {
        clearTimeout(timer)
        timer = null
      }
    },
  )

  function clear() {
    gen++
    entries.clear()
    if (timer) clearTimeout(timer)
    timer = null
    running = false
    catchUp = false
  }

  function dispose() {
    alive = false
    clear()
  }

  return { entries, want, invalidate, reload, clear, dispose, requests }
}

// ---------- 条目范围的第一件档号 ----------

function useFirstItemLabels(projectId: () => string | null) {
  /** 任务 id → 第一件的称呼（档号，没有时"第 n 件"）；null 表示取过了但没有。 */
  const labels = shallowRef(new Map<string, string | null>())
  const queue = new Set<string>()
  let running = false
  let gen = 0
  let alive = true

  function request(id: string) {
    if (labels.value.has(id) || queue.has(id)) return
    queue.add(id)
    void run()
  }

  async function run() {
    if (running || !alive) return
    running = true
    const my = gen
    try {
      while (queue.size && alive && my === gen) {
        const ids = [...queue].slice(0, 2)
        await Promise.all(
          ids.map(async (id) => {
            let label: string | null = null
            try {
              const res = await listExportTaskFiles(id, { limit: 1 }, HANDLE_ALL)
              const f = res.items[0]
              label = f ? itemName(f.archive_no, f.item_no) : null
            } catch {
              label = null
            }
            queue.delete(id)
            if (my !== gen || !projectId()) return
            const next = new Map(labels.value)
            next.set(id, label)
            labels.value = next
          }),
        )
      }
    } finally {
      running = false
    }
  }

  function clear() {
    gen++
    queue.clear()
    labels.value = new Map()
  }

  function dispose() {
    alive = false
    clear()
  }

  return { labels, request, clear, dispose }
}
