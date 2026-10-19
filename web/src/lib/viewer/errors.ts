// 原图加载失败的原因（M2-4）。查看器按 `kind` 显示三种提示之一：太大（只显示封面，提示下载原图）、解不了（只显示
// 封面，提示下载原图；浏览器里看不了原图也算这种）、加载失败（可以重试）。

export type ViewerFailureKind = 'too_large' | 'unsupported' | 'failed' | 'aborted'

export class ViewerLoadError extends Error {
  /** 错误码：壳的 img 协议的（见 lib/image-url.ts 的 TileSourceError），或 shell_only、no_url、tiles_failed、internal。 */
  readonly code: string
  /** HTTP 状态码（有的话）。 */
  readonly status?: number

  constructor(code: string, message: string, status?: number) {
    super(message)
    this.code = code
    this.status = status
  }

  get kind(): ViewerFailureKind {
    return failureKind(this.code, this.status)
  }
}

/** 错误码 → 提示的类别。 */
function failureKind(code: string, status?: number): ViewerFailureKind {
  if (code === 'aborted') return 'aborted'
  if (code === 'too_large') return 'too_large'
  // timeout：壳里切块超时（422），按"解不了"提示，不给重试（M2-4 审查第 1 轮）。shell_only：浏览器里不看原图。
  if (['unsupported', 'unsupported_format', 'orientation', 'corrupt', 'timeout', 'shell_only'].includes(code)) return 'unsupported'
  // 壳里 422 的其他原因本次运行内不会再试，按"解不了"提示。
  if (status === 422) return 'unsupported'
  return 'failed'
}
