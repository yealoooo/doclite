package upload

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// NaturalKey 是文件名的自然顺序排序键：按字节比较两个键，结果就是文件名的自然顺序（requirements 第 6 节：
// 同一子文件夹按文件名自然顺序串行，IMG_2 在 IMG_10 前）。队列库里存成 BLOB，SQLite 按 memcmp 比较。
//
// 规则：
//   - 连续的 ASCII 数字按数值比：编成 '0'、有效位数（1 字节）、去掉前导零的数字。'0' 是数字在 ASCII 里的位置，
//     所以数字和别的字符比的结果和逐字符比一样（`x.jpg` 在 `x1.jpg` 前：'.' < '0'）；位数少的数小；
//   - 其余字符不分大小写（转成小写）逐个比，按 UTF-8 编码（和码点顺序一致）；
//   - 全部相同（例如只差大小写、前导零）时按原文比：键的末尾是 0x00 加原文。
func NaturalKey(name string) []byte {
	key := make([]byte, 0, len(name)*2+2)
	for i := 0; i < len(name); {
		c := name[i]
		if c >= '0' && c <= '9' {
			j := i
			for j < len(name) && name[j] >= '0' && name[j] <= '9' {
				j++
			}
			digits := strings.TrimLeft(name[i:j], "0")
			if digits == "" {
				digits = "0"
			}
			n := min(len(digits), 255)
			key = append(key, '0', byte(n))
			key = append(key, digits[:n]...)
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(name[i:])
		key = utf8.AppendRune(key, unicode.ToLower(r))
		i += size
	}
	key = append(key, 0)
	return append(key, name...)
}
