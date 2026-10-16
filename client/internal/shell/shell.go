// Package shell 是 Wails 应用本身：主窗口、单实例、托盘、资产服务器（assets.go：嵌入的界面、客户端自带的页面、取图）、
// 桥的白名单（runtimeguard.go）、给页面的命令（bridge.go）、流式请求（stream.go）、`--smoke` 自检（smoke.go）、
// 导航保护（navguard_*.go）。
//
// 页面和壳之间的约定写在 client/BRIDGE.md（web 和 client 之间唯一的约定）：页面调 `invoke(命令, 参数)`，失败时 reject
// `{kind, message}`。Wails 下是一个绑定方法 `Bridge.Invoke(cmd, args)` 按命令名分发。
package shell

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"doclite/client/internal/appdata"
	"doclite/client/internal/imagecache"
	"doclite/client/internal/proxy"
)

// Identifier、ProductName 是用户定的标识（requirements 第 6 节"标识"，不再改）。
const (
	Identifier  = "com.doclite.desktop"
	ProductName = "Doclite"
)

// Config 是启动参数（main.go 准备好）。
type Config struct {
	Version string
	DataDir string
	// Smoke 不为 nil 表示以 --smoke 启动。
	Smoke *SmokeOptions
	// Background 为 true 时不抢焦点、不打扰用户：不激活应用，窗口透明且在最后面（同自检，见 prepareSmokeWindow），
	// 再启动、点托盘的"打开"都不显示窗口。只给开发和自动测试用（DOCLITE_BACKGROUND=1）。
	Background bool
	// Assets 是嵌入的界面（web 产物包里的 dist，见 cmd/webartifact）；WebVersion 是它的版本（产物包的清单），
	// 没有嵌入界面时为空。
	Assets     fs.FS
	WebVersion string
	// AppIcon、TrayIcon、TrayTemplate 是图标（PNG）。
	AppIcon      []byte
	TrayIcon     []byte
	TrayTemplate []byte
	Log          *slog.Logger
	// WailsLog 是给 Wails 自己用的日志（默认只记警告以上：它每个静态文件都记一条）。
	WailsLog *slog.Logger
	Proxy    *proxy.Proxy
}

// Shell 是运行中的壳。
type Shell struct {
	cfg    Config
	log    *slog.Logger
	proxy  *proxy.Proxy
	app    *application.App
	window *application.WebviewWindow
	tray   *tray
	smoke  *smokeRun
	// uploads 是上传队列、热文件夹、重扫件（M6-2，upload.go）。
	uploads *uploadState
	// images 是取图协议、本机图片缓存和切块（M6-3，images.go）。
	images *imagecache.Service

	// navGuard：导航保护装上了（macOS）。navBlocked：这个平台必须有导航保护却没装上，界面不开（assets.go）。
	navGuard   bool
	navBlocked bool
	// confirmServer 问用户要不要换服务器地址（nil 时弹系统确认框，见 bridge.go 的 changeServer；测试里换掉）。
	confirmServer func(ctx context.Context, from, to string) (bool, error)
	// openDir 用系统的文件管理器打开目录（nil 时用 Wails 的，见 settings.go 的 openLocalDir；测试里换掉）。
	openDir func(path string) error
	// osVersion 是系统的名字和版本（shell_info 的 os_version，第一次用到时问 Wails，之后不变）。
	osVersionOnce sync.Once
	osVersion     *string

	// quitting：正在退出（托盘的"退出"、Cmd+Q、自检结束），这时关窗不再缩到托盘。
	quitting atomic.Bool

	mu       sync.Mutex
	commands map[string]command
	// 托盘显示的会话状态（由代理的响应推断）。
	authRequired    bool
	upgradeRequired bool
}

// Run 建应用、窗口和托盘，运行到退出。返回退出码（自检用；正常退出是 0）。
func Run(cfg Config) (int, error) {
	s := &Shell{cfg: cfg, log: cfg.Log, proxy: cfg.Proxy}
	switch {
	case devServerURL() != "":
		s.log.Info("开发构建：界面转给 vite 的开发服务器", "url", devServerURL())
	case os.Getenv("FRONTEND_DEVSERVER_URL") != "":
		s.log.Warn("正式构建不认 FRONTEND_DEVSERVER_URL，只用嵌入的界面")
	}
	removeOldWebCache(cfg.DataDir, s.log)
	if cfg.Smoke != nil {
		s.smoke = newSmokeRun(s, cfg.Smoke)
	}
	s.registerCommands()
	s.proxy.Observe(s.observeResponse)

	s.navGuard = installNavigationGuard(s.log)
	// 导航保护在这个平台上是必需的却没装上：失败就关闭，只开客户端自带的页面（assets.go）。
	s.navBlocked = navGuardRequired && !s.navGuard
	if s.navBlocked {
		s.log.Error("导航保护没装上：不打开界面，只显示客户端自带的说明页")
	}

	opts := application.Options{
		Name:        ProductName,
		Description: "纸质档案数字化",
		Icon:        cfg.AppIcon,
		Logger:      cfg.WailsLog,
		Services:    []application.Service{application.NewService(&Bridge{s: s})},
		Assets: application.AssetOptions{
			// 嵌入的界面、客户端自带的页面、取图（assets.go）；最外层核对主机和来源，再核对桥的调用在不在白名单里
			// （runtimeguard.go）。中间件在 Wails 自己的 /wails/ 处理之前。
			Handler: s.assetHandler(),
			Middleware: func(next http.Handler) http.Handler {
				return assetGuard(s.log)(newRuntimeGuard(s.log).wrap(next))
			},
		},
		Mac: application.MacOptions{
			// 关掉窗口（缩到托盘）后应用继续运行。
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: true,
		},
		ShouldQuit: func() bool {
			// Cmd+Q、系统注销：照常退出。
			s.quitting.Store(true)
			return true
		},
		OnShutdown: func() {
			s.quitting.Store(true)
			s.stopUploads()
			s.log.Info("壳退出")
		},
		PostShutdown: func() {
			// macOS 上退出时 NSApplication 直接 exit(0)，app.Run 不会返回：自检的退出码只能在这里交出去
			// （窗口、托盘图标这时已经清理完）。
			if s.smoke != nil {
				os.Exit(s.smoke.exitCode())
			}
		},
	}
	if cfg.Smoke != nil || cfg.Background {
		// 不出现在 Dock、启动时不激活，不抢用户的焦点。
		opts.Mac.ActivationPolicy = application.ActivationPolicyAccessory
	}
	if cfg.Smoke == nil {
		opts.SingleInstance = &application.SingleInstanceOptions{
			UniqueID:               singleInstanceID(cfg.DataDir),
			OnSecondInstanceLaunch: func(application.SecondInstanceData) { s.secondInstance() },
		}
	}
	s.app = application.New(opts)
	s.app.HandleStream(apiStreamName, s.handleAPIStream)
	s.app.OnShutdown(s.images.Close)

	s.window = s.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     ProductName,
		Width:     1360,
		Height:    860,
		MinWidth:  1024,
		MinHeight: 640,
		// 先到客户端自带的关口页（没配置地址、连不上、要升级时就停在那里），没问题再进界面（shellpages）。
		URL:              "/shell/",
		Hidden:           cfg.Smoke != nil || cfg.Background,
		InitialPosition:  application.WindowCentered,
		BackgroundColour: application.NewRGB(255, 255, 255),
		// 壳不接管文件拖放：页面里的 HTML5 拖放（归类页拖页序）照常工作。往壳里传文件走热文件夹和上传按钮。
		EnableFileDrop: false,
	})
	s.window.RegisterHook(events.Common.WindowClosing, s.onWindowClosing)
	s.app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) {
		// 点 Dock 图标：显示隐藏的窗口。
		s.showWindow(true)
	})

	s.tray = newTray(s)
	s.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		s.tray.started()
		s.log.Info("壳已启动", "version", cfg.Version, "data_dir", cfg.DataDir, "server", s.proxy.Server(),
			"smoke", cfg.Smoke != nil)
		if cfg.Smoke != nil || cfg.Background {
			s.prepareSmokeWindow()
		}
		if s.smoke != nil {
			s.smoke.start()
		}
		s.startUploads()
	})

	if err := s.app.Run(); err != nil {
		return 1, err
	}
	if s.smoke != nil {
		return s.smoke.exitCode(), nil
	}
	return 0, nil
}

// onWindowClosing：关窗缩到托盘（托盘建好了、不是在退出时）；否则照常关闭。托盘没建起来时关窗就退出
// （否则既看不到窗口也退不出去）。
func (s *Shell) onWindowClosing(e *application.WindowEvent) {
	if s.quitting.Load() {
		return
	}
	if !s.tray.isCreated() {
		s.log.Warn("托盘没建起来，关窗即退出")
		go s.Quit()
		return
	}
	e.Cancel()
	s.window.Hide()
	s.log.Debug("关窗：缩到托盘")
}

// showWindow 显示主窗口；focus 时同时激活应用（用户点了"打开"、Dock 图标、再启动一次）。
// DOCLITE_BACKGROUND=1 时什么都不做（窗口保持透明，不打扰用户）。
func (s *Shell) showWindow(focus bool) {
	if s.window == nil || s.cfg.Background {
		return
	}
	s.window.Show()
	if focus {
		s.window.Focus()
	}
}

// secondInstance：再启动一次时聚焦已有的窗口（单实例）。
func (s *Shell) secondInstance() {
	s.log.Info("又启动了一次：显示已有的窗口")
	s.showWindow(true)
}

// Quit 真正退出（托盘的"退出"）。
func (s *Shell) Quit() {
	s.quitting.Store(true)
	s.app.Quit()
}

// singleInstanceID：正式的数据目录用 identifier；DOCLITE_DATA_DIR 指定的目录另算一个，开发时可以和正式的同时开。
func singleInstanceID(dataDir string) string {
	def, err := appdata.Dir(false)
	if err == nil && def == dataDir {
		return Identifier
	}
	var h uint32 = 2166136261
	for i := 0; i < len(dataDir); i++ {
		h ^= uint32(dataDir[i])
		h *= 16777619
	}
	const hex = "0123456789abcdef"
	var b [8]byte
	for i := range b {
		b[i] = hex[(h>>(28-4*i))&0xf]
	}
	return Identifier + "." + string(b[:])
}

// platformName 是 shell_info 的 platform：windows、macos、linux。
func platformName() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}
