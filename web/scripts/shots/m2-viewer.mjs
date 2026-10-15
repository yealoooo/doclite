// M2-4 查看原图（OpenSeadragon）的截图和断言：/files 右键"查看原图"。
//
//   cd shell/src-tauri && cargo build --release --example tiles-bench     # 壳模式的模拟要用（第一次约 2 分钟）
//   INIT_ADMIN_PASSWORD=admin123456 [SHOTS_TMP=<临时目录>] \
//     npm run screenshots -- --spec scripts/shots/m2-viewer.mjs --base http://localhost:<preview 端口>
//
// 前端地址是 vite preview（/api 代理到 server）。样本见 scripts/lib/viewer-shots.mjs（DEMO-A 下已有的 M1、M2-3 样本，
// 外加第一次运行时建好的"M2-4 查看器样本"文件夹，之后复用、不删）。局域网地址（非安全上下文）另见 m2-viewer-lan.mjs。
//
// 截图（docs/screenshots/m2/）：
// - 浏览器（真实数据）：viewer-jpeg、viewer-tiff-lzw、viewer-tiff-g4、viewer-100（100% 的局部）、viewer-too-large（超出预算，
//   只显示封面）、viewer-rotate（向右转 90°）、viewer-compare（左右并排，联动缩放）、viewer-compare-toggle（同位切换看上一轮）、
//   viewer-loading（限速下载 200 MB TIFF：先显示封面、下载进度，viewer-switch 中途另存）、viewer-switch（下载中连切两次文件
//   之后）、viewer-tiff-200mb（浏览器打开 200 MB TIFF，记内存和耗时）、viewer-compare-budget（对比两张大 TIFF，两侧共用
//   内存预算：放不下的一侧只显示封面；拦截 snapshots 换成现场生成的 TIFF）。
// - 壳（IPC 和 img 协议模拟，分块用壳的 tiles-bench 现场切）：shell-viewer-jpeg、shell-viewer-tiff-lzw、shell-viewer-tiff-g4、
//   shell-viewer-100、shell-viewer-200mb（只取看得到的块）、shell-viewer-too-large（img 协议 422）、shell-viewer-compare、
//   shell-viewer-switch、shell-viewer-flip（连按 10 次"下一个"之后）、shell-viewer-tile-failed（取块 503，提示和"重试"，
//   shell-viewer-tile-retry 中途另存）、shell-viewer-tile-retry（点"重试"之后）。
//
// 断言（失败时这一张算失败，脚本退出码 1）：
// - 除对比外，同一时间只有一张原图在加载（浏览器：DevTools 看到的 S3 上原图的 GET，Worker 被 terminate 时它的请求一起结束；
//   壳：页面里包一层 fetch，统计 img 协议的 dzi、tile 按快照分组，页面一取消就算结束），对比最多两张；
// - 切换文件时上一张的请求和 Worker 被取消：浏览器里 Worker 被销毁（workerdestroyed）、同时活着的 Worker 不超过 1 个，
//   另用一个开着 NetLog 的 Chrome 再跑一遍，核对 200 MB 和 8.7 MB 那两张的下载在网络栈里是 CANCELLED（DevTools 收不到
//   被 terminate 的 Worker 的请求结束）；壳里上一张的 dzi 请求 AbortError、之后不再取它的块；
// - 壳里界面只请求看得到的块：200 MB TIFF 适合窗口、100% 时请求过的块数都少于总块数（G4 的 100% 也一样）；
// - 超出预算时不下载原图，只显示封面；对比时两侧的视口缩放和中心一致，同位切换后位置不变。
// - 对比联动（M2-4 审查第 1 轮）：点"100%"后马上把鼠标移进左侧、移进右侧、留在工具栏，在右侧滚轮后移到左侧，
//   右侧滚轮后马上在左侧滚轮（中途换为准的一侧）：动画结束后两侧视口一致，点 100% 的几种右侧都是 100%（浏览器、壳各跑一遍）；
// - 壳里快速翻页：连按 10 次"下一个"（每次间隔不到 250 毫秒），只有最后停下的那张请求了分块描述；
// - 壳里取块失败（503）：每块自动重试共 3 次，仍失败的计数、提示"有 n 块没取到"，点"重试"后重新加载，不再缺块；
// - 浏览器里对比两张大 TIFF：两侧共用一份内存预算——放不下的一侧（over_budget）只显示封面、提示改用客户端、不下载原图；
//   等另一侧解完就够的一侧先等（data-step=wait），之后照常显示。
// 另外记下浏览器打开 200 MB TIFF 的耗时和渲染进程的内存峰值（viewer-tiff-200mb，独立的浏览器上下文）。

import { mkdirSync } from 'node:fs'
import path from 'node:path'

import {
  assert,
  BENCH_FOLDER,
  BIG_FILE,
  browserOriginalKey,
  COMPARE_FILE,
  installShellMock,
  login,
  openFolder,
  openViewer,
  sampleMemory,
  settle,
  shotsTmp,
  sleep,
  trackOriginals,
  trackWorkers,
  viewerContext,
  viewerData,
  waitViewer,
  withNetLog,
  dragBy,
  zoomAtPage,
  interceptCompareTiffs,
  recordViewerSteps,
} from '../lib/viewer-shots.mjs'

const admin = process.env.INIT_ADMIN_USERNAME ?? 'admin'
const adminPassword = process.env.INIT_ADMIN_PASSWORD ?? 'change-me-now'
const IDLE = 'networkidle2'

const JPEG = 'IMG_1.jpg'
const JPEG2 = 'IMG_2.jpg'
const LZW = 'tiff-lzw-rgb.tif'
const G4 = 'tiff-g4-1bit.tif'
const BIG200 = 'a3-600-none-rgb.tif'
/** 下载中的截图（viewer-switch 那一张的中途另存）：和截图同一个目录（命令行的 --out，默认 docs/screenshots/m2）。 */
const outArg = process.argv.indexOf('--out')
const OUT_DIR = path.resolve(outArg > 0 ? process.argv[outArg + 1] : '../docs/screenshots/m2')
const OUT_LOADING = path.join(OUT_DIR, 'viewer-loading.png')

let browserRef = null

async function ctxOf(page) {
  browserRef = page.browser()
  return viewerContext(new URL(page.url()).origin, { user: admin, password: adminPassword })
}

/** 打开某个样本所在的文件夹，右键它 → 查看原图。 */
async function view(page, name) {
  const ctx = await ctxOf(page)
  const f = ctx.files[name]
  assert(f, `没有样本 ${name}`)
  await openFolder(page, ctx.origin, f.folder_id)
  await openViewer(page, name)
  return f
}

async function click(page, testid) {
  await page.click(`[data-testid=${testid}]`)
}

/** 壳里：页面里在途的 img 协议请求（dzi、tile）按快照分组，同一时间最多几张（截图模拟里包了一层 fetch 统计）。 */
async function shellOneAtATime(page, label, max = 1) {
  const st = await page.evaluate(() => window.__shellImgStats)
  assert(st.max <= max, `${label}：同时在加载的原图有 ${st.max} 张（应不超过 ${max}）`)
  console.log(`  断言 ${label}：同时在加载的原图最多 ${st.max} 张`)
  return st
}

function oneAtATime(track, label, max = 1) {
  assert(track.maxConcurrent <= max, `${label}：同时在加载的原图有 ${track.maxConcurrent} 张（应不超过 ${max}）`)
  console.log(`  断言 ${label}：同时在加载的原图最多 ${track.maxConcurrent} 张`)
}

/** 浏览器里打开一个样本并等原图画完；断言来源、只有一张在加载。 */
function browserShot(name, file, mode, extra) {
  return {
    name,
    user: admin,
    path: '/files',
    waitUntil: IDLE,
    waitFor: '[data-testid=files-page]',
    async prepare(page) {
      const track = trackOriginals(page, browserOriginalKey)
      await view(page, file)
      const d = await waitViewer(page, ['ready'])
      assert(d.mode === mode, `${file} 应该用 ${mode} 模式，实际 ${d.mode}`)
      await settle(page)
      if (extra) await extra(page, d)
      oneAtATime(track, file)
    },
    delay: 400,
  }
}

// ---------- 壳的模拟 ----------

const shellTmp = () => shotsTmp('m2-viewer-shell')

/** 壳里的一张：注入 IPC、拦截 img 协议，经登录页登录。 */
function shellShot(name, prepare, mockOptions = {}) {
  let stats = null
  return {
    name,
    path: '/login',
    waitFor: '#username',
    async init(page) {
      const fail = new Map()
      if (mockOptions.failBig) {
        // 这时页面还没打开、不知道 file_id：先占位，prepare 里换成"超出预算"样本的 file_id。
        fail.set('__big__', { code: 'too_large', message: '原图太大，切不了块（模拟壳的 422 too_large）' })
      }
      stats = await installShellMock(page, { cacheDir: shellTmp(), fail, dziDelayMs: mockOptions.dziDelayMs ?? 0 })
      stats.fail = fail
    },
    async prepare(page) {
      await login(page, new URL(page.url()).origin, admin, adminPassword)
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

/**
 * 限速（12 MB/s）打开 200 MB 的 TIFF，下载中切到下一个（A4 300 DPI 灰度，8.7 MB），它开始下载后再切到下一个
 * （600 DPI G4）。`onLoading` 在 200 MB 那张下载中（已经显示封面、有进度）时调用。返回三张图的 S3 路径。
 */
async function switchScenario(page, origin, onLoading) {
  const ctx = await viewerContext(origin, { user: admin, password: adminPassword })
  const f = ctx.files[BIG200]
  assert(f.folder_key === 'bench', `${BIG200} 应该在 ${BENCH_FOLDER}`)
  const cdp = await page.createCDPSession()
  await cdp.send('Network.enable')
  await cdp.send('Network.emulateNetworkConditions', {
    offline: false,
    latency: 5,
    downloadThroughput: 12 * 1024 * 1024,
    uploadThroughput: 12 * 1024 * 1024,
  })
  try {
    await openFolder(page, origin, f.folder_id)
    await openViewer(page, BIG200)
    await page.waitForFunction(
      () => {
        const el = document.querySelector('[data-testid=image-viewer]')
        return (
          el?.dataset.step === 'download' &&
          el.dataset.cover === 'loaded' &&
          !!document.querySelector('[data-testid=viewer-loading]')?.textContent.match(/[1-9]\d?%/)
        )
      },
      { timeout: 30000, polling: 50 },
    )
    const ids = [(await viewerData(page)).fileId]
    if (onLoading) await onLoading()
    await click(page, 'viewer-next')
    await page.waitForFunction(() => document.querySelector('[data-testid=image-viewer]')?.dataset.step === 'download', {
      timeout: 10000,
      polling: 20,
    })
    ids.push((await viewerData(page)).fileId)
    await sleep(150)
    await click(page, 'viewer-next')
    const d = await waitViewer(page, ['ready'], { timeout: 60000 })
    ids.push(d.fileId)
    return ids
  } finally {
    await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 })
    await cdp.detach()
  }
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
    // ===== 浏览器（真实数据） =====
    browserShot('viewer-jpeg', JPEG, 'image'),
    browserShot('viewer-tiff-lzw', LZW, 'worker'),
    browserShot('viewer-tiff-g4', G4, 'worker'),
    browserShot('viewer-100', LZW, 'worker', async (page) => {
      await click(page, 'viewer-actual')
      await settle(page)
      const d = await viewerData(page)
      assert(d.zoom === '100', `点 100% 之后比例应该是 100%，实际 ${d.zoom}%`)
      // 拖到页面右上角（页码、标题）。
      await dragBy(page, '[data-testid=image-viewer]', -700, 1450)
      await settle(page)
      await page.mouse.move(5, 5)
      console.log('  断言 100%：data-zoom=100')
    }),
    {
      name: 'viewer-too-large',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const track = trackOriginals(page, browserOriginalKey)
        const f = await view(page, BIG_FILE)
        const d = await waitViewer(page, ['too_large', 'ready', 'failed', 'unsupported'])
        assert(d.state === 'too_large', `超出预算的样本应该是 too_large，实际 ${d.state}`)
        await page.waitForFunction(() => document.querySelector('[data-testid=image-viewer]')?.dataset.cover === 'loaded', { timeout: 10000 })
        assert(!track.started.some((r) => r.url.includes(f.file_id)), '超出预算时不应该下载原图')
        const text = await page.$eval('[data-testid=viewer-problem]', (el) => el.textContent)
        assert(text.includes('改用客户端') && text.includes('下载原图'), `提示里应该有"下载原图""改用客户端"：${text}`)
        console.log('  断言 超出预算：只显示封面，没有下载原图，提示下载原图或改用客户端')
        await settle(page)
      },
      delay: 400,
    },
    browserShot('viewer-rotate', JPEG2, 'image', async (page) => {
      await click(page, 'viewer-rotate-right')
      await settle(page)
      const d = await viewerData(page)
      assert(d.rotation === '90', `向右转之后应该是 90°，实际 ${d.rotation}`)
      console.log('  断言 旋转：data-rotation=90，适合窗口')
    }),
    {
      name: 'viewer-compare',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const track = trackOriginals(page, browserOriginalKey)
        await view(page, COMPARE_FILE)
        await waitViewer(page, ['ready'])
        await click(page, 'viewer-compare')
        await page.waitForSelector('[data-testid=compare-viewer]')
        await waitViewer(page, ['ready'], { index: 0 })
        await waitViewer(page, ['ready'], { index: 1 })
        const labels = await page.$$eval('[data-testid=compare-label]', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()))
        assert(labels[0].includes('v1') && labels[0].includes('模糊'), `左侧应该标 v1 和驳回原因"模糊"：${labels[0]}`)
        assert(labels[1].includes('v2'), `右侧应该标 v2：${labels[1]}`)
        await compareLinkScenarios(page, '对比联动（浏览器）')
        await click(page, 'viewer-fit')
        await settle(page, 1)
        await settle(page, 0)
        // 在右侧对着印章滚轮放大，左侧跟着动（v1 150 DPI 的印章边缘有锯齿，v2 300 DPI 是平滑的）。
        await zoomAtPage(page, '[data-testid=compare-pane][data-side=right]', 0.66, 0.84, 7)
        await settle(page, 1)
        await settle(page, 0)
        const [l, r] = [await viewerData(page, 0), await viewerData(page, 1)]
        assert(l.viewZoom === r.viewZoom && l.viewCenter === r.viewCenter, `两侧应该联动：左 ${l.viewZoom}@${l.viewCenter}，右 ${r.viewZoom}@${r.viewCenter}`)
        assert(Number(r.viewZoom) > 1.5, `右侧应该已经放大：${r.viewZoom}`)
        console.log(`  断言 对比联动：两侧视口缩放 ${r.viewZoom}、中心 ${r.viewCenter} 一致；左 ${l.zoom}%（v1 150 DPI）右 ${r.zoom}%（v2 300 DPI）`)
        oneAtATime(track, '对比', 2)
      },
      delay: 500,
    },
    {
      name: 'viewer-compare-toggle',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        await view(page, COMPARE_FILE)
        await waitViewer(page, ['ready'])
        await click(page, 'viewer-compare')
        await waitViewer(page, ['ready'], { index: 0 })
        await waitViewer(page, ['ready'], { index: 1 })
        await click(page, 'compare-mode-toggle')
        await page.waitForSelector('[data-testid=compare-viewer][data-mode=toggle]')
        // 先在显示的一侧（右，v2）对着印章放大，再切到左（v1），位置不变。
        await zoomAtPage(page, '[data-testid=compare-viewer]', 0.66, 0.84, 7)
        await settle(page, 1)
        const before = await viewerData(page, 1)
        await click(page, 'compare-toggle-side')
        await page.waitForSelector('[data-testid=compare-viewer][data-showing=left]')
        await settle(page, 0)
        const after = await viewerData(page, 0)
        assert(before.viewZoom === after.viewZoom && before.viewCenter === after.viewCenter, `切换后位置应该不变：${before.viewZoom}@${before.viewCenter} → ${after.viewZoom}@${after.viewCenter}`)
        console.log(`  断言 同位切换：切到 v1 后视口缩放 ${after.viewZoom}、中心 ${after.viewCenter} 不变`)
      },
      delay: 500,
    },
    {
      // 限速下载 200 MB 的 TIFF：下载中另存 viewer-loading.png（先显示封面、下载进度）；中途连切两次文件，上一张的请求
      // 和 Worker 被取消。这一张本身拍的是切换两次之后的样子。
      // DevTools 看不到被 terminate 的 Worker 的请求结束，"请求被取消"另用一个开着 NetLog 的 Chrome 再跑一遍核对。
      name: 'viewer-switch',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const origin = new URL(page.url()).origin
        const track = trackOriginals(page, browserOriginalKey)
        const workers = trackWorkers(page)
        await switchScenario(page, origin, async () => {
          assert(workers.live === 1 && track.current().length === 1, `200 MB 的原图应该正在下载、有 1 个 Worker（${track.current().length} 张、${workers.live} 个）`)
          mkdirSync(OUT_DIR, { recursive: true })
          await page.screenshot({ path: OUT_LOADING })
          console.log(`  另存 ${OUT_LOADING}（下载中：先显示封面、下载进度）`)
        })
        await sleep(300)
        assert(workers.maxLive <= 1, `同时活着的 Worker 最多应该 1 个，实际 ${workers.maxLive}`)
        assert(workers.destroyed === 2 && workers.live === 1, `切换两次应该销毁 2 个 Worker（创建 ${workers.created}、销毁 ${workers.destroyed}、活着 ${workers.live}）`)
        assert(track.terminated.length === 2, `应该有 2 个下载随 Worker 一起结束（${track.terminated.length} 个）`)
        oneAtATime(track, '切换文件（浏览器）')
        console.log(`  断言 切换文件：Worker 创建 ${workers.created} 个、销毁 ${workers.destroyed} 个、同时最多 ${workers.maxLive} 个`)

        // NetLog：网络栈里这两个下载确实被取消了，最后一张下载完整。
        const exe = page.browser().process()?.spawnfile
        let ids = []
        const reqs = await withNetLog(
          exe,
          shotsTmp('m2-viewer-netlog'),
          async (p) => {
            await login(p, origin, admin, adminPassword)
            ids = await switchScenario(p, origin)
          },
          ':8333/',
        )
        const pick = (id) => reqs.filter((r) => r.url.includes(`/${id}/v`) && !r.url.includes('.cover.'))
        const [a, b, c] = ids.map(pick)
        assert(a.length === 1 && a[0].cancelled && a[0].bytes < 200e6, `200 MB 的下载应该被取消：${JSON.stringify(a)}`)
        assert(b.length === 1 && b[0].cancelled, `第二张（8.7 MB）的下载应该被取消：${JSON.stringify(b)}`)
        assert(c.length === 1 && !c[0].cancelled, `最后一张应该下载完整：${JSON.stringify(c)}`)
        const mb = (n) => (n >= 1048576 ? `${(n / 1048576).toFixed(1)} MB` : `${Math.round(n / 1024)} KB`)
        console.log(
          `  断言 切换文件（NetLog）：200 MB 那张收到 ${mb(a[0].bytes)} 时 CANCELLED，8.7 MB 那张收到 ${mb(b[0].bytes)} 时 CANCELLED，最后一张 ${mb(c[0].bytes)} 下载完整`,
        )
      },
      delay: 300,
    },
    {
      // 浏览器打开 200 MB TIFF：渲染进程的内存峰值和耗时（写进结果）。用独立的浏览器上下文（新的 renderer 进程），
      // 取这段时间里涨得最多的 renderer 进程（别的上下文的页面闲着）。
      name: 'viewer-tiff-200mb',
      path: '/login',
      waitFor: '#username',
      init: async () => {},
      async prepare(page) {
        await login(page, new URL(page.url()).origin, admin, adminPassword)
        const ctx = await ctxOf(page)
        const f = ctx.files[BIG200]
        await openFolder(page, ctx.origin, f.folder_id)
        const mem = sampleMemory(page.browser(), 50)
        const t0 = Date.now()
        await openViewer(page, BIG200)
        const d = await waitViewer(page, ['ready'], { timeout: 120000 })
        const ms = Date.now() - t0
        await settle(page)
        await sleep(2000)
        const peak = mem.stop()
        const detail = await page.evaluate(() => document.querySelector('[data-testid=viewer-statusbar]')?.textContent)
        console.log(
          `  实测 浏览器打开 200 MB TIFF（7016×9921 RGB 无压缩）：右键到画完 ${ms} ms（查看器记 ${d.mode}）；渲染进程 RSS 峰值 ${peak.renderer} MB（打开前 ${peak.rendererBase} MB，画完 2 秒后 ${peak.rendererEnd} MB），GPU 进程峰值 ${peak.gpu} MB；状态栏：${detail?.replace(/\s+/g, ' ')}`,
        )
      },
      delay: 300,
    },
    {
      // 浏览器里对比两张大 TIFF（M2-4 审查第 1 轮）：拦截对比样本的 snapshots，两个版本换成现场生成的 Deflate TIFF。
      // 1. 各 5000 万像素（解码峰值约 540 MB、解完常驻约 250 MB）：同时解放不下，后读到文件头的一侧先等（data-step=wait），
      //    另一侧解完后照常显示；
      // 2. 各 8000 万像素（峰值约 860 MB、常驻约 410 MB）：另一侧解完也放不下，这一侧不下载，只显示封面，提示改用客户端。
      name: 'viewer-compare-budget',
      user: admin,
      path: '/files',
      waitFor: '[data-testid=files-page]',
      async prepare(page) {
        const ctx = await ctxOf(page)
        const f = ctx.files[COMPARE_FILE]
        let sizes = [[6000, 8334], [6000, 8334]]
        const synth = await interceptCompareTiffs(page, ctx.api, f.file_id, () => sizes)
        await view(page, COMPARE_FILE)
        await waitViewer(page, ['ready'])
        await recordViewerSteps(page)

        // 1. 先等后显示。
        await click(page, 'viewer-compare')
        await page.waitForSelector('[data-testid=compare-viewer]')
        await waitViewer(page, ['ready'], { index: 0, timeout: 120000 })
        await waitViewer(page, ['ready'], { index: 1, timeout: 120000 })
        const steps = await page.evaluate(() => window.__viewerSteps)
        const waited = [...new Set(steps.filter((x) => x.step === 'wait').map((x) => x.side))]
        assert(waited.length === 1, `5000 万像素的两侧：应该有一侧等过另一侧（data-step=wait），实际 ${JSON.stringify(waited)}`)
        const modes = (await bothSides(page)).map((d) => d.mode)
        assert(modes.every((m) => m === 'worker'), `两侧都应该用 Worker 解 TIFF：${modes}`)
        console.log(`  断言 共用预算（5000 万像素 × 2）：${waited[0]} 侧先等另一侧解完，之后两侧都显示了原图`)
        await click(page, 'compare-close')
        await waitViewer(page, ['ready'])

        // 2. 放不下的一侧只显示封面。
        sizes = [[8000, 10000], [8000, 10000]]
        synth.requests.clear()
        await page.evaluate(() => (window.__viewerSteps.length = 0))
        await click(page, 'viewer-compare')
        await page.waitForSelector('[data-testid=compare-viewer]')
        await page.waitForFunction(
          () => {
            const st = [...document.querySelectorAll('[data-testid=compare-pane] [data-testid=image-viewer]')].map((e) => e.dataset.state)
            return st.length === 2 && st.includes('ready') && st.includes('too_large')
          },
          { timeout: 120000, polling: 100 },
        )
        const sides = await bothSides(page)
        const deniedIdx = sides.findIndex((d) => d.state === 'too_large')
        const denied = sides[deniedIdx]
        await page.waitForFunction(
          (i) => document.querySelectorAll('[data-testid=compare-pane] [data-testid=image-viewer]')[i]?.dataset.cover === 'loaded',
          { timeout: 10000 },
          deniedIdx,
        )
        const problem = await page.$$eval('[data-testid=compare-pane] [data-testid=viewer-problem]', (els) =>
          els.map((e) => ({ code: e.dataset.code, text: e.textContent.replace(/\s+/g, ' ').trim() })),
        )
        assert(problem.length === 1 && problem[0].code === 'over_budget', `放不下的一侧应该提示 over_budget：${JSON.stringify(problem)}`)
        assert(problem[0].text.includes('改用客户端') && problem[0].text.includes('下载原图'), `提示里应该有"下载原图""改用客户端"：${problem[0].text}`)
        assert(!synth.requests.has(denied.snapshotId), `放不下的一侧不应该下载原图（请求了 ${synth.requests.get(denied.snapshotId)} 次）`)
        console.log(`  断言 共用预算（8000 万像素 × 2）：${deniedIdx === 0 ? '左' : '右'}侧放不下，只显示封面，没有下载原图；提示：${problem[0].text}`)
      },
      delay: 500,
    },

    // ===== 壳（IPC 和 img 协议模拟） =====
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
        assert(d.state === 'too_large', `img 协议返回 422 too_large 时应该是 too_large，实际 ${d.state}`)
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
        const ctx = await ctxOf(page)
        await openFolder(page, ctx.origin, ctx.files[BIG200].folder_id)
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
      // 快速翻页（M2-4 审查第 1 轮）：连按 10 次"下一个"，每次间隔不到 250 毫秒，只有最后停下的那张请求了分块描述。
      await openFolder(page, ctx.origin, ctx.files[JPEG].folder_id)
      await openViewer(page, JPEG)
      const first = await waitViewer(page, ['ready'])
      await settle(page)
      const before = new Map(stats.dzi)
      const t0 = Date.now()
      for (let i = 0; i < 10; i++) {
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
      console.log(`  断言 壳 快速翻页：${flipMs} ms 内按了 10 次"下一个"，停在 ${title}，只有这一张请求了分块描述`)
    }),
    shellShot('shell-viewer-tile-retry', async (page, { ctx, stats }) => {
      // 取块失败（M2-4 审查第 1 轮）：G4 第 10 级以上的块都回 503，每块自动重试，仍失败的计数、提示"重试"；
      // 恢复后点"重试"，重新加载，不再缺块。提示那一刻另存 shell-viewer-tile-failed.png。
      const f = ctx.files[G4]
      let failing = true
      stats.tileFail = (sid, level) => (failing && level >= 10 ? 503 : 0)
      await openFolder(page, ctx.origin, f.folder_id)
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
