<script setup lang="ts">
// 给用户勾选角色（新建用户、分配角色共用）。按防提权规则预先禁用不能勾选或取消的角色，并显示原因。
import { computed } from 'vue'

import type { RoleItem } from '@/api/types'
import { Checkbox } from '@/components/ui/checkbox'
import { assignRoleBlock } from '@/lib/privilege'
import { useSessionStore } from '@/stores/session'

const selected = defineModel<string[]>({ required: true })

const props = defineProps<{
  roles: RoleItem[]
  /** 用户原来的角色（新建用户时为空）。 */
  original: string[]
}>()

const session = useSessionStore()

const rows = computed(() =>
  props.roles.map((role) => ({
    role,
    checked: selected.value.includes(role.id),
    reason: assignRoleBlock(session.operator, role, props.original.includes(role.id)),
  })),
)

function toggle(id: string, on: boolean) {
  selected.value = on ? [...selected.value, id] : selected.value.filter((x) => x !== id)
}
</script>

<template>
  <div class="flex max-h-72 flex-col overflow-y-auto rounded-lg p-1 shadow-outline">
    <label
      v-for="row in rows"
      :key="row.role.id"
      class="flex items-start gap-2.5 rounded-md px-2.5 py-2"
      :class="row.reason ? 'cursor-not-allowed' : 'cursor-pointer hover:bg-muted'"
      :data-role="row.role.role_code"
    >
      <Checkbox
        class="mt-0.5"
        :model-value="row.checked"
        :disabled="!!row.reason"
        @update:model-value="(v) => toggle(row.role.id, v === true)"
      />
      <span class="min-w-0 flex-1">
        <span class="flex items-center gap-2">
          <span class="text-sm text-foreground" :class="row.reason ? 'opacity-60' : ''">
            {{ row.role.role_name }}
          </span>
          <span class="font-mono text-xs text-muted-foreground">{{ row.role.role_code }}</span>
        </span>
        <span v-if="row.reason" class="mt-0.5 block text-xs text-warn">{{ row.reason }}</span>
      </span>
    </label>
    <p v-if="roles.length === 0" class="px-2.5 py-2 text-sm text-muted-foreground">没有角色</p>
  </div>
</template>
