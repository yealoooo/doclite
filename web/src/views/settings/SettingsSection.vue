<script setup lang="ts">
// 设置页的一组（M7-11）：标题行（图标、标题、说明、右侧操作）+ 内容。
import type { Component } from 'vue'

const props = defineProps<{
  title: string
  icon: Component
  description?: string
}>()
</script>

<template>
  <article class="rounded-lg shadow-outline">
    <header class="flex items-center gap-3 border-b px-4 py-3">
      <component :is="props.icon" class="size-4.5 shrink-0 text-muted-foreground" />
      <div class="min-w-0 flex-1">
        <h2 class="text-sm font-semibold text-foreground">{{ props.title }}</h2>
        <p v-if="props.description" class="mt-0.5 text-xs text-muted-foreground">{{ props.description }}</p>
      </div>
      <slot name="actions" />
    </header>
    <div class="px-4 py-3.5">
      <slot />
    </div>
  </article>
</template>
