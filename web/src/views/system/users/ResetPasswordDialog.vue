<script setup lang="ts">
// 重置密码：成功后该用户所有会话失效，需要用新密码重新登录。
// 重置的是自己时，当前会话也会失效：成功后清掉本地会话、跳登录页（登录后回到这里）。
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'

import { errorMessage, HANDLE_ALL } from '@/api/http'
import { resetPassword } from '@/api/system'
import type { UserItem } from '@/api/types'
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
import { checkPassword } from '@/lib/validate'
import { useSessionStore } from '@/stores/session'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{ user: UserItem | null }>()

const session = useSessionStore()
const route = useRoute()
const router = useRouter()
const isSelf = computed(() => !!props.user && props.user.id === session.me?.id)

const password = ref('')
const confirm = ref('')
const error = ref('')
const saving = ref(false)

watch(open, (v) => {
  if (!v) return
  password.value = ''
  confirm.value = ''
  error.value = ''
})

async function submit() {
  if (!props.user) return
  error.value = checkPassword(password.value)
  if (error.value) return
  if (password.value !== confirm.value) {
    error.value = '两次输入的密码不一致'
    return
  }
  error.value = ''
  saving.value = true
  try {
    await resetPassword(props.user.id, password.value, HANDLE_ALL)
    open.value = false
    if (isSelf.value) {
      const redirect = route.fullPath
      session.clear()
      toast.success('密码已重置，请用新密码重新登录')
      await router.replace({ name: 'login', query: { redirect } })
      return
    }
    toast.success(`已重置 ${props.user.display_name} 的密码`)
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
        <DialogTitle>重置密码</DialogTitle>
        <DialogDescription v-if="isSelf">
          给你自己的账号（{{ user?.username }}）设置新密码。重置后你已登录的会话都会失效，需要用新密码重新登录。
        </DialogDescription>
        <DialogDescription v-else>
          给 {{ user?.display_name }}（{{ user?.username }}）设置新密码。重置后他已登录的会话都会失效。
        </DialogDescription>
      </DialogHeader>
      <form id="reset-form" class="grid gap-4" @submit.prevent="submit">
        <div class="grid gap-2">
          <Label for="rp-password">新密码</Label>
          <Input id="rp-password" v-model="password" type="password" autocomplete="new-password" />
        </div>
        <div class="grid gap-2">
          <Label for="rp-confirm">再输一次</Label>
          <Input id="rp-confirm" v-model="confirm" type="password" autocomplete="new-password" />
        </div>
        <p v-if="error" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive" role="alert">
          {{ error }}
        </p>
      </form>
      <DialogFooter>
        <Button variant="outline" :disabled="saving" @click="open = false">取消</Button>
        <Button type="submit" form="reset-form" :disabled="saving">
          {{ saving ? '保存中…' : '重置密码' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
