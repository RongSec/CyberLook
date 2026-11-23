package service

// v0.0.55 L0 规则判定的单测：这一层决定"要不要花 1~3 秒调模型、0~30 秒开浏览器"。
// 判宽了会把登录壳的名字直接入库（误报），判窄了等于什么都没省（退化回 0.0.54）。

import (
	"testing"

	"asset-platform/internal/scanner"
)

func TestTitleIsAllNoise(t *testing.T) {
	noise := []string{"登录", " 首页 ", "Login", "WELCOME", "控制台", "管理后台", "Home page", "登录页"}
	for _, s := range noise {
		if !titleIsAllNoise(s) {
			t.Errorf("titleIsAllNoise(%q) = false, want true", s)
		}
	}
	real := []string{"数据智能平台", "SAP NetWeaver", "OA 系统登录页", "Harbor", "GitLab", "深信服 SSL VPN"}
	for _, s := range real {
		if titleIsAllNoise(s) {
			t.Errorf("titleIsAllNoise(%q) = true, want false", s)
		}
	}
}

func TestTitleHasNoisySegment(t *testing.T) {
	for _, s := range []string{
		"数据智能平台 - 登录",
		"统一身份认证 | 首页",
		"OA系统_登录页",
		"管理平台 · 控制台",
		"Tomcat Manager: 管理后台",
	} {
		if !titleHasNoisySegment(s) {
			t.Errorf("titleHasNoisySegment(%q) = false, want true", s)
		}
	}
	no := []string{"Harbor", "示例集团-OA系统", "SAP NetWeaver", "IIS Windows Server"}
	for _, s := range no {
		if titleHasNoisySegment(s) {
			t.Errorf("titleHasNoisySegment(%q) = true, want false", s)
		}
	}
}

// SPA 骨架：正文几乎是空 div + 外部 bundle → 必须交给判断/渲染。
func TestNeedsJudge(t *testing.T) {
	spa := `<html><head><title>Vue App</title></head><body><div id="app"></div>` +
		`<script src="/static/js/app.1234.js"></script></body></html>`
	if !needsJudge([]scanner.Hop{hop("200 OK", "text/html", spa)}) {
		t.Error("SPA 骨架应需要判断 agent")
	}

	// 有正文、无外部脚本：正文足够，不需要判断（但 title 是构建默认值，识别仍会跑）
	full := `<html><head><title>Vue App</title></head><body>` +
		"<p>示例集团统一门户，提供单点登录、待办、邮件、通讯录等功能。</p>" +
		"<p>版权所有 示例信息技术有限公司 京ICP备00000000号 建议使用 Chrome 浏览器访问本站。</p>" +
		"<p>如遇登录问题请联系管理员 400-000-0000 或访问帮助中心获取更多信息与操作指引说明文档。</p>" +
		"</body></html>"
	if needsJudge([]scanner.Hop{hop("200 OK", "text/html", full)}) {
		t.Error("正文完整的页面不该再问判断 agent")
	}

	// 错误页：本地硬闸门会拦渲染，不必问模型
	if needsJudge([]scanner.Hop{hop("502 Bad Gateway", "text/html", "bad gateway")}) {
		t.Error("5xx 不该问判断 agent")
	}
}
