// 壳的启动状态：还没配置服务器、连不上、版本过低时整页显示对应的提示（ShellGate），不进入路由。
// 浏览器访问时一开始就是 ready，这些页面都不会出现。
//
// 状态：
// - checking：正在探测服务器；
// - setup：还没有服务器地址，或用户要修改地址（"连接服务器"页）；
// - unreachable：连不上已配置的服务器（"重试 / 修改地址"）；
// - upgrade：壳的版本低于服务器要求的最低版本（启动探测发现，或任何请求返回 426）；
// - ready：正常使用。路由守卫在这之前一直等待（whenReady）。

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { isTauri } from '@/lib/client'
import {
  checkServer,
  setServer,
  shellErrorMessage,
  shellInfo,
  type ProbeResult,
  type ShellInfo,
} from '@/lib/shell'

export type ShellPhase = 'checking' | 'setup' | 'unreachable' | 'upgrade' | 'ready'

/** 升级提示显示的信息（426 的 details 或启动探测的结果，design §5）。 */
export interface UpgradeInfo {
  client_version: string
  min_version: string | null
  latest_version: string | null
  download_url: string | null
}

function str(v: unknown): string | null {
  return typeof v === 'string' && v !== '' ? v : null
}

export const useShellStore = defineStore('shell', () => {
  const inShell = isTauri()
  const phase = ref<ShellPhase>(inShell ? 'checking' : 'ready')
  const info = ref<ShellInfo | null>(null)
  /** 最近一次探测的结果（连不上时显示原因）。 */
  const probe = ref<ProbeResult | null>(null)
  /** 壳内部出错（命令调用失败）时的说明。 */
  const failure = ref('')
  const upgrade = ref<UpgradeInfo | null>(null)
  /** 是否已经进入过 ready（进入过之后再离开，回来时要整页重载，页面状态可能已经不对）。 */
  let wasReady = false
  let phaseBeforeEdit: ShellPhase = 'checking'

  /** 界面的版本：壳里是壳的版本，浏览器里是构建时的版本（两者来自同一个 package.json）。 */
  const version = computed(() => info.value?.version ?? __APP_VERSION__)
  const serverUrl = computed(() => info.value?.server_url ?? null)

  let waiters: (() => void)[] = []
  function setPhase(p: ShellPhase) {
    phase.value = p
    if (p === 'ready') {
      wasReady = true
      const list = waiters
      waiters = []
      list.forEach((resolve) => resolve())
    }
  }

  /** 等到可以正常使用（路由守卫用）。 */
  function whenReady(): Promise<void> {
    if (phase.value === 'ready') return Promise.resolve()
    return new Promise((resolve) => waiters.push(resolve))
  }

  /** 读取壳的信息；失败时返回 null（这时显示"连不上"页和原因）。 */
  async function loadInfo(): Promise<ShellInfo | null> {
    if (!inShell) return null
    if (info.value) return info.value
    try {
      info.value = await shellInfo()
    } catch (e) {
      failure.value = `壳内部出错：${shellErrorMessage(e)}`
      setPhase('unreachable')
    }
    return info.value
  }

  /** 启动：没有服务器地址就进"连接服务器"页，否则探测服务器。 */
  async function init() {
    if (!inShell) return
    const i = await loadInfo()
    if (!i || i.smoke) return
    if (!i.server_url) {
      setPhase('setup')
      return
    }
    await check()
  }

  /** 探测已配置的服务器，决定进入 ready、unreachable 还是 upgrade。 */
  async function check() {
    setPhase('checking')
    failure.value = ''
    try {
      probe.value = await checkServer()
    } catch (e) {
      probe.value = null
      failure.value = shellErrorMessage(e)
      setPhase('unreachable')
      return
    }
    const p = probe.value
    if (!p.reachable) {
      setPhase('unreachable')
    } else if (p.upgrade_required) {
      upgrade.value = {
        client_version: p.client_version,
        min_version: p.min_version,
        latest_version: p.latest_version,
        download_url: p.download_url,
      }
      setPhase('upgrade')
    } else {
      setPhase('ready')
    }
  }

  /** 重试（连不上页、升级页）：已经用过一阵的页面整页重载，否则重新探测。 */
  async function retry() {
    if (wasReady) {
      window.location.replace('/')
      return
    }
    await check()
  }

  /** 任何请求返回 426 时调用（http.ts 的全局处理）。浏览器里不会出现。 */
  function requireUpgrade(details: unknown) {
    if (!inShell) return
    const d = (typeof details === 'object' && details !== null ? details : {}) as Record<string, unknown>
    upgrade.value = {
      client_version: str(d.client_version) ?? version.value,
      min_version: str(d.min_version),
      latest_version: str(d.latest_version),
      download_url: str(d.download_url),
    }
    setPhase('upgrade')
  }

  /** 进入"连接服务器"页修改地址（设置、登录页、连不上页、升级页）。 */
  function editServer() {
    if (!inShell) return
    if (phase.value !== 'setup') phaseBeforeEdit = phase.value
    setPhase('setup')
  }

  /** 放弃修改地址，回到之前的状态。 */
  function cancelEdit() {
    setPhase(phaseBeforeEdit === 'setup' ? 'checking' : phaseBeforeEdit)
    if (phaseBeforeEdit === 'setup' || phaseBeforeEdit === 'checking') void check()
  }

  /** 保存服务器地址后整页重载：地址变了会话已被清空，所有状态从头开始。 */
  async function saveServer(url: string) {
    await setServer(url)
    window.location.replace('/')
  }

  /** 自检时直接进入 ready（探测由自检自己做）。 */
  function markReady() {
    setPhase('ready')
  }

  return {
    inShell,
    phase,
    info,
    probe,
    failure,
    upgrade,
    version,
    serverUrl,
    whenReady,
    loadInfo,
    init,
    check,
    retry,
    requireUpgrade,
    editServer,
    cancelEdit,
    saveServer,
    markReady,
  }
})
