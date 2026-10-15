package apitest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"doclite/server/internal/platform/httpx"
)

// TestClientDisconnect：客户端中途断开（翻页、关页面、SSE 断开）不算服务端的错——访问日志记 499、Info 级别，
// 日志里没有 ERROR；SSE 被客户端断开时正常结束，记 200。
func TestClientDisconnect(t *testing.T) {
	t.Parallel()
	logs := newLogCapture()
	env := NewEnvWithLog(t, slog.New(logs))
	admin := env.Admin()
	d := env.CreateProject("CD")
	ctx := context.Background()

	// 看板查库时客户端断开：另一个事务锁住 folders，看板的查询一定卡在等锁；确认卡住了再断开。
	tx, err := env.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, "LOCK TABLE folders IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	summary := "/api/projects/" + d.ID.String() + "/catalog-summary"
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(rctx, "GET", env.URL+summary, nil)
		if err != nil {
			done <- err
			return
		}
		res, err := admin.hc.Do(req)
		if err == nil {
			res.Body.Close()
		}
		done <- err
	}()
	waitLockWaiters(t, env, "%FROM folders%", 1)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("请求应该被取消：%v", err)
	}
	rec := logs.waitAccess(t, summary)
	if got := rec.attrs["status"].Int64(); got != httpx.StatusClientClosedRequest || rec.level != slog.LevelInfo {
		t.Errorf("看板被客户端断开：访问日志 status=%d level=%s，应为 499 INFO\n%s", got, rec.level, logs.dump())
	}
	_ = tx.Rollback(ctx)

	// SSE：连上以后客户端断开。
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	res := streamRequest(t, sctx, admin, d.ID)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("打开实时通道：%d", res.StatusCode)
	}
	scancel()
	res.Body.Close()
	rec = logs.waitAccess(t, "/api/projects/"+d.ID.String()+"/stream")
	if got := rec.attrs["status"].Int64(); got != http.StatusOK || rec.level != slog.LevelInfo {
		t.Errorf("SSE 被客户端断开：访问日志 status=%d level=%s，应为 200 INFO\n%s", got, rec.level, logs.dump())
	}

	logs.noErrors(t)
}

// logCapture 是收集 server 日志的 slog.Handler（Debug 及以上都收），测试里查看。
type logCapture struct {
	*logStore
	attrs []slog.Attr // WithAttrs 加的
}

type logStore struct {
	mu      sync.Mutex
	recs    []logRecord
	changed chan struct{} // 每收到一条通知一次（容量 1，不会漏）
}

type logRecord struct {
	level slog.Level
	msg   string
	attrs map[string]slog.Value
}

func newLogCapture() *logCapture {
	return &logCapture{logStore: &logStore{changed: make(chan struct{}, 1)}}
}

func (h *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *logCapture) Handle(_ context.Context, r slog.Record) error {
	rec := logRecord{level: r.Level, msg: r.Message, attrs: map[string]slog.Value{}}
	for _, a := range h.attrs {
		rec.attrs[a.Key] = a.Value.Resolve()
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.Resolve()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, rec)
	h.mu.Unlock()
	select {
	case h.changed <- struct{}{}:
	default:
	}
	return nil
}

func (h *logCapture) WithAttrs(as []slog.Attr) slog.Handler {
	return &logCapture{logStore: h.logStore, attrs: append(append([]slog.Attr(nil), h.attrs...), as...)}
}

func (h *logCapture) WithGroup(string) slog.Handler { return h }

// waitAccess 等到访问日志（httpx.Logging 的"请求"）里有 path 这一行，最多 5 秒。
func (h *logCapture) waitAccess(t *testing.T, path string) logRecord {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		h.mu.Lock()
		for _, r := range h.recs {
			if r.msg == "请求" && r.attrs["path"].String() == path {
				h.mu.Unlock()
				return r
			}
		}
		h.mu.Unlock()
		select {
		case <-h.changed:
		case <-timeout:
			t.Fatalf("5 秒内访问日志里没有 %s\n%s", path, h.dump())
		}
	}
}

// noErrors 断言没有 ERROR 级别的日志。
func (h *logCapture) noErrors(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.recs {
		if r.level >= slog.LevelError {
			t.Errorf("不应有 ERROR 日志：%s %v", r.msg, r.attrs)
		}
	}
}

func (h *logCapture) dump() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var b strings.Builder
	for _, r := range h.recs {
		fmt.Fprintf(&b, "  %s %s %v\n", r.level, r.msg, r.attrs)
	}
	return b.String()
}
