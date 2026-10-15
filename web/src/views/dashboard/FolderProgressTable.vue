<script setup lang="ts">
// 文件夹进度表（M3-7，用户决定 Q6"加按文件夹逐层展开"）：每行一个文件夹整棵子树——文件数、审核进度（通过、待审、
// 驳回、待分配）、待重扫、归类进度（已定稿、已归类未定稿）、可导出（通过且可导出 / 全部可导出）。行由页面按
// catalog-summary 的 children 拼好传进来；点箭头展开下一层（页面去取那个文件夹的 catalog-summary）。
// 有子文件夹、又直接放了文件的文件夹，展开后多一行"直接放在这里的文件"，各行加起来才等于上一层。
// 截图脚本用的属性：data-testid=folder-progress（data-rows）；folder-row（data-folder-id、data-kind：folder / direct、
// data-depth、data-expanded、data-total、data-approved、data-rejected、data-cataloged、data-locked、data-exportable）；
// folder-toggle。
import { ChevronRightIcon, FolderIcon, FolderOpenIcon, LoaderCircleIcon } from '@lucide/vue'

import type { CatalogCounts } from '@/api/types'

import { catalogSegments, num, percentText, reviewSegments } from './dashboard'
import StackedBar from './StackedBar.vue'

export interface ProgressRow {
  /** 行的唯一键：文件夹 id，或 `direct:<文件夹 id>`。 */
  key: string
  kind: 'folder' | 'direct'
  folderId: string
  name: string
  depth: number
  counts: CatalogCounts
  expanded: boolean
  /** 展开了、还在取下一层。 */
  loading: boolean
  /** 已经知道没有子文件夹（不显示箭头）。 */
  leaf: boolean
}

const props = defineProps<{
  rows: ProgressRow[]
  requireLocked: boolean
}>()

const emit = defineEmits<{ toggle: [folderId: string] }>()
</script>

<template>
  <div class="min-w-0" data-testid="folder-progress" :data-rows="props.rows.length">
    <div
      class="grid grid-cols-[minmax(180px,1.6fr)_64px_minmax(130px,1.2fr)_56px_minmax(130px,1.2fr)_96px] items-center gap-3 border-b bg-muted/50 px-3 py-2 text-[11px] font-medium text-muted-foreground"
    >
      <span>文件夹（含子文件夹）</span>
      <span class="text-right">文件</span>
      <span>审核进度（通过）</span>
      <span class="text-right">待重扫</span>
      <span>归类进度（已归类）</span>
      <span class="text-right" :title="props.requireLocked ? '通过且已定稿 / 全部已定稿' : '通过且已归类 / 全部已归类'">可导出</span>
    </div>
    <div
      v-for="r in props.rows"
      :key="r.key"
      class="grid grid-cols-[minmax(180px,1.6fr)_64px_minmax(130px,1.2fr)_56px_minmax(130px,1.2fr)_96px] items-center gap-3 border-b px-3 py-2 text-[12.5px] last:border-b-0 hover:bg-muted/30"
      data-testid="folder-row"
      :data-folder-id="r.folderId"
      :data-kind="r.kind"
      :data-depth="r.depth"
      :data-expanded="r.kind === 'folder' ? String(r.expanded) : undefined"
      :data-total="r.counts.total"
      :data-approved="r.counts.review.approved"
      :data-rejected="r.counts.review.rejected"
      :data-cataloged="r.counts.catalog.cataloged"
      :data-locked="r.counts.catalog.locked"
      :data-exportable="r.counts.exportable.approved"
    >
      <div class="flex min-w-0 items-center gap-1" :style="{ paddingLeft: `${r.depth * 18}px` }">
        <template v-if="r.kind === 'folder'">
          <button
            v-if="!r.leaf"
            type="button"
            class="flex size-5 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground"
            :aria-label="r.expanded ? '收起' : '展开'"
            :aria-expanded="r.expanded"
            data-testid="folder-toggle"
            @click="emit('toggle', r.folderId)"
          >
            <LoaderCircleIcon v-if="r.loading" class="size-3.5 animate-spin" />
            <ChevronRightIcon v-else class="size-3.5 transition-transform" :class="r.expanded ? 'rotate-90' : ''" />
          </button>
          <span v-else class="size-5 shrink-0" />
          <component
            :is="r.expanded && !r.leaf ? FolderOpenIcon : FolderIcon"
            class="size-4 shrink-0 fill-amber-300/60 text-amber-600"
            :stroke-width="1.5"
          />
          <span class="min-w-0 truncate font-medium text-foreground" :title="r.name">{{ r.name }}</span>
        </template>
        <template v-else>
          <span class="size-5 shrink-0" />
          <span class="min-w-0 truncate text-muted-foreground">直接放在这里的文件</span>
        </template>
      </div>
      <span class="text-right tabular-nums text-foreground">{{ num(r.counts.total) }}</span>
      <div class="flex min-w-0 items-center gap-2">
        <StackedBar class="flex-1" :segments="reviewSegments(r.counts)" :total="r.counts.total" />
        <span class="w-9 shrink-0 text-right text-[11.5px] tabular-nums text-muted-foreground">
          {{ percentText(r.counts.review.approved, r.counts.total) }}
        </span>
      </div>
      <span
        class="text-right tabular-nums"
        :class="r.counts.review.rejected ? 'font-medium text-foreground' : 'text-muted-foreground/50'"
      >
        <span v-if="r.counts.review.rejected" class="mr-1 inline-block size-1.5 rounded-full bg-destructive align-middle" />{{
          num(r.counts.review.rejected)
        }}
      </span>
      <div class="flex min-w-0 items-center gap-2">
        <StackedBar class="flex-1" :segments="catalogSegments(r.counts)" :total="r.counts.total" />
        <span class="w-9 shrink-0 text-right text-[11.5px] tabular-nums text-muted-foreground">
          {{ percentText(r.counts.catalog.cataloged, r.counts.total) }}
        </span>
      </div>
      <span class="text-right tabular-nums" :title="`通过且可导出 ${num(r.counts.exportable.approved)}，全部可导出 ${num(r.counts.exportable.total)}`">
        <span :class="r.counts.exportable.approved ? 'font-medium text-foreground' : 'text-muted-foreground/50'">
          {{ num(r.counts.exportable.approved) }}
        </span>
        <span class="text-[11px] text-muted-foreground"> / {{ num(r.counts.exportable.total) }}</span>
      </span>
    </div>
    <div v-if="props.rows.length === 0" class="px-3 py-8 text-center text-sm text-muted-foreground">项目里还没有文件夹</div>
  </div>
</template>
