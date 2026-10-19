package storage

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"doclite/server/internal/platform/httpx"
	"doclite/server/internal/platform/s3"
)

// 存储配置的校验（requirements 5.4）。不合格一律 400 `invalid_input`。

const (
	maxProfileNameChars   = 64
	maxEndpointChars      = 255
	maxPathPrefixBytes    = 128 // 给项目编码、file_id 和 `v<no>.cover.jpg` 留足 1024 字节 key 的余量
	maxRegionChars        = 64
	maxAccessKeyChars     = 128
	maxSecretKeyChars     = 256
	maxRemarkChars        = 500
	defaultPathPrefix     = "doclite/"
	defaultURLExpireHours = 24
	minURLExpireHours     = 1
	maxURLExpireHours     = 168
	// InitialProfileName 是首次启动按 S3_* 登记的那条存储的名称。
	InitialProfileName = "SeaweedFS（随系统部署）"
)

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

func normalizeProfileName(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || utf8.RuneCountInString(v) > maxProfileNameChars || hasControl(v) {
		return "", httpx.Invalid("存储名称不能为空，最长 %d 个字符", maxProfileNameChars)
	}
	return v, nil
}

// normalizeEndpoint：http(s)://主机:端口，只能是根路径。
func normalizeEndpoint(field, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", httpx.Invalid("%s 不能为空", field)
	}
	if utf8.RuneCountInString(v) > maxEndpointChars {
		return "", httpx.Invalid("%s 最长 %d 个字符", field, maxEndpointChars)
	}
	ep, err := s3.NormalizeEndpoint(v)
	if err != nil {
		return "", httpx.Invalid("%s 不对：%s", field, err.Error())
	}
	return ep, nil
}

// normalizePublicEndpoint：空字符串表示和 endpoint 相同（nil）。
func normalizePublicEndpoint(v string) (*string, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	ep, err := normalizeEndpoint("public_endpoint", v)
	if err != nil {
		return nil, err
	}
	return &ep, nil
}

// normalizeBucket 按 S3 的桶命名规则做基本检查：3–63 个字符，只有小写字母、数字、`.` 和 `-`，以字母或数字开头和结尾，
// 没有 `..`，不是 IP 地址的样子。
func normalizeBucket(v string) (string, error) {
	v = strings.TrimSpace(v)
	bad := func(why string) (string, error) { return "", httpx.Invalid("桶名 %q 不对：%s", v, why) }
	if len(v) < 3 || len(v) > 63 {
		return bad("要有 3 到 63 个字符")
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			return bad("只能有小写字母、数字、. 和 -")
		}
	}
	edge := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }
	if !edge(v[0]) || !edge(v[len(v)-1]) {
		return bad("要以小写字母或数字开头和结尾")
	}
	if strings.Contains(v, "..") {
		return bad("不能有连续的两个 .")
	}
	if parts := strings.Split(v, "."); len(parts) == 4 {
		ip := true
		for _, p := range parts {
			if strings.Trim(p, "0123456789") != "" {
				ip = false
			}
		}
		if ip {
			return bad("不能是 IP 地址的样子")
		}
	}
	return v, nil
}

func normalizeRegion(v string) (string, error) {
	v = strings.TrimSpace(v)
	ok := v != "" && utf8.RuneCountInString(v) <= maxRegionChars
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			ok = false
		}
	}
	if !ok {
		return "", httpx.Invalid("region 要有 1 到 %d 个字符，只能有字母、数字、- 和 _，例如 %s", maxRegionChars, s3.DefaultRegion)
	}
	return v, nil
}

// normalizePathPrefix：空（对象直接放在桶的根下），或以 `/` 结尾、不以 `/` 开头、没有空段（`//`），
// 每段只有字母、数字和 `- _ .`（不能是 `.`、`..`），最长 128 字节。不自动补 `/`。
func normalizePathPrefix(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	bad := func(why string) (string, error) {
		return "", httpx.Invalid("key 前缀（path_prefix）%q 不对：%s", v, why)
	}
	if len(v) > maxPathPrefixBytes {
		return bad("最长 128 字节")
	}
	if strings.HasPrefix(v, "/") {
		return bad("不能以 / 开头")
	}
	body, ok := strings.CutSuffix(v, "/")
	if !ok {
		return bad("不为空时必须以 / 结尾，例如 doclite/")
	}
	for _, seg := range strings.Split(body, "/") {
		if seg == "" {
			return bad("不能有空段（//）")
		}
		if seg == "." || seg == ".." {
			return bad("不能有 . 或 .. 段")
		}
		for _, c := range seg {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return bad("每段只能有英文字母、数字和 - _ .")
			}
		}
	}
	return v, nil
}

func normalizeKey(field, v string, max int) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || utf8.RuneCountInString(v) > max || strings.IndexFunc(v, func(c rune) bool {
		return unicode.IsSpace(c) || unicode.IsControl(c)
	}) >= 0 {
		return "", httpx.Invalid("%s 不能为空，不能有空格，最长 %d 个字符", field, max)
	}
	return v, nil
}

func normalizeAccessKey(v string) (string, error) {
	return normalizeKey("access_key", v, maxAccessKeyChars)
}
func normalizeSecretKey(v string) (string, error) {
	return normalizeKey("secret_key", v, maxSecretKeyChars)
}

func checkURLExpireHours(h int) (int, error) {
	if h < minURLExpireHours || h > maxURLExpireHours {
		return 0, httpx.Invalid("url_expire_hours 要在 %d 到 %d 小时之间", minURLExpireHours, maxURLExpireHours)
	}
	return h, nil
}

func checkStatus(v string) (string, error) {
	switch v {
	case StatusActive, StatusReadonly, StatusDisabled:
		return v, nil
	}
	return "", httpx.Invalid("存储状态只能是 active、readonly 或 disabled")
}
