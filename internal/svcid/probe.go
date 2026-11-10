package svcid

// 自研协议探针：端口开放后判定"这是什么服务、什么版本"。
//
// 策略：**被动优先**——先只读不写（SSH/FTP/SMTP/POP3/IMAP/MySQL/VNC 都会主动发 banner），
// 拿不到再发一个最小探针（Redis PING / HTTP GET）。不做 nmap 那种多轮激进探测，
// 避免对生产服务产生副作用。
//
// 之所以不用 fingerprintx：它的 HTTP 插件依赖 wappalyzergo（规则数据 GPL-3.0），
// 与我们"商用交付不引入 GPL 数据"的底线冲突；而它真正的价值（51 个协议）里，
// 内网高频的十来个协议我们自己实现即可，且能保留**完整原始 banner 与版本号**。

import (
	"context"
	"crypto/tls"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ProbeResult 探针结果
type ProbeResult struct {
	Protocol string
	Product  string
	Version  string
	Banner   string
}

// 常见服务的端口提示（拿不到 banner 时用于兜底命名）
var portHints = map[int]string{
	22: "ssh", 21: "ftp", 23: "telnet", 25: "smtp", 53: "dns", 110: "pop3",
	143: "imap", 389: "ldap", 445: "smb", 1433: "mssql", 1521: "oracle",
	3306: "mysql", 3389: "rdp", 5432: "postgresql", 5900: "vnc", 6379: "redis",
	27017: "mongodb", 9200: "elasticsearch", 11211: "memcached", 2181: "zookeeper",
	5672: "amqp", 61616: "activemq", 5985: "winrm", 5986: "winrm",
	135: "msrpc", 623: "ipmi", 5120: "ipmi", 5123: "ipmi",
	47001: "winrm", 636: "ldaps",
	10050: "zabbix-agent", 10051: "zabbix-server", 8081: "http-alt",
	8084: "http-alt", 9090: "http-alt", 7001: "weblogic", 7002: "weblogic",
	199: "smux", 1720: "h323", 3900: "unify", 8086: "influxdb",
}

var (
	reSSH   = regexp.MustCompile(`^SSH-([\d.]+)-(\S+)`)
	reFTP   = regexp.MustCompile(`(?i)^220[ -].*?(vsftpd|proftpd|filezilla|serv-u|microsoft ftp|pure-ftpd|wu-ftpd)\s*([\w.\-]*)`)
	reFTPv2 = regexp.MustCompile(`(?i)^220[ -](.+)`)
	reSMTP  = regexp.MustCompile(`(?i)^220[ -].*?(microsoft exchange|exim|postfix|sendmail|zimbra|hmailserver|esmtp)\s*([\w.\-]*)`)
	rePop3  = regexp.MustCompile(`(?i)^\+OK[ -].*?(microsoft exchange|dovecot|courier|zimbra)\s*([\w.\-]*)`)
	reIMAP  = regexp.MustCompile(`(?i)^\* OK.*?(dovecot|microsoft exchange|courier|zimbra)\s*([\w.\-]*)`)
	reVNC   = regexp.MustCompile(`^RFB (\d{3}\.\d{3})`)
	reMySQL = regexp.MustCompile(`^([\d.]+)-?(\S*)`) // MySQL 握手包第 5 字节起是版本串
	reRedis = regexp.MustCompile(`(?m)^redis_version:([\w.\-]+)`)
	reHTTP  = regexp.MustCompile(`(?i)^HTTP/\d\.\d\s+\d{3}`)
)

// Probe 对一个开放端口做被动/最小探针识别
func Probe(ctx context.Context, ip string, port int, timeout time.Duration) ProbeResult {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	res := ProbeResult{Protocol: portHints[port]}
	if res.Protocol == "" {
		// Windows 动态 RPC 端口段（49152-65535 / 49664-49735）——不发 banner，
		// 但给个 msrpc 提示比裸 tcp 更有信息量
		if (port >= 49152 && port <= 49735) || (port >= 53000 && port <= 53100) {
			res.Protocol = "msrpc"
		} else {
			res.Protocol = "tcp"
		}
	}

	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return res
	}
	defer conn.Close()

	// 1) 被动读：等服务端主动发 banner（SSH/FTP/SMTP/POP3/IMAP/VNC/MySQL 都会发）
	_ = conn.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	if n > 0 {
		banner := string(buf[:n])
		res.Banner = sanitizeBanner(banner)
		if p, prod, ver, ok := matchBanner(banner); ok {
			if p != "" {
				res.Protocol = p
			}
			res.Product, res.Version = prod, ver
			return res
		}
	}

	// 2) MySQL 握手包：包头 4 字节（长度3+序号1），第 5 字节是 protocol version(0x0A)，
	//    版本串紧跟其后到 NUL 结束。实测 MySQL 5.7.44 响应 J\x00\x00\x00\n5.7.44\x00…
	if port == 3306 && n >= 8 {
		raw := buf[:n]
		if raw[4] == 10 {
			s := raw[5:]
			if i := strings.IndexByte(string(s), 0); i > 0 {
				ver := strings.TrimSpace(string(s[:i]))
				if ver != "" {
					res.Protocol, res.Product = "mysql", "MySQL"
					res.Version = ver
					res.Banner = sanitizeBanner("MySQL " + ver)
					return res
				}
			}
		}
	}

	// 2.5) LDAP：rootDSE 搜索（只读），响应里的 namingContexts/vendorName 能定产品
	if port == 389 || port == 636 || port == 3268 {
		_, _ = conn.Write(ldapRootDSERequest())
		_ = conn.SetReadDeadline(time.Now().Add(900 * time.Millisecond))
		n2, _ := conn.Read(buf)
		if n2 > 0 {
			if p, prod, ver, ok := matchLDAP(buf[:n2]); ok {
				res.Protocol, res.Product, res.Version = p, prod, ver
				res.Banner = sanitizeBanner(printableStrings(buf[:n2], 4))
				return res
			}
		}
	}

	// 2.6) AMQP：发协议头，RabbitMQ 等会回协议头或 connection.start（5671 可能是 TLS）
	if port == 5671 || port == 5672 {
		if port == 5671 {
			if tlsConn := tlsClient(conn); tlsConn != nil {
				conn = tlsConn
				defer conn.Close()
			}
		}
		_, _ = conn.Write([]byte("AMQP\x00\x00\x09\x01"))
		_ = conn.SetReadDeadline(time.Now().Add(900 * time.Millisecond))
		n2, _ := conn.Read(buf)
		if n2 > 0 {
			res.Protocol, res.Product = "amqp", "AMQP"
			if m := reRabbit.FindStringSubmatch(printableStrings(buf[:n2], 3)); len(m) > 0 {
				res.Product = "RabbitMQ"
			}
			res.Banner = sanitizeBanner(printableStrings(buf[:n2], 3))
			return res
		}
	}

	// 2.7) SMUX：Open request（RFC1592），SNMP multiplexer 会回
	if port == 199 {
		_, _ = conn.Write(smuxOpenRequest())
		_ = conn.SetReadDeadline(time.Now().Add(900 * time.Millisecond))
		n2, _ := conn.Read(buf)
		if n2 > 0 {
			if strings.Contains(printableStrings(buf[:n2], 3), "SMUX") || n2 > 10 {
				res.Protocol, res.Product = "smux", "Linux SNMP multiplexer (SMUX)"
				res.Banner = sanitizeBanner(printableStrings(buf[:n2], 3))
				return res
			}
		}
	}

	// 2.8) .NET Remoting（8086/4449 等）：发 TCP preamble，响应里有 .NET 痕迹
	if port == 8086 || port == 4449 || port == 8087 {
		_, _ = conn.Write(dotnetRemotingProbe())
		_ = conn.SetReadDeadline(time.Now().Add(900 * time.Millisecond))
		n2, _ := conn.Read(buf)
		if n2 > 0 {
			strs := printableStrings(buf[:n2], 3)
			if reDotNet.MatchString(strs) || n2 > 20 {
				res.Protocol, res.Product = "msdotnet", "MS .NET Remoting services"
				res.Banner = sanitizeBanner(strs)
				return res
			}
		}
	}

	// 3) 主动探一个：Redis PING（最常见、副作用最小）
	if port == 6379 || res.Protocol == "redis" {
		_, _ = conn.Write([]byte("*1\r\n$4\r\nPING\r\n"))
		_ = conn.SetReadDeadline(time.Now().Add(800 * time.Millisecond))
		n2, _ := conn.Read(buf)
		if n2 > 0 && strings.Contains(string(buf[:n2]), "PONG") {
			res.Protocol, res.Product = "redis", "Redis"
			// 再取 INFO 拿版本（只读命令）
			_, _ = conn.Write([]byte("INFO server\r\n"))
			_ = conn.SetReadDeadline(time.Now().Add(900 * time.Millisecond))
			n3, _ := conn.Read(buf)
			if n3 > 0 {
				if m := reRedis.FindStringSubmatch(string(buf[:n3])); len(m) > 1 {
					res.Version = m[1]
				}
			}
			res.Banner = "Redis"
			return res
		}
	}

	// 4) 兜底：未知 tcp 端口发一行，很多服务会回版本/签名
	if res.Protocol == "tcp" || res.Protocol == "msrpc" {
		_, _ = conn.Write([]byte("\r\n"))
		_ = conn.SetReadDeadline(time.Now().Add(900 * time.Millisecond))
		n2, _ := conn.Read(buf)
		if n2 > 0 {
			ban := printableStrings(buf[:n2], 4)
			if ban != "" {
				res.Banner = sanitizeBanner(ban)
				if p, prod, ver, ok := matchBanner(ban); ok {
					res.Protocol, res.Product, res.Version = p, prod, ver
					return res
				}
			}
		}
	}
	return res
}

// matchBanner 用正则从 banner 里解析协议/产品/版本
func matchBanner(b string) (proto, product, version string, ok bool) {
	// 通用：banner 里 product 后跟数字版本（Vsftpd 3.0.2 / Apache/2.4.41 / lighttpd/1.4.55）
	if m := reGenericVer.FindStringSubmatch(b); len(m) > 2 {
		return "", normalizeProduct(m[1]), m[2], true
	}
	if m := reSSH.FindStringSubmatch(b); len(m) > 2 {
		sw := m[2]
		ver := sw
		prod := sw
		// OpenSSH_8.1p1 → 产品 OpenSSH，版本 8.1p1
		if i := strings.IndexByte(sw, '_'); i > 0 {
			prod = sw[:i]
			ver = sw[i+1:]
		}
		return "ssh", prod, ver, true
	}
	if reHTTP.MatchString(b) {
		return "http", "", "", true
	}
	if m := reVNC.FindStringSubmatch(b); len(m) > 1 {
		return "vnc", "VNC", m[1], true
	}
	if m := reFTP.FindStringSubmatch(b); len(m) > 2 {
		return "ftp", m[1], strings.TrimSpace(m[2]), true
	}
	if m := reSMTP.FindStringSubmatch(b); len(m) > 2 {
		return "smtp", m[1], strings.TrimSpace(m[2]), true
	}
	if m := rePop3.FindStringSubmatch(b); len(m) > 2 {
		return "pop3", m[1], strings.TrimSpace(m[2]), true
	}
	if m := reIMAP.FindStringSubmatch(b); len(m) > 2 {
		return "imap", m[1], strings.TrimSpace(m[2]), true
	}
	// 兜底：FTP 200 行没识别出产品，仍算 ftp
	if strings.HasPrefix(b, "220") {
		return "ftp", "", "", true
	}
	return "", "", "", false
}

func sanitizeBanner(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.TrimSpace(s)
}

// ldapRootDSERequest 构造 LDAP SearchRequest(rootDSE, base scope, filter: present objectClass)
func ldapRootDSERequest() []byte {
	// 30 25 02 01 01 63 20 04 00 0a 01 00 0a 01 00 02 01 00 02 01 00 01 01 00 87 0b <objectClass> 30 00
	req := []byte{
		0x30, 0x25, 0x02, 0x01, 0x01, 0x63, 0x20,
		0x04, 0x00, 0x0a, 0x01, 0x00, 0x0a, 0x01, 0x00,
		0x02, 0x01, 0x00, 0x02, 0x01, 0x00, 0x01, 0x01, 0x00,
		0x87, 0x0b,
	}
	req = append(req, []byte("objectClass")...)
	return append(req, 0x30, 0x00)
}

// matchLDAP 从 LDAP 响应里定产品（ApacheDS / OpenLDAP / Microsoft AD / 389-Directory-Server）
func matchLDAP(b []byte) (proto, product, version string, ok bool) {
	strs := printableStrings(b, 3)
	if strs == "" {
		return "", "", "", false
	}
	proto = "ldap"
	switch {
	case regexp.MustCompile(`(?i)apacheds|apache directory`).MatchString(strs):
		product = "ApacheDS LDAP"
		if m := regexp.MustCompile(`(?i)ApacheDS[/ ]([\w.\-]+)`).FindStringSubmatch(strs); len(m) > 1 {
			version = m[1]
		}
	case regexp.MustCompile(`(?i)openldap`).MatchString(strs):
		product = "OpenLDAP"
		if m := regexp.MustCompile(`(?i)OpenLDAP[/ ]([\w.\-]+)`).FindStringSubmatch(strs); len(m) > 1 {
			version = m[1]
		}
	case regexp.MustCompile(`(?i)active directory|ntds settings|microsoft`).MatchString(strs):
		product = "Microsoft Active Directory"
	case regexp.MustCompile(`(?i)389 directory|red hat directory|rhds`).MatchString(strs):
		product = "Red Hat/389 Directory Server"
		if m := regexp.MustCompile(`(?i)(?:Red Hat|389)[^\d]*([\d.\-]+)`).FindStringSubmatch(strs); len(m) > 1 {
			version = m[1]
		}
	default:
		// 有 BER 响应但认不出厂商：至少确认是 LDAP
		return "ldap", "", "", true
	}
	return proto, product, version, true
}

var reRabbit = regexp.MustCompile(`(?i)rabbitmq|rabbit`)

// reGenericVer 从 banner 抽「产品名 + 版本」
var reGenericVer = regexp.MustCompile(`(?i)\b(vsftpd|proftpd|filezilla|serv-u|pure-ftpd|exim|postfix|sendmail|dovecot|courier|zimbra|hmailserver|lighttpd|nginx|apache|mysql|mariadb|postgresql|redis|mongodb|elasticsearch|openssh|dropbear|jetty|tomcat|weblogic|glassfish|cubism|iscdhcp|bind)[/ ]([vV]?\d[\w.\-]*)`)

// tlsClient 在已有连接上做 TLS 握手，失败返回 nil
func tlsClient(conn net.Conn) net.Conn {
	tc := tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
	_ = tc.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	if err := tc.Handshake(); err != nil {
		return nil
	}
	return tc
}

// smuxOpenRequest SMUX Open request（RFC1592）
func smuxOpenRequest() []byte {
	// ASN.1 BER: SEQUENCE { version 0, oid 1.3.6.1.6.3.1.1.1, description, timeout }
	return []byte{
		0x30, 0x18, 0x02, 0x01, 0x00, 0x06, 0x09, 0x2b, 0x06, 0x01, 0x06, 0x03, 0x01, 0x01, 0x01,
		0x04, 0x02, 0x00, 0x00, 0x02, 0x01, 0x00,
	}
}

// dotnetRemotingProbe .NET Remoting TCP 版本协商包
func dotnetRemotingProbe() []byte {
	// 单向消息头 + 空体，触发服务端版本响应
	return []byte{
		0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
}

var reDotNet = regexp.MustCompile(`(?i)\.net|remoting|microsoft|mscor|version=`)
