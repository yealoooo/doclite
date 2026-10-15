// 原图加载失败的原因（M2-4）。查看器按 `kind` 显示三种提示之一：超出预算（只显示封面，提示下载原图或改用客户端）、
// 解不了（只显示封面，提示下载原图）、加载失败（可以重试）。

export type ViewerFailureKind = 'too_large' | 'unsupported' | 'failed' | 'aborted'

export class ViewerLoadError extends Error {
  /** 错误码：浏览器里是 Worker 或下载的（too_large、over_budget、unsupported、orientation、corrupt、timeout、http、
   * network、internal……），壳里是 img 协议的（见 lib/image-url.ts 的 TileSourceError）。 */
  readonly code: string
  /** HTTP 状态码（有的话）。 */
  readonly status?: number
  /** 这次失败是在壳里切块时发生的（文案不提"改用客户端"）。 */
  readonly shell: boolean
  /**
   * 文件本身的问题（浏览器里读了文件头、解码之后才知道的 too_large、unsupported、orientation、corrupt，以及解码超时）：
   * 本次会话内记住这个快照，再打开时不再下载（和壳的 422 一样，见 load-original.ts）。
   */
  readonly permanent: boolean

  constructor(code: string, message: string, status?: number, shell = false, permanent = false) {
    super(message)
    this.code = code
    this.status = status
    this.shell = shell
    this.permanent = permanent
  }

  get kind(): ViewerFailureKind {
    return failureKind(this.code, this.status, this.shell)
  }
}

/** 错误码 → 提示的类别。 */
export function failureKind(code: string, status?: number, shell = false): ViewerFailureKind {
  if (code === 'aborted') return 'aborted'
  // over_budget：浏览器里同时打开的原图共用的内存预算不够（对比时的另一侧占着），按超出预算提示。
  if (code === 'too_large' || code === 'over_budget') return 'too_large'
  // timeout：浏览器里解码超时、壳里切块超时（422），都按"解不了"提示，不给重试（M2-4 审查第 1 轮）。
  if (['unsupported', 'unsupported_format', 'orientation', 'corrupt', 'timeout'].includes(code)) return 'unsupported'
  // 壳里 422 的其他原因本次运行内不会再试，按"解不了"提示。
  if (shell && status === 422) return 'unsupported'
  return 'failed'
}
