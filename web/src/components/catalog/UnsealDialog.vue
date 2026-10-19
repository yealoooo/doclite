<script setup lang="ts">
// 解除封存（M9，requirements 5.9"解除封存"，Mockup v3.1 的 askUnseal）：/catalog（左栏横条、条目详情）和 /files（横条）
// 共用，只有 catalog 菜单的人能打开。
// - 打开时取一次条目（要 row_version、状态）；写明后果：件号、档号、题名和著录都保留，文件夹可以加页、删页、调顺序，
//   这一件变成"修改中"、不能锁定导出，改完要"重新归档"。原因选填（漏扫了页 / 多扫了页 / 页的顺序不对 / 其他 / 不填）。
// - 已锁定的不能直接解除：写明"先解锁"，给"解锁"按钮（解锁后留在对话框里，可以接着解除封存）。
// - 已经在修改中（别人刚解除了）：写明，只给"关闭"。
// - 409 conflict（别人刚改过这一件）：重新取，提示再点一次。
// 成功后发 `unsealed`（新的详情）。
// 截图脚本用的属性：data-testid=unseal-dialog（data-state：loading、ready、locked、modifying、error）；原因 unseal-reason；
// 按钮 unseal-confirm、unseal-unlock。
import { LoaderCircleIcon, LockOpenIcon } from '@lucide/vue'
import { computed, ref, watch } from 'vue'
import { toast } from 'vue-sonner'

import { getArchiveItem, itemAction, unsealArchiveItem } from '@/api/catalog'
import { ApiError, errorMessage, HANDLE_ALL, isApiError } from '@/api/http'
import type { ArchiveItem } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { UNSEAL_REASONS } from '@/lib/catalog'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  itemId: string | null
  /** 件的简称（"办公室 0003"），标题里用；不给时用档号。 */
  label?: string
}>()

const emit = defineEmits<{ unsealed: [item: ArchiveItem]; changed: [item: ArchiveItem] }>()

const item = ref<ArchiveItem | null>(null)
const loadError = ref('')
const reason = ref('')
const busy = ref<'' | 'load' | 'unseal' | 'unlock'>('')
const note = ref('')

const state = computed(() => {
  if (loadError.value) return 'error'
  if (!item.value) return 'loading'
  if (item.value.status === 'locked') return 'locked'
  if (item.value.source_status === 'modifying') return 'modifying'
  return 'ready'
})
const name = computed(() => props.label || item.value?.archive_no || (item.value ? `第 ${item.value.item_no} 件` : ''))

async function load() {
  const id = props.itemId
  if (!id) return
  busy.value = 'load'
  try {
    const it = await getArchiveItem(id, HANDLE_ALL)
    if (props.itemId === id) item.value = it
  } catch (e) {
    loadError.value = isApiError(e, 'not_found') ? '这一件已经不在了（可能刚被撤销归档）' : errorMessage(e)
  } finally {
    busy.value = ''
  }
}

watch(open, (v) => {
  if (!v) return
  item.value = null
  loadError.value = ''
  reason.value = ''
  note.value = ''
  void load()
})

async function unseal() {
  const it = item.value
  if (!it || busy.value) return
  busy.value = 'unseal'
  note.value = ''
  try {
    const res = await unsealArchiveItem(it.id, it.row_version, reason.value || null, HANDLE_ALL)
    open.value = false
    emit('unsealed', res)
  } catch (e) {
    if (isApiError(e, 'conflict')) {
      note.value = '这一件刚被别人修改过，已重新读取，请再点一次'
      busy.value = ''
      await load()
      return
    }
    if (isApiError(e, 'item_locked') || isApiError(e, 'invalid_state')) {
      busy.value = ''
      await load()
    }
    note.value = e instanceof ApiError ? e.message : errorMessage(e)
  } finally {
    busy.value = ''
  }
}

async function unlock() {
  const it = item.value
  if (!it || busy.value) return
  busy.value = 'unlock'
  note.value = ''
  try {
    const res = await itemAction(it.id, 'unlock', it.row_version, HANDLE_ALL)
    item.value = res
    emit('changed', res)
    toast.success('已解锁，现在可以解除封存')
  } catch (e) {
    note.value = e instanceof ApiError ? e.message : errorMessage(e)
    if (isApiError(e, 'conflict')) {
      busy.value = ''
      await load()
    }
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-lg" data-testid="unseal-dialog" :data-state="state">
      <template v-if="state === 'locked'">
        <DialogHeader>
          <DialogTitle>这一件已锁定，不能直接解除封存</DialogTitle>
          <DialogDescription>
            {{ name }}「{{ item?.title || '（未填题名）' }}」已锁定。先解锁，再解除封存。
          </DialogDescription>
        </DialogHeader>
      </template>
      <template v-else-if="state === 'modifying'">
        <DialogHeader>
          <DialogTitle>{{ name }} 已经在修改中</DialogTitle>
          <DialogDescription>
            {{ item?.unsealed_by_name ?? '有人' }}刚解除了封存{{ item?.unseal_reason ? `（${item.unseal_reason}）` : '' }}，
            可以直接去文件目录修改，改完重新归档。
          </DialogDescription>
        </DialogHeader>
      </template>
      <template v-else>
        <DialogHeader>
          <DialogTitle>解除封存「{{ item?.source_folder_name ?? '…' }}」？</DialogTitle>
          <DialogDescription>
            已归档为 {{ name }}（{{ item?.status === 'locked' ? '已锁定' : '未锁定' }}）
          </DialogDescription>
        </DialogHeader>
        <div v-if="state === 'loading'" class="flex items-center gap-2 text-sm text-muted-foreground">
          <LoaderCircleIcon class="size-4 animate-spin" />正在读取…
        </div>
        <p v-else-if="state === 'error'" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive">{{ loadError }}</p>
        <template v-else>
          <div class="rounded-md bg-muted px-3 py-2 text-xs leading-relaxed text-foreground">
            解除后：<br>
            · 件号、档号、题名和著录<b>都保留</b>；<br>
            · 文件夹可以加页、删页、调顺序（在「文件目录」里改）；<br>
            · 这一件变成<b>"修改中"</b>，不能锁定、不能导出；<br>
            · 改完要点<b>"重新归档"</b>，到时会给你看改了什么。
          </div>
          <label class="grid gap-1 text-xs text-muted-foreground">
            原因（选填，记进日志）
            <select
              v-model="reason"
              class="h-8 rounded-md border border-input bg-background px-2 text-[13px] text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
              data-testid="unseal-reason"
            >
              <option value="">（不填）</option>
              <option v-for="r in UNSEAL_REASONS" :key="r" :value="r">{{ r }}</option>
            </select>
          </label>
        </template>
      </template>
      <p v-if="note" class="rounded-md bg-warn-soft px-3 py-2 text-xs text-warn" role="alert" data-testid="unseal-note">{{ note }}</p>
      <DialogFooter class="gap-2">
        <Button variant="outline" :disabled="busy === 'unseal' || busy === 'unlock'" @click="open = false">
          {{ state === 'modifying' || state === 'error' ? '关闭' : '取消' }}
        </Button>
        <Button v-if="state === 'locked'" variant="outline" :disabled="busy !== ''" data-testid="unseal-unlock" @click="unlock">
          <LoaderCircleIcon v-if="busy === 'unlock'" class="animate-spin" /><LockOpenIcon v-else />解锁
        </Button>
        <Button
          v-if="state === 'ready'"
          class="bg-orange-600 text-white hover:bg-orange-700"
          :disabled="busy !== ''"
          data-testid="unseal-confirm"
          @click="unseal"
        >
          <LoaderCircleIcon v-if="busy === 'unseal'" class="animate-spin" />解除封存
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
