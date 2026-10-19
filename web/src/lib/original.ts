// 下载原图（/files 的右键菜单和选择栏、查看原图的对话框、审核台）：客户端里交给系统浏览器（能力 external，壳的导航
// 保护不许页面离开），浏览器里在新窗口打开预签名的 original_url。地址只接受 http、https（lib/download.ts 的 httpHref）。
// 地址剩不到 10 分钟就重新取再打开（urlFresh）。

import { toast } from 'vue-sonner'

import { batchFiles } from '@/api/files'
import { errorMessage, HANDLE_ALL } from '@/api/http'
import { isShell } from '@/lib/client'
import { httpHref, openInSystemBrowser } from '@/lib/download'
import { shellErrorMessage } from '@/lib/shell'
import { useImageInfoStore } from '@/stores/image-info'

/** 预签名地址还能用多久以上才算新鲜（打开原图、对比的两侧）。 */
const URL_FRESH_MS = 10 * 60 * 1000

/** 预签名地址（`url_expires_at`）还能用 10 分钟以上。 */
export function urlFresh(expiresAt: string | null | undefined): boolean {
  return !!expiresAt && Date.parse(expiresAt) - Date.now() > URL_FRESH_MS
}

/** 一个原图地址；取不到时 `url` 为 null，`error` 写原因。 */
export interface OriginalUrl {
  url: string | null
  error?: string
}

/** 文件当前版本的原图地址：image-info store 里的快过期了就重新 batch。 */
export async function currentOriginalUrl(fileId: string): Promise<OriginalUrl> {
  let item = useImageInfoStore().get(fileId)
  if (!item || !urlFresh(item.url_expires_at)) {
    try {
      item = (await batchFiles([fileId], HANDLE_ALL)).items[0]
    } catch (e) {
      return { url: null, error: errorMessage(e) }
    }
    if (!item) return { url: null, error: '文件已不存在（可能刚被删除）' }
  }
  return { url: item.original_url, error: item.url_error?.message }
}

/** 打开原图地址（取不到时提示原因）。成功、失败都由这里提示。 */
export async function openOriginal(o: OriginalUrl) {
  if (!o.url) {
    toast.error('取不到原图地址', { description: o.error })
    return
  }
  try {
    const href = httpHref(o.url, '原图地址')
    if (isShell()) {
      await openInSystemBrowser(href)
      toast.success('已在系统浏览器里打开原图', { description: '另存时的文件名是 v1.jpg 这样的形式。' })
    } else {
      window.open(href, '_blank', 'noopener,noreferrer')
    }
  } catch (e) {
    toast.error(shellErrorMessage(e))
  }
}
