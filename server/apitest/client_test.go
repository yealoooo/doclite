package apitest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"testing"

	"github.com/google/uuid"
)

// Client 是一个浏览器：自己的 Cookie 罐，可以加固定的请求头（X-Client-Type、X-Client-Version）。
type Client struct {
	t      *testing.T
	base   string
	hc     *http.Client
	Header http.Header
}

// Anon 是没登录的客户端。
func (e *Env) Anon() *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{t: e.t, base: e.URL, hc: &http.Client{Jar: jar}, Header: http.Header{}}
}

// Login 用登录名和密码登录，失败时测试失败。
func (e *Env) Login(username, password string) *Client {
	e.t.Helper()
	c := e.Anon()
	c.Post("/api/auth/login", map[string]any{"username": username, "password": password}).Expect(http.StatusOK)
	return c
}

// Admin 是已登录的初始管理员（同一个 Env 里复用）。
func (e *Env) Admin() *Client {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.admin == nil {
		e.admin = e.Login(AdminUsername, AdminPassword)
	}
	return e.admin
}

// Response 是一次请求的结果。
type Response struct {
	t      *testing.T
	method string
	path   string
	Status int
	Header http.Header
	Body   []byte
}

// Do 发请求：body 不为 nil 时按 JSON 发送（string 和 []byte 原样发送，Content-Type 仍是 application/json）。
func (c *Client) Do(method, path string, body any) *Response {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = bytes.NewReader([]byte(b))
		case []byte:
			rd = bytes.NewReader(b)
		default:
			data, err := json.Marshal(body)
			if err != nil {
				c.t.Fatal(err)
			}
			rd = bytes.NewReader(data)
		}
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range c.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	res, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s：%v", method, path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return &Response{t: c.t, method: method, path: path, Status: res.StatusCode, Header: res.Header, Body: data}
}

func (c *Client) Get(path string) *Response            { c.t.Helper(); return c.Do("GET", path, nil) }
func (c *Client) Post(path string, body any) *Response { c.t.Helper(); return c.Do("POST", path, body) }
func (c *Client) Put(path string, body any) *Response  { c.t.Helper(); return c.Do("PUT", path, body) }
func (c *Client) Patch(path string, body any) *Response {
	c.t.Helper()
	return c.Do("PATCH", path, body)
}
func (c *Client) Delete(path string) *Response { c.t.Helper(); return c.Do("DELETE", path, nil) }

// Expect 要求状态码是 status，否则测试失败（打出响应体）。
func (r *Response) Expect(status int) *Response {
	r.t.Helper()
	if r.Status != status {
		r.t.Fatalf("%s %s：状态码 %d，期望 %d；响应：%s", r.method, r.path, r.Status, status, r.Body)
	}
	return r
}

// ExpectError 要求是错误响应：状态码 status、错误码 code，而且 message 不为空。
func (r *Response) ExpectError(status int, code string) *Response {
	r.t.Helper()
	r.Expect(status)
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(r.Body, &body); err != nil {
		r.t.Fatalf("%s %s：错误体不是 JSON：%s", r.method, r.path, r.Body)
	}
	if body.Code != code || body.Message == "" {
		r.t.Fatalf("%s %s：错误码 %q（message %q），期望 %q", r.method, r.path, body.Code, body.Message, code)
	}
	return r
}

// JSON 把响应体解析进 v。
func (r *Response) JSON(v any) *Response {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		r.t.Fatalf("%s %s：解析响应失败：%v；响应：%s", r.method, r.path, err, r.Body)
	}
	return r
}

// Code 是错误体的 code（不是错误响应时为空）。
func (r *Response) Code() string {
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(r.Body, &body)
	return body.Code
}

// Decode 把响应体解析成 T。
func Decode[T any](r *Response) T {
	r.t.Helper()
	var v T
	r.JSON(&v)
	return v
}

// ID 取响应体里的 id 字段。
func (r *Response) ID() uuid.UUID {
	r.t.Helper()
	var v struct {
		ID uuid.UUID `json:"id"`
	}
	r.JSON(&v)
	if v.ID == uuid.Nil {
		r.t.Fatalf("%s %s：响应里没有 id：%s", r.method, r.path, r.Body)
	}
	return v.ID
}
