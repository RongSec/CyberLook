package scanner

import "testing"

// v0.0.55：extractTitle 换成 x/net/html tokenizer 后的回归用例。
// 每条都对应旧实现的一个真实错法，不是凑数。
func TestExtractTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"基本", `<html><head><title>示例集团OA</title></head></html>`, "示例集团OA"},
		{"大写标签", `<HTML><HEAD><TITLE>IIS Windows Server</TITLE>`, "IIS Windows Server"},
		{
			// 旧实现：找第一个 ">" 就把属性值劈开，结果拿到 `b">示例平台`
			"属性值含右尖括号",
			`<title data-v="a>b">示例平台</title>`,
			"示例平台",
		},
		{
			// 旧实现：script 字符串里的 <title> 会先被命中
			"script 里的假 title",
			`<script>document.write("<title>FAKE</title>")</script><title>真标题</title>`,
			"真标题",
		},
		{
			// 旧实现：只还原 5 个命名实体，数字实体原样留下
			"数字实体还原",
			`<title>&#x793A;&#x4F8B;&#10;OA</title>`,
			"示例 OA",
		},
		{
			"命名实体与空白折叠",
			"<title>\n   Tomcat &amp; Co\t\n</title>",
			"Tomcat & Co",
		},
		{
			// 旧实现：内嵌标签原样留在标题里
			"内嵌标签",
			`<title><b>登录</b> 系统</title>`,
			"登录 系统",
		},
		{
			// 旧实现：没有 </title> 直接返回空
			"未闭合（响应被截断）",
			`<html><title>数据平台`,
			"数据平台",
		},
		{"注释里的 title 不算", `<!-- <title>假的</title> --><title>真的</title>`, "真的"},
		{"无 title", `<html><body>hello</body></html>`, ""},
		{"空响应", ``, ""},
		{"body 里的 title 也能取", `<body><title>B</title></body>`, "B"},
	}
	for _, c := range cases {
		if got := extractTitle(c.in); got != c.want {
			t.Errorf("%s: extractTitle(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// 长标题按 rune 截断，不能切出半个 UTF-8 字符。
func TestExtractTitleRuneTruncate(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "中"
	}
	got := extractTitle("<title>" + long + "</title>")
	if len([]rune(got)) != 200 {
		t.Fatalf("rune 长度 = %d, want 200", len([]rune(got)))
	}
	for _, r := range got {
		if r != '中' {
			t.Fatalf("截断产生乱码字符: %q", r)
		}
	}
}

// HTMLTitle 必须与 extractTitle 行为一致（对外入口，识别链依赖它）。
func TestHTMLTitleExported(t *testing.T) {
	if got := HTMLTitle("<title>X</title>"); got != "X" {
		t.Fatalf("HTMLTitle = %q", got)
	}
}
