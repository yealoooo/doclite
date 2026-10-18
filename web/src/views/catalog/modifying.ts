// 归类页顶部"✎ 修改中 n 件"的数据（M9，requirements 5.9"不让人忘"）。
//
// server 没有"项目里修改中的条目"这样的接口（M9-2 的遗留问题，见任务返回），这里用现有的读接口凑：沿归类树往下走，
// 只进子树里有条目的节点（`item_count > 0`），对直接放了条目的节点取一页列表（`limit=1`，只为看 `counts.modifying`），
// 有修改中的再把这个节点的条目全部取下来、挑出修改中的。同时最多 4 个请求。节点多的项目会慢一些；以后有了专门的接口
// 只要换掉 `loadModifyingItems`。

import { listArchiveCategories, listArchiveItems, MAX_ITEMS_PAGE } from '@/api/catalog'
import type { ArchiveCategory, ArchiveItemSummary } from '@/api/types'

export interface ModifyingItem extends ArchiveItemSummary {
  /** 所在节点从顶层起的名称（不含根）。 */
  category_names: string[]
}

const CONCURRENCY = 4

/** 一个节点里直接放的条目里修改中的。 */
async function modifyingIn(categoryId: string): Promise<ArchiveItemSummary[]> {
  const head = await listArchiveItems(categoryId, { limit: 1 }, true)
  if (!head.counts.modifying) return []
  const out: ArchiveItemSummary[] = []
  let cursor: string | null = null
  for (;;) {
    const page = await listArchiveItems(categoryId, { limit: MAX_ITEMS_PAGE, cursor }, true)
    out.push(...page.items.filter((i) => i.source_status === 'modifying'))
    cursor = page.next_cursor
    if (!cursor) break
  }
  return out
}

/** 走一遍归类树，返回项目里全部修改中的条目（按节点路径、件号排）。 */
export async function loadModifyingItems(projectId: string): Promise<ModifyingItem[]> {
  const found: ModifyingItem[] = []
  const queue: (() => Promise<void>)[] = []

  function visitLevel(parent: ArchiveCategory, children: ArchiveCategory[], names: string[]) {
    const direct = parent.item_count - children.reduce((n, c) => n + c.item_count, 0)
    if (direct > 0) {
      queue.push(async () => {
        const list = await modifyingIn(parent.id)
        found.push(...list.map((i) => ({ ...i, category_names: names })))
      })
    }
    for (const c of children) {
      if (c.item_count <= 0) continue
      const childNames = [...names, c.category_name]
      if (c.child_count > 0) {
        queue.push(async () => {
          const res = await listArchiveCategories(projectId, c.id, true)
          visitLevel(res.parent, res.items, childNames)
        })
      } else {
        queue.push(async () => {
          const list = await modifyingIn(c.id)
          found.push(...list.map((i) => ({ ...i, category_names: childNames })))
        })
      }
    }
  }

  const top = await listArchiveCategories(projectId, null, true)
  visitLevel(top.parent, top.items, [])

  // 简单的并发池：队列在执行中还会变长（往下一层）。
  let running = 0
  let failed: unknown = null
  await new Promise<void>((resolve) => {
    const pump = () => {
      if (failed !== null || (!queue.length && running === 0)) {
        if (running === 0) resolve()
        return
      }
      while (running < CONCURRENCY && queue.length) {
        const task = queue.shift()!
        running++
        task()
          .catch((e) => {
            failed ??= e
          })
          .finally(() => {
            running--
            pump()
          })
      }
    }
    pump()
  })
  if (failed !== null) throw failed
  return found.sort(
    (a, b) => a.category_names.join('\u0000').localeCompare(b.category_names.join('\u0000'), 'zh') || a.item_no - b.item_no,
  )
}
