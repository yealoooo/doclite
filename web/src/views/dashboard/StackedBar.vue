<script setup lang="ts">
// 看板用的横向堆叠条（纯 CSS，不加图表库）：每段按数量占比（flex-grow），段之间留 2px 白缝，没有填满的部分是浅灰的
// 底（例如"未归类"）。数为 0 的段不画；很小的段至少 2px，免得看不见。悬停整条时浏览器提示各段的数（title）。
import { computed } from 'vue'

export interface BarSegment {
  key: string
  label: string
  value: number
  /** 段的颜色（Tailwind 背景类）。 */
  color: string
}

const props = withDefaults(
  defineProps<{
    segments: BarSegment[]
    /** 整条代表多少；比各段之和大的部分画成底色。 */
    total: number
    /** 条的高度（px）。 */
    height?: number
  }>(),
  { height: 8 },
)

const parts = computed(() => {
  const shown = props.segments.filter((s) => s.value > 0)
  const used = shown.reduce((a, s) => a + s.value, 0)
  const rest = Math.max(0, props.total - used)
  return { shown, rest }
})

const title = computed(() =>
  props.segments.map((s) => `${s.label} ${s.value.toLocaleString()}`).join(' · ') +
  (props.total > 0 ? `（共 ${props.total.toLocaleString()}）` : ''),
)
</script>

<template>
  <div class="flex w-full gap-[2px] overflow-hidden rounded-[3px]" :style="{ height: `${props.height}px` }" :title="title">
    <span
      v-for="s in parts.shown"
      :key="s.key"
      class="min-w-[2px]"
      :class="s.color"
      :style="{ flexGrow: s.value, flexBasis: 0 }"
      :data-segment="s.key"
      :data-value="s.value"
    />
    <span v-if="parts.rest > 0 || props.total === 0" class="min-w-[2px] bg-muted" :style="{ flexGrow: parts.rest || 1, flexBasis: 0 }" />
  </div>
</template>
