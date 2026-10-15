// 文件目录整理操作的错误文案（M1-12）。server 的 message 已经说明了原因，这里按错误码补上"该怎么办"，
// 让重名、成环、操作根文件夹、已归类、编号用完、名字不合法（含 Windows 保留名）都有明确的提示。

import { ApiError, errorMessage } from '@/api/http'

export interface OpContext {
  /** 操作的对象。 */
  kind: 'file' | 'files' | 'folder'
  /** 新名字（改名、新建时）。 */
  name?: string
}

/** 给人看的一句话（标题）和补充说明。 */
export interface OpError {
  title: string
  detail?: string
}

export function opError(e: unknown, ctx: OpContext): OpError {
  if (!(e instanceof ApiError)) return { title: errorMessage(e) }
  const what = ctx.kind === 'folder' ? '文件夹' : '文件'
  switch (e.code) {
    case 'conflict':
      return ctx.name
        ? { title: `同一个文件夹里已有名为「${ctx.name}」的${what}（不区分大小写），请换一个名字` }
        : { title: `同一个文件夹里已有同名的${what}`, detail: e.message }
    case 'tree_cycle':
      return { title: '不能把文件夹移到它自己或它下面的子文件夹里' }
    case 'root_folder_locked':
      return { title: '根文件夹不能改名、移动或删除', detail: '根文件夹的名称跟着项目名称走（在「系统管理 › 项目」里改），备注可以改。' }
    case 'file_cataloged':
      return {
        title: ctx.kind === 'folder' ? '文件夹里有已归类的文件，不能删除' : '有已归类的文件，不能删除',
        detail: `${e.message}。请先到「归类」里把它们从档案条目中移出，再回来删除`,
      }
    case 'tree_full':
      return {
        title: '这一层的子文件夹数量已到上限',
        detail: '每个文件夹下最多 99999 个子文件夹，删除的也占编号。请换一个上级文件夹。',
      }
    case 'invalid_input':
      // 名字不合法（含 Windows 保留名）：server 的 message 写明了是哪一条。
      return { title: e.message }
    case 'not_found':
      return { title: `${what}已不存在（可能刚被别人删除或移动），列表已刷新`, detail: e.message }
    default:
      return { title: e.message }
  }
}

/** 对话框里显示的一行（标题加说明）。 */
export function opErrorText(e: unknown, ctx: OpContext): string {
  const r = opError(e, ctx)
  return r.detail ? `${r.title}。${r.detail}` : r.title
}
