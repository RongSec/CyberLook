package scanner

import (
	"compress/gzip"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Hop 一跳 HTTP 交互：一次请求 + 一次响应
type Hop struct {
	Index           int    `json:"index"`
	RequestLine     string `json:"requestLine"`
	RequestHeaders  string `json:"requestHeaders"`
	ResponseLine    string `json:"responseLine"`
	ResponseHeaders string `json:"responseHeaders"`
	Body            string `json:"body"`
	BodyNote        string `json:"bodyNote,omitempty"`
}

// HTTPTrace 主动请求目标，抓取完整请求/响应包，最多 maxHops 跳（默认 4：
// 覆盖 1 请求 1 响应、302 一跳、以及极少见的二次跳转）。
// 用途：只靠 banner（UnKnown/websocket-sharp 之类）根本判断不出业务系统，
// 必须让 AI 看到真实的请求行、响应头、页面内容。
func HTTPTrace(ip string, port int, scheme string, maxHops int) ([]Hop, error) {
	if maxHops <= 0 || maxHops > 4 {
		maxHops = 4
	}
	if scheme == "" {
		scheme = "http"
	}
	scheme = strings.ToLower(scheme)
	tr := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 5 * time.Second,
		DialContext: (&net.Dialer{Timeout: 4 * time.Second}).DialContext,
		DisableCompression: false,
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   8 * time.Second,
		// 自己跟随跳转，才能把每一跳都记下来
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	target := fmt.Sprintf("%s://%s:%d/", scheme, ip, port)
	var hops []Hop
	for i := 0; i < maxHops; i++ {
		req, err := http.NewRequest("GET", target, nil)
		if err != nil {
			break
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

		resp, err := client.Do(req)
		if err != nil {
			if len(hops) == 0 {
				return nil, err
			}
			break
		}
		body, note := readBodySnippet(resp, 3000)
		hop := Hop{
			Index:           i + 1,
			RequestLine:     fmt.Sprintf("%s %s %s", req.Method, req.URL.RequestURI(), "HTTP/1.1"),
			RequestHeaders:  dumpReqHeaders(req),
			ResponseLine:    fmt.Sprintf("%s %s", resp.Proto, resp.Status),
			ResponseHeaders: dumpRespHeaders(resp),
			Body:            body,
			BodyNote:        note,
		}
		resp.Body.Close()
		hops = append(hops, hop)

		loc := resp.Header.Get("Location")
		if loc == "" || resp.StatusCode < 300 || resp.StatusCode >= 400 {
			break
		}
		next, err := req.URL.Parse(loc)
		if err != nil || next.Host == "" {
			break
		}
		target = next.String()
	}
	return hops, nil
}

// readBodySnippet 读正文摘要；二进制只给说明，不塞乱码给模型
func readBodySnippet(resp *http.Response, limit int) (string, string) {
	var r io.Reader = resp.Body
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Encoding")), "gzip") {
		if gz, err := gzip.NewReader(resp.Body); err == nil {
			defer gz.Close()
			r = gz
		}
	}
	raw, _ := io.ReadAll(io.LimitReader(r, int64(limit*3)))
	if len(raw) == 0 {
		return "", "空响应体"
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	texty := strings.Contains(ct, "text") || strings.Contains(ct, "json") ||
		strings.Contains(ct, "xml") || strings.Contains(ct, "javascript") || ct == ""
	if !texty || !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0) {
		return "", fmt.Sprintf("二进制内容 %d 字节（%s）", len(raw), ct)
	}
	s := string(raw)
	if len([]rune(s)) > limit {
		s = string([]rune(s)[:limit]) + "…（已截断）"
	}
	return s, ""
}

// dumpReqHeaders 请求头（敏感值打码）
func dumpReqHeaders(req *http.Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Host: %s\n", req.URL.Host)
	for k, vs := range req.Header {
		fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(vs, ", "))
	}
	return maskHeader(b.String())
}

// dumpRespHeaders 响应头（Set-Cookie 打码）
func dumpRespHeaders(resp *http.Response) string {
	var b strings.Builder
	for k, vs := range resp.Header {
		v := strings.Join(vs, ", ")
		if len(v) > 300 {
			v = v[:300] + "…"
		}
		fmt.Fprintf(&b, "%s: %s\n", k, v)
	}
	return maskHeader(b.String())
}

// maskHeader 把 Cookie/Authorization/Set-Cookie 的值截断，避免把凭据写进对话记录
func maskHeader(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		low := strings.ToLower(ln)
		for _, k := range []string{"set-cookie:", "cookie:", "authorization:", "x-api-key:"} {
			if strings.HasPrefix(low, k) {
				parts := strings.SplitN(ln, ":", 2)
				if len(parts) == 2 {
					v := strings.TrimSpace(parts[1])
					if len(v) > 12 {
						v = v[:12] + "…(已脱敏)"
					}
					lines[i] = parts[0] + ": " + v
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

// TraceDigest 把多跳压成给模型的紧凑文本（控制 token）
func TraceDigest(hops []Hop) string {
	var b strings.Builder
	for _, h := range hops {
		fmt.Fprintf(&b, "—— 第 %d 跳 ——\n请求: %s\n", h.Index, h.RequestLine)
		fmt.Fprintf(&b, "请求头:\n%s", clip(h.RequestHeaders, 600))
		fmt.Fprintf(&b, "响应: %s\n响应头:\n%s", h.ResponseLine, clip(h.ResponseHeaders, 800))
		if h.Body != "" {
			fmt.Fprintf(&b, "响应体:\n%s\n", clip(h.Body, 1500))
		} else if h.BodyNote != "" {
			fmt.Fprintf(&b, "响应体: %s\n", h.BodyNote)
		}
	}
	return b.String()
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
