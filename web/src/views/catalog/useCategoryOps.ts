// 归类页左栏归类树上的节点操作（M3-5，从 CatalogView 拆出来）：新建、修改（CategoryFormDialog）、右键菜单针对的节点、
// 移动（CategoryPicker）、删除、按新模板重新生成档号。对话框的状态在这里，组件在页面的模板里。操作之后刷新树，
// 再调 `afterChange`（页面重取件列表和打开的件：档号可能都变了）。

import { reactive, ref } from 'vue'
import { toast } from 'vue-sonner'

import { deleteArchiveCategory, renumberArchiveItems, updateArchiveCategory } from '@/api/catalog'
import { ApiError, errorMessage, HANDLE_ALL, handledGlobally, isApiError } from '@/api/http'
import type { ArchiveCategory, UpdatedArchiveCategory } from '@/api/types'
import type { PickedCategory } from '@/components/catalog/CategoryPicker.vue'
import type { CategoryTreeState } from '@/composables/useCategoryTree'
import { CATEGORY_TYPES, categoryTypeLabel } from '@/lib/catalog'

import { categoryOpErrorText } from './catalog-errors'

/** 新建节点时默认的类型：上级的下一级（全宗 → 门类 → 年度 → 保管期限 → 机构 → 自定义）。 */
function nextType(parent: ArchiveCategory | null): string {
  if (!parent) return 'fonds'
  const i = (CATEGORY_TYPES as readonly string[]).indexOf(parent.category_type)
  return CATEGORY_TYPES[Math.min(i + 1, CATEGORY_TYPES.length - 1)] ?? 'custom'
}

export function useCategoryOps(opts: {
  tree: CategoryTreeState
  projectId: () => string | null
  /** 地址栏里当前打开的节点。 */
  currentCategory: () => string | null
  /** 打开一个节点（null：顶层）。 */
  go: (categoryId: string | null, itemId?: string | null, replace?: boolean) => Promise<unknown>
  /** 节点的编码、类型、位置变了，或重新生成了档号：重取件列表和打开的件。 */
  afterChange: () => void
}) {
  const { tree } = opts

  const formOpen = ref(false)
  const formParent = ref<ArchiveCategory | null>(null)
  const formNode = ref<ArchiveCategory | null>(null)
  const formDefaultType = ref('fonds')
  const formFocus = ref<'type' | 'code' | 'name'>('code')

  /** 右键菜单针对的节点（null：在空白处点的）。 */
  const menuNode = ref<ArchiveCategory | null>(null)

  function onTreeContextMenu(ev: MouseEvent) {
    const el = (ev.target as HTMLElement | null)?.closest<HTMLElement>('[data-category-id]')
    const id = el?.dataset.categoryId
    menuNode.value = id ? (tree.nodes.get(id) ?? null) : null
  }

  function askCreate(parent: ArchiveCategory | null) {
    formParent.value = parent
    formNode.value = null
    formDefaultType.value = nextType(parent)
    formFocus.value = 'code'
    formOpen.value = true
  }

  function askEdit(node: ArchiveCategory | null, focus: 'type' | 'code' | 'name' = 'name') {
    if (!node) return
    formParent.value = null
    formNode.value = node
    formFocus.value = focus
    formOpen.value = true
  }

  /** 重算之后的提示：重新生成了几个档号、几个因为撞号为空。 */
  function renumberToast(head: string, renumbered: number, conflicts: number) {
    if (conflicts > 0) {
      toast.warning(`${head}；有 ${conflicts} 件因为撞号档号为空`, {
        description: `${renumbered ? `重新生成了 ${renumbered} 个档号。` : ''}撞号的件档号为空、不能锁定，打开它们可以看到和谁撞了。`,
      })
    } else toast.success(renumbered ? `${head}，重新生成了 ${renumbered} 个档号` : head)
  }

  async function onCategoryCreated(node: ArchiveCategory) {
    toast.success(`已新建「${node.category_name}」（${categoryTypeLabel(node.category_type)} ${node.category_code}）`)
    const parentId = node.parent_id ?? tree.rootId.value
    if (parentId) await tree.load(parentId).catch(() => null)
    await opts.go(node.id)
  }

  /** 节点改了编码、类型、位置之后：刷新树、当前列表和详情（档号可能都变了）。 */
  async function afterCategoryChanged(ids: (string | null | undefined)[]) {
    await tree.refreshFor(ids.filter((x): x is string => !!x))
    opts.afterChange()
  }

  function onCategoryUpdated(node: UpdatedArchiveCategory, before: ArchiveCategory) {
    const what = [
      node.category_name !== before.category_name && '名称',
      node.category_code !== before.category_code && '编码',
      node.category_type !== before.category_type && '类型',
    ].filter(Boolean)
    renumberToast(`已修改「${node.category_name}」的${what.join('、') || '信息'}`, node.renumbered, node.archive_no_conflicts)
    void afterCategoryChanged([node.id, node.parent_id ?? tree.rootId.value])
  }

  // 移动节点
  const movePicker = reactive({ open: false, node: null as ArchiveCategory | null })
  function askMove(node: ArchiveCategory | null) {
    if (!node) return
    movePicker.node = node
    movePicker.open = true
  }
  async function doMove(target: PickedCategory) {
    const pid = opts.projectId()
    const node = movePicker.node
    if (!pid || !node) return
    try {
      const res = await updateArchiveCategory(pid, node.id, { parent_id: target.id }, HANDLE_ALL)
      renumberToast(
        `已把「${node.category_name}」移到「${target.path || '顶层'}」`,
        res.renumbered,
        res.archive_no_conflicts,
      )
      // 先用响应更新树上的这个节点（新的位置），再刷新原来和现在的上级、定位到它。
      await tree.afterMove(res, node.path_code, node.parent_id)
      await tree.reveal(node.id).catch(() => null)
      opts.afterChange()
    } catch (e) {
      if (!handledGlobally(e)) toast.error(categoryOpErrorText(e))
      if (isApiError(e, 'not_found')) void tree.refreshShown()
    }
  }

  // 删除节点
  const confirmDelete = reactive({ open: false, loading: false, node: null as ArchiveCategory | null })
  function askDelete(node: ArchiveCategory | null) {
    if (!node) return
    if (node.item_count > 0) {
      // 有条目的节点 server 会拒绝（409 category_not_empty），不用再确认一次。
      toast.error(`「${node.category_name}」（含下级节点）下还有 ${node.item_count} 件，不能删除`, {
        description: '请先把件移到别的节点，或撤销归档',
      })
      return
    }
    confirmDelete.node = node
    confirmDelete.open = true
  }
  async function doDelete() {
    const pid = opts.projectId()
    const node = confirmDelete.node
    if (!pid || !node || confirmDelete.loading) return
    confirmDelete.loading = true
    try {
      await deleteArchiveCategory(pid, node.id, HANDLE_ALL)
      confirmDelete.open = false
      toast.success(`已删除「${node.category_name}」${node.child_count ? '（含下级节点）' : ''}`)
      const cur = opts.currentCategory()
      const inside = !!cur && (cur === node.id || tree.isUnder(cur, node.id) === true)
      await tree.refreshFor([node.parent_id ?? tree.rootId.value!])
      if (inside) await opts.go(node.parent_id && node.parent_id !== tree.rootId.value ? node.parent_id : null, null, true)
    } catch (e) {
      if (!handledGlobally(e)) toast.error(categoryOpErrorText(e))
      if (e instanceof ApiError && e.status >= 400 && e.status < 500) {
        confirmDelete.open = false
        void tree.refreshShown()
      }
    } finally {
      confirmDelete.loading = false
    }
  }

  // 按新模板重新生成档号
  const confirmRenumber = reactive({ open: false, loading: false, categoryId: null as string | null, label: '' })
  function askRenumber(categoryId: string | null) {
    confirmRenumber.categoryId = categoryId
    confirmRenumber.label = categoryId
      ? `「${tree.nodes.get(categoryId)?.category_name ?? '这个节点'}」（含下级节点）`
      : '整个项目'
    confirmRenumber.open = true
  }
  async function doRenumber() {
    const pid = opts.projectId()
    if (!pid || confirmRenumber.loading) return
    confirmRenumber.loading = true
    try {
      const res = await renumberArchiveItems(pid, confirmRenumber.categoryId, HANDLE_ALL)
      confirmRenumber.open = false
      renumberToast(
        `已按新模板重新生成${confirmRenumber.label}的档号${res.skipped_locked ? `（${res.skipped_locked} 个已锁定的没有动）` : ''}`,
        res.renumbered,
        res.archive_no_conflicts,
      )
      if (!res.renumbered && !res.archive_no_conflicts) toast.info('档号都和现在的模板一致，没有变化')
      opts.afterChange()
    } catch (e) {
      if (!handledGlobally(e)) toast.error(errorMessage(e))
      if (e instanceof ApiError && e.status >= 400 && e.status < 500) confirmRenumber.open = false
    } finally {
      confirmRenumber.loading = false
    }
  }

  return {
    formOpen,
    formParent,
    formNode,
    formDefaultType,
    formFocus,
    menuNode,
    onTreeContextMenu,
    askCreate,
    askEdit,
    onCategoryCreated,
    onCategoryUpdated,
    movePicker,
    askMove,
    doMove,
    confirmDelete,
    askDelete,
    doDelete,
    confirmRenumber,
    askRenumber,
    doRenumber,
  }
}
