<script setup lang="ts">
// 设置页的"热文件夹"（M7-11，能力 upload）：当前监控的目录、上传到哪个文件夹、运行状态、最近的问题。
// 在这里只看；选目录、选目标、开始和暂停在扫描上传页（/scan），按钮跳过去。
import { ArrowRightIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'

import { getFolder } from '@/api/folders'
import { HANDLE_ALL, isApiError } from '@/api/http'
import { Button } from '@/components/ui/button'
import { formatDateTime } from '@/lib/format'
import type { HotFolderStatus } from '@/lib/scan-queue'
import { shellErrorMessage } from '@/lib/shell'
import { useSessionStore } from '@/stores/session'

const props = defineProps<{
  hotfolder: HotFolderStatus | null
  error: string
  /** 上传队列当前的服务器（配置里的目标属于别的服务器时不去查）。 */
  queueServer: string | null
}>()

const SCAN_PATH = '/scan'
const router = useRouter()
const session = useSessionStore()
const canScan = computed(() => session.canAccess(SCAN_PATH))

const config = computed(() => props.hotfolder?.config ?? null)
const otherServer = computed(() => {
  const c = config.value
  return !!c && !!props.queueServer && c.server_url !== props.queueServer
})

const state = computed(() => {
  if (!props.hotfolder) return { text: props.error ? '读取失败' : '正在读取…', cls: 'bg-muted text-muted-foreground' }
  if (props.hotfolder.running) return { text: '运行中', cls: 'bg-ok-soft text-ok' }
  if (config.value) return { text: '已暂停', cls: 'bg-warn-soft text-warn' }
  return { text: '未设置', cls: 'bg-muted text-muted-foreground' }
})

/** 目标文件夹：项目名和从根起的路径。 */
const target = ref<{ project: string; path: string } | null>(null)
const targetError = ref('')
let seq = 0

watch(
  () => [config.value?.target_folder_id ?? null, otherServer.value] as const,
  async ([id, other]) => {
    const mine = ++seq
    target.value = null
    targetError.value = ''
    if (!id) return
    if (other) {
      targetError.value = `目标文件夹在另一个服务器上（${config.value?.server_url}），请在扫描上传页重新选择`
      return
    }
    try {
      const f = await getFolder(id, HANDLE_ALL)
      if (mine !== seq) return
      const names = [...f.ancestors.slice(1).map((a) => a.folder_name), ...(f.parent_id ? [f.folder_name] : [])]
      target.value = { project: f.ancestors[0]?.folder_name ?? f.folder_name, path: `/${names.join('/')}` }
    } catch (e) {
      if (mine !== seq) return
      targetError.value =
        isApiError(e) && e.status === 404
          ? '目标文件夹已被删除，请在扫描上传页重新选择'
          : isApiError(e) && e.status === 403
            ? '你进不了目标文件夹所在的项目'
            : `读不到目标文件夹：${shellErrorMessage(e)}`
    }
  },
  { immediate: true },
)
</script>

<template>
  <div class="flex flex-col gap-3 text-sm">
    <p v-if="props.error && !props.hotfolder" class="text-destructive" role="alert">读不到热文件夹的状态：{{ props.error }}</p>
    <dl class="grid grid-cols-[88px_1fr] items-baseline gap-x-3 gap-y-2.5">
      <dt class="text-muted-foreground">状态</dt>
      <dd class="flex flex-wrap items-center gap-2">
        <span class="rounded-md px-2 py-0.5 text-xs" :class="state.cls" data-testid="settings-hotfolder-state">{{ state.text }}</span>
        <span v-if="props.hotfolder?.running" class="text-xs text-muted-foreground">
          {{ props.hotfolder.watching ? '实时监控目录' : `目录事件不可用，每 ${config?.scan_interval_secs ?? 15} 秒扫描一次` }}
          <template v-if="props.hotfolder.last_scan_at"> · 上次全量扫描 {{ formatDateTime(props.hotfolder.last_scan_at, { seconds: true }) }}</template>
        </span>
      </dd>
      <dt class="text-muted-foreground">监控目录</dt>
      <dd class="min-w-0">
        <code v-if="config" class="font-mono text-xs break-all text-foreground" data-testid="settings-hotfolder-dir">{{ config.watch_dir }}</code>
        <span v-else class="text-muted-foreground">还没设置：在扫描上传页选扫描软件保存文件的文件夹</span>
      </dd>
      <template v-if="config">
        <dt class="text-muted-foreground">上传到</dt>
        <dd class="min-w-0" data-testid="settings-hotfolder-target">
          <span v-if="target" class="text-foreground">
            {{ target.project }} <code class="font-mono text-xs">{{ target.path }}</code>
          </span>
          <span v-else-if="targetError" class="text-destructive">{{ targetError }}</span>
          <span v-else class="text-muted-foreground">…</span>
        </dd>
      </template>
      <template v-if="props.hotfolder?.error">
        <dt class="text-muted-foreground">最近的问题</dt>
        <dd class="text-destructive" data-testid="settings-hotfolder-error">{{ props.hotfolder.error }}</dd>
      </template>
    </dl>
    <div class="flex items-center gap-3">
      <Button
        v-if="canScan"
        size="sm"
        variant="outline"
        data-testid="settings-hotfolder-go"
        @click="router.push(SCAN_PATH)"
      >
        去扫描上传设置<ArrowRightIcon />
      </Button>
      <span v-else class="text-xs text-muted-foreground">当前账号没有「扫描上传」菜单，不能设置热文件夹。</span>
    </div>
  </div>
</template>
