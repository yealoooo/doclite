// 订阅当前项目的实时事件（M1-10）：`GET /api/projects/{pid}/stream`，经 `openStream()`（浏览器是 fetch 流，
// 壳里走壳的 api_stream）。
//
// - 断线（网络错误、server 结束流、5xx）后按指数退避重连：1、2、4、8、16、30 秒（封顶 30 秒，±20% 抖动）；
//   连接保持过 10 秒才把退避清零，免得 server 一连上就断时每秒重连一次。
// - 空闲超时：45 秒没有收到任何数据（server 每 15 秒发一条保活注释）就当作连接已半开，主动断开重连。
// - 断开期间的事件不补发：每次重新连上都调 `onOpen(false)`，调用方应整体刷新一次。
//   第一次连上调 `onOpen(true)`（页面的首次加载可能早于连接建立，调用方可以按需补刷一次）。
// - 401（会话失效，请求封装会跳登录页）、403、404（项目进不去了）不再重连，调 `onStopped(err)`；没给 onStopped 时
//   403、404 刷新项目列表（切换器会换到还能进的项目，页面随之重置）。
// - 项目 id 变了（切换项目）或组件卸载时断开；离开页面（pagehide）时断开，从往返缓存回来（pageshow persisted）
//   时重新连接，按重连处理（`onOpen(false)`）。
//
//   const stream = useProjectStream(() => project.currentId, {
//     onEvent: (e) => …,              // 已解析好的 ProjectEvent（只有本项目的）
//     onOpen: (first) => { if (!first) refreshAll() },
//     onStopped: (err) => …,          // 可选
//   })
//   stream.state.value                // 'idle' | 'connecting' | 'open' | 'retrying' | 'stopped'
//
// 事件多的页面用 `useEventBatch(flush)` 合批：事件先记进页面自己的待处理集合，`schedule()` 之后由 flush 一次处理。

import { getCurrentScope, onScopeDispose, ref, watch } from 'vue'

import { type ApiError, isApiError } from '@/api/http'
import { openStream } from '@/api/stream'
import type { ProjectEvent } from '@/api/types'
import { useProjectStore } from '@/stores/project'

export type StreamState = 'idle' | 'connecting' | 'open' | 'retrying' | 'stopped'

export interface ProjectStreamHandlers {
  onEvent: (event: ProjectEvent) => void
  /** 连接建立：first 为 true 表示这个项目的第一次连接，否则是断线后重新连上。 */
  onOpen?: (first: boolean) => void
  /** 因 401、403、404 停止重连。默认：403、404 时刷新项目列表。 */
  onStopped?: (err: ApiError) => void
}

const BASE_DELAY_MS = 1000
const MAX_DELAY_MS = 30_000
/** 连接保持这么久以上，断开后从最短的退避重新开始。 */
const STABLE_MS = 10_000
/** 这么久没有收到任何数据（含保活注释）就断开重连。 */
export const STREAM_IDLE_TIMEOUT_MS = 45_000

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) return resolve()
    const t = setTimeout(done, ms)
    signal.addEventListener('abort', done, { once: true })
    function done() {
      clearTimeout(t)
      signal.removeEventListener('abort', done)
      resolve()
    }
  })
}

/** 不该重连的错误：会话失效、没权限、项目不存在。 */
function isFatal(e: unknown): e is ApiError {
  return isApiError(e) && e.status >= 400 && e.status < 500 && e.status !== 408 && e.status !== 429
}

export function useProjectStream(projectId: () => string | null, handlers: ProjectStreamHandlers) {
  const project = useProjectStore()
  const onStopped =
    handlers.onStopped ??
    ((err: ApiError) => {
      if (err.status === 403 || err.status === 404) void project.refresh()
    })
  const state = ref<StreamState>('idle')
  let controller: AbortController | null = null

  /** 一次连接：返回时连接已结束（正常结束、出错、空闲超时或被取消）。 */
  async function attempt(pid: string, signal: AbortSignal, onOpen: () => void): Promise<{ openedAt: number }> {
    // 每次尝试用自己的 AbortController：外面取消时跟着取消，空闲超时时只断开这一次。
    const ctrl = new AbortController()
    const forward = () => ctrl.abort()
    signal.addEventListener('abort', forward, { once: true })
    let idleTimer: ReturnType<typeof setTimeout> | null = null
    const bump = () => {
      if (idleTimer) clearTimeout(idleTimer)
      idleTimer = setTimeout(() => ctrl.abort(), STREAM_IDLE_TIMEOUT_MS)
    }
    let openedAt = 0
    bump()
    try {
      await openStream(`/api/projects/${pid}/stream`, {
        signal: ctrl.signal,
        onOpen: () => {
          openedAt = Date.now()
          bump()
          onOpen()
        },
        onChunk: bump,
        onEvent: (ev) => {
          let event: ProjectEvent
          try {
            event = JSON.parse(ev.data) as ProjectEvent
          } catch {
            return
          }
          if (event.project_id === pid) handlers.onEvent(event)
        },
      })
    } finally {
      if (idleTimer) clearTimeout(idleTimer)
      signal.removeEventListener('abort', forward)
    }
    return { openedAt }
  }

  /** resumed：从往返缓存回来，第一次连上也按重连处理。 */
  async function run(pid: string, signal: AbortSignal, resumed: boolean) {
    let tries = 0
    let opened = resumed
    while (!signal.aborted) {
      state.value = opened || tries > 0 ? 'retrying' : 'connecting'
      let openedAt = 0
      try {
        const r = await attempt(pid, signal, () => {
          state.value = 'open'
          const first = !opened
          opened = true
          handlers.onOpen?.(first)
        })
        openedAt = r.openedAt
      } catch (e) {
        if (signal.aborted) return
        if (isFatal(e)) {
          state.value = 'stopped'
          onStopped(e)
          return
        }
      }
      if (signal.aborted) return
      if (openedAt && Date.now() - openedAt >= STABLE_MS) tries = 0
      const delay = Math.min(MAX_DELAY_MS, BASE_DELAY_MS * 2 ** tries) * (0.8 + Math.random() * 0.4)
      tries++
      state.value = 'retrying'
      await sleep(delay, signal)
    }
  }

  function stop() {
    controller?.abort()
    controller = null
  }

  function start(pid: string | null, resumed = false) {
    stop()
    state.value = 'idle'
    if (!pid) return
    controller = new AbortController()
    void run(pid, controller.signal, resumed)
  }

  watch(projectId, (pid) => start(pid), { immediate: true })

  // 离开页面（整页跳转、关闭、进入往返缓存）时主动断开：浏览器不一定马上取消进行中的流式请求，
  // 留着的连接会占住 HTTP/1.1 的连接数。从往返缓存回来时重新连接，按重连处理（调用方整体刷新）。
  const onPageHide = () => {
    stop()
    state.value = 'idle'
  }
  const onPageShow = (e: PageTransitionEvent) => {
    if (e.persisted) start(projectId(), true)
  }
  window.addEventListener('pagehide', onPageHide)
  window.addEventListener('pageshow', onPageShow)

  if (getCurrentScope()) {
    onScopeDispose(() => {
      stop()
      window.removeEventListener('pagehide', onPageHide)
      window.removeEventListener('pageshow', onPageShow)
    })
  }

  return { state, stop }
}

export interface EventBatchOptions {
  /** 最后一次 schedule 之后等多久（毫秒）。默认 300。 */
  debounceMs?: number
  /** 从第一次 schedule 起最多等多久（毫秒）。默认 1000。 */
  maxWaitMs?: number
  /**
   * 连续不断地有事件时（上一轮刚跑完不到 2 秒又来了，例如上传、批量操作期间）最多等多久（毫秒），让重取少一些。
   * 这时去抖也放宽到 2 秒（上传时大约一秒一条，300 ms 的去抖每条都会跑一轮）。默认同 maxWaitMs。
   */
  burstMaxWaitMs?: number
}

/** 两轮之间不到这么久算"连续不断"。 */
const BURST_WINDOW_MS = 2000

/**
 * 实时事件合批：`schedule()` 之后，最后一次调用起 debounceMs、从第一次起最多 maxWaitMs 跑一次 `flush`。flush 正在跑时
 * 不叠加：跑完马上再来一轮（这期间记下的事件一起处理）。组件卸载时取消。
 */
export function useEventBatch(flush: () => void | Promise<void>, opts: EventBatchOptions = {}) {
  const debounceMs = opts.debounceMs ?? 300
  const maxWaitMs = opts.maxWaitMs ?? 1000
  const burstMaxWaitMs = opts.burstMaxWaitMs ?? maxWaitMs
  let timer: ReturnType<typeof setTimeout> | null = null
  let firstAt = 0
  let lastRunAt = 0
  let running = false
  let again = false
  let disposed = false

  function schedule() {
    if (disposed) return
    const now = Date.now()
    if (!firstAt) firstAt = now
    if (timer) clearTimeout(timer)
    const burst = burstMaxWaitMs > maxWaitMs && now - lastRunAt < BURST_WINDOW_MS
    const max = burst ? burstMaxWaitMs : maxWaitMs
    const wait = burst ? Math.max(debounceMs, BURST_WINDOW_MS) : debounceMs
    timer = setTimeout(() => void run(), Math.min(wait, Math.max(0, firstAt + max - now)))
  }

  async function run() {
    timer = null
    firstAt = 0
    if (disposed) return
    if (running) {
      again = true
      return
    }
    running = true
    try {
      await flush()
    } catch {
      // 单个请求的失败各自处理过了。
    } finally {
      running = false
      lastRunAt = Date.now()
      if (again && !disposed) {
        again = false
        void run()
      }
    }
  }

  function cancel() {
    if (timer) clearTimeout(timer)
    timer = null
    firstAt = 0
  }

  if (getCurrentScope()) {
    onScopeDispose(() => {
      disposed = true
      cancel()
    })
  }

  return { schedule, cancel }
}
