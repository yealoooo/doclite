<script setup lang="ts">
// 重新归档（M9，requirements 5.9"重新归档"，Mockup v3.1 的 askReseal）：先给对比再确认。
// - 打开时取 `reseal-preview`：原来 n 页 → 现在 m 页，新增（在第几页）、去掉、顺序变了的页分别列出；没有变化也可以确认
//   （等于"没改，恢复封存"）。
// - `problem`（文件夹被删了、有了子文件夹、空了、超过 2000 页、新增的文件已在别的条目里）：写明原因，确认按钮置灰；
//   不要这一件就用"撤销归档"。
// - 确认带预览里的 row_version：409 conflict（这期间别人改了这一件）时重新取对比、提示再看一眼；409 invalid_state、
//   file_cataloged（文件夹刚又变了）同样重新取对比并显示 server 的说明。
// 成功后发 `resealed`（新的详情）。
// 截图脚本用的属性：data-testid=reseal-dialog（data-state：loading、ready、problem、error；data-old、data-new、
// data-added、data-removed、data-moved）；按钮 reseal-confirm。
import { LoaderCircleIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'

import { getResealPreview, resealArchiveItem } from '@/api/catalog'
import { ApiError, errorMessage, HANDLE_ALL, isApiError } from '@/api/http'
import type { ArchiveItem, ResealPreview } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  itemId: string | null
  /** 件的简称（"办公室 0003"）。 */
  label?: string
}>()

const emit = defineEmits<{ resealed: [item: ArchiveItem] }>()

const preview = ref<ResealPreview | null>(null)
const loadError = ref('')
const note = ref('')
const busy = ref<'' | 'load' | 'reseal'>('')

const state = computed(() => {
  if (loadError.value) return 'error'
  if (!preview.value) return 'loading'
  if (preview.value.problem) return 'problem'
  return 'ready'
})
const none = computed(() => !!preview.value && !preview.value.changed)
const notModifying = computed(() => !!preview.value && (preview.value.source_status !== 'modifying' || preview.value.status === 'locked'))

async function load() {
  const id = props.itemId
  if (!id) return
  busy.value = 'load'
  try {
    const p = await getResealPreview(id, HANDLE_ALL)
    if (props.itemId === id) {
      preview.value = p
      loadError.value = ''
    }
  } catch (e) {
    loadError.value = isApiError(e, 'not_found') ? '这一件已经不在了（可能刚被撤销归档）' : errorMessage(e)
  } finally {
    busy.value = ''
  }
}

watch(open, (v) => {
  if (!v) return
  preview.value = null
  loadError.value = ''
  note.value = ''
  void load()
})

async function confirm() {
  const p = preview.value
  if (!p || busy.value) return
  busy.value = 'reseal'
  note.value = ''
  try {
    const res = await resealArchiveItem(p.item_id, p.row_version, HANDLE_ALL)
    open.value = false
    emit('resealed', res)
  } catch (e) {
    note.value = isApiError(e, 'conflict')
      ? '这一件刚被别人修改过，下面的对比已经重新取，请再看一眼后确认'
      : e instanceof ApiError
        ? e.message
        : errorMessage(e)
    busy.value = ''
    if (e instanceof ApiError && e.status >= 400 && e.status < 500) await load()
  } finally {
    busy.value = ''
  }
}

function pageText(n: number) {
  return `第 ${n} 页`
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent
      class="sm:max-w-xl"
      data-testid="reseal-dialog"
      :data-state="state"
      :data-old="preview?.old_page_count"
      :data-new="preview?.new_page_count"
      :data-added="preview?.added.length"
      :data-removed="preview?.removed.length"
      :data-moved="preview?.moved.length"
    >
      <DialogHeader>
        <DialogTitle>重新归档 {{ props.label || '这一件' }}</DialogTitle>
        <DialogDescription>
          按文件夹「{{ preview?.folder_name ?? '…' }}」现在的样子更新页，然后再次封存。件号、档号、著录不变。
        </DialogDescription>
      </DialogHeader>

      <div v-if="state === 'loading'" class="flex items-center gap-2 text-sm text-muted-foreground">
        <LoaderCircleIcon class="size-4 animate-spin" />正在对比…
      </div>
      <p v-else-if="state === 'error'" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive">{{ loadError }}</p>
      <div v-else-if="preview" class="max-h-[52vh] space-y-2 overflow-y-auto text-compact">
        <div
          class="rounded-md px-3 py-2 text-xs"
          :class="none ? 'bg-muted text-foreground' : 'bg-modifying-soft text-foreground'"
          data-testid="reseal-summary"
        >
          原来 <b>{{ preview.old_page_count }}</b> 页 → 现在 <b>{{ preview.new_page_count }}</b> 页<template v-if="none">，没有变化（确认后恢复封存）</template>
          <span class="block text-muted-foreground" :title="preview.folder_path">来源：{{ preview.folder_path }}</span>
        </div>
        <p v-if="notModifying" class="rounded-md bg-warn-soft px-3 py-2 text-xs text-warn">
          这一件现在不在"修改中"（{{ preview.status === 'locked' ? '已锁定' : '已封存' }}），不需要重新归档。
        </p>
        <div v-if="preview.added.length" class="text-ok" data-testid="reseal-added">
          <div class="font-medium">＋ 新增 {{ preview.added.length }} 页</div>
          <ul class="mt-0.5 space-y-0.5 pl-4 text-xs">
            <li v-for="p in preview.added" :key="p.file_id" class="truncate" :title="`${p.folder_path}/${p.file_name}`">
              {{ p.file_name }}（{{ pageText(p.page_no) }}）
            </li>
          </ul>
        </div>
        <div v-if="preview.removed.length" class="text-destructive" data-testid="reseal-removed">
          <div class="font-medium">－ 去掉 {{ preview.removed.length }} 页</div>
          <ul class="mt-0.5 space-y-0.5 pl-4 text-xs">
            <li v-for="p in preview.removed" :key="p.file_id" class="truncate">
              {{ p.file_name }}（原{{ pageText(p.page_no) }}{{ p.deleted ? '，文件已删除' : `，现在在 ${p.folder_path}` }}）
            </li>
          </ul>
        </div>
        <div v-if="preview.moved.length" class="text-info" data-testid="reseal-moved">
          <div class="font-medium">↕ 顺序变了 {{ preview.moved.length }} 页</div>
          <ul class="mt-0.5 space-y-0.5 pl-4 text-xs">
            <li v-for="m in preview.moved" :key="m.file_id" class="truncate">
              {{ m.file_name }}：{{ pageText(m.from_page) }} → {{ pageText(m.to_page) }}
            </li>
          </ul>
        </div>
        <div v-if="preview.problem" class="rounded-md bg-destructive-soft px-3 py-2 text-xs text-destructive" role="alert" data-testid="reseal-problem" :data-code="preview.problem.code">
          <p>{{ preview.problem.message }}</p>
          <ul v-if="preview.problem.items?.length" class="mt-1 space-y-0.5">
            <li v-for="x in preview.problem.items" :key="x.file_id" class="truncate">
              在 {{ [...x.category_names, x.archive_no ?? `第 ${x.item_no} 件`].join(' / ') }} 第 {{ x.page_no }} 页
            </li>
          </ul>
          <p class="mt-1 text-destructive">改好文件夹后再重新归档；不要这一件了请用"撤销归档"。</p>
        </div>
      </div>
      <p v-if="note" class="rounded-md bg-warn-soft px-3 py-2 text-xs text-warn" role="alert" data-testid="reseal-note">{{ note }}</p>
      <DialogFooter class="gap-2">
        <Button variant="outline" :disabled="busy === 'reseal'" @click="open = false">再改改</Button>
        <Button
          :disabled="state !== 'ready' || notModifying || busy !== ''"
          data-testid="reseal-confirm"
          @click="confirm"
        >
          <LoaderCircleIcon v-if="busy === 'reseal'" class="animate-spin" />确认，重新归档并封存
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
