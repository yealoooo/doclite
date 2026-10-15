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
	"doclite/server/internal/platform/db"
	"doclite/server/migrations"
)

const shutdownTimeout = 15 * time.Second

// Run 启动 server：连数据库 → 执行迁移 → 建初始管理员 → 登记默认存储（库里没有存储配置时按 S3_*）→ 监听 HTTP；
// ctx 取消（收到 SIGINT、SIGTERM）后优雅退出（先结束 SSE 连接）。
func Run(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	log.Info("doclite server 启动", "version", Version, "listen", cfg.ListenAddr,
		"master_key_id", cfg.MasterKey.KeyID(), "session_ttl", cfg.SessionTTL.String())

	if err := os.MkdirAll(cfg.TmpDir, 0o755); err != nil {
		return fmt.Errorf("建临时目录 TMP_DIR=%s 失败：%w", cfg.TmpDir, err)
	}
	file.SweepTmp(cfg.TmpDir, log)

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
		WebDir:         cfg.WebDir,
		MasterKey:      cfg.MasterKey,
		Upload:         file.Options{TmpDir: cfg.TmpDir, MaxUploadBytes: cfg.MaxUploadBytes, MaxImagePixels: cfg.MaxImagePixels},
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

	switch {
	case cfg.WebDir == "":
		log.Warn("没有设置 WEB_DIR：只提供 /api，不托管网页")
	default:
		if st, err := os.Stat(cfg.WebDir); err != nil || !st.IsDir() {
			log.Warn("WEB_DIR 目录不存在，网页暂时打不开（build 之后不用重启）", "web_dir", cfg.WebDir)
		}
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

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("正在退出")
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Warn("没能在时限内处理完正在进行的请求", "error", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("已退出")
	return nil
}
