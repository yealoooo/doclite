<script setup lang="ts">
import { RouterView } from 'vue-router'

import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
</script>

<template>
  <TooltipProvider :delay-duration="150">
    <RouterView />
  </TooltipProvider>
  <Toaster position="top-center" close-button :toast-options="{ classes: { toast: 'rounded-lg' } }" />
</template>
