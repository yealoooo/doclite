<script setup lang="ts">
// 查看器的工具栏（M2-4）：适合窗口、100%、缩小、放大、当前比例、向左转、向右转。参考 plan.html p-review 的 .tools。
// 只发事件，不直接操作查看器：页面把事件接到 ImageViewer / CompareViewer 暴露的方法上（键盘也由页面绑定）。
// 两个插槽：start（最前面）、end（最后面，例如对比、下载原图）。
import { MinusIcon, PlusIcon, RotateCcwIcon, RotateCwIcon } from '@lucide/vue'

import type { ViewerView } from './ImageViewer.vue'

const props = defineProps<{
  view: ViewerView | null
  disabled?: boolean
}>()

const emit = defineEmits<{
  fit: []
  actual: []
  zoomIn: []
  zoomOut: []
  rotateLeft: []
  rotateRight: []
}>()

const isActual = () => props.view?.zoom != null && Math.abs(props.view.zoom - 1) < 0.005
</script>

<template>
  <div
    class="flex flex-wrap items-center gap-1 border-b bg-background px-2.5 py-1.5 text-xs text-foreground"
    data-testid="viewer-toolbar"
  >
    <slot name="start" />
    <button
      type="button"
      class="viewer-tool"
      :class="{ on: props.view?.fitted }"
      :disabled="props.disabled"
      data-testid="viewer-fit"
      title="适合窗口"
      @click="emit('fit')"
    >适合窗口</button>
    <button
      type="button"
      class="viewer-tool"
      :class="{ on: isActual() }"
      :disabled="props.disabled"
      data-testid="viewer-actual"
      title="100%：原图的一个像素对应屏幕上一个像素"
      @click="emit('actual')"
    >100%</button>
    <span class="mx-1 h-4 w-px bg-border" />
    <button type="button" class="viewer-tool" :disabled="props.disabled" data-testid="viewer-zoom-out" title="缩小" @click="emit('zoomOut')">
      <MinusIcon class="size-3.5" />
    </button>
    <span class="w-11 text-center font-mono text-2xs text-muted-foreground tabular-nums" data-testid="viewer-zoom">
      {{ props.view?.zoom == null ? '—' : `${Math.round(props.view.zoom * 100)}%` }}
    </span>
    <button type="button" class="viewer-tool" :disabled="props.disabled" data-testid="viewer-zoom-in" title="放大" @click="emit('zoomIn')">
      <PlusIcon class="size-3.5" />
    </button>
    <span class="mx-1 h-4 w-px bg-border" />
    <button
      type="button"
      class="viewer-tool"
      :disabled="props.disabled"
      data-testid="viewer-rotate-left"
      title="向左转（只转视图，不保存）"
      @click="emit('rotateLeft')"
    >
      <RotateCcwIcon class="size-3.5" />
    </button>
    <button
      type="button"
      class="viewer-tool"
      :disabled="props.disabled"
      data-testid="viewer-rotate-right"
      title="向右转（只转视图，不保存）"
      @click="emit('rotateRight')"
    >
      <RotateCwIcon class="size-3.5" />
    </button>
    <span v-if="props.view?.rotation" class="text-2xs text-muted-foreground" data-testid="viewer-rotation">
      已旋转 {{ props.view.rotation }}°（不保存）
    </span>
    <slot name="end" />
  </div>
</template>

<style>
.viewer-tool {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 26px;
  padding: 0 8px;
  border-radius: 5px;
  color: var(--sidebar-foreground);
  white-space: nowrap;
}
.viewer-tool:hover:not(:disabled) {
  background: var(--muted);
  color: var(--foreground);
}
.viewer-tool.on,
.viewer-tool.on:hover:not(:disabled) {
  background: var(--info-soft);
  color: var(--info);
  font-weight: 500;
}
.viewer-tool:disabled {
  opacity: 0.45;
}
.viewer-tool:focus-visible {
  outline: 2px solid var(--info);
  outline-offset: 1px;
}
</style>
