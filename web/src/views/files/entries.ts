// 文件目录主区的条目：当前文件夹的子文件夹在前、文件在后（顺序同 children 接口：sort_no、名称）。
// 选择用字符串键：文件夹 `d:<folder_id>`，文件 `f:<file_id>`。

import type { FileEntry, SubfolderEntry } from '@/api/types'

export type Entry =
  | { kind: 'folder'; key: string; folder: SubfolderEntry }
  | { kind: 'file'; key: string; file: FileEntry }

export type FileItemEntry = Extract<Entry, { kind: 'file' }>
export type FolderItemEntry = Extract<Entry, { kind: 'folder' }>

export const folderKey = (id: string) => `d:${id}`
export const fileKey = (fileId: string) => `f:${fileId}`

export function toEntries(folders: SubfolderEntry[], files: FileEntry[]): Entry[] {
  return [
    ...folders.map((folder): Entry => ({ kind: 'folder', key: folderKey(folder.id), folder })),
    ...files.map((file): Entry => ({ kind: 'file', key: fileKey(file.file_id), file })),
  ]
}

export type ViewMode = 'icons' | 'list'
