package imagecache

import (
	"strconv"
	"strings"
)

// Prefix 是取图地址的前缀（web/src/lib/image-url.ts 的 SHELL_IMAGE_BASE）。取图挂在壳的资产服务器上，和页面同源：
//
//	/shell/img/cover/<file_id>/<snapshot_id>                   封面（JPEG）
//	/shell/img/original/<file_id>/<snapshot_id>                原图
//	/shell/img/dzi/<file_id>/<snapshot_id>                     分块描述（DZI 的 JSON 写法）
//	/shell/img/tile/<file_id>/<snapshot_id>/<级>/<x>_<y>.<jpg|png>  一块
//	/shell/img/test/probe.png                                  自检的测试图（64×64）
const Prefix = "/shell/img/"

// 取图请求的种类。
const (
	OpCover    = "cover"
	OpOriginal = "original"
	OpDZI      = "dzi"
	OpTile     = "tile"
	OpTest     = "test"
)

// Request 是解析好的取图地址。file_id、snapshot_id 已转成小写。
type Request struct {
	Op         string
	FileID     string
	SnapshotID string
	// 只有 tile 有：级、列、行、扩展名（jpg、png）。
	Level, X, Y int
	Ext         string
}

// ParsePath 解析取图地址（r.URL.Path，已解码）。不认识的、格式不对的返回 false。
// 只接受规整的写法：每段不能为空，id 必须是 UUID，级和坐标是不带符号的十进制数。
func ParsePath(p string) (Request, bool) {
	rest, ok := strings.CutPrefix(p, Prefix)
	if !ok {
		return Request{}, false
	}
	if rest == "test/probe.png" {
		return Request{Op: OpTest}, true
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		return Request{}, false
	}
	fid, ok1 := normUUID(parts[1])
	sid, ok2 := normUUID(parts[2])
	if !ok1 || !ok2 {
		return Request{}, false
	}
	r := Request{Op: parts[0], FileID: fid, SnapshotID: sid}
	switch r.Op {
	case OpCover, OpOriginal, OpDZI:
		if len(parts) != 3 {
			return Request{}, false
		}
		return r, true
	case OpTile:
		if len(parts) != 5 {
			return Request{}, false
		}
		level, ok := smallInt(parts[3])
		if !ok || level > 62 {
			return Request{}, false
		}
		name, ext, ok := strings.Cut(parts[4], ".")
		if !ok || (ext != "jpg" && ext != "png") {
			return Request{}, false
		}
		xs, ys, ok := strings.Cut(name, "_")
		if !ok {
			return Request{}, false
		}
		x, okX := smallInt(xs)
		y, okY := smallInt(ys)
		if !okX || !okY {
			return Request{}, false
		}
		r.Level, r.X, r.Y, r.Ext = level, x, y, ext
		return r, true
	}
	return Request{}, false
}

// normUUID 核对 UUID 的写法（8-4-4-4-12 位十六进制），返回小写。
func normUUID(s string) (string, bool) {
	if len(s) != 36 {
		return "", false
	}
	b := []byte(s)
	for i, c := range b {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return "", false
			}
			continue
		}
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
			b[i] = c + ('a' - 'A')
		default:
			return "", false
		}
	}
	return string(b), true
}

// smallInt：1–6 位十进制数字（不带符号、不带空格）。
func smallInt(s string) (int, bool) {
	if s == "" || len(s) > 6 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// IsUUID 判断是不是 UUID 的写法（cache_clear 的 projectId 用）。
func IsUUID(s string) bool {
	_, ok := normUUID(s)
	return ok
}
