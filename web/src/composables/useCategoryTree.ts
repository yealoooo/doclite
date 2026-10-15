// 归类树的状态（M3-5）：按需加载一层（`GET /api/projects/{pid}/archive-categories?parent_id=`，catalog 菜单），
// 给 `CategoryTree` 组件用。加载、去重、竞态处理和文件夹树共用 `useLazyTree`，这里只注入归类节点的数据源。
//
// - 根节点（`parent_id` 为空，编码是项目编码）界面上不显示（design §7），树从它的子节点开始；移到顶层用根节点的 id；
// - 每次加载一层都用响应里的 `parent` 更新节点自己（子节点数、子树里的条目数都是新的）；
// - 没有"按 id 取祖先链"的接口：`reveal(id)` 沿 `parent_id` 一层层往上取（每层一次请求，同时也加载了那一层）；
// - `refreshFor(categoryIds)`（SSE `catalog.changed`）：节点子树里的条目数会沿祖先链一路变，所以重新加载这些节点
//   自己（结构可能变了）和它们已加载的祖先；树上还不知道的节点（例如别人在收起的分支里建了条目）无法确定位置，
//   整体刷新已加载的部分。
//
//   const tree = useCategoryTree()
//   await tree.init(projectId)          // 取根节点和第一层
//   <CategoryTree :tree="tree" :selected-id="categoryId" @select="go" />

import { computed } from 'vue'

import { listArchiveCategories } from '@/api/catalog'
import { isApiError } from '@/api/http'
import type { ArchiveCategory, ArchiveCategoryList } from '@/api/types'

import { useLazyTree } from './useLazyTree'

export function useCategoryTree() {
  let projectId: string | null = null
  let initSeq = 0

  const core = useLazyTree<ArchiveCategory, ArchiveCategoryList>({
    fetch: (id) => {
      if (!projectId) return Promise.reject(new Error('归类树还没有初始化'))
      return listArchiveCategories(projectId, id, ['not_found'])
    },
    children: (res) => res.items,
    updateSelf: (_self, res) => res.parent,
    isLeaf: (node) => node.child_count === 0,
    isGone: (e) => isApiError(e, 'not_found'),
    label: (node) => node.category_name,
  })

  const root = computed(() => (core.rootId.value ? (core.nodes.get(core.rootId.value) ?? null) : null))
  /** 根节点下的第一层（树的顶层）。 */
  const topLevel = computed(() => (root.value ? (core.childrenOf(root.value) ?? []) : []))
  /**
   * 直接放在根节点下的条目数（server 允许条目放在根节点下：PATCH 的 category_id 可以是根节点）：根节点整棵子树的
   * 条目数减去顶层各节点子树的条目数。根节点这一层还没加载时为 0。
   */
  const rootDirectCount = computed(() => {
    const r = root.value
    if (!r || !core.childIds.has(r.id)) return 0
    return Math.max(0, r.item_count - topLevel.value.reduce((n, c) => n + c.item_count, 0))
  })

  /** 换项目或第一次进来：取根节点和第一层。返回根节点；被更新的一次 init 取代时返回 null。 */
  async function init(pid: string): Promise<ArchiveCategory | null> {
    const my = ++initSeq
    projectId = pid
    const res = await listArchiveCategories(pid, null)
    if (my !== initSeq) return null
    core.reset(res.parent)
    core.apply(res.parent.id, res.items, () => res.parent)
    return res.parent
  }

  /** 清空（没有项目时）。 */
  function clear() {
    initSeq++
    projectId = null
    core.rootId.value = null
    core.nodes.clear()
    core.childIds.clear()
  }

  /**
   * 展开到一个节点：沿 parent_id 往上取到根，展开整条链。返回节点本身；节点不存在、已删除、不在这个项目里时
   * 返回 null。
   */
  async function reveal(id: string): Promise<ArchiveCategory | null> {
    const rootId = core.rootId.value
    if (!rootId) return null
    if (id === rootId) return core.nodes.get(id) ?? null
    let node = core.nodes.get(id)
    if (!node) {
      // 还不在树上：取它这一层（响应的 parent 就是它自己，会放进树里）。
      const res = await core.load(id)
      if (!res) return null
      node = res.parent
    }
    const chain: string[] = []
    let pid = node.parent_id
    while (pid && pid !== rootId) {
      let p = core.nodes.get(pid)
      if (!p) {
        const res = await core.load(pid)
        if (!res) return null
        p = res.parent
      }
      chain.unshift(pid)
      pid = p.parent_id
    }
    if (core.rootId.value !== rootId) return null
    await core.revealChain([rootId, ...chain])
    return core.nodes.get(id) ?? node
  }

  /** 节点的祖先（从上到下，不含根和它自己）；树上不知道的部分停下。 */
  function ancestorsOf(id: string): string[] {
    const out: string[] = []
    const rootId = core.rootId.value
    let cur = core.nodes.get(id)?.parent_id ?? null
    while (cur && cur !== rootId) {
      out.unshift(cur)
      cur = core.nodes.get(cur)?.parent_id ?? null
    }
    return out
  }

  /**
   * 事件涉及的节点：重新加载它们自己和祖先（子树里的条目数沿祖先链变）。只为看得到的展开节点发请求，收起的丢掉
   * 子节点、再展开时重新取（refreshShown，M3-5 审查：事件放大）。有不认识的节点时位置不明，刷新全部看得到的部分。
   */
  async function refreshFor(categoryIds: Iterable<string>) {
    const targets = new Set<string>()
    const rootId = core.rootId.value
    for (const id of categoryIds) {
      const node = core.nodes.get(id)
      if (!node) return core.refreshShown()
      targets.add(id)
      // 它自己的计数在父节点那一层里；一路往上。
      let cur: string | null = node.parent_id
      while (cur) {
        targets.add(cur)
        if (cur === rootId) break
        cur = core.nodes.get(cur)?.parent_id ?? null
      }
    }
    await core.refreshShown(targets)
  }

  /**
   * 节点移动之后（PATCH 成功）：先用响应更新树上的这个节点（新的 parent_id、path_code），重新加载它子树里已加载的层
   * （下级的 path_code 都变了），再刷新原来和现在的上级（M3-5 审查：移动没加载过的节点后树上找不到它）。
   */
  async function afterMove(updated: ArchiveCategory, oldPathCode: string, oldParentId: string | null) {
    core.patch(updated.id, () => ({ ...updated }))
    const inside = [...core.childIds.keys()].filter((id) => core.nodes.get(id)?.path_code.startsWith(oldPathCode))
    await core.refresh(inside)
    await refreshFor([oldParentId ?? core.rootId.value!, updated.parent_id ?? core.rootId.value!].filter(Boolean))
  }

  /** `id` 在不在 `ancestorId` 的子树里（含它自己）；两个都要在树上，不知道时返回 null。 */
  function isUnder(id: string, ancestorId: string): boolean | null {
    const a = core.nodes.get(ancestorId)
    const n = core.nodes.get(id)
    if (!a || !n) return null
    return n.path_code.startsWith(a.path_code)
  }

  /** 从顶层到这个节点的名称，用 ` / ` 连接（不含根）。 */
  function pathText(id: string): string {
    return core.pathOf(id).slice(1).split('/').join(' / ')
  }

  return {
    ...core,
    root,
    topLevel,
    rootDirectCount,
    init,
    afterMove,
    clear,
    reveal,
    ancestorsOf,
    refreshFor,
    isUnder,
    pathText,
  }
}

export type CategoryTreeState = ReturnType<typeof useCategoryTree>
