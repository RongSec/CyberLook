package svcid

// murmur3 x86_32 —— favicon hash 的口径与 Shodan/FOFA 一致：
// 先对 favicon 字节做 base64（每 76 字符插入换行），再算 murmur3_32，最后按有符号 int32 输出。
// 只依赖标准库，不引第三方。

import "encoding/base64"

// Mmh3Sum32 计算 mmh3 32 位（无符号）
func Mmh3Sum32(data []byte) uint32 {
	const (
		c1 = 0xcc9e2d51
		c2 = 0x1b873593
	)
	var h1 uint32
	n := len(data)
	i := 0
	for ; i+4 <= n; i += 4 {
		k1 := uint32(data[i]) | uint32(data[i+1])<<8 | uint32(data[i+2])<<16 | uint32(data[i+3])<<24
		k1 *= c1
		k1 = (k1 << 15) | (k1 >> 17)
		k1 *= c2
		h1 ^= k1
		h1 = (h1 << 13) | (h1 >> 19)
		h1 = h1*5 + 0xe6546b64
	}
	var k1 uint32
	switch n & 3 {
	case 3:
		k1 ^= uint32(data[i+2]) << 16
		fallthrough
	case 2:
		k1 ^= uint32(data[i+1]) << 8
		fallthrough
	case 1:
		k1 ^= uint32(data[i])
		k1 *= c1
		k1 = (k1 << 15) | (k1 >> 17)
		k1 *= c2
		h1 ^= k1
	}
	h1 ^= uint32(n)
	h1 ^= h1 >> 16
	h1 *= 0x85ebca6b
	h1 ^= h1 >> 13
	h1 *= 0xc2b2ae35
	h1 ^= h1 >> 16
	return h1
}

// FaviconMMH3 按 Shodan 口径计算 favicon hash：
// base64 编码 + 每 76 字符换行，再算 mmh3，返回有符号 int32 的字符串形式。
func FaviconMMH3(icon []byte) int32 {
	if len(icon) == 0 {
		return 0
	}
	enc := base64.StdEncoding.EncodeToString(icon)
	// 每 76 字符插入 \n（Shodan 的做法），末尾也补一个 \n
	var buf []byte
	for i := 0; i < len(enc); i += 76 {
		end := i + 76
		if end > len(enc) {
			end = len(enc)
		}
		buf = append(buf, enc[i:end]...)
		buf = append(buf, '\n')
	}
	return int32(Mmh3Sum32(buf))
}
