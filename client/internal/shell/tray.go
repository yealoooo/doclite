package shell

import (
	"net/url"
	"runtime"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// tray 是托盘图标（requirements 第 6 节"托盘和窗口"）。
//
// 提示文字第一行是状态："Doclite · 还没有连接服务器 / 客户端需要升级 / 未登录 / 登录已失效，需要重新登录 /
// 已登录 <服务器>"；第二行是上传队列的统计（队列打不开时写"上传队列不可用"）。M6-2 的上传队列和热文件夹见
// upload_tray.go（状态行加热文件夹的状态，统计行换成各状态的数量）。
// 菜单：两行状态（不能点）、打开 Doclite、设置…（M7-11）、暂停 / 继续热文件夹、退出。关窗缩到托盘，只有"退出"真正退出。
// macOS 上点一下图标弹菜单（菜单栏图标的习惯）；Windows 上左键打开窗口、右键弹菜单。
type tray struct {
	s    *Shell
	tray *application.SystemTray

	mu         sync.Mutex
	created    bool
	statusItem *application.MenuItem
	countsItem *application.MenuItem
	statusLine string
	countsLine string
	// toggleItem 是"暂停 / 继续热文件夹"（M6-2，upload_tray.go），toggleShown 是它现在显示的状态。
	toggleItem  *application.MenuItem
	toggleShown string
}

// countsUnavailable 是上传队列打不开时统计那一行的文字；--smoke 的 tray 一步认这几个字。
const countsUnavailable = "上传队列不可用"

func newTray(s *Shell) *tray {
	t := &tray{s: s}
	t.statusLine, t.countsLine = t.lines()

	menu := s.app.NewMenu()
	t.statusItem = menu.Add(t.statusLine).SetEnabled(false)
	t.countsItem = menu.Add(t.countsLine).SetEnabled(false)
	menu.AddSeparator()
	menu.Add("打开 Doclite").OnClick(func(*application.Context) { s.showWindow(true) })
	// M7-11：打开窗口并跳到设置页（settings.go）。
	menu.Add("设置…").OnClick(func(*application.Context) { s.openSettings() })
	t.addToggleItem(menu)
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) {
		s.log.Info("托盘菜单：退出")
		s.Quit()
	})

	t.tray = s.app.SystemTray.New()
	if runtime.GOOS == "darwin" && len(s.cfg.TrayTemplate) > 0 {
		t.tray.SetTemplateIcon(s.cfg.TrayTemplate)
	} else {
		t.tray.SetIcon(s.cfg.TrayIcon)
	}
	t.tray.SetTooltip(t.tooltip())
	t.tray.SetMenu(menu)
	if runtime.GOOS != "darwin" {
		t.tray.OnClick(func() { s.showWindow(true) })
	}
	return t
}

// started：应用跑起来后 Wails 才真正建图标（SystemTray.Run）。
func (t *tray) started() {
	t.mu.Lock()
	t.created = true
	t.mu.Unlock()
	t.s.log.Info("托盘已建好")
}

func (t *tray) isCreated() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.created
}

// lines 按壳当前的状态算出两行文字。
func (t *tray) lines() (status, counts string) {
	s := t.s
	s.mu.Lock()
	authRequired, upgradeRequired := s.authRequired, s.upgradeRequired
	s.mu.Unlock()
	server := s.proxy.Server()
	switch {
	case server == "":
		status = "还没有连接服务器"
	case upgradeRequired:
		status = "客户端需要升级"
	case !s.proxy.HasSession():
		// 没登录过、退出了、会话 Cookie 到期了。
		status = "未登录"
	case authRequired:
		// 还有会话 Cookie，但 server 不认了（被停用、在别处撤销）。
		status = "登录已失效，需要重新登录"
	default:
		status = "已登录 " + hostOf(server)
	}
	counts = countsUnavailable
	// M6-2：热文件夹的状态、上传队列的统计（upload_tray.go）。
	status, counts = s.uploadTrayLines(status, counts)
	return "Doclite · " + status, counts
}

func (t *tray) tooltip() string { return t.statusLine + "\n" + t.countsLine }

// refresh 按最新状态更新提示和菜单（有变化才改）。
func (t *tray) refresh() {
	t.refreshToggle()
	status, counts := t.lines()
	t.mu.Lock()
	if status == t.statusLine && counts == t.countsLine {
		t.mu.Unlock()
		return
	}
	t.statusLine, t.countsLine = status, counts
	tip := t.tooltip()
	t.mu.Unlock()
	t.statusItem.SetLabel(status)
	t.countsItem.SetLabel(counts)
	t.tray.SetTooltip(tip)
}

// TrayStatus 是 tray_status 的结果（web/src/lib/scan-queue.ts 的 TrayStatus）。
type TrayStatus struct {
	Created     bool        `json:"created"`
	Error       *string     `json:"error"`
	CloseToTray bool        `json:"close_to_tray"`
	Tooltip     *string     `json:"tooltip"`
	StatusLine  *string     `json:"status_line"`
	CountsLine  *string     `json:"counts_line"`
	Toggle      string      `json:"toggle"`
	CloseCheck  *CloseCheck `json:"close_check"`
}

// CloseCheck 是 --smoke 时模拟关窗的结果。
type CloseCheck struct {
	OK    bool    `json:"ok"`
	Error *string `json:"error"`
}

func (t *tray) status() TrayStatus {
	t.refresh()
	t.mu.Lock()
	defer t.mu.Unlock()
	tip, status, counts := t.tooltip(), t.statusLine, t.countsLine
	st := TrayStatus{
		Created:     t.created,
		CloseToTray: t.created,
		Tooltip:     &tip,
		StatusLine:  &status,
		CountsLine:  &counts,
		Toggle:      t.toggleState(),
	}
	if !t.created {
		msg := "托盘还没建好"
		st.Error = &msg
	}
	if t.s.smoke != nil {
		st.CloseCheck = t.s.smoke.closeCheckResult()
	}
	return st
}

func hostOf(server string) string {
	if u, err := url.Parse(server); err == nil && u.Host != "" {
		return u.Host
	}
	return server
}
