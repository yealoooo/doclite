// 壳（桌面客户端）的信息：版本、界面版本、服务器地址、能力。浏览器里 inShell 为 false，其余都是空的。
//
// 连接服务器、连不上、需要升级几页是客户端自带的（client/internal/shellpages，约定见 client/BRIDGE.md）：启动时客户端
// 先在它自己的关口页上探测好服务器才进界面，所以界面里没有这些页面，只在需要时跳过去（editServer、requireUpgrade）。

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { isShell, shellHas, type ShellCapability } from '@/lib/client'
import {
  editServerAddress,
  shellErrorMessage,
  shellInfo,
  shellPagesAvailable,
  showUpgradePage,
  type ShellInfo,
} from '@/lib/shell'

export const useShellStore = defineStore('shell', () => {
  const inShell = isShell()
  const info = ref<ShellInfo | null>(null)
  /** 读壳的信息失败时的说明。 */
  const failure = ref('')

  /** 界面（web）的版本：构建时的 package.json。客户端里就是客户端嵌入的那份 web 产物的版本。 */
  const webVersion = __APP_VERSION__
  /** 客户端的版本（壳里；浏览器里为 null）。 */
  const clientVersion = computed(() => info.value?.version ?? null)
  const serverUrl = computed(() => info.value?.server_url ?? null)

  /** 读取壳的信息（启动时 main.ts 先读，之后页面按能力显示本机功能）；失败时返回 null。 */
  async function loadInfo(): Promise<ShellInfo | null> {
    if (!inShell) return null
    if (info.value) return info.value
    try {
      info.value = await shellInfo()
    } catch (e) {
      failure.value = `读客户端的信息失败：${shellErrorMessage(e)}`
    }
    return info.value
  }

  /** 客户端有这项能力（浏览器里总是 false）。 */
  function has(cap: ShellCapability): boolean {
    return shellHas(cap)
  }

  /** 能不能在网页里改服务器地址（客户端自带的修改地址页）。 */
  const canEditServer = computed(() => inShell && info.value !== null && shellPagesAvailable())

  /** 去客户端自带的修改地址页（关于、登录页）。 */
  function editServer() {
    if (canEditServer.value) editServerAddress()
  }

  /**
   * 任何请求返回 426（http.ts 的全局处理）：去客户端自带的升级页（它自己探测）。客户端没有这一页时返回 false，
   * 调用方自己提示。浏览器里不会出现。
   */
  function requireUpgrade(): boolean {
    if (!inShell || !shellPagesAvailable()) return false
    showUpgradePage()
    return true
  }

  return {
    inShell,
    info,
    failure,
    webVersion,
    clientVersion,
    serverUrl,
    canEditServer,
    loadInfo,
    has,
    editServer,
    requireUpgrade,
  }
})
