package scanner

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// ensureUTF8 把 GBK/GB2312/GB18030 页面转成 UTF-8。
//
// 为什么必须有这一步：内网大量老系统（政企 OA、设备管理页）用 GBK，
// 之前只做 utf8.Valid 判断，非法就直接当成"二进制内容"把 body 丢掉——
// 于是 AI 只看到响应头，中文标题一个字都拿不到，识别自然不准。
//
// GB18030 是 GBK / GB2312 的超集，用它一个解码器就够。
// 注意：只在 utf8 非法时尝试，避免把本来就是 UTF-8 的内容二次解码搞坏。
func ensureUTF8(b []byte) []byte {
	if len(b) == 0 || utf8.Valid(b) {
		return b
	}
	out, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), b)
	if err != nil || len(out) == 0 {
		return b
	}
	if !utf8.Valid(out) {
		return b
	}
	return out
}

// sniffCharset 从 HTML 前 4KB 里找 <meta charset> / http-equiv 声明。
// 仅用于日志与判断是否为中文站点；实际解码统一走 GB18030。
func sniffCharset(body string) string {
	head := body
	if len(head) > 4096 {
		head = head[:4096]
	}
	low := strings.ToLower(head)
	for _, key := range []string{"charset=", "charset=\""} {
		if i := strings.Index(low, key); i >= 0 {
			rest := low[i+len(key):]
			rest = strings.TrimLeft(rest, "\"' ")
			end := 0
			for end < len(rest) && (isAlnum(rest[end]) || rest[end] == '-' || rest[end] == '_') {
				end++
			}
			if end > 0 {
				return rest[:end]
			}
		}
	}
	return ""
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
