<script setup lang="ts">
// 文件夹的归档状态徽标（M9，requirements 5.9"界面 /catalog"，Mockup v3.1 的 folderBadge）：/catalog 左栏和 /files 的
// 子文件夹都用它。
// - 已封存："🔒 办公室 0003"（浅紫；已锁定的是实心紫，见 ARCHIVE_STATE）；
// - 修改中："✎ 修改中 · 办公室 0003"（浅青）；
// - 不是任何条目的来源：`showNone` 时"未归档"（灰色），否则什么都不显示。
// 件号补零的位数：给了 `itemNoWidth` 用它，否则从档号末尾猜（/files 不取 catalog-settings）。
// 截图脚本用的属性：data-testid=folder-archive-badge（data-state：none、sealed、modifying；data-item-id）。
import { computed } from 'vue'

import type { FolderArchive } from '@/api/types'
import { StatusBadge } from '@/components/ui/status-badge'
import { ARCHIVE_STATE, folderArchiveState, itemNoWidthFrom, itemShortLabel } from '@/lib/catalog'

const props = defineProps<{
  archive: FolderArchive | null | undefined
  itemNoWidth?: number | null
  showNone?: boolean
}>()

const label = computed(() => {
  const a = props.archive
  if (!a) return ''
  const width = props.itemNoWidth ?? itemNoWidthFrom(a.archive_no, a.item_no)
  return itemShortLabel(a, width)
})

const state = computed(() => folderArchiveState(props.archive))

const title = computed(() => {
  const a = props.archive
  if (!a) return '还没有归档'
  const where = [...a.category_names, a.archive_no ?? `第 ${a.item_no} 件`].join(' / ')
  if (a.source_status === 'modifying') {
    return `修改中：将重新归档到 ${where}${a.title ? `「${a.title}」` : ''}`
  }
  return `已归档为 ${where}${a.title ? `「${a.title}」` : ''}（${a.locked ? '已锁定' : '未锁定'}），已封存`
})
</script>

<template>
  <StatusBadge
    v-if="props.archive"
    :status="state"
    :title="title"
    data-testid="folder-archive-badge"
    :data-state="props.archive.source_status === 'modifying' ? 'modifying' : 'sealed'"
    :data-item-id="props.archive.item_id"
  >{{ props.archive.source_status === 'modifying' ? `修改中 · ${label}` : label }}</StatusBadge>
  <StatusBadge
    v-else-if="props.showNone"
    :status="ARCHIVE_STATE.none"
    data-testid="folder-archive-badge"
    data-state="none"
  />
</template>
