// 项目（M1-5）：
// - 系统管理里的项目和项目授权（菜单码 system.projects）；
// - `GET /api/projects`：当前用户能进的项目（登录即可），给项目切换器用。
// 每个函数的最后一个参数可以传 `handle`，声明调用方自己显示哪些错误（见 http.ts）。

import { request, type RequestOptions } from './http'
import type {
  AccessCandidates,
  MyProject,
  Page,
  ProjectAccess,
  ProjectDetail,
  ProjectItem,
  ProjectStatus,
} from './types'

type Handle = RequestOptions['handle']

/** 当前用户能进的未删除项目（admin 是全部），含已归档的，按编码排序。 */
export function listMyProjects(handle?: Handle) {
  return request<MyProject[]>('/api/projects', { handle })
}

export interface ProjectListQuery {
  /** 编码或名称包含这段文字（不区分大小写）。 */
  keyword?: string
  /** 不传表示全部。 */
  status?: ProjectStatus
  page?: number
  page_size?: number
}

/** 未删除的项目，新建的在前。 */
export function listProjects(q: ProjectListQuery) {
  return request<Page<ProjectItem>>('/api/system/projects', { query: { ...q } })
}

export function getProject(id: string, handle?: Handle) {
  return request<ProjectDetail>(`/api/system/projects/${id}`, { handle })
}

export interface CreateProjectBody {
  /** `^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`，建好后不能改。 */
  project_code: string
  /** 同时是根文件夹的名称。 */
  project_name: string
  description?: string
}

export function createProject(body: CreateProjectBody, handle?: Handle) {
  return request<ProjectDetail>('/api/system/projects', { method: 'POST', body, handle })
}

export interface UpdateProjectBody {
  project_name?: string
  /** 空字符串表示清空。 */
  description?: string
  status?: ProjectStatus
  /** 整体替换 settings（M3-7 起项目编辑对话框的"著录项和档号"）。 */
  settings?: Record<string, unknown>
  /**
   * 版本检查（M4-4）：打开编辑时读到的 `updated_at`，**原样**传回（字符串带微秒，不要经过 `Date`，会截成毫秒）。
   * 和库里的不一样时 409 `conflict`，什么都不改；不传时不检查。
   */
  expected_updated_at?: string
}

export function updateProject(id: string, body: UpdateProjectBody, handle?: Handle) {
  return request<ProjectDetail>(`/api/system/projects/${id}`, { method: 'PATCH', body, handle })
}

/** 软删除。项目里还有文件时 409 `project_not_empty`。 */
export function deleteProject(id: string, handle?: Handle) {
  return request<void>(`/api/system/projects/${id}`, { method: 'DELETE', handle })
}

export function getProjectAccess(id: string, handle?: Handle) {
  return request<ProjectAccess>(`/api/projects/${id}/access`, { handle })
}

/** 整体替换项目授权。 */
export function setProjectAccess(id: string, userIds: string[], roleIds: string[], handle?: Handle) {
  return request<ProjectAccess>(`/api/projects/${id}/access`, {
    method: 'PUT',
    body: { user_ids: userIds, role_ids: roleIds },
    handle,
  })
}

/** 授权时的选人候选：全部用户（带 role_ids 和状态）和全部角色。 */
export function getAccessCandidates(handle?: Handle) {
  return request<AccessCandidates>('/api/system/projects/access-candidates', { handle })
}
