// doclite server：读配置、执行迁移、启动 HTTP、托管网页的构建产物。
//
// 配置见 server/.env.example；在 server/ 目录下运行时会读 server/.env。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"doclite/server/internal/app"
	"doclite/server/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置错误：", err)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, cfg, log); err != nil {
		log.Error("server 退出", "error", err)
		os.Exit(1)
	}
}
