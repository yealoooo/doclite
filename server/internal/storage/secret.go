package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"doclite/server/internal/config"
)

// secret_key 的加密（requirements 5.4）：AES-256-GCM，主密钥是 MASTER_KEY。
//
// 密文格式（版本 1）：`0x01 | key_id（4 字节）| nonce（12 字节）| 密文 | tag（16 字节）`。
//   - 版本号：以后换算法时加新版本；
//   - key_id 是主密钥的指纹（config.MasterKey.KeyID）：换了 MASTER_KEY 时能明确报"是用另一个主密钥加密的"；
//   - 附加认证数据（AAD）= 头部（版本号和 key_id）‖ `storage_profiles.secret_key_enc:<行 id>`：
//     头部被改、密文被挪到别的行，都解不开。
//
// 明文和密文都不进接口、审计和日志。

const (
	secretVersion  = 1
	keyIDBytes     = 4
	nonceBytes     = 12
	tagBytes       = 16
	headerBytes    = 1 + keyIDBytes
	minSecretBytes = headerBytes + nonceBytes + tagBytes
)

// 解不开的原因。
var (
	errSecretMalformed = errors.New("密文格式不对")
	errSecretTampered  = errors.New("密文校验失败（被改过，或者不是这一行的密文）")
)

type errOtherKey struct{ found, current string }

func (e errOtherKey) Error() string {
	return fmt.Sprintf("是用另一个 MASTER_KEY 加密的（加密时的主密钥指纹 %s，当前 MASTER_KEY 的指纹 %s）", e.found, e.current)
}

func secretAAD(header []byte, profileID uuid.UUID) []byte {
	return append(append([]byte{}, header...), "storage_profiles.secret_key_enc:"+profileID.String()...)
}

func keyID(mk config.MasterKey) []byte {
	b, err := hex.DecodeString(mk.KeyID())
	if err != nil || len(b) != keyIDBytes {
		panic("storage: 主密钥指纹的格式不对")
	}
	return b
}

func gcm(mk config.MasterKey) (cipher.AEAD, error) {
	block, err := aes.NewCipher(mk.Bytes())
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealSecret 加密 secret_key，密文绑定存储配置的 id。
func sealSecret(mk config.MasterKey, profileID uuid.UUID, plain string) ([]byte, error) {
	aead, err := gcm(mk)
	if err != nil {
		return nil, err
	}
	header := append([]byte{secretVersion}, keyID(mk)...)
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append(append([]byte{}, header...), nonce...)
	return aead.Seal(out, nonce, []byte(plain), secretAAD(header, profileID)), nil
}

// openSecret 解密 secret_key。解不开时返回给人看的原因（不含密钥）。
func openSecret(mk config.MasterKey, profileID uuid.UUID, enc []byte) (string, error) {
	if len(enc) < minSecretBytes {
		return "", errSecretMalformed
	}
	if enc[0] != secretVersion {
		return "", fmt.Errorf("不认识的密文版本 %d", enc[0])
	}
	header := enc[:headerBytes]
	if cur := keyID(mk); string(header[1:]) != string(cur) {
		return "", errOtherKey{found: hex.EncodeToString(header[1:]), current: hex.EncodeToString(cur)}
	}
	aead, err := gcm(mk)
	if err != nil {
		return "", err
	}
	nonce := enc[headerBytes : headerBytes+nonceBytes]
	plain, err := aead.Open(nil, nonce, enc[headerBytes+nonceBytes:], secretAAD(header, profileID))
	if err != nil {
		return "", errSecretTampered
	}
	return string(plain), nil
}
