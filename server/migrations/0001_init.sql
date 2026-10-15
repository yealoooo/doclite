CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- =====================================================================
-- 一、系统与权限（7 张）
-- =====================================================================
CREATE TABLE users (                                    -- 用户
  id            uuid PRIMARY KEY,
  username      text NOT NULL,                          -- 登录名
  display_name  text NOT NULL,                          -- 姓名
  password_hash text NOT NULL,                          -- argon2id
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  last_login_at timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ux_users_username ON users (lower(username));

CREATE TABLE roles (                                    -- 角色
  id          uuid PRIMARY KEY,
  role_code   text NOT NULL UNIQUE,                     -- admin / manager / scanner / reviewer / cataloger，也可以自建
  role_name   text NOT NULL,
  description text,
  is_builtin  boolean NOT NULL DEFAULT false,           -- 内置角色不能删除；admin 的菜单也不能改，防止所有人被锁在外面
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE menus (                                    -- 菜单 = 权限点。由迁移脚本写入，跟着代码版本走，界面上只能勾选
  menu_code   text PRIMARY KEY,                         -- 前端用它生成导航，后端接口也按它鉴权
  parent_code text REFERENCES menus(menu_code),
  menu_name   text NOT NULL,
  menu_type   text NOT NULL CHECK (menu_type IN ('group','menu')),  -- 以后做按钮级权限时增加 'button'
  route_path  text,                                     -- 前端路由，group 为空
  sort_no     int  NOT NULL DEFAULT 0
);

CREATE TABLE role_menus (                               -- 角色能看到哪些菜单
  role_id   uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  menu_code text NOT NULL REFERENCES menus(menu_code) ON DELETE CASCADE,
  PRIMARY KEY (role_id, menu_code)
);

CREATE TABLE user_roles (                               -- 用户有哪些角色；多个角色时菜单取并集
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role_id uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  PRIMARY KEY (user_id, role_id)
);
CREATE INDEX ix_user_roles_role ON user_roles (role_id);

CREATE TABLE user_sessions (                            -- 登录会话：在线数用于许可证席位控制，也可以强制下线
  id           uuid PRIMARY KEY,
  user_id      uuid NOT NULL REFERENCES users(id),
  token_hash   bytea NOT NULL UNIQUE,                   -- sha256(令牌)，库里不存明文令牌
  client_type  text NOT NULL CHECK (client_type IN ('browser','desktop')),
  ip_address   inet,
  user_agent   text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  revoked_at   timestamptz
);
CREATE INDEX ix_user_sessions_user ON user_sessions (user_id) WHERE revoked_at IS NULL;

CREATE TABLE system_settings (                          -- 系统设置：license / install_id / 默认参数 …
  setting_key   text PRIMARY KEY,
  setting_value jsonb NOT NULL,
  updated_by    uuid REFERENCES users(id),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

-- =====================================================================
-- 二、存储配置（1 张）
-- =====================================================================
CREATE TABLE storage_profiles (                         -- 存储配置：S3 兼容存储的连接信息。可以有多个，新内容写到默认那个
  id               uuid PRIMARY KEY,
  profile_name     text NOT NULL,                       -- "SeaweedFS（随系统部署）"
  storage_type     text NOT NULL DEFAULT 's3' CHECK (storage_type IN ('s3')),  -- 目前只支持 S3；以后要加本地目录时再放开
  endpoint         text NOT NULL,                       -- server 访问的地址：http://objectstore:8333（Docker 内网）
  public_endpoint  text,                                -- 工作站访问的地址：http://192.168.1.10:8333；为空时和 endpoint 相同
  bucket           text NOT NULL,
  region           text NOT NULL DEFAULT 'us-east-1',
  path_prefix      text NOT NULL DEFAULT 'doclite/',    -- 统一存储前缀，所有对象都放在它下面；新建存储时默认取系统设置里的值
  access_key       text NOT NULL,
  secret_key_enc   bytea NOT NULL,                      -- 用 .env 里的主密钥加密后存储
  force_path_style boolean NOT NULL DEFAULT true,       -- SeaweedFS 和多数自建 S3 都用 path-style
  url_expire_hours int NOT NULL DEFAULT 24 CHECK (url_expire_hours BETWEEN 1 AND 168),  -- 发给工作站的预签名 URL 有效期
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active','readonly','disabled')),
  is_default       boolean NOT NULL DEFAULT false,      -- 只影响新写入；旧快照照样按自己记录的存储去读
  remark           text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CHECK (NOT is_default OR status = 'active')
);
CREATE UNIQUE INDEX ux_storage_profiles_default ON storage_profiles (is_default) WHERE is_default;  -- 只能有一个默认

-- =====================================================================
-- 三、项目（3 张）
-- =====================================================================
CREATE TABLE projects (                                 -- 项目：一次数字化工作，自带归类方案、档号规则、著录项
  id           uuid PRIMARY KEY,
  project_code text NOT NULL,
  project_name text NOT NULL,
  description  text,
  settings     jsonb NOT NULL DEFAULT '{}',             -- 自动处理 / 审核 / 导出策略 / 档号模板 / 著录项定义
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
  created_by   uuid NOT NULL REFERENCES users(id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  deleted_at   timestamptz
);
CREATE UNIQUE INDEX ux_projects_code ON projects (project_code) WHERE deleted_at IS NULL;

CREATE TABLE project_members (                          -- 项目授权给哪些人
  project_id uuid NOT NULL REFERENCES projects(id),
  user_id    uuid NOT NULL REFERENCES users(id),
  joined_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, user_id)
);
CREATE INDEX ix_project_members_user ON project_members (user_id);

CREATE TABLE project_roles (                            -- 项目授权给哪些角色：拥有这些角色的用户都能进这个项目
  project_id uuid NOT NULL REFERENCES projects(id),
  role_id    uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, role_id)
);
-- 能不能进某个项目 = 在 project_members 里，或者拥有 project_roles 里的某个角色（系统管理员能进所有项目）
-- 能用哪些菜单，仍然只由全局角色决定

-- =====================================================================
-- 四、文件目录（3 张，含只有 id 的 files）：像电脑一样建文件夹、移动、改名；删除是软删除
-- =====================================================================
CREATE TABLE folders (                                  -- 文件夹
  id              uuid PRIMARY KEY,
  project_id      uuid NOT NULL REFERENCES projects(id),
  parent_id       uuid,                                 -- NULL 表示项目根文件夹（每个项目一个，建项目时自动创建）
  folder_name     text NOT NULL,
  path_code       text COLLATE "C" NOT NULL,            -- 路径编码：从根到自己，每层 5 位数字加一个点，如 '00001.00003.00012.'
  last_child_no   int  NOT NULL DEFAULT 0,              -- 已经分给子文件夹的最大序号；新建子文件夹时加 1，拼出它的编码
  sort_no         int  NOT NULL DEFAULT 0,
  remark          text,                                 -- 备注，比如原件位置：盒号、架位
  created_by      uuid NOT NULL REFERENCES users(id),
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  deleted_at      timestamptz,                          -- 软删除：非空表示已删除（删文件夹时，子文件夹和文件一起标记）
  deleted_by      uuid REFERENCES users(id),
  UNIQUE (id, project_id),
  UNIQUE (project_id, path_code),                       -- 编码不回收：软删除的文件夹继续占着编码，不会和新建的冲突。这个索引同时支撑前缀查询
  CHECK (path_code ~ '^([0-9]{5}\.)+$'),
  FOREIGN KEY (parent_id, project_id) REFERENCES folders (id, project_id)   -- 父文件夹必须在同一个项目
);
CREATE UNIQUE INDEX ux_folders_root ON folders (project_id) WHERE parent_id IS NULL;
CREATE UNIQUE INDEX ux_folders_name ON folders (parent_id, lower(folder_name)) WHERE deleted_at IS NULL;  -- 同级不重名

CREATE TABLE files (                                    -- 文件：只有 id。它就是 file_id，终生不变
  id         uuid PRIMARY KEY,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE folder_files (                             -- 文件：放在哪个文件夹、叫什么名字、审核到哪一步。界面上的"文件"就是这一行
  id              uuid PRIMARY KEY,
  project_id      uuid NOT NULL,
  folder_id       uuid NOT NULL,
  file_id         uuid NOT NULL UNIQUE REFERENCES files(id),  -- 一个文件只在一个位置；复制 = 新建一个 file
  file_name       text NOT NULL,                        -- 显示名，改了不影响归类
  sort_no         int  NOT NULL DEFAULT 0,              -- 上传顺序，也就是纸张的物理顺序
  review_status   text NOT NULL DEFAULT 'unassigned' CHECK (review_status IN ('unassigned','pending','approved','rejected')),
                                                        -- 审核状态：待分配 / 待审核 / 已通过 / 已驳回
  review_round    int  NOT NULL DEFAULT 1,              -- 第几轮：每重扫一次加 1
                                                        -- 审核后仍然可以编辑，状态不变；审的是哪一版记在 file_reviews.snapshot_id
  created_by      uuid NOT NULL REFERENCES users(id),
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  deleted_at      timestamptz,                          -- 软删除：非空表示已删除（已归类的文件要先移出条目才能删）
  deleted_by      uuid REFERENCES users(id),
  FOREIGN KEY (folder_id, project_id) REFERENCES folders (id, project_id),  -- 文件和文件夹必须在同一个项目
  UNIQUE (file_id, project_id)                          -- 供审核、归类做复合外键，保证不会跨项目
);
CREATE UNIQUE INDEX ux_folder_files_name ON folder_files (folder_id, lower(file_name)) WHERE deleted_at IS NULL;
CREATE INDEX ix_folder_files_list   ON folder_files (folder_id, sort_no) WHERE deleted_at IS NULL;
CREATE INDEX ix_folder_files_review ON folder_files (project_id, review_status) WHERE deleted_at IS NULL;  -- 待分配、待审核列表

-- =====================================================================
-- 五、文件快照（1 张）：每个版本的存储信息
-- =====================================================================
CREATE TABLE file_snapshots (                           -- 快照：每个版本一行，记录内容存在哪、是怎么来的。只追加，不修改、不删除
  id                 uuid PRIMARY KEY,
  file_id            uuid NOT NULL REFERENCES files(id),
  version_no         int  NOT NULL,                     -- 1, 2, 3 …；最大的就是当前版本，不另存"当前版本"指针
  snapshot_type      text NOT NULL CHECK (snapshot_type IN ('original','auto','edit','rescan','restore','copy')),
                                                        -- 原始上传 / 自动处理 / 人工修图 / 重扫 / 恢复旧快照 / 复制
  base_snapshot_id   uuid REFERENCES file_snapshots(id),-- 渲染基准：最近一次 original 或 rescan
  parent_snapshot_id uuid REFERENCES file_snapshots(id),-- 在哪个快照的基础上操作的
  edit_ops           jsonb,                             -- 从基准开始的完整操作序列，每一步是一次插件调用 {plugin, version, params}；restore / copy 记录来源快照
  snapshot_name      text,                              -- 命名快照，比如"去污后"
  storage_profile_id uuid NOT NULL REFERENCES storage_profiles(id),  -- 存在哪个存储
  storage_key        text NOT NULL,                     -- 原图：<前缀><项目编码>/<file_id>/v<版本号>.jpg，比如 doclite/WS2025/0199c3…/v2.jpg
                                                        --   按 ID 存放：拖动、改名只改数据库，对象不动。key 生成后就固定，以后项目编码改了也不影响
                                                        --   恢复快照、复制文件时直接指向已有对象的 key，不复制字节
  cover_key          text,                              -- 封面图（长边 400px，WebP）：和原图放在一起，<前缀><项目编码>/<file_id>/v<版本号>.cover.webp
                                                        --   上传时由 server 生成，和原图存在同一个存储里；为空表示没生成出来（显示占位图）
                                                        --   列表里显示它；打开大图时先用它占位，原图加载完再替换
                                                        --   原图是 TIFF 时浏览器不能直接显示，由前端解码（UTIF.js），不另存副本
                                                        -- 三个 key 都是完整的对象 key（已含前缀）：工作站 URL = public_endpoint / bucket / key + 预签名参数
  content_sha256     bytea NOT NULL,
  file_size          bigint NOT NULL,
  mime_type          text NOT NULL,                     -- image/jpeg | image/tiff | image/png
  upload_name        text,                              -- original / rescan 时上传的原始文件名
  width_px           int,                               -- 以下四项上传时从文件头读出，不解码整张图
  height_px          int,
  dpi                int,                               -- 低于 200 时给提示（参照 DA/T 31）
  color_mode         text CHECK (color_mode IN ('bw','gray','color')),
  -- 第一版入库不做自动分析；以后做成插件（空白页、疑似重复、倾斜等提示）时，再补 qc_hints、phash 这类字段
  created_by         uuid REFERENCES users(id),         -- NULL 表示系统自动处理
  created_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (file_id, version_no),
  UNIQUE (id, file_id)                                  -- 供复合外键：引用的快照必须属于同一个文件
);
CREATE INDEX ix_file_snapshots_object ON file_snapshots (storage_profile_id, storage_key);  -- 彻底删除前检查 key 是否还有人用
CREATE INDEX ix_file_snapshots_sha    ON file_snapshots (content_sha256);                  -- 上传查重

-- =====================================================================
-- 六、审核（1 张）
-- =====================================================================
CREATE TABLE file_reviews (                             -- 文件审核：项目负责人把文件分配给审核员，一行就是一次分配和它的结果
  id             uuid PRIMARY KEY,
  project_id     uuid NOT NULL,
  file_id        uuid NOT NULL,
  reviewer_id    uuid NOT NULL REFERENCES users(id),    -- 审核人
  assigned_by    uuid NOT NULL REFERENCES users(id),    -- 分配人（项目负责人）
  assigned_at    timestamptz NOT NULL DEFAULT now(),
  status         text NOT NULL DEFAULT 'open' CHECK (status IN ('open','done','cancelled')),  -- 进行中 / 已完成 / 已取消（改派给别人）
  result         text CHECK (result IN ('approved','rejected')),   -- 审核结果
  snapshot_id    uuid,                                  -- 审核时看的是哪个快照（审核后文件还可以继续编辑）
  reject_reasons text[] NOT NULL DEFAULT '{}',          -- blur 模糊 / skew 倾斜 / cut 裁切过度 / stain 污渍 / missing 缺页 / dup 重页 / orient 方向 / other
  comment        text,
  reviewed_at    timestamptz,
  FOREIGN KEY (file_id, project_id)  REFERENCES folder_files (file_id, project_id),
  FOREIGN KEY (snapshot_id, file_id) REFERENCES file_snapshots (id, file_id),
  CHECK ((status = 'done') = (result IS NOT NULL AND snapshot_id IS NOT NULL AND reviewed_at IS NOT NULL))
);
CREATE UNIQUE INDEX ux_file_reviews_open ON file_reviews (file_id) WHERE status = 'open';  -- 一个文件同时只分配给一个人
CREATE INDEX ix_file_reviews_reviewer ON file_reviews (reviewer_id, status);             -- 审核员的待办列表
CREATE INDEX ix_file_reviews_file     ON file_reviews (file_id, assigned_at);

-- =====================================================================
-- 七、归类（3 张）：归类树和文件目录是两棵独立的树，用同一套路径编码规则
-- =====================================================================
CREATE TABLE archive_categories (                       -- 归类树：全宗 / 门类 / 年度 / 保管期限 / 机构 …，层级不固定
  id            uuid PRIMARY KEY,
  project_id    uuid NOT NULL REFERENCES projects(id),
  parent_id     uuid,                                   -- NULL 表示项目的归类树根节点（建项目时自动创建，界面上不显示）
  path_code     text COLLATE "C" NOT NULL,              -- 路径编码，规则和文件夹相同：'00001.00002.00005.'
  last_child_no int  NOT NULL DEFAULT 0,
  category_type text NOT NULL,                          -- root / fonds / category / year / retention / org / custom，档号模板按它取值
  category_code text NOT NULL,                          -- 业务编码，拼进档号：Z109 / WS / 2025 / Y / BGS（和 path_code 是两回事）
  category_name text NOT NULL,
  sort_no       int  NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now(),
  deleted_at    timestamptz,
  UNIQUE (id, project_id),
  UNIQUE (project_id, path_code),
  CHECK (path_code ~ '^([0-9]{5}\.)+$'),
  FOREIGN KEY (parent_id, project_id) REFERENCES archive_categories (id, project_id)
);
CREATE UNIQUE INDEX ux_archive_categories_root ON archive_categories (project_id) WHERE parent_id IS NULL;
CREATE UNIQUE INDEX ux_archive_categories_code ON archive_categories (project_id, parent_id, category_code)
  NULLS NOT DISTINCT WHERE deleted_at IS NULL;

CREATE TABLE archive_items (                            -- 档案条目：案卷（volume）或 件（item）
  id             uuid PRIMARY KEY,
  project_id     uuid NOT NULL REFERENCES projects(id),
  category_id    uuid NOT NULL,
  parent_item_id uuid REFERENCES archive_items(id),     -- 卷内件指向所属案卷
  item_level     text NOT NULL CHECK (item_level IN ('volume','item')),
  item_no        int  NOT NULL,                         -- 件号 / 卷号（在分类内的顺序）
  archive_no     text,                                  -- 档号：按模板生成，也允许手改
  title          text NOT NULL DEFAULT '',              -- 题名
  extra_fields   jsonb NOT NULL DEFAULT '{}',           -- 其他著录项（字段定义在项目设置里）：责任者 / 文号 / 日期 / 密级 …
  status         text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','done','locked')),  -- 草稿 / 著录完成 / 定稿
  row_version    int  NOT NULL DEFAULT 1,               -- 乐观锁
  created_by     uuid NOT NULL REFERENCES users(id),
  updated_by     uuid REFERENCES users(id),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  deleted_at     timestamptz,
  UNIQUE (id, project_id),
  FOREIGN KEY (category_id, project_id) REFERENCES archive_categories (id, project_id)
);
CREATE UNIQUE INDEX ux_archive_items_no ON archive_items (project_id, archive_no) WHERE archive_no IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX ix_archive_items_category ON archive_items (category_id, item_no) WHERE deleted_at IS NULL;
CREATE INDEX ix_archive_items_fields   ON archive_items USING gin (extra_fields jsonb_path_ops);
CREATE INDEX ix_archive_items_title    ON archive_items USING gin (title gin_trgm_ops);

CREATE TABLE archive_item_files (                       -- 条目挂接的文件：只存 file_id，不管快照，也不管文件在哪个文件夹
  project_id      uuid NOT NULL,
  archive_item_id uuid NOT NULL,
  file_id         uuid NOT NULL,
  page_no         int  NOT NULL,                        -- 条目内的页序
  page_label      text,                                 -- 页号，为空时按 page_no；也支持"3-背"
  linked_by       uuid NOT NULL REFERENCES users(id),
  linked_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (archive_item_id, file_id),
  UNIQUE (file_id),                                     -- 一页只能归到一个条目
  CONSTRAINT ux_archive_item_page UNIQUE (archive_item_id, page_no) DEFERRABLE INITIALLY DEFERRED,  -- 整体重排时到事务末尾才检查
  FOREIGN KEY (archive_item_id, project_id) REFERENCES archive_items (id, project_id),
  FOREIGN KEY (file_id, project_id)         REFERENCES folder_files (file_id, project_id)
);

-- =====================================================================
-- 八、导出（2 张）：只能从归类发起，只导出已归类的文件
-- =====================================================================
CREATE TABLE export_tasks (                             -- 导出任务
  id                 uuid PRIMARY KEY,
  project_id         uuid NOT NULL REFERENCES projects(id),
  scope_type         text NOT NULL CHECK (scope_type IN ('category','item')),  -- 按归类树节点，或者按条目
  scope_ids          uuid[] NOT NULL,                   -- archive_categories.id 或 archive_items.id
  export_format      text NOT NULL CHECK (export_format IN ('pdf','pdf_ocr','ofd','images')),
  only_approved      boolean NOT NULL DEFAULT true,     -- 用户导出时勾选：只导出审核通过的文件；每页都用当前最新的快照
  status             text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','done','failed','canceled')),
  job_id             uuid,
  storage_profile_id uuid REFERENCES storage_profiles(id),  -- 导出物存在哪
  output_key         text,
  output_size        bigint,
  created_by         uuid NOT NULL REFERENCES users(id),
  created_at         timestamptz NOT NULL DEFAULT now(),
  finished_at        timestamptz,
  expires_at         timestamptz                        -- 到期清理导出物，明细保留
);
CREATE INDEX ix_export_tasks_project ON export_tasks (project_id, created_at DESC);

CREATE TABLE export_task_files (                        -- 导出明细：每一页当时用的是哪个快照
  export_task_id  uuid NOT NULL REFERENCES export_tasks(id),
  archive_item_id uuid NOT NULL REFERENCES archive_items(id),
  file_id         uuid NOT NULL,
  snapshot_id     uuid NOT NULL,
  page_no         int  NOT NULL,
  PRIMARY KEY (export_task_id, file_id),
  FOREIGN KEY (snapshot_id, file_id) REFERENCES file_snapshots (id, file_id)
);
CREATE INDEX ix_export_task_files_file ON export_task_files (file_id);

-- =====================================================================
-- 九、后台任务 · OCR · 日志（4 张）
-- =====================================================================
CREATE TABLE background_jobs (                          -- 后台任务队列：由 server 进程里的任务线程领取执行
  id             uuid PRIMARY KEY,
  project_id     uuid REFERENCES projects(id),
  job_type       text NOT NULL,                         -- 第一版只有 export 导出；以后加 storage_migrate 存储迁移
  payload        jsonb NOT NULL,
  status         text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','done','failed','canceled')),
  priority       smallint NOT NULL DEFAULT 0,           -- 数字大的先执行
  attempts       smallint NOT NULL DEFAULT 0,
  max_attempts   smallint NOT NULL DEFAULT 3,
  run_after      timestamptz NOT NULL DEFAULT now(),
  runner_id      text,                                  -- 哪个进程领走的（以后拆出独立 worker 也用这一列）
  heartbeat_at   timestamptz,                           -- 超时没有心跳就回收重试
  progress_done  int NOT NULL DEFAULT 0,
  progress_total int NOT NULL DEFAULT 0,
  error_message  text,
  created_by     uuid REFERENCES users(id),
  created_at     timestamptz NOT NULL DEFAULT now(),
  started_at     timestamptz,
  finished_at    timestamptz
);
CREATE INDEX ix_background_jobs_ready ON background_jobs (priority DESC, run_after) WHERE status = 'queued';

CREATE TABLE ocr_results (                              -- OCR 结果（后续版本启用）：按内容缓存，同一 sha256 只识别一次
  content_sha256     bytea NOT NULL,
  ocr_engine         text NOT NULL,                     -- 'rapidocr/pp-ocrv6@x.y'
  full_text          text NOT NULL,                     -- 全文检索用（二期）
  storage_profile_id uuid NOT NULL REFERENCES storage_profiles(id),
  layout_key         text NOT NULL,                     -- 文字框坐标文件，生成双层 PDF / OFD 时用
  created_at         timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (content_sha256, ocr_engine)
);

CREATE TABLE file_logs (                                -- 文件操作日志：每个文件发生过什么，一目了然
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  project_id  uuid NOT NULL,
  file_id     uuid NOT NULL REFERENCES files(id),
  action      text NOT NULL CHECK (action IN (
                'upload','rescan','rename','move','copy','delete',        -- 上传 / 重扫 / 改名 / 移动 / 复制 / 删除
                'assign_review','reassign_review','approve','reject','reopen',  -- 分配审核 / 改派 / 通过 / 驳回 / 撤回通过
                'link','unlink','export',                                 -- 挂接到条目 / 移出条目 / 被导出
                'edit','restore')),                                       -- 插件阶段：修图 / 恢复旧快照（审核后也可以编辑）
  operator_id uuid NOT NULL REFERENCES users(id),
  detail      jsonb NOT NULL DEFAULT '{}',              -- 当时的关键信息，名字也存一份：{"reviewer":"张审核"}、{"from":"盒 003","to":"盒 004"}、{"reasons":["blur"]}
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_file_logs_file    ON file_logs (file_id, id);
CREATE INDEX ix_file_logs_project ON file_logs (project_id, id);

CREATE TABLE audit_logs (                               -- 其他操作日志：文件夹、归类条目、导出任务、用户、角色、存储的变更
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  project_id  uuid,
  operator_id uuid REFERENCES users(id),                -- NULL 表示系统
  target_type text NOT NULL,                            -- folder / archive_item / export_task / role / user / storage_profile …
  target_id   uuid NOT NULL,
  action      text NOT NULL,
  detail      jsonb NOT NULL DEFAULT '{}',              -- 变更前后的差异
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ix_audit_logs_target  ON audit_logs (target_type, target_id, id);
CREATE INDEX ix_audit_logs_project ON audit_logs (project_id, id);

-- =====================================================================
-- 初始数据：菜单和内置角色
-- =====================================================================
INSERT INTO menus (menu_code, parent_code, menu_name, menu_type, route_path, sort_no) VALUES
  ('dashboard',       NULL,     '项目看板',   'menu',  '/dashboard',       10),
  ('files',           NULL,     '文件目录',   'menu',  '/files',           20),
  ('scan',            NULL,     '扫描上传',   'menu',  '/scan',            30),
  ('review',          NULL,     '我的审核',   'menu',  '/review',          40),
  ('review_assign',   NULL,     '审核分配',   'menu',  '/review-assign',   45),
  ('catalog',         NULL,     '归类',       'menu',  '/catalog',         50),
  ('export',          NULL,     '导出',       'menu',  '/export',          60),
  ('system',          NULL,     '系统管理',   'group', NULL,               90),
  ('system.users',    'system', '用户',       'menu',  '/system/users',    91),
  ('system.roles',    'system', '角色与权限', 'menu',  '/system/roles',    92),
  ('system.projects', 'system', '项目',       'menu',  '/system/projects', 93),
  ('system.storage',  'system', '存储配置',   'menu',  '/system/storage',  94),
  ('system.plugins',  'system', '插件能力',   'menu',  '/system/plugins',  95),  -- 只读：看当前版本带了哪些插件
  ('system.license',  'system', '许可证',     'menu',  '/system/license',  96),
  ('system.audit',    'system', '审计日志',   'menu',  '/system/audit',    97);

INSERT INTO roles (id, role_code, role_name, is_builtin) VALUES
  (gen_random_uuid(), 'admin',     '系统管理员', true),
  (gen_random_uuid(), 'manager',   '项目负责人', true),
  (gen_random_uuid(), 'scanner',   '扫描员',     true),
  (gen_random_uuid(), 'reviewer',  '审核员',     true),
  (gen_random_uuid(), 'cataloger', '归类员',     true);

INSERT INTO role_menus (role_id, menu_code)
SELECT r.id, m.menu_code FROM roles r JOIN menus m ON m.menu_type = 'menu' AND (
     r.role_code = 'admin'
  OR (r.role_code = 'manager'   AND m.menu_code IN ('dashboard','files','scan','review','review_assign','catalog','export'))
  OR (r.role_code = 'scanner'   AND m.menu_code IN ('files','scan'))
  OR (r.role_code = 'reviewer'  AND m.menu_code IN ('files','review'))
  OR (r.role_code = 'cataloger' AND m.menu_code IN ('files','catalog')));
