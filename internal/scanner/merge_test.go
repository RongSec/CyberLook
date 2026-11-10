package scanner

import "testing"

// TestMergeIdentification 三条口径：
//  1. 新旧表意一致 → 用旧（不标变更）
//  2. 新旧不一致   → 用新（标变更）
//  3. 两边都是通用词 → 保留旧值（避免 Login/Loading 抖动刷"新变更"）
func TestMergeIdentification(t *testing.T) {
	cases := []struct {
		name          string
		old, new      string
		wantMerged    string
		wantChanged   bool
		wantSourceAt  string // 只校验 changed 时有意义的来源，空表示不校验
	}{
		// ── 表意一致：写法不同，业务是同一套，必须保留旧值 ──
		{"纯后缀：登录", "中化云盘", "中化云盘 - 登录", "中化云盘", false, ""},
		{"纯后缀：首页", "中化云盘", "中化云盘首页", "中化云盘", false, ""},
		{"全角括号", "会议系统", "会议系统（新版）", "会议系统", false, ""},
		{"去系统二字", "OA系统", "OA", "OA系统", false, ""},
		{"大小写标点", "GitLab", "gitlab · 登录", "GitLab", false, ""},
		{"空格差异", "FutureMatrix LAN Switch 管理登录", "FutureMatrix  LAN  Switch 管理登录", "FutureMatrix LAN Switch 管理登录", false, ""},
		{"完全相同", "会议系统", "会议系统", "会议系统", false, ""},

		// ── 表意不一致：服务真的变了，用新值并标变更 ──
		{"完全不同", "会议系统", "财务系统", "财务系统", true, "tscan"},
		{"变成错误页", "FutureMatrix LAN Switch 管理登录", "403 Forbidden", "403 Forbidden", true, "tscan"},
		{"换产品", "用友NC", "致远OA", "致远OA", true, "tscan"},

		// ── 通用词之间抖动：保留旧值 ──
		{"通用词抖动", "Login", "Loading...", "Login", false, ""},

		// ── 通用词 → 有信息量：用新值 ──
		{"旧泛词新具体", "Login", "中化OA统一登录", "中化OA统一登录", true, "fingerprint"},

		// ── 边界 ──
		{"旧空", "", "中化云盘", "中化云盘", true, "fingerprint"},
		{"新空", "中化云盘", "", "中化云盘", false, ""},
	}

	for _, c := range cases {
		got, src, changed := MergeIdentification(c.old, c.new)
		if got != c.wantMerged {
			t.Errorf("[%s] merged=%q 期望 %q（old=%q new=%q）", c.name, got, c.wantMerged, c.old, c.new)
		}
		if changed != c.wantChanged {
			t.Errorf("[%s] changed=%v 期望 %v（old=%q new=%q）", c.name, changed, c.wantChanged, c.old, c.new)
		}
		if c.wantSourceAt != "" && src != c.wantSourceAt {
			t.Errorf("[%s] source=%q 期望 %q", c.name, src, c.wantSourceAt)
		}
	}
}

// TestTitleSimilar 相似度判定单测
func TestTitleSimilar(t *testing.T) {
	same := [][2]string{
		{"中化云盘", "中化云盘-登录"},
		{"会议系统", "会议系统管理平台"},
		{"内网负载均衡-备", "内网负载均衡-备 管理"},
		{"Elasticsearch", "elasticsearch head"},
		{"海波龙系统", "海波龙"},
	}
	for _, p := range same {
		if !titleSimilar(p[0], p[1]) {
			t.Errorf("应判为一致：%q vs %q", p[0], p[1])
		}
	}
	diff := [][2]string{
		{"会议系统", "财务系统"},
		{"用友NC", "致远OA"},
		{"中化云盘", "403 Forbidden"},
		{"OA", "VPN"},
	}
	for _, p := range diff {
		if titleSimilar(p[0], p[1]) {
			t.Errorf("应判为不一致：%q vs %q", p[0], p[1])
		}
	}
}
