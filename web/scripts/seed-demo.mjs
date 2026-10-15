#!/usr/bin/env node
// 在开发库里建演示账号（可重复执行：已存在的角色和用户会跳过）。
//
//   npm run seed:demo -- [--base http://127.0.0.1:9080] [--admin 登录名:密码]
//
// 用初始管理员登录。账号密码按顺序取：--admin 参数；环境变量 INIT_ADMIN_USERNAME、INIT_ADMIN_PASSWORD
// （和启动 server 用的是同一组变量）；默认值 admin / change-me-now（和 server/.env.example 一致）。
// 例如：INIT_ADMIN_PASSWORD=admin123456 npm run seed:demo
//
// - 5 个内置角色各一个用户：demo_admin、demo_manager、demo_scanner、demo_reviewer、demo_cataloger；
// - M3-5：另一个归类员 demo_noproject（cataloger 角色），永远不授权任何项目，看"没有能进的项目"用；
// - 自建角色 user_admin（用户管理员，菜单 system.users + system.roles）和用户 demo_useradmin，
//   用来演示非 admin 操作人在系统管理页面里被禁用的操作。
// - M1-5：两个演示项目和项目授权（已存在的项目不重建；授权只补上缺的，不撤销别人加的）：
//   - DEMO-A「演示项目 A · 文书档案」：个人授权给 demo_scanner、demo_manager；
//   - DEMO-B「演示项目 B · 科技档案」：授权给 reviewer 角色，另个人授权给 demo_manager；
//   - 于是 demo_manager 两个项目都能进，demo_scanner 只能进 A，demo_reviewer 只能进 B，
//     demo_noproject 一个都不能进（admin、demo_admin 能进所有项目）；demo_cataloger 这里不授权，M3-5 起由
//     scripts/lib/m3-demo.mjs 的 ensureM35Demo 个人授权进 DEMO-A（M3-8 的演示也要它）；
//   - 另建一个已停用的审核员 demo_disabled（授权对话框里演示"已停用"的标记，登录不了）。
// 所有演示用户的密码都是 demo123456。

import { parseArgs } from 'node:util'

const { values: args } = parseArgs({
  options: {
    base: { type: 'string', default: process.env.DOCLITE_BASE ?? 'http://127.0.0.1:9080' },
    admin: {
      type: 'string',
      default: `${process.env.INIT_ADMIN_USERNAME ?? 'admin'}:${process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'}`,
    },
    password: { type: 'string', default: 'demo123456' },
  },
})

const base = args.base.replace(/\/$/, '')
let cookie = ''

async function api(method, path, body) {
  const res = await fetch(base + path, {
    method,
    headers: {
      Accept: 'application/json',
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...(cookie ? { Cookie: cookie } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const setCookie = res.headers.get('set-cookie')
  if (setCookie) cookie = setCookie.split(';')[0]
  const text = await res.text()
  const data = text ? JSON.parse(text) : undefined
  if (!res.ok) throw new Error(`${method} ${path} → ${res.status} ${data?.code}: ${data?.message}`)
  return data
}

const [adminUser, ...rest] = args.admin.split(':')
await api('POST', '/api/auth/login', { username: adminUser, password: rest.join(':') })

let roles = await api('GET', '/api/system/roles')
if (!roles.some((r) => r.role_code === 'user_admin')) {
  await api('POST', '/api/system/roles', {
    role_code: 'user_admin',
    role_name: '用户管理员',
    description: '演示用：能管理用户和角色，但不是系统管理员',
    menu_codes: ['system.users', 'system.roles'],
  })
  console.log('已创建角色 user_admin')
  roles = await api('GET', '/api/system/roles')
}
const roleId = (code) => roles.find((r) => r.role_code === code).id

const demos = [
  ['demo_admin', '演示管理员', 'admin'],
  ['demo_manager', '演示负责人', 'manager'],
  ['demo_scanner', '演示扫描员', 'scanner'],
  ['demo_reviewer', '演示审核员', 'reviewer'],
  ['demo_cataloger', '演示归类员', 'cataloger'],
  ['demo_noproject', '演示归类员（无项目）', 'cataloger'],
  ['demo_useradmin', '演示用户管理员', 'user_admin'],
  ['demo_disabled', '演示停用审核员', 'reviewer'],
]

for (const [username, displayName, roleCode] of demos) {
  const page = await api('GET', `/api/system/users?keyword=${encodeURIComponent(username)}&page_size=100`)
  if (page.items.some((u) => u.username === username)) {
    console.log(`跳过 ${username}（已存在）`)
    continue
  }
  await api('POST', '/api/system/users', {
    username,
    display_name: displayName,
    password: args.password,
    role_ids: [roleId(roleCode)],
  })
  console.log(`已创建 ${username}（${roleCode}）`)
}

// demo_disabled：建好后停用（已经是停用状态就跳过）。
{
  const page = await api('GET', `/api/system/users?keyword=demo_disabled&page_size=100`)
  const user = page.items.find((u) => u.username === 'demo_disabled')
  if (user && user.status === 'active') {
    await api('POST', `/api/system/users/${user.id}/disable`)
    console.log('已停用 demo_disabled')
  }
}

// ---------- M1-5：演示项目和项目授权 ----------

const userPage = await api('GET', '/api/system/users?keyword=demo_&page_size=100')
const userId = (username) => {
  const user = userPage.items.find((u) => u.username === username)
  if (!user) throw new Error(`找不到用户 ${username}`)
  return user.id
}

const demoProjects = [
  {
    code: 'DEMO-A',
    name: '演示项目 A · 文书档案',
    description: '演示用：个人授权给 demo_scanner、demo_manager',
    users: ['demo_scanner', 'demo_manager'],
    roles: [],
  },
  {
    code: 'DEMO-B',
    name: '演示项目 B · 科技档案',
    description: '演示用：授权给审核员角色，另个人授权给 demo_manager',
    users: ['demo_manager'],
    roles: ['reviewer'],
  },
]

for (const demo of demoProjects) {
  const found = await api('GET', `/api/system/projects?keyword=${encodeURIComponent(demo.code)}&page_size=100`)
  let project = found.items.find((p) => p.project_code === demo.code)
  if (project) {
    console.log(`跳过项目 ${demo.code}（已存在）`)
  } else {
    project = await api('POST', '/api/system/projects', {
      project_code: demo.code,
      project_name: demo.name,
      description: demo.description,
    })
    console.log(`已创建项目 ${demo.code}`)
  }
  // 授权只补上缺的（整体替换接口：当前授权 ∪ 演示需要的）。
  const access = await api('GET', `/api/projects/${project.id}/access`)
  const userIds = new Set(access.users.map((u) => u.id))
  const roleIds = new Set(access.roles.map((r) => r.id))
  const before = userIds.size + roleIds.size
  for (const u of demo.users) userIds.add(userId(u))
  for (const r of demo.roles) roleIds.add(roleId(r))
  if (userIds.size + roleIds.size !== before) {
    await api('PUT', `/api/projects/${project.id}/access`, { user_ids: [...userIds], role_ids: [...roleIds] })
    console.log(`已授权 ${demo.code}：${[...demo.users, ...demo.roles.map((r) => `角色 ${r}`)].join('、')}`)
  }
}

await api('POST', '/api/auth/logout')
