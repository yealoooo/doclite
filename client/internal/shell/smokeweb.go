package shell

// 自检第 3 段用的小服务（只在 --smoke 里起，见 smoke.go）：监听 127.0.0.1 的随机端口，所有请求原样转给
// DOCLITE_SMOKE_SERVER（/api 和实时推送照转）。它是另一个地址，换地址一步换到它；记下经它的 /api 请求有几个。

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type smokeWeb struct {
	url   string
	srv   *http.Server
	proxy *httputil.ReverseProxy
	api   atomic.Int64
}

func startSmokeWeb(upstream string, log *slog.Logger) (*smokeWeb, error) {
	up, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	w := &smokeWeb{url: "http://" + ln.Addr().String()}
	w.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(up)
			r.Out.Host = up.Host
		},
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	w.srv = &http.Server{Handler: w, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = w.srv.Serve(ln) }()
	log.Info("自检：起了转发到服务器的小服务（换地址用）", "url", w.url, "upstream", upstream)
	return w, nil
}

func (w *smokeWeb) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = w.srv.Shutdown(ctx)
}

func (w *smokeWeb) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.api.Add(1)
	}
	w.proxy.ServeHTTP(rw, r)
}
