<script setup lang="ts">
// 看板的总数（2026-10-19 用户决定简化，Mockup v2）：整个项目一行五个数，按流程从左到右，每个一条单色进度条和一行小字。
// 审核按页（`total`、`review.*`）；归档、锁定、导出按件（`items.*`），页数做补充（已归档的页 `catalog.cataloged`）。
//   已上传 total 页 → 审核通过 review.approved / total 页 → 已归档 items.total 件 · catalog.cataloged 页 →
//   已锁定 items.locked / items.total 件 → 可导出 items.exportable 件 · items.exportable_pages 页。
// 截图脚本用的属性：data-testid=flow-step（data-kind：uploaded、approved、archived、locked、exportable；data-value 是大字
// 的主数字，另有 data-total、data-pages、data-unassigned、data-pending、data-rejected、data-modifying，见各步）。
import { computed } from 'vue'

import type { CatalogCounts } from '@/api/types'

import { barWidth, exportableText, num, percentText } from './dashboard'

const props = defineProps<{
  counts: CatalogCounts
  requireLocked: boolean
}>()

interface Step {
  kind: string
  label: string
  /** 大字：主数字和后面的小字。 */
  value: number
  unit: string
  bar: string
  color: string
  /** 进度条下面的一行小字（`em` 的部分是数字）。 */
  rest: { text: string; em?: boolean }[]
  title?: string
  data: Record<string, number>
}

const steps = computed<Step[]>(() => {
  const c = props.counts
  const it = c.items
  const ex = exportableText(props.requireLocked)
  const list: Step[] = [
    {
      kind: 'uploaded',
      label: '已上传',
      value: c.total,
      unit: '页',
      bar: c.total > 0 ? '100%' : '0%',
      color: 'bg-stone-600',
      rest: [],
      data: {},
    },
    {
      kind: 'approved',
      label: '审核通过',
      value: c.review.approved,
      unit: `/ ${num(c.total)} 页 · ${percentText(c.review.approved, c.total)}`,
      bar: barWidth(c.review.approved, c.total),
      color: 'bg-ok',
      rest: [
        { text: '待分配 ' },
        { text: num(c.review.unassigned), em: true },
        { text: ' · 待审 ' },
        { text: num(c.review.pending), em: true },
        { text: ' · 驳回 ' },
        { text: num(c.review.rejected), em: true },
      ],
      data: { total: c.total, unassigned: c.review.unassigned, pending: c.review.pending, rejected: c.review.rejected },
    },
    {
      kind: 'archived',
      label: '已归档',
      value: it.total,
      unit: `件 · ${num(c.catalog.cataloged)} 页`,
      bar: barWidth(c.catalog.cataloged, c.total),
      color: 'bg-violet-600',
      rest: [
        { text: `${num(c.catalog.cataloged)} / ${num(c.total)} 页 · ${percentText(c.catalog.cataloged, c.total)}` },
        ...(it.modifying ? [{ text: ' · 修改中 ' }, { text: num(it.modifying), em: true }, { text: ' 件' }] : []),
      ],
      data: { pages: c.catalog.cataloged, modifying: it.modifying },
    },
    {
      kind: 'locked',
      label: '已锁定',
      value: it.locked,
      unit: `/ ${num(it.total)} 件`,
      bar: barWidth(it.locked, it.total),
      color: 'bg-violet-900',
      rest: [{ text: '未锁定 ' }, { text: num(it.total - it.locked), em: true }, { text: ' 件' }],
      data: { total: it.total },
    },
    {
      kind: 'exportable',
      label: '可导出',
      value: it.exportable,
      unit: `件 · ${num(it.exportable_pages)} 页`,
      bar: barWidth(it.exportable_pages, c.total),
      color: 'bg-ok',
      rest: [{ text: ex.short }],
      title: ex.long,
      data: { pages: it.exportable_pages },
    },
  ]
  return list
})
</script>

<template>
  <div class="grid grid-cols-5 overflow-hidden rounded-lg shadow-[0_0_0_1px_rgba(0,0,0,0.08)]" data-testid="flow-steps">
    <div
      v-for="(s, i) in steps"
      :key="s.kind"
      class="relative min-w-0 border-r px-4 py-3.5 last:border-r-0"
      :class="[
        s.kind === 'exportable' ? 'bg-ok-soft/70' : '',
        i < steps.length - 1
          ? `after:absolute after:top-[22px] after:-right-[7px] after:z-[1] after:size-3 after:rotate-45 after:border-t after:border-r after:border-border after:content-[''] ${s.kind === 'exportable' ? 'after:bg-ok-soft' : 'after:bg-background'}`
          : '',
      ]"
      :title="s.title"
      data-testid="flow-step"
      :data-kind="s.kind"
      :data-value="s.value"
      v-bind="Object.fromEntries(Object.entries(s.data).map(([k, v]) => [`data-${k}`, v]))"
    >
      <div class="flex items-center gap-1.5 text-xs text-muted-foreground">
        <span
          class="inline-flex size-[18px] shrink-0 items-center justify-center rounded-full text-[11px]"
          :class="s.kind === 'exportable' ? 'bg-ok text-white' : 'bg-muted text-muted-foreground'"
        >{{ i + 1 }}</span>
        <span class="truncate">{{ s.label }}</span>
      </div>
      <div class="mt-1 flex flex-wrap items-baseline gap-x-1.5 leading-tight">
        <span class="text-2xl font-semibold tracking-tight text-foreground tabular-nums" data-testid="flow-value">{{ num(s.value) }}</span>
        <span class="text-[13px] text-muted-foreground tabular-nums">{{ s.unit }}</span>
      </div>
      <div class="mt-2 mb-1.5 h-1.5 overflow-hidden rounded-full bg-muted">
        <div class="h-full rounded-full" :class="s.color" :style="{ width: s.bar }" />
      </div>
      <div class="truncate text-xs text-muted-foreground tabular-nums">
        <template v-if="s.rest.length">
          <template v-for="(r, j) in s.rest" :key="j"><span v-if="r.em" class="text-foreground">{{ r.text }}</span><template v-else>{{ r.text }}</template></template>
        </template>
        <template v-else>&nbsp;</template>
      </div>
    </div>
  </div>
</template>
