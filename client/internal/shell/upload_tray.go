package shell

// M6-2：托盘上的上传队列和热文件夹（requirements 第 6 节"托盘和窗口"）：状态行加上热文件夹的状态（运行中、已暂停、
// 未设置；队列暂停时改成"已暂停：需要重新登录 / 客户端需要升级"），统计行是各状态的数量；菜单上的
// "暂停热文件夹 / 继续热文件夹"（没设置时灰掉；继续失败时打开主窗口并发 tray:error，页面弹出原因）。

import (
	"fmt"

	"github.com/wailsapp/wails/v3/pkg/application"

	"doclite/client/internal/upload"
)

// uploadTrayLines 按上传队列和热文件夹改托盘的两行文字（tray.lines 调用）。用的是队列最近一次算出的统计，不查库。
func (s *Shell) uploadTrayLines(status, counts string) (string, string) {
	u := s.uploads
	if u == nil || u.engine == nil {
		return status, counts
	}
	st := u.engine.LastStatus()
	switch {
	case st.Paused != nil && st.Paused.Reason == upload.PauseAuth:
		status = "已暂停：需要重新登录"
	case st.Paused != nil:
		status = "已暂停：客户端需要升级"
	default:
		hs := u.hot.Status()
		switch {
		case hs.Running:
			status += " · 热文件夹运行中"
		case hs.Config != nil:
			status += " · 热文件夹已暂停"
		default:
			status += " · 未设置热文件夹"
		}
	}
	c := st.Stats
	counts = fmt.Sprintf("排队 %d · 上传中 %d · 已入库 %d · 重复 %d · 失败 %d", c.Queued, c.Uploading, c.Done, c.Duplicate, c.Failed)
	if c.Waiting > 0 {
		counts = fmt.Sprintf("等待写入 %d · ", c.Waiting) + counts
	}
	return status, counts
}

// addToggleItem 在托盘菜单上加"暂停 / 继续热文件夹"（newTray 调用）。
func (t *tray) addToggleItem(menu *application.Menu) {
	t.toggleItem = menu.Add("继续热文件夹").SetEnabled(false).OnClick(func(*application.Context) {
		go t.s.toggleHotfolder()
	})
	t.refreshToggle()
}

// toggleState 是 tray_status 的 toggle：pause（正在运行，可以暂停）、start（暂停着，可以继续）、unavailable（没设置）。
func (t *tray) toggleState() string {
	u := t.s.uploads
	if u == nil || u.hot == nil {
		return "unavailable"
	}
	hs := u.hot.Status()
	switch {
	case hs.Running:
		return "pause"
	case hs.Config != nil:
		return "start"
	}
	return "unavailable"
}

// refreshToggle 按热文件夹的状态改菜单项（有变化才改）。
func (t *tray) refreshToggle() {
	if t.toggleItem == nil {
		return
	}
	state := t.toggleState()
	t.mu.Lock()
	if state == t.toggleShown {
		t.mu.Unlock()
		return
	}
	t.toggleShown = state
	t.mu.Unlock()
	label := "继续热文件夹"
	if state == "pause" {
		label = "暂停热文件夹"
	}
	t.toggleItem.SetLabel(label)
	t.toggleItem.SetEnabled(state != "unavailable")
}

// toggleHotfolder：托盘菜单的"暂停 / 继续热文件夹"（就是 /scan 页的暂停和开始）。继续失败（监控目录不在了、
// 服务器换了……）时打开主窗口，并发 tray:error（登录后的外框在任何页面弹出原因）。
func (s *Shell) toggleHotfolder() {
	u := s.uploads
	if u == nil || u.hot == nil {
		return
	}
	if u.hot.Status().Running {
		if _, err := u.hot.Pause(); err != nil {
			s.log.Error("托盘：暂停热文件夹失败", "err", err)
		}
		return
	}
	if _, err := u.hot.Start(); err != nil {
		s.log.Warn("托盘：继续热文件夹失败", "err", err)
		s.showWindow(true)
		if s.app != nil {
			s.app.Event.Emit("tray:error", map[string]string{"message": "继续热文件夹失败：" + err.Error()})
		}
	}
}
