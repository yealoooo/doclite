<script setup lang="ts">
// 状态矩阵（M3-7，用户决定 Q6）：4 行审核状态（待分配、待审、通过、驳回）× 3 列归档状态（未归档、已归档未锁定、
// 已锁定），另有每行合计、"可导出"一列和最后一行合计。"可导出"按项目设置 `export.require_locked_item`（true 只算
// 已锁定的，false 算已归档的），审核状态不影响能不能导出（"只导出审核通过的"在导出时勾选）；算作可导出的格子标绿。
// 每格写一句说明（plan.html 看板 Mockup 的写法），悬停看占比。
// 截图脚本用的属性：data-testid=matrix-cell（data-review、data-catalog（uncataloged、unlocked、locked、total、
// exportable）、data-value）。
import type { CatalogCounts, ReviewStatus } from '@/api/types'
import { REVIEW_STATUS } from '@/lib/files'

import {
  CATALOG_COLUMN_LABEL,
  CATALOG_COLUMNS,
  type CatalogColumn,
  cellNote,
  isExportableColumn,
  num,
  percentText,
  REVIEW_COLOR,
  REVIEW_ROWS,
} from './dashboard'

const props = defineProps<{
  counts: CatalogCounts
  requireLocked: boolean
}>()

function cellTitle(row: ReviewStatus, col: CatalogColumn): string {
  const v = props.counts.matrix[row][col]
  return `${REVIEW_STATUS[row].label} × ${CATALOG_COLUMN_LABEL[col]}：${v.toLocaleString()} 个文件，占全部的 ${percentText(v, props.counts.total)}`
}
</script>

<template>
  <div class="grid grid-cols-[76px_repeat(3,minmax(0,1fr))_minmax(0,0.8fr)_minmax(0,0.9fr)] gap-1.5 text-xs" data-testid="status-matrix">
    <!-- 表头 -->
    <div class="px-1 py-1 text-[11px] text-muted-foreground">审核＼归类</div>
    <div v-for="col in CATALOG_COLUMNS" :key="col" class="px-2 py-1 text-[11px] font-medium text-muted-foreground">
      {{ CATALOG_COLUMN_LABEL[col] }}
    </div>
    <div class="px-2 py-1 text-[11px] font-medium text-muted-foreground">合计</div>
    <div class="flex items-center gap-1 px-2 py-1 text-[11px] font-medium text-muted-foreground">
      <span class="size-1.5 rounded-full bg-ok" />可导出
    </div>

    <!-- 4 行 -->
    <template v-for="row in REVIEW_ROWS" :key="row">
      <div class="flex items-center gap-1.5 px-1 text-[12px] font-medium text-foreground">
        <span class="size-2 shrink-0 rounded-full" :class="REVIEW_COLOR[row]" />{{ REVIEW_STATUS[row].label }}
      </div>
      <div
        v-for="col in CATALOG_COLUMNS"
        :key="col"
        class="rounded-md px-2 py-1.5"
        :class="
          row === 'approved' && isExportableColumn(col, props.requireLocked)
            ? 'bg-ok-soft shadow-[0_0_0_1.5px_var(--ok)]'
            : 'bg-muted/60'
        "
        :title="cellTitle(row, col)"
        data-testid="matrix-cell"
        :data-review="row"
        :data-catalog="col"
        :data-value="props.counts.matrix[row][col]"
      >
        <b
          class="block text-[15px] leading-5 font-semibold tabular-nums"
          :class="props.counts.matrix[row][col] ? 'text-foreground' : 'text-muted-foreground/50'"
        >{{ num(props.counts.matrix[row][col]) }}</b>
        <span class="text-[11px] text-muted-foreground">{{ cellNote(row, col, props.requireLocked) }}</span>
      </div>
      <div
        class="flex items-center px-2 text-[13px] font-medium tabular-nums text-foreground"
        data-testid="matrix-cell"
        :data-review="row"
        data-catalog="total"
        :data-value="props.counts.matrix[row].total"
      >
        {{ num(props.counts.matrix[row].total) }}
      </div>
      <div
        class="flex items-center px-2 text-[13px] font-medium tabular-nums"
        :class="props.counts.exportable[row] ? 'text-foreground' : 'text-muted-foreground/50'"
        data-testid="matrix-cell"
        :data-review="row"
        data-catalog="exportable"
        :data-value="props.counts.exportable[row]"
      >
        {{ num(props.counts.exportable[row]) }}
      </div>
    </template>

    <!-- 合计 -->
    <div class="border-t px-1 pt-1.5 text-[12px] font-medium text-muted-foreground">合计</div>
    <div
      v-for="col in CATALOG_COLUMNS"
      :key="col"
      class="border-t px-2 pt-1.5 text-[13px] font-medium tabular-nums text-foreground"
      data-testid="matrix-cell"
      data-review="total"
      :data-catalog="col"
      :data-value="props.counts.catalog[col]"
    >
      {{ num(props.counts.catalog[col]) }}
      <span class="ml-0.5 text-[11px] font-normal text-muted-foreground">{{ percentText(props.counts.catalog[col], props.counts.total) }}</span>
    </div>
    <div
      class="border-t px-2 pt-1.5 text-[13px] font-semibold tabular-nums text-foreground"
      data-testid="matrix-cell"
      data-review="total"
      data-catalog="total"
      :data-value="props.counts.total"
    >
      {{ num(props.counts.total) }}
    </div>
    <div
      class="border-t px-2 pt-1.5 text-[13px] font-semibold tabular-nums text-foreground"
      data-testid="matrix-cell"
      data-review="total"
      data-catalog="exportable"
      :data-value="props.counts.exportable.total"
    >
      {{ num(props.counts.exportable.total) }}
    </div>
  </div>
</template>
