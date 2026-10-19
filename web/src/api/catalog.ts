// 归类（M3-1 的归类树和设置、M3-4 的读接口，design §2.2"接口细化（M3-4）"、§2.5、§7）：
// - 归类设置 `GET /api/projects/{pid}/catalog-settings`（能进项目即可）；
// - 归类树 `GET /api/projects/{pid}/archive-categories?parent_id=`（catalog 菜单，按需加载一层）；
// - 看板矩阵 `GET /api/projects/{pid}/catalog-summary?folder_id=`（dashboard 菜单）；
// - 文件挂在哪个条目 `GET /api/files/{fid}/catalog`、`POST /api/projects/{pid}/catalog-placements`（能进项目即可）；
// - M3-5：归类节点的新建、修改、删除（M3-1）和档案条目的读写、锁定解锁、批量锁定、按新模板重新生成档号（M3-2）。
// - M9-1：整个文件夹归档（items:from-folders）、解除封存、重新归档（先给对比）、撤销归档（DELETE）。
// 都不返回图片地址。每个函数的最后一个参数可以传 `handle`（见 http.ts）。
//
// 注意 403 `forbidden`（菜单不对或进不了项目）走全局处理（刷新会话、跳无权提示页），`handle` 拦不住：调用前先确认
// 有菜单、能进这个项目。

import { type Handle, request } from './http'
import type {
  ArchiveCategory,
  ArchiveCategoryList,
  ArchiveItem,
  ArchiveItemList,
  ArchiveResult,
  CatalogPlacements,
  CatalogSettings,
  CatalogSummary,
  FileCatalog,
  LockItemsResult,
  ModifyingList,
  RenumberResult,
  ResealPreview,
  UpdateArchiveItemBody,
  UpdatedArchiveCategory,
} from './types'

/** 归类要用的项目设置（补上了默认值）：档号模板、著录项、锁定和导出的开关、归类编码的规则。 */
export function getCatalogSettings(projectId: string, handle?: Handle) {
  return request<CatalogSettings>(`/api/projects/${projectId}/catalog-settings`, { handle })
}

/** 归类树的一层：`parentId` 的直接子节点（不传是根节点下的）。父节点不在这个项目里或已删除 404。 */
export function listArchiveCategories(projectId: string, parentId?: string | null, handle?: Handle) {
  return request<ArchiveCategoryList>(`/api/projects/${projectId}/archive-categories`, {
    query: { parent_id: parentId },
    handle,
  })
}

/**
 * 看板矩阵：一个文件夹（不传是根文件夹，即整个项目）整棵子树的"审核状态 × 归类状态"，直接放在里面的，以及每个
 * 子文件夹各自的子树。文件夹不在这个项目里或已删除 404。
 */
export function getCatalogSummary(projectId: string, folderId?: string | null, handle?: Handle) {
  return request<CatalogSummary>(`/api/projects/${projectId}/catalog-summary`, {
    query: { folder_id: folderId },
    handle,
  })
}

/** 一个文件归在哪个条目（没归档时 placement 为 null）。文件或所在文件夹已删除、不存在 404。 */
export function getFileCatalog(fileId: string, handle?: Handle) {
  return request<FileCatalog>(`/api/files/${fileId}/catalog`, { handle })
}

/** 一批文件（0–500 个）各挂在哪个条目；不存在、已删除、不在这个项目里的放进 `missing`。 */
export function getCatalogPlacements(projectId: string, fileIds: string[], handle?: Handle) {
  return request<CatalogPlacements>(`/api/projects/${projectId}/catalog-placements`, {
    method: 'POST',
    body: { file_ids: fileIds },
    handle,
  })
}

// ---------- 归类节点的写接口（M3-1，catalog 菜单；M3-5 用） ----------
// 错误：400 `invalid_input`（类型、编码、名称）；404（节点、父节点）；409 `conflict`（同一层编码重复）、`tree_full`、
// `tree_cycle`（移进自己的子树）、`item_locked`（子树里有已锁定的条目时移动、改编码、改类型）、`category_not_empty`
// （删除时子树里还有条目）、`invalid_state`（根节点；模板解析不了又要重算档号）。

export interface CategoryInput {
  category_type: string
  category_code: string
  category_name: string
}

/** 新建归类节点；`parent_id` 不传或 null 时建在根节点下。 */
export function createArchiveCategory(
  projectId: string,
  body: CategoryInput & { parent_id?: string | null },
  handle?: Handle,
) {
  return request<ArchiveCategory>(`/api/projects/${projectId}/archive-categories`, { method: 'POST', body, handle })
}

/**
 * 改名、改编码、改类型、移动（`parent_id`，移到顶层传根节点的 id）。不传的字段不改；和现在一样的当作没传。
 * 改编码、改类型、移动会重算子树里未锁定条目的档号（`renumbered`），撞号的留空（`archive_no_conflicts`）。
 */
export function updateArchiveCategory(
  projectId: string,
  id: string,
  body: Partial<CategoryInput> & { parent_id?: string | null },
  handle?: Handle,
) {
  return request<UpdatedArchiveCategory>(`/api/projects/${projectId}/archive-categories/${id}`, {
    method: 'PATCH',
    body,
    handle,
  })
}

/** 删除归类节点（软删除整棵子树）；子树里还有条目时 409 `category_not_empty`。 */
export function deleteArchiveCategory(projectId: string, id: string, handle?: Handle) {
  return request<void>(`/api/projects/${projectId}/archive-categories/${id}`, { method: 'DELETE', handle })
}

// ---------- 档案条目（M3-2；M9-1 起只能整个文件夹归档，catalog 菜单 + 能进项目，含解锁） ----------
// 检查顺序：404 → 状态（409 `item_locked` / `invalid_state`）→ row_version（409 `conflict`）→ 著录（400 `invalid_input`，
// `details.fields [{key, message}]`，题名的 key 是 `title`）→ 锁定的前置条件（409 `invalid_state`）。
// 去掉的接口（M9-1）：`POST archive-categories/{id}/items`（新建空条目）、`PUT archive-items/{id}/files`（按页挂接）、
// `mark-done`、`back-to-draft`。

/** 列表每页最多多少个（server 1–2000）。 */
export const MAX_ITEMS_PAGE = 2000
/** 批量锁定一次最多多少个。 */
export const MAX_LOCK_ITEMS = 500
/** 一次归档最多选多少个文件夹（展开子文件夹后也不能超过）。 */
export const MAX_ARCHIVE_FOLDERS = 500

/** 直接放在这个节点里的条目（不含下级节点），按件号排，keyset 分页。 */
export function listArchiveItems(
  categoryId: string,
  opts: { limit?: number; cursor?: string | null } = {},
  handle?: Handle,
) {
  return request<ArchiveItemList>(`/api/archive-categories/${categoryId}/items`, {
    query: { limit: opts.limit, cursor: opts.cursor },
    handle,
  })
}

export function getArchiveItem(id: string, handle?: Handle) {
  return request<ArchiveItem>(`/api/archive-items/${id}`, { handle })
}

/**
 * 整个文件夹归档（M9-1）：每个选中的文件夹自己成一件，有子文件夹的（盒子）它的直接子文件夹各成一件。`dry_run` 只给
 * 预览（不写库，200）；不带时同一事务建全部件（201），新件未锁定、已封存。不合格的跳过并给原因（`skipped`）。
 * 错误：400（0 个或超过 500 个、`titles` 不合规 details.fields）、404（details.ids）、409 `invalid_state`（根节点、
 * 模板不对、一件都建不出——details 是预览）、409 `file_cataloged`（{ids, items}）。
 */
export function archiveFolders(
  categoryId: string,
  body: { folder_ids: string[]; titles?: Record<string, string>; dry_run?: boolean },
  handle?: Handle,
) {
  return request<ArchiveResult>(`/api/archive-categories/${categoryId}/items:from-folders`, { method: 'POST', body, handle })
}

/** 改题名、著录（按 key 合并）、移到别的节点（`category_id`）、改件号（`item_no`）。 */
export function updateArchiveItem(id: string, body: UpdateArchiveItemBody, handle?: Handle) {
  return request<ArchiveItem>(`/api/archive-items/${id}`, { method: 'PATCH', body, handle })
}

/** 锁定、解锁。锁定：修改中 409 `invalid_state`，必填项没填 400 `invalid_input`（details.fields），其他前置条件 409。 */
export type ItemAction = 'lock' | 'unlock'

export function itemAction(id: string, action: ItemAction, rowVersion: number, handle?: Handle) {
  return request<ArchiveItem>(`/api/archive-items/${id}/${action}`, {
    method: 'POST',
    body: { row_version: rowVersion },
    handle,
  })
}

/** 解除封存（M9-1）：未锁定、已封存的 → 修改中；件号、档号、著录、页都不变。原因选填（≤ 200 字）。 */
export function unsealArchiveItem(id: string, rowVersion: number, reason?: string | null, handle?: Handle) {
  return request<ArchiveItem>(`/api/archive-items/${id}/unseal`, {
    method: 'POST',
    body: reason ? { row_version: rowVersion, reason } : { row_version: rowVersion },
    handle,
  })
}

/** 重新归档的对比（M9-1）：原页数 → 现页数，新增、去掉、调序；`problem` 是现在不能重新归档的原因。 */
export function getResealPreview(id: string, handle?: Handle) {
  return request<ResealPreview>(`/api/archive-items/${id}/reseal-preview`, { handle })
}

/**
 * 重新归档（M9-1）：修改中的 → 页换成来源文件夹现在的样子，再次封存。错误：409 `item_locked`、`invalid_state`（不在
 * 修改中，或者文件夹已经不能成为一件——message 写明原因）、`conflict`（row_version）、`file_cataloged`（{ids, items}）。
 */
export function resealArchiveItem(id: string, rowVersion: number, handle?: Handle) {
  return request<ArchiveItem>(`/api/archive-items/${id}/reseal`, {
    method: 'POST',
    body: { row_version: rowVersion },
    handle,
  })
}

/**
 * 批量锁定（1–500 个，一个事务）：已锁定的跳过；任何一个不满足锁定条件就整体不做，409 `invalid_state`
 * （`details.ids` 是不满足的全部条目，message 按原因计数）；不存在、已删除的 404（`details.ids`）。
 */
export function lockArchiveItems(projectId: string, itemIds: string[], handle?: Handle) {
  return request<LockItemsResult>(`/api/projects/${projectId}/archive-items:lock`, {
    method: 'POST',
    body: { item_ids: itemIds },
    handle,
  })
}

/** 项目里修改中的件（M9-2，归类页顶部"修改中 n 件"）：最多 500 件，按解除封存的时间倒序；`total` 是真实总数。 */
export function listModifyingItems(projectId: string, handle?: Handle) {
  return request<ModifyingList>(`/api/projects/${projectId}/archive-items:modifying`, { handle })
}

/**
 * 撤销归档（删除条目，204）：页一并移出（每页一条 unlink，带 `item_deleted: true`），文件夹回到未归档，件号不回收；
 * 已锁定的 409 `item_locked`。修改中的也可以。
 */
export function deleteArchiveItem(id: string, rowVersion: number, handle?: Handle) {
  return request<void>(`/api/archive-items/${id}`, { method: 'DELETE', query: { row_version: rowVersion }, handle })
}

/**
 * 取条目详情，但 403 `forbidden` 不走全局处理（M4-5）：导出页按地址栏里的条目 id 显示范围时用，id 可能指向进不了的
 * 项目（不在这个项目里），这时当作没有这个条目，不是"你的权限变了"。
 */
export function probeArchiveItem(id: string, handle?: Handle) {
  return request<ArchiveItem>(`/api/archive-items/${id}`, { handle, quiet403: true })
}

/** 按项目现在的模板重新生成 `categoryId` 子树（不传是整个项目）里未锁定条目的档号。 */
export function renumberArchiveItems(projectId: string, categoryId?: string | null, handle?: Handle) {
  return request<RenumberResult>(`/api/projects/${projectId}/archive-items:renumber`, {
    method: 'POST',
    body: categoryId ? { category_id: categoryId } : {},
    handle,
  })
}
