<script setup lang="ts">
// 快照历史（M1-10）：`GET /api/files/{fid}/snapshots`，按版本显示封面、类型、时间、创建人，以及规格和存储。
// 快照只追加（硬性规则 2），最大的版本号就是当前版本。封面地址同样经 imageSrc() 取。
import { LoaderCircleIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import { getFileSnapshots } from '@/api/files'
import { errorMessage, HANDLE_ALL } from '@/api/http'
import type { SnapshotItem } from '@/api/types'
import FileCover from '@/components/files/FileCover.vue'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { formatDateTime } from '@/lib/format'
import { formatBytes, formatSpec, isLowDpi, snapshotTypeLabel } from '@/lib/files'

const props = defineProps<{
  fileId: string | null
  fileName?: string
}>()
const open = defineModel<boolean>('open', { required: true })

const snapshots = ref<SnapshotItem[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
let seq = 0

/** 新的版本在上面。 */
const ordered = computed(() => [...snapshots.value].reverse())
const latest = computed(() => snapshots.value[snapshots.value.length - 1]?.version_no)

async function load(fileId: string) {
  const my = ++seq
  loading.value = true
  error.value = null
  try {
    const res = await getFileSnapshots(fileId, HANDLE_ALL)
    if (my === seq) snapshots.value = res.snapshots
  } catch (e) {
    if (my === seq) error.value = errorMessage(e)
  } finally {
    if (my === seq) loading.value = false
  }
}

watch(
  () => [open.value, props.fileId] as const,
  ([isOpen, fileId]) => {
    if (!isOpen || !fileId) return
    snapshots.value = []
    void load(fileId)
  },
  { immediate: true },
)
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="gap-4 sm:max-w-2xl" data-testid="snapshot-dialog">
      <DialogHeader>
        <DialogTitle>快照历史</DialogTitle>
        <DialogDescription>
          {{ props.fileName }} · 快照只追加不修改，最大的版本号就是当前版本
        </DialogDescription>
      </DialogHeader>

      <div class="max-h-[60vh] overflow-y-auto">
        <div v-if="loading && !snapshots.length" class="flex items-center gap-2 py-8 text-sm text-muted-foreground">
          <LoaderCircleIcon class="size-4 animate-spin" />正在加载…
        </div>
        <p v-else-if="error" class="py-6 text-sm text-destructive">{{ error }}</p>
        <ol v-else class="flex flex-col divide-y">
          <li
            v-for="s in ordered"
            :key="s.snapshot_id"
            class="flex gap-4 py-3"
            data-testid="snapshot-item"
            :data-version="s.version_no"
          >
            <FileCover v-if="props.fileId" :file-id="props.fileId" :info="s" class="w-[76px] shrink-0" />
            <div class="grid min-w-0 flex-1 grid-cols-[5em_minmax(0,1fr)] gap-x-3 gap-y-1 text-[13px]">
              <div class="col-span-2 mb-0.5 flex items-center gap-2">
                <span class="font-mono text-sm font-semibold text-foreground">v{{ s.version_no }}</span>
                <span class="rounded bg-muted px-1.5 text-xs leading-5 text-foreground">
                  {{ snapshotTypeLabel(s.snapshot_type) }}
                </span>
                <span
                  v-if="s.version_no === latest"
                  class="rounded bg-info-soft px-1.5 text-xs leading-5 text-info"
                >当前</span>
              </div>
              <span class="text-xs leading-5 text-muted-foreground">时间</span>
              <span>{{ formatDateTime(s.created_at) }}</span>
              <span class="text-xs leading-5 text-muted-foreground">创建人</span>
              <span>{{ s.created_by_name ?? '系统' }}</span>
              <template v-if="s.upload_name">
                <span class="text-xs leading-5 text-muted-foreground">原始文件名</span>
                <span class="truncate" :title="s.upload_name">{{ s.upload_name }}</span>
              </template>
              <span class="text-xs leading-5 text-muted-foreground">规格</span>
              <span :class="isLowDpi(s.dpi) ? 'text-warn' : ''">
                {{ formatBytes(s.file_size) }} · {{ formatSpec(s) }}
              </span>
              <span class="text-xs leading-5 text-muted-foreground">存储</span>
              <span class="min-w-0">
                {{ s.storage_profile_name }}
                <span class="block font-mono text-[11px] break-all text-muted-foreground">{{ s.storage_key }}</span>
              </span>
            </div>
          </li>
        </ol>
      </div>
    </DialogContent>
  </Dialog>
</template>
