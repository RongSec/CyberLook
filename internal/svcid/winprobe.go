package svcid

// Windows 系协议最小握手 + 通用版本提取 + 产品名规范化。
//
// 目标：在 445/3389/135/139/动态 RPC 端口上拿到**真实 OS/主机名/版本**，
// 而不是像 tscan 那样只写「Microsoft Windows RPC」这类端口猜测。
// 只做读性质的握手（negotiate/bind），不涉及认证与写操作。

import (
	"bytes"
	"net"
	"regexp"
	"strings"
	"time"
)

// printableStrings 抽出二进制响应里的可打印串（ASCII + UTF-16LE，nmap banner 风格）。
// Windows SMB/DCERPC 的 NativeOS/主机名常是 UTF-16LE，只抽 ASCII 会漏掉。
func printableStrings(b []byte, minLen int) string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) >= minLen {
			out = append(out, string(cur))
		}
		cur = nil
	}
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			cur = append(cur, rune(c))
		} else if c == 0x09 {
			cur = append(cur, ' ')
		} else {
			flush()
		}
	}
	flush()
	// UTF-16LE 通道
	cur = nil
	for i := 0; i+1 < len(b); i += 2 {
		lo, hi := b[i], b[i+1]
		if hi == 0 && lo >= 0x20 && lo < 0x7f {
			cur = append(cur, rune(lo))
		} else {
			flush()
		}
	}
	flush()
	return strings.Join(out, " | ")
}

// reWindows 系统串
var (
	reWinOS  = regexp.MustCompile(`(?i)(Windows (?:Server )?[A-Za-z0-9 .]+?(?:R2|20\d\d|XP|Vista|7|8|10|11)[^\x00,;|]{0,40})`)
	reWinSMB = regexp.MustCompile(`(?i)(Windows [^\x00;|]{3,60})`)
	reHost   = regexp.MustCompile(`(?i)hostname[:= ]+([A-Za-z0-9_\-\.]{2,40})`)
	reDomain = regexp.MustCompile(`(?i)domain[:= ]+([A-Za-z0-9_\-\.]{2,40})`)
	// 版本提取（HTTP/产品通用）
	reServerVer = regexp.MustCompile(`(?im)^Server:\s*([A-Za-z0-9\-\._ ]+?)(?:/([\d][\w\.\-]*))?`)
	reXPowerVer = regexp.MustCompile(`(?im)^X-Powered-By:\s*([^\r\n]+)`)
	reMetaGen   = regexp.MustCompile(`(?is)<meta[^>]+name=["']generator["'][^>]+content=["']([^"']{2,80})`)
	reTomcatVer = regexp.MustCompile(`(?i)(Apache Tomcat|Tomcat)/([\d][\w\.\-]*)`)
	reNginxVer  = regexp.MustCompile(`(?i)\bnginx/([\d][\w\.\-]*)`)
	reApacheVer = regexp.MustCompile(`(?i)\bApache/([\d][\w\.\-]*)`)
	reIISVer    = regexp.MustCompile(`(?i)\bMicrosoft-IIS/([\d][\w\.\-]*)`)
	rePHPVer    = regexp.MustCompile(`(?i)\bPHP/([\d][\w\.\-]*)`)
	reASPVer    = regexp.MustCompile(`(?i)\bASP\.NET`)
	reJettyVer  = regexp.MustCompile(`(?i)\bJetty\(?([vV]?[\d][\w\.\-]*)?\)?`)
)

// canonicalProduct 规则 id / 产品名 → 可读产品名
var canonicalProduct = map[string]string{
	"internet_information_server": "Microsoft IIS",
	"ibm-http-server":             "IBM HTTP Server",
	"tomcat_jk_connector":         "Apache Tomcat (JK)",
	"apache-http":                 "Apache httpd",
	"nginx":                       "nginx",
	"lighttpd":                    "lighttpd",
	"spring_cloud_config":         "Spring Cloud Config",
	"http_server":                 "Generic HTTP Server",
	"windows_server":              "Windows Server",
	"microsoft_iis":               "Microsoft IIS",
	"openssl":                     "OpenSSL",
	"tomcat":                      "Apache Tomcat",
	"apache_tomcat":               "Apache Tomcat",
	"jetty":                       "Jetty",
	"weblogic":                    "Oracle WebLogic",
	"websphere":                   "IBM WebSphere",
	"jboss":                       "JBoss",
	"exchange":                    "Microsoft Exchange",
	"vmware_esxi":                 "VMware ESXi",
	"vmware":                      "VMware",
	"grafana":                     "Grafana",
	"jenkins":                     "Jenkins",
	"gitlab":                      "GitLab",
	"zabbix":                      "Zabbix",
	"nacos":                       "Nacos",
	"consul":                      "Consul",
	"redis":                       "Redis",
	"mysql":                       "MySQL",
	"postgresql":                  "PostgreSQL",
	"mongodb":                     "MongoDB",
	"elasticsearch":               "Elasticsearch",
	"oracle":                      "Oracle DB",
	"mssql":                       "Microsoft SQL Server",
	"memcached":                   "Memcached",
	"samba":                       "Samba",
	"openssh":                     "OpenSSH",
	"microsoft_httpapi":           "Microsoft HTTPAPI",
	"microsoft-httpapi":           "Microsoft HTTPAPI",
	"winrm":                       "WinRM",
	"rpc":                         "MSRPC",
	"msrpc":                       "MSRPC",
	"smb":                         "SMB",
	"rdp":                         "RDP",
	"ldap":                        "LDAP",
	"kubernetes":                  "Kubernetes",
	"kubelet":                     "Kubernetes Kubelet",
	"haproxy":                     "HAProxy",
	"f5":                          "F5 BIG-IP",
	"fortinet":                    "Fortinet",
	"fortigate":                   "FortiGate",
	"cisco":                       "Cisco",
	"h3c":                         "H3C",
	"huawei":                      "Huawei",
	"sangfor":                     "Sangfor",
	"inspur":                      "Inspur",
	"lenovo":                      "Lenovo",
	"dell":                        "Dell",
	"hp":                          "HP",
	"ibm":                         "IBM",
}

// normalizeProduct 产品名规范化：优先查表，其次 snake_case 转可读
func normalizeProduct(s string) string {
	if s == "" {
		return ""
	}
	if v, ok := canonicalProduct[strings.ToLower(strings.TrimSpace(s))]; ok {
		return v
	}
	// snake_case / kebab-case → Title Case
	if strings.ContainsAny(s, "_-") {
		parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' })
		for i, p := range parts {
			if p == "" {
				continue
			}
			// 仅对 ASCII 分段做大写首字母。p[0] < 0x80 判断 UTF-8 安全
			// （ASCII 码点恒单字节）；非 ASCII（如中文）分段原样保留——
			// 旧写法 p[:1] 按字节切片会把多字节首字符切碎产生非法 UTF-8→U+FFFD。
			if p[0] < 0x80 {
				parts[i] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
		return strings.Join(parts, " ")
	}
	return s
}

// parseWindowsStrings 从可打印串里抽 OS/主机名/域名
func parseWindowsStrings(s string) (product, version, extra string) {
	if s == "" {
		return "", "", ""
	}
	if m := reWinOS.FindStringSubmatch(s); len(m) > 1 {
		product = "Microsoft " + strings.TrimSpace(m[1])
	} else if m := reWinSMB.FindStringSubmatch(s); len(m) > 1 {
		product = "Microsoft " + strings.TrimSpace(m[1])
	} else if m := regexp.MustCompile(`(?i)(Windows Server[^|]{0,50})`).FindStringSubmatch(s); len(m) > 1 {
		product = "Microsoft " + strings.TrimSpace(m[1])
	}
	if m := reHost.FindStringSubmatch(s); len(m) > 1 {
		extra = "host=" + m[1]
	}
	if m := reDomain.FindStringSubmatch(s); len(m) > 1 {
		if extra != "" {
			extra += " "
		}
		extra += "domain=" + m[1]
	}
	// Windows Server 2012 R2 Datacenter 9600 → version 9600/2012R2
	if m := regexp.MustCompile(`(\d{4,5})\b`).FindStringSubmatch(product); len(m) > 1 {
		version = m[1]
	}
	return product, version, extra
}

// smbNegotiatePayload nmap smbprog-neg 探针（FingerprintHub service 规则同款，验证过可用）。
// 关键：SMB1 dialect 串是 0x02 长度前缀，不是 0x00。
const smbNegotiatePayload = "\\0\\0\\0\\xa4\\xff\\x53\\x4d\\x42\\x72\\0\\0\\0\\0\\x08\\x01\\x40" +
	"\\0\\0\\0\\0\\0\\0\\0\\0\\0\\0\\0\\0\\0\\0\\x40\\x06\\0\\0\\x01\\0\\x00\\x81\\0" +
	"\\x02PC NETWORK PROGRAM 1.0\\0\\x02MICROSOFT NETWORKS 1.03\\0\\x02MICROSOFT NETWORKS 3.0" +
	"\\0\\x02LANMAN1.0\\0\\x02LM1.2X002\\0\\x02Samba\\0\\x02NT LANMAN 1.0\\0\\x02NT LM 0.12\\0"

// buildSMB1Negotiate 返回 nmap smbprog-neg 完整包。
// 注意：payload 自身已含 NetBIOS session message 头（\0\0\0\xa4），不要再包一层。
func buildSMB1Negotiate() []byte {
	return decodeEscaped(smbNegotiatePayload)
}

// buildRDPNegotiate 构造 X.224 Connection Request + RDP Negotiation Request
// TPKT(4) + X.224 CR(li + 6 字节) + cookie + RDP Negotiation(8 字节)
func buildRDPNegotiate() []byte {
	cookie := []byte("Cookie: mstshash=probe\r\n")
	x224 := []byte{0xe0, 0x00, 0x00, 0x00, 0x00, 0x00}            // X.224 CR class 0
	rdp := []byte{0x01, 0x00, 0x08, 0x00, 0x01, 0x00, 0x00, 0x00} // 请求 TLS
	body := append([]byte{}, x224...)
	body = append(body, cookie...)
	body = append(body, rdp...)
	total := uint16(4 + 1 + len(body))
	req := []byte{0x03, 0x00, byte(total >> 8), byte(total), byte(len(body))}
	return append(req, body...)
}

// buildDCERPCBind 构造 DCE/RPC Bind（到端点映射器）
func buildDCERPCBind() []byte {
	// 简化 bind：版本 5.0，pkt_type=11 (bind)，调用 id=1，绑定上下文 1 个，抽象语法 = EPM (e1af8308-5d1f-11c9-91a4-08002b14a0fa)
	hdr := []byte{
		0x05, 0x00, 0x0b, 0x03, 0x10, 0x00, 0x00, 0x00,
		0x48, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00,
		0xd0, 0x16, 0xd0, 0x16, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00,
		0x08, 0x83, 0xaf, 0xe1, 0x1f, 0x5d, 0xc9, 0x11,
		0x91, 0xa4, 0x08, 0x00, 0x2b, 0x14, 0xa0, 0xfa,
		0x02, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	return hdr
}

// probeWindows 主动握手识别 Windows 系协议，返回产品/版本/额外信息/原始串
func probeWindows(conn net.Conn, port int) (product, version, extra, banner string, ok bool) {
	var req []byte
	switch {
	case port == 445 || port == 139:
		req = buildSMB1Negotiate()
	case port == 3389:
		req = buildRDPNegotiate()
	case port == 135 || (port >= 49152 && port <= 49735):
		req = buildDCERPCBind()
	default:
		return "", "", "", "", false
	}
	_ = conn.SetDeadline(time.Now().Add(900 * time.Millisecond))
	if _, err := conn.Write(req); err != nil {
		return "", "", "", "", false
	}
	buf := make([]byte, 8192)
	n, _ := conn.Read(buf)
	if n <= 0 {
		return "", "", "", "", false
	}
	raw := buf[:n]
	// SMB2/3 响应（0xFE 'SMB'）没有 OS 串，只能确认协议
	if n >= 8 && raw[0] == 0xFE && string(raw[1:4]) == "SMB" {
		p := "Microsoft SMB"
		if port == 139 {
			p = "Microsoft NetBIOS Session Service"
		}
		return p, "", "", "", true
	}
	strs := printableStrings(raw, 4)
	p, v, e := parseWindowsStrings(strs)
	if p == "" {
		// 协议确认但拿不到 OS 串
		switch {
		case port == 445 || port == 139:
			p = "Microsoft SMB"
		case port == 3389:
			p = "Microsoft RDP"
		case port == 135:
			p = "Microsoft Windows RPC"
		}
	}
	return p, v, e, strs, true
}

// extractHTTPVersion 从 HTTP 响应里提取产品与版本
func extractHTTPVersion(headers, body string) (product, version string) {
	combined := headers + "\n" + body
	if m := reServerVer.FindStringSubmatch(headers); len(m) > 1 {
		product = strings.TrimSpace(m[1])
		if len(m) > 2 && m[2] != "" {
			version = m[2]
		}
	}
	if version == "" {
		if m := reNginxVer.FindStringSubmatch(combined); len(m) > 1 {
			product, version = "nginx", m[1]
		} else if m := reApacheVer.FindStringSubmatch(combined); len(m) > 1 {
			product, version = "Apache httpd", m[1]
		} else if m := reIISVer.FindStringSubmatch(combined); len(m) > 1 {
			product, version = "Microsoft IIS", m[1]
		} else if m := reTomcatVer.FindStringSubmatch(combined); len(m) > 2 {
			product, version = "Apache Tomcat", m[2]
		} else if m := reJettyVer.FindStringSubmatch(combined); len(m) > 1 {
			product = "Jetty"
			version = strings.TrimPrefix(m[1], "v")
		}
	}
	if version == "" {
		if m := reMetaGen.FindStringSubmatch(body); len(m) > 1 {
			gen := strings.TrimSpace(m[1])
			if v := regexp.MustCompile(`([\d][\w\.\-]*)`).FindStringSubmatch(gen); len(v) > 1 {
				version = v[1]
			}
			if product == "" {
				product = regexp.MustCompile(`\s+[\d].*$`).ReplaceAllString(gen, "")
			}
		}
	}
	if version == "" {
		if m := rePHPVer.FindStringSubmatch(combined); len(m) > 1 {
			product = "PHP"
			version = m[1]
		} else if reASPVer.MatchString(combined) && product == "" {
			product = "ASP.NET"
		}
	}
	return normalizeProduct(product), version
}

// mergeExtra 把 extra 拼进 banner（保留原串便于追溯）
func mergeExtra(banner, extra string) string {
	if extra == "" {
		return banner
	}
	if banner == "" {
		return extra
	}
	if strings.Contains(banner, extra) {
		return banner
	}
	return banner + " | " + extra
}

var _ = bytes.MinRead
