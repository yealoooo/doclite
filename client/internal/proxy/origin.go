package proxy

import (
	"net"
	"net/url"
	"strings"
)

// NormalizeServerURL 把用户填的服务器地址规整成 origin（`http://192.168.1.10:9080`）。
//
//   - 没写协议时按 http（内网多是 http）；只接受 http、https；
//   - 不能带用户名密码、查询串、片段；路径只能是空或 `/`（填的是服务器，不是某个页面）；
//   - 协议和主机名转小写，去掉结尾的 `/`；端口原样保留（http 的 80、https 的 443 去掉）。
//
// 格式不对时返回 InvalidRequest。
func NormalizeServerURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", InvalidRequest("请填写服务器地址")
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return "", InvalidRequest("服务器地址里不能有空格或控制字符")
		}
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", InvalidRequest("服务器地址格式不对，例如 http://192.168.1.10:9080")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", InvalidRequest("服务器地址只能是 http 或 https")
	}
	if u.User != nil {
		return "", InvalidRequest("服务器地址里不能带用户名或密码")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(s, "#") {
		return "", InvalidRequest("服务器地址里不能带 ? 或 #")
	}
	if u.Path != "" && u.Path != "/" {
		return "", InvalidRequest("只填服务器的地址和端口，不要带路径，例如 http://192.168.1.10:9080")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", InvalidRequest("服务器地址里没有主机名")
	}
	port := u.Port()
	if port != "" {
		n := 0
		for _, c := range port {
			if c < '0' || c > '9' {
				return "", InvalidRequest("端口号不对")
			}
			n = n*10 + int(c-'0')
			if n > 65535 {
				return "", InvalidRequest("端口号不对")
			}
		}
		if n == 0 {
			return "", InvalidRequest("端口号不对")
		}
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			port = ""
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", InvalidRequest("端口号不对")
	}
	hostport := host
	if strings.Contains(host, ":") { // IPv6
		hostport = "[" + host + "]"
	}
	if port != "" {
		hostport = net.JoinHostPort(host, port)
	}
	return scheme + "://" + hostport, nil
}
