<script setup lang="ts">
// 浏览器上传的结果列表（M1-12）：浮在文件目录主区的右下角，上传在后台进行，可以接着浏览。
// 每个任务一组：已入库、重名改名、内容重复已跳过、之前已上传过、DPI 提示、无封面、等待重试、失败、未传（前一页失败），
// 失败和未传的可以按原顺序重试。明细虚拟化（UploadJobItems），计数由 store 增量维护（job.counts），文件多时不卡。
// 状态来自 stores/uploads.ts（离开文件目录页再回来还在；刷新页面就没了）。
import { CheckIcon, ChevronDownIcon, ChevronUpIcon, CircleAlertIcon, LoaderCircleIcon, MinusIcon, RotateCwIcon, SquareIcon, XIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import { type UploadJob, useUploadsStore } from '@/stores/uploads'

import UploadJobItems from './UploadJobItems.vue'

const props = defineProps<{ projectId: string }>()

const uploads = useUploadsStore()
const jobs = computed(() => uploads.jobs.filter((j) => j.target.projectId === props.projectId))
const minimized = ref(false)
/** 展开明细的任务（默认最新的那个）。 */
const expanded = ref<Set<number>>(new Set())

watch(
  () => jobs.value[0]?.id,
  (id) => {
    if (id === undefined) return
    expanded.value = new Set([id])
    minimized.value = false
  },
  { immediate: true },
)

function toggle(job: UploadJob) {
  const next = new Set(expanded.value)
  if (next.has(job.id)) next.delete(job.id)
  else next.add(job.id)
  expanded.value = next
}

const active = computed(() => jobs.value.filter((j) => j.state === 'running' || j.state === 'queued'))
const summary = computed(() => {
  let total = 0
  let finished = 0
  for (const j of active.value) {
    total += j.counts.total
    finished += j.counts.total - j.counts.active
  }
  return { total, finished }
})

function jobTitle(job: UploadJob): string {
  return `${job.kind === 'folder' ? '上传文件夹' : '上传文件'}到「${job.target.name}」`
}
</script>

<template>
  <div
    v-if="jobs.length"
    class="pointer-events-auto absolute right-3 bottom-3 z-20 flex max-h-[min(70%,560px)] w-[460px] flex-col overflow-hidden rounded-xl border bg-popover text-[13px] shadow-lg"
    data-testid="upload-panel"
    :data-active="active.length"
  >
    <header class="flex h-10 shrink-0 items-center gap-2 border-b px-3">
      <LoaderCircleIcon v-if="active.length" class="size-4 animate-spin text-info" />
      <CheckIcon v-else class="size-4 text-ok" />
      <span class="font-medium text-foreground">
        <template v-if="active.length">正在上传 {{ summary.finished }} / {{ summary.total }}</template>
        <template v-else>上传完成</template>
      </span>
      <div class="flex-1" />
      <button
        v-if="!active.length"
        type="button"
        class="rounded px-1.5 py-0.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
        data-testid="upload-clear"
        @click="uploads.clearFinished()"
      >
        清除记录
      </button>
      <button
        type="button"
        class="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
        :title="minimized ? '展开' : '收起'"
        @click="minimized = !minimized"
      >
        <ChevronUpIcon v-if="minimized" class="size-4" />
        <MinusIcon v-else class="size-4" />
      </button>
    </header>

    <div v-if="!minimized" class="min-h-0 flex-1 overflow-y-auto">
      <section
        v-for="job in jobs"
        :key="job.id"
        class="border-b last:border-b-0"
        data-testid="upload-job"
        :data-job-id="job.id"
        :data-state="job.state"
      >
        <div class="flex items-start gap-2 px-3 pt-2.5 pb-2">
          <button type="button" class="mt-0.5 rounded p-0.5 text-muted-foreground hover:bg-muted" @click="toggle(job)">
            <ChevronDownIcon class="size-3.5 transition-transform" :class="expanded.has(job.id) ? '' : '-rotate-90'" />
          </button>
          <div class="min-w-0 flex-1">
            <div class="truncate font-medium text-foreground" :title="jobTitle(job)">{{ jobTitle(job) }}</div>
            <div class="mt-0.5 flex flex-wrap gap-x-2 gap-y-0.5 text-xs text-muted-foreground" data-testid="upload-job-counts">
              <span>共 {{ job.counts.total }} 个</span>
              <span v-if="job.counts.done" class="text-ok">已入库 {{ job.counts.done }}</span>
              <span v-if="job.counts.renamed" class="text-warn">改名 {{ job.counts.renamed }}</span>
              <span v-if="job.counts.already" class="text-info">之前已上传 {{ job.counts.already }}</span>
              <span v-if="job.counts.duplicate">重复跳过 {{ job.counts.duplicate }}</span>
              <span v-if="job.counts.lowDpi" class="text-warn">低 DPI {{ job.counts.lowDpi }}</span>
              <span v-if="job.counts.noCover">无封面 {{ job.counts.noCover }}</span>
              <span v-if="job.counts.failed" class="text-destructive">失败 {{ job.counts.failed }}</span>
              <span v-if="job.counts.held" class="text-warn">未传 {{ job.counts.held }}</span>
              <span v-if="job.counts.skipped">忽略 {{ job.counts.skipped }}</span>
              <span v-if="job.counts.cancelled">取消 {{ job.counts.cancelled }}</span>
              <span v-if="job.kind === 'folder' && job.folders">子文件夹 {{ job.folders }} 个</span>
            </div>
          </div>
          <div class="flex shrink-0 items-center gap-1">
            <button
              v-if="job.state === 'running' || job.state === 'queued'"
              type="button"
              class="flex items-center gap-1 rounded px-1.5 py-0.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
              data-testid="upload-cancel"
              @click="uploads.cancel(job)"
            >
              <SquareIcon class="size-3" />停止
            </button>
            <template v-else>
              <button
                v-if="job.counts.failed || job.counts.held || job.counts.cancelled"
                type="button"
                class="flex items-center gap-1 rounded px-1.5 py-0.5 text-xs text-info hover:bg-info-soft"
                data-testid="upload-retry"
                @click="uploads.retryFailed(job)"
              >
                <RotateCwIcon class="size-3" />重试失败的
              </button>
              <button
                type="button"
                class="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
                title="从列表里去掉"
                @click="uploads.dismiss(job)"
              >
                <XIcon class="size-3.5" />
              </button>
            </template>
          </div>
        </div>
        <div
          v-if="job.state === 'running' || job.state === 'queued'"
          class="mx-3 mb-2 h-1 overflow-hidden rounded-full bg-muted"
        >
          <div
            class="h-full rounded-full bg-info transition-[width]"
            :style="{ width: `${(100 * (job.counts.total - job.counts.active)) / Math.max(1, job.counts.total)}%` }"
          />
        </div>
        <ul v-if="job.folderErrors.length" class="mx-3 mb-2 flex flex-col gap-1">
          <li
            v-for="fe in job.folderErrors"
            :key="fe.path"
            class="flex gap-1.5 rounded-md bg-destructive-soft px-2 py-1 text-xs text-destructive"
            data-testid="upload-folder-error"
          >
            <CircleAlertIcon class="mt-px size-3.5 shrink-0" />
            <span class="[overflow-wrap:anywhere]">建子文件夹「{{ fe.path }}」失败：{{ fe.message }}</span>
          </li>
        </ul>
        <p v-if="job.ignored && expanded.has(job.id)" class="mx-3 mb-1.5 text-xs text-muted-foreground">
          另有 {{ job.ignored }} 个系统文件或隐藏文件没有上传（Thumbs.db、.DS_Store、以 . 开头的……）。
        </p>
        <UploadJobItems v-if="expanded.has(job.id)" :job="job" class="pb-1.5" />
      </section>
    </div>
  </div>
</template>
