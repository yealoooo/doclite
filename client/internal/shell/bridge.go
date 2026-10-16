package shell

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"doclite/client/internal/appdata"
	"doclite/client/internal/proxy"
)

// Bridge 是绑定给页面的服务，只有一个方法 Invoke。页面用壳的运行时（/wails/runtime.js）
// `Call.ByName("doclite/client/internal/shell.Bridge.Invoke", 命令, 参数)` 调用（约定见 client/BRIDGE.md）。
type Bridge struct {
	s *Shell
}

// Invoke 按命令名分发。args 是 JSON 对象（键和上一版 Tauri 的 invoke 参数一样，例如 `{url}`、`{req}`）。
// 失败时返回 *proxy.Error，页面收到的是 `{kind, message}`。这个客户端没有的命令（页面比客户端新）返回
// `unsupported`，页面据此提示升级客户端。
func (b *Bridge) Invoke(ctx context.Context, cmd string, args json.RawMessage) (any, error) {
	b.s.mu.Lock()
	fn, ok := b.s.commands[cmd]
	b.s.mu.Unlock()
	if !ok {
		return nil, &proxy.Error{Kind: proxy.KindUnsupported, Message: "这个版本的客户端没有命令 " + cmd + "，请升级客户端"}
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	res, err := fn(ctx, args)
	if err != nil {
		var pe *proxy.Error
		if !errors.As(err, &pe) {
			// 原始错误可能带本机路径：只写日志，页面看到一句概括。
			b.s.log.Error("命令出错", "cmd", cmd, "err", err)
			return nil, proxy.Internal("客户端内部出错（详情见客户端日志）")
		}
		if pe.Detail != "" {
			b.s.log.Warn("命令出错", "cmd", cmd, "message", pe.Message, "detail", pe.Detail)
		}
		return nil, pe
	}
	return res, nil
}

// command 是一个命令的实现。
type command func(ctx context.Context, args json.RawMessage) (any, error)

// handle 登记一个命令（参数解成 A）。以后的任务（M6-2 上传队列、M6-3 缓存）在自己的文件里调用它登记命令。
func handle[A any](s *Shell, name string, fn func(ctx context.Context, args A) (any, error)) {
	s.commands[name] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a A
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, proxy.InvalidRequest("命令 " + name + " 的参数格式不对")
		}
		return fn(ctx, a)
	}
}

// noArgs 是不带参数的命令的参数类型。
type noArgs struct{}

// capabilities 是这个客户端的能力（shell_info 的 capabilities），页面按它决定显示哪些本机功能。每一项对应哪些命令、
// 事件、地址见 client/BRIDGE.md；加命令时要么归进已有的一项，要么加一项新的（页面没看到这一项就隐藏或提示升级）。
var capabilities = []string{"api", "server", "pages", "external", "tray", "upload", "rescan", "images", "settings"}

func (s *Shell) registerCommands() {
	s.commands = map[string]command{}

	handle(s, "shell_info", func(context.Context, noArgs) (any, error) { return s.shellInfo(), nil })

	handle(s, "probe_server", func(ctx context.Context, a struct {
		URL string `json:"url"`
	}) (any, error) {
		server, err := proxy.NormalizeServerURL(a.URL)
		if err != nil {
			return nil, err
		}
		return s.proxy.Probe(ctx, server), nil
	})

	handle(s, "check_server", func(ctx context.Context, _ noArgs) (any, error) {
		server := s.proxy.Server()
		if server == "" {
			return nil, proxy.NotConfigured()
		}
		res := s.proxy.Probe(ctx, server)
		if res.Reachable {
			s.setSessionState(func() { s.upgradeRequired = res.UpgradeRequired })
		}
		return res, nil
	})

	handle(s, "set_server", func(ctx context.Context, a struct {
		URL string `json:"url"`
	}) (any, error) {
		server, err := proxy.NormalizeServerURL(a.URL)
		if err != nil {
			return nil, err
		}
		if err := s.changeServer(ctx, server); err != nil {
			return nil, err
		}
		return server, nil
	})

	handle(s, "api_fetch", func(ctx context.Context, a struct {
		Req *proxy.Request `json:"req"`
	}) (any, error) {
		if a.Req == nil {
			return nil, proxy.InvalidRequest("缺少 req")
		}
		return s.proxy.Fetch(ctx, a.Req)
	})

	handle(s, "open_external", func(_ context.Context, a struct {
		URL string `json:"url"`
	}) (any, error) {
		u, err := checkExternalURL(a.URL)
		if err != nil {
			return nil, err
		}
		if s.smoke != nil {
			// 自检不打开系统浏览器（不抢焦点），只记下地址，smoke_external_check 再核对（smokeflow.go）。
			s.smoke.recordExternal(u.String())
			s.log.Info("自检：open_external 只记下地址", "host", u.Host)
			return nil, nil
		}
		if err := s.app.Browser.OpenURL(u.String()); err != nil {
			return nil, proxy.InternalDetail("打开系统浏览器失败", err)
		}
		return nil, nil
	})

	handle(s, "tray_status", func(context.Context, noArgs) (any, error) { return s.tray.status(), nil })

	// M6-2：上传队列、热文件夹、重扫件（upload.go、hotfolder.go、rescan.go）。
	s.initUploads()
	s.registerImageCommands()
	// M7-11：设置页（settings.go）。
	s.registerSettingsCommands()

	// 自检用的命令（能力 smoke）只在 --smoke 进程里登记：正常启动时页面调它们得到 unsupported。
	if s.smoke == nil {
		return
	}
	handle(s, "smoke_config", func(context.Context, noArgs) (any, error) { return s.smoke.config(), nil })
	handle(s, "smoke_report", func(_ context.Context, a struct {
		Report json.RawMessage `json:"report"`
	}) (any, error) {
		return s.smoke.report(a.Report), nil
	})
	handle(s, "smoke_shell_view", func(_ context.Context, v ShellView) (any, error) {
		s.smoke.shellView(v)
		return nil, nil
	})
	// M6-4：自检的主流程（smokeflow.go）。
	s.registerSmokeFlowCommands()
}

// ShellInfo 是 shell_info 的结果（client/BRIDGE.md）。
type ShellInfo struct {
	// Version 是客户端的版本（client/VERSION），也就是 X-Client-Version。
	Version string `json:"version"`
	// WebVersion 是嵌入的界面的版本（web 产物包的清单，client/WEB_VERSION）；没有嵌入界面时为 null。
	WebVersion *string `json:"web_version"`
	// ServerURL 是已配置的服务器地址（web 服务的入口，也可以是 server 本身）；还没配置时为 null。
	ServerURL *string `json:"server_url"`
	DataDir   string  `json:"data_dir"`
	Platform  string  `json:"platform"`
	// Arch 是处理器架构（amd64、arm64），OSVersion 是系统的名字和版本（Wails 给的，例如
	// "Windows 10 Pro 2009 (Build: 19045)"、"MacOS 15.6"；取不到时为 null）。M7-11 加的，设置页的"关于"和诊断信息用。
	Arch      string  `json:"arch"`
	OSVersion *string `json:"os_version"`
	Smoke     bool    `json:"smoke"`
	// Capabilities 是这个客户端的能力（见 capabilities）。
	Capabilities []string `json:"capabilities"`
}

func (s *Shell) shellInfo() ShellInfo {
	info := ShellInfo{Version: s.cfg.Version, DataDir: s.cfg.DataDir, Platform: platformName(), Arch: runtime.GOARCH,
		OSVersion: s.osVersionText(), Smoke: s.smoke != nil}
	if v := s.cfg.WebVersion; v != "" {
		info.WebVersion = &v
	}
	if server := s.proxy.Server(); server != "" {
		info.ServerURL = &server
	}
	info.Capabilities = append([]string(nil), capabilities...)
	if s.smoke != nil {
		info.Capabilities = append(info.Capabilities, "smoke")
	}
	return info
}

// osVersionText 是 shell_info 的 os_version：第一次用到时问 Wails（macOS 跑一次 sysctl，Windows 读注册表），之后不变。
// 应用还没起来（测试）时为 null。
func (s *Shell) osVersionText() *string {
	if s.app == nil {
		return nil
	}
	s.osVersionOnce.Do(func() {
		sys := s.app.Env.Info().OSInfo
		if sys == nil {
			return
		}
		text := strings.TrimSpace(sys.Name)
		if v := strings.TrimSpace(sys.Version); v != "" && v != "Unknown" && !strings.Contains(text, v) {
			text = strings.TrimSpace(text + " " + v)
		}
		if text != "" {
			s.osVersion = &text
		}
	})
	return s.osVersion
}

// changeServer 是页面要换服务器地址（set_server）。页面证明不了是用户在操作（页面和客户端自带的页面同源，区分不出
// 是谁调的），所以已经配置过地址、又要改成别的地址时，由壳弹系统确认框（"从 A 改为 B"），用户点了才改。首次配置、
// 地址没变时不弹；--smoke 时跳过（自检自己换地址）。
func (s *Shell) changeServer(ctx context.Context, server string) error {
	current := s.proxy.Server()
	if current != "" && current != server && s.smoke == nil {
		confirm := s.confirmServer
		if confirm == nil {
			confirm = s.confirmServerDialog
		}
		ok, err := confirm(ctx, current, server)
		if err != nil {
			return proxy.InternalDetail("没能弹出确认框", err)
		}
		if !ok {
			s.log.Info("用户取消了修改服务器地址", "from", current, "to", server)
			return proxy.InvalidRequest("已取消，服务器地址没有改")
		}
	}
	return s.applyServer(server)
}

// confirmServerDialog 弹系统确认框问用户要不要把服务器地址从 from 改为 to（默认按钮是"取消"）。
func (s *Shell) confirmServerDialog(ctx context.Context, from, to string) (bool, error) {
	if s.app == nil {
		return false, errors.New("应用还没起来")
	}
	answer := make(chan bool, 1)
	d := s.app.Dialog.Question().SetTitle("修改服务器地址").
		SetMessage("把服务器地址\n从 " + from + "\n改为 " + to + "？\n\n改了之后要重新登录。如果不是你自己在改，请点「取消」。")
	yes := d.AddButton("改为新地址")
	yes.OnClick(func() { answer <- true })
	no := d.AddButton("取消")
	no.OnClick(func() { answer <- false })
	d.SetDefaultButton(no)
	d.SetCancelButton(no)
	if s.window != nil && s.window.IsVisible() && !s.cfg.Background {
		d.AttachToWindow(s.window)
	}
	d.Show()
	select {
	case ok := <-answer:
		return ok, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// applyServer 保存服务器地址并换过去：地址变了时清空会话（Cookie 罐），托盘的会话状态回到初始（图片缓存的来源记录、
// 上传队列按服务器隔离，各自发现换了服务器）。
func (s *Shell) applyServer(server string) error {
	if err := appdata.SaveSettings(s.cfg.DataDir, appdata.Settings{ServerURL: server}); err != nil {
		return proxy.InternalDetail("保存设置失败", err)
	}
	if !s.proxy.SetServer(server) {
		return nil
	}
	s.log.Info("换了服务器，会话已清空", "server", server)
	s.setSessionState(func() { s.authRequired, s.upgradeRequired = false, false })
	if s.smoke != nil {
		s.smoke.serverChanged(server)
	}
	return nil
}

// checkExternalURL：open_external 只打开 http、https 的地址（导出物、原图的预签名下载地址，升级的下载地址），
// 不带用户名密码，不超过 16 KB，没有空白和控制字符。地址由页面给，所以这里只限制种类。
func checkExternalURL(raw string) (*url.URL, error) {
	bad := proxy.InvalidRequest("只能用系统浏览器打开 http 或 https 地址")
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 16<<10 {
		return nil, bad
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return nil, bad
		}
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return nil, bad
	}
	return u, nil
}

// observeResponse：按代理的响应更新托盘的会话状态（401 需要重新登录、登录成功、426 需要升级）。
func (s *Shell) observeResponse(req *proxy.Request, status int, _ []byte) {
	switch {
	case status == http.StatusUpgradeRequired:
		s.setSessionState(func() { s.upgradeRequired = true })
	case req.Path == proxy.LoginPath && req.Method == http.MethodPost && status >= 200 && status < 300:
		s.setSessionState(func() { s.authRequired = false })
	case status == http.StatusUnauthorized && req.Path != proxy.LoginPath:
		s.setSessionState(func() { s.authRequired = true })
	}
}

func (s *Shell) setSessionState(change func()) {
	s.mu.Lock()
	change()
	s.mu.Unlock()
	if s.tray != nil {
		s.tray.refresh()
	}
}
