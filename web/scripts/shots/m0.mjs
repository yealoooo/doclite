// M0-5 的截图清单：npm run screenshots -- --spec scripts/shots/m0.mjs
// 账号来自 scripts/seed.mjs 建的演示用户，另加 server 首次启动时建的初始管理员：
// 登录名和密码取环境变量 INIT_ADMIN_USERNAME、INIT_ADMIN_PASSWORD（和启动 server 用的是同一组变量），
// 默认 admin / change-me-now（和 server/.env.example 一致）；也可以用 --account 登录名:密码 覆盖。
// 例如：INIT_ADMIN_PASSWORD=admin123456 npm run screenshots -- --spec scripts/shots/m0.mjs
//
// 每一项：name（文件名，不含 .png）、user（登录账号，省略表示未登录）、path（打开的地址）、
// waitFor（等到这个选择器出现）、prepare(page)（截图前的操作，例如悬停、点击）、delay（额外等待毫秒）。

const demo = 'demo123456'
const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'

export default {
  out: '../docs/screenshots/m0',
  accounts: {
    [admin]: adminPassword,
    demo_admin: demo,
    demo_manager: demo,
    demo_scanner: demo,
    demo_reviewer: demo,
    demo_cataloger: demo,
    demo_useradmin: demo,
  },
  shots: [
    { name: 'login', path: '/login', waitFor: '#username' },

    // 业务导航：admin 有"系统管理"入口，其他内置角色菜单各不相同、没有入口。
    // M3-7 起 /dashboard 是真实页面（项目看板，常开 SSE），看导航的用意不变。
    {
      name: 'admin-nav',
      user: admin,
      path: '/dashboard',
      waitFor: '[data-testid=dashboard-page]',
      waitUntil: 'networkidle2',
    },
    {
      name: 'manager-nav',
      user: 'demo_manager',
      path: '/dashboard',
      waitFor: '[data-testid=dashboard-page]',
      waitUntil: 'networkidle2',
    },
    // M1-10 起 /files 是真实页面（常开 SSE），这三张改开各自还是占位页的菜单，截图的用意（看导航）不变。
    // M1-13 起 /scan 也是真实页面（浏览器里是"热文件夹只能在客户端里用"的说明，不开 SSE），scanner 这张照旧开 /scan。
    // M2-7 起 /scan 的待重扫列表常开 SSE（M3-7 回归时发现这张等不到 networkidle0）。
    {
      name: 'scanner-nav',
      user: 'demo_scanner',
      path: '/scan',
      waitFor: '[data-testid=scan-browser-notice]',
      waitUntil: 'networkidle2',
    },
    // M2-6 起 /review 是真实页面（审核工作台，常开 SSE），看导航的用意不变。
    {
      name: 'reviewer-nav',
      user: 'demo_reviewer',
      path: '/review',
      waitFor: '[data-testid=review-page][data-list=ready]',
      waitUntil: 'networkidle2',
    },
    // M3-5 起 /catalog 是真实页面（归类，常开 SSE）；demo_cataloger 没有能进的项目时显示"没有可进入的项目"。
    {
      name: 'cataloger-nav',
      user: 'demo_cataloger',
      path: '/catalog',
      waitFor: '[data-testid=catalog-page]',
      waitUntil: 'networkidle2',
    },

    // 路由守卫：扫描员直接打开 /system/users。
    { name: 'scanner-forbidden', user: 'demo_scanner', path: '/system/users', waitFor: '[data-testid=forbidden]' },

    // 系统管理（admin）。
    { name: 'admin-users', user: admin, path: '/system/users', waitFor: '[data-username]' },
    { name: 'admin-roles', user: admin, path: '/system/roles?role=reviewer', waitFor: '[data-testid=menu-tree]' },
    { name: 'admin-roles-locked', user: admin, path: '/system/roles?role=admin', waitFor: '[data-testid=menu-tree]' },

    // 系统管理（非 admin，有 system.users + system.roles 的"用户管理员"）：被禁用的操作。
    {
      name: 'useradmin-users',
      user: 'demo_useradmin',
      path: '/system/users',
      // 锁图标在角色列表加载完之后才出现，这时按钮的禁用原因才是防提权规则。
      waitFor: '[data-username=demo_admin] [data-testid=row-lock]',
      async prepare(page) {
        await page.hover('[data-username=demo_admin] [data-blocked]')
      },
      delay: 500,
    },
    {
      name: 'useradmin-assign-roles',
      user: 'demo_useradmin',
      path: '/system/users',
      waitFor: '[data-username=demo_admin] [data-testid=row-lock]',
      async prepare(page) {
        const buttons = await page.$$('[data-username=demo_scanner] button')
        for (const b of buttons) {
          if ((await b.evaluate((el) => el.textContent?.trim())) === '角色') {
            await b.click()
            break
          }
        }
        await page.waitForSelector('[role=dialog] [data-role=admin]')
      },
      delay: 400,
    },
    { name: 'useradmin-roles', user: 'demo_useradmin', path: '/system/roles?role=user_admin', waitFor: '[data-testid=menu-tree]' },
  ],
}
