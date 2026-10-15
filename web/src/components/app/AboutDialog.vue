<script setup lang="ts">
// 关于：版本号、运行环境（方便远程排查）；壳里还显示服务器地址、本机数据目录，并能修改服务器地址。
import { computed } from 'vue'

import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useShellStore } from '@/stores/shell'

const open = defineModel<boolean>('open', { required: true })

const shell = useShellStore()
const runtime = computed(() => {
  if (!shell.inShell) return '浏览器'
  const platform = shell.info?.platform
  return platform ? `桌面客户端（${platform}）` : '桌面客户端'
})

function editServer() {
  open.value = false
  shell.editServer()
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent data-testid="about-dialog">
      <DialogHeader>
        <DialogTitle>关于 Doclite</DialogTitle>
        <DialogDescription>纸质档案数字化系统</DialogDescription>
      </DialogHeader>

      <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
        <dt class="text-muted-foreground">版本</dt>
        <dd class="font-mono text-foreground" data-testid="about-version">{{ shell.version }}</dd>
        <dt class="text-muted-foreground">运行环境</dt>
        <dd class="text-foreground">{{ runtime }}</dd>
        <template v-if="shell.inShell">
          <dt class="text-muted-foreground">服务器</dt>
          <dd class="font-mono break-all text-foreground">{{ shell.serverUrl ?? '—' }}</dd>
          <dt class="text-muted-foreground">数据目录</dt>
          <dd class="font-mono text-xs break-all text-foreground">{{ shell.info?.data_dir ?? '—' }}</dd>
        </template>
      </dl>

      <div v-if="shell.inShell" class="flex justify-end">
        <Button variant="outline" @click="editServer">修改服务器地址</Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
