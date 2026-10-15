// 文件夹树的状态（M1-10）：按需加载子文件夹，给 `FolderTree` 组件用。文件目录页的左侧树和 `FolderPicker`
// 各建一个实例（展开状态互不影响）。加载、去重、竞态处理在 `useLazyTree`（M3-5 抽出来和归类树共用），
// 这里只注入文件夹的数据源。
//
// - 只在展开时加载这个文件夹的子项（`GET /api/folders/{id}/children`，只取其中的子文件夹）；
// - `revealPath(folder)` 展开并加载祖先链（定位到深处的文件夹时）；
// - `refresh(ids)` 只重新加载已经加载过的那些（SSE 的 `folders.changed`）；`refreshAll()` 整体刷新；
// - 根文件夹的名称取自项目名（`reset(root)` 时传入），面包屑取到新名字时用 `rename()` 更新。
//
//   const tree = useFolderTree()
//   tree.reset({ id: project.root_folder_id, folder_name: project.project_name })
//   <FolderTree :tree="tree" :selected-id="folderId" @select="go" />

import { listChildren } from '@/api/folders'
import { isApiError } from '@/api/http'
import type { FolderChildren, FolderItem, SubfolderEntry } from '@/api/types'

import { useLazyTree } from './useLazyTree'

/** 根文件夹的路径编码（M1-5：建项目时固定为 `00001.`）。 */
export const ROOT_PATH_CODE = '00001.'

/** 树上的一个文件夹。 */
export interface TreeFolder {
  id: string
  folder_name: string
  remark: string | null
  path_code: string
  /** 子文件夹数；-1 表示还不知道（根文件夹在第一次加载前）。 */
  folder_count: number
  /** 文件数；-1 表示还不知道。 */
  file_count: number
}

export function useFolderTree() {
  const core = useLazyTree<TreeFolder, FolderChildren>({
    fetch: (id) => listChildren(id, ['not_found']),
    children: (res) => res.folders.map((f) => ({ ...f })),
    updateSelf: (self, res) =>
      self ? { ...self, folder_count: res.folders.length, file_count: res.files.length } : undefined,
    isLeaf: (node) => node.folder_count === 0,
    isGone: (e) => isApiError(e, 'not_found'),
    label: (node) => node.folder_name,
  })

  function reset(root: { id: string; folder_name: string }) {
    core.reset({
      id: root.id,
      folder_name: root.folder_name,
      remark: null,
      path_code: ROOT_PATH_CODE,
      folder_count: -1,
      file_count: -1,
    })
  }

  /** 用 children 的结果更新这个文件夹的子文件夹（文件目录页自己取了 children 时也调它，省一次请求）。 */
  function apply(id: string, folders: SubfolderEntry[], fileCount?: number) {
    core.apply(
      id,
      folders.map((f) => ({ ...f })),
      (self) => (self ? { ...self, folder_count: folders.length, file_count: fileCount ?? self.file_count } : undefined),
    )
  }

  /** 展开并加载祖先链，让 `folder` 出现在树上。 */
  async function revealPath(folder: Pick<FolderItem, 'id' | 'ancestors'>) {
    await core.revealChain(folder.ancestors.map((a) => a.id))
  }

  function rename(id: string, folderName: string) {
    core.patch(id, (n) => (n.folder_name === folderName ? n : { ...n, folder_name: folderName }))
  }

  return {
    nodes: core.nodes,
    childIds: core.childIds,
    loading: core.loading,
    expanded: core.expanded,
    rootId: core.rootId,
    reset,
    apply,
    /**
     * 加载（或重新加载）一个文件夹的子项，返回完整的 children（含文件）。
     * 文件夹已经不存在（404）时从树上去掉它，返回 null；其他错误照常抛出。
     */
    load: core.load,
    ensureLoaded: core.ensureLoaded,
    revealPath,
    refresh: core.refresh,
    refreshAll: core.refreshAll,
    rename,
    childrenOf: core.childrenOf,
    pathOf: core.pathOf,
  }
}

export type FolderTreeState = ReturnType<typeof useFolderTree>
