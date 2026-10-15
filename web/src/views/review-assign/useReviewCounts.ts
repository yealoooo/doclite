// 审核分配页的按文件夹状态计数（M2-5）：`GET /api/projects/{pid}/review-summary?folder_id=`。
//
// - 目录树每加载一层（展开、定位）对这个文件夹调一次 review-summary：它自己的子树计数（`counts`）、直接放在
//   里面的（`direct_counts`），以及每个子文件夹的子树计数（`children[].counts`）。所以树上显示的每个文件夹
//   （根，或已加载的文件夹的子文件夹）都有计数。不传 folder_id 是根文件夹（页头的"待分配 n、待审核 n……"）。
// - 实时刷新、按路径刷新祖先、文件夹移动后丢掉过时的 parent 记录、`refreshSubtree`、只采用最新一次请求的结果：
//   都在通用的 `composables/useFolderSummaries.ts` 里（M3-7 从这里抽出去，看板的 catalog-summary 也用它），
//   规则没变。
//
//   const counts = useReviewCounts(() => project.currentId, { canLookup: () => canBrowse.value })
//   counts.reset(rootId)
//   await counts.load(folderId)
//   counts.get(folderId)?.unassigned
//   await counts.refreshForFolders(event.folder_ids)

import { getReviewSummary } from '@/api/reviews'
import type { ReviewStatusCounts, ReviewSummary } from '@/api/types'
import { type FolderSummariesOptions, useFolderSummaries } from '@/composables/useFolderSummaries'

export type ReviewCountsOptions = FolderSummariesOptions

export function useReviewCounts(projectId: () => string | null, opts: ReviewCountsOptions = {}) {
  const s = useFolderSummaries<ReviewStatusCounts, ReviewSummary>(
    projectId,
    (pid, folderId, handle) => getReviewSummary(pid, folderId, handle),
    opts,
  )
  return {
    rootId: s.rootId,
    reset: s.reset,
    load: s.load,
    ensure: s.ensure,
    refreshAll: s.refreshAll,
    refreshForFolders: s.refreshForFolders,
    refreshSubtree: s.refreshSubtree,
    forgetUnloadedParents: s.forgetUnloadedParents,
    /** 子树计数（响应式）。 */
    get: s.get,
    /** 直接放在里面的计数（响应式）。 */
    directOf: s.directOf,
  }
}

export type ReviewCounts = ReturnType<typeof useReviewCounts>
