// 归类页顶部"✎ 修改中 n 件"的数据（M9，requirements 5.9"不让人忘"）：`GET /api/projects/{pid}/archive-items:modifying`
// （最多 500 件，按解除封存的时间倒序，total 是真实总数）。页面在进入、catalog.changed / folders.changed（去抖后）、
// 自己解除封存、重新归档、撤销归档之后重取。

import { listModifyingItems } from '@/api/catalog'
import type { ModifyingList } from '@/api/types'

export type { ModifyingItem } from '@/api/types'

export function loadModifyingItems(projectId: string): Promise<ModifyingList> {
  return listModifyingItems(projectId, true)
}
