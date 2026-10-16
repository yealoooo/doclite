// Command devshell 是截图脚本（web/scripts/shots）用的开发服务器，不打包进客户端。它在一个端口上做打包的壳的
// 资产服务器做的事，只是界面从 -web 指定的 web 入口（vite）现取（截图测的是 web 的源码，不是嵌入的产物包），
// 桥（/wails/runtime 的命令、事件、流）由截图脚本在页面里模拟（web/scripts/lib/wails-mock.mjs）。用法：
//
//	cd web && DOCLITE_SERVER=http://127.0.0.1:9080 npm run dev -- --port 5173 --strictPort
//	cd client && go run ./cmd/devshell -web http://127.0.0.1:5173 -listen 127.0.0.1:24173 -cache <临时目录>
//	cd web && npm run screenshots -- --spec scripts/shots/m1-shell.mjs --base http://127.0.0.1:24173
//
// 地址：
//   - 界面和 /api/：转给 -web（vite 的 dev 或 preview，/api 由它转给 server）。用 `http://wails.localhost:<端口>` 打开时
//     页面认为自己在壳里（web/src/lib/client.ts 的 isShell，Windows 上 WebView2 的写法；Chrome 把 *.localhost 解析到
//     本机），这时和壳一样给页面加 CSP（internal/webcsp）；用 127.0.0.1 打开就是普通的浏览器页面，不加。/api/ 里
//     batch、snapshots 的 200 响应交给本机图片缓存记来源，和壳的会话代理一样（登录、退出不清空记录：几个浏览器上下文共用）；
//   - /shell/：客户端自带的页面（internal/shellpages：连接服务器、连不上、需要升级）；
//   - /wails/runtime.js：Wails 的前端运行时（和壳里的同一个版本）；/wails/ 下的其他地址回 404（命令调用等由页面里的
//     模拟拦下）；
//   - /shell/img/：壳的取图协议、本机缓存和切块（internal/imagecache、internal/tiles，和壳同一份代码）；
//   - /__devshell/：给页面里的桥模拟用真实实现的命令：POST prepare_tiles {fileId, snapshotId}、GET cache_stats、
//     POST cache_clear {projectId}。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"testing/fstest"

	"github.com/wailsapp/wails/v3/pkg/application"

	"doclite/client/internal/imagecache"
	"doclite/client/internal/proxy"
	"doclite/client/internal/shellpages"
	"doclite/client/internal/webcsp"
)

// shellHost 是"壳模式"页面的主机名（Windows 上 WebView2 里页面的地址 http://wails.localhost）。
const shellHost = "wails.localhost"

// maxObserved 是记来源时最多读多大的响应（batch 最多 500 项，远小于它）。
const maxObserved = 32 << 20

func main() {
	listen := flag.String("listen", "127.0.0.1:24173", "监听地址")
	web := flag.String("web", "http://127.0.0.1:5173", "web 的入口（vite 的地址；界面和 /api/ 都转给它）")
	cacheDir := flag.String("cache", "", "本机图片缓存的目录（必填，用临时目录）")
	csp := flag.Bool("csp", true, "壳模式的页面加 CSP（和壳一样）")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if *cacheDir == "" {
		log.Error("要用 -cache 指定缓存目录")
		os.Exit(2)
	}
	origin, err := proxy.NormalizeServerURL(*web)
	if err != nil {
		log.Error("-web 不是 http(s) 地址", "web", *web, "err", err)
		os.Exit(2)
	}
	target, _ := url.Parse(origin)

	images := imagecache.New(imagecache.Config{Dir: *cacheDir, Log: log, Server: func() string { return origin }})
	defer images.Close()
	// Wails 的前端运行时（打包的壳由 Wails 自己在 /wails/runtime.js 提供）。
	runtimeJS := application.BundledAssetFileServer(fstest.MapFS{})

	mux := http.NewServeMux()
	mux.Handle(imagecache.Prefix, images)
	upstream := webProxy(target, origin, images, log)
	mux.Handle("/api/", upstream)
	mux.Handle("GET /wails/runtime.js", runtimeJS)
	// 其余 /wails/ 下的地址（命令调用、流、custom.js……）在打包的壳里由 Wails 处理，这里由页面里的模拟拦下；
	// 漏到这里的回 404，不转给 web 入口（和壳一样）。
	mux.Handle("/wails/", http.NotFoundHandler())
	mux.HandleFunc("POST /__devshell/prepare_tiles", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			FileID     string `json:"fileId"`
			SnapshotID string `json:"snapshotId"`
		}
		if json.NewDecoder(r.Body).Decode(&a) != nil {
			http.Error(w, "参数不对", http.StatusBadRequest)
			return
		}
		res, ok := images.PrepareTiles(a.FileID, a.SnapshotID)
		if !ok {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"kind": "invalid_request",
				"message": "这张图不在客户端的来源记录里（图片信息要先经 batch 或 snapshots 接口取得）"})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("GET /__devshell/cache_stats", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, images.Stats())
	})
	mux.HandleFunc("POST /__devshell/cache_clear", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			ProjectID *string `json:"projectId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&a)
		pid := ""
		if a.ProjectID != nil {
			pid = strings.ToLower(*a.ProjectID)
		}
		writeJSON(w, http.StatusOK, images.Clear(pid))
	})
	shellPages := http.HandlerFunc(shellpages.ServeHTTP)
	mux.Handle("/", withCSP(*csp, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/shell" || strings.HasPrefix(r.URL.Path, shellpages.Prefix) {
			shellPages.ServeHTTP(w, r)
			return
		}
		upstream.ServeHTTP(w, r)
	})))

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Error("监听失败", "addr", *listen, "err", err)
		os.Exit(1)
	}
	log.Info("devshell 已启动", "listen", *listen, "web", origin, "cache", *cacheDir,
		"shell_url", "http://"+shellHost+":"+portOf(*listen))
	if err := http.Serve(ln, mux); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("服务出错", "err", err)
		os.Exit(1)
	}
}

// withCSP：壳模式的地址（wails.localhost）上的页面和壳一样加 CSP。
func withCSP(on bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if on && hostname(r.Host) == shellHost {
			w.Header().Set("Content-Security-Policy", webcsp.Policy)
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		next.ServeHTTP(w, r)
	})
}

// webProxy 把界面和 /api/ 转给 web 入口；batch、snapshots 的 200 响应交给缓存记来源。SSE（text/event-stream）逐块转发。
// 上游的 CSP 不给页面（壳模式的页面用壳的 CSP，见 withCSP）。
func webProxy(target *url.URL, origin string, images *imagecache.Service, log *slog.Logger) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
		},
		ModifyResponse: func(res *http.Response) error {
			res.Header.Del("Content-Security-Policy")
			req := res.Request
			if res.StatusCode != http.StatusOK || !observed(req.Method, req.URL.Path) {
				return nil
			}
			body, err := io.ReadAll(io.LimitReader(res.Body, maxObserved))
			res.Body.Close()
			if err != nil {
				return err
			}
			images.Observe(origin, req.Method, req.URL.Path, res.StatusCode, body)
			res.Body = io.NopCloser(bytes.NewReader(body))
			return nil
		},
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// observed：要记来源的响应（和壳一样：POST /api/files/batch、GET /api/files/<fid>/snapshots）。
func observed(method, p string) bool {
	if method == http.MethodPost && p == "/api/files/batch" {
		return true
	}
	rest, ok := strings.CutPrefix(p, "/api/files/")
	if !ok || method != http.MethodGet {
		return false
	}
	fid, tail, ok := strings.Cut(rest, "/")
	return ok && tail == "snapshots" && imagecache.IsUUID(fid)
}

func hostname(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(hostport)
}

func portOf(addr string) string {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return p
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
