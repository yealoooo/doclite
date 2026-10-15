<script setup lang="ts">
// 交付后变更（M4-5，用户决定 Q6）：`GET /api/export-tasks/{id}/changes`，按件列出导出之后的变化和原因（有页出了新版本、
// 移出、移到别的条目、页序变了、新加了页、现在不是通过、著录变了、条目已删除）。数据来自页面的缓存（useExportTasks 的
// changes）：对话框开着时这个任务算"要用的"，项目里有 files.changed、catalog.changed 时会自动重取。
// "只补交有变更的 n 件"：交给页面用 `resubmit_of` 新建导出（照常预检；不把 id 列表放进 scope_ids）。
//
// 截图脚本用的属性：data-testid=task-changes-dialog（data-task-id、data-state、data-changed-count、data-resubmit-count）；
// 每件 task-change-item（data-item-id、data-reasons、data-deleted）；按钮 task-changes-resubmit、task-changes-refresh。
import { LoaderCircleIcon, RefreshCwIcon, SendIcon } from '@lucide/vue'
import { computed } from 'vue'

import type { ExportChangedItem, ExportTask } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { itemStatusOf } from '@/lib/catalog'
import { formatDateTime } from '@/lib/format'
import { CHANGE_LABEL, CHANGE_ORDER, itemName } from '@/lib/export'

import type { ChangesEntry } from './useExportTasks'

const props = defineProps<{
  task: ExportTask | null
  scopeTitle: string
  entry: ChangesEntry | undefined
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ resubmit: [task: ExportTask]; refresh: [] }>()

const data = computed(() => props.entry?.data ?? null)
/**
 * loading：第一次取着；error：最近一次取失败（有旧数据时照样列出来，上面标明出错）；stale：在重取、或者项目里有了变化
 * 等着重取（对话框开着时去抖后马上重取）；ready：最新的。
 */
const state = computed(() => {
  const e = props.entry
  if (!e) return 'loading'
  if (e.state === 'error') return 'error'
  if (e.state === 'loading' && !e.data) return 'loading'
  return e.stale || e.state === 'loading' ? 'stale' : 'ready'
})

const reasonChips = computed(() => {
  const d = data.value
  if (!d) return []
  return CHANGE_ORDER.filter((k) => (d.reason_counts[k] ?? 0) > 0).map((k) => ({ code: k, text: `${d.reason_counts[k]} 件${CHANGE_LABEL[k]}` }))
})

const deletedCount = computed(() => data.value?.reason_counts.item_deleted ?? 0)
const resubmitCount = computed(() => data.value?.resubmit_item_ids.length ?? 0)

function nowName(it: ExportChangedItem): string | null {
  if (!it.now) return null
  const before = itemName(it.archive_no, it.item_no)
  const after = itemName(it.now.archive_no, it.now.item_no)
  return before === after ? null : after
}

function fieldValue(v: unknown): string {
  if (v === null || v === undefined || v === '') return '（空）'
  return typeof v === 'string' ? v : JSON.stringify(v)
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent
      class="flex max-h-[84vh] flex-col gap-3 sm:max-w-3xl"
      data-testid="task-changes-dialog"
      :data-task-id="props.task?.id"
      :data-state="state"
      :data-changed-count="data?.changed_count"
      :data-resubmit-count="data ? resubmitCount : undefined"
      :data-fetched-at="props.entry?.fetchedAt || undefined"
    >
      <DialogHeader>
        <DialogTitle>交付后变更：{{ props.scopeTitle }}</DialogTitle>
        <DialogDescription>
          和 {{ formatDateTime(props.task?.created_at) }} 建任务时冻结的内容比，现在有变化的条目。只换了归类节点或节点改名不算变更。
        </DialogDescription>
      </DialogHeader>

      <div class="flex shrink-0 flex-wrap items-center gap-2 text-xs">
        <template v-if="data">
          <span class="font-medium text-foreground" data-testid="task-changes-summary">
            导出 {{ data.item_count.toLocaleString() }} 件，其中 {{ data.changed_count.toLocaleString() }} 件有变更
          </span>
          <span v-for="c in reasonChips" :key="c.code" class="rounded bg-warn-soft px-1.5 leading-5 text-warn" :data-reason="c.code">{{ c.text }}</span>
        </template>
        <span class="flex-1" />
        <LoaderCircleIcon v-if="state === 'loading' || props.entry?.state === 'loading'" class="size-3.5 animate-spin text-muted-foreground" />
        <Button size="xs" variant="ghost" data-testid="task-changes-refresh" @click="emit('refresh')"><RefreshCwIcon />刷新</Button>
      </div>

      <p
        v-if="state === 'error'"
        class="shrink-0 rounded-md bg-destructive-soft px-3 py-2 text-xs text-destructive"
        data-testid="task-changes-failed"
      >
        取交付后变更失败：{{ props.entry?.error }}。{{ data ? '下面是上一次取到的，可能已过时；' : '' }}会自动重试，也可以点"刷新"。
      </p>
      <div class="min-h-0 flex-1 overflow-y-auto rounded-lg border">
        <div v-if="state === 'loading'" class="flex items-center gap-2 p-4 text-xs text-muted-foreground">
          <LoaderCircleIcon class="size-3.5 animate-spin" />正在比较（约需一两百毫秒）…
        </div>
        <p v-else-if="state === 'error' && !data" class="p-4 text-xs text-muted-foreground">还没有取到过。</p>
        <div v-else-if="data && !data.items.length" class="p-6 text-center text-xs text-muted-foreground" data-testid="task-changes-empty">
          导出之后这些条目没有变化，不需要补交。
        </div>
        <ul v-else-if="data" class="divide-y">
          <li
            v-for="it in data.items"
            :key="it.item_id"
            class="px-3 py-2 text-xs"
            data-testid="task-change-item"
            :data-item-id="it.item_id"
            :data-reasons="it.reasons.map((r) => r.code).join(',')"
            :data-deleted="it.deleted ? 'true' : undefined"
          >
            <div class="flex items-center gap-2">
              <span class="w-8 shrink-0 text-muted-foreground tabular-nums">{{ it.seq }}</span>
              <span class="shrink-0 font-mono font-medium text-foreground">{{ itemName(it.archive_no, it.item_no) }}</span>
              <span v-if="nowName(it)" class="shrink-0 font-mono text-muted-foreground">→ 现在 {{ nowName(it) }}</span>
              <span class="min-w-0 flex-1 truncate text-foreground" :title="it.title">{{ it.title || '（未填题名）' }}</span>
              <span v-if="it.deleted" class="shrink-0 rounded-full bg-destructive-soft px-1.5 leading-5 text-destructive">已删除，不补交</span>
              <span v-else-if="it.now" class="shrink-0 rounded-full px-1.5 leading-5" :class="itemStatusOf(it.now.status).badge">现在{{ itemStatusOf(it.now.status).label }}</span>
            </div>
            <div class="mt-1 space-y-0.5 pl-10">
              <div v-for="r in it.reasons" :key="r.code" class="flex items-start gap-1.5" :data-reason="r.code">
                <span class="shrink-0 rounded bg-muted px-1 leading-5 text-muted-foreground">{{ CHANGE_LABEL[r.code] ?? r.code }}</span>
                <div class="min-w-0 flex-1 leading-5 text-foreground">
                  <template v-if="r.message !== CHANGE_LABEL[r.code]">{{ r.message }}</template>
                  <ul v-if="r.fields?.length" class="mt-0.5 space-y-px text-[11px] text-muted-foreground">
                    <li v-for="f in r.fields" :key="f.key">
                      {{ f.label }}：<span class="line-through">{{ fieldValue(f.before) }}</span> → <span class="text-foreground">{{ fieldValue(f.after) }}</span>
                    </li>
                  </ul>
                </div>
              </div>
            </div>
          </li>
          <li v-if="data.truncated" class="px-3 py-2 text-[11px] text-muted-foreground">
            只列出了前 {{ data.items.length }} 件；有变更的一共 {{ data.changed_count.toLocaleString() }} 件，补交时全部包含。
          </li>
        </ul>
      </div>

      <DialogFooter class="items-center">
        <span v-if="deletedCount" class="mr-auto text-[11px] text-muted-foreground">已删除的 {{ deletedCount }} 件不补交；用同一个档号新建的条目要另外导出。</span>
        <Button variant="outline" @click="open = false">关闭</Button>
        <Button
          :disabled="!resubmitCount || !props.task"
          data-testid="task-changes-resubmit"
          :data-count="resubmitCount"
          @click="props.task && emit('resubmit', props.task)"
        >
          <SendIcon />只补交有变更的 {{ resubmitCount.toLocaleString() }} 件
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
