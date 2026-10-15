package httpx

import (
	"encoding/json"
	"net/http"
)

// HandlerFunc 是接口的处理函数：出错时返回 error，由 [Router] 统一输出错误体。
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Middleware 包一层 http.Handler。
type Middleware func(http.Handler) http.Handler

// Chain 把几个中间件按顺序串起来：Chain(a, b)(h) 等于 a(b(h))。
func Chain(mws ...Middleware) Middleware {
	return func(h http.Handler) http.Handler {
		for i := len(mws) - 1; i >= 0; i-- {
			h = mws[i](h)
		}
		return h
	}
}

// Router 是 `/api` 的路由表，基于标准库 ServeMux 的"方法 + 路径"模式（`GET /api/users/{id}`）。
//
//   - 注册时必须给出访问控制（硬性规则 10：每个接口声明自己需要的菜单码）：auth.Gate 的
//     Public()、Login()、AnyMenu(…)、AllMenus(…)。传 nil 直接 panic，启动时就能发现漏写。
//   - 没有匹配的路径返回 JSON 404 `not_found`；路径对但方法不对返回 JSON 405 `method_not_allowed`。
type Router struct {
	mux *http.ServeMux
}

// NewRouter 建一个空路由表。
func NewRouter() *Router {
	return &Router{mux: http.NewServeMux()}
}

// Handle 注册一个接口。pattern 形如 `GET /api/system/users/{id}`；access 是访问控制中间件。
func (rt *Router) Handle(pattern string, access Middleware, h HandlerFunc) {
	if access == nil {
		panic("httpx: 接口 " + pattern + " 没有声明访问控制（Public / Login / AnyMenu / AllMenus）")
	}
	rt.mux.Handle(pattern, access(h))
}

// ServeHTTP 实现 http.Handler。
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, pattern := rt.mux.Handler(r); pattern == "" {
		// 没有匹配的路由：ServeMux 会写纯文本的 404 或 405（带 Allow 头），改写成统一的错误体。
		rt.mux.ServeHTTP(&jsonFallbackWriter{ResponseWriter: w}, r)
		return
	}
	rt.mux.ServeHTTP(w, r)
}

// ServeHTTP 让 HandlerFunc 实现 http.Handler：返回的错误写成错误体。
func (h HandlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h(w, r); err != nil {
		WriteError(w, r, err)
	}
}

// jsonFallbackWriter 把 ServeMux 自己写的 404、405 纯文本换成 JSON 错误体。
type jsonFallbackWriter struct {
	http.ResponseWriter
	replaced bool
}

func (w *jsonFallbackWriter) WriteHeader(status int) {
	var body errorBody
	switch status {
	case http.StatusNotFound:
		body = errorBody{Code: CodeNotFound, Message: "接口不存在"}
	case http.StatusMethodNotAllowed:
		body = errorBody{Code: CodeMethodNotAllowed, Message: "这个接口不支持该请求方法"}
	default:
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.replaced = true
	h := w.Header()
	h.Del("X-Content-Type-Options")
	h.Set("Content-Type", "application/json; charset=utf-8")
	w.ResponseWriter.WriteHeader(status)
	data, _ := json.Marshal(body)
	_, _ = w.ResponseWriter.Write(data)
}

func (w *jsonFallbackWriter) Write(b []byte) (int, error) {
	if w.replaced {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}
