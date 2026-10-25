<script setup lang="ts">
// 整块区域加载失败：居中的错误信息和"重试"按钮（列表底部的"加载更多失败"仍用文字链接）。
// 外层布局（flex-1、高度）由用的地方给 class。
import { Button } from '@/components/ui/button'

defineProps<{ message?: string | null }>()
const emit = defineEmits<{ retry: [] }>()
</script>

<template>
  <div class="flex flex-col items-center justify-center gap-3 p-4 text-center text-sm" data-testid="error-state">
    <p class="text-destructive">{{ message || '加载失败' }}</p>
    <Button variant="outline" size="sm" @click="emit('retry')">重试</Button>
  </div>
</template>
