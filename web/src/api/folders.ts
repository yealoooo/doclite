// 文件目录的文件夹（M1-6）：读文件夹（面包屑）、列子项、新建（菜单码 scan 或 files）；
// 改名、改备注、移动、删除（菜单码 files，M1-12）。每个函数的最后一个参数可以传 `handle`（见 http.ts）。

import { request, type RequestOptions, requestQuiet } from './http'
import type { CreateFolderBody, FolderChildren, FolderItem, UpdateFolderBody } from './types'

type Handle = RequestOptions['handle']

/** 文件夹本身和祖先链（从根到父）。不存在或已删除 404，进不了项目 403。 */
export function getFolder(id: string, handle?: Handle) {
  return request<FolderItem>(`/api/folders/${id}`, { handle })
}

/** 未删除的直接子文件夹和文件，都按 sort_no、名称排序，不分页，不带图片地址。 */
export function listChildren(id: string, handle?: Handle) {
  return request<FolderChildren>(`/api/folders/${id}/children`, { handle })
}

/**
 * 新建子文件夹：201 新建；`if_exists: 'return'` 时遇到同名 200 返回已有的。
 * 错误：400 `invalid_input`（名字不合法，含 Windows 保留名）、404（父文件夹不在了）、409 `conflict`（同名）、`tree_full`。
 */
export function createFolder(body: CreateFolderBody, handle?: Handle) {
  return request<FolderItem>('/api/folders', { method: 'POST', body, handle })
}

/**
 * 改名、改备注、移动（parent_id）。没有变化时不写审计、不发事件。
 * 错误：409 `conflict`（同名）、`root_folder_locked`（根文件夹只能改备注）、`tree_cycle`（移进自己的子树）、
 * `tree_full`；404（目标不在了或在别的项目）；400 名字不合法。
 */
export function updateFolder(id: string, body: UpdateFolderBody, handle?: Handle) {
  return request<FolderItem>(`/api/folders/${id}`, { method: 'PATCH', body, handle })
}

/** 软删除整棵子树（204）。409 `file_cataloged`（子树里有已归类的文件）、`root_folder_locked`。 */
export function deleteFolder(id: string, handle?: Handle) {
  return request<void>(`/api/folders/${id}`, { method: 'DELETE', handle })
}

/**
 * 探测一个文件夹（M3-6，归类页的 `?pool=`）：同 getFolder，只是文件夹在进不了的项目里时（403 `forbidden`）不走全局
 * 处理（不刷新会话），由调用方当作"没有这个文件夹"处理。
 */
export function probeFolder(id: string, handle?: Handle) {
  return requestQuiet<FolderItem>(`/api/folders/${id}`, { handle })
}
