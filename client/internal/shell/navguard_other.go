//go:build !darwin

package shell

import "log/slog"

// installNavigationGuard：Windows 上的导航保护在 M7 做（Wails 没有公开 WebView2 的 NavigationStarting，
// 要另想办法），这里只记一条日志。
func installNavigationGuard(log *slog.Logger) bool {
	log.Warn("这个平台还没有导航保护")
	return false
}
