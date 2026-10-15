// 系统管理 · 存储配置（菜单码 system.storage）。每个函数的最后一个参数可以传 `handle`，
// 声明调用方自己显示哪些错误（见 http.ts）。

import { request, type RequestOptions } from './http'
import type { ConnectionTestResult, StorageProfileItem, StorageStatus } from './types'

type Handle = RequestOptions['handle']

const BASE = '/api/system/storage-profiles'

/** 状态规则被违反时的 409 错误码。 */
export const STORAGE_CONFLICT_CODES = ['storage_not_active', 'storage_is_default', 'storage_in_use'] as const

/** 全部存储配置：默认的在最前，其余按创建时间。 */
export function listStorageProfiles() {
  return request<StorageProfileItem[]>(BASE)
}

/** 新建和"测试还没保存的配置"用的连接参数。 */
export interface StorageConnectionBody {
  endpoint: string
  /** 空字符串表示和 endpoint 相同。 */
  public_endpoint?: string
  bucket: string
  region?: string
  /** 空字符串表示不用前缀；不传时默认 `doclite/`。 */
  path_prefix?: string
  access_key: string
  secret_key: string
  force_path_style?: boolean
}

export interface CreateStorageBody extends StorageConnectionBody {
  profile_name: string
  url_expire_hours?: number
  remark?: string
}

export function createStorageProfile(body: CreateStorageBody, handle?: Handle) {
  return request<StorageProfileItem>(BASE, { method: 'POST', body, handle })
}

/**
 * PATCH 的请求体：不传的字段不改；public_endpoint、remark、path_prefix 传空字符串表示清空；
 * secret_key 不传或传空字符串表示不改。
 */
export interface UpdateStorageBody {
  profile_name?: string
  endpoint?: string
  public_endpoint?: string
  bucket?: string
  region?: string
  path_prefix?: string
  access_key?: string
  secret_key?: string
  force_path_style?: boolean
  url_expire_hours?: number
  status?: StorageStatus
  remark?: string
}

export function updateStorageProfile(id: string, body: UpdateStorageBody, handle?: Handle) {
  return request<StorageProfileItem>(`${BASE}/${id}`, { method: 'PATCH', body, handle })
}

export function deleteStorageProfile(id: string, handle?: Handle) {
  return request<void>(`${BASE}/${id}`, { method: 'DELETE', handle })
}

/** 设为默认存储（之后新上传的文件写到这里）。已经是默认时什么都不做。 */
export function setDefaultStorage(id: string, handle?: Handle) {
  return request<StorageProfileItem>(`${BASE}/${id}/set-default`, { method: 'POST', handle })
}

/** 测试还没保存的配置（新建表单）。输入不合格时 400；连接失败时照样 200，结论在 result 里。 */
export function testNewStorage(body: StorageConnectionBody, handle?: Handle) {
  return request<ConnectionTestResult>(`${BASE}/test`, { method: 'POST', body, handle })
}

/** 测试已保存的配置；`overrides` 是编辑表单里还没保存的修改（不带 secret_key 时用保存的）。 */
export function testStorageProfile(id: string, overrides?: UpdateStorageBody, handle?: Handle) {
  return request<ConnectionTestResult>(`${BASE}/${id}/test`, { method: 'POST', body: overrides, handle })
}
