// 退出登录（侧栏的用户区、设置页的"账号"共用）。退出失败时服务端会话可能还有效（Cookie 也还在），不能当作已退出：
// 留在当前页，提示重试。

import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import { useSessionStore } from '@/stores/session'

export function useLogout() {
  const session = useSessionStore()
  const router = useRouter()
  const loggingOut = ref(false)

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

  return { loggingOut, logout }
}
