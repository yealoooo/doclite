import { createPinia } from 'pinia'
import { createApp } from 'vue'
import { toast } from 'vue-sonner'
import 'vue-sonner/style.css'

import { setHttpHandlers } from '@/api/http'
import { isTauri } from '@/lib/client'
import { watchCspViolations } from '@/lib/csp'
import { useSessionStore } from '@/stores/session'
import { useShellStore } from '@/stores/shell'

import App from './App.vue'
import router from './router'
import './style.css'

if (isTauri()) watchCspViolations()

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)

setHttpHandlers({
  // 会话失效：清本地状态，跳登录页，登录后回到当前页面。
  onUnauthorized(err) {
    useSessionStore(pinia).clear()
    const current = router.currentRoute.value
    if (current.name === 'login') return
    toast.error(err.message, { id: 'unauthorized' })
    void router.replace({ name: 'login', query: { redirect: current.fullPath } })
  },
  // 没有菜单权限（通常是权限刚被改过）：重新拉当前用户和菜单，当前页面已经不能进就换成无权提示页。
  async onForbidden(err) {
    const session = useSessionStore(pinia)
    try {
      await session.refreshSession()
    } catch {
      return
    }
    const current = router.currentRoute.value
    if (current.meta.requiresMenu && !session.canAccess(current.path)) {
      void router.replace({ name: 'forbidden', query: { from: current.fullPath } })
    } else {
      toast.error(err.message, { id: 'forbidden' })
    }
  },
  // 操作要管理员：前端的预判可能已过时（例如自己的 admin 角色刚被收回），刷新后按钮会按最新权限禁用。
  // message 由请求封装或调用方在原页面提示。
  onAdminRequired() {
    useSessionStore(pinia)
      .refreshSession()
      .catch(() => {})
  },
  // 壳的版本低于服务器要求的最低版本：整页提示更新（只在壳里出现）。
  onUpgradeRequired(err) {
    useShellStore(pinia).requireUpgrade(err.details)
  },
})

function mountApp() {
  app.use(router)
  app.mount('#app')
}

async function start() {
  const shell = useShellStore(pinia)
  // 壳以 --smoke 启动时只跑自检（自检到"ui"一步才挂载界面）。
  const info = await shell.loadInfo()
  if (info?.smoke) {
    const { runSmoke } = await import('@/lib/smoke')
    await runSmoke({ mountApp, markReady: () => shell.markReady() })
    return
  }
  mountApp()
  // 壳里：探测服务器，决定显示"连接服务器"、"连不上"、升级提示还是进入界面；浏览器里什么也不做。
  await shell.init()
}

void start()
