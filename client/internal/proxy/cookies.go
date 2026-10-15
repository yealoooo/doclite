package proxy

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"doclite/client/internal/appdata"
)

// Cookie 是罐里的一条 Cookie（只记会用到的属性）。
type Cookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Expires 为零表示会话 Cookie（壳退出前有效；server 的会话 Cookie 都带 Max-Age）。
	Expires time.Time `json:"expires,omitzero"`
	Secure  bool      `json:"secure,omitempty"`
}

// cookieFile 是罐落盘的格式（数据目录下的 cookies.json）。
type cookieFile struct {
	// Server 是这些 Cookie 所属的服务器（origin）。和当前服务器不同时整罐作废。
	Server  string   `json:"server"`
	Cookies []Cookie `json:"cookies"`
}

// Jar 是壳的会话 Cookie 罐：只有一个服务器（已配置的那个），按名字存。
//
// 页面经代理发的请求、以后的上传队列和取图都用它，所以页面、热文件夹、缓存共用一个会话。
// 退出登录或换服务器时清空（requirements 第 6 节"会话"）。罐存在数据目录的 cookies.json（权限 0600），
// 壳重启后仍是登录状态（会话本身 12 小时到期，不续期）。
type Jar struct {
	mu      sync.Mutex
	path    string // 落盘的文件；空表示只在内存里（测试用）
	server  string
	cookies map[string]Cookie
}

// OpenJar 读取罐（文件不存在、读不了、不是这个服务器的都当空罐）。
func OpenJar(path, server string) *Jar {
	j := &Jar{path: path, server: server, cookies: map[string]Cookie{}}
	if path == "" {
		return j
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return j
	}
	var f cookieFile
	if json.Unmarshal(data, &f) != nil || f.Server != server {
		return j
	}
	for _, c := range f.Cookies {
		j.cookies[c.Name] = c
	}
	return j
}

// Server 是罐所属的服务器。
func (j *Jar) Server() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.server
}

// Reset 清空罐并换到另一个服务器（换服务器、退出登录时调用）。
func (j *Jar) Reset(server string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.server = server
	j.cookies = map[string]Cookie{}
	j.saveLocked()
}

// Header 返回请求要带的 Cookie 头（过期的不带并顺手删掉；Secure 的只在 https 下带）。没有时返回空串。
func (j *Jar) Header(now time.Time, https bool) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	names := make([]string, 0, len(j.cookies))
	changed := false
	for name, c := range j.cookies {
		if !c.Expires.IsZero() && !now.Before(c.Expires) {
			delete(j.cookies, name)
			changed = true
			continue
		}
		if c.Secure && !https {
			continue
		}
		names = append(names, name)
	}
	if changed {
		j.saveLocked()
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+j.cookies[name].Value)
	}
	return strings.Join(parts, "; ")
}

// Has 判断罐里有没有某个（没过期的）Cookie。
func (j *Jar) Has(name string, now time.Time) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	c, ok := j.cookies[name]
	return ok && (c.Expires.IsZero() || now.Before(c.Expires))
}

// Apply 记下响应里的 Set-Cookie（server 是发出请求时罐所属的服务器；罐已经换了服务器时不记）。
// Max-Age ≤ 0 或 Expires 已过的表示删除。
func (j *Jar) Apply(server string, setCookies []string, now time.Time) {
	if len(setCookies) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if server != j.server {
		return
	}
	changed := false
	for _, line := range setCookies {
		c, err := http.ParseSetCookie(line)
		if err != nil || c.Name == "" {
			continue
		}
		var expires time.Time
		switch {
		case c.MaxAge < 0:
			expires = now // 立即过期
		case c.MaxAge > 0:
			expires = now.Add(time.Duration(c.MaxAge) * time.Second)
		case !c.Expires.IsZero():
			expires = c.Expires
		}
		if !expires.IsZero() && !now.Before(expires) {
			if _, ok := j.cookies[c.Name]; ok {
				delete(j.cookies, c.Name)
				changed = true
			}
			continue
		}
		j.cookies[c.Name] = Cookie{Name: c.Name, Value: c.Value, Expires: expires, Secure: c.Secure}
		changed = true
	}
	if changed {
		j.saveLocked()
	}
}

func (j *Jar) saveLocked() {
	if j.path == "" {
		return
	}
	f := cookieFile{Server: j.server, Cookies: make([]Cookie, 0, len(j.cookies))}
	for _, c := range j.cookies {
		f.Cookies = append(f.Cookies, c)
	}
	sort.Slice(f.Cookies, func(a, b int) bool { return f.Cookies[a].Name < f.Cookies[b].Name })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	_ = appdata.WriteFileAtomic(j.path, data, 0o600)
}
