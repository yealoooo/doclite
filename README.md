# Doclite

纸质档案数字化系统：扫描上传 → 分配审核 → 审核（驳回重扫）→ 归类挂接 → 导出。服务端 Go（`server/`，只提供 `/api`），网页 Vue 3（`web/`，单独启动），桌面客户端 Wails v3（`client/`）。三个模块代码上互不依赖，只靠 HTTP 接口和壳的桥命令约定（见 `CLAUDE.md` 的"三个模块"）。需求见 `docs/requirements.md`，进度见 `docs/roadmap.md`，开发约定见 `CLAUDE.md`。

## 目录

- [本机试用](#本机试用)
- [本机开发](#本机开发)
- [跑测试](#跑测试)
- [跑截图脚本](#跑截图脚本)
- [桌面客户端](#桌面客户端)
- [后台任务（导出、存储迁移）](#后台任务导出存储迁移)
- [演示账号](#演示账号)
- [许可证](#许可证)

## 本机试用

在这台 Mac 上自己试：server 跑在 9080、连本机的 `doclite_dev`（已经有演示账号和演示数据），只提供接口；网页由 web 单独提供（vite preview，4173，把 `/api` 转给 9080），浏览器打开网页的地址。PostgreSQL 16 是 Homebrew 服务，一直在跑；`server/.env` 已经配好（不要改里面的 `MASTER_KEY`）。server 和网页各占一个终端窗口。

1. **SeaweedFS**（没在跑时；`lsof -i :8333` 没有输出就是没在跑）：

   ```sh
   AWS_ACCESS_KEY_ID=doclite-dev AWS_SECRET_ACCESS_KEY=doclite-dev-secret S3_BUCKET=doclite \
     /Users/yanlong/project/smart_scan_bak/backend_storage/bin/weed mini \
     -dir=$HOME/doclite-dev/seaweedfs -ip=127.0.0.1 -admin.ui=false
   ```

2. **server**（要在 `server/` 下运行，它读当前目录的 `.env`；启动时自动执行迁移，Ctrl+C 退出）：

   ```sh
   cd server
   go build -o bin/doclite-server ./cmd/server
   ./bin/doclite-server
   ```

   server 只提供接口：浏览器直接打开 9080 只会得到 JSON 的 404（`/api/health` 能看到版本）。这是开发版，不检查许可证。导出、存储迁移由拿到执行器锁的 server 执行：启动日志里有"另一个 server 进程正在执行后台任务"时，是别的 server（例如开发任务临时起的 2908x）先拿到了锁，导出会交给它——先停掉那个再重启 9080（见[后台任务](#后台任务导出存储迁移)）。
3. **网页**（另开一个终端，Ctrl+C 退出）：

   ```sh
   cd web
   npm ci            # 第一次、或者 package-lock.json 变了之后
   npm run build     # 第一次、或者 web/ 改过之后（产物在 web/dist）
   npm run preview   # http://127.0.0.1:4173，/api（含实时推送）转给 http://127.0.0.1:9080
   ```

   浏览器打开 <http://127.0.0.1:4173>。server 不在 9080 时用 `DOCLITE_SERVER=http://127.0.0.1:<端口> npm run preview`；换端口 `npm run preview -- --port <端口>`；让局域网里别的电脑也能打开加 `-- --host`（监听所有地址；经局域网地址打开是非安全上下文）。改前端时用 `npm run dev`（<http://127.0.0.1:5173>，热更新，不用 build），代理一样。
4. **桌面客户端**（界面随客户端打包：先把 web 打成产物包，客户端打包时嵌进去）：

   ```sh
   cd web && npm run release            # web/release/doclite-web-<版本>.tar.gz（先 build 再打包）
   cd ../client
   ~/go/bin/wails3 package              # 取 WEB_VERSION 指定版本的产物包嵌进去，产物 client/bin/Doclite.app
   open bin/Doclite.app
   ```

   第一次启动填服务器地址：`http://127.0.0.1:4173`（上一步的网页地址，和浏览器用的同一个；直接填 server 的 `http://127.0.0.1:9080` 也行，客户端只经它访问 `/api`）。界面是打包时嵌进去的那一版：改了 web 要重新 `npm run release`、`wails3 package`。关窗口只是缩到托盘（菜单栏图标），托盘里"退出"才真的退出；再次打开会回到已经开着的那个窗口。客户端的设置、会话、上传队列、图片缓存在 `~/Library/Application Support/Doclite/`。

**账号**：`admin` / `admin123456`（系统管理员，什么都能做）；演示账号的密码都是 `demo123456`，常用的有 `demo_scanner`（扫描上传、重扫）、`demo_manager`（审核分配、看板、导出）、`demo_reviewer`（审核）、`demo_cataloger`（归类），全部见[演示账号](#演示账号)。同一个人可以在浏览器和客户端各登录一次。

**可以拿来试的数据**（都在项目 DEMO-A；会改东西的操作最好在自己新建的文件夹里做）：

| 想试 | 用哪里 |
|---|---|
| 上传 | 文件目录里新建一个文件夹，用"上传文件 / 上传文件夹"（浏览器、客户端都行）；客户端的"扫描上传"页选一个本机目录当热文件夹，往里放 JPEG、PNG、TIFF。样本在 `server/testdata/samples/`（`tiff-multipage-2p.tif` 是故意会被拒的多页 TIFF）；同样内容的图在项目里只入库一次 |
| 浏览、看原图 | 「2025 文书档案/盒 WS-2025-001」2000 页；「M2-3 分块实测」有 200 MB 的 TIFF（客户端里切块看，浏览器里会慢或只显示封面）；「M6-3 切块自检」是各种压缩的 TIFF |
| 审核分配 | demo_manager → 审核分配：「M2-5 分配演示」40 页待分配 |
| 审核 | demo_reviewer → 我的审核：「M2-6 审核演示」还有 29 页待审（键盘 A 通过、R 驳回、C 对比上一轮） |
| 重扫 | demo_scanner → 扫描上传，右边"待重扫"：「M2 验收演示 2026-10-16 M6-4/盒 04/IMG_0003」是特意留着的已驳回页，「M2-7 重扫演示」里也还有几页。重扫件要选一张内容不同的图 |
| 归类挂接 | demo_cataloger → 归类：全宗 Z206「M3-6 挂接演示」（文件池选文件夹「M3-6 挂接演示」）；Z201「M3-5 归类演示」有 1200 多件 |
| 看板 | demo_manager → 项目看板：全宗 Z109「M3-7 看板演示」 |
| 导出 | demo_manager → 导出，或在归类树上右键"导出…"：全宗 Z405「M4-5 导出演示」的 BGS 有 3 件就绪；客户端里的下载交给系统浏览器 |
| 存储迁移 | admin → 系统管理 → 存储配置：「M7-2 迁移演示·源」已经迁到「·目标」并停用，可以看结果和失败详情 |
| 审计日志、许可证 | admin → 系统管理 → 审计日志（筛选、展开看前后差异）；许可证页在开发版只显示 install_id，不能导入 |

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

**分别启动 server 和 web**（三个模块各自独立，见 `CLAUDE.md` 的"三个模块"）：

```sh
cd server && go run ./cmd/server           # 在 server/ 下运行，读当前目录的 .env；启动时自动执行迁移；只提供 /api（LISTEN_ADDR，默认 9080）
cd web && npm ci && npm run dev            # 另一个终端；浏览器打开 http://127.0.0.1:5173
```

- server 只提供 `/api`，`/api` 以外的路径一律 JSON 404，不托管网页。
- web 用 vite 单独启动：`npm run dev`（5173，热更新）或 `npm run build && npm run preview`（4173，构建产物，和部署时的 nginx 一样是静态文件）。两种都把 `/api`（包括实时推送）代理给 `DOCLITE_SERVER`（默认 `http://127.0.0.1:9080`）；端口、监听地址用 vite 的参数：`DOCLITE_SERVER=http://127.0.0.1:29080 npm run preview -- --port 24173 --strictPort`（默认只监听 127.0.0.1，`--host` 监听所有地址）。
- 部署时 web 是单独的 nginx 容器（静态文件 + `/api` 反向代理给 server），M8 做。
- 端口约定：自己的 server 用 9080；并行的子任务用 29080–29082（server）和 24173–24175（vite preview），互不干扰。
- 同一个库上开多个 server 时，只有一个执行导出（见[后台任务](#后台任务导出存储迁移)）；多个 server 的 `TMP_DIR` 要分开（启动时会清空 `TMP_DIR/uploads`）。

## 跑测试

提交前：每个 Go 模块在自己的目录下 `gofmt -l .` 没有输出、`go vet ./...`、`go test ./...` 通过（没有 Go 工作区，server、client 各自独立）；改了 `web/` 时 `npm run build` 通过。

```sh
cd server && gofmt -l . && go vet ./... && go test ./...   # API 测试（82 个）+ 单元测试（含文件头、名称规则、档号模板），本机约 20 秒
cd client && gofmt -l . && go vet ./... && GOOS=windows go vet ./... && go test ./...   # 客户端的纯逻辑（Mac 上要 cgo，Wails 需要）
cd web && npm run build                                    # vue-tsc 类型检查 + 构建
```

- 读图片文件头的 `imagehdr` 有两份（`server/internal/platform/imagehdr`、`client/internal/imagehdr`），改了一份要同步另一份（连同测试和 `testdata/encoding`），两边的测试都跑；上传是否拒收以 server 为准。测试样本各带各的：`server/testdata/samples`、`client/testdata/samples`（完整副本）、`web/scripts/samples`（截图脚本要的几个）。
- **API 测试**（`server/apitest/`）连真实的 PostgreSQL 和 SeaweedFS。第一次运行时建模板库 `doclite_test_tpl_<迁移哈希>`，每个测试从它克隆一个库、用自己的 S3 前缀，结束时删掉（强杀可能留下 `doclite_test_<随机>` 库，可以手工删）。连接参数：`TEST_DATABASE_URL`（默认 `postgres://doclite@127.0.0.1:5432/doclite_test`）、`TEST_S3_ENDPOINT`、`TEST_S3_ACCESS_KEY`、`TEST_S3_SECRET_KEY`、`TEST_S3_BUCKET`（默认同上面的 `weed mini`）。SeaweedFS 没启动时测试直接报错。存储迁移的测试另用第二个桶 `TEST_S3_BUCKET2`（默认 `doclite-migrate-test`，不存在时测试自己建）。`TEST_LOG=1` 打出 server 日志。
- **客户端上传的集成测试**（要一个在跑的 server）：`cd client && DOCLITE_IT_SERVER=http://127.0.0.1:9080 DOCLITE_IT_USERNAME=… DOCLITE_IT_PASSWORD=… DOCLITE_IT_PROJECT=DEMO-A go test ./internal/upload/ -run Integration`。
- **性能抽测**（默认不跑，`//go:build perf`）：用 SQL 造 10 万文件（6 万页挂在 2400 件里），测审核读接口、看板、`catalog-placements`、2000 件 / 5 万页的导出预检、建任务、交付后变更，打印每个接口的中位数和最慢，超过 requirements 1.4 的目标就失败：

  ```sh
  cd server && go test -tags perf -run TestPerf -v -timeout 20m ./apitest/
  ```

## 跑截图脚本

`web/scripts/shots/*.mjs` 是上一版留下的界面验收：用本机的 Chrome（puppeteer-core，不下载浏览器；找不到时设 `CHROME_PATH`）无头打开页面、操作、断言、截图。失败的那张打 ✗，脚本退出码 1。

先分别起 server 和 web（见[本机开发](#本机开发)），`--base` 是 web 的地址：

```sh
cd server && go run ./cmd/server                        # 9080；要执行导出、迁移的脚本要它拿到执行器锁
cd web && npm run build && npm run preview              # 另一个终端：http://127.0.0.1:4173，/api 转给 9080
cd web && INIT_ADMIN_PASSWORD=<管理员密码> npm run screenshots -- --spec scripts/shots/m0.mjs --base http://127.0.0.1:4173
#   --base <URL>     web 的入口，默认 http://127.0.0.1:5173（npm run dev）
#   --only a,b       只跑这几张（按 name）
#   --out <目录>      截图放哪（默认 docs/screenshots/<里程碑>；截图不提交，跑完 git clean -fd docs/screenshots）
```

- `--base` 是 web 的入口：vite preview（`npm run preview`）或 dev（`npm run dev`），`/api` 由它们代理给 server；有壳模式截图的脚本要用 devshell（见下面）。server 只提供接口，不能当 `--base`。脚本在页面之外调接口也走这个地址。
- 并行任务各用一组端口：`DOCLITE_SERVER=http://127.0.0.1:29080 npm run preview -- --port 24173 --strictPort`，`--base http://127.0.0.1:24173`。
- 上传用的样本在 `web/scripts/samples/`（从 `server/testdata/samples` 复制的几个，清单见那里的 README），web 不读别的模块目录里的文件。
- `INIT_ADMIN_PASSWORD`（`INIT_ADMIN_USERNAME`）是管理员的密码，和启动 server 用同一组变量；演示账号的密码 `demo123456`（`DEMO_PASSWORD` 可改）。`SHOTS_TMP` 是脚本的临时目录（默认系统临时目录）。
- 几个较早的脚本直接 `psql` 读审计（M7-3 之后有 `GET /api/system/audit-logs`，新脚本用接口）：要 `psql` 在 PATH 里，`DATABASE_URL` 指向同一个库（默认本机 `doclite_dev`）。

**准备数据**（新库；已有的会跳过；seed 脚本只调接口，`--base` 是 server 本身或 web 的入口都行，默认 `http://127.0.0.1:9080`）：

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
- `m4-export`：server 要在执行导出（见[后台任务](#后台任务导出存储迁移)）。
- `m7-storage-migrate`：server 要在执行后台任务（迁移在它里面执行）；要有第二个桶 `doclite-migrate-test`（`echo 's3.bucket.create -name doclite-migrate-test' | weed shell -master=127.0.0.1:9333`）。脚本自己起一个转发到 SeaweedFS 的慢速代理当"源"存储的 server 地址（让迁移持续几秒），每跑一次在 DEMO-A 的「M7-2 迁移演示」下传一批文件，迁到「M7-2 迁移演示·目标」，跑完"源"迁空并停用。
- 壳的部分（`m1-shell`、`m1-scan`、`m1-cache` 和 `m1-organize`、`m2-rescan`、`m2-viewer`、`m2-review`、`m2-accept` 里名字带 `shell` 的几张）：`--base` 要指向 **devshell**（见下面）。这几张用 `http://wails.localhost:<端口>` 打开页面（`lib/client.ts` 的 `isShell()` 认 Windows 上 WebView2 的这个地址，Chrome 把 `*.localhost` 解析到本机），在页面脚本运行前注入 Wails 桥的模拟（`scripts/lib/wails-mock.mjs`：拦下 `/wails/runtime` 的命令调用、`_wails.dispatchWailsEvent` 推事件、`_wails.streamFactory` 给实时通道），正式前端代码不改。
- `m2-accept`：`M2_ACCEPT_SHELL_MOCK=1` 时壳模式审核那两步用上面的模拟（否则改在浏览器里做）；`M2_ACCEPT_SHELL=client/bin/Doclite.app/Contents/MacOS/Doclite`、`M2_ACCEPT_SERVER=<网页的入口>`（vite 的地址；不设时用 `--base`）时壳重扫、壳的大图两步跑打包的客户端 `--smoke`（隔离的数据目录、后台模式），不设时壳重扫改用接口模拟。`m3-accept` 的 `M3_ACCEPT_SHELL` 同理。

**devshell**（`client/cmd/devshell`，截图脚本的开发服务器，不打包）：一个端口上做打包的客户端的资产服务器做的事，只是界面从 `-web` 指定的 web 入口（vite dev 或 preview）现取（截图测的是 web 的源码，不是嵌入的产物包）——`/shell/` 是客户端自带的页面（`internal/shellpages`）、`/wails/runtime.js` 是 Wails 的运行时、`/api/` 转给 web 入口（它再转给 server）、取图地址 `/shell/img/…` 用客户端的 Go 代码（`internal/imagecache`、`internal/tiles`）现场下载、缓存、切块；壳模式的地址和客户端一样加 CSP。桥（命令、事件、流）由截图脚本在页面里模拟。浏览器的那些截图也可以照常经它跑。

```sh
cd web && DOCLITE_SERVER=http://127.0.0.1:9080 npm run dev                 # 网页入口 5173（或 build + preview 4173）
cd client && go run ./cmd/devshell -web http://127.0.0.1:5173 -listen 127.0.0.1:24173 -cache <临时目录>
cd web && INIT_ADMIN_PASSWORD=<管理员密码> npm run screenshots -- --spec scripts/shots/m1-scan.mjs --base http://127.0.0.1:24173
```

## 桌面客户端

Wails v3（命令行工具 `wails3`，`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.29`）。目前只有 Mac 的构建；Windows 的构建和安装包在 M8（打包交付）做。

**界面随客户端打包，版本跟客户端走**（M7-6，参照 Element：网页版和桌面版共用界面，桌面版打包指定版本的网页产物）。客户端不依赖 web 的源码，只要 web 的产物包：

```sh
cd web && npm run release        # 构建并打包：web/release/doclite-web-<package.json 的版本>.tar.gz（manifest.json + dist/）
cd client && wails3 package      # 打包成 client/bin/Doclite.app（ad-hoc 签名）
```

- `client/WEB_VERSION` 记要嵌入的 web 版本。打包（`wails3 build`、`wails3 package`）前的 frontend 任务（`client/cmd/webartifact`）按 `DOCLITE_WEB_ARTIFACT`（产物包的路径）取，没给就找 `../web/release/doclite-web-<WEB_VERSION>.tar.gz`；核对包里清单的版本和 `WEB_VERSION` 一致后解到 `client/frontend/`（不提交），再 embed 进可执行文件。找不到产物包或版本不对时构建失败并说明怎么办。别处构建好的产物包：`DOCLITE_WEB_ARTIFACT=/path/doclite-web-0.1.0.tar.gz wails3 package`（不需要 `web/` 目录）。以后拆仓库，改成从发布地址下载这个包；
- 升级界面：改 `web/package.json` 的版本、`npm run release`，改 `client/WEB_VERSION`，重新打包客户端。客户端的版本号在 `client/VERSION`（`X-Client-Version`，server 的 `MIN_CLIENT_VERSION` 和它比），"关于"里同时显示客户端版本和界面版本；
- 客户端里的"服务器地址"是 **web 服务的入口**（和浏览器用的同一个地址：部署时是 web 的 nginx 容器，本机是 vite 的 4173 或 5173），直接填 server 本身的地址也行——客户端只经它访问 `/api`（探测只要 `GET /api/health` 通）。已经配置过地址、再改成别的地址时，客户端弹系统确认框；
- 连接服务器、连不上、需要升级几页是客户端自带的（`client/internal/shellpages`）；
- 界面和客户端之间的约定（桥的命令、事件、能力、白名单、地址、信任边界）只有一份：[`client/BRIDGE.md`](client/BRIDGE.md)。

**开发时**：界面热更新用开发构建接 vite（只在开发构建里；正式构建只用嵌入的产物）：

```sh
cd web && DOCLITE_SERVER=http://127.0.0.1:9080 npm run dev          # http://127.0.0.1:5173
cd client && wails3 task build:dev                                  # 开发版：有产物包就嵌入，没有也能编译
FRONTEND_DEVSERVER_URL=http://127.0.0.1:5173 bin/Doclite            # 界面由 vite 给（热更新），Safari 能开网页检查器
```

客户端的服务器地址照样填（vite 的 5173 或 server 的 9080 都行）。不想动自己的设置时加 `DOCLITE_DATA_DIR=<临时目录>`。

**自检 `--smoke`**（参数从环境变量读；结果是标准输出的一行 JSON；退出码 0 成功、1 失败、2 连不上、3 需要升级）：

```sh
DOCLITE_SMOKE_SERVER=http://127.0.0.1:4173 DOCLITE_SMOKE_USERNAME=demo_scanner DOCLITE_SMOKE_PASSWORD=demo123456 \
DOCLITE_BACKGROUND=1 client/bin/Doclite.app/Contents/MacOS/Doclite --smoke
```

自检分三段：① 客户端自带的页面（壳操作 `/shell/`）：`shell_page` 还没配置地址时出现连接页、填连不上的地址提示连不上且不保存；`shell_unreachable` 配置成连不上的地址时出现"连不上"页、重试、改地址；`shell_connect` 填 `DOCLITE_SMOKE_SERVER` 进到嵌入的界面。② 界面里的各步（probe、login……csp，同以前）。③ 换地址：`shell_edit` 界面跳到客户端的修改地址页；`change_server` 换到壳另起的一个转发到 `DOCLITE_SMOKE_SERVER` 的地址，会话清空，接口请求走新地址（自检里不弹确认框）。

| 变量 | 说明 |
|---|---|
| `DOCLITE_SMOKE_SERVER`、`DOCLITE_SMOKE_USERNAME`、`DOCLITE_SMOKE_PASSWORD` | 服务器地址（web 的入口或 server 本身）和账号（账号至少能进一个项目） |
| `DOCLITE_SMOKE_TIMEOUT_SECS` | 整个自检的时限，默认 120（加上 pages 和主流程要 300 以上） |
| `DOCLITE_SMOKE_PAGES=1` | 另外逐个打开菜单里的页面 |
| `DOCLITE_SMOKE_RESCAN_PATH`、`_FILE_ID`、`_PROJECT`、`_EXPECT` | 设了 PATH 才跑 rescan 一步：用这个本机文件重扫（不设 FILE_ID 取待重扫列表第一个；EXPECT 是 `done` 或 `incomplete`）。重扫件的内容在项目里不能重复 |
| `DOCLITE_SMOKE_CACHE` | img_cache、tiles 两步：`cold`（默认，先清空自检的缓存，要求全部下载、现切）或 `warm`（要求全部命中；先跑一次 cold） |
| `DOCLITE_SMOKE_IMG_PROJECT`、`_IMG_FOLDER`、`_IMG_COUNT` | img_cache 一步取哪个项目、文件夹（从项目根起，`/` 分隔）的前几个文件（默认第一个有文件的文件夹、8 个） |
| `DOCLITE_SMOKE_TILES_PROJECT`、`_TILES_FOLDER`、`_TILES_FILES` | tiles、viewer 两步：默认找文件名含 g4、lzw 的 TIFF；`_TILES_FILES` 指定文件夹里的几个文件（逗号分隔，第一个给 viewer 看，含 JPEG 压缩 TIFF 的列表见 roadmap M6-3b） |
| `DOCLITE_SMOKE_FLOW_SAMPLES` | 设了才跑主流程五步（M6-4）：本机样本文件夹（单页 JPEG、PNG、TIFF，按文件名取前 2 × COUNT 个）。壳复制一份、每个文件写进本次独有的注释（内容不重复）：壳里上传（`pick_and_enqueue_upload` 用复制的文件夹代替系统对话框）→ 热文件夹（`hotfolder_pick_dir` 用准备好的空文件夹，壳再往里放文件）→ 分给自己、在查看器里看原图、逐页通过 → 建条目挂上、定稿 → 导出，下载地址经 `open_external` 交给壳（自检里不打开系统浏览器），壳用 HTTP 下载核对 zip。账号要有扫描、审核分配、审核、归类、导出菜单（admin）；server 要在执行导出。每次在项目里留下一个「<FOLDER>/自检 <时间>」文件夹和一个定稿的条目 |
| `DOCLITE_SMOKE_FLOW_PROJECT`、`_FOLDER`、`_COUNT`、`_FORMAT` | 主流程的项目（默认第一个能进的）、上级文件夹（默认「客户端自检」）、两种上传各几个（默认 3）、导出格式（`pdf` 默认或 `images`）。归类节点按档号模板用到的层级找或建：全宗 `ZJ`「客户端自检」/ `WS` / `2026` / `Y` / `ZJ` |

其他环境变量：`DOCLITE_DATA_DIR` 换数据目录（默认 `~/Library/Application Support/Doclite/`、`%LOCALAPPDATA%\Doclite\`；自检用其中的 `smoke/`）；`DOCLITE_BACKGROUND=1` 不抢焦点（开发、测试用）；`DOCLITE_LOG_LEVEL=debug`；`DOCLITE_LOG_STDERR=1` 日志同时写到标准错误。删掉数据目录里的 `upload-queue.db` 就是重置上传队列。

## 后台任务（导出、存储迁移）

导出在 server 进程里执行（`background_jobs` 表排队）。配置（都在 `server/.env.example` 里有说明）：

| 变量 | 默认 | 说明 |
|---|---|---|
| `JOB_CONCURRENCY` | 2 | 同时执行几个导出（0–16）。**0 表示这个进程不执行导出、也不做到期清理** |
| `JOB_HEARTBEAT_SECS` | 10 | 心跳间隔（顺便查取消、回收没人执行的任务） |
| `JOB_RETRY_BACKOFF_SECS` | 30 | 可重试的失败第一次重试前等多久，之后每次 × 4，最多 1 小时；一共最多执行 3 次 |
| `EXPORT_CLEANUP_INTERVAL_SECS` | 600 | 导出物到期清理的间隔（保留天数在项目设置 `export.retention_days`，默认 7） |
| `TMP_DIR` | 系统临时目录下的 `doclite` | 导出在 `TMP_DIR/exports/` 下生成 PDF，要放得下最大一件原图 × 1.2 |

**同一个库只有一个 server 进程执行后台任务**：执行器启动时拿数据库咨询锁 `(6006, 0)`，拿到的那个执行导出、做到期清理、管导出的临时目录；后启动的拿不到锁，只提供 HTTP（日志里一条警告），它那边建的导出由先启动的进程执行。所以本机多个 server 共用 `doclite_dev` 时，谁先启动谁执行；想让某个进程一定执行，就先停掉别的（或者给别的设 `JOB_CONCURRENCY=0`）。部署时只有一个 server 进程。

**存储迁移**（M7-2，系统管理 → 存储配置 →"迁移到…"）也是后台任务（`job_type = storage_migrate`），和导出共用上面的执行器、并发数和重试（迁移最多执行 5 次，排队时导出先领）。**整个系统同一时间只有一个迁移在排队或执行**；它执行期间占一个执行槽，导出的并发从 2 降到 1（`JOB_CONCURRENCY=1` 时迁移期间导出要等它完成）。一个对象读写存储暂时失败时就地重试 3 次（1、4、16 秒），还不行才停下这次执行、交给执行器重试（从断点接着做）。复制原图时用 `TMP_DIR/migrate/` 放临时文件（每个在复制的对象一个，同时最多 4 个）。迁移不删源存储里的对象；`JOB_CONCURRENCY=0` 或者拿不到执行器锁的进程上建的迁移，由拿着锁的进程执行（它的版本要有迁移，否则一直排队）。

迁完之后运维的顺序：先把原存储**改为只读**（不再写入，旧的导出物照常能下载）；导出物到期被自动清理（保留天数 + 链接有效期 + 24 小时；清理照常处理只读存储里的导出物，只跳过已停用存储里的）之后再**停用**。导出过的存储一直被导出任务记着（`export_tasks.storage_profile_id`，完成的任务清理后也保留"当时存在哪"），所以只能停用、不能删除；没导出过的、迁空了的可以删除。存储里的对象（迁走的原图和封面、清理前的导出物）要不要删由运维决定。

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

## 许可证

规则见 requirements 第 11 节：席位按**登录会话数**算（同一个人两处登录算 2；30 分钟内有活动的才算在线，打开的实时通道也算活动）；持有内置 admin 角色的人总能登录（会话照样计数）；到期后别人登不上、已登录的下一次请求被拒（`license_expired`），导入新的立即恢复；没导入时只有 admin 能登录（`license_missing`）；到期前 30 天起给管理员显示提醒条。界面在"系统管理 → 许可证"（菜单 `system.license`）。

**开发构建和正式构建**：server 只验签，公钥编进程序，**不能用环境变量改**。没编进公钥的构建（`go run`、普通的 `go build`）是开发版，不检查许可证（谁都能登录，许可证页显示"开发版，不检查许可证"，不能导入）。正式构建用 `-ldflags` 编进公钥，值是签发工具生成的 `public.pem` 中间那一行（SPKI 的 base64；也可以是 32 字节原始公钥的 base64）：

```sh
cd server
go build -ldflags "-X doclite/server/internal/license.publicKey=$(grep -v -- ----- /path/to/public.pem)" -o doclite-server ./cmd/server
```

值不对时 server 不启动（日志写明原因）；启动日志里有公钥的指纹和许可证的状态。正式公钥由用户提供，打包（M8）时写进构建脚本。

**install_id**：server 首次启动时生成，存在 `system_settings`（键 `install_id`），以后不变；格式是 `sha256:` + 64 位十六进制（签发工具要求的 `--hardware-id` 格式，内容是随机数）。许可证页显示，可以复制。

**签发**（工具在 `../bak/doclite-license`，Rust，Ed25519；编好的程序在 `../bak/doclite-license/bin/doclite-license`（Mac arm64，2026-10-16 清理编译产物时留下的），直接用；要重新编译用 `cargo build --release`，Web 管理后台见它的 README）：

```sh
doclite-license keygen --output ./keys            # 只做一次：private.pem 妥善保管（backup-key 做加密备份），public.pem 给构建用
doclite-license issue --private-key ./keys/private.pem --company "某某档案馆" --edition enterprise \
  --max-users 20 --duration 1y --hardware-id "<许可证页上的 install_id>" --output license.lic
doclite-license renew --private-key ./keys/private.pem --base-license license.lic --duration 1y --output renewed.lic   # 续期
```

`--max-users` 就是席位数；`--edition`（trial、standard、enterprise）只显示。把 .lic 交给客户，管理员在许可证页粘贴内容或选文件导入，立即生效；签名不对（`invalid_license`）、install_id 不符（`license_mismatch`）、已经过期（`license_file_expired`）都会说明。导入、替换写审计（`target_type=license`，`action` 是 `import` 或 `replace`）。

**和签发工具的兼容**：签名签的是工具把 payload 重新序列化成的规范 JSON（字段按名字排序、serde_json 的转义、chrono 的时间写法），`internal/license` 照着复现；`internal/license/testdata` 里是用工具签的 .lic（命令行 `issue`，以及按路径编译工具自己的 `license.rs`、`crypto.rs` 签的固定时间、带各种转义字符的几份）和工具算出的规范 JSON，单元测试逐字节比对、验签，API 测试导入其中几份。夹具里只有测试公钥，私钥不在仓库里。

**本机试正式构建**：用工具（或测试夹具的做法）生成一对测试密钥，按上面编进测试公钥，另起一个 server（换端口和 `TMP_DIR`）；它连的库里导入的许可证换回开发构建后不起作用，换成正式公钥后显示"无效"，要重新导入。

