<script setup lang="ts">
// 新建项目（编码、名称、说明）或编辑项目（名称、说明、状态）。编码建好后不能改。
// 接口错误（编码重复、校验失败……）显示在对话框里。
// M3-7：编辑时多一个"著录项和档号"标签页（CatalogSettingsEditor）：档号模板和预览、定稿前要求全部页审核通过、
// 著录项。打开时取项目详情（settings 补上了默认值），保存时和基本信息一起发一次 PATCH；只有这部分改了才带 settings
// （在详情的 settings 上只换这三处，其余原样，见 catalog-draft.ts）。server 的校验错误（400，message 以
// "settings 不对："开头）显示在这个标签页里，指向第几个著录项时标出那一行、指向模板时标出模板输入框。
// M4-4（用户决定 QB）：保存带版本检查 `expected_updated_at`，版本和比较"改了什么"的基准是同一份：项目详情（取详情
// 期间名称、说明、状态和保存按钮禁用，取到后基本信息换成详情里的；取详情失败时退回列表那一行）。409 `conflict` 时
// 重新取详情，按"打开时的、我的、现在的"三方合并：别人改过的部分（名称、说明、状态、著录项和档号）换成现在的；我动过
// 而且改得和别人不一样的，算我的修改没有保存；别人没动的部分保留我的修改；提示别人改了哪些、放弃了我的哪些，核对后
// 再保存。别人只改了对话框里看不到的东西（授权、settings 里的其他键）时不提示，用新版本直接重发一次。替换了 M3-7 的
// "保存前重读比对"。
// 截图脚本用的属性：project-form（data-tab、data-settings：loading、ready、error）、project-form-tab（data-tab）、
// project-form-error（data-scope：basic、settings）、project-form-conflict、project-form-submit。
import { LoaderCircleIcon } from '@lucide/vue'
import { computed, nextTick, ref, watch } from 'vue'

import { errorMessage, isApiError } from '@/api/http'
import { createProject, getProject, updateProject, type UpdateProjectBody } from '@/api/projects'
import type { ProjectDetail, ProjectItem, ProjectStatus } from '@/api/types'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import {
  checkProjectCode,
  checkProjectDescription,
  checkProjectName,
  MAX_PROJECT_CODE_CHARS,
  PROJECT_STATUS_LABEL,
} from '@/lib/project'

import {
  type CatalogDraft,
  draftFromSettings,
  draftSignature,
  fieldIndexOfError,
  settingsWithDraft,
} from './catalog-draft'
import CatalogSettingsEditor from './CatalogSettingsEditor.vue'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  /** 编辑的项目；为 null 时是新建。 */
  project: ProjectItem | null
}>()

const emit = defineEmits<{ saved: [project: ProjectDetail] }>()

const code = ref('')
const name = ref('')
const description = ref('')
const status = ref<ProjectStatus>('active')
const error = ref('')
const saving = ref(false)

const isEdit = computed(() => props.project !== null)
const STATUSES: ProjectStatus[] = ['active', 'archived']

// ---------- 著录项和档号（只在编辑时） ----------

type Tab = 'basic' | 'catalog'
const tab = ref<Tab>('basic')
const detail = ref<ProjectDetail | null>(null)
const detailState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const detailError = ref('')
const draft = ref<CatalogDraft | null>(null)
let initialSignature = ''
const settingsError = ref('')
/** 保存时 409（打开之后别人改过这个项目）、已换成最新内容时的提示（M4-4）。 */
const conflictNotice = ref('')
const highlightIndex = ref<number | null>(null)
const templateHighlight = ref(false)
let detailSeq = 0

// ---------- 版本（M4-4） ----------

/** 编辑的基准：对话框里"改了什么"跟它比，保存时它的 `updated_at` 就是 `expected_updated_at`。 */
type Base = Pick<ProjectDetail, 'project_name' | 'description' | 'status' | 'updated_at'>
/** 取到项目详情之前是列表里的那一行，取到之后是详情（409 后是重新取到的详情）。 */
const base = computed<Base | null>(() => detail.value ?? props.project)

/**
 * 编辑时正在取详情：名称、说明、状态和保存按钮禁用（M4-4 审查）。列表那一行可能很旧（改授权后不更新
 * `updated_at`，也不随别人的修改刷新），详情到达时基准整体换成详情；这期间要是能改，改过的字段会带着新版本
 * 悄悄盖掉别人的修改。新建项目不受影响。
 */
const basicLocked = computed(
  () => isEdit.value && (detailState.value === 'loading' || detailState.value === 'idle'),
)

type BasicField = 'name' | 'description' | 'status'
/** 用户动过的基本信息字段（输入、点状态按钮时记下；打开对话框、换成别人的值时清掉）。 */
const touched: Record<BasicField, boolean> = { name: false, description: false, status: false }

/**
 * 基本信息从基准 `old` 换到 `fresh`（详情到达、409 后重新取到）：别人改过的字段（`fresh` 和 `old` 不同）换成
 * `fresh` 的值；我动过、我的值和两边都不同的，算"我的修改没有保存"。我的值和别人改的一样时不算放弃。
 * 别人没改的字段保留我的值。返回别人改了的、放弃了我的修改的字段名。
 */
function mergeBasic(old: Base, fresh: Base): { changed: string[]; dropped: string[] } {
  const changed: string[] = []
  const dropped: string[] = []
  const fields: [BasicField, string, string, string, string, (v: string) => void][] = [
    ['name', '名称', name.value.trim(), old.project_name, fresh.project_name, (v) => (name.value = v)],
    [
      'description',
      '说明',
      description.value.trim(),
      old.description ?? '',
      fresh.description ?? '',
      (v) => (description.value = v),
    ],
    ['status', '状态', status.value, old.status, fresh.status, (v) => (status.value = v as ProjectStatus)],
  ]
  for (const [key, label, mine, before, theirs, set] of fields) {
    if (theirs === before) continue
    changed.push(label)
    if (touched[key] && mine !== before && mine !== theirs) dropped.push(label)
    set(theirs)
    touched[key] = false
  }
  return { changed, dropped }
}

async function loadDetail(id: string) {
  const my = ++detailSeq
  detailState.value = 'loading'
  detailError.value = ''
  try {
    const d = await getProject(id, true)
    if (my !== detailSeq) return
    // 第一次取到：基准从列表那一行换成详情。正常打开时取详情期间输入是禁用的，基本信息直接换成详情里的；只有
    // 取详情失败后改过、再点"重试"时才可能有我的修改，和 409 一样按字段合并并提示。
    if (!detail.value && props.project) {
      const { changed, dropped } = mergeBasic(props.project, d)
      if (dropped.length) {
        conflictNotice.value = `这个项目在你打开之后被别人改过（${changed.join('、')}），已换成最新的内容，你对${dropped.join('、')}的修改没有保存。请核对后再保存。`
      }
    }
    detail.value = d
    draft.value = draftFromSettings(d.settings)
    initialSignature = draftSignature(draft.value)
    detailState.value = 'ready'
  } catch (e) {
    if (my !== detailSeq) return
    detailState.value = 'error'
    detailError.value = errorMessage(e)
  }
}

const settingsDirty = computed(() => !!draft.value && draftSignature(draft.value) !== initialSignature)
const savedTemplate = computed(() => {
  const t = (detail.value?.settings.archive_no as Record<string, unknown> | undefined)?.template
  return typeof t === 'string' ? t : ''
})

watch(open, (v) => {
  if (!v) {
    detailSeq++
    return
  }
  code.value = props.project?.project_code ?? ''
  name.value = props.project?.project_name ?? ''
  description.value = props.project?.description ?? ''
  status.value = props.project?.status ?? 'active'
  touched.name = touched.description = touched.status = false
  error.value = ''
  tab.value = 'basic'
  detail.value = null
  draft.value = null
  initialSignature = ''
  settingsError.value = ''
  conflictNotice.value = ''
  highlightIndex.value = null
  templateHighlight.value = false
  detailState.value = 'idle'
  if (props.project) void loadDetail(props.project.id)
})

// 改了著录项或模板之后，旧的 server 错误标记不再准确：清掉标记（错误文字留着，直到下次保存）。
watch(
  () => (draft.value ? draftSignature(draft.value) : ''),
  () => {
    highlightIndex.value = null
    templateHighlight.value = false
  },
)

function validate(): string {
  return (
    (isEdit.value ? '' : checkProjectCode(code.value)) ||
    checkProjectName(name.value) ||
    checkProjectDescription(description.value)
  )
}

async function showSettingsError(message: string) {
  settingsError.value = message
  tab.value = 'catalog'
  await nextTick()
  highlightIndex.value = fieldIndexOfError(message)
  templateHighlight.value = message.includes('档号模板')
  await nextTick()
  const target =
    highlightIndex.value !== null
      ? document.querySelector(`[data-testid=item-field][data-index="${highlightIndex.value}"]`)
      : document.querySelector('[data-testid=project-form-error][data-scope=settings]')
  target?.scrollIntoView({ block: 'nearest' })
}

/** 对话框里相对基准 `b` 改了的内容（PATCH 的请求体，不含版本）。 */
function changesAgainst(b: Base): UpdateProjectBody {
  const body: UpdateProjectBody = {}
  if (name.value.trim() !== b.project_name) body.project_name = name.value.trim()
  if (description.value.trim() !== (b.description ?? '')) body.description = description.value.trim()
  if (status.value !== b.status) body.status = status.value
  // 著录项和档号：在详情的 settings 上只换三处，其余原样（catalog-draft.ts）。settings 是整体替换，有版本检查才不会
  // 把别人在这期间的修改盖掉。
  if (settingsDirty.value && detail.value && draft.value) {
    body.settings = settingsWithDraft(detail.value.settings, draft.value)
  }
  return body
}

/** settings 里由这个对话框编辑的部分（模板、锁定要求、著录项），用来判断别人改没改。 */
function catalogPart(settings: Record<string, unknown>): string {
  return draftSignature(draftFromSettings(settings))
}

/**
 * 保存时 409 `conflict`（`old` 是这次保存的基准）：重新取详情，三方合并（见文件开头）。返回 `retry`：别人只改了
 * 对话框里看不到的东西，基准已换成新的，可以直接重发；`notice`：已提示，等用户核对后再保存。版本没变（不是版本冲突）
 * 时抛出原来的错误。
 */
async function mergeAfterConflict(id: string, old: Base, err: unknown): Promise<'retry' | 'notice'> {
  const fresh = await getProject(id, true)
  if (fresh.updated_at === old.updated_at) throw err
  const { changed, dropped } = mergeBasic(old, fresh)
  const before = detail.value
  if (!before) {
    // 详情没取到（取详情失败，保存的只是基本信息）：直接用新取到的。
    detailSeq++
    draft.value = draftFromSettings(fresh.settings)
    initialSignature = draftSignature(draft.value)
  } else {
    const theirs = catalogPart(fresh.settings)
    if (theirs !== catalogPart(before.settings)) {
      changed.push('著录项和档号')
      // 我改过、而且改得和别人不一样，才算放弃了我的修改。
      if (settingsDirty.value && draft.value && draftSignature(draft.value) !== theirs) dropped.push('著录项和档号')
      draft.value = draftFromSettings(fresh.settings)
      initialSignature = draftSignature(draft.value)
    }
  }
  detail.value = fresh
  detailState.value = 'ready'
  if (changed.length === 0) return 'retry'
  conflictNotice.value =
    `这个项目在你打开之后被别人改过（${changed.join('、')}），已换成最新的内容` +
    (dropped.length ? `，你对${dropped.join('、')}的修改没有保存` : '') +
    (Object.keys(changesAgainst(fresh)).length ? '；你的其他修改还在' : '') +
    '。请核对后再保存。'
  tab.value = changed.includes('著录项和档号') ? 'catalog' : 'basic'
  return 'notice'
}

/** 保存编辑（带版本检查）。返回保存后的详情；`unchanged`：没有修改，没发请求；`null`：409 已提示，没保存。 */
async function saveEdit(id: string): Promise<ProjectDetail | 'unchanged' | null> {
  // 最多发两次：第二次是"别人只改了看不到的东西"之后用新版本重发；再 409 就提示，不无限重试。
  for (let attempt = 0; attempt < 2; attempt++) {
    const b = base.value!
    const body = changesAgainst(b)
    if (Object.keys(body).length === 0) return 'unchanged'
    try {
      return await updateProject(id, { ...body, expected_updated_at: b.updated_at }, true)
    } catch (err) {
      if (!isApiError(err, 'conflict')) throw err
      if ((await mergeAfterConflict(id, b, err)) === 'notice') return null
    }
  }
  conflictNotice.value = '这个项目刚被别人改过，已换成最新的版本，请再保存一次。'
  return null
}

async function submit() {
  if (basicLocked.value || saving.value) return
  error.value = validate()
  if (error.value) {
    tab.value = 'basic'
    return
  }
  saving.value = true
  settingsError.value = ''
  conflictNotice.value = ''
  try {
    let saved: ProjectDetail
    const p = props.project
    if (p) {
      const result = await saveEdit(p.id)
      if (result === null) return
      if (result === 'unchanged') {
        open.value = false
        return
      }
      saved = result
    } else {
      saved = await createProject(
        {
          project_code: code.value.trim(),
          project_name: name.value.trim(),
          description: description.value.trim() || undefined,
        },
        true,
      )
    }
    emit('saved', saved)
    open.value = false
  } catch (err) {
    const msg = errorMessage(err)
    if (msg.startsWith('settings 不对')) await showSettingsError(msg)
    else error.value = msg
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent
      :class="isEdit ? 'flex max-h-[90vh] flex-col gap-4 sm:max-w-4xl' : 'sm:max-w-lg'"
      data-testid="project-form"
      :data-tab="tab"
      :data-settings="isEdit ? detailState : undefined"
    >
      <DialogHeader>
        <DialogTitle>{{ isEdit ? '编辑项目' : '新建项目' }}</DialogTitle>
        <DialogDescription>
          {{
            isEdit
              ? '项目编码建好后不能修改。改名时，根文件夹的名称会一起改。'
              : '建好后自动生成根文件夹。建项目的人不会自动成为成员，系统管理员能进所有项目，其他人要在「授权」里勾选。'
          }}
        </DialogDescription>
      </DialogHeader>

      <div v-if="isEdit" class="flex w-fit shrink-0 rounded-md bg-muted p-0.5" role="tablist">
        <button
          v-for="t in ([['basic', '基本信息'], ['catalog', '著录项和档号']] as const)"
          :key="t[0]"
          type="button"
          role="tab"
          :aria-selected="tab === t[0]"
          class="flex items-center gap-1.5 rounded px-3 py-1 text-sm"
          :class="tab === t[0] ? 'bg-background font-medium text-foreground shadow-xs' : 'text-muted-foreground'"
          data-testid="project-form-tab"
          :data-tab="t[0]"
          @click="tab = t[0]"
        >
          {{ t[1] }}
          <span v-if="t[0] === 'catalog' && settingsDirty" class="size-1.5 rounded-full bg-info" title="有没保存的修改" />
          <span v-if="t[0] === 'catalog' && settingsError" class="size-1.5 rounded-full bg-destructive" title="保存失败" />
        </button>
      </div>

      <!-- 保存时 409（M4-4）：两个标签页都看得到。 -->
      <p
        v-if="conflictNotice"
        class="shrink-0 rounded-md bg-warn-soft px-3 py-2 text-sm text-foreground"
        role="alert"
        data-testid="project-form-conflict"
      >
        {{ conflictNotice }}
      </p>

      <form
        v-show="tab === 'basic'"
        id="project-form"
        class="grid gap-4"
        :class="isEdit ? 'max-w-2xl' : ''"
        @submit.prevent="submit"
      >
        <div class="grid grid-cols-[180px_1fr] gap-4">
          <div class="grid gap-2">
            <Label for="pf-code">项目编码</Label>
            <Input
              id="pf-code"
              v-model="code"
              class="font-mono"
              autocomplete="off"
              placeholder="例如 WS2025"
              :maxlength="MAX_PROJECT_CODE_CHARS"
              :disabled="isEdit"
            />
          </div>
          <div class="grid gap-2">
            <Label for="pf-name">项目名称</Label>
            <Input
              id="pf-name"
              v-model="name"
              autocomplete="off"
              placeholder="例如 市档案馆 2025 文书数字化"
              :disabled="basicLocked"
              @input="touched.name = true"
            />
          </div>
        </div>
        <p v-if="!isEdit" class="-mt-2 text-xs leading-relaxed text-muted-foreground">
          编码会用在存储路径里：英文字母或数字开头，只能有英文字母、数字和 . _ -，最长 32 个字符，区分大小写，建好后不能改。
          名称也是根文件夹的名称，不能含 \ / : * ? &lt; &gt; | 和双引号。
        </p>
        <div class="grid gap-2">
          <Label for="pf-desc">说明（可选）</Label>
          <Textarea
            id="pf-desc"
            v-model="description"
            rows="3"
            placeholder="例如 委托方、批次、交付要求"
            :disabled="basicLocked"
            @input="touched.description = true"
          />
        </div>
        <div v-if="isEdit" class="grid gap-2">
          <Label>状态</Label>
          <div class="inline-flex w-fit rounded-md bg-muted p-0.5" role="radiogroup">
            <button
              v-for="s in STATUSES"
              :key="s"
              type="button"
              role="radio"
              :aria-checked="status === s"
              :disabled="basicLocked"
              class="rounded px-3 py-1 text-sm disabled:cursor-not-allowed disabled:opacity-50"
              :class="status === s ? 'bg-background font-medium text-foreground shadow-xs' : 'text-muted-foreground'"
              @click="
                status = s;
                touched.status = true
              "
            >
              {{ PROJECT_STATUS_LABEL[s] }}
            </button>
          </div>
          <p class="text-xs text-muted-foreground">已归档只是一个标记：项目切换器默认不显示，不限制继续操作。</p>
        </div>
      </form>

      <!-- 著录项和档号（不放在 form 里：在这里按 Enter 不提交整个对话框）。用 v-show：切标签页时编辑器不卸载，
           它取到的已有归类编码在这个对话框里只取一遍（审查第 1 轮）。 -->
      <div v-if="isEdit" v-show="tab === 'catalog'" class="-mx-6 min-h-0 flex-1 overflow-y-auto px-6 py-1">
        <div v-if="detailState === 'loading' || detailState === 'idle'" class="flex items-center gap-2 py-10 text-sm text-muted-foreground">
          <LoaderCircleIcon class="size-4 animate-spin" />正在读项目设置…
        </div>
        <div v-else-if="detailState === 'error'" class="py-10 text-center text-sm">
          <p class="text-destructive">{{ detailError }}</p>
          <Button size="sm" variant="outline" class="mt-3" @click="props.project && loadDetail(props.project.id)">重试</Button>
        </div>
        <template v-else-if="draft">
          <p
            v-if="settingsError"
            class="mb-4 rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive"
            role="alert"
            data-testid="project-form-error"
            data-scope="settings"
          >
            没有保存：{{ settingsError }}
          </p>
          <CatalogSettingsEditor
            :draft="draft"
            :project-id="props.project!.id"
            :saved-template="savedTemplate"
            :highlight-index="highlightIndex"
            :template-highlight="templateHighlight"
          />
        </template>
      </div>

      <p
        v-if="error"
        class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive"
        role="alert"
        data-testid="project-form-error"
        data-scope="basic"
      >
        {{ error }}
      </p>

      <DialogFooter class="shrink-0">
        <span v-if="isEdit && settingsDirty" class="mr-auto self-center text-xs text-muted-foreground">
          著录项和档号有修改，保存时一起保存
        </span>
        <Button variant="outline" :disabled="saving" data-testid="project-form-cancel" @click="open = false">取消</Button>
        <Button type="submit" form="project-form" :disabled="saving || basicLocked" data-testid="project-form-submit">
          {{ saving ? '保存中…' : isEdit ? '保存' : '创建' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
