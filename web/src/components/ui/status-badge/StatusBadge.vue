<script setup lang="ts">
// 状态徽标（M10-5）：审核状态、归档状态、条目状态、导出任务、存储迁移、审计操作……同一个状态到处一个样子。
// `status` 是各模块状态表里的一项（lib/status.ts 的 StatusStyle：文字、颜色、可选的图标）；默认插槽可以换文字
// （例如"修改中 3"）。`dot` 时文字前加一个圆点（审核状态）。
// 尺寸：sm 小号（11px，列表行、树）；md 常规（12px，页头、筛选条）。
import { LockIcon, PencilLineIcon } from '@lucide/vue'
import type { HTMLAttributes } from 'vue'

import type { StatusIcon, StatusStyle } from '@/lib/status'
import { cn } from '@/lib/utils'

const props = withDefaults(
  defineProps<{
    status: StatusStyle
    size?: 'sm' | 'md'
    dot?: boolean
    class?: HTMLAttributes['class']
  }>(),
  { size: 'sm', dot: false, class: undefined },
)

const ICONS: Record<StatusIcon, typeof LockIcon> = { lock: LockIcon, pencil: PencilLineIcon }
</script>

<template>
  <span
    data-slot="status-badge"
    :class="
      cn(
        'inline-flex max-w-full shrink-0 items-center gap-1 rounded-full whitespace-nowrap',
        props.size === 'sm' ? 'px-1.5 text-2xs leading-[18px]' : 'px-2 text-xs leading-5',
        props.status.badge,
        props.class,
      )
    "
  >
    <span v-if="props.dot && props.status.dot" class="size-1.5 shrink-0 rounded-full" :class="props.status.dot" />
    <component :is="ICONS[props.status.icon]" v-else-if="props.status.icon" class="size-3 shrink-0" />
    <span class="truncate"><slot>{{ props.status.label }}</slot></span>
  </span>
</template>
