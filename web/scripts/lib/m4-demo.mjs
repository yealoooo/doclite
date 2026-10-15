// M4-5 的演示数据（导出页的截图用；常驻，M4-6 可以接着用）。全部经接口，在 DEMO-A 里：
// - 文件夹「M4-5 导出演示」16 页（EXP_0001–EXP_0016.png，合成的扫描页）：1–14 分给 demo_reviewer 并通过，15 待审
//   （demo_reviewer），16 待分配；
// - 文件夹「M4-5 导出演示（大页）」BIG_0001–BIG_00NN.png：A4 300 DPI 的 RGBA PNG（带一块半透明的印章）。PDF 放不进
//   带透明的 PNG，导出时 server 要解码再无损编码（M4-2 的转换），一页约零点几秒，用来截"生成中"的进度条；全部通过；
// - 归类树：全宗 Z405「M4-5 导出演示」/ 门类 WS「文书档案」/ 年度 2025「2025 年」/ 保管期限 Y「永久」/ 机构 BGS「办公室」、
//   DY「大页演示」；
// - BGS 的条目（demo_cataloger 带 file_ids 新建）：
//   1. 「关于 2025 年档案移交工作的通知」第 1–3 页，已定稿；
//   2. 「档案室第三季度工作总结」第 4–6 页，已定稿；
//   3. 「关于开展档案数字化成果验收的请示」第 7–9 页，已定稿；
//   4. 「2025 年第 4 期会议纪要」第 10、11、15 页（15 待审），著录完成没定稿 → 预检不就绪（没有定稿、有页不是通过）；
//   5. 「档案移交清册（待补）」没有页，草稿 → 预检不就绪（没有定稿、没有页）。
//   第 12–14、16 页不挂接（没归类的文件不会进任何导出）。
// - DY 的条目：「大页演示（第一册）」「大页演示（第二册）」各一半大页，已定稿。
// 截图脚本会对 BGS 第 1 件第 1 页做"撤回通过 → 驳回 → 重扫 → 通过"（交付后变更的演示），每跑一次多一个版本；
// ensureM45Demo 会把 1–3 件的页修回"通过"（中途失败留下的驳回、待审），保证它们就绪。

import { deflateSync, inflateRawSync, inflateSync } from 'node:zlib'

import { apiLogin, itemsIn, requiredFieldValues, uploadPage } from './m3-demo.mjs'
import { makeScanPage } from './scan-pages.mjs'

export const M45_FOLDER = 'M4-5 导出演示'
export const M45_BIG_FOLDER = 'M4-5 导出演示（大页）'
export const M45_FONDS_CODE = 'Z405'
export const M45_FONDS_NAME = 'M4-5 导出演示'
export const M45_PAGES = 16
/** 大页的页数（两件各一半）。 */
export const M45_BIG_PAGES = Number(process.env.M45_BIG_PAGES ?? 160)

const isErr = (r) => r && typeof r === 'object' && 'status' in r && 'code' in r && typeof r.status === 'number'

async function userId(admin, username) {
  const page = await admin('GET', `/api/system/users?keyword=${encodeURIComponent(username)}&page_size=100`)
  const u = page.items.find((x) => x.username === username)
  if (!u) throw new Error(`找不到用户 ${username}`)
  return u
}

async function ensureFolder(admin, parentId, name, remark) {
  const r = await admin('POST', '/api/folders', { parent_id: parentId, folder_name: name, remark, if_exists: 'return' })
  return r
}

async function filesOf(admin, folderId) {
  return (await admin('GET', `/api/folders/${folderId}/children`)).files
}

async function ensureCategory(admin, pid, parentId, type, code, name) {
  const q = parentId ? `?parent_id=${parentId}` : ''
  const found = (await admin('GET', `/api/projects/${pid}/archive-categories${q}`)).items.find((c) => c.category_code === code)
  if (found) return found
  return admin('POST', `/api/projects/${pid}/archive-categories`, { parent_id: parentId, category_type: type, category_code: code, category_name: name })
}

// ---------- 大页：RGBA PNG ----------

function crc32(buf) {
  let c
  const table = (crc32.table ??= Array.from({ length: 256 }, (_, n) => {
    c = n
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
    return c >>> 0
  }))
  let crc = 0xffffffff
  for (const b of buf) crc = table[(crc ^ b) & 0xff] ^ (crc >>> 8)
  return (crc ^ 0xffffffff) >>> 0
}

function chunk(type, data) {
  const len = Buffer.alloc(4)
  len.writeUInt32BE(data.length)
  const td = Buffer.concat([Buffer.from(type, 'latin1'), data])
  const crc = Buffer.alloc(4)
  crc.writeUInt32BE(crc32(td))
  return Buffer.concat([len, td, crc])
}

/** 把 makeScanPage 的 PNG（灰度或 RGB、过滤字节全是 0、一个 IDAT）转成 RGBA，右下角的印章区域半透明。 */
function toRgba(png, width, height, seedText) {
  let off = 8
  let ihdr = null
  const idat = []
  while (off < png.length) {
    const len = png.readUInt32BE(off)
    const type = png.toString('latin1', off + 4, off + 8)
    const data = png.subarray(off + 8, off + 8 + len)
    if (type === 'IHDR') ihdr = data
    if (type === 'IDAT') idat.push(data)
    off += 12 + len
  }
  const ch = ihdr[9] === 0 ? 1 : 3
  const raw = inflateSync(Buffer.concat(idat))
  const inStride = 1 + width * ch
  const outStride = 1 + width * 4
  const out = Buffer.alloc(outStride * height)
  const x0 = Math.round(width * 0.6)
  const y0 = Math.round(height * 0.78)
  for (let y = 0; y < height; y++) {
    out[y * outStride] = 0
    for (let x = 0; x < width; x++) {
      const i = y * inStride + 1 + x * ch
      const o = y * outStride + 1 + x * 4
      const r = raw[i]
      out[o] = r
      out[o + 1] = ch === 1 ? r : raw[i + 1]
      out[o + 2] = ch === 1 ? r : raw[i + 2]
      out[o + 3] = x >= x0 && y >= y0 ? 160 : 255
    }
  }
  const head = Buffer.alloc(13)
  head.writeUInt32BE(width, 0)
  head.writeUInt32BE(height, 4)
  head[8] = 8
  head[9] = 6 // RGBA
  const ppm = Math.round(300 / 0.0254)
  const phys = Buffer.alloc(9)
  phys.writeUInt32BE(ppm, 0)
  phys.writeUInt32BE(ppm, 4)
  phys[8] = 1
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', head),
    chunk('pHYs', phys),
    chunk('tEXt', Buffer.from(`Comment\0${seedText}`, 'latin1')),
    chunk('IDAT', deflateSync(out, { level: 6 })),
    chunk('IEND', Buffer.alloc(0)),
  ])
}

/** 上传一张大页（A4 300 DPI 的 RGBA PNG，带封面）。 */
async function uploadBigPage(call, folderId, name, n) {
  const p = makeScanPage({ seed: `m45-big ${n}`, pageNo: n, dpi: 300, color: true })
  const png = toRgba(p.png, p.width, p.height, `doclite-seed m45-big ${n}`)
  const form = new FormData()
  form.append('file_name', name)
  form.append('cover', new Blob([p.cover], { type: 'image/jpeg' }), 'cover.jpg')
  form.append('file', new Blob([png], { type: 'image/png' }), 'page.png')
  return call('POST', `/api/folders/${folderId}/files`, form)
}

// ---------- 审核：把页修回"通过" ----------

/**
 * 让这些文件都是"通过"：待分配的分给 demo_reviewer 再通过；待审的由 demo_reviewer 通过（分给别人的先改派）；驳回的
 * 重扫一版再通过（只在脚本中途失败、留下驳回时才会发生）。
 */
export async function ensureApproved(ctx, fileIds) {
  const { admin, rev, pid, reviewerId } = ctx
  for (let round = 0; round < 3; round++) {
    const st = await Promise.all(fileIds.map((id) => admin('GET', `/api/files/${id}/reviews`)))
    const byStatus = { unassigned: [], pending: [], rejected: [] }
    st.forEach((r, i) => {
      const s = r.review_status
      if (s && s !== 'approved') (byStatus[s] ??= []).push(fileIds[i])
    })
    if (!byStatus.unassigned.length && !byStatus.pending.length && !byStatus.rejected.length) return
    for (const id of byStatus.rejected) {
      const p = makeScanPage({ seed: `m45 repair ${id} ${Date.now()}`, pageNo: 1 })
      const form = new FormData()
      form.append('file_name', `repair_${id.slice(0, 8)}.png`)
      form.append('cover', new Blob([p.cover], { type: 'image/jpeg' }), 'cover.jpg')
      form.append('file', new Blob([p.png], { type: 'image/png' }), 'page.png')
      await admin('POST', `/api/files/${id}/rescan`, form)
    }
    if (byStatus.unassigned.length) {
      await admin('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: reviewerId, file_ids: byStatus.unassigned })
    }
    const pending = [...byStatus.pending, ...byStatus.unassigned, ...byStatus.rejected]
    if (pending.length) {
      const r = await rev('POST', `/api/projects/${pid}/reviews:approve`, { file_ids: pending }, { allow: [403, 409] })
      if (isErr(r)) {
        // 分给了别人（或者重扫后分配变了）：改派给 demo_reviewer 再通过。
        await admin('POST', `/api/projects/${pid}/review-assignments:reassign`, { to_reviewer_id: reviewerId, file_ids: pending }, { allow: [400, 404, 409] })
      }
    }
  }
}

// ---------- 建好（或找到）演示数据 ----------

export async function ensureM45Demo({ origin, admin, demoPassword = 'demo123456' }) {
  const project = (await admin('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-A')
  if (!project) throw new Error('找不到 DEMO-A，先跑 seed:demo')
  const pid = project.id
  const cat = await apiLogin(origin, 'demo_cataloger', demoPassword)
  const rev = await apiLogin(origin, 'demo_reviewer', demoPassword)
  const reviewer = await userId(admin, 'demo_reviewer')
  const ctx = { admin, rev, pid, reviewerId: reviewer.id }

  // ---- 文件夹和页 ----
  const folder = await ensureFolder(admin, project.root_folder_id, M45_FOLDER, 'M4-5 导出页演示：16 页（1–14 通过、15 待审、16 待分配）')
  let files = await filesOf(admin, folder.id)
  for (let n = files.length + 1; n <= M45_PAGES; n++) {
    await uploadPage(admin, folder.id, `EXP_${String(n).padStart(4, '0')}.png`, 'm45-export', n)
  }
  files = await filesOf(admin, folder.id)
  if (files.length < M45_PAGES) throw new Error(`「${M45_FOLDER}」应有 ${M45_PAGES} 页，现在 ${files.length} 页`)
  if (files.every((f) => f.review_status === 'unassigned')) {
    await admin('POST', `/api/projects/${pid}/review-assignments`, { reviewer_id: reviewer.id, file_ids: files.slice(0, 15).map((f) => f.file_id) })
    await rev('POST', `/api/projects/${pid}/reviews:approve`, { file_ids: files.slice(0, 14).map((f) => f.file_id) })
    files = await filesOf(admin, folder.id)
  }
  const page = (n) => files[n - 1]

  const big = await ensureFolder(admin, project.root_folder_id, M45_BIG_FOLDER, 'M4-5 导出页演示：A4 300 DPI 带透明的 PNG（导出 PDF 时要无损转换，截进度条用）')
  let bigFiles = await filesOf(admin, big.id)
  for (let n = bigFiles.length + 1; n <= M45_BIG_PAGES; n++) {
    await uploadBigPage(admin, big.id, `BIG_${String(n).padStart(4, '0')}.png`, n)
  }
  bigFiles = await filesOf(admin, big.id)
  if (bigFiles.some((f) => f.review_status !== 'approved')) await ensureApproved(ctx, bigFiles.map((f) => f.file_id))
  bigFiles = await filesOf(admin, big.id)

  // ---- 归类树 ----
  const fonds = await ensureCategory(admin, pid, null, 'fonds', M45_FONDS_CODE, M45_FONDS_NAME)
  const ws = await ensureCategory(admin, pid, fonds.id, 'category', 'WS', '文书档案')
  const year = await ensureCategory(admin, pid, ws.id, 'year', '2025', '2025 年')
  const y = await ensureCategory(admin, pid, year.id, 'retention', 'Y', '永久')
  const bgs = await ensureCategory(admin, pid, y.id, 'org', 'BGS', '办公室')
  const dy = await ensureCategory(admin, pid, y.id, 'org', 'DY', '大页演示')

  // ---- 条目 ----
  const required = await requiredFieldValues(cat, pid)
  const fields = (extra) => ({ ...required, ...extra })
  const specs = [
    { category: bgs, no: 1, title: '关于 2025 年档案移交工作的通知', fields: fields({ author: '市档案馆办公室', doc_no: '档办〔2025〕31 号', doc_date: '2025-09-12', secret: '内部' }), pages: [1, 2, 3], status: 'locked' },
    { category: bgs, no: 2, title: '档案室第三季度工作总结', fields: fields({ author: '档案室', doc_date: '2025-10', secret: '内部' }), pages: [4, 5, 6], status: 'locked' },
    { category: bgs, no: 3, title: '关于开展档案数字化成果验收的请示', fields: fields({ author: '市档案馆办公室', doc_no: '档办〔2025〕36 号', doc_date: '2025-10-08', secret: '内部' }), pages: [7, 8, 9], status: 'locked' },
    { category: bgs, no: 4, title: '2025 年第 4 期会议纪要', fields: fields({ author: '办公室', doc_date: '2025-10-10' }), pages: [10, 11, 15], status: 'done' },
    { category: bgs, no: 5, title: '档案移交清册（待补）', fields: {}, pages: [], status: 'draft' },
    { category: dy, no: 1, title: '大页演示（第一册）', fields: fields({ author: '保管部', doc_date: '2025' }), big: [0, Math.ceil(M45_BIG_PAGES / 2)], status: 'locked' },
    { category: dy, no: 2, title: '大页演示（第二册）', fields: fields({ author: '保管部', doc_date: '2025' }), big: [Math.ceil(M45_BIG_PAGES / 2), M45_BIG_PAGES], status: 'locked' },
  ]
  // 1–3 件的页要是"通过"（截图脚本的重扫演示中途失败时可能留下驳回、待审）。
  await ensureApproved(ctx, [1, 2, 3, 4, 5, 6, 7, 8, 9].map((n) => page(n).file_id))
  const items = []
  for (const s of specs) {
    // 按题名找（件号不回收：删掉重建的条目件号会往后排，s.no 只是第一次建时的件号）。
    let it = (await itemsIn(cat, s.category.id)).find((x) => x.title === s.title)
    const fileIds = s.big ? bigFiles.slice(s.big[0], s.big[1]).map((f) => f.file_id) : s.pages.map((n) => page(n).file_id)
    if (!it) {
      it = await cat('POST', `/api/archive-categories/${s.category.id}/items`, { title: s.title, extra_fields: s.fields, file_ids: fileIds })
    }
    let detail = await cat('GET', `/api/archive-items/${it.id}`)
    if (s.status !== 'draft' && detail.status === 'draft') {
      detail = await cat('POST', `/api/archive-items/${it.id}/mark-done`, { row_version: detail.row_version })
    }
    if (s.status === 'locked' && detail.status === 'done') {
      detail = await cat('POST', `/api/archive-items/${it.id}/lock`, { row_version: detail.row_version })
    }
    items.push({ ...detail, spec: s })
  }
  const adminMe = await admin('GET', '/api/me')
  return {
    project,
    pid,
    folder,
    bigFolder: big,
    files,
    bigFiles,
    categories: { fonds, ws, year, y, bgs, dy },
    items,
    users: { cataloger: cat, reviewer: rev, reviewerId: reviewer.id, adminId: adminMe.id },
  }
}

// ---------- 读导出的 zip（不加依赖：按中央目录读，支持 stored、deflate 和 zip64 的大小字段） ----------

/** 读 zip：返回 [{name, data: Buffer}]（按中央目录的顺序）。 */
export function readZip(buf) {
  // 找中央目录结束记录（从尾部往前，注释最长 64 KiB）。
  let eocd = -1
  for (let i = buf.length - 22; i >= Math.max(0, buf.length - 22 - 65535); i--) {
    if (buf.readUInt32LE(i) === 0x06054b50) {
      eocd = i
      break
    }
  }
  if (eocd < 0) throw new Error('不是 zip（找不到中央目录结束记录）')
  let count = buf.readUInt16LE(eocd + 10)
  let cdOffset = buf.readUInt32LE(eocd + 16)
  // zip64 的结束记录定位器在 EOCD 前 20 字节。
  if (eocd >= 20 && buf.readUInt32LE(eocd - 20) === 0x07064b50) {
    const z64 = Number(buf.readBigUInt64LE(eocd - 20 + 8))
    count = Number(buf.readBigUInt64LE(z64 + 32))
    cdOffset = Number(buf.readBigUInt64LE(z64 + 48))
  }
  const out = []
  let p = cdOffset
  for (let n = 0; n < count; n++) {
    if (buf.readUInt32LE(p) !== 0x02014b50) throw new Error(`中央目录第 ${n + 1} 项的签名不对`)
    const method = buf.readUInt16LE(p + 10)
    let csize = buf.readUInt32LE(p + 20)
    let usize = buf.readUInt32LE(p + 24)
    const nameLen = buf.readUInt16LE(p + 28)
    const extraLen = buf.readUInt16LE(p + 30)
    const commentLen = buf.readUInt16LE(p + 32)
    let local = buf.readUInt32LE(p + 42)
    const name = buf.toString('utf8', p + 46, p + 46 + nameLen)
    // zip64 扩展字段（0x0001）：按顺序放被设成 0xFFFFFFFF 的那几个。
    let e = p + 46 + nameLen
    const end = e + extraLen
    while (e + 4 <= end) {
      const id = buf.readUInt16LE(e)
      const size = buf.readUInt16LE(e + 2)
      if (id === 0x0001) {
        let q = e + 4
        if (usize === 0xffffffff) {
          usize = Number(buf.readBigUInt64LE(q))
          q += 8
        }
        if (csize === 0xffffffff) {
          csize = Number(buf.readBigUInt64LE(q))
          q += 8
        }
        if (local === 0xffffffff) local = Number(buf.readBigUInt64LE(q))
      }
      e += 4 + size
    }
    if (buf.readUInt32LE(local) !== 0x04034b50) throw new Error(`${name} 的本地头签名不对`)
    const dataAt = local + 30 + buf.readUInt16LE(local + 26) + buf.readUInt16LE(local + 28)
    const raw = buf.subarray(dataAt, dataAt + csize)
    const data = method === 0 ? Buffer.from(raw) : method === 8 ? inflateRawSync(raw) : null
    if (!data) throw new Error(`${name} 的压缩方式 ${method} 不认识`)
    if (data.length !== usize) throw new Error(`${name} 解出 ${data.length} 字节，中央目录写的是 ${usize}`)
    out.push({ name, data })
    p += 46 + nameLen + extraLen + commentLen
  }
  return out
}

/** 解析 CSV（逗号分隔、双引号转义、CRLF），去掉开头的 BOM。返回行的数组（第一行是表头）。 */
export function parseCsv(text) {
  if (text.charCodeAt(0) === 0xfeff) text = text.slice(1)
  const rows = []
  let row = []
  let field = ''
  let quoted = false
  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    if (quoted) {
      if (c === '"' && text[i + 1] === '"') {
        field += '"'
        i++
      } else if (c === '"') quoted = false
      else field += c
    } else if (c === '"') quoted = true
    else if (c === ',') {
      row.push(field)
      field = ''
    } else if (c === '\r' && text[i + 1] === '\n') {
      row.push(field)
      rows.push(row)
      row = []
      field = ''
      i++
    } else if (c === '\n') {
      row.push(field)
      rows.push(row)
      row = []
      field = ''
    } else field += c
  }
  if (field || row.length) {
    row.push(field)
    rows.push(row)
  }
  return rows
}
