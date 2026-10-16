<script setup lang="ts">
// 登录后的外框：左侧导航 + 右侧页面。
// 导航完全由 /api/me/menus 生成：
// - 业务界面：非 system.* 的菜单；有任意 system.* 菜单时，底部显示"⚙ 系统管理"入口。
// - /system 下：侧栏换成 system.* 菜单，顶部提供"← 返回业务界面"。
// 侧栏能收起（M7-11）：收起是一条窄栏，只有菜单图标（悬停提示名字），项目切换器只显示项目编码，用户区只剩头像
// （点开是用户菜单）；顶部的按钮或 ⌘B / Ctrl+B 切换，按本机记住（composables/useSidebarCollapse.ts）。
// 用户区的"设置"（M7-11，只在客户端里）：客户端的设置页 /settings，取代原来的"关于""本机图片缓存"两个对话框。
import {
  ArrowLeftIcon,
  LogOutIcon,
  MonitorCogIcon,
  PanelLeftCloseIcon,
  PanelLeftOpenIcon,
  SettingsIcon,
} from '@lucide/vue'
import { computed, ref } from 'vue'
import { RouterView, useRoute, useRouter } from 'vue-router'

import ProjectSwitcher from '@/components/app/ProjectSwitcher.vue'
import SideNavLink from '@/components/app/SideNavLink.vue'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useLogout } from '@/composables/useLogout'
import { SIDEBAR_SHORTCUT, useSidebarCollapse } from '@/composables/useSidebarCollapse'
import { SETTINGS_PATH } from '@/composables/useTraySettings'
import { menuIcon } from '@/lib/menu-icons'
import { useSessionStore } from '@/stores/session'
import { useShellStore } from '@/stores/shell'
// M1-13：托盘菜单"继续热文件夹"失败时（tray:error），在任何页面都弹出原因（只在壳里订阅）。
import { useTrayErrors } from '@/composables/useTrayErrors'
// M7-1：许可证提醒条（只给管理员）。
import LicenseBanner from '@/components/app/LicenseBanner.vue'

const session = useSessionStore()
const shell = useShellStore()
const route = useRoute()
const router = useRouter()

const inSystem = computed(() => route.path === '/system' || route.path.startsWith('/system/'))
const items = computed(() => (inSystem.value ? session.systemMenus : session.businessMenus))
const activeCode = computed(() => session.menuForPath(route.path)?.menu_code)
const roleNames = computed(() => session.me?.roles.map((r) => r.role_name).join(' · ') || '没有角色')
const initial = computed(() => (session.me?.display_name || session.me?.username || '?').slice(0, 1))

const { collapsed, toggle } = useSidebarCollapse({ shortcut: true })
function expand() {
  collapsed.value = false
}
const toggleLabel = computed(() => `${collapsed.value ? '展开' : '收起'}侧栏（${SIDEBAR_SHORTCUT}）`)

const { loggingOut, logout } = useLogout()

/** 客户端的设置页只在客户端里有。 */
const hasSettings = computed(() => shell.inShell)
const onSettings = computed(() => route.path === SETTINGS_PATH)
const userMenuOpen = ref(false)

function openSettings() {
  userMenuOpen.value = false
  void router.push(SETTINGS_PATH)
}

function logoutFromMenu() {
  userMenuOpen.value = false
  void logout()
}

// M1-13：托盘的出错提示。
useTrayErrors()
</script>

<template>
  <div class="flex h-screen min-h-0 bg-background">
    <aside
      class="flex shrink-0 flex-col overflow-x-hidden border-r border-sidebar-border bg-sidebar py-3 text-sidebar-foreground transition-[width] duration-200"
      :class="collapsed ? 'w-14 px-2' : 'w-56 px-3'"
      data-testid="side-nav"
      :data-collapsed="collapsed ? 'true' : 'false'"
    >
      <div
        class="mb-2 flex items-center border-b border-sidebar-border pt-1 pb-3"
        :class="collapsed ? 'flex-col gap-2' : 'gap-2.5 px-2'"
      >
        <span
          class="flex size-7 shrink-0 items-center justify-center rounded-md bg-primary text-[13px] font-semibold text-primary-foreground"
        >D</span>
        <template v-if="!collapsed">
          <div v-if="inSystem" class="min-w-0 flex-1">
            <div class="truncate text-sm font-semibold text-foreground">系统管理</div>
            <div class="truncate text-xs text-muted-foreground">用户、角色和系统设置</div>
          </div>
          <div v-else class="min-w-0 flex-1">
            <div class="truncate text-sm font-semibold text-foreground">Doclite</div>
            <div class="truncate text-xs text-muted-foreground">纸质档案数字化</div>
          </div>
        </template>
        <Tooltip>
          <TooltipTrigger as-child>
            <button
              type="button"
              class="shrink-0 rounded-md p-1.5 text-muted-foreground hover:bg-sidebar-accent hover:text-foreground"
              :aria-label="toggleLabel"
              data-testid="sidebar-toggle"
              @click="toggle"
            >
              <PanelLeftOpenIcon v-if="collapsed" class="size-4" />
              <PanelLeftCloseIcon v-else class="size-4" />
            </button>
          </TooltipTrigger>
          <TooltipContent side="right" :side-offset="8">{{ toggleLabel }}</TooltipContent>
        </Tooltip>
      </div>

      <!-- M1-5：业务界面的项目切换器（当前项目在 stores/project.ts）；没有业务菜单的用户不显示。 -->
      <ProjectSwitcher
        v-if="!inSystem && session.businessMenus.length > 0"
        :collapsed="collapsed"
        @expand="expand"
      />

      <SideNavLink
        v-if="inSystem && session.businessHome"
        :to="session.businessHome"
        :icon="ArrowLeftIcon"
        label="返回业务界面"
        :collapsed="collapsed"
        class="mb-1 text-info"
        data-testid="nav-back"
      />

      <nav class="flex flex-col gap-0.5">
        <SideNavLink
          v-for="m in items"
          :key="m.menu_code"
          :to="m.route_path!"
          :icon="menuIcon(m.menu_code)"
          :label="m.menu_name"
          :collapsed="collapsed"
          :active="activeCode === m.menu_code"
          :data-menu="m.menu_code"
          :data-active="activeCode === m.menu_code ? 'true' : undefined"
        />
        <p v-if="items.length === 0 && !collapsed" class="px-2.5 py-2 text-xs text-muted-foreground">
          没有可用的菜单
        </p>
      </nav>

      <div class="flex-1" />

      <SideNavLink
        v-if="!inSystem && session.hasSystem"
        to="/system"
        :icon="SettingsIcon"
        label="系统管理"
        :collapsed="collapsed"
        class="mb-2 bg-background font-medium text-foreground shadow-[0_0_0_1px_rgba(0,0,0,0.12)]"
        :class="collapsed ? '' : 'justify-center gap-1.5'"
        data-testid="nav-system"
      />

      <!-- 用户区：展开时是头像、姓名和角色、设置（只在客户端里）、退出；收起时只有头像，点开是用户菜单。 -->
      <div v-if="!collapsed" class="flex items-center gap-2.5 border-t border-sidebar-border px-2 pt-3" data-testid="user-area">
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
          v-if="hasSettings"
          type="button"
          class="rounded-md p-1.5 text-muted-foreground hover:bg-sidebar-accent hover:text-foreground"
          :class="onSettings ? 'bg-sidebar-accent text-foreground' : ''"
          title="设置（服务器、本机存储、版本）"
          data-testid="open-settings"
          @click="openSettings"
        >
          <MonitorCogIcon class="size-4" />
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
      <div v-else class="flex justify-center border-t border-sidebar-border pt-3" data-testid="user-area">
        <Popover v-model:open="userMenuOpen">
          <PopoverTrigger as-child>
            <button
              type="button"
              class="flex size-8 items-center justify-center rounded-full bg-secondary text-xs font-medium text-foreground hover:ring-2 hover:ring-sidebar-border"
              :class="onSettings ? 'ring-2 ring-sidebar-border' : ''"
              :aria-label="`${session.me?.display_name ?? ''}的菜单`"
              data-testid="user-menu"
            >{{ initial }}</button>
          </PopoverTrigger>
          <PopoverContent side="right" align="end" :side-offset="10" class="w-56 gap-0 p-1" data-testid="user-menu-content">
            <div class="border-b px-2.5 pt-1.5 pb-2">
              <div class="truncate text-sm font-medium text-foreground">{{ session.me?.display_name }}</div>
              <div class="truncate text-xs text-muted-foreground">{{ roleNames }}</div>
            </div>
            <button
              v-if="hasSettings"
              type="button"
              class="mt-1 flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-sm text-foreground hover:bg-muted"
              data-testid="open-settings"
              @click="openSettings"
            >
              <MonitorCogIcon class="size-4 text-muted-foreground" />设置
            </button>
            <button
              type="button"
              class="flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-sm text-foreground hover:bg-muted"
              :class="hasSettings ? '' : 'mt-1'"
              data-testid="logout"
              :disabled="loggingOut"
              @click="logoutFromMenu"
            >
              <LogOutIcon class="size-4 text-muted-foreground" />退出登录
            </button>
          </PopoverContent>
        </Popover>
      </div>
    </aside>

    <main class="flex min-w-0 flex-1 flex-col overflow-hidden">
      <LicenseBanner />
      <RouterView />
    </main>
  </div>
</template>
