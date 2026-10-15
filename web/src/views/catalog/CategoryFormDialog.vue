<script setup lang="ts">
// 新建归类节点，或修改一个节点的类型、编码、名称（M3-5）。规则见 design §2.3"归类节点"：
// - 类型只能是全宗、门类、年度、保管期限、机构、自定义（root 不能新建也不能改成）；同一棵树里可以有多层同类型的；
// - 编码 1–32 个字符，不能有空白、控制字符、花括号、`\ / : * ? " < > |` 和当前档号模板的分隔符（catalog-settings 给出
//   不能用的字符），同一层不能重复（区分大小写）；
// - 名称去掉首尾空白后 1–100 个字符，同一层可以重名；
// - 改编码、改类型会重算子树里未定稿条目的档号；子树里有已定稿的条目时 409 `item_locked`（server 的 message 写明了）。
// 先在本地校验，接口错误显示在对话框里。保存成功后由页面提示"重算了 n 个档号""n 个条目因为撞号档号为空"。
import { computed, ref, watch } from 'vue'

import { createArchiveCategory, updateArchiveCategory } from '@/api/catalog'
import type { ArchiveCategory, CatalogSettings, UpdatedArchiveCategory } from '@/api/types'
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
import { CATEGORY_TYPES, categoryTypeLabel, checkCategoryCode, checkCategoryName } from '@/lib/catalog'

import { categoryOpErrorText } from './catalog-errors'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  projectId: string
  settings: CatalogSettings | null
  /** 新建时的上级节点（null 是顶层）；编辑时不用。 */
  parent: ArchiveCategory | null
  /** 编辑的节点；为 null 时是新建。 */
  node: ArchiveCategory | null
  /** 新建时默认的类型（例如上级是年度时默认保管期限）。 */
  defaultType?: string
  /** 打开时把光标放在哪一栏。 */
  focus?: 'type' | 'code' | 'name'
}>()

const emit = defineEmits<{
  created: [node: ArchiveCategory]
  updated: [node: UpdatedArchiveCategory, before: ArchiveCategory]
}>()

const type = ref('fonds')
const code = ref('')
const name = ref('')
const error = ref('')
const saving = ref(false)

const isEdit = computed(() => props.node !== null)
const types = computed(() => props.settings?.category_types ?? [...CATEGORY_TYPES])
const levels = computed(() => props.settings?.archive_no_levels ?? [])

const codeChanged = computed(() => !!props.node && code.value !== props.node.category_code)
const typeChanged = computed(() => !!props.node && type.value !== props.node.category_type)

const codeProblem = computed(() => {
  if (!props.settings) return null
  // 和现在一样时不校验（改模板后已有的编码不回溯，design §2.3）。
  if (props.node && code.value === props.node.category_code) return null
  return checkCategoryCode(code.value, props.settings)
})
const nameProblem = computed(() => checkCategoryName(name.value, props.settings?.category_name_max_chars ?? 100))

watch(open, (v) => {
  if (!v) return
  error.value = ''
  if (props.node) {
    type.value = props.node.category_type
    code.value = props.node.category_code
    name.value = props.node.category_name
  } else {
    type.value = props.defaultType && types.value.includes(props.defaultType) ? props.defaultType : (types.value[0] ?? 'fonds')
    code.value = ''
    name.value = ''
  }
})

function onOpenAutoFocus(e: Event) {
  e.preventDefault()
  const id = props.focus === 'name' ? 'cf-name' : props.focus === 'type' ? 'cf-type' : 'cf-code'
  const el = document.getElementById(id) as HTMLInputElement | null
  el?.focus()
  if (el && 'select' in el && id !== 'cf-type') el.select()
}

async function submit() {
  error.value = codeProblem.value || nameProblem.value || ''
  if (error.value || saving.value) return
  saving.value = true
  try {
    const n = props.node
    if (n) {
      const body: { category_type?: string; category_code?: string; category_name?: string } = {}
      if (typeChanged.value) body.category_type = type.value
      if (codeChanged.value) body.category_code = code.value
      if (name.value.trim() !== n.category_name) body.category_name = name.value.trim()
      if (Object.keys(body).length === 0) {
        open.value = false
        return
      }
      const saved = await updateArchiveCategory(props.projectId, n.id, body, true)
      emit('updated', saved, n)
    } else {
      const saved = await createArchiveCategory(
        props.projectId,
        {
          parent_id: props.parent?.id ?? null,
          category_type: type.value,
          category_code: code.value,
          category_name: name.value.trim(),
        },
        true,
      )
      emit('created', saved)
    }
    open.value = false
  } catch (e) {
    error.value = categoryOpErrorText(e, { code: code.value })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-md" data-testid="category-form" @open-auto-focus="onOpenAutoFocus">
      <DialogHeader>
        <DialogTitle>{{ isEdit ? '修改归类节点' : '新建归类节点' }}</DialogTitle>
        <DialogDescription>
          <template v-if="isEdit && props.node">
            「{{ props.node.category_name }}」含下级共 {{ props.node.item_count }} 个条目。
          </template>
          <template v-else-if="props.parent">新建在「{{ props.parent.category_name }}」下面。</template>
          <template v-else>新建在顶层。</template>
          档号按模板沿上级链取各级的编码，模板用到：{{ levels.map(categoryTypeLabel).join('、') || '—' }}。
        </DialogDescription>
      </DialogHeader>

      <form id="category-form" class="grid gap-4" @submit.prevent="submit">
        <div class="grid grid-cols-[7rem_1fr] gap-3">
          <div class="grid gap-2">
            <Label for="cf-type">类型</Label>
            <select
              id="cf-type"
              v-model="type"
              class="h-9 rounded-md border border-input bg-background px-2 text-sm text-foreground shadow-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
              data-testid="category-form-type"
            >
              <option v-for="t in types" :key="t" :value="t">{{ categoryTypeLabel(t) }}</option>
            </select>
          </div>
          <div class="grid gap-2">
            <Label for="cf-code">编码</Label>
            <Input
              id="cf-code"
              v-model="code"
              autocomplete="off"
              class="font-mono"
              placeholder="例如 Z109、WS、2025、Y、BGS"
              :aria-invalid="!!codeProblem || undefined"
              data-testid="category-form-code"
            />
          </div>
        </div>
        <p v-if="codeProblem && code" class="-mt-2 text-xs text-destructive" data-testid="category-form-code-problem">
          {{ codeProblem }}
        </p>
        <p v-else-if="!levels.includes(type)" class="-mt-2 text-xs text-muted-foreground">
          档号模板里没有「{{ categoryTypeLabel(type) }}」，这一级的编码不进档号。
        </p>
        <div class="grid gap-2">
          <Label for="cf-name">名称</Label>
          <Input
            id="cf-name"
            v-model="name"
            autocomplete="off"
            placeholder="例如 文书档案、2025 年、永久、办公室"
            data-testid="category-form-name"
          />
        </div>
        <p
          v-if="isEdit && (codeChanged || typeChanged)"
          class="rounded-md bg-warn-soft px-3 py-2 text-xs text-warn"
          data-testid="category-form-renumber-hint"
        >
          改{{ codeChanged && typeChanged ? '编码和类型' : codeChanged ? '编码' : '类型' }}会重新生成这个节点（含下级）里
          未定稿条目的档号；有已定稿的条目时不能改。
        </p>
        <p
          v-if="error"
          class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive"
          role="alert"
          data-testid="category-form-error"
        >
          {{ error }}
        </p>
      </form>

      <DialogFooter>
        <Button variant="outline" :disabled="saving" @click="open = false">取消</Button>
        <Button type="submit" form="category-form" :disabled="saving || !props.settings" data-testid="category-form-submit">
          {{ saving ? '保存中…' : isEdit ? '保存' : '新建' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
