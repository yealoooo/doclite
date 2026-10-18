<script setup lang="ts">
// 打开一个已归档的文件夹时顶部的横条（M9，requirements 5.9"界面 /catalog"、5.5"封存"，Mockup v3.1）：/catalog 左栏
// 和 /files 共用。
// - **已封存**（紫色）："已归档为 办公室 0003（未锁定），已封存"，写明不能做什么、可以重扫；有 catalog 菜单时给
//   "解除封存去修改…"（`unseal` 事件，页面打开 UnsealDialog），没有时写"要修改请联系归类员解除封存"。
// - **修改中**（橙色）："修改中 · 将重新归档到 办公室 0003"，谁、何时解除、原因，三步进度（① 已解除封存 ② 在文件目录里
//   修改 ③ 重新归档）。按钮按 `where` 不同：
//   - `catalog`（归类页左栏）：有 files 菜单时"去文件目录修改"（`open-files`），"改完了，重新归档…"（`reseal`）；
//   - `files`（文件目录页）：有 catalog 菜单时"改完了，去归类页重新归档"（`open-catalog`），没有时写"改完后请告诉归类员
//     重新归档"。
// 截图脚本用的属性：data-testid=seal-banner（data-state：sealed、modifying；data-item-id）；按钮 seal-banner-unseal、
// seal-banner-reseal、seal-banner-open-files、seal-banner-open-catalog。
import { ExternalLinkIcon, LockIcon, PencilLineIcon } from '@lucide/vue'
import { computed } from 'vue'

import type { FolderArchive } from '@/api/types'
import { Button } from '@/components/ui/button'
import { itemNoWidthFrom, itemShortLabel } from '@/lib/catalog'
import { formatDateTime } from '@/lib/format'

const props = defineProps<{
  archive: FolderArchive
  where: 'catalog' | 'files'
  /** 有 catalog 菜单（能解除封存、重新归档）。 */
  canCatalog: boolean
  /** 有 files 菜单（能去文件目录修改）。 */
  canFiles?: boolean
  itemNoWidth?: number | null
}>()

const emit = defineEmits<{
  unseal: []
  reseal: []
  openFiles: []
  openCatalog: []
}>()

const label = computed(() => itemShortLabel(props.archive, props.itemNoWidth ?? itemNoWidthFrom(props.archive.archive_no, props.archive.item_no)))
const where = computed(() => [...props.archive.category_names, props.archive.archive_no ?? `第 ${props.archive.item_no} 件`].join(' / '))
const modifying = computed(() => props.archive.source_status === 'modifying')
</script>

<template>
  <div
    v-if="modifying"
    class="grid shrink-0 grid-cols-[auto_minmax(0,1fr)] gap-x-2.5 gap-y-1.5 border-b border-orange-300 bg-orange-50 px-3 py-2 text-xs leading-relaxed text-orange-950"
    data-testid="seal-banner"
    data-state="modifying"
    :data-item-id="props.archive.item_id"
  >
    <PencilLineIcon class="mt-0.5 size-4 shrink-0 text-orange-700" />
    <div class="min-w-0">
      <div class="text-[13px] font-semibold text-orange-800" :title="where">修改中 · 将重新归档到 {{ label }}</div>
      <div class="text-orange-900/80">
        {{ props.archive.unsealed_by_name ?? '有人' }}
        {{ props.archive.unsealed_at ? formatDateTime(props.archive.unsealed_at) : '' }} 解除封存{{ props.archive.unseal_reason ? `，原因：${props.archive.unseal_reason}` : '' }}。
        修改期间这一件不能锁定、不能导出。
      </div>
      <div class="mt-1 flex flex-wrap gap-1 text-[11px]" data-testid="seal-banner-steps">
        <span class="rounded bg-white/70 px-1.5">① 已解除封存</span>
        <span class="rounded bg-orange-600 px-1.5 text-white">② {{ props.where === 'files' ? '在这里修改' : '在文件目录里修改' }}</span>
        <span class="rounded bg-white/70 px-1.5">③ 重新归档</span>
      </div>
    </div>
    <div class="col-start-2 flex flex-wrap items-center gap-1.5">
      <template v-if="props.where === 'catalog'">
        <Button v-if="props.canFiles" size="xs" variant="outline" class="bg-white" data-testid="seal-banner-open-files" @click="emit('openFiles')">
          <ExternalLinkIcon />去文件目录修改
        </Button>
        <Button v-if="props.canCatalog" size="xs" class="bg-orange-600 text-white hover:bg-orange-700" data-testid="seal-banner-reseal" @click="emit('reseal')">
          改完了，重新归档…
        </Button>
      </template>
      <template v-else>
        <Button v-if="props.canCatalog" size="xs" class="bg-orange-600 text-white hover:bg-orange-700" data-testid="seal-banner-open-catalog" @click="emit('openCatalog')">
          改完了，去归类页重新归档
        </Button>
        <span v-else class="text-orange-900/80">改完后请告诉归类员重新归档</span>
      </template>
    </div>
  </div>
  <div
    v-else
    class="grid shrink-0 grid-cols-[auto_minmax(0,1fr)] gap-x-2.5 gap-y-1.5 border-b bg-violet-50 px-3 py-2 text-xs leading-relaxed text-violet-950"
    data-testid="seal-banner"
    data-state="sealed"
    :data-item-id="props.archive.item_id"
  >
    <LockIcon class="mt-0.5 size-4 shrink-0 text-violet-700" />
    <div class="min-w-0">
      <div class="text-[13px] font-semibold text-violet-900" :title="where">
        已归档为 {{ label }}（{{ props.archive.locked ? '已锁定' : '未锁定' }}），已封存
      </div>
      <div class="text-violet-900/80">
        不能加、删、移动文件，不能调顺序，文件夹不能改名、移动、删除；可以重扫、改备注。
        <template v-if="!props.canCatalog">要修改请联系归类员解除封存。</template>
      </div>
    </div>
    <div v-if="props.canCatalog" class="col-start-2 flex flex-wrap items-center gap-1.5">
      <Button v-if="props.where === 'files' && props.canCatalog" size="xs" variant="ghost" class="text-violet-800 hover:bg-violet-100" data-testid="seal-banner-open-catalog" @click="emit('openCatalog')">
        <ExternalLinkIcon />在归类页查看
      </Button>
      <Button v-if="props.canCatalog" size="xs" variant="outline" class="bg-white" data-testid="seal-banner-unseal" @click="emit('unseal')">
        解除封存去修改…
      </Button>
    </div>
  </div>
</template>
