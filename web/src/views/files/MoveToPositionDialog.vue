<script setup lang="ts">
// 移到第…页（M11-4，用户 2026-10-25）：把选中的文件（保持原来的先后）放到当前文件夹的第 n 页起，
// `PUT /api/folders/{id}/order` 带文件夹里全部文件的新顺序。页 = 按扫描顺序数的位置（归档按这个顺序排页）。
// 409 `conflict`（文件夹里的文件刚变了）：关掉对话框、提示并刷新（`conflict` 事件）；其他错误显示在对话框里。
import { computed, ref, watch } from 'vue'

import { reorderFolderFiles } from '@/api/folders'
import { ApiError, errorMessage, HANDLE_ALL, isApiError } from '@/api/http'
import type { FileEntry } from '@/api/types'
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

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  folderId: string | null
  /** 文件夹里的全部文件，按扫描顺序。 */
  files: FileEntry[]
  /** 要移动的文件。 */
  selected: FileEntry[]
}>()

const emit = defineEmits<{ saved: [position: number]; conflict: [message: string] }>()

const pos = ref<string | number>(1)
const error = ref('')
const saving = ref(false)

const picked = computed(() => {
  const ids = new Set(props.selected.map((f) => f.folder_file_id))
  return props.files.filter((f) => ids.has(f.folder_file_id))
})
const rest = computed(() => {
  const ids = new Set(props.selected.map((f) => f.folder_file_id))
  return props.files.filter((f) => !ids.has(f.folder_file_id))
})
/** 第 n 页最大能填多少（选中的放到最后）。 */
const maxPos = computed(() => rest.value.length + 1)
const n = computed(() => Number(pos.value))
const valid = computed(() => Number.isInteger(n.value) && n.value >= 1 && n.value <= maxPos.value)
/** 放在谁前面（预览）。 */
const hint = computed(() => {
  if (!valid.value) return `请输入 1 到 ${maxPos.value} 之间的整数`
  const next = rest.value[n.value - 1]
  return next ? `放在「${next.file_name}」前面` : '放到最后'
})

watch(open, (v) => {
  if (!v) return
  const first = picked.value[0]
  pos.value = first ? props.files.indexOf(first) + 1 : 1
  error.value = ''
})

async function submit() {
  const id = props.folderId
  if (!id || !valid.value || saving.value) return
  const order = [...rest.value.slice(0, n.value - 1), ...picked.value, ...rest.value.slice(n.value - 1)]
  if (order.every((f, i) => f === props.files[i])) {
    open.value = false
    return
  }
  saving.value = true
  error.value = ''
  try {
    await reorderFolderFiles(
      id,
      order.map((f) => f.folder_file_id),
      HANDLE_ALL,
    )
    emit('saved', n.value)
    open.value = false
  } catch (e) {
    if (isApiError(e, 'conflict')) {
      emit('conflict', (e as ApiError).message)
      open.value = false
    } else {
      error.value = errorMessage(e)
    }
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-md" data-testid="move-to-position">
      <DialogHeader>
        <DialogTitle>{{ picked.length === 1 ? `把「${picked[0]!.file_name}」移到第…页` : `把 ${picked.length} 个文件移到第…页` }}</DialogTitle>
        <DialogDescription>
          按扫描顺序数，这个文件夹共 {{ props.files.length }} 个文件；<template v-if="picked.length > 1">选中的文件保持原来的先后、排在一起，</template>归档时按这个顺序排页。
        </DialogDescription>
      </DialogHeader>
      <form id="move-to-position-form" class="grid gap-2" @submit.prevent="submit">
        <Label for="mp-pos">从第几页开始</Label>
        <div class="flex items-center gap-3">
          <Input id="mp-pos" v-model="pos" type="number" min="1" :max="maxPos" class="w-28" autocomplete="off" data-testid="move-to-position-input" />
          <span class="truncate text-sm" :class="valid ? 'text-muted-foreground' : 'text-destructive'" data-testid="move-to-position-hint">{{ hint }}</span>
        </div>
        <p v-if="error" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive" role="alert">{{ error }}</p>
      </form>
      <DialogFooter>
        <Button variant="outline" :disabled="saving" @click="open = false">取消</Button>
        <Button type="submit" form="move-to-position-form" :disabled="saving || !valid" data-testid="move-to-position-submit">
          {{ saving ? '保存中…' : '确定' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
