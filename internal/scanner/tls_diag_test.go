package scanner

import (
	"crypto/tls"
	"net"
	"os"
	"testing"
	"time"
)

// TestTLSMatrix 逐个尝试 TLS 配置，定位内网设备握手失败的具体原因。
// 用法：TLS_TARGET=198.18.8.65:443 go test ./internal/scanner/ -run TestTLSMatrix -v
func TestTLSMatrix(t *testing.T) {
	target := os.Getenv("TLS_TARGET")
	if target == "" {
		t.Skip("未设置 TLS_TARGET")
	}
	cases := []struct {
		name string
		cfg  *tls.Config
	}{
		{"默认(Go 现代配置)", &tls.Config{InsecureSkipVerify: true}},
		{"MinVersion=TLS1.0", &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS10}},
		{"仅 TLS1.2", &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}},
		{"TLS1.2 + 静态RSA套件", &tls.Config{
			InsecureSkipVerify: true, MinVersion: tls.VersionTLS10,
			CipherSuites: []uint16{
				tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_RSA_WITH_AES_256_CBC_SHA,
				tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			},
		}},
		{"全兼容套件", &tls.Config{
			InsecureSkipVerify: true, MinVersion: tls.VersionTLS10,
			CipherSuites: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
				tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
				tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_RSA_WITH_AES_256_CBC_SHA,
				tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			},
		}},
		{"全兼容 + 强制 HTTP/1.1", &tls.Config{
			InsecureSkipVerify: true, MinVersion: tls.VersionTLS10,
			NextProtos: []string{"http/1.1"},
			CipherSuites: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_RSA_WITH_AES_256_CBC_SHA,
				tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			},
		}},
	}
	for _, c := range cases {
		d := &net.Dialer{Timeout: 6 * time.Second}
		conn, err := tls.DialWithDialer(d, "tcp", target, c.cfg)
		if err != nil {
			t.Logf("[FAIL] %-24s %v", c.name, err)
			continue
		}
		st := conn.ConnectionState()
		t.Logf("[ OK ] %-24s 版本=%s 套件=%s ALPN=%q", c.name,
			tlsVersionName(st.Version), tls.CipherSuiteName(st.CipherSuite), st.NegotiatedProtocol)
		conn.Close()
	}
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS1.0"
	case tls.VersionTLS11:
		return "TLS1.1"
	case tls.VersionTLS12:
		return "TLS1.2"
	case tls.VersionTLS13:
		return "TLS1.3"
	}
	return "?"
}
