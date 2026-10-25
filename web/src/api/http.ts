// 统一的请求封装。所有接口都走 `request()`：
// - 浏览器里是同源 fetch，带 Cookie（会话是 HttpOnly Cookie `doclite_session`）；
// - 在桌面客户端（壳，Wails）里由壳的 Go 端代理（`api_fetch`，requirements 第 6 节"会话"）：会话 Cookie、
//   `X-Client-Type: desktop`、`X-Client-Version` 都由壳统一加，页面不自己加；`request()` 的用法两边完全一样；
// - 出错时解析 `{code, message, details?}`，统一抛出 `ApiError`；
// - 401：交给 `onUnauthorized`（清会话、跳登录页）；登录接口本身和 `authRedirect: false` 的请求除外；
// - 403 `forbidden`：交给 `onForbidden`（重新拉当前用户和菜单、跳无权提示页），属于路由级处理；
// - 403 `admin_required`：交给 `onAdminRequired`（重新拉当前用户和菜单，让防提权预判跟上最新权限），
//   message 照常提示（能进页面但操作要管理员，在原页面提示）；
// - 426 `client_upgrade_required`（只会在壳里出现）：交给 `onUpgradeRequired`（整页提示更新）；
// - 403 `license_expired`、`license_missing`（M7-1：许可证到期或没有有效的许可证，非管理员的请求被拒）：交给
//   `onLicenseBlocked`（提示后回登录页）；登录接口本身除外（登录页自己显示）；
// - 其他错误默认用 toast 提示 message；调用方要自己在原位置显示的，用 `handle` 声明接管哪些错误码
//   （或 `HANDLE_ALL` 接管全部），这些错误就不再弹 toast。上面几种全局处理过的错误带 `handledGlobally`，
//   调用方自己提示时用 `toastOpError()` 跳过它们（免得提示两次）。
// SSE 订阅用 `api/stream.ts` 的 `openStream()`。

import { toast } from 'vue-sonner'

import { isShell } from '@/lib/client'
import { isShellError, shellFetch } from '@/lib/shell'

export class ApiError extends Error {
  /** HTTP 状态码；网络不通时为 0。 */
  readonly status: number
  /** 后端的错误码，例如 `invalid_input`、`admin_required`；网络不通时为 `network`。 */
  readonly code: string
  /** 错误体里的 `details`（可选）：426 的升级信息、409 `duplicate_content` 的已有文件位置等。 */
  readonly details: unknown
  /** 请求封装已经全局处理过（并提示过）：401、403 `forbidden`、许可证挡住的 403、426。 */
  handledGlobally = false

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

/** 操作失败给人看的一句话：`byCode` 里有这个错误码的用它（补上"该怎么办"），没有的用 server 的 message。 */
export function errorText(err: unknown, byCode: Partial<Record<string, string>>): string {
  return err instanceof ApiError ? (byCode[err.code] ?? err.message) : errorMessage(err)
}

export type QueryValue = string | number | boolean | null | undefined

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  /** 请求体，按 JSON 发送。 */
  body?: unknown
  /** 查询参数；值为 null、undefined 或空字符串的项不发送。 */
  query?: Record<string, QueryValue>
  /**
   * 调用方自己处理的错误码（`HANDLE_ALL` 表示全部），这些错误不弹 toast。
   * 401、403 `forbidden`、403 `admin_required`、426 的全局处理不受影响。
   */
  handle?: Handle
  /** 为 false 时 401 不触发全局的"跳登录页"（启动时探测登录状态用）。默认 true。 */
  authRedirect?: boolean
  /**
   * 403 `forbidden` 不走全局处理（不刷新会话、不弹全局提示、不跳无权页），直接抛给调用方（没在 `handle` 里接管时
   * 照常弹 toast）。用于"地址栏里的 id 可能指向进不了的项目"这类探测（M3-6）：进不了就当作没有这个东西。
   */
  quiet403?: boolean
}

/** 调用方自己处理的错误码；`HANDLE_ALL`（true）表示全部。各 api 函数的最后一个参数。 */
export type Handle = true | readonly string[] | undefined

/** 调用方自己处理全部错误（不弹 toast）。 */
export const HANDLE_ALL = true

/** 请求封装已经全局处理过（并提示过）的错误：401（跳登录页）、403 `forbidden`（刷新菜单）、许可证挡住、426（升级提示）。 */
export function handledGlobally(e: unknown): boolean {
  return e instanceof ApiError && e.handledGlobally
}

/** 操作失败的提示（全局已经提示过的不再重复）。调用方传了 `HANDLE_ALL` 时由它在这里提示一次。 */
export function toastOpError(e: unknown, text = errorMessage(e), description?: string) {
  if (handledGlobally(e)) return
  toast.error(text, description ? { description } : undefined)
}

/** 4xx：这次操作的前提已经不成立（状态变了、文件夹没了、超过上限……），重试也没用。 */
export function isClientError(e: unknown): boolean {
  return e instanceof ApiError && e.status >= 400 && e.status < 500
}

interface Handlers {
  onUnauthorized: (err: ApiError) => void
  onForbidden: (err: ApiError) => void
  onAdminRequired: (err: ApiError) => void
  /** 426 `client_upgrade_required`：壳的版本低于服务器要求的最低版本（details 见 design §5）。 */
  onUpgradeRequired?: (err: ApiError) => void
  /**
   * M7-1：403 `license_expired`、`license_missing`（非管理员被许可证挡住）。`redirect` 为 false 时是启动时的
   * 登录状态探测（`authRedirect: false`），只提示、不跳转（路由守卫会去登录页）。
   */
  onLicenseBlocked?: (err: ApiError, redirect: boolean) => void
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

/** M7-1：许可证挡住非管理员时的错误码（403）。 */
export const LICENSE_BLOCK_CODES: readonly string[] = ['license_expired', 'license_missing']

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
  opts: Pick<RequestOptions, 'handle' | 'authRedirect' | 'quiet403'>,
): ApiError {
  const err = parseError(status, text)
  if (status === 401 && path !== LOGIN_PATH) {
    err.handledGlobally = true
    if (opts.authRedirect !== false) handlers.onUnauthorized(err)
    return err
  }
  if (status === 403 && err.code === 'forbidden' && !opts.quiet403) {
    err.handledGlobally = true
    handlers.onForbidden(err)
    return err
  }
  if (status === 403 && LICENSE_BLOCK_CODES.includes(err.code) && path !== LOGIN_PATH) {
    err.handledGlobally = true
    handlers.onLicenseBlocked?.(err, opts.authRedirect !== false)
    return err
  }
  if (status === 426 && err.code === 'client_upgrade_required') {
    err.handledGlobally = true
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

/** 发出请求：壳里走 Go 代理，浏览器里是同源 fetch。连不上时抛出 ApiError（status 0）。 */
async function send(
  method: string,
  path: string,
  query: Record<string, QueryValue> | undefined,
  headers: Record<string, string>,
  body: string | undefined,
): Promise<RawResponse> {
  if (isShell()) {
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
