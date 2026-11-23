package service

// v0.0.55 L0 规则判定的单测：这一层决定"要不要花 1~3 秒调模型、0~30 秒开浏览器"。
// 判宽了会把登录壳的名字直接入库（误报），判窄了等于什么都没省（退化回 0.0.54）。

import "testing"

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
