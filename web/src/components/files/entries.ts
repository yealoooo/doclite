// 文件目录主区的条目：当前文件夹的子文件夹在前、文件在后（顺序同 children 接口：sort_no、名称）。
// 选择用字符串键：文件夹 `d:<folder_id>`，文件 `f:<file_id>`。

import { useLocalStorage } from '@vueuse/core'

import type { FileEntry, SubfolderEntry } from '@/api/types'

export type Entry =
  | { kind: 'folder'; key: string; folder: SubfolderEntry }
  | { kind: 'file'; key: string; file: FileEntry }

export const folderKey = (id: string) => `d:${id}`
export const fileKey = (fileId: string) => `f:${fileId}`

export function toEntries(folders: SubfolderEntry[], files: FileEntry[]): Entry[] {
  return [
    ...folders.map((folder): Entry => ({ kind: 'folder', key: folderKey(folder.id), folder })),
    ...files.map((file): Entry => ({ kind: 'file', key: fileKey(file.file_id), file })),
  ]
}

export type ViewMode = 'icons' | 'list'

/** 图标还是列表（记在 localStorage 的 `key` 下，只是个人习惯，读写失败就用图标）。 */
export function useViewMode(key: string) {
  return useLocalStorage<ViewMode>(key, 'icons', {
    serializer: { read: (v) => (v === 'list' ? 'list' : 'icons'), write: (v) => v },
  })
}

/** 文件的排列（M11-4）：扫描顺序（sort_no，纸张顺序，归档按它排页）、文件名、入库时间（放进文件夹的时间）。 */
export type SortMode = 'scan' | 'name' | 'created'

/** 排列方式（记在 localStorage 的 `key` 下，只是个人习惯，读写失败就用扫描顺序）。 */
export function useSortMode(key: string) {
  return useLocalStorage<SortMode>(key, 'scan', {
    serializer: { read: (v) => (v === 'name' || v === 'created' ? v : 'scan'), write: (v) => v },
  })
}

const nameCollator = new Intl.Collator('zh-Hans-CN', { numeric: true, sensitivity: 'base' })

/** 按排列方式排文件（scan 原样返回：children 接口已按 sort_no、名称排好）。 */
export function sortFiles(files: FileEntry[], mode: SortMode): FileEntry[] {
  if (mode === 'scan') return files
  const out = files.slice()
  if (mode === 'name') out.sort((a, b) => nameCollator.compare(a.file_name, b.file_name) || a.sort_no - b.sort_no)
  else out.sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at) || a.sort_no - b.sort_no)
  return out
}
