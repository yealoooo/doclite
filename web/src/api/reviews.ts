// 审核分配（M2-1 的写接口、M2-2 的读接口，design §2.2、§7）：分配、改派、撤回通过；可以分给的人、按文件夹的
// 状态计数、open 分配、一个文件的全部分配。除撤回通过（review 或 review_assign）、一个文件的分配（review 或
// review_assign）外，菜单码都是 review_assign。都不返回图片地址，图片一律经 batch（image-info store）取。
// 每个函数的最后一个参数可以传 `handle`（见 http.ts）。
//
// 错误码（design §7）：400 `invalid_input`（审核人不合格且 message 说明是哪一条、超上限、参数不对）；
// 404（`details.ids` 列出不存在、已删除、不在这个项目里的文件）；409 `invalid_state`（那一行已经不是 open、
// 文件不是待审核 / 已通过）；403 `forbidden`（菜单不对或进不了项目，走全局处理）。

import { request, type RequestOptions } from './http'
import type {
  AssignResult,
  FileReviews,
  OpenAssignments,
  ProjectReviewers,
  ReassignOneResult,
  ReassignResult,
  ReopenResult,
  ReviewSummary,
} from './types'

type Handle = RequestOptions['handle']

/** 按选中的文件分配、批量改派一次最多的 file_id 数（超过 server 返回 400）。 */
export const MAX_REVIEW_FILE_IDS = 500
/** 按文件夹分配、按子树改派一次最多的文件数（只算待分配的 / 要改派的；超过 server 返回 400）。 */
export const MAX_REVIEW_FOLDER_FILES = 10000

/** 分配：`folder_id`（子树里所有未删除的待分配文件）和 `file_ids`（1–500 个，跳过不是待分配的）二选一。 */
export function assignReviews(
  projectId: string,
  body: { reviewer_id: string; folder_id: string } | { reviewer_id: string; file_ids: string[] },
  handle?: Handle,
) {
  return request<AssignResult>(`/api/projects/${projectId}/review-assignments`, { method: 'POST', body, handle })
}

/** 单个改派（`assignmentId` 是 open 的那一行 file_reviews 的 id）。原来就分给这个人时 `changed: false`。 */
export function reassignOne(assignmentId: string, reviewerId: string, handle?: Handle) {
  return request<ReassignOneResult>(`/api/review-assignments/${assignmentId}/reassign`, {
    method: 'POST',
    body: { reviewer_id: reviewerId },
    handle,
  })
}

/**
 * 批量改派：`file_ids`（最多 500 个），或"某人在某文件夹子树下的全部待办"（`from_reviewer_id` + `folder_id`，
 * 最多 1 万个）。只改派待审核的 open 分配，其余（不是待审核、没有 open 分配、已经分给目标的）跳过并计数。
 */
export function reassignMany(
  projectId: string,
  body:
    | { to_reviewer_id: string; file_ids: string[] }
    | { to_reviewer_id: string; from_reviewer_id: string; folder_id: string },
  handle?: Handle,
) {
  return request<ReassignResult>(`/api/projects/${projectId}/review-assignments:reassign`, {
    method: 'POST',
    body,
    handle,
  })
}

/**
 * 撤回通过（Q8）：approved → pending，重新分给通过它的审核人；他已不能再审这个项目时改为待分配
 * （`reviewer_id` 为 null）。不是已通过 409 `invalid_state`。
 */
export function reopenFile(fileId: string, handle?: Handle) {
  return request<ReopenResult>(`/api/files/${fileId}/reopen`, { method: 'POST', handle })
}

/** 可以分给的人和每人在这个项目里的待办数。 */
export function listReviewers(projectId: string, handle?: Handle) {
  return request<ProjectReviewers>(`/api/projects/${projectId}/reviewers`, { handle })
}

/** 一个文件夹（不传是根文件夹）和它每个子文件夹的子树状态计数。文件夹不在这个项目里或已删除 404。 */
export function getReviewSummary(projectId: string, folderId?: string | null, handle?: Handle) {
  return request<ReviewSummary>(`/api/projects/${projectId}/review-summary`, {
    query: { folder_id: folderId },
    handle,
  })
}

export interface OpenAssignmentsQuery {
  /** 子树；不传是整个项目。 */
  folder_id?: string | null
  reviewer_id?: string | null
  /** 默认 500，1–2000。 */
  limit?: number
  /** 上一页的 `next_cursor`。 */
  cursor?: string | null
}

/** open 分配（改派用），keyset 分页；`reviewers` 汇总范围里的审核人和他们能不能再审。 */
export function listOpenAssignments(projectId: string, q: OpenAssignmentsQuery, handle?: Handle) {
  return request<OpenAssignments>(`/api/projects/${projectId}/review-assignments`, { query: { ...q }, handle })
}

/** 一个文件的全部分配和结果（含已取消的，按分配的先后）。 */
export function getFileReviews(fileId: string, handle?: Handle) {
  return request<FileReviews>(`/api/files/${fileId}/reviews`, { handle })
}

// ---------- 审核工作台（M2-6）：我的审核、提交审核、批量通过、审核设置 ----------
// 菜单码：我的审核、提交审核、批量通过是 review；审核设置能进项目即可。错误码（design §2.2、§7）：
// 403 `not_assignee`（不是分给自己的 open 分配）；409 `invalid_state`（文件不是待审核）；409 `conflict`（带的
// snapshot_id 不是当前版本）；400 `invalid_input`（原因、说明不对）；批量通过任何一个不合规则整体不做
// （404 / 409 / 403，`details.ids` 列出这一类）。
// （import 写在这一段里：这个文件多个任务并行追加，不改上面已有的内容。）

import type { ApproveManyResult, MyReviews, MyReviewState, ReviewBody, ReviewedFile, ReviewSettings } from './types'

/** 批量通过一次最多的文件数（超过 server 返回 400）。 */
export const MAX_APPROVE_FILE_IDS = 500

export interface MyReviewsQuery {
  project_id: string
  /** 子树；不传是整个项目。 */
  folder_id?: string | null
  state?: MyReviewState
  /** 只看 DPI 不足的。 */
  low_dpi?: boolean
  /** 默认 500，1–2000。 */
  limit?: number
  /** 上一页的 `next_cursor`。 */
  cursor?: string | null
}

/** 我的审核（审核员的待办），keyset 分页；计数、文件夹、其他项目的待审数每页都有。 */
export function listMyReviews(q: MyReviewsQuery, handle?: Handle) {
  return request<MyReviews>('/api/me/reviews', {
    query: {
      project_id: q.project_id,
      folder_id: q.folder_id,
      state: q.state && q.state !== 'all' ? q.state : null,
      low_dpi: q.low_dpi ? 'true' : null,
      limit: q.limit,
      cursor: q.cursor,
    },
    handle,
  })
}

/** 审核一个文件（通过或驳回），带上看到的 `snapshot_id`。 */
export function submitReview(fileId: string, body: ReviewBody, handle?: Handle) {
  return request<ReviewedFile>(`/api/files/${fileId}/review`, { method: 'POST', body, handle })
}

/** 批量通过（Q9）：1–500 个，都要是分给自己的 open 分配，一个事务，任何一个不合规则整体不做。 */
export function approveMany(projectId: string, fileIds: string[], handle?: Handle) {
  return request<ApproveManyResult>(`/api/projects/${projectId}/reviews:approve`, {
    method: 'POST',
    body: { file_ids: fileIds },
    handle,
  })
}

/** 项目的审核设置：驳回原因列表和几个上限。 */
export function getReviewSettings(projectId: string, handle?: Handle) {
  return request<ReviewSettings>(`/api/projects/${projectId}/review-settings`, { handle })
}
