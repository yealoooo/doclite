<script setup lang="ts">
// 设置页的"本机存储"（M7-11）：
// - 数据目录：路径（shell_info.data_dir，所有客户端都有）；有 settings 能力时另有大小和"在访达中打开"；
// - 本机图片缓存（能力 images）：ImageCachePanel（占用、上限、各项目、改上限、清空）；
// - 上传队列（能力 upload）：等待、上传中、失败、已完成各多少，"重试失败的"；
// - 日志目录（能力 settings）：路径、大小、"打开"。
// 打开目录只给名字（data、logs、cache），路径由客户端自己算（client/BRIDGE.md）。
import { FolderOpenIcon, LoaderCircleIcon, RotateCcwIcon } from '@lucide/vue'
import { computed, ref } from 'vue'
import { toast } from 'vue-sonner'

import ImageCachePanel from '@/components/app/ImageCachePanel.vue'
import { Button } from '@/components/ui/button'
import { formatBytes } from '@/lib/files'
import { type QueueStatus, uploadRetryFailed } from '@/lib/scan-queue'
import { fileManagerName, type LocalDirName, type LocalDirs, openLocalDir, shellErrorMessage } from '@/lib/shell'

const props = defineProps<{
  platform: string | null
  dataDir: string | null
  /** 有 settings 能力：目录的大小、打开目录。 */
  canOpen: boolean
  dirs: LocalDirs | null
  dirsError: string
  hasImages: boolean
  hasUpload: boolean
  queue: QueueStatus | null
  queueError: string
}>()

const emit = defineEmits<{
  /** 重试了失败的：上传队列要重新读。 */
  'queue-changed': []
}>()

const openLabel = computed(() => `在${fileManagerName(props.platform)}中打开`)
const opening = ref<LocalDirName | ''>('')

async function open(dir: LocalDirName) {
  if (opening.value) return
  opening.value = dir
  try {
    await openLocalDir(dir)
  } catch (e) {
    toast.error(`打不开目录：${shellErrorMessage(e)}`)
  } finally {
    opening.value = ''
  }
}

const tiles = computed(() => {
  const c = props.queue?.stats
  if (!c) return []
  return [
    { key: 'waiting', label: '等待', count: c.waiting + c.queued, title: '等待写入完成、排队等上传（含等着重试的）' },
    { key: 'uploading', label: '上传中', count: c.uploading, title: '正在上传' },
    { key: 'failed', label: '失败', count: c.failed, title: '上传失败，可以重试' },
    { key: 'done', label: '已完成', count: c.done + c.duplicate, title: `已入库 ${c.done}，内容重复已跳过 ${c.duplicate}` },
  ]
})
const failed = computed(() => props.queue?.stats.failed ?? 0)
const retrying = ref(false)

async function retryFailed() {
  if (retrying.value || failed.value === 0) return
  retrying.value = true
  try {
    const n = await uploadRetryFailed()
    toast.success(n > 0 ? `已重新排队 ${n} 个失败的文件` : '没有失败的文件')
    emit('queue-changed')
  } catch (e) {
    toast.error(`重试失败：${shellErrorMessage(e)}`)
  } finally {
    retrying.value = false
  }
}
</script>

<template>
  <div class="flex flex-col gap-5 text-sm">
    <!-- 数据目录 -->
    <div class="flex items-start gap-3" data-testid="settings-data-dir">
      <div class="min-w-0 flex-1">
        <div class="text-muted-foreground">数据目录</div>
        <div class="mt-0.5 font-mono text-xs break-all text-foreground" data-testid="settings-data-path">
          {{ props.dirs?.data.path ?? props.dataDir ?? '—' }}
        </div>
        <div v-if="props.canOpen" class="mt-0.5 text-xs text-muted-foreground" data-testid="settings-data-size">
          {{ props.dirs ? `共 ${formatBytes(props.dirs.data.size_bytes)}（设置、登录状态、上传队列、日志、图片缓存）` : props.dirsError || '正在统计…' }}
        </div>
      </div>
      <Button
        v-if="props.canOpen"
        size="sm"
        variant="outline"
        :disabled="!!opening"
        data-testid="open-data-dir"
        @click="open('data')"
      >
        <LoaderCircleIcon v-if="opening === 'data'" class="animate-spin" /><FolderOpenIcon v-else />{{ openLabel }}
      </Button>
    </div>

    <!-- 本机图片缓存 -->
    <div v-if="props.hasImages" class="flex flex-col gap-2 border-t pt-4" data-testid="settings-cache">
      <div class="flex items-center gap-3">
        <div class="min-w-0 flex-1">
          <div class="font-medium text-foreground">本机图片缓存</div>
          <div class="text-xs text-muted-foreground">看过的封面和原图存在这台电脑上，再次打开时不用重新下载。</div>
        </div>
        <Button
          v-if="props.canOpen"
          size="sm"
          variant="ghost"
          :disabled="!!opening"
          data-testid="open-cache-dir"
          @click="open('cache')"
        >
          <LoaderCircleIcon v-if="opening === 'cache'" class="animate-spin" /><FolderOpenIcon v-else />打开
        </Button>
      </div>
      <ImageCachePanel />
    </div>

    <!-- 上传队列 -->
    <div v-if="props.hasUpload" class="flex flex-col gap-2 border-t pt-4" data-testid="settings-queue">
      <div class="flex items-center gap-3">
        <div class="min-w-0 flex-1">
          <div class="font-medium text-foreground">上传队列</div>
          <div class="text-xs text-muted-foreground">热文件夹和客户端里上传的文件，只算当前服务器的。</div>
        </div>
        <Button
          size="sm"
          variant="outline"
          :disabled="failed === 0 || retrying"
          data-testid="queue-retry-failed"
          @click="retryFailed"
        >
          <LoaderCircleIcon v-if="retrying" class="animate-spin" /><RotateCcwIcon v-else />重试失败的
        </Button>
      </div>
      <p v-if="props.queueError" class="text-destructive" role="alert">读不到上传队列：{{ props.queueError }}</p>
      <div v-else class="grid grid-cols-4 gap-2" data-testid="settings-queue-stats">
        <div
          v-for="t in tiles"
          :key="t.key"
          class="rounded-md bg-muted/60 px-3 py-2"
          :title="t.title"
          :data-testid="`queue-${t.key}`"
        >
          <div class="text-xs text-muted-foreground">{{ t.label }}</div>
          <div
            class="text-lg font-semibold tabular-nums"
            :class="t.key === 'failed' && t.count > 0 ? 'text-destructive' : 'text-foreground'"
            data-testid="queue-count"
          >
            {{ t.count }}
          </div>
        </div>
        <div v-if="!props.queue" class="col-span-4 text-muted-foreground">正在读取…</div>
      </div>
      <p v-if="props.queue?.paused" class="rounded-md bg-warn-soft px-3 py-2 text-warn" data-testid="settings-queue-paused">
        上传已暂停：{{ props.queue.paused.reason === 'auth_required' ? '需要重新登录，登录后自动继续' : '客户端需要升级' }}
      </p>
    </div>

    <!-- 日志目录 -->
    <div v-if="props.canOpen" class="flex items-start gap-3 border-t pt-4" data-testid="settings-logs-dir">
      <div class="min-w-0 flex-1">
        <div class="text-muted-foreground">日志目录</div>
        <div class="mt-0.5 font-mono text-xs break-all text-foreground" data-testid="settings-logs-path">
          {{ props.dirs?.logs.path ?? (props.dirsError || '正在读取…') }}
        </div>
        <div v-if="props.dirs" class="mt-0.5 text-xs text-muted-foreground">
          {{ props.dirs.logs.file_count }} 个文件，共 {{ formatBytes(props.dirs.logs.size_bytes) }}（按天一个文件，保留 14 天）
        </div>
      </div>
      <Button size="sm" variant="outline" :disabled="!!opening" data-testid="open-logs-dir" @click="open('logs')">
        <LoaderCircleIcon v-if="opening === 'logs'" class="animate-spin" /><FolderOpenIcon v-else />打开
      </Button>
    </div>
  </div>
</template>
