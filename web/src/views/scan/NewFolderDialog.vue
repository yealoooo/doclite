<script setup lang="ts">
// 选目标文件夹时"新建文件夹"（M1-13，放在 FolderPicker 的 footer-start 插槽里）：在当前选中的文件夹下面建一个
// 子文件夹，可以带备注（原件位置，重扫时用来找原纸）。`POST /api/folders`（菜单码 scan 或 files 都行，design §7）。
// 名字的规则由 server 校验（design §1.7：不能有 \ / : * ? < > | 和双引号、不能是 Windows 保留名……），
// 同名返回 409 `conflict`；错误都显示在对话框里。
import { ref, watch } from 'vue'

import { errorMessage, request } from '@/api/http'
import type { FolderItem } from '@/api/types'
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

/** 备注最长多少个字符（和 server 的 MAX_REMARK_CHARS 一致）。 */
const MAX_REMARK_CHARS = 500
/** 文件夹名最长多少个字符（design §1.7）。 */
const MAX_NAME_CHARS = 255

const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  /** 建在哪个文件夹下面。 */
  parent: { id: string; path: string } | null
}>()
const emit = defineEmits<{ created: [folder: FolderItem] }>()

const name = ref('')
const remark = ref('')
const error = ref('')
const saving = ref(false)

watch(open, (v) => {
  if (!v) return
  name.value = ''
  remark.value = ''
  error.value = ''
})

async function submit() {
  if (!props.parent || saving.value) return
  const folderName = name.value.trim()
  if (!folderName) {
    error.value = '请填写文件夹名'
    return
  }
  if ([...folderName].length > MAX_NAME_CHARS) {
    error.value = `文件夹名最长 ${MAX_NAME_CHARS} 个字符`
    return
  }
  if ([...remark.value.trim()].length > MAX_REMARK_CHARS) {
    error.value = `备注最长 ${MAX_REMARK_CHARS} 个字符`
    return
  }
  saving.value = true
  error.value = ''
  try {
    const folder = await request<FolderItem>('/api/folders', {
      method: 'POST',
      body: { parent_id: props.parent.id, folder_name: folderName, remark: remark.value.trim() || undefined },
      handle: true,
    })
    emit('created', folder)
    open.value = false
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-md" data-testid="new-folder-dialog">
      <DialogHeader>
        <DialogTitle>新建文件夹</DialogTitle>
        <DialogDescription>
          建在 <span class="font-medium text-foreground">{{ props.parent?.path ?? '/' }}</span> 下面。
        </DialogDescription>
      </DialogHeader>
      <form id="new-folder-form" class="grid gap-4" @submit.prevent="submit">
        <div class="grid gap-2">
          <Label for="nf-name">文件夹名</Label>
          <Input id="nf-name" v-model="name" autocomplete="off" placeholder="例如 盒 WS-2025-005" data-testid="new-folder-name" />
        </div>
        <div class="grid gap-2">
          <Label for="nf-remark">备注（可选）</Label>
          <Textarea
            id="nf-remark"
            v-model="remark"
            rows="2"
            placeholder="原件位置，例如 3 号库 A 架 13 层（重扫时用来找原纸）"
            data-testid="new-folder-remark"
          />
        </div>
        <p v-if="error" class="text-sm text-destructive" role="alert" data-testid="new-folder-error">{{ error }}</p>
      </form>
      <DialogFooter>
        <Button variant="outline" :disabled="saving" @click="open = false">取消</Button>
        <Button type="submit" form="new-folder-form" :disabled="saving || !props.parent" data-testid="new-folder-submit">
          {{ saving ? '正在建…' : '新建' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
