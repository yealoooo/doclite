<script setup lang="ts">
// 客户端版本过低（只在壳里出现）：启动时壳的版本低于服务器的最低版本，或者任何请求返回 426。
// 下载地址用系统浏览器打开（tauri-plugin-opener）。自动更新在 M5。
import { DownloadIcon, TriangleAlertIcon } from '@lucide/vue'
import { computed, ref } from 'vue'
import { toast } from 'vue-sonner'

import { Button } from '@/components/ui/button'
import { openExternal, shellErrorMessage } from '@/lib/shell'
import { useShellStore } from '@/stores/shell'

const shell = useShellStore()
const info = computed(() => shell.upgrade)
const retrying = ref(false)

async function download() {
  const url = info.value?.download_url
  if (!url) return
  try {
    await openExternal(url)
  } catch (e) {
    toast.error(`打开下载地址失败：${shellErrorMessage(e)}`)
  }
}

async function retry() {
  retrying.value = true
  try {
    await shell.retry()
  } finally {
    retrying.value = false
  }
}
</script>

<template>
  <div class="flex flex-col gap-4" data-testid="upgrade-required">
    <div class="flex items-start gap-3">
      <span class="flex size-9 shrink-0 items-center justify-center rounded-full bg-warn-soft text-warn">
        <TriangleAlertIcon class="size-4" />
      </span>
      <div class="min-w-0">
        <h1 class="text-lg font-semibold tracking-tight text-foreground">客户端版本过低，请更新</h1>
        <p class="mt-1 text-sm text-muted-foreground">服务器已经升级，这个版本的客户端不能再使用。</p>
      </div>
    </div>

    <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 rounded-lg border px-4 py-3 text-sm">
      <dt class="text-muted-foreground">当前版本</dt>
      <dd class="font-mono text-foreground" data-testid="upgrade-current">{{ info?.client_version }}</dd>
      <dt class="text-muted-foreground">最低版本</dt>
      <dd class="font-mono text-foreground" data-testid="upgrade-min">{{ info?.min_version ?? '—' }}</dd>
      <dt class="text-muted-foreground">最新版本</dt>
      <dd class="font-mono text-foreground">{{ info?.latest_version ?? '—' }}</dd>
      <dt class="text-muted-foreground">下载地址</dt>
      <dd class="font-mono break-all text-foreground">{{ info?.download_url ?? '—' }}</dd>
    </dl>

    <Button v-if="info?.download_url" data-testid="upgrade-download" @click="download">
      <DownloadIcon />下载新版本
    </Button>
    <p v-else class="rounded-md bg-info-soft px-3 py-2 text-sm text-info">
      服务器没有配置下载地址，请联系系统管理员获取新版本的安装包。
    </p>

    <div class="flex items-center justify-between text-sm">
      <button type="button" class="text-muted-foreground hover:text-foreground" @click="shell.editServer()">
        修改服务器地址
      </button>
      <button
        type="button"
        class="text-info hover:underline disabled:opacity-50"
        :disabled="retrying"
        @click="retry"
      >
        {{ retrying ? '正在检查…' : '已经更新了？重新检查' }}
      </button>
    </div>
  </div>
</template>
