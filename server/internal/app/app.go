// Package app 组装各模块、注册路由、启动和优雅退出。
//
// 新模块接入：在 New 里建它的 Service、Handler，调 Handler.Routes(api, gate) 注册路由。
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"doclite/server/internal/audit"
	"doclite/server/internal/auth"
	"doclite/server/internal/catalog"
	"doclite/server/internal/config"
	"doclite/server/internal/export"
	"doclite/server/internal/file"
	"doclite/server/internal/folder"
	"doclite/server/internal/license"
	"doclite/server/internal/platform/events"
	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/jobs"
	"doclite/server/internal/project"
	"doclite/server/internal/review"
	"doclite/server/internal/role"
	"doclite/server/internal/storage"
	"doclite/server/internal/user"
)

// Version 是 server 的版本，构建时可以用 -ldflags "-X doclite/server/internal/app.Version=…" 覆盖。
var Version = "0.1.0"

// Options 是组装用的配置（从 config.Config 来；测试里直接给）。
type Options struct {
	SessionTTL     time.Duration
	CookieSecure   bool
	Password       auth.PasswordParams // 零值表示用 auth.DefaultPasswordParams
	ClientVersions config.ClientVersions
	// WebDir 是网页构建产物目录；为空时不托管网页（`/api` 以外的路径一律 404）。
	WebDir string
	// MasterKey 是主密钥（加密存储配置的 secret_key），必填。
	MasterKey config.MasterKey
	// Stream 是实时通道的保活、复核间隔；零值用默认的 15 秒、60 秒（测试里调短）。
	Stream project.StreamOptions
	// Upload 是上传的配置（TMP_DIR、MAX_UPLOAD_BYTES、MAX_IMAGE_PIXELS）；零值用默认值。导出的临时目录也在 TMP_DIR 下。
	Upload file.Options
	// Jobs 是后台任务执行器的配置（JOB_CONCURRENCY、JOB_HEARTBEAT_SECS、JOB_RETRY_BACKOFF_SECS）。Concurrency 为 0 时
	// StartBackground 什么都不启动（测试默认如此，要执行导出的测试自己设）。
	Jobs jobs.Options
	// ExportCleanupInterval 是导出物到期清理的间隔（EXPORT_CLEANUP_INTERVAL_SECS）；零值 10 分钟。
	ExportCleanupInterval time.Duration
	// ExportPartSize 是导出物分片上传每片的大小，只给测试调小；零值按导出物的预计大小算（至少 8 MiB）。
	ExportPartSize int64
	// License 是许可证（M7-1）：PublicKey 为空时不检查（开发构建）。Run 里用编进程序的公钥，测试注入测试公钥。
	License license.Options
	// StorageMigrate 是存储迁移（M7-2）执行的配置（每页取几个对象、几个并行），只给测试调；TmpDir 为空时用 Upload.TmpDir。
	StorageMigrate storage.MigrateOptions
}

// App 是组装好的 server。
type App struct {
	log     *slog.Logger
	handler http.Handler

	Auth     *auth.Service
	Users    *user.Service
	Roles    *role.Service
	Projects *project.Service
	Storage  *storage.Service
	// Events 是项目的实时事件总线（server 退出时 Close，结束所有 SSE 连接）。
	Events *events.Bus

	Folders *folder.Service
	Files   *file.Service
	// Reviews 是审核分配、审核、重扫（M3-1）。
	Reviews *review.Service
	// Exports 是导出（M5-2）；Jobs 是后台任务执行器（导出在里面执行）。
	Exports *export.Service
	Jobs    *jobs.Executor
	// License 是许可证（M7-1）；处理请求之前要调 InitLicense。
	License *license.Service
	// Migrations 是存储迁移（M7-2，后台任务 storage_migrate）。
	Migrations *storage.Migrator

	cleanupEvery time.Duration
	stopCleanup  context.CancelFunc
	cleanupDone  chan struct{}
}

// New 组装各模块。数据库迁移要在这之前执行（Run 里先迁移）。
func New(pool *pgxpool.Pool, opts Options, log *slog.Logger) (*App, error) {
	if opts.MasterKey == (config.MasterKey{}) {
		return nil, errors.New("app: 没有设置主密钥（MASTER_KEY）")
	}
	authSvc, err := auth.NewService(pool, auth.Options{
		SessionTTL:   opts.SessionTTL,
		CookieSecure: opts.CookieSecure,
		Password:     opts.Password,
	})
	if err != nil {
		return nil, err
	}
	roleSvc := role.NewService(pool, authSvc)
	userSvc := user.NewService(pool, authSvc, roleSvc)
	bus := events.NewBus()
	projectSvc := project.NewService(pool, bus, authSvc)
	storageSvc := storage.NewService(pool, opts.MasterKey)
	folderSvc := folder.NewService(pool, bus, authSvc, storageSvc)
	fileSvc := file.NewService(pool, bus, authSvc, storageSvc, folderSvc, opts.Upload)
	reviewSvc := review.NewService(pool, bus, authSvc, storageSvc, folderSvc, fileSvc)
	executor := jobs.New(pool, opts.Jobs, log)
	exportSvc := export.NewService(pool, bus, storageSvc, executor, export.Options{TmpDir: opts.Upload.TmpDir, PartSize: opts.ExportPartSize}, log)
	executor.Register(export.JobType, exportSvc.JobHandler())
	// 导出的临时目录只有拿到单实例锁的进程才能清（别的进程可能正在用）。
	executor.OnStart(func() { export.SweepTmp(opts.Upload.TmpDir, log) })
	licenseSvc := license.NewService(pool, authSvc, opts.License)
	authSvc.SetLicense(licenseSvc)
	// 存储迁移（M7-2）：后台任务 storage_migrate；迁移的临时目录同样只有拿到单实例锁的进程才能清。
	migrateOpts := opts.StorageMigrate
	if migrateOpts.TmpDir == "" {
		migrateOpts.TmpDir = opts.Upload.TmpDir
	}
	migrator := storage.NewMigrator(storageSvc, executor, migrateOpts, log)
	executor.Register(storage.MigrateJobType, migrator.JobHandler())
	executor.OnStart(func() { storage.SweepMigrateTmp(migrateOpts.TmpDir, log) })

	gate := auth.NewGate(authSvc)
	api := httpx.NewRouter()
	api.Handle("GET /api/health", gate.Public(), health)
	auth.NewHandler(authSvc).Routes(api, gate)
	user.NewHandler(userSvc).Routes(api, gate)
	role.NewHandler(roleSvc).Routes(api, gate)
	project.NewHandler(projectSvc, opts.Stream).Routes(api, gate)
	storage.NewHandler(storageSvc).Routes(api, gate)
	catalog.NewHandler(catalog.NewService(pool, bus)).Routes(api, gate)
	folder.NewHandler(folderSvc).Routes(api, gate)
	file.NewHandler(fileSvc).Routes(api, gate)
	review.NewHandler(reviewSvc).Routes(api, gate)
	export.NewHandler(exportSvc).Routes(api, gate)
	license.NewHandler(licenseSvc).Routes(api, gate)
	audit.NewHandler(audit.NewService(pool)).Routes(api, gate)
	storage.NewMigrateHandler(migrator).Routes(api, gate)

	// /api 的最外层是客户端版本检查（先于鉴权）；/api 以外是网页。
	apiHandler := auth.ClientVersionCheck(opts.ClientVersions)(api)
	root := http.NewServeMux()
	root.Handle("/api", apiHandler)
	root.Handle("/api/", apiHandler)
	if opts.WebDir != "" {
		root.Handle("/", httpx.WebHandler(opts.WebDir))
	} else {
		root.Handle("/", http.NotFoundHandler())
	}

	cleanupEvery := opts.ExportCleanupInterval
	if cleanupEvery <= 0 {
		cleanupEvery = 10 * time.Minute
	}
	return &App{
		log:          log,
		handler:      httpx.Logging(log)(root),
		Auth:         authSvc,
		Users:        userSvc,
		Roles:        roleSvc,
		Projects:     projectSvc,
		Storage:      storageSvc,
		Events:       bus,
		Folders:      folderSvc,
		Files:        fileSvc,
		Reviews:      reviewSvc,
		Exports:      exportSvc,
		Jobs:         executor,
		License:      licenseSvc,
		Migrations:   migrator,
		cleanupEvery: cleanupEvery,
	}, nil
}

// StartBackground 启动后台任务执行器（拿单实例锁 → 清导出的临时目录 → 回收上次执行中的）和导出物的到期清理。
// JOB_CONCURRENCY 为 0 时都不启动（导出一直排队，过期的导出物也不删）；同一个库上另一个 server 进程拿着锁时也不启动
// （打一条 warn），HTTP 照常，下载和查询不受影响。
func (a *App) StartBackground(ctx context.Context) error {
	if !a.Jobs.Enabled() {
		a.log.Warn("JOB_CONCURRENCY=0：这个进程不执行后台任务（导出会一直排队），也不做导出物的到期清理")
		return nil
	}
	if err := a.Jobs.Start(ctx); err != nil {
		if errors.Is(err, jobs.ErrBusy) {
			a.log.Warn("另一个 server 进程正在执行后台任务：这个进程不执行导出、不做到期清理（HTTP 照常）")
			return nil
		}
		return err
	}
	cctx, cancel := context.WithCancel(context.Background())
	a.stopCleanup = cancel
	a.cleanupDone = make(chan struct{})
	go func() {
		defer close(a.cleanupDone)
		a.Exports.RunCleanup(cctx, a.cleanupEvery)
	}()
	return nil
}

// StopBackground 优雅退出后台任务：不再领取，执行中的导出停下、放回队列（不算一次尝试）；停止到期清理。最多等到 ctx
// 结束。
func (a *App) StopBackground(ctx context.Context) {
	a.Jobs.Stop(ctx)
	if a.stopCleanup != nil {
		a.stopCleanup()
		select {
		case <-a.cleanupDone:
		case <-ctx.Done():
		}
		a.stopCleanup = nil
	}
}

// Handler 是整个 server 的 http.Handler（含请求日志）。
func (a *App) Handler() http.Handler { return a.handler }

// EnsureInitialAdmin 首次启动时建初始管理员（库里没有用户时）。
func (a *App) EnsureInitialAdmin(ctx context.Context, admin *config.InitAdmin) error {
	if admin == nil {
		return a.Users.EnsureInitialAdmin(ctx, "", "", "", false, a.log)
	}
	return a.Users.EnsureInitialAdmin(ctx, admin.Username, admin.DisplayName, admin.Password, true, a.log)
}

// InitLicense 生成 install_id（首次启动）、读导入的许可证并验签（M7-1）。要在处理请求之前调用。
func (a *App) InitLicense(ctx context.Context) error {
	if err := a.License.Init(ctx); err != nil {
		return err
	}
	if a.License.Dev() {
		a.log.Warn("开发构建（没有编进许可证公钥）：不检查许可证")
	} else {
		v, err := a.License.Get(ctx)
		if err != nil {
			return err
		}
		a.log.Info("许可证", "status", v.Status, "install_id", v.InstallID)
	}
	return nil
}

// EnsureInitialStorage 首次启动时按 S3_* 登记默认存储（库里没有存储配置时）。
func (a *App) EnsureInitialStorage(ctx context.Context, init *config.InitialStorage) error {
	return a.Storage.EnsureInitial(ctx, init, a.log)
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// health 是 `GET /api/health`：进程活着就返回 200，不用登录，不检查客户端版本。
func health(w http.ResponseWriter, _ *http.Request) error {
	httpx.WriteJSON(w, http.StatusOK, healthResponse{Status: "ok", Version: Version})
	return nil
}
