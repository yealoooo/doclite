// M7-1 许可证的截图：npm run screenshots -- --spec scripts/shots/m7-license.mjs --base <server 地址>
//
//   INIT_ADMIN_PASSWORD=admin123456 npm run screenshots -- --spec scripts/shots/m7-license.mjs --base http://127.0.0.1:5173
//
// 要一个开发构建的 server（没有编进许可证公钥，不检查许可证）和 npm run seed 的演示账号。开发构建里许可证不会挡人，
// 所以登录页的三种错误、快到期的提醒条、导入出错、到期后被退回登录页都用请求拦截模拟服务器的响应（页面代码不改）：
//   - license-dev：许可证页的真实状态（开发版，不检查；install_id 可复制，没有导入区）；
//   - login-seats-full、login-expired、login-missing：登录页对三个错误码的中文说明；
//   - license-expiring：30 天内到期——顶部的提醒条（只给管理员）和许可证页；
//   - license-import-error：导入时 install_id 不符，在导入区里说明；
//   - expired-redirect：已登录的扫描员在许可证到期后点菜单，提示原因并回到登录页。

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const scanner = 'demo_scanner'
const demoPassword = process.env.DEMO_PASSWORD ?? 'demo123456'

const BASE = (() => {
  const i = process.argv.indexOf('--base')
  const v = i > 0 ? process.argv[i + 1] : (process.env.DOCLITE_WEB ?? 'http://127.0.0.1:5173')
  return v.replace(/\/$/, '')
})()

function expect(cond, message) {
  if (!cond) throw new Error(message)
}

/** 页面之外登录（node 的 fetch），返回会话 Cookie 的值和当前用户。 */
async function login(username, password) {
  const res = await fetch(`${BASE}/api/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  if (!res.ok) throw new Error(`登录 ${username} 失败：HTTP ${res.status}`)
  const m = res.headers.get('set-cookie')?.match(/doclite_session=([^;]+)/)
  if (!m) throw new Error('登录响应没有会话 Cookie')
  return { token: m[1], me: await res.json() }
}

/** 已登录的浏览器上下文：把会话 Cookie 放进去。 */
async function useSession(page, username, password) {
  const { token, me } = await login(username, password)
  await page.setCookie({ name: 'doclite_session', value: token, url: BASE, httpOnly: true, sameSite: 'Lax' })
  return me
}

const json = (status, body) => ({
  status,
  contentType: 'application/json; charset=utf-8',
  headers: { 'Cache-Control': 'no-store' },
  body: JSON.stringify(body),
})

/** 拦截请求：route(method, path) 返回 respond 的参数时用它回答，否则照常发给服务器。 */
async function intercept(page, route) {
  await page.setRequestInterception(true)
  page.on('request', (req) => {
    if (req.isInterceptResolutionHandled()) return
    const u = new URL(req.url())
    const r = u.origin === new URL(BASE).origin ? route(req.method(), u.pathname) : null
    if (r) req.respond(r).catch(() => {})
    else req.continue().catch(() => {})
  })
}

async function texts(page, selector) {
  return page.$$eval(selector, (els) => els.map((e) => e.textContent?.trim() ?? ''))
}

const INSTALL_ID = 'sha256:7f3c2b9e0d4a61c58e2f1b7a9c0d3e5f6a8b1c2d3e4f5a6b7c8d9e0f1a2b3c4d'
const DAY = 24 * 3600 * 1000

function licenseState(status, over = {}) {
  const now = Date.now()
  const lic =
    status === 'missing'
      ? null
      : {
          license_id: '0198f5a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b',
          company: '某市档案馆',
          edition: 'enterprise',
          max_users: 20,
          issued_at: new Date(now - 353 * DAY).toISOString(),
          expires_at: new Date(now + 12 * DAY + 3600 * 1000).toISOString(),
        }
  return {
    status,
    install_id: INSTALL_ID,
    license: lic,
    days_left: lic ? 13 : null,
    online_sessions: 14,
    invalid_reason: null,
    imported_at: lic ? new Date(now - 350 * DAY).toISOString() : null,
    imported_by: lic ? '系统管理员' : null,
    ...over,
  }
}

/** 登录页的一种错误：拦截登录接口，填表提交，等错误说明出现。 */
function loginErrorShot(name, code, message, details, mustInclude) {
  return {
    name,
    path: '/login',
    waitFor: '#username',
    async init(page) {
      await intercept(page, (method, path) =>
        method === 'POST' && path === '/api/auth/login' ? json(403, { code, message, details }) : null,
      )
    },
    async prepare(page) {
      await page.type('#username', 'demo_scanner')
      await page.type('#password', 'demo123456')
      await page.click('[data-testid=login-submit]')
      await page.waitForSelector(`[data-testid=login-error][data-code=${code}]`)
      const [text] = await texts(page, '[data-testid=login-error]')
      expect(text.includes(mustInclude), `${code} 的说明：${text}`)
    },
  }
}

export default {
  out: '../docs/screenshots/m7',
  accounts: { [admin]: adminPassword },
  shots: [
    {
      name: 'license-dev',
      user: admin,
      path: '/system/license',
      waitFor: '[data-testid=license-status][data-status=dev]',
      async prepare(page) {
        const [title] = await texts(page, '[data-testid=license-title]')
        expect(title.includes('开发版'), `标题：${title}`)
        const [id] = await texts(page, '[data-testid=install-id]')
        expect(/^sha256:[0-9a-f]{64}$/.test(id), `install_id：${id}`)
        expect((await page.$('[data-testid=license-import]')) === null, '开发版不该有导入区')
        expect((await page.$('[data-testid=license-banner]')) === null, '开发版不该有提醒条')
        // 复制 install_id（无头浏览器里剪贴板可能不可用，只看有提示）。
        await page.click('[data-testid=copy-install-id]')
        await page.waitForSelector('[data-sonner-toast]')
      },
      delay: 500,
    },

    loginErrorShot(
      'login-seats-full',
      'license_seats_full',
      '在线人数已达到许可证的上限，请等别人退出后再登录，或联系管理员',
      { max_users: 20, online_sessions: 20 },
      '20 个',
    ),
    loginErrorShot(
      'login-expired',
      'license_expired',
      '系统的许可证已到期，暂时只有管理员能登录，请联系管理员导入新的许可证',
      undefined,
      '已到期',
    ),
    loginErrorShot(
      'login-missing',
      'license_missing',
      '系统还没有导入有效的许可证，暂时只有管理员能登录，请联系管理员',
      undefined,
      '还没有导入',
    ),

    {
      // 30 天内到期：管理员页面顶部的提醒条和许可证页（GET /api/me 的 license_notice、许可证状态都是模拟的）。
      name: 'license-expiring',
      path: '/system/license',
      waitFor: '[data-testid=license-status][data-status=expiring]',
      async init(page) {
        const me = await useSession(page, admin, adminPassword)
        const state = licenseState('expiring')
        const notice = { status: 'expiring', expires_at: state.license.expires_at, days_left: 13 }
        await intercept(page, (method, path) => {
          if (method === 'GET' && path === '/api/me') return json(200, { ...me, license_notice: notice })
          if (method === 'GET' && path === '/api/system/license') return json(200, state)
          return null
        })
      },
      async prepare(page) {
        await page.waitForSelector('[data-testid=license-banner][data-status=expiring]')
        const [banner] = await texts(page, '[data-testid=license-banner]')
        expect(banner.includes('13 天后'), `提醒条：${banner}`)
        const [company] = await texts(page, '[data-testid=license-company]')
        expect(company === '某市档案馆', `公司：${company}`)
        const [online] = await texts(page, '[data-testid=license-online]')
        expect(online.includes('14 个登录') && online.includes('席位 20'), `在线：${online}`)
      },
    },

    {
      // 还没导入：导入一份为另一套系统签发的许可证（install_id 不符）。
      name: 'license-import-error',
      path: '/system/license',
      waitFor: '[data-testid=license-status][data-status=missing]',
      async init(page) {
        const me = await useSession(page, admin, adminPassword)
        await intercept(page, (method, path) => {
          if (method === 'GET' && path === '/api/me') {
            return json(200, { ...me, license_notice: { status: 'missing', expires_at: null, days_left: null } })
          }
          if (method === 'GET' && path === '/api/system/license') return json(200, licenseState('missing'))
          if (method === 'PUT' && path === '/api/system/license') {
            return json(400, {
              code: 'license_mismatch',
              message: '这份许可证是为另一套系统签发的（install_id 不符），请把本页显示的 install_id 发给供应商重新签发',
              details: { install_id: INSTALL_ID, license_install_id: 'sha256:' + '0'.repeat(64) },
            })
          }
          return null
        })
      },
      async prepare(page) {
        await page.waitForSelector('[data-testid=license-banner][data-status=missing]')
        await page.type(
          '[data-testid=license-text]',
          '-----BEGIN DOCLITE LICENSE-----\newogICJwYXlsb2FkIjogewogICAgImxpY2Vuc2VfaWQiOiAiMDE5OGY1YTAtMWMyZC03ZTNmLThh\n-----END DOCLITE LICENSE-----',
        )
        await page.click('[data-testid=license-submit]')
        await page.waitForSelector('[data-testid=license-import-error][data-code=license_mismatch]')
      },
    },

    {
      // 已登录的扫描员：许可证到期后点菜单，提示原因、回到登录页（之后的接口都按到期回答）。
      name: 'expired-redirect',
      path: '/scan',
      waitFor: '[data-testid=scan-browser-notice]',
      waitUntil: 'networkidle2',
      async init(page) {
        await useSession(page, scanner, demoPassword)
        page.licenseBlocked = false
        await intercept(page, (method, path) => {
          if (!page.licenseBlocked || !path.startsWith('/api/') || path.startsWith('/api/auth/')) return null
          return json(403, {
            code: 'license_expired',
            message: '系统的许可证已到期，暂时只有管理员能登录，请联系管理员导入新的许可证',
          })
        })
      },
      async prepare(page) {
        page.licenseBlocked = true
        await page.click('[data-menu=files]')
        await page.waitForFunction(() => location.pathname === '/login', { timeout: 10000 })
        await page.waitForSelector('[data-sonner-toast]')
        const toastText = await texts(page, '[data-sonner-toast]')
        expect(toastText.some((t) => t.includes('许可证已到期')), `提示：${toastText}`)
      },
      delay: 500,
    },
  ],
}
