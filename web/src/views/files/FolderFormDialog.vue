<script setup lang="ts">
// 新建文件夹（名称、备注）或编辑文件夹（改名、改备注）（M1-12）。备注一般写原件位置（例如"3 号库 A 架 12 层"）。
// 根文件夹只能改备注（名称跟着项目走）。名字先按 design §1.7 在本地校验；接口错误（重名、名字不合法、
// tree_full……）显示在对话框里。
import { computed, ref, watch } from 'vue'

import { createFolder, updateFolder } from '@/api/folders'
import type { FolderItem, UpdateFolderBody } from '@/api/types'
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
import { checkEntryName, checkRemark, MAX_ENTRY_NAME_CHARS } from '@/lib/validate'

import { opErrorText } from './op-errors'

export interface EditableFolder {
  id: string
  folder_name: string
  remark: string | null
  /** 根文件夹（只能改备注）。 */
  isRoot: boolean
}

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  /** 新建时的上级文件夹。 */
  parent: { id: string; folder_name: string } | null
  /** 编辑的文件夹；为 null 时是新建。 */
  folder: EditableFolder | null
  /** 打开时把光标放在哪一栏。 */
  focus?: 'name' | 'remark'
}>()

const emit = defineEmits<{ saved: [folder: FolderItem, created: boolean] }>()

const name = ref('')
const remark = ref('')
const error = ref('')
const saving = ref(false)

const isEdit = computed(() => props.folder !== null)
const nameLocked = computed(() => !!props.folder?.isRoot)

watch(open, (v) => {
  if (!v) return
  name.value = props.folder?.folder_name ?? ''
  remark.value = props.folder?.remark ?? ''
  error.value = ''
})

/** 打开时把光标放到名称（选中原名）或备注。 */
function onOpenAutoFocus(e: Event) {
  e.preventDefault()
  const id = props.focus === 'remark' || nameLocked.value ? 'ff-remark' : 'ff-name'
  const el = document.getElementById(id) as HTMLInputElement | HTMLTextAreaElement | null
  el?.focus()
  if (id === 'ff-name') el?.select()
}

async function submit() {
  error.value = (nameLocked.value ? '' : checkEntryName(name.value, 'folder')) || checkRemark(remark.value)
  if (error.value) return
  saving.value = true
  try {
    const f = props.folder
    if (f) {
      const body: UpdateFolderBody = {}
      if (!f.isRoot && name.value !== f.folder_name) body.folder_name = name.value
      if (remark.value.trim() !== (f.remark ?? '')) body.remark = remark.value.trim()
      if (Object.keys(body).length === 0) {
        open.value = false
        return
      }
      const saved = await updateFolder(f.id, body, true)
      emit('saved', saved, false)
    } else if (props.parent) {
      const saved = await createFolder(
        { parent_id: props.parent.id, folder_name: name.value, remark: remark.value.trim() || undefined },
        true,
      )
      emit('saved', saved, true)
    }
    open.value = false
  } catch (e) {
    error.value = opErrorText(e, { kind: 'folder', name: name.value })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-md" data-testid="folder-form" @open-auto-focus="onOpenAutoFocus">
      <DialogHeader>
        <DialogTitle>{{ isEdit ? (nameLocked ? '修改根文件夹的备注' : '编辑文件夹') : '新建文件夹' }}</DialogTitle>
        <DialogDescription>
          <template v-if="!isEdit && props.parent">新建在「{{ props.parent.folder_name }}」里。</template>
          <template v-else-if="nameLocked">根文件夹的名称跟着项目名称走，这里只能改备注。</template>
          <template v-else>改名、移动都不影响里面文件的审核和归类。</template>
        </DialogDescription>
      </DialogHeader>

      <form id="folder-form" class="grid gap-4" @submit.prevent="submit">
        <div class="grid gap-2">
          <Label for="ff-name">名称</Label>
          <Input
            id="ff-name"
            v-model="name"
            autocomplete="off"
            placeholder="例如 盒 WS-2025-005"
            :maxlength="MAX_ENTRY_NAME_CHARS"
            :disabled="nameLocked"
            data-testid="folder-form-name"
          />
          <p class="text-xs text-muted-foreground">
            同一位置不能重名（不区分大小写）；不能含 \ / : * ? &lt; &gt; | 和双引号，不能是 CON、NUL、COM1 这类 Windows 保留名。
          </p>
        </div>
        <div class="grid gap-2">
          <Label for="ff-remark">备注（可选）</Label>
          <Textarea
            id="ff-remark"
            v-model="remark"
            rows="2"
            placeholder="原件位置，例如 3 号库 A 架 12 层"
            data-testid="folder-form-remark"
          />
        </div>
        <p v-if="error" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive" role="alert" data-testid="folder-form-error">
          {{ error }}
        </p>
      </form>

      <DialogFooter>
        <Button variant="outline" :disabled="saving" @click="open = false">取消</Button>
        <Button type="submit" form="folder-form" :disabled="saving" data-testid="folder-form-submit">
          {{ saving ? '保存中…' : isEdit ? '保存' : '新建' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
