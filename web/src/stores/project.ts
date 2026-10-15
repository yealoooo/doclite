// 当前项目（M1-5）：业务界面的项目切换器、文件目录等业务页都从这里取。
//
// - 项目列表来自 `GET /api/projects`（当前用户能进的未删除项目，含已归档的）。
// - 当前项目只放在这里（Pinia）；localStorage 只记"这个用户上次选的项目"，下次进来时优先选它。
//   读写 localStorage 都包在 try/catch 里（隐私模式、存储被禁用时照常工作，只是记不住）。
// - 选当前项目的顺序：已选的仍能进就不变 → 上次选的（仍能进，已归档的也行）→ 第一个进行中的项目 → 没有。
//   已归档的项目默认不自动选、切换器里默认不显示（design §1.6）。
// - 换了登录用户（退出后别人登录）时自动清空，下次用到时重新加载。
//
// 用法（业务页）：
//   const project = useProjectStore()
//   await project.ensureLoaded()
//   if (!project.current) { /* 显示"没有能进的项目" */ }
//   watch(() => project.currentId, reloadPage)   // 用户在切换器里换了项目

import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'

import { listMyProjects } from '@/api/projects'
import type { MyProject } from '@/api/types'
import { useSessionStore } from '@/stores/session'

const LAST_PROJECT_KEY_PREFIX = 'doclite.lastProject.'

function readLast(userId: string): string | null {
  try {
    return window.localStorage.getItem(LAST_PROJECT_KEY_PREFIX + userId)
  } catch {
    return null
  }
}

function writeLast(userId: string, projectId: string | null) {
  try {
    if (projectId) window.localStorage.setItem(LAST_PROJECT_KEY_PREFIX + userId, projectId)
    else window.localStorage.removeItem(LAST_PROJECT_KEY_PREFIX + userId)
  } catch {
    // 存不了就算了：只是下次不能自动选中上次的项目。
  }
}

export const useProjectStore = defineStore('project', () => {
  const session = useSessionStore()

  /** 当前用户能进的项目（含已归档的），按编码排序。 */
  const projects = ref<MyProject[]>([])
  const currentId = ref<string | null>(null)
  /** idle：还没加载；loading：第一次加载中；ready：已加载；error：加载失败（projects 为空）。 */
  const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
  /** 列表属于哪个用户（换了用户就作废）。 */
  let loadedFor: string | null = null
  let loading: Promise<void> | null = null

  const current = computed(() => projects.value.find((p) => p.id === currentId.value) ?? null)
  const activeProjects = computed(() => projects.value.filter((p) => p.status === 'active'))
  const archivedProjects = computed(() => projects.value.filter((p) => p.status === 'archived'))

  function pickCurrent(userId: string) {
    const list = projects.value
    const has = (id: string | null) => id !== null && list.some((p) => p.id === id)
    if (has(currentId.value)) return
    const last = readLast(userId)
    currentId.value = has(last) ? last : (list.find((p) => p.status === 'active')?.id ?? null)
  }

  async function load() {
    const userId = session.me?.id
    if (!userId) {
      reset()
      return
    }
    if (state.value !== 'ready') state.value = 'loading'
    try {
      const list = await listMyProjects()
      if (session.me?.id !== userId) return // 加载期间换了用户
      projects.value = list
      loadedFor = userId
      state.value = 'ready'
      pickCurrent(userId)
    } catch {
      // 错误已由请求封装提示；切换器上另有"重试"。
      if (session.me?.id !== userId) return
      if (state.value !== 'ready') state.value = 'error'
    }
  }

  /** 第一次用到时加载；已经加载过（同一个用户）就直接返回。 */
  async function ensureLoaded() {
    if (state.value === 'ready' && loadedFor === session.me?.id) return
    await refresh()
  }

  /** 重新拉取项目列表（例如打开切换器时，刚被授权的项目要出现）；当前项目仍能进就不变。 */
  async function refresh() {
    loading ??= load().finally(() => {
      loading = null
    })
    await loading
  }

  /** 切换当前项目，并记住它（下次进来优先选它）。 */
  function select(projectId: string) {
    if (!projects.value.some((p) => p.id === projectId)) return
    currentId.value = projectId
    if (session.me) writeLast(session.me.id, projectId)
  }

  /**
   * 用户在切换器里换项目之前的检查（M3-5：归类页有没保存的著录时先确认）。页面挂载时注册、卸载时注销
   * （`addSwitchGuard` 返回注销函数）；任何一个返回 false 就不换。程序里的切换（打开别的项目里的文件夹等）不走这里。
   */
  const switchGuards = new Set<() => boolean | Promise<boolean>>()

  function addSwitchGuard(guard: () => boolean | Promise<boolean>): () => void {
    switchGuards.add(guard)
    return () => switchGuards.delete(guard)
  }

  async function confirmSwitch(): Promise<boolean> {
    for (const guard of [...switchGuards]) {
      if (!(await guard())) return false
    }
    return true
  }

  function reset() {
    projects.value = []
    currentId.value = null
    state.value = 'idle'
    loadedFor = null
  }

  // 退出登录、换了用户：清空，下次用到时按新用户重新加载。
  watch(
    () => session.me?.id ?? null,
    (id) => {
      if (id !== loadedFor) reset()
    },
  )

  return {
    projects,
    currentId,
    current,
    state,
    activeProjects,
    archivedProjects,
    ensureLoaded,
    refresh,
    select,
    addSwitchGuard,
    confirmSwitch,
    reset,
  }
})
