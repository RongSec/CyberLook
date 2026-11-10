package scanner

import (
	"strings"
	"unicode/utf8"
)

// MergeIdentification 合并新旧识别结果
//
// 规则：
//  1. 旧值为空 → 用新值（没有对比基准）
//  2. 新值为空 → 用旧值
//  3. 旧值是通用词（Login/Loading）→ 用新值（新的更具体）
//  4. 语义一致（去空格/大小写/标点后相同）→ 用旧值（避免无意义"新变更"）
//  5. 都有值且不同 → 用旧值（默认不变更，除非新的明显更好）
//
// 返回：(最终值, 来源, 是否有变更)
func MergeIdentification(old, new string) (string, string, bool) {
	old = strings.TrimSpace(old)
	new = strings.TrimSpace(new)

	// 1. 旧值为空
	if old == "" {
		if new != "" {
			return new, "fingerprint", true
		}
		return "", "", false
	}

	// 2. 新值为空
	if new == "" {
		return old, "tscan", false
	}

	// 3. 旧值是通用词、新值有信息量 → 用新的
	if isGenericTitle(old) && !isGenericTitle(new) {
		return new, "fingerprint", true
	}

	// 3.1 两边都是通用词 → 保留旧值。
	// Login → Loading... 这种纯抖动不该产生"新变更"，否则每次扫描都刷一批黄条。
	if isGenericTitle(old) && isGenericTitle(new) {
		return old, "tscan", false
	}

	// 4. 表意一致 → 用旧值（不标变更）
	if titleSimilar(old, new) {
		return old, "tscan", false
	}

	// 5. 表意不一致 → 用新值，标变更（服务确实变了，要让人看）
	return new, "tscan", true
}

// isGenericTitle 是否是通用标题（无信息量）
func isGenericTitle(s string) bool {
	if s == "" {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(s))
	switch lower {
	case "login", "loading...", "loading", "not found", "index", "home",
		"welcome", "管理登录", "登录", "loading…", "欢迎登录":
		return true
	}
	// 纯数字/符号
	if len(lower) <= 2 {
		return true
	}
	return false
}

// semanticEqual 语义比较（去空格/大小写/标点/HTML 实体）
func semanticEqual(a, b string) bool {
	return normalizeForCompare(a) == normalizeForCompare(b)
}

// titleNoise 标题里的"页面噪声词"：登录页/后台/系统 这类词只影响写法，不影响"是哪套系统"。
// 归一化时剔掉，避免「中化云盘」和「中化云盘 - 登录」被当成两次变更。
var titleNoise = []string{
	"管理系统", "管理平台", "系统登录", "登录页", "登录界面", "管理后台", "后台管理", "管理中心", "控制台",
	"升级版", "新版", "旧版", "版本", "系统", "平台", "网站", "官网", "后台", "首页", "主页",
	"管理", "登录", "登陆", "欢迎",
	"loginpage", "login", "logon", "signin", "signon", "homepage", "home", "index",
	"welcome", "admin", "console", "portal", "dashboard", "system", "page",
}

// normTitle 归一化成「只表达业务含义」的形式
func normTitle(s string) string {
	s = normalizeForCompare(s)
	if s == "" {
		return ""
	}
	for _, w := range titleNoise {
		s = strings.ReplaceAll(s, w, "")
	}
	return s
}

// titleSimilar 判断两个标题是否「表意一致」（同一套业务系统，只是写法不同）。
// 判定顺序：归一化后相等 → 一方包含另一方且长度接近 → 3-gram Jaccard ≥ 0.8
func titleSimilar(a, b string) bool {
	na, nb := normTitle(a), normTitle(b)
	if na == "" || nb == "" {
		// 归一化后为空说明整个标题都是噪声词，退回原始严格比较
		return semanticEqual(a, b)
	}
	if na == nb {
		return true
	}
	// 一方包含另一方：加了"XX系统/登录页"之类的后缀
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		short, long := na, nb
		if utf8.RuneCountInString(short) > utf8.RuneCountInString(long) {
			short, long = long, short
		}
		if float64(utf8.RuneCountInString(short))/float64(utf8.RuneCountInString(long)) >= 0.5 {
			return true
		}
	}
	return gramJaccard(na, nb) >= 0.8
}

// gramJaccard 字符 3-gram Jaccard 相似度
func gramJaccard(a, b string) float64 {
	ga, gb := grams(a), grams(b)
	if len(ga) == 0 || len(gb) == 0 {
		return 0
	}
	inter := 0
	for g := range ga {
		if _, ok := gb[g]; ok {
			inter++
		}
	}
	union := len(ga) + len(gb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// grams 取字符级 3-gram（不足 3 个字符就用整体）
func grams(s string) map[string]struct{} {
	r := []rune(s)
	out := map[string]struct{}{}
	if len(r) < 3 {
		out[string(r)] = struct{}{}
		return out
	}
	for i := 0; i+3 <= len(r); i++ {
		out[string(r[i:i+3])] = struct{}{}
	}
	return out
}

// normalizeForCompare 归一化字符串用于比较
func normalizeForCompare(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// 去 HTML 实体
	s = strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", "\"", "&#39;", "'", "&nbsp;", " ",
		"&hellip;", "...",
	).Replace(s)
	// 去所有空白和标点
	var sb strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' {
			sb.WriteRune(r)
		} else if r >= 0x4e00 && r <= 0x9fff {
			// 中文字符
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// isClearlyBetter 判断新的是否明显更好
//
// "明显更好"的定义：
//   - 新的有产品名（浪潮/VMware/深信服等），旧的没有
//   - 新的比旧的长 3 倍以上（更详细）
//   - 旧的是通用词，新的不是
func isClearlyBetter(new, old string) bool {
	newLower := strings.ToLower(new)
	oldLower := strings.ToLower(old)

	// 旧的是通用词，新的不是
	if isGenericTitle(old) && !isGenericTitle(new) {
		return true
	}

	// 新的有产品名，旧的没有
	productKeywords := []string{
		"inspur", "浪潮", "vmware", "esxi", "深信服", "sangfor",
		"华为", "h3c", "hp", "ibm", "kong", "apache", "nginx",
		"exchange", "vcenter", "grafana", "zabbix", "jenkins",
	}
	newHasProduct := false
	oldHasProduct := false
	for _, kw := range productKeywords {
		if strings.Contains(newLower, kw) {
			newHasProduct = true
		}
		if strings.Contains(oldLower, kw) {
			oldHasProduct = true
		}
	}
	if newHasProduct && !oldHasProduct {
		return true
	}

	// 新的明显更长（更详细）
	if len(new) > len(old)*3 && len(new) > 20 {
		return true
	}

	return false
}

// MergeFingerprint 合并指纹（同样的规则）
func MergeFingerprint(old, new string) (string, string, bool) {
	old = strings.TrimSpace(old)
	new = strings.TrimSpace(new)

	if old == "" {
		if new != "" {
			return new, "fingerprint", true
		}
		return "", "", false
	}
	if new == "" {
		return old, "tscan", false
	}

	// 语义一致（指纹用逗号分割后比较）
	if fingerprintEqual(old, new) {
		return old, "tscan", false
	}

	// 新的有更多产品名 → 用新的
	oldProducts := countProducts(old)
	newProducts := countProducts(new)
	if newProducts > oldProducts {
		return new, "fingerprint", true
	}

	// 默认用旧的
	return old, "tscan", false
}

// fingerprintEqual 指纹比较（去空格/大小写，逗号分隔后比较集合）
func fingerprintEqual(a, b string) bool {
	na := normalizeFingerprint(a)
	nb := normalizeFingerprint(b)
	return na == nb
}

func normalizeFingerprint(s string) string {
	s = strings.ToLower(s)
	parts := strings.Split(s, ",")
	var cleaned []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			cleaned = append(cleaned, p)
		}
	}
	return strings.Join(cleaned, ",")
}

// countProducts 统计指纹里的产品名数量
func countProducts(s string) int {
	lower := strings.ToLower(s)
	products := []string{
		"inspur", "浪潮", "vmware", "esxi", "深信服", "sangfor",
		"华为", "h3c", "hp", "ibm", "kong", "apache", "nginx",
		"exchange", "vcenter", "grafana", "zabbix", "jenkins",
		"ami", "american megatrends", "lexmark", "enableq",
	}
	count := 0
	for _, p := range products {
		if strings.Contains(lower, p) {
			count++
		}
	}
	return count
}
