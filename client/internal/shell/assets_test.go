package shell

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"doclite/client/internal/appdata"
	"doclite/client/internal/proxy"
)

// 资产服务器的最外层：主机、来源不是资产服务器自己的请求拦下（桥只对同源页面开放）。
func TestAssetGuard(t *testing.T) {
	h := assetGuard(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	cases := []struct {
		name    string
		host    string
		headers map[string]string
		want    int
	}{
		{"同源页面调桥", assetHost(), map[string]string{"Origin": assetScheme() + "://" + assetHost()}, 200},
		{"开发构建接 vite（带端口）", assetHost() + ":5173", map[string]string{"Origin": assetScheme() + "://" + assetHost() + ":5173"}, 200},
		{"同主机别的协议", assetHost(), map[string]string{"Origin": "https://" + assetHost()}, 403},
		{"没有 Origin（WebKit 的自定义协议、导航）", assetHost(), nil, 200},
		{"同源", assetHost(), map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		{"别的主机", "evil.example", nil, 403},
		{"别的来源", assetHost(), map[string]string{"Origin": "http://192.168.1.10:9080"}, 403},
		{"跨站", assetHost(), map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/wails/runtime", nil)
		r.Host = c.host
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s：%d，应为 %d", c.name, w.Code, c.want)
		}
	}
}

func TestCheckExternalURL(t *testing.T) {
	for _, ok := range []string{"http://192.168.1.10:8333/doclite/x.zip?X-Amz-Signature=abc", "https://example.com/Doclite.exe"} {
		if _, err := checkExternalURL(ok); err != nil {
			t.Errorf("%s 应该能打开：%v", ok, err)
		}
	}
	for _, bad := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "smb://host/share", "http://user:pw@host/",
		"http://host/a b", "wails://localhost/", "//host/x", "http:///x"} {
		if _, err := checkExternalURL(bad); err == nil {
			t.Errorf("%q 不该打开", bad)
		}
	}
}

// 改服务器地址：首次配置不问；已经配置过、要改成别的地址时问用户（"从 A 改为 B"），取消就不改；地址没变不问；
// 自检时不问。
func TestChangeServerConfirm(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.DiscardHandler)
	px := proxy.New("0.2.0", proxy.OpenJar(filepath.Join(dir, "cookies.json"), ""), log)
	s := &Shell{cfg: Config{DataDir: dir}, log: log, proxy: px}
	var asked []string
	answer := false
	s.confirmServer = func(_ context.Context, from, to string) (bool, error) {
		asked = append(asked, from+" -> "+to)
		return answer, nil
	}
	saved := func() string {
		st, err := appdata.LoadSettings(dir)
		if err != nil {
			t.Fatal(err)
		}
		return st.ServerURL
	}
	ctx := context.Background()
	const a, b, c = "http://192.168.1.10:9080", "http://10.0.0.5:9080", "http://10.0.0.6:9080"

	if err := s.changeServer(ctx, a); err != nil || len(asked) != 0 || saved() != a || px.Server() != a {
		t.Fatalf("首次配置不该问：%v %v %q", err, asked, saved())
	}
	if err := s.changeServer(ctx, a); err != nil || len(asked) != 0 {
		t.Fatalf("地址没变不该问：%v %v", err, asked)
	}
	err := s.changeServer(ctx, b)
	var pe *proxy.Error
	if !errors.As(err, &pe) || pe.Kind != proxy.KindInvalidRequest || len(asked) != 1 || asked[0] != a+" -> "+b {
		t.Fatalf("改地址要问，取消时报错：%v %v", err, asked)
	}
	if saved() != a || px.Server() != a {
		t.Fatalf("取消了不该改：%q %q", saved(), px.Server())
	}
	answer = true
	if err := s.changeServer(ctx, b); err != nil || saved() != b || px.Server() != b || len(asked) != 2 {
		t.Fatalf("确认了才改：%v %q %v", err, saved(), asked)
	}
	s.smoke = newSmokeRun(s, &SmokeOptions{})
	if err := s.changeServer(ctx, c); err != nil || saved() != c || len(asked) != 2 {
		t.Fatalf("自检时不该问：%v %q %v", err, saved(), asked)
	}
}
