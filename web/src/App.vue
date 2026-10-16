<script setup lang="ts">
import { RouterView } from 'vue-router'

import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
// M7-11：托盘菜单的"设置…"（只在有 settings 能力的客户端里订阅）。
import { useTraySettings } from '@/composables/useTraySettings'

useTraySettings()
</script>

<template>
  <TooltipProvider :delay-duration="150">
    <RouterView />
  </TooltipProvider>
  <Toaster position="top-center" close-button :toast-options="{ classes: { toast: 'rounded-lg' } }" />
</template>
