// Package fingerprint 三层 Web 指纹识别
//
// L1：单次请求，提取 title/meta/注释/框架/特征/Server头（7 维合一）
// L2：按需，CSS 类名前缀分析
// L3：按需，Go 代理 + Chrome headless 渲染（解决 SPA）
package fingerprint

import (
	"crypto/tls"
	"log"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Result 识别结果
type Result struct {
	Title       string   `json:"title"`
	MetaDesc    string   `json:"metaDesc"`
	Fingerprint string   `json:"fingerprint"`
	Framework   string   `json:"framework"`
	Server      string   `json:"server"`
	Layer       string   `json:"layer"` // L1 / L2 / L3
	Confidence  int      `json:"confidence"`
	Evidence    []string `json:"evidence"`
}

// legacyCiphers 内网老设备只支持这些旧密码套件（Go 1.26 默认已移除）
var legacyCiphers = []uint16{
	tls.TLS_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_RSA_WITH_AES_256_CBC_SHA,
	tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
	tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
}

// ClientCompat 返回兼容内网老设备的 HTTP 客户端
//
// 说明：内网老设备（BMC/管理平台）只支持旧 TLS 密码套件，Go 1.26 默认已移除，
// 必须显式指定 CipherSuites 才能握手成功。
// 同时支持 HTTPS_PROXY 环境变量，用于 Docker Desktop / 网络受限场景下走宿主机代理。
func ClientCompat(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS10,
			MaxVersion:         tls.VersionTLS13,
			CipherSuites:       legacyCiphers,
		},
		Proxy: http.ProxyFromEnvironment,
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

// ---- 产品特征库 ----

type productRule struct {
	Keywords []string // 命中任一即匹配
	Name     string
}

var productRules = []productRule{
	{[]string{"american megatrends", "ami bmc"}, "AMI BMC"},
	{[]string{"inspur", "浪潮"}, "浪潮"},
	{[]string{"深信服", "sangfor"}, "深信服"},
	{[]string{"超融合", "adesk"}, "深信服超融合"},
	{[]string{"vmware", "esxi"}, "VMware"},
	{[]string{"vcenter"}, "vCenter"},
	{[]string{"nsx"}, "VMware NSX"},
	{[]string{"vrealize", "skyline"}, "VMware vRealize"},
	{[]string{"hpe-ilo", "hp_ilo", "integrated_lights-out"}, "HP iLO"},
	{[]string{"ibm", "svc gui"}, "IBM"},
	{[]string{"h3c", "华三"}, "H3C"},
	{[]string{"huawei", "华为"}, "华为"},
	{[]string{"tongweb", "东方通"}, "TongWeb"},
	{[]string{"lexmark"}, "Lexmark"},
	{[]string{"centos-webpanel"}, "CWP"},
	{[]string{"kong"}, "Kong"},
	{[]string{"enableq"}, "EnableQ"},
	{[]string{"unifi", "ubiquiti"}, "UniFi"},
	{[]string{"wowonder"}, "Wowonder"},
	{[]string{"kylin", "麒麟"}, "麒麟"},
	{[]string{"envoy"}, "Envoy"},
	{[]string{"jenkins"}, "Jenkins"},
	{[]string{"grafana"}, "Grafana"},
	{[]string{"kibana"}, "Kibana"},
	{[]string{"zabbix"}, "Zabbix"},
	{[]string{"weblogic"}, "WebLogic"},
	{[]string{"tomcat"}, "Tomcat"},
	{[]string{"jboss"}, "JBoss"},
	{[]string{"exchange", "owa"}, "Exchange"},
	{[]string{"sundray", "信锐"}, "信锐"},
	{[]string{"topsec", "天融信"}, "天融信"},
	{[]string{"venustech", "启明星辰", "明御"}, "启明星辰"},
	{[]string{"dptech", "迪普"}, "迪普"},
}

var (
	titleRe    = regexp.MustCompile(`(?i)<title[^>]*>([^<]+)</title>`)
	metaDescRe = regexp.MustCompile(`(?i)<meta[^>]*name=["']description["'][^>]*content=["']([^"']+)`)
	metaDescRe2 = regexp.MustCompile(`(?i)<meta[^>]*content=["']([^"']+)["'][^>]*name=["']description`)
	commentRe  = regexp.MustCompile(`(?s)<!--(.*?)-->`)
	ngAppRe    = regexp.MustCompile(`(?i)ng-app=["']([^"']+)`)
	vueRe      = regexp.MustCompile(`(?i)(vue\.js|vue\.min\.js|__vue__|data-v-[0-9a-f]{8})`)
	reactRe    = regexp.MustCompile(`(?i)(react\.production|react-dom|__REACT_DEVTOOLS)`)
	cssLinkRe  = regexp.MustCompile(`(?i)href=["']([^"']+\.css[^"']*)`)
	cssPrefixRe = regexp.MustCompile(`\.([a-z]+-[a-z]+-[a-z]+)_`)
)

// Identify 三层识别主入口
func Identify(protocol, ip string, port int) *Result {
	scheme := "http"
	if protocol == "HTTPS" || port == 443 || port == 8443 || port == 4443 {
		scheme = "https"
	}
	baseURL := fmt.Sprintf("%s://%s:%d/", scheme, ip, port)

	// L1
	if r := l1Identify(baseURL); r != nil && r.Confidence >= 60 {
		return r
	} else if r != nil {
		// L2 补充
		if r2 := l2CSSAnalyze(baseURL, r); r2 != nil && r2.Confidence > r.Confidence {
			return r2
		}
		return r
	}

	// L1 完全失败，尝试 L2
	if r := l2CSSAnalyze(baseURL, &Result{}); r != nil {
		return r
	}

	return &Result{Layer: "none", Confidence: 0}
}

// l1Identify 单次请求提取 7 个维度
func l1Identify(baseURL string) *Result {
	client := ClientCompat(12 * time.Second)
	resp, err := client.Get(baseURL)
	if err != nil {
		log.Printf("[fp] L1 %s fail: %v", baseURL, err)
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	html := string(body)
	lower := strings.ToLower(html)
	log.Printf("[fp] L1 %s ok: %d bytes, status=%d", baseURL, len(body), resp.StatusCode)

	r := &Result{Layer: "L1"}
	var evidence []string

	// 1) title
	if m := titleRe.FindStringSubmatch(html); m != nil {
		r.Title = strings.TrimSpace(m[1])
		if r.Title != "" {
			evidence = append(evidence, "title:"+r.Title)
		}
	}

	// 2) meta description
	if m := metaDescRe.FindStringSubmatch(html); m != nil {
		r.MetaDesc = strings.TrimSpace(m[1])
	} else if m := metaDescRe2.FindStringSubmatch(html); m != nil {
		r.MetaDesc = strings.TrimSpace(m[1])
	}
	if r.MetaDesc != "" {
		evidence = append(evidence, "meta:"+r.MetaDesc)
	}

	// 3) HTML 注释（前 2000 字符）
	if m := commentRe.FindStringSubmatch(html); m != nil {
		c := strings.ToLower(m[1])
		for _, rule := range productRules {
			for _, kw := range rule.Keywords {
				if strings.Contains(c, kw) {
					evidence = append(evidence, "comment:"+kw)
					break
				}
			}
		}
	}

	// 4) 框架检测
	if m := ngAppRe.FindStringSubmatch(html); m != nil {
		r.Framework = "AngularJS(" + m[1] + ")"
		evidence = append(evidence, "ng-app:"+m[1])
	} else if vueRe.MatchString(html) {
		r.Framework = "Vue"
	} else if reactRe.MatchString(html) {
		r.Framework = "React"
	}

	// 5) Server 头
	if s := resp.Header.Get("Server"); s != "" {
		r.Server = s
		evidence = append(evidence, "server:"+s)
	}

	// 6) 产品特征匹配（body + server + meta）
	searchText := lower + " " + strings.ToLower(r.Server) + " " + strings.ToLower(r.MetaDesc)
	var products []string
	for _, rule := range productRules {
		for _, kw := range rule.Keywords {
			if strings.Contains(searchText, kw) {
				products = append(products, rule.Name)
				break
			}
		}
	}
	r.Fingerprint = strings.Join(uniqueStrings(products), ",")

	// 7) 置信度打分
	r.Confidence = scoreL1(r)
	r.Evidence = evidence
	return r
}

// scoreL1 L1 置信度
func scoreL1(r *Result) int {
	score := 0
	// 有意义的 title
	if r.Title != "" && !genericTitle(r.Title) {
		score += 50
	} else if r.Title != "" {
		score += 10
	}
	// meta description
	if r.MetaDesc != "" {
		score += 20
	}
	// 产品指纹
	if r.Fingerprint != "" {
		score += 30
	}
	// 只匹配到框架
	if r.Framework != "" && r.Fingerprint == "" && genericTitle(r.Title) {
		score += 15
	}
	return score
}

// genericTitle 是否是通用标题（无信息量）
func genericTitle(t string) bool {
	if t == "" {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(t))
	switch lower {
	case "login", "loading...", "loading", "not found", "index", "home", "welcome", "管理登录", "登录":
		return true
	}
	return false
}

// l2CSSAnalyze L2：CSS 类名前缀分析
func l2CSSAnalyze(baseURL string, base *Result) *Result {
	client := ClientCompat(10 * time.Second)
	resp, err := client.Get(baseURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	html := string(body)

	links := cssLinkRe.FindAllStringSubmatch(html, -1)
	if len(links) == 0 {
		return nil
	}

	r := base
	if r == nil {
		r = &Result{}
	}
	r.Layer = "L2"

	for i, link := range links {
		if i >= 3 { // 最多看 3 个 CSS
			break
		}
		cssURL := link[1]
		if !strings.HasPrefix(cssURL, "http") {
			cssURL = strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(cssURL, "/")
		}
		resp2, err := client.Get(cssURL)
		if err != nil {
			continue
		}
		cssBody, _ := io.ReadAll(io.LimitReader(resp2.Body, 256*1024))
		resp2.Body.Close()

		if m := cssPrefixRe.FindStringSubmatch(string(cssBody)); m != nil {
			prefix := m[1]
			if r.Fingerprint == "" {
				r.Fingerprint = prefix
			} else {
				r.Fingerprint += "," + prefix
			}
			r.Evidence = append(r.Evidence, "css:"+prefix)
			r.Confidence += 25
			break
		}
	}
	return r
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
