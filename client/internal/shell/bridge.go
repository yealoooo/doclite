package shell

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"doclite/client/internal/appdata"
	"doclite/client/internal/proxy"
)

// Bridge 是绑定给页面的服务，只有一个方法 Invoke。页面（web/src/lib/wails.ts）用
// `Call.ByName("doclite/client/internal/shell.Bridge.Invoke", 命令, 参数)` 调用。
type Bridge struct {
	s *Shell
}

// Invoke 按命令名分发。args 是 JSON 对象（键和上一版 Tauri 的 invoke 参数一样，例如 `{url}`、`{req}`）。
// 失败时返回 *proxy.Error，页面收到的是 `{kind, message}`。
func (b *Bridge) Invoke(ctx context.Context, cmd string, args json.RawMessage) (any, error) {
	b.s.mu.Lock()
	fn, ok := b.s.commands[cmd]
	b.s.mu.Unlock()
	if !ok {
		return nil, proxy.InvalidRequest("未知的命令：" + cmd)
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	res, err := fn(ctx, args)
	if err != nil {
		var pe *proxy.Error
		if !errors.As(err, &pe) {
			b.s.log.Error("命令出错", "cmd", cmd, "err", err)
			return nil, proxy.Internal(err.Error())
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

// unsupported：后续任务才做的命令先登记成"还不支持"，页面照常提示，不会因为"未知的命令"出错。
var unsupported = map[string]string{}

func (s *Shell) registerCommands() {
	s.commands = map[string]command{}
	for name, feature := range unsupported {
		msg := "这个版本的客户端还不支持" + feature + "，请先用浏览器操作"
		s.commands[name] = func(context.Context, json.RawMessage) (any, error) {
			return nil, &proxy.Error{Kind: proxy.KindUnsupported, Message: msg}
		}
	}

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

	handle(s, "set_server", func(_ context.Context, a struct {
		URL string `json:"url"`
	}) (any, error) {
		server, err := proxy.NormalizeServerURL(a.URL)
		if err != nil {
			return nil, err
		}
		if err := appdata.SaveSettings(s.cfg.DataDir, appdata.Settings{ServerURL: server}); err != nil {
			return nil, proxy.Internal("保存设置失败：" + err.Error())
		}
		if s.proxy.SetServer(server) {
			s.log.Info("换了服务器，会话已清空", "server", server)
			s.setSessionState(func() { s.authRequired, s.upgradeRequired = false, false })
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
		u, err := url.Parse(strings.TrimSpace(a.URL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, proxy.InvalidRequest("只能用系统浏览器打开 http 或 https 地址")
		}
		if err := s.app.Browser.OpenURL(u.String()); err != nil {
			return nil, proxy.Internal("打开系统浏览器失败：" + err.Error())
		}
		return nil, nil
	})

	handle(s, "tray_status", func(context.Context, noArgs) (any, error) { return s.tray.status(), nil })

	handle(s, "smoke_config", func(context.Context, noArgs) (any, error) {
		if s.smoke == nil {
			return nil, proxy.InvalidRequest("不是自检模式")
		}
		return s.smoke.config(), nil
	})

	handle(s, "smoke_report", func(_ context.Context, a struct {
		Report json.RawMessage `json:"report"`
	}) (any, error) {
		if s.smoke == nil {
			return nil, proxy.InvalidRequest("不是自检模式")
		}
		s.smoke.report(a.Report)
		return nil, nil
	})

	// M6-2：上传队列、热文件夹、重扫件（upload.go、hotfolder.go、rescan.go）。
	s.initUploads()
	s.registerImageCommands()
}

// ShellInfo 是 shell_info 的结果（web/src/lib/shell.ts 的 ShellInfo）。
type ShellInfo struct {
	Version   string  `json:"version"`
	ServerURL *string `json:"server_url"`
	DataDir   string  `json:"data_dir"`
	Platform  string  `json:"platform"`
	Smoke     bool    `json:"smoke"`
}

func (s *Shell) shellInfo() ShellInfo {
	info := ShellInfo{Version: s.cfg.Version, DataDir: s.cfg.DataDir, Platform: platformName(), Smoke: s.smoke != nil}
	if server := s.proxy.Server(); server != "" {
		info.ServerURL = &server
	}
	return info
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
