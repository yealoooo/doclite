<script setup lang="ts">
// /files 右键"查看原图"（M2-4，用户决定 Q11）：全屏层里用 ImageViewer 看原图。
// - 打开时看右键的那个文件；左右两个按钮在当前文件夹的文件之间切换（换图时上一张的请求、Worker 一起取消）。
// - 图片信息经 image-info store 取（batch，壳里因此记下来源）；当前这张和前后各一张一起取。
// - 版本号大于 1 时可以"对比上一版"（CompareViewer）：两侧取自 snapshots 接口；"上一版"是最近一次被驳回的版本
//   （文件日志里最近一条 reject 的 version_no，比当前小），没有驳回记录时取前一个版本；两侧的驳回原因也取自文件日志。
// - 工具栏：适合窗口、100%、缩放、旋转查看（不保存）、对比、下载原图。键盘只有 Esc 关闭（审核台的键位在 M2-6）。
import { ChevronLeftIcon, ChevronRightIcon, Columns2Icon, DownloadIcon, LayersIcon, LoaderCircleIcon, XIcon } from '@lucide/vue'
import { DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'
import { computed, ref, watch } from 'vue'
import { toast } from 'vue-sonner'

import { batchFiles, getFileLogs, getFileSnapshots } from '@/api/files'
import { errorMessage } from '@/api/http'
import type { FileEntry, FileLog, FileSnapshots, SnapshotItem } from '@/api/types'
import CompareViewer, { type CompareMode, type CompareSide, type CompareSideKey } from '@/components/viewer/CompareViewer.vue'
import ImageViewer, { type ViewerStatus, type ViewerView } from '@/components/viewer/ImageViewer.vue'
import ViewerToolbar from '@/components/viewer/ViewerToolbar.vue'
import { isTauri } from '@/lib/client'
import { formatSpec } from '@/lib/files'
import { openExternal, shellErrorMessage } from '@/lib/shell'
import type { ViewerImageInfo } from '@/lib/viewer/load-original'
import { useImageInfoStore } from '@/stores/image-info'

// 布尔型的 prop 不传时 Vue 按 false 处理，canReadLogs 的默认值要显式写成 true（M5-3：原来 /files 的对比一直不取日志，
// 左侧只显示"上一版"、没有驳回原因）。
const props = withDefaults(defineProps<{
  /** 当前文件夹里的文件（按列表的顺序），左右切换在它们之间。 */
  files: FileEntry[]
  /** 打开时看哪个文件。 */
  fileId: string | null
  /**
   * 能读文件日志（files 菜单）：对比时用它找"上一轮"和驳回原因。默认 true（/files 本身要 files 菜单）；归类页（M3-6）
   * 没有 files 菜单的人传 false，不调 `/logs`（403 forbidden 会走全局处理、跳无权提示页），按前一版对比、不显示原因。
   */
  canReadLogs?: boolean
}>(), { canReadLogs: true })
const open = defineModel<boolean>('open', { required: true })

const images = useImageInfoStore()
const inShell = isTauri()

const currentId = ref<string | null>(null)
/** 正在对比上一版。 */
const compare = ref(false)
const index = computed(() => props.files.findIndex((f) => f.file_id === currentId.value))
const entry = computed(() => props.files[index.value] ?? null)
const info = computed(() => (currentId.value ? images.get(currentId.value) : undefined))
const missing = computed(() => !!currentId.value && images.isMissing(currentId.value))

watch(
  () => [open.value, props.fileId] as const,
  ([isOpen, fileId]) => {
    if (!isOpen) return
    currentId.value = fileId
    compare.value = false
  },
  { immediate: true },
)

// 当前这张和前后各一张的图片信息（前后两张先取好，切换时不用等 batch）。
watch(
  () => [open.value, currentId.value, index.value] as const,
  ([isOpen, id, i]) => {
    if (!isOpen || !id) return
    const ids = [id]
    if (i > 0) ids.push(props.files[i - 1]!.file_id)
    if (i >= 0 && i < props.files.length - 1) ids.push(props.files[i + 1]!.file_id)
    images.request(ids)
  },
  { immediate: true },
)

function go(delta: number) {
  const i = index.value + delta
  if (i < 0 || i >= props.files.length) return
  currentId.value = props.files[i]!.file_id
  compare.value = false
}

// ---------- 查看器 ----------

const viewerRef = ref<InstanceType<typeof ImageViewer> | null>(null)
const compareRef = ref<InstanceType<typeof CompareViewer> | null>(null)
const status = ref<ViewerStatus | null>(null)
const view = ref<ViewerView | null>(null)

/** 这几种失败是图片信息过期了：原图地址过期（存储返回 403）、壳里没有来源记录（404）、取不到地址。 */
function staleInfo(s: ViewerStatus): boolean {
  return s.phase === 'failed' && ['http', 'not_found', 'no_url'].includes(s.code ?? '')
}

/**
 * 查看器的状态。图片信息过期时让 image-info store 重取一次 batch（壳因此重新记下来源），用户点"重试"时用新的信息。
 */
function onStatus(s: ViewerStatus) {
  status.value = s
  if (staleInfo(s) && currentId.value) images.reportBroken(currentId.value)
}

/** 工具栏的操作发给当前显示的查看器（对比时两侧一起）。 */
function target() {
  return compare.value ? compareRef.value : viewerRef.value
}

const modeLabel = computed(() => {
  switch (status.value?.mode) {
    case 'tiles':
      return '客户端分块'
    case 'image':
      return '浏览器整图'
    case 'worker':
      return '浏览器解码 TIFF'
    default:
      return ''
  }
})

const statusText = computed(() => {
  const s = status.value
  if (!s) return ''
  switch (s.phase) {
    case 'loading':
      return s.cover === 'loaded' ? '原图加载中 · 先显示封面' : '原图加载中'
    case 'ready':
      return s.elapsedMs != null ? `原图 · ${(s.elapsedMs / 1000).toFixed(1)} 秒` : '原图'
    case 'too_large':
      return '原图太大 · 只显示封面'
    case 'unsupported':
      return '看不了原图 · 只显示封面'
    case 'failed':
      return '原图加载失败'
    default:
      return ''
  }
})

// ---------- 对比上一版 ----------

const compareMode = ref<CompareMode>('side')
const compareShowing = ref<CompareSideKey>('right')
const compareLoading = ref(false)
const compareError = ref<string | null>(null)
const compareSides = ref<{ fileId: string; left: CompareSide; right: CompareSide } | null>(null)
let compareSeq = 0

function rejectOf(logs: FileLog[], versionNo: number): FileLog | undefined {
  // 日志新的在前：同一个版本被驳回多次（撤回通过后再驳回）时取最近一次。
  return logs.find((l) => l.action === 'reject' && l.detail.version_no === versionNo)
}

function sideOf(s: SnapshotItem, label: string, logs: FileLog[]): CompareSide {
  const r = rejectOf(logs, s.version_no)
  const reasons = Array.isArray(r?.detail.reasons) ? (r.detail.reasons as unknown[]).map(String) : null
  const comment = typeof r?.detail.comment === 'string' ? r.detail.comment : null
  return { info: s as ViewerImageInfo, versionNo: s.version_no, label, rejectReasons: reasons, rejectComment: comment }
}

async function openCompare() {
  const fid = currentId.value
  if (!fid) return
  const my = ++compareSeq
  compare.value = true
  compareMode.value = 'side'
  compareShowing.value = 'right'
  compareSides.value = null
  compareError.value = null
  compareLoading.value = true
  sidesRefreshedAt = 0
  try {
    const [snaps, logs] = await Promise.all([
      getFileSnapshots(fid, true),
      // 没有 files 菜单之外的权限问题时日志都能取到；取不到就不显示驳回原因。
      props.canReadLogs === false
        ? Promise.resolve([] as FileLog[])
        : getFileLogs(fid, true).then(
            (r) => r.logs,
            () => [] as FileLog[],
          ),
    ])
    if (my !== compareSeq) return
    const list = snaps.snapshots
    const cur = list[list.length - 1]
    if (!cur || list.length < 2) {
      compareError.value = '这个文件只有一个版本，没有可以对比的'
      return
    }
    const lastReject = logs.find(
      (l) => l.action === 'reject' && typeof l.detail.version_no === 'number' && l.detail.version_no < cur.version_no,
    )
    const prevNo = (lastReject?.detail.version_no as number | undefined) ?? cur.version_no - 1
    const prev = list.find((s) => s.version_no === prevNo) ?? list[list.length - 2]!
    compareSides.value = {
      fileId: fid,
      left: sideOf(prev, lastReject ? '上一轮' : '上一版', logs),
      right: sideOf(cur, '当前', logs),
    }
  } catch (e) {
    if (my === compareSeq) compareError.value = errorMessage(e)
  } finally {
    if (my === compareSeq) compareLoading.value = false
  }
}

function closeCompare() {
  compareSeq++
  compare.value = false
  compareSides.value = null
}

/** 地址还能用多久以上才算新鲜（打开原图、对比的两侧）。 */
const FRESH_MS = 10 * 60 * 1000
function fresh(expiresAt: string | null | undefined): boolean {
  return !!expiresAt && Date.parse(expiresAt) - Date.now() > FRESH_MS
}

let refreshingSides: Promise<FileSnapshots | null> | null = null
/** 上一次因为加载失败重取 snapshots 的时间（同一个对比里 30 秒内不再因为失败重取，和 image-info store 的 reportBroken 一样）。 */
let sidesRefreshedAt = 0
const SIDES_RETRY_MS = 30 * 1000

/**
 * 对比的两侧取自 snapshots 接口：重取一次（壳因此重新记下各个版本的来源），换上新的地址。版本不变，查看器不会重新
 * 加载，用户点"重试"时用新的地址。同时只取一次；对比已经关掉或换了文件就不换。
 */
function refreshCompareSides(): Promise<FileSnapshots | null> {
  const sides = compareSides.value
  if (!sides) return Promise.resolve(null)
  const my = compareSeq
  refreshingSides ??= getFileSnapshots(sides.fileId, true)
    .then((snaps) => {
      const cur = compareSides.value
      if (my !== compareSeq || !cur || cur.fileId !== snaps.file_id) return snaps
      const pick = (side: CompareSide): CompareSide => {
        const sid = side.info?.snapshot_id
        const s = snaps.snapshots.find((x) => x.snapshot_id === sid)
        return s ? { ...side, info: s as ViewerImageInfo } : side
      }
      compareSides.value = { ...cur, left: pick(cur.left), right: pick(cur.right) }
      return snaps
    })
    .catch(() => null)
    .finally(() => {
      refreshingSides = null
    })
  return refreshingSides
}

/**
 * 对比时某一侧的状态：图片信息过期时，重取那两个版本的地址（30 秒内最多一次），当前版本另外让 image-info store
 * 重取 batch。查看器不会因为换了地址自动重新加载，用户点"重试"时用新的。
 */
function onCompareStatus(_side: CompareSideKey, s: ViewerStatus) {
  if (!staleInfo(s) || !currentId.value) return
  images.reportBroken(currentId.value)
  if (Date.now() - sidesRefreshedAt < SIDES_RETRY_MS) return
  sidesRefreshedAt = Date.now()
  void refreshCompareSides()
}

// ---------- 下载原图 ----------

/** 打开原图地址（浏览器：新窗口；壳：系统浏览器）。地址剩不到 10 分钟就重新取（对比的一侧重取 snapshots）。 */
async function downloadOriginal(snapshot?: ViewerImageInfo | null) {
  const fid = currentId.value
  if (!fid) return
  let url = snapshot?.original_url ?? null
  if (snapshot && !fresh(snapshot.url_expires_at)) {
    const snaps = await refreshCompareSides()
    const s = snaps?.snapshots.find((x) => x.snapshot_id === snapshot.snapshot_id)
    if (!s) {
      toast.error('取不到原图地址（重取各个版本失败）')
      return
    }
    url = s.original_url
    if (!url) {
      toast.error('取不到原图地址', { description: s.url_error?.message })
      return
    }
  }
  if (!snapshot) {
    let item = images.get(fid)
    if (!item || !fresh(item.url_expires_at)) {
      try {
        item = (await batchFiles([fid], true)).items[0]
      } catch (e) {
        toast.error(`取不到原图地址：${errorMessage(e)}`)
        return
      }
    }
    url = item?.original_url ?? null
    if (!url) {
      toast.error('取不到原图地址', { description: item?.url_error?.message })
      return
    }
  }
  let parsed: URL
  try {
    parsed = new URL(url!)
  } catch {
    toast.error('原图地址格式不对')
    return
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    toast.error('原图地址不是 http 或 https，不能打开')
    return
  }
  if (inShell) {
    try {
      await openExternal(parsed.href)
      toast.success('已在系统浏览器里打开原图')
    } catch (e) {
      toast.error(shellErrorMessage(e))
    }
  } else {
    window.open(parsed.href, '_blank', 'noopener,noreferrer')
  }
}

function onCompareDownload(side: CompareSideKey) {
  const s = compareSides.value
  if (!s) return
  void downloadOriginal((side === 'left' ? s.left.info : s.right.info) ?? null)
}

const title = computed(() => entry.value?.file_name ?? '查看原图')
const spec = computed(() => (info.value ? formatSpec(info.value) : ''))
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 z-50 bg-black/40" />
      <DialogContent
        class="fixed inset-3 z-50 flex flex-col overflow-hidden rounded-xl bg-background shadow-[0_0_0_1px_rgba(0,0,0,0.08),0_16px_48px_rgba(0,0,0,0.25)] outline-none"
        data-testid="original-viewer"
        :data-file-id="currentId ?? undefined"
        :data-compare="compare ? 'on' : 'off'"
        @open-auto-focus.prevent
      >
        <!-- 标题行 -->
        <div class="flex h-11 shrink-0 items-center gap-2 border-b px-3">
          <DialogTitle class="min-w-0 truncate text-sm font-medium" data-testid="viewer-title">{{ title }}</DialogTitle>
          <DialogDescription class="shrink-0 font-mono text-xs text-muted-foreground">
            <template v-if="index >= 0">{{ index + 1 }} / {{ props.files.length }}</template>
            <template v-if="entry"> · v{{ entry.version_no }}</template>
          </DialogDescription>
          <div class="flex-1" />
          <button
            type="button"
            class="viewer-tool"
            :disabled="index <= 0"
            data-testid="viewer-prev"
            title="上一个文件"
            @click="go(-1)"
          >
            <ChevronLeftIcon class="size-4" />上一个
          </button>
          <button
            type="button"
            class="viewer-tool"
            :disabled="index < 0 || index >= props.files.length - 1"
            data-testid="viewer-next"
            title="下一个文件"
            @click="go(1)"
          >
            下一个<ChevronRightIcon class="size-4" />
          </button>
          <span class="mx-1 h-4 w-px bg-border" />
          <button type="button" class="viewer-tool" data-testid="viewer-close" title="关闭（Esc）" @click="open = false">
            <XIcon class="size-4" />
          </button>
        </div>

        <ViewerToolbar
          :view="view"
          :disabled="!currentId"
          @fit="target()?.fit()"
          @actual="target()?.actualSize()"
          @zoom-in="target()?.zoomIn()"
          @zoom-out="target()?.zoomOut()"
          @rotate-left="target()?.rotateLeft()"
          @rotate-right="target()?.rotateRight()"
        >
          <template #end>
            <span class="mx-1 h-4 w-px bg-border" />
            <template v-if="!compare">
              <button
                v-if="entry && entry.version_no > 1"
                type="button"
                class="viewer-tool"
                data-testid="viewer-compare"
                title="和上一版（上一轮被驳回的版本）对比"
                @click="openCompare()"
              >
                <Columns2Icon class="size-3.5" />对比上一版
              </button>
            </template>
            <template v-else>
              <button
                type="button"
                class="viewer-tool"
                :class="{ on: compareMode === 'side' }"
                data-testid="compare-mode-side"
                @click="compareMode = 'side'"
              >
                <Columns2Icon class="size-3.5" />左右并排
              </button>
              <button
                type="button"
                class="viewer-tool"
                :class="{ on: compareMode === 'toggle' }"
                data-testid="compare-mode-toggle"
                title="两张叠在同一个位置，切换着看"
                @click="compareMode = 'toggle'"
              >
                <LayersIcon class="size-3.5" />同位切换
              </button>
              <button type="button" class="viewer-tool" data-testid="compare-close" @click="closeCompare()">退出对比</button>
            </template>
            <div class="flex-1" />
            <button type="button" class="viewer-tool" data-testid="viewer-download-original" @click="downloadOriginal()">
              <DownloadIcon class="size-3.5" />下载原图
            </button>
          </template>
        </ViewerToolbar>

        <!-- 查看区 -->
        <div class="relative flex min-h-0 flex-1">
          <div v-if="missing" class="flex flex-1 items-center justify-center bg-zinc-200 text-sm text-muted-foreground">
            文件已不存在（可能刚被删除或移走）
          </div>
          <template v-else-if="compare">
            <div v-if="compareLoading" class="flex flex-1 items-center justify-center gap-2 bg-zinc-200 text-sm text-muted-foreground">
              <LoaderCircleIcon class="size-4 animate-spin" />正在取各个版本…
            </div>
            <div v-else-if="compareError" class="flex flex-1 items-center justify-center bg-zinc-200 text-sm text-destructive">
              {{ compareError }}
            </div>
            <CompareViewer
              v-else-if="compareSides"
              ref="compareRef"
              v-model:mode="compareMode"
              v-model:showing="compareShowing"
              :file-id="compareSides.fileId"
              :left="compareSides.left"
              :right="compareSides.right"
              @status="onCompareStatus"
              @view="(v) => (view = v)"
              @download="onCompareDownload"
            />
          </template>
          <ImageViewer
            v-else
            ref="viewerRef"
            class="min-w-0 flex-1"
            :file-id="currentId"
            :info="info"
            @status="onStatus"
            @view="(v) => (view = v)"
            @download="downloadOriginal()"
          />
        </div>

        <!-- 状态栏 -->
        <div
          class="flex h-7 shrink-0 items-center gap-3.5 overflow-hidden border-t bg-background px-3 font-mono text-[11px] whitespace-nowrap text-muted-foreground"
          data-testid="viewer-statusbar"
        >
          <span v-if="entry" class="truncate">{{ entry.file_name }}</span>
          <span v-if="spec">{{ spec }}</span>
          <span v-if="!compare && modeLabel">{{ modeLabel }}</span>
          <span class="flex-1" />
          <span v-if="!compare">{{ statusText }}</span>
          <span v-if="view?.zoom != null">{{ Math.round(view.zoom * 100) }}%</span>
        </div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
