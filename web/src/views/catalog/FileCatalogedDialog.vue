<script setup lang="ts">
// 要挂的文件已经在别的条目里（409 `file_cataloged`，M3-3；用户决定 Q3：一个文件只能在一个条目里）。
//
// - 按原条目分组列出每个文件"在 某节点 / 档号 第 p 页"（`details.items`，字段同 placements）；
// - **从原条目移出**：GET 原条目 → PUT 去掉这些文件（带原条目的 row_version；被别人刚改过 409 时重新读一次再试）。
//   原条目已定稿（`item_status = locked`）时按钮置灰（server 也会 409 `item_locked`）。全部移出后自动重试原来的操作
//   （`retry` 事件，页面在最新的页序上重做）；
// - **跳过这些**：去掉还挂在别的条目里的（已经移出的不算），挂接其余的（`skip` 事件带要跳过的 file_id；其余的为 0 时
//   不显示）；
// - 取消：什么都不做。移出进行中不能关（取消、Esc、点遮罩、叉都不响应）；关掉之后即使移出完成也不再重试。
// 截图脚本用的属性：根 data-testid=file-cataloged-dialog（data-working）；每组 data-testid=cataloged-group（data-item-id、
// data-locked、data-state）；按钮 cataloged-move-out、cataloged-skip、cataloged-cancel。
import { LoaderCircleIcon, LockIcon } from '@lucide/vue'
import { computed, reactive, watch } from 'vue'

import { getArchiveItem, putArchiveItemFiles } from '@/api/catalog'
import { errorMessage, isApiError } from '@/api/http'
import type { ArchiveItem, CatalogedElsewhere } from '@/api/types'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { formatItemNo } from '@/lib/catalog'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  items: CatalogedElsewhere[]
  /** file_id → 文件名。 */
  names: Record<string, string>
  /** 原来的操作一共要挂几个文件（"跳过这些，挂接其余的 n 个"）。 */
  total: number
  /** 原来的操作（标题里说明）。 */
  action: string
  itemNoWidth: number
}>()

const emit = defineEmits<{ retry: []; skip: [fileIds: string[]] }>()

interface Group {
  item_id: string
  item_no: number
  archive_no: string | null
  title: string
  category_names: string[]
  locked: boolean
  files: { file_id: string; page_no: number }[]
}

const groups = computed<Group[]>(() => {
  const map = new Map<string, Group>()
  for (const it of props.items) {
    let g = map.get(it.item_id)
    if (!g) {
      g = {
        item_id: it.item_id,
        item_no: it.item_no,
        archive_no: it.archive_no,
        title: it.title,
        category_names: it.category_names,
        locked: it.item_status === 'locked',
        files: [],
      }
      map.set(it.item_id, g)
    }
    g.files.push({ file_id: it.file_id, page_no: it.page_no })
  }
  for (const g of map.values()) g.files.sort((a, b) => a.page_no - b.page_no)
  return [...map.values()]
})

/** 每组的状态：移出中、已移出、出错（文字）。 */
const status = reactive<Record<string, { state: 'idle' | 'working' | 'done' | 'error'; message?: string }>>({})
watch(
  () => props.items,
  () => {
    for (const k of Object.keys(status)) delete status[k]
  },
)

const allDone = computed(() => groups.value.length > 0 && groups.value.every((g) => status[g.item_id]?.state === 'done'))
const working = computed(() => groups.value.some((g) => status[g.item_id]?.state === 'working'))
/** 还挂在别的条目里的（没移出的组）。 */
const blocked = computed(() =>
  groups.value.filter((g) => status[g.item_id]?.state !== 'done').flatMap((g) => g.files.map((f) => f.file_id)),
)
const remaining = computed(() => Math.max(0, props.total - blocked.value.length))

function noText(g: Pick<Group, 'archive_no' | 'item_no'>): string {
  return g.archive_no ?? `第 ${formatItemNo(g.item_no, props.itemNoWidth)} 件`
}

async function moveOut(g: Group) {
  if (g.locked || status[g.item_id]?.state === 'working') return
  status[g.item_id] = { state: 'working' }
  const drop = new Set(g.files.map((f) => f.file_id))
  try {
    for (let attempt = 0; ; attempt++) {
      let it: ArchiveItem
      try {
        it = await getArchiveItem(g.item_id, true)
      } catch (e) {
        // 原条目已经被删了：它的页也一起移出了。
        if (isApiError(e, 'not_found')) break
        throw e
      }
      if (it.status === 'locked') {
        status[g.item_id] = { state: 'error', message: '原条目已定稿，不能移出（要先由项目负责人解锁）' }
        return
      }
      const list = it.pages.map((p) => p.file_id).filter((id) => !drop.has(id))
      if (list.length === it.pages.length) break
      try {
        await putArchiveItemFiles(it.id, it.row_version, list, true)
        break
      } catch (e) {
        // 原条目刚被别人改过：重新读一次再试（最多 3 次）。
        if (isApiError(e, 'conflict') && attempt < 2) continue
        if (isApiError(e, 'not_found') && !(e.details as { ids?: unknown } | undefined)?.ids) break
        throw e
      }
    }
    status[g.item_id] = { state: 'done' }
  } catch (e) {
    status[g.item_id] = {
      state: 'error',
      message: isApiError(e, 'item_locked') ? '原条目已定稿，不能移出' : `移出失败：${errorMessage(e)}`,
    }
  }
  // 对话框已经关了（取消）就不再重试（M3-6 审查：移出进行中关掉对话框，原来仍会自动挂接）。
  if (allDone.value && open.value) emit('retry')
}

/** 移出进行中不让关（Esc、点遮罩、右上角的叉都不行），免得用户以为取消了、后台却接着重试。 */
function setOpen(v: boolean) {
  if (!v && working.value) return
  open.value = v
}
</script>

<template>
  <Dialog :open="open" @update:open="setOpen">
    <DialogContent
      class="sm:max-w-xl"
      data-testid="file-cataloged-dialog"
      :data-working="working ? 'true' : undefined"
      :show-close-button="!working"
      @escape-key-down="(e: KeyboardEvent) => working && e.preventDefault()"
      @interact-outside="(e: Event) => working && e.preventDefault()"
    >
      <DialogHeader>
        <DialogTitle>有 {{ props.items.length }} 个文件已经挂在别的条目里</DialogTitle>
        <DialogDescription>
          一个文件只能在一个条目里。{{ props.action }}没有做。可以先从原条目移出（移出后自动重试），或者跳过这些文件。
        </DialogDescription>
      </DialogHeader>
      <div class="max-h-[50vh] space-y-2 overflow-y-auto">
        <div
          v-for="g in groups"
          :key="g.item_id"
          class="rounded-md border px-3 py-2 text-sm"
          data-testid="cataloged-group"
          :data-item-id="g.item_id"
          :data-locked="g.locked ? 'true' : 'false'"
          :data-state="status[g.item_id]?.state ?? 'idle'"
        >
          <div class="flex items-start gap-2">
            <div class="min-w-0 flex-1">
              <div class="truncate text-xs text-muted-foreground" :title="g.category_names.join(' / ')">
                {{ g.category_names.join(' / ') || '（顶层）' }}
              </div>
              <div class="flex min-w-0 items-center gap-1.5 font-medium text-foreground">
                <span class="shrink-0 font-mono">{{ noText(g) }}</span>
                <span v-if="g.locked" class="inline-flex shrink-0 items-center gap-0.5 rounded-full bg-ok-soft px-1.5 text-[11px] leading-5 font-normal text-ok">
                  <LockIcon class="size-3" />已定稿
                </span>
                <span class="min-w-0 truncate font-normal text-muted-foreground">{{ g.title || '（还没填题名）' }}</span>
              </div>
            </div>
            <span v-if="status[g.item_id]?.state === 'done'" class="shrink-0 text-xs text-ok">已移出</span>
            <Button
              v-else
              size="xs"
              variant="outline"
              class="shrink-0"
              :disabled="g.locked || status[g.item_id]?.state === 'working'"
              :title="g.locked ? '原条目已定稿，不能移出（要先由项目负责人解锁）' : '从原条目里移出这些文件，然后重试'"
              data-testid="cataloged-move-out"
              @click="moveOut(g)"
            >
              <LoaderCircleIcon v-if="status[g.item_id]?.state === 'working'" class="animate-spin" />从原条目移出
            </Button>
          </div>
          <ul class="mt-1 space-y-0.5 text-xs text-muted-foreground">
            <li v-for="f in g.files" :key="f.file_id" class="truncate" data-testid="cataloged-file" :data-file-id="f.file_id">
              {{ props.names[f.file_id] ?? f.file_id }} — 在 {{ noText(g) }} 第 {{ f.page_no }} 页
            </li>
          </ul>
          <p v-if="status[g.item_id]?.state === 'error'" class="mt-1 text-xs text-destructive">{{ status[g.item_id]?.message }}</p>
        </div>
      </div>
      <DialogFooter class="gap-2">
        <Button variant="outline" :disabled="working" data-testid="cataloged-cancel" @click="setOpen(false)">取消</Button>
        <Button
          v-if="remaining > 0"
          variant="outline"
          :disabled="working"
          data-testid="cataloged-skip"
          @click="emit('skip', blocked)"
        >跳过{{ blocked.length < props.items.length ? '还在别的条目里的' : '这些' }}，挂接其余的 {{ remaining }} 个</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
