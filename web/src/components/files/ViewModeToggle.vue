<script setup lang="ts">
// 页头的"图标 / 列表"切换（/files、/review-assign），配合 entries.ts 的 useViewMode。
import { LayoutGridIcon, ListIcon } from '@lucide/vue'

import type { ViewMode } from './entries'

const view = defineModel<ViewMode>({ required: true })
</script>

<template>
  <div class="flex shrink-0 rounded-md bg-muted p-0.5" role="radiogroup" aria-label="视图">
    <button
      type="button"
      role="radio"
      :aria-checked="view === 'icons'"
      class="flex items-center gap-1 rounded px-2 py-1 text-xs"
      :class="view === 'icons' ? 'bg-background font-medium text-foreground shadow-sm' : 'text-muted-foreground'"
      data-testid="view-icons"
      @click="view = 'icons'"
    >
      <LayoutGridIcon class="size-3.5" />图标
    </button>
    <button
      type="button"
      role="radio"
      :aria-checked="view === 'list'"
      class="flex items-center gap-1 rounded px-2 py-1 text-xs"
      :class="view === 'list' ? 'bg-background font-medium text-foreground shadow-sm' : 'text-muted-foreground'"
      data-testid="view-list"
      @click="view = 'list'"
    >
      <ListIcon class="size-3.5" />列表
    </button>
  </div>
</template>
