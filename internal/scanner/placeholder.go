package scanner

import (
	"regexp"
	"strings"
)

// ===== 「标题是否结构性无效」判定 =====
//
// 这里不做「这个名字准不准」的语义判断（那交给模型），只判断一件事：
// 这个标题里**是否可能包含任何身份信息**。
//
// 起因：引擎的 needAi 标记漏掉了大量壳页。
// 实测库内 2848 条 HTTP 服务，2186 条（77%）从未走过 AI 识别，其中
// `Redirection……` 39 条、`HTTP` 671 条、`307 TEMPORARY REDIRECT` 5 条、
// 空标题 248 条。这些标题被引擎当成"已有标题"而跳过 AI，
// 于是 `198.18.8.65` 这种深信服超融合一直显示成「Redirection……」。
//
// 判定刻意保持"结构性"：只看形态，不做白名单灰度，避免又变成调不完的正则。
var reStatusText = regexp.MustCompile(`^\d{3}\s+[A-Z][A-Z0-9 \-/]*$`)

// 状态文本的常规大小写写法：`404 Not Found`、`403 Forbidden`。
// 上面那条只认全大写（`307 TEMPORARY REDIRECT`），漏掉了 IIS/nginx 常见的
// `HTTP Status 404 – Not Found`、`404 Not Found` 这类混排，这里补一条短语表。
var reStatusPhrase = regexp.MustCompile(`(?i)^\d{3}\s+(not found|forbidden|unauthorized|internal server error|` +
	`temporary redirect|permanent redirect|moved permanently|moved temporarily|found|bad request|` +
	`service unavailable|bad gateway|gateway timeout|not acceptable|request timeout)\b`)

// PlaceholderTitle 标题是否为结构性占位（不含身份信息）。
func PlaceholderTitle(title, statusCode string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return true
	}
	low := strings.ToLower(t)

	// 1) HTTP 状态文本被当标题：`307 TEMPORARY REDIRECT`、`404 Not Found`
	if reStatusText.MatchString(t) || reStatusPhrase.MatchString(t) {
		return true
	}

	// 2) 跳转壳页：`Redirection……`、`Redirecting to HTTPS`、`重定向中...`
	if strings.Contains(low, "redirect") ||
		strings.Contains(t, "重定向") || strings.Contains(t, "跳转") ||
		strings.Contains(t, "正在跳转") || strings.Contains(t, "正在重定向") {
		return true
	}

	// 3) 协议名 / 服务器名被当标题，本身不指向任何业务系统
	switch low {
	case "http", "https", "http server", "https server", "generic http server",
		"webserver", "web server", "unknown", "unknow", "httpd", "iis",
		"login", "logon", "welcome", "index of /", "it works!":
		return true
	}

	// 4) 通用登录壳 / 目录列表 / 默认欢迎页
	if strings.HasPrefix(low, "index of /") ||
		strings.HasPrefix(low, "welcome to nginx") ||
		strings.HasPrefix(low, "welcome to iis") ||
		strings.HasPrefix(low, "apache2 ") ||
		strings.HasPrefix(low, "test page for") {
		return true
	}

	// 5) 错误页标题：拿不到业务身份
	if strings.HasPrefix(low, "error ") || strings.HasPrefix(low, "http status 4") ||
		strings.HasPrefix(low, "http状态 4") || strings.HasPrefix(low, "http状态 5") {
		return true
	}

	// 6) 状态码本身是错误，且标题没有实质内容
	if statusCode != "" && len([]rune(t)) <= 12 {
		for _, p := range []string{"4", "5"} {
			if strings.HasPrefix(strings.TrimSpace(statusCode), p) {
				return true
			}
		}
	}
	return false
}

// TraceWeak 抓到的报文是否是「弱证据」——全是壳页、错误页或纯跳转页，
// 没有任何可判读的页面内容。这种情况必须继续走浏览器渲染，
// 不能让模型一句 sufficient=true 把渲染跳过。
func TraceWeak(hops []Hop) bool {
	if len(hops) == 0 {
		return true
	}
	for _, h := range hops {
		if visibleTextLen(h.Body) >= 200 {
			return false
		}
	}
	return true
}

// visibleTextLen 粗略统计响应体里的"可见文字"长度：
// 去掉标签、脚本、样式后还剩多少字。
func visibleTextLen(body string) int {
	if body == "" {
		return 0
	}
	if len(body) > 64*1024 {
		body = body[:64*1024]
	}
	low := strings.ToLower(body)
	// 去掉 script/style 整段
	for _, tag := range []string{"script", "style"} {
		for {
			s := strings.Index(low, "<"+tag)
			if s < 0 {
				break
			}
			e := strings.Index(low[s:], "</"+tag+">")
			if e < 0 {
				break
			}
			body = body[:s] + body[s+e+len("</"+tag+">"):]
			low = strings.ToLower(body)
		}
	}
	// 去标签
	var b strings.Builder
	in := false
	for _, r := range body {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			if r != '\n' && r != '\r' && r != '\t' && r != ' ' {
				b.WriteRune(r)
			}
		}
	}
	return len([]rune(b.String()))
}
