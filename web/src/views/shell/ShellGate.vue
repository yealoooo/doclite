<script setup lang="ts">
// 壳的整页提示（只在壳里出现）：正在连接、连接服务器、连不上、版本过低。ready 之前由 App.vue 显示它，不进入路由。
import { LoaderCircleIcon } from '@lucide/vue'

import { useShellStore } from '@/stores/shell'

import ConnectServer from './ConnectServer.vue'
import ServerUnreachable from './ServerUnreachable.vue'
import UpgradeRequired from './UpgradeRequired.vue'

const shell = useShellStore()
</script>

<template>
  <div class="flex min-h-screen flex-col items-center justify-center bg-[#fafafa] px-4 py-10">
    <div class="w-full max-w-[440px]">
      <div class="mb-6 flex flex-col items-center gap-3 text-center">
        <span
          class="flex size-10 items-center justify-center rounded-lg bg-primary text-lg font-semibold text-primary-foreground"
        >D</span>
        <p class="text-sm text-muted-foreground">Doclite 纸质档案数字化系统</p>
      </div>

      <div
        class="rounded-xl bg-card p-6 shadow-[0_0_0_1px_rgba(0,0,0,0.08),0_2px_2px_rgba(0,0,0,0.04),0_8px_16px_-8px_rgba(0,0,0,0.08)]"
      >
        <ConnectServer v-if="shell.phase === 'setup'" />
        <ServerUnreachable v-else-if="shell.phase === 'unreachable'" />
        <UpgradeRequired v-else-if="shell.phase === 'upgrade'" />
        <div
          v-else
          class="flex items-center justify-center gap-2 py-6 text-sm text-muted-foreground"
          data-testid="shell-checking"
        >
          <LoaderCircleIcon class="size-4 animate-spin" />
          正在连接服务器…
        </div>
      </div>

      <p class="mt-6 text-center text-xs text-muted-foreground" data-testid="shell-version">
        客户端版本 {{ shell.version }}
      </p>
    </div>
  </div>
</template>
