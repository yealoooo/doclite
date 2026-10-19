<script setup lang="ts">
// 存储迁移（M7-2）：最近一次迁移的详情（`GET …/{id}/migrate`）：状态、数量、源存储里还剩的快照、失败原因，和核对不一致
// 没有迁的清单（按对象：原因、源存储里的 key、引用它的快照——项目、文件夹、文件名、版本、file_id）。
// 截图脚本用的属性：data-testid=migration-detail（data-state、data-remaining、data-failures）、migration-failure
// （data-reason、data-snapshots）。
import { LoaderCircleIcon, RefreshCwIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import { errorMessage, HANDLE_ALL } from '@/api/http'
import { getStorageMigration } from '@/api/storage'
import type { StorageMigration, StorageProfileItem } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { formatDateTime } from '@/lib/format'
import { MIGRATION_FAILURE_LABEL, migrationStatusOf } from '@/lib/storage'

const props = defineProps<{ source: StorageProfileItem | null }>()
const open = defineModel<boolean>('open', { required: true })

const data = ref<StorageMigration | null>(null)
const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const error = ref('')
let seq = 0

async function load() {
  const id = props.source?.id
  if (!id) return
  const my = ++seq
  state.value = 'loading'
  try {
    const m = await getStorageMigration(id, HANDLE_ALL)
    if (my !== seq) return
    data.value = m
    state.value = 'ready'
  } catch (err) {
    if (my !== seq) return
    error.value = errorMessage(err)
    state.value = 'error'
  }
}

watch(
  [open, () => props.source?.id],
  ([v]) => {
    if (!v) return
    data.value = null
    void load()
  },
  { immediate: true },
)

const status = computed(() => (data.value ? migrationStatusOf(data.value) : null))
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent
      class="flex max-h-[84vh] flex-col gap-3 sm:max-w-3xl"
      data-testid="migration-detail"
      :data-state="state"
      :data-remaining="data?.remaining"
      :data-failures="data?.failures?.length"
    >
      <DialogHeader>
        <DialogTitle>「{{ props.source?.profile_name }}」的迁移</DialogTitle>
        <DialogDescription>
          <template v-if="data">
            迁到「{{ data.target_profile_name }}」 · {{ data.created_by_name ?? '—' }} 发起于 {{ formatDateTime(data.created_at) }}
            <template v-if="data.finished_at"> · 结束于 {{ formatDateTime(data.finished_at) }}</template>
          </template>
          <template v-else>最近一次迁移的结果。</template>
        </DialogDescription>
      </DialogHeader>

      <div class="flex shrink-0 flex-wrap items-center gap-2 text-xs">
        <template v-if="data && status">
          <span class="rounded-full px-2 leading-5 font-medium" :class="status.badge">{{ status.label }}</span>
          <span class="text-foreground tabular-nums">
            已迁 {{ data.migrated.toLocaleString() }} / 共 {{ data.total.toLocaleString() }} 个快照 · 核对不一致
            {{ data.failed.toLocaleString() }} 个（{{ data.failed_objects.toLocaleString() }} 个对象）
          </span>
          <span class="text-muted-foreground tabular-nums" data-testid="migration-remaining">
            · 源存储里现在还有 {{ (data.remaining ?? 0).toLocaleString() }} 个快照
          </span>
        </template>
        <span class="flex-1" />
        <LoaderCircleIcon v-if="state === 'loading'" class="size-3.5 animate-spin text-muted-foreground" />
        <Button size="xs" variant="ghost" @click="load"><RefreshCwIcon />刷新</Button>
      </div>

      <p v-if="state === 'error'" class="shrink-0 rounded-md bg-destructive-soft px-3 py-2 text-xs text-destructive">
        取迁移详情失败：{{ error }}
      </p>
      <p
        v-if="data?.error_message"
        class="shrink-0 rounded-md px-3 py-2 text-xs break-all"
        :class="data.status === 'failed' ? 'bg-destructive-soft text-destructive' : 'bg-warn-soft text-warn'"
      >
        {{ data.error_message }}
      </p>
      <p v-if="data && data.remaining === 0" class="shrink-0 rounded-md bg-ok-soft px-3 py-2 text-xs text-foreground">
        源存储里已经没有文件了。建议先改为只读（旧的导出物仍能下载），等导出物到期被自动清理后再停用。导出过的存储只能停用、不能删除。
        迁移没有删除源存储里的对象，要不要删由运维决定。
      </p>

      <div class="min-h-0 flex-1 overflow-y-auto rounded-lg border">
        <div v-if="state === 'loading' && !data" class="flex items-center gap-2 p-4 text-xs text-muted-foreground">
          <LoaderCircleIcon class="size-3.5 animate-spin" />加载中…
        </div>
        <div v-else-if="data && !data.failures?.length" class="p-6 text-center text-xs text-muted-foreground">
          没有核对不一致的文件。
        </div>
        <ul v-else-if="data" class="divide-y">
          <li
            v-for="f in data.failures"
            :key="f.storage_key"
            class="px-3 py-2 text-xs"
            data-testid="migration-failure"
            :data-reason="f.reason_code"
            :data-snapshots="f.snapshot_count"
          >
            <div class="flex items-start gap-2">
              <span class="shrink-0 rounded bg-warn-soft px-1.5 leading-5 text-warn">{{ MIGRATION_FAILURE_LABEL[f.reason_code] ?? f.reason_code }}</span>
              <div class="min-w-0 flex-1">
                <div class="leading-5 text-foreground">{{ f.reason }}</div>
                <div class="font-mono text-[11px] break-all text-muted-foreground">{{ f.storage_key }}</div>
                <ul class="mt-1 space-y-px text-[11px] text-muted-foreground">
                  <li v-for="s in f.snapshots" :key="s.snapshot_id">
                    <span class="text-foreground">{{ s.project_code ?? '—' }} / {{ s.folder_path ?? '—' }} / {{ s.file_name ?? '（找不到文件）' }}</span>
                    v{{ s.version_no }}<template v-if="s.file_deleted">（已删除）</template>
                    · file_id <span class="font-mono">{{ s.file_id }}</span>
                  </li>
                  <li v-if="f.snapshot_count > f.snapshots.length" data-testid="migration-failure-more">
                    等 {{ f.snapshot_count.toLocaleString() }} 个快照（只列出前 {{ f.snapshots.length }} 个）
                  </li>
                </ul>
              </div>
            </div>
          </li>
        </ul>
      </div>
      <p v-if="data?.failures_truncated" class="shrink-0 text-[11px] text-muted-foreground">
        只列出了前 200 个对象，还有没列出的；处理完再迁一次会重新核对剩下的。
      </p>
      <p v-if="data && data.failed > 0" class="shrink-0 text-[11px] text-muted-foreground">
        这些快照的位置没有改，还指向源存储。重扫或删除有问题的文件（或者修好存储里的对象）后，可以再迁一次。
      </p>
    </DialogContent>
  </Dialog>
</template>
