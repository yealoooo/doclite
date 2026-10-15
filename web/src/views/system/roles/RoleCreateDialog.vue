<script setup lang="ts">
// 新建角色：编码、名称、说明。菜单在右侧的权限树里勾选后保存。
import { ref, watch } from 'vue'

import { errorMessage } from '@/api/http'
import { createRole } from '@/api/system'
import type { RoleItem } from '@/api/types'
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
import { checkDescription, checkRoleCode, checkRoleName } from '@/lib/validate'

const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ created: [role: RoleItem] }>()

const roleCode = ref('')
const roleName = ref('')
const description = ref('')
const error = ref('')
const saving = ref(false)

watch(open, (v) => {
  if (!v) return
  roleCode.value = ''
  roleName.value = ''
  description.value = ''
  error.value = ''
})

/** 和后端一致的校验（长度按字符计数）。 */
function validate(): string {
  return checkRoleCode(roleCode.value) || checkRoleName(roleName.value) || checkDescription(description.value)
}

async function submit() {
  error.value = validate()
  if (error.value) return
  saving.value = true
  try {
    const role = await createRole(
      {
        role_code: roleCode.value.trim(),
        role_name: roleName.value.trim(),
        description: description.value.trim() || undefined,
      },
      true,
    )
    emit('created', role)
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
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>新建角色</DialogTitle>
        <DialogDescription>创建后在右侧勾选这个角色能看到的菜单。</DialogDescription>
      </DialogHeader>
      <form id="role-form" class="grid gap-4" @submit.prevent="submit">
        <div class="grid gap-2">
          <Label for="rf-code">角色编码</Label>
          <Input id="rf-code" v-model="roleCode" class="font-mono" placeholder="例如 qc_leader" autocomplete="off" />
          <p class="text-xs text-muted-foreground">小写字母开头，只能有小写字母、数字和下划线；建好后不能改</p>
        </div>
        <div class="grid gap-2">
          <Label for="rf-name">角色名称</Label>
          <Input id="rf-name" v-model="roleName" placeholder="例如 质检组长" autocomplete="off" />
        </div>
        <div class="grid gap-2">
          <Label for="rf-desc">说明（可选）</Label>
          <Textarea id="rf-desc" v-model="description" rows="2" />
        </div>
        <p v-if="error" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive" role="alert">
          {{ error }}
        </p>
      </form>
      <DialogFooter>
        <Button variant="outline" :disabled="saving" @click="open = false">取消</Button>
        <Button type="submit" form="role-form" :disabled="saving">{{ saving ? '创建中…' : '创建' }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
