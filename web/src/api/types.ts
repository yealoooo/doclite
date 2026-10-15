// 接口的请求和响应类型，和 server 的 doclite-api 一一对应（时间都是 RFC3339 字符串）。

/** `GET /api/me`、`POST /api/auth/login` 的响应。 */
export interface Me {
  id: string
  username: string
  display_name: string
  status: 'active' | 'disabled'
  /** 按 role_code 排序。 */
  roles: { role_code: string; role_name: string }[]
}

/** `GET /api/me/menus` 的一项（不含分组本身）。 */
export interface MyMenu {
  menu_code: string
  /** 上级分组，例如 `system.users` 的是 `system`。 */
  parent_code: string | null
  menu_name: string
  route_path: string | null
  sort_no: number
}

/** 用户列表和详情里的一个角色。 */
export interface UserRole {
  id: string
  role_code: string
  role_name: string
}

/** `/api/system/users` 的用户。 */
export interface UserItem {
  id: string
  username: string
  display_name: string
  status: 'active' | 'disabled'
  last_login_at: string | null
  created_at: string
  updated_at: string
  roles: UserRole[]
}

export interface Page<T> {
  items: T[]
  total: number
  page: number
  page_size: number
}

/** `/api/system/roles` 的角色。 */
export interface RoleItem {
  id: string
  role_code: string
  role_name: string
  description: string | null
  /** 内置角色不能删除；admin 的菜单不能改。 */
  is_builtin: boolean
  /** 按菜单 sort_no 排序。 */
  menu_codes: string[]
  /** 拥有这个角色的用户数（含已停用的）。 */
  user_count: number
  created_at: string
  updated_at: string
}

/** `GET /api/system/menus` 的一项（含分组）。 */
export interface MenuItem {
  menu_code: string
  parent_code: string | null
  menu_name: string
  menu_type: 'group' | 'menu'
  route_path: string | null
  sort_no: number
}

// ---------- 存储配置（M0-6） ----------

/** 存储状态：active 可读写、readonly 只读（不再写入新文件）、disabled 停用。 */
export type StorageStatus = 'active' | 'readonly' | 'disabled'

/** `/api/system/storage-profiles` 的一条存储配置。永远不含 secret_key。 */
export interface StorageProfileItem {
  id: string
  profile_name: string
  storage_type: 's3'
  /** server 访问 S3 的地址（规整过，没有末尾的 `/`）。 */
  endpoint: string
  /** 工作站访问 S3 的地址；null 表示和 endpoint 相同。 */
  public_endpoint: string | null
  bucket: string
  region: string
  /** 空字符串或以 `/` 结尾。 */
  path_prefix: string
  access_key: string
  /** 总是 true：secret_key 已保存（不返回）。 */
  has_secret: boolean
  /** 当前 MASTER_KEY 能解开保存的 secret_key；false 时要重新填写密钥。 */
  secret_readable: boolean
  force_path_style: boolean
  url_expire_hours: number
  status: StorageStatus
  is_default: boolean
  remark: string | null
  /** 有文件存在这个存储里：不能停用（可以改只读）。 */
  in_use: boolean
  /** 能删除：不是默认、没有被任何数据引用。 */
  deletable: boolean
  created_at: string
  updated_at: string
}

/** 连接测试的结论。 */
export type ConnectionTestResultCode =
  | 'ok'
  | 'unreachable'
  | 'auth_failed'
  | 'bucket_not_found'
  | 'secret_unreadable'
  | 'other'

/** public_endpoint 的检查（只在测试成功、而且 public_endpoint 和 endpoint 不同时才有）。 */
export interface PublicEndpointCheck {
  result: 'ok' | 'unreachable' | 'rejected'
  message: string
  http_status: number | null
  elapsed_ms: number
}

/** `POST …/test`、`POST …/{id}/test` 的响应。连接失败也是 200，结论在 result 里。 */
export interface ConnectionTestResult {
  ok: boolean
  result: ConnectionTestResultCode
  /** 给人看的中文说明。 */
  message: string
  /** 排查用的简短摘要（HTTP 状态码、S3 错误码……）；成功时为 null。 */
  detail: string | null
  elapsed_ms: number
  public_endpoint_check: PublicEndpointCheck | null
}

// ---------- 项目和项目授权（M1-5） ----------

/** 项目状态：active 进行中、archived 已归档（只是标记，项目切换器默认不显示，不限制写入）。 */
export type ProjectStatus = 'active' | 'archived'

/** `GET /api/projects` 的一项：当前用户能进的未删除项目（含已归档的），按编码排序。 */
export interface MyProject {
  id: string
  project_code: string
  project_name: string
  status: ProjectStatus
  /** 根文件夹：文件目录、热文件夹从这里开始。 */
  root_folder_id: string
}

/** `GET /api/system/projects` 列表里的一个项目。 */
export interface ProjectItem {
  id: string
  project_code: string
  project_name: string
  description: string | null
  status: ProjectStatus
  root_folder_id: string
  /** 个人授权的人数（含已停用的用户）。 */
  member_count: number
  /** 授权的角色数。 */
  role_count: number
  created_by: string
  created_at: string
  updated_at: string
}

/** 授权里的一个人。已停用的用户仍留在授权里。 */
export interface AccessUser {
  id: string
  username: string
  display_name: string
  status: 'active' | 'disabled'
}

/** 授权里的一个角色。 */
export interface AccessRole {
  id: string
  role_code: string
  role_name: string
}

/** `GET/PUT /api/projects/{pid}/access` 的响应。 */
export interface ProjectAccess {
  /** 个人授权，按登录名排序。 */
  users: AccessUser[]
  /** 角色授权，内置角色在前。 */
  roles: AccessRole[]
}

/** `/api/system/projects/{id}` 的项目详情。 */
export interface ProjectDetail extends ProjectAccess {
  id: string
  project_code: string
  project_name: string
  description: string | null
  status: ProjectStatus
  /** 补上了默认值的 settings（design §2.5）。 */
  settings: Record<string, unknown>
  root_folder_id: string
  category_root_id: string
  created_by: string
  created_at: string
  updated_at: string
}

/** 选人候选里的一个用户。 */
export interface CandidateUser extends AccessUser {
  /** 用户拥有的角色 id。 */
  role_ids: string[]
}

/** `GET /api/system/projects/access-candidates`：全部用户（精简信息）和全部角色。 */
export interface AccessCandidates {
  users: CandidateUser[]
  roles: AccessRole[]
}

// ---------- 文件目录（M1-6、M1-8；M1-10 浏览） ----------

/** 审核状态（design §2.2）。 */
export type ReviewStatus = 'unassigned' | 'pending' | 'approved' | 'rejected'

/** 颜色模式（design §4.3）：黑白（1 位）、灰度、彩色。 */
export type ColorMode = 'bw' | 'gray' | 'color'

/** 祖先链里的一个文件夹。 */
export interface FolderAncestor {
  id: string
  folder_name: string
  path_code: string
}

/** `GET /api/folders/{id}`：文件夹本身和祖先链（面包屑）。 */
export interface FolderItem {
  id: string
  project_id: string
  /** null 表示项目的根文件夹。 */
  parent_id: string | null
  folder_name: string
  /** 备注（原件位置等）。 */
  remark: string | null
  path_code: string
  created_at: string
  updated_at: string
  /** 从根文件夹到父文件夹，不含自己；根文件夹的为空数组。 */
  ancestors: FolderAncestor[]
}

/** children 里的一个子文件夹。 */
export interface SubfolderEntry {
  id: string
  folder_name: string
  remark: string | null
  path_code: string
  /** 未删除的直接子文件夹数。 */
  folder_count: number
  /** 未删除的直接文件数。 */
  file_count: number
}

/** children 里的一个文件（`folder_files` 的一行）。 */
export interface FileEntry {
  folder_file_id: string
  file_id: string
  file_name: string
  sort_no: number
  review_status: ReviewStatus
  review_round: number
  /** 当前版本号。 */
  version_no: number
  /** 是否已归类。 */
  cataloged: boolean
  created_at: string
}

/** `GET /api/folders/{id}/children`：都按 sort_no、名称排序，不分页，不带图片地址。 */
export interface FolderChildren {
  folders: SubfolderEntry[]
  files: FileEntry[]
}

/** 生成不了图片 URL 的原因（存储停用、密钥解不开、签名失败）。 */
export interface UrlError {
  /** `storage_unavailable`、`secret_unreadable`、`presign_failed`。 */
  code: string
  message: string
}

/** 快照的规格和图片地址（batch、snapshots 共用的字段）。 */
export interface SnapshotImageFields {
  snapshot_id: string
  version_no: number
  width_px: number | null
  height_px: number | null
  dpi: number | null
  color_mode: ColorMode | null
  file_size: number
  mime_type: string
  /** 小写 hex。 */
  content_sha256: string
  /** 没有封面、或生成不了 URL 时为 null。 */
  cover_url: string | null
  /** 生成不了 URL 时为 null。 */
  original_url: string | null
  url_expires_at: string | null
  /** 只在生成不了 URL 时出现。 */
  url_error?: UrlError
}

/** `POST /api/files/batch` 的一项：文件当前版本的信息和图片地址（design §4.4）。 */
export interface FileImageItem extends SnapshotImageFields {
  file_id: string
  project_id: string
}

export interface FileBatchResponse {
  /** 按请求里的顺序（去重后）。 */
  items: FileImageItem[]
  /** 不存在、无权访问、已删除（含所在文件夹已删除）的 file_id。 */
  missing: string[]
}

/** `GET /api/files/{fid}/snapshots` 的一个版本。 */
export interface SnapshotItem extends SnapshotImageFields {
  /** `original`、`rescan`、`copy`、`edit`、`restore`。 */
  snapshot_type: string
  /** original、rescan 时上传的原始文件名。 */
  upload_name: string | null
  created_at: string
  created_by: string | null
  /** 创建人现在的显示名；null 表示系统自动处理。 */
  created_by_name: string | null
  storage_profile_id: string
  storage_profile_name: string
  /** 原图的完整 key。 */
  storage_key: string
}

export interface FileSnapshots {
  file_id: string
  project_id: string
  /** 按版本号从小到大。 */
  snapshots: SnapshotItem[]
}

/** `GET /api/files/{fid}/logs` 的一条日志。 */
export interface FileLog {
  id: number
  /** design §2.4 的 action：upload、rescan、rename、move、copy、delete、assign_review…… */
  action: string
  operator_id: string
  /** 操作人现在的显示名。 */
  operator_name: string
  /** 当时的名字等，字段按 action 不同（见 roadmap M1-7、M1-8 的后续须知）。 */
  detail: Record<string, unknown>
  created_at: string
}

export interface FileLogs {
  file_id: string
  /** 新的在前。 */
  logs: FileLog[]
}

/** 实时事件（`GET /api/projects/{pid}/stream` 每条 data 的 JSON）。 */
export interface ProjectEvent {
  /** `folders.changed`、`files.changed`、`resync`；M3-1 起还有 `catalog.changed`。 */
  type: string
  project_id: string
  folder_ids?: string[]
  file_ids?: string[]
  /** `catalog.changed`：涉及的归类节点（没带 item_ids 时当作子树的根）。 */
  category_ids?: string[]
  /** `catalog.changed`：变了的条目；超过 500 个时不带。 */
  item_ids?: string[]
  /** `export.changed`（M4-1）：变了的导出任务（新建、取消、开始、进度、完成、失败、重试……）。 */
  task_ids?: string[]
}

// ---------- 文件目录的整理和上传（M1-6、M1-7、M1-8 的接口；M1-12 用） ----------

/** `POST /api/folders` 的请求体。`if_exists: 'return'` 时遇到同名返回已有的文件夹（200），否则 409 `conflict`。 */
export interface CreateFolderBody {
  parent_id: string
  folder_name: string
  remark?: string | null
  if_exists?: 'return'
}

/** `PATCH /api/folders/{id}` 的请求体：不传的字段不改；remark 传 `""` 清空；parent_id 是移动。 */
export interface UpdateFolderBody {
  folder_name?: string
  remark?: string
  parent_id?: string
}

/** `PATCH /api/folder-files/{id}` 的响应。 */
export interface FolderFileUpdated {
  folder_file_id: string
  file_id: string
  folder_id: string
  file_name: string
  sort_no: number
  /** 移动时目标文件夹里有同名文件、自动改了名。 */
  renamed: boolean
}

/** `POST /api/folder-files/{id}/copy` 的响应（201）。 */
export interface FolderFileCopied {
  folder_file_id: string
  /** 新文件的 file_id。 */
  file_id: string
  snapshot_id: string
  folder_id: string
  file_name: string
  sort_no: number
  renamed: boolean
}

/** `POST /api/folder-files:move` 的一项（按原文件夹 path_code、sort_no、名字依次排到目标末尾）。 */
export interface FolderFileMoved {
  folder_file_id: string
  file_id: string
  folder_id: string
  file_name: string
  sort_no: number
  /** 本来就在目标文件夹里的为 false。 */
  moved: boolean
  renamed: boolean
}

export interface FolderFilesMoved {
  items: FolderFileMoved[]
}

/** 上传的提示（design §4.3）：DPI 低于 200、DPI 未知。 */
export type UploadWarning = 'low_dpi' | 'dpi_unknown'

/**
 * `POST /api/folders/{id}/files` 的响应（design §4.3、roadmap M1-7）：新建 201；
 * 同一文件夹里已有 upload_name 和 sha 都相同的文件（之前已上传过）200，是已有文件现在的信息。
 */
export interface IngestResponse {
  file_id: string
  snapshot_id: string
  folder_file_id: string
  file_name: string
  /** 入库后的名字和这次的原始文件名不同（重名自动改名，或之前上传后被人改过名）。 */
  renamed: boolean
  sort_no: number
  version_no: number
  width_px: number | null
  height_px: number | null
  dpi: number | null
  color_mode: ColorMode | null
  file_size: number
  has_cover: boolean
  warnings: string[]
}

/** 409 `duplicate_content` 的 details：项目里已有的相同内容的文件。 */
export interface DuplicateContentDetails {
  file_id: string
  /** 从根文件夹起用 `/` 连接。 */
  folder_path: string
  file_name: string
}

// ---------- 审核分配（M2-1 的写接口、M2-2 的读接口；M2-5 用） ----------

/** 按审核状态的计数（review-summary）。 */
export interface ReviewStatusCounts {
  unassigned: number
  pending: number
  approved: number
  rejected: number
  total: number
}

/** `GET /api/projects/{pid}/review-summary?folder_id=` 的 children：未删除的子文件夹，各自整棵子树的计数。 */
export interface ReviewSummaryChild {
  folder_id: string
  folder_name: string
  counts: ReviewStatusCounts
}

/** `GET /api/projects/{pid}/review-summary?folder_id=`（review_assign）：不传 folder_id 是根文件夹（整个项目）。 */
export interface ReviewSummary {
  folder_id: string
  folder_name: string
  parent_id: string | null
  /** 这个文件夹整棵子树。 */
  counts: ReviewStatusCounts
  /** 直接放在里面的文件。 */
  direct_counts: ReviewStatusCounts
  /** 按 sort_no、名称排。 */
  children: ReviewSummaryChild[]
}

/** `GET /api/projects/{pid}/reviewers` 的一项：能分给的人（启用、有 review 菜单、能进这个项目）。 */
export interface ProjectReviewer {
  id: string
  username: string
  display_name: string
  /** 他在这个项目里未删除文件上的 open 分配个数（待办）。 */
  open: number
}

export interface ProjectReviewers {
  project_id: string
  /** 按显示名、用户名排，不分页。 */
  reviewers: ProjectReviewer[]
}

/** 审核人不能再审这个项目的原因（M2-2）。 */
export type ReviewerProblem = 'disabled' | 'no_review_menu' | 'no_project_access'

/** review-assignments 的 reviewers：范围（folder_id）里有 open 分配的审核人（不受 reviewer_id 筛选影响）。 */
export interface AssignmentReviewer {
  reviewer_id: string
  username: string
  display_name: string
  open: number
  can_review: boolean
  /** 为空即 can_review。 */
  problems: ReviewerProblem[]
}

/** review-assignments 的一项：一个 open 分配。 */
export interface OpenAssignment {
  /** file_reviews 的 id，直接用于单个改派。 */
  assignment_id: string
  file_id: string
  folder_file_id: string
  folder_id: string
  /** 从项目根文件夹起用 `/` 连接。 */
  folder_path: string
  file_name: string
  sort_no: number
  review_round: number
  reviewer_id: string
  reviewer_name: string
  reviewer_can_review: boolean
  assigned_by: string
  assigned_at: string
}

/** `GET /api/projects/{pid}/review-assignments?folder_id=&reviewer_id=&limit=&cursor=`（review_assign）。 */
export interface OpenAssignments {
  /** 符合 folder_id、reviewer_id 的 open 分配总数。 */
  total: number
  reviewers: AssignmentReviewer[]
  /** 按所在文件夹 path_code、sort_no、file_id 排。 */
  items: OpenAssignment[]
  next_cursor: string | null
}

/** `GET /api/files/{fid}/reviews` 的一行（含已取消的，按 id 升序）。 */
export interface FileReviewRow {
  assignment_id: string
  reviewer_id: string
  reviewer_name: string
  assigned_by: string
  assigned_by_name: string
  assigned_at: string
  /** `open`、`done`、`cancelled`。 */
  status: string
  /** `approved`、`rejected`；没审的为 null。 */
  result: string | null
  snapshot_id: string | null
  /** 所审快照的版本号；没审的为 null。 */
  version_no: number | null
  reject_reasons: string[]
  comment: string | null
  reviewed_at: string | null
}

/** `GET /api/files/{fid}/reviews`（review 或 review_assign）。 */
export interface FileReviews {
  file_id: string
  project_id: string
  folder_id: string
  review_status: ReviewStatus
  review_round: number
  /** 当前版本号。 */
  version_no: number
  reviews: FileReviewRow[]
}

/** `POST /api/projects/{pid}/review-assignments` 的响应。 */
export interface AssignResult {
  assigned: number
  /** 跳过的（不是待分配的）；按文件夹分配时是子树里不是待分配的文件数。 */
  skipped: number
  /** 这次分配了的文件。 */
  file_ids: string[]
}

/** `POST /api/review-assignments/{id}/reassign` 的响应。 */
export interface ReassignOneResult {
  assignment_id: string
  file_id: string
  folder_id: string
  reviewer_id: string
  from_reviewer_id: string
  /** 原来就分给这个人时为 false（不写日志、不发事件）。 */
  changed: boolean
}

/** `POST /api/projects/{pid}/review-assignments:reassign` 的响应。 */
export interface ReassignResult {
  reassigned: number
  skipped: number
  file_ids: string[]
}

/** `POST /api/files/{fid}/reopen` 的响应：重新分给的审核人；原审核人不能再审时为 null（改为待分配）。 */
export interface ReopenResult {
  file_id: string
  folder_id: string
  review_status: ReviewStatus
  review_round: number
  assignment_id: string | null
  reviewer_id: string | null
}

// ---------- 待重扫和重扫件（M2-2 的 rescans、M1-7 的重扫接口；M2-7 用） ----------

/** 最近一次驳回（rescans 的 `last_reject`）。 */
export interface LastReject {
  assignment_id: string
  reviewer_id: string
  /** 审核人现在的显示名。 */
  reviewer_name: string
  /** 驳回原因：内置的是代码（blur 等，用 `rejectReasonLabel` 翻成中文），项目自定义的是中文。 */
  reasons: string[]
  comment: string | null
  /** 驳回时审的快照和它的版本号。 */
  snapshot_id: string
  version_no: number
  reviewed_at: string
}

/** `GET /api/projects/{pid}/rescans` 的一项：一个已驳回、等重扫的文件。 */
export interface RescanItem {
  file_id: string
  folder_file_id: string
  folder_id: string
  folder_name: string
  /** 从项目根文件夹起用 `/` 连接（含根文件夹的名字）。 */
  folder_path: string
  /** 直接所在文件夹的备注（原件位置）。 */
  folder_remark: string | null
  file_name: string
  sort_no: number
  review_round: number
  /** 当前版本号。 */
  version_no: number
  /** 找不到驳回记录时为 null。 */
  last_reject: LastReject | null
}

/** `GET /api/projects/{pid}/rescans`（scan 或 files）：按文件夹 path_code、sort_no 排序，keyset 分页。 */
export interface RescanList {
  /** 范围里已驳回的文件总数（角标）。 */
  total: number
  items: RescanItem[]
  next_cursor: string | null
}

/**
 * `POST /api/files/{fid}/rescan` 的响应（201）：字段同上传，另加所在文件夹、审核状态（重新分给了最近一次驳回它的
 * 审核人是 pending；那个人已不能再审这个项目时 unassigned）、重扫后的轮次和分给的审核人（没分配时为 null）。
 */
export interface RescanResponse extends IngestResponse {
  folder_id: string
  review_status: ReviewStatus
  review_round: number
  reviewer_id: string | null
}

// ---------- 审核工作台（M2-1 的审核写接口、M2-2 的"我的审核"；M2-6 用） ----------

/** `GET /api/me/reviews` 的 `state`：all（缺省）、open（待审）、approved（我通过的）、rejected（我驳回的）。 */
export type MyReviewState = 'all' | 'open' | 'approved' | 'rejected'

/** 我的审核的计数（每个文件算一次；不受 state、low_dpi 影响，受 folder_id 影响）。 */
export interface MyReviewCounts {
  all: number
  open: number
  approved: number
  rejected: number
  /** 当前版本的 dpi 低于 `low_dpi_threshold`（读不出的不算），和其他几种交叉。 */
  low_dpi: number
}

/** 有我的审核的文件夹（文件直接所在的文件夹），按文件夹先序排。 */
export interface MyReviewFolder {
  folder_id: string
  folder_name: string
  /** 从项目根文件夹起用 `/` 连接。 */
  folder_path: string
  counts: MyReviewCounts
}

/** 我能进的其他项目里的待审个数（只列大于 0 的）。 */
export interface OtherProjectOpen {
  project_id: string
  /** 已归档的项目照样能审（design §1.6）。 */
  status: ProjectStatus
  open: number
}

/** 我的审核的一行：文件和最近一次分给我、没有取消的那一行分配。 */
export interface MyReviewItem {
  /** file_reviews 的 id。 */
  assignment_id: string
  file_id: string
  folder_file_id: string
  folder_id: string
  /** 从项目根文件夹起用 `/` 连接。 */
  folder_path: string
  file_name: string
  sort_no: number
  review_round: number
  /** 文件现在的审核状态。 */
  review_status: ReviewStatus
  /** 当前版本号和它的 dpi（读不出时为 null）。 */
  version_no: number
  dpi: number | null
  assigned_at: string
  /** 那一行分配：open（待审）或 done（已审）。 */
  status: 'open' | 'done'
  /** 那一行的结果；open 时为 null。 */
  result: 'approved' | 'rejected' | null
  reviewed_at: string | null
  /** 这一行是不是这个文件现在有效的那一行；false 时只是历史（不给撤回通过等操作）。 */
  is_current: boolean
}

/** `GET /api/me/reviews?project_id=&folder_id=&state=&low_dpi=&limit=&cursor=`（review）。 */
export interface MyReviews {
  project_id: string
  /** "DPI 不足"的界限（200）。 */
  low_dpi_threshold: number
  counts: MyReviewCounts
  folders: MyReviewFolder[]
  /** 其他项目的待审合计（页头提示）。 */
  other_projects_open: number
  /** 按项目编码排。 */
  other_projects: OtherProjectOpen[]
  /** 这一页（符合 state、low_dpi），按文件夹 path_code、sort_no、file_id 排。 */
  items: MyReviewItem[]
  /** null 表示没有下一页。 */
  next_cursor: string | null
}

/** `POST /api/files/{fid}/review` 的请求体。 */
export interface ReviewBody {
  result: 'approved' | 'rejected'
  /** 驳回时至少一个，只能从项目的原因列表里选；通过时不带。 */
  reject_reasons?: string[]
  /** 最多 500 字；选了 `other` 时必填。 */
  comment?: string | null
  /** 看到的快照；不是当前版本时 409 `conflict`。 */
  snapshot_id?: string
}

/** `POST /api/files/{fid}/review` 的响应（批量通过的每一项也是它）。 */
export interface ReviewedFile {
  file_id: string
  folder_id: string
  /** 写了结果的那一行分配。 */
  assignment_id: string
  result: 'approved' | 'rejected'
  review_status: ReviewStatus
  /** 审的快照（当前版本）。 */
  snapshot_id: string
  version_no: number
  review_round: number
  reviewed_at: string
}

/** `POST /api/projects/{pid}/reviews:approve` 的响应。 */
export interface ApproveManyResult {
  approved: number
  /** 按所在文件夹 path_code、sort_no 排。 */
  items: ReviewedFile[]
}

/** `GET /api/projects/{pid}/review-settings`（能进项目即可）。 */
export interface ReviewSettings {
  /** 内置的 8 个是代码（blur、skew……，前端翻成中文），项目自定义的直接是中文。 */
  reject_reasons: string[]
  rescan_reassign: string
  /** 说明最多多少个字符（500）。 */
  comment_max_chars: number
  /** 选中的文件一次最多多少个（500）。 */
  max_batch_files: number
  max_folder_files: number
}

// ---------- 归类：设置、看板矩阵、文件挂在哪个条目（M3-1、M3-4 的接口；M3-7 用） ----------

/** 档案条目的状态（design §2.3）：draft 著录中、done 著录完成、locked 定稿。 */
export type ItemStatus = 'draft' | 'done' | 'locked'

/** 著录项的类型（用户决定 Q1）。 */
export type ItemFieldType = 'text' | 'date' | 'enum'

/** 一个著录项（catalog-settings 返回的，补上了默认值；能原样写回 settings.item_fields）。 */
export interface ItemFieldDef {
  key: string
  label: string
  type: ItemFieldType
  required: boolean
  /** enum 的选项；其他类型为空数组。 */
  options: string[]
  /** text 的最大字符数（没设时 500）；其他类型为 null。 */
  max_length: number | null
  /** 新建条目时的默认值；没有时为 null。 */
  default: string | null
}

/** `GET /api/projects/{pid}/catalog-settings`（能进项目即可）：归类要用的项目设置（补上了默认值）和几个规则。 */
export interface CatalogSettings {
  archive_no_template: string
  /** 库里存的模板解析不了时的说明；正常为 null。 */
  archive_no_template_error: string | null
  /** 模板用到的节点类型，按模板里的顺序。 */
  archive_no_levels: string[]
  item_fields: ItemFieldDef[]
  lock_requires_approved: boolean
  require_locked_item: boolean
  category_types: string[]
  category_code_max_chars: number
  /** 归类编码里不能有的可见字符（当前模板的分隔符、花括号、`\ / : * ? " < > |`）。 */
  category_code_forbidden_chars: string[]
  category_name_max_chars: number
  date_formats: string[]
}

/** 归类树的一个节点（M3-1，`GET /api/projects/{pid}/archive-categories?parent_id=`，catalog 菜单）。 */
export interface ArchiveCategory {
  id: string
  project_id: string
  parent_id: string | null
  path_code: string
  /** root、fonds、category、year、retention、org、custom。 */
  category_type: string
  category_code: string
  category_name: string
  sort_no: number
  /** 未删除的直接子节点数。 */
  child_count: number
  /** 子树里（含自己）未删除的条目数。 */
  item_count: number
  created_at: string
}

/** `GET /api/projects/{pid}/archive-categories?parent_id=`：父节点本身和它的直接子节点。 */
export interface ArchiveCategoryList {
  parent: ArchiveCategory
  items: ArchiveCategory[]
}

/** 矩阵的一行（一种审核状态）：按归类状态分的文件数。 */
export interface CatalogRowCounts {
  /** 未归类。 */
  uncataloged: number
  /** 已归类未定稿（条目是 draft 或 done）。 */
  unlocked: number
  /** 已定稿。 */
  locked: number
  total: number
}

/** 4 种审核状态 × 3 种归类状态。 */
export interface CatalogMatrix {
  unassigned: CatalogRowCounts
  pending: CatalogRowCounts
  approved: CatalogRowCounts
  rejected: CatalogRowCounts
}

/** 每列（归类状态）的合计；`cataloged = unlocked + locked`。 */
export interface CatalogColumnCounts {
  uncataloged: number
  unlocked: number
  locked: number
  cataloged: number
  total: number
}

/** catalog-summary 的一组计数（一个文件夹整棵子树、直接放的文件或一个子文件夹的子树）。 */
export interface CatalogCounts {
  total: number
  /** 每行的合计，和 review-summary 的 counts 相同。 */
  review: ReviewStatusCounts
  catalog: CatalogColumnCounts
  matrix: CatalogMatrix
  /** 可导出的文件数（按 `export.require_locked_item`：true 只算已定稿的，false 算已归类的），按审核状态分开。 */
  exportable: ReviewStatusCounts
}

/** catalog-summary 的 children：未删除的子文件夹，各自整棵子树的计数。 */
export interface CatalogSummaryChild {
  folder_id: string
  folder_name: string
  counts: CatalogCounts
}

/** `GET /api/projects/{pid}/catalog-summary?folder_id=`（dashboard）：不传 folder_id 是根文件夹（整个项目）。 */
export interface CatalogSummary {
  folder_id: string
  folder_name: string
  parent_id: string | null
  /** 项目设置 `export.require_locked_item`（缺省 true）。 */
  require_locked_item: boolean
  counts: CatalogCounts
  direct_counts: CatalogCounts
  /** 按 sort_no、名称排。 */
  children: CatalogSummaryChild[]
}

/** 一个文件挂在哪个条目（M3-4）。界面上按 Q10 只显示档号（为空时"第 n 件"）、题名、第几页。 */
export interface CatalogPlacement {
  item_id: string
  item_no: number
  /** 缺某一级编码时为 null。 */
  archive_no: string | null
  title: string
  /** 条目里的页序。 */
  page_no: number
  item_status: ItemStatus
  category_id: string
  /** 条目所在归类节点的名称链，不含根节点。 */
  category_names: string[]
  /** `category_names` 用 `/` 连接，只作显示用。 */
  category_path: string
}

/** `GET /api/files/{fid}/catalog`（能进项目即可）。 */
export interface FileCatalog {
  file_id: string
  project_id: string
  /** 没挂接时为 null。 */
  placement: CatalogPlacement | null
}

/** `POST /api/projects/{pid}/catalog-placements` 的响应。 */
export interface CatalogPlacements {
  items: { file_id: string; placement: CatalogPlacement | null }[]
  missing: string[]
}

// ---------- 归类：归类节点的写接口、档案条目（M3-1、M3-2 的接口；M3-5 用） ----------

/** `PATCH /api/projects/{pid}/archive-categories/{id}` 的响应：节点加这次重算的结果。 */
export interface UpdatedArchiveCategory extends ArchiveCategory {
  /** 这次档号变了的条目数。 */
  renumbered: number
  /** 重算后因为撞号档号留空的条目数（M3-2 审查）。 */
  archive_no_conflicts: number
}

/** 条目里各审核状态的页数。 */
export interface ItemPageCounts {
  unassigned: number
  pending: number
  approved: number
  rejected: number
}

/** 档号为空的原因（详情接口读的时候算，不存）。 */
export interface ArchiveNoReason {
  /** incomplete 缺级、conflict 撞号、not_generated 生成得出但还没写进来、template_invalid 模板解析不了。 */
  code: string
  message: string
  /** incomplete：祖先链上缺的级别（fonds、category……，模板顺序）。 */
  missing?: string[]
  /** conflict、not_generated：按现在的模板生成得出的档号。 */
  archive_no?: string
  /** conflict：占着这个档号的条目。 */
  conflict_item_id?: string
  conflict_category_id?: string
  conflict_item_no?: number
}

/** 著录项的问题（`field_issues`、400 `invalid_input` 的 `details.fields`）；key 是 `title` 时指题名。 */
export interface FieldIssue {
  key: string
  message: string
}

/** 条目的一页（详情的 pages，按页号排；图片照旧经 batch 取）。 */
export interface ArchiveItemPage {
  file_id: string
  page_no: number
  file_name: string
  folder_id: string
  /** 所在文件夹的路径（从项目根文件夹起用 `/` 连接）。 */
  folder_path: string
  review_status: ReviewStatus
  review_round: number
  /** 当前版本号。 */
  version_no: number
  /** 条目已定稿，而且这一页的当前版本是定稿之后出的。 */
  new_version_after_lock: boolean
}

/** 列表里的一个条目（`GET /api/archive-categories/{id}/items`）。 */
export interface ArchiveItemSummary {
  id: string
  category_id: string
  item_no: number
  /** 缺级、撞号时为 null（列表只显示空档号，原因看详情）。 */
  archive_no: string | null
  title: string
  status: ItemStatus
  row_version: number
  page_count: number
  page_counts: ItemPageCounts
  locked_at: string | null
  new_version_after_lock: boolean
  updated_at: string
}

/** `GET /api/archive-categories/{id}/items` 的响应（keyset 分页，`total`、`counts` 不受分页影响）。 */
export interface ArchiveItemList {
  category_id: string
  /** 直接放在这个节点里的未删除条目数。 */
  total: number
  counts: { draft: number; done: number; locked: number }
  items: ArchiveItemSummary[]
  next_cursor: string | null
}

/** 条目详情（`GET /api/archive-items/{id}`；新建、修改、状态动作也返回它）。 */
export interface ArchiveItem extends ArchiveItemSummary {
  project_id: string
  /** 所在归类节点的名称路径（从根节点起用 `/` 连接）。 */
  category_path: string
  archive_no_reason: ArchiveNoReason | null
  /** 库里存的全部著录项（key → 字符串），含不在选项里的旧值、设置里已经没有的。 */
  extra_fields: Record<string, unknown>
  /** 存着的值和现在的设置对不上的。 */
  field_issues: FieldIssue[]
  created_by: string
  created_at: string
  updated_by: string | null
  pages: ArchiveItemPage[]
}

/** `PATCH /api/archive-items/{id}` 的请求体；不传的字段不改。 */
export interface UpdateArchiveItemBody {
  row_version: number
  title?: string
  /** 按 key 合并：给了的 key 改成新值（null 或空串清空），没给的不动。 */
  extra_fields?: Record<string, string | null>
  category_id?: string
  item_no?: number
}

/** `POST /api/projects/{pid}/archive-items:lock` 的响应。 */
export interface LockItemsResult {
  locked: number
  /** 原来就定稿了、跳过的个数。 */
  skipped: number
  item_ids: string[]
}

/** `POST /api/projects/{pid}/archive-items:renumber` 的响应。 */
export interface RenumberResult {
  /** 范围：这个节点的子树（整个项目时是根节点）。 */
  category_id: string
  renumbered: number
  skipped_locked: number
  archive_no_conflicts: number
}

// ---------- 挂接、页序和删除条目（M3-3 的接口；M3-6 用） ----------

/**
 * 409 `file_cataloged` 的 details 里的一项：要挂的文件已经挂在别的条目里（一个文件只能在一个条目里，Q3）。
 * 字段和 placements 一样，只是没有 `category_path`、多了 `file_id`。
 */
export interface CatalogedElsewhere {
  file_id: string
  item_id: string
  category_id: string
  /** 条目所在归类节点的名称链，从根节点的下一级起。 */
  category_names: string[]
  item_no: number
  archive_no: string | null
  title: string
  page_no: number
  item_status: ItemStatus
}

/** 409 `file_cataloged`（PUT 页序、带 file_ids 新建）的 details：`ids` 是 file_id。 */
export interface FileCatalogedDetails {
  ids: string[]
  items: CatalogedElsewhere[]
}

// ---------- 导出（M4-1、M4-3 的接口；M4-5 用） ----------

/** 导出格式：v1 只有 PDF（每件一个 PDF）和原图包（每件一个文件夹）；双层 PDF、OFD 以后的版本才有。 */
export type ExportFormat = 'pdf' | 'images'

/** 导出任务的状态（`export_tasks.status`）。 */
export type ExportTaskStatus = 'queued' | 'running' | 'done' | 'failed' | 'canceled'

/** 预检、建任务的范围：`scope_ids`（带 `scope_type`）和 `resubmit_of`（补交一个已完成任务的交付后变更）二选一。 */
export interface ExportScopeBody {
  scope_type?: 'category' | 'item'
  /** 1–5000 个，都要是这个项目里未删除的节点或条目。 */
  scope_ids?: string[]
  /** 补交：一个已完成的导出任务的 id，范围是它的交付后变更里有变更、没删除的条目。 */
  resubmit_of?: string
}

/** 预检的请求体（建任务另加 `only_ready`）。 */
export interface ExportPrecheckBody extends ExportScopeBody {
  export_format: ExportFormat
  /** 只导出审核通过的（不传是 true）。 */
  only_approved?: boolean
}

export interface CreateExportTaskBody extends ExportPrecheckBody {
  /** 只导出就绪的（不传是 false：有不就绪的条目时 409）。 */
  only_ready?: boolean
}

/** 不就绪原因里举例的一页。 */
export interface ExportPageRef {
  page_no: number
  file_id: string
  file_name: string
  review_status: ReviewStatus
}

/** 一个条目不能导出的原因：not_locked、no_archive_no、no_pages、pages_not_approved。 */
export interface ExportNotReadyReason {
  code: string
  /** 给人看的说明，例如"有 2 页不是审核通过（第 2 页：待审；第 3 页：驳回）"。 */
  message: string
  /** not_locked：条目现在的状态。 */
  item_status?: ItemStatus
  /** pages_not_approved：不是通过的页数。 */
  count?: number
  /** pages_not_approved：前 5 页。 */
  pages?: ExportPageRef[]
}

/** 预检里不能导出的条目（按导出顺序）。 */
export interface ExportNotReadyItem {
  item_id: string
  category_id: string
  /** 所在节点的名称链（不含根节点）。 */
  category_names: string[]
  item_no: number
  archive_no: string | null
  title: string
  status: ItemStatus
  page_count: number
  reasons: ExportNotReadyReason[]
}

/** 各原因涉及的件数（一件可以有几种原因）。 */
export interface ExportReasonCounts {
  not_locked: number
  no_archive_no: number
  no_pages: number
  pages_not_approved: number
}

/**
 * `POST /api/projects/{pid}/export-tasks:precheck` 的响应；建任务 409 `invalid_state` 的 `details` 也是它。
 * `not_ready` 最多列 500 件（`not_ready_truncated`），件数和 `reason_counts` 是全部的。
 */
export interface ExportPrecheck {
  scope_type: 'category' | 'item'
  /** 补交时是算出来的条目。 */
  scope_ids: string[]
  resubmit_of: string | null
  export_format: ExportFormat
  only_approved: boolean
  require_locked_item: boolean
  item_count: number
  page_count: number
  ready_item_count: number
  ready_page_count: number
  not_ready_count: number
  not_ready_page_count: number
  reason_counts: ExportReasonCounts
  not_ready: ExportNotReadyItem[]
  not_ready_truncated: boolean
  /** 一个任务最多导出多少页（20 万）。 */
  max_page_count: number
  /** 要导出的页数超过上限：不能建任务，要按分类分批导出。 */
  page_limit_exceeded: boolean
  page_limit_message: string | null
}

/** 任务范围里一个节点的名称链（`scope_type = category` 时；根节点是空数组）。 */
export interface ExportScopeCategory {
  id: string
  category_names: string[]
}

/** 导出任务（列表、详情、建任务、取消的响应）。 */
export interface ExportTask {
  id: string
  project_id: string
  scope_type: 'category' | 'item'
  scope_ids: string[]
  scope_categories: ExportScopeCategory[]
  /** 补交自哪个任务。 */
  resubmit_of: string | null
  export_format: string
  only_approved: boolean
  only_ready: boolean
  require_locked_item: boolean
  status: ExportTaskStatus
  /** 任务已取消、执行器还在清理（删已上传的部分）。 */
  cleaning_up: boolean
  /** 冻结的件数、页数。 */
  item_count: number
  page_count: number
  /** `only_ready` 时跳过的不就绪件数。 */
  skipped_item_count: number
  progress_done: number
  progress_total: number
  attempts: number
  max_attempts: number
  /** 失败的原因；排队重试时是"第 n 次执行失败，x 后自动重试：……"。可能有几十行（问题页清单）。 */
  error_message: string | null
  output_size: number | null
  /** 导出物的到期时间（之后自动删除，任务和明细保留）。 */
  expires_at: string | null
  expired: boolean
  created_by: string
  created_by_name: string | null
  created_at: string
  started_at: string | null
  finished_at: string | null
}

/** `GET /api/projects/{pid}/export-tasks`：最新的在前，keyset 分页。 */
export interface ExportTaskList {
  items: ExportTask[]
  next_cursor: string | null
}

/** 明细的一页（冻结时的件号、档号、题名和快照）。 */
export interface ExportTaskFile {
  /** 条目在导出里的序号（1 起）。 */
  seq: number
  item_id: string
  item_no: number
  archive_no: string | null
  title: string
  page_no: number
  file_id: string
  snapshot_id: string
  version_no: number
  mime_type: string
  file_size: number
  /** 小写十六进制。 */
  content_sha256: string
  upload_name: string | null
  /** 文件现在的名字。 */
  file_name: string | null
}

/** `GET /api/export-tasks/{id}/files`：按序号、页号，keyset 分页（limit 1–2000）。 */
export interface ExportTaskFiles {
  export_task_id: string
  item_count: number
  page_count: number
  items: ExportTaskFile[]
  next_cursor: string | null
}

/** 交付后变更原因里举例的一页。 */
export interface ExportChangePage {
  file_id: string
  file_name: string | null
  exported_page_no?: number
  page_no?: number
  exported_version_no?: number
  version_no?: number
  review_status?: ReviewStatus
  to_item_id?: string
  to_item_no?: number
  to_archive_no?: string
}

/** 著录的变化（`item_changed`）：label 是导出时的著录项名称。 */
export interface ExportFieldChange {
  key: string
  label: string
  before: unknown
  after: unknown
}

/** 交付后变更的一种原因。 */
export interface ExportChangeReason {
  /** new_version、pages_removed、pages_moved、pages_reordered、pages_added、not_approved、item_changed、item_deleted。 */
  code: string
  message: string
  count?: number
  pages?: ExportChangePage[]
  fields?: ExportFieldChange[]
}

/** 有变更的一件（件号、档号、题名、节点是导出时的，`now` 是现在的）。 */
export interface ExportChangedItem {
  seq: number
  item_id: string
  category_id: string
  category_names: string[]
  item_no: number
  archive_no: string | null
  title: string
  deleted: boolean
  now: { item_no: number; archive_no: string | null; title: string; status: ItemStatus } | null
  reasons: ExportChangeReason[]
}

/** `GET /api/export-tasks/{id}/changes`（只对 done 的任务）。 */
export interface ExportChanges {
  export_task_id: string
  only_approved: boolean
  item_count: number
  changed_count: number
  /** 每种原因的件数。 */
  reason_counts: Record<string, number>
  /** 有变更、没删除的条目（全部，按导出顺序）："只补交有变更的 n 件"就是它们。 */
  resubmit_item_ids: string[]
  /** 最多 500 件。 */
  items: ExportChangedItem[]
  truncated: boolean
}

/** `GET /api/export-tasks/{id}/download`：预签名地址，浏览器直接从 S3 下载。 */
export interface ExportDownload {
  url: string
  file_name: string
  size: number
  expires_at: string | null
  url_expires_at: string
}
