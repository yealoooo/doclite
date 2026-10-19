// M7-1：许可证（菜单码 system.license）。

import { type Handle, request } from './http'
import type { LicenseState } from './types'

/** 导入的错误码，由许可证页自己显示（不弹 toast）。 */
export const LICENSE_IMPORT_CODES = [
  'invalid_input',
  'invalid_license',
  'license_mismatch',
  'license_file_expired',
  'invalid_state',
] as const

export function getLicense() {
  return request<LicenseState>('/api/system/license')
}

/** 导入（或替换）：`text` 是 .lic 文件的全文。 */
export function importLicense(text: string, handle?: Handle) {
  return request<LicenseState>('/api/system/license', { method: 'PUT', body: { license: text }, handle })
}
