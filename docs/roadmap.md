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
7. M6 → M7

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

### [~] M2-1 文件目录、上传、图片信息、日志
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

### [x] M4-1 归类树、档号、条目、著录、定稿（commit e32f44b）
- **范围：** requirements 5.9 里不涉及文件的部分：节点（增删改移、编码和类型、按需加载）、档号和件号（含撞号规则、按新模板重新生成、空档号原因）、条目（增删改移、状态、著录校验、旧值规则、定稿和解锁、批量定稿）、`catalog-settings`；`catalog.changed` 事件（7）。删除条目、定稿前置里"≥ 1 页""全部页通过"先按"没有挂接行"处理（挂接在 M4-2）。
- **完成标志：** API 测试覆盖条目状态表、档号生成和撞号、著录校验；截图脚本 `m3-catalog.mjs` 里不涉及挂接的部分通过（有挂接的步骤 M4-2 再跑）。
- **前置：** M1-3（和 M2-1 并行）。
- **先读：** requirements 3.6、5.9、7。

- **结果：** 三项检查通过；API 测试 8 个（权限表、节点、条目状态表 8 × 3、定稿和批量定稿、档号生成/撞号含分档和互换/缺级、著录校验 17 例和旧值规则）+ 分档分配单元测试 1 个。正式的 `m3-catalog.mjs` 因为要先建文件夹、上传（M2-1）在准备数据时全部失败；用不涉及文件的脚本变体验证了 21 张（树、表单校验、400 定位、旧值、定稿只读、解锁、改编码、撞号、同步、批量定稿、节点操作、重连、事件合并、顶层条目、移动、换项目确认、虚拟滚动 1200 条）全部通过。这次运行往 DEMO-A 写了著录项，并把 demo_cataloger 加进了 DEMO-A 的授权。
- **自定细节：** 新的咨询锁类别 **6005（档号）**：共享归类锁下写档号（新建、移动、改件号）先按（项目, 档号）拿 `pg_advisory_xact_lock(6005, …)` 再查占用，后到的等前一个提交，不会撞 `ux_archive_items_no`（排在节点行、条目行之后，每个事务最多一个；批量重算拿归类树排他锁，不用它）；批量重算在应用里分档，先把"旧号正好是别人新号"的置空，再一条 `UPDATE … FROM unnest`；审计按条批量写；件号到 999999999 之后 409 `invalid_state`。条目列表 limit 默认 500（1–2000），cursor `<件号>_<id>`；删除可带 `?row_version=`。
- **临时处理（M4-2 替换）：** 有页的条目删除返回 409 `invalid_state`（`TODO(M4-2)`）；新建条目带非空 `file_ids` 返回 400。
- **须知（给 M4-2）：** 锁顺序 `project.LockCatalogTree(ctx, tx, pid, false)` → 分配件号时锁节点行（`lockCategories`）→ 条目行（`lockItem`、`lockItems`，`FOR NO KEY UPDATE`，id 升序）→ 之后才拿文件树锁和 files 行；锁住后依次查 已删除 404 → 状态（`errLockedEdit`）→ `row_version`（`staleVersion`）。定稿判断 `item_state.go` 的 `lockBlocker`，页数和审核状态来自 `itemPageStats`（读 `archive_item_files`、`folder_files`，不锁文件），挂接做好后不用改。档号：单个 `assignArchiveNo`（撞号留空、返回撞了谁），子树 `renumberSubtree`（要排他锁），空档号原因 `emptyReason`。`readItem`、`itemPages` 已能读页（文件名、路径、版本、"定稿后有新版本"），`page_counts`、`locked_at` 已算好。测试里的 `addPage` 直接写库造页，M4-2 后改走 `PUT /files`。
### [ ] M4-2 挂接、页序、归类信息、看板
- **范围：** requirements 5.9 的"挂接"（PUT files、新建条目带 file_ids、删除条目一并移出、日志、reorder 审计）、"归类信息"（placements、`files/{fid}/catalog`）、定稿前置里和页相关的条件、5.10 看板。
- **完成标志：** API 测试覆盖挂接的主要错误码；**并发**：两个条目抢同一个文件只有一个成功、审核和挂接同时进行不互相等（加锁顺序见 3.6）；截图脚本 `m3-catalog.mjs`、`m3-workbench.mjs`、`m3-dashboard.mjs`、`m3-catalog-info.mjs`、`m3-catalog-settings.mjs` 通过。
- **前置：** M4-1、M2-1（M3-1 合入后再跑审核相关的截图）。
- **先读：** requirements 3.6、5.9、5.10、7。
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
### [ ] M5-2 导出任务、执行、下载
- **范围：** requirements 5.11 其余部分：范围和预检、冻结、交付后变更、补交、取消、后台任务执行器（单服务器版：心跳查取消，启动时把 running 的放回队列，重试和退避）、下载、到期清理；`export.changed` 事件。
- **完成标志：** API 测试覆盖预检、冻结（建任务后重扫不影响这次导出）、交付后变更的几种原因、执行和下载的端到端（下载 zip 核对清单和明细）；**并发/恢复**：执行中重启 server 后能接着完成；截图脚本 `m4-export.mjs` 通过。
- **前置：** M4-2、M5-1。
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
