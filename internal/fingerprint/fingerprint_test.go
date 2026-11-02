package fingerprint

import (
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// startMock 起一个模拟站点，返回 baseURL 和关闭函数
func startMock(html string, headers map[string]string) (*httptest.Server, string) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/style.css" {
			w.Header().Set("Content-Type", "text/css")
			w.Write([]byte(".uedc-ppkg-login_browser{}.uedc-ppkg-login_title{}"))
			return
		}
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.Write([]byte(html))
	}))
	return srv, srv.URL
}

// TestL1Title 标题提取
func TestL1Title(t *testing.T) {
	html := `<html><head><title>浪潮 BMS 管理系统</title></head><body></body></html>`
	srv, url := startMock(html, nil)
	defer srv.Close()

	r := identifyURL(url)
	if r.Title != "浪潮 BMS 管理系统" {
		t.Errorf("title 期望「浪潮 BMS 管理系统」，得到「%s」", r.Title)
	}
	if r.Confidence < 50 {
		t.Errorf("置信度应 >= 50，得到 %d", r.Confidence)
	}
}

// TestL1Meta 从 meta description 识别产品
func TestL1Meta(t *testing.T) {
	html := `<html><head><title></title>
<meta name="description" content="ESXi Host Client">
</head><body></body></html>`
	srv, url := startMock(html, nil)
	defer srv.Close()

	r := identifyURL(url)
	if r.MetaDesc != "ESXi Host Client" {
		t.Errorf("metaDesc 期望「ESXi Host Client」，得到「%s」", r.MetaDesc)
	}
	if !strings.Contains(r.Fingerprint, "VMware") {
		t.Errorf("指纹应包含 VMware，得到「%s」", r.Fingerprint)
	}
}

// TestL1Comment 从 HTML 注释识别
func TestL1Comment(t *testing.T) {
	html := `<html><head><title>Management System</title></head>
<body><!-- (C) COPYRIGHT American Megatrends Inc. 2016-2017 --></body></html>`
	srv, url := startMock(html, nil)
	defer srv.Close()

	r := identifyURL(url)
	if !strings.Contains(r.Fingerprint, "AMI BMC") {
		t.Errorf("指纹应包含 AMI BMC，得到「%s」", r.Fingerprint)
	}
}

// TestL1Framework 框架检测
func TestL1Framework(t *testing.T) {
	html := `<html><head><title></title></head>
<body ng-app="esxUiApp"></body></html>`
	srv, url := startMock(html, nil)
	defer srv.Close()

	r := identifyURL(url)
	if !strings.Contains(r.Framework, "AngularJS") {
		t.Errorf("框架应识别为 AngularJS，得到「%s」", r.Framework)
	}
}

// TestL1ServerHeader Server 头识别
func TestL1ServerHeader(t *testing.T) {
	html := `<html><head><title></title></head><body></body></html>`
	srv, url := startMock(html, map[string]string{"Server": "A01-27/25U-FM5735_24"})
	defer srv.Close()

	r := identifyURL(url)
	if r.Server == "" {
		t.Error("应提取到 Server 头")
	}
}

// TestGenericTitle 通用标题不给高分
func TestGenericTitle(t *testing.T) {
	for _, title := range []string{"Login", "Loading...", "Not Found", ""} {
		html := `<html><head><title>` + title + `</title></head><body></body></html>`
		srv, url := startMock(html, nil)
		r := identifyURL(url)
		srv.Close()
		if r.Confidence >= 50 {
			t.Errorf("标题「%s」不应给 >= 50 分，得到 %d", title, r.Confidence)
		}
	}
}

// TestL2CSSFallback L2 CSS 分析兜底
func TestL2CSSFallback(t *testing.T) {
	html := `<html><head>
<title>Login</title>
<link rel="stylesheet" href="/style.css">
</head><body></body></html>`
	srv, url := startMock(html, nil)
	defer srv.Close()

	r := identifyURL(url)
	if !strings.Contains(r.Fingerprint, "uedc-ppkg") {
		t.Errorf("L2 应从 CSS 提取 uedc-ppkg，得到「%s」", r.Fingerprint)
	}
	if r.Layer != "L2" {
		t.Errorf("层应为 L2，得到「%s」", r.Layer)
	}
}

// TestGzipDecode gzip 自动解压
func TestGzipDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/html")
		gz := gzip.NewWriter(w)
		gz.Write([]byte(`<html><head><title>Management System</title></head>
<body><!-- Copyright American Megatrends Inc. --></body></html>`))
		gz.Close()
	}))
	defer srv.Close()

	r := identifyURL(srv.URL)
	if r.Title != "Management System" {
		t.Errorf("gzip 解压后 title 期望「Management System」，得到「%s」", r.Title)
	}
	if !strings.Contains(r.Fingerprint, "AMI BMC") {
		t.Errorf("gzip 解压后应识别 AMI BMC，得到「%s」", r.Fingerprint)
	}
}

// TestProductRules 产品规则覆盖
func TestProductRules(t *testing.T) {
	cases := []struct {
		html string
		want string
	}{
		{`<title>深信服超融合平台</title>`, "深信服"},
		{`<title>明御综合日志审计系统</title>`, "启明星辰"},
		{`<title>VMware NSX | Login</title>`, "NSX"},
		{`<title>HP iLO 4</title><body>Integrated_Lights-Out</body>`, "HP iLO"},
		{`<title>H3C 交换机</title>`, "H3C"},
		{`<title>Kong Manager</title>`, "Kong"},
		{`<title>EnableQ 问卷</title>`, "EnableQ"},
	}
	for _, c := range cases {
		srv, url := startMock(`<html><head>`+c.html+`</head><body></body></html>`, nil)
		r := identifyURL(url)
		srv.Close()
		if !strings.Contains(r.Fingerprint, c.want) {
			t.Errorf("HTML「%s」期望指纹包含「%s」，得到「%s」", c.html, c.want, r.Fingerprint)
		}
	}
}

// identifyURL 测试用：直接对 URL 做 L1+L2
func identifyURL(url string) *Result {
	if r := l1Identify(url); r != nil {
		if r.Confidence >= 60 {
			return r
		}
		if r2 := l2CSSAnalyze(url, r); r2 != nil {
			return r2
		}
		return r
	}
	if r := l2CSSAnalyze(url, &Result{}); r != nil {
		return r
	}
	return &Result{Layer: "none"}
}
