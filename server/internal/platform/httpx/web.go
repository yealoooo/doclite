package httpx

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// 托管网页的构建产物（`WEB_DIR`，一般是 web/dist），浏览器直接打开 server 就能用（requirements 1.3）。
//
//   - `/assets/*`：Vite 带哈希的文件，找到时发 `Cache-Control: public, max-age=31536000, immutable`；
//     找不到 404（不回退到 index.html，也不带长缓存，免得把 404 缓存一年）。
//   - 其他路径：找得到文件就返回文件，找不到回退到 index.html（前端路由），都发 `Cache-Control: no-cache`，
//     升级后马上拿到新界面。
//   - 每次请求时才读文件：启动时目录还不存在也照常挂载，之后再 `npm run build` 不用重启。
//   - `/api` 不经过这里（在 app 里分流）。
const (
	assetsCacheControl = "public, max-age=31536000, immutable"
	pageCacheControl   = "no-cache"
)

// WebHandler 托管目录 dir 里的网页。
func WebHandler(dir string) http.Handler {
	fsys := os.DirFS(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "."
		}
		if name == "assets" || strings.HasPrefix(name, "assets/") {
			if isFile(fsys, name) {
				w.Header().Set("Cache-Control", assetsCacheControl)
				http.ServeFileFS(w, r, fsys, name)
				return
			}
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", pageCacheControl)
		if name != "." && name != "index.html" && isFile(fsys, name) {
			http.ServeFileFS(w, r, fsys, name)
			return
		}
		if !isFile(fsys, "index.html") {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r, fsys)
	})
}

func isFile(fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	return err == nil && !st.IsDir()
}

// serveIndex 直接输出 index.html（http.ServeFileFS 会把以 /index.html 结尾的请求重定向，这里不需要）。
func serveIndex(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	f, err := fsys.Open("index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", st.ModTime(), rs)
}
