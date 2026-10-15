// Package semver 解析和比较 semver 2.0 版本号：客户端的版本和服务器的最低版本（`/api/shell/latest` 的
// min_version）比较，决定要不要整页提示更新（requirements 第 6 节"版本"）。
//
// 规则和 server 的 `internal/platform/semver` 一致（那个包是 server 的 internal，client 引用不到）：
// 只接受完整的 `主.次.修订`，可带先行版本（`-beta.1`）和构建元数据（`+build.5`）；数字不能有前导 0；
// `1.2.0-beta.1` 低于 `1.2.0`；构建元数据不参与比较。
package semver

import (
	"errors"
	"strconv"
	"strings"
)

// Version 是解析后的版本号。
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
	Build               string
}

// Parse 解析版本号（首尾空白忽略）。
func Parse(s string) (Version, error) {
	s = strings.TrimSpace(s)
	var v Version
	if s == "" {
		return v, errors.New("版本号为空")
	}
	rest := s
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		v.Build = rest[i+1:]
		rest = rest[:i]
		if !validIdents(v.Build, false) {
			return v, errors.New("构建元数据不合法")
		}
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		pre := rest[i+1:]
		rest = rest[:i]
		if !validIdents(pre, true) {
			return v, errors.New("先行版本不合法")
		}
		v.Pre = strings.Split(pre, ".")
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return v, errors.New("应为 主.次.修订 三段数字")
	}
	var nums [3]uint64
	for i, p := range parts {
		n, ok := numeric(p)
		if !ok {
			return v, errors.New(strconv.Quote(p) + " 不是合法的数字")
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, nil
}

// Compare 按 semver 优先级比较：a < b 返回 -1，相等 0，a > b 返回 1。
func Compare(a, b Version) int {
	for _, d := range [][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := compareIdent(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

// Less 判断版本号 a 是否低于 b；任何一个解析不了时返回 false 和错误（调用方按"不限制"处理）。
func Less(a, b string) (bool, error) {
	va, err := Parse(a)
	if err != nil {
		return false, err
	}
	vb, err := Parse(b)
	if err != nil {
		return false, err
	}
	return Compare(va, vb) < 0, nil
}

func compareIdent(a, b string) int {
	an, aNum := numeric(a)
	bn, bNum := numeric(b)
	switch {
	case aNum && bNum:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		}
		return 0
	case aNum:
		return -1
	case bNum:
		return 1
	}
	return strings.Compare(a, b)
}

func numeric(s string) (uint64, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

func validIdents(s string, pre bool) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		allDigits := true
		for _, c := range id {
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
				allDigits = false
			default:
				return false
			}
		}
		if pre && allDigits && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}
