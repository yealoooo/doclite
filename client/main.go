// Doclite 桌面客户端（壳）。界面是 web/ 的构建产物（打包时复制到 frontend/dist 再 embed 进来）；
// 壳负责会话代理、托盘、单实例，以后还有上传队列、热文件夹、本机图片缓存和大图切块（requirements 第 6 节）。
//
// 用法：
//
//	Doclite            正常启动
//	Doclite --smoke    自检（参数从环境变量读，见 internal/shell/smoke.go），结果是标准输出的一行 JSON，
//	                   退出码 0 成功、1 失败、2 连不上、3 需要升级
//
// 环境变量：DOCLITE_DATA_DIR 换数据目录；DOCLITE_LOG_LEVEL=debug 记更多日志；DOCLITE_LOG_STDERR=1 日志同时写到
// 标准错误；DOCLITE_BACKGROUND=1 不抢焦点（开发、测试用：不激活应用，窗口一开始隐藏）。
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing/fstest"
	"time"

	"doclite/client/internal/appdata"
	"doclite/client/internal/proxy"
	"doclite/client/internal/shell"
)

// version 是客户端版本，构建时用 -ldflags "-X main.version=…" 写入（和 web/package.json 的 version 相同，
// 见 Taskfile.yml）。请求头 X-Client-Version 就是它，server 拿它和 MIN_CLIENT_VERSION 比较。
var version = "0.0.0-dev"

//go:embed all:frontend
var frontend embed.FS

//go:embed packaging/appicon.png
var appIcon []byte

//go:embed packaging/tray.png
var trayIcon []byte

//go:embed packaging/tray-template.png
var trayTemplate []byte

func main() {
	os.Exit(run())
}

func run() int {
	smoke := false
	for _, a := range os.Args[1:] {
		if a == "--smoke" {
			smoke = true
		}
	}

	dataDir, err := appdata.Dir(smoke)
	if err != nil {
		return fatal(smoke, "找不到本机数据目录："+err.Error())
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fatal(smoke, "建不了数据目录："+err.Error())
	}
	logFile, err := appdata.OpenDailyLog(filepath.Join(dataDir, "logs"))
	if err != nil {
		return fatal(smoke, "打不开日志目录："+err.Error())
	}
	defer logFile.Close()
	log, wailsLog := newLoggers(logFile)

	settings, err := appdata.LoadSettings(dataDir)
	if err != nil {
		log.Warn("设置文件读不了，按没有配置处理", "err", err)
		settings = appdata.Settings{}
	}

	var smokeOpts *shell.SmokeOptions
	if smoke {
		server, err := proxy.NormalizeServerURL(os.Getenv("DOCLITE_SMOKE_SERVER"))
		if err != nil {
			return fatal(true, "DOCLITE_SMOKE_SERVER 不对："+err.Error())
		}
		timeout := 120 * time.Second
		if v, err := strconv.Atoi(os.Getenv("DOCLITE_SMOKE_TIMEOUT_SECS")); err == nil && v > 0 {
			timeout = time.Duration(v) * time.Second
		}
		smokeOpts = &shell.SmokeOptions{
			Server:   server,
			Username: os.Getenv("DOCLITE_SMOKE_USERNAME"),
			Password: os.Getenv("DOCLITE_SMOKE_PASSWORD"),
			Timeout:  timeout,
			Pages:    os.Getenv("DOCLITE_SMOKE_PAGES") == "1",
		}
		// 自检每次从头登录：用自己的设置，清空自己的会话。
		settings.ServerURL = server
		if err := appdata.SaveSettings(dataDir, settings); err != nil {
			return fatal(true, "写自检的设置失败："+err.Error())
		}
		os.Remove(appdata.CookiesPath(dataDir))
	}

	jar := proxy.OpenJar(appdata.CookiesPath(dataDir), settings.ServerURL)
	px := proxy.New(version, jar, log)

	code, err := shell.Run(shell.Config{
		Version:      version,
		DataDir:      dataDir,
		Smoke:        smokeOpts,
		Background:   os.Getenv("DOCLITE_BACKGROUND") == "1",
		Assets:       assets(log),
		AppIcon:      appIcon,
		TrayIcon:     trayIcon,
		TrayTemplate: trayTemplate,
		Log:          log,
		WailsLog:     wailsLog,
		Proxy:        px,
	})
	if err != nil {
		log.Error("壳运行出错", "err", err)
		if smoke {
			return fatal(true, "壳运行出错："+err.Error())
		}
		return 1
	}
	return code
}

// assets 是打包进来的界面；没有构建产物时（直接 go run、忘了构建前端）给一页说明。
func assets(log *slog.Logger) fs.FS {
	dist, err := fs.Sub(frontend, "frontend/dist")
	if err == nil {
		if _, err = fs.Stat(dist, "index.html"); err == nil {
			return dist
		}
	}
	log.Error("界面没有打包进来：client/frontend/dist/index.html 不存在，先运行 wails3 build")
	return fstest.MapFS{"index.html": {Data: []byte(`<!doctype html><meta charset="utf-8"><title>Doclite</title>` +
		`<p style="font:14px sans-serif;margin:2em">界面没有打包进这个客户端（client/frontend/dist 是空的）。请用 wails3 build 重新构建。</p>`)}}
}

// newLoggers 返回壳的日志和给 Wails 用的日志（同一个文件；Wails 的默认只记警告以上，debug 时全记）。
func newLoggers(file io.Writer) (*slog.Logger, *slog.Logger) {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("DOCLITE_LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	var w io.Writer = file
	if os.Getenv("DOCLITE_LOG_STDERR") == "1" {
		w = io.MultiWriter(file, os.Stderr)
	}
	wailsLevel := max(level, slog.LevelWarn)
	if level == slog.LevelDebug {
		wailsLevel = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
	wailsLog := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: wailsLevel})).With("from", "wails")
	return log, wailsLog
}

// fatal：启动就失败。自检时也按约定输出一行 JSON（退出码 1），正常启动时写到标准错误。
func fatal(smoke bool, msg string) int {
	if smoke {
		line, _ := json.Marshal(map[string]any{"outcome": "failed", "error": msg, "steps": []any{}, "client_version": version})
		fmt.Println(string(line))
	} else {
		fmt.Fprintln(os.Stderr, msg)
	}
	return 1
}
