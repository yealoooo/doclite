<script setup lang="ts">
// 选择文件夹的对话框（M1-10）：基于 FolderTree，按需加载。给 M1-12 的"移动到…"、M1-13 的"选择目标文件夹"用；
// 文件目录页的"转到…"也用它。
//
//   <FolderPicker
//     v-model:open="open"
//     :root-id="project.root_folder_id" :root-name="project.project_name"
//     :initial-folder-id="currentFolderId"            // 打开时定位到这里（可选，默认根文件夹）
//     title="移动到…" confirm-text="移动到这里"
//     :exclude="[{ path_code: folder.path_code }]"    // 不能选的子树（可选，例如移动文件夹时它自己）
//     :disabled-ids="[currentFolderId]"               // 不能选的单个文件夹（可选，例如文件当前所在的文件夹）
//     @confirm="(target) => …"                         // target: { id, folder_name, path, remark, path_code }
//   >
//     <template #footer-start="{ selected, refresh, select }">…（例如"新建文件夹"按钮）</template>
//   </FolderPicker>
//
// 每次打开都重新加载（不和页面左侧的树共用状态）。`footer-start` 插槽拿到当前选中的文件夹、
// `refresh(id)`（重新加载某个文件夹的子项）和 `select(id)`（选中并展开到它），新建文件夹后用。
import { computed, ref, watch } from 'vue'

import { getFolder } from '@/api/folders'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import FolderTree from '@/components/files/FolderTree.vue'
import { type TreeFolder, useFolderTree } from '@/composables/useFolderTree'
import { HANDLE_ALL } from '@/api/http'

export interface PickedFolder {
  id: string
  folder_name: string
  /** 从根起的路径（不含根），例如 `/2025 文书档案/盒 001`；根文件夹是 `/`。 */
  path: string
  remark: string | null
  path_code: string
}

const props = withDefaults(
  defineProps<{
    rootId: string
    rootName: string
    initialFolderId?: string | null
    title?: string
    description?: string
    confirmText?: string
    /** 不能选的子树（按路径编码前缀判断，含它自己）。 */
    exclude?: { path_code: string }[]
    /** 不能选的单个文件夹（仍可展开）。 */
    disabledIds?: string[]
  }>(),
  {
    initialFolderId: null,
    title: '选择文件夹',
    description: undefined,
    confirmText: '确定',
    exclude: () => [],
    disabledIds: () => [],
  },
)

const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ confirm: [folder: PickedFolder] }>()

const tree = useFolderTree()
const selectedId = ref<string | null>(null)

function excluded(f: TreeFolder): boolean {
  return props.exclude.some((e) => f.path_code.startsWith(e.path_code))
}

function selectable(id: string | null): boolean {
  if (!id || props.disabledIds.includes(id)) return false
  const node = tree.nodes.get(id)
  return !!node && !excluded(node)
}

const selected = computed<PickedFolder | null>(() => {
  const id = selectedId.value
  const node = id ? tree.nodes.get(id) : undefined
  if (!id || !node) return null
  return { id, folder_name: node.folder_name, path: tree.pathOf(id), remark: node.remark, path_code: node.path_code }
})

async function select(id: string) {
  selectedId.value = id
  if (id === tree.rootId.value) return
  try {
    const folder = await getFolder(id, HANDLE_ALL)
    if (selectedId.value !== id) return
    await tree.revealPath(folder)
    // 父文件夹之前就加载过、而这个文件夹是后来新建的：重新加载父文件夹。
    if (!tree.nodes.has(id) && folder.parent_id) await tree.load(folder.parent_id).catch(() => null)
    // 顺便展开它，方便选它下面的子文件夹。
    if (selectedId.value === id && !tree.expanded.value.includes(id)) tree.expanded.value = [...tree.expanded.value, id]
  } catch {
    // 文件夹已经不在了：回到根文件夹。
    if (selectedId.value === id) selectedId.value = tree.rootId.value
  }
}

async function refresh(id: string) {
  await tree.load(id).catch(() => null)
}

watch(
  open,
  (v) => {
    if (!v) return
    tree.reset({ id: props.rootId, folder_name: props.rootName })
    selectedId.value = props.rootId
    if (props.initialFolderId && props.initialFolderId !== props.rootId) void select(props.initialFolderId)
  },
  { immediate: true },
)

function confirm() {
  if (!selected.value || !selectable(selected.value.id)) return
  emit('confirm', selected.value)
  open.value = false
}

/** 双击一行：只有双击的就是当前选中、而且能选的文件夹时才确认（双击箭头、禁用的行不会走到这里）。 */
function onActivate(id: string) {
  if (id === selectedId.value && selectable(id)) confirm()
}

defineExpose({ refresh, select })
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="gap-4 sm:max-w-lg" data-testid="folder-picker">
      <DialogHeader>
        <DialogTitle>{{ props.title }}</DialogTitle>
        <DialogDescription v-if="props.description">{{ props.description }}</DialogDescription>
      </DialogHeader>

      <div class="h-80 overflow-auto rounded-lg border bg-background p-1.5">
        <FolderTree
          :tree="tree"
          :selected-id="selectedId"
          :is-disabled="excluded"
          @select="(id) => (selectedId = id)"
          @activate="onActivate"
        />
      </div>

      <div class="min-h-10 rounded-md bg-muted px-3 py-2 text-xs" data-testid="folder-picker-selected">
        <template v-if="selected">
          <div class="flex items-center gap-2">
            <span class="shrink-0 text-muted-foreground">选中</span>
            <span class="min-w-0 truncate font-medium text-foreground" :title="selected.path">{{ selected.path }}</span>
          </div>
          <div v-if="selected.remark" class="mt-0.5 flex gap-2">
            <span class="shrink-0 text-muted-foreground">原件</span>
            <span class="truncate text-foreground">{{ selected.remark }}</span>
          </div>
          <div v-if="!selectable(selected.id)" class="mt-0.5 text-destructive">不能选这个文件夹</div>
        </template>
        <span v-else class="text-muted-foreground">请选择一个文件夹</span>
      </div>

      <DialogFooter class="items-center sm:justify-between">
        <div class="flex items-center gap-2">
          <slot name="footer-start" :selected="selected" :refresh="refresh" :select="select" />
        </div>
        <div class="flex gap-2">
          <Button variant="outline" @click="open = false">取消</Button>
          <Button :disabled="!selectable(selectedId)" data-testid="folder-picker-confirm" @click="confirm">
            {{ props.confirmText }}
          </Button>
        </div>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
