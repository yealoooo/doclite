// Package proxy 是壳的会话代理（requirements 第 6 节"会话"）：页面经壳的命令访问已配置服务器上 `/api/` 下的路径，
// 会话 Cookie 只在这里（Go 端）保存，页面拿不到令牌；`X-Client-Type: desktop`、`X-Client-Version` 由这里统一加。
//
// 纯逻辑（地址规整、请求核对、Cookie 罐、UTF-8 切分）和发请求的代码在同一个包里，纯逻辑有单元测试。
package proxy

// Error 是命令失败时交给页面的错误，页面收到的是 `{kind, message}`（web/src/lib/shell.ts 的 ShellError）。
type Error struct {
	// Kind：not_configured（还没配置服务器）、invalid_request（参数不对）、network（连不上、中途断开）、
	// internal（壳内部出错）、unsupported（这个版本的壳还没有这个功能）。
	Kind    string `json:"kind"`
	Message string `json:"message"`
	// Detail 是只写进日志的细节（原始错误，可能带本机路径），不给页面。
	Detail string `json:"-"`
}

func (e *Error) Error() string { return e.Kind + ": " + e.Message }

// 错误种类。
const (
	KindNotConfigured  = "not_configured"
	KindInvalidRequest = "invalid_request"
	KindNetwork        = "network"
	KindInternal       = "internal"
	KindUnsupported    = "unsupported"
)

// InvalidRequest 是参数不对的错误。
func InvalidRequest(msg string) *Error { return &Error{Kind: KindInvalidRequest, Message: msg} }

// NotConfigured 是还没配置服务器的错误。
func NotConfigured() *Error {
	return &Error{Kind: KindNotConfigured, Message: "还没有配置服务器地址"}
}

// Network 是连不上服务器、连接中断的错误。
func Network(msg string) *Error { return &Error{Kind: KindNetwork, Message: msg} }

// Internal 是壳内部出错。
func Internal(msg string) *Error { return &Error{Kind: KindInternal, Message: msg} }

// InternalDetail 是壳内部出错：页面只看到一句概括，原始错误（可能带本机路径）只写进日志（Bridge.Invoke 记）。
func InternalDetail(summary string, err error) *Error {
	e := &Error{Kind: KindInternal, Message: summary + "（详情见客户端日志）"}
	if err != nil {
		e.Detail = err.Error()
	}
	return e
}
