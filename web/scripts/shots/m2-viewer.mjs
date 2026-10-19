// M2-4 查看原图（OpenSeadragon）的截图和断言：/files 右键"查看原图"。
//
//   cd web && npm run build && DOCLITE_SERVER=http://127.0.0.1:<server 端口> npm run preview -- --port <vite 端口> --strictPort
//   cd client && go run ./cmd/devshell -web http://127.0.0.1:<vite 端口> -listen 127.0.0.1:<端口> -cache <临时目录>   # 壳模式要用
//   INIT_ADMIN_PASSWORD=admin123456 [SHOTS_TMP=<临时目录>] \
//     npm run screenshots -- --spec scripts/shots/m2-viewer.mjs --base http://127.0.0.1:<devshell 的端口> --out <临时目录>
//
// 前端地址是 devshell（/api 经 vite 转给 server；取图地址就是客户端的缓存和切块代码）。原图只在客户端里看（M10-2 删掉了
// 浏览器里的整图模式和 TIFF 解码，原来的浏览器截图一起删掉）。样本见 scripts/lib/viewer-shots.mjs：`npm run seed` 的
// 「格式样本」（JPEG、TIFF LZW、G4），外加第一次运行时建好的"M2-4 查看器样本"文件夹（对比样本、17 MB 的 TIFF），之后
// 复用、不删。200 MB 的几张（shell-viewer-200mb、shell-viewer-switch）第一次运行时另在里面建"200 MB 切换"（约 210 MB）：
// 演示数据不放大文件，跑这几张建议用临时库（另建一个库、起一个 server、npm run seed），只跑别的用 --only 跳过它们。
//
// 截图（docs/screenshots/m2/，壳模式的地址 + Wails 桥的模拟，分块由 devshell 用客户端的 Go 代码现场切）：shell-viewer-jpeg、
// shell-viewer-tiff-lzw、shell-viewer-tiff-g4、shell-viewer-100、shell-viewer-200mb（只取看得到的块）、shell-viewer-too-large
// （取图地址 422）、shell-viewer-compare、shell-viewer-switch、shell-viewer-flip（连按 10 次"下一个"之后）、
// shell-viewer-tile-failed（取块 503，提示和"重试"，shell-viewer-tile-retry 中途另存）、shell-viewer-tile-retry（点"重试"之后）。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - 除对比外，同一时间只有一张原图在加载（页面里包一层 fetch，统计取图地址的 dzi、tile 按快照分组，页面一取消就算结束），
//   对比最多两张；切换文件时上一张的 dzi 请求 AbortError、之后不再取它的块；
// - 界面只请求看得到的块：200 MB TIFF 适合窗口、100% 时请求过的块数都少于总块数（G4 的 100% 也一样）；
// - 对比时两侧的视口缩放和中心一致，同位切换后位置不变；对比联动（M2-4 审查第 1 轮）：点"100%"后马上把鼠标移进左侧、
//   移进右侧、留在工具栏，在右侧滚轮后移到左侧，右侧滚轮后马上在左侧滚轮（中途换为准的一侧）：动画结束后两侧视口一致，
//   点 100% 的几种右侧都是 100%；
// - 快速翻页：在「格式样本」里从第一个连按"下一个"到最后一个（最多 10 次，每次间隔不到 250 毫秒），只有最后停下的那张
//   请求了分块描述；
// - 取块失败（503）：每块自动重试共 3 次，仍失败的计数、提示"有 n 块没取到"，点"重试"后重新加载，不再缺块。

import { mkdirSync } from 'node:fs'
import path from 'node:path'

import {
  assert,
  BIG200,
  BIG_FILE,
  apiOrigin,
  COMPARE_FILE,
  ensureBench,
  installShellMock,
  login,
  openFolder,
  openViewer,
  pageOrigin,
  settle,
  sleep,
  viewerContext,
  viewerData,
  waitViewer,
  dragBy,
  zoomAtPage,
} from '../lib/viewer-shots.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const IDLE = 'networkidle2'

// 「格式样本」里的（npm run seed）；BIG200 在"M2-4 查看器样本/200 MB 切换"（ensureBench）。
const JPEG = 'jpeg-rgb.jpg'
const LZW = 'tiff-lzw-rgb.tif'
const G4 = 'tiff-g4-1bit.tif'
/** 中途另存的截图：和截图同一个目录（命令行的 --out，默认 docs/screenshots/m2）。 */
const outArg = process.argv.indexOf('--out')
const OUT_DIR = path.resolve(outArg > 0 ? process.argv[outArg + 1] : '../docs/screenshots/m2')

async function ctxOf(page) {
  return viewerContext(apiOrigin(page), { user: admin, password: adminPassword })
}

/** 要"200 MB 切换"的几张：第一次运行时建好。 */
async function benchCtx(page) {
  return ensureBench(await ctxOf(page))
}

/** 打开某个样本所在的文件夹，右键它 → 查看原图。 */
async function view(page, name) {
  const ctx = await ctxOf(page)
  const f = ctx.files[name]
  assert(f, `没有样本 ${name}`)
  await openFolder(page, pageOrigin(page), f.folder_id)
  await openViewer(page, name)
  return f
}

async function click(page, testid) {
  await page.click(`[data-testid=${testid}]`)
}

/** 壳里：页面里在途的取图请求（dzi、tile）按快照分组，同一时间最多几张（截图模拟里包了一层 fetch 统计）。 */
async function shellOneAtATime(page, label, max = 1) {
  const st = await page.evaluate(() => window.__shellImgStats)
  assert(st.max <= max, `${label}：同时在加载的原图有 ${st.max} 张（应不超过 ${max}）`)
  console.log(`  断言 ${label}：同时在加载的原图最多 ${st.max} 张`)
  return st
}

// ---------- 壳的模拟 ----------

/** 壳里的一张：壳模式的地址、Wails 桥的模拟、拦截取图地址做统计，经登录页登录。 */
function shellShot(name, prepare, mockOptions = {}) {
  let stats = null
  return {
    name,
    path: '/login',
    shell: true,
    waitFor: '#username',
    async init(page) {
      const fail = new Map()
      if (mockOptions.failBig) {
        // 这时页面还没打开、不知道 file_id：先占位，prepare 里换成"超出预算"样本的 file_id。
        fail.set('__big__', { code: 'too_large', message: '原图太大，切不了块（模拟壳的 422 too_large）' })
      }
      stats = await installShellMock(page, { fail, dziDelayMs: mockOptions.dziDelayMs ?? 0 })
      stats.fail = fail
    },
    async prepare(page) {
      await login(page, pageOrigin(page), admin, adminPassword)
      await page.waitForSelector('[data-testid=side-nav]')
      const ctx = await ctxOf(page)
      if (stats.fail.has('__big__')) {
        stats.fail.set(ctx.files[BIG_FILE].file_id, stats.fail.get('__big__'))
        stats.fail.delete('__big__')
      }
      await prepare(page, { ctx, stats })
    },
    delay: 400,
  }
}

async function shellOpen(page, name, mode = 'tiles', states = ['ready']) {
  await view(page, name)
  const d = await waitViewer(page, states)
  if (states.includes('ready')) assert(d.mode === mode, `${name} 应该用 ${mode} 模式，实际 ${d.mode}`)
  await settle(page)
  return d
}

function tilesOf(stats, sid) {
  return stats.tiles.get(sid)?.size ?? 0
}

// ---------- 对比联动（M2-4 审查第 1 轮） ----------

/** 两侧的 data 属性。 */
function bothSides(page) {
  return page.evaluate(() =>
    [...document.querySelectorAll('[data-testid=compare-pane]')].map((p) => ({ ...p.querySelector('[data-testid=image-viewer]').dataset })),
  )
}

/**
 * 对比联动的几种操作（compare-test 的场景）：每种先回到适合窗口，做完等两侧的动画停下，要求两侧视口缩放、中心一致；
 * 点 100% 的几种另要求右侧（为准的一侧）是 100%。
 */
async function compareLinkScenarios(page, label) {
  const box = async (sel) => (await page.$(sel)).boundingBox()
  const left = await box('[data-testid=compare-pane][data-side=left]')
  const right = await box('[data-testid=compare-pane][data-side=right]')
  const actual = await box('[data-testid=viewer-actual]')
  const center = (b) => [b.x + b.width / 2, b.y + b.height / 2]
  const [ax, ay] = center(actual)
  const scenarios = [
    ['点 100% 后留在工具栏', true, async () => {}],
    ['点 100% 后移进右侧', true, async () => {
      await page.mouse.move(center(right)[0], ay)
      await page.mouse.move(...center(right))
    }],
    ['点 100% 后直接往下移进左侧', true, async () => {
      await page.mouse.move(ax, center(left)[1], { steps: 3 })
    }],
    ['在右侧滚轮 4 次后移到左侧', false, async () => {}, true],
    ['在右侧滚轮 2 次后马上在左侧滚轮 2 次', false, async () => {}, 'both'],
  ]
  for (const [name, hundred, after, wheel] of scenarios) {
    await click(page, 'viewer-fit')
    await settle(page, 0)
    await settle(page, 1)
    await page.mouse.move(ax, ay - 30)
    if (hundred) {
      await page.mouse.move(ax, ay)
      await page.mouse.down()
      await page.mouse.up()
      await sleep(60)
      await after()
    } else {
      await page.mouse.move(...center(right))
      await sleep(100)
      const n = wheel === 'both' ? 2 : 4
      for (let i = 0; i < n; i++) {
        await page.mouse.wheel({ deltaY: -120 })
        await sleep(30)
      }
      if (wheel === 'both') {
        await page.mouse.move(...center(left))
        for (let i = 0; i < 2; i++) {
          await page.mouse.wheel({ deltaY: -120 })
          await sleep(30)
        }
      } else {
        await page.mouse.move(left.x + left.width - 20, center(right)[1])
      }
    }
    await sleep(200)
    await settle(page, 0)
    await settle(page, 1)
    await sleep(300)
    const [l, r] = await bothSides(page)
    const toolbar = await page.$eval('[data-testid=viewer-zoom]', (e) => e.textContent.trim())
    assert(l.viewZoom === r.viewZoom && l.viewCenter === r.viewCenter, `${label} ${name}：两侧应该一致，左 ${l.viewZoom}@${l.viewCenter}，右 ${r.viewZoom}@${r.viewCenter}`)
    if (hundred) assert(r.zoom === '100' && toolbar === '100%', `${label} ${name}：右侧应该停在 100%，实际 ${r.zoom}%（工具栏 ${toolbar}）`)
    console.log(`  断言 ${label} ${name}：两侧视口 ${r.viewZoom}@${r.viewCenter} 一致；左 ${l.zoom}% 右 ${r.zoom}%（工具栏 ${toolbar}）`)
  }
  await page.mouse.move(ax, ay - 30)
}

// ---------- 截图清单 ----------

export default {
  out: '../docs/screenshots/m2',
  waitUntil: IDLE,
  accounts: { [admin]: adminPassword },
  shots: [
    // ===== 壳（壳模式的地址 + Wails 桥的模拟，取图和切块是 devshell 里客户端的 Go 代码） =====
    shellShot('shell-viewer-jpeg', async (page) => {
      await shellOpen(page, JPEG)
      await shellOneAtATime(page, `壳 ${JPEG}`)
    }),
    shellShot('shell-viewer-tiff-lzw', async (page) => {
      await shellOpen(page, LZW)
      await shellOneAtATime(page, `壳 ${LZW}`)
    }),
    shellShot('shell-viewer-tiff-g4', async (page) => {
      await shellOpen(page, G4)
      await shellOneAtATime(page, `壳 ${G4}`)
    }),
    shellShot('shell-viewer-100', async (page, { ctx, stats }) => {
      const d0 = await shellOpen(page, G4)
      await click(page, 'viewer-actual')
      await settle(page)
      await dragBy(page, '[data-testid=image-viewer]', -700, 1450)
      await settle(page)
      await page.mouse.move(5, 5)
      await waitViewer(page, ['ready'])
      await sleep(500)
      const d = await viewerData(page)
      assert(d.zoom === '100', `点 100% 之后比例应该是 100%，实际 ${d.zoom}%`)
      const desc = stats.descriptors.get(d0.snapshotId)
      const n = tilesOf(stats, d0.snapshotId)
      assert(n < desc.tile_count, `100% 时请求过的块（${n}）应该少于总块数（${desc.tile_count}）`)
      console.log(`  断言 壳 G4 100%：请求过 ${n} 块，共 ${desc.tile_count} 块（PNG，1 位黑白）`)
      void ctx
    }),
    shellShot('shell-viewer-200mb', async (page, { stats }) => {
      await benchCtx(page)
      const d0 = await shellOpen(page, BIG200)
      const desc = stats.descriptors.get(d0.snapshotId)
      const atFit = tilesOf(stats, d0.snapshotId)
      await click(page, 'viewer-actual')
      await settle(page)
      await sleep(800)
      const at100 = tilesOf(stats, d0.snapshotId)
      const attr = Number((await viewerData(page)).tileRequests)
      assert(atFit < desc.tile_count && at100 < desc.tile_count, `只应请求看得到的块：适合窗口 ${atFit}、100% ${at100}，共 ${desc.tile_count}`)
      console.log(
        `  断言 壳 200 MB TIFF 只取看得到的块：适合窗口时请求 ${atFit} 块，100% 后累计 ${at100} 块（查看器记 ${attr} 次），共 ${desc.tile_count} 块、${desc.levels} 级`,
      )
      await shellOneAtATime(page, '壳 200 MB')
    }),
    shellShot(
      'shell-viewer-too-large',
      async (page, { ctx, stats }) => {
        await view(page, BIG_FILE)
        const d = await waitViewer(page, ['too_large', 'ready', 'failed', 'unsupported'])
        assert(d.state === 'too_large', `取图地址返回 422 too_large 时应该是 too_large，实际 ${d.state}`)
        await page.waitForFunction(() => document.querySelector('[data-testid=image-viewer]')?.dataset.cover === 'loaded', { timeout: 10000 })
        const text = await page.$eval('[data-testid=viewer-problem]', (el) => el.textContent)
        assert(!text.includes('改用客户端'), `壳里不该提示"改用客户端"：${text}`)
        assert((stats.tiles.get(d.snapshotId)?.size ?? 0) === 0, '422 之后不应请求块')
        console.log('  断言 壳 422 too_large：只显示封面，提示下载原图')
        await settle(page)
        void ctx
      },
      { failBig: true },
    ),
    shellShot('shell-viewer-compare', async (page) => {
      await view(page, COMPARE_FILE)
      await waitViewer(page, ['ready'])
      await click(page, 'viewer-compare')
      await waitViewer(page, ['ready'], { index: 0 })
      await waitViewer(page, ['ready'], { index: 1 })
      const modes = [(await viewerData(page, 0)).mode, (await viewerData(page, 1)).mode]
      assert(modes.every((m) => m === 'tiles'), `壳里对比两侧都应该用分块：${modes}`)
      await compareLinkScenarios(page, '对比联动（壳）')
      await click(page, 'viewer-fit')
      await settle(page, 0)
      await settle(page, 1)
      await zoomAtPage(page, '[data-testid=compare-pane][data-side=left]', 0.8, 0.06, 8)
      await settle(page, 0)
      await settle(page, 1)
      const [l, r] = [await viewerData(page, 0), await viewerData(page, 1)]
      assert(l.viewZoom === r.viewZoom && l.viewCenter === r.viewCenter, `两侧应该联动：左 ${l.viewZoom}@${l.viewCenter}，右 ${r.viewZoom}@${r.viewCenter}`)
      console.log(`  断言 壳 对比联动（在左侧缩放）：视口缩放 ${l.viewZoom}、中心 ${l.viewCenter} 一致`)
      await shellOneAtATime(page, '壳 对比', 2)
    }),
    shellShot(
      'shell-viewer-switch',
      async (page, { stats }) => {
        // 每次取描述都晚 1.5 秒（模拟第一次打开要等壳下载、切块）：在等的时候切到下一个，上一张的 dzi 请求被取消，
        // 同一时间只有一张在加载。
        const ctx = await benchCtx(page)
        await openFolder(page, pageOrigin(page), ctx.files[BIG200].folder_id)
        await openViewer(page, BIG200)
        await page.waitForFunction(() => document.querySelector('[data-testid=image-viewer]')?.dataset.step === 'tiles', { timeout: 10000, polling: 20 })
        const first = (await viewerData(page)).snapshotId
        await sleep(200)
        await click(page, 'viewer-next')
        const d = await waitViewer(page, ['ready'], { timeout: 60000 })
        await settle(page)
        const st = await shellOneAtATime(page, '壳 切换文件')
        const aborted = st.aborted.filter((a) => a.sid === first)
        assert(aborted.some((a) => a.kind === 'dzi'), `切换后上一张的 dzi 请求应该被取消：${JSON.stringify(st.aborted)}`)
        assert(!stats.tiles.get(first)?.size, '上一张不应再请求块')
        assert(d.snapshotId !== first, '应该已经换成下一张')
        console.log(`  断言 壳 切换文件：上一张的 dzi 请求被取消（AbortError），没有再取它的块；下一张画完`)
      },
      { dziDelayMs: 1500 },
    ),
    shellShot('shell-viewer-flip', async (page, { ctx, stats }) => {
      // 快速翻页（M2-4 审查第 1 轮）：从「格式样本」的第一个连按"下一个"（按到最后一个，最多 10 次；M9-3 起这个文件夹只有
      // 8 个），每次间隔不到 250 毫秒，只有最后停下的那张请求了分块描述。
      const list = Object.values(ctx.files)
        .filter((f) => f.folder_key === 'samples')
        .sort((x, y) => x.sort_no - y.sort_no)
      const flips = Math.min(10, list.length - 1)
      assert(flips >= 5, `「格式样本」里的文件太少（${list.length} 个），翻不了页`)
      await openFolder(page, pageOrigin(page), list[0].folder_id)
      await openViewer(page, list[0].file_name)
      const first = await waitViewer(page, ['ready'])
      await settle(page)
      const before = new Map(stats.dzi)
      const t0 = Date.now()
      for (let i = 0; i < flips; i++) {
        await click(page, 'viewer-next')
        await sleep(80)
      }
      const flipMs = Date.now() - t0
      const d = await waitViewer(page, ['ready'], { timeout: 60000 })
      await settle(page)
      await sleep(500)
      const asked = [...stats.dzi].filter(([sid, n]) => n > (before.get(sid) ?? 0)).map(([sid]) => sid)
      assert(d.snapshotId !== first.snapshotId, '应该已经翻到后面的文件')
      assert(asked.length === 1 && asked[0] === d.snapshotId, `只有最后停下的那张应该请求分块描述，实际 ${asked.length} 张：${JSON.stringify(asked)}（停在 ${d.snapshotId}）`)
      const title = await page.$eval('[data-testid=viewer-title]', (e) => e.textContent.trim())
      assert(title === list[flips].file_name, `应该停在 ${list[flips].file_name}，实际 ${title}`)
      console.log(`  断言 壳 快速翻页：${flipMs} ms 内按了 ${flips} 次"下一个"，停在 ${title}，只有这一张请求了分块描述`)
    }),
    shellShot('shell-viewer-tile-retry', async (page, { ctx, stats }) => {
      // 取块失败（M2-4 审查第 1 轮）：G4 第 10 级以上的块都回 503，每块自动重试，仍失败的计数、提示"重试"；
      // 恢复后点"重试"，重新加载，不再缺块。提示那一刻另存 shell-viewer-tile-failed.png。
      const f = ctx.files[G4]
      let failing = true
      stats.tileFail = (sid, level) => (failing && level >= 10 ? 503 : 0)
      await openFolder(page, pageOrigin(page), f.folder_id)
      await openViewer(page, G4)
      await waitViewer(page, ['ready', 'failed'], { timeout: 60000 })
      await page.waitForSelector('[data-testid=viewer-tile-failures]', { timeout: 30000 })
      const d = await viewerData(page)
      const attempts = [...stats.tileAttempts].filter(([k]) => k.startsWith(`${d.snapshotId}/`) && Number(k.split('/')[1]) >= 10)
      assert(d.state === 'ready' && Number(d.tileFailures) > 0, `应该画出低几级、提示缺块：${d.state}，缺 ${d.tileFailures} 块`)
      assert(attempts.length > 0 && attempts.every(([, n]) => n === 3), `失败的块应该各请求 3 次（含 2 次自动重试）：${JSON.stringify(attempts)}`)
      mkdirSync(OUT_DIR, { recursive: true })
      await page.screenshot({ path: path.join(OUT_DIR, 'shell-viewer-tile-failed.png') })
      console.log(`  断言 壳 取块 503：${attempts.length} 块各请求 3 次后放弃，提示"有 ${d.tileFailures} 块原图没取到"和"重试"（另存 shell-viewer-tile-failed.png）`)
      failing = false
      await click(page, 'viewer-tile-retry')
      await page.waitForFunction(() => document.querySelector('[data-testid=image-viewer]')?.dataset.tileFailures === '0', { timeout: 10000, polling: 50 })
      const after = await waitViewer(page, ['ready'], { timeout: 60000 })
      await settle(page)
      await sleep(500)
      const again = await viewerData(page)
      assert(again.tileFailures === '0' && !(await page.$('[data-testid=viewer-tile-failures]')), `重试之后不应该再缺块：${again.tileFailures}`)
      console.log(`  断言 壳 取块恢复后点"重试"：重新加载，请求 ${after.tileRequests} 块，不再缺块`)
      stats.tileFail = null
    }),
  ],
}
