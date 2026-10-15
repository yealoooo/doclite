package proxy

import (
	"net/http"
	"strings"
	"time"
)

// 壳自己发的请求（M6-2 的上传队列、热文件夹、重扫件）和页面共用会话：同一个 Cookie 罐、同一套请求头
// （requirements 第 6 节"会话"：热文件夹、缓存、页面共用会话）。这些请求的地址由壳自己拼（已配置服务器的 /api/ 下），
// 不经 BuildURL 的核对；响应的 Set-Cookie 照样记进罐里。

// Attach 给壳自己发的请求加上 X-Client-Type、X-Client-Version 和会话 Cookie，返回罐所属的服务器。
// 请求的地址必须在这个服务器的 /api/ 下（否则不加 Cookie，返回 invalid_request）；还没配置服务器时返回 not_configured。
func (p *Proxy) Attach(hr *http.Request) (string, error) {
	server := p.jar.Server()
	if server == "" {
		return "", NotConfigured()
	}
	if !strings.HasPrefix(hr.URL.String(), server+"/api/") {
		return "", InvalidRequest("只能访问已配置服务器上 /api/ 下的路径")
	}
	p.addShellHeaders(hr, server)
	return server, nil
}

// Absorb 记下壳自己发的请求的响应里的 Set-Cookie（server 是 Attach 返回的服务器；罐已经换了服务器时不记）。
func (p *Proxy) Absorb(server string, resp *http.Response) {
	if resp != nil {
		p.jar.Apply(server, resp.Header.Values("Set-Cookie"), time.Now())
	}
}
