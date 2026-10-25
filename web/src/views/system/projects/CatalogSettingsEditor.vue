<script setup lang="ts">
// 项目编辑对话框的"著录项和档号"（M3-7，用户决定 Q1、Q2、Q5）：
// - 档号模板：输入框、插入占位符的按钮、实时预览（按 design §2.5 的规则在前端拼，用示例编码，lib/catalog.ts）、
//   用到的层级和分隔符；改模板后提示"已有条目的档号不会自动改"。
// - 改模板时检查已有的归类编码里有没有新模板的分隔符（M3-1 遗留的存疑：编码只在新建、修改时按当时的模板校验，
//   改模板不回溯，档号会分不清段）：有 catalog 菜单、又能进这个项目时，用 archive-categories 逐层取全部编码
//   （最多 2000 个节点）列出冲突的；否则只提示规则。只提示，不挡保存。
// - `catalog.lock_requires_approved`：定稿前要求全部页审核通过。
// - 著录项：增、删、改、上下移动；key、名称、类型（文本、日期、选项）、必填、选项（只有选项类型，一行一个）、
//   最大长度（只有文本，默认 500）、默认值。本地检查只是提示（catalog-draft.ts 的 fieldProblems），server 的 400
//   由对话框显示，`highlightIndex` 标出对应的那一行。
// 草稿（draft）由对话框持有，这里直接改它的字段。
// 截图脚本用的属性：data-testid=catalog-editor；archive-template（输入框）、template-preview（data-ok、data-value）、
// template-error、template-insert（data-token）、template-codes-warning（data-count）、lock-requires-approved；
// add-field；item-field（data-index、data-key、data-type、data-problems、data-highlight）、field-key、field-label、
// field-type（data-type）、field-required、field-options、field-max-length、field-default、field-up、field-down、
// field-remove、field-problem。
import { ArrowDownIcon, ArrowUpIcon, PlusIcon, RotateCcwIcon, Trash2Icon, TriangleAlertIcon } from '@lucide/vue'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

import { listArchiveCategories } from '@/api/catalog'
import type { ItemFieldType } from '@/api/types'
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/native-select'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import {
  categoryTypeLabel,
  CATEGORY_TYPES,
  DATE_FORMATS,
  DEFAULT_ARCHIVE_NO_TEMPLATE,
  DEFAULT_TEXT_MAX_LENGTH,
  ITEM_FIELD_TYPE_LABEL,
  parseArchiveNoTemplate,
  renderArchiveNo,
  SAMPLE_CODES,
  separatorsIn,
} from '@/lib/catalog'
import { useProjectStore } from '@/stores/project'
import { useSessionStore } from '@/stores/session'
import { HANDLE_ALL } from '@/api/http'

import { type CatalogDraft, fieldProblems, newField, optionLines } from './catalog-draft'

const props = defineProps<{
  draft: CatalogDraft
  projectId: string
  /** 读出来时的模板（改了才检查已有编码、提示重新生成）。 */
  savedTemplate: string
  /** server 的错误指向的著录项（第几个，从 0 起）。 */
  highlightIndex: number | null
  /** server 的错误是模板的。 */
  templateHighlight: boolean
}>()

const session = useSessionStore()
const projectStore = useProjectStore()

// ---------- 档号模板 ----------

const SAMPLE_ITEM_NO = 12
const parsed = computed(() => parseArchiveNoTemplate(props.draft.template))
const preview = computed(() => {
  const p = parsed.value
  if (!p.ok) return null
  return renderArchiveNo(p.template, SAMPLE_CODES, SAMPLE_ITEM_NO).value
})
const templateChanged = computed(() => props.draft.template !== props.savedTemplate)

const INSERTS: { token: string; label: string }[] = [
  ...CATEGORY_TYPES.map((t) => ({ token: `{${t}}`, label: categoryTypeLabel(t) })),
  { token: '{item_no:04}', label: '件号（补零到 4 位）' },
  { token: '{item_no}', label: '件号（不补零）' },
]

const templateInput = ref<HTMLInputElement | null>(null)
function insertToken(token: string) {
  const el = templateInput.value
  const v = props.draft.template
  const start = el?.selectionStart ?? v.length
  const end = el?.selectionEnd ?? v.length
  props.draft.template = v.slice(0, start) + token + v.slice(end)
  void nextTick(() => {
    el?.focus()
    const pos = start + token.length
    el?.setSelectionRange(pos, pos)
  })
}

// ---------- 已有归类编码里有没有新模板的分隔符 ----------

interface CodeRow {
  code: string
  name: string
  type: string
}
/** 最多逐层取多少个节点（太多时只检查前面这些）。 */
const MAX_CODES = 2000
const codes = ref<CodeRow[] | null>(null)
const codesState = ref<'idle' | 'loading' | 'ready' | 'partial' | 'unavailable' | 'error'>('idle')
let codesSeq = 0

async function canReadCategories(): Promise<boolean> {
  if (!session.menuCodes.has('catalog')) return false
  if (session.isAdmin) return true
  // 能不能进这个项目：看"我能进的项目"（403 会走全局的无权处理，不能试着调）。
  await projectStore.ensureLoaded().catch(() => null)
  return projectStore.projects.some((p) => p.id === props.projectId)
}

async function loadCodes() {
  const my = ++codesSeq
  codesState.value = 'loading'
  const allowed = await canReadCategories()
  if (my !== codesSeq) return
  if (!allowed) {
    codesState.value = 'unavailable'
    return
  }
  const out: CodeRow[] = []
  const queue: (string | null)[] = [null]
  let partial = false
  try {
    while (queue.length) {
      if (out.length >= MAX_CODES) {
        partial = true
        break
      }
      const batch = queue.splice(0, 4)
      const lists = await Promise.all(batch.map((id) => listArchiveCategories(props.projectId, id, HANDLE_ALL)))
      if (my !== codesSeq) return
      for (const l of lists) {
        for (const c of l.items) {
          out.push({ code: c.category_code, name: c.category_name, type: c.category_type })
          if (c.child_count > 0) queue.push(c.id)
        }
      }
    }
    codes.value = out
    codesState.value = partial ? 'partial' : 'ready'
  } catch {
    if (my === codesSeq) codesState.value = 'error'
  }
}

// 卸载（关掉对话框）后不再发请求：逐层取的循环每一批回来都核对 codesSeq（审查第 1 轮：原来卸载时不递增，
// 关掉对话框后还在接着取）。对话框里切标签页不卸载编辑器（v-show），所以一个对话框里最多取一遍。
onBeforeUnmount(() => {
  codesSeq++
})

// 第一次改模板（而且能解析）时取一次；同一个对话框里不重复取。
watch(
  () => templateChanged.value && parsed.value.ok,
  (need) => {
    if (need && codesState.value === 'idle') void loadCodes()
  },
  { immediate: true },
)

const conflicts = computed(() => {
  const p = parsed.value
  if (!p.ok || !templateChanged.value || !codes.value) return []
  const seps = p.template.separators
  return codes.value
    .map((c) => ({ ...c, found: separatorsIn(c.code, seps) }))
    .filter((c) => c.found.length > 0)
})

// ---------- 著录项 ----------

const TYPES: ItemFieldType[] = ['text', 'date', 'enum']

function addField() {
  props.draft.fields.push(newField())
  void nextTick(() => {
    const rows = document.querySelectorAll<HTMLInputElement>('[data-testid=item-field] [data-testid=field-key]')
    rows[rows.length - 1]?.focus()
  })
}

function move(i: number, delta: number) {
  const list = props.draft.fields
  const j = i + delta
  if (j < 0 || j >= list.length) return
  const [f] = list.splice(i, 1)
  list.splice(j, 0, f!)
}

function remove(i: number) {
  props.draft.fields.splice(i, 1)
}

function setType(i: number, t: ItemFieldType) {
  const f = props.draft.fields[i]!
  if (f.type === t) return
  f.type = t
  // 换类型时默认值多半不再合法：清掉；文本默认最大长度 500。
  f.defaultValue = ''
  if (t === 'text' && !f.maxLength) f.maxLength = String(DEFAULT_TEXT_MAX_LENGTH)
}

const problems = computed(() => props.draft.fields.map((f) => fieldProblems(f, props.draft.fields)))
</script>

<template>
  <div class="flex flex-col gap-6" data-testid="catalog-editor">
    <!-- ===== 档号模板 ===== -->
    <section class="flex flex-col gap-2">
      <div class="flex items-center gap-2">
        <h3 class="text-sm font-semibold text-foreground">档号模板</h3>
        <span class="text-xs text-muted-foreground">建条目时按它生成档号</span>
        <div class="flex-1" />
        <Button
          v-if="props.draft.template !== DEFAULT_ARCHIVE_NO_TEMPLATE"
          variant="ghost"
          size="xs"
          data-testid="template-default"
          @click="props.draft.template = DEFAULT_ARCHIVE_NO_TEMPLATE"
        >
          <RotateCcwIcon />用默认模板
        </Button>
      </div>
      <input
        ref="templateInput"
        v-model="props.draft.template"
        class="h-9 w-full min-w-0 rounded-md border border-input bg-transparent px-2.5 font-mono text-sm shadow-xs outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20"
        :aria-invalid="!parsed.ok || props.templateHighlight || undefined"
        spellcheck="false"
        autocomplete="off"
        :placeholder="DEFAULT_ARCHIVE_NO_TEMPLATE"
        data-testid="archive-template"
      />
      <div class="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
        插入
        <button
          v-for="t in INSERTS"
          :key="t.token"
          type="button"
          class="rounded-md bg-muted px-1.5 py-0.5 font-mono text-2xs text-foreground hover:bg-muted-foreground/15"
          :title="t.label"
          data-testid="template-insert"
          :data-token="t.token"
          @mousedown.prevent
          @click="insertToken(t.token)"
        >
          {{ t.token }}
        </button>
      </div>

      <div
        class="rounded-lg px-3 py-2.5 text-xs leading-5"
        :class="parsed.ok ? 'bg-muted/50 shadow-outline' : 'bg-destructive-soft'"
        data-testid="template-preview"
        :data-ok="String(parsed.ok)"
        :data-value="preview ?? undefined"
      >
        <template v-if="parsed.ok">
          <div class="flex items-baseline gap-2">
            <span class="text-muted-foreground">预览</span>
            <b class="font-mono text-base font-semibold tracking-tight text-foreground" data-testid="template-preview-value">{{ preview }}</b>
          </div>
          <div class="text-muted-foreground">
            示例：<template v-for="(t, i) in parsed.template.types" :key="t"
              >{{ i ? '、' : '' }}{{ categoryTypeLabel(t) }} {{ SAMPLE_CODES[t] }}</template
            ><template v-if="parsed.template.types.length">，</template>第 {{ SAMPLE_ITEM_NO }} 件{{
              parsed.template.itemNoWidth ? `（件号补零到 ${parsed.template.itemNoWidth} 位，位数多时照常全部写出）` : '（件号不补零）'
            }}
          </div>
          <div class="text-muted-foreground">
            层级：{{ parsed.template.types.map(categoryTypeLabel).join(' → ') || '（不用归类节点的编码）' }}。件往上找不到某一级时档号留空（可以归档，不能锁定）。
          </div>
          <div class="text-muted-foreground">
            分隔符：<span class="font-mono text-foreground">{{ parsed.template.separators.join(' ') || '—' }}</span>（归类编码里不能有）
          </div>
        </template>
        <p v-else class="text-destructive" data-testid="template-error">{{ parsed.error }}</p>
      </div>

      <!-- 改了模板 -->
      <div
        v-if="templateChanged && parsed.ok"
        class="flex gap-2 rounded-lg bg-warn-soft/70 px-3 py-2 text-xs leading-5 text-foreground"
        data-testid="template-codes-warning"
        :data-state="codesState"
        :data-count="conflicts.length"
      >
        <TriangleAlertIcon class="mt-0.5 size-3.5 shrink-0 text-warn" />
        <div class="min-w-0">
          <p>改模板后，已有条目的档号不会自动改；要按新模板重新生成，到归类页用"按新模板重新生成档号"（只改未锁定的）。</p>
          <p v-if="codesState === 'loading'" class="text-muted-foreground">正在检查已有的归类编码…</p>
          <template v-else-if="(codesState === 'ready' || codesState === 'partial') && conflicts.length">
            <p class="font-medium text-warn">
              已有 {{ conflicts.length }} 个归类编码含新模板的分隔符，拼出的档号会分不清段：
            </p>
            <ul class="mt-0.5 flex flex-wrap gap-x-3 gap-y-0.5">
              <li v-for="c in conflicts.slice(0, 8)" :key="`${c.type}:${c.code}:${c.name}`">
                <span class="font-mono text-foreground">{{ c.code }}</span>
                <span class="text-muted-foreground">（{{ categoryTypeLabel(c.type) }}「{{ c.name }}」，含 {{ c.found.join(' ') }}）</span>
              </li>
              <li v-if="conflicts.length > 8" class="text-muted-foreground">…还有 {{ conflicts.length - 8 }} 个</li>
            </ul>
            <p class="text-muted-foreground">编码改模板后不回溯校验。建议先到归类页改这些编码，或者换一个分隔符。</p>
          </template>
          <p v-else-if="codesState === 'ready' || codesState === 'partial'" class="text-muted-foreground">
            已检查 {{ codes?.length ?? 0 }} 个归类编码{{ codesState === 'partial' ? '（只检查了前面这些）' : '' }}，都不含新模板的分隔符
            <span class="font-mono text-foreground">{{ parsed.template.separators.join(' ') }}</span>。
          </p>
          <p v-else class="text-muted-foreground">
            {{ codesState === 'error' ? '没取到已有的归类编码。' : '' }}请确认已有的归类编码里没有新模板的分隔符
            <span class="font-mono text-foreground">{{ parsed.template.separators.join(' ') }}</span>：编码只在新建、修改时按当时的模板检查，改模板后不回溯，含分隔符的编码拼出的档号会分不清段。
          </p>
        </div>
      </div>
    </section>

    <!-- ===== 锁定 ===== -->
    <section class="flex flex-col gap-1.5">
      <h3 class="text-sm font-semibold text-foreground">锁定</h3>
      <label class="flex w-fit cursor-pointer items-center gap-2 text-sm text-foreground">
        <Checkbox
          :model-value="props.draft.lockRequiresApproved"
          data-testid="lock-requires-approved"
          @update:model-value="(v) => (props.draft.lockRequiresApproved = v === true)"
        />
        锁定前要求件的全部页审核通过
      </label>
      <p class="pl-6 text-xs text-muted-foreground">
        锁定还要求：不在修改中、必填著录项已填、至少 1 页、档号不为空。关掉后，没审完的页也能锁定（导出时仍可以只导出审核通过的）。
      </p>
    </section>

    <!-- ===== 著录项 ===== -->
    <section class="flex flex-col gap-2">
      <div class="flex items-center gap-2">
        <h3 class="text-sm font-semibold text-foreground">著录项</h3>
        <span class="text-xs text-muted-foreground">题名是固定的，不用在这里加；按这里的顺序显示在著录表单里</span>
        <div class="flex-1" />
        <Button variant="outline" size="xs" data-testid="add-field" @click="addField"><PlusIcon />添加著录项</Button>
      </div>

      <p v-if="props.draft.skippedFields" class="rounded-lg bg-warn-soft/70 px-3 py-2 text-xs text-foreground" data-testid="skipped-fields">
        项目设置里有 {{ props.draft.skippedFields }} 个著录项格式不对（以前写入的），这里读不出来；改了著录项或模板再保存时会去掉它们。
      </p>
      <p v-if="props.draft.fields.length === 0" class="rounded-lg bg-muted/50 px-3 py-4 text-center text-xs text-muted-foreground">
        还没有著录项，条目只有题名。点「添加著录项」加上责任者、文号、日期、密级……
      </p>

      <ol class="flex flex-col gap-2">
        <li
          v-for="(f, i) in props.draft.fields"
          :key="f.uid"
          class="rounded-lg px-3 py-2.5"
          :class="
            props.highlightIndex === i
              ? 'bg-destructive-soft/40 shadow-[0_0_0_1.5px_var(--destructive)]'
              : 'shadow-outline'
          "
          data-testid="item-field"
          :data-index="i"
          :data-key="f.key"
          :data-type="f.type"
          :data-problems="problems[i]!.length"
          :data-highlight="props.highlightIndex === i ? 'true' : undefined"
        >
          <div class="flex items-center gap-2">
            <div class="flex shrink-0 flex-col">
              <button
                type="button"
                class="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-30"
                :disabled="i === 0"
                aria-label="上移"
                data-testid="field-up"
                @click="move(i, -1)"
              >
                <ArrowUpIcon class="size-3" />
              </button>
              <button
                type="button"
                class="rounded p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-30"
                :disabled="i === props.draft.fields.length - 1"
                aria-label="下移"
                data-testid="field-down"
                @click="move(i, 1)"
              >
                <ArrowDownIcon class="size-3" />
              </button>
            </div>
            <span class="w-5 shrink-0 text-center text-xs text-muted-foreground tabular-nums">{{ i + 1 }}</span>
            <Input
              v-model="f.key"
              class="h-8 w-36 shrink-0 font-mono text-compact"
              placeholder="key，如 doc_no"
              autocomplete="off"
              spellcheck="false"
              :aria-invalid="problems[i]!.some((p) => p.startsWith('key')) || undefined"
              data-testid="field-key"
            />
            <Input
              v-model="f.label"
              class="h-8 min-w-0 flex-1"
              placeholder="名称，如 文号"
              autocomplete="off"
              :aria-invalid="problems[i]!.some((p) => p.startsWith('名称')) || undefined"
              data-testid="field-label"
            />
            <div class="flex shrink-0 rounded-md bg-muted p-0.5" role="radiogroup" aria-label="类型" data-testid="field-type" :data-type="f.type">
              <button
                v-for="t in TYPES"
                :key="t"
                type="button"
                role="radio"
                :aria-checked="f.type === t"
                class="rounded px-2 py-0.5 text-xs"
                :class="f.type === t ? 'bg-background font-medium text-foreground shadow-xs' : 'text-muted-foreground'"
                :data-value="t"
                @click="setType(i, t)"
              >
                {{ ITEM_FIELD_TYPE_LABEL[t] }}
              </button>
            </div>
            <label class="flex shrink-0 cursor-pointer items-center gap-1.5 text-xs text-foreground">
              <Checkbox :model-value="f.required" data-testid="field-required" @update:model-value="(v) => (f.required = v === true)" />必填
            </label>
            <Button
              variant="ghost"
              size="icon-sm"
              class="shrink-0 text-muted-foreground hover:text-destructive"
              aria-label="删除这个著录项"
              data-testid="field-remove"
              @click="remove(i)"
            >
              <Trash2Icon />
            </Button>
          </div>

          <div class="mt-2 flex items-start gap-3 pl-[52px] text-xs text-muted-foreground">
            <template v-if="f.type === 'text'">
              <label class="flex shrink-0 items-center gap-1.5">
                最大长度
                <Input
                  v-model="f.maxLength"
                  class="h-7 w-20 text-compact tabular-nums"
                  inputmode="numeric"
                  :placeholder="String(DEFAULT_TEXT_MAX_LENGTH)"
                  data-testid="field-max-length"
                />
                个字符
              </label>
              <label class="flex min-w-0 flex-1 items-center gap-1.5">
                默认值
                <Input v-model="f.defaultValue" class="h-7 min-w-0 flex-1 text-compact" placeholder="没有" data-testid="field-default" />
              </label>
            </template>
            <template v-else-if="f.type === 'date'">
              <label class="flex min-w-0 flex-1 items-center gap-1.5">
                默认值
                <Input
                  v-model="f.defaultValue"
                  class="h-7 w-40 font-mono text-compact"
                  placeholder="没有"
                  data-testid="field-default"
                />
                <span>格式 {{ DATE_FORMATS.join('、') }}（只写年、年月也行）</span>
              </label>
            </template>
            <template v-else>
              <label class="flex min-w-0 flex-1 items-start gap-1.5">
                <span class="pt-1">选项</span>
                <textarea
                  v-model="f.optionsText"
                  rows="3"
                  class="min-h-[60px] min-w-0 flex-1 rounded-md border border-input bg-transparent px-2 py-1 text-compact text-foreground shadow-xs outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
                  placeholder="一行一个，例如&#10;公开&#10;内部"
                  data-testid="field-options"
                />
              </label>
              <label class="flex shrink-0 items-center gap-1.5 pt-0.5">
                默认值
                <NativeSelect
                  v-model="f.defaultValue"
                  size="xs"
                  class="max-w-40"
                  data-testid="field-default"
                >
                  <option value="">没有</option>
                  <option v-if="f.defaultValue && !optionLines(f.optionsText).includes(f.defaultValue)" :value="f.defaultValue">
                    {{ f.defaultValue }}（不在选项里）
                  </option>
                  <option v-for="o in optionLines(f.optionsText)" :key="o" :value="o">{{ o }}</option>
                </NativeSelect>
              </label>
            </template>
          </div>

          <ul v-if="problems[i]!.length" class="mt-1.5 pl-[52px] text-xs text-destructive">
            <li v-for="p in problems[i]" :key="p" data-testid="field-problem">{{ p }}</li>
          </ul>
          <p
            v-if="f.originalKey !== null && f.key.trim() !== f.originalKey"
            class="mt-1.5 pl-[52px] text-xs text-warn"
          >
            改了 key：已有条目里按旧 key「{{ f.originalKey }}」存的内容不会跟着改。
          </p>
        </li>
      </ol>
      <p v-if="props.draft.fields.length" class="text-xs text-muted-foreground">
        删掉著录项或改 key，不会删已有条目里存的内容。必填在锁定时检查，未锁定时只检查类型和格式。
      </p>
    </section>
  </div>
</template>
