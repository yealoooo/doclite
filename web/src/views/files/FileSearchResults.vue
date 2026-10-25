<script setup lang="ts">
// 文件目录的文件名搜索结果（M11-4，用户 2026-10-25）：在当前文件夹和它的全部子文件夹里搜（`GET /api/folders/{id}/search`），
// 结果替换主区的文件列表：每行文件名和所在位置，点一行跳到那个文件夹并选中这个文件（由页面处理 `pick`）。
// 关键字由页面去抖后传进来；只显示最后一次请求的结果。
import { FileIcon, LoaderCircleIcon } from '@lucide/vue'
import { ref, watch } from 'vue'

import { searchFolderFiles } from '@/api/folders'
import { errorMessage, HANDLE_ALL } from '@/api/http'
import type { FolderSearchHit, FolderSearchResult } from '@/api/types'

const props = defineProps<{ folderId: string; folderName: string; query: string }>()

const emit = defineEmits<{ pick: [hit: FolderSearchHit] }>()

/** 一次最多显示的条数（server 最多 200）。 */
const LIMIT = 100

const result = ref<FolderSearchResult | null>(null)
const loading = ref(false)
const error = ref('')
let seq = 0

watch(
  () => [props.folderId, props.query] as const,
  async ([id, q]) => {
    const mine = ++seq
    loading.value = true
    error.value = ''
    try {
      const r = await searchFolderFiles(id, q, LIMIT, HANDLE_ALL)
      if (mine === seq) result.value = r
    } catch (e) {
      if (mine === seq) {
        result.value = null
        error.value = errorMessage(e)
      }
    } finally {
      if (mine === seq) loading.value = false
    }
  },
  { immediate: true },
)

/** 位置去掉开头的项目根文件夹：`/盒 1/件 3`；根是 `/`。 */
function locationOf(path: string): string {
  const i = path.indexOf('/')
  return i >= 0 ? path.slice(i) : '/'
}
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col" data-testid="search-results" :data-loading="loading">
    <div class="flex h-9 shrink-0 items-center gap-2 border-b bg-muted/40 px-4 text-xs text-muted-foreground">
      <LoaderCircleIcon v-if="loading" class="size-3.5 animate-spin" />
      <span class="truncate">
        在「{{ props.folderName }}」和它的子文件夹里搜索「{{ props.query }}」<template v-if="result && !loading">：{{ result.items.length }} 个文件</template>
      </span>
      <span v-if="result?.truncated" class="shrink-0 rounded bg-warn-soft px-1.5 text-warn" data-testid="search-truncated">
        只显示前 {{ result.items.length }} 个，请输入更具体的关键字
      </span>
      <div class="flex-1" />
      <span class="shrink-0">Esc 或清空搜索框回到文件列表</span>
    </div>
    <p v-if="error" class="m-4 rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive" role="alert">{{ error }}</p>
    <div
      v-else-if="result && result.items.length === 0 && !loading"
      class="flex flex-1 flex-col items-center justify-center gap-1 text-sm text-muted-foreground"
      data-testid="search-empty"
    >
      <span>没有文件名含「{{ props.query }}」的文件</span>
      <span class="text-xs">只搜当前文件夹和它的子文件夹；要搜整个项目，先回到根文件夹</span>
    </div>
    <ul v-else-if="result" class="min-h-0 flex-1 overflow-y-auto">
      <li v-for="hit in result.items" :key="hit.folder_file_id">
        <button
          type="button"
          class="flex w-full items-center gap-2 border-b border-border/60 px-4 py-2 text-left text-compact hover:bg-muted/60"
          data-testid="search-hit"
          :data-file-id="hit.file_id"
          @click="emit('pick', hit)"
        >
          <FileIcon class="size-4 shrink-0 text-muted-foreground" />
          <span class="min-w-0 flex-1 truncate text-foreground" :title="hit.file_name">{{ hit.file_name }}</span>
          <span class="max-w-[50%] shrink-0 truncate text-xs text-muted-foreground" :title="hit.folder_path">
            {{ locationOf(hit.folder_path) }}
          </span>
        </button>
      </li>
    </ul>
  </div>
</template>
