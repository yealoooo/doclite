// 统一的请求封装。所有接口都走 `request()`：
// - 浏览器里是同源 fetch，带 Cookie（会话是 HttpOnly Cookie `doclite_session`）；
// - 在 Tauri 壳里由 Rust 代理（`api_fetch`，design §3）：会话 Cookie、`X-Client-Type: desktop`、
//   `X-Client-Version` 都由 Rust 统一加，页面不自己加；`request()` 的用法两边完全一样；
// - 出错时解析 `{code, message, details?}`，统一抛出 `ApiError`；
// - 401：交给 `onUnauthorized`（清会话、跳登录页）；登录接口本身和 `authRedirect: false` 的请求除外；
// - 403 `forbidden`：交给 `onForbidden`（重新拉当前用户和菜单、跳无权提示页），属于路由级处理；
// - 403 `admin_required`：交给 `onAdminRequired`（重新拉当前用户和菜单，让防提权预判跟上最新权限），
//   message 照常提示（能进页面但操作要管理员，在原页面提示）；
// - 426 `client_upgrade_required`（只会在壳里出现）：交给 `onUpgradeRequired`（整页提示更新）；
// - 其他错误默认用 toast 提示 message；调用方要自己在原位置显示的，用 `handle` 声明接管哪些错误码
//   （或 `true` 接管全部），这些错误就不再弹 toast。
// SSE 订阅用 `api/stream.ts` 的 `openStream()`。

import { toast } from 'vue-sonner'

import { isTauri } from '@/lib/client'
import { isShellError, shellFetch } from '@/lib/shell'

export class ApiError extends Error {
  /** HTTP 状态码；网络不通时为 0。 */
  readonly status: number
  /** 后端的错误码，例如 `invalid_input`、`admin_required`；网络不通时为 `network`。 */
  readonly code: string
  /** 错误体里的 `details`（可选）：426 的升级信息、409 `duplicate_content` 的已有文件位置等。 */
  readonly details: unknown

  constructor(status: number, code: string, message: string, details?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.details = details
  }
}

/** 是否是接口错误；给了 `code` 时还要求错误码相同。 */
export function isApiError(err: unknown, code?: string): err is ApiError {
  return err instanceof ApiError && (code === undefined || err.code === code)
}

/** 任意错误转成给人看的一句话。 */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message
  if (err instanceof Error) return err.message
  return String(err)
}

export type QueryValue = string | number | boolean | null | undefined

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  /** 请求体，按 JSON 发送。 */
  body?: unknown
  /** 查询参数；值为 null、undefined 或空字符串的项不发送。 */
  query?: Record<string, QueryValue>
  /**
   * 调用方自己处理的错误码（`true` 表示全部），这些错误不弹 toast。
   * 401、403 `forbidden`、403 `admin_required`、426 的全局处理不受影响。
   */
  handle?: true | readonly string[]
  /** 为 false 时 401 不触发全局的"跳登录页"（启动时探测登录状态用）。默认 true。 */
  authRedirect?: boolean
}

interface Handlers {
  onUnauthorized: (err: ApiError) => void
  onForbidden: (err: ApiError) => void
  onAdminRequired: (err: ApiError) => void
  /** 426 `client_upgrade_required`：壳的版本低于服务器要求的最低版本（details 见 design §5）。 */
  onUpgradeRequired?: (err: ApiError) => void
}

let handlers: Handlers = {
  onUnauthorized: () => {},
  onForbidden: () => {},
  onAdminRequired: () => {},
}

/** 在 main.ts 里设置 401、403、426 的全局处理（避免 http 和 router、store 循环引用）。 */
export function setHttpHandlers(h: Handlers) {
  handlers = h
}

const LOGIN_PATH = '/api/auth/login'

/** 查询参数去掉空值后的键值对（顺序不变）。 */
export function queryPairs(query?: Record<string, QueryValue>): [string, string][] {
  if (!query) return []
  const pairs: [string, string][] = []
  for (const [k, v] of Object.entries(query)) {
    if (v === null || v === undefined || v === '') continue
    pairs.push([k, String(v)])
  }
  return pairs
}

export function buildUrl(path: string, query?: Record<string, QueryValue>): string {
  const qs = new URLSearchParams(queryPairs(query)).toString()
  return qs ? `${path}?${qs}` : path
}

/** 网络不通（连不上、超时、连接中断）。 */
export function networkError(): ApiError {
  return new ApiError(0, 'network', '无法连接服务器，请检查网络后重试')
}

/** 壳的命令失败（`{kind, message}`）转成 ApiError：连不上是 `network`，其余保留 kind。 */
export function shellTransportError(e: unknown): ApiError {
  if (isShellError(e)) {
    return e.kind === 'network' ? networkError() : new ApiError(0, e.kind, e.message)
  }
  return new ApiError(0, 'internal', e instanceof Error ? e.message : String(e))
}

function parseError(status: number, text: string): ApiError {
  let code = `http_${status}`
  let message = `服务器返回错误（HTTP ${status}）`
  let details: unknown
  try {
    const body = JSON.parse(text) as { code?: unknown; message?: unknown; details?: unknown }
    if (typeof body.code === 'string') code = body.code
    if (typeof body.message === 'string' && body.message) message = body.message
    details = body.details
  } catch {
    // 不是 JSON（例如代理返回的 502 页面），用默认说明。
  }
  return new ApiError(status, code, message, details)
}

function handled(opts: Pick<RequestOptions, 'handle'>, code: string): boolean {
  return opts.handle === true || (Array.isArray(opts.handle) && opts.handle.includes(code))
}

/**
 * 非 2xx 响应：解析错误体，执行 401、403、426 的全局处理，按 `handle` 决定是否弹 toast，返回要抛出的 ApiError。
 * `request()` 和 `openStream()` 共用。
 */
export function failResponse(
  path: string,
  status: number,
  text: string,
  opts: Pick<RequestOptions, 'handle' | 'authRedirect'>,
): ApiError {
  const err = parseError(status, text)
  if (status === 401 && path !== LOGIN_PATH) {
    if (opts.authRedirect !== false) handlers.onUnauthorized(err)
    return err
  }
  if (status === 403 && err.code === 'forbidden') {
    handlers.onForbidden(err)
    return err
  }
  if (status === 426 && err.code === 'client_upgrade_required') {
    handlers.onUpgradeRequired?.(err)
    return err
  }
  if (status === 403 && err.code === 'admin_required') handlers.onAdminRequired(err)
  if (!handled(opts, err.code)) toast.error(err.message)
  return err
}

interface RawResponse {
  status: number
  body: string
}

/** 发出请求：壳里走 Rust 代理，浏览器里是同源 fetch。连不上时抛出 ApiError（status 0）。 */
async function send(
  method: string,
  path: string,
  query: Record<string, QueryValue> | undefined,
  headers: Record<string, string>,
  body: string | undefined,
): Promise<RawResponse> {
  if (isTauri()) {
    try {
      const res = await shellFetch({ method, path, query: queryPairs(query), headers, body })
      return { status: res.status, body: res.body }
    } catch (e) {
      throw shellTransportError(e)
    }
  }
  try {
    const res = await fetch(buildUrl(path, query), {
      method,
      headers,
      body,
      credentials: 'same-origin',
    })
    return { status: res.status, body: await res.text() }
  } catch {
    throw networkError()
  }
}

export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'

  let res: RawResponse
  try {
    res = await send(
      opts.method ?? 'GET',
      path,
      opts.query,
      headers,
      opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    )
  } catch (e) {
    const err = e instanceof ApiError ? e : networkError()
    if (!handled(opts, err.code)) toast.error(err.message)
    throw err
  }

  if (res.status >= 200 && res.status < 300) {
    if (res.status === 204 || !res.body) return undefined as T
    return JSON.parse(res.body) as T
  }
  throw failResponse(path, res.status, res.body, opts)
}

// ---------- 上传（M1-12） ----------

/** multipart 上传的各部分（design §4.3）。浏览器不带 `content_sha256`（内网 http 不是安全上下文，不算 sha）。 */
export interface UploadParts {
  /** 原始文件名，UTF-8 原文；server 只认这个字段，不看 Content-Disposition 里的 filename。 */
  file_name: string
  /** 客户端生成的封面 JPEG（≤ 256 KiB）；生成不了时不带。 */
  cover?: Blob | null
  /** 原图。 */
  file: Blob
}

export interface UploadOptions extends Pick<RequestOptions, 'handle' | 'authRedirect'> {
  /** 取消上传：abort 后抛出 `ApiError(0, 'aborted')`，不弹提示。 */
  signal?: AbortSignal
}

export interface UploadResult<T> {
  /** 2xx 的状态码：上传接口新建是 201，"之前已上传过"是 200。 */
  status: number
  data: T
}

/**
 * multipart 上传（design §4.3）：按 `file_name` → `cover`（有的话）→ `file` 的顺序拼 FormData，用同源 fetch 发送
 * （浏览器自己定 boundary）。出错时和 `request()` 一样：解析 `{code, message, details?}` 抛 `ApiError`，
 * 401、403、426 走全局处理，其余按 `handle` 决定是否弹 toast。
 *
 * 只给浏览器用：壳里的上传走 Rust 的上传队列（`pick_and_enqueue_upload`，M1-9），页面不经代理发文件。
 */
export async function upload<T>(path: string, parts: UploadParts, opts: UploadOptions = {}): Promise<UploadResult<T>> {
  if (isTauri()) {
    throw new ApiError(0, 'unsupported', '客户端里的上传走上传队列，不能直接从页面发送文件')
  }
  const form = new FormData()
  form.append('file_name', parts.file_name)
  // 部分的文件名只用 ASCII（server 不看它；非 ASCII 的编码各浏览器不一样）。
  if (parts.cover) form.append('cover', parts.cover, 'cover.jpg')
  form.append('file', parts.file, 'original')

  let status: number
  let text: string
  try {
    const res = await fetch(path, {
      method: 'POST',
      headers: { Accept: 'application/json' },
      body: form,
      credentials: 'same-origin',
      signal: opts.signal,
    })
    status = res.status
    text = await res.text()
  } catch {
    if (opts.signal?.aborted) throw new ApiError(0, 'aborted', '已取消')
    const err = networkError()
    if (!handled(opts, err.code)) toast.error(err.message)
    throw err
  }
  if (status >= 200 && status < 300) {
    return { status, data: (text ? JSON.parse(text) : undefined) as T }
  }
  throw failResponse(path, status, text, opts)
}

// ---------- 不走全局 403 处理的读请求（M3-6 追加） ----------

/**
 * 和 `request()` 一样，只是 403 `forbidden` 不走全局处理（不刷新会话、不弹全局提示、不跳无权页），直接抛给调用方；
 * 调用方没在 `handle` 里接管时照常弹 toast。用于"地址栏里的 id 可能指向进不了的项目"这类探测（例如归类页的
 * `?pool=`，M3-6 审查第 1 轮）：进不了就当作没有这个东西，不是"你的权限变了"。401、426 照常走全局处理。
 */
export async function requestQuiet<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  let res: RawResponse
  try {
    res = await send(
      opts.method ?? 'GET',
      path,
      opts.query,
      headers,
      opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    )
  } catch (e) {
    const err = e instanceof ApiError ? e : networkError()
    if (!handled(opts, err.code)) toast.error(err.message)
    throw err
  }
  if (res.status >= 200 && res.status < 300) {
    if (res.status === 204 || !res.body) return undefined as T
    return JSON.parse(res.body) as T
  }
  if (res.status === 403) {
    const err = parseError(res.status, res.body)
    if (err.code === 'forbidden') {
      if (!handled(opts, err.code)) toast.error(err.message)
      throw err
    }
  }
  throw failResponse(path, res.status, res.body, opts)
}
