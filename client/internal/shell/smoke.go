package shell

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// SmokeOptions 是 `--smoke` 的参数（main.go 从环境变量读）：
//
//	DOCLITE_SMOKE_SERVER        服务器地址（必填）
//	DOCLITE_SMOKE_USERNAME      账号（要至少能进一个项目，sse 一步要订阅项目的实时通道）
//	DOCLITE_SMOKE_PASSWORD
//	DOCLITE_SMOKE_TIMEOUT_SECS  整个自检的时限，默认 120
//	DOCLITE_SMOKE_PAGES=1       另外逐个打开账号菜单里的页面（pages 一步）
type SmokeOptions struct {
	Server   string
	Username string
	Password string
	Timeout  time.Duration
	Pages    bool
}

// 自检的退出码（requirements 第 6 节"自检"）。
const (
	smokeExitOK          = 0
	smokeExitFailed      = 1
	smokeExitUnreachable = 2
	smokeExitUpgrade     = 3
)

// smokeRun 是一次自检：页面（web/src/lib/smoke.ts）跑各步，最后 smoke_report 交回结果，壳把它作为一行 JSON
// 写到标准输出和日志，按 outcome 退出。壳另外做一次"模拟关窗"（窗口只是缩到托盘才算通过），tray 一步核对。
type smokeRun struct {
	s    *Shell
	opts *SmokeOptions

	mu         sync.Mutex
	done       bool
	code       int
	closeCheck *CloseCheck
}

func newSmokeRun(s *Shell, opts *SmokeOptions) *smokeRun {
	return &smokeRun{s: s, opts: opts, code: smokeExitFailed}
}

// SmokeConfig 是 smoke_config 的结果（web/src/lib/shell.ts 的 SmokeConfig）。
type SmokeConfig struct {
	ServerURL  string `json:"server_url"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	ImageCache struct {
		Mode    string  `json:"mode"`
		Project *string `json:"project"`
		Folder  *string `json:"folder"`
		Count   int     `json:"count"`
	} `json:"image_cache"`
	Tiles struct {
		Project *string  `json:"project"`
		Folder  *string  `json:"folder"`
		Files   []string `json:"files"`
	} `json:"tiles"`
	// Rescan 是重扫一步的参数（M6-2，rescan.go）。
	Rescan SmokeRescan `json:"rescan"`
	// Capabilities 告诉页面这个壳有哪些功能，没有的步骤跳过（M6-2 做上传队列、M6-3 做取图协议后改成 true）。
	Capabilities SmokeCapabilities `json:"capabilities"`
	// Pages：逐个打开账号菜单里的页面（DOCLITE_SMOKE_PAGES=1）。
	Pages bool `json:"pages"`
}

// SmokeCapabilities：images 取图协议、本机缓存和切块（M6-3）；upload_queue 上传队列、热文件夹、重扫（M6-2）；
// csp 壳的 CSP（页面不能直连服务器，direct_blocked、csp 两步；和取图协议一起做）；nav_guard 导航保护。
type SmokeCapabilities struct {
	Images      bool `json:"images"`
	UploadQueue bool `json:"upload_queue"`
	CSP         bool `json:"csp"`
	// NavGuard：导航保护装上了（nav_guard 一步）。
	NavGuard bool `json:"nav_guard"`
}

func (r *smokeRun) config() SmokeConfig {
	var c SmokeConfig
	c.ServerURL = r.s.proxy.Server()
	c.Username = r.opts.Username
	c.Password = r.opts.Password
	c.ImageCache.Mode = "cold"
	c.ImageCache.Count = 8
	c.Tiles.Files = []string{}
	c.Pages = r.opts.Pages
	c.Capabilities.NavGuard = r.s.navGuard
	// M6-2：上传队列、热文件夹、重扫件。
	c.Capabilities.UploadQueue = true
	c.Rescan = smokeRescanConfig()
	return c
}

// start：应用起来后开始计时，并在 1.5 秒后模拟一次关窗。
func (r *smokeRun) start() {
	time.AfterFunc(r.opts.Timeout, func() {
		r.finish(map[string]any{
			"outcome": "failed",
			"error":   fmt.Sprintf("自检超时（%d 秒），页面没有交回结果", int(r.opts.Timeout/time.Second)),
			"steps":   []any{},
		}, smokeExitFailed)
	})
	time.AfterFunc(1500*time.Millisecond, r.runCloseCheck)
}

// runCloseCheck 模拟用户关掉主窗口：窗口应该只是隐藏（缩到托盘），壳不退出、窗口还在；之后把窗口放回去
// （自检的页面要一直运行，见 prepareSmokeWindow）。
func (r *smokeRun) runCloseCheck() {
	w := r.s.window
	w.Close()
	time.Sleep(800 * time.Millisecond)
	res := &CloseCheck{OK: true}
	if _, ok := r.s.app.Window.GetByID(w.ID()); !ok {
		msg := "关窗后主窗口被销毁了，没有缩到托盘"
		res = &CloseCheck{OK: false, Error: &msg}
	} else if w.IsVisible() {
		msg := "关窗后主窗口还显示着"
		res = &CloseCheck{OK: false, Error: &msg}
	}
	r.mu.Lock()
	r.closeCheck = res
	r.mu.Unlock()
	r.s.log.Info("自检：模拟关窗", "ok", res.OK)
	if res.OK {
		r.s.prepareSmokeWindow()
	}
}

func (r *smokeRun) closeCheckResult() *CloseCheck {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeCheck
}

// report：页面交回结果。
func (r *smokeRun) report(raw json.RawMessage) {
	var rep map[string]any
	if err := json.Unmarshal(raw, &rep); err != nil || rep == nil {
		rep = map[string]any{"outcome": "failed", "error": "页面交回的结果不是 JSON 对象"}
	}
	code := smokeExitFailed
	switch rep["outcome"] {
	case "ok":
		code = smokeExitOK
	case "unreachable":
		code = smokeExitUnreachable
	case "upgrade_required":
		code = smokeExitUpgrade
	}
	r.finish(rep, code)
}

// finish 输出一行 JSON 并退出（只做一次）。
func (r *smokeRun) finish(rep map[string]any, code int) {
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return
	}
	r.done = true
	r.code = code
	r.mu.Unlock()

	rep["client_version"] = r.s.cfg.Version
	rep["data_dir"] = r.s.cfg.DataDir
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(rep)
	os.Stdout.Write(buf.Bytes())
	r.s.log.Info("自检结束", "exit_code", code, "result", json.RawMessage(bytes.TrimSpace(buf.Bytes())))

	// 让 smoke_report 先返回给页面，再退出（退出码在 PostShutdown 里交给 os.Exit，见 shell.go）；
	// 退出卡住时直接结束进程。
	go func() {
		time.Sleep(200 * time.Millisecond)
		r.s.Quit()
		time.Sleep(5 * time.Second)
		os.Exit(code)
	}()
}

func (r *smokeRun) exitCode() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.code
}
