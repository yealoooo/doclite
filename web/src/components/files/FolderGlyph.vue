<script setup lang="ts">
// 文件夹图标（M10-5）：黄色文件夹，颜色只在这里（style.css 的 --folder、--folder-fill）。`open` 时画打开的文件夹。
// 大小用 class 给（size-4、size-16……）。
import { FolderIcon, FolderOpenIcon } from '@lucide/vue'

const props = withDefaults(defineProps<{ open?: boolean; strokeWidth?: number }>(), { open: false, strokeWidth: 1.5 })
</script>

<template>
  <component :is="props.open ? FolderOpenIcon : FolderIcon" class="shrink-0 fill-folder-fill text-folder" :stroke-width="props.strokeWidth" aria-hidden="true" />
</template>
