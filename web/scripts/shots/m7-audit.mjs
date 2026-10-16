// M7-3 的截图清单和断言：系统 → 审计日志（只读列表）。
//
//   INIT_ADMIN_PASSWORD=admin123456 npm run screenshots -- --spec scripts/shots/m7-audit.mjs --base <前端地址>
//
// 前端地址是托管了 web/dist 的 server（或 vite preview，/api 代理到 server）。脚本在页面之外调接口时也走这个地址。
// 数据：seed:demo 的 DEMO-A，库里已有几百条以上的审计（截图脚本跑过一遍就有）。脚本先用 admin 在 DEMO-A 根目录下
// 建一个"截图验证 M7-3 审计 <时间>"文件夹、改备注、再删掉（留下 3 条审计：新建、修改、删除；文件夹是软删除）。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - 列表按时间倒序（id 倒序），第一页 50 条，有"加载更多"；点了之后是 100 条、接在后面、没有重复；
// - 按项目 + 对象类型筛选（地址栏参数）：每一行都是 DEMO-A 的文件夹，刚才的删除、修改、新建都在，操作译成中文；
// - 展开"修改"那一条：备注的之前、之后；
// - 筛选栏操作（日期 = 今天、操作人 = admin、对象 = 文件夹、操作 = 删除）后地址栏同步、结果都符合；
// - 不认识的对象类型、动作原样显示，存储迁移（快照改位置、开始、结束）、许可证的审计译成中文（页面里包一层 fetch，往列表
//   响应里加五条假的，detail 照 server 写的样子）。
//
// 另有一张是 M7-3 的另一半（上传时拒绝导出不了的罕见编码）：在 DEMO-A 根目录下临时建一个文件夹，浏览器里上传
// shared/imagehdr/testdata/encoding 的两个夹具（算术编码的 JPEG、JPEG 2000 压缩的 TIFF），上传面板里显示服务器给的
// 原因，什么都没入库；做完删掉临时文件夹。

import { readFile } from 'node:fs/promises'

import { injectFiles, waitUploadDone } from '../lib/upload-samples.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const PROJECT = 'DEMO-A'
const ROWS = '[data-testid=audit-row]'

// ---------- 页面之外调接口（admin） ----------

let apiState = null

async function api(origin, method, path, body) {
  apiState ??= { cookie: '' }
  const res = await fetch(origin + path, {
    method,
    headers: {
      Accept: 'application/json',
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...(apiState.cookie ? { Cookie: apiState.cookie } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const sc = res.headers.get('set-cookie')
  if (sc) apiState.cookie = sc.split(';')[0]
  const text = await res.text()
  const data = text ? JSON.parse(text) : undefined
  if (!res.ok) throw new Error(`${method} ${path} → ${res.status} ${data?.code}: ${data?.message}`)
  return data
}

function stamp() {
  const d = new Date()
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`
}

function today() {
  const d = new Date()
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

let ctxPromise = null
let tempFolder = null
/** 登录 admin，取 DEMO-A，建、改、删一个文件夹留下三条审计（只做一次）。 */
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const me = await api(origin, 'POST', '/api/auth/login', { username: admin, password: adminPassword })
    const projects = await api(origin, 'GET', '/api/projects')
    const project = projects.find((p) => p.project_code === PROJECT)
    if (!project) throw new Error(`找不到项目 ${PROJECT}，先跑 seed:demo`)
    const name = `截图验证 M7-3 审计 ${stamp()}`
    const folder = await api(origin, 'POST', '/api/folders', { parent_id: project.root_folder_id, folder_name: name })
    await api(origin, 'PATCH', `/api/folders/${folder.id}`, { remark: '审计页截图：改了备注' })
    await api(origin, 'DELETE', `/api/folders/${folder.id}`)
    return { origin, project, folder, name, adminId: me.id }
  })()
  return ctxPromise
}

// ---------- 页面上的读取 ----------

/** 当前列表的行：id、类型、动作、各列文字。 */
function readRows(page) {
  return page.$$eval(ROWS, (rows) =>
    rows.map((r) => {
      const cells = [...r.querySelectorAll('td')].map((td) => td.textContent?.replace(/\s+/g, ' ').trim() ?? '')
      return {
        id: Number(r.dataset.auditId),
        type: r.dataset.targetType,
        target: r.dataset.targetId,
        action: r.dataset.action,
        time: cells[1],
        operator: cells[2],
        project: cells[3],
        object: cells[4],
        actionText: cells[5],
        summary: cells[6],
      }
    }),
  )
}

function assertDescending(rows, label) {
  for (let i = 1; i < rows.length; i++) {
    if (!(rows[i - 1].id > rows[i].id)) throw new Error(`${label}：没有按 id 倒序（${rows[i - 1].id}、${rows[i].id}）`)
  }
}

/** 等列表加载完（不在"加载中"）。 */
async function waitList(page) {
  await page.waitForFunction(
    () => !document.body.textContent?.includes('加载中…') && document.querySelector('[data-testid=audit-count]')?.textContent?.includes('已加载'),
    { timeout: 15000 },
  )
}

/** 往审计列表的响应前面加几条假的（不认识的类型、动作，存储迁移、许可证），看页面怎么显示。 */
async function injectFakeItems(page) {
  await page.evaluate(() => {
    if (window.__auditFake) return
    window.__auditFake = true
    const orig = window.fetch.bind(window)
    const fake = [
      {
        id: 900000003, created_at: new Date().toISOString(), operator_id: null, operator_name: null, operator_username: null,
        project_id: null, project_code: null, project_name: null, target_type: 'mystery_type',
        target_id: '01a1ffff-0000-7000-8000-000000000003', action: 'mystery_action', detail: { foo: 'bar', after: { level: 3 } },
      },
      // 存储迁移的三种审计，detail 照 server/internal/storage 写的样子（M7-2）。
      {
        id: 900000002, created_at: new Date().toISOString(), operator_id: null, operator_name: '系统管理员', operator_username: 'admin',
        project_id: null, project_code: 'DEMO-A', project_name: '演示项目 A · 文书档案', target_type: 'file_snapshot',
        target_id: '01a1ffff-0000-7000-8000-000000000002', action: 'storage_migrate',
        detail: {
          job_id: '01a1ffff-0000-7000-8000-000000000010', file_id: '01a1ffff-0000-7000-8000-000000000011',
          file_name: 'IMG_0001.png', version_no: 1,
          before: { storage_profile_id: 'p-src', storage_profile_name: '旧存储', storage_key: 'doclite/DEMO-A/x/v1.png', cover_key: 'doclite/DEMO-A/x/v1.cover.jpg' },
          after: { storage_profile_id: 'p-dst', storage_profile_name: '新存储', storage_key: 'archive/DEMO-A/x/v1.png', cover_key: 'archive/DEMO-A/x/v1.cover.jpg' },
        },
      },
      {
        id: 900000005, created_at: new Date().toISOString(), operator_id: null, operator_name: '系统管理员', operator_username: 'admin',
        project_id: null, project_code: null, project_name: null, target_type: 'storage_profile',
        target_id: '01a1ffff-0000-7000-8000-000000000005', action: 'migrate_start',
        detail: { job_id: '01a1ffff-0000-7000-8000-000000000010', profile_name: '旧存储', target_profile_id: 'p-dst', target_profile_name: '新存储', total: 25 },
      },
      {
        id: 900000004, created_at: new Date().toISOString(), operator_id: null, operator_name: null, operator_username: null,
        project_id: null, project_code: null, project_name: null, target_type: 'storage_profile',
        target_id: '01a1ffff-0000-7000-8000-000000000005', action: 'migrate_end',
        detail: {
          job_id: '01a1ffff-0000-7000-8000-000000000010', result: 'done', profile_name: '旧存储', target_profile_id: 'p-dst',
          target_profile_name: '新存储', total: 25, migrated: 24, failed: 1, failed_objects: 1, remaining: 1,
        },
      },
      {
        id: 900000001, created_at: new Date().toISOString(), operator_id: null, operator_name: '系统管理员', operator_username: 'admin',
        project_id: null, project_code: null, project_name: null, target_type: 'license',
        target_id: '01a1ffff-0000-7000-8000-000000000001', action: 'replace',
        detail: {
          before: { company: '演示公司', edition: 'trial', max_users: 5, expires_at: '2026-11-01T00:00:00Z' },
          after: { company: '演示公司', edition: 'standard', max_users: 20, expires_at: '2027-10-16T00:00:00Z' },
        },
      },
    ]
    window.fetch = async (input, init) => {
      const res = await orig(input, init)
      const url = typeof input === 'string' ? input : input.url
      if (!url.includes('/api/system/audit-logs?') || url.includes('cursor=')) return res
      const body = await res.json()
      body.items = [...fake, ...body.items]
      return new Response(JSON.stringify(body), { status: res.status, headers: { 'Content-Type': 'application/json' } })
    }
  })
}

const ENCODING_DIR = new URL('../../../shared/imagehdr/testdata/encoding/', import.meta.url)

export default {
  out: '../docs/screenshots/m7',
  accounts: {
    [admin]: adminPassword,
  },
  shots: [
    // 不加条件：第一页 50 条，按时间倒序；"加载更多"后 100 条、接在后面。
    {
      name: 'admin-audit-list',
      user: admin,
      path: '/system/audit',
      waitFor: ROWS,
      async prepare(page) {
        await context(page)
        await waitList(page)
        const first = await readRows(page)
        if (first.length !== 50) throw new Error(`第一页应有 50 条，实际 ${first.length}`)
        assertDescending(first, '第一页')
        if (first.some((r) => !r.time || !r.operator || !r.actionText)) throw new Error('有行缺时间、操作人或操作')
        await page.click('[data-testid=audit-more]')
        await page.waitForFunction((sel) => document.querySelectorAll(sel).length > 50, { timeout: 15000 }, ROWS)
        const all = await readRows(page)
        if (all.length !== 100) throw new Error(`加载更多后应有 100 条，实际 ${all.length}`)
        assertDescending(all, '加载更多')
        if (new Set(all.map((r) => r.id)).size !== all.length) throw new Error('加载更多后有重复的行')
        console.log(`  断言：第一页 50 条、加载更多后 100 条，id ${all[0].id} … ${all[99].id} 倒序无重复`)
        await page.evaluate(() => document.querySelector('section')?.scrollTo(0, 0))
      },
    },

    // 地址栏带条件（项目 + 对象类型）：刚才建、改、删的文件夹在最上面，操作译成中文；展开"修改"看之前、之后。
    {
      name: 'admin-audit-filtered-expand',
      user: admin,
      path: '/system/audit',
      waitFor: ROWS,
      async prepare(page) {
        const { origin, project, folder, name } = await context(page)
        await page.goto(`${origin}/system/audit?project=${project.id}&type=folder`, { waitUntil: 'networkidle0' })
        await waitList(page)
        const rows = await readRows(page)
        if (rows.length === 0 || rows.some((r) => r.type !== 'folder' || !r.project.includes(PROJECT))) {
          throw new Error(`按项目和文件夹筛选的结果不对：${JSON.stringify(rows.slice(0, 3))}`)
        }
        // 刚才那个文件夹的三条（别的任务同时往 DEMO-A 里建文件夹也不影响）：删除、修改、新建，按时间倒序。
        const top = rows.filter((r) => r.target === folder.id)
        const want = [['delete', '删除'], ['update', '修改'], ['create', '新建']]
        if (top.length !== 3) throw new Error(`${name} 应有 3 条审计，实际 ${top.length}`)
        top.forEach((r, i) => {
          if (r.action !== want[i][0] || r.actionText !== want[i][1] || !r.object.includes(name)) {
            throw new Error(`第 ${i + 1} 条应是「${want[i][1]}」${name}：${JSON.stringify(r)}`)
          }
        })
        if (!top[1].summary.includes('备注')) throw new Error(`修改那条的说明应提到备注：${top[1].summary}`)
        if (top[0].summary !== '—') throw new Error(`删空文件夹的说明应为空：${top[0].summary}`)
        const select = await page.$eval('[data-testid=audit-type]', (el) => el.value)
        if (select !== 'folder') throw new Error(`对象下拉框应是 folder，实际 ${select}`)
        await page.click(`${ROWS}[data-audit-id="${top[1].id}"]`)
        await page.waitForSelector(`[data-testid=audit-detail][data-audit-id="${top[1].id}"]`)
        const detail = await page.$eval(`[data-testid=audit-detail][data-audit-id="${top[1].id}"]`, (el) => el.textContent ?? '')
        for (const s of ['之前', '之后', '备注', '（空）', '审计页截图：改了备注', folder.id]) {
          if (!detail.includes(s)) throw new Error(`展开的详情里没有「${s}」：${detail.slice(0, 200)}`)
        }
        console.log(`  断言：${name} 的删除、修改、新建都在、按时间倒序；展开修改看到备注（空）→ 审计页截图：改了备注`)
      },
    },

    // 用筛选栏选条件：今天、admin、文件夹、删除；地址栏同步。
    {
      name: 'admin-audit-filter-bar',
      user: admin,
      path: '/system/audit',
      waitFor: ROWS,
      async prepare(page) {
        const { adminId, name } = await context(page)
        await page.waitForFunction(
          (id) => [...document.querySelectorAll('[data-testid=audit-operator] option')].some((o) => o.value === id),
          { timeout: 15000 },
          adminId,
        )
        const day = today()
        for (const sel of ['[data-testid=audit-from]', '[data-testid=audit-to]']) {
          await page.$eval(
            sel,
            (el, v) => {
              el.value = v
              el.dispatchEvent(new Event('input', { bubbles: true }))
            },
            day,
          )
        }
        await page.select('[data-testid=audit-operator]', adminId)
        await page.select('[data-testid=audit-type]', 'folder')
        await page.select('[data-testid=audit-action]', 'delete')
        await page.waitForFunction(() => location.search.includes('action=delete'), { timeout: 5000 })
        await waitList(page)
        const url = new URL(page.url())
        for (const [k, v] of [['from', day], ['to', day], ['operator', adminId], ['type', 'folder'], ['action', 'delete']]) {
          if (url.searchParams.get(k) !== v) throw new Error(`地址栏的 ${k} 应是 ${v}：${url.search}`)
        }
        const rows = await readRows(page)
        if (rows.length === 0 || rows.some((r) => r.type !== 'folder' || r.action !== 'delete' || r.operator !== '系统管理员')) {
          throw new Error(`筛选结果不对：${JSON.stringify(rows.slice(0, 3))}`)
        }
        if (!rows.some((r) => r.object.includes(name))) {
          throw new Error(`结果里没有今天删的 ${name}：${JSON.stringify(rows.slice(0, 3))}`)
        }
        if (!rows.every((r) => r.time.startsWith(day))) throw new Error('有不是今天的记录')
        console.log(`  断言：筛选栏选条件后地址栏同步，${rows.length} 条都是今天 admin 删除文件夹`)
      },
    },

    // 不认识的类型、动作原样显示；存储迁移、许可证（M7-2、M7-1 的审计）译成中文。
    {
      name: 'admin-audit-unknown',
      user: admin,
      path: '/system/audit',
      waitFor: ROWS,
      async prepare(page) {
        await injectFakeItems(page)
        // 改一下条件再改回来，让列表重新取（经过包了一层的 fetch）。
        await page.select('[data-testid=audit-type]', 'folder')
        await page.waitForFunction(() => location.search.includes('type=folder'))
        await page.select('[data-testid=audit-type]', '')
        await page.waitForFunction((sel) => document.querySelector(`${sel}[data-audit-id="900000003"]`), { timeout: 15000 }, ROWS)
        await waitList(page)
        const byId = new Map((await readRows(page)).map((r) => [r.id, r]))
        const [mystery, migrate, start, end, license] = [900000003, 900000002, 900000005, 900000004, 900000001].map((id) => {
          const r = byId.get(id)
          if (!r) throw new Error(`列表里没有假的第 ${id} 条`)
          return r
        })
        if (!mystery.object.startsWith('mystery_type') || mystery.actionText !== 'mystery_action' || mystery.operator !== '系统') {
          throw new Error(`不认识的应原样显示：${JSON.stringify(mystery)}`)
        }
        if (!migrate.object.startsWith('文件快照') || !migrate.object.includes('IMG_0001.png v1') || migrate.actionText !== '存储迁移' ||
          migrate.summary !== '「旧存储」→「新存储」') {
          throw new Error(`存储迁移：${JSON.stringify(migrate)}`)
        }
        if (!start.object.startsWith('存储配置') || !start.object.includes('旧存储') || start.actionText !== '开始迁移' ||
          !start.summary.includes('迁到「新存储」，共 25 个快照')) {
          throw new Error(`开始迁移：${JSON.stringify(start)}`)
        }
        if (!end.object.includes('旧存储') || end.actionText !== '迁移结束' || end.operator !== '系统' ||
          !end.summary.includes('已完成：迁到「新存储」24 个快照') || !end.summary.includes('核对不一致 1 个') ||
          !end.summary.includes('源存储里还剩 1 个')) {
          throw new Error(`迁移结束：${JSON.stringify(end)}`)
        }
        if (!license.object.includes('许可证') || !license.object.includes('演示公司') || license.actionText !== '替换许可证' ||
          !license.summary.includes('标准版') || !license.summary.includes('20 个会话') || !license.summary.includes('到期 2027-10-16')) {
          throw new Error(`许可证：${JSON.stringify(license)}`)
        }
        await page.click(`${ROWS}[data-audit-id="900000001"]`)
        await page.click(`${ROWS}[data-audit-id="900000003"]`)
        const detail = await page.$eval('[data-testid=audit-detail][data-audit-id="900000003"]', (el) => el.textContent ?? '')
        if (!detail.includes('foo') || !detail.includes('bar') || !detail.includes('level')) {
          throw new Error(`不认识的字段应原样显示：${detail}`)
        }
        console.log('  断言：mystery_type / mystery_action 原样显示；文件快照 / 存储迁移、存储配置 / 开始迁移、迁移结束，许可证 / 替换许可证译成中文')
      },
    },

    // 浏览器上传导出不了的罕见编码：服务器 400 invalid_image，上传面板里显示原因。
    {
      name: 'files-upload-rare-format',
      user: admin,
      path: '/system/audit',
      waitUntil: 'networkidle2',
      async prepare(page) {
        const { origin, project } = await context(page)
        tempFolder = await api(origin, 'POST', '/api/folders', {
          parent_id: project.root_folder_id,
          folder_name: `截图验证 M7-3 罕见格式 ${stamp()}`,
          remark: 'M7-3 截图脚本临时建的，做完会删掉',
        })
        await page.goto(`${origin}/files?folder=${tempFolder.id}`, { waitUntil: 'networkidle2' })
        await page.waitForSelector('[data-testid=files-page][data-list=ready]', { timeout: 15000 })
        const files = [
          { name: 'IMG_算术编码.jpg', data: await readFile(new URL('reject-jpeg-arith.jpg', ENCODING_DIR)) },
          { name: 'IMG_JPEG2000.tif', data: await readFile(new URL('reject-tiff-jpeg2000.tif', ENCODING_DIR)) },
        ]
        await injectFiles(page, 'files', files)
        const items = await waitUploadDone(page)
        const want = {
          'IMG_算术编码.jpg': '图片不合规：不支持的图片格式（算术编码的 JPEG），请换个保存格式重扫',
          'IMG_JPEG2000.tif': '图片不合规：不支持的图片格式（JPEG 2000 压缩的 TIFF），请换个保存格式重扫',
        }
        for (const [name, text] of Object.entries(want)) {
          const it = items.find((i) => i.name === name)
          if (!it || it.state !== 'failed' || it.errorCode !== 'invalid_image' || !it.text.includes(text)) {
            throw new Error(`${name} 应该被拒绝并显示「${text}」：${JSON.stringify(it)}`)
          }
        }
        const children = await api(origin, 'GET', `/api/folders/${tempFolder.id}/children`)
        if (children.files.length !== 0) throw new Error(`被拒绝的文件不应入库：${children.files.length} 个`)
        console.log('  断言：算术编码的 JPEG、JPEG 2000 压缩的 TIFF 上传被拒，面板里显示原因，没有入库')
      },
      async after(page) {
        if (!tempFolder) return
        const { origin } = await context(page)
        await api(origin, 'DELETE', `/api/folders/${tempFolder.id}`)
        tempFolder = null
      },
    },
  ],
}
