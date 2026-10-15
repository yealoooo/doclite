# Doclite

纸质档案数字化系统。服务端 Go，桌面客户端 Wails，网页前端 Vue 3。

核心流程：扫描员上传扫描件 → 项目负责人分配审核 → 审核员通过或驳回（驳回后重扫）；归类员把文件挂接到档案条目，和审核并行；导出只从归类发起。

这是第二次实现：上一版用 Rust 写到导出（M4），因为交付慢、维护成本高，用户决定用 Go 和 Wails 重写（2026-10-15）。上一版在 `../bak/doclite-v3`，**只作参考，不复制代码**；需求和规则以 `docs/requirements.md` 为准。

## 文档

| 文件 | 用途 |
|---|---|
| `docs/requirements.md` | **需求说明，开发的主要依据**：硬性规则、数据模型、权限、各模块的规则、客户端、实时事件、接口清单、用户的全部决定。按任务读相关章节 |
| `docs/roadmap.md` | 里程碑、任务、依赖、完成标志、遗留问题 |
| `server/migrations/000N_*.sql` | 建表 SQL，沿用上一版（25 张表，含菜单和内置角色的初始数据，约束在 PG 16 上验证过）。表和字段的注释就是字段说明。新迁移按 `000N` 顺序追加，不改已有的 |
| `docs/sql/key-queries.sql` | 上一版关键操作的参考 SQL（查重、path_code 分配、移动、分配审核、通过、软删除、导出预检……）。注释里的 Rust 写法、advisory lock 编号只作参考 |
| `../bak/doclite-v3/docs/design.md` | 上一版的设计文档（约 26 万字）。只在 requirements.md 不够细时按章节查，**不要整份读** |
| `../bak/doclite-v3/docs/plan.html` | 原始方案和界面 Mockup，约 190 KB。需要看某个界面时 grep `id="p-…"`，**不要整份读** |

## 技术栈

- **server**：Go 1.25，一个可执行文件，同时托管网页的构建产物。
  - HTTP：标准库 `net/http`（1.22 起的方法 + 路径路由），中间件自己写；日志 `log/slog`。
  - 数据库：`github.com/jackc/pgx/v5`（pgxpool），手写 SQL，不用 ORM。迁移：自己写一个小执行器，`embed` 迁移目录、按文件名顺序执行、记在 `schema_migrations` 表里，SQL 文件原样沿用。
  - 对象存储：`github.com/aws/aws-sdk-go-v2`（s3、分片上传）；预签名用 SDK 的 v4 签名器并**指定签名时间**（按天取整，硬性规则 12）。
  - 其他：`golang.org/x/crypto/argon2`（密码）、`github.com/google/uuid`（UUIDv7 主键）、`github.com/xuri/excelize/v2`（导出的目录.xlsx）、标准库 `archive/zip`、`image/jpeg`、`image/png`、`golang.org/x/image/tiff`（只在导出 PDF 需要无损转换时解码）。PDF 自己写最小的写入器（为了原样嵌入压缩数据）。
  - 新加依赖要纯 Go（不要 cgo）、许可证宽松；在任务返回里列出来。
- **数据库**：生产 PostgreSQL 18，开发本机 16，只用 16 也有的特性（主键由应用生成 UUIDv7）。
- **存储**：只支持 S3 兼容存储，默认 SeaweedFS。
- **web**：Vue 3 + Vite + TypeScript + Tailwind 4 + shadcn-vue（reka-ui）+ Pinia + vue-router，npm。**从上一版原样复制过来**（`web/`），接口不变所以基本不用改；只有和桌面客户端打交道的部分（原来调 Tauri）在做客户端时改成调 Wails。
- **client**：Wails（v2 或 v3，做客户端时定）。界面就是 `web/` 的构建产物；上传队列、热文件夹、本机图片缓存、大图切块、托盘用 Go 写。
- **部署**：只用 Docker（server、postgres、seaweedfs，可选 portainer），客户是 Windows 服务器上的 WSL2 + Docker，内网离线。本机没有 Docker。

## 目录

```
go.work                 Go 工作区：server、shared、（以后）client
server/                 Go 模块 doclite/server
  cmd/server/             入口：读配置、执行迁移、启动 HTTP 和后台任务、托管 web 构建产物
  internal/
    app/                  组装各模块、注册路由、启动和优雅退出
    config/               环境变量
    platform/             和业务无关的基础设施
      db/                   连接池、事务、迁移执行器
      httpx/                路由辅助、中间件、统一错误体、JSON 读写
      s3/                   对象读写、分片上传、预签名
      events/               项目实时通道（SSE）
      jobs/                 后台任务执行器（v1 只有导出）
    auth/                 登录、会话、菜单权限、项目授权检查
    user/ role/ project/ storage/
    folder/ file/         文件目录、上传、重扫、快照、图片信息、日志
    review/               审核分配和审核
    catalog/              归类树、档号、条目、挂接、归类信息、看板
    export/               预检、导出任务、交付后变更、执行、生成（pdf、zip、xlsx、csv）
  migrations/             000N_*.sql（embed 进可执行文件）
  apitest/                API 测试（真实 PG + S3）
shared/                 Go 模块 doclite/shared：server 和 client 共用的纯函数
  imagehdr/               JPEG、PNG、TIFF 文件头解析（不解码像素）
  names/                  文件名、Windows 保留名等规则
web/                    网页前端（从上一版复制）
client/                 Wails 桌面客户端（M6 再建）
deploy/                 compose、.env 示例；M7 再加 install.ps1、backup.ps1
docs/
```

**模块的写法**：每个业务模块（`auth`、`folder`、`review`……）里分三层：
- `handler.go`：HTTP，解析请求、调用 service、写响应；
- `service.go`：业务规则、事务边界、发事件；
- `store.go`：SQL。

模块之间通过对方的 service 调用，不直接查对方的表（只读的查询可以例外，写在 store 里并注明）。一个文件太长就按功能拆（`upload.go`、`move.go`……），不要一个文件几千行。

## 本机开发环境（macOS，无 Docker）

- **PostgreSQL 16**：Homebrew 服务，`127.0.0.1:5432`，trust 认证。命令在 `/opt/homebrew/opt/postgresql@16/bin/`。库 `doclite_dev`、`doclite_test`（2026-10-15 新建的空库；上一版的库改名为 `doclite_dev_bak`、`doclite_test_bak`，不要动）。`DATABASE_URL=postgres://doclite@127.0.0.1:5432/doclite_dev`。
- **SeaweedFS 4.15**：`/Users/yanlong/project/smart_scan_bak/backend_storage/bin/weed`，后台运行：

  ```sh
  mkdir -p ~/doclite-dev/seaweedfs
  AWS_ACCESS_KEY_ID=doclite-dev AWS_SECRET_ACCESS_KEY=doclite-dev-secret S3_BUCKET=doclite \
    /Users/yanlong/project/smart_scan_bak/backend_storage/bin/weed mini \
    -dir=$HOME/doclite-dev/seaweedfs -ip=127.0.0.1 -admin.ui=false
  ```

  - 桶要单独建：`echo 's3.bucket.create -name doclite' | weed shell -master=127.0.0.1:9333`（桶 `doclite` 已存在，上一版的对象也在里面，key 不会冲突）。
  - S3 地址 `http://127.0.0.1:8333`；一定加 `-ip=127.0.0.1`。生产用 4.45，交付前复验预签名和 CORS。
- **工具链**：Go 1.25、Node 23、npm 11。Wails v3 命令行工具 `~/go/bin/wails3`（v3.0.0-beta.29）。
- **环境变量**：`server/.env`（不提交，从上一版复制，里面有 `MASTER_KEY`）。**不要修改或重新生成 `MASTER_KEY`，不要打印它。** 模板写在 `server/.env.example`。
- **端口**：用户自己的 server 跑在 9080，**不要碰**。子任务起 server 用 29080、29081、29082，预览用 24173、24174、24175（每个并行任务用一组，总控在派任务时指定）。起 server 时还要设自己的 `TMP_DIR`（比如 scratchpad 里的任务子目录）：`.env` 里的 `/tmp/doclite` 是共用的，server 启动时会清空其中的 `uploads/`。

## 测试

**原则（用户决定 2026-10-15）：测试少而有用，API 测试为主。**

- **API 测试**（`server/apitest/`）：每个接口用真实的 PostgreSQL 和 SeaweedFS 测——正常流程、主要错误码、菜单权限（每个模块一张紧凑的"接口 × 角色"表）。内部重构不影响它们。
- **单元测试**：只给纯算法：文件头解析、文件名规则、档号模板和撞号、页序操作、PDF 原样嵌入的判断等。
- **并发测试**：只测 requirements 里点名的几个场景（多人同时挂接、审核和归类同时操作、同内容并发上传只入库一个、导出执行中重启后恢复）。
- **不写**：大量边角用例、性能压测（除非完成标志要求）、逐个格式的罕见变体。
- **隔离**：测试可能在不同 worktree 同时跑。每个测试用自己的库（用模板库 `CREATE DATABASE … TEMPLATE` 克隆，快），S3 用独立的 key 前缀，结束后清理。连接参数从环境变量读，默认值：`TEST_DATABASE_URL=postgres://doclite@127.0.0.1:5432/doclite_test`、`TEST_S3_ENDPOINT=http://127.0.0.1:8333`、`TEST_S3_ACCESS_KEY=doclite-dev`、`TEST_S3_SECRET_KEY=doclite-dev-secret`、`TEST_S3_BUCKET=doclite`。SeaweedFS 没启动时 S3 测试明确报错，不悄悄跳过。
- **网页回归**：`web/scripts/shots/` 里的截图脚本（上一版留下的）是现成的验收：Go 服务端某个模块做完后，跑那个模块的主流程脚本，证明前端照常工作。

## 硬性规则（违反就是 bug）

完整版和解释见 `docs/requirements.md` 第 2 节。

1. **file_id 是锚。** `files` 表只有 id，其他模块都通过 file_id 引用文件。
2. **快照只追加。** `file_snapshots` 不 UPDATE、不 DELETE，当前版本就是 `max(version_no)`。
3. **文件目录只管位置和名字。** 移动、改名只改 `folders` 和 `folder_files`。
4. **归类只存 file_id。** `archive_item_files` 不存快照，也不存文件夹。
5. **审核记录审的是哪个快照，但不锁定。**
6. **导出只从归类发起。** 只包含已归类的文件；"只导出审核通过的"由用户每次勾选。
7. **S3 key 按 ID 生成**：`<path_prefix><project_code>/<file_id>/v<no>.<jpg|png|tif>`，封面 `v<no>.cover.jpg`。
8. **树用 `parent_id` + `path_code`**：每层 5 位数字加一个点，按字节序比较（`COLLATE "C"`），编码由父节点 `last_child_no` 原子分配，不回收。
9. **软删除。** 只写 `deleted_at`；文件的操作写 `file_logs`，其他变更写 `audit_logs`。
10. **权限只到菜单级。** 每个接口声明自己需要的菜单码，中间件统一校验，无权 403；能进哪些项目由项目授权决定，admin 能进所有项目。
11. **server 不做图像处理。** 只读文件头，不解码、不修改；封面由客户端生成，server 只校验。**唯一例外**（用户决定 2026-10-15）：导出 PDF 时放不进 PDF 的图片由 server 无损转换，只在 `internal/export` 里做。
12. **server 不缓存图片。** 工作站用预签名 URL 直连 S3，签名时间按天取整（UTC+8）；本机缓存由客户端负责。
13. **后台任务在 server 进程内运行**，`background_jobs` + `FOR UPDATE SKIP LOCKED` 领取，v1 只有导出。
14. **大文件流式处理。** 上传边写临时文件边算 sha256，不把请求体读进内存；导出边生成边上传。
15. **命名**：表名英文复数；主键 `id`（UUIDv7）、外键 `单数表名_id`；名称带前缀（`folder_name`）；`status`、`xxx_type`、`xxx_no`、`xxx_at`。
16. **部署按单台服务器、单个 server 进程**（用户决定 2026-10-15）：不为多实例设计；需要互斥的地方可以用进程内的锁，但数据库层面的约束（唯一、外键、行锁顺序）照样要对。

## 工作规则

- **不越出任务范围。** 只做 `docs/roadmap.md` 里当前任务写明的内容。
- **先简单。** 按单台服务器、常见图片格式、主流程来做；requirements 里标了"（可简化：…）"的按简化的做。不要为想象中的极端情况加复杂处理，遇到了写进返回的遗留问题。
- **需要改设计时先停下来问用户，不要自己改。** 包括改表结构、改接口约定（和 requirements 第 8 节、上一版前端的调用方式不一致）、和硬性规则冲突、需要安装软件、需要推送到远端。用户同意后，改表要新增迁移文件并同步 requirements。
- **接口要和上一版一致**：前端是原样复制的，请求和响应的字段、错误码、状态码都要对得上。拿不准时看前端怎么调用（`web/src/api/*.ts`、`web/src/api/types.ts`），再看上一版设计文档对应的章节。
- **完成前必须通过**：
  - server：`gofmt -l .` 没有输出、`go vet ./...`、`go test ./...`；
  - web 有改动时：`npm run build`；
  - 涉及界面行为的，跑对应的截图脚本。
- **任务完成后**在 `docs/roadmap.md` 里勾选，写下遗留问题和后续任务要知道的事（由总控统一写）。
- **Git**：在 main 分支上工作，每个任务一个 commit，消息以任务编号开头，例如 `M1-1: server 骨架、登录和系统管理`。没有用户要求不要 push。
