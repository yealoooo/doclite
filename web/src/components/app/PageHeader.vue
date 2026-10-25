<script setup lang="ts">
// 页面顶栏：标题（默认取当前路由对应菜单的 menu_name）、标题旁的说明（meta）、右侧操作按钮（actions）。
// dense：矮一点、边距小一点（归类页三栏，要给下面多留地方）。
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import { useSessionStore } from '@/stores/session'

const props = defineProps<{ title?: string; dense?: boolean }>()

const route = useRoute()
const session = useSessionStore()
const text = computed(() => props.title ?? session.menuForPath(route.path)?.menu_name ?? '')
</script>

<template>
  <header class="flex shrink-0 items-center gap-3 border-b" :class="props.dense ? 'h-12 px-4' : 'h-14 px-6'">
    <h1 class="shrink-0 text-base font-semibold tracking-tight text-foreground">{{ text }}</h1>
    <slot name="meta" />
    <div class="flex-1" />
    <slot name="actions" />
  </header>
</template>
