<script setup lang="ts">
// 归类树（M3-5）：reka-ui 的 Tree，按需加载一层，样式和 FolderTree（文件夹树）一致。归类页的左栏和
// CategoryPicker（移动节点、移动条目时选目标）共用。
// - 状态在 `useCategoryTree()`（每个使用方一个实例）；根节点不显示，从第一层开始；
// - 每行：类型图标、名称、编码（等宽）、子树里的条目数（"· 48 件"）；悬停看类型和完整路径；
// - 点一行：选中（`select` 事件），没展开的顺便展开；点前面的箭头只展开、收起，不选中；
// - 双击一行：`activate` 事件（CategoryPicker 用来直接确认）；双击箭头、双击禁用的行不发；
// - 键盘：上下移动，左右收起、展开，Enter / 空格选中（reka-ui 自带）；
// - `isDisabled`：不能选的节点（例如移动节点时它自己和它的子树），显示为灰色；
// - 插槽 `suffix`（`{node, selected}`）：行尾的附加信息；`dropTargetId`：拖动经过的节点画虚线框（M9 归档）。
// - 截图脚本用的属性：每行 data-testid=tree-category、data-category-id、data-code、data-items。
import { ChevronRightIcon, LibraryBigIcon, LoaderCircleIcon } from '@lucide/vue'
import { TreeItem, TreeRoot } from 'reka-ui'
import { computed } from 'vue'

import type { ArchiveCategory } from '@/api/types'
import type { CategoryTreeState } from '@/composables/useCategoryTree'
import { categoryTypeLabel } from '@/lib/catalog'
import { cn } from '@/lib/utils'

const props = withDefaults(
  defineProps<{
    tree: CategoryTreeState
    /** 当前选中的节点。 */
    selectedId: string | null
    isDisabled?: (node: ArchiveCategory) => boolean
    /** 显示子树里的条目数（默认显示）。 */
    showCounts?: boolean
    /** 拖动文件夹经过的节点（M9：归类页把文件夹拖到节点上归档），画虚线框。 */
    dropTargetId?: string | null
  }>(),
  { isDisabled: undefined, showCounts: true, dropTargetId: null },
)

const emit = defineEmits<{ select: [id: string]; activate: [id: string] }>()

const items = computed(() => props.tree.topLevel.value)

const selectedNode = computed(() =>
  props.selectedId ? (props.tree.nodes.get(props.selectedId) ?? { id: props.selectedId }) : undefined,
)

function getKey(n: ArchiveCategory | { id: string }) {
  return n.id
}

function onChevron(target: EventTarget | null): boolean {
  return target instanceof Element && target.closest('[data-chevron]') !== null
}

interface TreeEventDetail {
  originalEvent: PointerEvent | KeyboardEvent
  value?: ArchiveCategory
  isExpanded: boolean
}

function onSelect(ev: CustomEvent<TreeEventDetail>) {
  // 选中状态由父组件通过 selectedId 控制。
  ev.preventDefault()
  const { originalEvent, value } = ev.detail
  if (!value || onChevron(originalEvent.target)) return
  if (props.isDisabled?.(value)) return
  emit('select', value.id)
}

function onDblclick(node: ArchiveCategory, ev: MouseEvent) {
  if (onChevron(ev.target) || props.isDisabled?.(node)) return
  emit('activate', node.id)
}

function onToggle(ev: CustomEvent<TreeEventDetail>) {
  const { originalEvent, isExpanded } = ev.detail
  // 点名字时只展开、不收起（收起用箭头或键盘的左键）。
  if (originalEvent.type === 'click' && !onChevron(originalEvent.target) && isExpanded) ev.preventDefault()
}

function rowTitle(n: ArchiveCategory): string {
  return `${categoryTypeLabel(n.category_type)}「${n.category_name}」· 编码 ${n.category_code} · 含下级 ${n.item_count} 件\n${props.tree.pathText(n.id)}`
}
</script>

<template>
  <TreeRoot
    v-slot="{ flattenItems }"
    :items="items"
    :get-key="getKey"
    :get-children="props.tree.childrenOf"
    :expanded="props.tree.expanded.value"
    :model-value="selectedNode"
    class="flex flex-col gap-px text-[13px] select-none"
    data-testid="category-tree"
    @update:expanded="(v: string[]) => (props.tree.expanded.value = v)"
  >
    <TreeItem
      v-for="item in flattenItems"
      v-slot="{ isExpanded, isSelected }"
      :key="item._id"
      v-bind="item.bind"
      :disabled="props.isDisabled?.(item.value) ?? false"
      :class="
        cn(
          'group flex cursor-pointer items-center gap-1 rounded-md py-1 pr-2 text-sidebar-foreground outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50',
          'data-disabled:cursor-not-allowed data-disabled:opacity-45 data-disabled:hover:bg-transparent',
          item.value.id === props.selectedId && 'bg-info-soft hover:bg-info-soft',
          item.value.id === props.dropTargetId && 'bg-info-soft outline-2 outline-info outline-dashed',
        )
      "
      :style="{ paddingLeft: `${(item.level - 1) * 14 + 4}px` }"
      :title="rowTitle(item.value)"
      :data-category-id="item.value.id"
      :data-code="item.value.category_code"
      :data-type="item.value.category_type"
      :data-items="item.value.item_count"
      :data-selected-category="item.value.id === props.selectedId ? '' : undefined"
      data-testid="tree-category"
      @select="onSelect"
      @dblclick="onDblclick(item.value, $event)"
      @toggle="onToggle"
    >
      <span
        data-chevron
        class="flex size-4 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-black/5"
      >
        <LoaderCircleIcon
          v-if="isExpanded && props.tree.loading.has(item.value.id) && !props.tree.childIds.has(item.value.id)"
          class="size-3 animate-spin"
        />
        <ChevronRightIcon
          v-else-if="item.hasChildren"
          class="size-3.5 transition-transform"
          :class="isExpanded ? 'rotate-90' : ''"
        />
      </span>
      <LibraryBigIcon class="size-4 shrink-0 text-muted-foreground/80" />
      <span
        class="min-w-0 truncate"
        :class="
          isSelected || item.value.id === props.selectedId
            ? 'font-medium text-info'
            : item.level === 1
              ? 'font-medium text-foreground'
              : ''
        "
      >
        {{ item.value.category_name }}
      </span>
      <span
        class="shrink-0 rounded bg-muted px-1 font-mono text-[11px] leading-4 text-muted-foreground"
        data-testid="tree-category-code"
      >{{ item.value.category_code }}</span>
      <span class="min-w-0 flex-1" />
      <span
        v-if="props.showCounts && item.value.item_count > 0"
        class="shrink-0 font-mono text-[11px] text-muted-foreground tabular-nums"
        data-testid="tree-category-count"
      >{{ item.value.item_count.toLocaleString() }} 件</span>
      <slot name="suffix" :node="item.value" :selected="item.value.id === props.selectedId" />
    </TreeItem>
  </TreeRoot>
</template>
