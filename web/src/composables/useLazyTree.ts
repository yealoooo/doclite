// 按需加载的树（M3-5 从 M1-10 的 useFolderTree 抽出来的通用部分）：数据源由调用方注入，文件目录树
// （useFolderTree）和归类树（useCategoryTree）共用同一套加载、去重、竞态处理。
//
// - 只在展开时加载一个节点的直接子节点（`source.fetch(id)`）；同一个节点第一次加载不重复发请求；
// - 每个节点只采用最新一次请求的结果；`reset` 之后丢弃之前发出的请求的结果；
// - 第一次加载还在路上时收到了这个节点的事件（`refresh`）：加载完成后再补刷一次（路上那份可能是事件之前的）；
// - 节点已不存在（`source.isGone(err)`，例如 404）时从树上去掉它；
// - `refresh(ids)` 只重新加载已经加载过的那些（SSE 事件）；`refreshAll()` 重新加载全部已加载的。
//
//   const tree = useLazyTree<Node, Result>({ fetch, children, updateSelf, isLeaf, isGone, label })
//   tree.reset(rootNode)
//   <reka-ui TreeRoot :get-children="tree.childrenOf" :expanded="tree.expanded.value" …>

import { ref, shallowReactive, watch } from 'vue'

export interface LazyTreeSource<N extends { id: string }, R> {
  /** 取一个节点的直接子节点（原始响应）。 */
  fetch: (id: string) => Promise<R>
  /** 响应里的子节点，按显示顺序。 */
  children: (res: R) => N[]
  /**
   * 用响应更新节点自己（例如子节点数、计数）；`self` 是树上现有的（还不在树上时为 undefined）。
   * 返回要放进树里的节点；返回 undefined 表示不放（例如响应里没有节点自己的信息）。
   */
  updateSelf?: (self: N | undefined, res: R) => N | undefined
  /** 已知没有子节点（不显示展开箭头）；还不知道时返回 false。 */
  isLeaf: (node: N) => boolean
  /** 这个错误表示节点已不存在（从树上去掉它，load 返回 null）。 */
  isGone: (err: unknown) => boolean
  /** `pathOf` 用的名字。 */
  label: (node: N) => string
}

export function useLazyTree<N extends { id: string }, R>(source: LazyTreeSource<N, R>) {
  const nodes = shallowReactive(new Map<string, N>())
  /** 已加载的节点 → 子节点 id（按 source 的顺序）。 */
  const childIds = shallowReactive(new Map<string, string[]>())
  /** 子节点 → 父节点（父节点加载过才知道）。 */
  const parentOf = new Map<string, string>()
  const loading = shallowReactive(new Set<string>())
  const expanded = ref<string[]>([])
  const rootId = ref<string | null>(null)
  /** reset 时加一，丢弃之前发出的请求的结果。 */
  let generation = 0
  /** 每个节点最近一次请求的序号：只采用最新的结果。 */
  const seqs = new Map<string, number>()
  /** 第一次加载（展开、定位）进行中的请求，同一个节点不重复发。 */
  const firstLoads = new Map<string, Promise<unknown>>()
  /** 第一次加载还在路上时收到了它的事件：加载完成后再补刷一次。 */
  const dirtyLoads = new Set<string>()
  let seq = 0

  function reset(root: N) {
    generation++
    nodes.clear()
    childIds.clear()
    parentOf.clear()
    loading.clear()
    seqs.clear()
    firstLoads.clear()
    dirtyLoads.clear()
    rootId.value = root.id
    nodes.set(root.id, root)
    expanded.value = [root.id]
  }

  /** 把子节点放进树里；`updateSelf` 更新节点自己（给了时）。 */
  function apply(id: string, children: N[], updateSelf?: (self: N | undefined) => N | undefined) {
    for (const c of children) {
      nodes.set(c.id, c)
      parentOf.set(c.id, id)
    }
    childIds.set(
      id,
      children.map((c) => c.id),
    )
    if (updateSelf) {
      const next = updateSelf(nodes.get(id))
      if (next) nodes.set(id, next)
    }
  }

  /**
   * 加载（或重新加载）一个节点的子节点，返回原始响应。
   * 节点已经不存在时从树上去掉它，返回 null；其他错误照常抛出。
   */
  async function load(id: string): Promise<R | null> {
    const gen = generation
    const mySeq = ++seq
    seqs.set(id, mySeq)
    loading.add(id)
    try {
      const res = await source.fetch(id)
      if (gen !== generation || seqs.get(id) !== mySeq) return res
      apply(id, source.children(res), source.updateSelf ? (self) => source.updateSelf!(self, res) : undefined)
      // 在路上时收到过它的事件：再取一次（用最新的结果覆盖）。
      if (dirtyLoads.delete(id)) void load(id).catch(() => null)
      return res
    } catch (e) {
      if (gen === generation && seqs.get(id) === mySeq) dirtyLoads.delete(id)
      if (gen === generation && source.isGone(e)) {
        forget(id)
        return null
      }
      throw e
    } finally {
      if (gen === generation && seqs.get(id) === mySeq) loading.delete(id)
    }
  }

  /** 节点已被删除：从树上去掉（它的父节点下次刷新时也会去掉它）。 */
  function forget(id: string) {
    childIds.delete(id)
    expanded.value = expanded.value.filter((x) => x !== id)
    const parent = parentOf.get(id)
    if (parent && childIds.has(parent)) {
      childIds.set(
        parent,
        childIds.get(parent)!.filter((x) => x !== id),
      )
    }
  }

  /** 没加载过的加载一次；正在加载的不重复发请求。 */
  function loadOnce(id: string): Promise<unknown> {
    if (childIds.has(id)) return Promise.resolve()
    const existing = firstLoads.get(id)
    if (existing) return existing
    const p: Promise<unknown> = load(id)
      .catch(() => null)
      .finally(() => {
        if (firstLoads.get(id) === p) firstLoads.delete(id)
      })
    firstLoads.set(id, p)
    return p
  }

  function ensureLoaded(id: string) {
    void loadOnce(id)
  }

  /** 展开并加载一串祖先（从上到下），让它们下面的节点出现在树上。 */
  async function revealChain(chain: string[]) {
    const want = new Set(expanded.value)
    for (const id of chain) want.add(id)
    expanded.value = [...want]
    await Promise.all(chain.map(loadOnce))
  }

  /**
   * 重新加载这些节点里已经加载过的（SSE 事件）；`except` 是调用方自己会重新加载的。
   * 第一次加载还在路上的，记下来，等它回来后补刷一次。
   */
  async function refresh(ids: Iterable<string>, except?: string | null) {
    const targets: string[] = []
    for (const id of new Set(ids)) {
      if (id === except) continue
      if (childIds.has(id)) targets.push(id)
      else if (loading.has(id)) dirtyLoads.add(id)
    }
    await Promise.all(targets.map((id) => load(id).catch(() => null)))
  }

  /** 重新加载所有已加载的节点（resync、重连后）；`except` 同上。 */
  async function refreshAll(except?: string | null) {
    await refresh([...childIds.keys()], except)
  }

  /** 看得到的展开节点：根，以及从根一路展开下来的节点（祖先有收起的不算）。 */
  function shownExpanded(): Set<string> {
    const root = rootId.value
    const out = new Set<string>()
    if (!root) return out
    const open = new Set(expanded.value)
    out.add(root)
    const queue = [root]
    while (queue.length) {
      const id = queue.pop()!
      for (const c of childIds.get(id) ?? []) {
        if (open.has(c) && !out.has(c)) {
          out.add(c)
          queue.push(c)
        }
      }
    }
    return out
  }

  /**
   * 只刷新看得到的部分（M3-5 审查：事件放大）：`targets`（不给是全部已加载的）里看得到的展开节点重新加载；
   * 收起的（或祖先收起了、看不到的）已加载节点丢掉子节点，下次展开时重新取，不为它们发请求。
   * 第一次加载还在路上、看得到的，等它回来后补刷一次。
   */
  async function refreshShown(targets?: Iterable<string>) {
    const shown = shownExpanded()
    const want = targets ? new Set(targets) : new Set(childIds.keys())
    const reload: string[] = []
    for (const id of want) {
      if (!childIds.has(id)) {
        if (loading.has(id) && shown.has(id)) dirtyLoads.add(id)
        continue
      }
      if (shown.has(id)) reload.push(id)
      else childIds.delete(id)
    }
    await Promise.all(reload.map((id) => load(id).catch(() => null)))
  }

  /** 改树上的一个节点（例如改名）；`fn` 返回原对象表示不变。 */
  function patch(id: string, fn: (node: N) => N) {
    const n = nodes.get(id)
    if (!n) return
    const next = fn(n)
    if (next !== n) nodes.set(id, next)
  }

  /** 已加载的子节点；没加载过时：有子节点（或还不知道）返回 []（显示展开箭头），没有返回 undefined。 */
  function childrenOf(node: N): N[] | undefined {
    const ids = childIds.get(node.id)
    if (ids) {
      const list = ids.map((id) => nodes.get(id)).filter((n): n is N => !!n)
      return list.length ? list : undefined
    }
    return source.isLeaf(node) ? undefined : []
  }

  /** 父节点（父节点加载过才知道）。 */
  function parentIdOf(id: string): string | undefined {
    return parentOf.get(id)
  }

  /** 从根到这个节点的名字（不含根），用 / 连接，例如 `/2025 文书档案/盒 001`；根是 `/`。 */
  function pathOf(id: string): string {
    const names: string[] = []
    let cur: string | undefined = id
    while (cur && cur !== rootId.value) {
      const n = nodes.get(cur)
      names.unshift(n ? source.label(n) : '…')
      cur = parentOf.get(cur)
    }
    return `/${names.join('/')}`
  }

  // 展开了还没加载的节点：加载它的子节点。
  watch(expanded, (list) => list.forEach(ensureLoaded))

  return {
    nodes,
    childIds,
    loading,
    expanded,
    rootId,
    reset,
    apply,
    load,
    forget,
    ensureLoaded,
    revealChain,
    refresh,
    refreshAll,
    shownExpanded,
    refreshShown,
    patch,
    childrenOf,
    parentIdOf,
    pathOf,
  }
}
