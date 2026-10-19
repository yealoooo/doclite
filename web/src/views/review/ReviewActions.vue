<script setup lang="ts">
// 审核工作台右栏底部的操作区（M2-6）：驳回原因面板、通过和驳回按钮、撤回通过，以及不能操作时的说明。
//
// - 驳回原因只能从项目的原因列表（review-settings）里选（Q4）：内置的 8 个是代码，显示中文；项目自定义的原样显示。
//   至少选一个；选了"其他"（other）时说明必填；说明去掉首尾空白后最多 500 个字（按字符算，和 server 一样），
//   不能有控制字符（换行、制表符可以）。不合规则时在面板里提示，不发请求。
// - 面板平时收起，只显示原因（点一个原因也会打开面板并勾上它）；R 打开（Q3）。打开时：1–9 勾选第几个原因、
//   Tab 到说明框、Enter 确认（说明框里 Shift+Enter 换行，输入法组字时的 Enter 不算）、Esc 取消。按键由页面统一
//   绑定，调这里暴露的方法（`toggleIndex`、`focusComment`、`confirm`、`cancel`）。
// - 当前文件不是待审时：我通过的、现在仍是这一行结论的（is_current）给"撤回通过"（Q8）；驳回的不能撤回（本期不做
//   撤回驳回）；is_current 为 false 的只是历史，不给任何操作。
// - 换了文件或分配（file_id、assignment_id 变了）、文件不再待审（包括驳回、通过成功之后）、Esc 取消时，收起面板、
//   清空勾选和说明。
// 截图脚本用的属性：data-testid=review-actions（data-file-id、data-panel：open / closed、data-can-act）；
// 原因 data-testid=reject-reason（data-code、data-index、data-checked）；说明 reject-comment；确认 reject-confirm；
// 取消 reject-cancel；提示 reject-error；按钮 review-approve、review-reject、review-reopen；说明文字 review-state-note。
import { CheckIcon, RotateCcwIcon, XIcon } from '@lucide/vue'
import { computed, nextTick, ref, watch } from 'vue'

import type { MyReviewItem } from '@/api/types'
import { Button } from '@/components/ui/button'
import { formatDateTime } from '@/lib/format'
import { OTHER_REJECT_REASON, rejectReasonLabel } from '@/lib/reject-reasons'

const props = defineProps<{
  item: MyReviewItem | null
  /** 项目的驳回原因（null：还在取，或取失败了）。 */
  reasons: string[] | null
  /** 取驳回原因失败的原因（显示"重试"）。 */
  reasonsError: string | null
  commentMax: number
  /** 现在能不能通过、驳回（待审、请求不在途、原图不在加载中）。 */
  canAct: boolean
  /** 显示在按钮上方的提示：不能操作的原因，或者"只看了封面"之类的注意事项。 */
  blockedHint: string | null
  /** 这个文件的请求在途。 */
  busy: boolean
  /** server 返回的驳回错误（400 invalid_input 等），显示在面板里。 */
  serverError: string | null
}>()

const emit = defineEmits<{
  approve: []
  reject: [payload: { reasons: string[]; comment: string | null }]
  reopen: []
  retryReasons: []
  /**
   * 用户要打开驳回面板（点"驳回重扫"，或面板收着时点了某个原因 reason）：由页面决定要不要先确认（只看了封面时），
   * 再调 `openPanel(reason)`。
   */
  openRequest: [reason: string | null]
}>()

const open = ref(false)
const selected = ref<string[]>([])
const comment = ref('')
const localError = ref<string | null>(null)
const commentEl = ref<HTMLTextAreaElement | null>(null)
const panelEl = ref<HTMLElement | null>(null)

const isOpenItem = computed(() => props.item?.status === 'open')
const canReopen = computed(
  () =>
    !!props.item &&
    props.item.status === 'done' &&
    props.item.result === 'approved' &&
    props.item.is_current &&
    props.item.review_status === 'approved',
)

// 换了文件，或者同一个文件换了一行分配（驳回后重扫回来是新的一行）：收起面板、清空勾选和说明（验收第 1 轮：
// 同一个文件"回来"之后还留着上一轮的原因，按 Enter 会用它们再驳回一次）。
watch(
  () => `${props.item?.file_id ?? ''}|${props.item?.assignment_id ?? ''}`,
  () => reset(),
)

/** 说明的字数（按字符，和 server 的 chars().count() 一样）。 */
const commentChars = computed(() => Array.from(comment.value.trim()).length)
const needsComment = computed(() => selected.value.includes(OTHER_REJECT_REASON))

/** 打开面板；reason：顺便勾上这个原因（面板收着时点了它）。 */
function openPanel(reason?: string | null) {
  if (!isOpenItem.value || !props.canAct) return
  open.value = true
  localError.value = null
  if (reason && !selected.value.includes(reason)) toggle(reason)
  void nextTick(() => panelEl.value?.focus({ preventScroll: true }))
}

function close() {
  open.value = false
  localError.value = null
}

/** 收起面板，清空勾选和说明（提交成功、换了文件或分配、不再待审时）；不动焦点。 */
function reset() {
  close()
  selected.value = []
  comment.value = ''
}

/** 取消（Esc）：同 reset，另外让焦点离开面板。 */
function cancel() {
  reset()
  ;(document.activeElement as HTMLElement | null)?.blur?.()
}

function toggle(code: string) {
  const i = selected.value.indexOf(code)
  selected.value = i >= 0 ? selected.value.filter((c) => c !== code) : [...selected.value, code]
  localError.value = null
}

/** 1–9：勾选或取消第 index 个原因（从 0 数）。 */
function toggleIndex(index: number) {
  const code = props.reasons?.[index]
  if (!open.value || !code) return
  toggle(code)
}

/** 点原因：面板开着就勾上或取消；没开时请页面打开（打开时勾上它）。 */
function onReasonClick(code: string) {
  if (open.value) toggle(code)
  else if (isOpenItem.value && props.canAct) emit('openRequest', code)
}

function focusComment() {
  if (!open.value) return
  commentEl.value?.focus()
}

// 控制字符：和 server 一致（Rust 的 char::is_control，即 C0、DEL、C1，去掉换行 \n、回车 \r、制表符 \t）。
// eslint-disable-next-line no-control-regex
const CONTROL_CHARS = /[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F-\u009F]/

function validate(): string | null {
  if (selected.value.length === 0) return '至少选一个驳回原因'
  const text = comment.value.trim()
  if (needsComment.value && !text) return '选了“其他”要写说明'
  if (Array.from(text).length > props.commentMax) return `说明最多 ${props.commentMax} 个字（现在 ${Array.from(text).length} 个）`
  if (CONTROL_CHARS.test(text)) return '说明里不能有控制字符'
  return null
}

function confirm() {
  if (!open.value || props.busy || !props.canAct) return
  const err = validate()
  if (err) {
    // 焦点不动（键盘用户按提示 Tab 进说明框；在说明框里按的 Enter 焦点本来就在那里）。
    localError.value = err
    return
  }
  const text = comment.value.trim()
  // 原因按项目列表里的顺序发（看着整齐；server 不在乎顺序）。
  const order = props.reasons ?? []
  const reasons = [...selected.value].sort((a, b) => order.indexOf(a) - order.indexOf(b))
  emit('reject', { reasons, comment: text || null })
}

function isOpen() {
  return open.value && isOpenItem.value
}

// 文件不再是待审了（驳回、通过成功，批量通过了它，刷新后变了）：收起面板，清空勾选和说明。
watch(isOpenItem, (v) => {
  if (!v) reset()
})

function isComment(el: EventTarget | null) {
  return !!el && el === commentEl.value
}

defineExpose({ openPanel, close, reset, cancel, toggleIndex, focusComment, confirm, isOpen, isComment })

const errorText = computed(() => localError.value ?? (open.value ? props.serverError : null))

const stateNote = computed(() => {
  const it = props.item
  if (!it) return null
  if (it.status === 'open') return null
  if (!it.is_current) return '这一行只是历史：文件后来被撤回或改派，现在的结论不是这一行，不能再操作'
  if (it.result === 'approved') {
    if (it.review_status !== 'approved') return `你在 ${formatDateTime(it.reviewed_at)} 通过过；文件现在不是"通过"了`
    return `你在 ${formatDateTime(it.reviewed_at)} 通过了它（v${it.version_no}）`
  }
  if (it.result === 'rejected') return `你在 ${formatDateTime(it.reviewed_at)} 驳回了它，等扫描员重扫（本期不能撤回驳回）`
  return null
})
</script>

<template>
  <div
    class="flex flex-col gap-2 border-t bg-background px-3.5 pt-3 pb-3"
    data-testid="review-actions"
    :data-file-id="props.item?.file_id"
    :data-panel="open ? 'open' : 'closed'"
    :data-can-act="props.canAct ? 'true' : 'false'"
  >
    <template v-if="props.item">
      <!-- 待审：驳回原因 + 通过、驳回 -->
      <template v-if="isOpenItem">
        <div
          ref="panelEl"
          tabindex="-1"
          class="flex flex-col gap-2 rounded-lg outline-none"
          :class="open ? 'bg-destructive-soft/40 p-2.5 ring-1 ring-destructive/20' : ''"
          data-testid="reject-panel"
        >
          <div class="flex flex-col gap-0.5">
            <span class="text-xs font-medium" :class="open ? 'text-destructive' : 'text-muted-foreground'">驳回原因</span>
            <span v-if="open" class="text-2xs text-muted-foreground">按 1–9 勾选 · Tab 写说明 · Enter 确认 · Esc 取消</span>
          </div>
          <div class="flex flex-wrap gap-1.5">
            <span v-if="!props.reasons && props.reasonsError" class="text-xs text-destructive" data-testid="reject-reasons-error">
              取不到原因列表：{{ props.reasonsError }}
              <button type="button" class="ml-1 underline" @mousedown.prevent @click="emit('retryReasons')">重试</button>
            </span>
            <span v-else-if="!props.reasons" class="text-xs text-muted-foreground">正在取原因列表…</span>
            <button
              v-for="(code, i) in props.reasons ?? []"
              :key="code"
              type="button"
              tabindex="-1"
              class="inline-flex h-6 items-center gap-1 rounded-full px-2 text-xs transition-colors"
              :class="
                selected.includes(code)
                  ? 'bg-destructive text-white'
                  : 'bg-muted text-foreground hover:bg-muted-foreground/15 disabled:opacity-50'
              "
              :disabled="!props.canAct"
              data-testid="reject-reason"
              :data-code="code"
              :data-index="i + 1"
              :data-checked="selected.includes(code) ? 'true' : 'false'"
              @mousedown.prevent
              @click="onReasonClick(code)"
            >
              <span
                v-if="open && i < 9"
                class="font-mono text-2xs"
                :class="selected.includes(code) ? 'text-white/80' : 'text-muted-foreground'"
              >{{ i + 1 }}</span>
              {{ rejectReasonLabel(code) }}
            </button>
          </div>
          <template v-if="open">
            <div class="relative">
              <textarea
                ref="commentEl"
                v-model="comment"
                rows="3"
                class="block w-full resize-none rounded-md border bg-background px-2.5 py-1.5 text-compact outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
                :placeholder="needsComment ? '说明（选了“其他”，必填）' : '说明（可选，Shift+Enter 换行）'"
                data-testid="reject-comment"
              />
              <span
                class="absolute right-2 bottom-1 font-mono text-2xs"
                :class="commentChars > props.commentMax ? 'text-destructive' : 'text-muted-foreground'"
              >{{ commentChars }}/{{ props.commentMax }}</span>
            </div>
            <p v-if="errorText" class="text-xs text-destructive" data-testid="reject-error">{{ errorText }}</p>
            <div class="flex gap-1.5">
              <Button variant="outline" size="sm" class="flex-1" data-testid="reject-cancel" @mousedown.prevent @click="cancel()">
                <XIcon />取消<span class="kbd">Esc</span>
              </Button>
              <Button
                variant="destructive"
                size="sm"
                class="flex-1"
                :disabled="props.busy || !props.canAct"
                data-testid="reject-confirm"
                @mousedown.prevent
                @click="confirm()"
              >
                {{ props.busy ? '提交中…' : '确认驳回' }}<span class="kbd">Enter</span>
              </Button>
            </div>
          </template>
        </div>

        <p v-if="props.blockedHint && !open" class="text-center text-2xs text-muted-foreground" data-testid="review-blocked">
          {{ props.blockedHint }}
        </p>
        <div v-if="!open" class="flex gap-1.5">
          <Button
            variant="destructive"
            class="flex-1"
            :disabled="!props.canAct || props.busy"
            data-testid="review-reject"
            @mousedown.prevent
            @click="emit('openRequest', null)"
          >
            驳回重扫<span class="kbd">R</span>
          </Button>
          <Button
            variant="success"
            class="flex-1"
            :disabled="!props.canAct || props.busy"
            data-testid="review-approve"
            @mousedown.prevent
            @click="emit('approve')"
          >
            <CheckIcon />{{ props.busy ? '提交中…' : '通过' }}<span class="kbd">A</span>
          </Button>
        </div>
      </template>

      <!-- 已审、历史 -->
      <template v-else>
        <p class="text-xs leading-5 text-muted-foreground" data-testid="review-state-note">{{ stateNote }}</p>
        <Button
          v-if="canReopen"
          variant="outline"
          size="sm"
          :disabled="props.busy"
          data-testid="review-reopen"
          @mousedown.prevent
          @click="emit('reopen')"
        >
          <RotateCcwIcon />撤回通过
        </Button>
      </template>
    </template>
    <p class="text-center text-2xs leading-4 text-muted-foreground">
      ← → 翻页 · 空格 适合窗口/100% · + − 0 缩放 · [ ] 旋转 · C 对比 · A 通过 · R 驳回
    </p>
  </div>
</template>

<style scoped>
.kbd {
  margin-left: 4px;
  border-radius: 3px;
  padding: 0 4px;
  font-family: var(--font-mono);
  font-size: 10px;
  line-height: 16px;
  background: rgba(0, 0, 0, 0.08);
  opacity: 0.8;
}
</style>
