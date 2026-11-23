package service

// 渲染闸门的单测：这一步决定"要不要花 10~30 秒打开浏览器"，
// 判错的代价是每条浪费十几秒（放过错误页）或者漏掉真系统（把 JS 登录页跳过）。

import (
	"testing"

	"asset-platform/internal/scanner"
)

func hop(status, ctype, body string) scanner.Hop {
	return scanner.Hop{
		ResponseLine:    "HTTP/1.1 " + status,
		ResponseHeaders: "Content-Type: " + ctype,
		Body:            body,
	}
}

func TestRenderWorthwhile(t *testing.T) {
	// v0.0.56：这里的语义收窄成"物理上能不能拿到更多东西"——只拦非 HTML。
	// 502/403/404 这些错误页现在**允许**渲染：要不要渲染由识别 agent 的
	// need_render 决定，不再由本地阈值拍板（阈值换环境就失效，属于特调）。
	cases := []struct {
		name string
		hops []scanner.Hop
		want bool
	}{
		{"无响应", nil, false},
		{"无状态行", []scanner.Hop{{ResponseLine: "garbage"}}, false},
		{"502 错误页（HTML，交给模型判）", []scanner.Hop{hop("502 Bad Gateway", "text/html", `<html><body><h1>502 Bad Gateway</h1></body></html>`)}, true},
		{"404 错误页（同上）", []scanner.Hop{hop("404 Not Found", "text/html", `<html><body>Not Found</body></html>`)}, true},
		{"403 登录页", []scanner.Hop{hop("403 Forbidden", "text/html", `<html><body>403 Forbidden</body></html>`)}, true},
		{"JSON 接口", []scanner.Hop{hop("200 OK", "application/json", `{"code":0}`)}, false},
		{"纯文本 pong", []scanner.Hop{hop("200 OK", "text/plain", `pong`)}, false},
		{"正常首页", []scanner.Hop{hop("200 OK", "text/html", `<html><title>OA</title><body>欢迎</body></html>`)}, true},
		{"SPA 空壳（有脚本无正文）", []scanner.Hop{hop("200 OK", "text/html",
			`<!doctype html><html><body><div id="app"></div><script src="/a.js"></script></body></html>`)}, true},
		{"无 Content-Type 但有脚本", []scanner.Hop{hop("200 OK", "", `<div id="root"></div><script src="/v.js"></script>`)}, true},
	}
	for _, c := range cases {
		got, why := renderWorthwhile(c.hops)
		if got != c.want {
			t.Fatalf("%s：期望 渲染=%v，实际 %v（%s）", c.name, c.want, got, why)
		}
	}
}

// hopStatus 必须能从各种响应行里取到状态码
func TestHopStatus(t *testing.T) {
	cases := map[string]int{
		"HTTP/1.1 502 Bad Gateway": 502,
		"HTTP/1.0 302 Found":       302,
		"HTTP/2.0 200":             200,
		"garbage":                  0,
	}
	for line, want := range cases {
		if got := hopStatus(line); got != want {
			t.Fatalf("%q 期望 %d，实际 %d", line, want, got)
		}
	}
}
