// Package similarity 页面内容相似度（SimHash）
//
// 采用 Google SimHash 思路：把文本映射成 64 位指纹，相似内容的指纹汉明距离小。
// 特征提取用「字符 3-gram」而非中文分词，避免引入 CGO 依赖（gojieba），
// 对中文页面同样有效——中文单字信息密度高，3-gram 已能覆盖主要语义片段。
package similarity

import (
	"math/bits"
	"strings"
	"unicode"
)

const (
	// offset64 FNV-1a 初始值
	offset64 = 14695981039346656037
	// prime64 FNV-1a 素数
	prime64 = 1099511628211
	// GramSize 特征切片长度
	GramSize = 3
	// MaxBodyChars 参与计算的最大字符数（防止超大页面拖慢）
	MaxBodyChars = 50000
)

// fnv1a64 FNV-1a 64 位哈希
func fnv1a64(s string) uint64 {
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}

// keyTextPattern 提取 <title> 与 <h1>-<h3> 内容，作为高权重特征
func keyTextPattern(body string) []string {
	lower := strings.ToLower(body)
	var out []string
	for _, tag := range []string{"title", "h1", "h2", "h3"} {
		open := "<" + tag
		closeTag := "</" + tag + ">"
		start := 0
		for {
			i := strings.Index(lower[start:], open)
			if i < 0 {
				break
			}
			i += start
			gt := strings.IndexByte(body[i:], '>')
			if gt < 0 {
				break
			}
			contentStart := i + gt + 1
			j := strings.Index(lower[contentStart:], closeTag)
			if j < 0 {
				break
			}
			txt := strings.TrimSpace(body[contentStart : contentStart+j])
			if txt != "" {
				out = append(out, txt)
			}
			start = contentStart + j + len(closeTag)
		}
	}
	return out
}

// normalize 去掉标签、脚本、样式、空白，只保留可见文本
// 关键文本（title/h1-h3）重复 5 次提高权重——同一系统改版时标题区通常稳定，
// 而换系统时标题区必然大变，加权后区分度明显提升
func normalize(body string) []rune {
	var b strings.Builder
	b.Grow(len(body))
	// 简单状态机：跳过 <script>/<style> 内容，其余标签去掉但保留文本
	lower := strings.ToLower(body)
	skip := 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '<' {
			rest := lower[i:]
			switch {
			case strings.HasPrefix(rest, "<script"):
				skip++
			case strings.HasPrefix(rest, "<style"):
				skip++
			case strings.HasPrefix(rest, "</script"):
				if skip > 0 {
					skip--
				}
			case strings.HasPrefix(rest, "</style"):
				if skip > 0 {
					skip--
				}
			}
			// 跳到标签结束
			if j := strings.IndexByte(body[i:], '>'); j >= 0 {
				i += j
			} else {
				break
			}
			continue
		}
		if skip == 0 {
			b.WriteByte(c)
		}
	}

	// 只保留字母/数字/汉字，其余当分隔
	out := make([]rune, 0, b.Len())
	for _, r := range b.String() {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, unicode.ToLower(r))
		}
	}
	// 关键文本加权
	var weighted []rune
	for _, k := range keyTextPattern(body) {
		for _, r := range k {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				weighted = append(weighted, unicode.ToLower(r))
			}
		}
	}
	for w := 0; w < 5; w++ {
		out = append(out, weighted...)
	}

	if len(out) > MaxBodyChars {
		out = out[:MaxBodyChars]
	}
	return out
}

// Fingerprint 计算页面内容的 64 位 SimHash 指纹
// body 为空或过短时返回 0（表示无法判定，调用方应跳过对比）
func Fingerprint(body string) uint64 {
	runes := normalize(body)
	if len(runes) < GramSize {
		return 0
	}
	var vec [64]int
	for i := 0; i+GramSize <= len(runes); i++ {
		h := fnv1a64(string(runes[i : i+GramSize]))
		for b := 0; b < 64; b++ {
			if h&(1<<uint(b)) != 0 {
				vec[b]++
			} else {
				vec[b]--
			}
		}
	}
	var sig uint64
	for b := 0; b < 64; b++ {
		if vec[b] > 0 {
			sig |= 1 << uint(b)
		}
	}
	if sig == 0 {
		return 0
	}
	return sig
}

// Hamming 两个指纹的汉明距离
func Hamming(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// Changed 判断页面是否发生实质变化
// 距离 > threshold 视为变化。Google 论文经验值 64 位取 3；
// 页面改版通常涉及大量文本，实际距离会远大于 3，这里默认阈值放到 8 更稳。
func Changed(oldFP, newFP uint64, threshold int) bool {
	if oldFP == 0 || newFP == 0 {
		return false // 任一侧缺指纹，不下结论
	}
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	return Hamming(oldFP, newFP) > threshold
}

// DefaultThreshold 默认汉明距离阈值
// 实测（含加权）：同一系统小改版距离约 10~14，完全不同系统约 28~35
// 取 16 落在中间，留出两侧余量
const DefaultThreshold = 16
