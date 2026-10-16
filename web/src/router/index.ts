// 路由表和路由守卫。
//
// 约定：
// - 导航完全由 `/api/me/menus` 生成；这里只登记页面组件。需要菜单权限的路由写 `meta.requiresMenu`，
//   守卫按路径匹配菜单的 `route_path`（相等或是它的子路径），不在当前用户菜单里就不能进入。
// - 业务路由是 design §7 的 `/dashboard`、`/files`、`/scan`、`/review`、`/review-assign`、`/catalog`、
//   `/export`；系统管理在 `/system/*` 下，和 `system.*` 菜单的 `route_path` 一一对应。
//   还没实现的页面先用占位页，实现时把 component 换掉即可。
// - 没登录跳 `/login?redirect=原路径`，登录后回到原路径；已登录访问 `/login` 跳走。
// - 没有权限显示 `/forbidden`（带 `from`），那里提供"前往第一个有权限的页面"。

import { createRouter, createWebHistory, type RouteLocationRaw } from 'vue-router'

import AppLayout from '@/layouts/AppLayout.vue'
import { safeRedirect } from '@/lib/redirect'
import { useSessionStore } from '@/stores/session'
import ForbiddenView from '@/views/ForbiddenView.vue'
import LoginView from '@/views/LoginView.vue'

declare module 'vue-router' {
  interface RouteMeta {
    /** 不需要登录（只有登录页）。 */
    public?: boolean
    /** 需要当前用户的菜单里有这个路径对应的菜单。 */
    requiresMenu?: boolean
    /** 不在菜单里的页面的标题（浏览器标题栏用；菜单里的页面用菜单名）。 */
    title?: string
  }
}

const PlaceholderView = () => import('@/views/PlaceholderView.vue')

/** 系统管理下还没实现的页面。 */
const SYSTEM_PLACEHOLDER_PATHS = ['plugins']

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/login', name: 'login', component: LoginView, meta: { public: true } },
    {
      path: '/',
      component: AppLayout,
      children: [
        // 登录后的默认页：守卫跳到第一个有权限的菜单；一个菜单都没有时停在这里显示无权提示。
        { path: '', name: 'home', component: ForbiddenView },
        // /system：跳到第一个有权限的系统管理菜单。
        { path: 'system', name: 'system-home', component: ForbiddenView },
        {
          path: 'system/users',
          component: () => import('@/views/system/UsersView.vue'),
          meta: { requiresMenu: true },
        },
        {
          path: 'system/roles',
          component: () => import('@/views/system/RolesView.vue'),
          meta: { requiresMenu: true },
        },
        {
          path: 'system/storage',
          component: () => import('@/views/system/StorageView.vue'),
          meta: { requiresMenu: true },
        },
        // M1-5：项目和项目授权。
        {
          path: 'system/projects',
          component: () => import('@/views/system/ProjectsView.vue'),
          meta: { requiresMenu: true },
        },
        // M1-10：文件目录（当前文件夹在 ?folder=<id>）。
        {
          path: 'files',
          component: () => import('@/views/files/FilesView.vue'),
          meta: { requiresMenu: true },
        },
        // M1-13：扫描上传（热文件夹和上传队列只在壳里；浏览器里引导到文件目录上传）。
        {
          path: 'scan',
          component: () => import('@/views/scan/ScanView.vue'),
          meta: { requiresMenu: true },
        },
        // M2-5：审核分配（当前文件夹在 ?folder=<id>）。
        {
          path: 'review-assign',
          component: () => import('@/views/review-assign/ReviewAssignView.vue'),
          meta: { requiresMenu: true },
        },
        // M2-6：审核工作台（范围缩到某个文件夹子树时在 ?folder=<id>）。
        {
          path: 'review',
          component: () => import('@/views/review/ReviewView.vue'),
          meta: { requiresMenu: true },
        },
        // M3-5：归类（归类树和条目著录；当前节点、条目在 ?category=<id>&item=<id>）。
        {
          path: 'catalog',
          component: () => import('@/views/catalog/CatalogView.vue'),
          meta: { requiresMenu: true },
        },
        // M3-7：项目看板（审核状态 × 归类状态的矩阵、KPI、文件夹进度表）。
        {
          path: 'dashboard',
          component: () => import('@/views/dashboard/DashboardView.vue'),
          meta: { requiresMenu: true },
        },
        // M4-5：导出交付（任务列表、新建导出和预检、明细、交付后变更；带范围进来见 lib/export.ts 的地址约定）。
        {
          path: 'export',
          component: () => import('@/views/export/ExportView.vue'),
          meta: { requiresMenu: true },
        },
        // M7-1：许可证（状态、install_id、导入）。
        {
          path: 'system/license',
          component: () => import('@/views/system/LicenseView.vue'),
          meta: { requiresMenu: true },
        },
        // M7-3：审计日志（只读列表，按时间、操作人、项目、对象、操作筛选）。
        {
          path: 'system/audit',
          component: () => import('@/views/system/AuditView.vue'),
          meta: { requiresMenu: true },
        },
        ...SYSTEM_PLACEHOLDER_PATHS.map((p) => ({
          path: `system/${p}`,
          component: PlaceholderView,
          meta: { requiresMenu: true },
        })),
        // M7-11：客户端的设置页（服务器、账号、本机存储、热文件夹、关于）。不是菜单，登录了就能进；只在客户端里有
        // 入口，浏览器里打开只显示一句说明。
        {
          path: 'settings',
          name: 'settings',
          component: () => import('@/views/settings/SettingsView.vue'),
          meta: { title: '设置' },
        },
        { path: 'forbidden', name: 'forbidden', component: ForbiddenView },
        {
          path: ':pathMatch(.*)*',
          name: 'not-found',
          component: () => import('@/views/NotFoundView.vue'),
        },
      ],
    },
  ],
})

router.beforeEach(async (to): Promise<boolean | RouteLocationRaw> => {
  const session = useSessionStore()
  await session.ensureLoaded()

  if (to.meta.public) {
    if (!session.me) return true
    return safeRedirect(to.query.redirect) ?? session.homePath ?? { name: 'home' }
  }

  if (!session.me) {
    return { name: 'login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath } }
  }

  if (to.name === 'home' && session.homePath) return session.homePath
  if (to.name === 'system-home') {
    return session.systemHome ?? { name: 'forbidden', query: { from: to.fullPath } }
  }
  if (to.meta.requiresMenu && !session.canAccess(to.path)) {
    return { name: 'forbidden', query: { from: to.fullPath } }
  }
  return true
})

router.afterEach((to) => {
  const session = useSessionStore()
  const menu = session.menuForPath(to.path)
  const title = menu?.menu_name ?? to.meta.title
  document.title = title ? `${title} · Doclite` : 'Doclite'
})

export default router
