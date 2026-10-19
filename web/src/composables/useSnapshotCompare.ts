// 对比两个版本（/files 的查看原图、审核台共用，M2-4、M2-6）：对比的状态（两侧、并排还是同位切换、正在取、出错）、
// 地址过期时重取两侧、下载某一侧的原图。两侧怎么选（上一轮被驳回的版本、自选的两个版本）、驳回原因从哪来由页面在
// `open(build)` 里决定。
//
//   const cmp = useSnapshotCompare({ refetch: (fid) => …, onStale: (fid) => images.reportBroken(fid) })
//   void cmp.open(async () => ({ fileId, left, right }))   // 返回字符串是错误（显示在对比区），null 是不对比

import { computed, onScopeDispose, ref } from 'vue'

import { errorMessage } from '@/api/http'
import type { SnapshotItem } from '@/api/types'
import type { CompareMode, CompareSide, CompareSideKey } from '@/components/viewer/CompareViewer.vue'
import type { ViewerStatus } from '@/components/viewer/ImageViewer.vue'
import { staleInfo } from '@/components/viewer/viewer-status'
import { type OriginalUrl, urlFresh } from '@/lib/original'
import type { ViewerImageInfo } from '@/lib/viewer/load-original'

export interface ComparePair {
  fileId: string
  left: CompareSide
  right: CompareSide
}

/** 同一个对比里因为加载失败重取两侧，30 秒内最多一次（和 image-info store 的 reportBroken 一样）。 */
const SIDES_RETRY_MS = 30 * 1000

export function useSnapshotCompare(opts: {
  /** 重取这个文件的各个版本（壳因此重新记下来源）；失败返回 null。 */
  refetch: (fileId: string) => Promise<SnapshotItem[] | null>
  /** 某一侧的图片信息过期了（当前版本另外让 image-info store 重取 batch）。 */
  onStale?: (fileId: string) => void
}) {
  const pair = ref<ComparePair | null>(null)
  const mode = ref<CompareMode>('side')
  const showing = ref<CompareSideKey>('right')
  const loading = ref(false)
  const error = ref<string | null>(null)
  /** 正在对比（含正在取、出错）。 */
  const active = computed(() => loading.value || !!error.value || !!pair.value)
  let seq = 0
  let refreshedAt = 0
  let refreshing: Promise<SnapshotItem[] | null> | null = null

  async function open(build: () => Promise<ComparePair | string | null>) {
    const my = ++seq
    pair.value = null
    error.value = null
    loading.value = true
    try {
      const r = await build()
      if (my !== seq) return
      if (typeof r === 'string') error.value = r
      else if (r) {
        pair.value = r
        mode.value = 'side'
        showing.value = 'right'
        refreshedAt = 0
      }
    } catch (e) {
      if (my === seq) error.value = errorMessage(e)
    } finally {
      if (my === seq) loading.value = false
    }
  }

  function close() {
    seq++
    loading.value = false
    error.value = null
    pair.value = null
  }

  /**
   * 重取两侧的地址（版本不变，查看器不会重新加载，用户点"重试"时用新的地址）。同时只取一次；对比已经关掉或换了文件
   * 就不换。返回取到的各个版本。
   */
  function refreshSides(): Promise<SnapshotItem[] | null> {
    const p = pair.value
    if (!p) return Promise.resolve(null)
    const my = seq
    refreshing ??= opts
      .refetch(p.fileId)
      .then((snaps) => {
        const cur = pair.value
        if (!snaps || my !== seq || !cur || cur.fileId !== p.fileId) return snaps
        const pick = (side: CompareSide): CompareSide => {
          const s = snaps.find((x) => x.snapshot_id === side.info?.snapshot_id)
          return s ? { ...side, info: s as ViewerImageInfo } : side
        }
        pair.value = { ...cur, left: pick(cur.left), right: pick(cur.right) }
        return snaps
      })
      .catch(() => null)
      .finally(() => {
        refreshing = null
      })
    return refreshing
  }

  /** 某一侧的状态：图片信息过期时重取两侧（30 秒内最多一次）。 */
  function onSideStatus(s: ViewerStatus) {
    const p = pair.value
    if (!p || !staleInfo(s)) return
    opts.onStale?.(p.fileId)
    if (Date.now() - refreshedAt < SIDES_RETRY_MS) return
    refreshedAt = Date.now()
    void refreshSides()
  }

  /** 某一侧的原图地址（剩不到 10 分钟就先重取两侧）。 */
  async function sideOriginal(side: CompareSideKey): Promise<OriginalUrl> {
    const p = pair.value
    let info = p ? (side === 'left' ? p.left.info : p.right.info) : null
    if (!info) return { url: null }
    if (!urlFresh(info.url_expires_at)) {
      const sid = info.snapshot_id
      info = (await refreshSides())?.find((x) => x.snapshot_id === sid)
      if (!info) return { url: null, error: '取不到这个版本（重取各个版本失败）' }
    }
    return { url: info.original_url, error: info.url_error?.message }
  }

  onScopeDispose(() => {
    seq++
  })

  return { pair, mode, showing, loading, error, active, open, close, refreshSides, onSideStatus, sideOriginal }
}
