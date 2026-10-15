package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// SSE 的默认间隔（requirements 第 7 节）。
const (
	DefaultKeepAlive = 15 * time.Second
	DefaultRecheck   = 60 * time.Second
	// recheckTimeout：一次复核超过这么久没有结果就断开（客户端会重连）。
	recheckTimeout = 10 * time.Second
)

// StreamOptions 是一条 SSE 连接的参数。
type StreamOptions struct {
	// KeepAlive 是保活注释的间隔，0 表示 DefaultKeepAlive。
	KeepAlive time.Duration
	// RecheckEvery 是复核会话和授权的间隔，0 表示 DefaultRecheck。
	RecheckEvery time.Duration
	// Recheck 复核会话和项目授权：返回错误时断开（会话失效、授权撤销、项目删除、查库出错）。必填。
	Recheck func(ctx context.Context) error
	// Until 是会话到期的时刻，到了就断开；零值表示不看。
	Until time.Time
	Log   *slog.Logger
}

// Serve 输出项目 projectID 的实时事件（`text/event-stream`），直到客户端断开、复核不通过、会话到期或总线关闭。
// 权限检查（登录、能进项目）由调用方在这之前做完。
//
// 格式：每条事件 `data: <JSON>\n\n`（没有 event 名称）；每 KeepAlive 一条注释 `: ping\n\n` 保活。
// 断开期间的事件不补发，客户端重连后整体刷新。
func Serve(w http.ResponseWriter, r *http.Request, bus *Bus, projectID uuid.UUID, opts StreamOptions) {
	if opts.KeepAlive <= 0 {
		opts.KeepAlive = DefaultKeepAlive
	}
	if opts.RecheckEvery <= 0 {
		opts.RecheckEvery = DefaultRecheck
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	sub := bus.Subscribe(projectID)
	defer sub.Close()

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		if r.Context().Err() != nil {
			// 写连接失败时 net/http 会取消请求的 context：客户端已经断开，不是服务端的问题。
			log.Debug("SSE 连上就被客户端断开", "project_id", projectID, "error", err)
		} else {
			log.Warn("SSE 不能刷出响应", "error", err)
		}
		return
	}

	keep := time.NewTicker(opts.KeepAlive)
	defer keep.Stop()
	recheck := time.NewTicker(opts.RecheckEvery)
	defer recheck.Stop()
	var expire <-chan time.Time
	if !opts.Until.IsZero() {
		t := time.NewTimer(time.Until(opts.Until))
		defer t.Stop()
		expire = t.C
	}

	write := func(data []byte) bool {
		if _, err := w.Write(data); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-bus.Done():
			return
		case <-expire:
			return
		case ev := <-sub.C:
			data, err := json.Marshal(ev)
			if err != nil {
				log.Error("SSE 事件序列化失败", "error", err)
				return
			}
			buf := make([]byte, 0, len(data)+8)
			buf = append(buf, "data: "...)
			buf = append(buf, data...)
			buf = append(buf, "\n\n"...)
			if !write(buf) {
				return
			}
		case <-keep.C:
			if !write([]byte(": ping\n\n")) {
				return
			}
		case <-recheck.C:
			cctx, cancel := context.WithTimeout(ctx, recheckTimeout)
			err := opts.Recheck(cctx)
			cancel()
			if err != nil {
				log.Debug("SSE 复核不通过，断开", "project_id", projectID, "reason", err)
				return
			}
		}
	}
}
