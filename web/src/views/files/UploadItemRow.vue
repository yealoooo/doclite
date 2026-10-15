<script setup lang="ts">
// 上传结果列表的一行（M1-12）。单独成组件：一个文件的状态变了只重渲染这一行。
import { CheckIcon, CircleAlertIcon, CopyIcon, LoaderCircleIcon, PauseIcon } from '@lucide/vue'
import { computed } from 'vue'

import { formatBytes, isLowDpi } from '@/lib/files'
import type { UploadItem } from '@/stores/uploads'

const props = defineProps<{ item: UploadItem }>()

const STATE_LABEL: Record<UploadItem['state'], string> = {
  waiting: '等待',
  cover: '生成封面…',
  uploading: '上传中…',
  retrying: '等待重试',
  done: '已入库',
  already: '之前已上传过',
  duplicate: '内容重复，已跳过',
  failed: '失败',
  held: '未传',
  skipped: '已忽略',
  cancelled: '已取消',
}

const stateClass = computed(() => {
  switch (props.item.state) {
    case 'done':
      return 'text-ok'
    case 'already':
      return 'text-info'
    case 'retrying':
    case 'held':
      return 'text-warn'
    case 'duplicate':
    case 'skipped':
    case 'cancelled':
      return 'text-muted-foreground'
    case 'failed':
      return 'text-destructive'
    default:
      return 'text-foreground'
  }
})

const path = computed(() => (props.item.dir.length ? `${props.item.dir.join('/')}/` : ''))

type Tone = 'warn' | 'muted' | 'bad' | 'info'

/** 名字下面的说明（可能有几条）。 */
const notes = computed(() => {
  const it = props.item
  const out: { text: string; tone: Tone }[] = []
  const r = it.result
  if (it.state === 'retrying' && it.retry) out.push({ text: it.retry, tone: 'warn' })
  if (it.state === 'done' && r?.renamed) out.push({ text: `重名，已改为「${r.file_name}」`, tone: 'warn' })
  if (it.state === 'already' && r) {
    out.push({ text: r.renamed ? `这个文件夹里已有，现名「${r.file_name}」` : '这个文件夹里已有，没有重复入库', tone: 'info' })
  }
  if (it.state === 'duplicate') {
    const d = it.duplicate
    out.push({ text: d ? `项目里已有相同内容：/${d.folder_path}/${d.file_name}` : '项目里已有相同内容的文件', tone: 'muted' })
  }
  if (r && (it.state === 'done' || it.state === 'already')) {
    if (isLowDpi(r.dpi)) out.push({ text: `${r.dpi} dpi，低于 200`, tone: 'warn' })
    else if (r.dpi == null) out.push({ text: 'DPI 未知（文件里没写分辨率）', tone: 'muted' })
    if (!r.has_cover) out.push({ text: `无封面${it.coverReason ? `：${it.coverReason}` : ''}`, tone: 'muted' })
  } else if (it.state === 'failed' || it.state === 'skipped' || it.state === 'held') {
    if (it.error) out.push({ text: it.error, tone: it.state === 'failed' ? 'bad' : it.state === 'held' ? 'warn' : 'muted' })
  }
  if (it.cover === 'rejected' && it.state !== 'failed' && r?.has_cover !== false) {
    out.push({ text: it.coverReason ?? '服务器没收封面', tone: 'muted' })
  }
  return out
})

const TONE: Record<Tone, string> = {
  warn: 'bg-warn-soft text-warn',
  muted: 'bg-muted text-muted-foreground',
  bad: 'bg-destructive-soft text-destructive',
  info: 'bg-info-soft text-info',
}
</script>

<template>
  <div
    class="px-3 py-1.5 hover:bg-muted/40"
    data-testid="upload-item"
    :data-name="props.item.name"
    :data-path="path + props.item.name"
    :data-state="props.item.state"
    :data-cover="props.item.cover"
    :data-cover-ms="props.item.coverMs ?? ''"
    :data-file-id="props.item.result?.file_id ?? ''"
    :data-has-cover="props.item.result ? String(props.item.result.has_cover) : ''"
    :data-warnings="props.item.result?.warnings.join(',') ?? ''"
    :data-error-code="props.item.errorCode ?? ''"
  >
    <div class="flex items-center gap-2">
      <span class="flex size-4 shrink-0 items-center justify-center">
        <LoaderCircleIcon
          v-if="props.item.state === 'cover' || props.item.state === 'uploading'"
          class="size-3.5 animate-spin text-info"
        />
        <PauseIcon v-else-if="props.item.state === 'retrying'" class="size-3.5 text-warn" />
        <CheckIcon v-else-if="props.item.state === 'done'" class="size-3.5 text-ok" />
        <CopyIcon
          v-else-if="props.item.state === 'duplicate' || props.item.state === 'already'"
          class="size-3.5 text-muted-foreground"
        />
        <CircleAlertIcon v-else-if="props.item.state === 'failed'" class="size-3.5 text-destructive" />
        <span v-else class="size-1.5 rounded-full bg-muted-foreground/40" />
      </span>
      <span class="min-w-0 flex-1 truncate" :title="path + props.item.name">
        <span class="text-muted-foreground">{{ path }}</span><span class="text-foreground">{{ props.item.name }}</span>
      </span>
      <span class="shrink-0 text-xs text-muted-foreground">{{ formatBytes(props.item.file.size) }}</span>
      <span class="w-[8.5em] shrink-0 text-right text-xs whitespace-nowrap" :class="stateClass">{{ STATE_LABEL[props.item.state] }}</span>
    </div>
    <div v-if="notes.length" class="mt-1 ml-6 flex flex-wrap gap-1">
      <span
        v-for="(n, i) in notes"
        :key="i"
        class="rounded px-1.5 text-[11px] leading-5 [overflow-wrap:anywhere]"
        :class="TONE[n.tone]"
      >{{ n.text }}</span>
    </div>
  </div>
</template>
