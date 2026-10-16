package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pack 写一个产物包：entries 是 名字 → 内容；link 非空时多放一个符号链接。
func pack(t *testing.T, dir, name string, entries map[string]string, link string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for n, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	if link != "" {
		tw.WriteHeader(&tar.Header{Name: link, Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	}
	tw.Close()
	gz.Close()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	versionFile := filepath.Join(dir, "WEB_VERSION")
	os.WriteFile(versionFile, []byte("0.1.0\n"), 0o644)
	out := filepath.Join(dir, "frontend")
	good := map[string]string{
		"manifest.json":        `{"name":"doclite-web","version":"0.1.0"}`,
		"dist/index.html":      "<!doctype html>",
		"dist/assets/index.js": "console.log(1)",
	}

	t.Setenv(artifactEnv, pack(t, dir, "ok.tar.gz", good, ""))
	if err := run(versionFile, out, false); err != nil {
		t.Fatalf("对的产物包应该能用：%v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "dist", "assets", "index.js")); string(b) != "console.log(1)" {
		t.Fatalf("没解出来：%q", b)
	}

	wrong := map[string]string{"manifest.json": `{"name":"doclite-web","version":"0.2.0"}`, "dist/index.html": "x"}
	t.Setenv(artifactEnv, pack(t, dir, "wrong.tar.gz", wrong, ""))
	if err := run(versionFile, out, false); err == nil || !strings.Contains(err.Error(), "0.2.0") {
		t.Fatalf("版本不对应该失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "dist")); err == nil {
		t.Fatal("版本不对时不该留下界面")
	}

	for name, entries := range map[string]map[string]string{
		"escape.tar.gz":   {"manifest.json": good["manifest.json"], "dist/index.html": "x", "../evil": "x"},
		"other.tar.gz":    {"manifest.json": good["manifest.json"], "dist/index.html": "x", "src/main.ts": "x"},
		"noindex.tar.gz":  {"manifest.json": good["manifest.json"], "dist/a.js": "x"},
		"nomanifest.gz":   {"dist/index.html": "x"},
		"badname.tar.gz":  {"manifest.json": `{"name":"other","version":"0.1.0"}`, "dist/index.html": "x"},
		"absolute.tar.gz": {"manifest.json": good["manifest.json"], "dist/index.html": "x", "/dist/x": "x"},
	} {
		t.Setenv(artifactEnv, pack(t, dir, name, entries, ""))
		if err := run(versionFile, out, false); err == nil {
			t.Errorf("%s 应该被拒", name)
		}
	}
	t.Setenv(artifactEnv, pack(t, dir, "link.tar.gz", good, "dist/link"))
	if err := run(versionFile, out, false); err == nil {
		t.Error("带符号链接的包应该被拒")
	}

	t.Setenv(artifactEnv, filepath.Join(dir, "missing.tar.gz"))
	if err := run(versionFile, out, false); err == nil || !strings.Contains(err.Error(), "npm run release") {
		t.Fatalf("找不到产物包应该失败并说明：%v", err)
	}
	if err := run(versionFile, out, true); err != nil {
		t.Fatalf("开发构建找不到产物包不算失败：%v", err)
	}
}
