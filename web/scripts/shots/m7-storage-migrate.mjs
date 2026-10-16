// M7-2 的截图清单和断言：系统 → 存储配置里的存储迁移（"迁移到…"、进度、取消、完成后停用源存储、失败清单）。
//
//   INIT_ADMIN_PASSWORD=admin123456 \
//     npm run screenshots -- --spec scripts/shots/m7-storage-migrate.mjs --base http://127.0.0.1:<web 端口>
//
// 前提：
// - --base 是 web 的入口（vite preview 或 dev），它代理到的 server **在执行后台任务**（拿到了执行器锁；同一个库上
//   只有一个 server 执行，见 README"后台任务"）——迁移在它里面执行，拿不到锁时迁移一直排队，脚本在"迁移中"那步失败；
// - 本机 SeaweedFS：S3 地址 M72_S3（默认 http://127.0.0.1:8333），密钥 M72_S3_ACCESS_KEY / M72_S3_SECRET_KEY（默认同
//   weed mini 的 doclite-dev / doclite-dev-secret）；要有第二个桶 doclite-migrate-test
//   （`echo 's3.bucket.create -name doclite-migrate-test' | weed shell -master=127.0.0.1:9333`）。
//
// 演示数据（常驻，每跑一次加一批）：存储「M7-2 迁移演示·源」（桶 doclite，前缀 m7-2-demo/src/）和「M7-2 迁移演示·目标」
// （桶 doclite-migrate-test，前缀 m7-2-demo/dst/）；DEMO-A 的文件夹「M7-2 迁移演示/<时间>」。"源"的 server 地址指向脚本
// 起的慢速代理（转发到 SeaweedFS，读对象每次慢 M72_DELAY_MS，默认 400 毫秒），让迁移持续几秒、看得到进度；工作站地址
// 直连 SeaweedFS。准备时把"源"临时设为默认、往文件夹里上传 24 页（带封面）、复制第 1 页（和它共用对象），再把原来的
// 默认存储设回去（出错时也设回去）。跑完"源"已迁空并停用（下次跑时重新启用、换成新的代理地址），"目标"里留下这次的
// 25 个快照；迁移不删对象，"源"前缀下的对象也留着。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - m7-migrate-entry：源存储卡片上"迁移到…"可以点；默认存储（有文件）的"迁移到…"禁用，原因提到默认存储。
// - m7-migrate-dialog：对话框列出能迁入的存储（目标、默认存储都在，源自己不在），默认选中默认存储；点"目标"后选中它、
//   提示它不是默认存储。
// - m7-migrate-cancel（另存 m7-migrate-progress.png）：开始迁移 → 源卡片的进度条"迁移中"、已迁 > 0 且 < 总数（总数 25），
//   和接口一致；目标卡片显示"正在从…迁入"；源存储的"设为默认"、目标存储的"停用""删除"按钮禁用并说明正在迁移，目标存储
//   的"迁移到…"禁用（整个系统同时只有一个迁移）→
//   截进度 → 取消迁移（确认框）→ 面板变成"已取消"，接口 canceled、已迁 < 25、源存储里还有文件；取消之后快照数不再变。
// - m7-migrate-done（另存 m7-migrate-detail.png）：再迁一次 → 面板"完成"、"已经没有文件了"；接口 done、剩 0、没有失败，
//   两次合起来 25 个快照都在目标存储（每个文件的快照列表里存储是"目标"，原图地址能取到）；"详情"对话框剩 0、没有失败；
//   面板提示先改只读、导出物清理后再停用；源存储点"改为只读"、再点"停用"→ 状态停用。
// - m7-migrate-failures（**模拟的响应**）：把列表和详情接口的响应改写成"完成但有 23 个快照核对不一致"（2 个对象：sha256
//   不一致的共用对象 2 个快照、源对象缺失的对象 21 个快照只列 1 个）→ 面板"完成（有失败）"、"查看失败清单（23）"；
//   对话框两行，原因、key、快照的位置对，第二行"等 21 个快照"。

import http from 'node:http'
import path from 'node:path'
import { mkdirSync } from 'node:fs'

import { apiLogin, stamp, uploadPage } from '../lib/m3-demo.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const S3 = process.env.M72_S3 ?? 'http://127.0.0.1:8333'
const S3_ACCESS_KEY = process.env.M72_S3_ACCESS_KEY ?? 'doclite-dev'
const S3_SECRET_KEY = process.env.M72_S3_SECRET_KEY ?? 'doclite-dev-secret'
const DELAY_MS = Number(process.env.M72_DELAY_MS ?? 400)
const SRC_NAME = 'M7-2 迁移演示·源'
const DST_NAME = 'M7-2 迁移演示·目标'
const PAGES = 24
// 另存的截图和清单的截图放在同一个目录：--out，默认 docs/screenshots/m7。
const OUT = (() => {
  const i = process.argv.indexOf('--out')
  return i > 0 ? process.argv[i + 1] : '../docs/screenshots/m7'
})()
const BASE = '/api/system/storage-profiles'

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

function assert(cond, message) {
  if (!cond) throw new Error(`断言失败：${message}`)
}

const card = (name) => `[data-testid=storage-card][data-profile-name="${name}"]`

/** 转发到 SeaweedFS 的代理：读对象（GET）每次先等 delay 毫秒。不改 Host 头，v4 签名照样对。 */
function startSlowProxy(target, delay) {
  const t = new URL(target)
  const state = { delay, gets: 0 }
  const server = http.createServer((req, res) => {
    const forward = () => {
      const up = http.request(
        { host: t.hostname, port: t.port, method: req.method, path: req.url, headers: req.headers },
        (ur) => {
          res.writeHead(ur.statusCode ?? 502, ur.headers)
          ur.pipe(res)
        },
      )
      up.on('error', () => res.destroy())
      req.pipe(up)
    }
    if (req.method === 'GET' && state.delay > 0) {
      state.gets++
      setTimeout(forward, state.delay)
    } else forward()
  })
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => {
      server.unref()
      resolve({ url: `http://127.0.0.1:${server.address().port}`, state })
    })
  })
}

async function waitFor(what, fn, timeout = 30000, every = 250) {
  const t0 = Date.now()
  for (;;) {
    const v = await fn()
    if (v) return v
    if (Date.now() - t0 > timeout) throw new Error(`${timeout} ms 内没有等到：${what}`)
    await sleep(every)
  }
}

const active = (m) => !!m && (m.status === 'queued' || m.status === 'running')

let ctxPromise = null
function context(page) {
  ctxPromise ??= (async () => {
    const origin = new URL(page.url()).origin
    const api = await apiLogin(origin, admin, adminPassword)
    const proxy = await startSlowProxy(S3, DELAY_MS)
    mkdirSync(OUT, { recursive: true })
    const conn = { access_key: S3_ACCESS_KEY, secret_key: S3_SECRET_KEY, force_path_style: true }

    let list = await api('GET', BASE)
    const def = list.find((p) => p.is_default)
    assert(def, '要有默认存储')
    let src = list.find((p) => p.profile_name === SRC_NAME)
    if (!src) {
      src = await api('POST', BASE, {
        ...conn, profile_name: SRC_NAME, endpoint: proxy.url, public_endpoint: S3, bucket: 'doclite',
        path_prefix: 'm7-2-demo/src/', remark: 'M7-2 截图脚本的迁移源（server 地址是脚本起的慢速代理，跑完迁空并停用）',
      })
    } else {
      if (active(src.migration)) {
        await api('POST', `${BASE}/${src.id}/migrate/cancel`, undefined, { allow: [409] })
        await waitFor('上次没结束的迁移停下', async () => !active((await api('GET', `${BASE}/${src.id}`)).migration))
      }
      src = await api('PATCH', `${BASE}/${src.id}`, { endpoint: proxy.url, status: 'active' })
    }
    let dst = list.find((p) => p.profile_name === DST_NAME)
    if (!dst) {
      dst = await api('POST', BASE, {
        ...conn, profile_name: DST_NAME, endpoint: S3, bucket: 'doclite-migrate-test', path_prefix: 'm7-2-demo/dst/',
        remark: 'M7-2 截图脚本的迁移目标（第二个桶）',
      })
    } else if (dst.status !== 'active') {
      dst = await api('PATCH', `${BASE}/${dst.id}`, { status: 'active' })
    }

    // 往"源"里放 24 页 + 1 个复制：临时把它设为默认，传完设回去。
    const project = (await api('GET', '/api/projects')).find((p) => p.project_code === 'DEMO-A')
    assert(project, '要有演示项目 DEMO-A（npm run seed:demo）')
    const top = await api('POST', '/api/folders', {
      parent_id: project.root_folder_id, folder_name: 'M7-2 迁移演示', if_exists: 'return',
      remark: 'M7-2 截图脚本：每次跑一个子文件夹，文件先传到"M7-2 迁移演示·源"再迁到"·目标"',
    })
    const run = stamp()
    const folder = await api('POST', '/api/folders', { parent_id: top.id, folder_name: run, if_exists: 'return' })
    const files = []
    await api('POST', `${BASE}/${src.id}/set-default`)
    try {
      for (let i = 1; i <= PAGES; i++) {
        files.push(await uploadPage(api, folder.id, `IMG_${String(i).padStart(4, '0')}.png`, `m7-2-${run}-${i}`, i))
      }
      files.push(await api('POST', `/api/folder-files/${files[0].folder_file_id}/copy`, {}))
    } finally {
      await api('POST', `${BASE}/${def.id}/set-default`)
    }
    list = await api('GET', BASE)
    assert(list.find((p) => p.id === def.id)?.is_default, '默认存储要设回去')
    return { origin, api, proxy, src, dst, def, folder, fileIds: files.map((f) => f.file_id), state: {} }
  })()
  return ctxPromise
}

async function shot(page, name) {
  await sleep(300)
  await page.screenshot({ path: path.join(OUT, `${name}.png`) })
  console.log(`  另存 ${name}.png`)
}

async function waitToast(page, text, timeout = 10000) {
  await page.waitForFunction(
    (t) => [...document.querySelectorAll('[data-sonner-toast]')].some((el) => el.textContent?.includes(t)),
    { timeout },
    text,
  )
}

/** 卡片里按钮的禁用原因（GuardedButton 外面的 span[data-blocked]）；没禁用时 null。 */
async function blockedReason(page, cardName, testid) {
  return page.$eval(card(cardName), (el, id) => {
    const btn = el.querySelector(`[data-testid=${id}]`)
    if (!btn) return 'missing'
    return btn.closest('[data-blocked]')?.getAttribute('data-blocked') ?? null
  }, testid)
}

async function buttonBlocked(page, cardName, text) {
  return page.$eval(card(cardName), (el, t) => {
    const btn = [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === t)
    if (!btn) return 'missing'
    return btn.closest('[data-blocked]')?.getAttribute('data-blocked') ?? null
  }, text)
}

async function panel(page, cardName) {
  return page.$eval(`${card(cardName)} [data-testid=storage-migration]`, (el) => ({
    status: el.getAttribute('data-status'),
    cancelRequested: el.getAttribute('data-cancel-requested') === 'true',
    migrated: Number(el.getAttribute('data-migrated')),
    total: Number(el.getAttribute('data-total')),
    failed: Number(el.getAttribute('data-failed')),
    target: el.getAttribute('data-target'),
    text: el.textContent ?? '',
  })).catch(() => null)
}

/** 打开源存储的"迁移到…"，选目标，开始。 */
async function startFromDialog(page, c) {
  await page.click(`${card(SRC_NAME)} [data-testid=storage-migrate]`)
  await page.waitForSelector('[data-testid=migrate-dialog]')
  await page.click(`[data-testid=migrate-target][data-profile-name="${DST_NAME}"]`)
  await page.waitForSelector(`[data-testid=migrate-target][data-profile-name="${DST_NAME}"][data-selected=true]`)
  await page.click('[data-testid=migrate-start]')
  await waitToast(page, `已开始把「${SRC_NAME}」里的文件迁到「${DST_NAME}」`)
  await page.waitForSelector('[data-testid=migrate-dialog]', { hidden: true })
  const m = await c.api('GET', `${BASE}/${c.src.id}/migrate`)
  assert(m.target_profile_id === c.dst.id && active(m), `接口里的迁移：${JSON.stringify(m)}`)
  return m
}

async function confirmDialog(page, text) {
  await page.waitForSelector('[role=alertdialog]')
  await page.evaluate((t) => {
    const btn = [...document.querySelectorAll('[role=alertdialog] button')].find((b) => b.textContent?.trim() === t)
    btn?.click()
  }, text)
}

export default {
  out: OUT,
  accounts: { [admin]: adminPassword },
  waitUntil: 'networkidle2',
  shots: [
    {
      name: 'm7-migrate-entry',
      user: admin,
      path: '/system/storage',
      waitFor: '[data-testid=storage-card]',
      async prepare(page) {
        // 默认存储按接口里的 is_default 找，不写死名字（开发库的默认存储改过名，M7-8）。
        const c = await context(page)
        await page.reload({ waitUntil: 'networkidle2' })
        await page.waitForSelector(card(SRC_NAME))
        const srcBlock = await blockedReason(page, SRC_NAME, 'storage-migrate')
        assert(srcBlock === null, `源存储的"迁移到…"应该能点：${srcBlock}`)
        const defBlock = await blockedReason(page, c.def.profile_name, 'storage-migrate')
        assert(typeof defBlock === 'string' && defBlock.includes('默认存储'), `默认存储的"迁移到…"应禁用：${defBlock}`)
        await page.$eval(card(SRC_NAME), (el) => el.scrollIntoView({ block: 'center' }))
      },
    },
    {
      name: 'm7-migrate-dialog',
      user: admin,
      path: '/system/storage',
      waitFor: card(SRC_NAME),
      async prepare(page) {
        const c = await context(page)
        await page.click(`${card(SRC_NAME)} [data-testid=storage-migrate]`)
        await page.waitForSelector('[data-testid=migrate-dialog]')
        const targets = await page.$$eval('[data-testid=migrate-target]', (els) =>
          els.map((e) => ({ name: e.getAttribute('data-profile-name'), selected: e.getAttribute('data-selected') === 'true' })),
        )
        const names = targets.map((t) => t.name)
        assert(names.includes(DST_NAME) && names.includes(c.def.profile_name) && !names.includes(SRC_NAME), `目标列表：${names}`)
        assert(targets.find((t) => t.selected)?.name === c.def.profile_name, `默认选中默认存储：${JSON.stringify(targets)}`)
        await page.click(`[data-testid=migrate-target][data-profile-name="${DST_NAME}"]`)
        await page.waitForSelector(`[data-testid=migrate-target][data-profile-name="${DST_NAME}"][data-selected=true]`)
        await page.waitForSelector('[data-testid=migrate-not-default]')
      },
      async after(page) {
        await page.keyboard.press('Escape')
      },
    },
    {
      name: 'm7-migrate-cancel',
      user: admin,
      path: '/system/storage',
      waitFor: card(SRC_NAME),
      async prepare(page) {
        const c = await context(page)
        const m = await startFromDialog(page, c)
        assert(m.total === PAGES + 1, `总数 ${m.total}`)
        // 迁移中：页面每 2 秒重取列表，断点约每秒一次。
        await page.waitForFunction(
          (sel) => {
            const el = document.querySelector(sel)
            const n = Number(el?.getAttribute('data-migrated'))
            return el?.getAttribute('data-status') === 'running' && n > 0 && n < Number(el.getAttribute('data-total'))
          },
          { timeout: 30000 },
          `${card(SRC_NAME)} [data-testid=storage-migration]`,
        ).catch(() => {
          throw new Error('迁移没有在执行（这个 server 拿到执行器锁了吗？）或者太快了（调大 M72_DELAY_MS）')
        })
        const ui = await panel(page, SRC_NAME)
        const api = await c.api('GET', `${BASE}/${c.src.id}/migrate`)
        assert(api.status === 'running' && api.migrated >= ui.migrated && ui.total === api.total, `界面 ${JSON.stringify(ui)}，接口 ${api.migrated}/${api.total}`)
        const incoming = await page.$eval(`${card(DST_NAME)} [data-testid=storage-incoming-migration]`, (el) => el.getAttribute('data-source'))
        assert(incoming === SRC_NAME, `目标卡片的迁入：${incoming}`)
        const sd = await blockedReason(page, SRC_NAME, 'storage-set-default')
        assert(sd?.includes('正在迁出'), `源存储的设为默认应禁用：${sd}`)
        for (const t of ['停用', '改为只读']) {
          const r = await buttonBlocked(page, DST_NAME, t)
          assert(r?.includes('正在往这个存储迁移'), `目标存储的"${t}"应禁用：${r}`)
        }
        const del = await blockedReason(page, DST_NAME, 'storage-delete')
        assert(del?.includes('迁移中'), `目标存储的删除应禁用：${del}`)
        const other = await blockedReason(page, DST_NAME, 'storage-migrate')
        assert(other === 'missing' || other?.includes('已有存储迁移在进行'), `目标存储的"迁移到…"应禁用：${other}`)
        await page.$eval(card(SRC_NAME), (el) => el.scrollIntoView({ block: 'center' }))
        await shot(page, 'm7-migrate-progress')

        await page.click(`${card(SRC_NAME)} [data-testid=migration-cancel]`)
        await confirmDialog(page, '取消迁移')
        await waitToast(page, `已取消「${SRC_NAME}」的迁移`)
        const fin = await waitFor('迁移取消完', async () => {
          const x = await c.api('GET', `${BASE}/${c.src.id}/migrate`)
          return x.status === 'canceled' ? x : null
        }, 15000)
        assert(fin.migrated > 0 && fin.migrated < PAGES + 1 && fin.remaining > 0, `取消后：${JSON.stringify(fin)}`)
        await sleep(1500)
        const again = await c.api('GET', `${BASE}/${c.src.id}/migrate`)
        assert(again.remaining === fin.remaining, `取消之后还在迁：${fin.remaining} → ${again.remaining}`)
        c.state.firstMigrated = fin.migrated
        await page.reload({ waitUntil: 'networkidle2' })
        await page.waitForSelector(`${card(SRC_NAME)} [data-testid=storage-migration][data-status=canceled]`)
        const block = await blockedReason(page, SRC_NAME, 'storage-migrate')
        assert(block === null, `取消后可以再迁：${block}`)
        await page.$eval(card(SRC_NAME), (el) => el.scrollIntoView({ block: 'center' }))
      },
    },
    {
      name: 'm7-migrate-done',
      user: admin,
      path: '/system/storage',
      waitFor: card(SRC_NAME),
      async prepare(page) {
        const c = await context(page)
        assert(c.state.firstMigrated, '要先跑 m7-migrate-cancel')
        const m = await startFromDialog(page, c)
        assert(m.total === PAGES + 1 - c.state.firstMigrated, `再迁的总数：${m.total}`)
        await page.waitForSelector(`${card(SRC_NAME)} [data-testid=storage-migration][data-status=done]`, { timeout: 60000 })
        await page.waitForSelector(`${card(SRC_NAME)} [data-testid=storage-migration-empty]`)
        const done = await c.api('GET', `${BASE}/${c.src.id}/migrate`)
        assert(done.failed === 0 && done.remaining === 0 && done.migrated === m.total, `完成：${JSON.stringify(done)}`)
        // 25 个快照都在目标存储，原图地址能取到。
        for (const fid of c.fileIds) {
          const s = await c.api('GET', `/api/files/${fid}/snapshots`)
          for (const x of s.snapshots) {
            assert(x.storage_profile_id === c.dst.id && x.storage_key.startsWith('m7-2-demo/dst/'), `${fid} 的快照：${x.storage_profile_name} ${x.storage_key}`)
          }
        }
        const batch = await c.api('POST', '/api/files/batch', { file_ids: c.fileIds })
        for (const it of batch.items) {
          const res = await fetch(it.original_url)
          assert(res.ok, `${it.file_id} 的原图取不到：${res.status}`)
          await res.arrayBuffer()
        }
        await page.click(`${card(SRC_NAME)} [data-testid=migration-details]`)
        await page.waitForSelector('[data-testid=migration-detail][data-state=ready]')
        const d = await page.$eval('[data-testid=migration-detail]', (el) => ({
          remaining: el.getAttribute('data-remaining'),
          failures: el.getAttribute('data-failures'),
        }))
        assert(d.remaining === '0' && (d.failures === null || d.failures === '0'), `详情：${JSON.stringify(d)}`)
        await shot(page, 'm7-migrate-detail')
        await page.keyboard.press('Escape')
        await page.waitForSelector('[data-testid=migration-detail]', { hidden: true })

        // 迁空了：按提示先改为只读，再停用（这个演示存储没有导出物，不用等清理）。
        const hint = await page.$eval(`${card(SRC_NAME)} [data-testid=storage-migration-empty]`, (el) => el.textContent ?? '')
        assert(hint.includes('建议先改为只读') && hint.includes('再停用'), `完成后的提示：${hint}`)
        const clickIn = (text) => page.evaluate((sel, t) => {
          const btn = [...document.querySelector(sel).querySelectorAll('button')].find((b) => b.textContent?.trim() === t)
          btn.click()
        }, card(SRC_NAME), text)
        assert((await buttonBlocked(page, SRC_NAME, '改为只读')) === null, '迁空后应能改为只读')
        await clickIn('改为只读')
        await waitToast(page, `「${SRC_NAME}」已改为只读`)
        await page.waitForFunction((sel) => [...document.querySelector(sel).querySelectorAll('button')]
          .some((b) => b.textContent?.trim() === '停用' && !b.disabled), {}, card(SRC_NAME))
        const r = await buttonBlocked(page, SRC_NAME, '停用')
        assert(r === null, `迁空后应能停用：${r}`)
        await clickIn('停用')
        await confirmDialog(page, '停用')
        await waitToast(page, `「${SRC_NAME}」已改为停用`)
        const src = await c.api('GET', `${BASE}/${c.src.id}`)
        assert(src.status === 'disabled' && !src.in_use, `停用后：${src.status} ${src.in_use}`)
        await page.$eval(card(SRC_NAME), (el) => el.scrollIntoView({ block: 'center' }))
      },
    },
    {
      name: 'm7-migrate-failures',
      user: admin,
      path: '/system/storage',
      waitFor: card(SRC_NAME),
      async prepare(page) {
        const c = await context(page)
        const real = await c.api('GET', `${BASE}/${c.src.id}/migrate`)
        const snap = (fid, v, name) => ({
          snapshot_id: fid, file_id: fid, version_no: v, project_code: 'DEMO-A', file_name: name,
          folder_path: `演示项目 A · 文书档案/M7-2 迁移演示/${c.folder.folder_name}`, file_deleted: false,
        })
        const fake = {
          ...real, failed: 23, failed_objects: 2, total: real.migrated + 23, remaining: 23, failures_truncated: false,
          failures: [
            {
              storage_key: `m7-2-demo/src/DEMO-A/${c.fileIds[1]}/v1.png`, reason_code: 'sha256_mismatch',
              reason: '源存储里的原图 sha256 和快照记的不一样，没有迁', failed_at: real.finished_at,
              snapshots: [snap(c.fileIds[1], 1, 'IMG_0002.png'), snap(c.fileIds[2], 1, 'IMG_0002 (1).png')], snapshot_count: 2,
            },
            {
              storage_key: `m7-2-demo/src/DEMO-A/${c.fileIds[3]}/v1.png`, reason_code: 'source_missing',
              reason: `源存储里没有这个对象（m7-2-demo/src/DEMO-A/${c.fileIds[3]}/v1.png），没有迁`, failed_at: real.finished_at,
              snapshots: [snap(c.fileIds[3], 1, 'IMG_0004.png')], snapshot_count: 21,
            },
          ],
        }
        const summary = { ...fake }
        delete summary.failures
        delete summary.remaining
        const handler = async (req) => {
          const u = new URL(req.url())
          try {
            if (req.method() === 'GET' && u.pathname === BASE) {
              const list = await c.api('GET', BASE)
              const body = list.map((p) => (p.id === c.src.id ? { ...p, in_use: true, status: 'readonly', migration: summary } : p))
              return void req.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
            }
            if (req.method() === 'GET' && u.pathname === `${BASE}/${c.src.id}/migrate`) {
              return void req.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(fake) })
            }
          } catch {
            // 出错时照常转发
          }
          req.continue().catch(() => {})
        }
        c.state.handler = handler
        await page.setRequestInterception(true)
        page.on('request', handler)
        await page.reload({ waitUntil: 'networkidle2' })
        const p = await waitFor('模拟的面板', () => panel(page, SRC_NAME), 10000)
        assert(p.status === 'done' && p.failed === 23 && p.text.includes('完成（有失败）') && p.text.includes('核对不一致 23 个') &&
          p.text.includes('查看失败清单（23）'), `面板：${JSON.stringify(p)}`)
        await page.click(`${card(SRC_NAME)} [data-testid=migration-details]`)
        await page.waitForSelector('[data-testid=migration-detail][data-state=ready]')
        const rows = await page.$$eval('[data-testid=migration-failure]', (els) =>
          els.map((e) => ({ reason: e.getAttribute('data-reason'), n: Number(e.getAttribute('data-snapshots')), text: e.textContent ?? '' })),
        )
        assert(rows.length === 2 && rows[0].reason === 'sha256_mismatch' && rows[0].n === 2 && rows[1].reason === 'source_missing' &&
          rows[1].n === 21 && rows[1].text.includes('等 21 个快照') && !rows[0].text.includes('等 '), `失败清单：${JSON.stringify(rows)}`)
        assert(rows[0].text.includes('sha256 不一致') && rows[0].text.includes('M7-2 迁移演示') && rows[1].text.includes('源对象缺失'),
          `失败清单的文字：${rows.map((r) => r.text).join(' | ')}`)
        const remain = await page.$eval('[data-testid=migration-remaining]', (el) => el.textContent ?? '')
        assert(remain.includes('23'), `还剩：${remain}`)
      },
      async after(page) {
        const c = await ctxPromise
        if (c?.state.handler) page.off('request', c.state.handler)
        await page.setRequestInterception(false).catch(() => {})
      },
    },
  ],
}
