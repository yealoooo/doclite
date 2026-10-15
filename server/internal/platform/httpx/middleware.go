package httpx

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

type loggerKey struct{}

// LoggerFrom 取请求的日志器（[Logging] 放进去的）；没有时用 slog.Default()。
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// Logging 是请求日志：每个 `/api` 请求一行（方法、路径、状态码、耗时、响应字节数），5xx 用 Error 级别；
// 网页的静态文件只在 Debug 级别记录。不记录请求体、Cookie 和查询参数。
//
// 同时把 log 放进请求的 context（[LoggerFrom]），并且兜住 handler 的 panic：返回 500 `internal`、记下调用栈。
func Logging(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			r = r.WithContext(context.WithValue(r.Context(), loggerKey{}, log))
			defer func() {
				if p := recover(); p != nil {
					if p == http.ErrAbortHandler {
						panic(p)
					}
					log.Error("请求处理时 panic", "method", r.Method, "path", r.URL.Path,
						"panic", fmt.Sprint(p), "stack", string(debug.Stack()))
					if !rec.wroteHeader {
						WriteError(rec, r, NewError(http.StatusInternalServerError, CodeInternal, "服务器内部错误，请稍后重试或联系管理员"))
					}
				}
				status := rec.status
				if status == 0 {
					status = http.StatusOK
				}
				level := slog.LevelInfo
				switch {
				case status >= 500:
					level = slog.LevelError
				case !strings.HasPrefix(r.URL.Path, "/api/"):
					level = slog.LevelDebug
				}
				log.Log(r.Context(), level, "请求",
					"method", r.Method,
					"path", r.URL.Path,
					"status", status,
					"ms", time.Since(start).Milliseconds(),
					"bytes", rec.bytes,
				)
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// statusRecorder 记下状态码和响应字节数。实现 Unwrap，http.ResponseController（SSE 的 Flush 等）能拿到底层的 writer。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.status = http.StatusOK
		s.wroteHeader = true
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Flush 让包了一层之后仍然能刷出（SSE）。
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		if !s.wroteHeader {
			s.status = http.StatusOK
			s.wroteHeader = true
		}
		f.Flush()
	}
}
