//go:build !darwin

package shell

// prepareSmokeWindow：Windows 上的自检窗口在 M7 处理（WebView2 隐藏时的节流规则不同，要实测）。
func (s *Shell) prepareSmokeWindow() {
	s.window.Show()
}
