package scanner

import (
	"fmt"
	"os"
	"testing"
)

// TestProbeLive 实探指定目标的跳转链路（人工验收用，不参与常规 CI）。
// 用法：PROBE=198.18.8.65:80 go test ./internal/scanner/ -run TestProbeLive -v
func TestProbeLive(t *testing.T) {
	target := os.Getenv("PROBE")
	if target == "" {
		t.Skip("未设置 PROBE，跳过实探")
	}
	var ip string
	var port int
	if _, err := fmt.Sscanf(target, "%31s", &target); err != nil {
		t.Fatal(err)
	}
	// 拆 ip:port
	host, p, ok := splitHostPort(target)
	if !ok {
		t.Fatalf("目标格式应为 ip:port，收到 %s", target)
	}
	ip, port = host, p
	scheme := os.Getenv("PROBE_SCHEME")
	if scheme == "" {
		scheme = "http"
	}
	hops, err := HTTPTrace(ip, port, scheme, 4)
	if err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	t.Logf("目标 %s://%s:%d 共 %d 跳", scheme, ip, port, len(hops))
	for _, h := range hops {
		t.Logf("--- 第 %d 跳 ---", h.Index)
		t.Logf("请求: %s", h.RequestLine)
		t.Logf("响应: %s", h.ResponseLine)
		for _, ln := range splitNl(h.ResponseHeaders) {
			if len(ln) > 6 && (hasPrefixFold(ln, "location:") || hasPrefixFold(ln, "content-type:")) {
				t.Logf("头: %s", ln)
			}
		}
		body := h.Body
		if len(body) > 300 {
			body = body[:300]
		}
		t.Logf("体前 300: %s", oneLineStr(body))
	}
}

func splitHostPort(s string) (string, int, bool) {
	// 从右找冒号，避免 IPv6 干扰（本实探只处理 IPv4）
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			var p int
			if _, err := fmt.Sscanf(s[i+1:], "%d", &p); err != nil {
				return "", 0, false
			}
			return s[:i], p, true
		}
	}
	return "", 0, false
}

func splitNl(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func hasPrefixFold(s, p string) bool {
	if len(s) < len(p) {
		return false
	}
	return equalFoldASCII(s[:len(p)], p)
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func oneLineStr(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' {
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
