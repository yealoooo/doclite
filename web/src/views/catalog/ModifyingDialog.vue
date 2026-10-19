<script setup lang="ts">
// "修改中 n 件"的清单（M9，requirements 5.9"不让人忘"，Mockup v3.1 的 askModList）：点归类页顶部的青色提醒打开。
// 每行：件（节点名 件号、题名）、来源文件夹、谁 / 何时 / 原因；"去文件目录修改"（/files 打开那个文件夹）、"打开"（右边
// 打开这一件）、"重新归档…"（页面打开 ResealDialog）。
// 截图脚本用的属性：data-testid=modifying-dialog（data-count）；每行 modifying-row（data-item-id）；按钮 modifying-reseal。
import { ExternalLinkIcon } from '@lucide/vue'

import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { formatItemNo } from '@/lib/catalog'
import { formatDateTime } from '@/lib/format'

import type { ModifyingItem } from './modifying'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  items: ModifyingItem[]
  /** 真实件数（清单最多 500 件）。 */
  total: number
  itemNoWidth: number
  canFiles: boolean
}>()

const emit = defineEmits<{
  openFiles: [folderId: string]
  openItem: [item: ModifyingItem]
  reseal: [item: ModifyingItem]
}>()

function label(it: ModifyingItem) {
  const node = it.category_names[it.category_names.length - 1]
  const no = formatItemNo(it.item_no, props.itemNoWidth)
  return node ? `${node} ${no}` : `第 ${no} 件`
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-3xl" data-testid="modifying-dialog" :data-count="props.total">
      <DialogHeader>
        <DialogTitle>修改中 {{ props.total }} 件</DialogTitle>
        <DialogDescription>
          这些件的来源文件夹已解除封存，改完要重新归档，否则不能锁定、导出。<template v-if="props.total > props.items.length">这里列出最近解除的 {{ props.items.length }} 件。</template>
        </DialogDescription>
      </DialogHeader>
      <div class="max-h-[55vh] overflow-y-auto">
        <table class="w-full border-collapse text-compact">
          <thead>
            <tr class="bg-muted/60 text-left text-xs text-muted-foreground">
              <th class="px-2 py-1.5 font-medium">件</th>
              <th class="px-2 py-1.5 font-medium">文件夹</th>
              <th class="px-2 py-1.5 font-medium">谁 / 原因</th>
              <th class="px-2 py-1.5" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="it in props.items" :key="it.item_id" class="border-b align-top" data-testid="modifying-row" :data-item-id="it.item_id">
              <td class="px-2 py-1.5">
                <button type="button" class="text-left hover:underline" :title="[...it.category_names, it.archive_no ?? ''].join(' / ')" @click="emit('openItem', it)">
                  <span class="font-mono text-xs font-medium">{{ label(it) }}</span>
                  <span class="block max-w-52 truncate text-xs text-muted-foreground">{{ it.title || '（未填题名）' }}</span>
                </button>
              </td>
              <td class="max-w-52 px-2 py-1.5">
                <span class="block truncate" :title="it.source_folder_path">{{ it.source_folder_name }}</span>
              </td>
              <td class="px-2 py-1.5 text-xs">
                {{ it.unsealed_by_name ?? '—' }} {{ formatDateTime(it.unsealed_at) }}
                <span class="block text-muted-foreground">{{ it.unseal_reason || '没填原因' }}</span>
              </td>
              <td class="px-2 py-1.5">
                <div class="flex flex-wrap justify-end gap-1">
                  <Button v-if="props.canFiles" size="xs" variant="outline" @click="emit('openFiles', it.source_folder_id)">
                    <ExternalLinkIcon />去修改
                  </Button>
                  <Button size="xs" variant="modify" data-testid="modifying-reseal" @click="emit('reseal', it)">
                    重新归档…
                  </Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-if="!props.items.length" class="py-6 text-center text-sm text-muted-foreground">现在没有修改中的件。</p>
      </div>
      <DialogFooter>
        <Button variant="outline" @click="open = false">关闭</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
