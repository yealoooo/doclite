package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"doclite/server/internal/config"
	"doclite/server/internal/file"
	"doclite/server/internal/license"
	"doclite/server/internal/platform/db"
	"doclite/server/internal/platform/jobs"
	"doclite/server/migrations"
)

const shutdownTimeout = 15 * time.Second

// jobsStopTimeout：退出时最多等多久让执行中的导出停下、放回队列（docker stop 默认 10 秒后强制结束）。
const jobsStopTimeout = 8 * time.Second

// Run 启动 server：连数据库 → 执行迁移 → 建初始管理员 → 登记默认存储（库里没有存储配置时按 S3_*）→ 监听端口
// （端口被占用就到此为止，不动临时目录和后台任务）→ 清上传的临时目录 → 启动后台任务（清导出的临时目录、把上次执行中的
// 放回队列、到期清理）→ 提供 HTTP；ctx 取消（收到 SIGINT、SIGTERM）后优雅退出（执行中的导出放回
// 队列，结束 SSE 连接）。
func Run(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	log.Info("doclite server 启动", "version", Version, "listen", cfg.ListenAddr,
		"master_key_id", cfg.MasterKey.KeyID(), "session_ttl", cfg.SessionTTL.String())

	// 编进程序的许可证公钥（M7-1）：值不对时不启动；没有时是开发构建，不检查许可证。
	licenseKey, err := license.BuiltinPublicKey()
	if err != nil {
		return err
	}
	if licenseKey != nil {
		log.Info("许可证公钥", "fingerprint", license.Fingerprint(licenseKey))
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if _, err := db.Migrate(ctx, pool, migrations.FS, log); err != nil {
		return err
	}

	a, err := New(pool, Options{
		SessionTTL:     cfg.SessionTTL,
		CookieSecure:   cfg.CookieSecure,
		ClientVersions: cfg.ClientVersions,
		MasterKey:      cfg.MasterKey,
		Upload:         file.Options{TmpDir: cfg.TmpDir, MaxUploadBytes: cfg.MaxUploadBytes, MaxImagePixels: cfg.MaxImagePixels},
		Jobs:           jobs.Options{Concurrency: cfg.JobConcurrency},
		License:        license.Options{PublicKey: licenseKey},
	}, log)
	if err != nil {
		return err
	}
	if err := a.EnsureInitialAdmin(ctx, cfg.InitAdmin); err != nil {
		return err
	}
	if err := a.EnsureInitialStorage(ctx, cfg.InitialStorage); err != nil {
		return err
	}
	if err := a.InitLicense(ctx); err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	// 退出时先结束 SSE 连接（它们不会自己结束，Shutdown 会一直等到超时）。
	srv.RegisterOnShutdown(a.Events.Close)
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败：%w", cfg.ListenAddr, err)
	}
	log.Info("开始监听", "addr", ln.Addr().String())

	// 端口拿到了才动临时目录和后台任务（同一个库上已经有 server 时，起不来的那个不能清掉别人的临时文件、回收别人的任务）。
	if err := os.MkdirAll(cfg.TmpDir, 0o755); err != nil {
		ln.Close()
		return fmt.Errorf("建临时目录 TMP_DIR=%s 失败：%w", cfg.TmpDir, err)
	}
	file.SweepTmp(cfg.TmpDir, log)
	if err := a.StartBackground(ctx); err != nil {
		ln.Close()
		return err
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		sctx, cancel := context.WithTimeout(context.Background(), jobsStopTimeout)
		a.StopBackground(sctx)
		cancel()
		return err
	case <-ctx.Done():
	}
	log.Info("正在退出")
	jobsDone := make(chan struct{})
	go func() {
		defer close(jobsDone)
		jctx, cancel := context.WithTimeout(context.Background(), jobsStopTimeout)
		defer cancel()
		a.StopBackground(jctx)
	}()
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	defer func() { <-jobsDone }()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Warn("没能在时限内处理完正在进行的请求", "error", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("已退出")
	return nil
}
