package proxy

import "unicode/utf8"

// splitUTF8 把上次剩下的字节和这次读到的拼起来，返回能完整解码的部分和留到下次的结尾（被读断的多字节字符，最多 3 字节）。
// 流式转发时一次 Read 可能在一个汉字中间断开，直接转成字符串会变成乱码。
func splitUTF8(carry, chunk []byte) (string, []byte) {
	b := append(carry, chunk...)
	cut := len(b)
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax+1; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				cut = i
			}
			break
		}
	}
	rest := make([]byte, len(b)-cut)
	copy(rest, b[cut:])
	return string(b[:cut]), rest
}
