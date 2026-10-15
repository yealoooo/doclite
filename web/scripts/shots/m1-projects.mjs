// M1-5 的截图清单：项目列表、新建项目、授权对话框、项目切换器、没有能进的项目时的空状态。
//
//   INIT_ADMIN_PASSWORD=admin123456 npm run screenshots -- --spec scripts/shots/m1-projects.mjs --base <前端地址>
//
// 前端地址可以是 vite dev / preview（/api 代理到 server），也可以是托管了 web/dist 的 server 本身。
// 数据来自 `npm run seed:demo`（M1-5 起建两个演示项目 DEMO-A、DEMO-B 和已停用的 demo_disabled）：
// - admin 能进所有项目；demo_scanner 只能进 DEMO-A；demo_noproject 一个都不能进（M3-5 起 demo_cataloger 授权进了 DEMO-A，
//   "没有能进的项目"改用 seed:demo 建的 demo_noproject）；
// - DEMO-B 授权给审核员角色，授权对话框里能看到这个角色带进的人（含已停用的 demo_disabled）。
// 管理员账号同 m0.mjs（INIT_ADMIN_USERNAME / INIT_ADMIN_PASSWORD，默认 admin / change-me-now）。

const demo = 'demo123456'
const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'

/** 点开业务界面侧栏的项目切换器，等选项出现。 */
async function openSwitcher(page) {
  await page.click('[data-testid=project-switcher-trigger]')
  await page.waitForSelector('[data-testid=project-option]')
}

/** 点表格里某个项目那一行的某个按钮（按文字找）。 */
async function clickRowButton(page, code, text) {
  const buttons = await page.$$(`tr[data-code="${code}"] button`)
  for (const b of buttons) {
    if ((await b.evaluate((el) => el.textContent?.trim())) === text) {
      await b.click()
      return
    }
  }
  throw new Error(`找不到 ${code} 那一行的"${text}"按钮`)
}

export default {
  out: '../docs/screenshots/m1',
  accounts: {
    [admin]: adminPassword,
    demo_scanner: demo,
    demo_noproject: demo,
  },
  shots: [
    // 系统管理 · 项目（admin）。
    { name: 'projects-list', user: admin, path: '/system/projects', waitFor: 'tr[data-code="DEMO-A"]' },
    {
      name: 'projects-create',
      user: admin,
      path: '/system/projects',
      waitFor: 'tr[data-code="DEMO-A"]',
      async prepare(page) {
        await page.click('[data-testid=project-create]')
        await page.waitForSelector('#pf-code')
        await page.type('#pf-code', 'WS2026')
        await page.type('#pf-name', '市档案馆 2026 文书数字化')
        await page.type('#pf-desc', '第一批：2026 年度文书档案，约 3 万页')
      },
      delay: 400,
    },
    {
      name: 'projects-access',
      user: admin,
      path: '/system/projects',
      waitFor: 'tr[data-code="DEMO-B"]',
      async prepare(page) {
        await clickRowButton(page, 'DEMO-B', '授权')
        await page.waitForSelector('[data-testid=role-members]')
      },
      delay: 400,
    },

    // 业务界面的项目切换器：admin 能进所有项目，demo_scanner 只看得到 DEMO-A。
    {
      name: 'switcher-admin',
      user: admin,
      path: '/dashboard',
      // M3-7 起 /dashboard 常开一条 SSE 连接，等不到 networkidle0。
      waitUntil: 'networkidle2',
      waitFor: '[data-testid=project-current]',
      prepare: openSwitcher,
      delay: 400,
    },
    {
      name: 'switcher-scanner',
      user: 'demo_scanner',
      path: '/files',
      // M1-10 起 /files 常开一条 SSE 连接，等不到 networkidle0。
      waitUntil: 'networkidle2',
      waitFor: '[data-testid=project-current]',
      prepare: openSwitcher,
      delay: 400,
    },
    // 没有能进的项目时的空状态（demo_noproject，归类员角色、不授权任何项目）。
    { name: 'switcher-empty', user: 'demo_noproject', path: '/files', waitFor: '[data-testid=project-empty]' },
  ],
}
