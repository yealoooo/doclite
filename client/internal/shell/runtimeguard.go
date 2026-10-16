package shell

// 桥的入口 /wails/runtime 的白名单（M7-6 审查）。
//
// Wails 的运行时在 POST /wails/runtime 上不只有绑定方法（Call），还有 Browser.OpenURL、Clipboard、Dialog、Window、
// Application.Quit 等对象；而且它不看请求方法，请求体为空时从查询串取参数（transport_http.go 的 processBody），
// `<img src="/wails/runtime?object=…">`、text/plain 的表单也能触发。页面只该调 Bridge.Invoke，所以这里在 Wails 之前收紧（审查时页面还来自网络，现在随客户端
// 打包，照样收紧）：
//   - 只许 POST、Content-Type: application/json、没有查询串、请求体非空；
//   - 请求体按 Wails 自己的结构解析（同样的 Go 结构体和 encoding/json：重复的键以最后一个为准、键名不分大小写，
//     和 Wails 看到的一致），只放行页面和客户端自带的页面实际用到的两种：
//       Call（object 0）的 CallBinding（method 0），methodName 是 Bridge.Invoke —— 所有命令都走它；
//       CancelCall（object 10，method 0）—— 运行时取消一个还在进行的 Call（CancellablePromise）。
//     别的对象、方法一律 403；
//   - 运行时把超过 512 KB 的请求体分块发（x-wails-chunk-id、-index、-total）：这里先把各块收齐拼好再按上面校验，
//     放行时把拼好的整个请求体当作一个普通请求交给 Wails（Wails 不再看到分块）；没收齐的块回 200（和 Wails 一样）。
//
// 运行时还有一条不经 HTTP 的通道（macOS 的 webkit.messageHandlers、Windows 的 chrome.webview.postMessage），
// Wails 只认窗口拖动、改大小、wails:runtime:ready 几种消息（WebviewWindow.HandleMessage；AllowSimpleEventEmit 没开），
// 碰不到命令和本机功能。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	runtimePath = "/wails/runtime"
	// invokeMethodName 是页面调的唯一绑定方法（web/src/lib/wails.ts、internal/shellpages/pages/shell.js）。
	invokeMethodName = "doclite/client/internal/shell.Bridge.Invoke"

	// Wails 的运行时对象和方法编号（pkg/application/messageprocessor.go、messageprocessor_call.go）。
	wailsObjectCall       = 0
	wailsObjectCancelCall = 10
	wailsMethodCall       = 0 // CallBinding
	wailsMethodCancel     = 0 // 运行时的 CancelMethod

	// 和 Wails 的 HTTPTransport 一样的上限（transport_http.go）。
	runtimeMaxBody     = 64 << 20
	runtimeChunkMax    = 1 << 20
	runtimeChunkTotal  = 1024
	runtimeChunkTTL    = 30 * time.Second
	runtimeChunkIDMax  = 64
	runtimeChunkSets   = 16
	runtimeChunkGlobal = 2 * runtimeMaxBody

	headerChunkID    = "x-wails-chunk-id"
	headerChunkIndex = "x-wails-chunk-index"
	headerChunkTotal = "x-wails-chunk-total"
)

// wailsRuntimeRequest、wailsCallOptions 和 Wails 解析请求用的结构体一字不差（transport_http.go 的 request、
// bindings.go 的 CallOptions），用同样的 encoding/json 解析，得到的就是 Wails 将要执行的。
type wailsRuntimeRequest struct {
	Object *int            `json:"object"`
	Method *int            `json:"method"`
	Args   json.RawMessage `json:"args"`
}

type wailsCallOptions struct {
	MethodName string            `json:"methodName"`
	Args       []json.RawMessage `json:"args"`
	MethodID   uint32            `json:"methodID"`
}

// checkRuntimeCall 判断一个完整的 /wails/runtime 请求体能不能交给 Wails。
func checkRuntimeCall(body []byte) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return errors.New("请求体是空的")
	}
	var req wailsRuntimeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("请求体不是 Wails 的运行时调用：%v", err)
	}
	if req.Object == nil || req.Method == nil {
		return errors.New("缺少 object 或 method")
	}
	switch *req.Object {
	case wailsObjectCall:
		if *req.Method != wailsMethodCall {
			return fmt.Errorf("不许调 Call 的方法 %d", *req.Method)
		}
		var opts wailsCallOptions
		if len(req.Args) == 0 {
			return errors.New("Call 缺少参数")
		}
		if err := json.Unmarshal(req.Args, &opts); err != nil {
			return fmt.Errorf("Call 的参数格式不对：%v", err)
		}
		if opts.MethodName != invokeMethodName {
			return fmt.Errorf("不许调绑定方法 %q（id %d）", opts.MethodName, opts.MethodID)
		}
		return nil
	case wailsObjectCancelCall:
		if *req.Method != wailsMethodCancel {
			return fmt.Errorf("不许调 CancelCall 的方法 %d", *req.Method)
		}
		return nil
	}
	return fmt.Errorf("不许调 Wails 的运行时对象 %d", *req.Object)
}

// runtimeGuard 是 /wails/runtime 的中间件（见文件开头）。
type runtimeGuard struct {
	log *slog.Logger
	now func() time.Time

	mu    sync.Mutex
	sets  map[string]*chunkSet
	bytes int
}

type chunkSet struct {
	total   int
	parts   map[int][]byte
	size    int
	created time.Time
}

func newRuntimeGuard(log *slog.Logger) *runtimeGuard {
	return &runtimeGuard{log: log, now: time.Now, sets: map[string]*chunkSet{}}
}

func (g *runtimeGuard) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != runtimePath {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost || r.URL.RawQuery != "" {
			g.reject(w, r, http.StatusForbidden, "只许不带查询串的 POST")
			return
		}
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			g.reject(w, r, http.StatusForbidden, "Content-Type 不是 application/json")
			return
		}
		if id := r.Header.Get(headerChunkID); id != "" {
			g.chunk(w, r, next, id)
			return
		}
		if r.Header.Get(headerChunkIndex) != "" || r.Header.Get(headerChunkTotal) != "" {
			g.reject(w, r, http.StatusBadRequest, "分块的请求缺少 chunk id")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, runtimeMaxBody+1))
		if err != nil {
			g.reject(w, r, http.StatusBadRequest, "读请求体失败")
			return
		}
		if len(body) > runtimeMaxBody {
			g.reject(w, r, http.StatusRequestEntityTooLarge, "请求体太大")
			return
		}
		g.forward(w, r, next, body)
	})
}

// forward 校验完整的请求体，通过了就当作一个普通（不分块的）请求交给 Wails。
func (g *runtimeGuard) forward(w http.ResponseWriter, r *http.Request, next http.Handler, body []byte) {
	if err := checkRuntimeCall(body); err != nil {
		g.reject(w, r, http.StatusForbidden, err.Error())
		return
	}
	out := r.Clone(r.Context())
	out.Header.Del(headerChunkID)
	out.Header.Del(headerChunkIndex)
	out.Header.Del(headerChunkTotal)
	out.Header.Set("Content-Length", strconv.Itoa(len(body)))
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	next.ServeHTTP(w, out)
}

// chunk 收一块；收齐了就拼起来校验、交给 Wails。
func (g *runtimeGuard) chunk(w http.ResponseWriter, r *http.Request, next http.Handler, id string) {
	if len(id) > runtimeChunkIDMax {
		g.reject(w, r, http.StatusBadRequest, "chunk id 太长")
		return
	}
	total, err := strconv.Atoi(r.Header.Get(headerChunkTotal))
	if err != nil || total <= 0 || total > runtimeChunkTotal {
		g.reject(w, r, http.StatusBadRequest, "chunk total 不对")
		return
	}
	index, err := strconv.Atoi(r.Header.Get(headerChunkIndex))
	if err != nil || index < 0 || index >= total {
		g.reject(w, r, http.StatusBadRequest, "chunk index 不对")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, runtimeChunkMax+1))
	if err != nil || len(data) > runtimeChunkMax {
		g.reject(w, r, http.StatusBadRequest, "分块太大或读不出来")
		return
	}

	g.mu.Lock()
	g.expireLocked()
	set := g.sets[id]
	if set == nil {
		if len(g.sets) >= runtimeChunkSets {
			g.mu.Unlock()
			g.reject(w, r, http.StatusTooManyRequests, "同时在收的分块请求太多")
			return
		}
		set = &chunkSet{total: total, parts: map[int][]byte{}, created: g.now()}
		g.sets[id] = set
	}
	if set.total != total {
		g.dropLocked(id)
		g.mu.Unlock()
		g.reject(w, r, http.StatusBadRequest, "chunk total 前后不一致")
		return
	}
	if old, ok := set.parts[index]; ok {
		set.size -= len(old)
		g.bytes -= len(old)
	}
	set.parts[index] = data
	set.size += len(data)
	g.bytes += len(data)
	if set.size > runtimeMaxBody || g.bytes > runtimeChunkGlobal {
		g.dropLocked(id)
		g.mu.Unlock()
		g.reject(w, r, http.StatusRequestEntityTooLarge, "分块拼起来太大")
		return
	}
	if len(set.parts) < set.total {
		g.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	}
	indexes := make([]int, 0, len(set.parts))
	for i := range set.parts {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	body := make([]byte, 0, set.size)
	for _, i := range indexes {
		body = append(body, set.parts[i]...)
	}
	g.dropLocked(id)
	g.mu.Unlock()
	g.forward(w, r, next, body)
}

func (g *runtimeGuard) dropLocked(id string) {
	if set := g.sets[id]; set != nil {
		g.bytes -= set.size
		delete(g.sets, id)
	}
}

// expireLocked 丢掉 30 秒还没收齐的分块。
func (g *runtimeGuard) expireLocked() {
	now := g.now()
	for id, set := range g.sets {
		if now.Sub(set.created) > runtimeChunkTTL {
			g.dropLocked(id)
		}
	}
}

func (g *runtimeGuard) reject(w http.ResponseWriter, r *http.Request, status int, why string) {
	g.log.Warn("桥：拦下了不在白名单里的运行时调用", "method", r.Method, "status", status, "why", why)
	http.Error(w, "forbidden: "+why, status)
}
