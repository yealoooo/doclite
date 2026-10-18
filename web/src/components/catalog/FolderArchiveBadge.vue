<script setup lang="ts">
// 文件夹的归档状态徽标（M9，requirements 5.9"界面 /catalog"，Mockup v3.1 的 folderBadge）：/catalog 左栏和 /files 的
// 子文件夹都用它。
// - 已封存："🔒 办公室 0003"（紫色；已锁定的深一点）；
// - 修改中："✎ 修改中 · 办公室 0003"（橙色）；
// - 不是任何条目的来源：`showNone` 时"未归档"（灰色），否则什么都不显示。
// 件号补零的位数：给了 `itemNoWidth` 用它，否则从档号末尾猜（/files 不取 catalog-settings）。
// 截图脚本用的属性：data-testid=folder-archive-badge（data-state：none、sealed、modifying；data-item-id）。
import { LockIcon, PencilLineIcon } from '@lucide/vue'
import { computed } from 'vue'

import type { FolderArchive } from '@/api/types'
import { itemNoWidthFrom, itemShortLabel } from '@/lib/catalog'

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
  <span
    v-if="props.archive && props.archive.source_status === 'modifying'"
    class="inline-flex max-w-full shrink-0 items-center gap-1 truncate rounded-full border border-orange-300 bg-orange-50 px-1.5 text-[11px] leading-[18px] whitespace-nowrap text-orange-700"
    :title="title"
    data-testid="folder-archive-badge"
    data-state="modifying"
    :data-item-id="props.archive.item_id"
  >
    <PencilLineIcon class="size-3 shrink-0" /><span class="truncate">修改中 · {{ label }}</span>
  </span>
  <span
    v-else-if="props.archive"
    class="inline-flex max-w-full shrink-0 items-center gap-1 truncate rounded-full px-1.5 text-[11px] leading-[18px] whitespace-nowrap"
    :class="props.archive.locked ? 'bg-violet-200 text-violet-800' : 'bg-violet-100 text-violet-700'"
    :title="title"
    data-testid="folder-archive-badge"
    data-state="sealed"
    :data-item-id="props.archive.item_id"
  >
    <LockIcon class="size-3 shrink-0" /><span class="truncate">{{ label }}</span>
  </span>
  <span
    v-else-if="props.showNone"
    class="inline-flex shrink-0 items-center rounded-full bg-muted px-1.5 text-[11px] leading-[18px] whitespace-nowrap text-muted-foreground"
    data-testid="folder-archive-badge"
    data-state="none"
  >未归档</span>
</template>
