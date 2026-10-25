<script setup lang="ts">
// 上传重扫件（M2-7）：/scan 页侧栏的待重扫列表、/files 的右键菜单和选择栏都用它。
//
// 显示要重扫的文件：现在的封面、位置、文件夹备注（原件位置）、第几轮、驳回原因和说明、审核人；提示"重扫件不要扫进
// 热文件夹"。"选择重扫件…"：
// rescanInShell → 壳的 `pick_and_rescan`（系统对话框、sha、封面、上传都在壳的 Go 代码里，同步返回）。浏览器里（只是开发
// 工具）不能上传，提示"请在客户端中使用"。
// 结果显示在对话框里：成功（新版本、第几轮、自动分给谁、有没有封面）；`invalid_state` 是"上一次已经成功"；
// `duplicate_content` 是"可能选错了文件"，写出内容相同的那个文件的位置；其他错误照实写。上传期间不能关闭，
// 显示进行到哪一步（核对文件、生成封面、上传的百分比）。
// 选的文件不完整（扫描软件还在写）、扩展名不对时不发请求，直接提示。
// 结束后发 `finished`，调用方刷新列表（SSE 的 files.changed 随后也会到）。
//
// 从 /files 打开时 target 里没有驳回记录：按所在文件夹翻待重扫列表找（lib/rescan.ts 的 findRescan）；查不到说明它
// 已经不是已驳回了，查失败时只说"读不到驳回原因"，照样可以上传。
import { CheckCircle2Icon, FileWarningIcon, LoaderCircleIcon, ScanLineIcon, TriangleAlertIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import type { LastReject } from '@/api/types'
import FileCover from '@/components/files/FileCover.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { CLIENT_TOO_OLD, isShell, SHELL_ONLY, shellHas } from '@/lib/client'
import { formatDateTime } from '@/lib/format'
import { formatBytes, reviewStatusOf } from '@/lib/files'
import {
  describeRescanError,
  findRescan,
  reassignText,
  type RescanBytes,
  type RescanLookup,
  type RescanMessage,
  type RescanOutcome,
  type RescanPhase,
  type RescanTarget,
  rescanInShell,
  withinProject,
} from '@/lib/rescan'
import { rejectReasonLabel } from '@/lib/reviews'
import { useImageInfoStore } from '@/stores/image-info'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  target: RescanTarget | null
  /** 驳回原因还不知道时（/files 打开的），按文件夹去待重扫列表里找。 */
  projectId: string | null
}>()

const emit = defineEmits<{ finished: [outcome: RescanOutcome, message: RescanMessage | null] }>()

const images = useImageInfoStore()
/** 重扫件由壳选文件、上传（能力 rescan）；浏览器里、客户端没有这项能力时不能重扫。 */
const shellCanRescan = shellHas('rescan')

type State = 'idle' | 'working' | 'done' | 'failed'
const state = ref<State>('idle')
const phase = ref<RescanPhase | null>(null)
/** 上传的进度。 */
const bytes = ref<RescanBytes | null>(null)
const outcome = ref<RescanOutcome | null>(null)
const message = ref<RescanMessage | null>(null)
/** 打开时补查的驳回记录（target 里没有时）：null 不用查；'loading' 正在查；否则是查的结果。 */
const looked = ref<RescanLookup | 'loading' | null>(null)
let seq = 0

const busy = computed(() => state.value === 'working')

watch(open, (v) => {
  if (!v) return
  seq++
  state.value = 'idle'
  phase.value = null
  bytes.value = null
  outcome.value = null
  message.value = null
  looked.value = null
  const t = props.target
  if (!t) return
  images.request([t.file_id])
  if (t.last_reject === undefined && props.projectId) void lookUp(t, props.projectId, seq)
})

/** 在所在文件夹的待重扫列表里找这个文件（跟着 next_cursor 翻，越过这个文件夹就停）。 */
async function lookUp(t: RescanTarget, projectId: string, my: number) {
  looked.value = 'loading'
  const r = await findRescan(projectId, t.folder_id, t.file_id)
  if (my === seq) looked.value = r
}

const found = computed(() => (looked.value && looked.value !== 'loading' && looked.value.status === 'found' ? looked.value.item : null))
const lookupState = computed(() => (looked.value === 'loading' ? 'loading' : (looked.value?.status ?? null)))

/** 最近一次驳回；null 表示没有或还不知道（查不到、查失败时不猜）。 */
const lastReject = computed<LastReject | null>(() => {
  const t = props.target
  if (!t) return null
  if (t.last_reject !== undefined) return t.last_reject
  return found.value?.last_reject ?? null
})
const location = computed(() => {
  const t = props.target
  if (!t) return ''
  const p = t.folder_path ?? (found.value ? withinProject(found.value.folder_path) : '')
  return p ? `${p}/${t.file_name}` : t.file_name
})
const remark = computed(() => props.target?.folder_remark ?? found.value?.folder_remark ?? null)
const reasonsText = computed(() => lastReject.value?.reasons.map(rejectReasonLabel).join('、') ?? '')

const PHASE_TEXT: Record<RescanPhase, string> = {
  picking: '在系统对话框里选择重扫件…',
  hashing: '正在核对文件（是否写完）…',
  cover: '正在生成封面…',
  uploading: '正在上传…',
  retry_without_cover: '服务器没收封面，去掉封面重新上传…',
  retry_conflict: '版本号刚被改了，再试一次…',
}

const progressText = computed(() => {
  const p = phase.value
  if (!p) return '请稍候…'
  const b = bytes.value
  if ((p === 'uploading' || p === 'retry_without_cover') && b && b.size > 0) {
    const pct = Math.min(100, Math.floor((b.sent / b.size) * 100))
    return `${PHASE_TEXT[p].replace(/…$/, '')} ${pct}%（${formatBytes(b.sent)} / ${formatBytes(b.size)}）`
  }
  return PHASE_TEXT[p]
})

function onOpenChange(v: boolean) {
  if (busy.value && !v) return
  open.value = v
}

function pick() {
  if (busy.value || !props.target) return
  if (shellCanRescan) {
    void run()
    return
  }
  state.value = 'failed'
  const title = isShell() ? CLIENT_TOO_OLD : SHELL_ONLY
  message.value = { tone: 'error', title, detail: null, location: null, canRetry: false, refresh: false }
}

async function run() {
  const t = props.target
  if (!t || busy.value) return
  const my = seq
  state.value = 'working'
  message.value = null
  outcome.value = null
  phase.value = null
  bytes.value = null
  const onPhase = (p: RescanPhase, b?: RescanBytes) => {
    if (my !== seq) return
    phase.value = p
    bytes.value = b ?? null
  }
  let out: RescanOutcome
  try {
    out = await rescanInShell(t.file_id, onPhase)
  } catch (e) {
    // 壳的命令本身失败（文件读不了、正在传另一张……）。
    const m = describeRescanError(e, t.file_id, t.version_no)
    if (my === seq) {
      state.value = 'failed'
      message.value = m
    }
    return
  }
  if (my !== seq) return
  phase.value = null
  outcome.value = out
  if (out.kind === 'cancelled') {
    state.value = 'idle'
    return
  }
  if (out.kind === 'done') {
    state.value = 'done'
    images.invalidate([t.file_id])
    emit('finished', out, null)
    return
  }
  const m = describeRescanError(out.error, t.file_id, t.version_no)
  message.value = m
  state.value = m.tone === 'ok' ? 'done' : 'failed'
  if (m.refresh) images.invalidate([t.file_id])
  emit('finished', out, m)
}

const done = computed(() => (outcome.value?.kind === 'done' ? outcome.value : null))
const coverNote = computed(() => {
  const o = done.value
  if (!o) return null
  if (o.cover.status === 'included') return '带封面'
  if (o.cover.status === 'rejected') return `无封面（服务器没收封面：${o.cover.reason ?? ''}）`
  return `无封面（生成不了：${o.cover.reason ?? '原因不明'}）`
})
</script>

<template>
  <Dialog :open="open" @update:open="onOpenChange">
    <DialogContent
      class="sm:max-w-lg"
      :show-close-button="!busy"
      data-testid="rescan-dialog"
      :data-state="state"
      :data-file-id="target?.file_id"
      @escape-key-down="busy && $event.preventDefault()"
      @pointer-down-outside="busy && $event.preventDefault()"
    >
      <DialogHeader>
        <DialogTitle>上传重扫件</DialogTitle>
        <DialogDescription>
          重扫件作为新版本（v{{ (target?.version_no ?? 0) + 1 }}）追加到原来的文件，文件名、位置和归类都不变，审核轮次加 1。
        </DialogDescription>
      </DialogHeader>

      <div v-if="target" class="flex gap-3" data-testid="rescan-target">
        <FileCover :file-id="target.file_id" :info="images.get(target.file_id)" class="w-20 shrink-0" />
        <div class="min-w-0 flex-1 space-y-1 text-sm">
          <div class="truncate font-medium text-foreground" :title="location">{{ target.file_name }}</div>
          <div class="truncate text-xs text-muted-foreground" :title="location" data-testid="rescan-location">{{ location }}</div>
          <div class="text-xs" data-testid="rescan-remark">
            <span class="text-muted-foreground">原件位置：</span>
            <span v-if="remark" class="text-foreground">{{ remark }}</span>
            <span v-else class="text-muted-foreground">（文件夹没有备注）</span>
          </div>
          <div class="text-xs text-muted-foreground">
            {{ done ? '重扫前：' : '' }}第 {{ target.review_round }} 轮 · {{ done ? '' : '现在是 ' }}v{{ target.version_no }}
            <template v-if="lastReject"> · {{ lastReject.reviewer_name }} {{ formatDateTime(lastReject.reviewed_at) }} 驳回</template>
          </div>
          <div v-if="lookupState === 'loading'" class="flex items-center gap-1 text-xs text-muted-foreground" data-testid="rescan-lookup-loading">
            <LoaderCircleIcon class="size-3 animate-spin" />正在读取驳回原因…
          </div>
          <div v-else-if="lastReject" class="rounded-md bg-destructive-soft px-2 py-1 text-xs text-destructive" data-testid="rescan-reasons">
            驳回：{{ reasonsText }}
            <div v-if="lastReject.comment" class="mt-0.5 whitespace-pre-wrap text-foreground/80">说明：{{ lastReject.comment }}</div>
          </div>
          <div v-else-if="lookupState === 'missing'" class="rounded-md bg-warn-soft px-2 py-1 text-xs text-warn" data-testid="rescan-not-rejected">
            这个文件已经不在待重扫列表里了（可能刚被重扫过），上传多半会提示它已经不是"已驳回"。
          </div>
          <div v-else-if="lookupState === 'error'" class="rounded-md bg-muted px-2 py-1 text-xs text-muted-foreground" data-testid="rescan-lookup-error">
            读不到驳回原因（{{ looked && looked !== 'loading' && looked.status === 'error' ? looked.message : '' }}），仍然可以上传。
          </div>
          <div v-else-if="!lastReject && (target.last_reject === null || lookupState === 'found')" class="text-xs text-muted-foreground" data-testid="rescan-no-reject">
            没有找到驳回记录。
          </div>
        </div>
      </div>

      <div class="flex gap-2 rounded-md bg-warn-soft px-3 py-2 text-xs leading-relaxed text-foreground/85" data-testid="rescan-hotfolder-hint">
        <TriangleAlertIcon class="mt-0.5 size-3.5 shrink-0 text-warn" />
        <div>
          <span class="font-medium text-warn">重扫件不要扫进热文件夹。</span>
          热文件夹会把它当成新的一页，传成另一个文件，这一页仍是"已驳回"。请扫到别的目录，再在这里选择这一张。
        </div>
      </div>

      <!-- 进行中 -->
      <div v-if="state === 'working'" class="flex items-center gap-2 text-sm text-muted-foreground" data-testid="rescan-progress">
        <LoaderCircleIcon class="size-4 animate-spin" /><span data-testid="rescan-progress-text" :data-phase="phase ?? ''">{{ progressText }}</span>
      </div>

      <!-- 成功 -->
      <div
        v-else-if="done"
        class="flex gap-2 rounded-md bg-ok-soft px-3 py-2.5 text-sm"
        role="status"
        data-testid="rescan-result"
        data-result="done"
      >
        <CheckCircle2Icon class="mt-0.5 size-4 shrink-0 text-ok" />
        <div class="min-w-0 space-y-0.5">
          <div class="font-medium text-ok" data-testid="rescan-result-title">
            已重扫：现在是 v{{ done.result.version_no }}，{{ reviewStatusOf(done.result.review_status).label }}（第 {{ done.result.review_round }} 轮）
          </div>
          <div class="text-foreground/85" data-testid="rescan-reassign">{{ reassignText(done.result, lastReject) }}</div>
          <div class="truncate text-xs text-muted-foreground" :title="done.localName">
            {{ done.localName }} · {{ coverNote }}<template v-if="done.result.warnings.includes('low_dpi')"> · DPI {{ done.result.dpi }}，低于 200</template>
          </div>
        </div>
      </div>

      <!-- 失败，或"上一次已经成功" -->
      <div
        v-else-if="message"
        class="flex gap-2 rounded-md px-3 py-2.5 text-sm"
        :class="message.tone === 'ok' ? 'bg-info-soft' : message.tone === 'warn' ? 'bg-warn-soft' : 'bg-destructive-soft'"
        role="alert"
        data-testid="rescan-result"
        :data-result="outcome?.kind === 'failed' ? outcome.error.code : 'shell_error'"
      >
        <FileWarningIcon
          class="mt-0.5 size-4 shrink-0"
          :class="message.tone === 'ok' ? 'text-info' : message.tone === 'warn' ? 'text-warn' : 'text-destructive'"
        />
        <div class="min-w-0 space-y-0.5">
          <div
            class="font-medium"
            :class="message.tone === 'ok' ? 'text-info' : message.tone === 'warn' ? 'text-warn' : 'text-destructive'"
            data-testid="rescan-result-title"
          >
            {{ message.title }}
          </div>
          <div v-if="message.location" class="text-foreground" data-testid="rescan-duplicate-of">
            内容相同的文件：<span class="font-medium">{{ message.location }}</span>
          </div>
          <div v-if="message.detail" class="text-foreground/80">{{ message.detail }}</div>
          <div v-if="outcome?.kind === 'failed' && outcome.localName" class="truncate text-xs text-muted-foreground">
            选的文件：{{ outcome.localName }}
          </div>
        </div>
      </div>

      <DialogFooter>
        <template v-if="state === 'done' || (state === 'failed' && message && !message.canRetry)">
          <Button data-testid="rescan-close" @click="open = false">关闭</Button>
        </template>
        <template v-else>
          <Button variant="outline" :disabled="busy" @click="open = false">取消</Button>
          <Button :disabled="busy || !target" data-testid="rescan-pick" @click="pick">
            <ScanLineIcon />{{ state === 'failed' ? '重新选择…' : '选择重扫件…' }}
          </Button>
        </template>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
