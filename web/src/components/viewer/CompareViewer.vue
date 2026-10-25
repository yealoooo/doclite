<script setup lang="ts">
// 对比视图（M2-4，用户决定 Q2）：同一个文件的两个版本（一般左边是上一轮被驳回的版本，右边是当前版本）。
// - side：左右并排，缩放和平移联动：以"为准的一侧"（leader）为准，另一侧跟着动。只有真实的输入才换 leader：
//   在某一侧按下鼠标（拖动）、滚轮，或者调下面的操作方法（工具栏、键盘，按 viewSide 那一侧）；鼠标只是划过不换
//   （M2-4 审查第 1 轮：点"100%"后鼠标划进另一侧，原来为准的一侧的动画会被截断）。换 leader 时先把旧 leader 的
//   目标值（动画的终点，不是动画进行到一半的值）同步给新 leader，接着在新 leader 上操作。
// - toggle：两张叠在同一个位置，只显示一张，`toggleSide()` 在两张之间切换（位置、缩放不变），方便看出差别。
// - 两侧角标写版本号和那一轮的驳回原因（数据由页面传进来：M2-6 用 M2-2 的审核记录，/files 里用文件日志）。
// - 两张原图同时加载（对比时允许，Q2）；每侧是一个 ImageViewer，各自显示加载、失败、太大的提示。
//   `status` 事件带上是哪一侧，页面据此重取过期的图片信息。
// - 操作方法同 ImageViewer（对为准的一侧做，另一侧联动），另有 setMode、toggleMode、showSide、toggleSide。
//   键盘由页面绑定（M2-6）。
// 截图脚本用的属性：data-testid=compare-viewer，data-mode（side、toggle）、data-showing（left、right）；
// 两侧 data-testid=compare-pane、data-side。
import { ArrowLeftRightIcon } from '@lucide/vue'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import { rejectReasonsText } from '@/lib/reject-reasons'
import type { ViewerImageInfo } from '@/lib/viewer/load-original'
import type { OsdViewer } from '@/lib/viewer/osd'

import ImageViewer, { type ViewerStatus, type ViewerView } from './ImageViewer.vue'

export interface CompareSide {
  info: ViewerImageInfo | null | undefined
  /** 角标上的版本号。 */
  versionNo: number
  /** 角标上的说明，例如"上一轮"、"当前"。 */
  label?: string
  /** 那一轮的驳回原因（内置的传代码，例如 blur；项目自定义的传中文）；没有驳回时不传。 */
  rejectReasons?: readonly string[] | null
  /** 那一轮驳回的说明。 */
  rejectComment?: string | null
}

export type CompareMode = 'side' | 'toggle'
export type CompareSideKey = 'left' | 'right'

const props = defineProps<{
  fileId: string
  left: CompareSide
  right: CompareSide
}>()

const mode = defineModel<CompareMode>('mode', { default: 'side' })
/** toggle 模式下显示哪一侧。 */
const showing = defineModel<CompareSideKey>('showing', { default: 'right' })

const emit = defineEmits<{
  status: [side: CompareSideKey, status: ViewerStatus]
  /** 视图变了（并排时取右侧，同位切换时取显示的那一侧；两侧联动，缩放比例按各自原图的像素算）。 */
  view: [view: ViewerView]
  download: [side: CompareSideKey]
}>()

const leftRef = ref<InstanceType<typeof ImageViewer> | null>(null)
const rightRef = ref<InstanceType<typeof ImageViewer> | null>(null)

const both = () => [leftRef.value, rightRef.value].filter((v): v is InstanceType<typeof ImageViewer> => !!v)

// ---------- 联动 ----------

/** 以哪一侧为准（最近一次真实输入的那一侧；toggle 模式下是显示的那一侧）。 */
let leader: CompareSideKey = 'right'
const cleanups: (() => void)[] = []

function osdOf(side: CompareSideKey): OsdViewer | null {
  return (side === 'left' ? leftRef.value : rightRef.value)?.viewer() ?? null
}

/**
 * 把一侧的缩放、中心、旋转原样同步到另一侧（立即，不动画）。`current` 为 true 时取这一侧此刻的值（联动时每一帧
 * 跟着动画走），false 时取动画的目标值（换 leader、同位切换时，免得把动画截在半路）。
 */
function syncFrom(side: CompareSideKey, current = true) {
  const a = osdOf(side)
  const b = osdOf(side === 'left' ? 'right' : 'left')
  if (!a || !b) return
  const rotation = a.viewport.getRotation(current)
  if (Math.abs(b.viewport.getRotation(true) - rotation) > 0.01) b.viewport.setRotation(rotation, true)
  b.viewport.zoomTo(a.viewport.getZoom(current), undefined, true)
  b.viewport.panTo(a.viewport.getCenter(current), true)
}

function link() {
  for (const side of ['left', 'right'] as const) {
    const v = osdOf(side)
    if (!v) continue
    const handler = () => {
      if (leader === side) syncFrom(side)
    }
    v.addHandler('viewport-change', handler)
    cleanups.push(() => v.removeHandler('viewport-change', handler))
  }
}

/** 换为准的一侧：先把旧 leader 的目标值同步给新的一侧（旧的一侧还在动画也不截断，两侧都停在终点）。 */
function setLeader(side: CompareSideKey) {
  if (side === leader) return
  syncFrom(leader, false)
  leader = side
}

/** 某一侧有真实的输入（按下鼠标、滚轮）：以这一侧为准。toggle 模式下一直是显示的那一侧。 */
function lead(side: CompareSideKey) {
  if (mode.value === 'toggle') return
  setLeader(side)
}

watch([mode, showing], () => {
  if (mode.value === 'toggle') {
    // 显示的那一侧为准；切换前把它的视图（目标值）同步给另一侧，切过去时位置不变。
    setLeader(showing.value)
  }
  const v = (viewSide() === 'left' ? leftRef.value : rightRef.value)?.getView()
  if (v) emit('view', v)
})

/** 工具栏显示哪一侧的视图：并排时取右侧（当前版本），同位切换时取显示的那一侧。 */
function viewSide(): CompareSideKey {
  return mode.value === 'toggle' ? showing.value : 'right'
}

function onSideView(side: CompareSideKey, v: ViewerView) {
  if (side === viewSide()) emit('view', v)
}

onMounted(() => {
  void nextTick(link)
})
onBeforeUnmount(() => {
  for (const c of cleanups) c()
  cleanups.length = 0
})

// ---------- 对外的方法 ----------
// 缩放、旋转只对"为准的一侧"（并排时右侧，同位切换时显示的一侧）做，另一侧跟着联动；两侧原图的像素不同时
// （例如 150 DPI 和 300 DPI），100% 按为准的一侧算。

function primary(): InstanceType<typeof ImageViewer> | null {
  const side = viewSide()
  setLeader(side)
  return side === 'left' ? leftRef.value : rightRef.value
}

function fit() {
  primary()?.fit()
}
function actualSize() {
  primary()?.actualSize()
}
function toggleFit() {
  primary()?.toggleFit()
}
function zoomIn() {
  primary()?.zoomIn()
}
function zoomOut() {
  primary()?.zoomOut()
}
function rotateLeft() {
  primary()?.rotateLeft()
}
function rotateRight() {
  primary()?.rotateRight()
}
function resetRotation() {
  primary()?.resetRotation()
}
function setMode(m: CompareMode) {
  mode.value = m
}
function toggleMode() {
  mode.value = mode.value === 'side' ? 'toggle' : 'side'
}
function showSide(side: CompareSideKey) {
  if (mode.value === 'toggle') syncFrom(showing.value, false)
  showing.value = side
}
/** toggle 模式下切换显示的一侧（side 模式下先切到 toggle）。 */
function toggleSide() {
  if (mode.value !== 'toggle') {
    mode.value = 'toggle'
    return
  }
  showSide(showing.value === 'left' ? 'right' : 'left')
}
function getView(): ViewerView | null {
  return (viewSide() === 'left' ? leftRef.value : rightRef.value)?.getView() ?? null
}
function reload() {
  for (const v of both()) v.reload()
}

defineExpose({
  fit,
  actualSize,
  toggleFit,
  zoomIn,
  zoomOut,
  rotateLeft,
  rotateRight,
  resetRotation,
  setMode,
  toggleMode,
  showSide,
  toggleSide,
  getView,
  reload,
})

// ---------- 角标 ----------

function reasonsOf(side: CompareSide): string {
  return rejectReasonsText(side.rejectReasons)
}

const sides = computed(() => [
  { key: 'left' as const, side: props.left },
  { key: 'right' as const, side: props.right },
])
</script>

<template>
  <div
    class="relative flex min-h-0 min-w-0 flex-1 bg-canvas"
    :class="mode === 'side' ? 'gap-px' : ''"
    data-testid="compare-viewer"
    :data-mode="mode"
    :data-showing="showing"
  >
    <div
      v-for="{ key, side } in sides"
      :key="key"
      class="min-h-0 min-w-0"
      :class="
        mode === 'side'
          ? 'relative flex-1'
          : ['absolute inset-0', showing === key ? 'z-10' : 'invisible z-0']
      "
      data-testid="compare-pane"
      :data-side="key"
      @pointerdown.capture="lead(key)"
      @wheel.capture="lead(key)"
    >
      <ImageViewer
        :ref="(el) => (key === 'left' ? (leftRef = el as never) : (rightRef = el as never))"
        class="size-full"
        :file-id="props.fileId"
        :info="side.info"
        @status="(s) => emit('status', key, s)"
        @view="(v) => onSideView(key, v)"
        @download="emit('download', key)"
      >
        <div
          class="pointer-events-none absolute top-2 left-2 flex max-w-[calc(100%-1rem)] flex-col items-start gap-1"
          data-testid="compare-label"
          :data-side="key"
        >
          <span
            class="rounded px-1.5 font-mono text-xs leading-5 font-medium shadow-outline"
            :class="key === 'right' ? 'bg-foreground text-background' : 'bg-background text-foreground'"
          >v{{ side.versionNo }}<template v-if="side.label"> · {{ side.label }}</template></span>
          <span
            v-if="side.rejectReasons?.length"
            class="max-w-full rounded bg-destructive-soft px-1.5 text-xs leading-5 text-destructive ring-1 ring-destructive/15"
            data-testid="compare-reasons"
          >驳回：{{ reasonsOf(side) }}</span>
          <span
            v-if="side.rejectComment"
            class="line-clamp-2 max-w-full rounded bg-background/90 px-1.5 text-2xs leading-4 text-muted-foreground"
          >{{ side.rejectComment }}</span>
        </div>
      </ImageViewer>
    </div>

    <button
      v-if="mode === 'toggle'"
      type="button"
      class="absolute top-2 left-1/2 z-20 flex -translate-x-1/2 items-center gap-1 rounded-md bg-background/95 px-2 py-1 text-xs shadow-float hover:bg-background"
      data-testid="compare-toggle-side"
      @click="toggleSide()"
    >
      <ArrowLeftRightIcon class="size-3.5" />
      切换到 v{{ showing === 'left' ? props.right.versionNo : props.left.versionNo }}
    </button>
  </div>
</template>
