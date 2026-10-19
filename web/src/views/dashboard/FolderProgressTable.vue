<script setup lang="ts">
// 按文件夹的表（M3-7，用户决定 Q6"按文件夹逐层展开"；2026-10-19 简化，Mockup v2）：每行一个文件夹整棵子树——页数、
// 审核通过（单色条 + x/y 页）、已归档、已锁定。行由页面按 catalog-summary 的 children 拼好传进来；点箭头展开下一层
// （页面去取那个文件夹的 catalog-summary）。有子文件夹、又直接放了文件的文件夹，展开后多一行"直接放在这里的文件"，
// 各行加起来才等于上一层。
// - 已归档、已锁定：文件夹自己归档成了一件（children 的 archive）→ 档号（为空时"第 n 件"，修改中加标记）、"已锁定"或"—"；
//   有下级文件夹的（盒子等）→ 下面的合计"n 件 · m 页"、"a 件"；别的（没归档的文件夹、直接放的文件）有件时同盒子，没有时"—"。
// 截图脚本用的属性：data-testid=folder-progress（data-rows）；folder-row（data-folder-id、data-kind：folder / direct、
// data-depth、data-expanded、data-total、data-approved、data-items、data-cataloged、data-locked-items，自己归档了的另有
// data-archive-no（档号或"第 n 件"）、data-archive-locked、data-archive-modifying）；folder-toggle。
import { ChevronRightIcon, FileIcon, FolderIcon, FolderOpenIcon, LoaderCircleIcon, LockIcon, PencilIcon } from '@lucide/vue'

import type { CatalogCounts, FolderArchive } from '@/api/types'
import { placementNo } from '@/lib/catalog'

import { formatCount } from '@/lib/format'

import { barWidth } from './dashboard'

export interface ProgressRow {
  /** 行的唯一键：文件夹 id，或 `direct:<文件夹 id>`。 */
  key: string
  kind: 'folder' | 'direct'
  folderId: string
  name: string
  depth: number
  counts: CatalogCounts
  /** 文件夹自己的归档信息（没归档、直接放的文件为 null）。 */
  archive: FolderArchive | null
  /** 有下级文件夹（显示箭头，已归档、已锁定写合计）。 */
  hasChildren: boolean
  expanded: boolean
  /** 展开了、还在取下一层。 */
  loading: boolean
}

const props = defineProps<{ rows: ProgressRow[] }>()

const emit = defineEmits<{ toggle: [folderId: string] }>()

/** 已归档、已锁定两列写合计（"n 件 · m 页"、"a 件"）。 */
function showTotals(r: ProgressRow): boolean {
  return !r.archive && (r.hasChildren || r.counts.items.total > 0)
}

function archiveTitle(a: FolderArchive): string {
  const where = a.category_names.length ? `${a.category_names.join(' › ')}\n` : ''
  const mod = a.source_status === 'modifying' ? '\n修改中：解除了封存，改完要重新归档' : ''
  return `${where}${placementNo(a)} ${a.title}${mod}`
}

const COLS = 'grid-cols-[minmax(220px,2.2fr)_64px_minmax(150px,1.4fr)_minmax(120px,1fr)_96px]'
</script>

<template>
  <div class="min-w-0" data-testid="folder-progress" :data-rows="props.rows.length">
    <div class="grid items-center gap-3.5 border-b bg-muted/50 px-3.5 py-2 text-xs font-medium text-muted-foreground" :class="COLS">
      <span>文件夹</span>
      <span class="text-right">页数</span>
      <span>审核通过</span>
      <span>已归档</span>
      <span>已锁定</span>
    </div>
    <div
      v-for="r in props.rows"
      :key="r.key"
      class="grid items-center gap-3.5 border-b px-3.5 py-2 text-[13px] last:border-b-0 hover:bg-muted/30"
      :class="COLS"
      data-testid="folder-row"
      :data-folder-id="r.folderId"
      :data-kind="r.kind"
      :data-depth="r.depth"
      :data-expanded="r.kind === 'folder' ? String(r.expanded) : undefined"
      :data-total="r.counts.total"
      :data-approved="r.counts.review.approved"
      :data-items="r.counts.items.total"
      :data-cataloged="r.counts.catalog.cataloged"
      :data-locked-items="r.counts.items.locked"
      :data-archive-no="r.archive ? placementNo(r.archive) : undefined"
      :data-archive-locked="r.archive ? String(r.archive.locked) : undefined"
      :data-archive-modifying="r.archive ? String(r.archive.source_status === 'modifying') : undefined"
    >
      <div class="flex min-w-0 items-center gap-1" :style="{ paddingLeft: `${r.depth * 18}px` }">
        <template v-if="r.kind === 'folder'">
          <button
            v-if="r.hasChildren"
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
            :is="r.expanded ? FolderOpenIcon : FolderIcon"
            class="size-4 shrink-0 fill-amber-300/60 text-amber-600"
            :stroke-width="1.5"
          />
          <span class="min-w-0 truncate text-foreground" :class="r.hasChildren ? 'font-semibold' : ''" :title="r.name">{{ r.name }}</span>
        </template>
        <template v-else>
          <span class="size-5 shrink-0" />
          <FileIcon class="size-4 shrink-0 text-muted-foreground" :stroke-width="1.5" />
          <span class="min-w-0 truncate text-muted-foreground">直接放在这里的文件</span>
        </template>
      </div>
      <span class="text-right tabular-nums text-foreground">{{ formatCount(r.counts.total) }}</span>
      <div class="flex min-w-0 items-center gap-2">
        <div class="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
          <div class="h-full rounded-full bg-ok" :style="{ width: barWidth(r.counts.review.approved, r.counts.total) }" />
        </div>
        <span class="shrink-0 text-xs text-muted-foreground tabular-nums">{{ formatCount(r.counts.review.approved) }}/{{ formatCount(r.counts.total) }}</span>
      </div>
      <!-- 已归档 -->
      <span v-if="r.archive" class="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground" :title="archiveTitle(r.archive)">
        <span class="truncate font-mono text-[11.5px] text-foreground">{{ placementNo(r.archive) }}</span>
        <span
          v-if="r.archive.source_status === 'modifying'"
          class="inline-flex shrink-0 items-center gap-0.5 rounded-full bg-warn-soft px-1.5 text-[11px] text-warn"
          data-testid="archive-modifying"
        ><PencilIcon class="size-3" />修改中</span>
      </span>
      <span v-else-if="showTotals(r)" class="truncate text-xs text-muted-foreground tabular-nums">
        {{ formatCount(r.counts.items.total) }} 件 · {{ formatCount(r.counts.catalog.cataloged) }} 页
      </span>
      <span v-else class="text-xs text-muted-foreground/50">—</span>
      <!-- 已锁定 -->
      <span v-if="r.archive?.locked" class="flex items-center gap-1 text-xs text-muted-foreground">
        <LockIcon class="size-3.5 text-violet-900" />已锁定
      </span>
      <span v-else-if="showTotals(r)" class="truncate text-xs text-muted-foreground tabular-nums">{{ formatCount(r.counts.items.locked) }} 件</span>
      <span v-else class="text-xs text-muted-foreground/50">—</span>
    </div>
    <div v-if="props.rows.length === 0" class="px-3 py-8 text-center text-sm text-muted-foreground">项目里还没有文件夹</div>
  </div>
</template>
