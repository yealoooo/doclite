// M3-7 的演示数据（看板、归类信息栏的截图用；M3-8 也可以接着用）：DEMO-A 下的
// - 文件夹「M3-7 看板演示」：盒 01（20 页）、盒 02（16 页，1 页低 DPI）、盒 03（8 页）、盒 03/补扫（4 页），
//   先用 seed:files 传好（命令见 ensureM37Demo 的报错）；
// - 审核：盒 01 全部分给 demo_reviewer，通过 1–14、驳回 15–16（模糊）、17–20 待审；盒 02 的 1–10 分给 demo_reviewer，
//   通过 1–8、9–10 待审，11–16 待分配；盒 03 都待分配；
// - 归类树（经接口建，M3-1）：全宗 Z109「M3-7 看板演示」/ 门类 WS「文书」/ 年度 2025「2025 年」/ 保管期限 Y「永久」/
//   机构 BGS「办公室」，以及 2025 年下的保管期限 D30「30 年」（没有机构这一级，条目的档号留空）；
// - 条目和挂接：
//   1. 机构 BGS 第 1 件 Z109-WS·2025-Y-BGS-0001「关于开展 2025 年档案数字化工作的通知」已定稿：盒 01 第 1–6 页；
//   2. 第 2 件 …-0002「办公室 2025 年上半年工作总结」著录完成：盒 01 第 7–16 页（含 2 页驳回的）；
//   3. 第 3 件 …-0003「关于调整档案库房管理制度的请示」著录中：盒 02 第 1–12 页（通过、待审、待分配都有）；
//   4. 保管期限 D30 第 1 件（档号为空，界面显示"第 1 件"）著录中、没填题名：盒 03 第 1–4 页（都待分配，归类先行）。
//   M3-7 时（M3-2、M3-3 合入前）这些是用 psql 直接写库造的（doclite_dev 里现在的就是，4 个条目没有审计，定稿的那件没有
//   lock 审计）；**M3-6 起只走接口**：缺哪个条目就用带 file_ids 的 POST 新建（M3-3），再标记完成、定稿（必填的著录项按
//   catalog-settings 补上演示值）。已经有的（按件号）不动。

import { execFileSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'

import { makeScanPage } from './scan-pages.mjs'

export const M37_FOLDER = 'M3-7 看板演示'
export const M37_FONDS_CODE = 'Z109'
export const M37_FONDS_NAME = 'M3-7 看板演示'
const PSQL = process.env.PSQL ?? '/opt/homebrew/opt/postgresql@16/bin/psql'
const DATABASE_URL = process.env.DATABASE_URL ?? 'postgres://doclite@127.0.0.1:5432/doclite_dev'

/** 应用生成的 UUIDv7（主键规则：硬性规则 15）。脚本里用 node:crypto，不在 web/src 里。 */
export function uuidv7() {
  const b = randomBytes(16)
  let ts = BigInt(Date.now())
  for (let i = 5; i >= 0; i--) {
    b[i] = Number(ts & 0xffn)
    ts >>= 8n
  }
  b[6] = (b[6] & 0x0f) | 0x70
  b[8] = (b[8] & 0x3f) | 0x80
  const h = b.toString('hex')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

/**
 * 在开发库里执行一段 SQL（一个事务，出错就停）。返回 psql 的输出（-At）。M3-6 起截图脚本只用它**读**（核对审计、
 * 读 settings 原样），造数据、清理一律走接口；m3-catalog-info 改题名还用它写（定稿的条目接口改不了，见那个脚本）。
 */
export function runSql(sql) {
  return execFileSync(PSQL, [DATABASE_URL, '-v', 'ON_ERROR_STOP=1', '-q', '-At', '-1', '-f', '-'], {
    input: sql,
    encoding: 'utf8',
  })
}

/** SQL 字符串字面量。 */
export function lit(v) {
  if (v === null || v === undefined) return 'NULL'
  return `'${String(v).replaceAll("'", "''")}'`
}

/** 一个登录会话（各自的 Cookie）。 */
export async function apiLogin(origin, username, password) {
  let cookie = ''
  const call = async (method, path, body, { allow = [] } = {}) => {
    const form = body instanceof FormData
    const res = await fetch(origin + path, {
      method,
      headers: {
        Accept: 'application/json',
        ...(body !== undefined && !form ? { 'Content-Type': 'application/json' } : {}),
        ...(cookie ? { Cookie: cookie } : {}),
      },
      body: body === undefined ? undefined : form ? body : JSON.stringify(body),
    })
    const sc = res.headers.get('set-cookie')
    if (sc) cookie = sc.split(';')[0]
    const text = await res.text()
    const data = text ? JSON.parse(text) : undefined
    if (!res.ok && !allow.includes(res.status)) {
      throw new Error(`${username}: ${method} ${path} → ${res.status} ${data?.code}: ${data?.message}`)
    }
    return allow.includes(res.status) && !res.ok ? { status: res.status, ...data } : data
  }
  await call('POST', '/api/auth/login', { username, password })
  call.cookie = () => cookie
  call.username = username
  return call
}

/** 用接口上传一页合成的 PNG（带封面），返回入库结果。 */
export async function uploadPage(call, folderId, name, seed, pageNo) {
  const p = makeScanPage({ seed, pageNo })
  const form = new FormData()
  form.append('file_name', name)
  form.append('cover', new Blob([p.cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([p.png], { type: 'image/png' }), 'page.png')
  return call('POST', `/api/folders/${folderId}/files`, form)
}

/**
 * 在 fn 执行期间拦截页面发出的请求：URL 符合 match 的记下来（发出时间、同时在途的最大个数），可以按 `log.delayMs`
 * 推迟放行（模拟慢接口）；其余的立即放行。fn 结束前要等在途的都回来（`waitQuiet`），再关掉拦截。
 * 用来统计看板、设置编辑器发了多少请求（M3-7 审查第 1 轮）。
 */
export async function interceptRequests(page, match, fn) {
  const log = {
    started: [],
    inflight: 0,
    maxInflight: 0,
    delayMs: 0,
    reset() {
      this.started = []
      this.maxInflight = this.inflight
    },
    /** 等到没有在途的、并且（从调用时起算）quietMs 内没有新请求。 */
    async waitQuiet(quietMs = 1500, timeout = 15000) {
      const t0 = Date.now()
      while (Date.now() - t0 < timeout) {
        const lastAt = Math.max(t0, this.started.length ? this.started[this.started.length - 1].at : 0)
        if (this.inflight === 0 && Date.now() - lastAt >= quietMs) return
        await new Promise((r) => setTimeout(r, 100))
      }
      throw new Error(`${timeout} ms 内请求没有停下来（在途 ${this.inflight} 个）`)
    },
  }
  const onRequest = (req) => {
    if (!match(req.url())) {
      req.continue().catch(() => {})
      return
    }
    log.started.push({ url: req.url(), at: Date.now() })
    log.inflight++
    log.maxInflight = Math.max(log.maxInflight, log.inflight)
    const go = () => req.continue().catch(() => {})
    if (log.delayMs > 0) setTimeout(go, log.delayMs)
    else go()
  }
  const onDone = (req) => {
    if (match(req.url())) log.inflight--
  }
  await page.setRequestInterception(true)
  page.on('request', onRequest)
  page.on('requestfinished', onDone)
  page.on('requestfailed', onDone)
  try {
    return await fn(log)
  } finally {
    log.delayMs = 0
    await log.waitQuiet(200, 10000).catch(() => {})
    page.off('request', onRequest)
    page.off('requestfinished', onDone)
    page.off('requestfailed', onDone)
    await page.setRequestInterception(false)
  }
}

/** 时间戳（文件夹名、编码里用）。 */
export function stamp() {
  return new Date().toISOString().replace(/[-:T]/g, '').slice(0, 14)
}

async function childFolder(admin, parentId, name) {
  const c = await admin('GET', `/api/folders/${parentId}/children`)
  return c.folders.find((f) => f.folder_name === name) ?? null
}

/** 文件夹里直接放的文件（按 sort_no），文件名 → file_id。 */
async function filesOf(admin, folderId) {
  const c = await admin('GET', `/api/folders/${folderId}/children`)
  return c.files
}

async function userId(admin, username) {
  const page = await admin('GET', `/api/system/users?keyword=${encodeURIComponent(username)}&page_size=100`)
  const u = page.items.find((x) => x.username === username)
  if (!u) throw new Error(`找不到用户 ${username}`)
  return u
}

/** 归类树里一层：编码 → 节点。 */
async function categories(admin, pid, parentId) {
  const q = parentId ? `?parent_id=${parentId}` : ''
  return (await admin('GET', `/api/projects/${pid}/archive-categories${q}`)).items
}

async function ensureCategory(admin, pid, parentId, type, code, name) {
  const found = (await categories(admin, pid, parentId)).find((c) => c.category_code === code)
  if (found) return found
  return admin('POST', `/api/projects/${pid}/archive-categories`, {
    parent_id: parentId,
    category_type: type,
    category_code: code,
    category_name: name,
  })
}

/**
 * 建好（或找到）M3-7 的演示数据，返回 {project, folders: {top, box1, box2, box3, extra}, files: {box1, box2, box3},
 * categories: {fonds, org, d30}, items: [{id, archive_no, item_no, title, status, file_ids}]}。
 */
export async function ensureM37Demo({ origin, admin, demoPassword = 'demo123456' }) {
  const project = (await admin('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-A')
  if (!project) throw new Error('找不到 DEMO-A，先跑 seed:demo')
  const pid = project.id
  const top = await childFolder(admin, project.root_folder_id, M37_FOLDER)
  const box1 = top && (await childFolder(admin, top.id, '盒 01'))
  const box2 = top && (await childFolder(admin, top.id, '盒 02'))
  const box3 = top && (await childFolder(admin, top.id, '盒 03'))
  const extra = box3 && (await childFolder(admin, box3.id, '补扫'))
  if (!top || !box1 || !box2 || !box3 || !extra) {
    throw new Error(
      `找不到 DEMO-A/${M37_FOLDER} 的演示文件夹，先用 seed:files 传好：\n` +
        `  npm run seed:files -- --project DEMO-A --folder "${M37_FOLDER}/盒 01" --count 20 --remark "M3-7 看板演示：审核和归类并行"\n` +
        `  npm run seed:files -- --project DEMO-A --folder "${M37_FOLDER}/盒 02" --count 16 --low-dpi 1\n` +
        `  npm run seed:files -- --project DEMO-A --folder "${M37_FOLDER}/盒 03" --count 8\n` +
        `  npm run seed:files -- --project DEMO-A --folder "${M37_FOLDER}/盒 03/补扫" --count 4\n` +
        '  （都加 --base 和 --user）',
    )
  }
  const ids = (list, from, to) => list.slice(from - 1, to).map((f) => f.file_id)

  // ---- 审核（只在还是待分配时做） ----
  const reviewer = await userId(admin, 'demo_reviewer')
  const before1 = await filesOf(admin, box1.id)
  if (before1.every((f) => f.review_status === 'unassigned')) {
    const before2 = await filesOf(admin, box2.id)
    await admin('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: reviewer.id, file_ids: ids(before1, 1, 20) })
    await admin('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: reviewer.id, file_ids: ids(before2, 1, 10) })
    const rev = await apiLogin(origin, 'demo_reviewer', demoPassword)
    await rev('POST', `/api/projects/${pid}/reviews:approve`, { file_ids: [...ids(before1, 1, 14), ...ids(before2, 1, 8)] })
    for (const fid of ids(before1, 15, 16)) {
      await rev('POST', `/api/files/${fid}/review`, { result: 'rejected', reject_reasons: ['blur'], comment: 'M3-7 演示：字迹模糊' })
    }
  }
  const f1 = await filesOf(admin, box1.id)
  const f2 = await filesOf(admin, box2.id)
  const f3 = await filesOf(admin, box3.id)

  // ---- 归类树（经接口） ----
  const fonds = await ensureCategory(admin, pid, null, 'fonds', M37_FONDS_CODE, M37_FONDS_NAME)
  const cat = await ensureCategory(admin, pid, fonds.id, 'category', 'WS', '文书')
  const year = await ensureCategory(admin, pid, cat.id, 'year', '2025', '2025 年')
  const ret = await ensureCategory(admin, pid, year.id, 'retention', 'Y', '永久')
  const org = await ensureCategory(admin, pid, ret.id, 'org', 'BGS', '办公室')
  const d30 = await ensureCategory(admin, pid, year.id, 'retention', 'D30', '30 年')

  // ---- 条目和挂接（M3-6 起经接口；已有的按件号找到就不动） ----
  const specs = [
    { category: org, item_no: 1, archive_no: 'Z109-WS·2025-Y-BGS-0001', title: '关于开展 2025 年档案数字化工作的通知', status: 'locked', files: ids(f1, 1, 6) },
    { category: org, item_no: 2, archive_no: 'Z109-WS·2025-Y-BGS-0002', title: '办公室 2025 年上半年工作总结', status: 'done', files: ids(f1, 7, 16) },
    { category: org, item_no: 3, archive_no: 'Z109-WS·2025-Y-BGS-0003', title: '关于调整档案库房管理制度的请示', status: 'draft', files: ids(f2, 1, 12) },
    { category: d30, item_no: 1, archive_no: null, title: '', status: 'draft', files: ids(f3, 1, 4) },
  ]
  const items = []
  const listed = new Map()
  const required = await requiredFieldValues(admin, pid)
  for (const s of specs) {
    if (!listed.has(s.category.id)) listed.set(s.category.id, await itemsIn(admin, s.category.id))
    const have = listed.get(s.category.id).find((x) => x.item_no === s.item_no)
    if (have) {
      items.push({ id: have.id, ...s })
      continue
    }
    // 带 file_ids 新建（一个事务挂上），要完成、定稿的补上必填的著录项（第 1 件的页都已通过，满足"定稿要求全部页通过"）。
    let it = await admin('POST', `/api/archive-categories/${s.category.id}/items`, {
      title: s.title,
      extra_fields: s.status === 'draft' ? {} : required,
      file_ids: s.files,
    })
    if (it.item_no !== s.item_no) throw new Error(`「${s.category.category_name}」新建的条目是第 ${it.item_no} 件，应是第 ${s.item_no} 件（节点里有别的条目？）`)
    if (s.status !== 'draft') it = await admin('POST', `/api/archive-items/${it.id}/mark-done`, { row_version: it.row_version })
    if (s.status === 'locked') it = await admin('POST', `/api/archive-items/${it.id}/lock`, { row_version: it.row_version })
    listed.get(s.category.id).push(it)
    items.push({ id: it.id, ...s })
  }
  return {
    project,
    folders: { top, box1, box2, box3, extra },
    files: { box1: f1, box2: f2, box3: f3 },
    categories: { fonds, org, d30 },
    items,
  }
}

// ---------------------------------------------------------------------------
// M3-5 的演示数据（归类页 /catalog 的截图用；常驻，M3-6、M3-8 可以接着用）：DEMO-A 下的
// - 著录项（DEMO-A 的 settings 里还没有 item_fields 时才写，只写 item_fields 这一项）：责任者（文本、必填、最长 100）、
//   文号（文本、最长 50）、日期（日期、必填）、密级（选项 公开/内部/秘密、默认 内部）、备注（文本、最长 1000）；
// - demo_cataloger 加进 DEMO-A 的个人授权（只有 files、catalog 菜单，看不到解锁）；
// - 文件夹「M3-5 归类演示」12 页（经上传接口），全部分给 demo_reviewer，第 1–10 页通过、11–12 待审；
// - 归类树（经接口）：全宗 Z201「M3-5 归类演示」/ 门类 WS「文书档案」/ 年度 2025「2025 年」/ 保管期限 Y「永久」/
//   机构 BGS「办公室」、RSC「人事处」，2025 年下另有保管期限 D30「30 年」（没有机构这一级，条目的档号为空）、D10「10 年」；
// - 条目（经接口建、著录、改状态，demo_cataloger 操作；挂接在 M3-3 合入前是直接写库造的——doclite_dev 里现在的就是，
//   M3-6 起缺页时经 PUT 挂接）：
//   BGS 第 1 件「关于 2025 年档案工作要点的通知」已定稿（第 1–4 页）；第 2 件「办公室第三季度工作总结」已完成（第 5–8 页
//   和第 11 页，1 页待审，不能定稿）；第 3 件「关于开展档案数字化工作的请示」草稿（第 9–10 页）；第 4 件没填题名；
//   第 5 件「会议纪要（第 12 期）」草稿。RSC 第 1、2 件草稿。D30 第 1 件草稿（档号为空：缺机构）。第 12 页不挂接。
// 已经建过（归类树里有全宗 Z201）就只补缺的部分。
// ---------------------------------------------------------------------------

export const M35_FOLDER = 'M3-5 归类演示'
export const M35_FONDS_CODE = 'Z201'
export const M35_FONDS_NAME = 'M3-5 归类演示'

export const M35_ITEM_FIELDS = [
  { key: 'author', label: '责任者', type: 'text', required: true, options: [], max_length: 100, default: null },
  { key: 'doc_no', label: '文号', type: 'text', required: false, options: [], max_length: 50, default: null },
  { key: 'doc_date', label: '日期', type: 'date', required: true, options: [], max_length: null, default: null },
  { key: 'secret', label: '密级', type: 'enum', required: false, options: ['公开', '内部', '秘密'], max_length: null, default: '内部' },
  { key: 'remark', label: '备注', type: 'text', required: false, options: [], max_length: 1000, default: null },
]

/** 项目里存的 settings（原样，没补默认值）。 */
export function storedSettings(pid) {
  return JSON.parse(runSql(`SELECT settings::text FROM projects WHERE id = ${lit(pid)};`).trim() || '{}')
}

/** 直接放在一个归类节点里的条目（按件号，最多 2000 个）。 */
export async function itemsIn(call, categoryId) {
  return (await call('GET', `/api/archive-categories/${categoryId}/items?limit=2000`)).items
}

/** 项目里必填的著录项的演示值（标记完成、定稿要用）：text「演示」、date「2025」、enum 第一个选项。 */
export async function requiredFieldValues(call, pid) {
  const s = await call('GET', `/api/projects/${pid}/catalog-settings`)
  const out = {}
  for (const f of s.item_fields) {
    if (!f.required) continue
    out[f.key] = f.type === 'date' ? '2025' : f.type === 'enum' ? (f.options[0] ?? '') : '演示'
  }
  return out
}

/**
 * 把这些文件挂到条目末尾（M3-3 的 `PUT /api/archive-items/{id}/files`：读最新的页序和 row_version，追加后整份提交；
 * 被别人刚改过 409 时重读再试一次）。返回新的详情。M3-6 起取代原来直接写库的 linkPagesDirect。
 */
export async function linkPages(call, itemId, files) {
  for (let attempt = 0; ; attempt++) {
    const cur = await call('GET', `/api/archive-items/${itemId}`)
    const ids = [...cur.pages.map((p) => p.file_id), ...files.map((f) => f.file_id ?? f)]
    const r = await call('PUT', `/api/archive-items/${itemId}/files`, { row_version: cur.row_version, file_ids: ids }, { allow: attempt ? [] : [409] })
    if (!isErr(r)) return r
    if (r.code !== 'conflict') throw new Error(`挂接到条目 ${itemId} 失败：${r.status} ${r.code}: ${r.message}`)
  }
}

/** apiLogin 的 call 在 allow 的错误码上返回 {status: 数字, code, …}；成功的条目响应里 status 是 draft 这样的字符串。 */
function isErr(r) {
  return !!r && typeof r === 'object' && typeof r.status === 'number'
}

/** 清理前先解锁已定稿的临时条目（要 catalog 和 review_assign，admin 有）。返回解锁后的条目；解不了返回 null。 */
async function unlockForCleanup(call, item) {
  if (item.status !== 'locked') return item
  const r = await call('POST', `/api/archive-items/${item.id}/unlock`, { row_version: item.row_version }, { allow: [403, 404, 409] })
  if (isErr(r)) {
    console.warn(`  清理：条目 ${item.id} 已定稿、解不了锁（${r.status} ${r.code}），跳过`)
    return null
  }
  return r
}

/**
 * 删掉临时条目（截图脚本的清理，不抛错）：M3-3 的 `DELETE /api/archive-items/{id}?row_version=`（有页时一并移出），
 * 已定稿的先解锁；被别人刚改过 409 conflict 时重读再试一次。M3-6 起只走接口（去掉了原来直接写库的兜底）。
 */
export async function deleteItemsForCleanup(call, ids) {
  for (const id of ids) {
    try {
      for (let attempt = 0; attempt < 2; attempt++) {
        let item = await call('GET', `/api/archive-items/${id}`, undefined, { allow: [404] })
        if (isErr(item)) break // 已经不在了
        item = await unlockForCleanup(call, item)
        if (!item) break
        const r = await call('DELETE', `/api/archive-items/${id}?row_version=${item.row_version}`, undefined, { allow: [404, 409] })
        if (!isErr(r) || r.status === 404) break
        if (r.code === 'conflict' && attempt === 0) continue
        console.warn(`  清理：条目 ${id} 删不掉（${r.status} ${r.code}），跳过`)
        break
      }
    } catch (e) {
      console.warn(`  清理：条目 ${id} 出错：${e.message}`)
    }
  }
}

/**
 * 把临时条目的页全部移出（截图脚本的清理，之后才能删文件夹；不抛错）：已定稿的先解锁，再
 * `PUT /api/archive-items/{id}/files {row_version, file_ids: []}`（M3-3）。M3-6 起只走接口。
 */
export async function unlinkAllForCleanup(call, itemId) {
  try {
    for (let attempt = 0; attempt < 2; attempt++) {
      let item = await call('GET', `/api/archive-items/${itemId}`, undefined, { allow: [404] })
      if (isErr(item) || !item.page_count) return
      item = await unlockForCleanup(call, item)
      if (!item) return
      const r = await call('PUT', `/api/archive-items/${itemId}/files`, { row_version: item.row_version, file_ids: [] }, { allow: [404, 409] })
      if (!isErr(r) || r.status === 404) return
      if (r.code === 'conflict' && attempt === 0) continue
      console.warn(`  清理：条目 ${itemId} 移出页失败（${r.status} ${r.code}），跳过`)
      return
    }
  } catch (e) {
    console.warn(`  清理：移出条目 ${itemId} 的页出错：${e.message}`)
  }
}

// ---------------------------------------------------------------------------
// 虚拟滚动用的常驻节点（M3-5 审查：原来每跑一次建 1200 个、用完软删除，留下大量软删除的条目）：
// 保管期限 D10「10 年」下的机构 VS「M3-5 虚拟滚动（常驻）」，条目不够 1200 个时补足，以后都复用。
// ---------------------------------------------------------------------------

export const M35_VIRTUAL_CODE = 'VS'
export const M35_VIRTUAL_NAME = 'M3-5 虚拟滚动（常驻）'
export const M35_VIRTUAL_COUNT = 1200

export async function ensureM35Virtual(call, demo, count = M35_VIRTUAL_COUNT) {
  const node = await ensureCategory(call, demo.pid, demo.categories.d10.id, 'org', M35_VIRTUAL_CODE, M35_VIRTUAL_NAME)
  const have = (await call('GET', `/api/archive-categories/${node.id}/items?limit=1`)).total
  let next = have
  if (have < count) {
    const t0 = Date.now()
    await Promise.all(
      Array.from({ length: 8 }, async () => {
        while (next < count) {
          const i = ++next
          await call('POST', `/api/archive-categories/${node.id}/items`, { title: `虚拟滚动测试条目 ${i}` })
        }
      }),
    )
    console.log(`  「${M35_VIRTUAL_NAME}」补了 ${count - have} 个条目（${Date.now() - t0} ms）`)
  }
  return node
}

async function ensureFolder(call, parentId, name, remark) {
  return call('POST', '/api/folders', { parent_id: parentId, folder_name: name, remark, if_exists: 'return' })
}

/** 建好（或找到）M3-5 的演示数据，返回 {project, pid, folder, files, categories, items, users}。 */
export async function ensureM35Demo({ origin, admin, demoPassword = 'demo123456' }) {
  const project = (await admin('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-A')
  if (!project) throw new Error('找不到 DEMO-A，先跑 seed:demo')
  const pid = project.id

  // ---- 著录项（只在还没有时写，只动 item_fields） ----
  const stored = storedSettings(pid)
  if (!Array.isArray(stored.item_fields) || stored.item_fields.length === 0) {
    await admin('PATCH', `/api/system/projects/${pid}`, { settings: { ...stored, item_fields: M35_ITEM_FIELDS } })
    console.log('  DEMO-A 写入了著录项（责任者、文号、日期、密级、备注）')
  }

  // ---- demo_cataloger 进 DEMO-A ----
  const access = await admin('GET', `/api/projects/${pid}/access`)
  const cataloger = await userId(admin, 'demo_cataloger')
  if (!access.users.some((u) => u.id === cataloger.id)) {
    await admin('PUT', `/api/projects/${pid}/access`, {
      user_ids: [...access.users.map((u) => u.id), cataloger.id],
      role_ids: access.roles.map((r) => r.id),
    })
    console.log('  demo_cataloger 加进了 DEMO-A 的授权')
  }
  const cat = await apiLogin(origin, 'demo_cataloger', demoPassword)
  const manager = await apiLogin(origin, 'demo_manager', demoPassword)
  const adminMe = await admin('GET', '/api/me')

  // ---- 文件夹和 12 页 ----
  const folder = await ensureFolder(admin, project.root_folder_id, M35_FOLDER, 'M3-5 归类页演示：归类树和条目著录')
  let files = await filesOf(admin, folder.id)
  for (let n = files.length + 1; n <= 12; n++) {
    await uploadPage(admin, folder.id, `CAT_${String(n).padStart(4, '0')}.png`, 'm35-catalog', n)
  }
  files = await filesOf(admin, folder.id)
  if (files.length < 12) throw new Error(`「${M35_FOLDER}」应有 12 页，现在 ${files.length} 页`)
  if (files.every((f) => f.review_status === 'unassigned')) {
    const reviewer = await userId(admin, 'demo_reviewer')
    await admin('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: reviewer.id, file_ids: files.map((f) => f.file_id) })
    const rev = await apiLogin(origin, 'demo_reviewer', demoPassword)
    await rev('POST', `/api/projects/${pid}/reviews:approve`, { file_ids: files.slice(0, 10).map((f) => f.file_id) })
    files = await filesOf(admin, folder.id)
  }
  const page = (n) => files[n - 1]

  // ---- 归类树 ----
  const fonds = await ensureCategory(admin, pid, null, 'fonds', M35_FONDS_CODE, M35_FONDS_NAME)
  const ws = await ensureCategory(admin, pid, fonds.id, 'category', 'WS', '文书档案')
  const year = await ensureCategory(admin, pid, ws.id, 'year', '2025', '2025 年')
  const y = await ensureCategory(admin, pid, year.id, 'retention', 'Y', '永久')
  const bgs = await ensureCategory(admin, pid, y.id, 'org', 'BGS', '办公室')
  const rsc = await ensureCategory(admin, pid, y.id, 'org', 'RSC', '人事处')
  const d30 = await ensureCategory(admin, pid, year.id, 'retention', 'D30', '30 年')
  const d10 = await ensureCategory(admin, pid, year.id, 'retention', 'D10', '10 年')

  // ---- 条目 ----
  const listOf = async (categoryId) => (await cat('GET', `/api/archive-categories/${categoryId}/items?limit=2000`)).items
  const specs = [
    { category: bgs, no: 1, title: '关于 2025 年档案工作要点的通知', fields: { author: '市档案馆办公室', doc_no: '档办〔2025〕3 号', doc_date: '2025-01-15', secret: '内部' }, pages: [1, 2, 3, 4], status: 'locked' },
    { category: bgs, no: 2, title: '办公室第三季度工作总结', fields: { author: '市档案馆办公室', doc_date: '2025-10', secret: '内部' }, pages: [5, 6, 7, 8, 11], status: 'done' },
    { category: bgs, no: 3, title: '关于开展档案数字化工作的请示', fields: { author: '市档案馆办公室', doc_no: '档办〔2025〕17 号', doc_date: '2025-03-14', secret: '内部' }, pages: [9, 10], status: 'draft' },
    { category: bgs, no: 4, title: '', fields: {}, pages: [], status: 'draft' },
    { category: bgs, no: 5, title: '会议纪要（第 12 期）', fields: { author: '办公室', doc_date: '2025-06' }, pages: [], status: 'draft' },
    { category: rsc, no: 1, title: '关于 2025 年度职工培训计划的通知', fields: { author: '人事处', doc_date: '2025-02-20', secret: '公开' }, pages: [], status: 'draft' },
    { category: rsc, no: 2, title: '人事处 2025 年上半年工作总结', fields: { author: '人事处', doc_date: '2025-07' }, pages: [], status: 'draft' },
    { category: d30, no: 1, title: '2025 年度库房温湿度记录', fields: { author: '保管部', doc_date: '2025' }, pages: [], status: 'draft' },
  ]
  const items = []
  for (const s of specs) {
    let it = (await listOf(s.category.id)).find((x) => x.item_no === s.no)
    if (!it) {
      const created = await cat('POST', `/api/archive-categories/${s.category.id}/items`, { title: s.title, extra_fields: s.fields })
      if (created.item_no !== s.no) throw new Error(`「${s.category.category_name}」新建的条目是第 ${created.item_no} 件，应是第 ${s.no} 件（节点里有别的条目？）`)
      it = created
    }
    let detail = await cat('GET', `/api/archive-items/${it.id}`)
    if (s.pages.length && detail.page_count === 0) {
      // M3-6 起经 PUT 挂接（原来在 M3-3 合入前直接写库）。
      detail = await linkPages(cat, it.id, s.pages.map(page))
    }
    if (s.status !== 'draft' && detail.status === 'draft') {
      detail = await cat('POST', `/api/archive-items/${it.id}/mark-done`, { row_version: detail.row_version })
    }
    if (s.status === 'locked' && detail.status === 'done') {
      detail = await cat('POST', `/api/archive-items/${it.id}/lock`, { row_version: detail.row_version })
    }
    items.push({ ...detail, spec: s })
  }
  return {
    project,
    pid,
    folder,
    files,
    categories: { fonds, ws, year, y, bgs, rsc, d30, d10 },
    items,
    users: { cataloger: cat, manager, adminId: adminMe.id, catalogerId: cataloger.id },
  }
}

// ---------------------------------------------------------------------------
// M3-6 的演示数据（挂接工作台的截图用；常驻，M3-8 可以接着用）。全部经接口：
// - 文件夹「M3-6 挂接演示」24 页（WB_0001–WB_0024.png，经上传接口），第 1–20 页分给 demo_reviewer：1–14 通过、15 驳回
//   （模糊）、16–20 待审；21–24 待分配；
// - 归类树：全宗 Z206「M3-6 挂接演示」/ 门类 WS「文书档案」/ 年度 2025「2025 年」/ 保管期限 Y「永久」/ 机构 BGS「办公室」；
// - 条目（demo_cataloger 带 file_ids 新建，M3-3）：BGS 第 1 件「关于印发档案数字化挂接规范的通知」草稿，第 1–4、15、16
//   页（通过、驳回、待审都有）；第 2 件「2025 年第三季度办公会议纪要」已完成，第 5–8 页。其余 16 页（9–14、17–24）在
//   待归类池里（"仅未归类"）。
// 截图脚本不改这些（只看、只选）；要挂接、拖动、删除的都另建临时的文件夹、节点、条目，用完删掉。
// demo_cataloger 要能进 DEMO-A（M3-5 的 ensureM35Demo 已经加了授权；没有时这里补上）。
// ---------------------------------------------------------------------------

export const M36_FOLDER = 'M3-6 挂接演示'
export const M36_FONDS_CODE = 'Z206'
export const M36_FONDS_NAME = 'M3-6 挂接演示'
export const M36_PAGES = 24

/** 建好（或找到）M3-6 的演示数据，返回 {project, pid, folder, files, categories, items, users}。 */
export async function ensureM36Demo({ origin, admin, demoPassword = 'demo123456' }) {
  const project = (await admin('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-A')
  if (!project) throw new Error('找不到 DEMO-A，先跑 seed:demo')
  const pid = project.id

  // ---- demo_cataloger 进 DEMO-A ----
  const access = await admin('GET', `/api/projects/${pid}/access`)
  const cataloger = await userId(admin, 'demo_cataloger')
  if (!access.users.some((u) => u.id === cataloger.id)) {
    await admin('PUT', `/api/projects/${pid}/access`, {
      user_ids: [...access.users.map((u) => u.id), cataloger.id],
      role_ids: access.roles.map((r) => r.id),
    })
    console.log('  demo_cataloger 加进了 DEMO-A 的授权')
  }
  const cat = await apiLogin(origin, 'demo_cataloger', demoPassword)
  const rev = await apiLogin(origin, 'demo_reviewer', demoPassword)
  const reviewer = await userId(admin, 'demo_reviewer')

  // ---- 文件夹和 24 页 ----
  const folder = await ensureFolder(admin, project.root_folder_id, M36_FOLDER, 'M3-6 挂接工作台演示：待归类池')
  let files = await filesOf(admin, folder.id)
  for (let n = files.length + 1; n <= M36_PAGES; n++) {
    await uploadPage(admin, folder.id, `WB_${String(n).padStart(4, '0')}.png`, 'm36-workbench', n)
  }
  files = await filesOf(admin, folder.id)
  if (files.length < M36_PAGES) throw new Error(`「${M36_FOLDER}」应有 ${M36_PAGES} 页，现在 ${files.length} 页`)
  if (files.every((f) => f.review_status === 'unassigned')) {
    await admin('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: reviewer.id, file_ids: files.slice(0, 20).map((f) => f.file_id) })
    await rev('POST', `/api/projects/${pid}/reviews:approve`, { file_ids: files.slice(0, 14).map((f) => f.file_id) })
    await rev('POST', `/api/files/${files[14].file_id}/review`, { result: 'rejected', reject_reasons: ['blur'], comment: 'M3-6 演示：字迹模糊' })
    files = await filesOf(admin, folder.id)
  }
  const page = (n) => files[n - 1]

  // ---- 归类树 ----
  const fonds = await ensureCategory(admin, pid, null, 'fonds', M36_FONDS_CODE, M36_FONDS_NAME)
  const ws = await ensureCategory(admin, pid, fonds.id, 'category', 'WS', '文书档案')
  const year = await ensureCategory(admin, pid, ws.id, 'year', '2025', '2025 年')
  const y = await ensureCategory(admin, pid, year.id, 'retention', 'Y', '永久')
  const bgs = await ensureCategory(admin, pid, y.id, 'org', 'BGS', '办公室')

  // ---- 条目（带 file_ids 新建） ----
  const specs = [
    { no: 1, title: '关于印发档案数字化挂接规范的通知', fields: { author: '市档案馆办公室', doc_no: '档办〔2025〕21 号', doc_date: '2025-09-01', secret: '内部' }, pages: [1, 2, 3, 4, 15, 16], status: 'draft' },
    { no: 2, title: '2025 年第三季度办公会议纪要', fields: { author: '办公室', doc_date: '2025-10', secret: '内部' }, pages: [5, 6, 7, 8], status: 'done' },
  ]
  const items = []
  for (const s of specs) {
    let it = (await itemsIn(cat, bgs.id)).find((x) => x.item_no === s.no)
    if (!it) {
      it = await cat('POST', `/api/archive-categories/${bgs.id}/items`, { title: s.title, extra_fields: s.fields, file_ids: s.pages.map((n) => page(n).file_id) })
      if (it.item_no !== s.no) throw new Error(`「办公室」新建的条目是第 ${it.item_no} 件，应是第 ${s.no} 件`)
    }
    let detail = await cat('GET', `/api/archive-items/${it.id}`)
    if (s.status !== 'draft' && detail.status === 'draft') {
      detail = await cat('POST', `/api/archive-items/${it.id}/mark-done`, { row_version: detail.row_version })
    }
    items.push({ ...detail, spec: s })
  }
  const adminMe = await admin('GET', '/api/me')
  return {
    project,
    pid,
    folder,
    files,
    categories: { fonds, ws, year, y, bgs },
    items,
    users: { cataloger: cat, reviewer: rev, adminId: adminMe.id, catalogerId: cataloger.id, reviewerId: reviewer.id },
  }
}
