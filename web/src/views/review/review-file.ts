// 审核工作台里一个文件的审核记录（`GET /api/files/{fid}/reviews`，M2-2）的几个取法（M2-6）。
// 行按 id 升序（分配的先后）；"最近一次"都按 id 取（M2-1），不按时间。

import type { FileReviewRow, SnapshotItem } from '@/api/types'

/**
 * "上一轮"（M2-4 须知、用户决定 Q2）：最近一条 `result=rejected`、没有取消、`snapshot_id` 不是当前快照的行。
 * 没有时返回 null（第 1 轮，或者上一轮是撤回通过之类不是驳回的）。
 */
export function previousRejection(rows: readonly FileReviewRow[], currentSnapshotId: string | null): FileReviewRow | null {
  for (let i = rows.length - 1; i >= 0; i--) {
    const r = rows[i]!
    if (r.result === 'rejected' && r.status !== 'cancelled' && r.snapshot_id && r.snapshot_id !== currentSnapshotId) return r
  }
  return null
}

/** 审这个快照的最近一条有结果的行（快照时间线上写"通过：某人""驳回：原因"）。 */
export function resultForSnapshot(rows: readonly FileReviewRow[], snapshotId: string): FileReviewRow | null {
  for (let i = rows.length - 1; i >= 0; i--) {
    const r = rows[i]!
    if (r.snapshot_id === snapshotId && r.status === 'done' && r.result) return r
  }
  return null
}

/** 驳回这个快照的最近一条行（对比视图的角标写原因）。 */
export function rejectionForSnapshot(rows: readonly FileReviewRow[], snapshotId: string): FileReviewRow | null {
  for (let i = rows.length - 1; i >= 0; i--) {
    const r = rows[i]!
    if (r.snapshot_id === snapshotId && r.status === 'done' && r.result === 'rejected') return r
  }
  return null
}

/**
 * 默认对比哪两个版本：左边是上一轮被驳回的版本（没有时是前一个版本），右边是当前版本。只有一个版本时返回 null。
 */
export function defaultComparePair(
  snapshots: readonly SnapshotItem[],
  prev: FileReviewRow | null,
): { left: SnapshotItem; right: SnapshotItem; leftLabel: string } | null {
  if (snapshots.length < 2) return null
  const right = snapshots[snapshots.length - 1]!
  const fromReject = prev ? snapshots.find((s) => s.snapshot_id === prev.snapshot_id) : undefined
  if (fromReject && fromReject.snapshot_id !== right.snapshot_id) return { left: fromReject, right, leftLabel: '上一轮' }
  return { left: snapshots[snapshots.length - 2]!, right, leftLabel: '上一版' }
}
