import { createPinia } from 'pinia'
import { createApp } from 'vue'
import { toast } from 'vue-sonner'
import 'vue-sonner/style.css'

import { setHttpHandlers } from '@/api/http'
import { isShell } from '@/lib/client'
import { watchCspViolations } from '@/lib/csp'
import { useSessionStore } from '@/stores/session'
import { useShellStore } from '@/stores/shell'

import App from './App.vue'
import router from './router'
import './style.css'

if (isShell()) watchCspViolations()

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
  // 客户端的版本低于服务器要求的最低版本（只在壳里出现）：去客户端自带的升级页；客户端没有这一页时提示。
  onUpgradeRequired(err) {
    if (!useShellStore(pinia).requireUpgrade()) toast.error(err.message, { id: 'upgrade-required', duration: 10000 })
  },
  // M7-1：许可证到期（或没有有效的许可证），非管理员的请求被拒：提示原因，回登录页（管理员导入新的许可证后再登录）。
  onLicenseBlocked(err, redirect) {
    useSessionStore(pinia).clear()
    toast.error(err.message, { id: 'license-blocked', duration: 10000 })
    if (!redirect) return
    const current = router.currentRoute.value
    if (current.name === 'login') return
    void router.replace({ name: 'login', query: { redirect: current.fullPath } })
  },
})

function mountApp() {
  app.use(router)
  app.mount('#app')
}

async function start() {
  const shell = useShellStore(pinia)
  // 壳里先读客户端的信息和能力（界面按能力显示本机功能）；服务器已经由客户端自带的关口页探测过。
  const info = await shell.loadInfo()
  // 壳以 --smoke 启动时只跑自检（自检到"ui"一步才挂载界面）。
  if (info?.smoke) {
    const { runSmoke } = await import('@/lib/smoke')
    await runSmoke({ mountApp })
    return
  }
  mountApp()
  if (shell.failure) toast.error(shell.failure, { id: 'shell-info', duration: 10000 })
}

void start()
