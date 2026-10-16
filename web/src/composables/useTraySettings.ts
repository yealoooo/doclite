// 托盘菜单的"设置…"（M7-11）：壳显示主窗口并发 `tray:open-settings`，页面跳到设置页（没登录时路由守卫先带到登录页，
// 登录后回来）。在根组件（App.vue）里订阅，登录页上也收得到；客户端没有 settings 能力（比网页旧）时不订阅。

import { onBeforeUnmount, onMounted } from 'vue'
import { useRouter } from 'vue-router'

import { shellHas } from '@/lib/client'
import { listen } from '@/lib/wails'

export const SETTINGS_PATH = '/settings'

export function useTraySettings() {
  if (!shellHas('settings')) return
  const router = useRouter()
  let stop: (() => void) | null = null
  let alive = true

  onMounted(async () => {
    try {
      const unlisten = await listen<null>('tray:open-settings', () => {
        if (router.currentRoute.value.path !== SETTINGS_PATH) void router.push(SETTINGS_PATH)
      })
      if (alive) stop = unlisten
      else unlisten()
    } catch {
      // 订阅不了：托盘的"设置…"只是打开窗口。
    }
  })

  onBeforeUnmount(() => {
    alive = false
    stop?.()
    stop = null
  })
}
