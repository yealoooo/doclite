<script setup lang="ts">
// 连不上已配置的服务器（只在壳里出现）：显示原因，可以重试或修改地址。
import { UnplugIcon } from '@lucide/vue'
import { computed, ref } from 'vue'

import { Button } from '@/components/ui/button'
import { useShellStore } from '@/stores/shell'

const shell = useShellStore()
const retrying = ref(false)
const reason = computed(() => shell.failure || shell.probe?.error || '原因不明')

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
  <div class="flex flex-col gap-4" data-testid="server-unreachable">
    <div class="flex items-start gap-3">
      <span class="flex size-9 shrink-0 items-center justify-center rounded-full bg-destructive-soft text-destructive">
        <UnplugIcon class="size-4" />
      </span>
      <div class="min-w-0">
        <h1 class="text-lg font-semibold tracking-tight text-foreground">连不上服务器</h1>
        <p class="mt-1 font-mono text-sm break-all text-muted-foreground">{{ shell.serverUrl }}</p>
      </div>
    </div>

    <p class="rounded-md bg-destructive-soft px-3 py-2 text-sm break-all text-destructive" role="alert">
      {{ reason }}
    </p>
    <p class="text-sm text-muted-foreground">
      请确认服务器已经启动、网络是通的。服务器地址变了的话，点"修改地址"重新填写。
    </p>

    <div class="flex gap-2">
      <Button variant="outline" class="flex-1" :disabled="retrying" @click="shell.editServer()">修改地址</Button>
      <Button class="flex-1" :disabled="retrying" data-testid="retry" @click="retry">
        {{ retrying ? '正在重试…' : '重试' }}
      </Button>
    </div>
  </div>
</template>
