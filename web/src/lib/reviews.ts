// 审核的显示文字（M2-5）：驳回原因、审核人不能再审的原因、一个文件的分配行的状态。
// 驳回原因：内置的 8 个存代码，前端翻成中文；项目自定义的直接存中文，原样显示（用户决定 Q4）。
// 驳回原因的中文只有一份，在 lib/reject-reasons.ts（M2-6 合并）；这里沿用原来的名字转出去。

import type { FileReviewRow, ReviewerProblem } from '@/api/types'

export { BUILTIN_REJECT_REASONS as REJECT_REASON_LABELS, rejectReasonLabel } from './reject-reasons'

const PROBLEM_LABELS: Record<ReviewerProblem, string> = {
  disabled: '已停用',
  no_review_menu: '没有审核菜单',
  no_project_access: '已不能进入这个项目',
}

/** 审核人不能再审这个项目的原因（review-assignments 的 `problems`）。 */
export function reviewerProblemLabel(problem: string): string {
  return PROBLEM_LABELS[problem as ReviewerProblem] ?? problem
}

/** 几个原因连成一句：`已停用、已不能进入这个项目`。 */
export function reviewerProblemsText(problems: readonly string[]): string {
  return problems.map(reviewerProblemLabel).join('、')
}

/** 一个文件的分配行（files/{fid}/reviews）的状态文字。 */
export function reviewRowLabel(row: Pick<FileReviewRow, 'status' | 'result'>): string {
  if (row.status === 'open') return '待审'
  if (row.status === 'cancelled') return '已取消'
  if (row.result === 'approved') return '通过'
  if (row.result === 'rejected') return '驳回'
  return row.status
}
