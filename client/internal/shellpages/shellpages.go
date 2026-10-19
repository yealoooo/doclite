// Package shellpages 是客户端自带的几页（M7-6）：启动关口、首次连接、连不上、需要升级、不能打开界面。简单的 HTML、
// CSS、JS（pages/），embed 进客户端，不依赖 web；挂在资产服务器的 /shell/ 下，和界面同源、用同一个桥。页面逻辑见
// pages/shell.js，约定见 client/BRIDGE.md。打包的壳（internal/shell）和截图脚本用的开发服务器（cmd/devshell）用同一份。
package shellpages

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"strconv"
)

//go:embed pages
var files embed.FS

// Prefix 是这几页的地址前缀（/shell/img/ 是取图，由 internal/imagecache 处理，不到这里）。
const Prefix = "/shell/"

// 显示哪一页（ServeView 的 view）。
const (
	// ViewGate 是启动时的关口：页面自己按设置和探测结果决定显示什么。
	ViewGate = ""
	// ViewBlocked：客户端不能打开界面（导航保护没装上，reason 写原因），没有可点的按钮。
	ViewBlocked = "blocked"
)

var index = template.Must(template.ParseFS(files, "pages/index.html"))

var static = map[string]string{
	"/shell/shell.css": "text/css; charset=utf-8",
	"/shell/shell.js":  "text/javascript; charset=utf-8",
}

// ServeHTTP 处理 /shell/ 下的页面和它的样式、脚本。
func ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "只能 GET", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/shell", "/shell/", "/shell/index.html":
		ServeView(w, r, ViewGate, "")
		return
	}
	ctype, ok := static[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := files.ReadFile("pages/" + r.URL.Path[len(Prefix):])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	write(w, r, ctype, data)
}

// ServeView 在当前地址上给出客户端的页面，并指定显示哪一页（导航保护没装上时，资产服务器在界面的地址上用它）。
func ServeView(w http.ResponseWriter, r *http.Request, view, reason string) {
	var buf bytes.Buffer
	if err := index.Execute(&buf, struct{ View, Reason string }{view, reason}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	write(w, r, "text/html; charset=utf-8", buf.Bytes())
}

func write(w http.ResponseWriter, r *http.Request, ctype string, data []byte) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
