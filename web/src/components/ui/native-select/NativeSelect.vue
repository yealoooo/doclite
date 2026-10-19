<script setup lang="ts" generic="T">
// 原生下拉框（M10-5）：样式和 Input 一样（边框、焦点环、字号），高度用 size 选：default h-9（和 Input 一样）、
// sm h-8（筛选栏、对话框里的一行）、xs h-7（表格里）。用 v-model；<option> 写在插槽里。
import type { HTMLAttributes } from 'vue'

import { cn } from '@/lib/utils'

const props = withDefaults(defineProps<{ size?: 'default' | 'sm' | 'xs'; class?: HTMLAttributes['class'] }>(), {
  size: 'default',
  class: undefined,
})

const model = defineModel<T>()
</script>

<template>
  <select
    v-model="model"
    data-slot="native-select"
    :class="
      cn(
        'min-w-0 rounded-md border border-input bg-background text-foreground shadow-xs transition-[color,box-shadow] outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20',
        props.size === 'default' ? 'h-9 px-2 text-sm' : props.size === 'sm' ? 'h-8 px-2 text-sm' : 'h-7 px-1.5 text-compact',
        props.class,
      )
    "
  >
    <slot />
  </select>
</template>
