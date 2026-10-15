<script setup lang="ts">
// 分配角色：整体替换用户的角色（PUT /api/system/users/{id}/roles）。
import { computed, ref, watch } from 'vue'

import { errorMessage } from '@/api/http'
import { setUserRoles } from '@/api/system'
import type { RoleItem, UserItem } from '@/api/types'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

import RoleChecklist from './RoleChecklist.vue'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  user: UserItem | null
  roles: RoleItem[]
}>()

const emit = defineEmits<{ saved: [user: UserItem] }>()

const selected = ref<string[]>([])
const error = ref('')
const saving = ref(false)

const original = computed(() => props.user?.roles.map((r) => r.id) ?? [])
const changed = computed(() => {
  const a = new Set(selected.value)
  return a.size !== original.value.length || original.value.some((id) => !a.has(id))
})

watch(open, (v) => {
  if (!v) return
  selected.value = [...original.value]
  error.value = ''
})

async function submit() {
  if (!props.user) return
  error.value = ''
  saving.value = true
  try {
    const saved = await setUserRoles(props.user.id, selected.value, true)
    emit('saved', saved)
    open.value = false
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>分配角色</DialogTitle>
        <DialogDescription>
          {{ user?.display_name }}（{{ user?.username }}）的菜单是所勾选角色的菜单并集。
        </DialogDescription>
      </DialogHeader>
      <RoleChecklist v-model="selected" :roles="roles" :original="original" />
      <p v-if="error" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive" role="alert">
        {{ error }}
      </p>
      <DialogFooter>
        <Button variant="outline" :disabled="saving" @click="open = false">取消</Button>
        <Button :disabled="saving || !changed" @click="submit">{{ saving ? '保存中…' : '保存' }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
