// 按文件夹的子树计数（M2-5 的 useReviewCounts 抽出来的通用部分；M3-7 的看板也用）：review-summary、
// catalog-summary 这类接口——传一个文件夹（不传是根文件夹）返回它自己的子树计数（`counts`）、直接放在里面的
// （`direct_counts`），以及每个子文件夹的子树计数（`children[].counts`），外加 `parent_id`。
//
// - 每"加载"一个文件夹调一次接口；所以根、已加载的文件夹和它们的子文件夹都有子树计数。
// - 实时刷新（`files.changed` 只带文件直接所在的文件夹，design §2.2）：祖先的计数也变了，要按路径刷新——
//   沿 parent 链（来自接口的 parent_id 和 children）找到全部祖先，重新加载其中已加载过的；链不完整的
//   （这个文件夹所在的那一层还没加载过）用 `GET /api/folders/{id}` 的祖先链补上（要 files 或 scan 菜单，
//   `canLookup()` 为 false 时改为刷新全部已加载的）。
// - 文件夹移动后 parent 关系会变（M2-5 审查第 1 轮）：一个文件夹的结果回来时，去掉"父文件夹是它、但已经不在它的
//   children 里"的记录；收到 `folders.changed` 时用 `forgetUnloadedParents()` 丢掉没加载过的文件夹的记录（它们的
//   parent 只能从祖先链补，可能已经过时），下次按需重新查。
// - `refreshSubtree(ids)`：除了祖先，还刷新这些文件夹下面已加载的文件夹（自己按文件夹操作之后）。
// - 同一个文件夹只采用最新一次请求的结果；`reset()`（换项目）后丢弃在途的结果。文件夹已不存在（404）时不再跟踪它。
//
//   const s = useFolderSummaries(() => project.currentId, (pid, fid, handle) => getCatalogSummary(pid, fid, handle))
//   s.reset(rootId); await s.load(rootId)
//   s.get(folderId)        // 子树计数（响应式）
//   s.summaryOf(folderId)  // 这个文件夹自己的完整结果（只有加载过的有，响应式）
//   await s.refreshForFolders(event.folder_ids)

import { ref, shallowReactive } from 'vue'

import { getFolder } from '@/api/folders'
import { isApiError } from '@/api/http'

/** 一次事件里最多按路径补查多少个不认识的文件夹；更多时直接刷新全部已加载的。 */
const MAX_PATH_LOOKUPS = 10

/** 接口结果要有的字段（review-summary、catalog-summary 都是这个结构）。 */
export interface FolderSummaryShape<C> {
  folder_id: string
  folder_name: string
  parent_id: string | null
  counts: C
  direct_counts: C
  children: { folder_id: string; folder_name: string; counts: C }[]
}

/** 取一个文件夹的结果；`folderId` 为 null 表示根文件夹。404 要交给调用方（`handle` 里带 not_found）。 */
export type FolderSummaryFetcher<S> = (projectId: string, folderId: string | null, handle: readonly string[]) => Promise<S>

export interface FolderSummariesOptions {
  /** 能不能用 `GET /api/folders/{id}` 补祖先链（要 files 或 scan 菜单）；默认能。 */
  canLookup?: () => boolean
}

export function useFolderSummaries<C, S extends FolderSummaryShape<C>>(
  projectId: () => string | null,
  fetchSummary: FolderSummaryFetcher<S>,
  opts: FolderSummariesOptions = {},
) {
  /** 文件夹 → 整棵子树的计数。 */
  const subtree = shallowReactive(new Map<string, C>())
  /** 文件夹 → 直接放在里面的文件的计数（只有调过它自己的接口的才有）。 */
  const direct = shallowReactive(new Map<string, C>())
  /** 文件夹 → 它自己的完整结果（只有调过它自己的接口的才有）。 */
  const summaries = shallowReactive(new Map<string, S>())
  /** 文件夹 → 父文件夹（根是 null）。 */
  const parentOf = new Map<string, string | null>()
  /** 调过接口的文件夹（刷新时只刷新这些）。 */
  const loaded = new Set<string>()
  const rootId = ref<string | null>(null)
  let generation = 0
  let seq = 0
  const seqs = new Map<string, number>()

  function reset(root: string | null) {
    generation++
    subtree.clear()
    direct.clear()
    summaries.clear()
    parentOf.clear()
    loaded.clear()
    seqs.clear()
    rootId.value = root
  }

  function apply(s: S) {
    subtree.set(s.folder_id, s.counts)
    direct.set(s.folder_id, s.direct_counts)
    summaries.set(s.folder_id, s)
    parentOf.set(s.folder_id, s.parent_id)
    const kids = new Set(s.children.map((c) => c.folder_id))
    // 原来记在它下面、现在已经不在的（被移走、删掉）：parent 关系作废。
    for (const [child, parent] of parentOf) {
      if (parent === s.folder_id && !kids.has(child)) parentOf.delete(child)
    }
    for (const c of s.children) {
      subtree.set(c.folder_id, c.counts)
      parentOf.set(c.folder_id, s.folder_id)
    }
  }

  /** 加载（或重新加载）一个文件夹；文件夹已不存在（404）时不再跟踪它，返回 null。 */
  async function load(folderId: string): Promise<S | null> {
    const pid = projectId()
    if (!pid) return null
    const gen = generation
    const my = ++seq
    seqs.set(folderId, my)
    loaded.add(folderId)
    try {
      const s = await fetchSummary(pid, folderId === rootId.value ? null : folderId, ['not_found'])
      if (gen === generation && seqs.get(folderId) === my) apply(s)
      return s
    } catch (e) {
      if (gen === generation && isApiError(e, 'not_found')) {
        loaded.delete(folderId)
        subtree.delete(folderId)
        direct.delete(folderId)
        summaries.delete(folderId)
        parentOf.delete(folderId)
        return null
      }
      throw e
    }
  }

  /** 没加载过的加载一次（目录树新加载了一层时）。 */
  function ensure(ids: Iterable<string>) {
    for (const id of ids) if (!loaded.has(id)) void load(id).catch(() => null)
  }

  /** 从这个文件夹往上到根的 id（含自己）；链不完整（有不认识的父文件夹）时返回 null。 */
  function chainOf(id: string): string[] | null {
    const chain: string[] = []
    let cur: string | null | undefined = id
    const seen = new Set<string>()
    while (cur) {
      if (seen.has(cur)) return null
      seen.add(cur)
      chain.push(cur)
      if (cur === rootId.value) return chain
      cur = parentOf.get(cur)
      if (cur === undefined) return null
    }
    return chain
  }

  /** 重新加载全部已加载过的。 */
  async function refreshAll() {
    await Promise.all([...loaded].map((id) => load(id).catch(() => null)))
  }

  /**
   * 不再跟踪这些文件夹（看板收起时，M3-7 审查第 1 轮）：从"已加载"里去掉，丢掉它们自己的结果和直接放的计数，
   * 在途的结果回来也不再采用；以后的 refreshAll、按路径刷新都不再取它们，要用时重新 load。子树计数（父文件夹的
   * children 也会给）和 parent 记录保留。根文件夹不受影响。useReviewCounts 不调用它。
   */
  function forget(ids: Iterable<string>) {
    for (const id of ids) {
      if (id === rootId.value) continue
      loaded.delete(id)
      seqs.delete(id)
      summaries.delete(id)
      direct.delete(id)
    }
  }

  /** 文件夹结构变了（`folders.changed`）：丢掉没加载过的文件夹的 parent 记录（已加载的由各自的结果维护）。 */
  function forgetUnloadedParents() {
    for (const id of [...parentOf.keys()]) if (!loaded.has(id)) parentOf.delete(id)
  }

  /**
   * 这些文件夹里的文件状态变了（事件的 folder_ids，只是直接所在的文件夹）：它们自己和全部祖先的计数都要刷新。
   * 只重新加载其中已加载过的文件夹（它们的结果同时带回各自子文件夹的计数）。
   */
  async function refreshForFolders(ids: Iterable<string>) {
    const affected = new Set<string>()
    const unknown: string[] = []
    for (const id of new Set(ids)) {
      const chain = chainOf(id)
      if (chain) chain.forEach((x) => affected.add(x))
      else unknown.push(id)
    }
    if (unknown.length > MAX_PATH_LOOKUPS || (unknown.length && opts.canLookup && !opts.canLookup())) {
      await refreshAll()
      return
    }
    // 不认识的文件夹（它那一层还没加载过）：取祖先链补上 parent 关系。
    const gen = generation
    await Promise.all(
      unknown.map(async (id) => {
        try {
          const f = await getFolder(id, ['not_found'])
          if (gen !== generation) return
          const chain = [...f.ancestors.map((a) => a.id), f.id]
          chain.forEach((x, i) => {
            if (i > 0) parentOf.set(x, chain[i - 1]!)
            affected.add(x)
          })
        } catch {
          // 文件夹已经没了（被删、被移走）：它原来的祖先不知道，刷新全部已加载的兜底。
          for (const x of loaded) affected.add(x)
        }
      }),
    )
    if (gen !== generation) return
    await Promise.all([...affected].filter((id) => loaded.has(id)).map((id) => load(id).catch(() => null)))
  }

  /**
   * 这些文件夹整棵子树里的文件状态变了（自己按文件夹操作之后）：祖先之外，这些文件夹下面已加载的文件夹也要刷新
   * （实时连接断开时，靠事件刷新不到）。
   */
  async function refreshSubtree(ids: Iterable<string>) {
    const set = new Set(ids)
    const below: string[] = []
    for (const id of loaded) {
      if (set.has(id)) continue
      if (chainOf(id)?.some((x) => set.has(x))) below.push(id)
    }
    await Promise.all([refreshForFolders(set), ...below.map((id) => load(id).catch(() => null))])
  }

  return {
    rootId,
    reset,
    load,
    ensure,
    refreshAll,
    refreshForFolders,
    refreshSubtree,
    forgetUnloadedParents,
    /** 子树计数（响应式）。 */
    get: (id: string | null | undefined) => (id ? subtree.get(id) : undefined),
    /** 直接放在里面的计数（响应式）。 */
    directOf: (id: string | null | undefined) => (id ? direct.get(id) : undefined),
    /** 这个文件夹自己的完整结果（含 children 的顺序和名字；只有加载过的有，响应式）。 */
    summaryOf: (id: string | null | undefined) => (id ? summaries.get(id) : undefined),
    /** 是否调过它自己的接口（含在途的）。 */
    isLoaded: (id: string) => loaded.has(id),
    /** 正在跟踪（加载过、没被 forget）的文件夹。 */
    loadedIds: () => [...loaded],
    forget,
  }
}
