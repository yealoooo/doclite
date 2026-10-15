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
### [~] M5-2 导出任务、执行、下载
- **范围：** requirements 5.11 其余部分：范围和预检、冻结、交付后变更、补交、取消、后台任务执行器（单服务器版：心跳查取消，启动时把 running 的放回队列，重试和退避）、下载、到期清理；`export.changed` 事件。
- **完成标志：** API 测试覆盖预检、冻结（建任务后重扫不影响这次导出）、交付后变更的几种原因、执行和下载的端到端（下载 zip 核对清单和明细）；**并发/恢复**：执行中重启 server 后能接着完成；截图脚本 `m4-export.mjs` 通过。
- **前置：** M4-2、M5-1。
- **先读：** requirements 3.6、5.11、7。

### [ ] M5-3 服务端验收
- **范围：** 用 Go 服务端把上一版 M1–M4 的主流程截图脚本全部跑一遍（`web/scripts/shots/m1-accept.mjs`、`m2-accept.mjs`、`m3-accept.mjs`，依赖桌面客户端的步骤跳过，留给 M6）；补齐 README（本机开发、跑测试、跑截图脚本）；整理遗留问题。
- **完成标志：** 总控向用户报告服务端完成情况。

---

## M6 桌面客户端（Wails）

**用户决定（2026-10-16）：** 用 **Wails v3**（当前 v3.0.0-beta.29，自带托盘、单实例、多窗口；v2 没有自带托盘）。命令行工具 `~/go/bin/wails3` 已装，`wails3 doctor` 通过。

**做法：** 客户端放在 `client/`（Go 模块 `doclite/client`，加进 `go.work`），界面就是 `web/` 的构建产物。前端和壳之间的接口沿用上一版：约 26 个命令（`shell_info`、`probe_server`、`check_server`、`set_server`、`api_fetch`、`api_stream`、`api_stream_cancel`、`open_external`、`tray_status`、`upload_*`、`hotfolder_*`、`pick_and_enqueue_upload`、`pick_and_rescan`、`cache_*`、`prepare_tiles`、`smoke_*`）、1 个事件（`rescan:progress`）、1 个取图协议（上一版是 `img://`）。前端只改适配层（`web/src/lib/shell.ts`、`stores/shell.ts`、`api/http.ts`、`api/stream.ts`、`lib/image-url.ts` 等），去掉 `@tauri-apps/api`，换成 Wails 的前端运行时；其余页面不动。需求见 requirements 第 6 节。

**排期：** M6-1 → M6-2 ∥ M6-3 → M6-4。

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
### [~] M6-2 上传队列、热文件夹、重扫
- **范围：** requirements 第 6 节的上传队列（本机库，纯 Go）、热文件夹（写完判定、忽略规则、同子文件夹串行、≤ 2 个并行、断网重试、401/426 暂停）、封面生成（Go 解码 JPEG、PNG、TIFF，长边 400 的 JPEG，方向用 `shared/imagehdr` 读，和 server 一致）、`files:check` 预检、`pick_and_enqueue_upload`、`pick_and_rescan` 和 `rescan:progress`、/scan 页的状态；`--smoke` 的 rescan 步骤。
- **前置：** M6-1。

### [~] M6-3 本机图片缓存、大图切块
- **范围：** 取图协议（Wails 下的写法实现者定，页面里的地址形式随之改 `lib/image-url.ts`）：先查本机缓存，没有用预签名 URL 下载并存下；按 snapshot_id 存；LRU（默认 2 GB）；只认来源记录；`cache_*`；大图切块 `prepare_tiles`（DZI 512、重叠 1，转正后原尺寸，JPEG 质量 90 / 1 位 PNG）；`--smoke` 的 img、img_cache、tiles、viewer 步骤。
- **前置：** M6-1（和 M6-2 并行）。

### [ ] M6-4 客户端验收（Mac）
- **范围：** 打包的客户端连 Go server 走一遍上传（热文件夹、壳里上传）、审核看大图、归类、导出下载（交给系统浏览器）；`--smoke` 全部步骤；截图脚本里依赖壳的部分（m1-shell、m1-scan、m1-cache、m2-viewer 等）。Windows 上的验收在 M7。
- **前置：** M6-1 到 M6-3，以及服务端 M5。

---

## M7 部署交付

原上一版的 M5：Docker 镜像和 compose、install.ps1、backup.ps1（封面必须备份）、离线交付包、许可证、存储迁移、客户端自动更新、SeaweedFS 4.45 复验、在用户的 Windows 上远程验收。requirements 第 11 节列了要问用户的事，开工前一起问。
