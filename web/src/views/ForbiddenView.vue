<script setup lang="ts">
// 无权提示页：路由守卫挡下的页面（query.from），或者账号一个菜单都没有。
import { LockIcon } from '@lucide/vue'
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'

import PageHeader from '@/components/app/PageHeader.vue'
import { Button } from '@/components/ui/button'
import { useSessionStore } from '@/stores/session'

const route = useRoute()
const session = useSessionStore()

const from = computed(() => (typeof route.query.from === 'string' ? route.query.from : ''))
const homeMenu = computed(() => (session.homePath ? session.menuForPath(session.homePath) : undefined))
</script>

<template>
  <PageHeader title="没有权限" />
  <section class="flex flex-1 items-center justify-center p-6">
    <div class="flex max-w-md flex-col items-center gap-3 text-center" data-testid="forbidden">
      <span class="flex size-11 items-center justify-center rounded-full bg-secondary text-muted-foreground">
        <LockIcon class="size-5" />
      </span>
      <template v-if="session.navMenus.length === 0">
        <h2 class="text-lg font-semibold text-foreground">你的账号还没有可用的菜单</h2>
        <p class="text-sm text-muted-foreground">菜单由角色决定，请联系系统管理员给你分配角色。</p>
      </template>
      <template v-else>
        <h2 class="text-lg font-semibold text-foreground">你没有访问这个页面的权限</h2>
        <p v-if="from" class="font-mono text-xs text-muted-foreground">{{ from }}</p>
        <p class="text-sm text-muted-foreground">菜单权限由角色决定，如需开通请联系系统管理员。</p>
        <Button v-if="homeMenu" as-child variant="outline" class="mt-2">
          <RouterLink :to="homeMenu.route_path!">前往「{{ homeMenu.menu_name }}」</RouterLink>
        </Button>
      </template>
    </div>
  </section>
</template>
