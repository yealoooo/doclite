<script setup lang="ts">
// 一个文件的操作日志（M2-5）：`GET /api/files/{fid}/logs`（菜单码 files），新的在前；每条显示动作、操作人、时间
// 和 `logSummary()` 的说明（含审核动作：分配、改派、通过、驳回、撤回通过，以及重扫的自动再分配）。
// - 换文件后去抖 150 ms 再取（方向键、Shift 选择时不连发）；`refreshToken` 变了重新取（SSE 说这个文件变了），
//   刷新期间先保留旧内容；换文件时马上清空，在途的请求作废。
// - 审核分配页的右栏用它；文件目录的详情面板（FileDetailPanel）有自己的一份（和快照一起取），说明文字共用
//   `lib/files.ts` 的 `logSummary`。
import { LoaderCircleIcon } from '@lucide/vue'
import { onBeforeUnmount, ref, watch } from 'vue'

import { getFileLogs } from '@/api/files'
import { errorMessage } from '@/api/http'
import type { FileLog } from '@/api/types'
import { logActionLabel, logSummary } from '@/lib/files'
import { formatDateTime } from '@/lib/format'

const props = withDefaults(
  defineProps<{
    fileId: string | null
    /** 变了就重新取。 */
    refreshToken?: number
    title?: string
  }>(),
  { refreshToken: 0, title: '操作日志' },
)

const logs = ref<FileLog[] | null>(null)
const loading = ref(false)
const loadError = ref<string | null>(null)
const loadedFor = ref<string | null>(null)
let timer: ReturnType<typeof setTimeout> | null = null
let seq = 0

async function load(fileId: string) {
  const my = ++seq
  loading.value = true
  loadError.value = null
  try {
    const res = await getFileLogs(fileId, true)
    if (my !== seq || props.fileId !== fileId) return
    logs.value = res.logs
    loadedFor.value = fileId
  } catch (e) {
    if (my !== seq || props.fileId !== fileId) return
    logs.value = null
    loadedFor.value = null
    loadError.value = errorMessage(e)
  } finally {
    if (my === seq) loading.value = false
  }
}

watch(
  () => [props.fileId, props.refreshToken] as const,
  ([fileId], old) => {
    if (timer) clearTimeout(timer)
    if (!fileId || old?.[0] !== fileId) {
      seq++
      logs.value = null
      loadedFor.value = null
      loadError.value = null
      loading.value = false
    }
    if (!fileId) return
    loading.value = true
    timer = setTimeout(() => void load(fileId), 150)
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
  seq++
})
</script>

<template>
  <section data-testid="file-log-panel" :data-state="loading ? 'loading' : loadError ? 'error' : logs ? 'ready' : 'idle'">
    <h3 class="mb-2 flex items-center gap-2 text-xs font-medium text-muted-foreground">
      {{ props.title }}
      <LoaderCircleIcon v-if="loading" class="size-3 animate-spin" />
    </h3>
    <p v-if="loadError" class="text-xs text-destructive">{{ loadError }}</p>
    <ol v-else-if="logs && loadedFor === props.fileId" class="flex flex-col gap-2.5" data-testid="file-logs">
      <li
        v-for="log in logs"
        :key="log.id"
        class="border-l-2 border-border pl-2.5"
        data-testid="file-log"
        :data-action="log.action"
      >
        <div class="flex items-baseline gap-1.5">
          <span class="font-medium text-foreground">{{ logActionLabel(log.action) }}</span>
          <span class="truncate text-xs text-muted-foreground">{{ log.operator_name }}</span>
          <span class="ml-auto shrink-0 text-[11px] text-muted-foreground">{{ formatDateTime(log.created_at) }}</span>
        </div>
        <div v-if="logSummary(log)" class="mt-0.5 text-xs [overflow-wrap:anywhere] text-muted-foreground" data-testid="file-log-summary">
          {{ logSummary(log) }}
        </div>
      </li>
      <li v-if="logs.length === 0" class="text-xs text-muted-foreground">没有日志</li>
    </ol>
  </section>
</template>
