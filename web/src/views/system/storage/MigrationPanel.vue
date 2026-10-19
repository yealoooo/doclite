<script setup lang="ts">
// 存储迁移（M7-2）：源存储卡片上最近一次迁移的进度和结果。
// - 排队中（失败过的显示"等待重试"和原因）；迁移中：进度条、已迁 / 总数 / 失败，可以取消；正在取消；
// - 完成：已迁、失败（有失败时"查看失败清单"），源存储没有文件了就提示可以停用；失败：原因；已取消：迁了多少。
// 截图脚本用的属性：data-testid=storage-migration（data-status、data-cancel-requested、data-migrated、data-total、
// data-failed、data-target）、migration-cancel、migration-details。
import { BanIcon, ListIcon } from '@lucide/vue'
import { computed } from 'vue'

import type { StorageMigration } from '@/api/types'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/ui/status-badge'
import { formatDateTime } from '@/lib/format'
import { migrationActive, migrationPercent, migrationStatusOf } from '@/lib/storage'

const props = defineProps<{
  migration: StorageMigration
  /** 源存储里还有没有文件（列表的 in_use）。 */
  sourceInUse: boolean
  canceling: boolean
}>()
const emit = defineEmits<{ cancel: []; details: [] }>()

const m = computed(() => props.migration)
const status = computed(() => migrationStatusOf(m.value))
const active = computed(() => migrationActive(m.value))
const percent = computed(() => migrationPercent(m.value))
const retrying = computed(() => m.value.status === 'queued' && m.value.attempts > 0)
</script>

<template>
  <div
    class="rounded-md bg-muted/50 px-3 py-2.5 text-xs"
    data-testid="storage-migration"
    :data-status="m.status"
    :data-cancel-requested="m.cancel_requested ? 'true' : undefined"
    :data-migrated="m.migrated"
    :data-total="m.total"
    :data-failed="m.failed"
    :data-target="m.target_profile_name"
  >
    <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
      <span class="font-medium text-foreground">迁移到「{{ m.target_profile_name }}」</span>
      <StatusBadge :status="status" size="md" />
      <span class="text-2xs text-muted-foreground">
        {{ m.created_by_name ?? '—' }} · {{ formatDateTime(m.created_at) }}
        <template v-if="m.finished_at"> · 结束于 {{ formatDateTime(m.finished_at) }}</template>
      </span>
      <span class="flex-1" />
      <Button
        v-if="active && !m.cancel_requested"
        size="xs"
        variant="ghost"
        class="text-destructive hover:text-destructive"
        :disabled="props.canceling"
        data-testid="migration-cancel"
        @click="emit('cancel')"
      ><BanIcon />取消迁移</Button>
      <Button
        v-if="!active"
        size="xs"
        variant="ghost"
        data-testid="migration-details"
        @click="emit('details')"
      ><ListIcon />{{ m.failed > 0 ? `查看失败清单（${m.failed}）` : '详情' }}</Button>
    </div>

    <div v-if="active" class="mt-2">
      <div class="flex items-center gap-2">
        <div
          class="h-1.5 min-w-0 flex-1 overflow-hidden rounded-full bg-background"
          role="progressbar"
          :aria-valuenow="percent"
          aria-valuemin="0"
          aria-valuemax="100"
        >
          <div class="h-full rounded-full bg-info transition-[width] duration-500" :style="{ width: `${percent}%` }" />
        </div>
        <span class="shrink-0 text-muted-foreground tabular-nums">{{ percent }}%</span>
      </div>
    </div>

    <p class="mt-1.5 text-2xs text-muted-foreground tabular-nums" data-testid="storage-migration-counts">
      已迁 {{ m.migrated.toLocaleString() }} / 共 {{ m.total.toLocaleString() }} 个快照
      <template v-if="m.failed > 0">
        · <span class="text-warn">核对不一致 {{ m.failed.toLocaleString() }} 个（{{ m.failed_objects.toLocaleString() }} 个对象，没有改）</span>
      </template>
      <template v-if="m.status === 'running' && m.attempts > 1"> · 第 {{ m.attempts }} 次执行（从断点接着做）</template>
      <template v-if="m.status === 'queued' && !retrying"> · 等空闲的执行器</template>
    </p>

    <p v-if="m.status === 'running' && m.cancel_requested" class="mt-1 text-2xs text-muted-foreground">
      正在停下：手上的这几个做完就停，已经迁的不退回。
    </p>
    <p
      v-if="(m.status === 'failed' || retrying) && m.error_message"
      class="mt-1.5 rounded-md px-2.5 py-1.5 text-2xs break-all"
      :class="m.status === 'failed' ? 'bg-destructive-soft text-destructive' : 'bg-warn-soft text-warn'"
      data-testid="storage-migration-error"
    >
      {{ m.error_message }}
    </p>
    <p v-if="m.status === 'done' && !props.sourceInUse" class="mt-1 text-2xs text-ok" data-testid="storage-migration-empty">
      这个存储里已经没有文件了。建议先改为只读（旧的导出物仍能下载），等导出物到期被自动清理后再停用；存储里的对象没有删除。
    </p>
    <p v-else-if="!active && props.sourceInUse" class="mt-1 text-2xs text-muted-foreground">
      这个存储里还有文件（{{ m.failed > 0 ? '核对不一致的、' : '' }}没迁完的），可以处理后再迁一次。
    </p>
  </div>
</template>
