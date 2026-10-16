package shell

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"doclite/client/internal/proxy"
)

// settingsShell 是只登记了设置页命令的壳，openDir 记下要打开的目录（不真的打开）。
func settingsShell(t *testing.T) (*Shell, string, *[]string) {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"logs", "image-cache"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var opened []string
	s := &Shell{cfg: Config{DataDir: dir}, log: slog.New(slog.DiscardHandler), commands: map[string]command{}}
	s.openDir = func(path string) error {
		opened = append(opened, path)
		return nil
	}
	s.registerSettingsCommands()
	return s, dir, &opened
}

// open_local_dir 只认 data、logs、cache 三个名字，路径由壳自己算；别的一律 invalid_request，不打开任何东西。
func TestOpenLocalDir(t *testing.T) {
	s, dir, opened := settingsShell(t)
	b := &Bridge{s: s}
	ctx := context.Background()

	ok := map[string]string{
		`{"dir":"data"}`:  dir,
		`{"dir":"logs"}`:  filepath.Join(dir, "logs"),
		`{"dir":"cache"}`: filepath.Join(dir, "image-cache"),
		// 多给的键不认：打开的仍是壳算出来的目录。
		`{"dir":"data","path":"/etc"}`: dir,
	}
	for args, want := range ok {
		*opened = nil
		res, err := b.Invoke(ctx, "open_local_dir", json.RawMessage(args))
		if err != nil || res != nil {
			t.Errorf("%s：%v %v", args, res, err)
			continue
		}
		if len(*opened) != 1 || (*opened)[0] != want {
			t.Errorf("%s 打开的是 %q，应为 %q", args, *opened, want)
		}
	}

	bad := []string{
		`{}`, `null`, `{"dir":""}`, `{"dir":"DATA"}`, `{"dir":" data"}`, `{"dir":"data "}`, `{"dir":"logs/"}`,
		`{"dir":"data/.."}`, `{"dir":"../data"}`, `{"dir":".."}`, `{"dir":"."}`, `{"dir":"/etc"}`, `{"dir":"C:\\Windows"}`,
		`{"dir":"logs\u0000"}`, `{"dir":"image-cache"}`, `{"dir":"smoke"}`, `{"path":"/etc"}`, `{"dir":1}`,
		`{"dir":["data"]}`, `{"dir":{"name":"data"}}`, `{"dir":null}`, `"data"`,
	}
	*opened = nil
	for _, args := range bad {
		_, err := b.Invoke(ctx, "open_local_dir", json.RawMessage(args))
		var pe *proxy.Error
		if !errors.As(err, &pe) || pe.Kind != proxy.KindInvalidRequest {
			t.Errorf("%s 应该被拒绝（invalid_request）：%v", args, err)
		}
	}
	if len(*opened) != 0 {
		t.Fatalf("被拒绝的参数也打开了目录：%q", *opened)
	}

	// 目录不在了：internal，给页面的话里不带本机路径。
	if err := os.RemoveAll(filepath.Join(dir, "image-cache")); err != nil {
		t.Fatal(err)
	}
	_, err := b.Invoke(ctx, "open_local_dir", json.RawMessage(`{"dir":"cache"}`))
	var pe *proxy.Error
	if !errors.As(err, &pe) || pe.Kind != proxy.KindInternal || strings.Contains(pe.Message, dir) {
		t.Fatalf("目录不在时应为不带路径的 internal：%v", err)
	}
	if len(*opened) != 0 {
		t.Fatalf("目录不在也去打开了：%q", *opened)
	}
}

// local_dirs 给出三个目录的路径；数据目录的大小含日志，不逐个数图片缓存目录（缓存的大小取缓存自己的统计）。
func TestLocalDirs(t *testing.T) {
	s, dir, _ := settingsShell(t)
	write := func(rel string, n int) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("settings.json", 10)
	write("logs/doclite-2026-10-17.log", 100)
	write("logs/doclite-2026-10-16.log", 50)
	write("smoke/logs/doclite-2026-10-17.log", 7)
	write("image-cache/ab/cd.jpg", 1000)

	res, err := (&Bridge{s: s}).Invoke(context.Background(), "local_dirs", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := res.(LocalDirs)
	if got.Data.Path != dir || got.Logs.Path != filepath.Join(dir, "logs") || got.Cache.Path != filepath.Join(dir, "image-cache") {
		t.Fatalf("路径不对：%+v", got)
	}
	if got.Logs.SizeBytes != 150 || got.Logs.FileCount != 2 {
		t.Errorf("日志目录：%+v，应为 150 字节 2 个文件", got.Logs)
	}
	if got.Data.SizeBytes != 167 || got.Data.FileCount != 4 {
		t.Errorf("数据目录：%+v，应为 167 字节 4 个文件（不数图片缓存目录）", got.Data)
	}
	if got.Cache.SizeBytes != 0 || got.Cache.FileCount != 0 {
		t.Errorf("没有缓存服务时缓存的大小应为 0：%+v", got.Cache)
	}
}
