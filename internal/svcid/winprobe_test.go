package svcid

import (
	"testing"
	"unicode/utf8"
)

// TestNormalizeProduct 覆盖缺陷①修复：Title Case 分支不得按字节切多字节字符
// （旧实现 p[:1] 把 UTF-8 中文首字节切碎 → 非法 UTF-8 → U+FFFD 乱码）。
func TestNormalizeProduct(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		// 缺陷①核心用例：中文+分隔符，首段中文必须原样保留
		{"中国电信-天翼宽带政企网关", "中国电信 天翼宽带政企网关"},
		// 中文无分隔符：不进 Title Case 分支，原样返回
		{"浪潮服务器lpmi管理口", "浪潮服务器lpmi管理口"},
		// 既有英文行为回归：不在表内的 snake_case → Title Case
		{"internet_information_server_2019", "Internet Information Server 2019"},
		{"some_vendor_device", "Some Vendor Device"},
		// canonicalProduct 表内值行为不变（查表优先于 Title Case）
		{"nginx", "nginx"},
		{"Nginx", "nginx"},
		{"vmware_esxi", "VMware ESXi"}, // 含分隔符但在表内命中，不进 Title Case
		{"tomcat", "Apache Tomcat"},
		// 中英混合段：ASCII 段照常大写，中文段原样
		{"vmware-天翼", "Vmware 天翼"},
		{"abc-中国电信", "Abc 中国电信"},
		// 全 ASCII 多段回归
		{"spring-boot-admin-x", "Spring Boot Admin X"},
		// 分隔符开头：FieldsFunc 丢空段，剩余 ASCII 段大写
		{"-telecom-gw", "Telecom Gw"},
	}
	for _, c := range cases {
		got := normalizeProduct(c.in)
		if got != c.want {
			t.Errorf("normalizeProduct(%q) = %q, want %q", c.in, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("normalizeProduct(%q) = %q is not valid UTF-8", c.in, got)
		}
	}
}

// TestNormalizeProductNoReplacementChar 显式断言输出不含 U+FFFD（乱码哨兵），
// 覆盖规则库中带分隔符的国产设备产品名。
func TestNormalizeProductNoReplacementChar(t *testing.T) {
	inputs := []string{
		"中国电信-天翼宽带政企网关",
		"深信服-下一代防火墙 Ngaf",
		"锐捷-Rg Ew1200g",
		"浪潮-服务器lpmi管理口",
		"华为-USG6000系列防火墙",
	}
	for _, s := range inputs {
		got := normalizeProduct(s)
		for _, r := range got {
			if r == utf8.RuneError {
				t.Fatalf("normalizeProduct(%q) = %q contains U+FFFD", s, got)
			}
		}
	}
}
