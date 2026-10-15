<script setup lang="ts">
// 选择归类节点的对话框（M3-5）：基于 CategoryTree，按需加载。归类页的"移动节点到…"和"条目移到别的节点…"用。
//
//   <CategoryPicker
//     v-model:open="open"
//     :project-id="pid"
//     :initial-id="current"                           // 打开时定位到这里（可选）
//     title="移动到…" confirm-text="移到这里"
//     :exclude="[{ path_code: node.path_code }]"      // 不能选的子树（可选，例如移动节点时它自己）
//     :disabled-ids="[node.parent_id]"                // 不能选的单个节点（可选，例如现在所在的位置）
//     allow-top                                        // 列出"顶层"（根节点，移动节点到最上层时用）
//     @confirm="(t) => …"                              // t: { id, path, node }
//   />
//
// 每次打开都重新加载（不和页面左侧的树共用状态）。
import { computed, ref, watch } from 'vue'

import type { ArchiveCategory } from '@/api/types'
import CategoryTree from '@/components/catalog/CategoryTree.vue'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useCategoryTree } from '@/composables/useCategoryTree'
import { categoryTypeLabel } from '@/lib/catalog'

export interface PickedCategory {
  id: string
  /** 从顶层起的名称路径；顶层（根节点）是空串。 */
  path: string
  node: ArchiveCategory
}

const props = withDefaults(
  defineProps<{
    projectId: string
    initialId?: string | null
    title?: string
    description?: string
    confirmText?: string
    exclude?: { path_code: string }[]
    disabledIds?: (string | null)[]
    /** 列出"顶层"（根节点）。 */
    allowTop?: boolean
  }>(),
  {
    initialId: null,
    title: '选择归类节点',
    description: undefined,
    confirmText: '确定',
    exclude: () => [],
    disabledIds: () => [],
    allowTop: false,
  },
)

const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ confirm: [target: PickedCategory] }>()

const tree = useCategoryTree()
const selectedId = ref<string | null>(null)
const state = ref<'loading' | 'ready' | 'error'>('loading')
/** 打开时定位到初始节点也做完了（之后树不再因为定位而展开、挪位置；截图脚本等它再点）。 */
const settled = ref(false)

function excluded(n: ArchiveCategory): boolean {
  return props.exclude.some((e) => n.path_code.startsWith(e.path_code))
}

function selectable(id: string | null): boolean {
  if (!id || props.disabledIds.includes(id)) return false
  if (id === tree.rootId.value) return props.allowTop
  const node = tree.nodes.get(id)
  return !!node && !excluded(node)
}

const selected = computed<PickedCategory | null>(() => {
  const id = selectedId.value
  const node = id ? tree.nodes.get(id) : undefined
  if (!id || !node) return null
  return { id, path: id === tree.rootId.value ? '' : tree.pathText(id), node }
})

let openSeq = 0
watch(
  open,
  async (v) => {
    if (!v) return
    const my = ++openSeq
    state.value = 'loading'
    settled.value = false
    selectedId.value = null
    try {
      const root = await tree.init(props.projectId)
      if (my !== openSeq || !root) return
      state.value = 'ready'
      if (props.initialId && props.initialId !== root.id) {
        const node = await tree.reveal(props.initialId).catch(() => null)
        if (my !== openSeq) return
        if (node) {
          selectedId.value = node.id
          if (!tree.expanded.value.includes(node.id)) tree.expanded.value = [...tree.expanded.value, node.id]
        }
      } else if (props.allowTop) selectedId.value = root.id
      if (my === openSeq) settled.value = true
    } catch {
      if (my === openSeq) state.value = 'error'
    }
  },
  { immediate: true },
)

function confirm() {
  if (!selected.value || !selectable(selected.value.id)) return
  emit('confirm', selected.value)
  open.value = false
}

/** 双击一行：只有双击的就是当前选中、而且能选的节点时才确认。 */
function onActivate(id: string) {
  if (id === selectedId.value && selectable(id)) confirm()
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent
      class="gap-4 sm:max-w-lg"
      data-testid="category-picker"
      :data-state-load="state"
      :data-settled="settled ? 'true' : undefined"
    >
      <DialogHeader>
        <DialogTitle>{{ props.title }}</DialogTitle>
        <DialogDescription v-if="props.description">{{ props.description }}</DialogDescription>
      </DialogHeader>

      <div class="h-80 overflow-auto rounded-lg border bg-background p-1.5">
        <p v-if="state === 'error'" class="p-3 text-sm text-destructive">归类树加载失败，请关掉重试</p>
        <template v-else>
          <button
            v-if="props.allowTop && tree.rootId.value"
            type="button"
            class="mb-px flex w-full items-center gap-1.5 rounded-md px-2 py-1 text-left text-[13px] hover:bg-muted"
            :class="[
              selectedId === tree.rootId.value ? 'bg-info-soft font-medium text-info hover:bg-info-soft' : 'text-foreground',
              !selectable(tree.rootId.value) && 'cursor-not-allowed opacity-45',
            ]"
            data-testid="category-picker-top"
            @click="selectedId = tree.rootId.value"
          >
            （顶层，{{ tree.root.value?.category_name }}）
          </button>
          <CategoryTree
            :tree="tree"
            :selected-id="selectedId"
            :is-disabled="excluded"
            :show-counts="false"
            @select="(id) => (selectedId = id)"
            @activate="onActivate"
          />
          <p v-if="state === 'ready' && !tree.topLevel.value.length" class="p-3 text-sm text-muted-foreground">
            还没有归类节点
          </p>
        </template>
      </div>

      <div class="min-h-10 rounded-md bg-muted px-3 py-2 text-xs" data-testid="category-picker-selected">
        <template v-if="selected">
          <div class="flex items-center gap-2">
            <span class="shrink-0 text-muted-foreground">选中</span>
            <span class="min-w-0 truncate font-medium text-foreground" :title="selected.path">
              {{ selected.path || '顶层' }}
            </span>
            <span v-if="selected.id !== tree.rootId.value" class="shrink-0 text-muted-foreground">
              {{ categoryTypeLabel(selected.node.category_type) }} · {{ selected.node.category_code }}
            </span>
          </div>
          <div v-if="!selectable(selected.id)" class="mt-0.5 text-destructive">不能选这个节点</div>
        </template>
        <span v-else class="text-muted-foreground">请选择一个归类节点</span>
      </div>

      <DialogFooter>
        <Button variant="outline" @click="open = false">取消</Button>
        <Button :disabled="!selectable(selectedId)" data-testid="category-picker-confirm" @click="confirm">
          {{ props.confirmText }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
