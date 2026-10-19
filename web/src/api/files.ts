// 按 file_id 访问的文件接口（M1-8，design §4.4）：看图（batch、snapshots）和文件日志。
// 图片地址一律经 `lib/image-url.ts` 的 `imageSrc()` 使用，不直接放进 <img>。

import { type Handle, request } from './http'
import type { FileBatchResponse, FileLogs, FileSnapshots } from './types'

/** batch 一次最多的 file_id 数（超出 server 返回 400）。 */
export const MAX_BATCH_IDS = 500

/**
 * 文件当前版本的信息和预签名 URL（登录即可）。最多 500 个；不存在、无权访问、已删除的放进 `missing`。
 * 一般不直接调，用 `stores/image-info.ts`（去抖、合批、缓存、快到期时重取）。
 */
export function batchFiles(fileIds: string[], handle?: Handle) {
  return request<FileBatchResponse>('/api/files/batch', {
    method: 'POST',
    body: { file_ids: fileIds },
    handle,
  })
}

/** 文件的全部版本（按版本号从小到大），各带图片地址。文件已删除或无权访问 404。 */
export function getFileSnapshots(fileId: string, handle?: Handle) {
  return request<FileSnapshots>(`/api/files/${fileId}/snapshots`, { handle })
}

/** 文件日志（菜单码 files），新的在前。文件已删除 404，进不了项目 403。 */
export function getFileLogs(fileId: string, handle?: Handle) {
  return request<FileLogs>(`/api/files/${fileId}/logs`, { handle })
}
