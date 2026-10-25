# Doclite 需求说明（Go + Wails 重写用）

> 依据 Doclite v3（Rust 版）的 design.md（最权威）、roadmap.md、CLAUDE.md、迁移 SQL、M1–M3 报告，截至 2026-10-15。只写做什么、规则、用户定过什么，不写原版实现。
> 标记：**【用户 日期】** 用户决定；**【总控】** 开发中代定、用户确认里程碑时没反对；**（可简化：…）** 原版为多实例、罕见格式、极端故障做的处理，重写按单台服务器、常见图片格式可简化，规则保留；**（待确认）** 开工前问用户。

## 1. 总览

### 1.1 用途和核心流程

纸质档案数字化，对照 DA/T 31（"归类"即"数据挂接"）。全新系统，不兼容旧版、不迁移旧数据。

1. **上传**：扫描员经客户端（热文件夹，或文件目录里的上传按钮）上传，进文件目录，状态"待分配"。
2. **分配审核**：负责人按文件夹或选中的文件分给审核员，可改派。
3. **审核**：逐页看原图，通过或驳回（带原因）。驳回 → 重扫（同一 file_id 追加版本，轮次 +1）→ 自动回原审核员 → 对比上一轮再审。
4. **归档**（和审核并行，互不限制）：建归类树；**整个文件夹归档**——把盒子（或几个子文件夹）放到归类节点上，每个子文件夹生成一件，文件夹里的文件和顺序就是页；归档后文件夹**封存**；著录、生成档号、锁定。要改页：解除封存 → 在文件目录里改 → 重新归档【用户 2026-10-19，见 5.9】。
5. **导出**：只从归类树发起；预检 → 后台生成 zip（每件一个 PDF 或原图包）→ 浏览器直连 S3 下载；之后的变化列为"交付后变更"，可只补交变了的件。

两棵树：**文件目录**按物理来源组织（"2025 文书档案 / 盒 WS-2025-003 / 关于…的通知"），由扫描员像电脑文件夹一样整理；**归类树**是档案逻辑（全宗 → 门类 → 年度 → 保管期限 → 机构 → 条目（件）→ 页序），决定档号和著录。**数据单向流动**：文件目录整理好 → 整个文件夹归档进归类树，件记住来源文件夹、页存 file_id；归档后来源文件夹封存，归类树这边不改页。两棵树同一结构（parent_id + path_code），共用树组件。

### 1.2 部署

- 客户：Windows 服务器，WSL2 + Docker Engine，内网不联网，离线交付包。
- 容器：web（nginx，托管界面并把 `/api` 反向代理给 server）、server（9080，只提供 `/api`）、PostgreSQL 18、SeaweedFS（`weed mini`，S3 8333，工作站直连看图）、可选 Portainer（只绑 127.0.0.1）。卷 pg、s3、tmp（磁盘，不用 tmpfs）；PG 18 镜像数据目录挂 `/var/lib/postgresql`。
- 启动时自动迁移；升级 = `docker load` + `docker compose up -d`。
- 只支持 S3 兼容存储（SeaweedFS 生产 4.45、开发 4.15）。数据库只用 PG 16 也有的特性，主键由应用生成 UUIDv7。
- 重写按**单台服务器、单个 server 进程**设计。

### 1.3 客户端

- **三个独立模块，代码上互不依赖**【用户 2026-10-16，为以后拆仓库】：
  - **server**：只提供 `/api`，不托管网页；
  - **web**：界面的源码，独立开发（vite，把 `/api` 代理给 server）。**交付不提供网页服务、不提供浏览器访问**【用户 2026-10-17：交付就是客户端】：web 只用来构建带版本号的产物包，嵌进客户端；vite、截图脚本里的浏览器只是开发和测试工具；
  - **client**：桌面壳，**界面随客户端打包，版本跟客户端走**【用户 2026-10-16，参照 Element：网页版和桌面版共用界面，桌面版打包指定版本的网页产物；当天先试过"界面跟 web 服务走"，因客户端本机权限大、页面来自网络时信任边界难守、6Mbps 慢网下首屏慢而改回】。client 不依赖 web 源码：web 构建出带版本号的产物包，client 记下要用的 web 版本（`client/WEB_VERSION`），打包时取这个产物嵌进去。客户端配置的"服务器地址"是**服务端接口的地址**（开发时 `http://127.0.0.1:9080`；部署时服务端端口或 nginx 入口都行，客户端只访问 `/api`），客户端不依赖网页服务【用户 2026-10-17 确认】。只有随客户端发布的页面能调桥。改界面要升级客户端：服务端的最低客户端版本 + 客户端自动更新（M8）；接口只增不改，兼容旧一版客户端。
  - 模块之间只靠约定：HTTP 接口（第 8 节）和壳的桥命令（客户端里的约定文档）。没有共用代码；两边都要的（读图片文件头）各留一份，上传拒收以 server 为准。
- 只有壳有：热文件夹、上传队列、本机图片缓存、大图切块、托盘。
- 浏览器经内网 http 访问不是安全上下文，不能用 `crypto.subtle`、`crypto.randomUUID`。
- 前端技术栈由重写会话定（原版 Vue 3 + TS）。

### 1.4 进度和性能目标

- 上一版（Rust）M0–M3 用户已确认【用户 2026-10-14】；Go 重写版 M1–M7 已完成（2026-10-16）；打包交付（M8）等用户在自己电脑上验证后再定【用户 2026-10-16】；插件和二期不排期。
- 审核和归类同时处理同一文件夹：不互相拒绝、不死锁、等待 < 1 秒【用户 2026-10-14】。
- 10 万文件：审核读接口 < 200 ms，看板 < 200 ms，500 个文件的"挂在哪" < 100 ms。导出 2000 件 / 5 万页：预检、建任务、交付后变更各 < 1 秒（建任务本机实测 0.97–1.04 秒，用户 2026-10-16 接受、正式硬件重测）；单任务 ≤ 20 万页。
- 另一个窗口 3 秒内看到变化。server 内存不随文件大小、页数增长。

## 2. 硬性规则（违反就是 bug）

1. **file_id 是锚。** files 只有 id，其他模块都用 file_id 引用文件，终生不变。
2. **快照只追加。** file_snapshots 不改不删，当前版本 = max(version_no)。恢复旧版 = 追加快照、复用旧 key，不复制字节。
3. **文件目录只管位置和名字。** 移动、改名只改 folders、folder_files；对象、快照、审核、归类不动。**已封存的文件夹**（是某个未删除条目的来源、且不在"修改中"）不能改结构，见 5.5【用户 2026-10-19】。
4. **只能整个文件夹归档。** 件记住来源文件夹（`archive_items.source_folder_id`，一个文件夹只对应一件）；页 = 归档时文件夹里的文件，挂接只存 file_id、不存快照；归类员永远看到最新快照。归类树这边不能单独挂页、移页、排页序【用户 2026-10-19，取代 2026-10-14 Q3、Q4 的按页挂接和待归类池】。
5. **审核记下审的快照，但不锁定。** 审后仍可编辑（插件阶段），状态不变。
6. **导出只从归类发起。** 范围是归类节点或条目，只含已归类文件；"只导出审核通过的"每次勾选。
7. **S3 key 按 ID。** `<path_prefix><project_code>/<file_id>/v<no>.<jpg|png|tif>`（按文件头格式），封面 `v<no>.cover.jpg`；移动时 key 不变。
8. **树用 parent_id + path_code。** 每层 5 位数字加点（`00001.00003.`），字节序比较；编码由父节点 last_child_no 原子分配、不回收；移动前按前缀判环。
9. **软删除。** 只写 deleted_at，不做回收站；文件操作写 file_logs，其他写 audit_logs。
10. **权限只到菜单级。** 接口声明菜单码，中间件校验，无权 403；角色全局，进项目靠项目授权，admin 进所有项目。
11. **server 不做图像处理。** 只读文件头，不解码、不修改、不 OCR、不分析；封面由客户端生成（长边 400 的 JPEG），server 只校验。只约束 server，壳可以解码。
    - **唯一例外**【用户 2026-10-15】：导出 PDF 时，能原样嵌入的页原样嵌入；放不进的（带透明的 PNG、隔行 PNG、TIFF 分平面、少见压缩）由 server 无损转换，像素不变。只在导出模块。
12. **server 不缓存图片。** 工作站用预签名 URL 直连 S3，签名时间按天取整；本机缓存由壳按 file_id + snapshot_id 存。
13. **后台任务在 server 进程内**，background_jobs + `FOR UPDATE SKIP LOCKED` 领取；v1 只有导出和存储迁移（M7-2，用户 2026-10-16）。
14. **大文件流式。** 上传边写临时文件边算 sha256，不把请求体读进内存。
15. **命名**：表名英文复数、关联表两实体拼接；主键 id、外键 `单数表名_id`；名称带前缀（folder_name）；`status`、`xxx_type`、`xxx_no`、`xxx_at`。
16. **改设计先问用户**（改表、改接口约定、冲突本节规则、装软件）。改表只新增迁移 `000N_xxx.sql`。

## 3. 数据模型

### 3.1 建表 SQL 直接沿用

- `/Users/yanlong/project/doclite-project/bak/doclite-v3/server/migrations/0001_init.sql`：25 张表 + 菜单和内置角色初始数据，约束在 PG 16 验证过，字段注释即说明；需要 `pg_trgm`。
- 同目录 `0002_review_indexes.sql`（我的审核的部分索引）、`0003_item_no_index.sql`（已删除条目的件号索引）。
- **`0004`（2026-10-19，整个文件夹归档）**：archive_items 加 `source_folder_id`（引用 folders，未删除条目里唯一的部分索引）、`source_status`（sealed / modifying）、`unsealed_by`、`unsealed_at`、`unseal_reason`；status 的 done 改成 draft，CHECK 改为 draft / locked。**不处理旧数据**【用户 2026-10-19：旧数据不用管，开发库直接重建】：开发库 `doclite_dev` 删掉重建，再用精简的演示数据脚本造数据（只要够功能测试，见 roadmap M9-3）。
- **`0005_perf_indexes.sql`（M10-1，只加索引）**：`archive_item_files (project_id, file_id) INCLUDE (archive_item_id)`（看板按项目取挂接）、`file_reviews (project_id, reviewer_id) INCLUDE (file_id) WHERE status = 'open'`（可以分配的人、改派列表按项目取进行中的分配）。
- **`0006_drop_plugins_menu.sql`（M10-7）**：删掉菜单 `system.plugins`（只有占位页），角色授权随外键级联删除。
- **`0007_modifying_audit_indexes.sql`（M11-1，只加索引，用户 2026-10-25 同意）**：`archive_items (project_id, unsealed_at DESC NULLS LAST, id) WHERE source_status = 'modifying' AND deleted_at IS NULL`（修改中的件）、`audit_logs USING brin (created_at)`（审计日志按时间筛选，直接按 created_at 过滤）。
- 参考 SQL：`/Users/yanlong/project/doclite-project/bak/doclite-v3/docs/sql/key-queries.sql`（查重、path_code 分配、移动、分配、通过、软删除、导出预检、任务领取等）。
- 0001 中 `cover_key` 注释"WebP、server 生成"已过时。预留不写：快照类型 auto/edit/restore、案卷（volume）、page_label、pdf_ocr/ofd、ocr_results。

### 3.2 25 张表

| 表 | 用途 |
|---|---|
| users | 用户；登录名不分大小写唯一；只停用不删 |
| roles | 角色；5 个内置，可自建；role_code 不可改 |
| menus / role_menus / user_roles | 菜单（迁移写入）、角色的菜单、用户的角色 |
| user_sessions | 会话：令牌 sha256、client_type、到期、撤销 |
| system_settings | 许可证、install_id 等（M5） |
| storage_profiles | S3 存储配置，一个默认，密钥加密 |
| projects | 项目：编码、名称、settings JSON、active/archived |
| project_members / project_roles | 项目授权给人、给角色 |
| folders | 文件夹树，remark 记原件位置 |
| files | 只有 id = file_id |
| folder_files | 界面上的"文件"：文件夹、名字、sort_no、审核状态、轮次；file_id 唯一 |
| file_snapshots | 版本：存储、key、封面 key、sha256、大小、mime、宽高、DPI、颜色、upload_name、类型 |
| file_reviews | 一次分配和结果：审核人、分配人、open/done/cancelled、结果、所审快照、原因、说明 |
| archive_categories | 归类树：类型、业务编码、名称 |
| archive_items | 条目（件）：节点、件号、档号、题名、extra_fields、draft（未锁定）/locked（已锁定）、row_version；来源文件夹、封存状态 sealed/modifying 和解除封存的人、时间、原因（迁移 0004） |
| archive_item_files | 挂接：条目、file_id（唯一）、页号 |
| export_tasks / export_task_files | 导出任务；每页当时的快照和页号 |
| background_jobs | 任务队列：payload、状态、尝试、心跳、进度 |
| ocr_results | 二期 |
| file_logs / audit_logs | 文件日志；其他变更审计 |

### 3.3 原则

- **path_code**：根文件夹和归类根都是 `00001.`，每项目一个根；子编码 = 父编码 + 父 last_child_no 原子 +1 的 5 位数 + 点；不回收；每层 ≤ 99999（超出 409 `tree_full`）；子树按前缀区间查，按 path_code 排序即先序。移动：拿锁 → 目标以自己编码开头 409 `tree_cycle` → 新父下领编码 → 一条 UPDATE 换子树前缀。归类的 `category_code`（拼档号）和 path_code 无关。
- **软删除**：文件、文件夹、项目、节点、条目；删文件夹时子树一起；重名和唯一只看未删除的。
- **两种状态独立**：审核状态 = `folder_files.review_status`（unassigned/pending/approved/rejected）；归档状态 = 有无挂接行（条目 locked = 已锁定）。不变量：已删除的条目没有挂接行、不占来源文件夹。
- **文件夹的归档状态**（两边显示同一个）：未归档（不是任何未删除条目的来源）/ 已归档 · 未锁定（sealed + draft）/ 已归档 · 已锁定（sealed + locked）/ 修改中（modifying，一定是 draft）。不变量：sealed 的条目，页 = 来源文件夹里未删除的文件（按 sort_no、名称），由封存检查保证，不靠事后比对。
- 一个文件同时只有一行 open 分配，只在一个条目里；一个文件夹只是一个未删除条目的来源；项目内未删除条目档号唯一。
- 日志 detail 存当时的名字和从项目根起的文件夹路径，只看日志能还原一个文件的全过程。
- 乐观锁：条目 row_version；项目 updated_at。

### 3.4 projects.settings（读时补默认；写时整体替换，只校验已知键，保留未知键，≤ 64 KiB）

| 键 | 含义 |
|---|---|
| `review.reject_reasons` | 默认 blur 模糊、skew 倾斜、cut 裁切过度、stain 污渍、missing 缺页、dup 重页、orient 方向、other 其他；内置存代码、界面译中文，自定义的存中文；至少一个，空列表按默认 |
| `review.rescan_reassign` | 只有 `same_reviewer` |
| `archive_no.template` | 如 `{fonds}-{category}·{year}-{retention}-{org}-{item_no:04}` |
| `item_fields` | `[{key, label, type: text/date/enum, required, options, max_length, default}]` |
| `catalog.lock_requires_approved` | 锁定要求全部页通过，默认 true |
| `export.require_locked_item` | 导出要求已锁定，默认 true |
| `export.retention_days` | 导出物保留天数，默认 7，1–365 |

### 3.5 日志

- file_logs action：upload、rescan、rename、move、copy、delete、assign_review、reassign_review、approve、reject、reopen、link、unlink、export（edit、restore 留给插件）；审核动作带 version_no、review_round。
- 改名、移动文件夹只写审计；删文件夹每个文件记 delete【用户 2026-09-27】。
- audit_logs target：user、role、storage_profile、project、folder、archive_category、archive_item、export_task；detail `{before, after}` 只放变了的。空修改不写库、不审计、不发事件。登录退出不审计。
- 归档相关的 archive_item 审计 action：archive（归档，带来源文件夹路径和页数）、unseal（解除封存，带原因）、reseal（重新归档，带新增、去掉、调序的页）、lock、unlock、delete（撤销归档）；每页照旧写 file_logs link / unlink。

### 3.6 并发和加锁

- **项目文件树锁**（每项目一把）：共享 = 建或改名文件夹、上传、复制、移动文件、重扫、删单个文件、审核写、改项目；排他 = 移动文件夹、删子树、删项目。
- **项目归类树锁**（每项目一把）：共享 = 建或改名节点、条目增改移删和状态、挂接、项目改名；排他 = 移动或删节点子树、改 category_code/type、批量重算档号。
- **顺序**：归类树锁 → 节点行 → 条目行（id 升序）→ 文件树锁 → 项目行 → files 行（id 升序）→ folders 行 → 存储配置行 → 内容锁。**例外**（M7-2）：复制在内容锁里重读源快照，发现它刚被存储迁移改到别的存储时，在内容锁之后再对那个存储配置行拿 FOR KEY SHARE（和插入快照时外键检查拿的锁一样）；不会成环，因为对存储配置行拿 FOR UPDATE 的只有设默认、停用、删除，它们不拿也不等内容锁、树锁、files 行、folders 行。存储迁移改快照位置的事务：job 行 → 存储配置行（FOR KEY SHARE）→ 内容锁 → 快照行。
- **审核不等归类**：审核不拿归类锁、不锁条目；拿着文件树锁的事务不等归类的锁和行；挂接、项目改名这类两棵树都要的操作，先拿归类锁和归类行，最后拿文件树锁【总控】。
- **files 行是每个文件的写锁**：分配、改派、审核、撤回、重扫、删除、挂接、移出都先锁它，锁后重读 folder_files；所以挂接和删除已归类文件互斥。
- **封存检查和归档读文件清单要互斥**【总控】：归档、重新归档读来源文件夹的文件清单，和往这个文件夹上传、移入移出、复制进去、删除不能交错（否则可能"刚归档完又进来一个文件"）。**做法（M9-1）**：两边都锁同一个文件夹行（FOR NO KEY UPDATE）——文件目录的写在"文件树共享锁 → files 行 → folders 行"之后锁住要改的文件夹再查封存；归档、重新归档在"归类树锁 → 节点或条目行 → 文件树共享锁 → 来源文件夹行（id 升序）"之后读清单，**之后不再锁 files 行**（否则和 files → folders 的顺序成环）；档号咨询锁 6005 在文件夹行之后按键排序一次拿全。catalog 直接锁 folders 行是模块边界的例外（folder 依赖 catalog，反过来调不了）。并发测试"同一文件夹同时归档和上传"。
- 行锁用不挡外键检查的 `FOR NO KEY UPDATE`，例外只有任务领取的 `SKIP LOCKED`。
- **等锁超时**【总控 M10-1】：每个连接 `lock_timeout = 30s`（`DATABASE_URL` 里可以改），等锁超过就报错，接口返回 503 `busy`（"有别的操作正在进行，请稍后再试"），不无限地等；不设 `statement_timeout`（导出要读很久）。连接池没写 `pool_max_conns` 时上限 20。
- **内容锁**：上传、重扫按（项目, sha256）加锁、锁内复查重。建导出任务不拿树锁，在一致快照里只插新行。事件提交后才发。
- （可简化：内容锁、任务领取锁、存储配置表锁、重扫按文件的锁可用进程内互斥；但"同内容并发只入库一个""同一文件重扫串行"必须保证。）

## 4. 权限和角色

菜单码：dashboard 项目看板、files 文件目录、scan 扫描上传、review 我的审核、review_assign 审核分配、catalog 归类、export 导出；system 组下 system.users 用户、system.roles 角色与权限、system.projects 项目、system.storage 存储配置、system.license 许可证、system.audit 审计日志（原来的 system.plugins 插件能力占位页由迁移 0006 去掉，用户 2026-10-19）。路由 `/dashboard`、`/files`、`/scan`、`/review`、`/review-assign`、`/catalog`、`/export`、`/system/*`。

| 内置角色 | 菜单 | 职责 |
|---|---|---|
| admin 系统管理员 | 全部（锁定） | 系统管理，进所有项目 |
| manager 项目负责人 | dashboard files scan review review_assign catalog export | 分配改派、看进度、导出；默认不能建项目 |
| scanner 扫描员 | files scan | 建文件夹、上传、重扫（整理文件目录） |
| reviewer 审核员 | files review | 审分给自己的 |
| cataloger 归类员 | files catalog | 归档、解除封存、重新归档、著录、锁定、解锁 |

- 多角色菜单取并集，`GET /api/me/menus` 生成导航和路由守卫；有 `system.*` 时显示"系统管理"入口，/system 侧栏换成系统菜单并能返回业务界面。
- 接口一般"有其中一个菜单即可"；**归类的全部操作（含解锁）只要 catalog + 能进项目**，不另设负责人、不加字段【用户 2026-10-19，取代 2026-10-14"解锁要 catalog 和 review_assign"】。审核设置、归类设置、归类信息、SSE 只要能进项目；我的项目、图片信息、快照登录即可。
- **项目授权**：在 project_members、有 project_roles 里的角色、或 admin。项目不存在或已删除 404，没授权 403；以资源 id 为参数的接口先反查项目（找不到、已删除、所在文件夹或项目已删除都 404）。
- **检查顺序**：菜单 403 → 项目 404/403 → 参数 400 → 状态 409 → 版本 409 → 内容 400 → 前置条件 409。进不了项目的人带错参数也是 403。
- 内置角色不能删，admin 菜单不能改；迁移新增菜单要同时给 admin。
- **防提权**【用户 2026-09-26】（非 admin 违反 → 403 `admin_required`）：① 授予收回 admin、改持有 admin 的用户须是 admin；② 增减任何角色的 `system.*` 须是 admin；③ 给用户新加的角色里的 `system.*` 须是操作人菜单子集；④ 被改用户现有的 `system.*` 也须是子集。
- `system.projects` 的人建项目和授权（默认只 admin，可加给负责人）【用户 2026-09-27】；能管所有项目、不要求自己能进；建项目的人不自动成为成员。
- 403 code：`forbidden`、`admin_required`、`user_disabled`、`not_assignee`（审的不是自己的；不用 forbidden，免得前端刷新会话【用户 2026-10-07】）。

## 5. 各模块

### 5.1 登录和会话（M0）

- HttpOnly Cookie `doclite_session`（SameSite=Lax，Secure 可配、默认关），`<img>` 也能带鉴权；库里只存令牌 sha256。
- 有效期 `SESSION_TTL_HOURS`（默认 12，1–720），从登录起算、**不续期**【用户 2026-09-27】；last_seen_at 每分钟最多写一次。
- 读 `X-Client-Type`（browser 缺省、desktop，其他 400）。停用用户不能登录（403 `user_disabled`），停用即撤销其会话。库里没有用户时用 `INIT_ADMIN_USERNAME/PASSWORD` 建 admin。登录名 1–64 无空白，显示名 1–64，密码 6–128，argon2id。
- 边界：CSRF 只靠 SameSite=Lax【用户 2026-09-26】；不限速；IP 取 TCP 对端。原版限制密码哈希并发、排队 5 秒 503 `busy`（可简化）。

### 5.2 用户和角色（M0）

- 界面 /system/users（新建、编辑、停用启用、重置密码、改角色）、/system/roles（勾菜单、显示人数）。
- 不能停用自己（409 `cannot_disable_self`）、不能去掉最后一个 admin（409 `last_admin`）。role_code `^[a-z][a-z0-9_]{0,31}$`；内置不能删（409 `builtin_role`）；admin 菜单不能改（409 `admin_menus_locked`）。读角色列表 system.roles 或 system.users 均可。分页 page_size 默认 20、最大 100。

### 5.3 项目和授权（M1）

- 界面 /system/projects：列表、新建编辑（含著录设置）、删除、授权（勾人和角色，标出停用的）。业务界面左上角项目切换器：不显示归档的，记住上次选的。
- 规则【用户 2026-09-27】：project_code `^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`（不能是 `.` `..`），区分大小写，不可改，未删除项目中唯一，进 S3 key。名称自动去首尾空白、1–100 字，其余按文件夹名规则【用户 2026-10-07】；说明 ≤ 500。
- 建项目同一事务建根文件夹（名 = 项目名）和归类根（编码 = 项目编码、名 = 项目名、类型 root）；改名时一起改。删除是软删除，有未删除文件 409 `project_not_empty`。archived 只是标记。
- 授权 `PUT access {user_ids, role_ids}` 整体替换（各 ≤ 1000，可授权给停用用户）。`GET /api/projects` 返回能进的未删除项目（含归档）。
- **版本检查**【用户 2026-10-15，QB】：PATCH 可带 `expected_updated_at`，与库里不同 409 `conflict` 不改；不带不查。updated_at 写成 `max(now, 旧值 + 1µs)`。编辑对话框 409 后重载并按"打开时 / 我的 / 现在的"三方合并提示。
- 保存著录设置时系统默认值也写进项目（用户 2026-10-15 接受）。

### 5.4 存储配置（M0）

- 界面 /system/storage：卡片列表、新增编辑、测试连接、设默认、删除。
- 字段见建表 SQL：endpoint 是 server 访问地址，public_endpoint 是工作站访问地址（预签名按它签，空时同 endpoint）；path_prefix 默认 `doclite/`；url_expire_hours 1–168，默认 24。
- secret_key 用 `MASTER_KEY`（32 字节 base64，缺了不启动）AES-256-GCM 加密，绑定行 id，任何输出都不含它；解不开 → 500 `secret_unreadable`，界面提示重填。PATCH 不传或空串 = 不改。
- 首次启动无存储配置时用 `S3_*` 环境变量登记默认的"SeaweedFS（随系统部署）"。
- 状态 active ⇄ readonly → disabled（可再启用）：只有 active 能设默认、仅一个默认；默认的不能改只读、停用、删除；有快照引用的不能停用；只有非默认且没被引用的能删。409 `storage_not_active`、`storage_is_default`、`storage_in_use`。
- 测试连接：List(1) → 故意签错，确认对端是 S3 且校验签名 → public_endpoint 不同时预签名访问不存在的 key，应得 404 NoSuchKey。结果 ok / unreachable / auth_failed / bucket_not_found / secret_unreadable / other；不跟重定向，只回摘要；可测未保存的配置。
- 校验：endpoint 只能根路径；path_prefix 空或以 `/` 结尾、不以 `/` 开头、无 `//`、每段 `[A-Za-z0-9._-]`；bucket 按 S3 规则。
- 换存储：新的设默认后新快照写新存储，旧快照按自己的 storage_profile_id 读。写快照时存储须仍 active 且默认，否则 409 `storage_changed`。
- **存储迁移**（M7-2，规则见第 11 节）：`POST {id}/migrate {target_profile_id}`。源是默认存储 409 `storage_is_default`（先把别的设默认），目标不是 active 409 `storage_not_active`，目标和源同一个位置（地址、桶、前缀都相同）400，没有快照 409 `invalid_state`；**整个系统同一时间只有一个迁移在排队或执行**，有了再建 409 `invalid_state`（迁移执行时占执行器一个槽）。迁移进行中：源不能设默认、不能删，目标不能改只读或停用、不能删（409 `invalid_state`）；源可以改只读。目标里的 key = 目标的 path_prefix + `<project_code>/<file_id>/v<no>.<ext>`。迁移不删源存储的对象。迁完的源存储建议先改只读（旧导出物仍能下载，到期清理照常删只读存储里的导出物），清理完再停用；导出过的存储一直被导出任务记着，只能停用、不能删除。

### 5.5 文件目录（M1）

- **界面 /files**（像资源管理器）：目录树；面包屑和备注；封面网格 / 列表（虚拟滚动）；多选；详情（file_id、位置、版本、存储和 key、规格、审核和归类信息、日志）；右键（重命名、移动、复制、下载原图、查看原图、快照历史、删除，已驳回的可"上传重扫件"）；"已驳回"筛选；上传文件 / 文件夹。
- **名称**【用户 2026-09-27】：1–255 字；不含 `\ / : * ? < > |`、双引号、控制字符；首尾不空白、不以点结尾、不是 `.` `..`。**Windows 保留名**【用户 2026-09-28】CON、PRN、AUX、NUL、COM1–9、LPT1–9 不分大小写，取第一个点前的部分去尾空格比较（`con.txt`、`con .jpg` 不行；`con (1).jpg`、`COM10.jpg` 可以），400 `invalid_input`，上传也不自动改名。老名字移动、复制时不重校验【用户 2026-10-07】。
- **重名**（同文件夹、不分大小写、不算已删除）：明确改名撞名 409 `conflict`；上传、移动、复制文件撞名自动改为"名字 (n).扩展名"；新建、移动文件夹撞名 409；新建文件夹带 `if_exists: "return"` 时返回已有的（200）。
- **顺序**：sort_no = 目标文件夹未删除文件 max+1（到达先后 = 纸张顺序），移入复制入排末尾；列表按 sort_no、名称。
- 根文件夹不能改名、移动、删除（409 `root_folder_locked`），可改备注。移进自己子树 409 `tree_cycle`；目标在别项目或已删除 404；只在本项目内移动复制。
- **复制**：新 file_id，v1 类型 copy，复用源当前快照的对象，记来源；新文件待分配、自动改名、排末尾；只在新文件记 copy 日志；源存储停用 409 `storage_unavailable`。
- 同时改名和移动：新名在目标重名 409，日志先 rename 后 move。
- **删除**：软删除，删文件夹连子树；已归类的文件不能删（409 `file_cataloged`）；取消进行中的分配，每文件记 delete。删子树要拿文件树排他锁（整个项目的上传、审核都等它），所以**子树里未删除的文件超过 10000 个时不能一次删**（400 `invalid_input`，"请先分批删除它的子文件夹"；拿锁之前先数一次，锁住后再看一次）【总控 M10-1】。
- **封存**【用户 2026-10-18、19】：文件夹是某个未删除条目的来源、且条目不在"修改中"时，以下一律 409 `folder_archived`（details：folder_id、条目 id、件号、档号、题名、节点名称链、锁定与否，界面据此给"解除封存去修改"的引导）：往里上传、复制进去、移入移出文件、删除里面的文件、给里面的文件改名、在里面建子文件夹、这个文件夹本身改名、移动、删除。**可以**：重扫（同一文件出新版本）、审核、改这个文件夹的备注。上级文件夹（盒子）不封存：改名、移动可以，封存的子文件夹跟着走、内容不变；删上级时子树里有已归类文件照旧 409 `file_cataloged`。"修改中"的文件夹和未归档的一样可以整理，里面的页也可以删（重新归档时从件里移出）【总控】。批量操作里有一个碰到封存就整体不做。
- **批量** move / delete：≤ 500、同一项目（否则 400）、一个事务、一个不合规就整体不做（details.ids 是 folder_files id）。
- children 一次返回子文件夹（名、备注、子文件夹数、文件数、**归档信息**）和文件（id、名、sort_no、审核状态、轮次、版本号、是否已归类），不分页、不带图片地址。归档信息（新增字段，没有时为 null）：条目 id、件号、档号、题名、节点名称链、锁定与否、封存状态（sealed / modifying）、解除封存的人和时间；`GET folders/{id}` 同样带上自己的归档信息。日志倒序带操作人名；已删除的文件 404。
- 菜单：读、列、建文件夹 scan 或 files；整理只要 files。

### 5.6 上传和重扫（M1、M2）

- **请求**【用户 2026-09-28】：一个 multipart，字段按序各一次：`file_name`（必填）、`content_sha256`（可选，壳带）、`cover`（可选 JPEG ≤ 256 KiB）、`file`（必填，最后，≤ `MAX_UPLOAD_BYTES` 默认 512 MiB）。缺字段、顺序错、重复、多余、文本 > 4 KiB → 400 `invalid_input`。文件名只认 `file_name`。
- **流程**：原图流式写临时文件并算 sha（超限 413 `payload_too_large`，不符 400 `checksum_mismatch`）→ 读文件头 → 校验封面 → 预判重复 → 事务外传原图和封面到默认存储 → 短事务加锁、复查重、写 files / 快照 v1 original / folder_files unassigned / upload 日志 → 删临时文件 → 发 `files.changed`。
  - 收完请求体后"传 S3 → 写库 → 发事件"必须做完，不受客户端断开影响。业务错误要读完剩余请求体再返回。请求体空闲 60 秒按 400。
  - 写库失败留下的对象只记警告。对象带 `Cache-Control: private, max-age=<预签名有效期>, immutable`，元数据存原始文件名。临时文件总要删，启动时清残留（可简化：原版为多实例共用临时目录做了文件锁）。
- **文件头**（不解码像素）：按魔数只收 JPEG、PNG、TIFF；其他（含 BigTIFF）415 `unsupported_media_type`；空、截断、读不出 400 `invalid_image`。读宽高、DPI（四舍五入；没写为空，显示"未知"）、颜色模式（调色板 color，1 位 bw，单通道 gray，其余 color）、方向（JPEG 看 EXIF，TIFF 看 Orientation，PNG 不看；5–8 交换宽高，存转正后的）。多页 TIFF 拒绝（400，提示拆页）；像素 > `MAX_IMAGE_PIXELS`（3 亿）400。（可简化：原版对畸形 JPEG/EXIF 容错很细，常见文件即可，但壳读方向要和 server 一致。）
- **封面**【用户 2026-09-28】：JPEG，长边 400（不放大），质量约 75，按转正后的方向、不带 EXIF 方向，透明铺白。只由客户端（壳）本机生成【M10-2：浏览器只是开发工具，浏览器上传和前端生成封面的代码已删】；生成不了就不带。server 校验：≤ 256 KiB、是 JPEG 有 SOF、以 `FF D9` 结尾、长边 ≤ 400 且 ≤ 原图、方向 2–8 拒绝、横竖一致、短边比例差 ≤ 1 像素；不合规 400 `invalid_cover`（什么都没写），客户端去掉封面重传：**封面可以缺，不能因封面丢页**。
- **查重**【用户 2026-09-27】：同项目未删除文件当前版本 sha 相同 → 409 `duplicate_content`（details 带已有文件位置）；别的项目不算。**先判断断网重试**：同文件夹有 upload_name 和 sha 都相同的文件 → 200 返回已有文件。没有"仍然上传"，要保留同内容用复制。壳的预检 `files:check {file_name, sha256, size}` → `{duplicate, already_uploaded, final_name}`。
- **响应** 201：file_id、snapshot_id、folder_file_id、file_name、renamed、sort_no、version_no、宽高、dpi、color_mode、file_size、has_cover、warnings（`low_dpi` < 200、`dpi_unknown`）。
- **文件目录的上传按钮**：只在客户端里（"上传文件""上传文件夹"入上传队列，"上传文件夹"按相对路径逐层建文件夹）；浏览器里只提示"请在客户端中使用"【M10-2：浏览器上传已删】。
- **重扫** `POST /api/files/{fid}/rescan`（同格式）：只收 rejected（否则 409 `invalid_state`，客户端据此认为上次已成功）；追加 rescan 快照、轮次 +1、→ pending，给最近一次驳回它的人新增 open；那人不能再审（停用、没 review 菜单、失去授权任一条）→ unassigned、日志记 skipped_reviewer【用户 2026-10-07】；一个短事务完成。也查重【用户 2026-10-04】：和别的文件或自己当前版本相同 409，和自己更早版本相同放行。版本号对不上 409 `conflict`。同一文件重扫串行。
- **待重扫** `GET /api/projects/{pid}/rescans`：rejected 的文件，带文件夹备注（原件位置）和最近一次驳回（人、原因、说明、版本）。

### 5.7 看图（M1、M2）

- **列表不返回图片地址**。图片信息统一由 `POST /api/files/batch {file_ids ≤ 500}` 给【用户 2026-09-26】：当前版本的 snapshot_id、version_no、宽高、dpi、颜色、大小、mime、sha256、cover_url（无封面 null）、original_url、url_expires_at【用户 2026-09-27】。只给能进的项目；不存在、无权、已删除放 `missing`。存储停用或密钥坏时 URL 为 null，带 `url_error`。
- `GET /api/files/{fid}/snapshots`：全部版本，带类型、时间、创建人、存储和 key、URL。
- **预签名**：先查权限，用快照所在存储的 public_endpoint 签；签名时间取 **UTC+8 当天 0 点**，同一天同一张图 URL 不变；有效期 `url_expire_hours×3600+86400` 秒、封顶 7 天。图片不经 server，密钥不下发。
- 列表只显示封面、虚拟滚动；看原图一次一张，先显示封面。
- **查看器**【用户 2026-10-07，Q1】：OpenSeadragon 局部显示。只在客户端里看原图：用壳切的分块，只取看得到的块，100% 是真实像素；浏览器里只显示封面，提示"请在客户端中使用"【M10-2：浏览器里的整图模式和 TIFF 解码已删】。0.5 倍适合窗口到 400%；100% = 1 原图像素对 1 CSS 像素；旋转只转视图。
- **审的必须是看到的那一版**【总控】：原图加载中不响应通过驳回；原图封面都没显示不能审；原图失败而封面已显示要先确认"只看了封面"；对比时在看旧版不能审。提交带看到的 snapshot_id。
- **下载原图**【用户 2026-09-27】：不加接口，客户端里交给系统浏览器打开 original_url（浏览器里新窗口打开）。

### 5.8 审核分配和审核（M2）

| 当前 | 动作 | 结果 | 副作用 |
|---|---|---|---|
| unassigned | 分配（负责人） | pending | 新增 open；assign_review |
| pending | 改派（负责人） | pending | 旧行 cancelled、新增 open；reassign_review |
| pending | 通过 / 驳回（审核员） | approved / rejected | 那行 done、结果、当前快照（驳回加原因说明） |
| rejected | 重扫（扫描员） | pending 或 unassigned | 见 5.6 |
| approved | 撤回通过 | pending 或 unassigned | 给最近通过它的人新增 open；reopen；轮次不变 |
| 任意 | 删除 | 软删除 | 取消进行中的分配 |

- "最近一次通过 / 驳回"按 file_reviews 的 id（UUIDv7）倒序取。
- **能分给的人**：启用、有 review 菜单、能进项目（含 admin、负责人自己），否则 400【用户 2026-10-07，Q5】。
- **分配** `{reviewer_id, folder_id | file_ids}`：按文件夹 = 子树全部待分配（≤ 1 万）；按文件 1–500；非待分配的跳过计数（"跳过了 n 个"）；有不存在、已删除、别项目的整体 404（details.ids）。不做取消分配。
- **改派**：单个（已不是 open 或不是 pending 409；原来就是这人 200 `changed:false`）；批量（选中 ≤ 500，或某人在某子树的全部待审 ≤ 1 万）；只改 pending 的，其余跳过。
- **审核** `POST /api/files/{fid}/review {result, reject_reasons?, comment?, snapshot_id?}`：不是 pending 409 `invalid_state` → 不是自己的 403 `not_assignee`（负责人、admin 也一样）→ 快照不是当前版本 409 `conflict`。
- **驳回原因**【Q4】：至少一个，只从项目列表选（区分大小写，重复算一个）；选 other 时说明必填；说明 ≤ 500 字、无控制字符（换行制表可以）。通过不能带原因，可带说明。
- **批量通过**【Q9】：选中 1–500，一个事务；依次 404 → 409 → 403，有一类就整体不做。**撤回通过**【Q8】：只有 review 的只能撤自己的，有 review_assign 的能撤任何人的；不做撤回驳回。
- `GET review-settings` → `{reject_reasons, rescan_reassign, comment_max_chars: 500, max_batch_files: 500, max_folder_files: 10000}`。
- **读接口**：只含未删除的；folder_id 是子树；列表按文件夹 path_code、sort_no、file_id 排，keyset 分页（limit 默认 500、≤ 2000，cursor 原样回传）；计数不受分页影响，和列表同一只读快照。
  - **我的审核** `GET /api/me/reviews?project_id=&folder_id=&state=all|open|approved|rejected&low_dpi=`：project_id 必填（待办跟随项目切换，Q12）；每文件取最近一次分给我、没取消的那行，`is_current` 标是否仍有效；带各标签计数、按文件夹计数、"其他项目还有 n 个待审"。
  - 另有：可分配的人（带待办数）、按文件夹状态计数（子树、直接放的、各子文件夹）、open 分配（标出不能再审的人和原因）、一个文件的全部分配（含已取消）。
- 事件：状态变了才发 files.changed，> 500 只带 folder_ids【Q10】。
- **分配页**：左树带待分配数，中间是当前文件夹直接放的文件，右边可分配的人。没选文件 = 整个文件夹含子文件夹（先确认，> 1 万不能整个分）；不能再审的人标红。只有 review_assign 没 files/scan 的人只看到计数【总控】。
- **审核台**：左栏我的审核（按文件夹分组，状态标签和"DPI 不足"）；中间查看器，第 2 轮起显示"上一轮（vN）因…被驳回"；右栏版本、审核记录、文件和归类信息、驳回原因。审完跳下一张待审，审完的行留原位，新分配的靠核对计数发现【总控】。不做放大镜。
- **键位**【Q3】：← → ↑ ↓ 翻页；A 通过并跳下一张；R 驳回面板（1–9 勾原因、Tab 说明、Enter 确认、Esc 取消）；空格 适合窗口 / 100%；+ − 0 缩放；[ ] 旋转；C 对比，V 同位切换；Esc 依次关面板、对比、多选。输入框里字母键不触发；按住的重复按键忽略。
- **对比**【Q2】：左右并排、联动，另可 V 同位切换；"上一轮"取最近一次驳回审的快照；也可选任意两版；两侧标版本和驳回原因。

### 5.9 归类（M3；归档方式 2026-10-19 改为"整个文件夹归档"）

> 本节"定稿"一律改称**锁定**；条目状态只有 **draft（未锁定）/ locked（已锁定）**，去掉 done（已完成）【用户 2026-10-19】。

**节点**

- 类型：root、fonds、category、year、retention、org、custom；root 只在建项目时建。同一链可有多层同类型。
- 编码 1–32 字，不能有空白、控制字符、`{}`、`\ / : * ? " < > |` 和当前模板的分隔符；只在新建和改编码时校验；同层判重（区分大小写，409 `conflict`）。名称去首尾空白 1–100 字，同层可重名。
- 根节点不能改名、改编码、改类型、移动、删除（409 `invalid_state`），界面不显示。
- 移动、改编码、改类型：子树有已锁定条目 409 `item_locked`【用户 2026-10-14，Q8】；否则同事务重算子树未锁定条目的档号。删除：软删子树；还有未删除条目 409 `category_not_empty`【Q8】。
- 按需加载直接子节点，带子节点数、子树条目数。

**档号和件号**【用户 2026-10-14，Q2】

- 模板占位符 `{fonds}{category}{year}{retention}{org}{custom}`、`{item_no}` / `{item_no:0N}`（补零到 N 位，N ≤ 10）。`{item_no}` 正好一次、节点占位符各最多一次、相邻占位符间要有分隔符；文字不能有空白、控制字符、文件名非法字符、花括号；1–200 字；不合法 400。
- 生成：沿祖先链，占位符取**离条目最近的**同类型节点编码；缺级则档号留空（能建不能锁定）。建条目时生成，移动节点、改编码、改类型、移动条目、改件号时重算（已锁定的不动）；改模板不自动重算，提供"按新模板重新生成"（只改未锁定的）。不支持手改档号。
- **撞号**【总控】：生成的档号已被别的未删除条目占 → 自动生成和重算都留空不报错，只有显式改件号撞号 409 `conflict`。批量重算时范围外和已锁定的先占，范围内分档先到先得：① 有号不换 ② 有号要换 ③ 原为空，档内按 id；结果确定。
- 详情给空档号原因：incomplete（缺哪级）、conflict（被谁占）、not_generated（重新生成会补上）、template_invalid。
- **件号**：节点内 max+1，**算上已删除条目**（不回收）；未锁定可手改（1–999999999），已占（含已删除）409；移到别的节点取目标 max+1。重新归档不改件号、档号。

**归档**【用户 2026-10-18、19】（条目只能这样产生，不再有"新建空条目"）

- **件文件夹**：能成为一件的文件夹 = 不是根、没有未删除的子文件夹、有 1–2000 个未删除文件、还不是别的未删除条目的来源。
- **归档** `POST /api/archive-categories/{id}/items:from-folders {folder_ids, titles?, dry_run?}`：目标节点不能是根（409 `invalid_state`）。folder_ids 1–500 个，每个是件文件夹（自己成一件）或上级文件夹（它的**直接子文件夹**各成一件）；展开后 ≤ 500 件。选中的文件夹有子文件夹时按上级处理（根文件夹总按上级），它自己直接放的文件不归档，结果里用 `loose_files` 提示"盒 … 里直接放的 n 个文件不会归档"【总控】。展开后所有件的页数合计 ≤ 5 万，超过 400 `invalid_input`【总控，审查：一个事务太大会间接拖住审核】。不合格的跳过并给原因：`has_subfolders`（含下级文件夹）、`empty`、`too_many_pages`（> 2000）、`archived`（已归档为某件，带件号）、`file_cataloged`（里面有文件还在别的条目里，比如从修改中的文件夹移过来的，带在哪一件）。`dry_run: true` 只返回预览（将新建的件：来源文件夹、默认题名、页数；跳过的和原因），不写库；不带时同一事务建全部件，一件都建不出 409 `invalid_state`（details 即预览）。题名默认 = 文件夹名，`titles {folder_id: 题名}` 可改。页 = 文件夹里未删除的文件按 sort_no、名称排，页号 1..n。新件 draft、sealed，件号按节点 max+1 依次领，档号照常生成。
- **封存**：归档后来源文件夹封存（规则见 5.5）。重扫、审核不受影响，条目里自动显示新版本和"第 n 轮 / 待审"。
- **解除封存** `POST /api/archive-items/{id}/unseal {row_version, reason?}`：只有 draft 且 sealed 的可以（locked 409 `item_locked`，已是 modifying 409 `invalid_state`）；reason 选填 ≤ 200 字。→ modifying，记下谁、何时、原因；件号、档号、著录、页都不变，**和文件夹的绑定不断**。修改中：不能锁定、导出预检不就绪；著录照常可改。
- **重新归档** `GET /api/archive-items/{id}/reseal-preview` → 原页数、现页数、新增（文件、在第几页）、去掉、调序（只算相对顺序变了的，同 reorder 审计的算法）；`POST /api/archive-items/{id}/reseal {row_version}`：只有 modifying 的可以；来源文件夹要仍是件文件夹（被删了、有了子文件夹、空了、> 2000 → 409 `invalid_state` 写明原因；不要这一件就撤销归档）；新增的文件已在别的条目里（比如从另一个修改中的文件夹移过来）409 `file_cataloged`，提示先重新归档那一件。成功：页换成文件夹现在的清单、→ sealed、没有变化也可以（等于"没改，恢复封存"）。
- **修改中的件** `GET /api/projects/{pid}/archive-items:modifying` → `{items, total}`：项目里全部修改中的未删除条目（件号、档号、题名、节点名称链、来源文件夹、解除封存的人 / 时间 / 原因、row_version），按解除时间倒序、最多 500 条；给归类页顶部的"修改中 n 件"用【总控，M9-2】。
- **撤销归档** = `DELETE /api/archive-items/{id}`：只有 draft 的可以（locked 409 `item_locked`）；页一并移出（unlink 带 item_deleted），件号不回收，文件夹回到未归档。
- 一个文件只在一个条目（`archive_item_files.file_id` 唯一）、一个文件夹只对应一个未删除条目（`source_folder_id` 部分唯一索引）。

**条目**（只做"件"）

| 动作 \ 状态 | draft · sealed | draft · modifying | locked（一定 sealed） |
|---|---|---|---|
| 改著录、移动（换节点）、改件号 | 可以 | 可以 | 409 `item_locked` |
| 解除封存 | → modifying | 409 `invalid_state` | 409 `item_locked` |
| 重新归档 | 409 `invalid_state` | → sealed | 409 `item_locked` |
| 锁定 | → locked（查前置） | 409 `invalid_state`（修改中，先重新归档） | 409 `invalid_state` |
| 解锁 | 409 `invalid_state` | 409 `invalid_state` | → draft |
| 撤销归档（删除） | 可以 | 可以 | 409 `item_locked` |

- 成功动作 row_version +1 并审计；PATCH、状态动作都带 row_version，不一致 409 `conflict`。挂接文件的重扫、审核不改它。无实际变化不写库。
- **著录**【Q1】：题名固定列（≤ 1000 字）；其他存 extra_fields，PATCH 按 key 合并，null 或空串清空。text ≤ max_length（默认 500，可 1–10000）；date `YYYY` / `YYYY-MM` / `YYYY-MM-DD` 真实日期；enum 在选项里。未锁定时只查格式，**必填在锁定时查**，现有值也要合当前设置。新建时没给的用 default。
- 改设置后的旧值（不在选项、超长、已删除的著录项）不改就能保存，详情原样返回并用 `field_issues` 标出；不能悄悄丢掉【Q1】。
- 校验不过 400 `invalid_input`，`details.fields [{key, message}]` 一次列全（题名 key 为 `title`，著录项不能叫 title）。
- **锁定前置**：sealed（不在修改中）、著录合格（含必填）、≥ 1 页、档号不空、`lock_requires_approved` 时全部页通过；不满足 409 `invalid_state` 写明（"有 2 页还没审核通过（例如第 3 页）"，必填项不满足时 400 `invalid_input` 带 details.fields）。锁定后著录、档号、移动、删除、解除封存冻结，文件仍可重扫；有页在锁定后出了新快照就标"锁定后有新版本"（按最近一次锁定审计的时间比，解锁后不标）。
- **解锁**只要 catalog，→ draft。**批量锁定** 1–500，已锁定跳过，有一个不满足整体 409（details.ids 列全部，message 按原因计数）。
- **日志**：归档、重新归档每页 link / unlink，带件号、档号、题名、页号、文件名、路径、版本、审核状态；重新归档里留下的页相对顺序变了写一条 reorder 审计，只记挪动过的页（最长递增子序列之外）。
- **去掉的接口**：`PUT archive-items/{id}/files`（按页挂接）、`mark-done`、`back-to-draft`、`POST archive-categories/{id}/items`（新建空条目）。

**归类信息（只读）**【Q10】：能进项目就能看。`POST catalog-placements {file_ids ≤ 500}`、`GET files/{fid}/catalog` 返回挂在哪（件号、档号、题名、页号、状态、节点名称链数组，不含根）；/review 和 /files 只显示档号（空时"第 n 件"）、题名、第几页。`GET catalog-settings` 返回补全的模板及错误、用到的层级、著录项、开关、可选类型、编码禁用字符、日期格式。

**界面 /catalog**【用户 2026-10-18、19，Mockup `doclite-project/doclite-catalog-redesign.html` v3.1】：左右两个资源管理器，中间"归档 →"。

- **左：文件目录**（要 files 或 scan 菜单）：目录树 + 当前文件夹的内容；每个子文件夹显示归档状态（未归档 / 🔒 节点名 件号 / ✎ 修改中），盒子显示"已归档 n/m"；打开封存的文件夹时顶部紫色横条"已归档为 … 已封存"和"解除封存去修改…"，修改中的是橙色横条（将重新归档到哪一件、谁何时解除、原因，三步进度，"改完了，重新归档…"）。
- **右：归档目录**：归类树（工具栏和右键：新建、改名称编码类型、移动、删除、重新生成档号、导出，同现在）+ 选中节点的件列表（件号、题名、页数、封存和锁定状态；勾选批量锁定）；双击打开一件：档号、状态、著录表单、页（只读，来自文件夹）、锁定 / 解锁、移到别的节点、改件号、解除封存、撤销归档；修改中的件顶部橙色横条和"去文件目录""重新归档…"。根下直接放的条目单列一行【总控】。
- **归档**：左边选文件夹（盒子或几个子文件夹，只能选文件夹）→ 拖到右边树上的节点、件列表，或点"归档 →"（放到右边选中的节点）→ 预览对话框（每件的件号、可改的题名、页数，跳过的和原因）→ "归档并封存"。
- **解除封存**对话框：写明件号档号著录保留、修改中不能锁定导出、改完要重新归档；原因选填（漏扫 / 多扫 / 顺序不对 / 其他）。**重新归档**对话框：原 n 页 → 现 m 页，新增、去掉、调序分别列出。
- **不让人忘**：归类页顶部常驻"✎ 修改中 n 件"，点开列出件、文件夹、谁、原因，可跳去修改或直接重新归档；文件目录的树上修改中的文件夹所在的盒子带橙点。
- **/files** 也显示同样的横条和状态：封存的文件夹里上传、删除、移动等按钮置灰并写原因；拖文件进去时给"已封存，要修改请到归类页解除封存"的说明而不是报错。/review 的归类信息不变（档号、题名、第几页）。
- 著录设置在 /system/projects 编辑里（字段增删改排序、模板实时预览、锁定开关），默认只有 system.projects 能改【Q1】。改设置不发事件。
- 客户端里页面的 HTML5 拖放要能用（WebView2 上要关掉壳的原生文件拖放）【总控】。

### 5.10 项目看板（M3）

【用户 2026-10-14，Q6】只有 dashboard 能看；实时聚合，不建汇总表；人员统计、今天 / 本周不做。

【用户 2026-10-19，Mockup `doclite-project/doclite-dashboard-redesign.html` v2】原来的看板（6 个 KPI、"审核 × 归类"矩阵、两条多色进度条、文件夹表每行两条多色条）同一组数字说了几遍、看着乱，改成**只要简单的统计**：去掉矩阵、多色进度条和"待办"之类的东西；**审核按页数，归档、锁定、导出按件数**（一个文件夹归档成一件，页数做补充）。

- **总数**（整个项目，按流程从左到右一行五个数，每个一条单色进度条）：已上传 `N 页` → 审核通过 `x / N 页 · %`（小字：待分配 · 待审 · 驳回）→ 已归档 `n 件 · m 页`（小字：`m / N 页 · %`；有修改中的件时加"修改中 k 件"）→ 已锁定 `a / n 件`（小字：未锁定 `n − a` 件）→ 可导出 `e 件 · p 页`（小字：`require_locked_item` 为 true 时"已锁定且全部页审核通过"，false 时"已归档且全部页审核通过"）。
- **可导出的件** = 导出预检勾"只导出审核通过的"时的**就绪**（5.11）：不在修改中、（`require_locked_item` 时）已锁定、档号不空、≥ 1 页、全部页 approved；页 = 挂接行里文件和所在文件夹都未删除的。两边的条件要一起改。
- **按文件夹**的表（`catalog-summary?folder_id=` 逐层展开；有子文件夹又直接放了文件的，展开后多一行"直接放在这里的文件"）：每行页数、审核通过（单色条 + `x/y`）、已归档、已锁定。有下级文件夹的行（盒子等）写下面的合计：已归档 `n 件 · m 页`、已锁定 `a 件`；整个归档成一件的文件夹写档号（为空写"第 n 件"，修改中加标记），已锁定写"已锁定"或"—"；没归档的写"—"。
- 件算在来源文件夹（`archive_items.source_folder_id`）里；页（文件）算在它现在所在的文件夹里。
- **短时缓存**【总控 M10-1】：看板和审核的按文件夹计数（`review-summary`）每次实时聚合，实时事件一来几个客户端同时重新取，所以同一个（项目、文件夹、项目的数据版本）5 秒内只算一次，同时来的合并成一次（进程内，硬性规则 16）。项目每发一条事件（export.changed 除外）数据版本就变，所以看不到事件之前的旧数；没发事件的变化（改显示名）最多晚 5 秒。

### 5.11 导出（M4）

**范围**：`category`（节点子树里未删除的条目，重叠算一次）或 `item`；scope_ids 1–5000，须是本项目未删除的（否则 404 details.ids）。条目按节点 path_code、件号、id 排。页 = 挂接行里文件和文件夹都未删除的，页号 1..n。没归类的文件不可能被导出。

**预检**【用户 2026-10-15，Q7】：就绪 = 不在修改中、（require_locked_item 时）已锁定、档号不空、≥ 1 页、（勾"只导出审核通过的"，默认勾）全部页 approved。原因码 `source_modifying`（来源文件夹修改中，2026-10-19 新增）、`not_locked`、`no_archive_no`、`no_pages`、`pages_not_approved`（带前 5 页，如"0012 第 3 页：待审"）。给件数页数、就绪与否、各原因件数、不就绪条目（≤ 500 件）。单任务 ≤ **20 万页**【总控】。建任务时没有条目、无一就绪、没勾"只导出就绪的"而有不就绪、超页数 → 409 `invalid_state`，details 即预检结果。

**冻结**【Q4】：点导出那一刻在一致快照里定下条目、页和每页的最新快照（export_task_files），连同条目当时的档号、题名、著录、节点名称链、状态（原版存 job payload，写后不改、job 行永久保留【总控】）。之后的变化不影响这次导出，归入交付后变更。建任务不拿树锁。

**导出物**【Q2、Q3】：一个 zip（不压缩、UTF-8 名、> 4 GB 可用）。PDF 每件 `<档号>.pdf` 平铺；原图包每件 `<档号>/0001.<jpg|tif|png>`（保留原格式）。都附 `目录.xlsx`（每件：序号、档号、题名、各著录项、页数、状态、分类路径；首行加粗冻结；全文本）和 `清单.csv`（UTF-8 BOM、CRLF；每页：档号、页号、zip 内文件名、file_id、snapshot_id、版本、sha256、原文件名、审核状态）——每页能反查的依据；原文件名以 `= + - @` 等开头加 `'`。

- 名字 = 档号：Windows 非法字符换 `_`；结尾点或空格加 `_`；保留名在第一个点前那段后加 `_`（`con.2025` → `con_.2025`）；重名（不分大小写）或撞目录、清单名时加 ` (2)`…
- **PDF 1.7**：一页一图铺满；Title 题名、Subject 档号。尺寸 = 像素 × 72 / DPI，横纵分别算：100–2400 算合理，一个合理而另一个 ≥ 50 时各用各的（传真 204×98 不压扁），否则都用合理的那个，都不合理按 300【总控】。JPEG / TIFF 方向用变换转正。
- **原样嵌入**【Q1】：JPEG → DCTDecode；PNG（不隔行无透明）→ Flate + 预测器；TIFF 无压缩、LZW、Deflate、PackBits、G3/G4、JPEG → 对应过滤器，多条带拼一页；调色板 → Indexed。**放不进的无损转换**（Flate，透明转 SMask）。（可简化：原版还处理了 16 位预测器、预乘 alpha、旧式 JPEG-in-TIFF、条带 > 1024、> 10 GB 等；重写保证常见扫描格式，罕见的无损转换，转不了按不支持报错。）
- **出错**：任一页有问题（大小或 sha 不符、格式不符、损坏、不支持）整个导出失败、不跳过；再把剩下的页只看文件头查一遍，一次列出全部问题页（档号、页号、file_id、snapshot_id、原因），提示重扫或移出后重导；不重试。

**执行**【Q7】：同时 ≤ 2 个（`JOB_CONCURRENCY`），其余排队；可重试的失败（S3、临时盘、写库）自动重试，一共最多执行 3 次（max_attempts），重试前等 30 秒 × 4^(n−1)（30 秒、2 分钟…，最多 1 小时），复用同一 job 行。一次一件一页，页从 S3 流式读到临时目录，zip 边生成边分片上传；开始前查临时盘（PDF 要最大一件原图 × 1.2）。进度按页、≤ 每秒一次。成功时每页一条 file_logs `export`，和任务 done 同一事务。导出物 key `<path_prefix>exports/<project_code>/<task_id>/export-<第几次>.zip`。

- **取消**【总控】：排队的直接 canceled；执行中的任务立即 canceled，job 等执行器中止上传、删临时文件后再 canceled，期间 `cleaning_up: true`；已结束的取消 409。执行器改任务状态带条件，不能把已取消覆盖成 done。
- 优雅退出时执行中的放回队列（不算一次尝试）；被杀后重启要能接着处理。（可简化：原版的租约、心跳超时回收、多执行器领取锁是为多实例；单服务器只需心跳查取消、启动时把 running 的放回队列。）

**下载和保留**【Q5】：`GET download` 返回 JSON `{url, file_name, size, expires_at, url_expires_at}`，不用 302（壳经代理会把 zip 转一遍）；url 是按天取整、带 `response-content-disposition` 的预签名地址，浏览器直连 S3，壳交给系统浏览器；每次写审计。文件名 `<项目编码>_<范围名>_<日期>.zip`（≤ 40 字，日期 UTC+8）。保留 7 天后自动删，任务和明细永久保留，可按原范围重导；过期下载 409 `invalid_state`，存储停用 409 `storage_unavailable`。定期清理（默认 10 分钟）：done 且过期超过 `url_expire_hours + 24h` 宽限的、failed / canceled 留下的，删对象、清 output_key。（可简化：原版另扫描近 7 天删"没人记着"的残留对象。）

**交付后变更**【Q6】：只对 done 的任务，按件列出：new_version、pages_removed、pages_moved、pages_reordered、pages_added、not_approved（勾了只导出通过时）、item_changed（题名、档号、著录）、item_deleted；只换节点而档号不变、节点改名不算；≤ 500 件。"只补交有变更的 n 件"：建任务传 `resubmit_of`，server 同一快照算条目，不受 5000 限制【总控】。

**权限和界面**：要 export + 能进项目。/export：任务列表（进度、下载、明细、"交付后变更 n"、取消）；新建（从归类树选范围、格式、"只导出审核通过的"、预检问题清单、"只导出就绪的 n 件"；双层 PDF、OFD 标"以后的版本"）；补交。/catalog 的节点和条目上有"导出"入口。交付后变更的刷新要节流【总控】。

## 6. 桌面客户端（壳）

- **标识**【用户 2026-09-27】：identifier `com.doclite.desktop`（不再改，改了安装更新会当成另一个应用）；productName `Doclite`；单实例（再启动聚焦已有窗口）。
- **数据目录**【用户 2026-09-27】：`%LOCALAPPDATA%\Doclite\`（macOS `~/Library/Application Support/Doclite/`）：设置、会话 Cookie、上传队列库、日志（按天滚动）、图片缓存、`smoke\`。
- **会话**【用户 2026-09-27；界面随客户端打包、地址填 server 的接口地址，见第 1 节】：首次启动填服务器地址（探测 `/api/health`，再不带会话请求一次 `/api/me` 识别 426 需要升级；`/api/shell/latest` 和 `shell/download` 还没做，随自动更新 M8 做），连不上可重试或改地址。页面和 server 跨源，**壳代理全部 `/api/` 请求**：只许已配置 server 的 `/api/` 路径；壳保存 Cookie（退出或换服务器清空），页面拿不到令牌；壳加 `X-Client-Type: desktop`、`X-Client-Version`；SSE 经壳逐块转发；不用 Bearer（Wails 下怎么实现由重写定，"页面拿不到令牌、客户端头由壳统一加"保留）。热文件夹、缓存、页面共用会话；到期时队列暂停，登录后自动继续。本机能力只开给壳的资产服务器给出的页面（同源，CSP、导航保护照旧）。
- **版本**【用户 2026-09-27】：server 配 `MIN_CLIENT_VERSION`（不设不限）、`CLIENT_LATEST_VERSION`、`CLIENT_DOWNLOAD_URL`；低于最低 426 `client_upgrade_required`（details 带版本和下载地址），格式错 400 `invalid_client_version`；不带头放行；`/api/health`、`/api/shell/*` 不查。壳启动查 `shell/latest`，低于最低或收到 426 时整页提示更新。只在接口不兼容时调高；不做版本范围。
- **上传队列**：本机库，一行一个本地文件（路径、大小、修改时间、sha、目标、状态 等待写入 / 排队 / 上传中 / 已入库 / 重复 / 失败、重试、错误）；一个数据目录只一个进程开队列；重启时上传中回到排队。热文件夹和壳里的上传按钮都进它。
- **热文件夹**【用户 2026-09-27】：只监控一个目录，对应目标文件夹（可换）；子文件夹逐层按名字建（if_exists）并记住 folder_id（服务端改名移动照传，删除后重建）。写完判定：大小和修改时间 2 秒不变 → Windows 能独占打开 → JPEG 尾 `FF D9`、PNG 有 IEND；60 秒仍不完整记失败。忽略隐藏、系统、临时文件（`.`、`~$` 开头、`.tmp`、Thumbs.db 等）；不跟符号链接，≤ 32 层。同一子文件夹按文件名自然顺序串行（IMG_2 在 IMG_10 前），≤ 2 个子文件夹并行。
- **每个文件**：核对大小时间 → 算 sha → 取或建文件夹 → 预检 → 生成封面（不依赖系统库；生成不了就不带）→ 流式上传。本地文件不动；没变不重传，变了当新内容。失败自动重试（1 秒起翻倍、封顶 5 分钟），断网整文件重传；400、403 永久失败；网络、5xx 临时；401、426 暂停；invalid_cover 去封面重传。暂停 = 停监控，重启按上次状态恢复。（可简化：原版为 10 万级积压、中途换服务器做了很多细节；"不丢页、不重复、顺序对、断网或 server 重启后接着传"必须保证。）
- **/scan 页**：热文件夹卡片（目录只能用系统对话框选，目标在当前项目里选或新建；开始 / 暂停）；各状态计数；最近文件（进度、DPI、无封面、重复位置、改名、重试倒计时）；右侧待重扫。浏览器的 /scan 只提示"请在客户端中使用"。
- **重扫件上传**【用户 2026-10-07，Q6】：不进队列：系统对话框选一个文件 → 查文件尾完整 → 算 sha → 封面 → 上传，显示进度和结果；同时只一个；提示"重扫件不要扫进热文件夹"。只在客户端里（浏览器里提示"请在客户端中使用"）。
- **图片缓存**：页面经壳的自定义协议取图（`<cover|original>/<file_id>/<snapshot_id>`），先查本机，没有用预签名 URL 下载并存下。按 snapshot_id 存（不用版本号：库回退时同一 v2 可能是另一张），不需失效。默认 2 GB（100 MB–1 TB 可改），按最近访问淘汰到 90%；可按项目查看和清空；大于上限一半的不存。原图校验 sha256、封面校验是 JPEG。**只认来源记录**：只用该快照最近出现在 batch / snapshots 响应里的 URL；命中缓存也要在记录里（缓存是所有账号共用的）；登录、退出、换服务器清空记录。（可简化：原版的索引损坏重建、启动对账。）
- **大图切块**【Q1】：DZI，512×512、重叠 1，最高级是转正后原尺寸、逐级减半；灰度彩色 JPEG（质量 90），1 位 PNG；存进缓存参与淘汰；同一快照只切一次，同时 ≤ 2 张，可提前切下一张；切不了 422，页面只显示封面。（可简化：原版对条带 TIFF 逐行流式解码；可整张解码，但要有内存和时间上限。）
- **设置页**【用户 2026-10-17，M7-11】：客户端的设置都在 /settings（只在客户端里有，按客户端的能力显示），取代原来的"关于""本机图片缓存"两个对话框和 /scan 页头部的缓存摘要：服务器（地址、连接状态和响应时间、服务端版本、修改地址）、账号（姓名、登录名、角色、退出）、本机存储（数据目录、本机图片缓存、上传队列、日志目录，目录可在系统的文件管理器中打开，页面只给名字 data / logs / cache，路径由客户端算）、热文件夹（状态，去 /scan 设置）、关于（客户端和界面的版本、运行环境、复制诊断信息，不带令牌、Cookie、密码）。侧栏可收起（只剩图标，⌘B / Ctrl+B，本机记住）。
- **托盘和窗口**：提示状态（需重新登录、需升级、未连接、热文件夹运行 / 暂停 / 未设置）和各状态数量；菜单：打开、设置…（打开窗口并跳到设置页）、暂停 / 继续热文件夹、退出。关窗缩到托盘、上传继续；只有"退出"真退出；退出时移除图标。App Nap 不处理【用户 2026-10-07】。
- **自检 `--smoke`**：地址和账号从环境变量读，用 `smoke\` 目录；逐项查登录、协议取图、SSE 心跳、缓存、切块、查看器、托盘、扫描页等；输出一行 JSON；退出码 0 成功、1 失败、2 连不上、3 需升级。

## 7. 实时事件（SSE）

`GET /api/projects/{pid}/stream`：能进项目即可；只推本项目；`data: {"type", "project_id", ...}`，**只带 id**，客户端据此重取。15 秒保活；60 秒复核会话和授权，失效即断；提交后才发；断开期间不补发，重连后整体刷新；积压溢出发 `resync`。客户端 45 秒无数据重连，401/403/404 不重连，收到后去抖约 300 ms 刷新。没有实际变化、改设置不发。

| type | 字段 | 含义 |
|---|---|---|
| folders.changed | folder_ids | 文件夹建、改名、改备注、移动、删除、归档状态变（归档、解除封存、重新归档、撤销归档、锁定、解锁）；项目改名时是根 |
| files.changed | folder_ids, file_ids? | 上传、重扫、改名、移动、复制、删除、审核状态变、挂上移出（只换页号不算）；> 500 个只带 folder_ids |
| catalog.changed | category_ids?, item_ids | 归类节点或条目变化 |
| export.changed | task_ids | 任务建立、取消、开始、进度（≤ 1 次/秒）、完成、失败、重试、清理完 |
| resync | — | 整体刷新 |

**catalog.changed 的 item_ids**【总控】：存在时按条目刷新；`[]` = 只有节点变；**不存在** = 变了的条目 > 500，把 category_ids 当子树根整体刷新。item_ids 只列条目本身变了的（档号、著录、状态、页、封存状态）；节点改名、移动、改编码会改子树条目的路径显示，显示归类路径的地方要按 category_ids 刷新，/files、/review 的归类信息收到任何 catalog.changed 都重取。归档、解除封存、重新归档、撤销归档先发 files.changed（有挂上移出时）和 folders.changed（来源文件夹的归档状态变了，folder_ids 是来源文件夹）再发 catalog.changed。

## 8. 接口清单

都在 `/api` 下，JSON，时间 RFC 3339，错误体 `{code, message, details?}`。

| 路径（省略 /api/） | 菜单 | 说明 |
|---|---|---|
| GET health · shell/latest · shell/download/{v}（shell/* 还没做，随自动更新 M8） | 不用登录 | 健康、客户端版本、安装包 |
| POST auth/login · auth/logout · GET me · me/menus · projects | 登录 | 会话、菜单、我的项目 |
| system/users（GET/POST）· {id}（GET/PATCH）· {id}/disable·enable·reset-password（POST）· {id}/roles（PUT） | system.users | 用户 |
| GET system/roles | roles 或 users | 角色列表 |
| system/roles（POST）· {id}（GET/PATCH/DELETE）· {id}/menus（PUT）· GET system/menus | system.roles | 角色、菜单 |
| system/storage-profiles（GET/POST）· {id}（GET/PATCH/DELETE）· test · {id}/test · {id}/set-default · {id}/migrate（POST 建、GET 最近一次的进度和失败清单）· {id}/migrate/cancel（M7-2） | system.storage | 存储 |
| system/projects（GET/POST）· {id}（GET/PATCH/DELETE）· access-candidates · projects/{pid}/access（GET/PUT） | system.projects | 项目、授权 |
| system/license（GET、PUT `{"license": "<.lic 全文>"}`）（M7-1） | system.license | 许可证状态、导入 |
| GET system/audit-logs（from、to、operator_id、project_id、target_type、action、limit、cursor → `{items, next_cursor}`）· GET system/audit-logs/filter-options（M7-3） | system.audit | 审计日志 |
| folders/{id}（GET）· folders/{id}/children · POST folders | scan 或 files | 文件夹 |
| folders/{id}（PATCH/DELETE）· folder-files/{id}（PATCH/DELETE）· {id}/copy · folder-files:move · :delete | files | 整理 |
| POST folders/{id}/files · files:check · files/{fid}/rescan · GET projects/{pid}/rescans | scan 或 files | 上传、预检、重扫、待重扫 |
| POST files/batch · GET files/{fid}/snapshots | 登录 | 图片、版本 |
| GET files/{fid}/logs | files | 日志 |
| POST projects/{pid}/review-assignments · review-assignments/{id}/reassign · projects/{pid}/review-assignments:reassign · GET …/reviewers · …/review-summary · …/review-assignments | review_assign | 分配、改派、查询 |
| GET me/reviews · POST files/{fid}/review · projects/{pid}/reviews:approve | review | 我的审核、审、批量通过 |
| POST files/{fid}/reopen · GET files/{fid}/reviews | review 或 review_assign | 撤回、分配历史 |
| GET …/review-settings · …/catalog-settings · POST …/catalog-placements · GET files/{fid}/catalog · …/stream | 能进项目 | 设置、归类信息、SSE |
| projects/{pid}/archive-categories（GET/POST）· {id}（PATCH/DELETE） | catalog | 节点 |
| GET archive-categories/{id}/items · POST archive-categories/{id}/items:from-folders · archive-items/{id}（GET/PATCH/DELETE）· {id}/lock · {id}/unlock · {id}/unseal · GET {id}/reseal-preview · POST {id}/reseal · GET projects/{pid}/archive-items:modifying · projects/{pid}/archive-items:lock · :renumber | catalog | 条目、归档、封存、锁定解锁（2026-10-19 去掉 POST items、mark-done、back-to-draft、PUT files；解锁不再要 review_assign） |
| GET projects/{pid}/catalog-summary | dashboard | 看板（5.10）：`folder_id`（不传是根文件夹）、`folder_name`、`parent_id`、`require_locked_item`；`counts`（整棵子树）、`direct_counts`（直接放在里面的）各是 `{total, review{unassigned,pending,approved,rejected,total}, catalog{uncataloged,unlocked,locked,cataloged,total}, items{total,locked,modifying,exportable,exportable_pages}}`——total、review、catalog 是页数，items 是件数（2026-10-19 加：未删除的件，按来源文件夹归组，子树算来源文件夹在子树里的、direct 算来源文件夹就是它自己的；exportable 同预检勾"只导出审核通过的"时的就绪）；`children[]` 是 `{folder_id, folder_name, folder_count, archive, counts}`（folder_count、archive 和文件目录 children 里文件夹的相同，2026-10-19 加）。原来的 `matrix`、按文件数的 `exportable` 2026-10-19 去掉 |
| POST projects/{pid}/export-tasks:precheck · export-tasks（POST/GET）· export-tasks/{id} · /files · /changes · /download · /cancel | export | 导出 |

**错误码**：400 `invalid_license`、`license_mismatch`、`license_file_expired`（M7-1 导入）；400 `invalid_input`（参数、名称、格式、著录 `details.fields`、批量跨项目、空列表或 > 500）、`invalid_client_version`、`checksum_mismatch`、`invalid_image`、`invalid_cover`；401 `unauthorized`、`invalid_credentials`；403 `forbidden`、`admin_required`、`user_disabled`、`not_assignee`、`license_missing`、`license_expired`、`license_seats_full`（M7-1）；404 `not_found`（批量 details.ids：文件目录批量是 folder_files id，审核和挂接是 file_id，批量定稿是条目 id，导出是节点或条目 id）；405 `method_not_allowed`；409 `conflict`（唯一冲突、版本过期、件号占用、显式改件号撞号、审的不是当前快照）、`invalid_state`（状态不允许、定稿前置、导出不就绪）、`tree_cycle`、`tree_full`、`root_folder_locked`、`file_cataloged`、`folder_archived`（文件夹已封存，2026-10-19）、`duplicate_content`、`storage_changed`、`storage_unavailable`、`storage_not_active`、`storage_is_default`、`storage_in_use`、`project_not_empty`、`item_locked`、`category_not_empty`、`last_admin`、`cannot_disable_self`、`builtin_role`、`admin_menus_locked`；413 `payload_too_large`；415 `unsupported_media_type`；426 `client_upgrade_required`；500 `internal`、`secret_unreadable`；503 `busy`。

## 9. 用户决定汇总（按时间）

- **2026-09-26**：防提权四条；图片信息统一走 batch；CSRF 暂不处理；预签名按天取整（UTC+8），自己实现签名。
- **2026-09-27**：CI 不做、镜像和 compose 暂缓；项目规则；名称、重名、顺序规则；重扫到原 file_id；文件夹改名移动只审计；会话 12 小时不续期；壳代理请求；key 扩展名统一；入库上限、多页 TIFF 拒绝、无 DPI 存空；查重和断网重试、预检；batch 字段；下载原图不加接口；壳 identifier 和数据目录；决策 5 界面打包进客户端；最低客户端版本；热文件夹 v1；有扫描仪时真扫补测。
- **2026-09-28**：封面由客户端生成、随原图一个 multipart，server 不做图像处理；禁止 Windows 保留名；封面必须备份；插件出新版本时自带封面。
- **2026-10-04**：重扫也查重；Windows 打包和验收移到 M5。
- **2026-10-07**：App Nap 不处理；项目名去首尾空白、1–100 字；老名字不重校验；M2 不改表、同意迁移 0002；Q1 OpenSeadragon + 壳里切块、不换 Electron；Q2 对比；Q3 键位；Q4 驳回原因；Q5 分配规则；Q6 待重扫入口和壳的一次性重扫；Q8 撤回通过、不做撤回驳回；Q9 批量通过；Q10 > 500 只带 folder_ids；Q11 /files 查看原图；Q12 待办跟随项目；Q13 `not_assignee`、带 snapshot_id、DPI 筛选、不做放大镜；原审核人不能再审时改待分配。
- **2026-10-14**：先做完图像处理以外的流程，M6 和二期不排期；确认 M2；M3 全部按建议（Q1 著录项、Q2 档号件号、Q3 一文件一条目 / ≤ 2000 页 / 原生拖放 / 不做页号和案卷、Q4 待归类池、Q5 定稿解锁批量定稿、Q6 看板、Q7 互不阻塞定义和新事件错误码、Q8 删除移动、Q9 不改表、Q10 归类信息）。
- **2026-10-15**：确认 M3；M4 Q1（用户改选）放不进 PDF 的由 server 无损转换；Q2 导出物格式；Q3 xlsx + CSV；Q4 建任务时冻结；Q5 存 S3 直连下载、保留 7 天；Q6 交付后变更和补交；Q7 预检、并发 2、可取消、重试 3 次；QA 迁移 0003；QB 项目保存版本检查；接受"保存著录设置写入默认值"。
- **2026-10-18、19（归类改成整个文件夹归档，Mockup `doclite-project/doclite-catalog-redesign.html` v3.1）**：一个文件只能归类一次（照旧）；只能整个文件夹归档，选盒子时每个直接子文件夹一件，题名默认文件夹名，含下级文件夹的跳过并提示；数据单向，文件目录自己维护，归档后文件夹封存（盒子本身不封存）；可以重扫；要改走"解除封存（原因选填）→ 修改 → 重新归档（给对比）"，件号档号著录保留、绑定不断，修改中不能锁定和导出，到处提示；撤销归档 = 删除这一件；条目状态去掉"已完成"，只有未锁定 / 已锁定，必填在锁定时查；归类全部操作（含解锁）只要 catalog + 能进项目，不另设负责人。为此同意改表（迁移 0004）和接口。取代 2026-10-14 的 Q3（按页挂接、原生拖放排序）、Q4（待归类池）、Q5 里的"已完成"和"解锁要 review_assign"。
- **2026-10-19（看板简化，Mockup `doclite-project/doclite-dashboard-redesign.html` v2）**：去掉矩阵、多色进度条和"待办"，只要简单的统计——一行五个数（已上传 → 审核通过 → 已归档 → 已锁定 → 可导出）+ 按文件夹的表（页数、审核通过、已归档、已锁定）；审核按页，归档、锁定、导出按件（页数做补充）；同意为此给 catalog-summary 加字段（不改表）。见 5.10。

## 10. 总控代定的规则

| 规则 | 原因 |
|---|---|
| 没看到这一版不能审，只看了封面要确认；对比看旧版时不能审 | 防止没看图就通过 |
| 自动分回的条件从"停用"扩到三条 | 不把文件分给打不开的人 |
| 审核批量混进别项目的文件整批 404 | 不透露别的项目 |
| 按子树改派 ≤ 1 万；通过可带说明 | 和分配一致 |
| 只有 review_assign 的人在分配页只看计数 | 浏览目录要 files 或 scan |
| 审完的行留原位；新分配靠核对计数发现 | 翻页稳定；不为每个事件重拉 |
| 分配记录含已取消；我的审核用 is_current 标历史 | 完整改派历史 |
| 档号撞号留空，只有手改件号报错 | 否则后建的条目建不出来 |
| 重算档号分档先到先得 | 不让快定稿的条目丢号 |
| 归类锁和归类行在文件树锁之前 | 原顺序审核被间接拖慢 1.4 秒，改后约 0.01 秒 |
| ~~挂接每步立即保存，冲突做成预览~~（2026-10-19 不再按页挂接） | 减少两人抢页，操作不丢 |
| 撤销归档（删有页的条目）写明页数、期间有变化重新确认 | 不误移别人刚挂的页 |
| 封存检查和归档读文件清单互斥（2026-10-19） | 否则"刚归档完又进来一个文件"，封存的不变量被破坏 |
| 根下直接放的条目单列 | 否则看不见、移不走 |
| 客户端关原生文件拖放 | Windows 上页面拖拽会失效 |
| catalog.changed item_ids 三种含义 | 分清"无条目变"和"太多" |
| ~~看板可导出按审核状态拆，主 KPI"通过且可导出"~~（2026-10-19 看板简化，可导出按件、同预检就绪） | 和导出默认勾选一致 |
| 执行中取消：任务立即 canceled，job 清理完再改 | server 被杀时不停在 running |
| 冻结的条目信息不改、job 永久保留、重试复用 | 交付后变更要和它比 |
| 补交用 resubmit_of 不受 5000 限制 | 重新生成档号后可能上万件 |
| 单任务 ≤ 20 万页 | 建任务是同步的 |
| PDF 尺寸横纵 DPI 分别算、兜底 300 | 传真不压扁 |
| 一页失败后检查剩余页一次列出 | 一次处理完 |
| 条带 > 1024 的 TIFF 页转换 | 对象太多阅读器打不开（可简化） |
| 交付后变更刷新节流 | 原来 24 秒请求 77 次 |

## 11. M7 的决定和 M8 待做的

**用户决定（2026-10-16，服务端报告 `doclite-project/server-report.html`）：**

- **存储迁移**【用户 2026-10-16】：允许迁移**只改快照的存放位置**（`file_snapshots.storage_profile_id`、`storage_key`、`cover_key`），这是"快照只追加"的唯一例外。先复制、按 sha256 核对一致才改；迁原图和封面，导出物不迁（7 天自动删）；迁移时照常上传审核（新上传进默认存储）；中断后从断点继续；每个改了位置的快照写一条审计（`target_type=file_snapshot`、`action=storage_migrate`），任务开始、结束、取消也写审计。`storage-profiles/{id}/migrate` 由后台任务（`job_type=storage_migrate`）执行；多个快照共用一个对象（恢复、复制）时只复制一次。迁完原存储可停用。
- **许可证**【用户 2026-10-16】：**按登录会话数算**（同一个人在两处登录算 2）。【总控】在线会话 = 未撤销、未到期、30 分钟内有活动（`last_seen_at`；打开的实时通道算活动）；满了新登录被拒（403 `license_seats_full`）；持有内置 admin 角色的人总能登录（会话照样计数）；到期后非 admin 不能登录、已登录的下一次请求被拒（`license_expired`），数据不动，导入新许可证立即恢复；到期前 30 天在页面顶部提醒有 `system.license` 菜单的人（M7-1，admin 一定有）；还没导入许可证时只有 admin 能登录（`license_missing`）。许可证绑定服务端首次启动生成的 `install_id`（存 `system_settings`），"系统 → 许可证"页显示 install_id、导入（粘贴或选 .lic 文件）、显示公司、版本、人数、到期、当前在线数。签发用上一版的工具 `../bak/doclite-license`（Ed25519，`hardware_id` 填 install_id），服务端只验签，公钥编进程序。
- **审计日志页**【用户 2026-10-16】：做简单版——只读分页列表，按时间、操作人、操作类型（target_type、action）、项目筛选，有 `system.audit` 菜单的人能看；新加一个查询接口。
- **上传时拒绝导出不了的罕见格式**【总控建议，用户未反对 2026-10-16】：算术编码、无损、12 位的 JPEG，JPEG2000、JBIG 等压缩的 TIFF，上传时按文件头拒绝（`invalid_image`，提示换格式重扫）。
- **新建导出任务**【总控建议，用户未反对 2026-10-16】：5 万页实测 0.97–1.04 秒，先接受，正式硬件上重测（perf 测试按 1.5 秒判失败）。
- **打包和交付**【用户 2026-10-16】：**先不走打包流程**——用户先在自己电脑上开发验证，没问题后再一起定打包流程。以下暂缓：镜像、compose、CI；install.ps1；backup.ps1（建议：每天 2 点、数据库保留 7 天 + 4 个周日、图片增量同步、删掉的多留 30 天、附 restore.ps1；**封面必须备份**【用户 2026-09-28】）；离线交付包；自动更新（建议：先只用自己的 Ed25519 更新签名，不买 Windows 代码签名证书）；SeaweedFS 4.45 复验；Windows 远程验收（按 `/Users/yanlong/project/doclite-project/bak/doclite-v3/docs/acceptance/m1-windows.md`）。
- **export-tree** 运维命令：按目录结构导出文件和清单（容灾、交接），不跳过封面。随打包一起做。

## 12. v1 不做

插件（修图、纠偏等，M6）；OCR、双层 PDF、OFD、二维码分隔页、全文检索、按钮级权限、抽检、返工统计、壳直接驱动扫描仪（二期）；手改档号、案卷、页号；撤回驳回；看板人员统计和今天 / 本周；放大镜；跨项目移动复制；复制文件夹；恢复已删除；带真实文件名下载原图；封面补生成。
