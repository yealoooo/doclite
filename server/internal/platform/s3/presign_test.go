package s3

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSignTimeAndExpiry(t *testing.T) {
	cases := []struct{ now, want string }{
		{"2026-10-16T01:00:00Z", "2026-10-15T16:00:00Z"}, // UTC+8 09:00 → 当天 0 点
		{"2026-10-16T15:59:59Z", "2026-10-15T16:00:00Z"}, // UTC+8 23:59:59
		{"2026-10-16T16:00:00Z", "2026-10-16T16:00:00Z"}, // UTC+8 第二天 0 点
		{"2026-10-16T08:30:00+08:00", "2026-10-15T16:00:00Z"},
	}
	for _, c := range cases {
		now, _ := time.Parse(time.RFC3339, c.now)
		if got := SignTime(now).Format(time.RFC3339); got != c.want {
			t.Errorf("SignTime(%s) = %s，期望 %s", c.now, got, c.want)
		}
	}
	for hours, want := range map[int]time.Duration{1: 25 * time.Hour, 24: 48 * time.Hour, 144: 168 * time.Hour, 168: 168 * time.Hour} {
		if got := PresignExpiry(hours); got != want {
			t.Errorf("PresignExpiry(%d) = %s，期望 %s", hours, got, want)
		}
	}
}

func TestPresignGet(t *testing.T) {
	cfg := Config{Endpoint: "http://objectstore:8333", PublicEndpoint: "http://192.168.1.10:8333/", Bucket: "doclite",
		AccessKey: "ak", SecretKey: "sk", ForcePathStyle: true}
	now := time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)
	p, err := PresignGet(cfg, "doclite/WS2025/0199c3/v1.jpg", 24, now)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(p.URL)
	q := u.Query()
	if u.Host != "192.168.1.10:8333" || u.Path != "/doclite/doclite/WS2025/0199c3/v1.jpg" ||
		q.Get("X-Amz-Date") != "20261015T160000Z" || q.Get("X-Amz-Expires") != "172800" ||
		q.Get("X-Amz-Signature") == "" || !strings.HasPrefix(q.Get("X-Amz-Credential"), "ak/20261015/us-east-1/s3/") {
		t.Fatalf("URL：%s", p.URL)
	}
	if want := time.Date(2026, 10, 17, 16, 0, 0, 0, time.UTC); !p.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt %s，期望 %s", p.ExpiresAt, want)
	}
	// 同一天里 URL 不变；第二天变。
	p2, _ := PresignGet(cfg, "doclite/WS2025/0199c3/v1.jpg", 24, now.Add(12*time.Hour))
	p3, _ := PresignGet(cfg, "doclite/WS2025/0199c3/v1.jpg", 24, now.Add(13*time.Hour))
	if p2.URL != p.URL || p3.URL == p.URL {
		t.Fatalf("按天取整不对：\n%s\n%s\n%s", p.URL, p2.URL, p3.URL)
	}
	// 换了 secret_key（access_key 不变）签名要跟着变（SDK 的签名器按 access_key 缓存派生密钥，不能共用）。
	other := cfg
	other.SecretKey = "sk2"
	if p5, _ := PresignGet(other, "doclite/WS2025/0199c3/v1.jpg", 24, now); p5.URL == p.URL {
		t.Fatal("换了 secret_key，签名没变")
	}
	// 虚拟主机方式；key 里的特殊字符按 S3 的规则编码。
	cfg.ForcePathStyle = false
	cfg.PublicEndpoint = ""
	p4, _ := PresignGet(cfg, "a b/c+d.jpg", 1, now)
	if !strings.HasPrefix(p4.URL, "http://doclite.objectstore:8333/a%20b/c%2Bd.jpg?") {
		t.Fatalf("虚拟主机方式：%s", p4.URL)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8333":   "http://127.0.0.1:8333",
		" HTTP://host:8333/ ":     "http://host:8333",
		"https://s3.example.com":  "https://s3.example.com",
		"http://[::1]:8333":       "http://[::1]:8333",
		"http://objectstore:8333": "http://objectstore:8333",
	} {
		got, err := NormalizeEndpoint(in)
		if err != nil || got != want {
			t.Errorf("NormalizeEndpoint(%q) = %q, %v；期望 %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "127.0.0.1:8333", "ftp://h", "http://", "http://h/path", "http://h?x=1", "http://h#f",
		"http://u:p@h", "http://h:port"} {
		if got, err := NormalizeEndpoint(bad); err == nil {
			t.Errorf("NormalizeEndpoint(%q) = %q，期望报错", bad, got)
		}
	}
}
