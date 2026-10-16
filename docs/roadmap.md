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
3. M2-1 ∥ M4-1
4. M3-1 ∥ M4-2
5. M5-2
6. M5-3 服务端验收 → 向用户报告
7. M6-1 可以和服务端并行（2026-10-16 起）；M6-2 ∥ M6-3 → M6-4 → M7

---

## M1 骨架和系统管理

### [x] M1-1 server 骨架、登录会话、用户和角色（commit 833976d）
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

- **结果：** 三项检查通过；API 测试 17 个（约 2 秒）+ semver 单元测试 2 个；`m0.mjs` 13 张里 12 张通过（`reviewer-nav` 要项目和"我的审核"，M1-3、M3-1 之后再跑）。依赖：pgx/v5 v5.11.0、google/uuid、x/crypto v0.55.0（**x/crypto v0.56 起要 go 1.26，加依赖用 `GOTOOLCHAIN=local go get …`，别让 go.mod 升到 1.26**）。doclite_dev 已执行 0001–0003，有 admin / admin123456 和 seed-demo 建的演示用户。
- **自定细节：** 客户端版本检查（426、`invalid_client_version`）已做；`/api/projects` 已按授权查询；登录在 auth 的事务里写 `users.last_login_at`（跨模块例外，store 里注明）；JSON 请求体要 application/json、上限 1 MiB（413）、未知字段忽略；`/api` 下没有的路径 JSON 404、方法不对 JSON 405；时间按 UTC 输出；argon2id m=19 MiB t=2 p=1，同时计算数 = CPU 核数（2–16），排队超 5 秒 503 `busy`，登录名不存在也做占位校验；停用、重置密码同一事务撤销会话；用户、角色的写事务先锁 admin 角色那一行（防提权和"最后一个管理员"在并发下绕不过去），都写审计；`.env` 只读当前目录，`WEB_DIR` 不设就不托管网页，`RUST_LOG` 不再读（用 `LOG_LEVEL`）。迁移执行器：咨询锁 `(6000,1)`，已执行的文件核对 sha256，库里有不认识的版本拒绝启动。
- **须知（后面所有任务）：**
  - **路由：** 在 `app.New` 里建 Service、Handler，`xxx.NewHandler(svc).Routes(api, gate)`；Routes 里 `r.Handle("GET /api/folders/{id}", g.AnyMenu(auth.MenuScan, auth.MenuFiles), h.get)`、`g.AllMenus(...)`、`g.Public`、`g.Login`（访问控制必须给，nil 启动 panic）；当前用户 `auth.Current(ctx)`。
  - **handler：** `func(w, r) error`；`httpx.PathUUID(r, "id")`、`httpx.ParsePage(r)`、`httpx.ReadJSON(r, &req)`、`httpx.WriteJSON(w, 201, v)`、`httpx.NewPage(items, total, page)`。
  - **错误：** `httpx.Invalid(...)`、`httpx.NotFound("文件夹")`、`httpx.Conflict("tree_cycle", "…")`、`httpx.AdminRequired`、`httpx.NewError(...).WithDetails(...)`；其他错误一律 500 `internal`（细节只进日志）。
  - **数据库：** `db.InTx(ctx, pool, func(tx pgx.Tx) error {...})`、`db.NewID()`、`db.UniqueViolation(err, "约束名")`；审计 `audit.Write(ctx, tx, audit.Entry{...})`；store 是包内函数，第一个参数 `db.Querier`。
  - **API 测试：** `t.Parallel(); env := NewEnv(t)`（自己的库、httptest server、admin / `admin-pass-123`）；`env.Admin()`、`env.Login`、`env.Anon()`、`env.As("reviewer" | "none" | "system.users" | "catalog+review_assign")`、`env.CreateUser`、`env.CreateRole`、`env.Exec`、`QueryValue[T]`；`.Expect(201)`、`.ExpectError(409, "last_admin")`、`.JSON(&v)`、`.ID()`；权限表 `CheckPerms(t, env, table)`（表头为角色码、`none`、菜单码组合，可选 `anon`；`Y` 放行、`-` 403、`anon` 401）。`S3Env()`、`S3Prefix(t)`（**M1-3 加 S3 客户端后在这里注册清理前缀**）。测试库：模板 `doclite_test_tpl_<迁移哈希>`，克隆 `doclite_test_<随机>`，强杀可能留下克隆库，可手工删。
### [x] M1-2 shared 库：文件头解析、名称规则、档号模板（commit 4b442fd）
- **范围：** `shared` 模块（纯函数，不碰数据库和网络）：
  - `imagehdr`：JPEG、PNG、TIFF 文件头解析（requirements 5.6"文件头"：格式、宽高、DPI、颜色模式、方向、多页 TIFF、像素上限；不解码像素）；
  - `names`：文件和文件夹名称规则、Windows 保留名（5.5）；项目编码规则（5.3）；
  - 档号模板的解析和渲染（5.9"档号和件号"：占位符、补零、分隔符、缺级留空）。放在 `shared` 还是 `server/internal/catalog`，实现者定，说明理由。
  - **测试样本**：上一版的截图脚本和测试用 Rust 的 `gen-samples` 现场生成样本（JPEG、TIFF 无压缩、LZW、G4、PNG 等 18 种）。在 `../bak/doclite-v3/shell/src-tauri` 用 `cargo run --example gen-samples` 生成一次，挑够用的放进 `server/testdata/samples/`（总共几 MB 以内）提交；`web/scripts/lib/upload-samples.mjs` 改为读这个目录，不再调 cargo。
- **完成标志：** 单元测试覆盖样本文件的解析结果、名称规则、模板的合法与非法写法。
- **先读：** requirements 5.3、5.5、5.6、5.9；上一版 `../bak/doclite-v3/server/crates/doclite-infra/src/images/` 只作规则参考。

### [x] M1-3 项目和授权、存储配置、预签名、实时通道（commit efb9b7f）
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
- **结果：** 三项检查通过；API 测试 28 个、单元测试新增 5 个（events 和 stream 加 `-race` 也通过）；`m0-storage.mjs` 3/3、`m1-projects.mjs` 6/6；`m0.mjs` 12/13（reviewer-nav 只缺 M3-1 的 `GET /api/me/reviews`、`GET /api/projects/{pid}/review-settings`）。依赖 aws-sdk-go-v2 v1.47.3、credentials、service/s3、smithy-go（Apache-2.0）。doclite_dev 现在有默认存储"SeaweedFS（随系统部署）"（前缀 `doclite/`）和 DEMO-A、DEMO-B。
- **自定细节：** `expected_updated_at` 用 `time.Equal` 比较（换时区相同、截到毫秒不同）；项目改名加锁顺序 归类树锁（共享）→ 归类根行 → 文件树锁（共享）→ 项目行 → 根文件夹，发 `folders.changed`（根文件夹）；删项目只写 `projects.deleted_at`；PATCH 忽略 `project_code`；settings 存原文、读时补默认、已知键校验（错误以"settings 不对："开头，著录项带 `item_fields[i]`）；授权每类 ≤ 1000，不存在的 id 400 `details.ids`；密文 `0x01|key_id|nonce|密文+tag`，AAD 绑定行 id；存储写操作进程内互斥；测试连接用自己签的原生 HTTP 请求。**坑：SDK 的 v4 签名器按"access_key + 日期"缓存派生密钥、不看 secret_key，共用签名器会拿到旧密钥的签名——每次签名都要新建签名器。**
- **遗留：** `s3.PresignGet` 还不能带 `response-content-disposition`（M5-2 扩展）；SDK 客户端每次新建、没有缓存；分片上传没做（`Put` 要 `io.ReadSeeker`）；`reject_reasons` 读时"空列表按默认"留给 M3-1。
- **须知（后面所有任务）：**
  - **提交后发事件：** `s.events.InTx(ctx, s.pool, func(tx pgx.Tx, out *events.Pending) error { …; out.Add(events.FilesChanged(pid, folderIDs, fileIDs)); return nil })`；不在事务里的用 `bus.Publish(...)`。构造函数 `FoldersChanged(pid, ids...)`、`FilesChanged(pid, folderIDs, fileIDs)`（> 500 只带 folder_ids）、`CatalogChanged(pid, catIDs, itemIDs)`（无条目输出 `[]`，> 500 不带）、`ExportChanged(pid, ids...)`、`Resync(pid)`。
  - **项目授权：** `g.InProject(g.AnyMenu(auth.MenuX), auth.PathProject("pid"))`；资源 id 参数时自己写 `func(r *http.Request) (uuid.UUID, error)` 反查项目（不存在或已删除 404）；handler 里 `auth.ProjectID(ctx)`；请求体带项目用 `authSvc.CheckProjectAccess(ctx, uid, pid)`，批量用 `authSvc.AccessibleProjects(ctx, uid, ids)`。
  - **树锁：** `project.LockFileTree(ctx, tx, pid, exclusive)`、`project.LockCatalogTree(...)`（事务级咨询锁 6001、6002，键是 pid 的 FNV-32），顺序见 `project/locks.go` 注释和 requirements 3.6。
  - **存储：** `storageSvc.DefaultTarget(ctx)` → `{ProfileID, PathPrefix, URLExpireHours, Client}`（没有默认 409 `storage_unavailable`）；写快照的事务里先 `storageSvc.LockForSnapshot(ctx, tx, profileID)`（不再是 active 默认 409 `storage_changed`）；`storageSvc.Client(ctx, profileID)`；`s3.Client` 有 `Put`（`io.ReadSeeker`，可设 ContentType、CacheControl、Metadata）、`Get`、`Delete`、`List`、`DeletePrefix`。
  - **预签名：** `s3.PresignGet(cfg, key, urlExpireHours, now)`、`s3.SignTime(now)`、`s3.PresignExpiry(h)`；快照用 `storageSvc.ImageURLs(ctx, []storage.ObjectRef{...})` → 带 `cover_url`、`original_url`、`url_expires_at`、`url_error?` 的结构，可以直接嵌进 batch、snapshots 的响应。
  - **给 M4：** `project.EffectiveSettings`、`IsValidDateValue`、`CheckTextValue`、`DefaultTextMaxLength`、`MaxTextMaxLength`、`TitleKey`、`DefaultRejectReasons`。
  - **apitest：** `env.UseS3()` → `(profileID, prefix)`；`S3Client(t)`；`S3Prefix(t)`（结束时清理前缀）；`PutObject`、`env.CreateProject(code)`、`env.GrantProject(pid, users...)`；`openStream`、`openStreamStatus`；`NewEnv(t, fastStream)`。
---

## M2 文件目录和上传

### [x] M2-1 文件目录、上传、图片信息、日志（commit b119da4）
- **范围：** 文件夹树和列表（children）、新建、改名、备注、移动、复制、删除、批量移动删除（5.5）；上传（5.6：multipart 字段顺序、流式写临时文件和 sha256、文件头、封面校验、查重和断网重试、预检 `files:check`、内容锁）；`files/batch`、`snapshots`（5.7）；文件日志；`files.changed`、`folders.changed` 事件。重扫接口放到 M3-1。
- **完成标志：** API 测试覆盖主流程和主要错误码；**并发**：同内容并发上传只入库一个；截图脚本 `m1-files.mjs`、`m1-organize.mjs`（浏览器上传部分）通过。
- **前置：** M1-2、M1-3。
- **先读：** requirements 3、5.5、5.6、5.7、7；key-queries 的上传、path_code 分配、移动、软删除。

- **结果：** 三项检查通过（加 `-race` 也通过）；API 测试新增 9 个（apitest 共 37 个）、封面校验单元测试 13 例；**并发**：同内容并发上传只入库一个（临时去掉内容锁时测试失败，证明有效）；`m1-files.mjs` 10/10、`m1-organize.mjs` 10/10。doclite_dev 的 DEMO-A 灌了 2025 文书档案下的盒 WS-2025-001（2000 页）、002（48 页）、003（24 页 + 补扫 6 页、附件 4 页）和空的盒 004，对象在 `doclite/DEMO-A/` 下。
- **自定细节：** 内容锁用 PG 事务级咨询锁 `(6003, fnv32(项目 id + sha))`（提交时自动释放）；临时文件 `TMP_DIR/uploads/upload-*.part`，启动只清这个目录，封面放内存；上传路由最外层 `UploadBody` 中间件先读完剩余请求体再写响应、60 秒空闲按 400；对象带 `x-amz-meta-original-name` 和 `Cache-Control: private, max-age=<预签名有效期>, immutable`；快照 `created_at` 用 `clock_timestamp()`；复制的快照 `parent_snapshot_id` 指向源、`edit_ops` 记来源；snapshots 进不了项目 404，logs 403；批量检查顺序 空或 > 500 → 跨项目 400 → 授权 403 → 不存在 404（details.ids 是 folder_files id）；文件夹审计 target `folder`（create、update、move、delete）；文件日志 detail 见 `internal/file`。
- **遗留：** 重扫的"同一文件串行"锁（M3-1 做）；孤儿对象只记日志不清理；children 不分页；删文件夹遇到已归类的 409 不带 details.ids；M4-2 合入前详情面板的归类信息显示"文件已不存在"。
- **须知（给 M3-1、M4-2、M5-2）：**
  - **模块依赖方向：** folder 引用 file 的包级函数（`LockFiles`、`WriteLogs`、`Current`、`InsertCopy`）和 `file.Slot`；file 通过接口 `file.Folders`（由 `folder.Service` 实现，在 app.New 注入）领位置、插入 folder_files、取路径。两个包不互相 import。
  - **路径和反查：** `folderSvc.Path(ctx, q, folderID)`、`Paths(ctx, q, ids)`（`项目名/…/文件夹`）；`folderSvc.ProjectOf`（文件夹）、`ProjectOfEntry`（folder_files id）、`fileSvc.ProjectOfFile`（file_id，文件、文件夹、项目都要未删除，否则 404）。
  - **当前快照：** `file.Current(ctx, q, fid)`、`file.CurrentOf(ctx, q, ids)`。
  - **锁文件：** 事务里 `project.LockFileTree(ctx, tx, pid, false)` → `file.LockFiles(ctx, tx, ids)`（id 升序 FOR NO KEY UPDATE）→ 重读 folder_files（未删除、所在文件夹未删除）。
  - **文件日志：** `file.WriteLogs(ctx, tx, file.Log{ProjectID, FileID, Action: file.ActionApprove, OperatorID, Detail})`（多条一次 INSERT，保持顺序；action 常量在 file 包）。
  - **改 `folder_files.review_status`（M3-1）：** folder 模块现在没有对外的写方法，M3-1 在 folder 里加一个，或者在 review 的 store 里写明跨模块写的例外。
  - **重扫复用上传：** 路由 `httpx.Chain(fileSvc.UploadBody(), g.InProject(...))`；`u, err := fileSvc.Receive(r)`（`defer u.Close()`）；之后用 `context.WithoutCancel`；事务外 `storageSvc.DefaultTarget` → `file.ProjectCode` → `file.KeysFor(prefix, code, fid, v, u.Info.Format)` → `fileSvc.PutObjects`；事务内 文件树锁 → `LockFiles` → 重读、查状态和版本 → `storageSvc.LockForSnapshot` → `file.LockContent` → `fileSvc.FindDuplicate(ctx, tx, pid, u.SHA256, &fid)` → `file.InsertSnapshot(&file.NewSnapshot{SnapshotType: file.SnapshotRescan, …})` → 写日志 → 提交后发 `files.changed`。
---

## M3 审核

### [x] M3-1 审核分配、审核、重扫（commit 84f5082）
- **范围：** requirements 5.8 全部（分配、改派、审核、批量通过、撤回通过、读接口、审核设置）和 5.6 的重扫、待重扫列表；状态流转表；事件。
- **完成标志：** API 测试覆盖状态流转表的每一格和主要错误码；**并发**：审核和整理（移动、删除）同时操作不死锁；截图脚本 `m2-assign.mjs`、`m2-review.mjs`、`m2-rescan.mjs`（浏览器部分）通过。
- **前置：** M2-1。
- **先读：** requirements 3.6、5.6（重扫）、5.8；key-queries 的分配审核、通过。

- **结果：** 三项检查通过；apitest 新增 6 个（权限表 14 接口 × 10 角色、状态流转表 28 格 + 2 格、分配改派、审核/批量/撤回/我的审核、重扫、并发）；**并发**：审核、改派、撤回和批量移动、移动文件夹、删除同时跑不死锁、5 条 SQL 一致性检查通过；同一文件同时两个重扫串行（临时去掉互斥时测试失败）。截图 `m0.mjs` 13/13（reviewer-nav 通过）、`m2-assign.mjs` 6/6、`m2-review.mjs` 浏览器部分 13/13（`shell-review`、`shell-review-compare` 要客户端，留给 M6）、`m2-rescan.mjs` 6/6（另跑了 `M27_E2E=1` 的 e2e）。doclite_dev：新建 demo_reviewer2，DEMO-A 授权 demo_reviewer、demo_reviewer2；演示文件夹「M2-5 分配演示」40 页、「M2-6 审核演示」30 页、「M2-7 重扫演示」10 页。
- **自定细节：** 改审核状态在 `internal/folder/review_state.go` 加对外写方法 `folderSvc.SetReviewStatus(ctx, tx, fileIDs, status)`、`SetReviewState(ctx, tx, fileID, status, round)`，review 不直接写 folder_files；读接口 cursor `<path_code>_<sort_no>_<file_id>`；`reviewed_at` 用 `statement_timestamp()`；重扫后新开的分配 `assigned_by` 沿用被驳回那一行；批量里不在本项目的文件 404（details.ids 是 file_id）；撤回通过先查状态 409 再查是不是自己通过的 403 `not_assignee`；驳回原因个数按请求算、不超过项目列表长度和 64、之后去重；日志 `skipped_reviewer` 写原审核人的显示名；大的读查询用 `pgx.QueryExecModeDescribeExec`（EXPLAIN 确认"我的审核"走 0002 的索引）；重扫串行用进程内按 file_id 的互斥（从预检、传 S3 到提交整段在锁里）。
- **遗留：** 重扫"版本号对不上 409"的路径测不到（重扫已串行，以后插件追加快照才会走到）；没做 10 万文件的性能测量；重扫写库失败的对象只记警告；`is_current` 取 id 最大的那一行（撤回给了不能再审的人时，原审核人的"通过"仍标为 is_current）。
- **须知（给 M4-2、M5-2）：** 审核状态读 `folder_files.review_status`、`review_round`（常量 `folder.ReviewUnassigned/Pending/Approved/Rejected`），其他模块只读不锁，只有 review 经 `folderSvc.SetReview*` 写；审核只拿"文件树共享锁 → files 行"，不改 `archive_items.row_version`；"最近一次通过/驳回"按 `file_reviews` id 倒序，同一文件同时只有一行 open；审的是哪一版 = 最近一次 done 行的 `snapshot_id`；file_logs action assign_review、reassign_review、approve、reject、reopen、rescan，detail 带 `version_no`、`review_round`；测试辅助 `apitest/reviews_fixtures_test.go` 的 `newReviewEnv`、`fileIn(state, name)` 造任意审核状态的文件。
---

## M4 归类和看板

### [x] M4-1 归类树、档号、条目、著录、定稿（commit e32f44b）
- **范围：** requirements 5.9 里不涉及文件的部分：节点（增删改移、编码和类型、按需加载）、档号和件号（含撞号规则、按新模板重新生成、空档号原因）、条目（增删改移、状态、著录校验、旧值规则、定稿和解锁、批量定稿）、`catalog-settings`；`catalog.changed` 事件（7）。删除条目、定稿前置里"≥ 1 页""全部页通过"先按"没有挂接行"处理（挂接在 M4-2）。
- **完成标志：** API 测试覆盖条目状态表、档号生成和撞号、著录校验；截图脚本 `m3-catalog.mjs` 里不涉及挂接的部分通过（有挂接的步骤 M4-2 再跑）。
- **前置：** M1-3（和 M2-1 并行）。
- **先读：** requirements 3.6、5.9、7。

- **结果：** 三项检查通过；API 测试 8 个（权限表、节点、条目状态表 8 × 3、定稿和批量定稿、档号生成/撞号含分档和互换/缺级、著录校验 17 例和旧值规则）+ 分档分配单元测试 1 个。正式的 `m3-catalog.mjs` 因为要先建文件夹、上传（M2-1）在准备数据时全部失败；用不涉及文件的脚本变体验证了 21 张（树、表单校验、400 定位、旧值、定稿只读、解锁、改编码、撞号、同步、批量定稿、节点操作、重连、事件合并、顶层条目、移动、换项目确认、虚拟滚动 1200 条）全部通过。这次运行往 DEMO-A 写了著录项，并把 demo_cataloger 加进了 DEMO-A 的授权。
- **自定细节：** 新的咨询锁类别 **6005（档号）**：共享归类锁下写档号（新建、移动、改件号）先按（项目, 档号）拿 `pg_advisory_xact_lock(6005, …)` 再查占用，后到的等前一个提交，不会撞 `ux_archive_items_no`（排在节点行、条目行之后，每个事务最多一个；批量重算拿归类树排他锁，不用它）；批量重算在应用里分档，先把"旧号正好是别人新号"的置空，再一条 `UPDATE … FROM unnest`；审计按条批量写；件号到 999999999 之后 409 `invalid_state`。条目列表 limit 默认 500（1–2000），cursor `<件号>_<id>`；删除可带 `?row_version=`。
- **临时处理（M4-2 替换）：** 有页的条目删除返回 409 `invalid_state`（`TODO(M4-2)`）；新建条目带非空 `file_ids` 返回 400。
- **须知（给 M4-2）：** 锁顺序 `project.LockCatalogTree(ctx, tx, pid, false)` → 分配件号时锁节点行（`lockCategories`）→ 条目行（`lockItem`、`lockItems`，`FOR NO KEY UPDATE`，id 升序）→ 之后才拿文件树锁和 files 行；锁住后依次查 已删除 404 → 状态（`errLockedEdit`）→ `row_version`（`staleVersion`）。定稿判断 `item_state.go` 的 `lockBlocker`，页数和审核状态来自 `itemPageStats`（读 `archive_item_files`、`folder_files`，不锁文件），挂接做好后不用改。档号：单个 `assignArchiveNo`（撞号留空、返回撞了谁），子树 `renumberSubtree`（要排他锁），空档号原因 `emptyReason`。`readItem`、`itemPages` 已能读页（文件名、路径、版本、"定稿后有新版本"），`page_counts`、`locked_at` 已算好。测试里的 `addPage` 直接写库造页，M4-2 后改走 `PUT /files`。
### [x] M4-2 挂接、页序、归类信息、看板（commit 1bfb960）
- **范围：** requirements 5.9 的"挂接"（PUT files、新建条目带 file_ids、删除条目一并移出、日志、reorder 审计）、"归类信息"（placements、`files/{fid}/catalog`）、定稿前置里和页相关的条件、5.10 看板。
- **完成标志：** API 测试覆盖挂接的主要错误码；**并发**：两个条目抢同一个文件只有一个成功、审核和挂接同时进行不互相等（加锁顺序见 3.6）；截图脚本 `m3-catalog.mjs`、`m3-workbench.mjs`、`m3-dashboard.mjs`、`m3-catalog-info.mjs`、`m3-catalog-settings.mjs` 通过。
- **前置：** M4-1、M2-1（M3-1 合入后再跑审核相关的截图）。
- **先读：** requirements 3.6、5.9、5.10、7。
- **结果：** 三项检查通过；apitest 新增 6 个（apitest 共 57 个）、catalog 单元测试 3 个；**并发**：两个条目抢同一个文件只有一个成功（含带 file_ids 新建）；挂接在等条目行时，移动文件夹约 6–8 ms、真实审核通过约 4–7 ms 完成（临时把顺序改成"先文件树锁再条目行"时测试失败）。截图 `m3-catalog` 20/20、`m3-workbench` 20/20、`m3-dashboard` 5/5、`m3-catalog-info` 4/4、`m3-catalog-settings` 5/5。一轮审查：七个重点都没发现缺陷；审查另做 3 轮并发压测（10 goroutine × 6 秒，每轮 1.1–2.3 万次混合写），0 个 5xx、没有死锁、不变量都成立；挂接和删除同一文件两种先后各 10 次，结果只有"挂接 200 + 删除 409"或"删除 204 + 挂接 404"。
- **自定细节：** PUT 检查顺序 参数 400 → 条目 404 → `item_locked`（状态先于版本）→ `conflict` → 文件 404（details.ids 是 file_id，按请求顺序）→ 409 `file_cataloged`（details `{ids, items}`）；`files.changed` 的 file_ids 先列移出、再列挂上；reorder 审计 `{page_count, linked, unlinked, moved:[{file_id, file_name, from_page, to_page}]}`；唯一约束兜底 409 `conflict`；只锁住了 files 行的文件才挂，重读时多出来的按 404。`shared/archiveno` 加了 `debugQuote`，让模板"{ 没闭合"的提示和前端一致。看板整个项目约 19 ms（2250 个文件）。doclite_dev：DEMO-B 建了常驻文件夹「2025 科技档案」，项目 M37-SET（脚本建的）。
- **遗留：** 看板耗时随全系统挂接行数增长（`archive_item_files`、`archive_items` 没有按项目的索引），没在 10 万文件规模上测。
- **须知（给 M5-2）：** 条目的页 `archive_item_files WHERE archive_item_id = … ORDER BY page_no`（页号一定 1..n，页对应的文件和文件夹一定未删除）；当前版本 `file.CurrentOf`；catalog 里读页的函数没导出，export 在自己的 store 里写只读查询（注明跨模块读）；"已归类" = `archive_item_files` 有这行；已定稿 = `status = 'locked'`，定稿时间取最近一条 `archive_item/lock` 审计；6005 只给新建、移动条目、改件号用，**拿着文件树锁或 files 行的事务不能再拿 6005**。
---

## M5 导出

### [x] M5-1 导出物生成（PDF、原图包、目录、清单）（commit 9b80d3a）
- **范围：** `internal/export` 里的生成部分（纯函数，输入冻结的条目和页、读原图的接口，输出写进一个 writer 的 zip）：PDF 原样嵌入、放不进的无损转换（硬性规则 11 的例外）、横纵 DPI、原图包、`目录.xlsx`、`清单.csv`、Windows 安全的文件名（requirements 5.11"导出物"）。
- **完成标志：** 单元测试覆盖常见扫描格式（JPEG、G4 TIFF、LZW TIFF、无压缩 TIFF、PNG、带透明的 PNG）原样嵌入或转换的结果（嵌入的字节、转换的像素）；用 macOS 的 `sips` 抽查 PDF 能打开、尺寸对。罕见格式不逐个做。
- **前置：** M1-2（和 M1-3 并行）。
- **先读：** requirements 2（第 11 条）、5.11。

- **结果：** `server/internal/export/bundle`，单元测试 51 个用例（覆盖率约 71%），macOS 上用 sips 渲染抽查；依赖 excelize v2.11.0、x/image v0.44.0（正式代码只用 `tiff/lzw`）。**JPEG 压缩的 TIFF（7 和旧式 6）也原样嵌入**（用户要求 PDF 不限图片类型；总控任务说明一开始写错成不支持，已补）。
- **原样嵌入：** JPEG（基线、渐进，1/3/4 分量，Adobe CMYK 反转）；PNG（不隔行、无 alpha 和 tRNS，拼 IDAT + Predictor 15）；TIFF 无压缩（拼一个对象）、LZW/Deflate/PackBits（每条带一个对象，8 位预测器写进 DecodeParms）、CCITT MH/G3/G4、JPEG 压缩（每条带或分块一个 DCTDecode，拼 JPEGTables，YCbCr 写 ColorTransform 1）；WhiteIsZero 用 Decode，FillOrder=2 按位反转；调色板写成一份间接对象。**转换**（Flate + 预测器，像素不变，透明转 SMask）：带 alpha/tRNS/隔行的 PNG；TIFF 分平面、额外通道、16 位小端、16 位带预测器、16 位调色板、条带 > 1024、PackBits 里有 128、分块（非 JPEG）——条带、分块自己解压（`x/image/tiff` 不支持分平面且会悄悄解错）。**不支持**：旧式 LZW、浮点或有符号样本、YCbCr（非 JPEG）、CIELab、预测器 3、分块的 CCITT、JPEG 无损/分层/算术/12 位、JPEG 压缩又分平面或带 alpha。
- **自定：** DPI 规则"另一个方向在 50–2400 之间才各用各的"（600×5000 按 600×600）；页面边长限制 3–14400 磅；新增可重试的错误类 Temp（临时盘）。
- **遗留：** 一件 PDF 超过约 9.3 GB 报 TooLarge（没写交叉引用流）；超过 4 GB 的 zip 条目用数据描述符 + zip64，本地文件头没有 zip64 扩展字段（Info-ZIP、7-Zip 能读；Windows 自带解压要在 M7 实测）；转换整张解码，受 `ConvertMemoryLimit` 限制；72 DPI 的图按规则当不合理、按 300 算，页面会显得很小。
- **须知（给 M5-2）：**
  - `Generate(ctx, job *Job, src PageSource, w io.Writer, opts Options, progress func(Progress)) (Summary, error)`；`Format` 是 `FormatPDF = "pdf"`、`FormatImages = "images"`（和 `export_tasks.export_format` 一致）；`Options{TmpDir, Now（传 UTC+8 的时间，zip 的 DOS 时间按它的时区写）, ConvertMemoryLimit}`；`Progress{PagesDone, PagesTotal, ItemsDone, ItemsTotal, BytesWritten}` 每页回调一次，节流由调用方做；`Summary{Size, Items, Pages, EmbeddedPages, ConvertedPages, Names}`。
  - `PageSource` 两个方法：`Open(ctx, Page) (io.ReadCloser, error)`（整个对象）、`ReadAt(ctx, Page, b, off)`（Range 读，到末尾返回 `io.EOF`，只有 Inspect 用）。
  - 错误统一是 `*bundle.Error{Kind, Reason}`，`Retryable()`（Source、Temp、Output）、`PageProblem()`（Integrity、Corrupt、Unsupported、TooLarge，带档号、页号、file_id、snapshot_id、`ItemIndex`、`PageIndex`），`IsRetryable(err)`；Canceled 时 `errors.Is(err, context.Canceled)` 成立。出错后从这一页之后对剩下的页调 `Inspect(ctx, src, job.Format, item, page, opts)` 列出全部问题页（查不出 sha256 不对和压缩数据内部损坏）。
  - 成功时 zip 已写完但 writer 没关，由调用方完成分片上传；失败或取消时调用方中止上传。`Page.ReviewStatus` 传执行时 `folder_files.review_status` 的当前值。页号只要求严格递增，原图包文件名用页号。
### [x] M5-2 导出任务、执行、下载（commit f122a57）
- **范围：** requirements 5.11 其余部分：范围和预检、冻结、交付后变更、补交、取消、后台任务执行器（单服务器版：心跳查取消，启动时把 running 的放回队列，重试和退避）、下载、到期清理；`export.changed` 事件。
- **完成标志：** API 测试覆盖预检、冻结（建任务后重扫不影响这次导出）、交付后变更的几种原因、执行和下载的端到端（下载 zip 核对清单和明细）；**并发/恢复**：执行中重启 server 后能接着完成；截图脚本 `m4-export.mjs` 通过。
- **前置：** M4-2、M5-1。
- **先读：** requirements 3.6、5.11、7。

- **结果：** 三项检查通过；Export 部分 `-race` 连跑多轮稳定；`m4-export.mjs` 12/12。API 测试：预检、端到端（5 种样本上传→分配→通过→挂接→定稿→导出 PDF 和原图包→下载核对 zip、清单、明细、sha256、Content-Disposition、file_logs、审计、事件）、冻结和交付后变更 8 种原因加补交、取消（排队中、执行中）、恢复（模拟被杀重启、优雅退出放回、读原图失败退避重试）、权限表、`TestExportReaper`、`TestExportMultipart`；单元测试：最长递增子序列、下载文件名、退避。一轮审查（1 中 4 低，已改）。
- **做法：** 执行器 `internal/platform/jobs`（`jobs.go` 类型和工具、`executor.go` 执行器）：短事务 `FOR UPDATE SKIP LOCKED` 领取（先登记"领取中"），进程内信号量控制并发，心跳查取消，`Interrupt` 立即通知；可重试的失败按 `backoff × 4^(n−1)`（≤ 1 小时）排队，最多 3 次；**定期回收**（周期 = 心跳）：库里 running 但不在本进程执行或领取中的 job，已取消的收尾、次数用完的 failed、其余放回；**单实例保护**：执行器在专用连接上持有会话级 `pg_try_advisory_lock(6006, 0)`，拿不到就不执行、不回收、不清导出临时目录（HTTP 照常），连接断了重拿、拿不到就停；启动顺序 先 `net.Listen` → 清临时目录 → `StartBackground`。所有改状态的事务 job 行 → 任务行，改任务一律 `WHERE status='running'`；完成时先写 file_logs 再锁行，任务已取消就回滚并删导出物；每次执行先用 `WithoutCancel` 删上一次的导出物，删不掉返回 Retry、不覆盖旧 key；分片上传（自己写的，默认每片 8 MiB，内存约 2 片）出错、取消、panic 都 Abort。冻结用 REPEATABLE READ + COPY 写明细，payload 存条目当时的状态（`payload_version` 1）。下载 `PresignGetWith` + content-disposition（ASCII 兜底名 + RFC 5987）。到期清理宽限期 `expires_at + url_expire_hours + 24h`。临时盘不够时第 1 次执行重试。咨询锁 6000–6006 的列表登记在 `internal/project/locks.go`。清单表头"版本"改成"版本号"（和上一版、截图脚本一致）。
- **遗留：** 2000 件 / 5 万页规模下建任务、交付后变更的耗时没测；160 页带透明的 PNG 转 PDF 约 22 秒（上一版约 10 秒，M5-1 的转换可优化）；进程被杀时没完成的分片上传靠运维 `s3.clean.uploads`；原图有问题时检查剩余页是 4 页并发按 Range 读，20 万页时会慢（只在出错时）；存储已停用时下载 409 的路径没测到。
- **须知（给 M5-3、M6-4）：** `app.Run` 里 `StartBackground`（`JOB_CONCURRENCY=0` 时不执行也不清理，打 warn）；测试要执行导出用 `newExportEnv(t, code, withProxy)` + `e.start()`（`proxy.delay`、`proxy.failGets`），辅助 `approvedPages`、`lockedItem`、`createTask`、`waitDone`、`zipEntries`、`manifestRows`；`App.Exports.Cleanup(ctx)`；下载返回 JSON，url 是带 content-disposition 的预签名地址，壳交给系统浏览器。**同一个库上只有一个 server 进程执行后台任务**（用户的 9080 和子任务的 2908x 共用 doclite_dev 时，谁先启动谁执行）。
### [x] M5-3 服务端验收（commit d7ebfc9）
- **范围：** 用 Go 服务端把上一版 M1–M4 的主流程截图脚本全部跑一遍（`web/scripts/shots/m1-accept.mjs`、`m2-accept.mjs`、`m3-accept.mjs`，依赖桌面客户端的步骤跳过，留给 M6）；补齐 README（本机开发、跑测试、跑截图脚本）；整理遗留问题。
- **完成标志：** 总控向用户报告服务端完成情况。

- **结果：** 浏览器能跑的截图 **176/176**（m0、m0-storage、m1-projects/files/organize/upload-lan/accept、m2-assign/review/rescan/viewer/viewer-lan/accept、m3 五个加 m3-accept、m4-export）；服务端代码没改；`go test ./...`（apitest 65 个）、`-tags perf` vet、shared、`npm run build` 都过。依赖壳的部分（Tauri 模拟、tiles-bench）全部跳过，留给 M6-4；`m1-shell`、`m1-scan`、`m1-cache` 没跑。m3-accept：审核和归类并行时写请求中位数 7–16 ms；m2-accept：分配 100 页 175 ms、键盘审 80 页 6.8 s。
- **性能（`go test -tags perf -run TestPerf ./apitest/`，10 万文件、1000 盒、6 万页挂 2400 件）：** 我的审核 118–149 ms（目标 200）、review-summary 13–28、看板 5–51、placements 7（目标 100）、导出预检 55–89（目标 1 s）、交付后变更 304、**建导出任务（2000 件 / 5 万页）827–985 ms，最慢 1033**，大头是 COPY 5 万行明细（外键检查约 0.3–0.4 s），加索引无用 → 问用户。
- **改动：** `README.md`（本机开发、测试、perf、截图脚本、客户端打包和自检、后台任务、演示账号）；`server/apitest/perf_test.go`（build tag perf）；脚本 `m1-accept`、`upload-samples`（改读 `server/testdata/samples`，`sampleVariant`；虚拟列表滚动收齐）、`m2-viewer-lan`（按 sort_no）、`m2-accept`（`M2_ACCEPT_SHELL_MOCK`、`M2_ACCEPT_BIG_G4`）；**前端 `OriginalViewerDialog.vue` 的 `canReadLogs` 默认值改成 true**（上一版就有的 bug：/files 对比原图从不取日志、没有驳回原因，即 M6-3 记的 viewer-compare 问题）。
- **遗留：** Windows 验证项（zip64、导航保护、托盘、证书、CSP、混合内容、热文件夹文件锁、中文长路径、安装包等）和 SeaweedFS 4.45 复验、生产硬件（WSL2 + Docker、PG 18）重测性能 → M7。客户端断开的请求被记成 ERROR / 500（`httpx/errors.go`）→ M5-4。以后再做：S3 客户端无缓存、原图上传不分片、孤儿对象只记日志、children 不分页、删含已归类文件的文件夹 409 不带 details.ids、`is_current` 边角、导出单件 PDF > 9.3 GB 报错、透明 PNG 转换慢、看板暂不加索引、kqueue 每文件一个句柄、图标是占位的。
- **须知：** 各 worktree 的 `.env` 共用 `TMP_DIR=/tmp/doclite`，server 启动时清空 `uploads/` → 子任务起 server 一律设自己的 `TMP_DIR`（已写进 CLAUDE.md）。doclite_dev 的 DEMO-A 留有「M1 / M2 / M3 验收演示 2026-10-16」、全宗 Z308、8 个导出任务；`doclite_test` 下有 7 个没连接的 `doclite_test_<随机>` 克隆库（测试中断留下的）。

### [x] M5-4 服务端小修：客户端断开不算错误（commit 41f0e49）
- **范围：** 客户端中途断开（翻页、关页面）导致的 `context canceled` 现在记成 `level=ERROR status=500`；改成 info 级别、状态 499（只记日志，不写响应），SSE 正常断开也不报错。
- **完成标志：** API 测试：请求进行中取消上下文，日志里不出现 ERROR；三项检查通过。

---
- **结果：** 三项检查通过；`TestClientDisconnect`（看板请求卡在等锁时取消 → 访问日志 499、INFO；SSE 连上后断开 → 200、INFO；全程无 ERROR）加 `-race -count=30` 稳定；撤掉修复时测试失败、日志正是事故那两行。
- **做法：** `httpx.WriteError` 里 `clientGone(r, err)`：请求 context 的取消原因是 `context.Canceled` 且错误链里有 `context.Canceled` → 只写 499（`httpx.StatusClientClosedRequest`）、Debug 日志；服务端自己的超时、handler 自己派生的 context 仍按 500 ERROR。鉴权、项目授权、版本检查都走 `WriteError`，一处生效。SSE 刷响应头时失败且请求已取消 → Debug。apitest 新增 `NewEnvWithLog(t, log, …)`。
- **遗留：** 上传请求体传到一半断开记 400 `invalid_input`（INFO）；原图已进 S3、写库前断开会留孤儿对象（Warn），以后可让写库一步 `WithoutCancel`。

## M6 桌面客户端（Wails）

**用户决定（2026-10-16）：** 用 **Wails v3**（当前 v3.0.0-beta.29，自带托盘、单实例、多窗口；v2 没有自带托盘）。命令行工具 `~/go/bin/wails3` 已装，`wails3 doctor` 通过。

**做法：** 客户端放在 `client/`（Go 模块 `doclite/client`，加进 `go.work`），界面就是 `web/` 的构建产物。前端和壳之间的接口沿用上一版：约 26 个命令（`shell_info`、`probe_server`、`check_server`、`set_server`、`api_fetch`、`api_stream`、`api_stream_cancel`、`open_external`、`tray_status`、`upload_*`、`hotfolder_*`、`pick_and_enqueue_upload`、`pick_and_rescan`、`cache_*`、`prepare_tiles`、`smoke_*`）、1 个事件（`rescan:progress`）、1 个取图协议（上一版是 `img://`）。前端只改适配层（`web/src/lib/shell.ts`、`stores/shell.ts`、`api/http.ts`、`api/stream.ts`、`lib/image-url.ts` 等），去掉 `@tauri-apps/api`，换成 Wails 的前端运行时；其余页面不动。需求见 requirements 第 6 节。

**排期：** M6-1 → M6-2 ∥ M6-3 → M6-3b → M6-4。

### [x] M6-1 客户端骨架、连接和会话代理、托盘（commit bd5d915）
- **范围：** `client/` 的 Wails v3 应用：窗口加载 `web/dist`；单实例（再启动聚焦已有窗口）；数据目录（`~/Library/Application Support/Doclite/`、`%LOCALAPPDATA%\Doclite\`）、设置、日志按天滚动；首次启动填服务器地址（`probe_server`、`check_server`、`set_server`）；**会话代理**：`api_fetch`、`api_stream`（SSE 逐块转发）、`api_stream_cancel`——只许已配置 server 的 `/api/` 路径，Cookie 由壳保存（退出或换服务器清空），页面拿不到令牌，壳加 `X-Client-Type: desktop`、`X-Client-Version`；`shell_info`；`open_external`（只许 http、https）；导航保护（页面不能跳到外部地址）；页面里的 HTML5 拖放要能用（壳不接管文件拖放）；托盘（状态提示、打开、退出；关窗缩到托盘，只有"退出"真退出）；`--smoke` 的基础步骤（登录、`me`、菜单、实时通道、托盘、界面）。前端适配层改成调 Wails。
- **完成标志：** Mac 上 `wails3` 打包出 `.app`；`--smoke` 基础步骤通过；用打包的客户端连 Go server 能登录、浏览各页面（截图脚本里能在壳外验证的仍在浏览器里跑）；`npm run build` 通过；客户端的 Go 代码 `gofmt`、`go vet`、`go test` 通过（只给纯逻辑写单元测试，例如路径白名单、Cookie 处理）。
- **先读：** CLAUDE.md；requirements 第 6 节；上一版壳的规则参考 `../bak/doclite-v3/docs/design.md` §4.5、§5（只看规则）；前端 `web/src/lib/shell.ts`、`stores/shell.ts`、`api/http.ts`、`api/stream.ts`、`lib/smoke.ts`。

- **结果：** `cd client && wails3 package` 出 `client/bin/Doclite.app`（identifier `com.doclite.desktop`，0.1.0，ad-hoc 签名）；`--smoke` 基础步骤通过（probe、login、me、menus、stream、sse 心跳 15 s、img_direct、tray、ui、nav_guard、logout），连不上退出码 2、要升级退出码 3；admin 打开 14 个页面都没有出错提示；客户端 7 个单元测试；`gofmt`、`go vet`（含 `GOOS=windows`）、`go test`、`npm run build` 通过。**没有抢焦点**（自检和 `DOCLITE_BACKGROUND=1` 时不激活、不进 Dock；窗口透明放最后面，用 WebKit 私有接口关掉"被遮挡即挂起"），只有托盘图标会短暂出现。依赖：Go `github.com/wailsapp/wails/v3 v3.0.0-beta.29`（Mac 上要 cgo）；npm 去掉 `@tauri-apps/api`、加 `@wailsio/runtime@3.0.0-beta.29`（只在壳里按需加载）。
- **结构：** `client/`（模块 `doclite/client`）：`main.go`；`internal/appdata`（数据目录、settings.json、日志按天滚动保留 14 天；`DOCLITE_DATA_DIR` 可换目录）、`internal/semver`、`internal/proxy`（会话代理、路径白名单、Cookie 罐 `cookies.json` 0600、UTF-8 切分、探测）、`internal/shell`（`bridge.go` 命令表、`stream.go`、`tray.go`、`smoke.go`、导航保护）；平台资源在 `packaging/`，产物在 `client/bin/`；构建时把 `web/dist` 复制到 `client/frontend/dist` 再 embed。前端新增 `web/src/lib/wails.ts`（`invoke`、`listen`、`openShellSocket`），`lib/client.ts` 的 `isShell()`（保留 `isTauri` 别名）、`shellImages()`。
- **做法：** 只绑定一个方法 `Bridge.Invoke(命令名, 参数)`，Go 端按命令表分发，页面写法仍是 `invoke('probe_server', {url})`，错误 `{kind, message}`。`api_fetch` 由 Go 的 `net/http` 发，只许已配置服务器的 `/api/` 路径（拒绝 `..`、编码的点和斜杠等，拼好后再核对），页面只能带少数请求头，壳统一加 `X-Client-Type: desktop`、`X-Client-Version`，页面拿不到 Set-Cookie，不跟随重定向。**SSE 走 Wails 的流**（`app.HandleStream`，挂起的轮询，每帧一个完整响应）——因为 Wails 在 Windows 上会把资产服务器的响应整块攒完才交给 WebView2，SSE 不能走资产服务器。单实例、托盘（两行状态、打开、退出；关窗缩到托盘）、`EnableFileDrop: false`、Mac 上的导航保护（只许 `wails:`、`about:`）。
- **遗留（M7 在 Windows 上做或验证）：** Windows 的导航保护还没有（Wails 没公开 WebView2 的 NavigationStarting）；自检窗口在 Windows 上会直接显示；托盘左键、退出残留图标、默认菜单栏要实测；CSP（direct_blocked、csp 两步）和取图协议一起在 M6-3 做；**壳里现在直连存储看图，存储是局域网 http 地址时可能被当作混合内容拦下**（M6-3 换成取图协议）；Mac 上退出码在 Wails 的 PostShutdown 里交出（升级 Wails 时留意）；导航保护是给 `WebviewWindowDelegate` 补方法实现的；server 证书不受信任时连不上；图标是占位的；Info.plist 打开了 ATS 任意加载（内网 http）。
- **须知（给 M6-2、M6-3）：**
  - **加命令：** `internal/shell/` 下新建文件，在 `registerCommands()` 里 `handle(s, "命令名", func(ctx, args 参数结构体) (any, error))`（参数 JSON 键沿用上一版，如 `fileId`），错误返回 `*proxy.Error`（invalid_request、not_configured、network、internal、unsupported），做完从 `bridge.go` 的 `unsupported` 表里删掉。
  - **事件：** Go `s.app.Event.Emit("upload:item", 数据)`，页面 `listen(名字, payload => …)`；事件名沿用 upload:*、hotfolder:state、tray:error、rescan:progress。
  - **会话：** 上传队列、取图和页面共用 `proxy` 的同一个 `Jar` 和 `addShellHeaders` 那套请求头，401、426 时暂停；`Proxy.Observe(func(req, status, body))` 能看到每个 api_fetch 的响应（M6-3 从 batch、snapshots 记来源，在 SetServer、退出、登录成功时清空）。
  - **能力开关：** 托盘统计在 `tray.go` 的 `lines()`；做完把自检的 `SmokeCapabilities` 里 UploadQueue、Images、CSP 改成 true。
  - **取图（M6-3）：** 建议在资产服务器上挂 `/shell/img/<cover|original>/<file_id>/<snapshot_id>`（和页面同源，不需要 CORS，CSP 用 `'self'`；图片和切块是完整响应，Windows 上整块缓冲也没关系）；`web/src/lib/image-url.ts` 已预留 `SHELL_IMAGE_BASE`，做完把 `client.ts` 的 `SHELL_IMAGE_PROTOCOL` 改成 true；**`spaHandler` 会把没扩展名的路径回退到 index.html，取图路径要在它之前处理**。
### [x] M6-2 上传队列、热文件夹、重扫（commit 8a0e78c）
- **范围：** requirements 第 6 节的上传队列（本机库，纯 Go）、热文件夹（写完判定、忽略规则、同子文件夹串行、≤ 2 个并行、断网重试、401/426 暂停）、封面生成（Go 解码 JPEG、PNG、TIFF，长边 400 的 JPEG，方向用 `shared/imagehdr` 读，和 server 一致）、`files:check` 预检、`pick_and_enqueue_upload`、`pick_and_rescan` 和 `rescan:progress`、/scan 页的状态；`--smoke` 的 rescan 步骤。
- **前置：** M6-1。

- **结果：** 检查通过（含 `GOOS=windows go vet`、`go build`）、`npm run build`、`wails3 package`；客户端单元测试 8 个（自然顺序、忽略规则、写完判定、文件尾、重试间隔、multipart 顺序、封面 10 个样本、8 种方向）+ 集成测试 1 个（`DOCLITE_IT_SERVER=… go test ./internal/upload/ -run Integration`）；`--smoke` 19 步通过（rescan 一步 278 ms，v1→v2、轮次 1→2、自动分回、封面 included；不完整的文件被拒绝）。**热文件夹实测**（打包的客户端后台模式）：自然顺序入库、忽略隐藏/临时/符号链接、运行中新建子文件夹、两层子文件夹、多页 TIFF 永久失败、写了一半的等写完、杀掉重启自动恢复不重传、停掉 server 后退避重试恢复后按顺序入库；JPEG 压缩的 TIFF 不带封面，EXIF 方向 6 的封面方向正。没有抢焦点（自检和后台模式下进程是 UIElement）。依赖：`modernc.org/sqlite v1.59.0`（v1.60 要 Go 1.26）、`fsnotify v1.10.1`、`x/image v0.44.0`。
- **结构：** `client/internal/upload/`（纯 Go，不依赖 Wails）：`store.go`（本机库 SQLite，按状态计数、按"服务器 + 目标 + 子路径"分组取自然顺序队首）、`engine.go`、`process.go`、`hotfolder.go`、`settle.go`、`cover.go`、`multipart.go`、`api.go`、`rescan.go`，`lock_*`（`upload-queue.lock`，flock/LockFileEx，一个数据目录一个进程）、`attr_*`（隐藏、系统属性，独占打开）；`client/internal/shell/{upload,hotfolder,rescan,upload_tray}.go`；`proxy/attach.go`（壳自己发的请求和页面共用 Jar、请求头）。
- **遗留（Windows 上验证，M7）：** 独占打开的判断对扫描软件写入的影响；隐藏和系统属性；LockFileEx；fsnotify 的目录句柄对删除、改名的影响；对话框的文件类型筛选；中文用户名和长路径；托盘提示 127 字上限和菜单改文字后刷新。**其他：** macOS kqueue 每个文件一个句柄，几千个文件以上可能用完；426 暂停没实测；被拒绝的子文件夹不记账；换服务器后热文件夹最多 15 秒内停下；封面一次一张、解码内存上限 512 MiB；对话框相关的路径和托盘"暂停 / 继续"没有自动化测试。
- **须知（给 M6-4）：** smoke 重扫件用 `DOCLITE_SMOKE_RESCAN_PATH`、`DOCLITE_SMOKE_RESCAN_FILE_ID`、`DOCLITE_SMOKE_RESCAN_PROJECT`、`DOCLITE_SMOKE_RESCAN_EXPECT=incomplete`（重扫件内容在项目里不能重复，可往 JPEG 里插 COM 注释区分；每跑一次 done 用掉一页已驳回的文件，「M2-7 重扫演示/盒 003/IMG_0001.png」已用掉）；不点界面跑热文件夹：在数据目录的 `upload-queue.db` 写入热文件夹配置（running=1）并放好 `cookies.json`、`settings.json`；删掉 `upload-queue.db` 就是重置队列。
### [x] M6-3 本机图片缓存、大图切块（commit 1cce987）
- **范围：** 取图协议（Wails 下的写法实现者定，页面里的地址形式随之改 `lib/image-url.ts`）：先查本机缓存，没有用预签名 URL 下载并存下；按 snapshot_id 存；LRU（默认 2 GB）；只认来源记录；`cache_*`；大图切块 `prepare_tiles`（DZI 512、重叠 1，转正后原尺寸，JPEG 质量 90 / 1 位 PNG）；`--smoke` 的 img、img_cache、tiles、viewer 步骤。
- **前置：** M6-1（和 M6-2 并行）。

- **结果：** 检查通过（含 `GOOS=windows go vet`、imagecache 和 tiles 的 `-race`）、`npm run build`、`wails3 package`；单元测试 9 个；`--smoke` 打开 images、csp 后 cold、warm 两遍各 18 步全过（img、img_cache 8 张、tiles G4 61 块 74 ms / LZW 14 块 21 ms、viewer 只取 17/61 块、direct_blocked、csp），`DOCLITE_SMOKE_PAGES=1` 14 个页面在 CSP 下无错；200 MB 的 TIFF 取描述 1.1–1.6 s、查看器只取 388 块里的 9 块；没有抢焦点（viewer 一步也不用留给 M6-4）。`m2-viewer.mjs` 浏览器部分 10/11（`viewer-compare` 左侧标签不显示驳回原因，换回改动前的网页构建一样失败，是前端原有问题，待查）。
- **做法：** 取图挂在资产服务器上、和页面同源（在 `spaHandler` 之前）：`/shell/img/{cover|original}/<fid>/<sid>`、`/shell/img/dzi/<fid>/<sid>`（JSON 描述）、`/shell/img/tile/<fid>/<sid>/<级>/<x>_<y>.<jpg|png>`、`/shell/img/test/probe.png`；失败返回 JSON + `X-Doclite-Error`（404/422/502/503/500）。来源记录用 `Proxy.Observe` 记 batch、snapshots 的 200 响应（内存、≤ 1 万个），命中缓存也要在记录里，登录、退出、换服务器清空。缓存 `<数据目录>/image-cache/`：`index.json`（JSON + 内存，延迟写盘）、`<fid>/<sid>.cover.jpg`、`.original.<ext>`、`.tiles/…`；默认 2 GB（100 MB–1 TB），LRU 淘汰到 90%，大于上限一半的不存；原图核对大小和 sha256（≤ 512 MiB）。切块：整张解码（image/jpeg、png、x/image/tiff），8 种方向转正，DZI 512、重叠 1，灰度彩色 JPEG 90、1 位最高级 1 位 PNG；同一快照只切一次、同时 ≤ 2 张；内存估算超过 1.5 GiB 返回 422（200 MB A3 600 DPI 实际峰值约 1 GB），120 秒超时。CSP 由资产服务器加：`script-src 'self'`、`connect-src 'self'`、`img-src 'self' data: blob:`、`style-src 'self' 'unsafe-inline'` 等。
- **遗留：** **JPEG 压缩的 TIFF、多页 TIFF 切块返回 422（只显示封面）；M6-2 的封面生成对 JPEG 压缩的 TIFF 也不出封面** → M6-3b 补。Windows（M7）：WebView2 是否认响应头里的 CSP；大原图经资产服务器会整块进内存；被读着的文件删不掉（淘汰时残留）；自检窗口在非 Mac 上会直接显示；切块的内存峰值。其他：退出再登录那一瞬间在途的 batch 响应可能在登录后才被记下；解码做到一半不能打断；和 M6-2 封面生成在解码、转正上有重复（`tiles` 包这部分没导出）。
- **须知（给 M6-4）：** 自检环境变量 `DOCLITE_SMOKE_CACHE`（cold/warm）、`DOCLITE_SMOKE_IMG_PROJECT`、`DOCLITE_SMOKE_IMG_FOLDER`、`DOCLITE_SMOKE_IMG_COUNT`、`DOCLITE_SMOKE_TILES_PROJECT`、`DOCLITE_SMOKE_TILES_FOLDER`、`DOCLITE_SMOKE_TILES_FILES`；推荐 `DOCLITE_DATA_DIR=<临时目录> DOCLITE_SMOKE_SERVER=… DOCLITE_SMOKE_USERNAME=admin DOCLITE_SMOKE_PASSWORD=admin123456 DOCLITE_SMOKE_IMG_PROJECT=DEMO-A DOCLITE_SMOKE_TILES_FOLDER="M6-3 切块自检" bin/Doclite.app/Contents/MacOS/Doclite --smoke`；`m2-viewer.mjs` 里壳的部分依赖 Tauri 模拟和 tiles-bench，要改写或跳过；**并行任务在 scratchpad 里要各用自己的子目录和进程名**（同名的 server 进程曾被别的任务停掉）。doclite_dev 的 DEMO-A 下有「M6-3 切块自检」（6 个样本）、「M2-3 分块实测」（含 200 MB 的 TIFF）等样本文件夹。

### [x] M6-3b 客户端补：JPEG 压缩 TIFF 的封面和切块（commit 0b4b489）
- **范围：** JPEG 压缩的 TIFF（compression 7，以及能定位到完整 JPEG 的旧式 6）在客户端也要能解码：把各条带或分块的 JPEG 数据（拼上 JPEGTables）交给 `image/jpeg` 解码再拼成整张，YCbCr、RGB、CMYK、灰度都要对；M6-2 的封面生成和 M6-3 的切块共用这一份解码（抽成客户端里的一个包，顺便消掉两边在解码、转正上的重复）。多页 TIFF 仍然拒绝（上传时 server 已拒）。
- **完成标志：** 单元测试用 `server/testdata/samples/tiff-jpeg-ycbcr.tif` 和 M5-1 的 `bundle/testdata` 里 JPEG 压缩的夹具，检查封面和切块的像素（和同一页的 LZW 样本比较平均颜色）；`--smoke` 的 tiles 一步加上这张样本。
- **前置：** M6-2、M6-3。
- **结果：** gofmt、vet（含 `GOOS=windows`）、test 都过，imgdecode、tiles、imagecache 的 `-race` 也过；`npm run build`、`wails3 package` 过。`--smoke` cold、warm 两遍 18 步全过，tiles 一步 11 张（G4、LZW、9 张 JPEG 压缩的 TIFF）cold 全部切成功、warm 全命中。上传集成测试（连 29081）：JPEG 压缩的 TIFF 现在带封面。7016×9921 的 JPEG TIFF：切 388 块 1.2–1.6 s，进程峰值 430–520 MB；封面 1.3–1.5 s。单元测试会抓颜色错误（故意把 RGB 当 YCbCr、把 CMYK 反相都会失败）。
- **做法：** 新包 `client/internal/imgdecode`：`Open(path, maxPixels) (*Source, error)` 读文件头，`(*Source).Decode(ctx)` 解出文件里存的方向；错误 `*imgdecode.Error{Kind}`（unsupported_format、unsupported、corrupt、too_large、io、canceled）。转正函数（`OrientedSize`、`OrientPoint`、`RowPlacement`、`OrientRGBA`）从 tiles 和 cover 收进来，`tiles/orient.go` 删除；tiles 和 cover 都改走这个包。支持：Compression=7 条带和分块，有 JPEGTables 就拼进去；Compression=6 只认 JPEGInterchangeFormat 指向的完整 JPEG（和服务端导出一致）。颜色按 TIFF 的 Photometric 算：6 YCbCr、2 RGB（不管分量 ID、有没有 Adobe 标记）、1/0 灰度、5 CMYK（InkSet=1）；分平面支持 RGB、CMYK。不支持：每个样本不是 8 位、调色板、带透明或额外通道、YCbCr 分平面、无损或算术编码或 12 位的 JPEG。条带逐个解码贴进目标图；比标称大出 16 以上算坏文件，防止小文件声明巨大尺寸；ctx 在条带之间和每读 4 KB 检查。其他压缩的 TIFF 仍交给 x/image/tiff。imagecache 的切块失败本来就只记在内存里，升级重启后会重新切。
- **遗留：** Windows 上只跑了 vet（M7）。自带 Adobe 标记的 CMYK JPEG 条带：客户端按 Adobe 惯例反相显示，服务端导出 PDF 原样嵌入，两边可能不一致（罕见，没有样本）。YCbCr 的 ReferenceBlackWhite 不是默认值时忽略。很大的单条带解码时 image/jpeg 内部那次分配打断不了；单条带 RGB、CMYK 解码瞬时约 7–8 字节/像素。浏览器里上传生成封面（`web/src/lib/cover-core.ts`）没查。已在队列里记成"没封面"的旧文件不补。
- **须知（给 M6-4）：** DEMO-A「M6-3 切块自检」里新增了 8 个 JPEG TIFF 样本（原来那张 `tiff-jpeg-ycbcr.tif` 仍没有封面）。自检默认只切 G4 和 LZW，切 JPEG TIFF 要设 `DOCLITE_SMOKE_TILES_FILES="tiff-g4-1bit.tif,tiff-lzw-rgb.tif,tiff-jpeg-ycbcr.tif,tiff-jpeg-ycbcr-tables.tif,tiff-jpeg-rgb.tif,tiff-jpeg-tiles.tif,tiff-jpeg-planar.tif,tiff-jpeg-gray.tif,tiff-jpeg-cmyk.tif,tiff-jpeg-rgb-ids.tif,tiff-ojpeg.tif"`（G4 必须在第一个，viewer 一步看第一张；设了 FILES 不核对块格式）。`tiles.OrientedSize` 改为 `imgdecode.OrientedSize`。
### [x] M6-4 客户端验收（Mac）（commit 4423ee4）
- **范围：** 打包的客户端连 Go server 走一遍上传（热文件夹、壳里上传）、审核看大图、归类、导出下载（交给系统浏览器）；`--smoke` 全部步骤；截图脚本里依赖壳的部分（m1-shell、m1-scan、m1-cache、m2-viewer 等）。Windows 上的验收在 M7。
- **前置：** M6-1 到 M6-3，以及服务端 M5。
- **结果：** 检查全过（含 `GOOS=windows go vet`、`wails3 package`）；`--smoke` cold、warm 两遍各 24 步全过（含 rescan、tiles 11 张、14 个页面巡检、新加的主流程五步）；截图脚本壳模式全部跑通：m1-shell 6/6、m1-cache 4/4、m1-scan 8/8、m1-organize 10/10、m2-rescan 6/6、m2-viewer 21/21、m2-review 15/15、m2-accept 15/15（`M2_ACCEPT_SHELL_MOCK=1`；200 MB TIFF 切 388 块 1.0 s，壳内存峰值约 1.1 GB）；server 日志无 ERROR、WARN、5xx。客户端和前端没发现 bug，修的都是脚本。
- **做法：** 壳的模拟 `web/scripts/lib/wails-mock.mjs`：页面用 `http://wails.localhost:<端口>` 打开（`isShell()` 认它），包住 `fetch` 拦 `POST /wails/runtime` 交给脚本的命令表，`_wails.dispatchWailsEvent` 推事件，`_wails.streamFactory` 换成经同源 `/api` 转发的模拟流；**没改正式前端代码**。`client/cmd/devshell`（不打包）：托管 `web/dist`、转发 `/api/`、`/shell/img/` 用客户端的 imagecache 和 tiles 现切、加同一份 CSP（挪到 `client/internal/webcsp`）。Tauri 模拟和 tiles-bench 去掉。自检加主流程五步（`DOCLITE_SMOKE_FLOW_SAMPLES` 才跑）：壳里上传、热文件夹、分配并在查看器里看块后逐页通过、建条目挂接定稿、导出并走 `openDownload → open_external`；自检模式下 `open_external`、上传对话框、选目录对话框改走自检路径（都以 `s.smoke != nil` 为条件），`smoke_external_check` 用 HTTP 下载核对 zip。
- **遗留（用户亲手在 Mac 上试）：** 正常启动（填服务器地址、登录、关窗到托盘、Dock 打开、单实例聚焦）；托盘（状态、打开、暂停/继续、退出后图标消失）；系统对话框（/scan 选监控目录、/files 上传文件和文件夹、重扫件选择）；系统浏览器下载（/export、下载原图、升级提示）；真实扫描软件慢慢写大文件；/catalog 页序拖放；全键盘审核、查看器手感；本机缓存面板、"关于"里改服务器地址。**Windows（打包阶段）：** WebView2 的 CSP、导航保护、自检窗口、托盘、资产服务器大原图进内存、热文件夹文件锁和中文长路径、证书、混合内容、zip64、安装包、截图模拟和 devshell 复跑。
- **须知：** devshell：`cd client && go run ./cmd/devshell -server <server> -listen 127.0.0.1:<端口> -cache <临时目录>`，截图 `--base http://127.0.0.1:<端口>`。自检主流程要 server 在执行导出，每跑一次在 DEMO-A 留「客户端自检/自检 <时间>」（6 页）、一个定稿条目（全宗 ZJ）和一个导出任务；重扫件内容在项目里不能重复。m1-scan 改用 DEMO-B；「2025 文书档案/盒 WS-2025-004」备注被脚本改成"3 号库 A 架 14 层"。「M2 验收演示 2026-10-16 M6-4」的「盒 04/IMG_0003」留着驳回给用户亲手演示。

---

## M7 收尾功能（打包暂缓）

**用户决定（2026-10-16）：** 存储迁移允许只改快照的存放位置；许可证按登录会话数算；审计日志页做简单版；**先不走打包流程**，用户在自己电脑上验证没问题后再定打包。详见 requirements 第 11 节。**排期：** M7-1 ∥ M7-2 ∥ M7-3 → M7-4 验收 → 用户验证 → M8 打包交付。

### [x] M7-1 许可证（commit 1bbc87e）
- **范围：** requirements 第 11 节"许可证"：install_id（首次启动生成，存 system_settings）、导入和查看接口、Ed25519 验签（兼容 `../bak/doclite-license` 签发的 .lic 格式，`hardware_id` = install_id）、登录和每个请求的席位与到期检查、到期提醒；前端"系统 → 许可证"页（替换占位页）、登录页的错误提示、到期提醒条。开发和测试用的密钥对：私钥只放测试数据或 scratchpad，**正式公钥由用户提供**，先留配置位置。
- **完成标志：** API 测试：没许可证只有 admin 能登录、导入（签名错、install_id 不符、过期）、席位满拒绝、同人两处登录算 2、30 分钟无活动不计、admin 总能登录、到期后已登录的请求被拒、导入新证立即恢复、权限表；截图脚本新建一个 `m7-license.mjs`。
- **前置：** M5、M6。

- **结果：** 三项检查通过（server、client 含 `GOOS=windows`、web）；`m7-license.mjs` 7/7、`m0.mjs` 13/13；`bak/doclite-license` 未留任何文件。
- **做法：** `internal/license`：.lic 兼容上一版签发工具——签名签的是 payload 重新序列化的规范 JSON（字段按名排序、紧凑、serde_json 转义规则、chrono 时间格式），`lic.go` 手写序列化复现；夹具 5 份（工具命令行签 1 份 + 按路径编译工具源码签 4 份，含整秒、毫秒、纳秒、特殊字符公司名）和工具算出的规范 JSON，逐字节比对。**install_id = `sha256:` + 64 位十六进制**（工具 `--hardware-id` 只收这种格式；总控同意），首次启动生成存 `system_settings.install_id`。公钥 `-ldflags "-X doclite/server/internal/license.publicKey=<pem 中间一行或 32 字节 base64>"`，无环境变量覆盖；没编进去 = 开发版不检查；值不对拒绝启动。接口 `GET/PUT /api/system/license`（PUT 体 `{"license":"<全文>"}`，导入、替换写审计 `target_type=license`、`action=import|replace`；相同内容不动；开发版 409 `invalid_state`）。错误码：403 `license_missing`、`license_expired`、`license_seats_full`（details 带 max_users、online_sessions）；400 `invalid_license`（details.reason format|signature）、`license_mismatch`、`license_file_expired`。登录事务里（锁住用户行后）取咨询锁 (6011,0) 数在线会话；admin（内置角色）不受限但计数；密码错仍先 401；到期后非 admin 每个请求 403（只看内存）；SSE 复核会话时更新 last_seen_at（已有，加了测试）。`me` 加可选 `license_notice {status, expires_at, days_left}`，给有 system.license 菜单的人。前端 `LicenseView.vue`、`LicenseBanner.vue`、登录页提示、全局处理 `license_expired`/`license_missing` 回登录页；客户端上传队列、热文件夹、重扫遇到这两个码和 401 一样暂停。
- **遗留：** 多个 server 同库时导入后其他进程要重启才生效（按单进程设计）；被许可证拒的旧会话不撤销，30 分钟后自然不计；正式构建的 Windows 实测随 M8。requirements 第 8 节接口和错误码表待补（M7-4）。
- **须知：** 用户要用签发工具 `keygen` 生成正式密钥对（`backup-key` 加密备份私钥），`public.pem` 在 M8 写进构建脚本；签发：许可证页复制 install_id → `doclite-license issue --hardware-id <install_id> --max-users N --duration 1y --company … --edition …`，续期 `renew`。测试用 `app.Options.License` 注入测试公钥和时钟，apitest 有 `InitLicense`。
### [x] M7-2 存储迁移（commit 291498b）
- **范围：** requirements 第 11 节"存储迁移"：`POST storage-profiles/{id}/migrate`（把这个存储里的原图和封面迁到目标存储）、进度查询、取消；后台任务 `storage_migrate`（复用 `internal/platform/jobs`），分批复制 → sha256 核对 → 改快照位置 → 写审计，断点续；迁移时照常上传；共用对象只复制一次；完成后原存储可停用。前端在"系统 → 存储配置"里加迁移按钮、进度和结果。
- **完成标志：** API 测试：端到端（两个桶，迁完新存储能读、原存储停用后照常看图和导出）、迁移中上传和重扫、中途重启续传、取消、核对不一致不改、共用对象、权限表；截图脚本新建 `m7-storage-migrate.mjs`。
- **前置：** M5、M6。

- **结果：** 三项检查通过；迁移测试 `-race` 多轮稳定：`TestStorageMigrate`（换前缀、换桶、共用对象只复制一次、看图和导出正常、迁空能停用、迁移中改存储配置 409）、`TestStorageMigrateWhileWorking`、`TestStorageMigrateCopyRace`（两种先后确定性构造）、`TestStorageMigrateMismatch`、`TestStorageMigrateCancelResume`、`TestPermsStorageMigrate`；`m7-storage-migrate.mjs` 5/5、`m0-storage.mjs` 3/3。一轮独立审查：无高，1 中（两个迁移占满执行槽、导出排不上）6 低，已改；第 8 条（失败清单给有 system.storage 菜单的人看各项目文件信息）总控接受。
- **做法：** `POST system/storage-profiles/{id}/migrate {target_profile_id}` → 201、`GET …/migrate`（最近一次，带 remaining 和失败清单）、`POST …/migrate/cancel`；列表每项带 `migration`、`incoming_migration` 摘要（不带失败清单）。**整个系统同时只有一个迁移**（409 `invalid_state`）。任务 `storage_migrate`，优先级 −1，最多 5 次；状态在 `payload`（源、目标、cancel、state：cursor、计数、失败清单、audit_mark=建任务时的 max(audit_logs.id)）。一页 32 个对象按 storage_key、4 个 worker：读源到 `TMP_DIR/migrate/` 同时算 sha256 核对 → 写目标（key = 目标前缀 + `<项目编码>/<file_id>/v<n>.<ext>`）→ 读回核对 → 短事务 `UPDATE … WHERE 源、key、sha256、大小` 改位置并逐快照写审计；可重试错误单个对象内 1/4/16 秒退避重试 3 次。改位置事务加锁：job 行 FOR SHARE → 目标存储行 FOR KEY SHARE → 内容锁（项目 + sha256，升序）→ 快照行；取消接口 job 行 FOR NO KEY UPDATE（取消返回后不再改快照）。复制改为拿存储行后再拿内容锁、锁内重读当前快照（内容锁挪到 `project.LockContent`），重读后存储变了再对新存储行拿 FOR KEY SHARE（"内容锁最后拿"的例外，已写进注释和 requirements 3.6）。设默认对旧默认行改 FOR UPDATE，迁移期间不会再有快照写进源存储。审计：每快照 `file_snapshot/storage_migrate`；开始、取消、结束 `storage_profile/migrate_start|migrate_cancel|migrate_end`。不删源对象。失败清单最多 200 个对象、每个最多 20 个快照（带 snapshot_count）。前端存储配置页：迁移对话框、进度、取消、结果和失败详情、"正在迁入"。
- **遗留：** 导出过的存储永远只能停用、不能删（`export_tasks.storage_profile_id` 外键，M5-2 原规则）。运维顺序（结果面板、README、requirements 5.4 都写了）：迁完先改只读（旧导出物仍能下载、到期清理照常）→ 等导出物到期被清理 → 再停用。只能识别"地址、桶、前缀都相同"的同一目标；迁移执行期间导出并发从 2 降到 1；没测大规模性能。
- **须知：** doclite_dev 里有存储「M7-2 迁移演示·源」（已迁空停用）和「·目标」（桶 `doclite-migrate-test`，75 个快照），DEMO-A「M7-2 迁移演示」；SeaweedFS 两个演示前缀下的对象不删。测试第二个桶用 `TEST_S3_BUCKET2`（README）。`app.Options.StorageMigrate`（只给测试调每页大小、并行数、退避）。
### [x] M7-3 审计日志页、上传拒绝罕见格式（commit c75fd18）
- **范围：** ① 审计日志查询接口（时间、操作人、target_type、action、项目筛选，按 id 倒序游标分页）和"系统 → 审计日志"页（替换占位页），显示可读的操作说明；② 上传时按文件头拒绝导出不了的罕见格式（`shared/imagehdr`，服务端上传和客户端、浏览器上传前的检查都用它）。
- **完成标志：** API 测试（筛选、分页、权限表、10 万行审计时的耗时）；罕见格式的样本被拒、常见格式不受影响；截图脚本新建 `m7-audit.mjs`。
- **前置：** M5、M6。

- **结果：** server、shared、client（含 `GOOS=windows`）、`-tags perf` vet、web 检查都过；`m7-audit.mjs` 5/5、`m1-files.mjs` 10/10；`TestPerfAudit`（10 万条，含 3 万条迁移）17 种筛选中位数 0.7–7 ms（目标 200），不加索引。
- **做法：** ① `GET /api/system/audit-logs?from=&to=&operator_id=&project_id=&target_type=&action=&limit=&cursor=` → `{items, next_cursor}`（id 倒序游标，limit 默认 50、上限 200，`[from, to)` RFC 3339；时间先在主键上二分换成 id 范围、两头各放宽 1 小时，再按 created_at 精确过滤；一页一条 SQL 带操作人、项目）；`GET /api/system/audit-logs/filter-options`（全部用户含停用、全部项目含已删除）。菜单 `system.audit`。前端 `AuditView.vue`（筛选同步到地址栏、加载更多、展开看前后差异），翻译集中在 `web/src/lib/audit.ts`（9 类对象、21 种动作 + license import/replace + file_snapshot/storage_migrate，不认识的原样显示）。② `shared/imagehdr.ParseUpload`（规则在 `encoding.go`，`Parse` 不变）：拒绝导出做不了的编码——JPEG 无损、分层、算术编码、非 8 位、分量数不是 1/3/4；TIFF 的 JPEG2000、JBIG 等压缩、非常规位数、浮点、CIELab、非 JPEG 的 YCbCr、找不到完整 JPEG 的旧式 6、JPEG 压缩又分平面或带透明等。server 上传、重扫和客户端上传队列、壳里重扫共用，400 `invalid_image`（"不支持的图片格式（…），请换个保存格式重扫"），客户端被拒的不发请求、不重试。`TestUploadCheckMatchesExport` 核对四个目录全部样本：导出不支持 ⇔ 上传拒绝。
- **遗留：** 客户端看不了但导出能做的（不拒，客户端没封面、不能切块）：2、4 位 TIFF、非 JPEG 的 CMYK TIFF、灰度带透明 TIFF、非 JPEG 的分平面 TIFF、CCITT MH、4 分量无 Adobe APP14 的 JPEG。反过来分平面 JPEG TIFF 客户端能看但导出不行，现在上传被拒（已入库的不受影响）。时间筛选假设写审计的事务不超过 1 小时（M7-2 要短事务）；100 万条时全表扫估计约 70 ms。README 第 88 行"审计没有读接口"过时、requirements 第 8 节接口表待补（M7-4）。
- **须知：** `m7-audit.mjs` 每跑一次在 DEMO-A 留一个已软删除的「截图验证 M7-3 审计 <时间>」文件夹。M7-2 的开始、结束、取消动作名定下后补进 `web/src/lib/audit.ts` 的 `AUDIT_ACTIONS` / `ACTION_LABELS`。
### [x] M7-4 收尾验收（commit 5a9bdaf）
- **范围：** M7-1 到 M7-3 合并后，全部截图脚本（浏览器和壳模式）、`--smoke`、API 测试再跑一遍；整理给用户的本机试用说明。
- **前置：** M7-1、M7-2、M7-3。
- **结果：** server（API 测试 82 个、单元测试 31 个）、shared（20 个）、client（含 `GOOS=windows` vet 和交叉编译）、`npm run build`、`wails3 package` 全过；**截图脚本 26 个 225/225**（浏览器、devshell 壳模式、局域网 preview；m2-accept 新建「M2 验收演示 2026-10-16 M7-4」，m3-accept 用试跑模式）；`--smoke` cold、warm 各 24/24；server 日志无 ERROR、5xx。perf：建导出任务第 1 遍（机器忙）中位数 1.036 s 超线，第 2 遍 968 ms，其余都在目标内（我的审核 110–132、看板 46–51、预检 49–95、交付后变更 301–388 ms、审计 0.7–10 ms）→ 总控把这一项的判失败线放宽到 1.5 秒（用户已接受、正式硬件重测）。跨任务：开发版全部演示账号能登录；审计页对迁移、许可证的中文说明正确；带测试公钥的构建连临时克隆库：未导入只有 admin 角色能登录、导入 50 席后都能登录、换 3 席后第 4 个 `license_seats_full`、admin 照样能登录（临时库已删）。m2-accept 200 MB TIFF：壳里切 388 块 978 ms、峰值 1073 MiB。
- **修了什么：** 前端、客户端、服务端没发现 bug；脚本：`m7-audit` 的迁移假数据改成实际格式、多个脚本另存截图改为跟 `--out` 走、`m2-accept` G4 默认名改为 `a4-g4-1bit.tif`；"Windows 在 M7"改为 M8；README 开头加"本机试用"、测试个数、后台任务一节改名"导出、存储迁移"。
- **遗留：** doclite_dev 有三个「M2 验收演示 …」（不带后缀、M6-4、M7-4，各有一页驳回的「盒 04/IMG_0003」），「客户端自检」下多个自检文件夹；`doclite_test` 下 12 个没人连的 `doclite_test_<随机>` 克隆库可手工删；许可证页"当前在线"可能超过席位数（admin 不受限），可加一句说明；devshell 页面取消请求时记 WARN；seed:demo 不建 `demo_reviewer2`。M8：Windows 验证项、正式公钥写进构建脚本、`wails3` 不在 PATH（用 `~/go/bin/wails3`）。

### [x] M7-5 模块独立：去掉 shared，server 只提供接口，web 单独启动（commit c998548）
**用户决定（2026-10-16）：** server、web、client 三个独立模块，代码上互不依赖，为以后拆仓库做准备；web 本地开发单独启动，部署时是单独的 nginx 容器（requirements 第 1 节）。
- **范围：** ① `shared/archiveno` → 服务端归类模块下的独立小包；`shared/names` → 服务端 `platform/`；`shared/imagehdr` → 服务端、客户端各一份（只用标准库，不依赖各自的其他模块）；删掉 `shared/`、`go.work`、`go.work.sum`，server 和 client 各自独立编译、能 `go mod tidy`。② server 去掉 `WEB_DIR` 托管网页，只提供 `/api`。③ web 自成一体：截图脚本和 seed 脚本用 web 自己的样本（不再读 `server/testdata/samples`），本地试用、截图脚本的 `--base` 改成 vite（dev 或 preview）。④ 更新 CLAUDE.md、README（本机试用：分别启动 server 和 web）。
- **完成标志：** 三项检查和全部测试通过；截图脚本抽跑（m0、m1-files、m4-export）以 vite preview 为入口通过。

- **结果：** server、client 在 `GOWORK=off` 下三项检查全过，`go mod tidy` 没升版本；`npm run build`、`wails3 package` 过；以 vite preview 为入口 m0 13/13、m1-files 10/10、m4-export 13/13、m7-audit 5/5；server 日志无 ERROR、5xx。
- **做法：** `shared/names` → `server/internal/platform/names`；`shared/archiveno` → `server/internal/platform/archiveno`（project 也用，自带禁用字符常量，只依赖标准库）；`shared/imagehdr` → `server/internal/platform/imagehdr` 和 `client/internal/imagehdr` 各一份（代码相同，开头写明另一份在哪、改动两边同步、拒收以 server 为准）。删掉 `shared/`、`go.work`、`go.work.sum`。样本：`client/testdata/samples` 全量副本、`client/internal/imgdecode/testdata` 和 `server/internal/export/bundle/testdata` 互补夹具、`web/scripts/samples`（截图要的 8 个 + `encoding/` 2 个罕见编码）。server 去掉 `WEB_DIR`（配置、`httpx/web.go`、测试），`/api` 以外一律 JSON 404。vite 默认监听 `127.0.0.1`（本机 localhost 只监听 ::1）。CLAUDE.md 加"三个模块"一节；README 改成分别启动 server 和 web；requirements 1.2 容器说明改为 web（nginx）+ server（只提供 /api）。
- **须知：** server `cd server && go run ./cmd/server`（9080 只有 `/api`）；web `npm run dev`（5173）或 `npm run build && npm run preview`（4173），都把 `/api` 和实时推送转给 `DOCLITE_SERVER`（默认 9080）；并行任务 `DOCLITE_SERVER=http://127.0.0.1:2908x npm run preview -- --port 2417x --strictPort`；截图脚本 `--base` 用 web 的地址。imagehdr 改一份要同步另一份。
### [~] M7-6 客户端界面随客户端打包（web 产物按版本嵌入）、桥加固
**用户决定（2026-10-16）：** 先做了"界面跟 web 服务走"（7b80b77，未合并），审查出 1 高 1 中 8 低；用户问成熟产品的做法后改为**随客户端打包、版本跟客户端走**（Element 的做法），web 仍是独立模块（requirements 第 1 节）。
- **范围：** 去掉网页转发和网页缓存（`internal/webassets`）；web 构建出带版本号的产物包，client 用 `client/WEB_VERSION` 指定版本、打包时嵌入，不依赖 web 源码；客户端地址是 web 服务入口，只经它访问 `/api`；保留客户端自带的连接、连不上、升级页，能力协商，`client/BRIDGE.md`；加固（两种方式都要的）：`/wails/runtime` 白名单、改地址系统确认、升级页只信探测结果、导航保护失败就关闭、网页按能力判断、错误不带本机路径。
- **完成标志：** client 检查和测试；打包不需要 web 源码（只要产物包）；自检、壳模式截图在远程环境下跑通。

## M8 打包交付（暂缓，等用户验证后再定）

**已定（用户 2026-10-16）：** web 是单独的 nginx 容器（静态文件 + `/api` 反向代理给 server；实时推送关缓冲、上传大小上限、超时要配），server 只提供接口；compose 里是 web、server、PostgreSQL、SeaweedFS 四个服务。

原上一版的 M5：Docker 镜像和 compose、install.ps1、backup.ps1（封面必须备份）、离线交付包、客户端自动更新、export-tree、SeaweedFS 4.45 复验、在用户的 Windows 上远程验收。requirements 第 11 节记了建议做法。
