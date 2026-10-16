package shell

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

const invokeBody = `{"object":0,"method":0,"args":{"call-id":"c1","methodName":"doclite/client/internal/shell.Bridge.Invoke","args":["shell_info",{}]}}`

// guarded 返回过了白名单的中间件，以及交给 Wails 的请求体（nil 表示没交给 Wails）。
func guarded() (http.Handler, *[]byte) {
	var got []byte
	called := &got
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(headerChunkID) != "" {
			panic("分块的头不该交给 Wails")
		}
		b, _ := io.ReadAll(r.Body)
		*called = append([]byte{}, b...)
		if *called == nil {
			*called = []byte{}
		}
		w.WriteHeader(http.StatusOK)
	})
	return newRuntimeGuard(slog.New(slog.DiscardHandler)).wrap(next), called
}

func runtimeReq(method, target, ctype, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	return r
}

func TestRuntimeGuard(t *testing.T) {
	cases := []struct {
		name   string
		req    *http.Request
		status int
	}{
		{"Bridge.Invoke", runtimeReq("POST", "/wails/runtime", "application/json", invokeBody), 200},
		{"带 charset", runtimeReq("POST", "/wails/runtime", "application/json; charset=utf-8", invokeBody), 200},
		{"CancelCall", runtimeReq("POST", "/wails/runtime", "application/json", `{"object":10,"method":0,"args":{"call-id":"c1"}}`), 200},
		{"Browser.OpenURL", runtimeReq("POST", "/wails/runtime", "application/json", `{"object":9,"method":0,"args":{"url":"smb://evil/x"}}`), 403},
		{"Clipboard", runtimeReq("POST", "/wails/runtime", "application/json", `{"object":1,"method":1}`), 403},
		{"Application.Quit", runtimeReq("POST", "/wails/runtime", "application/json", `{"object":2,"method":2}`), 403},
		{"Dialog", runtimeReq("POST", "/wails/runtime", "application/json", `{"object":5,"method":4,"args":{}}`), 403},
		{"Window", runtimeReq("POST", "/wails/runtime", "application/json", `{"object":6,"method":0}`), 403},
		{"Events.Emit", runtimeReq("POST", "/wails/runtime", "application/json", `{"object":3,"method":0,"args":{"name":"x"}}`), 403},
		{"Call 别的方法", runtimeReq("POST", "/wails/runtime", "application/json", `{"object":0,"method":1,"args":{"call-id":"c","methodName":"doclite/client/internal/shell.Bridge.Invoke"}}`), 403},
		{"别的绑定方法", runtimeReq("POST", "/wails/runtime", "application/json", `{"object":0,"method":0,"args":{"call-id":"c","methodName":"x.Y.Z"}}`), 403},
		{"按 id 调绑定方法", runtimeReq("POST", "/wails/runtime", "application/json", `{"object":0,"method":0,"args":{"call-id":"c","methodID":12345}}`), 403},
		// 重复的键和 Wails 一样以最后一个为准（键名不分大小写）：最后是 9 就拒绝，最后是 0 才放行。
		{"重复的键，最后是 Browser", runtimeReq("POST", "/wails/runtime", "application/json",
			`{"object":0,"method":0,"args":{"call-id":"c","methodName":"doclite/client/internal/shell.Bridge.Invoke"},"Object":9}`), 403},
		{"重复的方法名，最后是别的", runtimeReq("POST", "/wails/runtime", "application/json",
			`{"object":0,"method":0,"args":{"call-id":"c","methodName":"doclite/client/internal/shell.Bridge.Invoke","MethodName":"x.Y.Z"}}`), 403},
		{"GET 加查询串", runtimeReq("GET", "/wails/runtime?object=9&method=0&args=%7B%22url%22%3A%22smb%3A%2F%2Fx%22%7D", "", ""), 403},
		{"POST 空体加查询串", runtimeReq("POST", "/wails/runtime?object=9&method=0", "application/json", ""), 403},
		{"POST 空体", runtimeReq("POST", "/wails/runtime", "application/json", ""), 403},
		{"text/plain 表单", runtimeReq("POST", "/wails/runtime", "text/plain", invokeBody), 403},
		{"urlencoded 表单", runtimeReq("POST", "/wails/runtime", "application/x-www-form-urlencoded", "object=9&method=0"), 403},
		{"不是 JSON", runtimeReq("POST", "/wails/runtime", "application/json", "object=9"), 403},
	}
	for _, c := range cases {
		h, got := guarded()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, c.req)
		if w.Code != c.status {
			t.Errorf("%s：%d，应为 %d（%s）", c.name, w.Code, c.status, w.Body.String())
		}
		if (c.status == 200) != (*got != nil) {
			t.Errorf("%s：交给 Wails 的是 %q", c.name, *got)
		}
	}

	// 别的地址不管（运行时脚本、流、取图……由后面的处理器处理）。
	h, got := guarded()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/wails/runtime.js", nil))
	if w.Code != 200 || *got == nil {
		t.Errorf("/wails/runtime.js 应该照常交给后面：%d", w.Code)
	}
}

// 分块的请求：收齐了才校验，第一块看着没问题也不行。
func TestRuntimeGuardChunks(t *testing.T) {
	send := func(h http.Handler, id string, body string, parts int) []int {
		var codes []int
		size := (len(body) + parts - 1) / parts
		for i := 0; i < parts; i++ {
			end := min((i+1)*size, len(body))
			r := runtimeReq("POST", "/wails/runtime", "application/json", body[i*size:end])
			r.Header.Set(headerChunkID, id)
			r.Header.Set(headerChunkIndex, strconv.Itoa(i))
			r.Header.Set(headerChunkTotal, strconv.Itoa(parts))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			codes = append(codes, w.Code)
		}
		return codes
	}

	h, got := guarded()
	big := strings.Replace(invokeBody, `{}]`, `{"pad":"`+strings.Repeat("x", 3000)+`"}]`, 1)
	codes := send(h, "ok1", big, 3)
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 200 || !bytes.Equal(*got, []byte(big)) {
		t.Fatalf("分块的 Bridge.Invoke 应该拼好交给 Wails：%v，%d 字节", codes, len(*got))
	}

	// 前面是 Call 的样子，最后一块里把 object 改成 Browser。
	h, got = guarded()
	evil := `{"object":0,"method":0,"args":{"call-id":"c","methodName":"doclite/client/internal/shell.Bridge.Invoke","args":["shell_info",{}]},"pad":"` +
		strings.Repeat("y", 2000) + `","object":9}`
	codes = send(h, "evil", evil, 4)
	if codes[3] != http.StatusForbidden || *got != nil {
		t.Fatalf("拼起来是 Browser 的分块请求应该被拒：%v", codes)
	}

	// 分块的头不对。
	h, _ = guarded()
	r := runtimeReq("POST", "/wails/runtime", "application/json", "{}")
	r.Header.Set(headerChunkID, "bad")
	r.Header.Set(headerChunkIndex, "0")
	r.Header.Set(headerChunkTotal, "99999")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("chunk total 太大应该 400：%d", w.Code)
	}
	r = runtimeReq("POST", "/wails/runtime", "application/json", invokeBody)
	r.Header.Set(headerChunkIndex, "0")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("没有 chunk id 的分块应该 400：%d", w.Code)
	}
}
