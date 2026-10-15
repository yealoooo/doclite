package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// LoginPath、LogoutPath：登录成功、退出登录要特别处理（托盘状态、清空罐）。
	LoginPath  = "/api/auth/login"
	LogoutPath = "/api/auth/logout"

	// 普通请求的总时限。
	fetchTimeout = 2 * time.Minute
	// 普通请求响应体的上限（都是 JSON；批量接口一次最多 500 项）。
	maxResponseBody = 64 << 20
	// 流式请求每次最多读多少字节转给页面。
	streamReadSize = 32 << 10
)

// Observer 在每个 `api_fetch` 请求有了响应后调用（status 是 HTTP 状态码，body 是响应体）。托盘据此显示
// "需要重新登录""客户端需要升级"；以后取图的来源记录（M6-3）也从这里记 batch、snapshots 的响应。
type Observer func(req *Request, status int, body []byte)

// Proxy 是会话代理。服务器地址和 Cookie 罐都在这里；页面只经它访问 server。
type Proxy struct {
	version string
	log     *slog.Logger
	jar     *Jar

	fetchClient  *http.Client
	streamClient *http.Client

	mu        sync.Mutex
	observers []Observer
}

// New 建会话代理。jar 是已经按当前服务器打开的罐（服务器为空表示还没配置）。
func New(version string, jar *Jar, log *slog.Logger) *Proxy {
	newTransport := func() *http.Transport {
		return &http.Transport{
			// 内网直连 server，不走系统代理。
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: fetchTimeout,
			// 不让 Go 自动加 Accept-Encoding: gzip 再解压：SSE 经解压器会被攒成大块，失去逐块转发的意义。
			DisableCompression: true,
		}
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Proxy{
		version:      version,
		log:          log,
		jar:          jar,
		fetchClient:  &http.Client{Transport: newTransport(), Timeout: fetchTimeout, CheckRedirect: noRedirect},
		streamClient: &http.Client{Transport: newTransport(), CheckRedirect: noRedirect},
	}
}

// Server 是已配置的服务器（origin），没配置时为空。
func (p *Proxy) Server() string { return p.jar.Server() }

// SetServer 换服务器：罐清空（换了服务器要重新登录）。地址没变时什么都不做。返回是否换了。
func (p *Proxy) SetServer(server string) bool {
	if p.jar.Server() == server {
		return false
	}
	p.jar.Reset(server)
	return true
}

// HasSession 判断罐里有没有会话 Cookie（没登录、已退出、过期时为 false）。
func (p *Proxy) HasSession() bool { return p.jar.Has("doclite_session", time.Now()) }

// Observe 登记一个观察者。
func (p *Proxy) Observe(o Observer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observers = append(p.observers, o)
}

func (p *Proxy) notify(req *Request, status int, body []byte) {
	p.mu.Lock()
	list := append([]Observer(nil), p.observers...)
	p.mu.Unlock()
	for _, o := range list {
		o(req, status, body)
	}
}

// newRequest 核对页面的请求、建好 HTTP 请求（带上壳的头和 Cookie）。返回请求和发出时罐所属的服务器。
func (p *Proxy) newRequest(ctx context.Context, req *Request) (*http.Request, string, error) {
	server := p.jar.Server()
	if server == "" {
		return nil, "", NotConfigured()
	}
	target, err := BuildURL(server, req)
	if err != nil {
		return nil, "", err
	}
	var body io.Reader
	if req.Body != nil {
		body = strings.NewReader(*req.Body)
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, target, body)
	if err != nil {
		return nil, "", InvalidRequest("请求格式不对")
	}
	for name, value := range req.Headers {
		hr.Header.Set(name, value)
	}
	p.addShellHeaders(hr, server)
	return hr, server, nil
}

func (p *Proxy) addShellHeaders(hr *http.Request, server string) {
	hr.Header.Set("X-Client-Type", "desktop")
	hr.Header.Set("X-Client-Version", p.version)
	if c := p.jar.Header(time.Now(), strings.HasPrefix(server, "https://")); c != "" {
		hr.Header.Set("Cookie", c)
	}
}

// afterResponse 记下 Set-Cookie；退出登录（不论结果）清空罐。
func (p *Proxy) afterResponse(server string, req *Request, resp *http.Response) {
	if resp != nil {
		p.jar.Apply(server, resp.Header.Values("Set-Cookie"), time.Now())
	}
	if req.Method == http.MethodPost && req.Path == LogoutPath && p.jar.Server() == server {
		p.jar.Reset(server)
	}
}

// Fetch 代理一个普通请求（`api_fetch`）：等完整的响应再返回。4xx、5xx 照常返回（页面按错误体处理），
// 连不上、超时返回 network 错误。
func (p *Proxy) Fetch(ctx context.Context, req *Request) (*Response, error) {
	hr, server, err := p.newRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	resp, err := p.fetchClient.Do(hr)
	if err != nil {
		p.afterResponse(server, req, nil)
		if ctx.Err() != nil {
			return nil, Network("请求已取消")
		}
		p.log.Warn("请求失败", "method", req.Method, "path", req.Path, "err", err)
		return nil, Network(describeNetError(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	p.afterResponse(server, req, resp)
	if err != nil {
		p.log.Warn("读响应失败", "method", req.Method, "path", req.Path, "status", resp.StatusCode, "err", err)
		return nil, Network("读取服务器的响应时连接中断")
	}
	if len(data) > maxResponseBody {
		return nil, Internal("服务器的响应太大")
	}
	p.log.Debug("api_fetch", "method", req.Method, "path", req.Path, "status", resp.StatusCode,
		"ms", time.Since(started).Milliseconds())
	p.notify(req, resp.StatusCode, data)
	return &Response{Status: resp.StatusCode, Headers: pickResponseHeaders(resp.Header), Body: string(data)}, nil
}

// StreamEvent 是流式请求推给页面的一条消息（web/src/lib/shell.ts 的 ShellStreamEvent）：
// `{event: "open", status, headers}`、`{event: "chunk", data}`、`{event: "end"}`、`{event: "error", kind, message}`。
type StreamEvent map[string]any

// Stream 代理一个流式请求（`api_stream`，SSE 用）：收到响应头发 open，之后每读到一块数据就发一条 chunk，
// 服务器结束时发 end；连不上或中途断开时发一条 error。ctx 取消（页面取消、页面重载、壳退出）时断开连接，
// 不再发任何消息。emit 返回错误（页面已经不在）时同样断开。请求本身不合格时直接返回错误，不发消息。
func (p *Proxy) Stream(ctx context.Context, req *Request, emit func(StreamEvent) error) error {
	hr, server, err := p.newRequest(ctx, req)
	if err != nil {
		return err
	}
	go func() {
		resp, err := p.streamClient.Do(hr)
		if err != nil {
			p.afterResponse(server, req, nil)
			if ctx.Err() == nil {
				p.log.Warn("流式请求失败", "path", req.Path, "err", err)
				_ = emit(StreamEvent{"event": "error", "kind": KindNetwork, "message": describeNetError(err)})
			}
			return
		}
		defer resp.Body.Close()
		p.afterResponse(server, req, resp)
		p.log.Debug("api_stream 开始", "path", req.Path, "status", resp.StatusCode)
		if emit(StreamEvent{"event": "open", "status": resp.StatusCode, "headers": pickResponseHeaders(resp.Header)}) != nil {
			return
		}
		buf := make([]byte, streamReadSize)
		var carry []byte
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				var text string
				text, carry = splitUTF8(carry, buf[:n])
				if text != "" {
					if emit(StreamEvent{"event": "chunk", "data": text}) != nil {
						return
					}
				}
			}
			if rerr == nil {
				continue
			}
			if ctx.Err() != nil {
				p.log.Debug("api_stream 已取消", "path", req.Path)
				return
			}
			if errors.Is(rerr, io.EOF) {
				if len(carry) > 0 {
					if emit(StreamEvent{"event": "chunk", "data": string(carry)}) != nil {
						return
					}
				}
				p.log.Debug("api_stream 结束", "path", req.Path)
				_ = emit(StreamEvent{"event": "end"})
				return
			}
			p.log.Info("api_stream 中断", "path", req.Path, "err", rerr)
			_ = emit(StreamEvent{"event": "error", "kind": KindNetwork, "message": "和服务器的连接中断了"})
			return
		}
	}()
	return nil
}

// describeNetError 把连不上的原因写成给人看的一句话。
func describeNetError(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "连接服务器超时"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "找不到这个服务器（主机名不对，或网络不通）"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "actively refused"):
		return "服务器拒绝连接（服务没有运行，或端口不对）"
	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "network is unreachable"):
		return "网络不通，连不到服务器"
	case strings.Contains(msg, "connection reset"), strings.Contains(msg, "EOF"):
		return "和服务器的连接被断开"
	}
	return "无法连接服务器：" + msg
}
