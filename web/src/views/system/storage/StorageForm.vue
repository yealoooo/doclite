<script setup lang="ts">
// 存储配置页右侧的表单：新增存储，或编辑选中的存储（参照 plan.html §4 "存储配置" Mockup）。
// - Secret Key 只写不读：编辑时留空表示不修改；保存的密钥用当前 MASTER_KEY 解不开时醒目提示重新填写。
// - "测试连接"测的是表单上的内容：新增时调 `POST …/test`；编辑时调 `POST …/{id}/test`，带上还没保存的修改
//   （Secret Key 留空时用保存的）。测试不会保存任何东西。
// - 接口错误（校验失败等）显示在表单里。
// - 表单只在"编辑对象换了"时重新载入。同一条配置被别处改了（列表刷新后 updated_at 变了）：
//   没有未保存的修改就直接载入新数据；有未保存的修改时不覆盖，提示"已被修改"，由用户选择放弃修改重新载入
//   或继续编辑（继续编辑时保存只提交自己改过的字段，不会把别处的修改改回去）。
// - 切换编辑对象、改为新增之前，页面通过 `isDirty()` 判断要不要先确认放弃修改。
import { KeyRoundIcon, PlugZapIcon, TriangleAlertIcon } from '@lucide/vue'
import { computed, nextTick, reactive, ref, useTemplateRef, watch } from 'vue'

import { errorMessage } from '@/api/http'
import {
  createStorageProfile,
  testNewStorage,
  testStorageProfile,
  updateStorageProfile,
  type StorageConnectionBody,
  type UpdateStorageBody,
} from '@/api/storage'
import type { ConnectionTestResult, StorageProfileItem } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  checkBucket,
  checkEndpoint,
  checkKey,
  checkPathPrefix,
  checkProfileName,
  checkRegion,
  checkRemark,
  checkUrlExpireHours,
} from '@/lib/storage'

import TestResultPanel from './TestResultPanel.vue'

const props = defineProps<{
  /** 编辑的存储；为 null 时是新增。 */
  profile: StorageProfileItem | null
}>()

const emit = defineEmits<{
  saved: [profile: StorageProfileItem, created: boolean]
  cancel: []
}>()

/** 表单里可编辑的值（不含 Secret Key）。 */
interface Values {
  profileName: string
  endpoint: string
  publicEndpoint: string
  bucket: string
  pathPrefix: string
  region: string
  urlExpireHours: number
  forcePathStyle: boolean
  accessKey: string
  remark: string
}

function valuesOf(p: StorageProfileItem | null): Values {
  return {
    profileName: p?.profile_name ?? '',
    endpoint: p?.endpoint ?? '',
    publicEndpoint: p?.public_endpoint ?? '',
    bucket: p?.bucket ?? '',
    pathPrefix: p?.path_prefix ?? 'doclite/',
    region: p?.region ?? 'us-east-1',
    urlExpireHours: p?.url_expire_hours ?? 24,
    forcePathStyle: p?.force_path_style ?? true,
    accessKey: p?.access_key ?? '',
    remark: p?.remark ?? '',
  }
}

const form = reactive({ ...valuesOf(null), urlExpireHours: 24 as number | string, secretKey: '' })
/** 表单载入时的值：判断有没有未保存的修改、编辑时算出改了哪些字段，都和它比。 */
const baseline = ref<Values>(valuesOf(null))
/** 载入的是哪条配置（新增时为 null）、载入时它的 updated_at。 */
const loadedId = ref<string | null>(null)
const loadedAt = ref<string | null>(null)
/** 用户看过"已被修改"的提示、选择继续编辑时，那条配置的 updated_at。 */
const acknowledgedAt = ref<string | null>(null)

const error = ref('')
const saving = ref(false)
const testing = ref(false)
const testResult = ref<ConnectionTestResult | null>(null)
/** 测试的代数：换了编辑对象（重新载入）时加一，之前发起的测试回来后发现代数不对就丢弃结果。 */
let testSeq = 0

const isEdit = computed(() => props.profile !== null)
/** 测试结果在表单最下面，可能在可视区域外：测试开始和出结果时滚到它。 */
const resultBox = useTemplateRef<HTMLElement>('resultBox')

async function revealResult() {
  await nextTick()
  resultBox.value?.scrollIntoView({ block: 'nearest' })
}

/** 有没有未保存的修改（和载入时比；填了 Secret Key 也算）。 */
const dirty = computed(() => {
  const b = baseline.value
  return (
    form.profileName.trim() !== b.profileName ||
    form.endpoint.trim() !== b.endpoint ||
    form.publicEndpoint.trim() !== b.publicEndpoint ||
    form.bucket.trim() !== b.bucket ||
    form.pathPrefix.trim() !== b.pathPrefix ||
    form.region.trim() !== b.region ||
    Number(form.urlExpireHours) !== b.urlExpireHours ||
    form.forcePathStyle !== b.forcePathStyle ||
    form.accessKey.trim() !== b.accessKey ||
    form.remark.trim() !== b.remark ||
    form.secretKey !== ''
  )
})

/** 同一条配置在载入之后被别处改了，而表单里有未保存的修改（用户还没选择继续编辑）。 */
const stale = computed(
  () =>
    !!props.profile &&
    props.profile.id === loadedId.value &&
    props.profile.updated_at !== loadedAt.value &&
    props.profile.updated_at !== acknowledgedAt.value &&
    dirty.value,
)

/** 把表单换成 `p` 的内容（null 为新增的默认值），作废正在进行的测试。 */
function loadFrom(p: StorageProfileItem | null) {
  const values = valuesOf(p)
  Object.assign(form, values, { secretKey: '' })
  baseline.value = values
  loadedId.value = p?.id ?? null
  loadedAt.value = p?.updated_at ?? null
  acknowledgedAt.value = null
  error.value = ''
  testSeq++
  testing.value = false
  testResult.value = null
}

watch(
  () => props.profile,
  (p) => {
    // 换了编辑对象（包括新增 ↔ 编辑）：重新载入。
    if ((p?.id ?? null) !== loadedId.value) {
      loadFrom(p)
      return
    }
    // 同一条配置被别处改了：没有未保存的修改就直接载入新数据；有的话由 `stale` 提示用户选择。
    if (p && p.updated_at !== loadedAt.value && !dirty.value) loadFrom(p)
  },
  { immediate: true },
)

// 用户把修改都改回去了（不再有未保存的修改），而这条配置已被别处改过：载入新数据。
watch(dirty, (d) => {
  const p = props.profile
  if (!d && p && p.id === loadedId.value && p.updated_at !== loadedAt.value) loadFrom(p)
})

function reloadDiscardingChanges() {
  loadFrom(props.profile)
}

function keepEditing() {
  acknowledgedAt.value = props.profile?.updated_at ?? null
}

/** 连接参数的校验（测试和保存都要）。新增时 Access Key、Secret Key 必填；编辑时 Secret Key 可以留空。 */
function validateConnection(): string {
  return (
    checkEndpoint(form.endpoint, 'server 访问地址', true) ||
    checkEndpoint(form.publicEndpoint, '工作站访问地址', false) ||
    checkBucket(form.bucket) ||
    checkPathPrefix(form.pathPrefix) ||
    checkRegion(form.region) ||
    checkKey(form.accessKey, 'Access Key', true) ||
    checkKey(form.secretKey, 'Secret Key', !isEdit.value)
  )
}

function validateAll(): string {
  return (
    checkProfileName(form.profileName) ||
    validateConnection() ||
    checkUrlExpireHours(form.urlExpireHours) ||
    checkRemark(form.remark)
  )
}

/** 表单上的连接参数（Secret Key 留空时不带）。 */
function connectionFields(): UpdateStorageBody {
  const body: UpdateStorageBody = {
    endpoint: form.endpoint.trim(),
    public_endpoint: form.publicEndpoint.trim(),
    bucket: form.bucket.trim(),
    region: form.region.trim(),
    path_prefix: form.pathPrefix.trim(),
    access_key: form.accessKey.trim(),
    force_path_style: form.forcePathStyle,
  }
  if (form.secretKey.trim()) body.secret_key = form.secretKey.trim()
  return body
}

async function runTest() {
  error.value = validateConnection()
  if (error.value) return
  // 结果和发起时的编辑对象绑定：中途换了编辑对象（loadFrom 让代数加一），回来的结果直接丢弃。
  const seq = ++testSeq
  testing.value = true
  testResult.value = null
  void revealResult()
  try {
    const fields = connectionFields()
    const result = props.profile
      ? await testStorageProfile(props.profile.id, fields, true)
      : await testNewStorage(fields as StorageConnectionBody, true)
    if (seq !== testSeq) return
    testResult.value = result
  } catch (err) {
    if (seq !== testSeq) return
    error.value = errorMessage(err)
  } finally {
    if (seq === testSeq) {
      testing.value = false
      void revealResult()
    }
  }
}

/** 编辑时只发自己改过的字段（和载入时比），不会把别处的修改改回去。 */
function changedFields(): UpdateStorageBody {
  const b = baseline.value
  const fields = connectionFields()
  const body: UpdateStorageBody = {}
  if (form.profileName.trim() !== b.profileName) body.profile_name = form.profileName.trim()
  if (fields.endpoint !== b.endpoint) body.endpoint = fields.endpoint
  if (fields.public_endpoint !== b.publicEndpoint) body.public_endpoint = fields.public_endpoint
  if (fields.bucket !== b.bucket) body.bucket = fields.bucket
  if (fields.region !== b.region) body.region = fields.region
  if (fields.path_prefix !== b.pathPrefix) body.path_prefix = fields.path_prefix
  if (fields.access_key !== b.accessKey) body.access_key = fields.access_key
  if (fields.force_path_style !== b.forcePathStyle) body.force_path_style = fields.force_path_style
  if (Number(form.urlExpireHours) !== b.urlExpireHours) body.url_expire_hours = Number(form.urlExpireHours)
  if (form.remark.trim() !== b.remark) body.remark = form.remark.trim()
  if (fields.secret_key) body.secret_key = fields.secret_key
  return body
}

async function submit() {
  error.value = validateAll()
  if (error.value) return
  const startedFor = loadedId.value
  saving.value = true
  try {
    if (props.profile) {
      const body = changedFields()
      const saved = Object.keys(body).length
        ? await updateStorageProfile(props.profile.id, body, true)
        : props.profile
      // 保存期间换了编辑对象时不动现在的表单。
      if (loadedId.value === startedFor) loadFrom(saved)
      emit('saved', saved, false)
    } else {
      const fields = connectionFields()
      const saved = await createStorageProfile(
        {
          ...(fields as StorageConnectionBody),
          profile_name: form.profileName.trim(),
          url_expire_hours: Number(form.urlExpireHours),
          remark: form.remark.trim() || undefined,
        },
        true,
      )
      if (loadedId.value === startedFor) loadFrom(null)
      emit('saved', saved, true)
    }
  } catch (err) {
    if (loadedId.value === startedFor) error.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}

defineExpose({
  /** 有没有未保存的修改（切换编辑对象、改为新增之前要先确认）。 */
  isDirty: () => dirty.value,
})
</script>

<template>
  <form class="flex flex-col gap-3.5" data-testid="storage-form" @submit.prevent="submit">
    <div class="flex items-center gap-2">
      <h2 class="text-sm font-semibold text-foreground">
        {{ isEdit ? `编辑：${profile?.profile_name}` : '新增存储（S3 兼容）' }}
      </h2>
      <div class="flex-1" />
      <Button v-if="isEdit" type="button" size="xs" variant="ghost" @click="emit('cancel')">改为新增</Button>
    </div>

    <div
      v-if="stale"
      class="flex flex-col gap-2 rounded-md bg-warn-soft px-3 py-2.5 text-sm text-foreground"
      role="alert"
      data-testid="form-stale"
    >
      <p class="flex items-start gap-2">
        <TriangleAlertIcon class="mt-0.5 size-4 shrink-0 text-warn" />
        这条配置已被修改（例如刚被设为默认、改了状态，或者别人保存过），你在表单里的修改还没有保存。
      </p>
      <div class="flex gap-2 pl-6">
        <Button type="button" size="xs" variant="outline" @click="reloadDiscardingChanges">放弃修改，重新载入</Button>
        <Button type="button" size="xs" variant="ghost" @click="keepEditing">继续编辑</Button>
      </div>
      <p class="pl-6 text-xs text-muted-foreground">继续编辑时，保存只提交你改过的字段。</p>
    </div>

    <p
      v-if="profile && !profile.secret_readable"
      class="flex items-start gap-2 rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive"
      role="alert"
      data-testid="form-secret-unreadable"
    >
      <KeyRoundIcon class="mt-0.5 size-4 shrink-0" />
      主密钥不匹配：保存的 Secret Key 用当前的 MASTER_KEY 解不开。请重新填写 Secret Key 后保存（或恢复原来的 MASTER_KEY）。
    </p>

    <div class="grid gap-1.5">
      <Label for="sf-name">名称</Label>
      <Input id="sf-name" v-model="form.profileName" autocomplete="off" placeholder="例如 客户对象存储" />
    </div>
    <div class="grid gap-1.5">
      <Label for="sf-endpoint">server 访问地址</Label>
      <Input id="sf-endpoint" v-model="form.endpoint" autocomplete="off" placeholder="http://objectstore:8333" />
    </div>
    <div class="grid gap-1.5">
      <Label for="sf-public">工作站访问地址</Label>
      <Input id="sf-public" v-model="form.publicEndpoint" autocomplete="off" placeholder="为空时与 server 访问地址相同" />
      <p class="text-xs text-muted-foreground">工作站看图用它；server 在 Docker 里时填服务器的局域网地址。</p>
    </div>
    <div class="grid grid-cols-2 gap-3">
      <div class="grid gap-1.5">
        <Label for="sf-bucket">Bucket</Label>
        <Input id="sf-bucket" v-model="form.bucket" autocomplete="off" placeholder="doclite" />
      </div>
      <div class="grid gap-1.5">
        <Label for="sf-prefix">统一前缀</Label>
        <Input id="sf-prefix" v-model="form.pathPrefix" autocomplete="off" placeholder="为空表示不用前缀" />
      </div>
    </div>
    <div class="grid grid-cols-2 gap-3">
      <div class="grid gap-1.5">
        <Label for="sf-region">Region</Label>
        <Input id="sf-region" v-model="form.region" autocomplete="off" />
      </div>
      <div class="grid gap-1.5">
        <Label for="sf-expire">URL 有效期（小时）</Label>
        <Input id="sf-expire" v-model="form.urlExpireHours" type="number" min="1" max="168" />
      </div>
    </div>
    <label
      class="flex items-center gap-2 text-sm"
      title="SeaweedFS、MinIO 等自建存储一般要勾选；要求虚拟主机方式（bucket.host）的云存储取消勾选"
    >
      <Checkbox
        id="sf-path-style"
        :model-value="form.forcePathStyle"
        @update:model-value="(v) => (form.forcePathStyle = v === true)"
      />
      Path-style 访问<span class="text-xs text-muted-foreground">（自建存储一般要勾选）</span>
    </label>
    <div class="grid grid-cols-2 gap-3">
      <div class="grid gap-1.5">
        <Label for="sf-access">Access Key</Label>
        <Input id="sf-access" v-model="form.accessKey" autocomplete="off" />
      </div>
      <div class="grid gap-1.5">
        <Label for="sf-secret">Secret Key（加密存储）</Label>
        <Input
          id="sf-secret"
          v-model="form.secretKey"
          type="password"
          autocomplete="new-password"
          :placeholder="isEdit ? '留空表示不修改' : ''"
        />
      </div>
    </div>
    <p v-if="isEdit" class="-mt-2 text-xs text-muted-foreground">保存的 Secret Key 不会显示；留空表示不修改。</p>
    <div class="grid gap-1.5">
      <Label for="sf-remark">备注</Label>
      <Input id="sf-remark" v-model="form.remark" autocomplete="off" placeholder="可选" />
    </div>

    <p v-if="error" class="rounded-md bg-destructive-soft px-3 py-2 text-sm text-destructive" role="alert" data-testid="storage-form-error">
      {{ error }}
    </p>

    <div class="flex gap-2">
      <Button type="button" variant="outline" :disabled="testing || saving" data-testid="storage-test" @click="runTest">
        <PlugZapIcon />{{ testing ? '测试中…' : '测试连接' }}
      </Button>
      <Button type="submit" class="flex-1" :disabled="saving || testing" data-testid="storage-save">
        {{ saving ? '保存中…' : isEdit ? '保存' : '创建' }}
      </Button>
    </div>
    <div ref="resultBox">
      <TestResultPanel :result="testResult" :testing="testing" />
    </div>
  </form>
</template>
