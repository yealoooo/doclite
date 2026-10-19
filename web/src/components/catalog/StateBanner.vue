<script setup lang="ts">
// 已封存、修改中横条的外框（M10-5）：SealBanner（/catalog 左栏、/files）和条目详情的横条共用。左边图标，右边正文
// （默认插槽）和按钮（actions 插槽）。颜色按 lib/catalog.ts 的 ARCHIVE_STATE：sealed 浅紫、锁，modifying 浅青、铅笔。
// 标题写 `text-compact font-semibold text-sealed`（或 text-modifying）。data-testid 等属性落到外层的 div 上。
import { LockIcon, PencilLineIcon } from '@lucide/vue'

const props = defineProps<{ tone: 'sealed' | 'modifying' }>()
</script>

<template>
  <div
    class="grid shrink-0 grid-cols-[auto_minmax(0,1fr)] gap-x-2.5 gap-y-1.5 border-b px-3 py-2 text-xs leading-relaxed text-foreground"
    :class="props.tone === 'modifying' ? 'border-modifying/30 bg-modifying-soft/60' : 'bg-sealed-soft/50'"
  >
    <PencilLineIcon v-if="props.tone === 'modifying'" class="mt-0.5 size-4 shrink-0 text-modifying" />
    <LockIcon v-else class="mt-0.5 size-4 shrink-0 text-sealed" />
    <div class="min-w-0"><slot /></div>
    <div v-if="$slots.actions" class="col-start-2 flex flex-wrap items-center gap-1.5"><slot name="actions" /></div>
  </div>
</template>
