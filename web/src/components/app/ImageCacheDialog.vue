<script setup lang="ts">
// 用户菜单里的"本机图片缓存"（M1-11，只在壳里出现）：对话框里放 ImageCachePanel。每次打开都重新读取。
import ImageCachePanel from '@/components/app/ImageCachePanel.vue'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'

const open = defineModel<boolean>('open', { required: true })
</script>

<template>
  <Dialog v-model:open="open">
    <!-- 打开时不自动聚焦到第一个输入框（上限），免得误改。 -->
    <DialogContent class="gap-4 sm:max-w-2xl" data-testid="image-cache-dialog" @open-auto-focus.prevent>
      <DialogHeader>
        <DialogTitle>本机图片缓存</DialogTitle>
        <DialogDescription>看过的封面和原图存在这台电脑上，再次打开时不用重新下载。</DialogDescription>
      </DialogHeader>
      <ImageCachePanel v-if="open" />
    </DialogContent>
  </Dialog>
</template>
