package shell

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// SmokeOptions 是 `--smoke` 的参数（main.go 从环境变量读）：
//
//	DOCLITE_SMOKE_SERVER        服务器地址（必填；web 服务的入口或 server 本身，客户端只经它访问 /api）
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

// smokeRun 是一次自检。分三段（M7-6），结果合成一行 JSON 写到标准输出和日志，按 outcome 退出：
//
//  1. 客户端自带的页面（壳用 ExecJS 操作 /shell/，页面每换一个状态就调 smoke_shell_view 报告）：
//     shell_page     还没配置地址时出现连接页；填一个连不上的地址，提示连不上、不保存；
//     shell_unreachable  配置成连不上的地址：出现"连不上"页，重试仍是连不上，点"修改地址"回到连接页；
//     shell_connect  填 DOCLITE_SMOKE_SERVER 连接：进到嵌入的界面（界面调 smoke_config）。连不上、要升级时到此结束；
//  2. 界面里的自检（web/src/lib/smoke.ts，stage=main）：登录、取图、切块、托盘、各页面……交回 smoke_report；
//     壳回 {next: "edit_server"}，界面随即像设置页里的"修改服务器地址"那样跳到 /shell/?edit；
//  3. 换地址（壳起一个转发到 DOCLITE_SMOKE_SERVER 的小服务 smokeWeb，它是另一个地址）：
//     shell_edit     界面跳到客户端的修改地址页；
//     change_server  换成 smokeWeb 的地址：会话清空（/api/me 是 401），之后的接口请求走新地址。
//
// 界面每次进来都调 smoke_config：第一次是 stage=main（完整的自检），之后是 stage=page（只报告会话和版本）。
// 壳另外在启动 1.5 秒后做一次"模拟关窗"（窗口只是缩到托盘才算通过），tray 一步核对。
type smokeRun struct {
	s    *Shell
	opts *SmokeOptions

	mu         sync.Mutex
	done       bool
	code       int
	closeCheck *CloseCheck
	// flow 是主流程用的本机文件夹和 open_external 收到的地址（M6-4，smokeflow.go）。
	flow *smokeFlowState

	// changed 在页面报告新状态、交回结果、换了地址时关闭并换新的（等待的一方据此醒来）。
	changed chan struct{}
	// views 是客户端页面报告过几次状态，view 是最近一次。
	views int
	view  ShellView
	// stage：""（还没进网页）、"main"（网页在跑完整的自检）、"page"（之后进来的网页只报告）。
	stage string
	// before、after 是壳自己跑的步骤（第 1、3 段），main 是网页交回的结果（第 2 段）。
	before, after []smokeStep
	main          map[string]any
	viewsAtMain   int
	pages         []map[string]any
	lastReset     string
	web           *smokeWeb
}

// ShellView 是客户端页面报告的状态（smoke_shell_view，见 internal/shellpages/pages/shell.js）。
type ShellView struct {
	// Page、Seq：哪一次加载的页面、它的第几次报告（同一页面先发后到的旧报告丢掉）。
	Page      string  `json:"page"`
	Seq       int     `json:"seq"`
	View      string  `json:"view"`
	Editing   bool    `json:"editing"`
	ServerURL *string `json:"server_url"`
	Error     *string `json:"error"`
	Attempt   int     `json:"attempt"`
}

type smokeStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	MS     int64  `json:"ms"`
	Detail any    `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// smokeStop：提前结束自检，并指定 outcome。
type smokeStop struct {
	outcome string
	msg     string
}

func (e *smokeStop) Error() string { return e.msg }

func newSmokeRun(s *Shell, opts *SmokeOptions) *smokeRun {
	return &smokeRun{s: s, opts: opts, code: smokeExitFailed, changed: make(chan struct{})}
}

// SmokeConfig 是 smoke_config 的结果（web/src/lib/shell.ts 的 SmokeConfig）。
type SmokeConfig struct {
	// Stage：main（完整的自检）或 page（只报告看到的页面标记和会话，见 smokeRun）。
	Stage      string `json:"stage"`
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
	// Flow 是主流程几步的参数（M6-4，smokeflow.go）。
	Flow SmokeFlow `json:"flow"`
	// Capabilities 告诉页面这个壳有哪些功能，没有的步骤跳过。
	Capabilities SmokeCapabilities `json:"capabilities"`
	// Pages：逐个打开账号菜单里的页面（DOCLITE_SMOKE_PAGES=1）。
	Pages bool `json:"pages"`
}

// SmokeCapabilities：images 取图协议、本机缓存和切块（M6-3）；upload_queue 上传队列、热文件夹、重扫（M6-2）；
// csp 壳的 CSP（页面不能直连服务器，direct_blocked、csp 两步）；nav_guard 导航保护。
type SmokeCapabilities struct {
	Images      bool `json:"images"`
	UploadQueue bool `json:"upload_queue"`
	CSP         bool `json:"csp"`
	// NavGuard：导航保护装上了（nav_guard 一步）。
	NavGuard bool `json:"nav_guard"`
}

func (r *smokeRun) config() SmokeConfig {
	var c SmokeConfig
	r.mu.Lock()
	if r.stage == "" {
		r.stage = "main"
		c.Stage = "main"
	} else {
		r.stage = "page"
		c.Stage = "page"
	}
	r.notifyLocked()
	r.mu.Unlock()
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
	c.Flow = smokeFlowConfig()
	r.s.imageSmokeConfig(&c)
	return c
}

// start：应用起来后开始计时，1.5 秒后模拟一次关窗，同时开始按段往下走。
func (r *smokeRun) start() {
	time.AfterFunc(r.opts.Timeout, func() {
		r.finishWith("failed", fmt.Sprintf("自检超时（%d 秒）", int(r.opts.Timeout/time.Second)))
	})
	time.AfterFunc(1500*time.Millisecond, r.runCloseCheck)
	go r.drive()
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

// ---------- 页面报告 ----------

func (r *smokeRun) notifyLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// shellView：客户端页面报告了新状态。
func (r *smokeRun) shellView(v ShellView) {
	r.mu.Lock()
	if v.Page != "" && v.Page == r.view.Page && v.Seq <= r.view.Seq {
		r.mu.Unlock()
		return
	}
	r.views++
	r.view = v
	r.notifyLocked()
	r.mu.Unlock()
	r.s.log.Info("自检：客户端页面", "view", v.View, "editing", v.Editing, "attempt", v.Attempt)
}

// report：网页交回结果。stage=page 的是第 3 段的报告；否则是第 2 段完整的自检，回 {next: "edit_server"}
// 让网页接着跳到客户端的修改地址页。
func (r *smokeRun) report(raw json.RawMessage) map[string]any {
	var rep map[string]any
	if err := json.Unmarshal(raw, &rep); err != nil || rep == nil {
		rep = map[string]any{"outcome": "failed", "error": "页面交回的结果不是 JSON 对象"}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if rep["stage"] == "page" {
		r.pages = append(r.pages, rep)
		r.notifyLocked()
		return map[string]any{}
	}
	if r.main != nil {
		return map[string]any{}
	}
	r.main = rep
	r.viewsAtMain = r.views
	r.notifyLocked()
	return map[string]any{"next": "edit_server"}
}

// serverChanged：自检里换了地址（applyServer）。
func (r *smokeRun) serverChanged(server string) {
	r.mu.Lock()
	r.lastReset = server
	r.notifyLocked()
	r.mu.Unlock()
}

// wait 等到 cond 成立（持锁调用 cond），超时返回错误。
func (r *smokeRun) wait(timeout time.Duration, cond func() bool) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		r.mu.Lock()
		ok := cond()
		ch := r.changed
		r.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ch:
		case <-deadline.C:
			return fmt.Errorf("等了 %d 秒", int(timeout/time.Second))
		}
	}
}

// waitView 等客户端页面在第 after 次报告之后报告出符合条件的状态，返回那个状态。
func (r *smokeRun) waitView(after int, timeout time.Duration, what string, ok func(ShellView) bool) (ShellView, error) {
	var got ShellView
	err := r.wait(timeout, func() bool {
		if r.views > after && ok(r.view) {
			got = r.view
			return true
		}
		return false
	})
	if err != nil {
		r.mu.Lock()
		last := r.view
		r.mu.Unlock()
		return got, fmt.Errorf("客户端页面没有%s（%v；最近一次是 %s）", what, err, describeView(last))
	}
	return got, nil
}

func describeView(v ShellView) string {
	s := v.View
	if s == "" {
		return "还没有报告"
	}
	if v.Editing {
		s += "（修改地址）"
	}
	if v.Error != nil {
		s += "：" + *v.Error
	}
	return s
}

func (r *smokeRun) viewCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.views
}

func (r *smokeRun) pageCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pages)
}

// waitPage 等网页在第 after 个之后交回 stage=page 的报告。
func (r *smokeRun) waitPage(after int, timeout time.Duration) (map[string]any, error) {
	var got map[string]any
	err := r.wait(timeout, func() bool {
		if len(r.pages) > after {
			got = r.pages[len(r.pages)-1]
			return true
		}
		return false
	})
	if err != nil {
		return nil, fmt.Errorf("网页没有加载起来（%v）", err)
	}
	return got, nil
}

// exec 在主窗口的页面里执行一段脚本（操作客户端页面的 window.docliteShell）。
func (r *smokeRun) exec(js string) { r.s.window.ExecJS(js) }

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ---------- 按段往下走 ----------

// step 跑壳自己的一步，记进 list。返回这一步的错误。
func (r *smokeRun) step(list *[]smokeStep, name string, fn func() (any, error)) error {
	started := time.Now()
	detail, err := fn()
	st := smokeStep{Name: name, OK: err == nil, MS: time.Since(started).Milliseconds(), Detail: detail}
	if err != nil {
		st.Error = err.Error()
	}
	r.mu.Lock()
	*list = append(*list, st)
	r.mu.Unlock()
	r.s.log.Info("自检：壳的一步", "step", name, "ok", err == nil, "err", st.Error)
	return err
}

func (r *smokeRun) drive() {
	server := r.opts.Server
	stop := func(err error) {
		var ss *smokeStop
		if errors.As(err, &ss) {
			r.finishWith(ss.outcome, ss.msg)
		} else {
			r.finishWith("failed", err.Error())
		}
	}

	// 第 1 段：客户端自带的页面。
	if err := r.step(&r.before, "shell_page", r.checkShellPage); err != nil {
		stop(err)
		return
	}
	if err := r.step(&r.before, "shell_unreachable", r.checkShellUnreachable); err != nil {
		stop(err)
		return
	}
	if err := r.step(&r.before, "shell_connect", func() (any, error) { return r.connect(server) }); err != nil {
		stop(err)
		return
	}

	// 第 2 段：网页里的自检（时限由 start 的总计时器管）。
	if err := r.wait(r.opts.Timeout, func() bool { return r.main != nil }); err != nil {
		return
	}
	r.mu.Lock()
	outcome := r.main["outcome"]
	r.mu.Unlock()
	if outcome == "unreachable" || outcome == "upgrade_required" {
		r.finishWith("", "")
		return
	}

	// 第 3 段：换地址。
	defer func() {
		if r.web != nil {
			r.web.close()
		}
	}()
	steps := []struct {
		name string
		fn   func() (any, error)
	}{
		{"shell_edit", r.checkShellEdit},
		{"change_server", r.checkChangeServer},
	}
	for _, st := range steps {
		if err := r.step(&r.after, st.name, st.fn); err != nil {
			break
		}
	}
	r.finishWith("", "")
}

// checkShellPage：还没配置地址时出现客户端的连接页；填一个连不上的地址，提示连不上、不保存。
func (r *smokeRun) checkShellPage() (any, error) {
	if _, err := r.waitView(0, 20*time.Second, "出现连接页", func(v ShellView) bool {
		return v.View == "connect" && !v.Editing
	}); err != nil {
		return nil, err
	}
	bad, err := closedLocalURL()
	if err != nil {
		return nil, err
	}
	n := r.viewCount()
	r.exec("window.docliteShell.connect(" + jsString(bad) + ")")
	v, err := r.waitView(n, 20*time.Second, "提示连不上", func(v ShellView) bool {
		return v.View == "connect" && v.Error != nil
	})
	if err != nil {
		return nil, err
	}
	if got := r.s.proxy.Server(); got != "" {
		return nil, fmt.Errorf("连不上的地址也被保存了：%s", got)
	}
	return map[string]any{"view": "connect", "rejected": bad, "error": *v.Error}, nil
}

// checkShellUnreachable：配置成连不上的地址后打开关口：出现"连不上"页；重试还是连不上；"修改地址"回到连接页。
func (r *smokeRun) checkShellUnreachable() (any, error) {
	bad, err := closedLocalURL()
	if err != nil {
		return nil, err
	}
	if err := r.s.applyServer(bad); err != nil {
		return nil, err
	}
	n := r.viewCount()
	r.s.window.SetURL("/shell/")
	v, err := r.waitView(n, 20*time.Second, "出现连不上页", func(v ShellView) bool { return v.View == "unreachable" })
	if err != nil {
		return nil, err
	}
	if v.ServerURL == nil || *v.ServerURL != bad {
		return nil, fmt.Errorf("连不上页显示的地址不对：%v", v.ServerURL)
	}
	n = r.viewCount()
	r.exec("window.docliteShell.retry()")
	again, err := r.waitView(n, 20*time.Second, "重试后仍显示连不上", func(x ShellView) bool {
		return x.View == "unreachable" && x.Attempt > v.Attempt
	})
	if err != nil {
		return nil, err
	}
	n = r.viewCount()
	r.exec("window.docliteShell.edit()")
	if _, err := r.waitView(n, 10*time.Second, "回到修改地址页", func(x ShellView) bool {
		return x.View == "connect" && x.Editing
	}); err != nil {
		return nil, err
	}
	reason := ""
	if again.Error != nil {
		reason = *again.Error
	}
	return map[string]any{"server": bad, "reason": reason, "attempts": again.Attempt}, nil
}

// connect 在客户端的连接页上填地址连接，等网页加载起来（网页调 smoke_config）。连不上、要升级时提前结束自检。
func (r *smokeRun) connect(server string) (any, error) {
	n := r.viewCount()
	r.mu.Lock()
	stage0 := r.stage
	r.mu.Unlock()
	r.exec("window.docliteShell.connect(" + jsString(server) + ")")
	err := r.wait(45*time.Second, func() bool {
		if r.stage != stage0 && r.stage != "" {
			return true
		}
		if r.views <= n {
			return false
		}
		v := r.view
		return (v.View == "connect" && v.Error != nil) || v.View == "unreachable" || v.View == "upgrade"
	})
	r.mu.Lock()
	stage, v := r.stage, r.view
	views := r.views
	r.mu.Unlock()
	if stage != stage0 && stage != "" {
		return map[string]any{"server": server, "stage": stage}, nil
	}
	if err == nil && views > n {
		switch v.View {
		case "upgrade":
			return nil, &smokeStop{"upgrade_required", "客户端版本低于服务器要求的最低版本（客户端的升级页）"}
		case "connect", "unreachable":
			reason := "连不上 " + server
			if v.Error != nil {
				reason = *v.Error
			}
			return nil, &smokeStop{"unreachable", reason}
		}
	}
	return nil, fmt.Errorf("连接 %s 后网页没有加载起来（%v；客户端页面最近是 %s）", server, err, describeView(v))
}

// checkShellEdit：网页交回结果后跳到客户端的修改地址页（/shell/?edit）。
func (r *smokeRun) checkShellEdit() (any, error) {
	r.mu.Lock()
	n := r.viewsAtMain
	r.mu.Unlock()
	v, err := r.waitView(n, 20*time.Second, "出现修改地址页", func(v ShellView) bool { return v.View == "connect" && v.Editing })
	if err != nil {
		return nil, err
	}
	return map[string]any{"view": v.View, "editing": v.Editing}, nil
}

// checkChangeServer：换成 smokeWeb 的地址（另一个 origin）：会话清空，之后的接口请求走新地址。
func (r *smokeRun) checkChangeServer() (any, error) {
	web, err := startSmokeWeb(r.opts.Server, r.s.log)
	if err != nil {
		return nil, err
	}
	r.web = web
	pages := r.pageCount()
	r.exec("window.docliteShell.connect(" + jsString(web.url) + ")")
	page, err := r.waitPage(pages, 45*time.Second)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	reset := r.lastReset
	r.mu.Unlock()
	detail := map[string]any{"server": web.url, "me_status": page["me_status"], "api_requests": web.api.Load(),
		"web_version": page["web_version"], "path": page["path"]}
	switch {
	case reset != web.url || r.s.proxy.Server() != web.url:
		return detail, fmt.Errorf("没有换到新地址（现在是 %s）", r.s.proxy.Server())
	case r.s.proxy.HasSession():
		return detail, errors.New("换地址后会话 Cookie 还在")
	case fmt.Sprint(page["me_status"]) != "401":
		return detail, fmt.Errorf("换地址后会话应该清空（/api/me 应为 401，实际 %v）", page["me_status"])
	case web.api.Load() == 0:
		return detail, errors.New("换地址后接口请求没有走新地址")
	}
	return detail, nil
}

// closedLocalURL 返回一个本机没人监听的地址（连不上）。
func closedLocalURL() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	ln.Close()
	return "http://" + addr, nil
}

// ---------- 结束 ----------

// finishWith 合起三段的步骤输出一行 JSON 并退出（只做一次）。outcome 为空时按网页交回的结果和壳的步骤定；
// 不为空时（超时、提前结束）用它，msg 是原因。
func (r *smokeRun) finishWith(outcome, msg string) {
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return
	}
	r.done = true
	rep := map[string]any{}
	var steps []any
	for _, st := range r.before {
		steps = append(steps, st)
	}
	var webErr string
	if r.main != nil {
		for k, v := range r.main {
			rep[k] = v
		}
		if list, ok := r.main["steps"].([]any); ok {
			steps = append(steps, list...)
		}
		webErr, _ = r.main["error"].(string)
	}
	for _, st := range r.after {
		steps = append(steps, st)
	}
	if steps == nil {
		steps = []any{}
	}
	rep["steps"] = steps

	if outcome == "" {
		outcome = "failed"
		if r.main != nil {
			if o, ok := r.main["outcome"].(string); ok {
				outcome = o
			}
		}
		msg = webErr
		var failed []string
		for _, st := range append(append([]smokeStep{}, r.before...), r.after...) {
			if !st.OK {
				failed = append(failed, st.Name+"："+st.Error)
			}
		}
		if len(failed) > 0 {
			if outcome == "ok" {
				outcome, msg = "failed", ""
			}
			for _, f := range failed {
				if msg != "" {
					msg += "；"
				}
				msg += f
			}
		}
	} else if r.main == nil && msg == "" {
		msg = "网页没有交回结果"
	}
	rep["outcome"] = outcome
	if msg != "" {
		rep["error"] = msg
	} else {
		delete(rep, "error")
	}
	code := smokeExitFailed
	switch outcome {
	case "ok":
		code = smokeExitOK
	case "unreachable":
		code = smokeExitUnreachable
	case "upgrade_required":
		code = smokeExitUpgrade
	}
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
