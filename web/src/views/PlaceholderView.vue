<script setup lang="ts">
// 还没实现的页面：标题和说明都取自当前路由对应的菜单。
import { ConstructionIcon } from '@lucide/vue'
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import PageHeader from '@/components/app/PageHeader.vue'
import { useSessionStore } from '@/stores/session'

const route = useRoute()
const session = useSessionStore()
const menu = computed(() => session.menuForPath(route.path))
</script>

<template>
  <PageHeader />
  <section class="flex flex-1 items-center justify-center p-6">
    <div class="flex max-w-md flex-col items-center gap-3 text-center" data-testid="placeholder">
      <span class="flex size-11 items-center justify-center rounded-full bg-secondary text-muted-foreground">
        <ConstructionIcon class="size-5" />
      </span>
      <h2 class="text-lg font-semibold text-foreground">「{{ menu?.menu_name }}」页面建设中</h2>
      <p class="text-sm text-muted-foreground">这个菜单已经按角色开放给你，页面会在后续版本中实现。</p>
      <p class="font-mono text-xs text-muted-foreground">
        {{ menu?.route_path }} · {{ menu?.menu_code }}
      </p>
    </div>
  </section>
</template>
