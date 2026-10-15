package proxy

import (
	"net/http"
	"net/url"
	"strings"
)

// Request 是页面交给 `api_fetch`、`api_stream` 的请求（web/src/lib/shell.ts 的 ShellRequest）。
type Request struct {
	Method string `json:"method"`
	// Path 以 /api/ 开头，不带查询串。
	Path string `json:"path"`
	// Query 是查询参数的键值对（顺序保留）。
	Query [][]string `json:"query"`
	// Headers 只能是 allowedRequestHeaders 里的几个。
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body"`
}

// Response 是 `api_fetch` 的结果（web/src/lib/shell.ts 的 ShellResponse）。
type Response struct {
	Status int `json:"status"`
	// Headers 只有 allowedResponseHeaders 里的几个，名字小写。
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// 页面能带的请求头。会话 Cookie、X-Client-Type、X-Client-Version 由壳加，页面不能自己加。
var allowedRequestHeaders = map[string]bool{
	"accept":        true,
	"content-type":  true,
	"if-match":      true,
	"if-none-match": true,
	"last-event-id": true,
}

// 交给页面的响应头。Set-Cookie 永远不给（页面拿不到会话令牌）。
var allowedResponseHeaders = []string{
	"content-type",
	"content-disposition",
	"etag",
	"last-modified",
	"cache-control",
	"retry-after",
}

var allowedMethods = map[string]bool{
	http.MethodGet:    true,
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

// 请求体的上限（页面经代理发的都是 JSON；上传原图走上传队列，不经代理）。
const maxRequestBody = 16 << 20

// BuildURL 核对页面的请求，拼出要访问的完整地址（server 是规整过的 origin）。
//
// 只许 `/api/` 下的路径：路径里只能有 URL 允许的字符（不能有 `?`、`#`、`\`、空白），不能有空段、`.`、`..`，
// 也不能用 `%2e`、`%2f`、`%5c` 绕开；拼好后再核对一次主机和路径没变。
func BuildURL(server string, req *Request) (string, error) {
	if !allowedMethods[req.Method] {
		return "", InvalidRequest("不支持的请求方法：" + req.Method)
	}
	if err := checkPath(req.Path); err != nil {
		return "", err
	}
	for name := range req.Headers {
		if !allowedRequestHeaders[strings.ToLower(name)] {
			return "", InvalidRequest("页面不能设置请求头 " + name)
		}
	}
	if req.Body != nil && len(*req.Body) > maxRequestBody {
		return "", InvalidRequest("请求体太大")
	}
	base, err := url.Parse(server)
	if err != nil || base.Host == "" {
		return "", NotConfigured()
	}
	var qs strings.Builder
	for _, kv := range req.Query {
		if len(kv) != 2 {
			return "", InvalidRequest("查询参数应为 [名, 值] 的数组")
		}
		if kv[0] == "" {
			return "", InvalidRequest("查询参数的名字不能为空")
		}
		if qs.Len() > 0 {
			qs.WriteByte('&')
		}
		qs.WriteString(url.QueryEscape(kv[0]))
		qs.WriteByte('=')
		qs.WriteString(url.QueryEscape(kv[1]))
	}
	full := server + req.Path
	if qs.Len() > 0 {
		full += "?" + qs.String()
	}
	u, err := url.Parse(full)
	if err != nil {
		return "", InvalidRequest("请求地址格式不对")
	}
	if u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.EscapedPath() != req.Path {
		return "", InvalidRequest("只能访问已配置服务器上 /api/ 下的路径")
	}
	return u.String(), nil
}

func checkPath(p string) error {
	bad := InvalidRequest("只能访问已配置服务器上 /api/ 下的路径")
	if !strings.HasPrefix(p, "/api/") || len(p) > 2048 {
		return bad
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("-._~/:@!$&'()*+,;=%", c) >= 0:
		default:
			return bad
		}
	}
	lower := strings.ToLower(p)
	for _, enc := range []string{"%2e", "%2f", "%5c", "%00"} {
		if strings.Contains(lower, enc) {
			return bad
		}
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return bad
		}
	}
	return nil
}

// pickResponseHeaders 取出交给页面的响应头（名字小写）。
func pickResponseHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(allowedResponseHeaders))
	for _, name := range allowedResponseHeaders {
		if v := h.Get(name); v != "" {
			out[name] = v
		}
	}
	return out
}
