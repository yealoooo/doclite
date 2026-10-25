// 浏览文件目录里的一个文件夹（/files、/review-assign、/catalog 的左栏共用）：当前文件夹（本身和祖先链，面包屑）、它的
// 子项（children，同时更新树上这一层）、加载状态；打开、重新加载列表、重取面包屑，带竞态保护（换了文件夹、组件卸载后
// 在途的结果作废）。各页面不同的地方用回调：取文件夹的接口、不在当前项目里怎么办、打开失败怎么办、当前文件夹被删掉了
// 去哪儿、换文件夹时清掉什么。
//
//   const browser = useFolderBrowser({ tree, rootId: () => …, fetchFolder: (id) => getFolder(id, […]), onGone: (parent) => … })
//   watch(folderId, (id) => void browser.open(id))
//   browser.applyChanges(folderIds, fileIds)   // 实时事件、操作之后：刷新受影响的树、列表、面包屑

import { computed, onScopeDispose, ref } from 'vue'

import { errorMessage, isApiError } from '@/api/http'
import type { FolderChildren, FolderItem } from '@/api/types'

import type { FolderTreeState } from './useFolderTree'

export type FolderListState = 'idle' | 'loading' | 'ready' | 'error'

export interface FolderBrowserOptions {
  tree: FolderTreeState
  /** 根文件夹的 id（树的根；面包屑取到新名字时更新它的名字）。 */
  rootId: () => string | null
  /** 取文件夹本身和祖先链；不存在等错误照常抛出。 */
  fetchFolder: (id: string) => Promise<FolderItem>
  /**
   * 取到了要打开的文件夹：返回 false 就不打开（例如不在当前项目里，调用方已经处理：切项目、回到根）。`current()` 为
   * false 时这次打开已经作废（调用方等了别的请求之后要先看它）。
   */
  accept?: (detail: FolderItem, current: () => boolean) => boolean | Promise<boolean>
  /** 打开成功（列表加载之前）。 */
  onOpened?: (detail: FolderItem) => void
  /** 打开失败：返回 true 表示调用方已经处理（例如回到根文件夹），否则显示错误。 */
  onOpenFailed?: (e: unknown, id: string) => boolean | Promise<boolean>
  /** 当前文件夹被删掉了（列表或面包屑 404）：`parentId` 是它的上级。 */
  onGone: (parentId: string | null) => void
  /** 换了文件夹（清掉选择等）。 */
  onSwitch?: () => void
  /** 列表加载好了（每次重新加载成功后）。 */
  onListLoaded?: (id: string, res: FolderChildren) => void
}

export function useFolderBrowser(opts: FolderBrowserOptions) {
  const { tree } = opts
  const folder = ref<FolderItem | null>(null)
  const children = ref<FolderChildren | null>(null)
  const state = ref<FolderListState>('idle')
  const error = ref<string | null>(null)
  /** 列表最近一次加载完成的时间（0 表示还没加载过）。 */
  let loadedAt = 0
  let openSeq = 0
  let listSeq = 0
  /** 还在（卸载后在途的回调不再导航）。 */
  let alive = true
  /** 打开文件夹的过程中（面包屑还没取到）有了改动：打开完成后补刷一次。 */
  let staleWhileOpening = false
  let goneHandled: string | null = null

  /** 面包屑：根 → … → 当前。 */
  const crumbs = computed(() =>
    folder.value
      ? [...folder.value.ancestors, { id: folder.value.id, folder_name: folder.value.folder_name, path_code: folder.value.path_code }]
      : [],
  )

  function renameRoot(detail: FolderItem) {
    const root = opts.rootId()
    if (root) tree.rename(root, detail.ancestors[0]?.folder_name ?? detail.folder_name)
  }

  /** 换项目等：清空（在途的结果作废）。 */
  function reset() {
    openSeq++
    listSeq++
    folder.value = null
    children.value = null
    state.value = 'idle'
    error.value = null
    loadedAt = 0
  }

  /** 进入一个文件夹：取它本身和祖先链（面包屑），展开树，加载列表。 */
  async function open(id: string) {
    const my = ++openSeq
    if (folder.value?.id !== id) {
      folder.value = null
      children.value = null
      opts.onSwitch?.()
    }
    state.value = 'loading'
    error.value = null
    staleWhileOpening = false
    try {
      const detail = await opts.fetchFolder(id)
      if (my !== openSeq) return
      if (opts.accept && !(await opts.accept(detail, () => my === openSeq))) return
      if (my !== openSeq) return
      folder.value = detail
      renameRoot(detail)
      void tree.revealPath(detail)
      opts.onOpened?.(detail)
      await reload()
      if (my === openSeq && staleWhileOpening) {
        staleWhileOpening = false
        void reload()
        void reloadCrumb()
      }
    } catch (e) {
      if (my !== openSeq) return
      if (opts.onOpenFailed && (await opts.onOpenFailed(e, id))) return
      if (my !== openSeq) return
      state.value = 'error'
      error.value = errorMessage(e)
    }
  }

  /** 重新加载当前文件夹的子项（同时更新树上这一层）。已经有列表时刷新失败保留旧的（错误已由请求封装提示）。 */
  async function reload() {
    const id = folder.value?.id
    if (!id) return
    const my = ++listSeq
    try {
      const res = await tree.load(id)
      if (my !== listSeq || folder.value?.id !== id) return
      if (!res) {
        gone(id)
        return
      }
      children.value = res
      state.value = 'ready'
      loadedAt = Date.now()
      opts.onListLoaded?.(id, res)
    } catch (e) {
      if (my !== listSeq) return
      if (children.value) state.value = 'ready'
      else {
        state.value = 'error'
        error.value = errorMessage(e)
      }
    }
  }

  /** 重新取面包屑（当前文件夹或祖先改名、移动）。 */
  async function reloadCrumb() {
    const id = folder.value?.id
    if (!id) return
    try {
      const detail = await opts.fetchFolder(id)
      if (folder.value?.id !== id) return
      folder.value = detail
      renameRoot(detail)
    } catch (e) {
      if (isApiError(e, 'not_found')) gone(id)
    }
  }

  function gone(id: string) {
    if (!alive || goneHandled === id || folder.value?.id !== id) return
    goneHandled = id
    opts.onGone(folder.value.parent_id ?? null)
  }

  /**
   * 这些文件夹（null：不知道范围，全部）、文件变了：刷新树上已加载的、当前文件夹的列表（当前文件夹、它的子文件夹、
   * 它里面的文件受影响时）、面包屑（路径上的文件夹变了时）。正在打开时等打开完成后补刷。返回列表是否受影响。
   */
  function applyChanges(folderIds: ReadonlySet<string> | null, fileIds?: ReadonlySet<string>): boolean {
    const cur = folder.value
    if (!cur) {
      if (state.value === 'loading') staleWhileOpening = true
      if (folderIds) void tree.refresh(folderIds, null)
      else void tree.refreshShown()
      return false
    }
    const affected =
      !folderIds ||
      folderIds.has(cur.id) ||
      !!children.value?.folders.some((f) => folderIds.has(f.id)) ||
      (!!fileIds?.size && !!children.value?.files.some((f) => fileIds.has(f.file_id)))
    // 当前文件夹由 reload 重新加载（它同时更新树上这一层），树上不重复请求。
    if (folderIds) void tree.refresh(folderIds, affected ? cur.id : null)
    else void tree.refreshShown(cur.id)
    if (affected) void reload()
    if (!folderIds || crumbs.value.some((c) => folderIds.has(c.id))) void reloadCrumb()
    return affected
  }

  onScopeDispose(() => {
    alive = false
    openSeq++
    listSeq++
  })

  return {
    folder,
    children,
    state,
    error,
    crumbs,
    /** 列表加载过了。 */
    loaded: () => loadedAt > 0,
    reset,
    open,
    reload,
    reloadCrumb,
    applyChanges,
  }
}
