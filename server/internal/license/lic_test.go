package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testdata 里的 .lic 是签发工具（../bak/doclite-license）签的：
//   - cli-issue.lic：工具的命令行 `issue`（bak 里已有的可执行文件）；
//   - issue-now.lic、fixed-*.lic：编译工具自己的 src/license.rs、src/crypto.rs（按路径引用，依赖版本同工具的
//     Cargo.lock）签的，fixed-* 用固定时间覆盖整秒、毫秒、微秒、纳秒几种写法，issue-now 的公司名带各种要转义的字符；
//     *.canonical 是工具算出来的规范 JSON（签名签的字节）。
//
// 测试密钥对只在开发机的临时目录里生成，这里只放公钥（test-public.pem）。

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fixtureKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pk, err := ParsePublicKey(readFixture(t, "test-public.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return pk
}

// TestCanonicalGolden：和工具的兼容性测试（tests/license_compat.rs）里的黄金字节串一致。
func TestCanonicalGolden(t *testing.T) {
	ts := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	p := Payload{
		LicenseID: "11111111-2222-3333-4444-555555555555", Company: "Acme 测试公司", Edition: "enterprise", MaxUsers: 50,
		IssuedAt: ts("2026-01-02T03:04:05Z"), ExpiresAt: ts("2027-01-02T03:04:05Z"),
		HardwareID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	want := `{"company":"Acme 测试公司","edition":"enterprise","expires_at":"2027-01-02T03:04:05Z","hardware_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","issued_at":"2026-01-02T03:04:05Z","license_id":"11111111-2222-3333-4444-555555555555","max_users":50}`
	if got := string(Canonical(p)); got != want {
		t.Fatalf("整秒：\n%s\n%s", got, want)
	}
	p.IssuedAt, p.ExpiresAt = ts("2026-06-12T08:30:00.123456789Z"), ts("2026-07-12T08:30:00.123456Z")
	want = `{"company":"Acme 测试公司","edition":"enterprise","expires_at":"2026-07-12T08:30:00.123456Z","hardware_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","issued_at":"2026-06-12T08:30:00.123456789Z","license_id":"11111111-2222-3333-4444-555555555555","max_users":50}`
	if got := string(Canonical(p)); got != want {
		t.Fatalf("小数秒：\n%s\n%s", got, want)
	}
	// 时区不是 UTC 的也按 UTC 写；整毫秒写 3 位。
	if got := FormatTime(ts("2026-06-12T16:30:00.5+08:00")); got != "2026-06-12T08:30:00.500Z" {
		t.Fatalf("毫秒：%s", got)
	}
}

// TestToolFixtures：工具签的每一份都能解析、规范 JSON 和工具算的逐字节相同、验签通过；改一个字段、换一把公钥都验不过。
func TestToolFixtures(t *testing.T) {
	pk := fixtureKey(t)
	names := []string{"cli-issue", "issue-now", "fixed-seconds", "fixed-millis", "fixed-nanos"}
	for _, name := range names {
		f, err := Decode(readFixture(t, name+".lic"))
		if err != nil {
			t.Fatalf("%s：%v", name, err)
		}
		if f.Version != 1 || !strings.HasPrefix(f.Payload.HardwareID, "sha256:") {
			t.Fatalf("%s：%+v", name, f)
		}
		if name != "cli-issue" {
			want := strings.TrimSuffix(readFixture(t, name+".canonical"), "\n")
			if got := string(Canonical(f.Payload)); got != want {
				t.Fatalf("%s 规范 JSON 不同：\n%s\n%s", name, got, want)
			}
		}
		if err := Verify(pk, f); err != nil {
			t.Fatalf("%s：%v", name, err)
		}
		// 篡改：席位、到期、公司、install_id 各改一处。
		for _, tamper := range []func(*Payload){
			func(p *Payload) { p.MaxUsers++ },
			func(p *Payload) { p.ExpiresAt = p.ExpiresAt.Add(time.Nanosecond) },
			func(p *Payload) { p.Company += " " },
			func(p *Payload) { p.HardwareID = strings.ToUpper(p.HardwareID) },
		} {
			g := *f
			tamper(&g.Payload)
			if Verify(pk, &g) == nil {
				t.Fatalf("%s：篡改后仍然验签通过", name)
			}
		}
		other, _, _ := ed25519.GenerateKey(rand.Reader)
		if Verify(other, f) == nil {
			t.Fatalf("%s：换一把公钥仍然验签通过", name)
		}
	}
	// 解码后的特殊字符原样还原（签名能验过已经说明了这一点，这里再看一眼）。
	f, _ := Decode(readFixture(t, "issue-now.lic"))
	for _, s := range []string{`"引号"`, `\反斜杠`, "<a&b>", "tab[\t]", "nl[\n]", "c01[\x01]", "del[\x7f]", "ls[ ]", "😀"} {
		if !strings.Contains(f.Payload.Company, s) {
			t.Fatalf("公司名里没有 %q：%q", s, f.Payload.Company)
		}
	}
}

// TestTamperedFile：直接改文件里的 JSON（把席位从 50 改成 500）再编码回去，验签失败。
func TestTamperedFile(t *testing.T) {
	text := readFixture(t, "fixed-seconds.lic")
	var b64 strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if line != "" && !strings.HasPrefix(line, "-----") {
			b64.WriteString(line)
		}
	}
	data, _ := base64.StdEncoding.DecodeString(b64.String())
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["payload"].(map[string]any)["max_users"] = 500
	data, _ = json.Marshal(raw)
	tampered := licHeader + "\n" + base64.StdEncoding.EncodeToString(data) + "\n" + licFooter + "\n"
	f, err := Decode(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if f.Payload.MaxUsers != 500 || Verify(fixtureKey(t), f) != ErrBadSignature {
		t.Fatalf("篡改的文件：%+v", f.Payload)
	}
}

// TestSignRoundTrip：服务端给测试造许可证的 Sign → Decode → Verify 走得通（编码和工具同一格式）。
func TestSignRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	p := Payload{LicenseID: "0198f5a0-0000-7000-8000-000000000009", Company: "<测试>& ", Edition: "trial", MaxUsers: 2,
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), HardwareID: "sha256:" + strings.Repeat("ab", 32)}
	text := Sign(priv, p)
	f, err := Decode(text)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(pub, f); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if len(line) > 76 {
			t.Fatalf("行长 %d", len(line))
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, text := range []string{
		"",
		licHeader + "\n" + licFooter,
		"不是 base64",
		base64.StdEncoding.EncodeToString([]byte(`{"payload":{}}`)),
		base64.StdEncoding.EncodeToString([]byte(`[1,2]`)),
	} {
		if _, err := Decode(text); err == nil {
			t.Fatalf("应该失败：%q", text)
		} else if _, ok := err.(*FormatError); !ok {
			t.Fatalf("错误类型：%T", err)
		}
	}
}

func TestParsePublicKey(t *testing.T) {
	pem := readFixture(t, "test-public.pem")
	a, err := ParsePublicKey(pem)
	if err != nil {
		t.Fatal(err)
	}
	// PEM 中间那一行（-ldflags 用这个）。
	line := strings.Split(strings.TrimSpace(pem), "\n")[1]
	b, err := ParsePublicKey(line)
	if err != nil || !a.Equal(b) {
		t.Fatalf("SPKI base64：%v", err)
	}
	c, err := ParsePublicKey(base64.StdEncoding.EncodeToString(a))
	if err != nil || !a.Equal(c) {
		t.Fatalf("原始 32 字节：%v", err)
	}
	if _, err := ParsePublicKey("abc"); err == nil {
		t.Fatal("abc 应该失败")
	}
}
