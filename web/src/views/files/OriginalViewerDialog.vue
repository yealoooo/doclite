<script setup lang="ts">
// /files 右键"查看原图"（M2-4，用户决定 Q11）：全屏层里用 ImageViewer 看原图。
// - 打开时看右键的那个文件；左右两个按钮在当前文件夹的文件之间切换（换图时上一张的请求一起取消）。
// - 图片信息经 image-info store 取（batch，壳里因此记下来源）；当前这张和前后各一张一起取。
// - 版本号大于 1 时可以"对比上一版"（CompareViewer，useSnapshotCompare）：两侧取自 snapshots 接口；"上一版"是最近一次
//   被驳回的版本（文件日志里最近一条 reject 的 version_no，比当前小），没有驳回记录时取前一个版本；两侧的驳回原因也取自文件日志。
// - 工具栏：适合窗口、100%、缩放、旋转查看（不保存）、对比、下载原图。键盘只有 Esc 关闭（审核台的键位在 M2-6）。
import { ChevronLeftIcon, ChevronRightIcon, Columns2Icon, DownloadIcon, LayersIcon, LoaderCircleIcon, XIcon } from '@lucide/vue'
import { DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'
import { computed, ref, watch } from 'vue'

import { getFileLogs, getFileSnapshots } from '@/api/files'
import type { FileEntry, FileLog, SnapshotItem } from '@/api/types'
import CompareViewer, { type CompareSide, type CompareSideKey } from '@/components/viewer/CompareViewer.vue'
import ImageViewer, { type ViewerStatus, type ViewerView } from '@/components/viewer/ImageViewer.vue'
import { staleInfo, viewerStatusText } from '@/components/viewer/viewer-status'
import ViewerToolbar from '@/components/viewer/ViewerToolbar.vue'
import { useSnapshotCompare } from '@/composables/useSnapshotCompare'
import { formatSpec } from '@/lib/files'
import { currentOriginalUrl, openOriginal } from '@/lib/original'
import type { ViewerImageInfo } from '@/lib/viewer/load-original'
import { useImageInfoStore } from '@/stores/image-info'
import { HANDLE_ALL } from '@/api/http'

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

const currentId = ref<string | null>(null)
const index = computed(() => props.files.findIndex((f) => f.file_id === currentId.value))
const entry = computed(() => props.files[index.value] ?? null)
const info = computed(() => (currentId.value ? images.get(currentId.value) : undefined))
const missing = computed(() => !!currentId.value && images.isMissing(currentId.value))

const cmp = useSnapshotCompare({
  refetch: (fid) => getFileSnapshots(fid, HANDLE_ALL).then((r) => r.snapshots),
  onStale: (fid) => images.reportBroken(fid),
})
const { pair: compareSides, mode: compareMode, showing: compareShowing, loading: compareLoading, error: compareError } = cmp
/** 正在对比上一版（含正在取、出错）。 */
const compare = cmp.active

watch(
  () => [open.value, props.fileId] as const,
  ([isOpen, fileId]) => {
    if (!isOpen) return
    currentId.value = fileId
    cmp.close()
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
  cmp.close()
}

// ---------- 查看器 ----------

const viewerRef = ref<InstanceType<typeof ImageViewer> | null>(null)
const compareRef = ref<InstanceType<typeof CompareViewer> | null>(null)
const status = ref<ViewerStatus | null>(null)
const view = ref<ViewerView | null>(null)

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

const statusText = computed(() => viewerStatusText(status.value))

// ---------- 对比上一版 ----------

function sideOf(s: SnapshotItem, label: string, logs: FileLog[]): CompareSide {
  // 日志新的在前：同一个版本被驳回多次（撤回通过后再驳回）时取最近一次。
  const r = logs.find((l) => l.action === 'reject' && l.detail.version_no === s.version_no)
  const reasons = Array.isArray(r?.detail.reasons) ? (r.detail.reasons as unknown[]).map(String) : null
  const comment = typeof r?.detail.comment === 'string' ? r.detail.comment : null
  return { info: s as ViewerImageInfo, versionNo: s.version_no, label, rejectReasons: reasons, rejectComment: comment }
}

function openCompare() {
  const fid = currentId.value
  if (!fid) return
  void cmp.open(async () => {
    const [snaps, logs] = await Promise.all([
      getFileSnapshots(fid, HANDLE_ALL),
      // 没有 files 菜单之外的权限问题时日志都能取到；取不到就不显示驳回原因。
      props.canReadLogs === false
        ? Promise.resolve([] as FileLog[])
        : getFileLogs(fid, HANDLE_ALL).then(
            (r) => r.logs,
            () => [] as FileLog[],
          ),
    ])
    const list = snaps.snapshots
    const cur = list[list.length - 1]
    if (!cur || list.length < 2) return '这个文件只有一个版本，没有可以对比的'
    const lastReject = logs.find(
      (l) => l.action === 'reject' && typeof l.detail.version_no === 'number' && l.detail.version_no < cur.version_no,
    )
    const prevNo = (lastReject?.detail.version_no as number | undefined) ?? cur.version_no - 1
    const prev = list.find((s) => s.version_no === prevNo) ?? list[list.length - 2]!
    return { fileId: fid, left: sideOf(prev, lastReject ? '上一轮' : '上一版', logs), right: sideOf(cur, '当前', logs) }
  })
}

function onCompareStatus(_side: CompareSideKey, s: ViewerStatus) {
  cmp.onSideStatus(s)
}

// ---------- 下载原图 ----------

async function downloadOriginal() {
  if (currentId.value) await openOriginal(await currentOriginalUrl(currentId.value))
}

async function onCompareDownload(side: CompareSideKey) {
  await openOriginal(await cmp.sideOriginal(side))
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
              <button type="button" class="viewer-tool" data-testid="compare-close" @click="cmp.close()">退出对比</button>
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
          <span class="flex-1" />
          <span v-if="!compare">{{ statusText }}</span>
          <span v-if="view?.zoom != null">{{ Math.round(view.zoom * 100) }}%</span>
        </div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
