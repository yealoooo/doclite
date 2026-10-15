<script setup lang="ts">
import { RouterView } from 'vue-router'

import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useShellStore } from '@/stores/shell'
import ShellGate from '@/views/shell/ShellGate.vue'

// 壳里还没连上服务器、版本过低时整页显示 ShellGate，不进入路由（浏览器里一直是 ready）。
const shell = useShellStore()
</script>

<template>
  <TooltipProvider :delay-duration="150">
    <RouterView v-if="shell.phase === 'ready'" />
    <ShellGate v-else />
  </TooltipProvider>
  <Toaster position="top-center" close-button :toast-options="{ classes: { toast: 'rounded-lg' } }" />
</template>
