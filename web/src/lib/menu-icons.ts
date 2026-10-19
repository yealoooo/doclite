// 侧栏菜单的图标（M7-11）：菜单来自 /api/me/menus，没有图标字段，这里按菜单码配。不认识的菜单用一个圆点。

import {
  CircleIcon,
  ClipboardCheckIcon,
  DatabaseIcon,
  FolderKanbanIcon,
  FolderTreeIcon,
  KeyRoundIcon,
  LayoutDashboardIcon,
  LibraryIcon,
  ListChecksIcon,
  PackageIcon,
  ScanLineIcon,
  ScrollTextIcon,
  ShieldCheckIcon,
  UsersIcon,
} from '@lucide/vue'
import type { Component } from 'vue'

const ICONS: Record<string, Component> = {
  dashboard: LayoutDashboardIcon,
  files: FolderTreeIcon,
  scan: ScanLineIcon,
  review: ClipboardCheckIcon,
  review_assign: ListChecksIcon,
  catalog: LibraryIcon,
  export: PackageIcon,
  'system.users': UsersIcon,
  'system.roles': ShieldCheckIcon,
  'system.projects': FolderKanbanIcon,
  'system.storage': DatabaseIcon,
  'system.license': KeyRoundIcon,
  'system.audit': ScrollTextIcon,
}

export function menuIcon(menuCode: string): Component {
  return ICONS[menuCode] ?? CircleIcon
}
