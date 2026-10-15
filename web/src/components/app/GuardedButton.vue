<script setup lang="ts">
// 可能因权限被禁用的按钮：给了 `reason` 时按钮禁用，鼠标移上去（或键盘聚焦）显示原因。
// 禁用的 <button> 收不到鼠标事件，所以外面包一层 span 当提示的触发器。
import type { ButtonVariants } from '@/components/ui/button'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

defineOptions({ inheritAttrs: false })

defineProps<{
  reason?: string | null
  variant?: ButtonVariants['variant']
  size?: ButtonVariants['size']
  disabled?: boolean
}>()
</script>

<template>
  <Tooltip v-if="reason">
    <TooltipTrigger as-child>
      <span tabindex="0" class="inline-flex cursor-not-allowed rounded-md" :data-blocked="reason">
        <Button :variant="variant" :size="size" disabled v-bind="$attrs">
          <slot />
        </Button>
      </span>
    </TooltipTrigger>
    <TooltipContent side="top">{{ reason }}</TooltipContent>
  </Tooltip>
  <Button v-else :variant="variant" :size="size" :disabled="disabled" v-bind="$attrs">
    <slot />
  </Button>
</template>
