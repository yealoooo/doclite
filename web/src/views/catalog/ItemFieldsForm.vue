<script setup lang="ts">
// 条目详情的著录表单（M3-5，从 ItemDetail 拆出来）：题名和按 catalog-settings 的 item_fields 动态生成的各项（text，长的用
// 多行框；date；enum 下拉），旧值的提示，设置里已经删掉的著录项（只能保留或清空），收起（只留题名，记在 localStorage）。
// 状态（表单值、错误、别人改成的值）都在 ItemDetail 里，这里只显示，改动都发事件（题名用 v-model:title）。
import { ChevronDownIcon, ChevronUpIcon } from '@lucide/vue'
import { useLocalStorage } from '@vueuse/core'
import { computed, watch } from 'vue'

import type { ItemFieldDef } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { TITLE_KEY } from '@/lib/catalog'
import { FLAG_SERIALIZER } from '@/lib/utils'

const props = defineProps<{
  defs: ItemFieldDef[]
  /** 著录项的表单值（改动发 `field`）。 */
  values: Record<string, string>
  /** 每一项开始编辑时的值。 */
  initial: Record<string, string>
  /** 设置里已经没有、点了"清空"的著录项。 */
  cleared: Set<string>
  /** 每一项的错误（`title` 是题名）。 */
  errors: Record<string, string>
  /** 两边都改了的项：key → 别人改成的值。 */
  theirs: Record<string, string>
  /** 库里的值和现在的设置对不上的项（field_issues）：key → 说明。 */
  issues: Map<string, string>
  /** 库里存着、设置里已经没有的著录项。 */
  orphanKeys: string[]
  locked: boolean
}>()
const title = defineModel<string>('title', { required: true })

const emit = defineEmits<{
  submit: []
  /** 题名改了（清掉它的错误）。 */
  input: [key: string]
  /** 一个著录项改成了 value（父组件写进表单值、清掉它的错误）。 */
  field: [key: string, value: string]
  /** 清空、保留设置里已经没有的著录项。 */
  clear: [key: string]
  keep: [key: string]
}>()

// 著录项收起（给下面的页腾地方）：只留题名；记在 localStorage（个人习惯，读写失败就展开）。
const formCollapsed = useLocalStorage('doclite.catalog.form-collapsed', false, { serializer: FLAG_SERIALIZER })
// 收起着的项有错误（本地校验、server 的 400）：展开，让用户看得到。
watch(
  () => props.errors,
  (e) => {
    if (formCollapsed.value && Object.keys(e).some((k) => k !== TITLE_KEY)) formCollapsed.value = false
  },
)
/** 收起时的一行摘要：各项的值（空的不写）。 */
const collapsedSummary = computed(() =>
  props.defs
    .map((d) => [d.label, (props.values[d.key] ?? '').trim()] as const)
    .filter(([, v]) => v)
    .map(([l, v]) => `${l}：${v}`)
    .join(' · '),
)

function fieldError(key: string): string | undefined {
  return props.errors[key]
}

/** 库里存的 enum 值不在现在的选项里（旧值）：一直留在下拉里，改走以后还能改回来。 */
function oldEnumValue(d: ItemFieldDef): string | null {
  const v = props.initial[d.key] ?? ''
  return v && !d.options.includes(v) ? v : null
}

function isLong(d: ItemFieldDef): boolean {
  return d.type === 'text' && (d.max_length ?? 500) > 200
}

const inputCls =
  'h-8 w-full min-w-0 rounded-md border border-input bg-background px-2 text-compact text-foreground shadow-xs outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:bg-muted/50 disabled:text-foreground aria-invalid:border-destructive'
</script>

<template>
  <form
    class="relative max-h-[45%] shrink-0 overflow-y-auto border-b px-4 py-2.5"
    data-testid="item-form"
    :data-collapsed="formCollapsed ? 'true' : undefined"
    @submit.prevent="emit('submit')"
  >
    <button
      v-if="defs.length || orphanKeys.length"
      type="button"
      class="absolute top-2 right-4 z-10 flex items-center gap-0.5 rounded px-1.5 text-2xs leading-5 text-muted-foreground hover:bg-muted hover:text-foreground"
      :title="formCollapsed ? '展开全部著录项' : '只留题名，给下面的页腾地方'"
      data-testid="item-form-toggle"
      @click="formCollapsed = !formCollapsed"
    >
      <template v-if="formCollapsed">
        展开著录项（{{ defs.length + orphanKeys.length }}）<ChevronDownIcon class="size-3" />
      </template>
      <template v-else>收起著录项<ChevronUpIcon class="size-3" /></template>
    </button>
    <div class="grid grid-cols-[repeat(auto-fill,minmax(180px,1fr))] gap-x-3 gap-y-2">
      <!-- 题名（固定列） -->
      <label
        class="col-span-full grid gap-1"
        data-testid="item-field"
        :data-key="TITLE_KEY"
        :data-error="fieldError(TITLE_KEY) ?? undefined"
      >
        <span class="text-xs font-medium text-muted-foreground">题名 <span class="text-destructive">*</span></span>
        <input
          v-model="title"
          :class="inputCls"
          :disabled="locked"
          :aria-invalid="!!fieldError(TITLE_KEY) || undefined"
          placeholder="（未填题名）"
          data-testid="item-title"
          @input="emit('input', TITLE_KEY)"
        >
        <span v-if="fieldError(TITLE_KEY)" class="text-2xs text-destructive" data-testid="field-error">{{ fieldError(TITLE_KEY) }}</span>
        <span v-if="theirs[TITLE_KEY] !== undefined" class="text-2xs text-warn">别人改成了：{{ theirs[TITLE_KEY] || '（空）' }}</span>
      </label>

      <!-- 设置里的著录项 -->
      <label
        v-for="d in defs"
        v-show="!formCollapsed"
        :key="d.key"
        class="grid content-start gap-1"
        :class="isLong(d) && 'col-span-2'"
        data-testid="item-field"
        :data-key="d.key"
        :data-type="d.type"
        :data-error="fieldError(d.key) ?? undefined"
        :data-issue="issues.get(d.key) ?? undefined"
      >
        <span class="truncate text-xs font-medium text-muted-foreground" :title="d.key">
          {{ d.label }}<span v-if="d.required" class="text-destructive"> *</span>
        </span>
        <select
          v-if="d.type === 'enum'"
          :value="props.values[d.key] ?? ''"
          :class="inputCls"
          :disabled="locked"
          :aria-invalid="!!fieldError(d.key) || undefined"
          data-testid="item-field-input"
          @change="emit('field', d.key, ($event.target as HTMLSelectElement).value)"
        >
          <option value="">（不填）</option>
          <option v-if="oldEnumValue(d)" :value="oldEnumValue(d)!">{{ oldEnumValue(d) }}（旧值，不在选项里）</option>
          <option v-for="o in d.options" :key="o" :value="o">{{ o }}</option>
        </select>
        <Textarea
          v-else-if="isLong(d)"
          :model-value="props.values[d.key] ?? ''"
          class="min-h-14 px-2 py-1 text-compact md:text-compact"
          rows="2"
          :disabled="locked"
          :aria-invalid="!!fieldError(d.key) || undefined"
          data-testid="item-field-input"
          @update:model-value="(v) => emit('field', d.key, String(v))"
        />
        <input
          v-else
          :value="props.values[d.key] ?? ''"
          :class="[inputCls, d.type === 'date' && 'font-mono']"
          :disabled="locked"
          :aria-invalid="!!fieldError(d.key) || undefined"
          :placeholder="d.type === 'date' ? 'YYYY、YYYY-MM 或 YYYY-MM-DD' : ''"
          data-testid="item-field-input"
          @input="emit('field', d.key, ($event.target as HTMLInputElement).value)"
        >
        <span v-if="fieldError(d.key)" class="text-2xs text-destructive" data-testid="field-error">{{ fieldError(d.key) }}</span>
        <span
          v-else-if="issues.get(d.key) && (values[d.key] ?? '').trim() === (initial[d.key] ?? '').trim()"
          class="text-2xs text-warn"
          data-testid="field-issue"
        >旧值：{{ issues.get(d.key) }}。不改它照样能保存，锁定前要改掉。</span>
        <span v-if="theirs[d.key] !== undefined" class="text-2xs text-warn">别人改成了：{{ theirs[d.key] || '（空）' }}</span>
      </label>
    </div>

    <!-- 设置里已经没有的著录项（旧值） -->
    <div
      v-if="orphanKeys.length"
      v-show="!formCollapsed"
      class="mt-3 rounded-md border border-dashed px-3 py-2"
      data-testid="item-orphan-fields"
    >
      <div class="text-xs font-medium text-muted-foreground">项目设置里已经删掉的著录项（原值保留，保存时不会丢；也可以清空）</div>
      <div
        v-for="k in orphanKeys"
        :key="k"
        class="mt-1.5 flex items-center gap-2 text-compact"
        data-testid="item-orphan-field"
        :data-key="k"
        :data-cleared="cleared.has(k) ? 'true' : undefined"
      >
        <span class="w-28 shrink-0 truncate font-mono text-xs text-muted-foreground" :title="k">{{ k }}</span>
        <span class="min-w-0 flex-1 truncate" :class="cleared.has(k) ? 'text-muted-foreground line-through' : 'text-foreground'">
          {{ initial[k] || '（空）' }}
        </span>
        <template v-if="!locked">
          <Button v-if="!cleared.has(k)" type="button" size="xs" variant="ghost" @click="emit('clear', k)">清空</Button>
          <Button v-else type="button" size="xs" variant="ghost" @click="emit('keep', k)">保留</Button>
        </template>
      </div>
    </div>
    <p v-if="!defs.length" class="mt-2 text-xs text-muted-foreground">
      项目还没有设置其他著录项（在「系统管理 › 项目」的"著录项和档号"里设置）。
    </p>
    <p v-else-if="formCollapsed && collapsedSummary" class="mt-1.5 truncate text-xs text-muted-foreground" data-testid="item-form-summary">
      {{ collapsedSummary }}
    </p>
  </form>
</template>
