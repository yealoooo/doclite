//go:build darwin

package shell

/*
#cgo LDFLAGS: -framework Cocoa -framework WebKit

int dl_prepareSmokeWindow(void *nsWindow);
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

// prepareSmokeWindow 让 --smoke 的页面一直在跑、又不打扰用户：
//
// 窗口完全隐藏（orderOut）时，WebKit 认为页面不可见，几秒后就把网页进程挂起（定时器、轮询都停了，自检卡住）。
// 所以自检时把窗口放进窗口列表（orderBack，不激活应用、不成为焦点窗口），但完全透明、不接收鼠标；再关掉 WKWebView
// 的"被遮挡就当不可见"（私有接口 _setWindowOcclusionDetectionEnabled:，被别的窗口挡住也照常运行）。
// 只用于自检和 DOCLITE_BACKGROUND=1（开发、自动测试）；正常使用时窗口就是普通的窗口。
func (s *Shell) prepareSmokeWindow() {
	ptr := s.window.NativeWindow()
	if ptr == nil {
		s.log.Warn("拿不到主窗口的原生句柄，窗口没有设为透明")
		return
	}
	var rc C.int
	application.InvokeSync(func() { rc = C.dl_prepareSmokeWindow(ptr) })
	switch rc {
	case 0:
		s.log.Info("窗口设为透明、放在最后面、不检测遮挡（自检或 DOCLITE_BACKGROUND）")
	case 1:
		s.log.Warn("主窗口里找不到 WKWebView，没有关掉遮挡检测")
	case 2:
		s.log.Warn("这个版本的 WebKit 不能关掉遮挡检测，窗口被挡住时页面可能变慢")
	}
}
