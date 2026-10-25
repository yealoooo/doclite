// 审核分配操作的错误文案（M2-5）。server 的 message 已经说明了原因（例如审核人不合格是哪一条、超过上限），
// 这里按错误码补上"该怎么办"。错误码见 design §7（M2-1）。提示用 api/http.ts 的 toastOpError（全局处理过的不重复提示）。
// invalid_input（审核人已停用、没有审核菜单、进不了这个项目；超过 500 个或 1 万个；参数不对）用 server 的 message。

import { errorText } from '@/api/http'

export function reviewOpErrorText(e: unknown): string {
  return errorText(e, {
    not_found: '有文件或文件夹已不存在（可能刚被删除或移走），列表已刷新，请重新选择',
    invalid_state: '文件的审核状态刚刚变了（可能被别人分配、审完或改派），列表已刷新',
  })
}
