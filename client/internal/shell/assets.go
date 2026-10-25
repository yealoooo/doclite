package shell

// 资产服务器（WebView 里页面的全部请求都到这里，页面地址是 macOS 的 wails://localhost、Windows 的
// http://wails.localhost）。界面随客户端打包（M7-6）：web 的产物包按 client/WEB_VERSION 指定的版本嵌进可执行文件
// （Taskfile 的 frontend 任务、cmd/webartifact）。按路径分给：
//
//	/wails/       Wails 自己的运行时、桥（命令调用）、流、事件（Wails 在我们的处理器之前处理；没处理的回 404）。
//	              桥的入口 /wails/runtime 先过白名单（runtimeguard.go）
//	/shell/img/   取图、本机图片缓存、切块（internal/imagecache）
//	/shell/       客户端自带的页面（internal/shellpages：连接服务器、连不上、需要升级）
//	/api/         回 404：页面的接口请求一律走桥（api_fetch、api_stream），由会话代理带上 Cookie 和客户端头
//	其余          嵌入的 web 产物（没有扩展名的路径回退到 index.html）
//
// 开发构建（不带 production 标签）设了 FRONTEND_DEVSERVER_URL 时，"其余"由 Wails 转给 vite 的开发服务器（热更新）；
// 正式构建只用嵌入的产物（Wails 的正式构建不看这个环境变量）。
// 页面和桥同源，CSP 用 'self'（internal/webcsp，每个响应都加）。最外层的中间件（assetGuard）另外拦下主机不对、
// 来源（Origin、Sec-Fetch-Site）不是资产服务器自己的请求。macOS 上导航保护装不上时失败就关闭：只开 /shell/。

import (
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"doclite/client/internal/imagecache"
	"doclite/client/internal/shellpages"
	"doclite/client/internal/webcsp"
)

// assetScheme、assetHost 是 WebView 里页面的协议和主机（Wails 的 assetserver：macOS 自定义协议 wails://localhost，
// Windows 上 WebView2 拦截 http://wails.localhost）。开发构建接 vite 时 Wails 会在主机后面带上 vite 的端口，所以只比主机名。
func assetScheme() string {
	if runtime.GOOS == "darwin" {
		return "wails"
	}
	return "http"
}

func assetHost() string {
	if runtime.GOOS == "darwin" {
		return "localhost"
	}
	return "wails.localhost"
}

// devServerURL 是开发构建里 Wails 转给的 vite 开发服务器（FRONTEND_DEVSERVER_URL）；正式构建总是空的。
func devServerURL() string {
	if !devBuild {
		return ""
	}
	return os.Getenv("FRONTEND_DEVSERVER_URL")
}

// assetHandler 是资产服务器的处理器（Wails 处理完 /wails/ 下它自己的地址之后才到这里）。
func (s *Shell) assetHandler() http.Handler {
	pages := spaHandler(s.cfg.Assets)
	policy := webcsp.Policy
	if devServerURL() != "" {
		policy = webcsp.DevPolicy
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, imagecache.Prefix) {
			s.images.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", policy)
		h.Set("X-Content-Type-Options", "nosniff")
		switch {
		case p == "/shell" || strings.HasPrefix(p, shellpages.Prefix):
			shellpages.ServeHTTP(w, r)
		case p == "/wails" || strings.HasPrefix(p, "/wails/"):
			http.NotFound(w, r)
		case p == "/api" || strings.HasPrefix(p, "/api/"):
			h.Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found","message":"壳里的接口请求要经桥（api_fetch、api_stream）"}`))
		case s.navBlocked:
			// 导航保护没装上：不开界面（失败就关闭），只留客户端自带的页面说明原因。
			if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
				shellpages.ServeView(w, r, shellpages.ViewBlocked, navBlockedReason)
			} else {
				http.Error(w, "导航保护没装上，不开界面", http.StatusForbidden)
			}
		default:
			pages.ServeHTTP(w, r)
		}
	})
}

// navBlockedReason 是导航保护装不上时客户端页面上的说明。
const navBlockedReason = "客户端的安全保护（导航保护）没能装上，为了安全不打开界面。请重新安装客户端；还是这样请联系系统管理员，" +
	"并把客户端日志发给他。"

// spaHandler 给出嵌入的界面：有这个文件就给文件；没有扩展名的路径（前端路由，例如刷新时停在 /files）回退到
// index.html。开发构建接 vite 时 Wails 的文件服务器直接转给 vite，vite 自己做回退，这里不插手。
func spaHandler(dist fs.FS) http.Handler {
	files := application.AssetFileServerFS(dist)
	if devServerURL() != "" {
		return files
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "." && path.Ext(name) == "" {
			if _, err := fs.Stat(dist, name); err != nil {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}

// assetGuard 是资产服务器最外层的中间件（包括 Wails 的 /wails/ 地址）：
//   - 主机必须是资产服务器自己的（macOS 上 Wails 不查 wails:// 的主机，wails://别的主机 也会到这里）；
//   - 带了 Origin 的必须是资产服务器自己的协议和主机（端口不管：开发构建接 vite 时带 vite 的端口）；
//   - 带了 Sec-Fetch-Site 的不能是 cross-site、same-site。
//
// 导航保护让页面离不开资产服务器（navguard_*），这里是第二道：万一有别处的页面在这个 WebView 里，它也调不到桥、
// 拿不到页面和图片。实测 macOS 的 WebKit 对自定义协议的 fetch（包括桥的 POST /wails/runtime）不带 Origin，
// 只有模块脚本等 CORS 方式的加载带 Origin: wails://localhost，所以在 macOS 上主要靠导航保护和 CSP（frame-src 'none'）；
// Windows 的 WebView2（Chromium）对跨源请求总带 Origin、Sec-Fetch-Site，这一道在那里管用（M8 实测）。
func assetGuard(log *slog.Logger) func(http.Handler) http.Handler {
	scheme, host := assetScheme(), assetHost()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reject := ""
			if h := hostOnly(r.Host); h != "" && h != host {
				reject = "主机不对：" + r.Host
			} else if o := r.Header.Get("Origin"); o != "" && !sameAssetOrigin(o, scheme, host) {
				reject = "来源不对：" + o
			} else if sfs := r.Header.Get("Sec-Fetch-Site"); sfs == "cross-site" || sfs == "same-site" {
				reject = "跨站请求：" + sfs
			}
			if reject != "" {
				log.Warn("资产服务器：拦下了不是本客户端页面发来的请求", "path", r.URL.Path, "method", r.Method, "why", reject)
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func sameAssetOrigin(origin, scheme, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Scheme == scheme && u.User == nil && u.Path == "" && hostOnly(u.Host) == host
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(hostport)
}
