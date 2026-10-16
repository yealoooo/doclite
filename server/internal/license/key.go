package license

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// publicKey 是编进程序的正式公钥，构建时用 -ldflags 设置（不提供环境变量覆盖：客户能改环境变量）：
//
//	go build -ldflags "-X doclite/server/internal/license.publicKey=MCowBQYDK2VwAyEA…" ./cmd/server
//
// 值是签发工具 `keygen` 生成的 public.pem 中间那一行（SPKI DER 的 base64），也可以是 32 字节原始公钥的 base64。
// 为空（开发构建）时不检查许可证。
var publicKey string

// BuiltinPublicKey 是编进程序的正式公钥；没编进去（开发构建）时返回 nil。值不对时返回错误（server 不启动，
// 免得打错的构建悄悄不检查许可证）。
func BuiltinPublicKey() (ed25519.PublicKey, error) {
	if strings.TrimSpace(publicKey) == "" {
		return nil, nil
	}
	pk, err := ParsePublicKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("编进程序的许可证公钥不对（-ldflags -X doclite/server/internal/license.publicKey）：%w", err)
	}
	return pk, nil
}

// ParsePublicKey 解析 Ed25519 公钥：PEM（`-----BEGIN PUBLIC KEY-----`）、SPKI DER 的 base64，或 32 字节原始公钥的
// base64。
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	var der []byte
	if strings.HasPrefix(s, "-----") {
		block, _ := pem.Decode([]byte(s))
		if block == nil {
			return nil, errors.New("不是 PEM")
		}
		der = block.Bytes
	} else {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, errors.New("不是 base64")
		}
		if len(b) == ed25519.PublicKeySize {
			return ed25519.PublicKey(b), nil
		}
		der = b
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("不是公钥：%w", err)
	}
	pk, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("不是 Ed25519 公钥")
	}
	return pk, nil
}

// Fingerprint 是公钥的指纹（sha256 的前 8 字节，十六进制），只用来在日志里认出是哪把公钥。
func Fingerprint(pk ed25519.PublicKey) string {
	sum := sha256.Sum256(pk)
	return hex.EncodeToString(sum[:8])
}
