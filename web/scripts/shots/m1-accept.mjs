// M1-14 验收（Mac）：演示数据、项目权限（浏览器上传和在上传的数据上整理的两张 M10-2 删掉了：上传只在客户端里，
// 整理见 m1-organize.mjs）。
//
//   npm run screenshots -- --spec scripts/shots/m1-accept.mjs --base http://127.0.0.1:<web 端口>
//
// --base 是 web 的入口（vite preview 或 dev，/api 代理到 server；M7-5 起 server 不托管界面）。
// 账号和数据来自 npm run seed（M9-3）：demo_scanner（个人授权 DEMO-A）、demo_reviewer（个人授权 DEMO-A，审核员角色授权
// DEMO-B）、demo_noproject（归类员角色、没有授权；M3-5 起 demo_cataloger 授权进了 DEMO-A，改用它）。accept-perm-scanner、
// accept-perm-reviewer 要用 admin 读各项目和授权（INIT_ADMIN_PASSWORD，默认 change-me-now）。
//
// - accept-demo-folder / accept-demo-box：seed 的「2025 文书档案」（原来是热文件夹演示的「M1 验收演示」5 盒 × 100 页，
//   M9-3 起不再造）：核对两个盒和盒 WS-2025-001 的 5 个子文件夹的页数（3、4、2、5、3）同 seed，再打开其中页数最多的
//   子文件夹看封面（图标视图，封面全部显示）。
// - accept-perm-scanner / -reviewer / -cataloger：三个人看到的项目不同（切换器截图）；直接调别人项目的接口返回 403
//   （demo_scanner 读 DEMO-B 的根文件夹），审核员能进的项目正好是授权给他的（个人授权或他的角色被授权，按 admin 读到的
//   授权算；seed 之后是 DEMO-A 个人授权 + DEMO-B 角色授权，库里另有没授权给他的项目时再核对读它 403），demo_noproject
//   用 batch 取 DEMO-A 的文件全部落在 missing（batch 登录即可，只返回能进的项目里的文件）。截图名
//   accept-perm-cataloger 不变。

import { apiLogin } from '../lib/api.mjs'

const demo = 'demo123456'
const adminUser = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const IDLE = 'networkidle2'

/** 跨截图共用的状态（截图按清单顺序执行）。 */
const state = {}

function assert(cond, msg) {
  if (!cond) throw new Error(msg)
}

/** 在页面里用这个账号的会话调接口，返回 {status, data}；不在 allow 里的非 2xx 抛错。 */
async function call(page, method, url, body, allow = []) {
  const res = await page.evaluate(
    async (method, url, body) => {
      const r = await fetch(url, {
        method,
        headers: { Accept: 'application/json', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
        body: body === undefined ? undefined : JSON.stringify(body),
      })
      const text = await r.text()
      return { status: r.status, text }
    },
    method,
    url,
    body,
  )
  const data = res.text ? JSON.parse(res.text) : undefined
  if ((res.status < 200 || res.status >= 300) && !allow.includes(res.status)) {
    throw new Error(`${method} ${url} → ${res.status} ${data?.code}: ${data?.message}`)
  }
  return { status: res.status, data }
}

const get = async (page, url) => (await call(page, 'GET', url)).data

async function project(page, code) {
  const list = await get(page, '/api/projects')
  return list.find((p) => p.project_code === code)
}

async function childByName(page, folderId, name) {
  const ch = await get(page, `/api/folders/${folderId}/children`)
  return ch.folders.find((f) => f.folder_name === name)
}

async function openFolder(page, id) {
  await page.goto(`${new URL(page.url()).origin}/files?folder=${id}`, { waitUntil: IDLE })
  await page.waitForSelector('[data-testid=files-page][data-list=ready]', { timeout: 15000 })
}

async function waitCovers(page, timeout = 20000) {
  await page.waitForFunction(
    () =>
      [...document.querySelectorAll('[data-testid=file-cover]')].every(
        (c) => !['loading', 'loading-image'].includes(c.dataset.coverState),
      ),
    { timeout },
  )
}

async function openSwitcher(page) {
  await page.click('[data-testid=project-switcher-trigger]')
  await page.waitForSelector('[data-testid=project-option]')
}

/** npm run seed 造的「2025 文书档案」（scripts/seed.mjs 的 BOX1、BOX2）。 */
const SEED_YEAR = '2025 文书档案'
const SEED_BOXES = ['盒 WS-2025-001', '盒 WS-2025-002']
const SEED_BOX1_PAGES = [3, 4, 2, 5, 3]

export default {
  out: '../docs/screenshots/m1',
  accounts: { demo_scanner: demo, demo_reviewer: demo, demo_noproject: demo },
  waitUntil: IDLE,
  shots: [
    {
      name: 'accept-demo-folder',
      user: 'demo_scanner',
      path: '/files',
      waitFor: '[data-testid=files-page]',
      delay: 500,
      async prepare(page) {
        state.a ??= await project(page, 'DEMO-A')
        const demoFolder = await childByName(page, state.a.root_folder_id, SEED_YEAR)
        assert(demoFolder, `DEMO-A 根下找不到「${SEED_YEAR}」，先跑 npm run seed`)
        const ch = await get(page, `/api/folders/${demoFolder.id}/children`)
        const names = ch.folders.map((f) => f.folder_name)
        assert(JSON.stringify(names) === JSON.stringify(SEED_BOXES), `「${SEED_YEAR}」里应正好有 ${SEED_BOXES}：${names}`)
        // 盒 001 的 5 个子文件夹（每个一件）的页数同 seed。
        const subs = (await get(page, `/api/folders/${ch.folders[0].id}/children`)).folders
        const counts = subs.map((f) => f.file_count)
        assert(JSON.stringify(counts) === JSON.stringify(SEED_BOX1_PAGES), `${SEED_BOXES[0]} 的子文件夹页数应为 ${SEED_BOX1_PAGES}：${counts}`)
        console.log(`  ${demoFolder.folder_name}：${ch.folders.map((f) => `${f.folder_name}（${f.folder_count} 个子文件夹）`).join('，')}；${SEED_BOXES[0]} 各件 ${counts.join('、')} 页`)
        state.demoFolder = demoFolder
        state.box = subs.reduce((a, b) => (b.file_count > a.file_count ? b : a))
        state.demoBox = (await get(page, `/api/folders/${state.box.id}/children`)).files.map((f) => f.file_id)
        await openFolder(page, demoFolder.id)
      },
    },
    {
      name: 'accept-demo-box',
      user: 'demo_scanner',
      path: '/files',
      waitFor: '[data-testid=files-page]',
      delay: 800,
      async prepare(page) {
        assert(state.box, '上一张（accept-demo-folder）没成功')
        await openFolder(page, state.box.id)
        await page.click('[data-testid=view-icons]')
        await waitCovers(page)
        const s = await page.$$eval('[data-testid=file-cover]', (els) => els.map((e) => e.dataset.coverState))
        assert(s.length > 0 && s.every((x) => x === 'loaded'), `封面没有全部显示：${[...new Set(s)]}`)
        console.log(`  ${state.box.folder_name}：可见的 ${s.length} 张封面都显示了（浏览器里走预签名 URL）`)
      },
    },
    {
      name: 'accept-perm-scanner',
      user: 'demo_scanner',
      path: '/files',
      waitFor: '[data-testid=project-current]',
      delay: 400,
      async prepare(page) {
        const codes = (await get(page, '/api/projects')).map((p) => p.project_code)
        assert(codes.join() === 'DEMO-A', `demo_scanner 能进的项目应只有 DEMO-A：${codes}`)
        state.a ??= await project(page, 'DEMO-A')
        // 越权：用 admin 取 DEMO-B 的根文件夹，demo_scanner 读它应 403（seed 之后 demo_reviewer 两个项目都能进）。
        const admin = await apiLogin(new URL(page.url()).origin, adminUser, adminPassword)
        const b = (await admin('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-B')
        assert(b, '找不到 DEMO-B，先跑 npm run seed')
        const r = await call(page, 'GET', `/api/folders/${b.root_folder_id}/children`, undefined, [403])
        assert(r.status === 403, `demo_scanner 读 DEMO-B 的根文件夹应 403：${r.status}`)
        await openSwitcher(page)
        console.log(`  demo_scanner（个人授权）：${codes}；读 DEMO-B 的根文件夹 → ${r.status} ${r.data.code}`)
      },
    },
    {
      name: 'accept-perm-reviewer',
      user: 'demo_reviewer',
      path: '/files',
      waitFor: '[data-testid=project-current]',
      delay: 400,
      async prepare(page) {
        assert(state.a, '先跑 accept-perm-scanner（取 DEMO-A）')
        const list = await get(page, '/api/projects')
        // 先记下 DEMO-B（accept-perm-cataloger 要用），再断言：断言不过时后面那张照样能跑（M3-5）。
        state.b = list.find((p) => p.project_code === 'DEMO-B')
        // 审核员只能进授权给他的项目：用 admin 读每个项目的授权（个人授权、角色授权），算出应有的集合（M2-5 起 demo_reviewer
        // 另有 DEMO-A 的个人授权，原来写死的"只有 DEMO-B"不成立，M3-8 改）。
        const me = await get(page, '/api/me')
        const admin = await apiLogin(new URL(page.url()).origin, adminUser, adminPassword)
        const roleIds = (await admin('GET', `/api/system/users?keyword=${me.username}&page_size=100`)).items.find((u) => u.id === me.id).roles.map((r) => r.id)
        const want = []
        const how = []
        let outside = null
        for (const p of await admin('GET', '/api/projects')) {
          const acc = await admin('GET', `/api/projects/${p.id}/access`)
          const personal = acc.users.some((u) => u.id === me.id)
          const byRole = acc.roles.some((r) => roleIds.includes(r.id))
          if (personal || byRole) {
            want.push(p.project_code)
            how.push(`${p.project_code}（${[personal && '个人授权', byRole && '审核员角色授权'].filter(Boolean).join('、')}）`)
          } else outside ??= p
        }
        const got = list.map((p) => p.project_code).sort()
        assert(JSON.stringify(got) === JSON.stringify(want.sort()), `demo_reviewer 能进的项目应正好是授权给他的 ${want}：${got}`)
        assert(how.includes('DEMO-A（个人授权）'), `demo_reviewer 应有 DEMO-A 的个人授权（npm run seed）：${how}`)
        assert(how.includes('DEMO-B（审核员角色授权）'), `demo_reviewer 应经审核员角色能进 DEMO-B（npm run seed）：${how}`)
        // seed 只有 DEMO-A、DEMO-B，都授权给了 demo_reviewer；库里另有没授权给他的项目时（别的脚本建的）再核对 403。
        let denied = '库里没有没授权给他的项目（越权的 403 见 accept-perm-scanner）'
        if (outside) {
          const r = await call(page, 'GET', `/api/folders/${outside.root_folder_id}/children`, undefined, [403])
          assert(r.status === 403, `demo_reviewer 读没授权的 ${outside.project_code} 的根文件夹应 403：${r.status}`)
          denied = `读没授权的 ${outside.project_code} 的根文件夹 → ${r.status} ${r.data.code}`
        }
        await openSwitcher(page)
        console.log(`  demo_reviewer：${how.join('、')}；${denied}`)
      },
    },
    {
      name: 'accept-perm-cataloger',
      user: 'demo_noproject',
      path: '/files',
      waitFor: '[data-testid=project-empty]',
      async prepare(page) {
        const codes = (await get(page, '/api/projects')).map((p) => p.project_code)
        assert(codes.length === 0, `demo_noproject 不应能进任何项目：${codes}`)
        const checks = [
          ['GET', `/api/folders/${state.a.root_folder_id}`],
          ['GET', `/api/folders/${state.a.root_folder_id}/children`],
          ['GET', `/api/folders/${state.b.root_folder_id}/children`],
          ['POST', '/api/folders', { parent_id: state.a.root_folder_id, folder_name: '不该建出来' }],
          ['POST', `/api/folders/${state.a.root_folder_id}/files:check`, { file_name: 'a.jpg', sha256: '0'.repeat(64), size: 1 }],
          ['GET', `/api/projects/${state.a.id}/stream`],
        ]
        const out = []
        for (const [m, u, b] of checks) {
          const r = await call(page, m, u, b, [403])
          assert(r.status === 403, `${m} ${u} 应 403：${r.status}`)
          out.push(`${m} ${u.replace(/[0-9a-f-]{36}/g, '…')} → 403 ${r.data.code}`)
        }
        // batch 是"登录即可"：别人项目里的文件都放进 missing，不报错。
        const demoBox = state.demoBox
        if (demoBox) {
          const r = (await call(page, 'POST', '/api/files/batch', { file_ids: demoBox.slice(0, 5) })).data
          assert(r.items.length === 0 && r.missing.length === 5, 'demo_noproject 的 batch 应全部 missing')
          out.push('POST /api/files/batch（DEMO-A 的 5 个文件）→ 200，items 0、missing 5')
        }
        console.log(`  demo_noproject（未授权）：没有能进的项目\n    ${out.join('\n    ')}`)
      },
    },
  ],
}
