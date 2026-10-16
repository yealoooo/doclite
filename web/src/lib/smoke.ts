// 壳的 `--smoke` 自检里界面的那一段（只在壳以 --smoke 启动时运行，见 client/internal/shell/smoke.go：之前壳先测了
// 客户端自带的连接页、连不上页，之后壳接着测换地址）。
//
// smoke_config 的 stage：
// - main：完整的自检，用嵌入的界面和真实的传输层依次执行下面各步（壳没有的功能——smoke_config 的
//   capabilities——对应的步骤跳过，记为 ok 并在 detail.skipped 写原因）；交回结果后壳回 {next: 'edit_server'}，
//   界面像"关于"里的"修改服务器地址"那样跳到客户端的修改地址页；
// - page：换地址后进来的界面，只报告 GET /api/me 的状态码和界面版本，不挂载界面。
//
// main 的各步：
//   probe   探测服务器（health + shell/latest）
//   login   用环境变量里的账号登录（api_fetch，壳加 X-Client-Type: desktop）
//   me      GET /api/me，登录名对得上
//   menus   GET /api/me/menus
//   stream  用 api_stream（Wails 的流逐块推送）再取一次 /api/me/menus，结果和 menus 一致
//   sse     GET /api/projects 取第一个能进的项目，经 api_stream 订阅 /api/projects/{pid}/stream，
//           收到 server 的保活心跳（`:` 注释）就断开（M1-3）。server 每 15 秒发一次心跳，第一次在连上后约
//           15 秒，所以这一步最多等 25 秒。Windows 上它同时验证 UU 端口映射能不能传 SSE。
//           这一步失败时照样跑完后面各步（最后按 failed 结束）；426 和连不上除外，立即停止。
//   img     加载 img 协议的测试图
//   img_cache  本机图片缓存（M1-11）：用 batch 取 N 个文件（项目、文件夹、N 由环境变量指定，见 smoke.go），
//           按 imageSrc 加载封面（壳的 img 协议），每张 naturalWidth > 0；比较这一步前后的缓存计数：
//           cold（默认，先清空自检目录的缓存）要求全部未命中、每张下载一次；warm 要求全部命中、下载 0 次
//           （跑过一次 cold 之后再跑，可以先停掉 S3）。最后请求一张不在记录里的图，要求被拒绝（404）。
//           这一步失败时也照样跑完后面各步（最后按 failed 结束）。
//   img_direct  （壳还没有取图协议时，M6-1）看图退回到预签名 URL 直连存储：取第一个有文件的文件夹的前几个文件，
//           <img> 加载封面，再像浏览器的原图查看器一样用 fetch（CORS）下载一张原图，核对大小。没有文件时跳过
//   tiles   原图分块（M2-3）：找一张 CCITT G4 和一张 LZW 的 TIFF（文件名含 g4、lzw；文件夹由环境变量指定，
//           默认按层找），经 batch 记下来源后，用 XHR 取分块描述（和 OpenSeadragon 一样，验证 CSP 的 connect-src 和
//           CORS），核对格式（G4 是 png、LZW 是 jpg）、宽高和 batch 一致；再用 <img> 加载最高一级的第一块和最后一块、
//           第 8 级的 0_0，核对尺寸；最后请求一块超出范围的，要求被拒绝。cold 要求这次切的，warm 要求命中。
//           实测时可以用 DOCLITE_SMOKE_TILES_FILES 指定文件夹里的几个文件（例如 200 MB 的 TIFF），改为切这几个。
//           失败时也照样跑完后面各步。
//   viewer  原图查看器（M2-4）：在页面角落的隐藏容器（800×600，visibility:hidden）里挂一个真正的 ImageViewer
//           （OpenSeadragon），看 tiles 一步用过的那张 G4（指定了 DOCLITE_SMOKE_TILES_FILES 时用第一个），图片信息照常经
//           request() 调 batch 取。等它 ready，要求：mode 是 tiles；请求过的块数 > 0 且少于描述里的总块数；OpenSeadragon
//           画过块（tile-drawn）；这一步没有新的 CSP 违规。另外：再挂一次不带封面的（封面经 img 协议加载、没有 CORS 头，
//           画过它的画布读不出像素），读画布的像素，要求有深色的点（块真的画到了画布上）；用 fetch 取描述和一块超出范围的块，
//           记下 X-Doclite-Cache、X-Doclite-Error 读不读得到（壳的 Access-Control-Expose-Headers）。
//           这一步要求壳的窗口看得见：窗口被别的窗口完全挡住、最小化或在别的桌面时，WebKit 暂停 requestAnimationFrame
//           （OpenSeadragon 靠它取块、画块）并大幅放慢定时器，这一步会一直等下去。所以先看 visibilityState，不是 visible
//           就等 visibilitychange（最多约 20 秒），还看不见就直接失败并说明原因（把 Doclite 的窗口放到最前面再跑）。
//           失败时也照样跑完后面各步。
//   tray    托盘（M1-13）：已创建、关窗会缩到托盘，提示文字里有排队、上传中、已入库、重复、失败的数量，
//           并且和 upload_status 的统计一致（自检用 smoke/ 下的队列，一般都是 0）；壳启动后模拟过一次关窗
//           （close_check），窗口只是隐藏、壳没有退出。失败时照样跑完后面各步。
//   ui      挂载真正的界面，等业务布局（侧栏导航）出现
//   pages   （DOCLITE_SMOKE_PAGES=1 才跑）逐个打开账号菜单里的页面：路由没被改走（无权、登录页），1.5 秒内
//           没有出错提示（toast）和未处理的异常。壳还不支持的功能（"还不支持"）、server 还没有的接口（"接口不存在"，
//           服务端并行开发时会有）的提示单独记下，不算失败
//   nav_guard  导航保护：让主页面跳到服务器的网页，要求被壳拦下（页面还在 wails 的地址上）。壳没装上导航保护时跳过
//   runtime_guard  桥的白名单（M7-6）：页面自己发几种不在白名单里的 /wails/runtime 调用（带查询串的 GET、text/plain、
//           System.Environment、别的绑定方法），都要 403。失败时照样跑完后面各步
//   scan_page  打开 /scan（M1-13）：经真实的壳命令和事件读出上传队列、热文件夹和缓存摘要。账号没有"扫描上传"
//           菜单时跳过（detail 里 skipped）。失败时照样跑完后面各步。
//   rescan  重扫件（M2-7，lib/smoke-rescan.ts）：设置了 DOCLITE_SMOKE_RESCAN_PATH 才跑，否则跳过。经真实的
//           pick_and_rescan（自检里用那个本机文件，不弹对话框）重扫一页已驳回的文件，用接口核对新版本、封面、
//           自动再分配和 rescan 日志。失败时照样跑完后面各步。
//   flow_upload、flow_hotfolder、flow_review、flow_catalog、flow_export  主流程（M6-4，lib/smoke-flow.ts）：设置了
//           DOCLITE_SMOKE_FLOW_SAMPLES 才跑，否则跳过。壳里上传、热文件夹（都不弹系统对话框）→ 分给自己、在查看器里看原图、
//           逐页通过 → 新建条目挂上、定稿 → 导出、经 open_external 拿到下载地址（自检里不打开系统浏览器），壳用 HTTP
//           下载核对 zip。每一步失败时照样跑完后面各步。
//   logout  退出登录，之后 GET /api/me 返回 401
//   direct_blocked  页面自己直连服务器会被 CSP 挡住
//   csp     整个过程中没有别的 CSP 违规
// 结果交给壳（smoke_report），由壳写到标准输出和日志后退出。
// outcome：ok；unreachable（连不上）；upgrade_required（版本过低或 426）；failed（其他）。

import { createApp } from 'vue'

import { listChildren } from '@/api/folders'
import { ApiError, errorMessage, failResponse, request } from '@/api/http'
import type { FileBatchResponse, FileEntry, Me, MyMenu, MyProject } from '@/api/types'
import { cspViolations } from '@/lib/csp'
import { cacheClear, cacheStats } from '@/lib/image-cache'
import {
  imageSrc,
  shellTestImageSrc,
  tileLevelSize,
  tileSourceSrc,
  type TileSourceDescriptor,
} from '@/lib/image-url'
import { trayStatus, uploadStatus, type QueueStatus } from '@/lib/scan-queue'
import type { ViewerStatus } from '@/components/viewer/ImageViewer.vue'
import type { ViewerImageInfo } from '@/lib/viewer/load-original'
// M2-7：重扫一步。
import { checkRescan } from '@/lib/smoke-rescan'
// M6-4：主流程几步。
import {
  checkFlowCatalog,
  checkFlowExport,
  checkFlowHotfolder,
  checkFlowReview,
  checkFlowUpload,
  newFlowState,
} from '@/lib/smoke-flow'
import router from '@/router'
import { useSessionStore } from '@/stores/session'
import {
  checkServer,
  editServerAddress,
  shellErrorMessage,
  shellStream,
  shellStreamCancel,
  smokeConfig,
  smokeReport,
  type ShellStreamEvent,
  type SmokeConfig,
} from '@/lib/shell'

type Outcome = 'ok' | 'failed' | 'unreachable' | 'upgrade_required'

interface Step {
  name: string
  ok: boolean
  ms: number
  detail?: unknown
  error?: string
}

/** 提前结束自检，并指定 outcome。 */
class SmokeStop extends Error {
  readonly outcome: Outcome
  constructor(outcome: Outcome, message: string) {
    super(message)
    this.outcome = outcome
  }
}

const STEP_TIMEOUT_MS = 15_000
/** sse 一步等心跳的时限：server 的保活间隔 15 秒（server `stream.rs` 的 KEEP_ALIVE_INTERVAL），再留 10 秒余量。 */
const HEARTBEAT_TIMEOUT_MS = 25_000
/** img_cache 一步的时限（封面要经 UU 端口映射从 S3 下载）。 */
const IMG_CACHE_TIMEOUT_MS = 45_000
/** img_cache 没指定文件夹时，最多看多少个文件夹找有文件的。 */
const MAX_FOLDER_SCAN = 30
/** tiles 一步的时限：cold 时要下载原图再切块（debug 壳的切块代码没有优化，A4 300 DPI 也要几秒）。 */
const TILES_TIMEOUT_MS = 120_000
/** viewer 一步的时限（两次挂载查看器；分块 tiles 一步已经切好）。 */
const VIEWER_TIMEOUT_MS = 60_000
/** 等一个查看器 ready 的时限。 */
const VIEWER_READY_MS = 25_000
/** rescan 一步的时限（M2-7）：算 sha、生成封面（debug 壳也优化了图像依赖）、上传一页。 */
const RESCAN_TIMEOUT_MS = 60_000
/** 主流程各步的时限（上传等入库、导出等执行完，见 lib/smoke-flow.ts）。 */
const FLOW_STEP_TIMEOUT_MS = 120_000
const FLOW_EXPORT_TIMEOUT_MS = 240_000
/** pages 一步：每个页面打开后等多久再看有没有出错提示；整步的时限。 */
const PAGE_SETTLE_MS = 1500
const PAGES_TIMEOUT_MS = 120_000
/** nav_guard 一步：让主页面跳走以后等多久（跳走了的话这段时间里页面就没了）。 */
const NAV_GUARD_WAIT_MS = 3000

function withTimeout<T>(p: Promise<T>, what: string, ms = STEP_TIMEOUT_MS): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`${what}超时（${ms / 1000} 秒）`)), ms)
    p.then(
      (v) => {
        clearTimeout(timer)
        resolve(v)
      },
      (e: unknown) => {
        clearTimeout(timer)
        reject(e)
      },
    )
  })
}

function outcomeOf(e: unknown): Outcome {
  if (e instanceof SmokeStop) return e.outcome
  if (e instanceof ApiError) {
    if (e.status === 426) return 'upgrade_required'
    if (e.status === 0 && e.code === 'network') return 'unreachable'
  }
  return 'failed'
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message}（HTTP ${e.status} ${e.code}）`
  if (e instanceof Error) return e.message
  return shellErrorMessage(e)
}

function status(text: string) {
  const el = document.getElementById('smoke-status')
  if (el) el.textContent = text
}

/** 用 api_stream 取一个普通接口的完整响应（验证流能把数据逐块送到页面）。 */
function streamText(path: string): Promise<{ status: number; body: string; chunks: number }> {
  return new Promise((resolve, reject) => {
    let code = 0
    let body = ''
    let chunks = 0
    const onEvent = (m: ShellStreamEvent) => {
      if (m.event === 'open') code = m.status
      else if (m.event === 'chunk') {
        chunks++
        body += m.data
      } else if (m.event === 'end') resolve({ status: code, body, chunks })
      else reject(new Error(`流式请求失败：${m.message}`))
    }
    shellStream({ method: 'GET', path, query: [], headers: { Accept: 'application/json' } }, onEvent).catch(
      (e: unknown) => reject(new Error(shellErrorMessage(e))),
    )
  })
}

interface Heartbeat {
  status: number
  content_type: string
  /** 从发起订阅到收到响应头（毫秒）。 */
  open_ms: number
  /** 从发起订阅到收到第一条心跳（毫秒），正常约 15000。 */
  heartbeat_ms: number
  chunks: number
}

/**
 * 经 api_stream 订阅一个 SSE 接口，等到第一条保活心跳（以 `:` 开头的注释行）就断开。
 * 非 2xx（按错误体转成 ApiError，426 会让自检以 upgrade_required 结束）、服务器提前结束流、连接出错、
 * 超过 HEARTBEAT_TIMEOUT_MS 都算失败。不管结果如何都取消这个流，壳随即断开连接。
 */
function waitHeartbeat(path: string): Promise<Heartbeat> {
  return new Promise<Heartbeat>((resolve, reject) => {
    const started = performance.now()
    const elapsed = () => Math.round(performance.now() - started)
    let status = 0
    let contentType = ''
    let openMs = 0
    let chunks = 0
    let text = ''
    let streamId: number | null = null
    let settled = false

    const finish = (err: Error | null, value?: Heartbeat) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      if (streamId !== null) void shellStreamCancel(streamId).catch(() => {})
      if (err || !value) reject(err ?? new Error('没有结果'))
      else resolve(value)
    }
    const timer = setTimeout(() => {
      const got = status ? `HTTP ${status}，已收到 ${chunks} 块数据` : '还没收到响应头'
      finish(new Error(`${HEARTBEAT_TIMEOUT_MS / 1000} 秒内没有收到心跳（${got}）`))
    }, HEARTBEAT_TIMEOUT_MS)

    const onEvent = (m: ShellStreamEvent) => {
      if (settled) return
      switch (m.event) {
        case 'open':
          status = m.status
          contentType = m.headers['content-type'] ?? ''
          openMs = elapsed()
          if (status === 200 && !contentType.startsWith('text/event-stream')) {
            finish(new Error(`响应的 Content-Type 是 ${contentType || '（空）'}，不是 text/event-stream`))
          }
          break
        case 'chunk':
          chunks++
          text += m.data
          // 心跳是 SSE 注释：一行以 ":" 开头（axum 的 KeepAlive 发 ":\n\n"）。事件是 "data: …" 行，不会误判。
          if (status === 200 && /(^|[\r\n]):/.test(text)) {
            finish(null, { status, content_type: contentType, open_ms: openMs, heartbeat_ms: elapsed(), chunks })
          }
          break
        case 'end':
          if (status >= 200 && status < 300) finish(new Error('服务器提前结束了流，没等到心跳'))
          else finish(failResponse(path, status, text, { handle: true }))
          break
        case 'error':
          finish(new Error(`流式请求失败：${m.message}`))
          break
      }
    }

    shellStream({ method: 'GET', path, query: [], headers: { Accept: 'text/event-stream' } }, onEvent)
      .then((id) => {
        streamId = id
        // invoke 返回之前就已经有结果了：现在才知道 id，补一次取消。
        if (settled) void shellStreamCancel(id).catch(() => {})
      })
      .catch((e: unknown) => finish(new Error(shellErrorMessage(e))))
  })
}

function loadImage(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.alt = 'img 协议测试图'
    img.onload = () => resolve(img)
    img.onerror = () => reject(new Error(`测试图加载失败：${src}`))
    img.src = src
    document.getElementById('smoke')?.appendChild(img)
  })
}

/** 加载一张封面（小图放进自检的角落），返回 naturalWidth。 */
function loadCover(src: string): Promise<number> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.alt = '封面'
    img.style.cssText = 'width:24px;height:34px;object-fit:contain;margin:1px;border:1px solid #eee'
    img.onload = () => resolve(img.naturalWidth)
    img.onerror = () => reject(new Error(`封面加载失败：${src}`))
    img.src = src
    document.getElementById('smoke')?.appendChild(img)
  })
}

/** 这张图应该加载失败（img 协议返回 404）。 */
function expectImageRejected(src: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => reject(new Error(`不在记录里的图片也加载出来了：${src}`))
    img.onerror = () => resolve()
    img.src = src
  })
}

/** 找要加载封面的文件：指定了文件夹就用它，否则从根起按层找第一个有文件的文件夹。返回前 count 个 file_id。 */
async function findFiles(
  rootId: string,
  folder: string | null,
  count: number,
): Promise<{ path: string; fileIds: string[] }> {
  if (folder) {
    let id = rootId
    for (const name of folder.split('/').filter(Boolean)) {
      const children = await listChildren(id, true)
      const sub = children.folders.find((f) => f.folder_name === name)
      if (!sub) throw new Error(`找不到文件夹 ${folder}（没有「${name}」这一层）`)
      id = sub.id
    }
    const children = await listChildren(id, true)
    if (children.files.length === 0) throw new Error(`文件夹 ${folder} 里没有文件（先用 seed:files 传几张图）`)
    return { path: folder, fileIds: children.files.slice(0, count).map((f) => f.file_id) }
  }
  const queue = [{ id: rootId, path: '' }]
  for (let visited = 0; queue.length > 0 && visited < MAX_FOLDER_SCAN; visited++) {
    const { id, path } = queue.shift()!
    const children = await listChildren(id, true)
    if (children.files.length > 0) {
      return { path, fileIds: children.files.slice(0, count).map((f) => f.file_id) }
    }
    for (const f of children.folders) {
      if (f.file_count > 0 || f.folder_count > 0) {
        queue.push({ id: f.id, path: path ? `${path}/${f.folder_name}` : f.folder_name })
      }
    }
  }
  throw new Error('项目里没找到有文件的文件夹（先用 seed:files 传几张图，或用 DOCLITE_SMOKE_IMG_FOLDER 指定）')
}

/** img_cache 一步（M1-11，见文件开头）。 */
async function checkImageCache(opts: SmokeConfig['image_cache']) {
  if (opts.mode === 'cold') {
    await cacheClear(null)
    const cleared = await cacheStats()
    if (cleared.file_count !== 0) throw new Error(`清空后缓存里还有 ${cleared.file_count} 个文件`)
  }
  const projects = await request<MyProject[]>('/api/projects', { handle: true })
  const project = opts.project
    ? projects.find((p) => p.project_code === opts.project)
    : (projects.find((p) => p.status === 'active') ?? projects[0])
  if (!project) {
    throw new Error(opts.project ? `这个账号进不了项目 ${opts.project}` : '这个账号没有能进的项目')
  }
  const found = await findFiles(project.root_folder_id, opts.folder, opts.count)
  const batch = await request<FileBatchResponse>('/api/files/batch', {
    method: 'POST',
    body: { file_ids: found.fileIds },
    handle: true,
  })
  const items = batch.items.filter((i) => i.cover_url)
  if (items.length === 0) {
    throw new Error(`文件夹 ${found.path || '（根）'} 的前 ${found.fileIds.length} 个文件都没有封面`)
  }
  const srcs = items.map((i) => imageSrc(i, 'cover')!)

  const before = (await cacheStats()).counters
  const started = performance.now()
  const widths = await Promise.all(srcs.map(loadCover))
  const ms = Math.round(performance.now() - started)
  const after = await cacheStats()
  const zero = widths.filter((w) => !(w > 0)).length
  if (zero > 0) throw new Error(`${zero} 张封面的 naturalWidth 是 0`)

  const n = items.length
  const delta = {
    hits: after.counters.hits - before.hits,
    misses: after.counters.misses - before.misses,
    downloads: after.counters.downloads - before.downloads,
    download_bytes: after.counters.download_bytes - before.download_bytes,
  }
  const counts = `命中 ${delta.hits}、未命中 ${delta.misses}、下载 ${delta.downloads}`
  if (opts.mode === 'cold' && !(delta.misses === n && delta.downloads === n && delta.hits === 0)) {
    throw new Error(`cold 要求 ${n} 张封面全部未命中、各下载一次，实际${counts}`)
  }
  if (opts.mode === 'warm' && !(delta.hits === n && delta.downloads === 0 && delta.misses === 0)) {
    throw new Error(`warm 要求 ${n} 张封面全部命中、不下载，实际${counts}（先跑一次 cold）`)
  }

  // 不在记录里的图（页面编造的 id）：壳一律拒绝，不去下载。
  const bogus = imageSrc(
    {
      file_id: '00000000-0000-7000-8000-000000000000',
      snapshot_id: '00000000-0000-7000-8000-000000000001',
      cover_url: 'http://example.invalid/x.jpg',
      original_url: null,
    },
    'cover',
  )!
  await expectImageRejected(bogus)
  const end = await cacheStats()
  if (end.counters.rejected !== after.counters.rejected + 1) {
    throw new Error('不在记录里的图片没有被壳拒绝')
  }

  return {
    mode: opts.mode,
    project_code: project.project_code,
    folder: found.path || '/',
    files: found.fileIds.length,
    covers: n,
    ms,
    ...delta,
    unrecorded_rejected: true,
    src: srcs[0],
    cache_dir: end.dir,
    cache_files: end.file_count,
    cache_bytes: end.total_bytes,
  }
}

/** 找要切块的两张 TIFF：指定了文件夹就在里面找，否则从根起按层找第一个同时有 g4、lzw 的文件夹。 */
async function findTiffs(
  rootId: string,
  folder: string | null,
): Promise<{ path: string; g4: FileEntry; lzw: FileEntry }> {
  const pick = (files: FileEntry[]) => {
    const tiffs = files.filter((f) => /\.tiff?$/i.test(f.file_name))
    const g4 = tiffs.find((f) => /g4/i.test(f.file_name))
    const lzw = tiffs.find((f) => /lzw/i.test(f.file_name))
    return g4 && lzw ? { g4, lzw } : null
  }
  if (folder) {
    let id = rootId
    for (const name of folder.split('/').filter(Boolean)) {
      const children = await listChildren(id, true)
      const sub = children.folders.find((f) => f.folder_name === name)
      if (!sub) throw new Error(`找不到文件夹 ${folder}（没有「${name}」这一层）`)
      id = sub.id
    }
    const found = pick((await listChildren(id, true)).files)
    if (!found) throw new Error(`文件夹 ${folder} 里没有文件名含 g4、lzw 的 TIFF（用 gen-samples 生成后上传）`)
    return { path: folder, ...found }
  }
  const queue = [{ id: rootId, path: '' }]
  for (let visited = 0; queue.length > 0 && visited < MAX_FOLDER_SCAN; visited++) {
    const { id, path } = queue.shift()!
    const children = await listChildren(id, true)
    const found = pick(children.files)
    if (found) return { path, ...found }
    for (const f of children.folders) {
      if (f.file_count > 0 || f.folder_count > 0) {
        queue.push({ id: f.id, path: path ? `${path}/${f.folder_name}` : f.folder_name })
      }
    }
  }
  throw new Error('项目里没找到同时有 g4、lzw 的 TIFF 的文件夹（用 DOCLITE_SMOKE_TILES_FOLDER 指定）')
}

/** 文件夹（从项目根起的路径）里按名字找几个文件。 */
async function findNamed(rootId: string, folder: string, names: string[]): Promise<FileEntry[]> {
  let id = rootId
  for (const name of folder.split('/').filter(Boolean)) {
    const children = await listChildren(id, true)
    const sub = children.folders.find((f) => f.folder_name === name)
    if (!sub) throw new Error(`找不到文件夹 ${folder}（没有「${name}」这一层）`)
    id = sub.id
  }
  const files = (await listChildren(id, true)).files
  return names.map((n) => {
    const f = files.find((x) => x.file_name === n)
    if (!f) throw new Error(`文件夹 ${folder} 里没有 ${n}`)
    return f
  })
}

/** 用 XHR 取分块描述（OpenSeadragon 也是 XHR）。 */
function xhrJson(src: string): Promise<{ status: number; body: unknown; cache: string | null; error: string | null }> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('GET', src)
    xhr.onload = () => {
      let body: unknown = null
      try {
        body = JSON.parse(xhr.responseText)
      } catch {
        body = xhr.responseText
      }
      resolve({
        status: xhr.status,
        body,
        cache: xhr.getResponseHeader('x-doclite-cache'),
        error: xhr.getResponseHeader('x-doclite-error'),
      })
    }
    xhr.onerror = () => reject(new Error(`XHR 取分块描述失败（CSP 或 CORS？）：${src}`))
    xhr.send()
  })
}

/** 加载一块，返回宽高（加载失败 reject）。 */
function loadTile(src: string): Promise<{ width: number; height: number }> {
  return new Promise((resolve, reject) => {
    const img = new Image()
    img.onload = () => resolve({ width: img.naturalWidth, height: img.naturalHeight })
    img.onerror = () => reject(new Error(`块加载失败：${src}`))
    img.src = src
  })
}

/** 块 (x, y) 的尺寸：[x×T − O, (x+1)×T + O) 裁到这一级的范围。 */
function tileSize(d: TileSourceDescriptor, level: number, x: number, y: number) {
  const { width, height } = tileLevelSize(d, level)
  const span = (i: number, len: number) =>
    Math.min((i + 1) * d.tile_size + d.overlap, len) - Math.max(i * d.tile_size - d.overlap, 0)
  return { width: span(x, width), height: span(y, height) }
}

/** tiles 一步用过的 G4（viewer 一步接着用它；指定了文件时是第一个）。 */
let viewerTarget: FileEntry | null = null

/** tiles 一步（M2-3，见文件开头）。 */
async function checkTiles(cfg: SmokeConfig) {
  const projects = await request<MyProject[]>('/api/projects', { handle: true })
  const code = cfg.tiles.project ?? cfg.image_cache.project
  const project = code
    ? projects.find((p) => p.project_code === code)
    : (projects.find((p) => p.status === 'active') ?? projects[0])
  if (!project) throw new Error(code ? `这个账号进不了项目 ${code}` : '这个账号没有能进的项目')
  let path: string
  let targets: { kind: string; entry: FileEntry; format: 'png' | 'jpg' | null }[]
  if (cfg.tiles.files.length > 0) {
    // 实测：指定的几个文件（文件夹必须给了，见 smoke.go）。
    const named = await findNamed(project.root_folder_id, cfg.tiles.folder ?? '', cfg.tiles.files)
    path = cfg.tiles.folder ?? ''
    targets = named.map((entry) => ({ kind: 'file', entry, format: null }))
    viewerTarget = named[0] ?? null
  } else {
    const found = await findTiffs(project.root_folder_id, cfg.tiles.folder)
    path = found.path
    targets = [
      { kind: 'g4', entry: found.g4, format: 'png' },
      { kind: 'lzw', entry: found.lzw, format: 'jpg' },
    ]
    viewerTarget = found.g4
  }
  const batch = await request<FileBatchResponse>('/api/files/batch', {
    method: 'POST',
    body: { file_ids: targets.map((t) => t.entry.file_id) },
    handle: true,
  })
  const results = []
  for (const { kind, entry, format } of targets) {
    const item = batch.items.find((i) => i.file_id === entry.file_id)
    if (!item) throw new Error(`batch 里没有 ${entry.file_name}`)
    const src = tileSourceSrc(item)
    if (!src) throw new Error(`${entry.file_name} 没有原图地址`)
    const started = performance.now()
    const res = await xhrJson(src)
    const ms = Math.round(performance.now() - started)
    if (res.status !== 200) {
      throw new Error(`${entry.file_name} 的分块描述返回 HTTP ${res.status}（${res.error ?? ''}）：${JSON.stringify(res.body)}`)
    }
    const d = res.body as TileSourceDescriptor
    if (d.Image?.xmlns !== 'http://schemas.microsoft.com/deepzoom/2008') throw new Error('描述不是 DZI 的写法')
    if (format && d.Image.Format !== format) {
      throw new Error(`${entry.file_name} 的块是 ${d.Image.Format}，应为 ${format}`)
    }
    if (Number(d.Image.Size.Width) !== item.width_px || Number(d.Image.Size.Height) !== item.height_px) {
      throw new Error(
        `描述的宽高 ${d.Image.Size.Width}×${d.Image.Size.Height} 和 batch 的 ${item.width_px}×${item.height_px} 不一致`,
      )
    }
    if (cfg.image_cache.mode === 'cold' && d.hit) throw new Error(`cold 要求 ${entry.file_name} 是这次切的，实际命中`)
    if (cfg.image_cache.mode === 'warm' && !d.hit) throw new Error(`warm 要求 ${entry.file_name} 命中，实际这次才切`)
    const max = d.max_level
    const { width, height } = tileLevelSize(d, max)
    const cols = Math.ceil(width / d.tile_size)
    const rows = Math.ceil(height / d.tile_size)
    const tiles: { level: number; x: number; y: number }[] = [
      { level: max, x: 0, y: 0 },
      { level: max, x: cols - 1, y: rows - 1 },
      { level: Math.min(8, max), x: 0, y: 0 },
    ]
    const tileStarted = performance.now()
    for (const t of tiles) {
      const url = `${d.Image.Url}${t.level}/${t.x}_${t.y}.${d.Image.Format}`
      const got = await loadTile(url)
      const want = tileSize(d, t.level, t.x, t.y)
      if (got.width !== want.width || got.height !== want.height) {
        throw new Error(`块 ${t.level}/${t.x}_${t.y} 是 ${got.width}×${got.height}，应为 ${want.width}×${want.height}`)
      }
    }
    const tileMs = Math.round(performance.now() - tileStarted)
    // 超出范围的块：壳拒绝（404），<img> 加载失败。
    await expectImageRejected(`${d.Image.Url}${max}/${cols}_0.${d.Image.Format}`)
    results.push({
      kind,
      file: entry.file_name,
      size: `${d.width}×${d.height}`,
      mode: d.mode,
      format: d.format,
      levels: d.levels,
      tile_count: d.tile_count,
      tiles_bytes: d.tiles_bytes,
      streamed: d.streamed,
      hit: d.hit,
      cache_header: res.cache,
      descriptor_ms: ms,
      generate_ms: d.elapsed_ms,
      tiles_loaded: tiles.length,
      tiles_ms: tileMs,
      tile_url: `${d.Image.Url}${max}/0_0.${d.Image.Format}`,
    })
  }
  const stats = await cacheStats()
  return {
    project_code: project.project_code,
    folder: path || '/',
    files: results,
    tilings: stats.counters.tilings,
    tiling_failures: stats.counters.tiling_failures,
  }
}

// ---------- viewer（M2-4） ----------

interface MountedViewer {
  status: ViewerStatus
  /** OpenSeadragon 画原图的块的次数（tile-drawn）。 */
  drawn: number
  canvas: HTMLCanvasElement | null
  ms: number
  unmount: () => void
}

/** 在隐藏容器里挂一个 ImageViewer，等它 ready（失败、超时都 reject，并卸掉）。 */
async function mountViewer(fileId: string, info: ViewerImageInfo): Promise<MountedViewer> {
  const { default: ImageViewer } = await import('@/components/viewer/ImageViewer.vue')
  const host = document.createElement('div')
  host.style.cssText = 'position:fixed;left:0;top:0;width:800px;height:600px;visibility:hidden;pointer-events:none;z-index:-1'
  document.body.appendChild(host)
  let last: ViewerStatus | null = null
  let settle: { resolve: (s: ViewerStatus) => void; reject: (e: Error) => void } | null = null
  const done = new Promise<ViewerStatus>((resolve, reject) => {
    settle = { resolve, reject }
  })
  const app = createApp(ImageViewer, {
    class: 'size-full',
    fileId,
    info,
    overlays: false,
    onStatus: (st: ViewerStatus) => {
      last = st
      if (st.phase === 'ready') settle?.resolve(st)
      else if (st.phase !== 'loading' && st.phase !== 'empty') settle?.reject(new Error(`查看器 ${st.phase}：${st.code ?? ''} ${st.message ?? ''}`))
    },
  })
  const unmount = () => {
    app.unmount()
    host.remove()
  }
  const started = performance.now()
  const vm = app.mount(host) as unknown as { viewer: () => import('@/lib/viewer/osd').OsdViewer | null }
  let drawn = 0
  vm.viewer()?.addHandler('tile-drawn', (e: { tile?: { getUrl?: () => string } }) => {
    // 只数原图的块（壳的块地址是 /shell/img/tile/…；封面那一层是 /shell/img/cover/…）。
    if (String(e.tile?.getUrl?.() ?? '').includes('/tile/')) drawn++
  })
  try {
    const st = await withTimeout(done, '等查看器画完原图', VIEWER_READY_MS).catch((e: unknown) => {
      const at = last ? `（停在 ${last.phase}/${last.step ?? '-'}，封面 ${last.cover}，请求过 ${last.tileRequests} 块）` : ''
      throw new Error(`${e instanceof Error ? e.message : String(e)}${at}`)
    })
    // 再等两帧，让 OpenSeadragon 把最后一批块画上去。
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))
    return { status: st, drawn, canvas: host.querySelector('.openseadragon-canvas canvas'), ms: Math.round(performance.now() - started), unmount }
  } catch (e) {
    unmount()
    throw e
  }
}

/** 窗口看得见（visibilityState 为 visible）；看不见时等 visibilitychange，最多约 `ms` 毫秒（看不见时定时器会被放慢，只是兜底）。 */
function waitVisible(ms: number): Promise<boolean> {
  if (document.visibilityState === 'visible') return Promise.resolve(true)
  return new Promise((resolve) => {
    const done = (v: boolean) => {
      document.removeEventListener('visibilitychange', onChange)
      clearTimeout(timer)
      resolve(v)
    }
    const onChange = () => {
      if (document.visibilityState === 'visible') done(true)
    }
    document.addEventListener('visibilitychange', onChange)
    const timer = setTimeout(() => done(false), ms)
  })
}

/**
 * 页面现在能不能画：可见性、有没有焦点，`ms` 毫秒（墙上时间）里 requestAnimationFrame 回调了几次、定时器晚了多久。
 * WebKit 在窗口看不见（被别的窗口完全挡住、在别的桌面）时暂停 rAF、放慢定时器，OpenSeadragon 就不取块、不画。
 */
function frameProbe(ms = 500): Promise<{ visibility: string; focus: boolean; frames: number; timer_late_ms: number }> {
  return new Promise((resolve) => {
    let frames = 0
    let running = true
    const loop = () => {
      if (!running) return
      frames++
      requestAnimationFrame(loop)
    }
    requestAnimationFrame(loop)
    const started = performance.now()
    setTimeout(() => {
      running = false
      resolve({
        visibility: document.visibilityState,
        focus: document.hasFocus(),
        frames,
        timer_late_ms: Math.round(performance.now() - started - ms),
      })
    }, ms)
  })
}

/** 画布上不透明的点、深色的点（亮度 < 128）的个数。 */
function canvasInk(canvas: HTMLCanvasElement): { width: number; height: number; opaque: number; dark: number } {
  const ctx = canvas.getContext('2d')
  if (!ctx) throw new Error('取不到画布的 2D 上下文')
  const { data } = ctx.getImageData(0, 0, canvas.width, canvas.height)
  let opaque = 0
  let dark = 0
  for (let i = 0; i < data.length; i += 4) {
    if (data[i + 3]! === 0) continue
    opaque++
    if (data[i]! * 0.299 + data[i + 1]! * 0.587 + data[i + 2]! * 0.114 < 128) dark++
  }
  return { width: canvas.width, height: canvas.height, opaque, dark }
}

/** viewer 一步（M2-4，见文件开头）。 */
async function checkViewer() {
  const entry = viewerTarget
  if (!entry) throw new Error('tiles 一步没有找到要看的 TIFF（这一步用它）')
  const batch = await request<FileBatchResponse>('/api/files/batch', {
    method: 'POST',
    body: { file_ids: [entry.file_id] },
    handle: true,
  })
  const item = batch.items.find((i) => i.file_id === entry.file_id)
  if (!item) throw new Error(`batch 里没有 ${entry.file_name}`)
  const cspBefore = cspViolations().length
  if (!(await waitVisible(20_000))) {
    throw new Error(
      `壳的窗口看不见（visibilityState=${document.visibilityState}：被别的窗口完全挡住、最小化或在别的桌面），WebKit 暂停了 requestAnimationFrame，OpenSeadragon 不会取块、画块；把 Doclite 的窗口放到最前面再跑一次`,
    )
  }
  const probe = await frameProbe()

  // 1. 和页面上一样：先显示封面，原图（分块）就绪后换上。
  const a = await mountViewer(entry.file_id, item).catch((e: unknown) => {
    throw new Error(`${e instanceof Error ? e.message : String(e)}；页面：${JSON.stringify(probe)}`)
  })
  const st = a.status
  const total = Number(st.detail?.tile_count ?? 0)
  const drawn = a.drawn
  a.unmount()
  if (st.mode !== 'tiles') throw new Error(`查看器用的是 ${st.mode} 模式，应为 tiles`)
  if (!(st.tileRequests > 0 && st.tileRequests < total)) {
    throw new Error(`请求过 ${st.tileRequests} 块，应大于 0 且少于总块数 ${total}`)
  }
  if (drawn === 0) throw new Error('OpenSeadragon 没有画过原图的块')
  if (st.tileFailures > 0) throw new Error(`有 ${st.tileFailures} 块没取到`)

  // 2. 不带封面再挂一次，读画布的像素（块真的画上去了）。
  const b = await mountViewer(entry.file_id, { ...item, cover_url: null })
  let ink: ReturnType<typeof canvasInk>
  try {
    if (!b.canvas) throw new Error('找不到 OpenSeadragon 的画布')
    ink = canvasInk(b.canvas)
  } finally {
    b.unmount()
  }
  if (ink.opaque === 0 || ink.dark === 0) throw new Error(`画布上没有画出原图：${JSON.stringify(ink)}`)

  // 3. 描述、超出范围的块的响应头（fetch，和 OpenSeadragon 取块一样）。
  const src = tileSourceSrc(item)!
  const dres = await fetch(src, { cache: 'no-store' })
  const d = (await dres.json()) as TileSourceDescriptor
  const { width } = tileLevelSize(d, d.max_level)
  const cols = Math.ceil(width / d.tile_size)
  const bad = await fetch(`${d.Image.Url}${d.max_level}/${cols}_0.${d.Image.Format}`)

  const violations = cspViolations().slice(cspBefore)
  if (violations.length > 0) throw new Error(`有 ${violations.length} 条 CSP 违规：${JSON.stringify(violations)}`)
  return {
    file: entry.file_name,
    file_id: entry.file_id,
    mode: st.mode,
    cover: st.cover,
    tile_requests: st.tileRequests,
    tile_count: total,
    tile_failures: st.tileFailures,
    tiles_drawn: drawn,
    levels: st.detail?.levels,
    format: st.detail?.format,
    hit: st.detail?.hit,
    ready_ms: a.ms,
    elapsed_ms: st.elapsedMs,
    no_cover_ready_ms: b.ms,
    no_cover_requests: b.status.tileRequests,
    canvas: ink,
    descriptor_cache_header: dres.headers.get('x-doclite-cache'),
    out_of_range_tile: { status: bad.status, error_header: bad.headers.get('x-doclite-error') },
    page: probe,
    csp_violations: violations.length,
  }
}

/** tray 一步（M1-13，见文件开头）。 */
async function checkTray() {
  /** 托盘提示里应该有的几段（队列打不开时托盘写"上传队列不可用"）。 */
  const expectedOf = (queue: QueueStatus | null) =>
    queue
      ? [
          `排队 ${queue.stats.queued}`,
          `上传中 ${queue.stats.uploading}`,
          `已入库 ${queue.stats.done}`,
          `重复 ${queue.stats.duplicate}`,
          `失败 ${queue.stats.failed}`,
        ]
      : ['上传队列不可用']
  const readQueue = () => uploadStatus().catch(() => null)
  // 托盘的刷新任务会合并 250 毫秒内的变化；队列正在上传时数量一直在变：最多等 5 秒，每次都重新读两边。
  let queue = await readQueue()
  let t = await trayStatus()
  let missing = expectedOf(queue).filter((e) => !(t.tooltip ?? '').includes(e))
  const deadline = performance.now() + 5000
  while (t.created && missing.length > 0 && performance.now() < deadline) {
    await new Promise((r) => setTimeout(r, 250))
    t = await trayStatus()
    queue = await readQueue()
    missing = expectedOf(queue).filter((e) => !(t.tooltip ?? '').includes(e))
  }
  if (!t.created) throw new Error(`托盘没有建起来：${t.error ?? '原因不明'}`)
  if (!t.close_to_tray) throw new Error('托盘建好了，但关窗不会缩到托盘')
  if (missing.length > 0) throw new Error(`托盘提示里没有 ${missing.join('、')}：${JSON.stringify(t.tooltip)}`)
  // 壳启动后模拟过一次关窗（壳做，约 2 秒）：窗口只是缩到托盘、没有被销毁才算通过。
  for (let i = 0; i < 40 && !t.close_check; i++) {
    await new Promise((r) => setTimeout(r, 250))
    t = await trayStatus()
  }
  if (!t.close_check) throw new Error('壳没有做关窗自检')
  if (!t.close_check.ok) throw new Error(`关窗自检没通过：${t.close_check.error ?? '原因不明'}`)
  return {
    created: t.created,
    close_to_tray: t.close_to_tray,
    close_check: t.close_check,
    tooltip: t.tooltip,
    menu: [t.status_line, t.counts_line],
    toggle: t.toggle,
    queue_server: queue?.server_url ?? null,
  }
}

/** scan_page 一步（M1-13）：打开 /scan，等上传队列、热文件夹、缓存摘要都读出来。 */
async function checkScanPage() {
  if (!useSessionStore().canAccess('/scan')) return { skipped: '这个账号没有「扫描上传」菜单' }
  await router.push('/scan')
  await waitForSelector('[data-testid=scan-page][data-state=ready]')
  await waitForSelector('[data-testid=hotfolder-card]')
  const summary = await waitForSelector('[data-testid=image-cache-summary] [data-testid=cache-total]')
  const tiles = Object.fromEntries(
    [...document.querySelectorAll('[data-testid=queue-stats] button')].map((b) => [
      (b.getAttribute('data-testid') ?? '').replace('tile-', ''),
      b.querySelector('[data-testid=tile-count]')?.textContent?.trim() ?? '',
    ]),
  )
  const card = document.querySelector('[data-testid=hotfolder-card]')
  return {
    path: window.location.pathname,
    tiles,
    rows: document.querySelectorAll('[data-testid=queue-row]').length,
    hotfolder_configured: card?.getAttribute('data-configured') === 'true',
    hotfolder_running: card?.getAttribute('data-running') === 'true',
    cache_summary: summary.textContent?.trim() ?? '',
  }
}

/** img_direct 一步（M6-1，见文件开头）。 */
async function checkImageDirect(opts: SmokeConfig['image_cache']) {
  const projects = await request<MyProject[]>('/api/projects', { handle: true })
  const project = projects.find((p) => p.status === 'active') ?? projects[0]
  if (!project) return { skipped: '这个账号没有能进的项目' }
  let found: { path: string; fileIds: string[] }
  try {
    found = await findFiles(project.root_folder_id, null, Math.min(opts.count, 4))
  } catch {
    return { skipped: `项目 ${project.project_code} 里没有文件` }
  }
  const batch = await request<FileBatchResponse>('/api/files/batch', {
    method: 'POST',
    body: { file_ids: found.fileIds },
    handle: true,
  })
  const covers = batch.items.map((i) => imageSrc(i, 'cover')).filter((src): src is string => !!src)
  const widths = await Promise.all(covers.map(loadCover))
  const original = batch.items.find((i) => i.original_url)
  let originalBytes: number | null = null
  if (original) {
    const res = await fetch(imageSrc(original, 'original')!, { cache: 'no-store' })
    if (!res.ok) throw new Error(`下载原图返回 HTTP ${res.status}`)
    originalBytes = (await res.arrayBuffer()).byteLength
    if (originalBytes !== original.file_size) {
      throw new Error(`原图大小不对：${originalBytes}，应为 ${original.file_size}`)
    }
  }
  return {
    project_code: project.project_code,
    folder: found.path,
    covers: widths.length,
    original_bytes: originalBytes,
    secure_context: window.isSecureContext,
  }
}

/** pages 一步（M6-1，见文件开头）。 */
async function checkPages() {
  const session = useSessionStore()
  const paths = session.navMenus.map((m) => m.route_path).filter((p): p is string => !!p)
  if (paths.length === 0) throw new Error('这个账号没有能打开的页面')
  const pages: { path: string; ms: number; errors: string[]; unsupported: string[]; missing_api: string[] }[] = []
  const problems: string[] = []
  for (const path of paths) {
    status(`自检：pages ${path}…`)
    const errors: string[] = []
    const unsupported: string[] = []
    const missingApi: string[] = []
    const onError = (e: ErrorEvent) => errors.push(`异常：${e.message}`)
    const onRejection = (e: PromiseRejectionEvent) => errors.push(`未处理的异常：${describe(e.reason)}`)
    window.addEventListener('error', onError)
    window.addEventListener('unhandledrejection', onRejection)
    const seen = new Set(document.querySelectorAll('[data-sonner-toast]'))
    const started = performance.now()
    try {
      await router.push(path)
      await new Promise((r) => setTimeout(r, PAGE_SETTLE_MS))
    } finally {
      window.removeEventListener('error', onError)
      window.removeEventListener('unhandledrejection', onRejection)
    }
    for (const t of document.querySelectorAll('[data-sonner-toast]')) {
      if (seen.has(t) || t.getAttribute('data-type') !== 'error') continue
      const text = t.textContent?.trim() ?? ''
      if (text.includes('还不支持')) unsupported.push(text)
      else if (text.includes('接口不存在')) missingApi.push(text)
      else errors.push(`出错提示：${text}`)
    }
    const at = router.currentRoute.value.path
    if (at !== path) errors.push(`路由被改到了 ${at}`)
    pages.push({ path, ms: Math.round(performance.now() - started), errors, unsupported, missing_api: missingApi })
    if (errors.length > 0) problems.push(`${path}：${errors.join('；')}`)
  }
  if (problems.length > 0) {
    const fine = pages.filter((p) => p.errors.length === 0).map((p) => p.path)
    throw new Error(`${problems.join(' | ')}（其余 ${fine.length} 个正常：${fine.join('、')}）`)
  }
  return { count: pages.length, pages }
}

/** nav_guard 一步（M6-1，见文件开头）。 */
async function checkNavGuard(serverUrl: string) {
  const target = `${serverUrl}/`
  const before = window.location.href
  window.location.assign(target)
  await new Promise((r) => setTimeout(r, NAV_GUARD_WAIT_MS))
  if (window.location.href !== before) throw new Error(`页面跳到了 ${window.location.href}`)
  return { target, blocked: true, still_at: window.location.origin }
}

/**
 * runtime_guard 一步（M7-8）：桥的入口 /wails/runtime 只放行 Bridge.Invoke 和取消调用（client/BRIDGE.md 的"桥的白名单"）。
 * 页面自己发几种不在白名单里的调用，都要 403（壳的日志里各记一条"拦下了不在白名单里的运行时调用"）。挑的都是即使放过去
 * 也无害的调用：System.Environment（只读）和一个不存在的绑定方法。
 */
async function checkRuntimeGuard() {
  const url = '/wails/runtime'
  const json = { 'Content-Type': 'application/json' }
  const environment = JSON.stringify({ object: 8, method: 1 })
  const probes: [string, RequestInit & { query?: string }][] = [
    ['get_query', { method: 'GET', query: '?object=8&method=1' }],
    ['post_text_plain', { method: 'POST', headers: { 'Content-Type': 'text/plain' }, body: environment }],
    ['post_system_environment', { method: 'POST', headers: json, body: environment }],
    [
      'post_other_binding',
      {
        method: 'POST',
        headers: json,
        body: JSON.stringify({ object: 0, method: 0, args: { methodName: 'doclite/client/internal/shell.Bridge.NotAllowed', args: [] } }),
      },
    ],
  ]
  const results: { name: string; status: number }[] = []
  for (const [name, { query, ...init }] of probes) {
    const res = await fetch(url + (query ?? ''), { ...init, cache: 'no-store' })
    results.push({ name, status: res.status })
  }
  const passed = results.filter((r) => r.status !== 403)
  if (passed.length > 0) throw new Error(`不在白名单里的调用没有被拦下：${passed.map((r) => `${r.name} → ${r.status}`).join('、')}`)
  return { rejected: results.length, probes: results }
}

function waitForSelector(selector: string): Promise<Element> {
  return new Promise((resolve, reject) => {
    const started = Date.now()
    const tick = () => {
      const el = document.querySelector(selector)
      if (el) resolve(el)
      else if (Date.now() - started > STEP_TIMEOUT_MS) reject(new Error(`界面上没有出现 ${selector}`))
      else setTimeout(tick, 100)
    }
    tick()
  })
}

export interface SmokeHooks {
  /** 挂载真正的界面（安装路由、mount）。 */
  mountApp: () => void
}

/** stage=page：只报告会话（GET /api/me 的状态码）和界面版本（壳据此核对换地址）。 */
async function reportPage() {
  let meStatus = 0
  try {
    await request<Me>('/api/me', { authRedirect: false, handle: true })
    meStatus = 200
  } catch (e) {
    meStatus = e instanceof ApiError ? e.status : -1
  }
  try {
    await smokeReport({ stage: 'page', me_status: meStatus, path: window.location.pathname, web_version: __APP_VERSION__ })
  } catch (e) {
    console.error('提交自检结果失败', errorMessage(e))
  }
}

/**
 * 开发构建接 vite 开发服务器时，热更新的 WebSocket 连不上（或被 CSP 挡下）的不算违规。只在 vite 的开发模式里
 * （import.meta.env.DEV）。
 */
function ignorableViolation(blocked: string): boolean {
  return import.meta.env.DEV && /^wss?:/.test(blocked)
}

export async function runSmoke(hooks: SmokeHooks) {
  let cfg: SmokeConfig
  try {
    cfg = await smokeConfig()
  } catch (e) {
    await smokeReport({ outcome: 'failed', error: `读自检参数失败：${describe(e)}`, steps: [] }).catch(() => {})
    return
  }
  if (cfg.stage === 'page') {
    await reportPage()
    return
  }

  const container = document.createElement('div')
  container.id = 'smoke'
  container.style.cssText = 'position:fixed;right:12px;bottom:12px;z-index:9999;font:12px monospace;background:#fff;padding:8px;border:1px solid #ddd'
  const line = document.createElement('div')
  line.id = 'smoke-status'
  container.appendChild(line)
  document.body.appendChild(container)

  const steps: Step[] = []
  let outcome: Outcome = 'ok'
  let error: string | undefined
  /** 失败了但不中断自检的那几步的错误（sse、img_cache、tiles、viewer、tray、scan_page）：后面各步照跑，最后按 failed 结束。 */
  const deferred: { name: string; error: unknown }[] = []

  async function step<T>(name: string, fn: () => Promise<T>, timeoutMs = STEP_TIMEOUT_MS): Promise<T> {
    status(`自检：${name}…`)
    const started = performance.now()
    try {
      const detail = await withTimeout(fn(), name, timeoutMs)
      steps.push({ name, ok: true, ms: Math.round(performance.now() - started), detail })
      return detail
    } catch (e) {
      steps.push({ name, ok: false, ms: Math.round(performance.now() - started), error: describe(e) })
      throw e
    }
  }

  /** 壳没有这个功能：记为通过并写明跳过的原因。 */
  function skip(name: string, reason: string) {
    steps.push({ name, ok: true, ms: 0, detail: { skipped: reason } })
  }

  try {
    // 旧版壳没有 capabilities：视为都有。
    const caps = cfg.capabilities ?? { images: true, upload_queue: true, csp: true, nav_guard: false }

    await step('probe', async () => {
      const p = await checkServer()
      const detail = {
        server: p.server_url,
        server_version: p.server_version,
        min_version: p.min_version,
        latest_version: p.latest_version,
        client_version: p.client_version,
      }
      if (!p.reachable) throw new SmokeStop('unreachable', p.error ?? '连不上服务器')
      if (p.upgrade_required) {
        throw new SmokeStop('upgrade_required', `客户端版本 ${p.client_version} 低于最低版本 ${p.min_version}`)
      }
      return detail
    })

    const me = await step('login', async () => {
      const user = await request<Me>('/api/auth/login', {
        method: 'POST',
        body: { username: cfg.username, password: cfg.password },
        handle: true,
      })
      return { id: user.id, username: user.username }
    })

    await step('me', async () => {
      const user = await request<Me>('/api/me', { handle: true })
      if (user.id !== me.id) throw new Error(`/api/me 返回的用户不对：${user.username}`)
      return { username: user.username, roles: user.roles.map((r) => r.role_code) }
    })

    const menus = await step('menus', async () => {
      const list = await request<MyMenu[]>('/api/me/menus', { handle: true })
      return { count: list.length, codes: list.map((m) => m.menu_code) }
    })

    await step('stream', async () => {
      const r = await streamText('/api/me/menus')
      if (r.status !== 200) throw new Error(`流式请求返回 HTTP ${r.status}`)
      const list = JSON.parse(r.body) as MyMenu[]
      if (list.length !== menus.count) throw new Error(`流式结果 ${list.length} 项，和 menus 的 ${menus.count} 项不一致`)
      return { status: r.status, chunks: r.chunks, bytes: r.body.length }
    })

    // sse 失败时只记下来、接着跑后面各步（一轮冒烟就能拿到 img、ui、csp 等在 Windows 上的结果），
    // 最后仍按失败结束；只有 426 和连不上才立即停止。
    try {
      await step(
        'sse',
        async () => {
          const projects = await request<MyProject[]>('/api/projects', { handle: true })
          const project = projects.find((p) => p.status === 'active') ?? projects[0]
          if (!project) {
            throw new Error('这个账号没有能进的项目，没法订阅实时通道（先在 server 上运行 seed:demo，或给这个账号授权一个项目）')
          }
          const hb = await waitHeartbeat(`/api/projects/${project.id}/stream`)
          return { project_code: project.project_code, project_id: project.id, ...hb }
        },
        // 外层时限比等心跳的时限多留 5 秒（含 GET /api/projects），让 waitHeartbeat 自己报出更具体的原因。
        HEARTBEAT_TIMEOUT_MS + 5_000,
      )
    } catch (e) {
      if (outcomeOf(e) !== 'failed') throw e
      deferred.push({ name: 'sse', error: e })
    }

    if (!caps.images) {
      for (const name of ['img', 'img_cache', 'tiles', 'viewer']) skip(name, '壳还没有取图协议、本机缓存和切块（M6-3）')
      // 壳还没有取图协议时，看图和浏览器一样直连存储（M6-1）：确认在壳的页面里也能用。失败时接着跑后面各步。
      try {
        await step('img_direct', () => checkImageDirect(cfg.image_cache), IMG_CACHE_TIMEOUT_MS)
      } catch (e) {
        if (outcomeOf(e) !== 'failed') throw e
        deferred.push({ name: 'img_direct', error: e })
      }
    } else {
      await step('img', async () => {
        const src = shellTestImageSrc()
        const img = await loadImage(src)
        if (img.naturalWidth !== 64) throw new Error(`测试图尺寸不对：${img.naturalWidth}`)
        return { src, width: img.naturalWidth, height: img.naturalHeight }
      })

      // 本机图片缓存（M1-11）。失败时同样接着跑后面各步；426 和连不上除外。
      try {
        await step('img_cache', () => checkImageCache(cfg.image_cache), IMG_CACHE_TIMEOUT_MS)
      } catch (e) {
        if (outcomeOf(e) !== 'failed') throw e
        deferred.push({ name: 'img_cache', error: e })
      }

      // 原图分块（M2-3）。失败时同样接着跑后面各步。
      try {
        await step('tiles', () => checkTiles(cfg), TILES_TIMEOUT_MS)
      } catch (e) {
        if (outcomeOf(e) !== 'failed') throw e
        deferred.push({ name: 'tiles', error: e })
      }

      // 原图查看器（M2-4）。失败时同样接着跑后面各步。
      try {
        await step('viewer', checkViewer, VIEWER_TIMEOUT_MS)
      } catch (e) {
        if (outcomeOf(e) !== 'failed') throw e
        deferred.push({ name: 'viewer', error: e })
      }

    }

    // 托盘（M1-13）。失败时同样接着跑后面各步。
    try {
      await step('tray', checkTray)
    } catch (e) {
      if (outcomeOf(e) !== 'failed') throw e
      deferred.push({ name: 'tray', error: e })
    }

    await step('ui', async () => {
      hooks.mountApp()
      await waitForSelector('[data-testid=side-nav]')
      return { path: window.location.pathname }
    })

    // 逐个打开菜单里的页面（DOCLITE_SMOKE_PAGES=1）。失败时同样接着跑后面各步。
    if (cfg.pages) {
      try {
        await step('pages', checkPages, PAGES_TIMEOUT_MS)
      } catch (e) {
        if (outcomeOf(e) !== 'failed') throw e
        deferred.push({ name: 'pages', error: e })
      }
    }

    if (!caps.upload_queue) {
      skip('scan_page', '壳还没有上传队列和热文件夹（M6-2）')
      skip('rescan', '壳还没有重扫件上传（M6-2）')
    } else {
      // /scan 页（M1-13）。失败时同样接着跑后面各步。
      try {
        await step('scan_page', checkScanPage)
      } catch (e) {
        if (outcomeOf(e) !== 'failed') throw e
        deferred.push({ name: 'scan_page', error: e })
      }

      // 重扫件（M2-7）。没设置 DOCLITE_SMOKE_RESCAN_PATH 时跳过；失败时同样接着跑后面各步。
      try {
        await step('rescan', () => checkRescan(cfg.rescan), RESCAN_TIMEOUT_MS)
      } catch (e) {
        if (outcomeOf(e) !== 'failed') throw e
        deferred.push({ name: 'rescan', error: e })
      }
    }

    // 主流程（M6-4）。没设置 DOCLITE_SMOKE_FLOW_SAMPLES 时跳过；每一步失败时同样接着跑后面各步。
    const flowSteps: [string, (st: ReturnType<typeof newFlowState>) => Promise<unknown>, number][] = [
      ['flow_upload', checkFlowUpload, FLOW_STEP_TIMEOUT_MS],
      ['flow_hotfolder', checkFlowHotfolder, FLOW_STEP_TIMEOUT_MS],
      ['flow_review', (st) => checkFlowReview(st, mountViewer), FLOW_STEP_TIMEOUT_MS],
      ['flow_catalog', checkFlowCatalog, STEP_TIMEOUT_MS],
      ['flow_export', checkFlowExport, FLOW_EXPORT_TIMEOUT_MS],
    ]
    if (!cfg.flow?.enabled || !caps.upload_queue || !caps.images) {
      const why = !cfg.flow?.enabled ? '没有设置 DOCLITE_SMOKE_FLOW_SAMPLES' : '壳还没有上传队列或取图协议'
      for (const [name] of flowSteps) skip(name, why)
    } else {
      const flow = newFlowState(cfg.flow)
      for (const [name, fn, timeout] of flowSteps) {
        try {
          await step(name, () => fn(flow), timeout)
        } catch (e) {
          if (outcomeOf(e) !== 'failed') throw e
          deferred.push({ name, error: e })
        }
      }
    }

    // 导航保护（M6-1）。失败时同样接着跑后面各步（导航真的跳走了的话页面已经不在，自检会超时失败）。
    if (!caps.nav_guard) {
      skip('nav_guard', '壳在这个平台上还没有导航保护')
    } else {
      try {
        await step('nav_guard', () => checkNavGuard(cfg.server_url))
      } catch (e) {
        if (outcomeOf(e) !== 'failed') throw e
        deferred.push({ name: 'nav_guard', error: e })
      }
    }

    // 桥的白名单（M7-6 的加固，M7-8 加的自检）。失败时同样接着跑后面各步。
    try {
      await step('runtime_guard', checkRuntimeGuard)
    } catch (e) {
      if (outcomeOf(e) !== 'failed') throw e
      deferred.push({ name: 'runtime_guard', error: e })
    }

    await step('logout', async () => {
      await request<void>('/api/auth/logout', { method: 'POST', handle: true })
      try {
        await request<Me>('/api/me', { authRedirect: false, handle: true })
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return { after_logout: 401 }
        throw e
      }
      throw new Error('退出登录后 /api/me 仍然返回 200')
    })

    if (!caps.csp) {
      skip('direct_blocked', '壳的 CSP 和取图协议一起做（M6-3）')
      skip('csp', '壳的 CSP 和取图协议一起做（M6-3）')
    } else {
      // 页面不能绕过壳直连服务器：no-cors 的 fetch 不受 CORS 限制，只有 CSP 能挡住它。
      await step('direct_blocked', async () => {
        const url = `${cfg.server_url}/api/health`
        try {
          await fetch(url, { mode: 'no-cors', cache: 'no-store' })
        } catch {
          return { url, blocked: true }
        }
        throw new Error(`页面能直接访问 ${url}，CSP 没有生效`)
      })

      await step('csp', async () => {
        // direct_blocked 那一次是预期的违规，不算；vite 开发服务器的热更新连接也不算。
        const all = cspViolations()
        const unexpected = all.filter((v) => !v.blocked.startsWith(cfg.server_url) && !ignorableViolation(v.blocked))
        if (unexpected.length > 0) throw new Error(`有 ${unexpected.length} 条 CSP 违规`)
        return { violations: 0, ignored_dev_hmr: all.filter((v) => ignorableViolation(v.blocked)).length }
      })
    }
  } catch (e) {
    outcome = outcomeOf(e)
    error = describe(e)
  }
  if (deferred.length > 0) {
    const first = deferred.map((d) => `${d.name}：${describe(d.error)}`).join('；')
    if (outcome === 'ok') {
      outcome = 'failed'
      error = first
    } else {
      error = `${first}；之后又有：${error ?? ''}`
    }
  }

  status(`自检结束：${outcome}`)
  const report: Record<string, unknown> = { outcome, steps, csp_violations: cspViolations() }
  if (error) report.error = error
  let next: string | undefined
  try {
    next = (await smokeReport(report))?.next
  } catch (e) {
    console.error('提交自检结果失败', errorMessage(e))
  }
  // 壳接着测换地址：像"关于"里的"修改服务器地址"一样去客户端的修改地址页。
  if (next === 'edit_server') editServerAddress()
}
