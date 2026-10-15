// SSE 订阅：`openStream()`。以后的实时订阅（例如 `GET /api/projects/{pid}/stream`）都用它，不用 EventSource。
// - 浏览器里是同源 fetch + ReadableStream（带 Cookie）；
// - 壳里走 Rust 的 `api_stream`（tauri Channel 逐块推送；wry 的自定义协议是整块缓冲的，SSE 不能走协议）。
// 两边都用 `SseParser` 解析帧，行为一致。

import { isTauri } from '@/lib/client'
import { shellStream, shellStreamCancel, type ShellStreamEvent } from '@/lib/shell'
import { SseParser, type SseEvent } from '@/lib/sse'

import {
  ApiError,
  buildUrl,
  failResponse,
  networkError,
  queryPairs,
  shellTransportError,
  type QueryValue,
} from './http'

export type { SseEvent } from '@/lib/sse'

export interface OpenStreamOptions {
  query?: Record<string, QueryValue>
  /** 取消订阅（组件卸载、切换项目时）。取消后 Promise resolve，不再回调。 */
  signal?: AbortSignal
  /** 连接建立（收到 2xx 响应头）时调用。 */
  onOpen?: () => void
  /** 每个完整的事件调用一次。 */
  onEvent: (event: SseEvent) => void
  /**
   * 连接建立后每收到一块数据调用一次（含 `:` 开头的保活注释，它们不会产生事件）。可选（M1-10）：
   * 调用方用它做空闲超时，判断半开的连接。
   */
  onChunk?: () => void
}

/** 错误响应不弹 toast（由调用方决定怎么重连或提示）；401、403、426 的全局处理照常执行。 */
const STREAM_ERROR_OPTIONS = { handle: true } as const

/**
 * 订阅 SSE。
 * - 服务器正常结束流、或用 `signal` 取消时，Promise resolve；
 * - 非 2xx 时 reject ApiError（错误体按 `{code, message, details?}` 解析）；
 * - 连不上、中途断开时 reject ApiError（status 0，code `network`）。
 * 不自动重连：事件可能在断开期间丢失，调用方重连前应先整体刷新一次。
 */
export function openStream(path: string, opts: OpenStreamOptions): Promise<void> {
  return isTauri() ? openShellStream(path, opts) : openBrowserStream(path, opts)
}

function emit(opts: OpenStreamOptions, events: SseEvent[]) {
  for (const e of events) {
    try {
      opts.onEvent(e)
    } catch (err) {
      console.error('SSE 事件处理出错', err)
    }
  }
}

async function openBrowserStream(path: string, opts: OpenStreamOptions): Promise<void> {
  if (opts.signal?.aborted) return
  let res: Response
  try {
    res = await fetch(buildUrl(path, opts.query), {
      headers: { Accept: 'text/event-stream' },
      credentials: 'same-origin',
      cache: 'no-store',
      signal: opts.signal,
    })
  } catch {
    if (opts.signal?.aborted) return
    throw networkError()
  }
  if (!res.ok || !res.body) {
    const text = await res.text().catch(() => '')
    throw failResponse(path, res.status, text, STREAM_ERROR_OPTIONS)
  }
  opts.onOpen?.()

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  const parser = new SseParser()
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      opts.onChunk?.()
      emit(opts, parser.push(decoder.decode(value, { stream: true })))
    }
    emit(opts, parser.push(decoder.decode()))
  } catch {
    if (opts.signal?.aborted) return
    throw networkError()
  }
}

function openShellStream(path: string, opts: OpenStreamOptions): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    if (opts.signal?.aborted) {
      resolve()
      return
    }
    const parser = new SseParser()
    let status = 0
    let errorBody = ''
    let streamId: number | null = null
    let aborted = false
    let settled = false

    const ok = () => status >= 200 && status < 300
    const finish = (err?: ApiError) => {
      if (settled) return
      settled = true
      opts.signal?.removeEventListener('abort', onAbort)
      if (err) reject(err)
      else resolve()
    }
    const onAbort = () => {
      aborted = true
      if (streamId !== null) void shellStreamCancel(streamId).catch(() => {})
      finish()
    }
    opts.signal?.addEventListener('abort', onAbort, { once: true })

    const onMessage = (m: ShellStreamEvent) => {
      if (settled) return
      switch (m.event) {
        case 'open':
          status = m.status
          if (ok()) opts.onOpen?.()
          break
        case 'chunk':
          if (ok()) {
            opts.onChunk?.()
            emit(opts, parser.push(m.data))
          } else errorBody += m.data
          break
        case 'end':
          finish(ok() ? undefined : failResponse(path, status, errorBody, STREAM_ERROR_OPTIONS))
          break
        case 'error':
          finish(shellTransportError({ kind: m.kind, message: m.message }))
          break
      }
    }

    shellStream(
      { method: 'GET', path, query: queryPairs(opts.query), headers: { Accept: 'text/event-stream' } },
      onMessage,
    )
      .then((id) => {
        streamId = id
        // invoke 返回之前就被取消了：现在才知道 id，补一次取消。
        if (aborted) void shellStreamCancel(id).catch(() => {})
      })
      .catch((e: unknown) => finish(shellTransportError(e)))
  })
}
