// 托盘菜单上"继续热文件夹"失败时（例如监控目录不在了、服务器换了），壳打开主窗口并发 `tray:error {message}`
// （shell/src-tauri/src/tray.rs）。在登录后的外框（AppLayout）里订阅，不管当前在哪个页面都弹出原因；
// 不在扫描上传页、又有这个菜单时，提示上带"去扫描上传"。只在壳里订阅，组件卸载时取消。

import { onBeforeUnmount, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import { isTauri } from '@/lib/client'
import { listenQueueEvents } from '@/lib/scan-queue'
import { useSessionStore } from '@/stores/session'

const SCAN_PATH = '/scan'

export function useTrayErrors() {
  if (!isTauri()) return
  const router = useRouter()
  const session = useSessionStore()
  let stop: (() => void) | null = null
  let alive = true

  function show(message: string) {
    const elsewhere = router.currentRoute.value.path !== SCAN_PATH && session.canAccess(SCAN_PATH)
    toast.error(message, {
      id: 'tray-error',
      duration: 10_000,
      action: elsewhere ? { label: '去扫描上传', onClick: () => void router.push(SCAN_PATH) } : undefined,
    })
  }

  onMounted(async () => {
    try {
      const unlisten = await listenQueueEvents({ trayError: show })
      if (alive) stop = unlisten
      else unlisten()
    } catch {
      // 订阅不了（壳的权限不对）：只是看不到托盘的出错提示，/scan 页的热文件夹状态照常。
    }
  })

  onBeforeUnmount(() => {
    alive = false
    stop?.()
    stop = null
  })
}
