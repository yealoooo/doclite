# Doclite 路线图（Go + Wails）

状态标记：`[ ]` 未开始 · `[~]` 进行中 · `[x]` 已完成（附 commit）

**背景（用户决定 2026-10-15）：** 上一版（Rust，`../bak/doclite-v3`）做到 M4 导出，交付慢、维护成本高，改用 Go（服务端）+ Wails（客户端）重写。需求和规则不变，见 `docs/requirements.md`；建表 SQL、接口约定、网页前端沿用。目标仍是"图像处理以外的流程全部做完"：服务端（M1–M5）→ 客户端（M6）→ 部署交付（M7）。插件和二期不排期。

**节奏（用户决定 2026-10-15）：**
- 测试少而有用：API 测试为主，单元测试只给纯算法，并发测试只测点名的场景（见 CLAUDE.md"测试"）。
- 按单台服务器、常见图片格式、主流程做；requirements 里"（可简化：…）"的按简化的做。
- 独立审查只给有风险的服务端任务（并发、加锁、导出执行），审一轮；其余由总控自己验证（检查、API 测试、截图脚本）。
- 同时最多 3 个子任务。
- 服务端做完（M5 结束）向用户报告一次；客户端、部署各报告一次。

**每个任务的通用完成标志：** `gofmt -l .` 没有输出、`go vet ./...`、`go test ./...` 通过；接口和上一版前端对得上，用对应的截图脚本回归。下面只写各任务另外的要求。

**排期：**
1. M1-1 ∥ M1-2
2. M1-3 ∥ M5-1
3. M2-1
4. M3-1 ∥ M4-1
5. M5-2
6. M5-3 服务端验收 → 向用户报告
7. M6 → M7

---

## M1 骨架和系统管理

### [~] M1-1 server 骨架、登录会话、用户和角色
- **范围：**
  - `go.work`、`server` 模块的目录结构（按 CLAUDE.md）；
  - 配置（沿用上一版的环境变量名：`DATABASE_URL`、`LISTEN_ADDR`、`TMP_DIR`、`MASTER_KEY`、`S3_*`、`INIT_ADMIN_*`、`SESSION_TTL_HOURS`、`COOKIE_SECURE`……，日志级别改用 `LOG_LEVEL`）、`server/.env.example`；
  - 数据库连接池、事务辅助、迁移执行器（embed、`schema_migrations`、按序执行 0001–0003）；
  - HTTP：路由、统一错误体 `{code, message, details?}`、JSON、请求日志、托管 `web/dist`（前端路由回退到 index.html）、`/api/health`；
  - 登录会话（requirements 5.1）、菜单权限中间件、`/api/me`、`/api/me/menus`、`/api/projects`（这时项目表是空的，返回空列表即可，授权检查在 M1-3）；
  - 用户和角色（5.2，含防提权）；
  - API 测试的支撑：模板库克隆、S3 前缀、登录辅助、"接口 × 角色"表的写法。
- **完成标志：** API 测试覆盖登录、会话、用户、角色、菜单权限；用 Go server 托管前端，跑截图脚本 `m0.mjs`（登录、导航、用户、角色）通过。
- **先读：** CLAUDE.md；requirements 1、2、3.1、4、5.1、5.2、8；`server/migrations/0001_init.sql`；前端 `web/src/api/{http,auth,system}*.ts`、`web/src/api/types.ts`。

### [x] M1-2 shared 库：文件头解析、名称规则、档号模板（commit 4b442fd）
- **范围：** `shared` 模块（纯函数，不碰数据库和网络）：
  - `imagehdr`：JPEG、PNG、TIFF 文件头解析（requirements 5.6"文件头"：格式、宽高、DPI、颜色模式、方向、多页 TIFF、像素上限；不解码像素）；
  - `names`：文件和文件夹名称规则、Windows 保留名（5.5）；项目编码规则（5.3）；
  - 档号模板的解析和渲染（5.9"档号和件号"：占位符、补零、分隔符、缺级留空）。放在 `shared` 还是 `server/internal/catalog`，实现者定，说明理由。
  - **测试样本**：上一版的截图脚本和测试用 Rust 的 `gen-samples` 现场生成样本（JPEG、TIFF 无压缩、LZW、G4、PNG 等 18 种）。在 `../bak/doclite-v3/shell/src-tauri` 用 `cargo run --example gen-samples` 生成一次，挑够用的放进 `server/testdata/samples/`（总共几 MB 以内）提交；`web/scripts/lib/upload-samples.mjs` 改为读这个目录，不再调 cargo。
- **完成标志：** 单元测试覆盖样本文件的解析结果、名称规则、模板的合法与非法写法。
- **先读：** requirements 5.3、5.5、5.6、5.9；上一版 `../bak/doclite-v3/server/crates/doclite-infra/src/images/` 只作规则参考。

### [ ] M1-3 项目和授权、存储配置、预签名、实时通道
- **范围：** 项目和授权（5.3，含版本检查 `expected_updated_at`、建项目时建根文件夹和归类根）；存储配置（5.4：加密密钥、测试连接、默认、状态）；首次启动登记默认存储；预签名（5.7：按天取整的签名时间、`public_endpoint`）；项目授权检查（4）；实时通道 `GET /api/projects/{pid}/stream`（7：保活、复核会话、resync；事件总线供后面的模块发事件）。
- **完成标志：** API 测试覆盖以上；截图脚本 `m0-storage.mjs`、`m1-projects.mjs` 通过。
- **前置：** M1-1。
- **先读：** requirements 4、5.3、5.4、5.7、7、8。

- **结果：** `shared` 模块（`doclite/shared`，只用标准库）：`imagehdr`、`names`、`archiveno`；单元测试 imagehdr 65 个子测试、names 5 个、archiveno 5 个（覆盖率 84%、100%、97%）。样本 12 个在 `server/testdata/samples/`（3.8 MB，README 有格式、尺寸、DPI、方向和生成命令）；`web/scripts/lib/upload-samples.mjs` 改为读这个目录（每个进程往样本里加一段注释，保持"每次运行内容不同、不被判重复"）。
- **须知：**
  - `imagehdr.Parse(r io.ReaderAt, size int64, maxPixels int64) (Info, error)`（`DefaultMaxPixels = 300_000_000`；先读 64 KiB，需要时按偏移补读）；`Info{Format, Width, Height（转正后）, DPI（0 = 未知）, ColorMode（bw/gray/color/空）, Orientation（1–8）}`、`StoredSize()`、`Format.MIME()`、`Format.Ext()`；错误用 `errors.Is`：`ErrUnsupported` → 415 `unsupported_media_type`，`ErrInvalid`、`ErrMultiPage`、`ErrTooLarge` → 400 `invalid_image`，其他 → 500；`err.Error()` 是给用户看的中文说明。**封面校验不在这里**，M2-1 用 `Parse` 读封面后自己查（JPEG、方向 2–8 拒绝、长边和比例、`FF D9`、≤ 256 KiB）。
  - `names`：`ValidateFileName`、`ValidateFolderName`、`IsWindowsReserved`、`NumberedName(name, n)`（重名改"名字 (n).扩展名"）、`NormalizeProjectCode`、`NormalizeProjectName`、`ValidateCategoryCode(code, separators)`、`CategoryCodeForbiddenChars(separators)`；错误一律 400 `invalid_input`。归类节点名称的规则没做，留给 M4-1。
  - `archiveno`：`Parse(template) (*Template, error)`（错误以"档号模板不对："开头）、`Render(chain []Node, itemNo int) (archiveNo string, missing []CategoryType)`、`Levels()`、`Separators()`、`ItemNoWidth()`、`NearestCode()`、`DefaultTemplate`、`FallbackSeparators`、`CategoryType` 常量、`AssignableTypes`、`Label()`。
  - imagehdr 里的 IFD 读取没导出；M5-1 需要压缩方式、条带等细节时扩展或自己读。
  - **遗留：** `m1-accept.mjs` 还自己调 gen-samples（M5-3 改）；bak 里的 gen-samples 要用已编译好的 `target/release/examples/gen-samples`（搬家后 cargo 重新编译会失败）；JPEG 段之间的多余字节按读不出来处理（遇到真实扫描件这样写再放宽）。
---

## M2 文件目录和上传

### [ ] M2-1 文件目录、上传、图片信息、日志
- **范围：** 文件夹树和列表（children）、新建、改名、备注、移动、复制、删除、批量移动删除（5.5）；上传（5.6：multipart 字段顺序、流式写临时文件和 sha256、文件头、封面校验、查重和断网重试、预检 `files:check`、内容锁）；`files/batch`、`snapshots`（5.7）；文件日志；`files.changed`、`folders.changed` 事件。重扫接口放到 M3-1。
- **完成标志：** API 测试覆盖主流程和主要错误码；**并发**：同内容并发上传只入库一个；截图脚本 `m1-files.mjs`、`m1-organize.mjs`（浏览器上传部分）通过。
- **前置：** M1-2、M1-3。
- **先读：** requirements 3、5.5、5.6、5.7、7；key-queries 的上传、path_code 分配、移动、软删除。

---

## M3 审核

### [ ] M3-1 审核分配、审核、重扫
- **范围：** requirements 5.8 全部（分配、改派、审核、批量通过、撤回通过、读接口、审核设置）和 5.6 的重扫、待重扫列表；状态流转表；事件。
- **完成标志：** API 测试覆盖状态流转表的每一格和主要错误码；**并发**：审核和整理（移动、删除）同时操作不死锁；截图脚本 `m2-assign.mjs`、`m2-review.mjs`、`m2-rescan.mjs`（浏览器部分）通过。
- **前置：** M2-1。
- **先读：** requirements 3.6、5.6（重扫）、5.8；key-queries 的分配审核、通过。

---

## M4 归类和看板

### [ ] M4-1 归类树、档号、条目、挂接、归类信息、看板
- **范围：** requirements 5.9 全部（节点、档号和件号含撞号规则、条目状态和著录校验、定稿解锁和批量定稿、挂接和页序、删除条目、归类信息、catalog-settings、按新模板重新生成）和 5.10 看板；`catalog.changed` 事件（7）。
- **完成标志：** API 测试覆盖条目状态表、档号生成和撞号、挂接的主要错误码；**并发**：两个条目抢同一个文件只有一个成功、审核和挂接同时进行不互相等（加锁顺序见 3.6）；截图脚本 `m3-catalog.mjs`、`m3-workbench.mjs`、`m3-dashboard.mjs`、`m3-catalog-info.mjs`、`m3-catalog-settings.mjs` 通过。
- **前置：** M2-1（和 M3-1 并行）。
- **先读：** requirements 3.6、5.9、5.10、7。

---

## M5 导出

### [~] M5-1 导出物生成（PDF、原图包、目录、清单）
- **范围：** `internal/export` 里的生成部分（纯函数，输入冻结的条目和页、读原图的接口，输出写进一个 writer 的 zip）：PDF 原样嵌入、放不进的无损转换（硬性规则 11 的例外）、横纵 DPI、原图包、`目录.xlsx`、`清单.csv`、Windows 安全的文件名（requirements 5.11"导出物"）。
- **完成标志：** 单元测试覆盖常见扫描格式（JPEG、G4 TIFF、LZW TIFF、无压缩 TIFF、PNG、带透明的 PNG）原样嵌入或转换的结果（嵌入的字节、转换的像素）；用 macOS 的 `sips` 抽查 PDF 能打开、尺寸对。罕见格式不逐个做。
- **前置：** M1-2（和 M1-3 并行）。
- **先读：** requirements 2（第 11 条）、5.11。

### [ ] M5-2 导出任务、执行、下载
- **范围：** requirements 5.11 其余部分：范围和预检、冻结、交付后变更、补交、取消、后台任务执行器（单服务器版：心跳查取消，启动时把 running 的放回队列，重试和退避）、下载、到期清理；`export.changed` 事件。
- **完成标志：** API 测试覆盖预检、冻结（建任务后重扫不影响这次导出）、交付后变更的几种原因、执行和下载的端到端（下载 zip 核对清单和明细）；**并发/恢复**：执行中重启 server 后能接着完成；截图脚本 `m4-export.mjs` 通过。
- **前置：** M4-1、M5-1。
- **先读：** requirements 3.6、5.11、7。

### [ ] M5-3 服务端验收
- **范围：** 用 Go 服务端把上一版 M1–M4 的主流程截图脚本全部跑一遍（`web/scripts/shots/m1-accept.mjs`、`m2-accept.mjs`、`m3-accept.mjs`，依赖桌面客户端的步骤跳过，留给 M6）；补齐 README（本机开发、跑测试、跑截图脚本）；整理遗留问题。
- **完成标志：** 总控向用户报告服务端完成情况。

---

## M6 桌面客户端（Wails）

开工前和用户定：Wails v2 还是 v3（v3 自带托盘、还在 beta）；安装 Wails 命令行工具。任务到时再拆：客户端骨架和会话代理、上传队列和热文件夹、本机图片缓存和大图切块、托盘、重扫、自检；前端里调 Tauri 的部分改成调 Wails。

---

## M7 部署交付

原上一版的 M5：Docker 镜像和 compose、install.ps1、backup.ps1（封面必须备份）、离线交付包、许可证、存储迁移、客户端自动更新、SeaweedFS 4.45 复验、在用户的 Windows 上远程验收。requirements 第 11 节列了要问用户的事，开工前一起问。
