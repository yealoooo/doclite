# Doclite

纸质档案数字化系统：扫描上传 → 分配审核 → 审核（驳回重扫）→ 归类挂接 → 导出。服务端 Go（`server/`），桌面客户端 Wails v3（`client/`），网页 Vue 3（`web/`），共用的纯函数在 `shared/`。需求见 `docs/requirements.md`，进度见 `docs/roadmap.md`，开发约定见 `CLAUDE.md`。

## 目录

- [本机开发](#本机开发)
- [跑测试](#跑测试)
- [跑截图脚本](#跑截图脚本)
- [桌面客户端](#桌面客户端)
- [后台任务（导出）](#后台任务导出)
- [演示账号](#演示账号)

## 本机开发

需要 Go 1.25、Node 23 / npm 11、PostgreSQL 16（生产是 18，只用 16 也有的特性）、SeaweedFS（或别的 S3 兼容存储）。Mac 上不用 Docker。

**PostgreSQL**：建一个能建库的账号和两个库（API 测试要克隆库，所以要 `CREATEDB`；迁移里的 `pg_trgm` 是可信扩展，库的属主就能建）。

```sh
createuser --createdb doclite
createdb -O doclite doclite_dev
createdb -O doclite doclite_test
```

**SeaweedFS**：`weed mini` 单进程起 S3（8333），桶要单独建一次。`-ip=127.0.0.1` 不能省（预签名地址用它）。

```sh
mkdir -p ~/doclite-dev/seaweedfs
AWS_ACCESS_KEY_ID=doclite-dev AWS_SECRET_ACCESS_KEY=doclite-dev-secret S3_BUCKET=doclite \
  weed mini -dir=$HOME/doclite-dev/seaweedfs -ip=127.0.0.1 -admin.ui=false
echo 's3.bucket.create -name doclite' | weed shell -master=127.0.0.1:9333
```

**`server/.env`**：从 `server/.env.example` 复制（每一项的说明都在模板里）。要改的只有：

- `MASTER_KEY`：`openssl rand -base64 32` 生成一次，**之后不要改**（存储配置的密钥用它加密，换了就解不开）；
- `INIT_ADMIN_USERNAME`、`INIT_ADMIN_PASSWORD`：库里一个用户都没有时用它们建管理员；
- `S3_*`：库里一条存储配置都没有时用它们登记默认存储（模板里的值对应上面的 `weed mini`）。

`server/.env` 不提交。环境变量优先于 `.env`。

**启动 server**（在 `server/` 下运行，读当前目录的 `.env`；启动时自动执行迁移）：

```sh
cd web && npm ci && npm run build          # 网页构建产物在 web/dist
cd ../server && WEB_DIR=../web/dist go run ./cmd/server
# 浏览器打开 http://127.0.0.1:9080（LISTEN_ADDR）
```

- `WEB_DIR` 设了才托管网页（`/api` 以外的路径回退到 `index.html`）；不设只提供 `/api`。重新 `npm run build` 不用重启 server。
- 改前端时也可以用 vite：`cd web && DOCLITE_SERVER=http://127.0.0.1:9080 npm run dev`（`/api` 代理到 server，默认 9080）。
- 端口约定：自己的 server 用 9080；并行的子任务用 29080–29082（server）和 24173–24175（vite preview），互不干扰。
- 同一个库上开多个 server 时，只有一个执行导出（见[后台任务](#后台任务导出)）；多个 server 的 `TMP_DIR` 要分开（启动时会清空 `TMP_DIR/uploads`）。

## 跑测试

提交前：每个 Go 模块 `gofmt -l .` 没有输出、`go vet ./...`、`go test ./...` 通过；改了 `web/` 时 `npm run build` 通过。

```sh
cd server && gofmt -l . && go vet ./... && go test ./...   # API 测试（65 个）+ 单元测试，本机约 15 秒
cd shared && go test ./...                                 # 文件头、名称规则、档号模板
cd client && go test ./...                                 # 客户端的纯逻辑（Mac 上要 cgo，Wails 需要）
cd web && npm run build                                    # vue-tsc 类型检查 + 构建
```

- **API 测试**（`server/apitest/`）连真实的 PostgreSQL 和 SeaweedFS。第一次运行时建模板库 `doclite_test_tpl_<迁移哈希>`，每个测试从它克隆一个库、用自己的 S3 前缀，结束时删掉（强杀可能留下 `doclite_test_<随机>` 库，可以手工删）。连接参数：`TEST_DATABASE_URL`（默认 `postgres://doclite@127.0.0.1:5432/doclite_test`）、`TEST_S3_ENDPOINT`、`TEST_S3_ACCESS_KEY`、`TEST_S3_SECRET_KEY`、`TEST_S3_BUCKET`（默认同上面的 `weed mini`）。SeaweedFS 没启动时测试直接报错。`TEST_LOG=1` 打出 server 日志。
- **客户端上传的集成测试**（要一个在跑的 server）：`cd client && DOCLITE_IT_SERVER=http://127.0.0.1:9080 DOCLITE_IT_USERNAME=… DOCLITE_IT_PASSWORD=… DOCLITE_IT_PROJECT=DEMO-A go test ./internal/upload/ -run Integration`。
- **性能抽测**（默认不跑，`//go:build perf`）：用 SQL 造 10 万文件（6 万页挂在 2400 件里），测审核读接口、看板、`catalog-placements`、2000 件 / 5 万页的导出预检、建任务、交付后变更，打印每个接口的中位数和最慢，超过 requirements 1.4 的目标就失败：

  ```sh
  cd server && go test -tags perf -run TestPerf -v -timeout 20m ./apitest/
  ```

## 跑截图脚本

`web/scripts/shots/*.mjs` 是上一版留下的界面验收：用本机的 Chrome（puppeteer-core，不下载浏览器；找不到时设 `CHROME_PATH`）无头打开页面、操作、断言、截图。失败的那张打 ✗，脚本退出码 1。

```sh
cd web
INIT_ADMIN_PASSWORD=<管理员密码> npm run screenshots -- --spec scripts/shots/m0.mjs --base http://127.0.0.1:9080
#   --only a,b       只跑这几张（按 name）
#   --out <目录>      截图放哪（默认 docs/screenshots/<里程碑>；截图不提交，跑完 git clean -fd docs/screenshots）
```

- `--base` 指向托管了 `web/dist` 的 server（或 vite preview、devshell，`/api` 由它们代理；有壳模式截图的脚本要用 devshell，见下面）。脚本在页面之外调接口也走这个地址。
- `INIT_ADMIN_PASSWORD`（`INIT_ADMIN_USERNAME`）是管理员的密码，和启动 server 用同一组变量；演示账号的密码 `demo123456`（`DEMO_PASSWORD` 可改）。`SHOTS_TMP` 是脚本的临时目录（默认系统临时目录）。
- 审计没有读接口，几个脚本直接 `psql` 读库：要 `psql` 在 PATH 里，`DATABASE_URL` 指向同一个库（默认本机 `doclite_dev`）。

**准备数据**（新库；已有的会跳过）：

```sh
INIT_ADMIN_PASSWORD=<管理员密码> npm run seed:demo -- --base http://127.0.0.1:9080   # 演示账号、DEMO-A、DEMO-B、授权
npm run seed:files -- --base http://127.0.0.1:9080 --user demo_scanner:demo123456 --project DEMO-A \
  --folder "2025 文书档案/盒 WS-2025-001" --count 2000 --low-dpi 3 --no-cover 3
```

`seed:files` 往一个文件夹里传 N 张合成的扫描页（PNG + 客户端规格的封面，路径不存在就逐层建，按页码顺序上传，可以重跑；`--remark` 写原件位置）。每个脚本开头写了它要的演示数据和 `seed:files` 命令（M1-10、M2-5 到 M2-7、M2 验收……；那里的 `$S`、`$U` 是 bash 写法，zsh 里要把参数写全）；归类、看板、导出的演示数据由脚本第一次运行时自己建（`scripts/lib/m3-demo.mjs`、`m4-demo.mjs`）。

**几个特别的脚本**：

- `m1-upload-lan`、`m2-viewer-lan`：要用局域网地址打开（非安全上下文）。只在跑这一步时开 `DOCLITE_SERVER=http://127.0.0.1:9080 npx vite preview --host --port 24173 --strictPort`，`--base http://$(ipconfig getifaddr en0):24173`。
- `m1-accept`：`ACCEPT_DEMO` 指定热文件夹演示的文件夹（5 个盒 × 100 页，没有时用 `seed:files` 造）。
- `m2-accept`：要先建「M2 验收演示 <日期>」（脚本开头有命令）；只能走一遍，再跑换一个文件夹（`M2_ACCEPT_FOLDER`）。
- `m3-accept`：自己建「M3 验收演示 <日期>」和全宗 Z308，只能走一遍；再跑换 `M3_ACCEPT_FOLDER`、`M3_ACCEPT_FONDS`，或者 `M3_ACCEPT_TRIAL=1` 试跑（最后删掉自己建的数据）。
- `m4-export`：server 要在执行导出（见下一节）。
- 壳的部分（`m1-shell`、`m1-scan`、`m1-cache` 和 `m1-organize`、`m2-rescan`、`m2-viewer`、`m2-review`、`m2-accept` 里名字带 `shell` 的几张）：`--base` 要指向 **devshell**（见下面）。这几张用 `http://wails.localhost:<端口>` 打开页面（`lib/client.ts` 的 `isShell()` 认 Windows 上 WebView2 的这个地址，Chrome 把 `*.localhost` 解析到本机），在页面脚本运行前注入 Wails 桥的模拟（`scripts/lib/wails-mock.mjs`：拦下 `/wails/runtime` 的命令调用、`_wails.dispatchWailsEvent` 推事件、`_wails.streamFactory` 给实时通道），正式前端代码不改。
- `m2-accept`：`M2_ACCEPT_SHELL_MOCK=1` 时壳模式审核那两步用上面的模拟（否则改在浏览器里做）；`M2_ACCEPT_SHELL=client/bin/Doclite.app/Contents/MacOS/Doclite`、`M2_ACCEPT_SERVER=<server 本身的地址>` 时壳重扫、壳的大图两步跑打包的客户端 `--smoke`（隔离的数据目录、后台模式），不设时壳重扫改用接口模拟。`m3-accept` 的 `M3_ACCEPT_SHELL` 同理。

**devshell**（`client/cmd/devshell`，截图脚本的开发服务器，不打包）：一个端口上托管 `web/dist`、把 `/api/` 转给 server、用客户端的 Go 代码（`internal/imagecache`、`internal/tiles`，和打包的客户端同一份）回答取图地址 `/shell/img/…`（下载、本机缓存、切块）；壳模式的地址和客户端一样加 CSP。浏览器的那些截图也可以照常经它跑。

```sh
cd client && go run ./cmd/devshell -server http://127.0.0.1:9080 -listen 127.0.0.1:24173 -cache <临时目录>
cd web && INIT_ADMIN_PASSWORD=<管理员密码> npm run screenshots -- --spec scripts/shots/m1-scan.mjs --base http://127.0.0.1:24173
```

## 桌面客户端

Wails v3（命令行工具 `wails3`，`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.29`）。目前只有 Mac 的构建，Windows 在 M7。

```sh
cd client && wails3 package     # 构建 web/、编译、打包成 client/bin/Doclite.app（ad-hoc 签名）
open client/bin/Doclite.app     # 首次启动填服务器地址
```

开发版（能开网页检查器）：`wails3 task build:dev`；界面热更新先 `cd web && npm run dev`，再 `FRONTEND_DEVSERVER_URL=http://localhost:5173 client/bin/Doclite`。

**自检 `--smoke`**（参数从环境变量读；结果是标准输出的一行 JSON；退出码 0 成功、1 失败、2 连不上、3 需要升级）：

```sh
DOCLITE_SMOKE_SERVER=http://127.0.0.1:9080 DOCLITE_SMOKE_USERNAME=demo_scanner DOCLITE_SMOKE_PASSWORD=demo123456 \
DOCLITE_BACKGROUND=1 client/bin/Doclite.app/Contents/MacOS/Doclite --smoke
```

| 变量 | 说明 |
|---|---|
| `DOCLITE_SMOKE_SERVER`、`DOCLITE_SMOKE_USERNAME`、`DOCLITE_SMOKE_PASSWORD` | 服务器和账号（账号至少能进一个项目） |
| `DOCLITE_SMOKE_TIMEOUT_SECS` | 整个自检的时限，默认 120（加上 pages 和主流程要 300 以上） |
| `DOCLITE_SMOKE_PAGES=1` | 另外逐个打开菜单里的页面 |
| `DOCLITE_SMOKE_RESCAN_PATH`、`_FILE_ID`、`_PROJECT`、`_EXPECT` | 设了 PATH 才跑 rescan 一步：用这个本机文件重扫（不设 FILE_ID 取待重扫列表第一个；EXPECT 是 `done` 或 `incomplete`）。重扫件的内容在项目里不能重复 |
| `DOCLITE_SMOKE_CACHE` | img_cache、tiles 两步：`cold`（默认，先清空自检的缓存，要求全部下载、现切）或 `warm`（要求全部命中；先跑一次 cold） |
| `DOCLITE_SMOKE_IMG_PROJECT`、`_IMG_FOLDER`、`_IMG_COUNT` | img_cache 一步取哪个项目、文件夹（从项目根起，`/` 分隔）的前几个文件（默认第一个有文件的文件夹、8 个） |
| `DOCLITE_SMOKE_TILES_PROJECT`、`_TILES_FOLDER`、`_TILES_FILES` | tiles、viewer 两步：默认找文件名含 g4、lzw 的 TIFF；`_TILES_FILES` 指定文件夹里的几个文件（逗号分隔，第一个给 viewer 看，含 JPEG 压缩 TIFF 的列表见 roadmap M6-3b） |
| `DOCLITE_SMOKE_FLOW_SAMPLES` | 设了才跑主流程五步（M6-4）：本机样本文件夹（单页 JPEG、PNG、TIFF，按文件名取前 2 × COUNT 个）。壳复制一份、每个文件写进本次独有的注释（内容不重复）：壳里上传（`pick_and_enqueue_upload` 用复制的文件夹代替系统对话框）→ 热文件夹（`hotfolder_pick_dir` 用准备好的空文件夹，壳再往里放文件）→ 分给自己、在查看器里看原图、逐页通过 → 建条目挂上、定稿 → 导出，下载地址经 `open_external` 交给壳（自检里不打开系统浏览器），壳用 HTTP 下载核对 zip。账号要有扫描、审核分配、审核、归类、导出菜单（admin）；server 要在执行导出。每次在项目里留下一个「<FOLDER>/自检 <时间>」文件夹和一个定稿的条目 |
| `DOCLITE_SMOKE_FLOW_PROJECT`、`_FOLDER`、`_COUNT`、`_FORMAT` | 主流程的项目（默认第一个能进的）、上级文件夹（默认「客户端自检」）、两种上传各几个（默认 3）、导出格式（`pdf` 默认或 `images`）。归类节点按档号模板用到的层级找或建：全宗 `ZJ`「客户端自检」/ `WS` / `2026` / `Y` / `ZJ` |

其他环境变量：`DOCLITE_DATA_DIR` 换数据目录（默认 `~/Library/Application Support/Doclite/`、`%LOCALAPPDATA%\Doclite\`；自检用其中的 `smoke/`）；`DOCLITE_BACKGROUND=1` 不抢焦点（开发、测试用）；`DOCLITE_LOG_LEVEL=debug`；`DOCLITE_LOG_STDERR=1` 日志同时写到标准错误。删掉数据目录里的 `upload-queue.db` 就是重置上传队列。

## 后台任务（导出）

导出在 server 进程里执行（`background_jobs` 表排队）。配置（都在 `server/.env.example` 里有说明）：

| 变量 | 默认 | 说明 |
|---|---|---|
| `JOB_CONCURRENCY` | 2 | 同时执行几个导出（0–16）。**0 表示这个进程不执行导出、也不做到期清理** |
| `JOB_HEARTBEAT_SECS` | 10 | 心跳间隔（顺便查取消、回收没人执行的任务） |
| `JOB_RETRY_BACKOFF_SECS` | 30 | 可重试的失败第一次重试前等多久，之后每次 × 4，最多 1 小时；一共最多执行 3 次 |
| `EXPORT_CLEANUP_INTERVAL_SECS` | 600 | 导出物到期清理的间隔（保留天数在项目设置 `export.retention_days`，默认 7） |
| `TMP_DIR` | 系统临时目录下的 `doclite` | 导出在 `TMP_DIR/exports/` 下生成 PDF，要放得下最大一件原图 × 1.2 |

**同一个库只有一个 server 进程执行后台任务**：执行器启动时拿数据库咨询锁 `(6006, 0)`，拿到的那个执行导出、做到期清理、管导出的临时目录；后启动的拿不到锁，只提供 HTTP（日志里一条警告），它那边建的导出由先启动的进程执行。所以本机多个 server 共用 `doclite_dev` 时，谁先启动谁执行；想让某个进程一定执行，就先停掉别的（或者给别的设 `JOB_CONCURRENCY=0`）。部署时只有一个 server 进程。

## 演示账号

`npm run seed:demo` 建的，密码都是 `demo123456`；管理员 `admin` 的密码是建库时的 `INIT_ADMIN_PASSWORD`（本机 `doclite_dev` 是 `admin123456`）。

| 账号 | 角色 | 能进的项目 | 用途 |
|---|---|---|---|
| `admin` | 系统管理员 | 全部 | 初始管理员 |
| `demo_admin` | 系统管理员 | 全部 | |
| `demo_manager` | 项目负责人 | DEMO-A、DEMO-B | 分配审核、看板、导出、解锁 |
| `demo_scanner` | 扫描员 | DEMO-A | 上传、重扫 |
| `demo_reviewer` | 审核员 | DEMO-B（角色授权）、DEMO-A（个人授权，M2 起） | 审核 |
| `demo_reviewer2` | 审核员 | DEMO-A（个人授权） | 改派、停用审核人的演示（M2 起手工建） |
| `demo_cataloger` | 归类员 | DEMO-A（个人授权，`m3-demo.mjs` 第一次运行时加） | 归类挂接 |
| `demo_noproject` | 归类员 | 没有 | "没有能进的项目" |
| `demo_useradmin` | 用户管理员（自建：用户、角色） | 没有 | 非 admin 的系统管理 |
| `demo_disabled` | 审核员（已停用） | DEMO-B | 授权对话框里的"已停用"，登录不了 |

新库上 `demo_reviewer2` 和 DEMO-A 里 `demo_reviewer`、`demo_reviewer2` 的个人授权要手工补（系统管理 → 用户、项目 → 授权），M2 的截图脚本要用。
