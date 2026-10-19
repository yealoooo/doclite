package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"doclite/client/internal/proxy"
)

// Session 是和页面共用的会话（proxy.Proxy 实现）：同一个服务器、同一个 Cookie 罐、同一套请求头
// （X-Client-Type、X-Client-Version）。requirements 第 6 节：热文件夹、缓存、页面共用会话。
type Session interface {
	// Server 是已配置的服务器（origin）；没配置时为空。
	Server() string
	// HasSession：罐里有没有（没过期的）会话 Cookie。
	HasSession() bool
	// Attach 给请求加上壳的请求头和会话 Cookie，返回罐所属的服务器。还没配置服务器时报错。
	Attach(req *http.Request) (string, error)
	// Absorb 记下响应里的 Set-Cookie（server 是 Attach 返回的服务器）。
	Absorb(server string, resp *http.Response)
}

// APIError 是 server 返回的错误（错误体 `{code, message, details?}`）。
type APIError struct {
	Status  int
	Code    string
	Message string
	Details json.RawMessage
}

func (e *APIError) Error() string { return fmt.Sprintf("HTTP %d %s：%s", e.Status, e.Code, e.Message) }

// NetError 是连不上服务器、请求中途断开、超时。
type NetError struct {
	Msg string
	Err error
}

func (e *NetError) Error() string { return e.Msg }
func (e *NetError) Unwrap() error { return e.Err }

// errServerChanged：发请求前发现服务器换了（这一项属于原来的服务器，放回排队，换回来后接着传）。
var errServerChanged = errors.New("服务器地址换了")

// errNoServer：还没配置服务器。
var errNoServer = errors.New("还没有配置服务器地址")

// Client 发上传队列、热文件夹、重扫要用的请求。
type Client struct {
	sess Session
	http *http.Client
}

// NewClient 建客户端。上传原图可能要很久，不设总时限：等响应头最多 10 分钟（server 收完还要传 S3），发送时
// 60 秒没有进展算中断（见 [Client.Upload]）。
func NewClient(sess Session) *Client {
	tr := &http.Transport{
		Proxy:                 nil, // 内网直连 server，不走系统代理（和会话代理一样）
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Minute,
		ExpectContinueTimeout: 0,
	}
	return &Client{
		sess: sess,
		http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

// Server 是当前服务器。
func (c *Client) Server() string { return c.sess.Server() }

// HasSession：有没有会话 Cookie。
func (c *Client) HasSession() bool { return c.sess.HasSession() }

// maxJSONResponse：JSON 响应的上限。
const maxJSONResponse = 4 << 20

// request 是一次请求。expect 不为空时，罐所属的服务器不是它就不发（errServerChanged）。
type request struct {
	method      string
	path        string
	expect      string
	body        io.Reader
	length      int64
	contentType string
	out         any
}

func (c *Client) do(ctx context.Context, r request) (int, error) {
	server := c.sess.Server()
	if server == "" {
		return 0, errNoServer
	}
	if r.expect != "" && r.expect != server {
		return 0, errServerChanged
	}
	hr, err := http.NewRequestWithContext(ctx, r.method, server+r.path, r.body)
	if err != nil {
		return 0, err
	}
	if r.body != nil {
		hr.ContentLength = r.length
		hr.Header.Set("Content-Type", r.contentType)
	}
	hr.Header.Set("Accept", "application/json")
	attached, err := c.sess.Attach(hr)
	if err != nil {
		return 0, err
	}
	if attached != server {
		return 0, errServerChanged
	}
	resp, err := c.http.Do(hr)
	if err != nil {
		return 0, &NetError{Msg: describeNetError(ctx, err), Err: err}
	}
	defer resp.Body.Close()
	c.sess.Absorb(server, resp)
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONResponse))
	if err != nil {
		return resp.StatusCode, &NetError{Msg: "读取服务器的响应时连接中断", Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, parseAPIError(resp.StatusCode, data)
	}
	if r.out != nil {
		if err := json.Unmarshal(data, r.out); err != nil {
			return resp.StatusCode, fmt.Errorf("服务器的响应格式不对：%w", err)
		}
	}
	return resp.StatusCode, nil
}

func parseAPIError(status int, data []byte) *APIError {
	var body struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	}
	e := &APIError{Status: status}
	if json.Unmarshal(data, &body) == nil && body.Code != "" {
		e.Code, e.Message, e.Details = body.Code, body.Message, body.Details
		if string(e.Details) == "null" {
			e.Details = nil
		}
		return e
	}
	e.Code = fmt.Sprintf("http_%d", status)
	e.Message = fmt.Sprintf("服务器返回 HTTP %d", status)
	return e
}

// describeNetError：取消、60 秒没有进展另说，其余同会话代理（proxy.DescribeNetError）。
func describeNetError(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), errStalled) {
			return "上传中断：60 秒没有进展"
		}
		return "请求已取消"
	}
	return proxy.DescribeNetError(err)
}

func jsonBody(v any) (io.Reader, int64) {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b), int64(len(b))
}

// Folder 是 `POST /api/folders` 的结果里用到的部分。
type Folder struct {
	ID         string `json:"id"`
	FolderName string `json:"folder_name"`
}

// CreateFolder 在 parentID 下按名字取或建子文件夹（`if_exists: "return"`）。
func (c *Client) CreateFolder(ctx context.Context, expect, parentID, name string) (*Folder, error) {
	body, n := jsonBody(map[string]any{"parent_id": parentID, "folder_name": name, "if_exists": "return"})
	var f Folder
	_, err := c.do(ctx, request{method: http.MethodPost, path: "/api/folders", expect: expect, body: body, length: n,
		contentType: "application/json", out: &f})
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// CheckResult 是预检 `POST /api/folders/{id}/files:check` 的结果。
type CheckResult struct {
	Duplicate       json.RawMessage `json:"duplicate"`
	AlreadyUploaded bool            `json:"already_uploaded"`
	FinalName       string          `json:"final_name"`
}

// Check 是预检：要不要传、传上去叫什么。
func (c *Client) Check(ctx context.Context, expect, folderID, fileName, sha string, size int64) (*CheckResult, error) {
	body, n := jsonBody(map[string]any{"file_name": fileName, "sha256": sha, "size": size})
	var r CheckResult
	_, err := c.do(ctx, request{method: http.MethodPost, path: "/api/folders/" + folderID + "/files:check", expect: expect,
		body: body, length: n, contentType: "application/json", out: &r})
	if err != nil {
		return nil, err
	}
	if string(r.Duplicate) == "null" {
		r.Duplicate = nil
	}
	return &r, nil
}

// Me 是 `GET /api/me`：上传时网络出错，先用它看是不是会话失效（401）或要升级（426）。
func (c *Client) Me(ctx context.Context, expect string) error {
	_, err := c.do(ctx, request{method: http.MethodGet, path: "/api/me", expect: expect})
	return err
}

// Ingested 是上传（201 新建、200 断网重试命中）和重扫（201）的响应里用到的字段；Raw 是完整的响应体。
type Ingested struct {
	FileID   string   `json:"file_id"`
	FileName string   `json:"file_name"`
	SortNo   int64    `json:"sort_no"`
	FileSize int64    `json:"file_size"`
	HasCover bool     `json:"has_cover"`
	Warnings []string `json:"warnings"`
	Raw      json.RawMessage
}

// UploadInput 是一次上传（或重扫）的内容。
type UploadInput struct {
	Path     string // 本机文件
	FileName string
	SHA256   string
	Size     int64
	Cover    []byte // nil 表示不带
}

// errStalled：发送原图时 60 秒没有进展。
var errStalled = errors.New("上传 60 秒没有进展")

// stallTimeout：发送原图时这么久没有进展就断开重来。
const stallTimeout = 60 * time.Second

// Upload 把一个本机文件按 multipart（file_name、content_sha256、cover?、file）流式发到 path
// （`/api/folders/{id}/files` 或 `/api/files/{fid}/rescan`）。progress（可为 nil）在发送原图的过程中被调用
// （已发出的原图字节数）。文件在发送过程中变短了返回 [ErrFileChanged]。
func (c *Client) Upload(ctx context.Context, expect, path string, in UploadInput, progress func(sent int64)) (*Ingested, error) {
	body, err := NewBody(in.FileName, in.SHA256, in.Cover, in.Size)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(in.Path)
	if err != nil {
		return nil, &localError{code: ErrCodeFileUnreadable, err: err}
	}
	defer f.Close()

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	pr := &progressReader{r: f, progress: progress}
	pr.touch()
	// 看门狗：网络卡住时 Transport 停在写请求体上，读文件的 Read 不再被调用。
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if !pr.sending() {
					continue
				}
				if time.Since(pr.last()) > stallTimeout {
					cancel(errStalled)
					return
				}
			}
		}
	}()

	var out json.RawMessage
	_, err = c.do(ctx, request{method: http.MethodPost, path: path, expect: expect, body: body.Reader(pr), length: body.Length,
		contentType: body.ContentType, out: &out})
	pr.done()
	if err != nil {
		if errors.Is(err, ErrFileChanged) {
			return nil, ErrFileChanged
		}
		return nil, err
	}
	var ing Ingested
	if err := json.Unmarshal(out, &ing); err != nil {
		return nil, fmt.Errorf("服务器的响应格式不对：%w", err)
	}
	ing.Raw = out
	return &ing, nil
}

// uploadDroppingCover 是 [Client.Upload]；server 不收封面（400 invalid_cover）时去掉封面再传一次（封面可以缺，
// 不能因封面丢页；不算重试次数），dropped 收到 server 的说明。上传队列和重扫件共用。
func (c *Client) uploadDroppingCover(ctx context.Context, expect, path string, in UploadInput, progress func(sent int64),
	dropped func(msg string)) (*Ingested, error) {
	for {
		ing, err := c.Upload(ctx, expect, path, in, progress)
		var ae *APIError
		if err != nil && in.Cover != nil && errors.As(err, &ae) && ae.Code == "invalid_cover" {
			in.Cover = nil
			dropped(ae.Message)
			continue
		}
		return ing, err
	}
}

// probeSession：上传时网络出错，先问一次 GET /api/me（10 秒）：会话失效（401）、要升级（426）或许可证挡住
// （403，M7-1）时返回那个错误，否则 nil（按网络错误处理）。上传队列和重扫件共用。
func (c *Client) probeSession(ctx context.Context, server string) *APIError {
	mctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var me *APIError
	if errors.As(c.Me(mctx, server), &me) && (me.Status == http.StatusUnauthorized ||
		me.Status == http.StatusUpgradeRequired || licenseBlocked(me)) {
		return me
	}
	return nil
}

// progressReader 数已发出的原图字节数，记下最后一次有进展的时间。
type progressReader struct {
	r        io.Reader
	progress func(int64)

	mu       sync.Mutex
	sent     int64
	lastAt   time.Time
	finished bool
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.mu.Lock()
		p.sent += int64(n)
		p.lastAt = time.Now()
		sent := p.sent
		p.mu.Unlock()
		if p.progress != nil {
			p.progress(sent)
		}
	}
	if err == io.EOF {
		p.done()
	}
	return n, err
}

func (p *progressReader) touch() {
	p.mu.Lock()
	p.lastAt = time.Now()
	p.mu.Unlock()
}

func (p *progressReader) last() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastAt
}

// sending：原图还没发完（发完以后等响应不算卡住，由 ResponseHeaderTimeout 管）。
func (p *progressReader) sending() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.finished
}

func (p *progressReader) done() {
	p.mu.Lock()
	p.finished = true
	p.mu.Unlock()
}

// localError 是本机文件的问题（读不了、不见了、变了）。
type localError struct {
	code string
	err  error
}

func (e *localError) Error() string { return e.err.Error() }
func (e *localError) Unwrap() error { return e.err }
