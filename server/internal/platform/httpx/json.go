package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

// MaxJSONBody 是 JSON 请求体的上限：1 MiB（最大的 JSON 是项目 settings，≤ 64 KiB）。上传走 multipart，另有上限。
const MaxJSONBody = 1 << 20

// WriteJSON 输出 JSON 响应。v 为 nil 时只写状态码（204 之类）。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	if v == nil {
		w.WriteHeader(status)
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		// 只会是代码写错了（不能序列化的类型），按 500 处理。
		data = []byte(`{"code":"internal","message":"服务器内部错误，请稍后重试或联系管理员"}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// ReadJSON 把请求体解析进 dst：
//   - Content-Type 必须是 application/json，否则 400 `invalid_input`；
//   - 超过 [MaxJSONBody] 413 `payload_too_large`；
//   - 不是合法 JSON、类型不对 400 `invalid_input`；不认识的字段忽略。
func ReadJSON(r *http.Request, dst any) error {
	ct := r.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
		return Invalid("请求体要是 JSON（Content-Type: application/json）")
	}
	body := io.LimitReader(r.Body, MaxJSONBody+1)
	data, err := io.ReadAll(body)
	if err != nil {
		return Invalid("读请求体失败：%v", err)
	}
	if len(data) > MaxJSONBody {
		return NewError(http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
			fmt.Sprintf("请求体太大（上限 %d KiB）", MaxJSONBody>>10))
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return Invalid("缺少请求体")
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return Invalid("请求体格式不对：%s", describeJSONError(err))
	}
	return nil
}

// ReadOptionalJSON 同 [ReadJSON]，但请求体可以没有（前端不带请求体的 POST：没有 Content-Type、长度为 0）：
// 这时 dst 不变。有请求体时规则和 ReadJSON 一样。
func ReadOptionalJSON(r *http.Request, dst any) error {
	if r.ContentLength == 0 && r.Header.Get("Content-Type") == "" {
		return nil
	}
	return ReadJSON(r, dst)
}

func describeJSONError(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		if typeErr.Field != "" {
			return fmt.Sprintf("字段 %s 的类型不对（应为 %s）", typeErr.Field, jsonKind(typeErr.Type.Kind().String()))
		}
		return "类型不对"
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "不是合法的 JSON"
	}
	if strings.Contains(err.Error(), "UUID") {
		// uuid.UUID 的 UnmarshalText 返回的错误。
		return "有的 id 不是合法的 UUID"
	}
	return err.Error()
}

func jsonKind(kind string) string {
	switch kind {
	case "string":
		return "字符串"
	case "slice", "array":
		return "数组"
	case "struct", "map":
		return "对象"
	case "bool":
		return "true 或 false"
	case "int", "int32", "int64", "uint", "uint32", "uint64", "float64":
		return "数字"
	}
	return kind
}
