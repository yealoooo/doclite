// Package httpx 是 HTTP 的基础设施：路由、中间件、统一错误体、JSON 读写、分页和路径参数。
//
// 错误体：所有接口出错时返回 `{"code": "<错误码>", "message": "<给人看的中文说明>", "details": …}`，
// details 可选、没有时不输出。错误码见 requirements 第 8 节。
//
// handler 返回 error：
//   - [*Error] 按它的状态码和错误码输出；
//   - 客户端已经断开造成的（[clientGone]）不算错误：只写状态码 499，日志不报错；
//   - 等锁超时（lock_timeout，见 db.LockTimeout）503 `busy`；
//   - 其他错误一律 500 `internal`，细节只写日志，不返回给调用方。
package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"doclite/server/internal/platform/db"
)

// Error 是带 HTTP 状态码和错误码的错误。service 里也用它表达业务错误（`last_admin` 之类）。
type Error struct {
	Status  int
	Code    string
	Message string
	// Details 放进响应体的 details（426 的升级信息、批量操作的 ids……）；nil 时不输出。
	Details any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// NewError 建一个错误。
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// WithDetails 返回带 details 的副本。
func (e *Error) WithDetails(details any) *Error {
	c := *e
	c.Details = details
	return &c
}

// 常用错误码。
const (
	CodeInvalidInput         = "invalid_input"
	CodeInvalidClientVersion = "invalid_client_version"
	CodeUnauthorized         = "unauthorized"
	CodeInvalidCredentials   = "invalid_credentials"
	CodeForbidden            = "forbidden"
	CodeAdminRequired        = "admin_required"
	CodeUserDisabled         = "user_disabled"
	CodeNotFound             = "not_found"
	CodeMethodNotAllowed     = "method_not_allowed"
	CodeConflict             = "conflict"
	CodeInvalidState         = "invalid_state"
	CodePayloadTooLarge      = "payload_too_large"
	CodeUnsupportedMedia     = "unsupported_media_type"
	CodeUpgradeRequired      = "client_upgrade_required"
	CodeInternal             = "internal"
	CodeBusy                 = "busy"
	// 几个模块共用的业务错误码。
	CodeFolderArchived = "folder_archived" // 文件夹已封存（硬性规则 3）
	CodeFileCataloged  = "file_cataloged"  // 文件已归类
	CodeItemLocked     = "item_locked"
	CodeTreeFull       = "tree_full"
	CodeTreeCycle      = "tree_cycle"
	// M7-1：许可证。登录和请求被拒（403）：没有有效的许可证、已到期、在线会话满了；导入时（400）：文件不对或签名
	// 验不过、install_id 不符、已经过期。
	CodeLicenseMissing     = "license_missing"
	CodeLicenseExpired     = "license_expired"
	CodeLicenseSeatsFull   = "license_seats_full"
	CodeInvalidLicense     = "invalid_license"
	CodeLicenseMismatch    = "license_mismatch"
	CodeLicenseFileExpired = "license_file_expired"
)

// Invalid 是 400 `invalid_input`。
func Invalid(format string, args ...any) *Error {
	return NewError(http.StatusBadRequest, CodeInvalidInput, fmt.Sprintf(format, args...))
}

// NotFound 是 404 `not_found`，what 是东西的中文名称，例如"用户"。
func NotFound(what string) *Error {
	return NewError(http.StatusNotFound, CodeNotFound, what+"不存在")
}

// NotFoundIDs 是批量接口的 404 `not_found`：details.ids 是找不到的 id。
func NotFoundIDs(message string, ids any) *Error {
	return NewError(http.StatusNotFound, CodeNotFound, message).WithDetails(map[string]any{"ids": ids})
}

// Conflict 是 409，code 为空时用 `conflict`。
func Conflict(code, message string) *Error {
	if code == "" {
		code = CodeConflict
	}
	return NewError(http.StatusConflict, code, message)
}

// InvalidState 是 409 `invalid_state`（当前状态不允许这个操作）。
func InvalidState(message string) *Error {
	return NewError(http.StatusConflict, CodeInvalidState, message)
}

// Forbidden 是 403 `forbidden`（菜单权限不够）。
func Forbidden() *Error {
	return NewError(http.StatusForbidden, CodeForbidden, "没有权限执行这个操作")
}

// AdminRequired 是 403 `admin_required`（防提权，requirements 第 4 节）。
func AdminRequired(message string) *Error {
	return NewError(http.StatusForbidden, CodeAdminRequired, message)
}

// Unauthorized 是 401 `unauthorized`（没登录，或会话过期、已撤销、用户已停用）。
func Unauthorized() *Error {
	return NewError(http.StatusUnauthorized, CodeUnauthorized, "未登录或登录已失效，请重新登录")
}

// Busy 是 503 `busy`。
func Busy() *Error {
	return NewError(http.StatusServiceUnavailable, CodeBusy, "服务器繁忙，请稍后再试")
}

// AsError 取出 err 链里的 *Error。
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// StatusClientClosedRequest 是客户端在拿到响应之前就断开时记的状态码（nginx 的约定，不是标准状态码）。
// 只是为了访问日志：客户端已经不在了，写出去也没人收。
const StatusClientClosedRequest = 499

// clientGone 判断 err 是不是客户端断开造成的：请求的 context 已经被取消（不是超时），并且 err 的链里有 context.Canceled
// （查库、读写 S3 时 context 被取消）。
//
// 请求的 context 只在客户端断开时被取消（HTTP/1 关连接、HTTP/2 取消流）：http.Server 没有设置会被取消的 BaseContext、
// ConnContext，也没有 TimeoutHandler、WriteTimeout。服务端自己的超时（DeadlineExceeded）、handler 里自己派生又取消的
// context（这时请求的 context 还没取消）都不算，仍按 500 记 ERROR。
func clientGone(r *http.Request, err error) bool {
	return errors.Is(context.Cause(r.Context()), context.Canceled) && errors.Is(err, context.Canceled)
}

// WriteError 输出错误体。不是 *Error 的错误：客户端已经断开的（[clientGone]）只写状态码 499、日志记 Debug；
// 其他的写 Error 日志（带请求的方法和路径）后返回 500 `internal`。
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	e, ok := AsError(err)
	if !ok {
		if clientGone(r, err) {
			LoggerFrom(r.Context()).Debug("客户端已断开，请求没有处理完", "method", r.Method, "path", r.URL.Path, "error", err)
			w.WriteHeader(StatusClientClosedRequest)
			return
		}
		if db.LockTimedOut(err) {
			// 等锁超过了 lock_timeout（db.LockTimeout）：别的大操作正拿着锁，稍后再试就好。
			LoggerFrom(r.Context()).Warn("等锁超时", "method", r.Method, "path", r.URL.Path, "error", err)
			e = NewError(http.StatusServiceUnavailable, CodeBusy, "有别的操作正在进行（比如删除或移动很大的文件夹），请稍后再试")
		} else {
			LoggerFrom(r.Context()).Error("请求处理失败", "method", r.Method, "path", r.URL.Path, "error", err)
			e = NewError(http.StatusInternalServerError, CodeInternal, "服务器内部错误，请稍后重试或联系管理员")
		}
	}
	WriteJSON(w, e.Status, errorBody{Code: e.Code, Message: e.Message, Details: e.Details})
}
