// 归类页操作的错误文案（M3-5）。server 的 message 大多已经写明了原因和该怎么办（哪个编码重复、哪一条锁定的
// 前置条件不满足、有几件已锁定……），这里只给没有上下文的几种补一句。错误码见 design §7（M3-1、M3-2）。
// item_locked、category_not_empty、invalid_state（根节点、模板解析不了）、invalid_input：server 写明了。

import { errorText } from '@/api/http'

/** 归类节点的新建、修改、移动、删除。 */
export function categoryOpErrorText(e: unknown, ctx: { code?: string } = {}): string {
  return errorText(e, {
    conflict: ctx.code ? `同一层已有编码为「${ctx.code}」的节点（区分大小写），请换一个编码` : undefined,
    tree_cycle: '不能把节点移到它自己或它下面的节点里',
    tree_full: '这一层的节点数量已到上限，请换一个上级节点',
    not_found: '节点已不存在（可能刚被别人删除），归类树已刷新',
  })
}

/** 条目的保存、状态动作、移动。 */
export function itemOpErrorText(e: unknown): string {
  return errorText(e, { not_found: '条目或目标节点已不存在（可能刚被别人删除），已刷新' })
}
