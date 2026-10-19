// 查看器状态的两个小工具（/files 的查看原图、审核台共用）。

import type { ViewerStatus } from './ImageViewer.vue'

/** 这几种失败是图片信息过期了：原图地址过期（存储返回 403）、壳里没有来源记录（404）、取不到地址。 */
export function staleInfo(s: ViewerStatus): boolean {
  return s.phase === 'failed' && ['http', 'not_found', 'no_url'].includes(s.code ?? '')
}

/** 状态栏上的一句话。 */
export function viewerStatusText(s: ViewerStatus | null | undefined): string {
  if (!s) return ''
  switch (s.phase) {
    case 'loading':
      return s.cover === 'loaded' ? '原图加载中 · 先显示封面' : '原图加载中'
    case 'ready':
      if (s.elapsedMs == null) return '原图'
      return s.elapsedMs < 1000 ? `原图 · ${s.elapsedMs} ms` : `原图 · ${(s.elapsedMs / 1000).toFixed(1)} 秒`
    case 'too_large':
      return '原图太大 · 只显示封面'
    case 'unsupported':
      return '看不了原图 · 只显示封面'
    case 'failed':
      return '原图加载失败'
    default:
      return ''
  }
}
