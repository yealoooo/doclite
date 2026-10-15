// 审核分配操作的错误文案（M2-5）。server 的 message 已经说明了原因（例如审核人不合格是哪一条、超过上限），
// 这里按错误码补上"该怎么办"。错误码见 design §7（M2-1）。

import { toast } from 'vue-sonner'

import { ApiError, errorMessage } from '@/api/http'

export function reviewOpErrorText(e: unknown): string {
  if (!(e instanceof ApiError)) return errorMessage(e)
  switch (e.code) {
    case 'invalid_input':
      // 审核人已停用、没有审核菜单、进不了这个项目；超过 500 个或 1 万个；参数不对。
      return e.message
    case 'not_found':
      return '有文件或文件夹已不存在（可能刚被删除或移走），列表已刷新，请重新选择'
    case 'invalid_state':
      return '文件的审核状态刚刚变了（可能被别人分配、审完或改派），列表已刷新'
    default:
      return e.message
  }
}

/**
 * 请求封装已经全局处理过（并提示过）的错误：401（跳登录页）、403 `forbidden`（刷新菜单）、426（升级提示）。
 * 调用方传了 `handle: true` 时其余错误都不弹提示，由调用方提示一次。
 */
export function handledGlobally(e: unknown): boolean {
  return (
    e instanceof ApiError &&
    (e.status === 401 || (e.status === 403 && e.code === 'forbidden') || e.status === 426)
  )
}

/** 操作失败的提示（全局已经提示过的不再重复）。 */
export function toastOpError(e: unknown, text = reviewOpErrorText(e), description?: string) {
  if (handledGlobally(e)) return
  toast.error(text, description ? { description } : undefined)
}

/** 4xx：这次操作的前提已经不成立（状态变了、文件夹没了、超过上限……），重试也没用。 */
export function isClientError(e: unknown): boolean {
  return e instanceof ApiError && e.status >= 400 && e.status < 500
}
