<script setup lang="ts">
// 角色的菜单勾选树：按 GET /api/system/menus 的 parent_code 分组，分组本身不能单独勾选，
// 它的勾选框是"全选/半选"。`locked` 时全部只读；否则按防提权规则逐项禁用。
import { CheckIcon, LockIcon, MinusIcon } from '@lucide/vue'
import { computed } from 'vue'

import type { MenuItem } from '@/api/types'
import { Checkbox } from '@/components/ui/checkbox'
import { roleMenuBlock } from '@/lib/privilege'
import { useSessionStore } from '@/stores/session'

const selected = defineModel<string[]>({ required: true })

const props = defineProps<{
  menus: MenuItem[]
  /** 整棵树只读的原因（例如 admin 角色）；为空时可编辑。 */
  locked?: string | null
}>()

const session = useSessionStore()

interface Row {
  menu: MenuItem
  reason: string | null
}

interface Node {
  item: Row
  children: Row[]
}

const tree = computed<Node[]>(() => {
  const bySort = (a: MenuItem, b: MenuItem) => a.sort_no - b.sort_no
  const row = (menu: MenuItem): Row => ({
    menu,
    reason: props.locked || roleMenuBlock(session.operator, menu.menu_code),
  })
  return props.menus
    .filter((m) => m.parent_code === null)
    .sort(bySort)
    .map((m) => ({
      item: row(m),
      children: props.menus
        .filter((c) => c.parent_code === m.menu_code && c.menu_type === 'menu')
        .sort(bySort)
        .map(row),
    }))
})

const selectedSet = computed(() => new Set(selected.value))

function setChecked(codes: string[], on: boolean) {
  const next = new Set(selected.value)
  for (const c of codes) {
    if (on) next.add(c)
    else next.delete(c)
  }
  // 按菜单顺序输出，方便比较和展示。
  const order = props.menus.map((m) => m.menu_code)
  selected.value = order.filter((c) => next.has(c))
}

function groupState(node: Node): boolean | 'indeterminate' {
  const n = node.children.filter((c) => selectedSet.value.has(c.menu.menu_code)).length
  if (n === 0) return false
  return n === node.children.length ? true : 'indeterminate'
}

function groupDisabled(node: Node): boolean {
  return node.children.length === 0 || node.children.every((c) => c.reason)
}

function toggleGroup(node: Node) {
  const editable = node.children.filter((c) => !c.reason).map((c) => c.menu.menu_code)
  const allOn = editable.every((c) => selectedSet.value.has(c))
  setChecked(editable, !allOn)
}
</script>

<template>
  <div class="rounded-lg p-1.5 shadow-outline" data-testid="menu-tree">
    <template v-for="node in tree" :key="node.item.menu.menu_code">
      <!-- 分组：勾选框表示子菜单全选/半选 -->
      <label
        v-if="node.item.menu.menu_type === 'group'"
        class="flex items-center gap-2.5 rounded-md px-2.5 py-1.5"
        :class="groupDisabled(node) ? 'cursor-not-allowed' : 'cursor-pointer hover:bg-muted'"
      >
        <Checkbox
          :model-value="groupState(node)"
          :disabled="groupDisabled(node)"
          class="data-[state=indeterminate]:border-primary data-[state=indeterminate]:bg-primary data-[state=indeterminate]:text-primary-foreground"
          @update:model-value="toggleGroup(node)"
        >
          <template #default="{ state }">
            <MinusIcon v-if="state === 'indeterminate'" />
            <CheckIcon v-else />
          </template>
        </Checkbox>
        <span class="text-sm font-medium text-foreground">{{ node.item.menu.menu_name }}</span>
        <code class="ml-auto text-xs text-muted-foreground">{{ node.item.menu.menu_code }}</code>
      </label>
      <label
        v-else
        class="flex items-center gap-2.5 rounded-md px-2.5 py-1.5"
        :class="node.item.reason ? 'cursor-not-allowed' : 'cursor-pointer hover:bg-muted'"
        :title="node.item.reason ?? undefined"
        :data-menu="node.item.menu.menu_code"
      >
        <Checkbox
          :model-value="selectedSet.has(node.item.menu.menu_code)"
          :disabled="!!node.item.reason"
          @update:model-value="(v) => setChecked([node.item.menu.menu_code], v === true)"
        />
        <span class="text-sm text-foreground">{{ node.item.menu.menu_name }}</span>
        <code class="ml-auto text-xs text-muted-foreground">{{ node.item.menu.menu_code }}</code>
      </label>

      <label
        v-for="child in node.children"
        :key="child.menu.menu_code"
        class="flex items-center gap-2.5 rounded-md py-1.5 pr-2.5 pl-9"
        :class="child.reason ? 'cursor-not-allowed' : 'cursor-pointer hover:bg-muted'"
        :title="child.reason ?? undefined"
        :data-menu="child.menu.menu_code"
      >
        <Checkbox
          :model-value="selectedSet.has(child.menu.menu_code)"
          :disabled="!!child.reason"
          @update:model-value="(v) => setChecked([child.menu.menu_code], v === true)"
        />
        <span class="text-sm text-foreground">{{ child.menu.menu_name }}</span>
        <LockIcon v-if="child.reason && !locked" class="size-3.5 text-muted-foreground" />
        <code class="ml-auto text-xs text-muted-foreground">{{ child.menu.menu_code }}</code>
      </label>
    </template>
  </div>
</template>
