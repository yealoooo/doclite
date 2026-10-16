package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeServerURL(t *testing.T) {
	ok := map[string]string{
		"192.168.1.10:9080":         "http://192.168.1.10:9080",
		" http://Server.LAN:9080/ ": "http://server.lan:9080",
		"HTTPS://example.com":       "https://example.com",
		"http://host:80":            "http://host",
		"https://host:443/":         "https://host",
		"http://[::1]:9080":         "http://[::1]:9080",
		"localhost":                 "http://localhost",
	}
	for in, want := range ok {
		got, err := NormalizeServerURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeServerURL(%q) = %q, %v，应为 %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "   ", "ftp://host", "http://user:pw@host", "http://host/login", "http://host?x=1",
		"http://host#a", "http://host:99999", "http://host:0", "http://:9080", "http://ho st", "file:///etc/passwd",
		"javascript:alert(1)", "http://host:abc",
	} {
		_, err := NormalizeServerURL(in)
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindInvalidRequest {
			t.Errorf("NormalizeServerURL(%q) 应返回 invalid_request，实际 %v", in, err)
		}
	}
}

func TestBuildURL(t *testing.T) {
	const server = "http://10.0.0.5:9080"
	body := `{"a":1}`
	got, err := BuildURL(server, &Request{
		Method:  "POST",
		Path:    "/api/folder-files:move",
		Query:   [][]string{{"q", "盒 003&x=1"}, {"limit", "50"}},
		Headers: map[string]string{"Accept": "application/json", "Content-Type": "application/json"},
		Body:    &body,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "http://10.0.0.5:9080/api/folder-files:move?q=%E7%9B%92+003%26x%3D1&limit=50"
	if got != want {
		t.Errorf("BuildURL = %q，应为 %q", got, want)
	}

	bad := []Request{
		{Method: "GET", Path: "/health"},
		{Method: "GET", Path: "/api"},
		{Method: "GET", Path: "/api/../admin"},
		{Method: "GET", Path: "/api/./me"},
		{Method: "GET", Path: "/api//me"},
		{Method: "GET", Path: "/api/%2e%2e/x"},
		{Method: "GET", Path: "/api/a%2Fb"},
		{Method: "GET", Path: "/api/me?x=1"},
		{Method: "GET", Path: "/api/me#x"},
		{Method: "GET", Path: "/api/me\\x"},
		{Method: "GET", Path: "/api/m e"},
		{Method: "GET", Path: "//evil.com/api/me"},
		{Method: "GET", Path: "/api/me/"},
		{Method: "TRACE", Path: "/api/me"},
		{Method: "GET", Path: "/api/me", Headers: map[string]string{"Cookie": "doclite_session=x"}},
		{Method: "GET", Path: "/api/me", Headers: map[string]string{"X-Client-Version": "9.9.9"}},
		{Method: "GET", Path: "/api/me", Query: [][]string{{"only-one"}}},
		{Method: "GET", Path: "/api/me", Query: [][]string{{"", "v"}}},
	}
	for _, r := range bad {
		if _, err := BuildURL(server, &r); err == nil {
			t.Errorf("BuildURL(%s %q, headers %v, query %v) 应该被拒绝", r.Method, r.Path, r.Headers, r.Query)
		}
	}
	if _, err := BuildURL("", &Request{Method: "GET", Path: "/api/me"}); err == nil {
		t.Error("没配置服务器时应该失败")
	}
}

func TestJar(t *testing.T) {
	const server = "http://10.0.0.5:9080"
	path := filepath.Join(t.TempDir(), "cookies.json")
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

	j := OpenJar(path, server)
	if j.Header(now, false) != "" {
		t.Fatal("新罐应该是空的")
	}
	j.Apply(server, []string{
		"doclite_session=tok1; Path=/; Max-Age=43200; HttpOnly; SameSite=Lax",
		"pref=a; Path=/",
		"sec=s; Secure",
	}, now)
	if got := j.Header(now, false); got != "doclite_session=tok1; pref=a" {
		t.Errorf("http 下的 Cookie 头 = %q", got)
	}
	if got := j.Header(now, true); got != "doclite_session=tok1; pref=a; sec=s" {
		t.Errorf("https 下的 Cookie 头 = %q", got)
	}

	// 落盘后重新打开还在；换了服务器的罐作废。
	if got := OpenJar(path, server).Header(now, false); got != "doclite_session=tok1; pref=a" {
		t.Errorf("重新打开后 = %q", got)
	}
	if got := OpenJar(path, "http://other:9080").Header(now, false); got != "" {
		t.Errorf("别的服务器不应读到这个罐：%q", got)
	}

	// 到期后不再带。
	if got := j.Header(now.Add(13*time.Hour), false); got != "pref=a" {
		t.Errorf("会话到期后 = %q", got)
	}
	if j.Has("doclite_session", now) {
		t.Error("过期的会话 Cookie 应该已被删掉")
	}

	// 服务器删除 Cookie（Max-Age=0 或过去的 Expires）。
	j.Apply(server, []string{"doclite_session=tok2; Max-Age=60"}, now)
	j.Apply(server, []string{"doclite_session=; Max-Age=0", "pref=; Expires=Thu, 01 Jan 1970 00:00:00 GMT"}, now)
	if got := j.Header(now, false); got != "" {
		t.Errorf("删除后 = %q", got)
	}

	// 发请求时的服务器和罐已经不一样（中途换了服务器）：不记。
	j.Apply("http://old:9080", []string{"doclite_session=stale; Max-Age=60"}, now)
	if j.Has("doclite_session", now) {
		t.Error("不应记下旧服务器的 Cookie")
	}

	// 换服务器清空。
	j.Apply(server, []string{"doclite_session=tok3; Max-Age=60"}, now)
	j.Reset("http://new:9080")
	if j.Header(now, false) != "" || j.Server() != "http://new:9080" {
		t.Error("换服务器后罐应该是空的")
	}
	if got := OpenJar(path, server).Header(now, false); got != "" {
		t.Errorf("清空应该写回文件：%q", got)
	}
}

func TestSplitUTF8(t *testing.T) {
	full := []byte("data: {\"name\":\"盒 003\"}\n\n")
	// 每个位置切一刀，拼回来的字符串都和原文一样，且每段都是合法 UTF-8。
	for cut := 0; cut <= len(full); cut++ {
		a, rest := splitUTF8(nil, full[:cut])
		b, rest2 := splitUTF8(rest, full[cut:])
		if len(rest2) != 0 {
			t.Fatalf("切在 %d：结尾还剩 %v", cut, rest2)
		}
		if a+b != string(full) {
			t.Fatalf("切在 %d：%q + %q", cut, a, b)
		}
		if strings.ContainsRune(a, '�') || strings.ContainsRune(b, '�') {
			t.Fatalf("切在 %d 出现乱码", cut)
		}
	}
}

// Probe：只要 /api/health 通就连得上（web 服务的入口和 server 本身都行，不要求 GET / 是网页）；要不要升级看
// /api/shell/latest，没有这个接口时看不带会话的 /api/me 回不回 426。
func TestProbe(t *testing.T) {
	latest, minVersion := true, "0.3.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/health":
			io.WriteString(w, `{"status":"ok","version":"9.9.9"}`)
		case "/api/shell/latest":
			if !latest {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"code":"not_found"}`)
				return
			}
			io.WriteString(w, `{"min_version":"`+minVersion+`","latest_version":"0.3.1","download_url":""}`)
		case "/api/me":
			if r.Header.Get("Cookie") != "" {
				t.Error("探测不该带会话")
			}
			if r.Header.Get("X-Client-Version") == "0.2.0" && minVersion == "0.3.0" {
				w.WriteHeader(http.StatusUpgradeRequired)
				io.WriteString(w, `{"code":"client_upgrade_required","message":"x","details":{"client_version":"0.2.0",`+
					`"min_version":"0.3.0","latest_version":"0.3.2","download_url":"http://dl.example/Doclite.exe"}}`)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"code":"unauthorized"}`)
		default:
			// server 本身：/api 以外一律 404。
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"code":"not_found"}`)
		}
	}))
	defer srv.Close()
	p := New("0.2.0", OpenJar(filepath.Join(t.TempDir(), "cookies.json"), ""), slog.New(slog.DiscardHandler))

	res := p.Probe(context.Background(), srv.URL)
	if !res.Reachable || !res.UpgradeRequired || *res.MinVersion != "0.3.0" || *res.LatestVersion != "0.3.1" ||
		res.DownloadURL == nil || *res.DownloadURL != "http://dl.example/Doclite.exe" {
		t.Fatalf("要升级（latest 里没给的下载地址用 426 的）：%+v", res)
	}
	latest = false
	res = p.Probe(context.Background(), srv.URL)
	if !res.Reachable || !res.UpgradeRequired || *res.LatestVersion != "0.3.2" {
		t.Fatalf("没有 /api/shell/latest 时靠 /api/me 的 426 发现要升级：%+v", res)
	}
	minVersion = "0.1.0"
	res = p.Probe(context.Background(), srv.URL)
	if !res.Reachable || res.UpgradeRequired || res.Error != nil {
		t.Fatalf("版本够、GET / 不是网页也算连得上：%+v", res)
	}
}
