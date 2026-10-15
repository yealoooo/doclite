<script setup lang="ts">
// 审核工作台右栏上半部分（M2-6，参考 plan.html p-review 的 rv-side）：文件标题、"上一轮因为某原因被驳回"、快照时间线、
// 每一轮的审核记录、文件信息。数据由页面取好传进来（snapshots、files/{fid}/reviews、batch），这里只显示。
// - 快照时间线：新的在上，当前版本标"当前"；每个版本写创建人、时间和审它的结果（通过：某人 / 驳回：原因）。
//   可以勾两个版本对比（Q2："快照面板里可以选任意两个版本对比"），默认勾上一轮被驳回的版本和当前版本。
// - 审核记录：files/{fid}/reviews 的全部行（按分配的先后），已取消的（改派、删除留下的）默认隐藏，可以展开。
// 截图脚本用的属性：data-testid=review-detail（data-file-id、data-state）；prev-reject（data-version）；
// snapshot-item（data-version、data-current、data-picked）；compare-picked；review-record（data-status、data-result）；
// show-cancelled。
// - M3-7：文件信息下面是"归类信息（只读，不影响审核）"（CatalogPlacementInfo，自己取 files/{fid}/catalog；
//   `catalogRefresh` 变了重新取），只给档号、题名、第几页（用户决定 Q10）。testid catalog-info。
import { AlertTriangleIcon, Columns2Icon, DownloadIcon, LoaderCircleIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import type { FileImageItem, FileReviewRow, FileReviews, MyReviewItem, SnapshotItem } from '@/api/types'
import CatalogPlacementInfo from '@/components/files/CatalogPlacementInfo.vue'
import { formatDateTime } from '@/lib/format'
import { colorModeLabel, formatBytes, formatSpec, snapshotTypeLabel } from '@/lib/files'
import { rejectReasonsText } from '@/lib/reject-reasons'
import { reviewRowLabel } from '@/lib/reviews'

import { defaultComparePair, resultForSnapshot } from './review-file'

const props = defineProps<{
  item: MyReviewItem | null
  /** 当前版本的图片信息（batch）。 */
  info: FileImageItem | undefined
  snapshots: SnapshotItem[] | null
  reviews: FileReviews | null
  state: 'idle' | 'loading' | 'ready' | 'error'
  error: string | null
  prevReject: FileReviewRow | null
  lowDpiThreshold: number
  /** 正在对比的两个快照（snapshot_id），时间线上标出来。 */
  comparing: readonly [string, string] | null
  /** M3-7：变了就重新取归类信息。 */
  catalogRefresh?: number
}>()

const emit = defineEmits<{
  /** 对比这两个版本（左、右的 snapshot_id，左边是旧的）。 */
  compare: [left: string, right: string]
  download: []
  retry: []
}>()

const showCancelled = ref(false)
/** 勾来对比的版本（最多两个）。 */
const picked = ref<string[]>([])

const ordered = computed(() => [...(props.snapshots ?? [])].reverse())
const currentSid = computed(() => props.snapshots?.[props.snapshots.length - 1]?.snapshot_id ?? null)
const rows = computed(() => props.reviews?.reviews ?? [])
const cancelledCount = computed(() => rows.value.filter((r) => r.status === 'cancelled').length)
const shownRows = computed(() => (showCancelled.value ? rows.value : rows.value.filter((r) => r.status !== 'cancelled')))
const prevVersion = computed(() => props.prevReject?.version_no ?? null)

// 换了文件、版本变了：默认勾上一轮被驳回的版本（没有时前一个版本）和当前版本。
watch(
  () => [props.item?.file_id ?? null, props.snapshots?.map((s) => s.snapshot_id).join(',') ?? '', props.prevReject?.snapshot_id ?? ''] as const,
  () => {
    const pair = props.snapshots ? defaultComparePair(props.snapshots, props.prevReject) : null
    picked.value = pair ? [pair.left.snapshot_id, pair.right.snapshot_id] : []
  },
  { immediate: true },
)
watch(
  () => props.item?.file_id ?? null,
  () => {
    showCancelled.value = false
  },
)

function togglePick(sid: string) {
  const list = picked.value.filter((x) => x !== sid)
  if (list.length === picked.value.length) {
    list.push(sid)
    // 最多两个：再勾第三个时去掉最早勾的那个。
    while (list.length > 2) list.shift()
  }
  picked.value = list
}

const pickedPair = computed(() => {
  if (picked.value.length !== 2 || !props.snapshots) return null
  const a = props.snapshots.find((s) => s.snapshot_id === picked.value[0])
  const b = props.snapshots.find((s) => s.snapshot_id === picked.value[1])
  if (!a || !b) return null
  return a.version_no < b.version_no ? ([a, b] as const) : ([b, a] as const)
})

function snapshotNote(s: SnapshotItem): string {
  const r = resultForSnapshot(rows.value, s.snapshot_id)
  if (!r) return ''
  if (r.result === 'approved') return `通过：${r.reviewer_name}`
  return `驳回：${rejectReasonsText(r.reject_reasons)}`
}

function isComparing(sid: string): boolean {
  return !!props.comparing && props.comparing.includes(sid)
}

const lowDpi = computed(() => props.info?.dpi != null && props.info.dpi < props.lowDpiThreshold)
const formatName = computed(() => {
  const m = props.info?.mime_type ?? ''
  if (m === 'image/jpeg') return 'JPEG'
  if (m === 'image/png') return 'PNG'
  if (m === 'image/tiff') return 'TIFF'
  return m || '—'
})

function recordClass(r: FileReviewRow): string {
  if (r.status === 'open') return 'bg-warn-soft text-warn'
  if (r.status === 'cancelled') return 'bg-muted text-muted-foreground'
  if (r.result === 'approved') return 'bg-ok-soft text-ok'
  if (r.result === 'rejected') return 'bg-destructive-soft text-destructive'
  return 'bg-muted text-muted-foreground'
}
</script>

<template>
  <div class="flex flex-col gap-4 px-3.5 py-3" data-testid="review-detail" :data-file-id="props.item?.file_id" :data-state="props.state">
    <template v-if="props.item">
      <!-- 标题 -->
      <div class="min-w-0">
        <div class="flex items-center gap-1.5">
          <h2 class="min-w-0 truncate text-sm font-semibold text-foreground" :title="props.item.file_name" data-testid="review-file-name">
            {{ props.item.file_name }}
          </h2>
          <span
            v-if="props.item.review_round > 1"
            class="shrink-0 rounded bg-warn-soft px-1.5 text-[11px] leading-5 text-warn"
            data-testid="review-round"
          >第 {{ props.item.review_round }} 轮</span>
        </div>
        <div class="mt-0.5 truncate text-xs text-muted-foreground" :title="`/${props.item.folder_path}`">/{{ props.item.folder_path }}</div>
      </div>

      <!-- 上一轮被驳回 -->
      <div
        v-if="props.prevReject && props.item.review_round > 1"
        class="rounded-lg bg-warn-soft/70 px-3 py-2 text-xs leading-5 text-foreground shadow-[0_0_0_1px_rgba(180,83,9,0.18)]"
        data-testid="prev-reject"
        :data-version="prevVersion ?? undefined"
      >
        <div class="flex items-start gap-1.5">
          <AlertTriangleIcon class="mt-0.5 size-3.5 shrink-0 text-warn" />
          <div class="min-w-0">
            <div>
              上一轮<template v-if="prevVersion">（v{{ prevVersion }}）</template>因<b class="font-medium">“{{ rejectReasonsText(props.prevReject.reject_reasons) }}”</b>被驳回
            </div>
            <div v-if="props.prevReject.comment" class="text-muted-foreground">说明：{{ props.prevReject.comment }}</div>
            <div class="text-muted-foreground">{{ props.prevReject.reviewer_name }} · {{ formatDateTime(props.prevReject.reviewed_at) }} · 按 C 和这一版对比</div>
          </div>
        </div>
      </div>

      <div v-if="props.state === 'loading' && !props.snapshots" class="flex items-center gap-2 text-xs text-muted-foreground">
        <LoaderCircleIcon class="size-3.5 animate-spin" />正在取版本和审核记录…
      </div>
      <div v-else-if="props.state === 'error'" class="text-xs text-destructive">
        {{ props.error ?? '取不到版本和审核记录' }}
        <button type="button" class="ml-1 underline" @click="emit('retry')">重试</button>
      </div>

      <!-- 快照时间线 -->
      <section v-if="props.snapshots">
        <div class="mb-1.5 text-xs font-medium text-muted-foreground">快照</div>
        <ol class="relative flex flex-col">
          <li
            v-for="(s, i) in ordered"
            :key="s.snapshot_id"
            class="relative flex gap-2.5 pb-2.5 pl-1"
            data-testid="snapshot-item"
            :data-version="s.version_no"
            :data-current="s.snapshot_id === currentSid ? 'true' : undefined"
            :data-picked="picked.includes(s.snapshot_id) ? 'true' : undefined"
          >
            <span v-if="i < ordered.length - 1" class="absolute top-3.5 bottom-0 left-[8.5px] w-px bg-border" />
            <span
              class="relative mt-1 size-2.5 shrink-0 rounded-full"
              :class="s.snapshot_id === currentSid ? 'bg-info shadow-[0_0_0_3px_var(--info-soft)]' : 'bg-muted-foreground/40'"
            />
            <div class="min-w-0 flex-1 text-xs leading-5">
              <div class="flex items-center gap-1.5">
                <b class="font-medium text-foreground">v{{ s.version_no }} · {{ snapshotTypeLabel(s.snapshot_type) }}</b>
                <span v-if="s.snapshot_id === currentSid" class="rounded bg-info-soft px-1 text-[10px] leading-4 text-info">当前</span>
                <span v-if="isComparing(s.snapshot_id)" class="rounded bg-foreground/80 px-1 text-[10px] leading-4 text-background">对比中</span>
              </div>
              <div class="truncate text-muted-foreground">
                {{ s.created_by_name ?? '系统' }} · {{ formatDateTime(s.created_at) }}<template v-if="snapshotNote(s)"> · {{ snapshotNote(s) }}</template>
              </div>
              <div class="truncate text-muted-foreground/80">{{ formatSpec(s) }}</div>
            </div>
            <label
              v-if="ordered.length > 1"
              class="flex shrink-0 cursor-pointer items-start gap-1 pt-0.5 text-[11px] text-muted-foreground"
              title="勾两个版本对比"
            >
              <input
                type="checkbox"
                class="mt-0.5 size-3.5 accent-[var(--info)]"
                :checked="picked.includes(s.snapshot_id)"
                data-testid="snapshot-pick"
                @change="togglePick(s.snapshot_id)"
              />对比
            </label>
          </li>
        </ol>
        <div class="flex gap-1.5">
          <button
            v-if="ordered.length > 1"
            type="button"
            class="inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs shadow-[0_0_0_1px_rgba(0,0,0,0.1)] hover:bg-muted disabled:opacity-50"
            :disabled="!pickedPair"
            data-testid="compare-picked"
            @mousedown.prevent
            @click="pickedPair && emit('compare', pickedPair[0].snapshot_id, pickedPair[1].snapshot_id)"
          >
            <Columns2Icon class="size-3.5" />
            <template v-if="pickedPair">对比 v{{ pickedPair[0].version_no }} 和 v{{ pickedPair[1].version_no }}</template>
            <template v-else>勾两个版本对比</template>
          </button>
          <button
            type="button"
            class="inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
            data-testid="detail-download"
            @mousedown.prevent
            @click="emit('download')"
          >
            <DownloadIcon class="size-3.5" />下载原图
          </button>
        </div>
      </section>

      <!-- 审核记录 -->
      <section v-if="props.reviews">
        <div class="mb-1.5 flex items-center gap-2 text-xs font-medium text-muted-foreground">
          审核记录
          <span class="flex-1" />
          <button
            v-if="cancelledCount"
            type="button"
            class="font-normal hover:text-foreground"
            data-testid="show-cancelled"
            @click="showCancelled = !showCancelled"
          >{{ showCancelled ? '隐藏已取消的' : `显示已取消的 ${cancelledCount} 条` }}</button>
        </div>
        <p v-if="!shownRows.length" class="text-xs text-muted-foreground">没有审核记录</p>
        <ol class="flex flex-col gap-1.5">
          <li
            v-for="r in shownRows"
            :key="r.assignment_id"
            class="rounded-md bg-muted/40 px-2.5 py-1.5 text-xs leading-5"
            data-testid="review-record"
            :data-status="r.status"
            :data-result="r.result ?? undefined"
          >
            <div class="flex items-center gap-1.5">
              <span class="rounded-full px-1.5 text-[11px] leading-4" :class="recordClass(r)">{{ reviewRowLabel(r) }}</span>
              <span class="min-w-0 truncate text-foreground">{{ r.reviewer_name }}</span>
              <span v-if="r.version_no" class="font-mono text-[11px] text-muted-foreground">审 v{{ r.version_no }}</span>
              <span class="flex-1" />
              <span class="shrink-0 text-[11px] text-muted-foreground">{{ formatDateTime(r.reviewed_at ?? r.assigned_at) }}</span>
            </div>
            <div v-if="r.reject_reasons.length" class="text-destructive">原因：{{ rejectReasonsText(r.reject_reasons) }}</div>
            <div v-if="r.comment" class="break-words text-muted-foreground">说明：{{ r.comment }}</div>
            <div class="text-[11px] text-muted-foreground/80">{{ r.assigned_by_name }} 分配于 {{ formatDateTime(r.assigned_at) }}</div>
          </li>
        </ol>
      </section>

      <!-- 文件信息 -->
      <section class="text-xs leading-6" data-testid="review-file-info">
        <div class="mb-0.5 font-medium text-muted-foreground">文件信息（上传时从文件头读出）</div>
        <template v-if="props.info">
          <div class="flex items-center gap-1.5">
            <span :class="lowDpi ? 'text-warn' : 'text-ok'">{{ lowDpi ? '!' : '✓' }}</span>
            <span>{{ formatSpec(props.info, false) }} · {{ colorModeLabel(props.info.color_mode) }}</span>
            <span v-if="lowDpi" class="text-warn" data-testid="review-low-dpi">DPI 不足（低于 {{ props.lowDpiThreshold }}）</span>
          </div>
          <div class="flex items-center gap-1.5">
            <span class="text-ok">✓</span>
            <span class="min-w-0 truncate">{{ formatName }} · {{ formatBytes(props.info.file_size) }} · v{{ props.info.version_no }}</span>
          </div>
        </template>
        <div v-if="props.item.review_round > 1" class="flex items-center gap-1.5">
          <span class="text-warn">!</span>
          <span>
            第 {{ props.item.review_round }} 轮<template v-if="props.prevReject">：上一轮因“{{ rejectReasonsText(props.prevReject.reject_reasons) }}”被驳回</template>
          </span>
        </div>
      </section>

      <!-- 归类信息（M3-7，只读） -->
      <CatalogPlacementInfo :file-id="props.item.file_id" :refresh-token="props.catalogRefresh ?? 0" note="不影响审核" />
    </template>
    <p v-else class="py-6 text-center text-xs text-muted-foreground">在左边选一个文件</p>
  </div>
</template>
