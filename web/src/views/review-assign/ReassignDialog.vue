<script setup lang="ts">
// 改派（M2-5，用户决定 Q5）：当前文件夹整棵子树里的 open 分配（`GET /api/projects/{pid}/review-assignments`），
// keyset 分页、每页 500 个，"加载更多"接着取（按 file_id 去重，M2-2 须知）。
// - 上面一排是范围里有待审的审核人和个数（不受筛选影响），点一个只看他的；已不能再审这个项目的人
//   （停用、没有审核菜单、失去项目授权，`problems`）标红并写明原因，他名下的行整行高亮。
// - 改派目标从"可以分给的人"里选（和右栏同一份）。三种做法：
//   1. 一行的"改派"：单个改派 `POST /api/review-assignments/{id}/reassign`；
//   2. 勾选几行 → "改派选中的"：批量改派（file_ids，每 500 个一批；中途失败时只取消已成功那几批的勾选）；
//   3. 筛选了某人时 → "把他在这里的全部待审改派"：按子树改派（from_reviewer_id + folder_id，最多 1 万个），先确认；
//      确认框记下打开时的文件夹、原审核人、目标，范围变了就不发请求。
// - 对话框跟着当前文件夹：页面的文件夹一变（点树、后退、当前文件夹被删回到上一级）就关闭（审查第 1 轮）。
// - 不做取消分配（用改派代替，Q5）。`refreshToken` 变了（SSE）按已加载的行数重新取（超过 2000 行时分几页取），
//   勾选保留仍在的；勾选的行不在了（刚被审完、改派、删除）时提示。
// - 错误只提示一次：请求都声明自己处理错误（handle: HANDLE_ALL），全局已经处理过的（401、403 forbidden、426）不再提示。
import { AlertTriangleIcon, LoaderCircleIcon, RotateCcwIcon } from '@lucide/vue'
import { computed, ref, shallowRef, watch } from 'vue'
import { toast } from 'vue-sonner'

import { errorMessage, HANDLE_ALL, handledGlobally, isApiError, isClientError, toastOpError } from '@/api/http'
import {
  listOpenAssignments,
  MAX_REVIEW_FILE_IDS,
  MAX_REVIEW_FOLDER_FILES,
  reassignMany,
  reassignOne,
} from '@/api/reviews'
import type { AssignmentReviewer, OpenAssignment, OpenAssignments, ProjectReviewer } from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { formatDateTime } from '@/lib/format'
import { reviewerProblemsText } from '@/lib/reviews'
import { chunk } from '@/lib/utils'

import { reviewOpErrorText } from './review-errors'

const open = defineModel<boolean>('open', { required: true })
/** 改派目标（和右栏共用）。 */
const target = defineModel<string | null>('target', { required: true })

const props = defineProps<{
  projectId: string
  folder: { id: string; name: string } | null
  /** 可以分给的人（改派目标）。 */
  targets: ProjectReviewer[] | null
  refreshToken: number
}>()

const emit = defineEmits<{
  /** 改派成功：文件所在的文件夹；`subtree` 为 true 时是按子树改派的那个文件夹（整棵子树都可能变了）。 */
  changed: [folderIds: string[], subtree: boolean]
}>()

/** "加载更多"每页的行数；接口一页最多 2000。 */
const PAGE = 500
const MAX_LIMIT = 2000

const reviewers = ref<AssignmentReviewer[]>([])
const items = shallowRef<OpenAssignment[]>([])
const total = ref(0)
const nextCursor = ref<string | null>(null)
const loading = ref(false)
const loadError = ref<string | null>(null)
const filter = ref<string | null>(null)
const checked = shallowRef<ReadonlySet<string>>(new Set())
const busy = ref(false)
let seq = 0

const problemOf = computed(() => new Map(reviewers.value.map((r) => [r.reviewer_id, r])))
const problemReviewers = computed(() => reviewers.value.filter((r) => !r.can_review))
/** `演示审核员2（已停用）名下有 5 个待审；……` */
const problemSummary = computed(() =>
  problemReviewers.value
    .map((p) => `${p.display_name}（${reviewerProblemsText(p.problems)}）名下有 ${p.open} 个待审`)
    .join('；'),
)
const filterReviewer = computed(() => reviewers.value.find((r) => r.reviewer_id === filter.value) ?? null)
const targetReviewer = computed(() => props.targets?.find((r) => r.id === target.value) ?? null)
const checkedRows = computed(() => items.value.filter((r) => checked.value.has(r.assignment_id)))
const allChecked = computed(() => items.value.length > 0 && checkedRows.value.length === items.value.length)

function uncheck(ids: Iterable<string>) {
  const next = new Set(checked.value)
  for (const id of ids) next.delete(id)
  checked.value = next
}

/**
 * 加载列表。`keep`：按已加载的行数重新取（实时刷新、操作之后），超过一页上限时分几页取，按 file_id 去重；
 * `notify`：勾选的行不在了时提示（只在实时刷新时，自己的操作不提示）。
 */
async function load({ keep = false, notify = false } = {}) {
  const folder = props.folder
  if (!folder) return
  const my = ++seq
  loading.value = true
  loadError.value = null
  const want = keep ? Math.max(PAGE, items.value.length) : PAGE
  try {
    const rows: OpenAssignment[] = []
    const seen = new Set<string>()
    let cursor: string | null = null
    let last: OpenAssignments | null = null
    do {
      last = await listOpenAssignments(
        props.projectId,
        { folder_id: folder.id, reviewer_id: filter.value, limit: Math.min(MAX_LIMIT, want - rows.length), cursor },
        HANDLE_ALL,
      )
      if (my !== seq) return
      for (const r of last.items) {
        if (seen.has(r.file_id)) continue
        seen.add(r.file_id)
        rows.push(r)
      }
      cursor = last.next_cursor
    } while (cursor && rows.length < want)
    reviewers.value = last.reviewers
    total.value = last.total
    items.value = rows
    nextCursor.value = cursor
    // 勾选只保留仍在列表里的；不在了的（刚被审完、改派、删除）在实时刷新时告诉用户。
    const present = new Set(rows.map((r) => r.assignment_id))
    const kept = [...checked.value].filter((id) => present.has(id))
    const lost = checked.value.size - kept.length
    if (lost) checked.value = new Set(kept)
    if (notify && lost) toast.info(`勾选的 ${lost} 个已经不在列表里（刚被审完、改派或删除），已取消勾选`)
    // 筛选的人已经没有待审了：回到全部。
    if (filter.value && !last.reviewers.some((r) => r.reviewer_id === filter.value)) filter.value = null
  } catch (e) {
    if (my !== seq) return
    loadError.value = errorMessage(e)
  } finally {
    if (my === seq) loading.value = false
  }
}

async function loadMore() {
  const folder = props.folder
  const cursor = nextCursor.value
  if (!folder || !cursor || loading.value) return
  const my = ++seq
  loading.value = true
  try {
    const res = await listOpenAssignments(
      props.projectId,
      { folder_id: folder.id, reviewer_id: filter.value, limit: PAGE, cursor },
      HANDLE_ALL,
    )
    if (my !== seq) return
    // 翻页期间移动了文件可能重复（design §2.2），按 file_id 去重。
    const seen = new Set(items.value.map((r) => r.file_id))
    items.value = [...items.value, ...res.items.filter((r) => !seen.has(r.file_id))]
    reviewers.value = res.reviewers
    total.value = res.total
    nextCursor.value = res.next_cursor
  } catch (e) {
    if (my !== seq) return
    if (!handledGlobally(e)) toast.error(errorMessage(e))
  } finally {
    if (my === seq) loading.value = false
  }
}

watch(open, (v) => {
  if (v) {
    const wasFiltered = filter.value !== null
    filter.value = null
    checked.value = new Set()
    items.value = []
    reviewers.value = []
    total.value = 0
    nextCursor.value = null
    // 上次关闭时有筛选的，清掉筛选会触发下面的 watch 去加载，这里不重复。
    if (!wasFiltered) void load()
  } else {
    seq++
    confirmAll.value = false
  }
})
watch(filter, () => {
  if (!open.value) return
  checked.value = new Set()
  void load()
})
watch(
  () => props.refreshToken,
  () => {
    if (open.value && !busy.value) void load({ keep: true, notify: true })
  },
)
// 页面的当前文件夹变了：范围不再是标题上写的那个，关掉（审查第 1 轮）。
watch(
  () => props.folder?.id ?? null,
  (id, old) => {
    if (open.value && id !== old) open.value = false
  },
)

function toggle(id: string, on: boolean) {
  const next = new Set(checked.value)
  if (on) next.add(id)
  else next.delete(id)
  checked.value = next
}
function toggleAll(on: boolean) {
  checked.value = on ? new Set(items.value.map((r) => r.assignment_id)) : new Set()
}

// ---------- 改派 ----------

async function reassignRow(row: OpenAssignment) {
  const to = targetReviewer.value
  if (!to || busy.value) return
  busy.value = true
  try {
    const res = await reassignOne(row.assignment_id, to.id, HANDLE_ALL)
    uncheck([row.assignment_id])
    if (res.changed) {
      toast.success(`已把「${row.file_name}」改派给 ${to.display_name}`)
      emit('changed', [row.folder_id], false)
    } else toast.info(`「${row.file_name}」原来就分给 ${to.display_name}，没有改动`)
  } catch (e) {
    toastOpError(
      e,
      isApiError(e, 'invalid_state') ? '这一行的分配已经变了（可能刚审完或被别人改派），列表已刷新' : reviewOpErrorText(e),
    )
  } finally {
    busy.value = false
    void load({ keep: true })
  }
}

async function reassignChecked() {
  const to = targetReviewer.value
  const rows = checkedRows.value
  if (!to || !rows.length || busy.value) return
  busy.value = true
  let reassigned = 0
  let skipped = 0
  let done = 0
  const folders = new Set<string>()
  try {
    for (const part of chunk(rows, MAX_REVIEW_FILE_IDS)) {
      const res = await reassignMany(props.projectId, { to_reviewer_id: to.id, file_ids: part.map((r) => r.file_id) }, HANDLE_ALL)
      reassigned += res.reassigned
      skipped += res.skipped
      done += part.length
      part.forEach((r) => folders.add(r.folder_id))
      // 这一批已经处理了：取消它们的勾选（失败时剩下的保持勾选，可以重试）。
      uncheck(part.map((r) => r.assignment_id))
    }
    toast.success(
      `已改派 ${reassigned} 个给 ${to.display_name}${skipped ? `，跳过了 ${skipped} 个` : ''}`,
      skipped ? { description: '跳过的已经分给了他，或者刚被审完、改派' } : undefined,
    )
  } catch (e) {
    toastOpError(
      e,
      reviewOpErrorText(e),
      done
        ? `前 ${done} 个已处理（改派 ${reassigned} 个），没有处理的 ${rows.length - done} 个仍保持勾选`
        : undefined,
    )
  } finally {
    busy.value = false
    if (reassigned) emit('changed', [...folders], false)
    void load({ keep: true })
  }
}

/** "把他在这里的全部待审改派"的确认框：打开时记下范围（文件夹、原审核人、目标、个数）。 */
const confirmAll = ref(false)
const allScope = ref<{
  folderId: string
  folderName: string
  fromId: string
  fromName: string
  toId: string
  toName: string
  count: number
} | null>(null)
const allTooMany = computed(() => (filterReviewer.value?.open ?? 0) > MAX_REVIEW_FOLDER_FILES)

function askAll() {
  const to = targetReviewer.value
  const from = filterReviewer.value
  const folder = props.folder
  if (!to || !from || !folder) return
  allScope.value = {
    folderId: folder.id,
    folderName: folder.name,
    fromId: from.reviewer_id,
    fromName: from.display_name,
    toId: to.id,
    toName: to.display_name,
    count: from.open,
  }
  confirmAll.value = true
}

async function reassignAllOfFilter() {
  const scope = allScope.value
  if (!scope || busy.value) return
  if (!open.value || props.folder?.id !== scope.folderId) {
    // 范围已经变了（对话框关了、换了文件夹）：不发请求。
    confirmAll.value = false
    return
  }
  busy.value = true
  try {
    const res = await reassignMany(
      props.projectId,
      { to_reviewer_id: scope.toId, from_reviewer_id: scope.fromId, folder_id: scope.folderId },
      HANDLE_ALL,
    )
    toast.success(
      `已把 ${scope.fromName} 的 ${res.reassigned} 个待审改派给 ${scope.toName}${res.skipped ? `，跳过了 ${res.skipped} 个` : ''}`,
    )
    confirmAll.value = false
    if (res.reassigned) emit('changed', [scope.folderId], true)
  } catch (e) {
    toastOpError(e, reviewOpErrorText(e))
    // 4xx（文件夹没了、超过上限、目标不合格）：前提不成立，关掉确认框；网络错误、5xx 留着可以重试。
    if (isClientError(e)) confirmAll.value = false
  } finally {
    busy.value = false
    void load()
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="flex max-h-[88vh] flex-col gap-3 sm:max-w-4xl" data-testid="reassign-dialog" :data-state="loading ? 'loading' : 'ready'">
      <DialogHeader>
        <DialogTitle>改派 · {{ props.folder?.name }}（含子文件夹）</DialogTitle>
        <DialogDescription>
          这里只列出待审核的分配。改派后原来的分配标记为"已取消"，历史都留着；文件从原审核人的待办里消失。
        </DialogDescription>
      </DialogHeader>

      <!-- 范围里有待审的审核人（点一个只看他的） -->
      <div class="flex flex-wrap items-center gap-1.5" data-testid="assignment-reviewers">
        <button
          type="button"
          class="rounded-full px-2.5 text-xs leading-6"
          :class="filter === null ? 'bg-foreground text-background' : 'bg-muted text-foreground hover:bg-muted/70'"
          data-testid="assignment-filter-all"
          @click="filter = null"
        >
          全部 {{ reviewers.reduce((n, r) => n + r.open, 0) }}
        </button>
        <button
          v-for="r in reviewers"
          :key="r.reviewer_id"
          type="button"
          class="rounded-full px-2.5 text-xs leading-6"
          :class="[
            filter === r.reviewer_id
              ? r.can_review
                ? 'bg-foreground text-background'
                : 'bg-destructive text-white'
              : r.can_review
                ? 'bg-muted text-foreground hover:bg-muted/70'
                : 'bg-destructive-soft text-destructive shadow-[0_0_0_1px_var(--destructive)]',
          ]"
          :title="r.can_review ? undefined : `不能再审这个项目：${reviewerProblemsText(r.problems)}`"
          data-testid="assignment-reviewer"
          :data-reviewer-id="r.reviewer_id"
          :data-can-review="r.can_review"
          @click="filter = r.reviewer_id"
        >
          {{ r.display_name }} {{ r.open }}
          <template v-if="!r.can_review"> · {{ reviewerProblemsText(r.problems) }}</template>
        </button>
      </div>

      <p
        v-if="problemReviewers.length"
        class="flex items-start gap-1.5 rounded-md bg-destructive-soft px-3 py-2 text-xs leading-relaxed text-destructive"
        data-testid="assignment-problems"
      >
        <AlertTriangleIcon class="mt-0.5 size-3.5 shrink-0" />
        <span>{{ problemSummary }}。他们已经审不了这些文件，请改派给别人。</span>
      </p>

      <!-- 列表 -->
      <div class="min-h-[200px] flex-1 overflow-y-auto rounded-md shadow-[0_0_0_1px_var(--border)]">
        <div
          class="sticky top-0 z-10 grid items-center gap-2 border-b bg-muted/90 px-3 py-1.5 text-xs text-muted-foreground backdrop-blur"
          style="grid-template-columns: 20px minmax(0, 1fr) 52px 180px 104px 76px"
        >
          <Checkbox
            :model-value="allChecked"
            :disabled="!items.length"
            aria-label="全选已加载的"
            data-testid="assignment-check-all"
            @update:model-value="(v) => toggleAll(v === true)"
          />
          <span>文件</span>
          <span>轮次</span>
          <span>审核人</span>
          <span>分配时间</span>
          <span />
        </div>
        <div v-if="loading && !items.length" class="flex items-center justify-center gap-2 py-10 text-sm text-muted-foreground">
          <LoaderCircleIcon class="size-4 animate-spin" />正在加载…
        </div>
        <p v-else-if="loadError" class="py-8 text-center text-sm text-destructive">{{ loadError }}</p>
        <p v-else-if="!items.length" class="py-10 text-center text-sm text-muted-foreground" data-testid="assignment-empty">
          这个文件夹里没有待审核的分配
        </p>
        <div
          v-for="row in items"
          :key="row.assignment_id"
          class="grid items-center gap-2 border-b border-border/60 px-3 py-1.5 text-[13px]"
          :class="row.reviewer_can_review ? (checked.has(row.assignment_id) ? 'bg-info-soft' : '') : 'bg-destructive-soft/70'"
          style="grid-template-columns: 20px minmax(0, 1fr) 52px 180px 104px 76px"
          data-testid="assignment-row"
          :data-assignment-id="row.assignment_id"
          :data-file-id="row.file_id"
          :data-reviewer-id="row.reviewer_id"
          :data-can-review="row.reviewer_can_review"
        >
          <Checkbox
            :model-value="checked.has(row.assignment_id)"
            :aria-label="`选择 ${row.file_name}`"
            data-testid="assignment-check"
            @update:model-value="(v) => toggle(row.assignment_id, v === true)"
          />
          <span class="min-w-0">
            <span class="block truncate text-foreground" :title="row.file_name">{{ row.file_name }}</span>
            <span class="block truncate text-[11px] text-muted-foreground" :title="`/${row.folder_path}`">/{{ row.folder_path }}</span>
          </span>
          <span class="text-xs text-muted-foreground">第 {{ row.review_round }} 轮</span>
          <span class="min-w-0">
            <span class="block truncate" :class="row.reviewer_can_review ? 'text-foreground' : 'font-medium text-destructive'">
              {{ row.reviewer_name }}
            </span>
            <span
              v-if="!row.reviewer_can_review"
              class="block truncate text-[11px] text-destructive"
              data-testid="assignment-problem"
            >{{ reviewerProblemsText(problemOf.get(row.reviewer_id)?.problems ?? []) || '已不能再审' }}</span>
          </span>
          <span class="text-xs text-muted-foreground">{{ formatDateTime(row.assigned_at) }}</span>
          <Button
            variant="outline"
            size="xs"
            :disabled="!targetReviewer || targetReviewer.id === row.reviewer_id || busy"
            data-testid="assignment-reassign"
            @click="reassignRow(row)"
          >
            <RotateCcwIcon />改派
          </Button>
        </div>
        <div v-if="items.length" class="flex items-center justify-between px-3 py-2 text-xs text-muted-foreground">
          <span data-testid="assignment-loaded">已加载 {{ items.length }} / {{ total }} 个</span>
          <Button v-if="nextCursor" variant="ghost" size="xs" :disabled="loading" @click="loadMore">加载更多</Button>
        </div>
      </div>

      <!-- 改派给谁、怎么改派 -->
      <div class="flex flex-wrap items-center gap-2 border-t pt-3" data-testid="reassign-footer">
        <label class="flex items-center gap-2 text-sm text-muted-foreground">
          改派给
          <select
            v-model="target"
            class="h-8 min-w-40 rounded-md border bg-background px-2 text-sm text-foreground outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
            data-testid="reassign-target"
          >
            <option :value="null" disabled>选择审核人…</option>
            <option v-for="r in props.targets ?? []" :key="r.id" :value="r.id">
              {{ r.display_name }}（待办 {{ r.open }}）
            </option>
          </select>
        </label>
        <div class="flex-1" />
        <Button
          variant="outline"
          size="sm"
          :disabled="!targetReviewer || !checkedRows.length || busy"
          data-testid="reassign-checked"
          @click="reassignChecked"
        >
          改派选中的 {{ checkedRows.length }} 个
        </Button>
        <Button
          v-if="filterReviewer"
          size="sm"
          :disabled="!targetReviewer || targetReviewer.id === filterReviewer.reviewer_id || busy || allTooMany"
          :title="allTooMany ? `一次最多改派 ${MAX_REVIEW_FOLDER_FILES} 个，请选子文件夹` : undefined"
          data-testid="reassign-all-of"
          @click="askAll"
        >
          把 {{ filterReviewer.display_name }} 在这里的全部 {{ filterReviewer.open }} 个改派
        </Button>
      </div>

      <ConfirmDialog
        v-model:open="confirmAll"
        :title="`把 ${allScope?.fromName ?? ''} 的待审改派给 ${allScope?.toName ?? ''}？`"
        :description="`「${allScope?.folderName ?? ''}」（含子文件夹）里分给 ${allScope?.fromName ?? ''} 的全部 ${allScope?.count ?? 0} 个待审核文件，都改派给 ${allScope?.toName ?? ''}。`"
        confirm-text="改派"
        :loading="busy"
        @confirm="reassignAllOfFilter"
      />
    </DialogContent>
  </Dialog>
</template>
