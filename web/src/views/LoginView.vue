<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { errorMessage, isApiError } from '@/api/http'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { safeRedirect } from '@/lib/redirect'
import { useSessionStore } from '@/stores/session'
import { useShellStore } from '@/stores/shell'

const session = useSessionStore()
const shell = useShellStore()
const router = useRouter()
const route = useRoute()

const username = ref('')
const password = ref('')
const error = ref('')
/** 错误码（M7-1：许可证的三种错误给专门的说明）。 */
const errorCode = ref('')
const submitting = ref(false)

/** 登录失败的说明：许可证挡住的三种给中文说明，其余用服务器的 message。 */
function loginError(err: unknown): string {
  if (isApiError(err, 'license_seats_full')) {
    const max = (err.details as { max_users?: number } | undefined)?.max_users
    return `在线人数已达到许可证的上限${max ? `（${max} 个）` : ''}，请等其他人退出后再登录，或联系管理员。同一账号在几处登录就占几个。`
  }
  if (isApiError(err, 'license_expired')) {
    return '系统的许可证已到期，暂时只有管理员能登录。请联系管理员导入新的许可证。'
  }
  if (isApiError(err, 'license_missing')) {
    return '系统还没有导入有效的许可证，暂时只有管理员能登录。请联系管理员。'
  }
  return errorMessage(err)
}

const canSubmit = computed(() => username.value.trim() !== '' && password.value !== '' && !submitting.value)

async function submit() {
  if (!canSubmit.value) return
  error.value = ''
  errorCode.value = ''
  submitting.value = true
  try {
    await session.login(username.value.trim(), password.value)
    await router.replace(safeRedirect(route.query.redirect) ?? '/')
  } catch (err) {
    error.value = loginError(err)
    errorCode.value = isApiError(err) ? err.code : ''
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="flex min-h-screen items-center justify-center bg-[#fafafa] px-4">
    <div class="w-full max-w-[360px]">
      <div class="mb-8 flex flex-col items-center gap-3 text-center">
        <span
          class="flex size-10 items-center justify-center rounded-lg bg-primary text-lg font-semibold text-primary-foreground"
        >D</span>
        <div>
          <h1 class="text-2xl font-semibold tracking-tight text-foreground">登录 Doclite</h1>
          <p class="mt-1 text-sm text-muted-foreground">纸质档案数字化系统</p>
        </div>
      </div>

      <form
        class="flex flex-col gap-4 rounded-xl bg-card p-6 shadow-[0_0_0_1px_rgba(0,0,0,0.08),0_2px_2px_rgba(0,0,0,0.04),0_8px_16px_-8px_rgba(0,0,0,0.08)]"
        @submit.prevent="submit"
      >
        <div class="flex flex-col gap-2">
          <Label for="username">登录名</Label>
          <Input id="username" v-model="username" autocomplete="username" autofocus />
        </div>
        <div class="flex flex-col gap-2">
          <Label for="password">密码</Label>
          <Input id="password" v-model="password" type="password" autocomplete="current-password" />
        </div>
        <p
          v-if="error"
          class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive"
          role="alert"
          data-testid="login-error"
          :data-code="errorCode"
        >
          {{ error }}
        </p>
        <Button type="submit" class="mt-1 w-full" :disabled="!canSubmit" data-testid="login-submit">
          {{ submitting ? '登录中…' : '登录' }}
        </Button>
      </form>

      <p class="mt-6 text-center text-xs text-muted-foreground">忘记密码请联系系统管理员重置</p>
      <!-- 壳里：显示连的是哪台服务器，可以修改（去客户端自带的修改地址页）。 -->
      <p v-if="shell.inShell" class="mt-2 text-center text-xs text-muted-foreground" data-testid="login-server">
        服务器 <span class="font-mono">{{ shell.serverUrl }}</span>
        <template v-if="shell.canEditServer">
          ·
          <button type="button" class="text-info hover:underline" @click="shell.editServer()">修改</button>
        </template>
      </p>
    </div>
  </div>
</template>
