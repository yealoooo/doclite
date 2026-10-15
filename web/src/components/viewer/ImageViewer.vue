<script setup lang="ts">
// 原图查看器（M2-4，基于 OpenSeadragon；design §4.4 的"看原图"，用户决定 Q1）。
//
// - 先显示封面（当作原图大小，缩放、位置和原图一致），原图就绪后盖上去，画完再拿掉封面。
// - 原图：壳里用 M2-3 的分块（只取看得到的块）；浏览器里 JPEG、PNG 整图模式，TIFF 在 Worker 里按封面的预算解码
//   （lib/viewer/load-original.ts）。同一个查看器同时只加载一张原图：换图（fileId、snapshot_id 变了）时先取消上一张
//   的请求、停掉它的 Worker，再开始新的。壳里换文件后先只显示封面，停留 SHELL_SWITCH_SETTLE_MS 再取分块描述
//   （快速翻页时中间那几张不发请求，壳那边就不会下载、切块；M2-4 审查第 1 轮）。
// - 浏览器里页面上的查看器共用一份内存预算（对比时两侧共用，lib/viewer/budget.ts），放不下的那一张只显示封面。
// - 取块失败（壳里 503、超时等，每块已自动重试过）：OpenSeadragon 不会再取这一块，那里只能显示低一级的块（发虚）
//   或空白；失败的块数到 TILE_FAILURE_NOTICE 就提示，带"重试"（reload）。一块都没取到就按加载失败（保留封面，可以重试）。
// - 操作：适合窗口、100%、滚轮缩放、拖动平移、旋转查看（只转视图，不保存）。按钮和键盘由使用它的页面做，
//   通过 ref 调下面暴露的方法（M2-6 的键位见 roadmap Q3）。OpenSeadragon 自带的按键已关掉。
// - 三种提示：加载中（带下载进度）、失败（可以重试）、超出预算或解不了（只显示封面，提示下载原图；浏览器里另提示
//   改用客户端）。页面要自己显示提示时把 `overlays` 设为 false，按 `status` 事件显示。
// - 截图脚本用的属性：data-testid=image-viewer，data-state（empty、loading、ready、too_large、unsupported、failed）、
//   data-step、data-mode（tiles、image、worker）、data-cover（none、loading、loaded、failed）、data-zoom（百分比）、
//   data-rotation、data-file-id、data-snapshot-id、data-tile-requests（壳里这张图请求过的块数）、
//   data-tile-failures（这张图没取到的块数）、data-view-zoom、data-view-center（OpenSeadragon 的视口缩放和中心）。
import { AlertTriangleIcon, DownloadIcon, ImageOffIcon, LoaderCircleIcon, RotateCwIcon } from '@lucide/vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, shallowRef, watch } from 'vue'

import { Button } from '@/components/ui/button'
import { isTauri } from '@/lib/client'
import { formatBytes } from '@/lib/files'
import { imageSrc } from '@/lib/image-url'
import { ViewerLoadError } from '@/lib/viewer/errors'
import { type LoadedOriginal, loadOriginal, type LoadStep, type OriginalMode, type ViewerImageInfo } from '@/lib/viewer/load-original'
import { addImage, CoverTileSource, createOsdViewer, type OsdTiledImage, type OsdViewer } from '@/lib/viewer/osd'

export type ViewerPhase = 'empty' | 'loading' | 'ready' | 'too_large' | 'unsupported' | 'failed'

/** 查看器的状态（`status` 事件、`getStatus()`）。 */
export interface ViewerStatus {
  phase: ViewerPhase
  /** loading 时在做什么：tiles 等壳准备分块；download 下载（progress 有值）；decode 解码；render 取块、画出来。 */
  step: LoadStep | null
  /** 下载进度 0–1（没有时为 null）。 */
  progress: number | null
  /** 失败、超出预算时的错误码和说明。 */
  code: string | null
  message: string | null
  /** 失败时能不能重试（超出预算、解不了的不能）。 */
  retryable: boolean
  /** 原图从哪来。 */
  mode: OriginalMode | null
  cover: 'none' | 'loading' | 'loaded' | 'failed'
  /** 壳里这张图请求过的块数（data-tile-requests）。 */
  tileRequests: number
  /** 这张图没取到的块数（自动重试之后仍失败的；data-tile-failures）。 */
  tileFailures: number
  /** 从开始加载到原图画完的毫秒数（ready 之后有值）。 */
  elapsedMs: number | null
  /** 原图加载的细节（壳：分块描述；浏览器 TIFF：各步耗时和常驻内存）。 */
  detail: Record<string, unknown> | null
}

/** 视图（`view` 事件、`getView()`）。 */
export interface ViewerView {
  /** 原图像素和屏幕（CSS）像素之比：1 = 100%。还没有图时为 null。 */
  zoom: number | null
  /** 旋转查看的角度：0、90、180、270（只转视图，不保存）。 */
  rotation: number
  /** 当前是"适合窗口"。 */
  fitted: boolean
}

const props = withDefaults(
  defineProps<{
    fileId: string | null
    /** batch 的一项或 snapshots 的一个版本；没有时显示空白。 */
    info: ViewerImageInfo | null | undefined
    /** 在查看器里显示加载、失败、超出预算的提示（默认显示）。 */
    overlays?: boolean
  }>(),
  { overlays: true },
)

const emit = defineEmits<{
  status: [status: ViewerStatus]
  view: [view: ViewerView]
  /** 用户在提示里点了"下载原图"（由页面去打开原图地址）。 */
  download: []
}>()

const inShell = isTauri()
/** 壳里换文件之后停留多久再取分块描述（毫秒，见文件开头）。 */
const SHELL_SWITCH_SETTLE_MS = 250
/** 没取到的块到这个数就提示（每块已经自动重试过，剩下的都看得出来：发虚或空白，所以 1 块就提示）。 */
const TILE_FAILURE_NOTICE = 1
const canvasEl = ref<HTMLElement | null>(null)
const osd = shallowRef<OsdViewer | null>(null)

const status = reactive<ViewerStatus>(blankStatus('empty'))
const view = reactive<ViewerView>({ zoom: null, rotation: 0, fitted: true })

function blankStatus(phase: ViewerPhase): ViewerStatus {
  return {
    phase,
    step: null,
    progress: null,
    code: null,
    message: null,
    retryable: false,
    mode: null,
    cover: 'none',
    tileRequests: 0,
    tileFailures: 0,
    elapsedMs: null,
    detail: null,
  }
}

function emitStatus() {
  emit('status', { ...status })
}

// ---------- 一次加载（换图时整个作废） ----------

interface Session {
  ac: AbortController
  startedAt: number
  original: LoadedOriginal | null
  /** 原图在 OpenSeadragon 里的图层（加进去之后）。 */
  originalItem: OsdTiledImage | null
  /** 原图取到的块数（判断"一块都没取到"）。 */
  tilesLoaded: number
  coverItem: OsdTiledImage | null
  /** 已经回到过"适合窗口"（第一个图层加进来时）。 */
  homed: boolean
  /** 原图已经画完一次（封面可以拿掉了）。 */
  shown: boolean
}

let session: Session | null = null
/** 让 reload() 在 fileId、snapshot_id 没变时也重新加载。 */
const reloadToken = ref(0)
/** 最近一次换文件（fileId 变了；不含第一次显示）的时间，壳里从这时起停留 SHELL_SWITCH_SETTLE_MS。 */
let switchedAt = 0

function stopSession() {
  const s = session
  session = null
  if (!s) return
  s.ac.abort()
  s.original?.dispose()
  const v = osd.value
  if (v) {
    v.imageLoader.clear()
    v.world.removeAll()
  }
}

function startSession() {
  const v = osd.value
  if (!v) return
  stopSession()
  const fileId = props.fileId
  const info = props.info
  Object.assign(status, blankStatus(fileId && info ? 'loading' : 'empty'))
  view.rotation = 0
  v.viewport.setRotation(0, true)
  if (!fileId || !info) {
    emitStatus()
    updateView()
    return
  }
  const s: Session = {
    ac: new AbortController(),
    startedAt: performance.now(),
    original: null,
    originalItem: null,
    tilesLoaded: 0,
    coverItem: null,
    homed: false,
    shown: false,
  }
  session = s
  addCover(s, v, fileId, info)
  emitStatus()

  // 壳里刚换了文件：从换的那一刻起停留一会儿再取分块描述（重试、同一个文件换了版本不停）。
  const settleMs = inShell && switchedAt ? Math.max(0, Math.round(SHELL_SWITCH_SETTLE_MS - (performance.now() - switchedAt))) : 0
  loadOriginal(
    fileId,
    info,
    s.ac.signal,
    {
      onStep(step, progress) {
        if (session !== s) return
        status.step = step
        status.progress = progress
        emitStatus()
      },
      onTile() {
        if (session !== s) return
        status.tileRequests++
      },
    },
    { settleMs },
  )
    .then((orig) => {
      if (session !== s) {
        orig.dispose()
        return
      }
      s.original = orig
      status.mode = orig.mode
      status.detail = orig.detail
      emitStatus()
      addImage(v, {
        tileSource: orig.tileSource,
        x: 0,
        y: 0,
        width: 1,
        success: (e: { item: OsdTiledImage }) => {
          if (session !== s) {
            v.world.removeItem(e.item)
            return
          }
          s.originalItem = e.item
          homeOnce(s, v)
          const item = e.item
          const onLoaded = () => {
            if (item.getFullyLoaded()) originalShown(s, v)
          }
          item.addHandler('fully-loaded-change', onLoaded)
          onLoaded()
        },
        error: (e: { message?: string }) => {
          if (session !== s) return
          fail(new ViewerLoadError('internal', `原图加不进查看器：${e.message ?? '未知错误'}`))
        },
      })
    })
    .catch((e: unknown) => {
      if (session !== s) return
      const err = e instanceof ViewerLoadError ? e : new ViewerLoadError('internal', e instanceof Error ? e.message : String(e))
      if (err.kind === 'aborted') return
      fail(err)
    })
}

function addCover(s: Session, v: OsdViewer, fileId: string, info: ViewerImageInfo) {
  const src = imageSrc({ file_id: fileId, snapshot_id: info.snapshot_id, cover_url: info.cover_url, original_url: info.original_url }, 'cover')
  if (!src) {
    status.cover = 'none'
    return
  }
  status.cover = 'loading'
  const w = info.width_px
  const h = info.height_px
  // 知道原图宽高时，封面当作原图大小的一块（坐标、100% 都和原图一致）；不知道时按封面本身的大小。
  const tileSource =
    w && h ? new CoverTileSource(src, w, h) : { type: 'image', url: src, crossOriginPolicy: false, buildPyramid: false }
  addImage(v, {
    tileSource,
    index: 0,
    x: 0,
    y: 0,
    width: 1,
    success: (e: { item: OsdTiledImage }) => {
      if (session !== s || s.shown) {
        v.world.removeItem(e.item)
        return
      }
      s.coverItem = e.item
      homeOnce(s, v)
      const item = e.item
      const onLoaded = () => {
        if (item.getFullyLoaded() && session === s && status.cover === 'loading') {
          status.cover = 'loaded'
          emitStatus()
        }
      }
      item.addHandler('fully-loaded-change', onLoaded)
      onLoaded()
    },
    error: () => {
      if (session !== s) return
      status.cover = 'failed'
      emitStatus()
    },
  })
}

/** 这次加载第一个图层加进来时回到"适合窗口"。 */
function homeOnce(s: Session, v: OsdViewer) {
  if (s.homed) return
  s.homed = true
  v.viewport.goHome(true)
  updateView()
}

function originalShown(s: Session, v: OsdViewer) {
  if (s.shown || session !== s) return
  // 看得到的块都失败了（OpenSeadragon 把失败的块当作"不存在"，照样算加载完）：保留封面，按加载失败提示。
  if (s.tilesLoaded === 0 && status.tileFailures > 0) {
    fail(new ViewerLoadError('tiles_failed', `原图的 ${status.tileFailures} 块都没取到`))
    return
  }
  s.shown = true
  if (s.coverItem) {
    v.world.removeItem(s.coverItem)
    s.coverItem = null
  }
  status.phase = 'ready'
  status.step = null
  status.progress = null
  status.elapsedMs = Math.round(performance.now() - s.startedAt)
  emitStatus()
}

function fail(err: ViewerLoadError) {
  const kind = err.kind
  status.phase = kind === 'too_large' ? 'too_large' : kind === 'unsupported' ? 'unsupported' : 'failed'
  status.step = null
  status.progress = null
  status.code = err.code
  status.message = err.message
  status.retryable = kind === 'failed'
  emitStatus()
}

// 换文件的时间（同步记下，下面的 watch 开始新的一次加载时要用）。
watch(
  () => props.fileId,
  (now, before) => {
    if (before != null && now !== before) switchedAt = performance.now()
  },
  { flush: 'sync' },
)

// 换图（file_id、快照、有没有原图地址变了）时重新加载；只是 URL 换了（同一天重签、store 重取，info 换了一个对象）
// 不重新加载，用户点"重试"时才用新的地址。比较的是拼起来的字符串：getter 每次返回新数组的话，info 一换对象就会被
// 当成"变了"（M2-4 审查第 1 轮发现：对比时重取 snapshots 会让失败的一侧反复重新加载）。
watch(
  () =>
    [props.fileId ?? '', props.info?.snapshot_id ?? '', !!props.info?.original_url, !!props.info?.cover_url, reloadToken.value].join('|'),
  () => startSession(),
)

// 块加载失败：封面的（例如壳的 img 协议 404）记下来，提示里不再说"先显示封面"；原图的计数（见文件开头）。
// 换图、关掉时 OpenSeadragon 放弃的块也会报失败，那时 session 已经换了，不算。
function onTileFailed(e: { tiledImage?: OsdTiledImage }) {
  const s = session
  if (!s || !e.tiledImage) return
  if (e.tiledImage === s.coverItem && status.cover !== 'failed') {
    status.cover = 'failed'
    emitStatus()
  } else if (e.tiledImage === s.originalItem) {
    status.tileFailures++
    emitStatus()
  }
}

function onTileLoaded(e: { tiledImage?: OsdTiledImage }) {
  const s = session
  if (s && e.tiledImage && e.tiledImage === s.originalItem) s.tilesLoaded++
}

// ---------- 视图 ----------

let viewFrame = 0
function scheduleView() {
  if (viewFrame) return
  viewFrame = requestAnimationFrame(() => {
    viewFrame = 0
    updateView()
  })
}

function topItem(): OsdTiledImage | null {
  const v = osd.value
  if (!v) return null
  const n = v.world.getItemCount()
  return n ? v.world.getItemAt(n - 1) : null
}

/** OpenSeadragon 的视口缩放和中心（data-view-zoom、data-view-center，截图脚本核对对比视图的联动）。 */
const viewportAttrs = reactive({ zoom: '', center: '' })

function updateView() {
  const v = osd.value
  const item = topItem()
  if (v) {
    const c = v.viewport.getCenter(true)
    viewportAttrs.zoom = v.viewport.getZoom(true).toFixed(4)
    viewportAttrs.center = `${c.x.toFixed(4)},${c.y.toFixed(4)}`
  }
  const rotation = v ? ((Math.round(v.viewport.getRotation()) % 360) + 360) % 360 : 0
  const zoom = v && item ? item.viewportToImageZoom(v.viewport.getZoom(true)) : null
  // "适合窗口"：缩放和中心都和 home 一样（中心差不到可见宽度的 1%；拖动过就不算）。
  let fitted = true
  if (v) {
    const zoomNow = v.viewport.getZoom()
    const home = v.viewport.getHomeBounds().getCenter()
    const c = v.viewport.getCenter()
    const tol = 0.01 / zoomNow
    fitted = Math.abs(zoomNow / v.viewport.getHomeZoom() - 1) < 0.01 && Math.abs(c.x - home.x) < tol && Math.abs(c.y - home.y) < tol
  }
  if (view.zoom === zoom && view.rotation === rotation && view.fitted === fitted) return
  view.zoom = zoom
  view.rotation = rotation
  view.fitted = fitted
  emit('view', { ...view })
}

const zoomPercent = computed(() => (view.zoom == null ? null : Math.round(view.zoom * 100)))

// ---------- 对外的方法 ----------

/** 适合窗口。 */
function fit(immediately = false) {
  osd.value?.viewport.goHome(immediately)
}

/** 100%：原图的 1 个像素对应屏幕上 1 个 CSS 像素（以视图中心为准）。 */
function actualSize(immediately = false) {
  const v = osd.value
  const item = topItem()
  if (!v || !item) return
  v.viewport.zoomTo(item.imageToViewportZoom(1), undefined, immediately)
  v.viewport.applyConstraints()
}

/** 适合窗口和 100% 之间切换（Q3 的空格）。 */
function toggleFit() {
  if (view.fitted) actualSize()
  else fit()
}

/** 放大、缩小（每次 1.25 倍）。 */
function zoomIn() {
  const v = osd.value
  if (!v) return
  v.viewport.zoomBy(1.25)
  v.viewport.applyConstraints()
}
function zoomOut() {
  const v = osd.value
  if (!v) return
  v.viewport.zoomBy(0.8)
  v.viewport.applyConstraints()
}

/** 旋转查看（只转视图，不保存）：正数顺时针。转之前是"适合窗口"的，转完重新适合窗口。 */
function rotateBy(degrees: number) {
  const v = osd.value
  if (!v) return
  const wasFitted = view.fitted
  v.viewport.rotateBy(degrees)
  if (wasFitted) v.viewport.goHome()
  scheduleView()
}
function rotateLeft() {
  rotateBy(-90)
}
function rotateRight() {
  rotateBy(90)
}
function resetRotation() {
  const v = osd.value
  if (!v) return
  v.viewport.setRotation(0)
  v.viewport.goHome()
}

/** 重新加载（失败后重试；图片信息换过的话用新的地址）。 */
function reload() {
  reloadToken.value++
}

function getStatus(): ViewerStatus {
  return { ...status }
}
function getView(): ViewerView {
  return { ...view }
}
/** OpenSeadragon 的 Viewer（对比视图联动用；页面一般用不到）。 */
function viewer(): OsdViewer | null {
  return osd.value
}

defineExpose({ fit, actualSize, toggleFit, zoomIn, zoomOut, rotateBy, rotateLeft, rotateRight, resetRotation, reload, getStatus, getView, viewer })

// ---------- 生命周期 ----------

onMounted(() => {
  const v = createOsdViewer(canvasEl.value!)
  osd.value = v
  v.addHandler('viewport-change', scheduleView)
  v.addHandler('rotate', scheduleView)
  // 窗口大小变了：原来是"适合窗口"的，按新的大小重新适合窗口（否则保持图像大小不变）。
  v.addHandler('resize', () => {
    if (view.fitted) v.viewport.goHome(true)
    scheduleView()
  })
  v.addHandler('animation-finish', updateView)
  v.addHandler('tile-load-failed', onTileFailed as never)
  v.addHandler('tile-loaded', onTileLoaded as never)
  startSession()
})

onBeforeUnmount(() => {
  stopSession()
  if (viewFrame) cancelAnimationFrame(viewFrame)
  osd.value?.destroy()
  osd.value = null
})

// ---------- 提示 ----------

const showingCover = computed(() => status.phase !== 'ready' && status.phase !== 'empty' && status.cover === 'loaded')

const loadingText = computed(() => {
  switch (status.step) {
    case 'tiles':
      return '客户端正在准备原图（第一次打开大图要等几秒）'
    case 'wait':
      return '等另一张原图解完（浏览器里的内存预算）'
    case 'download': {
      const pct = status.progress == null ? '' : ` ${Math.floor(status.progress * 100)}%`
      return `正在下载原图${pct}`
    }
    case 'decode':
      return '正在解码原图'
    default:
      return '正在显示原图'
  }
})

const downloadSize = computed(() => {
  if (status.step !== 'download' || status.progress == null || !props.info) return ''
  const total = props.info.file_size
  return `${formatBytes(Math.round(total * status.progress))} / ${formatBytes(total)}`
})

const problemTitle = computed(() => {
  const coverNote = status.cover === 'loaded' ? '，只显示封面' : ''
  if (status.phase === 'too_large' && status.code === 'over_budget') return `浏览器里的内存不够再打开这张原图${coverNote}`
  if (status.phase === 'too_large') return inShell ? `原图太大${coverNote}` : `原图太大，浏览器里看不了${coverNote}`
  if (status.phase === 'unsupported') return `看不了原图${coverNote}`
  return '原图加载失败'
})

const problemHint = computed(() => {
  if (status.phase === 'failed') return status.retryable ? '可以重试，或者下载原图查看。' : '可以下载原图查看。'
  return inShell ? '可以下载原图，用别的软件查看。' : '可以下载原图，或者改用客户端查看（客户端按块显示大图，不受浏览器的限制）。'
})
</script>

<template>
  <div
    class="relative overflow-hidden bg-zinc-200 select-none"
    data-testid="image-viewer"
    :data-state="status.phase"
    :data-step="status.step ?? undefined"
    :data-mode="status.mode ?? undefined"
    :data-cover="status.cover"
    :data-zoom="zoomPercent ?? undefined"
    :data-rotation="view.rotation"
    :data-file-id="props.fileId ?? undefined"
    :data-snapshot-id="props.info?.snapshot_id"
    :data-tile-requests="status.tileRequests"
    :data-tile-failures="status.tileFailures"
    :data-view-zoom="viewportAttrs.zoom"
    :data-view-center="viewportAttrs.center"
  >
    <div ref="canvasEl" class="absolute inset-0" data-testid="viewer-canvas" />

    <!-- 没有封面、还没有原图时的占位 -->
    <div
      v-if="status.phase !== 'ready' && status.phase !== 'empty' && status.cover !== 'loaded'"
      class="pointer-events-none absolute inset-0 flex flex-col items-center justify-center gap-1.5 text-muted-foreground"
    >
      <ImageOffIcon v-if="status.phase !== 'loading'" class="size-8 opacity-60" :stroke-width="1.5" />
      <span v-if="status.phase !== 'loading'" class="text-xs">{{ status.cover === 'failed' ? '封面也加载失败' : '没有封面' }}</span>
    </div>

    <span
      v-if="showingCover"
      class="pointer-events-none absolute top-2 right-2 rounded bg-foreground/70 px-1.5 text-[11px] leading-5 text-background"
      data-testid="viewer-cover-badge"
    >封面（长边 400 像素）</span>

    <template v-if="props.overlays">
      <!-- 加载中 -->
      <div
        v-if="status.phase === 'loading'"
        class="absolute bottom-4 left-1/2 flex -translate-x-1/2 items-center gap-2 rounded-full bg-background/95 py-1.5 pr-3.5 pl-3 text-xs text-foreground shadow-[0_0_0_1px_rgba(0,0,0,0.08),0_2px_8px_rgba(0,0,0,0.08)]"
        data-testid="viewer-loading"
      >
        <LoaderCircleIcon class="size-3.5 animate-spin text-muted-foreground" />
        <span>{{ loadingText }}</span>
        <span v-if="downloadSize" class="font-mono text-[11px] text-muted-foreground">{{ downloadSize }}</span>
        <span v-if="status.cover === 'loaded'" class="text-muted-foreground">· 先显示封面</span>
        <span
          v-if="status.step === 'download' && status.progress != null"
          class="absolute right-3 bottom-0 left-3 h-0.5 overflow-hidden rounded-full bg-muted"
        >
          <span class="block h-full bg-info transition-[width]" :style="{ width: `${Math.floor(status.progress * 100)}%` }" />
        </span>
      </div>

      <!-- 原图画出来了，但有块没取到（那里发虚或空白） -->
      <div
        v-else-if="status.phase === 'ready' && status.tileFailures >= TILE_FAILURE_NOTICE"
        class="absolute bottom-4 left-1/2 flex -translate-x-1/2 items-center gap-2 rounded-full bg-background/95 py-1 pr-1.5 pl-3 text-xs text-foreground shadow-[0_0_0_1px_rgba(0,0,0,0.08),0_2px_8px_rgba(0,0,0,0.08)]"
        data-testid="viewer-tile-failures"
        :data-count="status.tileFailures"
      >
        <AlertTriangleIcon class="size-3.5 shrink-0 text-warn" />
        <span>有 {{ status.tileFailures }} 块原图没取到，那里可能发虚或空白</span>
        <Button variant="outline" size="xs" data-testid="viewer-tile-retry" @click="reload()">
          <RotateCwIcon />重试
        </Button>
      </div>

      <!-- 失败、超出预算、解不了 -->
      <div
        v-else-if="status.phase === 'failed' || status.phase === 'too_large' || status.phase === 'unsupported'"
        class="absolute bottom-4 left-1/2 w-[min(460px,calc(100%-2rem))] -translate-x-1/2 rounded-lg bg-background p-3 text-[13px] text-foreground shadow-[0_0_0_1px_rgba(0,0,0,0.08),0_2px_2px_rgba(0,0,0,0.04),0_8px_16px_-4px_rgba(0,0,0,0.12)]"
        data-testid="viewer-problem"
        :data-kind="status.phase"
        :data-code="status.code ?? undefined"
      >
        <div class="flex items-start gap-2">
          <AlertTriangleIcon class="mt-0.5 size-4 shrink-0" :class="status.phase === 'failed' ? 'text-destructive' : 'text-warn'" />
          <div class="min-w-0 flex-1">
            <div class="font-medium">{{ problemTitle }}</div>
            <div v-if="status.message" class="mt-0.5 text-xs break-words text-muted-foreground">{{ status.message }}</div>
            <div class="mt-0.5 text-xs text-muted-foreground">{{ problemHint }}</div>
          </div>
        </div>
        <div class="mt-2 flex justify-end gap-1.5">
          <Button v-if="status.retryable" variant="outline" size="xs" data-testid="viewer-retry" @click="reload()">
            <RotateCwIcon />重试
          </Button>
          <Button variant="outline" size="xs" data-testid="viewer-download" @click="emit('download')">
            <DownloadIcon />下载原图
          </Button>
        </div>
      </div>
    </template>
    <slot />
  </div>
</template>

<style>
/* OpenSeadragon 的画布拿到焦点时不画浏览器默认的轮廓（键盘由页面绑定，不在画布上）。 */
[data-testid='viewer-canvas'] .openseadragon-canvas:focus {
  outline: none;
}
</style>
