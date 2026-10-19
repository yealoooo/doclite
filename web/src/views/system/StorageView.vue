<script setup lang="ts">
// 系统管理 · 存储配置（参照 plan.html §4 "存储配置" Mockup）：左边存储列表，右边新增或编辑的表单。
// - 列表每项：名称、默认标记、状态、地址、桶、前缀、是否有文件；可以测试连接、编辑、设为默认、改状态、删除。
// - 状态规则（和后端一致，按钮预先禁用并说明原因，后端仍会返回 409）：只有可读写的存储能设为默认；
//   默认存储不能改只读、停用、删除；有文件的存储不能停用（可以改只读）；被引用的存储不能删除。
// - 保存的 Secret Key 用当前 MASTER_KEY 解不开时（secret_readable = false），醒目提示重新填写密钥。
// - 右侧表单有未保存的修改时，切换编辑对象、改为新增都先确认"放弃修改"（同角色与权限页）。
// - 卡片上的测试结论只对测试时的那版配置有效：列表刷新后这条配置的 updated_at 变了（保存、改状态、设默认），
//   或者被删掉了，就清掉它的测试结论；测试期间配置变了，回来的结果直接丢弃。
// - 存储迁移（M7-2）：有文件的存储可以"迁移到…"别的可读写存储（MigrateDialog）；源存储卡片上显示最近一次迁移的进度和
//   结果（MigrationPanel，可以取消、看失败清单 MigrationDetailDialog），目标存储卡片上显示"正在迁入"。有进行中的迁移时
//   每 2 秒重取一次列表（列表里带迁移的摘要），都结束了就停。
import {
  ArrowRightLeftIcon,
  HardDriveIcon,
  KeyRoundIcon,
  PencilIcon,
  PlugZapIcon,
  PlusIcon,
  StarIcon,
  TriangleAlertIcon,
} from '@lucide/vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, useTemplateRef, watch } from 'vue'
import { toast } from 'vue-sonner'

import { errorMessage } from '@/api/http'
import {
  cancelStorageMigration,
  deleteStorageProfile,
  listStorageProfiles,
  setDefaultStorage,
  STORAGE_CONFLICT_CODES,
  testStorageProfile,
  updateStorageProfile,
} from '@/api/storage'
import type { ConnectionTestResult, StorageMigration, StorageProfileItem, StorageStatus } from '@/api/types'
import ConfirmDialog from '@/components/app/ConfirmDialog.vue'
import GuardedButton from '@/components/app/GuardedButton.vue'
import PageHeader from '@/components/app/PageHeader.vue'
import { StatusBadge } from '@/components/ui/status-badge'
import { Button } from '@/components/ui/button'
import {
  conflictTitle,
  deleteBlock,
  migrateBlock,
  migrationActive,
  migrationPercent,
  setDefaultBlock,
  STATUS_LABEL,
  STORAGE_STATUS,
  statusBlock,
} from '@/lib/storage'

import MigrateDialog from './storage/MigrateDialog.vue'
import MigrationDetailDialog from './storage/MigrationDetailDialog.vue'
import MigrationPanel from './storage/MigrationPanel.vue'
import StorageForm from './storage/StorageForm.vue'
import TestResultPanel from './storage/TestResultPanel.vue'

const profiles = ref<StorageProfileItem[]>([])
const formRef = useTemplateRef<InstanceType<typeof StorageForm>>('form')
const loaded = ref(false)
const listError = ref('')
/** 右侧表单正在编辑的存储；null 表示新增。 */
const editingId = ref<string | null>(null)
const editing = computed(() => profiles.value.find((p) => p.id === editingId.value) ?? null)
const hasDefault = computed(() => profiles.value.some((p) => p.is_default))

async function load() {
  try {
    profiles.value = await listStorageProfiles()
    listError.value = ''
    dropStaleCardTests()
    if (editingId.value && !editing.value) editingId.value = null
  } catch (err) {
    listError.value = errorMessage(err)
  } finally {
    loaded.value = true
  }
}

onMounted(load)

/** 状态规则的 409 用中文标题 + 后端的说明提示，其他错误由请求封装提示。 */
function showConflict(err: unknown) {
  const title = conflictTitle(err)
  if (title) toast.error(title, { description: errorMessage(err) })
}

// ---------- 列表上的测试连接 ----------

interface CardTest {
  /** 哪一次测试（同一张卡片再测一次时，旧的结果作废）。 */
  token: number
  /** 测试时这条配置的 updated_at：配置改了，结论就不再适用。 */
  updatedAt: string
  testing: boolean
  result: ConnectionTestResult | null
}

const cardTests = reactive<Record<string, CardTest>>({})
let cardTestToken = 0

/** 这次测试的结论还适用：还是最近一次测试，而且配置没变。 */
function cardTestCurrent(id: string, token: number): CardTest | null {
  const entry = cardTests[id]
  const p = profiles.value.find((x) => x.id === id)
  return entry && entry.token === token && p?.updated_at === entry.updatedAt ? entry : null
}

async function testCard(p: StorageProfileItem) {
  const token = ++cardTestToken
  cardTests[p.id] = { token, updatedAt: p.updated_at, testing: true, result: null }
  try {
    const result = await testStorageProfile(p.id)
    const entry = cardTestCurrent(p.id, token)
    if (entry) {
      entry.testing = false
      entry.result = result
    } else if (cardTests[p.id]?.token === token) {
      delete cardTests[p.id]
    }
  } catch {
    // 错误已由请求封装提示。
    if (cardTests[p.id]?.token === token) delete cardTests[p.id]
  }
}

/** 列表刷新后：配置变了（updated_at 不同）或已删除的存储，清掉它的测试结论。 */
function dropStaleCardTests() {
  for (const [id, entry] of Object.entries(cardTests)) {
    const p = profiles.value.find((x) => x.id === id)
    if (!p || p.updated_at !== entry.updatedAt) delete cardTests[id]
  }
}

// ---------- 切换编辑对象（有未保存的修改时先确认） ----------

const discardOpen = ref(false)
let pendingAction: (() => void) | null = null

/** 表单有未保存的修改时先确认放弃，确认后（或本来就没有修改时）执行 `action`。 */
function guardDirty(action: () => void) {
  if (!formRef.value?.isDirty()) {
    action()
    return
  }
  pendingAction = action
  discardOpen.value = true
}

function confirmDiscard() {
  discardOpen.value = false
  const action = pendingAction
  pendingAction = null
  action?.()
}

// 用取消关掉确认框：放弃这次切换。
watch(discardOpen, (open) => {
  if (!open) pendingAction = null
})

function editProfile(p: StorageProfileItem) {
  if (p.id === editingId.value) return
  guardDirty(() => {
    editingId.value = p.id
  })
}

function startCreate() {
  if (editingId.value === null) return
  guardDirty(() => {
    editingId.value = null
  })
}

// ---------- 设默认、改状态、删除 ----------

const busyId = ref<string | null>(null)

async function makeDefault(p: StorageProfileItem) {
  busyId.value = p.id
  try {
    await setDefaultStorage(p.id, STORAGE_CONFLICT_CODES)
    toast.success(`已把「${p.profile_name}」设为默认存储，之后新上传的文件存到这里`)
  } catch (err) {
    showConflict(err)
  } finally {
    busyId.value = null
    await load()
  }
}

async function changeStatus(p: StorageProfileItem, status: StorageStatus) {
  busyId.value = p.id
  try {
    await updateStorageProfile(p.id, { status }, STORAGE_CONFLICT_CODES)
    toast.success(`「${p.profile_name}」已改为${STATUS_LABEL[status]}`)
    return true
  } catch (err) {
    showConflict(err)
    return false
  } finally {
    busyId.value = null
    await load()
  }
}

const disableTarget = ref<StorageProfileItem | null>(null)
const disableOpen = ref(false)
const deleteTarget = ref<StorageProfileItem | null>(null)
const deleteOpen = ref(false)
const deleting = ref(false)

function openDisable(p: StorageProfileItem) {
  disableTarget.value = p
  disableOpen.value = true
}

async function confirmDisable() {
  if (!disableTarget.value) return
  if (await changeStatus(disableTarget.value, 'disabled')) disableOpen.value = false
}

function openDelete(p: StorageProfileItem) {
  deleteTarget.value = p
  deleteOpen.value = true
}

async function confirmDelete() {
  const p = deleteTarget.value
  if (!p) return
  deleting.value = true
  try {
    await deleteStorageProfile(p.id, STORAGE_CONFLICT_CODES)
    toast.success(`已删除「${p.profile_name}」`)
    deleteOpen.value = false
    if (editingId.value === p.id) editingId.value = null
  } catch (err) {
    showConflict(err)
  } finally {
    deleting.value = false
    await load()
  }
}

// ---------- 表单 ----------

async function onSaved(p: StorageProfileItem, created: boolean) {
  toast.success(created ? `已创建「${p.profile_name}」` : `已保存「${p.profile_name}」`)
  await load()
  // 新建完接着编辑它（保存期间用户切到了别的存储时不跳回来）。
  if (created && editingId.value === null) editingId.value = p.id
}

// ---------- 存储迁移（M7-2） ----------

const migrateSource = ref<StorageProfileItem | null>(null)
const migrateOpen = ref(false)
const detailSource = ref<StorageProfileItem | null>(null)
const detailOpen = ref(false)
const cancelSource = ref<StorageProfileItem | null>(null)
const cancelOpen = ref(false)
const canceling = ref(false)

function openMigrate(p: StorageProfileItem) {
  migrateSource.value = p
  migrateOpen.value = true
}

async function onMigrationStarted(m: StorageMigration) {
  toast.success(`已开始把「${m.source_profile_name}」里的文件迁到「${m.target_profile_name}」`, {
    description: `共 ${m.total.toLocaleString()} 个快照，在后台进行，可以离开这个页面。`,
  })
  await load()
}

function openDetail(p: StorageProfileItem) {
  detailSource.value = p
  detailOpen.value = true
}

function openCancel(p: StorageProfileItem) {
  cancelSource.value = p
  cancelOpen.value = true
}

async function confirmCancel() {
  const p = cancelSource.value
  if (!p) return
  canceling.value = true
  try {
    await cancelStorageMigration(p.id, STORAGE_CONFLICT_CODES)
    toast.success(`已取消「${p.profile_name}」的迁移`, { description: '已经迁过去的不退回；之后可以再迁剩下的。' })
    cancelOpen.value = false
  } catch (err) {
    showConflict(err)
  } finally {
    canceling.value = false
    await load()
  }
}

/** 有进行中的迁移（迁出或迁入）时每 2 秒重取一次列表。 */
const anyMigrationActive = computed(() =>
  profiles.value.some((p) => migrationActive(p.migration) || migrationActive(p.incoming_migration)),
)
let pollTimer: ReturnType<typeof setTimeout> | null = null
watch(profiles, () => {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = null
  if (anyMigrationActive.value) pollTimer = setTimeout(() => void load(), 2000)
})
onBeforeUnmount(() => {
  if (pollTimer) clearTimeout(pollTimer)
})

</script>

<template>
  <PageHeader>
    <template #meta>
      <span class="rounded-md bg-secondary px-2 py-0.5 text-xs text-muted-foreground">S3 兼容存储</span>
    </template>
    <template #actions>
      <Button size="sm" data-testid="storage-create" @click="startCreate"><PlusIcon />新增存储</Button>
    </template>
  </PageHeader>

  <div class="grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)_420px]">
    <!-- 存储列表 -->
    <section class="flex min-h-0 flex-col gap-3 overflow-y-auto px-6 py-5">
      <div
        v-if="loaded && !listError && !hasDefault"
        class="flex items-start gap-2 rounded-lg bg-warn-soft px-4 py-3 text-sm text-foreground"
        data-testid="no-default"
      >
        <TriangleAlertIcon class="mt-0.5 size-4 shrink-0 text-warn" />
        还没有默认存储：新上传的文件没有地方存。请新增一个存储，测试通过后设为默认。
      </div>
      <div
        v-if="listError"
        class="flex items-center gap-3 rounded-lg bg-destructive-soft px-4 py-3 text-sm text-destructive"
        role="alert"
      >
        <p class="flex-1">存储列表加载失败：{{ listError }}</p>
        <Button size="sm" variant="outline" @click="load">重试</Button>
      </div>

      <article
        v-for="p in profiles"
        :key="p.id"
        class="flex flex-col gap-3 rounded-lg px-4 py-3.5"
        :class="[
          p.id === editingId ? 'shadow-[0_0_0_2px_var(--color-ring)]' : 'shadow-outline',
          p.is_default ? 'bg-muted/40' : '',
        ]"
        data-testid="storage-card"
        :data-profile-name="p.profile_name"
      >
        <div class="flex items-start gap-3">
          <HardDriveIcon class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
          <div class="flex min-w-0 flex-1 flex-col gap-1">
            <div class="flex flex-wrap items-center gap-1.5">
              <span class="font-semibold text-foreground">{{ p.profile_name }}</span>
              <span
                v-if="p.is_default"
                class="inline-flex items-center gap-1 rounded-md bg-primary px-1.5 py-0.5 text-2xs leading-none text-primary-foreground"
                data-testid="default-badge"
              ><StarIcon class="size-3" />默认</span>
              <StatusBadge :status="STORAGE_STATUS[p.status]" />
              <span class="rounded-md bg-secondary px-1.5 py-0.5 text-2xs leading-none text-muted-foreground">
                {{ p.in_use ? '有文件存在这里' : '还没有文件' }}
              </span>
            </div>
            <p class="text-xs break-all text-muted-foreground">
              {{ p.storage_type }} · bucket <code class="text-foreground">{{ p.bucket }}</code> · 前缀
              <code class="text-foreground">{{ p.path_prefix || '（无）' }}</code> · server 用
              <code class="text-foreground">{{ p.endpoint }}</code> · 工作站用
              <code v-if="p.public_endpoint" class="text-foreground">{{ p.public_endpoint }}</code>
              <span v-else>同 server 地址</span>
            </p>
            <p class="text-xs text-muted-foreground">
              region {{ p.region }} · {{ p.force_path_style ? 'path-style' : '虚拟主机方式' }} · 预签名 URL 有效期
              {{ p.url_expire_hours }} 小时 · Access Key <code>{{ p.access_key }}</code>
            </p>
            <p v-if="p.remark" class="text-xs text-muted-foreground">备注：{{ p.remark }}</p>
          </div>
        </div>

        <p
          v-if="!p.secret_readable"
          class="flex items-start gap-2 rounded-md bg-destructive-soft px-3 py-2 text-sm font-medium text-destructive"
          role="alert"
          data-testid="secret-unreadable"
        >
          <KeyRoundIcon class="mt-0.5 size-4 shrink-0" />
          主密钥不匹配，请重新填写密钥：保存的 Secret Key 用当前的 MASTER_KEY 解不开，这个存储现在用不了。
          点"编辑"重新填写 Secret Key 后保存（或恢复原来的 MASTER_KEY）。
        </p>

        <MigrationPanel
          v-if="p.migration"
          :migration="p.migration"
          :source-in-use="p.in_use"
          :canceling="canceling && cancelSource?.id === p.id"
          @cancel="openCancel(p)"
          @details="openDetail(p)"
        />
        <p
          v-if="p.incoming_migration"
          class="flex items-center gap-2 rounded-md bg-info-soft px-3 py-2 text-xs text-foreground"
          data-testid="storage-incoming-migration"
          :data-source="p.incoming_migration.source_profile_name"
        >
          <ArrowRightLeftIcon class="size-3.5 shrink-0 text-info" />
          正在从「{{ p.incoming_migration.source_profile_name }}」迁入：已迁
          {{ p.incoming_migration.migrated.toLocaleString() }} / {{ p.incoming_migration.total.toLocaleString() }} 个快照（{{
            migrationPercent(p.incoming_migration)
          }}%）。迁移期间不能改为只读、停用或删除。
        </p>

        <TestResultPanel
          v-if="cardTests[p.id]"
          :result="cardTests[p.id]?.result ?? null"
          :testing="cardTests[p.id]?.testing ?? false"
        />

        <div class="flex flex-wrap items-center gap-1">
          <Button
            size="xs"
            variant="outline"
            :disabled="cardTests[p.id]?.testing"
            data-testid="card-test"
            @click="testCard(p)"
          ><PlugZapIcon />测试连接</Button>
          <Button size="xs" variant="ghost" data-testid="storage-edit" @click="editProfile(p)">
            <PencilIcon />编辑
          </Button>
          <GuardedButton
            v-if="!p.is_default"
            size="xs"
            variant="ghost"
            :reason="setDefaultBlock(p)"
            :disabled="busyId === p.id"
            data-testid="storage-set-default"
            @click="makeDefault(p)"
          >设为默认</GuardedButton>
          <GuardedButton
            v-if="p.in_use || p.migration"
            size="xs"
            variant="ghost"
            :reason="migrateBlock(p, profiles)"
            :disabled="busyId === p.id"
            data-testid="storage-migrate"
            @click="openMigrate(p)"
          ><ArrowRightLeftIcon />迁移到…</GuardedButton>
          <div class="flex-1" />
          <GuardedButton
            v-if="p.status !== 'active'"
            size="xs"
            variant="ghost"
            :reason="statusBlock(p, 'active')"
            :disabled="busyId === p.id"
            @click="changeStatus(p, 'active')"
          >启用</GuardedButton>
          <GuardedButton
            v-if="p.status !== 'readonly'"
            size="xs"
            variant="ghost"
            :reason="statusBlock(p, 'readonly')"
            :disabled="busyId === p.id"
            @click="changeStatus(p, 'readonly')"
          >改为只读</GuardedButton>
          <GuardedButton
            v-if="p.status !== 'disabled'"
            size="xs"
            variant="ghost"
            class="text-destructive hover:text-destructive"
            :reason="statusBlock(p, 'disabled')"
            :disabled="busyId === p.id"
            @click="openDisable(p)"
          >停用</GuardedButton>
          <GuardedButton
            size="xs"
            variant="ghost"
            class="text-destructive hover:text-destructive"
            :reason="deleteBlock(p)"
            :disabled="busyId === p.id"
            data-testid="storage-delete"
            @click="openDelete(p)"
          >删除</GuardedButton>
        </div>
      </article>

      <p v-if="loaded && !listError && profiles.length === 0" class="py-10 text-center text-sm text-muted-foreground">
        还没有存储配置
      </p>
      <p v-if="!loaded" class="py-10 text-center text-sm text-muted-foreground">加载中…</p>

      <div class="rounded-lg bg-info-soft px-4 py-3 text-xs leading-relaxed text-foreground">
        设为默认只影响<strong>新写入</strong>的文件；已有的快照照样按自己记录的存储读取。
        要把旧存储里的文件搬到新存储，用"迁移到…"：后台复制原图和封面、按 sha256 核对一致后才改快照的位置，迁完的旧存储可以停用
        （迁移不删除旧存储里的对象）。
        工作站看图直接访问 S3：server 先检查权限，再用"工作站访问地址"签发预签名 URL，S3 的密钥不会下发到工作站。
        Secret Key 用 MASTER_KEY 加密后保存，页面上不会显示。
      </div>
    </section>

    <!-- 新增 / 编辑 -->
    <aside class="min-h-0 overflow-y-auto border-l px-5 py-5">
      <StorageForm ref="form" :profile="editing" @saved="onSaved" @cancel="startCreate" />
    </aside>
  </div>

  <ConfirmDialog
    v-model:open="discardOpen"
    title="放弃未保存的修改？"
    :description="`右侧表单${editing ? `（${editing.profile_name}）` : '（新增存储）'}里的修改还没有保存，继续会丢失这些修改。`"
    confirm-text="放弃修改"
    destructive
    @confirm="confirmDiscard"
  />
  <ConfirmDialog
    v-model:open="disableOpen"
    title="停用存储"
    :description="`停用「${disableTarget?.profile_name}」后它不再使用。它里面没有文件，之后可以重新启用。`"
    confirm-text="停用"
    destructive
    :loading="busyId !== null"
    @confirm="confirmDisable"
  />
  <MigrateDialog v-model:open="migrateOpen" :source="migrateSource" :profiles="profiles" @started="onMigrationStarted" />
  <MigrationDetailDialog v-model:open="detailOpen" :source="detailSource" />
  <ConfirmDialog
    v-model:open="cancelOpen"
    title="取消迁移"
    :description="`停下「${cancelSource?.profile_name}」的迁移。已经迁过去的不退回（它们已经在新存储里，照常能用）；之后可以再发起迁移，接着迁剩下的。`"
    confirm-text="取消迁移"
    destructive
    :loading="canceling"
    @confirm="confirmCancel"
  />
  <ConfirmDialog
    v-model:open="deleteOpen"
    title="删除存储配置"
    :description="`删除「${deleteTarget?.profile_name}」这条配置（不会删除存储上的对象）。它没有被任何文件或导出任务使用。这个操作不能撤销。`"
    confirm-text="删除"
    destructive
    :loading="deleting"
    @confirm="confirmDelete"
  />
</template>
