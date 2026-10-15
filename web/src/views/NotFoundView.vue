<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'

import PageHeader from '@/components/app/PageHeader.vue'
import { Button } from '@/components/ui/button'

const route = useRoute()
</script>

<template>
  <PageHeader title="页面不存在" />
  <section class="flex flex-1 items-center justify-center p-6">
    <div class="flex max-w-md flex-col items-center gap-3 text-center">
      <h2 class="text-lg font-semibold text-foreground">找不到这个页面</h2>
      <p class="font-mono text-xs text-muted-foreground">{{ route.fullPath }}</p>
      <Button as-child variant="outline" class="mt-2">
        <RouterLink to="/">回到首页</RouterLink>
      </Button>
    </div>
  </section>
</template>
