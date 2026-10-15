// 当前会话：登录的用户和他的菜单。导航、路由守卫、防提权预判都从这里取。

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { fetchMe, fetchMyMenus, login as apiLogin, logout as apiLogout } from '@/api/auth'
import type { Me, MyMenu } from '@/api/types'
import { ADMIN_ROLE_CODE, isSystemMenu, type Operator } from '@/lib/privilege'

/** `path` 是否落在菜单的 `route_path` 下（相等，或是它的子路径）。 */
function pathUnder(path: string, routePath: string): boolean {
  return path === routePath || path.startsWith(routePath.endsWith('/') ? routePath : `${routePath}/`)
}

export const useSessionStore = defineStore('session', () => {
  const me = ref<Me | null>(null)
  const menus = ref<MyMenu[]>([])
  /** 是否已经探测过登录状态（第一次路由跳转时探测）。 */
  const loaded = ref(false)
  let loading: Promise<void> | null = null

  const isAdmin = computed(() => me.value?.roles.some((r) => r.role_code === ADMIN_ROLE_CODE) ?? false)
  const menuCodes = computed(() => new Set(menus.value.map((m) => m.menu_code)))
  const operator = computed<Operator>(() => ({ isAdmin: isAdmin.value, menuCodes: menuCodes.value }))

  /** 能出现在导航里的菜单（有 route_path），按 sort_no 排序。 */
  const navMenus = computed(() =>
    menus.value.filter((m) => m.route_path).sort((a, b) => a.sort_no - b.sort_no),
  )
  const businessMenus = computed(() => navMenus.value.filter((m) => !isSystemMenu(m.menu_code)))
  const systemMenus = computed(() => navMenus.value.filter((m) => isSystemMenu(m.menu_code)))
  const hasSystem = computed(() => systemMenus.value.length > 0)

  /** 业务界面的第一个页面；没有业务菜单时为 null。 */
  const businessHome = computed(() => businessMenus.value[0]?.route_path ?? null)
  /** 系统管理的第一个页面；没有系统管理菜单时为 null。 */
  const systemHome = computed(() => systemMenus.value[0]?.route_path ?? null)
  /** 登录后的默认页面：优先业务界面；一个菜单都没有时为 null。 */
  const homePath = computed(() => businessHome.value ?? systemHome.value)

  /** 路径对应的菜单（取 route_path 最长的匹配）；不在当前用户菜单里时为 undefined。 */
  function menuForPath(path: string): MyMenu | undefined {
    let best: MyMenu | undefined
    for (const m of navMenus.value) {
      const rp = m.route_path!
      if (pathUnder(path, rp) && (!best || rp.length > best.route_path!.length)) best = m
    }
    return best
  }

  function canAccess(path: string): boolean {
    return menuForPath(path) !== undefined
  }

  async function load() {
    try {
      me.value = await fetchMe({ probe: true })
      menus.value = await fetchMyMenus()
    } catch {
      // 没登录（401），或者服务器连不上：都按未登录处理，登录页会再报具体错误。
      me.value = null
      menus.value = []
    }
    loaded.value = true
  }

  /** 第一次调用时探测登录状态；之后直接返回。 */
  async function ensureLoaded() {
    if (loaded.value) return
    loading ??= load().finally(() => {
      loading = null
    })
    await loading
  }

  /** 登录；失败时抛出 ApiError（由登录页显示）。 */
  async function login(username: string, password: string) {
    const user = await apiLogin(username, password)
    const list = await fetchMyMenus()
    me.value = user
    menus.value = list
    loaded.value = true
  }

  /**
   * 退出。失败时（网络不通、服务器出错）抛出 ApiError，不清本地状态：服务端会话可能还有效，
   * HttpOnly Cookie 也还在，不能当作已经退出（扫描工位常是多人共用电脑）。
   */
  async function logout() {
    await apiLogout()
    clear()
  }

  /** 清掉本地会话（会话失效时）。 */
  function clear() {
    me.value = null
    menus.value = []
    loaded.value = true
  }

  /**
   * 重新拉取当前用户和菜单：`isAdmin`、侧栏的姓名和角色来自 `me`，导航和守卫来自 `menus`，两个一起刷新。
   * 调用时机：改了自己的资料或角色、改了某个角色的菜单、接口返回 403 `forbidden` 或 `admin_required`。
   * 会话失效时 401 由请求封装统一处理（跳登录页）；用户变了（同一浏览器的别的标签页换了账号）就整页重进首页。
   */
  async function refreshSession() {
    const [user, list] = await Promise.all([fetchMe(), fetchMyMenus()])
    if (me.value && user.id !== me.value.id) {
      window.location.replace('/')
      return
    }
    me.value = user
    menus.value = list
  }

  return {
    me,
    menus,
    loaded,
    isAdmin,
    menuCodes,
    operator,
    navMenus,
    businessMenus,
    systemMenus,
    hasSystem,
    businessHome,
    systemHome,
    homePath,
    menuForPath,
    canAccess,
    ensureLoaded,
    login,
    logout,
    clear,
    refreshSession,
  }
})
