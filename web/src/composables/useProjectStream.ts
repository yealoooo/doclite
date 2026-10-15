// 订阅当前项目的实时事件（M1-10）：`GET /api/projects/{pid}/stream`，经 `openStream()`（浏览器是 fetch 流，
// 壳里走 Rust 的 api_stream）。
//
// - 断线（网络错误、server 结束流、5xx）后按指数退避重连：1、2、4、8、16、30 秒（封顶 30 秒，±20% 抖动）；
//   连接保持过 10 秒才把退避清零，免得 server 一连上就断时每秒重连一次。
// - 空闲超时：45 秒没有收到任何数据（server 每 15 秒发一条保活注释）就当作连接已半开，主动断开重连。
//   和壳的 `net/stream.rs`（DEFAULT_IDLE_TIMEOUT 45 秒）一致。
// - 断开期间的事件不补发：每次重新连上都调 `onOpen(false)`，调用方应整体刷新一次。
//   第一次连上调 `onOpen(true)`（页面的首次加载可能早于连接建立，调用方可以按需补刷一次）。
// - 401（会话失效，请求封装会跳登录页）、403、404（项目进不去了）不再重连，调 `onStopped(err)`。
// - 项目 id 变了（切换项目）或组件卸载时断开；离开页面（pagehide）时断开，从往返缓存回来（pageshow persisted）
//   时重新连接，按重连处理（`onOpen(false)`）。
//
//   const stream = useProjectStream(() => project.currentId, {
//     onEvent: (e) => …,              // 已解析好的 ProjectEvent（只有本项目的）
//     onOpen: (first) => { if (!first) refreshAll() },
//     onStopped: (err) => …,          // 可选
//   })
//   stream.state.value                // 'idle' | 'connecting' | 'open' | 'retrying' | 'stopped'

import { getCurrentScope, onScopeDispose, ref, watch } from 'vue'

import { type ApiError, isApiError } from '@/api/http'
import { openStream } from '@/api/stream'
import type { ProjectEvent } from '@/api/types'

export type StreamState = 'idle' | 'connecting' | 'open' | 'retrying' | 'stopped'

export interface ProjectStreamHandlers {
  onEvent: (event: ProjectEvent) => void
  /** 连接建立：first 为 true 表示这个项目的第一次连接，否则是断线后重新连上。 */
  onOpen?: (first: boolean) => void
  /** 因 401、403、404 停止重连。 */
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
          handlers.onStopped?.(e)
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
