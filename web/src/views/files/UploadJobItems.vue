<script setup lang="ts">
// 一个上传任务的明细（M1-12）：用 @tanstack/vue-virtual 虚拟化，几千个文件时也只渲染看得见的几十行；
// 行高随说明的多少变化，渲染后按实际高度量（measureElement，内容变了 ResizeObserver 会重量）。
import { useVirtualizer } from '@tanstack/vue-virtual'
import { computed, ref } from 'vue'

import type { UploadJob } from '@/stores/uploads'

import UploadItemRow from './UploadItemRow.vue'

const props = defineProps<{ job: UploadJob }>()

/** 明细区域最高这么多像素，再多就在里面滚动。 */
const MAX_HEIGHT = 400

const scrollEl = ref<HTMLElement | null>(null)
const virtualizer = useVirtualizer(
  computed(() => ({
    count: props.job.items.length,
    getScrollElement: () => scrollEl.value,
    // 先按"一行名字 + 一行说明"估，渲染后按实际高度改。
    estimateSize: () => 52,
    overscan: 8,
  })),
)
const rows = computed(() => virtualizer.value.getVirtualItems())
const totalSize = computed(() => virtualizer.value.getTotalSize())

function measure(el: unknown) {
  if (el instanceof Element) virtualizer.value.measureElement(el)
}
</script>

<template>
  <div ref="scrollEl" class="overflow-y-auto" :style="{ height: `${Math.min(totalSize, MAX_HEIGHT)}px` }" data-testid="upload-items">
    <div class="relative w-full" :style="{ height: `${totalSize}px` }">
      <div
        v-for="row in rows"
        :key="row.index"
        :ref="measure"
        :data-index="row.index"
        class="absolute top-0 left-0 w-full"
        :style="{ transform: `translateY(${row.start}px)` }"
      >
        <UploadItemRow v-if="props.job.items[row.index]" :item="props.job.items[row.index]!" />
      </div>
    </div>
  </div>
</template>
