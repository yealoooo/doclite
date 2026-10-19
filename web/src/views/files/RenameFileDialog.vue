<script setup lang="ts">
// 重命名文件（M1-12，F2）：`PATCH /api/folder-files/{id}`。只改 folder_files 的名字（硬性规则 3），
// S3 对象、快照、审核、归类都不动。撞名返回 409 `conflict`（明确的改名操作不自动改名，design §1.7）。
import { ref, watch } from 'vue'

import { updateFolderFile } from '@/api/folder-files'
import type { FileEntry, FolderFileUpdated } from '@/api/types'
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
import { checkEntryName, MAX_ENTRY_NAME_CHARS } from '@/lib/validate'
import { HANDLE_ALL } from '@/api/http'

import { opErrorText } from './op-errors'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{ file: FileEntry | null }>()

const emit = defineEmits<{ saved: [result: FolderFileUpdated] }>()

const name = ref('')
const error = ref('')
const saving = ref(false)

watch(open, (v) => {
  if (!v) return
  name.value = props.file?.file_name ?? ''
  error.value = ''
})

/** 打开时选中主文件名（不含扩展名），方便直接输入。 */
function onOpenAutoFocus(e: Event) {
  e.preventDefault()
  const el = document.getElementById('rf-name') as HTMLInputElement | null
  if (!el) return
  el.focus()
  const dot = name.value.lastIndexOf('.')
  el.setSelectionRange(0, dot > 0 ? dot : name.value.length)
}

async function submit() {
  const f = props.file
  if (!f) return
  error.value = checkEntryName(name.value, 'file')
  if (error.value) return
  if (name.value === f.file_name) {
    open.value = false
    return
  }
  saving.value = true
  try {
    const saved = await updateFolderFile(f.folder_file_id, { file_name: name.value }, HANDLE_ALL)
    emit('saved', saved)
    open.value = false
  } catch (e) {
    error.value = opErrorText(e, { kind: 'file', name: name.value })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-md" data-testid="rename-file" @open-auto-focus="onOpenAutoFocus">
      <DialogHeader>
        <DialogTitle>重命名</DialogTitle>
        <DialogDescription>只改文件目录里的名字，原图、快照、审核和归类都不受影响。</DialogDescription>
      </DialogHeader>
      <form id="rename-file-form" class="grid gap-3" @submit.prevent="submit">
        <div class="grid gap-2">
          <Label for="rf-name">新名字</Label>
          <Input id="rf-name" v-model="name" autocomplete="off" :maxlength="MAX_ENTRY_NAME_CHARS" data-testid="rename-file-name" />
        </div>
        <p v-if="error" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive" role="alert" data-testid="rename-file-error">
          {{ error }}
        </p>
      </form>
      <DialogFooter>
        <Button variant="outline" :disabled="saving" @click="open = false">取消</Button>
        <Button type="submit" form="rename-file-form" :disabled="saving" data-testid="rename-file-submit">
          {{ saving ? '保存中…' : '确定' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
