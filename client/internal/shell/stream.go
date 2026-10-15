package shell

import (
	"errors"

	"github.com/wailsapp/wails/v3/pkg/application"

	"doclite/client/internal/proxy"
)

// apiStreamName 是流式请求（`api_stream`）用的 Wails 流的名字（web/src/lib/shell.ts 的 shellStream）。
const apiStreamName = "doclite.api_stream"

// handleAPIStream 处理一次 `api_stream`：页面打开一个 Wails 流，先发一帧 `{req}`；壳代理这个请求，把
// open、chunk…、end（或 error）逐条发回去，发完关掉流。
//
// 为什么用 Wails 的流（GoStream）而不是资产服务器的中间件：Windows 上 Wails 把资产服务器的响应整块攒到处理结束
// 才交给 WebView2（internal/assetserver/webview/responsewriter_windows.go），SSE 走中间件会一直等到连接断开。
// Wails 的流是"挂起的轮询"：Go 端每发一帧，正在等的那次轮询请求就带着已排队的帧完整地结束，页面马上再发下一次
// 轮询——每一帧都装在一个完整的响应里送到页面，不依赖 WebView2 支持流式的响应体，所以在 WebView2 上同样逐块到达。
// 页面关流（取消订阅）、页面重载、窗口关闭、壳退出时 conn 的 Context 被取消，代理随之断开到 server 的连接。
func (s *Shell) handleAPIStream(c *application.StreamConn) {
	defer c.Close()
	var first struct {
		Req *proxy.Request `json:"req"`
	}
	if err := c.ReceiveJSON(&first); err != nil {
		return
	}
	if first.Req == nil {
		_ = c.SendJSON(proxy.StreamEvent{"event": "error", "kind": proxy.KindInvalidRequest, "message": "缺少 req"})
		return
	}
	ctx := c.Context()
	done := make(chan struct{})
	emit := func(ev proxy.StreamEvent) error {
		err := c.SendJSON(ev)
		if e := ev["event"]; e == "end" || e == "error" {
			close(done)
		}
		return err
	}
	if err := s.proxy.Stream(ctx, first.Req, emit); err != nil {
		var pe *proxy.Error
		if !errors.As(err, &pe) {
			pe = proxy.Internal(err.Error())
		}
		_ = c.SendJSON(proxy.StreamEvent{"event": "error", "kind": pe.Kind, "message": pe.Message})
		return
	}
	// 等代理发完（end、error），或者页面关了流。
	select {
	case <-done:
	case <-ctx.Done():
	}
}
