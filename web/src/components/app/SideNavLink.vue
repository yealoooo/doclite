<script setup lang="ts">
// 侧栏的一项（M7-11）：图标 + 名字。侧栏收起时只显示图标，悬停在右边提示名字。
// 给了 `to` 是路由链接，没给是按钮（点了发 click）。
import type { Component } from 'vue'
import { RouterLink, type RouteLocationRaw } from 'vue-router'

import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

const props = defineProps<{
  label: string
  icon: Component
  collapsed: boolean
  to?: RouteLocationRaw
  active?: boolean
  class?: string
}>()

const emit = defineEmits<{ click: [] }>()

// data-testid、data-menu 等放到链接或按钮上，不放到外面的 Tooltip 上。
defineOptions({ inheritAttrs: false })

const base = 'flex items-center rounded-md text-sm transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground'
</script>

<template>
  <Tooltip :disabled="!props.collapsed">
    <TooltipTrigger as-child>
      <RouterLink
        v-if="props.to"
        :to="props.to"
        :aria-label="props.collapsed ? props.label : undefined"
        :class="
          cn(
            base,
            props.collapsed ? 'h-9 justify-center' : 'gap-2.5 px-2.5 py-2',
            props.active ? 'bg-sidebar-accent font-medium text-sidebar-accent-foreground' : '',
            props.class,
          )
        "
        v-bind="$attrs"
      >
        <component :is="props.icon" class="size-4 shrink-0" />
        <span v-if="!props.collapsed" class="truncate">{{ props.label }}</span>
      </RouterLink>
      <button
        v-else
        type="button"
        :aria-label="props.collapsed ? props.label : undefined"
        :class="
          cn(
            base,
            props.collapsed ? 'h-9 justify-center' : 'gap-2.5 px-2.5 py-2',
            props.active ? 'bg-sidebar-accent font-medium text-sidebar-accent-foreground' : '',
            props.class,
          )
        "
        v-bind="$attrs"
        @click="emit('click')"
      >
        <component :is="props.icon" class="size-4 shrink-0" />
        <span v-if="!props.collapsed" class="truncate">{{ props.label }}</span>
      </button>
    </TooltipTrigger>
    <TooltipContent side="right" :side-offset="8">{{ props.label }}</TooltipContent>
  </Tooltip>
</template>
