<script setup lang="ts">
// 审核分配页的右栏（M2-5，参考 plan.html 的 p-assign）：
// - "分给谁"：可以分给的人（reviewers 接口：启用、有「我的审核」菜单、能进这个项目）和各自的待办数，单选一个；
// - 分配：没选文件时按整个文件夹（含子文件夹）分配，页面先弹确认框写明"含子文件夹，共 n 个待分配"；
//   选了文件时按选中的分配（不是待分配的由 server 跳过并计数）；选中的里有待审核的，可以批量改派给这个人
//   （个数只算现在不是他在审的，都是他在审时按钮禁用并说明，M2-8 验收）；
// - 这个文件夹（子树）里有待审分给了已不能再审的人（停用、没有审核菜单、失去项目授权）时显示提醒，可打开改派；
// - 选中一个文件：当前审核人和每一轮的分配（`GET /api/files/{fid}/reviews`），单个改派、撤回通过（Q8），
//   以及这个文件的操作日志。
// 操作本身由页面执行（emit），这里只负责显示和收集选择。
import { AlertTriangleIcon, LoaderCircleIcon, RotateCcwIcon, UserRoundCheckIcon, UsersRoundIcon } from '@lucide/vue'
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import { errorMessage } from '@/api/http'
import { getFileReviews } from '@/api/reviews'
import type { AssignmentReviewer, FileEntry, FileReviews, ProjectReviewer } from '@/api/types'
import FileCover from '@/components/files/FileCover.vue'
import FileLogList from '@/components/files/FileLogList.vue'
import { Button } from '@/components/ui/button'
import { reviewStatusOf } from '@/lib/files'
import { formatDateTime } from '@/lib/format'
import { rejectReasonLabel, reviewerProblemsText, reviewRowLabel } from '@/lib/reviews'
import { useImageInfoStore } from '@/stores/image-info'

const props = defineProps<{
  /** null：还在加载。 */
  reviewers: ProjectReviewer[] | null
  folderName: string | null
  /** 当前文件夹整棵子树的待分配数；null：还不知道。 */
  folderUnassigned: number | null
  /** 按文件夹一次最多分配多少个。 */
  folderLimit: number
  selectedFiles: FileEntry[]
  /** 选中的里能改派给右栏选中的人的个数：待审、而且现在不是他在审（页面按当前文件夹的 open 分配算）。 */
  reassignableCount: number
  /** 当前文件夹子树里有 open 分配、但已不能再审的人。 */
  problemReviewers: AssignmentReviewer[]
  busy: boolean
  /** 最近一次分配、改派的结果（含"跳过了 n 个"）。 */
  lastResult: string | null
  /** 变了就重新取选中文件的分配和日志。 */
  refreshToken: number
  canSeeLogs: boolean
}>()

const target = defineModel<string | null>('target', { required: true })

const emit = defineEmits<{
  assignFolder: []
  assignSelected: []
  reassignSelected: []
  reassignOne: [assignmentId: string, file: FileEntry]
  reopen: [file: FileEntry]
  openReassign: []
}>()

const images = useImageInfoStore()

const targetReviewer = computed(() => props.reviewers?.find((r) => r.id === target.value) ?? null)
const mode = computed(() => (props.selectedFiles.length ? 'files' : 'folder'))
const selectedBy = computed(() => {
  const c = { unassigned: 0, pending: 0, approved: 0, rejected: 0 }
  for (const f of props.selectedFiles) c[f.review_status]++
  return c
})
const assignCount = computed(() =>
  mode.value === 'files' ? props.selectedFiles.length : (props.folderUnassigned ?? 0),
)
/** 选中的待审里已经是右栏选中的人在审的（改派给他没有意义）。 */
const alreadyTarget = computed(() => (targetReviewer.value ? selectedBy.value.pending - props.reassignableCount : 0))
const folderTooMany = computed(() => mode.value === 'folder' && (props.folderUnassigned ?? 0) > props.folderLimit)
const canAssign = computed(
  () => !!targetReviewer.value && !props.busy && assignCount.value > 0 && !folderTooMany.value,
)
const assignLabel = computed(() => {
  const who = targetReviewer.value?.display_name ?? '…'
  return `分给${who}（${assignCount.value} 个）`
})

function onAssign() {
  if (!canAssign.value) return
  if (mode.value === 'folder') emit('assignFolder')
  else emit('assignSelected')
}

// ---------- 选中一个文件：分配记录 ----------

const single = computed(() => (props.selectedFiles.length === 1 ? props.selectedFiles[0]! : null))
const reviews = ref<FileReviews | null>(null)
const reviewsFor = ref<string | null>(null)
const reviewsLoading = ref(false)
const reviewsError = ref<string | null>(null)
let timer: ReturnType<typeof setTimeout> | null = null
let seq = 0

async function loadReviews(fileId: string) {
  const my = ++seq
  reviewsLoading.value = true
  reviewsError.value = null
  try {
    const res = await getFileReviews(fileId, true)
    if (my !== seq || single.value?.file_id !== fileId) return
    reviews.value = res
    reviewsFor.value = fileId
  } catch (e) {
    if (my !== seq || single.value?.file_id !== fileId) return
    reviews.value = null
    reviewsFor.value = null
    reviewsError.value = errorMessage(e)
  } finally {
    if (my === seq) reviewsLoading.value = false
  }
}

watch(
  () => [single.value?.file_id ?? null, single.value?.review_status, props.refreshToken] as const,
  ([fileId], old) => {
    if (timer) clearTimeout(timer)
    if (!fileId || old?.[0] !== fileId) {
      seq++
      reviews.value = null
      reviewsFor.value = null
      reviewsError.value = null
      reviewsLoading.value = false
    }
    if (!fileId) return
    images.request([fileId])
    reviewsLoading.value = true
    timer = setTimeout(() => void loadReviews(fileId), 150)
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
  seq++
})

const current = computed(() => (reviews.value && reviewsFor.value === single.value?.file_id ? reviews.value : null))
/** 文件现在的状态：分配记录取到了用它（更新），否则用列表里的。 */
const status = computed(() => current.value?.review_status ?? single.value?.review_status ?? 'unassigned')
const openRow = computed(() => current.value?.reviews.find((r) => r.status === 'open') ?? null)
/** 每一轮的分配，新的在前（含已取消的）。 */
const history = computed(() => (current.value ? [...current.value.reviews].reverse() : []))
/** 最近一次有结论的那一行（按分配的先后，最新的在后）。 */
const lastDone = computed(() =>
  current.value ? [...current.value.reviews].reverse().find((r) => r.status === 'done') : undefined,
)
/**
 * 审核人一栏（审查第 1 轮：只在文件现在就是这个状态时写"通过：某人""驳回：某人"）：
 * 待审写 open 那一行的审核人；通过、驳回写最近一次给出这个结论的人；待分配写"现在没有分给谁"。
 */
const reviewerLine = computed<{ label: string; name: string | null } | null>(() => {
  const cur = current.value
  if (!cur) return null
  if (openRow.value) return { label: '审核人', name: openRow.value.reviewer_name }
  const done = lastDone.value
  if (status.value === 'approved' && done?.result === 'approved') return { label: '通过', name: done.reviewer_name }
  if (status.value === 'rejected' && done?.result === 'rejected') return { label: '驳回', name: done.reviewer_name }
  if (status.value === 'unassigned') return { label: '现在没有分给谁', name: null }
  return { label: '审核人', name: '—' }
})
</script>

<template>
  <div class="flex flex-col gap-4 p-4 text-[13px]" data-testid="assign-panel">
    <!-- ===== 分给谁 ===== -->
    <section>
      <h3 class="mb-2 flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <UsersRoundIcon class="size-3.5" />分给谁
      </h3>
      <div v-if="!props.reviewers" class="flex items-center gap-2 py-2 text-xs text-muted-foreground">
        <LoaderCircleIcon class="size-3.5 animate-spin" />正在加载…
      </div>
      <p v-else-if="props.reviewers.length === 0" class="text-xs leading-relaxed text-muted-foreground">
        没有可以分给的人。审核人要启用、有「我的审核」菜单、能进这个项目（在「系统管理 › 项目」里授权）。
      </p>
      <div v-else class="flex flex-col gap-1" role="radiogroup" aria-label="审核人" data-testid="reviewer-list">
        <button
          v-for="r in props.reviewers"
          :key="r.id"
          type="button"
          role="radio"
          :aria-checked="r.id === target"
          class="flex items-center gap-2 rounded-md px-2.5 py-1.5 text-left"
          :class="r.id === target ? 'bg-info-soft shadow-[0_0_0_1px_var(--info)]' : 'shadow-[0_0_0_1px_var(--border)] hover:bg-muted/70'"
          data-testid="reviewer-row"
          :data-reviewer-id="r.id"
          :data-username="r.username"
          :data-open="r.open"
          @click="target = r.id"
        >
          <span class="min-w-0 flex-1">
            <span class="block truncate font-medium text-foreground">{{ r.display_name }}</span>
            <span class="block truncate text-[11px] text-muted-foreground">{{ r.username }}</span>
          </span>
          <span
            class="shrink-0 rounded-full px-2 text-[11px] leading-5"
            :class="r.open > 0 ? 'bg-warn-soft text-warn' : 'bg-muted text-muted-foreground'"
            data-testid="reviewer-open"
          >待办 {{ r.open }}</span>
        </button>
      </div>
    </section>

    <!-- ===== 分配和改派 ===== -->
    <section class="flex flex-col gap-2" data-testid="assign-actions" :data-mode="mode">
      <div class="text-xs text-muted-foreground" data-testid="assign-scope">
        <template v-if="mode === 'folder'">
          整个文件夹「{{ props.folderName ?? '…' }}」（含子文件夹）：
          <span class="font-medium text-foreground">{{ props.folderUnassigned ?? '…' }}</span> 个待分配
        </template>
        <template v-else>
          已选 <span class="font-medium text-foreground">{{ props.selectedFiles.length }}</span> 个文件：待分配
          {{ selectedBy.unassigned }}、待审 {{ selectedBy.pending }}、通过 {{ selectedBy.approved }}、驳回 {{ selectedBy.rejected }}
        </template>
      </div>
      <Button :disabled="!canAssign" data-testid="assign-button" :data-mode="mode" @click="onAssign">
        <LoaderCircleIcon v-if="props.busy" class="animate-spin" />
        <UserRoundCheckIcon v-else />{{ assignLabel }}
      </Button>
      <p v-if="!targetReviewer" class="text-xs text-muted-foreground">先在上面选一个审核人。</p>
      <p v-else-if="folderTooMany" class="text-xs text-warn">
        一次最多按文件夹分配 {{ props.folderLimit.toLocaleString() }} 页，请选子文件夹分配。
      </p>
      <p v-else-if="mode === 'files' && selectedBy.unassigned < props.selectedFiles.length" class="text-xs text-muted-foreground">
        其中 {{ props.selectedFiles.length - selectedBy.unassigned }} 个不是待分配的，会跳过（已分配的要用改派）。
      </p>
      <Button
        v-if="mode === 'files' && selectedBy.pending > 0"
        variant="outline"
        :disabled="!targetReviewer || props.busy || props.reassignableCount === 0"
        data-testid="bulk-reassign-button"
        :data-count="props.reassignableCount"
        @click="emit('reassignSelected')"
      >
        <RotateCcwIcon />改派给{{ targetReviewer?.display_name ?? '…' }}（{{ props.reassignableCount }} 个待审）
      </Button>
      <p
        v-if="mode === 'files' && targetReviewer && selectedBy.pending > 0 && alreadyTarget > 0"
        class="text-xs text-muted-foreground"
        data-testid="bulk-reassign-note"
      >
        <template v-if="props.reassignableCount === 0">选中的都已经是{{ targetReviewer.display_name }}在审</template>
        <template v-else>其中 {{ alreadyTarget }} 个已经是{{ targetReviewer.display_name }}在审，不改派</template>
      </p>
      <p
        v-if="props.lastResult"
        class="rounded-md bg-muted/70 px-2.5 py-1.5 text-xs leading-relaxed text-foreground"
        data-testid="last-result"
      >
        {{ props.lastResult }}
      </p>
    </section>

    <!-- ===== 不能再审的人 ===== -->
    <section
      v-if="props.problemReviewers.length"
      class="rounded-lg bg-destructive-soft px-3 py-2.5 text-xs leading-relaxed text-destructive"
      data-testid="problem-reviewers"
    >
      <div class="mb-1 flex items-center gap-1.5 font-medium">
        <AlertTriangleIcon class="size-3.5" />有待审的文件分给了已不能再审的人
      </div>
      <div v-for="p in props.problemReviewers" :key="p.reviewer_id">
        {{ p.display_name }}：{{ p.open }} 个（{{ reviewerProblemsText(p.problems) }}）
      </div>
      <Button variant="outline" size="xs" class="mt-2" data-testid="problem-reassign" @click="emit('openReassign')">
        改派…
      </Button>
    </section>

    <!-- ===== 选中一个文件 ===== -->
    <section v-if="single" class="flex flex-col gap-3 border-t pt-4" data-testid="single-file" :data-file-id="single.file_id">
      <div class="flex gap-3">
        <FileCover
          :file-id="single.file_id"
          :info="images.get(single.file_id)"
          class="w-[60px] shrink-0"
          @broken="images.reportBroken(single.file_id)"
        />
        <div class="min-w-0 flex-1">
          <div class="font-semibold [overflow-wrap:anywhere] text-foreground" data-testid="single-file-name">{{ single.file_name }}</div>
          <div class="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
            <span
              class="inline-flex items-center gap-1 rounded-full px-1.5 leading-5"
              :class="reviewStatusOf(status).badge"
              data-testid="single-file-status"
              :data-status="status"
            >{{ reviewStatusOf(status).label }}</span>
            <span>v{{ current?.version_no ?? single.version_no }} · 第 {{ current?.review_round ?? single.review_round }} 轮</span>
          </div>
          <div class="mt-1 text-xs text-muted-foreground" data-testid="single-file-reviewer">
            <template v-if="reviewerLine">
              {{ reviewerLine.label }}<template v-if="reviewerLine.name">：<span class="text-foreground">{{ reviewerLine.name }}</span></template>
            </template>
            <LoaderCircleIcon v-else-if="reviewsLoading" class="inline size-3 animate-spin" />
          </div>
        </div>
      </div>

      <p v-if="reviewsError" class="text-xs text-destructive">{{ reviewsError }}</p>

      <Button
        v-if="status === 'pending' && openRow"
        variant="outline"
        size="sm"
        :disabled="!targetReviewer || targetReviewer.id === openRow.reviewer_id || props.busy"
        :title="targetReviewer?.id === openRow.reviewer_id ? '已经分给这个人了' : undefined"
        data-testid="single-reassign-button"
        @click="emit('reassignOne', openRow.assignment_id, single)"
      >
        <RotateCcwIcon />改派给{{ targetReviewer?.display_name ?? '…' }}
      </Button>
      <Button
        v-if="status === 'approved'"
        variant="outline"
        size="sm"
        :disabled="props.busy"
        data-testid="reopen-button"
        @click="emit('reopen', single)"
      >
        <RotateCcwIcon />撤回通过
      </Button>

      <div v-if="history.length" data-testid="review-history">
        <h3 class="mb-1.5 text-xs font-medium text-muted-foreground">分配和审核</h3>
        <ol class="flex flex-col gap-1">
          <li
            v-for="r in history"
            :key="r.assignment_id"
            class="flex flex-wrap items-baseline gap-x-1.5 text-xs"
            :class="r.status === 'cancelled' ? 'text-muted-foreground line-through decoration-muted-foreground/50' : 'text-foreground'"
            data-testid="review-row"
            :data-status="r.status"
            :data-result="r.result ?? ''"
          >
            <span class="font-medium">{{ r.reviewer_name }}</span>
            <span>{{ reviewRowLabel(r) }}</span>
            <span v-if="r.version_no" class="font-mono text-muted-foreground">v{{ r.version_no }}</span>
            <span v-if="r.reject_reasons.length" class="text-destructive">{{ r.reject_reasons.map(rejectReasonLabel).join('、') }}</span>
            <span class="ml-auto text-[11px] text-muted-foreground no-underline">{{ formatDateTime(r.reviewed_at ?? r.assigned_at) }}</span>
          </li>
        </ol>
      </div>

      <FileLogList v-if="props.canSeeLogs" :file-id="single.file_id" :refresh-token="props.refreshToken" title="操作日志" />
      <p v-else class="text-xs text-muted-foreground">看操作日志需要「文件目录」菜单。</p>
    </section>

    <p v-else class="text-xs leading-relaxed text-muted-foreground">
      一个文件同时只能分给一个人。改派时原来的分配标记为"已取消"，历史都留着；被驳回的文件重扫后会自动再分给上一次的审核人。
      每次分配都记进文件的操作日志，选中一个文件可以查看。
    </p>
  </div>
</template>
