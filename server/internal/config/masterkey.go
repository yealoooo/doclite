package config

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// MasterKeyHint 是生成主密钥的命令，写进错误提示。
const MasterKeyHint = "openssl rand -base64 32"

// MasterKey 是主密钥：32 字节随机数的标准 base64（44 个字符，末尾一个 `=`）。
// 加解密在 M1-3（存储配置的 secret_key）。String() 只输出指纹，不会把密钥打进日志。
type MasterKey struct {
	key [32]byte
}

// ParseMasterKey 解析 MASTER_KEY：标准 base64，解码后正好 32 字节，不能全是 0。
// 错误信息里不带密钥本身。
func ParseMasterKey(v string) (MasterKey, error) {
	var mk MasterKey
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return mk, fmt.Errorf("MASTER_KEY 不是合法的 base64；用 `%s` 生成", MasterKeyHint)
	}
	if len(raw) != len(mk.key) {
		return mk, fmt.Errorf("MASTER_KEY 解码后是 %d 字节，应为 32 字节；用 `%s` 生成", len(raw), MasterKeyHint)
	}
	zero := true
	for _, b := range raw {
		if b != 0 {
			zero = false
			break
		}
	}
	if zero {
		return mk, fmt.Errorf("MASTER_KEY 不能全是 0；用 `%s` 生成", MasterKeyHint)
	}
	copy(mk.key[:], raw)
	return mk, nil
}

// Bytes 是 32 字节的原始密钥（AES-256）。
func (m MasterKey) Bytes() []byte { return m.key[:] }

// KeyID 是主密钥的指纹（8 个十六进制字符，和上一版的算法一样：
// SHA-256("doclite master key id v1\0" ‖ 密钥) 的前 4 字节）。不泄露密钥，可以写日志，用来核对两处的 MASTER_KEY 是否一致。
func (m MasterKey) KeyID() string {
	h := sha256.New()
	h.Write([]byte("doclite master key id v1\x00"))
	h.Write(m.key[:])
	return hex.EncodeToString(h.Sum(nil)[:4])
}

// String 只输出指纹。
func (m MasterKey) String() string { return "MasterKey(" + m.KeyID() + ")" }

// GoString 只输出指纹（%#v）。
func (m MasterKey) GoString() string { return m.String() }
