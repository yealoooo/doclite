<script setup lang="ts">
// 一个文件"归到了哪件档案"（只读，M3-7，用户决定 Q10）：`GET /api/files/{fid}/catalog`（能进项目即可）。
// 只显示档号（为空时"第 n 件"）、题名、第几页；没挂接时显示"未归类"。/review 右栏、/files 详情面板用。
// - 换文件后去抖 150 ms 再取（方向键、快速翻页时不连发）；`refreshToken` 变了重新取（SSE：这个文件的
//   `files.changed`，或任何 `catalog.changed`——档号、题名、页序变了），刷新期间先保留旧内容；换文件时马上清空，
//   在途的请求作废。
// - 文件已删除（404）时显示"文件已不存在"，不弹提示。
// 截图脚本用的属性：data-testid=catalog-info（data-state：loading、ready、error；data-placed：true / false；
// data-archive-no、data-page-no、data-item-id）。
import { LoaderCircleIcon } from '@lucide/vue'
import { computed, onBeforeUnmount, ref, watch } from 'vue'

import { getFileCatalog } from '@/api/catalog'
import { errorMessage, isApiError } from '@/api/http'
import type { CatalogPlacement } from '@/api/types'
import { placementNo } from '@/lib/catalog'

const props = withDefaults(
  defineProps<{
    fileId: string | null
    /** 变了就重新取。 */
    refreshToken?: number
    /** 标题下面的一句说明（例如"不影响审核"）。 */
    note?: string
  }>(),
  { refreshToken: 0, note: '' },
)

const placement = ref<CatalogPlacement | null>(null)
const loadedFor = ref<string | null>(null)
const loading = ref(false)
const loadError = ref<string | null>(null)
let timer: ReturnType<typeof setTimeout> | null = null
let seq = 0

async function load(fileId: string) {
  const my = ++seq
  loading.value = true
  try {
    const res = await getFileCatalog(fileId, true)
    if (my !== seq || props.fileId !== fileId) return
    placement.value = res.placement
    loadedFor.value = fileId
    loadError.value = null
  } catch (e) {
    if (my !== seq || props.fileId !== fileId) return
    placement.value = null
    loadedFor.value = null
    loadError.value = isApiError(e, 'not_found') ? '文件已不存在' : errorMessage(e)
  } finally {
    if (my === seq) loading.value = false
  }
}

watch(
  () => [props.fileId, props.refreshToken] as const,
  ([fileId], old) => {
    if (timer) clearTimeout(timer)
    if (!fileId || old?.[0] !== fileId) {
      seq++
      placement.value = null
      loadedFor.value = null
      loadError.value = null
      loading.value = false
    }
    if (!fileId) return
    loading.value = true
    timer = setTimeout(() => void load(fileId), 150)
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
  seq++
})

const ready = computed(() => loadedFor.value !== null && loadedFor.value === props.fileId)
const state = computed(() => (loadError.value ? 'error' : ready.value ? 'ready' : 'loading'))
</script>

<template>
  <section
    class="rounded-lg bg-violet-50 px-3 py-2 text-xs leading-5 text-violet-950 shadow-[0_0_0_1px_rgba(109,40,217,0.12)]"
    data-testid="catalog-info"
    :data-state="state"
    :data-placed="ready ? String(!!placement) : undefined"
    :data-archive-no="ready && placement ? placementNo(placement) : undefined"
    :data-page-no="ready && placement ? placement.page_no : undefined"
    :data-item-id="ready && placement ? placement.item_id : undefined"
  >
    <div class="flex items-center gap-1.5 text-[11px] text-violet-900/70">
      <span>归类信息（只读）</span>
      <span v-if="props.note">· {{ props.note }}</span>
      <LoaderCircleIcon v-if="loading" class="ml-auto size-3 animate-spin" />
    </div>
    <p v-if="loadError" class="text-destructive">{{ loadError }}</p>
    <template v-else-if="ready">
      <template v-if="placement">
        <div class="flex flex-wrap items-baseline gap-x-2" :title="`已挂接：${placementNo(placement)} 第 ${placement.page_no} 页`">
          <b
            class="min-w-0 font-medium [overflow-wrap:anywhere] text-foreground"
            :class="placement.archive_no ? 'font-mono text-[12px]' : ''"
            data-testid="catalog-info-no"
          >
            {{ placementNo(placement) }}
          </b>
          <span class="shrink-0 whitespace-nowrap text-foreground" data-testid="catalog-info-page">第 {{ placement.page_no }} 页</span>
        </div>
        <div class="[overflow-wrap:anywhere] text-foreground/80" data-testid="catalog-info-title">
          {{ placement.title || '（还没填题名）' }}
        </div>
      </template>
      <div v-else class="text-violet-900/80" data-testid="catalog-info-none">未归类</div>
    </template>
    <div v-else class="text-violet-900/50">正在取…</div>
  </section>
</template>
