// 状态徽标（components/ui/status-badge）的样式约定。各模块的状态表都是这个形状、都叫 `XXX_STATUS`（或 ARCHIVE_STATE），
// 取样式用 `xxxStatusOf()`：
// - 审核状态 REVIEW_STATUS / reviewStatusOf（lib/files.ts）；
// - 归档状态 ARCHIVE_STATE、条目状态 ITEM_STATUS / itemStatusOf（lib/catalog.ts）；
// - 导出任务 TASK_STATUS / taskStatusOf（lib/export.ts）；
// - 存储迁移 migrationStatusOf、存储状态 STORAGE_STATUS（lib/storage.ts）；
// - 审计的操作 auditActionStatusOf（lib/audit.ts）；
// - 项目状态 PROJECT_STATUS（lib/project.ts）、用户状态 USER_STATUS（这里）。
// 颜色只用 style.css 里的语义色（muted、info、ok、warn、destructive、sealed、modifying），同一个状态到处一个样子。
//
// 这个文件只有类型和常量（lib/catalog.ts 只能 `import type` 它，截图脚本在 Node 里直接载入 catalog.ts）。

/** 徽标前的小图标（StatusBadge 里换成 lucide 图标）：lock 已归档、已锁定，pencil 修改中。 */
export type StatusIcon = 'lock' | 'pencil'

export interface StatusStyle {
  label: string
  /** 徽标的底色和文字色。 */
  badge: string
  /** 只显示一个圆点时的颜色（列表里的小圆点）。 */
  dot?: string
  icon?: StatusIcon
}

/** 不认识的状态：灰色，文字原样显示。 */
export function unknownStatus(label: string): StatusStyle {
  return { label, badge: 'bg-muted text-muted-foreground', dot: 'bg-muted-foreground/45' }
}

/** 用户状态（系统管理 › 用户）。 */
export const USER_STATUS: Record<'active' | 'disabled', StatusStyle> = {
  active: { label: '启用', badge: 'bg-ok-soft text-ok' },
  disabled: { label: '停用', badge: 'bg-destructive-soft text-destructive' },
}
