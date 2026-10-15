<script setup lang="ts">
// 最近文件列表（M1-13，参考 p-upload Mockup 的队列表）：文件、大小、进度、状态。
// - 低 DPI（server 的 warnings 里有 low_dpi）给提示；没带封面的标"无封面"（悬停看原因）；
// - 内容重复的写出已有文件的位置；server 自动改了名的写出入库后的名字；
// - 失败项可以单独重试（父组件调 `upload_retry_failed`）。
import {
  CircleAlertIcon,
  CircleCheckIcon,
  ClockIcon,
  CopyIcon,
  LoaderCircleIcon,
  RotateCcwIcon,
} from '@lucide/vue'

import { Button } from '@/components/ui/button'
import { formatBytes } from '@/lib/files'
import type { ProgressEvent, QueueItem } from '@/lib/scan-queue'

import {
  coverNote,
  duplicateOf,
  errorText,
  isLowDpi,
  isRetrying,
  localLabel,
  missingCover,
  progressPercent,
  statusText,
} from './status'

const props = defineProps<{
  rows: QueueItem[]
  progressOf: (id: number) => ProgressEvent | null
  now: number
  /** 正在重试的 id。 */
  retrying: Set<number>
}>()
const emit = defineEmits<{ retry: [item: QueueItem] }>()

function barClass(item: QueueItem): string {
  switch (item.status) {
    case 'done':
      return 'bg-ok'
    case 'duplicate':
      return 'bg-muted-foreground/40'
    case 'failed':
      return 'bg-destructive'
    default:
      return 'bg-info'
  }
}

function statusClass(item: QueueItem): string {
  switch (item.status) {
    case 'done':
      return 'text-ok'
    case 'failed':
      return 'text-destructive'
    case 'queued':
      return isRetrying(item) ? 'text-warn' : 'text-muted-foreground'
    case 'waiting':
      return item.error_code === 'file_locked' ? 'text-warn' : 'text-muted-foreground'
    case 'uploading':
      return 'text-info'
    default:
      return 'text-muted-foreground'
  }
}
</script>

<template>
  <div class="text-sm" data-testid="queue-list">
    <div
      class="grid grid-cols-[20px_minmax(0,1fr)_72px_120px_minmax(0,300px)] items-center gap-3 border-b bg-muted/60 px-4 py-2 text-xs text-muted-foreground"
    >
      <span />
      <span>文件</span>
      <span class="text-right">大小</span>
      <span>进度</span>
      <span>状态</span>
    </div>
    <div
      v-for="item in props.rows"
      :key="item.id"
      class="grid grid-cols-[20px_minmax(0,1fr)_72px_120px_minmax(0,300px)] items-center gap-3 border-b px-4 py-2 last:border-b-0"
      data-testid="queue-row"
      :data-id="item.id"
      :data-status="item.status"
    >
      <!-- 状态图标 -->
      <span class="flex items-center justify-center">
        <LoaderCircleIcon v-if="item.status === 'uploading'" class="size-4 animate-spin text-info" />
        <CircleCheckIcon v-else-if="item.status === 'done'" class="size-4 text-ok" />
        <CopyIcon v-else-if="item.status === 'duplicate'" class="size-3.5 text-muted-foreground" />
        <CircleAlertIcon v-else-if="item.status === 'failed'" class="size-4 text-destructive" />
        <ClockIcon v-else class="size-3.5" :class="isRetrying(item) ? 'text-warn' : 'text-muted-foreground'" />
      </span>

      <!-- 文件 -->
      <div class="min-w-0">
        <div class="flex min-w-0 items-center gap-1.5">
          <span v-if="localLabel(item).dir" class="max-w-[45%] shrink truncate text-muted-foreground" :title="item.rel_dir">
            {{ localLabel(item).dir }} /
          </span>
          <span class="truncate font-medium text-foreground" :title="item.local_path">{{ item.file_name }}</span>
          <span
            v-if="isLowDpi(item)"
            class="shrink-0 rounded bg-warn-soft px-1.5 py-px text-[11px] font-medium text-warn"
            title="扫描分辨率低于 200 DPI，可能需要重扫"
            data-testid="badge-low-dpi"
          >DPI 低于 200</span>
          <span
            v-else-if="item.warnings.includes('dpi_unknown')"
            class="shrink-0 rounded bg-muted px-1.5 py-px text-[11px] text-muted-foreground"
            title="文件里没有写分辨率"
          >DPI 未知</span>
          <span
            v-if="missingCover(item)"
            class="shrink-0 rounded bg-muted px-1.5 py-px text-[11px] font-medium text-muted-foreground"
            :title="coverNote(item)"
            data-testid="badge-no-cover"
          >无封面</span>
          <span
            v-if="item.source === 'manual'"
            class="shrink-0 rounded bg-info-soft px-1.5 py-px text-[11px] text-info"
            title="从文件目录的上传按钮加入的"
          >上传按钮</span>
        </div>
        <div
          v-if="item.status === 'duplicate' && duplicateOf(item)"
          class="mt-0.5 truncate text-xs text-muted-foreground"
          :title="duplicateOf(item)!"
          data-testid="duplicate-of"
        >
          与已有的 {{ duplicateOf(item) }} 内容相同
        </div>
        <div
          v-else-if="item.status === 'done' && item.server_file_name && item.server_file_name !== item.file_name"
          class="mt-0.5 truncate text-xs text-muted-foreground"
        >
          同名文件已存在，入库为 {{ item.server_file_name }}
        </div>
        <div
          v-else-if="isRetrying(item) && item.error_message"
          class="mt-0.5 truncate text-xs text-warn"
          :title="item.error_message"
        >
          {{ errorText(item) }}
        </div>
      </div>

      <!-- 大小 -->
      <span class="text-right text-muted-foreground tabular-nums">{{ formatBytes(item.size) }}</span>

      <!-- 进度 -->
      <div class="h-1.5 overflow-hidden rounded-full bg-muted" :title="`${progressPercent(item, props.progressOf(item.id))}%`">
        <div
          class="h-full rounded-full transition-[width] duration-200"
          :class="[
            barClass(item),
            item.status === 'uploading' && !props.progressOf(item.id) ? 'w-1/4 animate-pulse' : '',
          ]"
          :style="
            item.status === 'uploading' && !props.progressOf(item.id)
              ? undefined
              : { width: `${progressPercent(item, props.progressOf(item.id))}%` }
          "
          data-testid="queue-bar"
        />
      </div>

      <!-- 状态 -->
      <div class="flex min-w-0 items-center gap-2">
        <span
          class="min-w-0 truncate text-xs"
          :class="statusClass(item)"
          :title="item.status === 'failed' ? `${errorText(item)}${item.error_code ? `（${item.error_code}）` : ''}` : undefined"
          data-testid="queue-status"
        >
          <template v-if="item.status === 'done'">✓ </template>{{ statusText(item, props.progressOf(item.id), props.now) }}
        </span>
        <Button
          v-if="item.status === 'failed'"
          variant="outline"
          size="xs"
          class="shrink-0"
          :disabled="props.retrying.has(item.id)"
          data-testid="queue-retry"
          @click="emit('retry', item)"
        >
          <RotateCcwIcon />重试
        </Button>
      </div>
    </div>
  </div>
</template>
