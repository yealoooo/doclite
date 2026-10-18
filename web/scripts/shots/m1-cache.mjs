// M1-11 本机图片缓存面板的截图：npm run screenshots -- --spec scripts/shots/m1-cache.mjs --base <devshell 的地址>
// （M7-11 起面板在客户端的设置页"本机存储"里，原来用户区的"本机图片缓存"对话框去掉了。）
//
// 和 m1-shell.mjs 一样，以壳模式的地址打开、注入 Wails 桥的模拟（scripts/lib/wails-mock.mjs），让页面以为自己在壳里：
// - shell_info / check_server：已配置服务器、连得上（Windows 的数据目录）；
// - api_fetch：转给 devshell 的 /api（真实 server，要先启动），项目名取自真实的 GET /api/projects；
// - cache_stats / cache_clear / cache_set_limit：在页面里模拟一份缓存（DEMO-A、DEMO-B 用真实的项目 id，
//   另有一个当前账号进不了的项目），清空时从模拟数据里删掉。
// 管理员账号同 m0.mjs（INIT_ADMIN_USERNAME / INIT_ADMIN_PASSWORD，默认 admin / change-me-now）。
//
// 截图：
// - shell-cache-entry：左下角用户区的"设置"入口（浏览器里没有）；
// - shell-cache-panel：用户区 → 设置 → 本机存储里的本机图片缓存，显示总占用、上限、缓存目录、各项目的占用；
// - shell-cache-confirm：点 DEMO-B 那一行的"清空"，确认框；
// - shell-cache-cleared：确认后 DEMO-B 那一行消失，总占用变小，右下角提示释放了多少。

import { installWailsMock } from '../lib/wails-mock.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'

/** 在页面里执行（会被序列化，不能引用外部变量）。 */
function cacheMock() {
  const MB = 1024 * 1024
  let cache = null

  async function buildCache() {
    let projects = []
    try {
      const res = await fetch('/api/projects')
      if (res.ok) projects = await res.json()
    } catch {
      // 取不到就只有"其他项目"那一行。
    }
    const a = projects.find((p) => p.project_code === 'DEMO-A') ?? projects[0]
    const b = projects.find((p) => p.project_code === 'DEMO-B') ?? projects[1]
    const list = []
    if (a) list.push({ project_id: a.id, total_bytes: Math.round(196.3 * MB), file_count: 2060, cover_count: 2048, original_count: 12 })
    if (b && b !== a) list.push({ project_id: b.id, total_bytes: Math.round(6.1 * MB), file_count: 22, cover_count: 20, original_count: 2 })
    // 当前账号进不了的项目（例如别的账号在这台电脑上看过）。
    list.push({ project_id: '0199b6f2-1c3e-7a10-9f00-5a1e2c3d4e5f', total_bytes: Math.round(9.8 * MB), file_count: 410, cover_count: 410, original_count: 0 })
    return {
      dir: 'C:\\Users\\scan01\\AppData\\Local\\Doclite\\image-cache',
      available: true,
      error: null,
      limit_bytes: 2 * 1024 * MB,
      projects: list,
      counters: { hits: 1830, misses: 252, downloads: 252, download_bytes: Math.round(31.5 * MB), rejected: 0, errors: 0 },
    }
  }

  function withTotals(c) {
    const projects = [...c.projects].sort((x, y) => y.total_bytes - x.total_bytes)
    return {
      ...c,
      projects,
      total_bytes: projects.reduce((s, p) => s + p.total_bytes, 0),
      file_count: projects.reduce((s, p) => s + p.file_count, 0),
    }
  }

  Object.assign(window.__shell.commands, {
    async cache_stats() {
      cache ??= await buildCache()
      // 慢一点，看得到加载的样子。
      await new Promise((r) => setTimeout(r, 100))
      return withTotals(cache)
    },
    async cache_clear(args) {
      cache ??= await buildCache()
      const pid = args.projectId
      const removed = cache.projects.filter((p) => pid == null || p.project_id === pid)
      cache.projects = cache.projects.filter((p) => !removed.includes(p))
      await new Promise((r) => setTimeout(r, 300))
      return {
        removed_files: removed.reduce((s, p) => s + p.file_count, 0),
        removed_bytes: removed.reduce((s, p) => s + p.total_bytes, 0),
        failed_files: 0,
      }
    },
    async cache_set_limit(args) {
      cache ??= await buildCache()
      cache.limit_bytes = args.limitBytes
      return withTotals(cache)
    },
  })
}

const mock = (page) => installWailsMock(page, { serverUrl: 'http://127.0.0.1:9080' }, cacheMock)

/** 登录，从用户区进设置页，等本机图片缓存里各项目的占用出来，滚到缓存那一块。 */
async function openPanel(page) {
  await page.type('#username', admin)
  await page.type('#password', adminPassword)
  await page.click('[data-testid=login-submit]')
  await page.waitForSelector('[data-testid=side-nav]')
  await page.click('[data-testid=user-area] [data-testid=open-settings]')
  await page.waitForSelector('[data-testid=settings-cache] [data-testid=cache-project-row]')
  await page.$eval('[data-testid=settings-cache]', (el) => el.scrollIntoView({ block: 'start' }))
}

/** 点 DEMO-B 那一行的"清空"，等确认框。 */
async function askClearDemoB(page) {
  const pid = await page.evaluate(async () => {
    const res = await fetch('/api/projects')
    const list = await res.json()
    return (list.find((p) => p.project_code === 'DEMO-B') ?? list[1])?.id
  })
  if (!pid) throw new Error('找不到 DEMO-B（先运行 npm run seed）')
  await page.click(`[data-testid=cache-project-row][data-project-id="${pid}"] [data-testid=cache-clear-project]`)
  await page.waitForSelector('[role=alertdialog]')
  return pid
}

export default {
  out: '../docs/screenshots/m1',
  accounts: {},
  shots: [
    {
      // 左下角用户区的"设置"入口（只在壳里出现）。
      name: 'shell-cache-entry',
      path: '/login',
      shell: true,
      init: mock,
      waitFor: '#username',
      async prepare(page) {
        await page.type('#username', admin)
        await page.type('#password', adminPassword)
        await page.click('[data-testid=login-submit]')
        await page.waitForSelector('[data-testid=side-nav] [data-testid=open-settings]')
        await page.hover('[data-testid=side-nav] [data-testid=open-settings]')
      },
    },
    {
      name: 'shell-cache-panel',
      path: '/login',
      shell: true,
      init: mock,
      waitFor: '#username',
      async prepare(page) {
        await openPanel(page)
        const rows = await page.$$eval('[data-testid=cache-project-row]', (els) => els.length)
        if (rows !== 3) throw new Error(`应该有 3 个项目的占用，实际 ${rows} 个`)
        const total = await page.$eval('[data-testid=cache-total]', (el) => el.textContent.trim())
        if (total !== '212 MB') throw new Error(`总占用应该是 212 MB，实际 ${total}`)
      },
      delay: 500,
    },
    {
      name: 'shell-cache-confirm',
      path: '/login',
      shell: true,
      init: mock,
      waitFor: '#username',
      async prepare(page) {
        await openPanel(page)
        await askClearDemoB(page)
      },
      delay: 500,
    },
    {
      name: 'shell-cache-cleared',
      path: '/login',
      shell: true,
      init: mock,
      waitFor: '#username',
      async prepare(page) {
        await openPanel(page)
        const pid = await askClearDemoB(page)
        const confirm = await page.waitForSelector('[role=alertdialog] button.bg-destructive, [role=alertdialog] button:last-of-type')
        await confirm.click()
        await page.waitForFunction(
          (id) => !document.querySelector(`[data-testid=cache-project-row][data-project-id="${id}"]`),
          { timeout: 5000 },
          pid,
        )
        await page.waitForFunction(() => document.body.innerText.includes('已清空'), { timeout: 5000 })
        const rows = await page.$$eval('[data-testid=cache-project-row]', (els) => els.length)
        if (rows !== 2) throw new Error(`清空后应该剩 2 个项目，实际 ${rows} 个`)
        const total = await page.$eval('[data-testid=cache-total]', (el) => el.textContent.trim())
        if (total !== '206 MB') throw new Error(`清空 DEMO-B 后总占用应该是 206 MB，实际 ${total}`)
      },
      delay: 600,
    },
  ],
}
