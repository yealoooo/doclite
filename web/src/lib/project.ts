// 项目页用到的文字和表单校验，和 server 的 doclite_domain::projects 一致（只做预检，后端仍会校验）。

import type { ProjectStatus } from '@/api/types'
import type { StatusStyle } from '@/lib/status'

import { charCount, CONTROL } from './validate'

export const PROJECT_STATUS_LABEL: Record<ProjectStatus, string> = {
  active: '进行中',
  archived: '已归档',
}

export const PROJECT_STATUS: Record<ProjectStatus, StatusStyle> = {
  active: { label: PROJECT_STATUS_LABEL.active, badge: 'bg-ok-soft text-ok' },
  archived: { label: PROJECT_STATUS_LABEL.archived, badge: 'bg-muted text-muted-foreground' },
}

export const MAX_PROJECT_CODE_CHARS = 32
export const MAX_PROJECT_NAME_CHARS = 100
export const MAX_PROJECT_DESCRIPTION_CHARS = 500

/** 项目编码：英文字母或数字开头，只能有英文字母、数字和 . _ -，最长 32 个字符；建好后不能改。 */
export function checkProjectCode(value: string): string {
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/.test(value.trim())) {
    return `项目编码要以英文字母或数字开头，只能包含英文字母、数字和 . _ -，最长 ${MAX_PROJECT_CODE_CHARS} 个字符`
  }
  return ''
}

/** 项目名称：它也是根文件夹的名称，按文件夹名的规则（design §1.7）。 */
export function checkProjectName(value: string): string {
  const v = value.trim()
  if (!v || charCount(v) > MAX_PROJECT_NAME_CHARS) return `项目名称不能为空，最长 ${MAX_PROJECT_NAME_CHARS} 个字符`
  const bad = /[\\/:*?<>|"]/.exec(v)
  if (bad) return `项目名称不能含 ${bad[0]}（它也是根文件夹的名称，不能含 \\ / : * ? < > | 和双引号）`
  if (CONTROL.test(v)) return '项目名称不能含控制字符'
  if (v.endsWith('.')) return '项目名称不能以点结尾（它也是根文件夹的名称）'
  return ''
}

export function checkProjectDescription(value: string): string {
  if (charCount(value.trim()) > MAX_PROJECT_DESCRIPTION_CHARS) return `说明最长 ${MAX_PROJECT_DESCRIPTION_CHARS} 个字符`
  return ''
}
