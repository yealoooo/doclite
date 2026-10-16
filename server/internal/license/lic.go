// Package license 是许可证（requirements 第 11 节）：.lic 文件的解析和 Ed25519 验签、install_id、导入和查看，
// 以及登录和每个请求的席位、到期检查（实现 auth.License）。
//
// .lic 文件由签发工具 `../bak/doclite-license` 生成，这里只验签，必须和它签出来的文件逐字节兼容：
//
//	-----BEGIN DOCLITE LICENSE-----
//	base64（标准字母表、带填充，每行 76 个字符）
//	-----END DOCLITE LICENSE-----
//
// base64 里是 JSON `{"payload": {…}, "signature": "<base64>", "version": 1}`（serde_json 的 pretty 格式；解析时不在乎
// 格式）。payload 有 7 个字段：license_id、company、edition、max_users、issued_at、expires_at、hardware_id。
//
// **签名签的不是文件里的字节**，而是工具把 payload 重新序列化成的"规范 JSON"（[Canonical]）：字段按名字的字节序排、
// 没有空白，字符串按 serde_json 的规则转义，时间按 chrono 的规则写（UTC、`Z` 结尾、小数秒取 0/3/6/9 位里最短的）。
// 工具验签时也是先解析再重新序列化，所以这里照做：解析 → 重新序列化 → 验签。
package license

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Payload 是许可证的内容（工具的 LicensePayload）。
type Payload struct {
	LicenseID string
	Company   string
	// Edition 只显示（trial、standard、enterprise）。
	Edition string
	// MaxUsers 是席位数：同时在线的登录会话数上限。
	MaxUsers  uint32
	IssuedAt  time.Time
	ExpiresAt time.Time
	// HardwareID 是绑定的 install_id。
	HardwareID string
}

// File 是解析出来的 .lic 文件。
type File struct {
	Payload   Payload
	Signature []byte
	Version   uint32
}

// FormatError 是 .lic 的格式不对（不是 base64、不是 JSON、缺字段、时间写法不对……）。
type FormatError struct{ Reason string }

func (e *FormatError) Error() string { return "许可证文件格式不对：" + e.Reason }

// ErrBadSignature 是签名验不过（文件被改过，或者不是这把公钥对应的私钥签的）。
var ErrBadSignature = errors.New("许可证签名校验没有通过")

const (
	licHeader = "-----BEGIN DOCLITE LICENSE-----"
	licFooter = "-----END DOCLITE LICENSE-----"
)

// rawFile、rawPayload 用指针区分"缺字段"（工具用 serde 解析，缺字段就失败）。
type rawFile struct {
	Payload   *rawPayload `json:"payload"`
	Signature *string     `json:"signature"`
	Version   *uint32     `json:"version"`
}

type rawPayload struct {
	LicenseID  *string `json:"license_id"`
	Company    *string `json:"company"`
	Edition    *string `json:"edition"`
	MaxUsers   *uint32 `json:"max_users"`
	IssuedAt   *string `json:"issued_at"`
	ExpiresAt  *string `json:"expires_at"`
	HardwareID *string `json:"hardware_id"`
}

// Decode 解析 .lic 文本（工具的 decode_license_file）：去掉空行和 `-----` 开头的行、拼起来 base64 解码、解析 JSON。
// 不验签。
func Decode(text string) (*File, error) {
	var b64 strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-----") {
			continue
		}
		b64.WriteString(line)
	}
	if b64.Len() == 0 {
		return nil, &FormatError{"内容是空的"}
	}
	data, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		return nil, &FormatError{"不是 base64"}
	}
	var raw rawFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &FormatError{"不是许可证的 JSON"}
	}
	if raw.Payload == nil || raw.Signature == nil || raw.Version == nil {
		return nil, &FormatError{"缺少 payload、signature 或 version"}
	}
	rp := raw.Payload
	if rp.LicenseID == nil || rp.Company == nil || rp.Edition == nil || rp.MaxUsers == nil ||
		rp.IssuedAt == nil || rp.ExpiresAt == nil || rp.HardwareID == nil {
		return nil, &FormatError{"payload 缺字段"}
	}
	issued, err := parseTime(*rp.IssuedAt)
	if err != nil {
		return nil, &FormatError{"签发时间写法不对"}
	}
	expires, err := parseTime(*rp.ExpiresAt)
	if err != nil {
		return nil, &FormatError{"到期时间写法不对"}
	}
	sig, err := base64.StdEncoding.DecodeString(*raw.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, &FormatError{"签名不是 64 字节的 base64"}
	}
	return &File{
		Payload: Payload{
			LicenseID: *rp.LicenseID, Company: *rp.Company, Edition: *rp.Edition, MaxUsers: *rp.MaxUsers,
			IssuedAt: issued, ExpiresAt: expires, HardwareID: *rp.HardwareID,
		},
		Signature: sig,
		Version:   *raw.Version,
	}, nil
}

// Verify 用公钥验签（签的是 [Canonical]）。
func Verify(pub ed25519.PublicKey, f *File) error {
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, Canonical(f.Payload), f.Signature) {
		return ErrBadSignature
	}
	return nil
}

// parseTime 解析 RFC 3339 时间（chrono 的 DateTime<Utc> 反序列化：任意时区，转成 UTC）。年份只认 4 位
// （超出时 chrono 的写法不同，签名对不上，直接当格式不对）。
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	t = t.UTC()
	if t.Year() < 0 || t.Year() > 9999 {
		return time.Time{}, fmt.Errorf("年份超出范围：%d", t.Year())
	}
	return t, nil
}

// Canonical 是签名的对象：payload 的规范 JSON（工具的 canonical_json：serde_json 把字段按名字排序后紧凑输出）。
func Canonical(p Payload) []byte {
	var b bytes.Buffer
	b.WriteString(`{"company":`)
	writeJSONString(&b, p.Company)
	b.WriteString(`,"edition":`)
	writeJSONString(&b, p.Edition)
	b.WriteString(`,"expires_at":`)
	writeJSONString(&b, FormatTime(p.ExpiresAt))
	b.WriteString(`,"hardware_id":`)
	writeJSONString(&b, p.HardwareID)
	b.WriteString(`,"issued_at":`)
	writeJSONString(&b, FormatTime(p.IssuedAt))
	b.WriteString(`,"license_id":`)
	writeJSONString(&b, p.LicenseID)
	b.WriteString(`,"max_users":`)
	b.WriteString(strconv.FormatUint(uint64(p.MaxUsers), 10))
	b.WriteByte('}')
	return b.Bytes()
}

// FormatTime 按 chrono 序列化 DateTime<Utc> 的规则写时间（RFC 3339、SecondsFormat::AutoSi、`Z`）：
// 没有小数秒时不写；纳秒是整毫秒写 3 位，整微秒写 6 位，否则 9 位。
func FormatTime(t time.Time) string {
	t = t.UTC()
	s := t.Format("2006-01-02T15:04:05")
	switch ns := t.Nanosecond(); {
	case ns == 0:
	case ns%1_000_000 == 0:
		s += fmt.Sprintf(".%03d", ns/1_000_000)
	case ns%1_000 == 0:
		s += fmt.Sprintf(".%06d", ns/1_000)
	default:
		s += fmt.Sprintf(".%09d", ns)
	}
	return s + "Z"
}

// writeJSONString 按 serde_json 的规则写字符串：只转义 `"`、`\` 和 0x00–0x1F（\b \t \n \f \r 用短写法，其余
// \u00xx 小写十六进制）；非 ASCII 原样输出（不转义 U+2028、U+2029，也不转义 < > &，这几处和 Go 的 encoding/json
// 不一样）。
func writeJSONString(b *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0xf])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}

// Encode 把 payload 和签名写成 .lic 文本（工具的 encode_license_file：pretty JSON → base64 → 每行 76 个字符）。
// 服务端不签发，只给测试造许可证用。
func Encode(p Payload, sig []byte) string {
	type payloadJSON struct {
		LicenseID  string `json:"license_id"`
		Company    string `json:"company"`
		Edition    string `json:"edition"`
		MaxUsers   uint32 `json:"max_users"`
		IssuedAt   string `json:"issued_at"`
		ExpiresAt  string `json:"expires_at"`
		HardwareID string `json:"hardware_id"`
	}
	data, _ := json.MarshalIndent(struct {
		Payload   payloadJSON `json:"payload"`
		Signature string      `json:"signature"`
		Version   uint32      `json:"version"`
	}{
		Payload: payloadJSON{
			LicenseID: p.LicenseID, Company: p.Company, Edition: p.Edition, MaxUsers: p.MaxUsers,
			IssuedAt: FormatTime(p.IssuedAt), ExpiresAt: FormatTime(p.ExpiresAt), HardwareID: p.HardwareID,
		},
		Signature: base64.StdEncoding.EncodeToString(sig),
		Version:   1,
	}, "", "  ")
	enc := base64.StdEncoding.EncodeToString(data)
	var out strings.Builder
	out.WriteString(licHeader + "\n")
	for len(enc) > 76 {
		out.WriteString(enc[:76] + "\n")
		enc = enc[76:]
	}
	if enc != "" {
		out.WriteString(enc + "\n")
	}
	out.WriteString(licFooter + "\n")
	return out.String()
}

// Sign 签一个 payload，返回 .lic 文本。服务端不签发，只给测试用。
func Sign(priv ed25519.PrivateKey, p Payload) string {
	return Encode(p, ed25519.Sign(priv, Canonical(p)))
}
