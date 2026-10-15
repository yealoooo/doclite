<script setup lang="ts">
// 导出任务列表的一行（M4-5）：范围、格式、件数和页数、状态和进度、操作（下载、明细、交付后变更 n、取消、重新导出）。
//
// - 状态：排队中（失败过的显示"等待重试"和原因）；生成中（进度条，progress_done/progress_total 页）；完成（大小、保留到
//   哪天；过期后"已过期"，可以按原来的范围重新导出）；失败（error_message，可能有几十行，可以展开）；已取消（清理中
//   显示"正在清理"）。
// - 行进入可视区时告诉页面（`visible`）：done 的任务这时才取"交付后变更 n"，条目范围的任务这时才取第一件的档号。
// - 截图脚本用的属性：data-testid=export-task（data-task-id、data-status、data-progress-done、data-progress-total、
//   data-cleaning-up、data-expired、data-retrying、data-changes、data-changes-state、data-scope）。
import {
  BanIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  DownloadIcon,
  FileDiffIcon,
  ListIcon,
  LoaderCircleIcon,
  RotateCcwIcon,
} from '@lucide/vue'
import { useIntersectionObserver } from '@vueuse/core'
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import { MAX_SCOPE_IDS } from '@/api/export'
import type { ExportTask } from '@/api/types'
import { formatBytes } from '@/lib/files'
import { formatDateTime } from '@/lib/format'
import {
  formatLabel,
  isCancelable,
  isExpired,
  isWaitingRetry,
  progressPercent,
  scopeTitle,
  taskStatusOf,
} from '@/lib/export'

import type { ChangesEntry } from './useExportTasks'

const props = defineProps<{
  task: ExportTask
  /** 现在的时间（毫秒，页面每 30 秒更新一次；判断过期用）。 */
  now: number
  /** 交付后变更的缓存（done 的任务）。 */
  changes: ChangesEntry | undefined
  /** 条目范围的第一件档号（取到了才有）。 */
  firstItem: string | null | undefined
  /** 补交的源任务的范围标题（列表里有它时）。 */
  resubmitSource: string | null
  downloading: boolean
  highlighted: boolean
}>()

const emit = defineEmits<{
  visible: [visible: boolean]
  download: []
  files: []
  changes: []
  cancel: []
  reexport: []
}>()

const el = ref<HTMLElement | null>(null)
const visible = ref(false)
const { stop } = useIntersectionObserver(el, ([entry]) => {
  visible.value = !!entry?.isIntersecting
})
watch(visible, (v) => emit('visible', v), { immediate: true })
onBeforeUnmount(() => {
  stop()
  if (visible.value) emit('visible', false)
})

const t = computed(() => props.task)
const status = computed(() => taskStatusOf(t.value.status))
const expired = computed(() => t.value.status === 'done' && isExpired(t.value, props.now))
const retrying = computed(() => isWaitingRetry(t.value))
const percent = computed(() => progressPercent(t.value))
const title = computed(() => scopeTitle(t.value, props.firstItem))

/** 错误信息的第一行（列表里只显示一行，可以展开看全部）。 */
const errorFirstLine = computed(() => (t.value.error_message ?? '').split('\n')[0] ?? '')
const errorMultiline = computed(() => (t.value.error_message ?? '').includes('\n') || (t.value.error_message ?? '').length > 80)
const errorOpen = ref(false)

const changedCount = computed(() => (props.changes?.data ? props.changes.data.changed_count : null))
/**
 * none：还没取；loading：取着（有旧值时照常显示旧值）；ready：最新；stale：项目里有了变化、可能已过时（列表行最多
 * 20 秒后重取）；error：最近一次取失败（有旧值时显示旧值并标出来）。
 */
const changesState = computed(() => {
  const c = props.changes
  if (!c) return 'none'
  if (c.state === 'loading') return 'loading'
  if (c.state === 'error') return 'error'
  return c.stale ? 'stale' : 'ready'
})

/** 可以按原来的范围重新导出：过期了的、失败的、取消的。 */
const canReexport = computed(() => expired.value || t.value.status === 'failed' || (t.value.status === 'canceled' && !t.value.cleaning_up))
/** 补交的条目超过手选范围的上限（5000 件）：不能按条目重新导出，要在原任务里重新补交（审查第 1 轮）。 */
const tooManyToReexport = computed(() => t.value.scope_ids.length > MAX_SCOPE_IDS)
</script>

<template>
  <div
    ref="el"
    class="border-b border-border/70 px-5 py-2.5 transition-colors"
    :class="props.highlighted ? 'bg-info-soft/60' : 'hover:bg-muted/40'"
    data-testid="export-task"
    :data-task-id="t.id"
    :data-status="t.status"
    :data-progress-done="t.progress_done"
    :data-progress-total="t.progress_total"
    :data-cleaning-up="t.cleaning_up ? 'true' : undefined"
    :data-expired="expired ? 'true' : undefined"
    :data-retrying="retrying ? 'true' : undefined"
    :data-changes="changedCount ?? undefined"
    :data-changes-state="t.status === 'done' ? changesState : undefined"
    :data-scope="title"
    :data-resubmit-of="t.resubmit_of ?? undefined"
  >
    <div class="grid grid-cols-[minmax(0,1fr)_64px_104px_minmax(150px,210px)] items-start gap-x-4">
      <!-- 范围 -->
      <div class="min-w-0">
        <div class="truncate text-[13px] font-medium text-foreground" :title="title" data-testid="export-task-scope">{{ title }}</div>
        <div class="mt-0.5 flex flex-wrap items-center gap-x-2 text-[11px] text-muted-foreground">
          <span
            v-if="t.resubmit_of"
            class="inline-block max-w-full truncate rounded bg-info-soft px-1 align-bottom leading-4 text-info"
            :title="`补交自${props.resubmitSource ? `「${props.resubmitSource}」` : '一个已完成的导出'}（任务 ${t.resubmit_of}）`"
          >
            补交自{{ props.resubmitSource ? `「${props.resubmitSource}」` : '一个已完成的导出' }}
          </span>
          <span v-else-if="t.scope_type === 'item'">按条目</span>
          <span v-if="t.only_approved">只导出审核通过的</span>
          <span v-if="t.skipped_item_count" class="text-warn" :title="`建任务时只导出了就绪的，跳过了 ${t.skipped_item_count} 件不就绪的`">跳过 {{ t.skipped_item_count }} 件</span>
          <span>{{ t.created_by_name ?? '—' }} · {{ formatDateTime(t.created_at) }}</span>
        </div>
      </div>

      <!-- 格式 -->
      <div class="pt-px text-xs text-foreground" data-testid="export-task-format">{{ formatLabel(t.export_format) }}</div>

      <!-- 件数、页数 -->
      <div class="pt-px text-xs text-foreground tabular-nums" data-testid="export-task-counts" :data-items="t.item_count" :data-pages="t.page_count">
        {{ t.item_count.toLocaleString() }} 件 · {{ t.page_count.toLocaleString() }} 页
      </div>

      <!-- 状态 -->
      <div class="min-w-0 text-xs" data-testid="export-task-status">
        <template v-if="t.status === 'running'">
          <div class="flex items-center gap-2">
            <div class="h-1.5 min-w-0 flex-1 overflow-hidden rounded-full bg-muted" role="progressbar" :aria-valuenow="percent" aria-valuemin="0" aria-valuemax="100">
              <div class="h-full rounded-full bg-info transition-[width] duration-500" :style="{ width: `${percent}%` }" />
            </div>
            <span class="shrink-0 text-muted-foreground tabular-nums">{{ percent }}%</span>
          </div>
          <div class="mt-0.5 text-[11px] text-muted-foreground tabular-nums">
            生成中 · {{ t.progress_done.toLocaleString() }} / {{ t.progress_total.toLocaleString() }} 页
            <span v-if="t.attempts > 1">· 第 {{ t.attempts }} 次执行</span>
          </div>
        </template>
        <template v-else-if="retrying">
          <span class="rounded-full bg-warn-soft px-2 leading-5 font-medium text-warn">等待重试</span>
          <span class="ml-1 text-[11px] text-muted-foreground">已执行 {{ t.attempts }} / {{ t.max_attempts }} 次</span>
        </template>
        <template v-else-if="t.status === 'canceled' && t.cleaning_up">
          <span class="inline-flex items-center gap-1 rounded-full bg-muted px-2 leading-5 font-medium text-muted-foreground" data-testid="export-task-cleaning">
            <LoaderCircleIcon class="size-3 animate-spin" />正在清理
          </span>
          <span class="ml-1 text-[11px] text-muted-foreground">已取消，正在删除已生成的部分</span>
        </template>
        <template v-else-if="expired">
          <span class="rounded-full bg-muted px-2 leading-5 font-medium text-muted-foreground" data-testid="export-task-expired">已过期</span>
          <span class="ml-1 text-[11px] text-muted-foreground">导出物已于 {{ formatDateTime(t.expires_at) }} 删除</span>
        </template>
        <template v-else>
          <span class="rounded-full px-2 leading-5 font-medium" :class="status.badge">{{ status.label }}</span>
          <span v-if="t.status === 'done'" class="ml-1 text-[11px] text-muted-foreground tabular-nums">
            {{ formatBytes(t.output_size) }} · 保留到 {{ formatDateTime(t.expires_at).slice(5) }}
          </span>
          <span v-else-if="t.status === 'queued'" class="ml-1 text-[11px] text-muted-foreground">等空闲的执行器（同时最多 2 个）</span>
          <span v-else-if="t.status === 'canceled'" class="ml-1 text-[11px] text-muted-foreground">{{ formatDateTime(t.finished_at) }}</span>
        </template>
      </div>
    </div>

    <!-- 失败、等待重试的原因（可能有几十行：问题页清单） -->
    <div
      v-if="(t.status === 'failed' || retrying) && t.error_message"
      class="mt-1.5 rounded-md px-2.5 py-1.5 text-[11px]"
      :class="t.status === 'failed' ? 'bg-destructive-soft text-destructive' : 'bg-warn-soft text-warn'"
      data-testid="export-task-error"
      :data-open="errorOpen ? 'true' : undefined"
    >
      <div class="flex items-start gap-1.5">
        <button
          v-if="errorMultiline"
          type="button"
          class="mt-px shrink-0 rounded hover:bg-background/50"
          :aria-label="errorOpen ? '收起' : '展开全部'"
          data-testid="export-task-error-toggle"
          @click="errorOpen = !errorOpen"
        >
          <ChevronDownIcon v-if="errorOpen" class="size-3.5" /><ChevronRightIcon v-else class="size-3.5" />
        </button>
        <div
          v-if="errorOpen"
          class="max-h-64 min-w-0 flex-1 overflow-auto font-mono leading-5 whitespace-pre-wrap select-text"
          data-testid="export-task-error-full"
        >{{ t.error_message }}</div>
        <div v-else class="min-w-0 flex-1 truncate" :title="t.error_message">{{ errorFirstLine }}</div>
      </div>
    </div>

    <!-- 操作 -->
    <div class="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs" data-testid="export-task-actions">
      <button
        v-if="t.status === 'done' && !expired"
        type="button"
        class="inline-flex items-center gap-1 font-medium text-info hover:underline disabled:opacity-50"
        :disabled="props.downloading"
        data-testid="task-download"
        @click="emit('download')"
      >
        <LoaderCircleIcon v-if="props.downloading" class="size-3.5 animate-spin" /><DownloadIcon v-else class="size-3.5" />下载
      </button>
      <button type="button" class="inline-flex items-center gap-1 text-foreground hover:underline" data-testid="task-files" @click="emit('files')">
        <ListIcon class="size-3.5" />明细
      </button>
      <template v-if="t.status === 'done'">
        <button
          v-if="changedCount !== null"
          type="button"
          class="inline-flex items-center gap-1 hover:underline"
          :class="changedCount > 0 ? 'font-medium text-warn' : 'text-muted-foreground'"
          :title="changedCount > 0 ? '导出之后有变化的条目' : '导出之后这些条目没有变化'"
          data-testid="task-changes"
          :data-count="changedCount"
          @click="emit('changes')"
        >
          <FileDiffIcon class="size-3.5" />交付后变更 {{ changedCount }}
          <LoaderCircleIcon v-if="changesState === 'loading'" class="size-3 animate-spin opacity-60" />
        </button>
        <button
          v-if="changedCount !== null && changesState === 'error'"
          type="button"
          class="-ml-2 text-[11px] text-destructive hover:underline"
          :title="`最近一次取交付后变更失败：${props.changes?.error ?? ''}`"
          data-testid="task-changes-error"
          @click="emit('changes')"
        >· 可能已过时，点开重试</button>
        <span
          v-else-if="changedCount !== null && changesState === 'stale'"
          class="-ml-2 text-[11px] text-muted-foreground"
          title="项目里有了新的审核、扫描或归类变化，这个数字可能已过时，最迟约 20 秒后更新；点开看最新的"
          data-testid="task-changes-stale"
        >· 可能已过时</span>
        <span v-else-if="changesState === 'loading' || changesState === 'none'" class="inline-flex items-center gap-1 text-muted-foreground" data-testid="task-changes-loading">
          <LoaderCircleIcon class="size-3 animate-spin" />交付后变更…
        </span>
        <button
          v-else-if="changesState === 'error'"
          type="button"
          class="text-destructive hover:underline"
          :title="props.changes?.error"
          data-testid="task-changes-error"
          @click="emit('changes')"
        >交付后变更（取不到，点开重试）</button>
      </template>
      <button
        v-if="isCancelable(t)"
        type="button"
        class="inline-flex items-center gap-1 text-muted-foreground hover:text-destructive hover:underline"
        data-testid="task-cancel"
        @click="emit('cancel')"
      >
        <BanIcon class="size-3.5" />取消
      </button>
      <button
        v-if="canReexport"
        type="button"
        class="inline-flex items-center gap-1 text-foreground hover:underline disabled:cursor-not-allowed disabled:text-muted-foreground disabled:no-underline"
        :disabled="tooManyToReexport"
        :title="
          tooManyToReexport
            ? `这次补交有 ${t.scope_ids.length.toLocaleString()} 件，超过手选范围的上限 ${MAX_SCOPE_IDS.toLocaleString()} 件；请在原任务的交付后变更里重新补交`
            : t.resubmit_of
              ? '按这次补交的条目重新导出'
              : '按原来的范围重新导出（重新预检，用现在的最新版本）'
        "
        data-testid="task-reexport"
        :data-disabled="tooManyToReexport ? 'true' : undefined"
        @click="emit('reexport')"
      >
        <RotateCcwIcon class="size-3.5" />按原来的范围重新导出
      </button>
    </div>
  </div>
</template>
