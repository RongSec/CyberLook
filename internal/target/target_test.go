package target

import "testing"

// TestExcluderWildcard 免扫描清单的默认项 *.*.*.1 必须真的生效。
//
// 这是回归测试：老实现把排除项先 Expand 成 IP 集合，而 `*.*.*.1` 会被展开成
// 0.0.0.1/0（前缀位数 < 16 → 原样返回字符串），于是这条排除永远匹配不上任何 IP，
// 网段网关照样被当存活主机塞进 65535 端口的扫描队列。
func TestExcluderWildcard(t *testing.T) {
	ex := NewExcluder([]string{"*.*.*.1"})
	for _, ip := range []string{"10.24.2.1", "10.24.48.1", "172.16.0.1", "10.193.128.1"} {
		if !ex.Contains(ip) {
			t.Fatalf("%s 应被 *.*.*.1 排除", ip)
		}
	}
	for _, ip := range []string{"10.24.2.2", "10.24.2.11", "10.24.48.254", "10.24.1.10"} {
		if ex.Contains(ip) {
			t.Fatalf("%s 不应被排除", ip)
		}
	}
}

func TestExcluderMixed(t *testing.T) {
	ex := NewExcluder([]string{"10.24.4.0/24", "10.24.5.10-20", "10.24.6.*", "10.24.7.9"})
	cases := map[string]bool{
		"10.24.4.33":  true,  // CIDR
		"10.24.5.15":  true,  // 范围
		"10.24.6.200": true,  // 通配
		"10.24.7.9":   true,  // 单 IP
		"10.24.7.10":  false, // 相邻 IP 不能被误伤
		"10.24.5.21":  false, // 范围外
		"10.24.8.1":   false,
	}
	for ip, want := range cases {
		if got := ex.Contains(ip); got != want {
			t.Fatalf("%s: got %v want %v", ip, got, want)
		}
	}
	// 空清单不能排除任何东西
	empty := NewExcluder(nil)
	if empty.Contains("10.24.2.1") {
		t.Fatal("空清单不应排除任何 IP")
	}
}

func TestResolveDropsGateway(t *testing.T) {
	got := Resolve([]string{"10.24.9.0/24"}, []string{"*.*.*.1"})
	if len(got) != 255 {
		t.Fatalf("期望 255 个（去掉 .1），实际 %d", len(got))
	}
	for _, ip := range got {
		if ip == "10.24.9.1" {
			t.Fatal("网关 10.24.9.1 应被排除")
		}
	}
}
