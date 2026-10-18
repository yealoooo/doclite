<script setup lang="ts">
// 看板的 KPI（M3-7，取法见 roadmap M3-4 的须知）：已上传 `total`、审核通过 `review.approved`、待重扫
// `review.rejected`、已归类 `catalog.cataloged`、已定稿 `catalog.locked`、可导出 `exportable.approved`（主数字，
// "通过且可导出"，导出时"只导出审核通过的"默认勾选）/ `exportable.total`（次要数字）。整个项目（根文件夹）。
// 截图脚本用的属性：data-testid=kpi（data-kind、data-value；可导出另有 data-total）。
import { computed } from 'vue'

import type { CatalogCounts } from '@/api/types'

import { num, percentText } from './dashboard'

const props = defineProps<{
  counts: CatalogCounts
  requireLocked: boolean
}>()

interface Tile {
  kind: string
  label: string
  value: number
  dot: string | null
  sub: string
  total?: number
}

const tiles = computed<Tile[]>(() => {
  const c = props.counts
  return [
    { kind: 'total', label: '已上传文件', value: c.total, dot: null, sub: '未删除的文件' },
    {
      kind: 'approved',
      label: '审核通过',
      value: c.review.approved,
      dot: 'bg-ok',
      sub: `占 ${percentText(c.review.approved, c.total)} · 待审 ${num(c.review.pending)}`,
    },
    {
      kind: 'rejected',
      label: '待重扫',
      value: c.review.rejected,
      dot: 'bg-destructive',
      sub: '已驳回，等扫描员重扫',
    },
    {
      kind: 'cataloged',
      label: '已归档',
      value: c.catalog.cataloged,
      dot: 'bg-violet-300',
      sub: `占 ${percentText(c.catalog.cataloged, c.total)} · 未归档 ${num(c.catalog.uncataloged)}`,
    },
    {
      kind: 'locked',
      label: '已锁定',
      value: c.catalog.locked,
      dot: 'bg-violet-700',
      sub: `占 ${percentText(c.catalog.locked, c.total)}`,
    },
    {
      kind: 'exportable',
      label: props.requireLocked ? '可导出（通过且已锁定）' : '可导出（通过且已归档）',
      value: c.exportable.approved,
      dot: null,
      sub: `全部可导出 ${num(c.exportable.total)}（含未通过 ${num(c.exportable.total - c.exportable.approved)}）`,
      total: c.exportable.total,
    },
  ]
})
</script>

<template>
  <div class="grid grid-cols-6 gap-3" data-testid="kpi-tiles">
    <div
      v-for="t in tiles"
      :key="t.kind"
      class="min-w-0 rounded-lg px-3.5 py-3"
      :class="
        t.kind === 'exportable'
          ? 'bg-ok-soft/60 shadow-[0_0_0_1px_rgba(21,128,61,0.35)]'
          : 'shadow-[0_0_0_1px_rgba(0,0,0,0.08),0_2px_2px_rgba(0,0,0,0.03)]'
      "
      data-testid="kpi"
      :data-kind="t.kind"
      :data-value="t.value"
      :data-total="t.total"
    >
      <div class="flex items-center gap-1.5 truncate text-xs text-muted-foreground">
        <span v-if="t.dot" class="size-2 shrink-0 rounded-full" :class="t.dot" />
        <span class="truncate">{{ t.label }}</span>
      </div>
      <div class="mt-1 text-2xl leading-8 font-semibold tracking-tight text-foreground" data-testid="kpi-value">{{ num(t.value) }}</div>
      <div class="truncate text-[11.5px] text-muted-foreground" :title="t.sub">{{ t.sub }}</div>
    </div>
  </div>
</template>
