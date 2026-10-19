<script setup lang="ts">
// 页头的实时连接状态（useProjectStream 的 state）：绿点"实时"、红点"已断开"、闪烁的黄点"连接中…"。
import type { StreamState } from '@/composables/useProjectStream'

defineProps<{ state: StreamState }>()
</script>

<template>
  <span
    class="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground"
    :title="state === 'open' ? '已连接实时更新' : '实时更新已断开，正在重连'"
  >
    <span
      class="size-1.5 rounded-full"
      :class="state === 'open' ? 'bg-ok' : state === 'stopped' ? 'bg-destructive' : 'animate-pulse bg-warn'"
    />
    {{ state === 'open' ? '实时' : state === 'stopped' ? '已断开' : '连接中…' }}
  </span>
</template>
