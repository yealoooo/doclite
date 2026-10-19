<script setup lang="ts">
// 文件夹树（M1-10）：reka-ui 的 Tree，按需加载子文件夹。文件目录页的左侧树和 FolderPicker 共用。
// - 状态在 `useFolderTree()`（每个使用方一个实例）；
// - 点一行：选中（`select` 事件），没展开的顺便展开；点前面的箭头只展开、收起，不选中；
// - 双击一行：`activate` 事件（例如 FolderPicker 用来直接确认）；双击箭头、双击禁用的行不发；
// - 键盘：上下移动，左右收起、展开，Enter / 空格选中（reka-ui 自带）；
// - `isDisabled`：不能选的文件夹（例如移动文件夹时，它自己和它的子树），显示为灰色。
// - 插槽 `suffix`（`{folder, selected}`）：行尾的附加信息，例如审核分配页的待分配计数（M2-5）。
import { ChevronRightIcon, LoaderCircleIcon } from '@lucide/vue'
import FolderGlyph from '@/components/files/FolderGlyph.vue'
import { TreeItem, TreeRoot } from 'reka-ui'
import { computed } from 'vue'

import type { FolderTreeState, TreeFolder } from '@/composables/useFolderTree'
import { cn } from '@/lib/utils'

const props = defineProps<{
  tree: FolderTreeState
  /** 当前选中的文件夹。 */
  selectedId: string | null
  isDisabled?: (folder: TreeFolder) => boolean
}>()

const emit = defineEmits<{ select: [id: string]; activate: [id: string] }>()

const items = computed(() => {
  const id = props.tree.rootId.value
  const root = id ? props.tree.nodes.get(id) : undefined
  return root ? [root] : []
})

const selectedNode = computed(() =>
  props.selectedId ? (props.tree.nodes.get(props.selectedId) ?? { id: props.selectedId }) : undefined,
)

function getKey(f: TreeFolder | { id: string }) {
  return f.id
}

function onChevron(target: EventTarget | null): boolean {
  return target instanceof Element && target.closest('[data-chevron]') !== null
}

interface TreeEventDetail {
  originalEvent: PointerEvent | KeyboardEvent
  value?: TreeFolder
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

function onDblclick(folder: TreeFolder, ev: MouseEvent) {
  if (onChevron(ev.target) || props.isDisabled?.(folder)) return
  emit('activate', folder.id)
}

function onToggle(ev: CustomEvent<TreeEventDetail>) {
  const { originalEvent, isExpanded } = ev.detail
  // 点名字时只展开、不收起（收起用箭头或键盘的左键）。
  if (originalEvent.type === 'click' && !onChevron(originalEvent.target) && isExpanded) ev.preventDefault()
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
    class="flex flex-col gap-px text-compact select-none"
    data-testid="folder-tree"
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
        )
      "
      :style="{ paddingLeft: `${(item.level - 1) * 14 + 4}px` }"
      :data-folder-id="item.value.id"
      :data-selected-folder="item.value.id === props.selectedId ? '' : undefined"
      data-testid="tree-folder"
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
      <FolderGlyph :open="isExpanded && item.hasChildren" :stroke-width="2" class="size-4" />
      <span
        class="min-w-0 flex-1 truncate"
        :class="
          isSelected || item.value.id === props.selectedId
            ? 'font-medium text-info'
            : item.level === 1
              ? 'font-medium text-foreground'
              : ''
        "
        :title="item.value.remark ? `${item.value.folder_name}（${item.value.remark}）` : item.value.folder_name"
      >
        {{ item.value.folder_name }}
      </span>
      <!-- 行尾的附加信息（M2-5：审核分配页的待分配计数）。 -->
      <slot name="suffix" :folder="item.value" :selected="item.value.id === props.selectedId" />
    </TreeItem>
  </TreeRoot>
</template>

