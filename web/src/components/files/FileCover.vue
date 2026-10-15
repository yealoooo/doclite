<script setup lang="ts">
// 封面（M1-10）：按固定比例（A4 竖版）占位，图片按比例缩放居中。地址一律通过 imageSrc() 取。
// 状态（data-cover-state，截图脚本用）：
// - loading：图片信息还没取到；
// - none：没有封面（cover_url 为 null），显示"无封面"占位；
// - error：取不到 URL（url_error：存储停用、密钥解不开），同样显示"无封面"占位，悬停看原因；
// - broken：<img> 加载失败（例如 URL 过期，会让 store 重取一次）；
// - loaded / loading-image：图片已显示 / 正在下载。
import { ImageOffIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import type { SnapshotImageFields } from '@/api/types'
import { imageSrc } from '@/lib/image-url'
import { cn } from '@/lib/utils'

const props = defineProps<{
  fileId: string
  /** 图片信息（batch 或 snapshots 的一项）；还没取到时为 undefined。 */
  info?: Pick<SnapshotImageFields, 'snapshot_id' | 'cover_url' | 'original_url' | 'url_error'>
  /** 小尺寸（列表视图）：占位里只显示图标。 */
  compact?: boolean
  class?: string
}>()

const emit = defineEmits<{ broken: [] }>()

const src = computed(() =>
  props.info
    ? imageSrc(
        {
          file_id: props.fileId,
          snapshot_id: props.info.snapshot_id,
          cover_url: props.info.cover_url,
          original_url: props.info.original_url,
        },
        'cover',
      )
    : null,
)

const loaded = ref(false)
const broken = ref(false)
/** 换了就重新建 <img>（地址没变时也会重新请求一次）。 */
const attempt = ref(0)
watch(src, () => {
  loaded.value = false
  broken.value = false
})
// 加载失败后，图片信息换了一份（store 重取了，哪怕 URL 相同：同一天签的预签名 URL、壳的 img:// 地址都不变）：
// 清掉失败状态再试一次。
watch(
  () => props.info,
  () => {
    if (!broken.value) return
    broken.value = false
    loaded.value = false
    attempt.value++
  },
)

const state = computed(() => {
  if (!props.info) return 'loading'
  if (props.info.url_error) return 'error'
  if (!src.value) return 'none'
  if (broken.value) return 'broken'
  return loaded.value ? 'loaded' : 'loading-image'
})

const title = computed(() => {
  if (state.value === 'error') return `无封面：${props.info?.url_error?.message ?? '取不到图片地址'}`
  if (state.value === 'none') return '无封面（上传时没有带封面）'
  if (state.value === 'broken') return '封面加载失败'
  return undefined
})

function onError() {
  broken.value = true
  emit('broken')
}
</script>

<template>
  <div
    :class="
      cn(
        'relative flex aspect-[1/1.414] items-center justify-center overflow-hidden rounded-[3px] bg-white shadow-[0_0_0_1px_rgba(0,0,0,0.10)]',
        state === 'loading' && 'animate-pulse bg-muted',
        props.class,
      )
    "
    :data-cover-state="state"
    :title="title"
    data-testid="file-cover"
  >
    <img
      v-if="src && !broken && !props.info?.url_error"
      :key="attempt"
      :src="src"
      alt=""
      draggable="false"
      decoding="async"
      class="size-full object-contain"
      :class="loaded ? '' : 'opacity-0'"
      @load="loaded = true"
      @error="onError"
    />
    <div
      v-else-if="state !== 'loading'"
      class="flex flex-col items-center gap-1 px-1 text-center text-muted-foreground"
      :class="state === 'none' || state === 'error' ? 'bg-[repeating-linear-gradient(135deg,transparent_0_6px,rgba(0,0,0,0.025)_6px_12px)] absolute inset-0 justify-center' : ''"
    >
      <ImageOffIcon :class="props.compact ? 'size-3.5' : 'size-5'" />
      <template v-if="!props.compact">
        <span class="text-[11px] leading-tight">{{ state === 'broken' ? '加载失败' : '无封面' }}</span>
        <span v-if="state === 'error'" class="text-[10px] leading-tight text-destructive">存储不可用</span>
      </template>
    </div>
    <slot />
  </div>
</template>
