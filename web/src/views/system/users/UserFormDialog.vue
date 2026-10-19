<script setup lang="ts">
// 新建用户（登录名、姓名、初始密码、角色）或编辑资料（登录名、姓名）。
// 接口错误（重名、校验失败、admin_required……）显示在对话框里。
import { computed, ref, watch } from 'vue'

import { errorMessage, HANDLE_ALL } from '@/api/http'
import { createUser, updateUser, type UpdateUserBody } from '@/api/system'
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
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { checkDisplayName, checkPassword, checkUsername } from '@/lib/validate'

import RoleChecklist from './RoleChecklist.vue'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  /** 编辑的用户；为 null 时是新建。 */
  user: UserItem | null
  roles: RoleItem[]
}>()

const emit = defineEmits<{ saved: [user: UserItem] }>()

const username = ref('')
const displayName = ref('')
const password = ref('')
const roleIds = ref<string[]>([])
const error = ref('')
const saving = ref(false)

const isEdit = computed(() => props.user !== null)

watch(open, (v) => {
  if (!v) return
  username.value = props.user?.username ?? ''
  displayName.value = props.user?.display_name ?? ''
  password.value = ''
  roleIds.value = []
  error.value = ''
})

/** 和后端一致的校验（长度按字符计数）。 */
function validate(): string {
  return (
    checkUsername(username.value) ||
    checkDisplayName(displayName.value) ||
    (isEdit.value ? '' : checkPassword(password.value))
  )
}

async function submit() {
  error.value = validate()
  if (error.value) return
  saving.value = true
  try {
    let saved: UserItem
    if (props.user) {
      const body: UpdateUserBody = {}
      if (username.value.trim() !== props.user.username) body.username = username.value.trim()
      if (displayName.value.trim() !== props.user.display_name) body.display_name = displayName.value.trim()
      saved = Object.keys(body).length ? await updateUser(props.user.id, body, HANDLE_ALL) : props.user
    } else {
      saved = await createUser(
        {
          username: username.value.trim(),
          display_name: displayName.value.trim(),
          password: password.value,
          role_ids: roleIds.value,
        },
        HANDLE_ALL,
      )
    }
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
        <DialogTitle>{{ isEdit ? '编辑用户' : '新建用户' }}</DialogTitle>
        <DialogDescription>
          {{ isEdit ? '修改登录名和姓名。密码和角色在列表里单独操作。' : '新用户创建后即为启用状态，用初始密码登录。' }}
        </DialogDescription>
      </DialogHeader>

      <form id="user-form" class="grid gap-4" @submit.prevent="submit">
        <div class="grid grid-cols-2 gap-4">
          <div class="grid gap-2">
            <Label for="uf-username">登录名</Label>
            <Input id="uf-username" v-model="username" autocomplete="off" />
          </div>
          <div class="grid gap-2">
            <Label for="uf-display">姓名</Label>
            <Input id="uf-display" v-model="displayName" autocomplete="off" />
          </div>
        </div>
        <template v-if="!isEdit">
          <div class="grid gap-2">
            <Label for="uf-password">初始密码</Label>
            <Input id="uf-password" v-model="password" type="password" autocomplete="new-password" />
            <p class="text-xs text-muted-foreground">6 到 128 个字符</p>
          </div>
          <div class="grid gap-2">
            <Label>角色</Label>
            <RoleChecklist v-model="roleIds" :roles="roles" :original="[]" />
          </div>
        </template>
        <p v-if="error" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive" role="alert">
          {{ error }}
        </p>
      </form>

      <DialogFooter>
        <Button variant="outline" :disabled="saving" @click="open = false">取消</Button>
        <Button type="submit" form="user-form" :disabled="saving">
          {{ saving ? '保存中…' : isEdit ? '保存' : '创建' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
