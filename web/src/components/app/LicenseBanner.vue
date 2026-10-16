<script setup lang="ts">
// M7-1：许可证提醒条（页面顶部）。只有 GET /api/me 带了 license_notice 时显示：服务器只给有 system.license 菜单的人
// （管理员），在许可证 30 天内到期、已到期、没导入、无效时带上。导入新的许可证后许可证页会刷新会话，提醒条随之更新。
import { TriangleAlertIcon } from '@lucide/vue'
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'

import { formatDateTime } from '@/lib/format'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const route = useRoute()

const LICENSE_PATH = '/system/license'

const notice = computed(() => session.me?.license_notice ?? null)
const text = computed(() => {
  const n = notice.value
  if (!n) return ''
  const date = formatDateTime(n.expires_at).slice(0, 10)
  switch (n.status) {
    case 'expiring':
      return n.days_left !== null && n.days_left <= 1
        ? `许可证将在 ${date} 到期（不到一天）。到期后除管理员以外的人都不能登录，请尽快导入新的许可证。`
        : `许可证将在 ${n.days_left} 天后（${date}）到期。到期后除管理员以外的人都不能登录，请尽快导入新的许可证。`
    case 'expired':
      return `许可证已于 ${date} 到期，现在除管理员以外的人都不能登录。请导入新的许可证。`
    case 'invalid':
      return '许可证无效（与这个系统不符），现在除管理员以外的人都不能登录。请重新导入许可证。'
    default:
      return '还没有导入许可证，现在除管理员以外的人都不能登录。请导入许可证。'
  }
})
const urgent = computed(() => notice.value !== null && notice.value.status !== 'expiring')
const showLink = computed(() => session.canAccess(LICENSE_PATH) && route.path !== LICENSE_PATH)
</script>

<template>
  <div
    v-if="notice"
    class="flex shrink-0 items-center gap-2 border-b px-6 py-2 text-sm"
    :class="urgent ? 'bg-destructive-soft text-destructive' : 'bg-warn-soft text-foreground'"
    role="alert"
    data-testid="license-banner"
    :data-status="notice.status"
  >
    <TriangleAlertIcon class="size-4 shrink-0" :class="urgent ? '' : 'text-warn'" />
    <span class="min-w-0 flex-1">{{ text }}</span>
    <RouterLink v-if="showLink" :to="LICENSE_PATH" class="shrink-0 font-medium text-info hover:underline">
      去导入许可证
    </RouterLink>
  </div>
</template>
