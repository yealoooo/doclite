// Command devshell 是截图脚本（web/scripts/shots）用的开发服务器，不打包进客户端。一个端口上同时提供：
//
//   - 界面：web/dist（没有扩展名的路径回退到 index.html）。用 `http://wails.localhost:<端口>` 打开时页面认为自己在壳里
//     （web/src/lib/client.ts 的 isShell，Windows 上 WebView2 的写法；Chrome 把 *.localhost 解析到本机），这时和壳一样
//     给页面加 CSP（internal/webcsp）；用 127.0.0.1 打开就是普通的浏览器页面，不加。
//   - /api/：反向代理到 server（页面里模拟的 api_fetch、截图脚本在页面之外调接口都走这里）。batch、snapshots 的 200
//     响应交给本机图片缓存记来源，和壳的会话代理一样（登录、退出不清空记录：几个浏览器上下文共用这一份缓存）。
//   - /shell/img/：壳的取图协议、本机缓存和切块（internal/imagecache、internal/tiles，和打包的壳同一份代码）。
//   - /__devshell/：给页面里的 Wails 桥模拟（web/scripts/lib/wails-mock.mjs）用真实实现的命令：
//     POST prepare_tiles {fileId, snapshotId}、GET cache_stats、POST cache_clear {projectId}。
//
// Wails 的桥本身（/wails/runtime 的命令、事件、流）由截图脚本在页面里模拟。
//
//	cd client && go run ./cmd/devshell -server http://127.0.0.1:9080 -listen 127.0.0.1:24173 -cache <临时目录>
//	cd web && npm run screenshots -- --spec scripts/shots/m1-shell.mjs --base http://127.0.0.1:24173
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"

	"doclite/client/internal/imagecache"
	"doclite/client/internal/webcsp"
)

// shellHost 是"壳模式"页面的主机名（Windows 上 WebView2 里页面的地址 http://wails.localhost）。
const shellHost = "wails.localhost"

// maxObserved 是记来源时最多读多大的响应（batch 最多 500 项，远小于它）。
const maxObserved = 32 << 20

func main() {
	listen := flag.String("listen", "127.0.0.1:24173", "监听地址")
	server := flag.String("server", "http://127.0.0.1:9080", "Doclite server 的地址（/api/ 转给它）")
	web := flag.String("web", "../web/dist", "界面的构建产物（web/dist）")
	cacheDir := flag.String("cache", "", "本机图片缓存的目录（必填，用临时目录）")
	csp := flag.Bool("csp", true, "壳模式的页面加 CSP（和壳一样）")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if *cacheDir == "" {
		log.Error("要用 -cache 指定缓存目录")
		os.Exit(2)
	}
	target, err := url.Parse(strings.TrimRight(*server, "/"))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		log.Error("-server 不是 http(s) 地址", "server", *server)
		os.Exit(2)
	}
	serverURL := target.Scheme + "://" + target.Host
	if _, err := os.Stat(path.Join(*web, "index.html")); err != nil {
		log.Error("没有界面的构建产物，先 cd web && npm run build", "web", *web)
		os.Exit(2)
	}

	images := imagecache.New(imagecache.Config{Dir: *cacheDir, Log: log, Server: func() string { return serverURL }})
	defer images.Close()

	mux := http.NewServeMux()
	mux.Handle(imagecache.Prefix, images)
	mux.Handle("/api/", apiProxy(target, serverURL, images, log))
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
	mux.Handle("/", spa(os.DirFS(*web), *csp))

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Error("监听失败", "addr", *listen, "err", err)
		os.Exit(1)
	}
	log.Info("devshell 已启动", "listen", *listen, "server", serverURL, "web", *web, "cache", *cacheDir,
		"shell_url", "http://"+shellHost+":"+portOf(*listen))
	if err := http.Serve(ln, mux); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("服务出错", "err", err)
		os.Exit(1)
	}
}

// apiProxy 把 /api/ 转给 server；batch、snapshots 的 200 响应交给缓存记来源。SSE（text/event-stream）逐块转发。
func apiProxy(target *url.URL, serverURL string, images *imagecache.Service, log *slog.Logger) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = target.Host
		},
		ModifyResponse: func(res *http.Response) error {
			req := res.Request
			if res.StatusCode != http.StatusOK || !observed(req.Method, req.URL.Path) {
				return nil
			}
			body, err := io.ReadAll(io.LimitReader(res.Body, maxObserved))
			res.Body.Close()
			if err != nil {
				return err
			}
			images.Observe(serverURL, req.Method, req.URL.Path, res.StatusCode, body)
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

// spa 托管界面：有这个文件就给文件，没有扩展名的路径回退到 index.html。壳模式的页面加 CSP。
func spa(dist fs.FS, csp bool) http.Handler {
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if csp && hostname(r.Host) == shellHost {
			w.Header().Set("Content-Security-Policy", webcsp.Policy)
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "." && path.Ext(name) == "" {
			if _, err := fs.Stat(dist, name); err != nil {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		// index.html 不缓存（重新构建后刷新就是新的）。
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Cache-Control", "no-store")
		}
		files.ServeHTTP(w, r)
	})
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
