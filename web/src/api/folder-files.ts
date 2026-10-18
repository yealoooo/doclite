// 文件目录里的文件（folder_files 的一行，id 是 folder_file_id）：改名、移动、复制、删除、批量移动和删除（M1-8，
// 菜单码 files），以及上传（M1-7，菜单码 scan 或 files）。每个函数的最后一个参数可以传 `handle`（见 http.ts）。
// 只改位置和名字（硬性规则 3）：S3 对象、快照、审核、归类都不动。
// M9：封存的文件夹（requirements 5.5）里上传、复制进去、移入移出、删除、改名一律 409 `folder_archived`；重扫照常。

import { request, upload, type RequestOptions, type UploadOptions, type UploadParts } from './http'
import type { FolderFileCopied, FolderFilesMoved, FolderFileUpdated, IngestResponse } from './types'

type Handle = RequestOptions['handle']

/** 批量接口一次最多的 id 数（超出 server 返回 400）。 */
export const MAX_BULK_IDS = 500

/**
 * 改名（`file_name`）或移动到本项目的另一个文件夹（`folder_id`）。
 * 改名撞名 409 `conflict`；移动时目标里有同名的自动改名（`renamed`），排到目标末尾。
 */
export function updateFolderFile(id: string, body: { file_name?: string; folder_id?: string }, handle?: Handle) {
  return request<FolderFileUpdated>(`/api/folder-files/${id}`, { method: 'PATCH', body, handle })
}

/** 复制（新的 file_id，不复制字节）；不给 folder_id 时复制到原文件夹，名字自动改为 `名字 (1).jpg`。 */
export function copyFolderFile(id: string, folderId?: string, handle?: Handle) {
  return request<FolderFileCopied>(`/api/folder-files/${id}/copy`, {
    method: 'POST',
    body: folderId ? { folder_id: folderId } : {},
    handle,
  })
}

/** 软删除（204）。已归类的 409 `file_cataloged`。 */
export function deleteFolderFile(id: string, handle?: Handle) {
  return request<void>(`/api/folder-files/${id}`, { method: 'DELETE', handle })
}

/**
 * 批量移动（最多 500 个，同一项目，一个事务）。任何一个不合规则就整体不做，details 是 `{ids}`：
 * 不存在或已删除 404，不在同一项目 400。
 */
export function moveFolderFiles(ids: string[], folderId: string, handle?: Handle) {
  return request<FolderFilesMoved>('/api/folder-files:move', { method: 'POST', body: { ids, folder_id: folderId }, handle })
}

/** 批量删除（最多 500 个）。有已归类的整体不做：409 `file_cataloged`，details 是 `{ids}`。 */
export function deleteFolderFiles(ids: string[], handle?: Handle) {
  return request<void>('/api/folder-files:delete', { method: 'POST', body: { ids }, handle })
}

/**
 * 上传一个文件到文件夹（design §4.3）：201 新建；200 之前已上传过（同一文件夹里 upload_name 和 sha 都相同）。
 * 错误：400 `invalid_input`/`invalid_image`/`invalid_cover`、404、409 `duplicate_content`（details 是已有文件的位置）、
 * `storage_changed`、`storage_unavailable`、413、415。只在浏览器里用（壳走上传队列）。
 */
export function uploadToFolder(folderId: string, parts: UploadParts, opts?: UploadOptions) {
  return upload<IngestResponse>(`/api/folders/${folderId}/files`, parts, opts)
}
