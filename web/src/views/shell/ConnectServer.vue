<script setup lang="ts">
// 连接服务器（只在壳里出现）：第一次启动还没有服务器地址时，或者用户要修改地址时。
// 由壳探测 {地址}/api/health 和 /api/shell/latest，能连上才保存；保存后整页重载。
import { computed, ref } from 'vue'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { isShellError, probeServer, shellErrorMessage } from '@/lib/shell'
import { useShellStore } from '@/stores/shell'

const shell = useShellStore()

const address = ref(shell.serverUrl ?? '')
const error = ref('')
const busy = ref(false)
const editing = computed(() => shell.serverUrl !== null)
const canSubmit = computed(() => address.value.trim() !== '' && !busy.value)

async function submit() {
  if (!canSubmit.value) return
  error.value = ''
  busy.value = true
  try {
    const result = await probeServer(address.value.trim())
    if (!result.reachable) {
      error.value = `连不上 ${result.server_url}：${result.error ?? '原因不明'}`
      return
    }
    // 版本过低也先保存：重载后显示升级提示。
    await shell.saveServer(result.server_url)
  } catch (e) {
    error.value = isShellError(e) && e.kind === 'invalid_request' ? e.message : `保存失败：${shellErrorMessage(e)}`
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <form class="flex flex-col gap-4" data-testid="connect-server" @submit.prevent="submit">
    <div>
      <h1 class="text-lg font-semibold tracking-tight text-foreground">
        {{ editing ? '修改服务器地址' : '连接服务器' }}
      </h1>
      <p class="mt-1 text-sm text-muted-foreground">
        填写 Doclite 服务器的地址，可以问系统管理员要。
      </p>
    </div>

    <div class="flex flex-col gap-2">
      <Label for="server-address">服务器地址</Label>
      <Input
        id="server-address"
        v-model="address"
        placeholder="例如 http://192.168.1.10:9080"
        autocomplete="off"
        spellcheck="false"
        autofocus
      />
    </div>

    <p
      v-if="error"
      class="rounded-md bg-destructive-soft px-3 py-2 text-sm break-all text-destructive"
      role="alert"
      data-testid="connect-error"
    >
      {{ error }}
    </p>
    <p v-else-if="editing" class="rounded-md bg-info-soft px-3 py-2 text-sm text-info">
      换到另一台服务器后需要重新登录。
    </p>

    <div class="mt-1 flex gap-2">
      <Button v-if="editing" type="button" variant="outline" class="flex-1" :disabled="busy" @click="shell.cancelEdit()">
        取消
      </Button>
      <Button type="submit" class="flex-1" :disabled="!canSubmit" data-testid="connect-submit">
        {{ busy ? '正在连接…' : '连接' }}
      </Button>
    </div>
  </form>
</template>
