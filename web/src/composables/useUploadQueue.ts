// /scan 页的上传队列和热文件夹状态（M1-13，只在壳里用）：先订阅壳的事件，再读一次状态和最近的文件，之后全靠事件更新。
//
// - 列表按 id 倒序（发现的先后，新发现的在上面），一行在原地更新状态和进度，不随更新时间跳来跳去（和 p-upload
//   Mockup 一致）；最多留 LIST_LIMIT 行，"上传中"和"失败"的行不受这个限制（积压很多时也看得到正在传的和要处理的）。
// - `filter` 只看某一种状态（点计数格子）：重新读一次这一种状态的最近文件；之后事件里不再是这种状态的从列表里拿掉。
// - 只显示当前服务器的项（换过服务器时，队列里还有原来服务器的项，壳不处理它们，M1-9）。
// - 上传进度来自 `upload:progress`（约 250 毫秒一次），离开"上传中"就清掉。
// - **事件和读回来的快照谁新：**
//   - 列表：读最近文件的期间，事件里收到的项记进 `touched`（被删的记 null）。快照回来后用它建新表，再把 `touched`
//     盖上去（同一项比 `updated_at`，快照更新时以快照为准；删掉的一律删掉）。
//   - 整体状态、热文件夹状态：`upload:state`、`hotfolder:state` 各有一个序号。读状态或调命令（`withStatus`、
//     `withHotfolder`）的期间序号变了，说明收到了更新的事件，就不用读回来的结果覆盖。壳在这些命令改了状态之后
//     都会发事件，所以丢掉命令的结果不会停在旧状态上。
//
//   const q = useUploadQueue()
//   q.status.value / q.hotfolder.value / q.rows.value / q.progressOf(id) / q.filter.value = 'failed'
//   await q.withHotfolder(() => hotfolderStart())

import { computed, onBeforeUnmount, onMounted, ref, shallowRef, triggerRef, watch } from 'vue'

import {
  hotfolderStatus,
  listenQueueEvents,
  type HotFolderStatus,
  type ItemStatus,
  type ProgressEvent,
  type QueueItem,
  type QueueStatus,
  uploadRecent,
  uploadStatus,
} from '@/lib/scan-queue'
import { shellErrorMessage } from '@/lib/shell'

/** 列表最多留多少行（"上传中""失败"的不算）。 */
export const LIST_LIMIT = 200

/** 不受 LIST_LIMIT 限制的状态。 */
const KEEP_STATUSES: readonly ItemStatus[] = ['uploading', 'failed']

export interface UploadQueueHandlers {
  /** 队列因 401 暂停（壳发 `upload:auth-required`）。 */
  onAuthRequired?: () => void
}

/** 其余的只留 id 最大的 `limit` 项，"上传中""失败"的另外都留着（不算在 `limit` 里）。原地修改 `map`，返回删掉了几项。 */
export function trimItems(map: Map<number, QueueItem>, limit = LIST_LIMIT): number {
  if (map.size <= limit) return 0
  const ids = [...map.values()]
    .filter((i) => !KEEP_STATUSES.includes(i.status))
    .map((i) => i.id)
    .sort((a, b) => b - a)
  let removed = 0
  for (const id of ids.slice(limit)) {
    map.delete(id)
    removed++
  }
  return removed
}

/**
 * 读回来的快照 + 读的期间事件里收到的项 → 新表。`touched` 里 null 表示删掉了；同一项快照更新时以快照为准。
 * `keep` 决定一项该不该显示（当前服务器、筛选）。
 */
export function mergeSnapshot(
  snapshot: QueueItem[],
  touched: Map<number, QueueItem | null>,
  keep: (item: QueueItem) => boolean,
): Map<number, QueueItem> {
  const map = new Map<number, QueueItem>()
  const byId = new Map(snapshot.map((s) => [s.id, s]))
  for (const item of snapshot) if (keep(item)) map.set(item.id, item)
  for (const [id, item] of touched) {
    if (item === null) {
      map.delete(id)
      continue
    }
    const snap = byId.get(id)
    if (snap && snap.updated_at > item.updated_at) continue
    if (keep(item)) map.set(id, item)
    else map.delete(id)
  }
  return map
}

export function useUploadQueue(handlers: UploadQueueHandlers = {}) {
  const status = ref<QueueStatus | null>(null)
  const hotfolder = ref<HotFolderStatus | null>(null)
  /** id → 项。用 shallowRef + triggerRef：一次事件只改一项，不让 Vue 深度代理几百个对象。 */
  const items = shallowRef(new Map<number, QueueItem>())
  const progress = shallowRef(new Map<number, ProgressEvent>())
  const filter = ref<ItemStatus | null>(null)
  /** idle → loading → ready；error 时 loadError 是原因。 */
  const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
  const loadError = ref('')
  /** 列表正在按 filter 重新读取。 */
  const listLoading = ref(false)
  /** 每秒走一次（"x 秒后重试"的倒计时用）。 */
  const now = ref(Date.now())

  let stop: (() => void) | null = null
  let alive = true
  let listSeq = 0
  /** 正在读最近文件时，事件里收到的项（null 表示删掉了）。 */
  let touched: Map<number, QueueItem | null> | null = null
  /** 收到过几次 `upload:state`、`hotfolder:state`。 */
  let stateSeq = 0
  let hotfolderSeq = 0
  let clock: ReturnType<typeof setInterval> | null = null

  const serverUrl = computed(() => status.value?.server_url ?? null)

  function belongs(item: QueueItem): boolean {
    const server = serverUrl.value
    if (server && item.server_url !== server) return false
    return filter.value === null || item.status === filter.value
  }

  /** 事件里收到一项（新的或更新）：不该显示的拿掉。 */
  function upsert(item: QueueItem) {
    touched?.set(item.id, item)
    const map = items.value
    if (item.status !== 'uploading' && progress.value.delete(item.id)) triggerRef(progress)
    if (!belongs(item)) {
      if (map.delete(item.id)) triggerRef(items)
      return
    }
    map.set(item.id, item)
    trimItems(map)
    triggerRef(items)
  }

  function remove(id: number) {
    touched?.set(id, null)
    if (items.value.delete(id)) triggerRef(items)
    if (progress.value.delete(id)) triggerRef(progress)
  }

  const rows = computed(() => [...items.value.values()].sort((a, b) => b.id - a.id))

  function progressOf(id: number): ProgressEvent | null {
    return progress.value.get(id) ?? null
  }

  /** 按当前的 filter 重新读最近的文件（读的期间收到的事件盖在快照上）。 */
  async function loadList() {
    const seq = ++listSeq
    const seen = new Map<number, QueueItem | null>()
    touched = seen
    listLoading.value = true
    try {
      const list = await uploadRecent(LIST_LIMIT, filter.value ? [filter.value] : undefined)
      if (!alive || seq !== listSeq) return
      const map = mergeSnapshot(list, seen, belongs)
      trimItems(map)
      items.value = map
      let dropped = false
      for (const id of progress.value.keys()) {
        if (map.get(id)?.status !== 'uploading') dropped = progress.value.delete(id) || dropped
      }
      if (dropped) triggerRef(progress)
    } finally {
      if (touched === seen) touched = null
      if (seq === listSeq) listLoading.value = false
    }
  }

  /** 调一个返回整体状态的命令；期间收到过 `upload:state` 就不用命令的结果覆盖（事件更新）。 */
  async function withStatus(run: () => Promise<QueueStatus>): Promise<QueueStatus> {
    const seq = stateSeq
    const s = await run()
    if (alive && seq === stateSeq) status.value = s
    return s
  }

  /** 调一个返回热文件夹状态的命令；期间收到过 `hotfolder:state` 就不用命令的结果覆盖。 */
  async function withHotfolder(run: () => Promise<HotFolderStatus>): Promise<HotFolderStatus> {
    const seq = hotfolderSeq
    const h = await run()
    if (alive && seq === hotfolderSeq) hotfolder.value = h
    return h
  }

  async function refreshStatus() {
    await Promise.all([withStatus(uploadStatus), withHotfolder(hotfolderStatus)])
  }

  async function load() {
    state.value = 'loading'
    loadError.value = ''
    try {
      // 先订阅，再读：读的期间发生的变化不会丢（见文件开头）。
      stop ??= await listenQueueEvents({
        item: upsert,
        itemRemoved: remove,
        progress: (p) => {
          const item = items.value.get(p.id)
          if (!item || item.status !== 'uploading') return
          progress.value.set(p.id, p)
          triggerRef(progress)
        },
        state: (s) => {
          stateSeq++
          status.value = s
        },
        hotfolder: (h) => {
          hotfolderSeq++
          hotfolder.value = h
        },
        authRequired: () => handlers.onAuthRequired?.(),
      })
      if (!alive) {
        stop()
        return
      }
      await refreshStatus()
      await loadList()
      if (alive) state.value = 'ready'
    } catch (e) {
      if (!alive) return
      loadError.value = shellErrorMessage(e)
      state.value = 'error'
    }
  }

  watch(filter, () => {
    if (state.value === 'ready') void loadList().catch((e: unknown) => (loadError.value = shellErrorMessage(e)))
  })

  onMounted(() => {
    void load()
    clock = setInterval(() => (now.value = Date.now()), 1000)
  })

  onBeforeUnmount(() => {
    alive = false
    stop?.()
    stop = null
    if (clock) clearInterval(clock)
  })

  return {
    status,
    hotfolder,
    rows,
    filter,
    state,
    loadError,
    listLoading,
    now,
    progressOf,
    load,
    refreshStatus,
    loadList,
    withStatus,
    withHotfolder,
  }
}
