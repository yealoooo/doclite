// M1-14 验收（Mac）：浏览器上传、目录整理、项目权限。
//
//   npm run screenshots -- --spec scripts/shots/m1-accept.mjs --base http://127.0.0.1:<web 端口>
//
// --base 是 web 的入口（vite preview 或 dev，/api 代理到 server；M7-5 起 server 不托管界面）。
// 账号来自 npm run seed：demo_scanner（个人授权 DEMO-A）、demo_reviewer（审核员角色授权 DEMO-B；M2-5 起另有 DEMO-A 的个人
// 授权）、demo_noproject（归类员角色、没有授权；M3-5 起 demo_cataloger 授权进了 DEMO-A，改用它）。accept-perm-reviewer
// 要用 admin 读各项目的授权（INIT_ADMIN_PASSWORD，默认 change-me-now）。
//
// - accept-upload：在 DEMO-A 根下建临时文件夹"M1-14 浏览器上传和整理 <时间>"，用"上传文件夹"（注入合成的
//   File 列表，和选择框同一段代码）传 scripts/samples 里的样本：JPEG（彩色、灰度）、PNG（带 alpha、1 位）、
//   TIFF（无压缩、LZW、CCITT G4），各 3 页（同一个样本加不同的注释，字节不同），分在 JPEG、PNG、TIFF 三个子文件夹里。断言全部入库、都带上了封面，
//   batch 的封面是 JPEG、长边 ≤ 400、方向和原图一致。
// - accept-organize：把 PNG 移到 JPEG 下面，删除 TIFF 里的一个文件，再删除 JPEG（连同 PNG）。断言：路径编码换了
//   前缀；目录树和计数正确；batch 把删掉的文件都放进 missing；被删文件的日志接口返回 404，剩下的文件日志只有 upload。
//   最后一行 `ACCEPT_IDS {...}` 给外面的 SQL 核对（file_logs、audit_logs、path_code）。after 里软删除临时文件夹。
// - accept-demo-folder / accept-demo-box：热文件夹演示的文件夹（默认 DEMO-A 根下最新的"M1 验收演示 …"，可用
//   ACCEPT_DEMO 指定名字）有 5 个盒，每盒 100 页；打开第 3 个盒看封面。没有热文件夹演示的数据时可以用 seed:files 造
//   （M5-3：npm run seed:files -- --base … --user demo_scanner:demo123456 --project DEMO-A --folder "M1 验收演示 <日期>/盒 01"
//   --count 100，盒 01–05 各一次）。
// - accept-perm-scanner / -reviewer / -cataloger：三个人看到的项目不同（切换器截图）；直接调别人项目的接口返回 403，
//   审核员能进的项目正好是授权给他的（个人授权或他的角色被授权，按 admin 读到的授权算；M3-8 起不再写死"只有 DEMO-B"），
//   demo_noproject 用 batch 取 DEMO-A 的文件全部落在 missing（batch 登录即可，只返回能进的项目里的文件）。截图名
//   accept-perm-cataloger 不变。
//
// 样本原来每次现场调上一版的 gen-samples（Rust）生成；M5-3 起读提交的样本（M7-5 起是 web 自己的 scripts/samples，见
// scripts/lib/upload-samples.mjs），不再依赖 Rust 工具链。

import { apiLogin } from '../lib/api.mjs'
import { checkCovers, injectFiles, sampleVariant, waitUploadDone } from '../lib/upload-samples.mjs'

const demo = 'demo123456'
const adminUser = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const IDLE = 'networkidle2'

/** 跨截图共用的状态（截图按清单顺序执行）。 */
const state = {}

function assert(cond, msg) {
  if (!cond) throw new Error(msg)
}

function stamp() {
  return new Date().toISOString().replace(/[-:T]/g, '').slice(0, 14)
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

const EXT = { jpeg: 'jpg', png: 'png', tiff: 'tif' }

/** 每种样本 3 页（文件名同 gen-samples --pages 3：`<样本>-p<n>.<扩展名>`），返回 Map<文件名, Buffer>。 */
async function samples3() {
  const map = new Map()
  for (const stem of Object.values(KINDS).flat()) {
    const ext = EXT[stem.split('-')[0]]
    for (let n = 1; n <= 3; n++) map.set(`${stem}-p${n}.${ext}`, await sampleVariant(`${stem}.${ext}`, n))
  }
  return map
}

const KINDS = {
  JPEG: ['jpeg-rgb', 'jpeg-gray'],
  PNG: ['png-alpha', 'png-1bit'],
  TIFF: ['tiff-none-gray8', 'tiff-lzw-rgb', 'tiff-g4-1bit'],
}

export default {
  out: '../docs/screenshots/m1',
  accounts: { demo_scanner: demo, demo_reviewer: demo, demo_noproject: demo },
  waitUntil: IDLE,
  shots: [
    {
      name: 'accept-upload',
      user: 'demo_scanner',
      path: '/files',
      waitFor: '[data-testid=files-page]',
      delay: 600,
      async prepare(page) {
        const a = await project(page, 'DEMO-A')
        assert(a, 'demo_scanner 进不了 DEMO-A')
        state.a = a
        const temp = (
          await call(page, 'POST', '/api/folders', {
            parent_id: a.root_folder_id,
            folder_name: `M1-14 浏览器上传和整理 ${stamp()}`,
            remark: 'M1-14 验收脚本建的临时文件夹，做完软删除',
          })
        ).data
        state.temp = temp
        const samples = await samples3()
        const top = '浏览器样本'
        const files = []
        for (const [sub, stems] of Object.entries(KINDS)) {
          for (const stem of stems) {
            for (const name of [...samples.keys()].filter((n) => n.startsWith(`${stem}-p`)).sort()) {
              files.push({ path: `${top}/${sub}/${name}`, name, data: samples.get(name) })
            }
          }
        }
        assert(files.length === 21, `样本数不对：${files.length}（应为 7 种 × 3 页）`)
        await openFolder(page, temp.id)
        await injectFiles(page, 'folder', files)
        const items = await waitUploadDone(page, 300000)
        for (const it of items) assert(it.state === 'done' && it.fileId, `${it.path} 没有入库：${it.state} ${it.text}`)
        for (const it of items) assert(it.hasCover === true, `${it.path} 没带上封面：${it.text}`)
        const ms = (stem) =>
          items
            .filter((i) => i.name.startsWith(stem))
            .map((i) => i.coverMs)
            .join('/')
        console.log(
          `  上传 ${items.length} 个文件，全部入库、带封面；浏览器生成封面耗时（ms）：` +
            Object.values(KINDS)
              .flat()
              .map((s) => `${s} ${ms(s)}`)
              .join('，'),
        )
        const api = async (method, url, body) => (await call(page, method, url, body)).data
        const covers = await checkCovers(
          api,
          items.map((i) => i.fileId),
        )
        console.log(
          `  batch 封面：${covers.length} 张都是 JPEG，尺寸 ${[...new Set(covers.map((c) => `${c.width}×${c.height}`))].join('、')}，` +
            `${Math.min(...covers.map((c) => c.bytes))}–${Math.max(...covers.map((c) => c.bytes))} 字节`,
        )
        state.items = items
        state.top = await childByName(page, temp.id, top)
        // 截图：停在临时文件夹，右下角的上传结果列表展开着。
      },
    },
    {
      name: 'accept-organize',
      user: 'demo_scanner',
      path: '/files',
      waitFor: '[data-testid=files-page]',
      delay: 600,
      async prepare(page) {
        assert(state.top, '上一张（accept-upload）没成功')
        const sub = {}
        for (const n of Object.keys(KINDS)) sub[n] = await childByName(page, state.top.id, n)
        const tiffFiles = (await get(page, `/api/folders/${sub.TIFF.id}/children`)).files
        assert(tiffFiles.length === 9, `TIFF 里应有 9 个文件：${tiffFiles.length}`)
        // 1. 移动子文件夹：PNG → JPEG 下面。
        const moved = (await call(page, 'PATCH', `/api/folders/${sub.PNG.id}`, { parent_id: sub.JPEG.id })).data
        assert(moved.path_code.startsWith(sub.JPEG.path_code) && moved.path_code !== sub.PNG.path_code, `移动后路径编码不对：${moved.path_code}`)
        console.log(`  1. 移动文件夹 PNG：${sub.PNG.path_code} → ${moved.path_code}（JPEG 是 ${sub.JPEG.path_code}）`)
        // 2. 软删除一个文件（TIFF 里第一个）。
        const victim = tiffFiles[0]
        assert((await call(page, 'DELETE', `/api/folder-files/${victim.folder_file_id}`)).status === 204, '删除文件应返回 204')
        console.log(`  2. 删除文件 TIFF/${victim.file_name}`)
        // 3. 软删除一个子文件夹（JPEG，连同移进去的 PNG）。
        assert((await call(page, 'DELETE', `/api/folders/${sub.JPEG.id}`)).status === 204, '删除文件夹应返回 204')
        console.log('  3. 删除文件夹 JPEG（连同里面的 PNG）')
        // 目录树和计数。
        const top = await get(page, `/api/folders/${state.top.id}/children`)
        assert(top.folders.length === 1 && top.folders[0].folder_name === 'TIFF', `剩下的子文件夹不对：${top.folders.map((f) => f.folder_name)}`)
        assert(top.folders[0].file_count === 8 && top.folders[0].folder_count === 0, `TIFF 的计数不对：${JSON.stringify(top.folders[0])}`)
        const tiffNow = (await get(page, `/api/folders/${sub.TIFF.id}/children`)).files
        assert(tiffNow.length === 8 && !tiffNow.some((f) => f.file_id === victim.file_id), 'TIFF 里应剩 8 个文件')
        for (const id of [sub.JPEG.id, sub.PNG.id]) assert((await call(page, 'GET', `/api/folders/${id}`, undefined, [404])).status === 404, '删掉的文件夹应返回 404')
        // batch 的 missing。
        const all = state.items.map((i) => i.fileId)
        const batch = (await call(page, 'POST', '/api/files/batch', { file_ids: all })).data
        const deleted = state.items.filter((i) => !i.path.includes('/TIFF/')).map((i) => i.fileId).concat(victim.file_id)
        assert(batch.missing.length === 13 && deleted.every((id) => batch.missing.includes(id)), `missing 不对：${batch.missing.length}`)
        assert(batch.items.length === 8, `batch 的 items 应为 8：${batch.items.length}`)
        console.log(`  batch：21 个 id → items ${batch.items.length}、missing ${batch.missing.length}（删掉的 12 个 JPEG/PNG + 1 个 TIFF）`)
        // 文件日志：删掉的文件 404，剩下的只有 upload。
        assert((await call(page, 'GET', `/api/files/${victim.file_id}/logs`, undefined, [404])).status === 404, '删掉的文件日志接口应 404')
        const logs = (await get(page, `/api/files/${tiffNow[0].file_id}/logs`)).logs
        assert(logs.length === 1 && logs[0].action === 'upload', `剩下的文件日志应只有 upload：${logs.map((l) => l.action)}`)
        console.log(
          'ACCEPT_IDS ' +
            JSON.stringify({
              temp: state.temp.id,
              top: state.top.id,
              jpeg: sub.JPEG.id,
              png: sub.PNG.id,
              tiff: sub.TIFF.id,
              jpeg_path_before: sub.JPEG.path_code,
              png_path_before: sub.PNG.path_code,
              png_path_after: moved.path_code,
              deleted_file: victim.file_id,
            }),
        )
        await openFolder(page, state.top.id)
        await page.click('[data-testid=view-list]')
      },
      async after(page) {
        if (!state.temp) return
        const r = await call(page, 'DELETE', `/api/folders/${state.temp.id}`, undefined, [404])
        console.log(`  已软删除临时文件夹「${state.temp.folder_name}」（${r.status}）`)
      },
    },
    {
      name: 'accept-demo-folder',
      user: 'demo_scanner',
      path: '/files',
      waitFor: '[data-testid=files-page]',
      delay: 500,
      async prepare(page) {
        state.a ??= await project(page, 'DEMO-A')
        const root = await get(page, `/api/folders/${state.a.root_folder_id}/children`)
        const wanted = process.env.ACCEPT_DEMO
        const demoFolder = wanted
          ? root.folders.find((f) => f.folder_name === wanted)
          : root.folders.filter((f) => f.folder_name.startsWith('M1 验收演示')).at(-1)
        assert(demoFolder, '找不到热文件夹演示的文件夹（M1 验收演示 …）')
        const ch = await get(page, `/api/folders/${demoFolder.id}/children`)
        const boxes = ch.folders.map((f) => `${f.folder_name}:${f.file_count}`)
        assert(ch.folders.length === 5 && ch.folders.every((f) => f.file_count === 100), `应有 5 个盒、每盒 100 页：${boxes}`)
        console.log(`  ${demoFolder.folder_name}：${boxes.join('，')}`)
        state.demoFolder = demoFolder
        state.box3 = ch.folders[2]
        state.demoBox = (await get(page, `/api/folders/${state.box3.id}/children`)).files.map((f) => f.file_id)
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
        assert(state.box3, '上一张（accept-demo-folder）没成功')
        await openFolder(page, state.box3.id)
        await page.click('[data-testid=view-icons]')
        await waitCovers(page)
        const s = await page.$$eval('[data-testid=file-cover]', (els) => els.map((e) => e.dataset.coverState))
        assert(s.length > 0 && s.every((x) => x === 'loaded'), `封面没有全部显示：${[...new Set(s)]}`)
        console.log(`  ${state.box3.folder_name}：可见的 ${s.length} 张封面都显示了（浏览器里走预签名 URL）`)
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
        await openSwitcher(page)
        console.log(`  demo_scanner（个人授权）：${codes}`)
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
        assert(want.includes('DEMO-B'), 'demo_reviewer 应经审核员角色能进 DEMO-B')
        assert(outside, '应至少有一个没授权给 demo_reviewer 的项目（npm run seed 之外的，例如 M37-SET）')
        const r = await call(page, 'GET', `/api/folders/${outside.root_folder_id}/children`, undefined, [403])
        assert(r.status === 403, `demo_reviewer 读没授权的 ${outside.project_code} 的根文件夹应 403：${r.status}`)
        await openSwitcher(page)
        console.log(`  demo_reviewer：${how.join('、')}；读没授权的 ${outside.project_code} 的根文件夹 → ${r.status} ${r.data.code}`)
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
