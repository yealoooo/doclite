<script setup lang="ts">
// 连接测试的结果：结论、说明、耗时、排查摘要，以及 public_endpoint（工作站地址）的检查。
// 测试进行中显示加载状态。最长约 30 秒：后端测试连接每一步都有 10 秒超时（连接 5 秒），依次是
// 访问存储（List）、用故意签错的请求校验签名、检查工作站地址（public_endpoint，和 server 地址相同时跳过）。
import { CircleCheckIcon, CircleXIcon, LoaderCircleIcon, TriangleAlertIcon } from '@lucide/vue'
import { computed } from 'vue'

import type { ConnectionTestResult } from '@/api/types'
import { PUBLIC_RESULT_LABEL, RESULT_LABEL } from '@/lib/storage'

const props = defineProps<{
  result: ConnectionTestResult | null
  testing: boolean
}>()

const publicCheck = computed(() => props.result?.public_endpoint_check ?? null)
</script>

<template>
  <div
    v-if="testing"
    class="flex items-center gap-2 rounded-md bg-muted px-3 py-2.5 text-sm text-muted-foreground"
    data-testid="storage-testing"
  >
    <LoaderCircleIcon class="size-4 animate-spin" />
    正在测试连接…（最长约 30 秒）
  </div>
  <div
    v-else-if="result"
    class="flex flex-col gap-1.5 rounded-md px-3 py-2.5 text-sm"
    :class="result.ok ? 'bg-ok-soft' : 'bg-destructive-soft'"
    data-testid="storage-test-result"
    :data-result="result.result"
  >
    <div class="flex items-center gap-2 font-medium" :class="result.ok ? 'text-ok' : 'text-destructive'">
      <CircleCheckIcon v-if="result.ok" class="size-4" />
      <CircleXIcon v-else class="size-4" />
      {{ RESULT_LABEL[result.result] }}
      <span class="ml-auto text-xs font-normal text-muted-foreground tabular-nums">{{ result.elapsed_ms }} ms</span>
    </div>
    <p class="text-foreground">{{ result.message }}</p>
    <p v-if="result.detail" class="font-mono text-xs break-all text-muted-foreground">{{ result.detail }}</p>

    <div
      v-if="publicCheck"
      class="mt-1 flex flex-col gap-1 border-t border-black/10 pt-2"
      data-testid="storage-public-check"
      :data-result="publicCheck.result"
    >
      <div
        class="flex items-center gap-2 font-medium"
        :class="publicCheck.result === 'ok' ? 'text-ok' : 'text-warn'"
      >
        <CircleCheckIcon v-if="publicCheck.result === 'ok'" class="size-4" />
        <TriangleAlertIcon v-else class="size-4" />
        {{ PUBLIC_RESULT_LABEL[publicCheck.result] }}
        <span class="ml-auto text-xs font-normal text-muted-foreground tabular-nums">
          <template v-if="publicCheck.http_status">HTTP {{ publicCheck.http_status }} · </template>{{ publicCheck.elapsed_ms }} ms
        </span>
      </div>
      <p class="text-foreground">{{ publicCheck.message }}</p>
    </div>
  </div>
</template>
