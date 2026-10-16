<script setup lang="ts">
// 登录后的外框：左侧导航 + 右侧页面。
// 导航完全由 /api/me/menus 生成：
// - 业务界面：非 system.* 的菜单；有任意 system.* 菜单时，底部显示"⚙ 系统管理"入口。
// - /system 下：侧栏换成 system.* 菜单，顶部提供"← 返回业务界面"。
import { InfoIcon, LogOutIcon, SettingsIcon } from '@lucide/vue'
import { computed, ref } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import AboutDialog from '@/components/app/AboutDialog.vue'
import ProjectSwitcher from '@/components/app/ProjectSwitcher.vue'
import { useSessionStore } from '@/stores/session'
// M1-11：本机图片缓存的入口（只在壳里出现）。
import { HardDriveIcon } from '@lucide/vue'
import ImageCacheDialog from '@/components/app/ImageCacheDialog.vue'
import { useShellStore } from '@/stores/shell'
// M1-13：托盘菜单"继续热文件夹"失败时（tray:error），在任何页面都弹出原因（只在壳里订阅）。
import { useTrayErrors } from '@/composables/useTrayErrors'
// M7-1：许可证提醒条（只给管理员）。
import LicenseBanner from '@/components/app/LicenseBanner.vue'

const session = useSessionStore()
const route = useRoute()
const router = useRouter()

const inSystem = computed(() => route.path === '/system' || route.path.startsWith('/system/'))
const items = computed(() => (inSystem.value ? session.systemMenus : session.businessMenus))
const activeCode = computed(() => session.menuForPath(route.path)?.menu_code)
const roleNames = computed(() => session.me?.roles.map((r) => r.role_name).join(' · ') || '没有角色')
const initial = computed(() => (session.me?.display_name || session.me?.username || '?').slice(0, 1))

const loggingOut = ref(false)
const aboutOpen = ref(false)
const shell = useShellStore()
const cacheOpen = ref(false)

// 退出失败时服务端会话可能还有效（Cookie 也还在），不能当作已退出：留在当前页，提示重试。
async function logout() {
  if (loggingOut.value) return
  loggingOut.value = true
  try {
    await session.logout()
  } catch {
    toast.error('退出失败，会话可能仍然有效，请检查网络后重试', { id: 'logout-failed' })
    return
  } finally {
    loggingOut.value = false
  }
  await router.replace({ name: 'login' })
}

// M1-13：托盘的出错提示。
useTrayErrors()
</script>

<template>
  <div class="flex h-screen min-h-0 bg-background">
    <aside
      class="flex w-56 shrink-0 flex-col border-r border-sidebar-border bg-sidebar px-3 py-3 text-sidebar-foreground"
      data-testid="side-nav"
    >
      <div class="mb-2 flex items-center gap-2.5 border-b border-sidebar-border px-2 pt-1 pb-3">
        <span
          class="flex size-7 shrink-0 items-center justify-center rounded-md bg-primary text-[13px] font-semibold text-primary-foreground"
        >D</span>
        <div v-if="inSystem" class="min-w-0">
          <div class="truncate text-sm font-semibold text-foreground">系统管理</div>
          <div class="truncate text-xs text-muted-foreground">用户、角色和系统设置</div>
        </div>
        <div v-else class="min-w-0">
          <div class="truncate text-sm font-semibold text-foreground">Doclite</div>
          <div class="truncate text-xs text-muted-foreground">纸质档案数字化</div>
        </div>
      </div>

      <!-- M1-5：业务界面的项目切换器（当前项目在 stores/project.ts）；没有业务菜单的用户不显示。 -->
      <ProjectSwitcher v-if="!inSystem && session.businessMenus.length > 0" />

      <RouterLink
        v-if="inSystem && session.businessHome"
        :to="session.businessHome"
        class="mb-1 rounded-md px-2.5 py-2 text-sm text-info hover:bg-sidebar-accent"
        data-testid="nav-back"
      >
        ← 返回业务界面
      </RouterLink>

      <nav class="flex flex-col gap-0.5">
        <RouterLink
          v-for="m in items"
          :key="m.menu_code"
          :to="m.route_path!"
          :data-menu="m.menu_code"
          class="rounded-md px-2.5 py-2 text-sm transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
          :class="
            activeCode === m.menu_code
              ? 'bg-sidebar-accent font-medium text-sidebar-accent-foreground'
              : ''
          "
        >
          {{ m.menu_name }}
        </RouterLink>
        <p v-if="items.length === 0" class="px-2.5 py-2 text-xs text-muted-foreground">
          没有可用的菜单
        </p>
      </nav>

      <div class="flex-1" />

      <RouterLink
        v-if="!inSystem && session.hasSystem"
        to="/system"
        class="mb-2 flex items-center justify-center gap-1.5 rounded-md bg-background px-2.5 py-2 text-sm font-medium text-foreground shadow-[0_0_0_1px_rgba(0,0,0,0.12)] hover:bg-sidebar-accent"
        data-testid="nav-system"
      >
        <SettingsIcon class="size-4" />系统管理
      </RouterLink>

      <!-- M1-11：用户区的"本机图片缓存"（只在有这项能力的客户端里出现；单独一行，不挤占用户名的位置）。 -->
      <button
        v-if="shell.has('images')"
        type="button"
        class="mb-1 flex items-center gap-2 rounded-md px-2.5 py-1.5 text-xs text-muted-foreground hover:bg-sidebar-accent hover:text-foreground"
        data-testid="image-cache"
        @click="cacheOpen = true"
      >
        <HardDriveIcon class="size-3.5" />本机图片缓存
      </button>

      <div class="flex items-center gap-2.5 border-t border-sidebar-border px-2 pt-3">
        <span
          class="flex size-8 shrink-0 items-center justify-center rounded-full bg-secondary text-xs font-medium text-foreground"
        >{{ initial }}</span>
        <div class="min-w-0 flex-1">
          <div class="truncate text-sm font-medium text-foreground">
            {{ session.me?.display_name }}
          </div>
          <div class="truncate text-xs text-muted-foreground" :title="roleNames">{{ roleNames }}</div>
        </div>
        <button
          type="button"
          class="rounded-md p-1.5 text-muted-foreground hover:bg-sidebar-accent hover:text-foreground"
          title="关于（版本号、服务器地址）"
          data-testid="about"
          @click="aboutOpen = true"
        >
          <InfoIcon class="size-4" />
        </button>
        <button
          type="button"
          class="rounded-md p-1.5 text-muted-foreground hover:bg-sidebar-accent hover:text-foreground"
          title="退出登录"
          data-testid="logout"
          :disabled="loggingOut"
          @click="logout"
        >
          <LogOutIcon class="size-4" />
        </button>
      </div>
    </aside>

    <main class="flex min-w-0 flex-1 flex-col overflow-hidden">
      <LicenseBanner />
      <RouterView />
    </main>
    <AboutDialog v-model:open="aboutOpen" />
    <ImageCacheDialog v-if="shell.has('images')" v-model:open="cacheOpen" />
  </div>
</template>
