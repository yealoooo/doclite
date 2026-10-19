<script setup lang="ts">
// 归档预览和确认（M9，requirements 5.9"归档"，Mockup v3.1 的 askArchive）：左栏选好的文件夹放到右边的节点上时打开。
// - 打开时先 `dry_run` 取预览：每件的件号、题名（默认文件夹名，可改）、页数、来源路径；跳过的和原因（已归档、含下级
//   文件夹、空文件夹、超过 2000 页）。一件都建不出时只显示跳过的，确认按钮置灰。
// - 被当成上级的文件夹（盒子）自己直接放的文件不会归档（`loose_files`），写成提示。
// - "归档并封存"：同一个请求去掉 dry_run，带改过的题名（`titles {folder_id: 题名}`，没改的不带）。
//   - 400 带 details.fields（题名不合规）：标到对应的行；别的 400（例如展开后总页数超过 5 万）显示 server 的说明；
//   - 409 invalid_state 带预览（这期间文件夹变了，一件都建不出）、file_cataloged：显示说明，重新取预览；
//   - 404（文件夹刚被删）：提示并关掉，页面刷新左栏。
// 成功后发 `archived`（server 的结果，含新条目的 id）。
// 截图脚本用的属性：data-testid=archive-dialog（data-state：loading、ready、empty、error；data-items、data-skipped）；
// 每行 archive-row（data-folder-id），题名框 archive-title；跳过的 archive-skipped（data-reason）；按钮 archive-confirm。
import { LoaderCircleIcon } from '@lucide/vue'
import { computed, reactive, ref, watch } from 'vue'

import { archiveFolders } from '@/api/catalog'
import { ApiError, errorMessage, HANDLE_ALL, isApiError } from '@/api/http'
import type { ArchiveResult } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { ARCHIVE_SKIP_LABEL, checkTitle, fieldIssuesOf, formatItemNo } from '@/lib/catalog'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  categoryId: string | null
  /** 节点从顶层起的名称路径（标题里用）。 */
  categoryLabel: string
  folderIds: string[]
  itemNoWidth: number
}>()

const emit = defineEmits<{ archived: [result: ArchiveResult]; stale: [] }>()

const plan = ref<ArchiveResult | null>(null)
const loadError = ref('')
const note = ref('')
const busy = ref<'' | 'load' | 'archive'>('')
/** folder_id → 题名（表单）。 */
const titles = reactive<Record<string, string>>({})
/** folder_id → 题名的错误。 */
const rowErrors = ref<Record<string, string>>({})

const state = computed(() => {
  if (loadError.value) return 'error'
  if (!plan.value) return 'loading'
  return plan.value.items.length ? 'ready' : 'empty'
})

async function load() {
  const cid = props.categoryId
  if (!cid || !props.folderIds.length) return
  busy.value = 'load'
  try {
    const res = await archiveFolders(cid, { folder_ids: props.folderIds, dry_run: true }, HANDLE_ALL)
    plan.value = res
    loadError.value = ''
    for (const k of Object.keys(titles)) delete titles[k]
    for (const it of res.items) titles[it.folder_id] = it.title
  } catch (e) {
    if (isApiError(e, 'invalid_state') && e.details && typeof e.details === 'object' && 'skipped' in (e.details as object)) {
      plan.value = e.details as ArchiveResult
      note.value = (e as ApiError).message
    } else {
      loadError.value =
        isApiError(e, 'not_found') ? '有文件夹已不存在（可能刚被删除或移走），请重新选择' : e instanceof ApiError ? e.message : errorMessage(e)
      if (isApiError(e, 'not_found')) emit('stale')
    }
  } finally {
    busy.value = ''
  }
}

watch(open, (v) => {
  if (!v) return
  plan.value = null
  loadError.value = ''
  note.value = ''
  rowErrors.value = {}
  void load()
})

function onTitleInput(id: string) {
  if (rowErrors.value[id]) {
    const next = { ...rowErrors.value }
    delete next[id]
    rowErrors.value = next
  }
}

async function confirm() {
  const p = plan.value
  const cid = props.categoryId
  if (!p || !cid || busy.value || !p.items.length) return
  const changed: Record<string, string> = {}
  const errs: Record<string, string> = {}
  for (const it of p.items) {
    const t = (titles[it.folder_id] ?? '').trim()
    const why = checkTitle(t)
    if (why) errs[it.folder_id] = why
    if (t !== it.title) changed[it.folder_id] = t
  }
  if (Object.keys(errs).length) {
    rowErrors.value = errs
    return
  }
  busy.value = 'archive'
  note.value = ''
  try {
    const res = await archiveFolders(
      cid,
      { folder_ids: props.folderIds, ...(Object.keys(changed).length ? { titles: changed } : {}) },
      HANDLE_ALL,
    )
    open.value = false
    emit('archived', res)
  } catch (e) {
    busy.value = ''
    const fields = fieldIssuesOf(e)
    if (fields?.length) {
      // message 是"「文件夹名」：…"，按文件夹名标到行上；标不上的放在下面。
      const errs2: Record<string, string> = {}
      const rest: string[] = []
      for (const f of fields) {
        const row = p.items.find((it) => f.message.startsWith(`「${it.folder_name}」`))
        if (row) errs2[row.folder_id] = f.message.slice(row.folder_name.length + 3)
        else rest.push(f.message)
      }
      rowErrors.value = errs2
      if (rest.length) note.value = rest.join('；')
      return
    }
    note.value = e instanceof ApiError ? e.message : errorMessage(e)
    if (isApiError(e, 'not_found')) {
      emit('stale')
      return
    }
    // 这期间文件夹变了（别人刚归档、刚往里放了已归档的文件……）：重新取预览。
    if (e instanceof ApiError && e.status === 409) {
      const keep = note.value
      await load()
      note.value = keep
    }
  } finally {
    busy.value = ''
  }
}

const totalPages = computed(() => plan.value?.items.reduce((n, it) => n + it.page_count, 0) ?? 0)
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent
      class="sm:max-w-2xl"
      data-testid="archive-dialog"
      :data-state="state"
      :data-items="plan?.items.length"
      :data-skipped="plan?.skipped.length"
    >
      <DialogHeader>
        <DialogTitle>
          归档到「{{ props.categoryLabel }}」<template v-if="plan?.items.length">，新建 {{ plan.items.length }} 件</template>
        </DialogTitle>
        <DialogDescription>
          每个子文件夹一件，文件和顺序就是页；题名默认是文件夹名，可以改。归档后这些文件夹会<b>封存</b>，要改需要先解除封存。
        </DialogDescription>
      </DialogHeader>

      <div v-if="state === 'loading'" class="flex items-center gap-2 text-sm text-muted-foreground">
        <LoaderCircleIcon class="size-4 animate-spin" />正在生成预览…
      </div>
      <p v-else-if="state === 'error'" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive">{{ loadError }}</p>
      <div v-else-if="plan" class="max-h-[55vh] space-y-3 overflow-y-auto">
        <table v-if="plan.items.length" class="w-full border-collapse text-compact">
          <thead>
            <tr class="bg-muted/60 text-left text-xs text-muted-foreground">
              <th class="w-16 px-2 py-1.5 font-medium">件号</th>
              <th class="px-2 py-1.5 font-medium">题名（默认文件夹名，可改）</th>
              <th class="w-14 px-2 py-1.5 text-right font-medium">页数</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="it in plan.items" :key="it.folder_id" class="border-b align-top" data-testid="archive-row" :data-folder-id="it.folder_id">
              <td class="px-2 py-1.5 font-mono text-xs" :title="it.archive_no ?? '档号为空（缺级），可以归档，不能锁定'">
                {{ formatItemNo(it.item_no, props.itemNoWidth) }}
              </td>
              <td class="px-2 py-1">
                <Input
                  v-model="titles[it.folder_id]"
                  class="h-7 px-1.5 text-compact md:text-compact"
                  :aria-invalid="!!rowErrors[it.folder_id] || undefined"
                  data-testid="archive-title"
                  @input="onTitleInput(it.folder_id)"
                />
                <div class="mt-0.5 truncate text-2xs text-muted-foreground" :title="it.folder_path">{{ it.folder_path }}</div>
                <div v-if="rowErrors[it.folder_id]" class="text-2xs text-destructive">{{ rowErrors[it.folder_id] }}</div>
              </td>
              <td class="px-2 py-1.5 text-right tabular-nums">{{ it.page_count }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="rounded-md bg-warn-soft px-3 py-2 text-sm text-warn" data-testid="archive-empty">选中的文件夹里没有能归档成一件的。</p>
        <div v-if="plan.loose_files?.length" class="rounded-md bg-warn-soft px-3 py-2 text-xs text-warn" data-testid="archive-loose-files">
          <p v-for="l in plan.loose_files" :key="l.folder_id" :title="l.folder_path">
            「{{ l.folder_name }}」里直接放的 {{ l.file_count }} 个文件不会归档（只归档它的子文件夹）。
          </p>
        </div>
        <div v-if="plan.skipped.length" class="rounded-md bg-muted px-3 py-2 text-xs" data-testid="archive-skipped-list">
          <div class="mb-1 font-medium text-foreground">跳过 {{ plan.skipped.length }} 个：</div>
          <ul class="space-y-0.5 text-muted-foreground">
            <li v-for="s in plan.skipped" :key="s.folder_id" class="truncate" data-testid="archive-skipped" :data-reason="s.reason" :title="s.folder_path">
              <span class="text-foreground">{{ s.folder_name }}</span>：{{ ARCHIVE_SKIP_LABEL[s.reason] ?? s.reason }} · {{ s.message }}
            </li>
          </ul>
        </div>
      </div>
      <p v-if="note" class="rounded-md bg-warn-soft px-3 py-2 text-xs text-warn" role="alert" data-testid="archive-note">{{ note }}</p>
      <DialogFooter class="items-center gap-2">
        <span v-if="plan?.items.length" class="mr-auto text-xs text-muted-foreground">共 {{ totalPages }} 页</span>
        <Button variant="outline" :disabled="busy === 'archive'" @click="open = false">取消</Button>
        <Button :disabled="state !== 'ready' || busy !== ''" data-testid="archive-confirm" @click="confirm">
          <LoaderCircleIcon v-if="busy === 'archive'" class="animate-spin" />归档并封存
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
