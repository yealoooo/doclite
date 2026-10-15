//go:build darwin

package shell

/*
#cgo LDFLAGS: -framework Foundation -framework WebKit

int dl_installNavigationGuard(void);
*/
import "C"

import (
	"log/slog"
	"sync/atomic"
)

var navLog atomic.Pointer[slog.Logger]

//export docliteNavigationBlocked
func docliteNavigationBlocked(url *C.char, mainFrame C.int) {
	if l := navLog.Load(); l != nil {
		l.Warn("导航保护：拦下了跳到外部地址的导航", "url", C.GoString(url), "main_frame", mainFrame != 0)
	}
}

// installNavigationGuard 装上导航保护（页面不能跳到外部地址），返回是否装上了。
func installNavigationGuard(log *slog.Logger) bool {
	navLog.Store(log)
	switch C.dl_installNavigationGuard() {
	case 0:
		log.Info("导航保护已装上")
		return true
	case 1:
		log.Error("导航保护没装上：找不到 Wails 的窗口代理类 WebviewWindowDelegate")
	case 2:
		log.Error("导航保护没装上：WebviewWindowDelegate 已经有 decidePolicyForNavigationAction（Wails 升级后要重新处理）")
	}
	return false
}
