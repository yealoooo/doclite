#!/usr/bin/env node
// 演示数据（M9-3，取代原来的 seed:demo、M3/M4 的演示数据和 seed:files 造的大数据）：在一个刚迁移好的空库上，只经接口
// 建好演示账号、两个项目和够功能测试用的约 40 页。可以重跑：已有的跳过（按名字找），状态已经到位的不再动。
//
//   INIT_ADMIN_PASSWORD=<管理员密码> npm run seed -- [--base http://127.0.0.1:9080] [--admin 登录名:密码] [--password demo123456]
//
// --base 是 server 本身或 web 的入口（vite 把 /api 转给 server）。管理员账号按顺序取 --admin、环境变量
// INIT_ADMIN_USERNAME / INIT_ADMIN_PASSWORD（和启动 server 的是同一组）、默认 admin / change-me-now。
//
// 建的东西（README"可以拿来试的数据""演示账号"是同一份说明）：
// - 账号（密码都是 demo123456）：demo_admin、demo_manager、demo_scanner、demo_reviewer、demo_reviewer2、demo_cataloger、
//   demo_noproject（归类员，没有项目）、demo_useradmin（自建角色 user_admin：system.users + system.roles）、
//   demo_disabled（已停用的审核员）。
// - DEMO-A「演示项目 A · 文书档案」：个人授权 demo_manager、demo_scanner、demo_reviewer、demo_reviewer2、demo_cataloger；
//   著录项 责任者（必填）、文号、成文日期、密级，档号模板 {fonds}-{category}·{year}-{retention}-{org}-{item_no:04}。
//   DEMO-B「演示项目 B · 科技档案」：空项目，个人授权 demo_manager，角色授权审核员（demo_reviewer、demo_disabled）。
// - DEMO-A 的文件（合成的 A4 扫描页，带封面，demo_scanner 上传，按页码顺序）：
//   - 2025 文书档案 / 盒 WS-2025-001（备注 3 号库 A 架 11 层）：5 个子文件夹（3、4、2、5、3 页，第 4 个里 1 页低 DPI）；
//   - 2025 文书档案 / 盒 WS-2025-002（备注 3 号库 A 架 12 层）：3 个子文件夹（6、6、3 页，第 3 个里 1 页无封面）、
//     「附件（含下级）」（1 页 + 子文件夹「扫描件」2 页，归档时演示"跳过"）、盒子里直接放的「盒内目录.png」（演示散页提示）；
//   - 格式样本：scripts/samples 的 8 个（JPEG 彩色、灰度、EXIF 方向 6，PNG 1 位、带透明，TIFF G4、LZW、无压缩；没有封面）。
// - 审核：盒 001 分给 demo_reviewer（demo_manager 分配）：第 1、3 个子文件夹全部通过；第 2 个通过 3 页、驳回第 3 页；
//   第 4 个通过 2 页、驳回第 2 页、其余待审；第 5 个待审。驳回的 2 页在"待重扫"里。盒 002、格式样本待分配。
// - 归类树（demo_cataloger）：全宗 Z001 文书档案 › 门类 WS 文书 › 年度 2025 › 保管期限 Y 永久、D30 30 年 › 机构
//   BGS 办公室、CWK 财务科（两个保管期限下都有）。盒 001 的前 3 个子文件夹归档到 永久 › 办公室（第 1–3 件，著录填好），
//   第 1 件已锁定（全部页通过，可以试导出），第 2 件有 1 页驳回（锁不了、导出预检不就绪），第 3 件可以锁定。都是封存状态。

import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'

import { apiLogin } from './lib/api.mjs'
import { makeScanPage } from './lib/scan-pages.mjs'

const SAMPLES_DIR = fileURLToPath(new URL('./samples/', import.meta.url))

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

const origin = args.base.replace(/\/$/, '')
const [adminUser, ...adminPw] = args.admin.split(':')
const admin = await apiLogin(origin, adminUser, adminPw.join(':'))
const PW = args.password

// ---------- 账号 ----------

let roles = await admin('GET', '/api/system/roles')
if (!roles.some((r) => r.role_code === 'user_admin')) {
  await admin('POST', '/api/system/roles', {
    role_code: 'user_admin',
    role_name: '用户管理员',
    description: '演示用：能管理用户和角色，但不是系统管理员',
    menu_codes: ['system.users', 'system.roles'],
  })
  console.log('已建角色 user_admin')
  roles = await admin('GET', '/api/system/roles')
}
const roleId = (code) => roles.find((r) => r.role_code === code).id

const USERS = [
  ['demo_admin', '演示管理员', 'admin'],
  ['demo_manager', '演示负责人', 'manager'],
  ['demo_scanner', '演示扫描员', 'scanner'],
  ['demo_reviewer', '演示审核员', 'reviewer'],
  ['demo_reviewer2', '演示审核员二', 'reviewer'],
  ['demo_cataloger', '演示归类员', 'cataloger'],
  ['demo_noproject', '演示归类员（无项目）', 'cataloger'],
  ['demo_useradmin', '演示用户管理员', 'user_admin'],
  ['demo_disabled', '演示停用审核员', 'reviewer'],
]
const users = {}
for (const [username, displayName, role] of USERS) {
  const page = await admin('GET', `/api/system/users?keyword=${encodeURIComponent(username)}&page_size=100`)
  let u = page.items.find((x) => x.username === username)
  if (!u) {
    u = await admin('POST', '/api/system/users', { username, display_name: displayName, password: PW, role_ids: [roleId(role)] })
    console.log(`已建账号 ${username}（${role}）`)
  }
  users[username] = u
}
if (users.demo_disabled.status === 'active') {
  await admin('POST', `/api/system/users/${users.demo_disabled.id}/disable`)
  console.log('已停用 demo_disabled')
}

// ---------- 项目和授权 ----------

async function ensureProject(code, name, description, userNames, roleCodes) {
  const found = await admin('GET', `/api/system/projects?keyword=${encodeURIComponent(code)}&page_size=100`)
  let p = found.items.find((x) => x.project_code === code)
  if (!p) {
    p = await admin('POST', '/api/system/projects', { project_code: code, project_name: name, description })
    console.log(`已建项目 ${code}`)
  }
  // 授权只补上缺的（接口是整体替换：现有的 ∪ 演示要的）。
  const access = await admin('GET', `/api/projects/${p.id}/access`)
  const uids = new Set(access.users.map((u) => u.id))
  const rids = new Set(access.roles.map((r) => r.id))
  const before = uids.size + rids.size
  for (const n of userNames) uids.add(users[n].id)
  for (const r of roleCodes) rids.add(roleId(r))
  if (uids.size + rids.size !== before) {
    await admin('PUT', `/api/projects/${p.id}/access`, { user_ids: [...uids], role_ids: [...rids] })
    console.log(`已授权 ${code}：${[...userNames, ...roleCodes.map((r) => `角色 ${r}`)].join('、')}`)
  }
  return admin('GET', `/api/system/projects/${p.id}`)
}

let projA = await ensureProject(
  'DEMO-A',
  '演示项目 A · 文书档案',
  '演示用：文书档案，约 40 页（npm run seed）',
  ['demo_manager', 'demo_scanner', 'demo_reviewer', 'demo_reviewer2', 'demo_cataloger'],
  [],
)
await ensureProject('DEMO-B', '演示项目 B · 科技档案', '演示用：空项目（权限演示）', ['demo_manager'], ['reviewer'])

// 著录项和档号模板：还没有著录项时才设（不覆盖之后在界面里改过的）。
const ITEM_FIELDS = [
  { key: 'author', label: '责任者', type: 'text', required: true, max_length: 100 },
  { key: 'doc_no', label: '文号', type: 'text', required: false, max_length: 50 },
  { key: 'doc_date', label: '成文日期', type: 'date', required: false },
  { key: 'secret', label: '密级', type: 'enum', required: false, options: ['公开', '内部', '秘密'], default: '内部' },
]
if (!projA.settings.item_fields?.length) {
  const settings = {
    ...projA.settings,
    archive_no: { template: '{fonds}-{category}·{year}-{retention}-{org}-{item_no:04}' },
    item_fields: ITEM_FIELDS,
  }
  projA = await admin('PATCH', `/api/system/projects/${projA.id}`, { settings })
  console.log('已设 DEMO-A 的著录项和档号模板')
}
const pid = projA.id

// ---------- 文件 ----------

const scanner = await apiLogin(origin, 'demo_scanner', PW)

async function folder(parentId, name, remark) {
  const f = await scanner('POST', '/api/folders', { parent_id: parentId, folder_name: name, remark, if_exists: 'return' })
  if (remark !== undefined && (f.remark ?? '') !== remark) await scanner('PATCH', `/api/folders/${f.id}`, { remark })
  return f
}
const children = (id) => scanner('GET', `/api/folders/${id}/children`)

let uploaded = 0
/** 上传一个文件（已有同名的跳过）。`make()` 返回 {data, type, cover?}。 */
async function upload(folderId, existing, name, make) {
  if (existing.has(name.toLowerCase())) return
  const { data, type, cover } = make()
  const form = new FormData()
  form.append('file_name', name)
  form.append('content_sha256', createHash('sha256').update(data).digest('hex'))
  if (cover) form.append('cover', new Blob([cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([data], { type }), name)
  const r = await scanner('POST', `/api/folders/${folderId}/files`, form, { allow: [409] })
  if (r?.status === 409) console.warn(`  跳过 ${name}：${r.code} ${r.message}`)
  else uploaded++
}

/** 一个文件夹里 n 页合成的扫描页 IMG_0001.png…；lowDpi、noCover 是页码（从 1 起）。 */
async function pages(f, seedPath, n, { lowDpi = [], noCover = [] } = {}) {
  const existing = new Set((await children(f.id)).files.map((x) => x.file_name.toLowerCase()))
  for (let i = 1; i <= n; i++) {
    await upload(f.id, existing, `IMG_${String(i).padStart(4, '0')}.png`, () => {
      const p = makeScanPage({ seed: `DEMO-A/${seedPath}#${i}`, pageNo: i, dpi: lowDpi.includes(i) ? 150 : 200 })
      return { data: p.png, type: 'image/png', cover: noCover.includes(i) ? null : p.cover }
    })
  }
}

const root = projA.root_folder_id
const year = await folder(root, '2025 文书档案')
const box1 = await folder(year.id, '盒 WS-2025-001', '3 号库 A 架 11 层')
const box2 = await folder(year.id, '盒 WS-2025-002', '3 号库 A 架 12 层')

const BOX1 = [
  ['关于印发2025年档案工作要点的通知', 3, {}],
  ['办公室2024年度工作总结', 4, {}],
  ['关于调整2025年春节值班安排的通知', 2, {}],
  ['2025年第一季度办公会议纪要', 5, { lowDpi: [4] }],
  ['关于申请购置档案密集架的请示', 3, {}],
]
const BOX2 = [
  ['2025年1月财务收支报表', 6, {}],
  ['关于2025年度部门预算的批复', 6, {}],
  ['差旅费报销管理办法', 3, { noCover: [2] }],
]
const subs1 = []
for (const [name, n, opt] of BOX1) {
  const f = await folder(box1.id, name)
  await pages(f, `盒 WS-2025-001/${name}`, n, opt)
  subs1.push(f)
}
for (const [name, n, opt] of BOX2) {
  const f = await folder(box2.id, name)
  await pages(f, `盒 WS-2025-002/${name}`, n, opt)
}
const att = await folder(box2.id, '附件（含下级）')
await pages(att, '盒 WS-2025-002/附件（含下级）', 1)
await pages(await folder(att.id, '扫描件'), '盒 WS-2025-002/附件（含下级）/扫描件', 2)
{
  const existing = new Set((await children(box2.id)).files.map((x) => x.file_name.toLowerCase()))
  await upload(box2.id, existing, '盒内目录.png', () => {
    const p = makeScanPage({ seed: 'DEMO-A/盒 WS-2025-002/盒内目录', pageNo: 1 })
    return { data: p.png, type: 'image/png', cover: p.cover }
  })
}

const samples = await folder(root, '格式样本', 'scripts/samples 的几种格式（没有封面）')
{
  const existing = new Set((await children(samples.id)).files.map((x) => x.file_name.toLowerCase()))
  const SAMPLES = [
    ['jpeg-rgb.jpg', 'image/jpeg'],
    ['jpeg-gray.jpg', 'image/jpeg'],
    ['jpeg-exif6.jpg', 'image/jpeg'],
    ['png-1bit.png', 'image/png'],
    ['png-alpha.png', 'image/png'],
    ['tiff-g4-1bit.tif', 'image/tiff'],
    ['tiff-lzw-rgb.tif', 'image/tiff'],
    ['tiff-none-gray8.tif', 'image/tiff'],
  ]
  for (const [name, type] of SAMPLES) {
    await upload(samples.id, existing, name, () => ({ data: readFileSync(path.join(SAMPLES_DIR, name)), type }))
  }
}
console.log(`文件：新传 ${uploaded} 个`)

// ---------- 审核 ----------

const manager = await apiLogin(origin, 'demo_manager', PW)
const reviewer = await apiLogin(origin, 'demo_reviewer', PW)
// 盒 001 里还待分配的整个分给 demo_reviewer（按文件夹分配只分待分配的）。
const box1Files = async () => {
  const out = []
  for (const f of subs1) out.push((await children(f.id)).files)
  return out
}
let files1 = await box1Files()
if (files1.flat().some((x) => x.review_status === 'unassigned')) {
  const r = await manager('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: users.demo_reviewer.id, folder_id: box1.id })
  console.log(`已把盒 001 的 ${r.assigned} 页分给 demo_reviewer`)
  files1 = await box1Files()
}
// 每个子文件夹：哪几页通过、哪页驳回（其余待审）。只动还待审的。
const PLAN = [
  { approve: [1, 2, 3] },
  { approve: [1, 2, 4], reject: { page: 3, reasons: ['blur'], comment: '字迹模糊，请重扫' } },
  { approve: [1, 2] },
  { approve: [1, 3], reject: { page: 2, reasons: ['skew'], comment: '页面歪斜，下边缘被裁掉' } },
  {},
]
const toApprove = []
for (const [i, plan] of PLAN.entries()) {
  const list = files1[i]
  for (const n of plan.approve ?? []) if (list[n - 1]?.review_status === 'pending') toApprove.push(list[n - 1].file_id)
  const rej = plan.reject && list[plan.reject.page - 1]
  if (rej?.review_status === 'pending') {
    await reviewer('POST', `/api/files/${rej.file_id}/review`, { result: 'rejected', reject_reasons: plan.reject.reasons, comment: plan.reject.comment })
    console.log(`已驳回 ${subs1[i].folder_name}/${rej.file_name}`)
  }
}
if (toApprove.length) {
  await reviewer('POST', `/api/projects/${pid}/reviews:approve`, { file_ids: toApprove })
  console.log(`已通过 ${toApprove.length} 页`)
}

// ---------- 归类 ----------

const cataloger = await apiLogin(origin, 'demo_cataloger', PW)

async function category(parentId, type, code, name) {
  const q = parentId ? `?parent_id=${parentId}` : ''
  const found = (await cataloger('GET', `/api/projects/${pid}/archive-categories${q}`)).items.find((c) => c.category_code === code)
  if (found) return found
  return cataloger('POST', `/api/projects/${pid}/archive-categories`, { parent_id: parentId, category_type: type, category_code: code, category_name: name })
}
const fonds = await category(null, 'fonds', 'Z001', '文书档案')
const ws = await category(fonds.id, 'category', 'WS', '文书')
const y2025 = await category(ws.id, 'year', '2025', '2025')
const yong = await category(y2025.id, 'retention', 'Y', '永久')
const d30 = await category(y2025.id, 'retention', 'D30', '30 年')
const bgs = await category(yong.id, 'org', 'BGS', '办公室')
await category(yong.id, 'org', 'CWK', '财务科')
await category(d30.id, 'org', 'BGS', '办公室')
await category(d30.id, 'org', 'CWK', '财务科')

// 盒 001 的前 3 个子文件夹归档到 永久 › 办公室（已经归档的跳过）。
const FIELDS = [
  { author: '市档案馆办公室', doc_no: '档办〔2025〕3 号', doc_date: '2025-01-15', secret: '内部' },
  { author: '市档案馆办公室', doc_date: '2025-01-20', secret: '内部' },
  { author: '市档案馆办公室', doc_no: '档办〔2025〕8 号', doc_date: '2025-01-22', secret: '公开' },
]
const boxKids = (await children(box1.id)).folders
const toArchive = subs1.slice(0, 3).filter((f) => !boxKids.find((k) => k.id === f.id)?.archive)
if (toArchive.length) {
  const r = await cataloger('POST', `/api/archive-categories/${bgs.id}/items:from-folders`, { folder_ids: toArchive.map((f) => f.id) })
  console.log(`已归档 ${r.items.length} 件到 永久 › 办公室：${r.items.map((x) => x.archive_no ?? `第 ${x.item_no} 件`).join('、')}`)
}
const archived = (await children(box1.id)).folders
for (const [i, f] of subs1.slice(0, 3).entries()) {
  const a = archived.find((k) => k.id === f.id)?.archive
  if (!a) throw new Error(`「${f.folder_name}」没有归档`)
  let item = await cataloger('GET', `/api/archive-items/${a.item_id}`)
  if (!item.extra_fields?.author && item.status === 'draft') {
    item = await cataloger('PATCH', `/api/archive-items/${item.id}`, { row_version: item.row_version, extra_fields: FIELDS[i] })
    console.log(`已填著录：${item.archive_no}`)
  }
  if (i === 0 && item.status === 'draft' && item.source_status === 'sealed') {
    item = await cataloger('POST', `/api/archive-items/${item.id}/lock`, { row_version: item.row_version })
    console.log(`已锁定：${item.archive_no}`)
  }
}

// ---------- 小结 ----------

const sum = (await admin('GET', `/api/projects/${pid}/catalog-summary`)).counts
console.log(
  `DEMO-A：${sum.total} 页（通过 ${sum.review.approved}、驳回 ${sum.review.rejected}、待审 ${sum.review.pending}、` +
    `待分配 ${sum.review.unassigned}；已归档 ${sum.items.total} 件 ${sum.catalog.cataloged} 页、已锁定 ${sum.items.locked} 件、` +
    `可导出 ${sum.items.exportable} 件 ${sum.items.exportable_pages} 页）`,
)
