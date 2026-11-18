package scanner

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
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

// ReadBodyMax 单跳响应体最多抓多少字节。
// 历史值 3000 字符：SPA 首页动辄几百 KB，产品名/版权常在后半段，
// 3000 字截断后 AI 只能看到 <head>，识别自然虚。32KB 能覆盖绝大多数登录页/首页。
const ReadBodyMax = 32 * 1024

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
		DialContext:         (&net.Dialer{Timeout: 4 * time.Second}).DialContext,
		DisableCompression:  false,
	}
	// 不走 http.Client：它在拿到响应后会解析 Location，一旦目标返回畸形头
	// （实测 10.24.12.151:80 回 `Location: https://10.24.12.151:80:443/`）
	// 就直接报 "failed to parse Location header"，整条链路作废、白掉一次 AI 判定。
	// 用 Transport.RoundTrip 只发一次请求，跳转由我们自己处理，畸形头也能记下来当证据。
	target := fmt.Sprintf("%s://%s:%d/", scheme, ip, port)
	var hops []Hop
	for i := 0; i < maxHops; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
		if err != nil {
			cancel()
			break
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

		resp, err := tr.RoundTrip(req)
		if err != nil {
			cancel()
			if len(hops) == 0 {
				return nil, err
			}
			break
		}
		body, note := readBodySnippet(resp, ReadBodyMax)
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
		cancel()
		hops = append(hops, hop)

		loc := strings.TrimSpace(resp.Header.Get("Location"))
		if loc == "" || resp.StatusCode < 300 || resp.StatusCode >= 400 {
			break
		}
		// 自己拼下一跳，而不是 next.String()：设备/反代的 Location 常写成
		// "https://<ip>:80/" 或干脆缺端口，直接用会把 host:port 拼成 ":80:443" 这类畸形串，
		// 抓包整条链路失败 → 白掉一次 AI 判定（实测 10.24.12.151:80）。
		// 规则：主机固定为目标 IP，端口取 Location 里的（缺省按 scheme 补），只允许 http/https。
		nu, perr := url.Parse(loc)
		if perr != nil {
			break
		}
		nextScheme := strings.ToLower(nu.Scheme)
		if nextScheme == "" {
			nextScheme = req.URL.Scheme
		}
		if nextScheme != "http" && nextScheme != "https" {
			break
		}
		nextPort := nu.Port()
		if nextPort == "" {
			if nextScheme == "https" {
				nextPort = "443"
			} else {
				nextPort = "80"
			}
		}
		if nu.Host != "" && !strings.EqualFold(nu.Hostname(), ip) {
			break // 跨主机不跟，避免把别的资产拉进这次识别
		}
		pathQ := nu.RequestURI()
		if pathQ == "" || pathQ == "/" {
			pathQ = "/"
		}
		target = nextScheme + "://" + net.JoinHostPort(ip, nextPort) + pathQ
	}
	return hops, nil
}

// readBodySnippet 读响应体（文本类），保留完整报文供 AI 判读。
//
// 三个历史坑，都会让"证据不全 → 识别不准"：
//  1. 只读 3000 字符 → SPA/大首页的产品名在后半段，看不到；
//  2. GBK 页面 utf8.Valid=false 被当二进制整段丢掉 → 中文标题全没了；
//  3. gzip 未解 → 全是乱码。
func readBodySnippet(resp *http.Response, limit int) (string, string) {
	var r io.Reader = resp.Body
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Encoding")), "gzip") {
		if gz, err := gzip.NewReader(resp.Body); err == nil {
			defer gz.Close()
			r = gz
		}
	}
	// 多读 1 字节用于判断"是否还有更多"
	raw, _ := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	total := len(raw)
	truncated := false
	if total > limit {
		raw = raw[:limit]
		truncated = true
	}
	if total == 0 {
		return "", "空响应体"
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	texty := strings.Contains(ct, "text") || strings.Contains(ct, "json") ||
		strings.Contains(ct, "xml") || strings.Contains(ct, "javascript") || ct == ""
	if !texty {
		return "", fmt.Sprintf("非文本内容（%s，%d 字节）", ct, total)
	}

	raw = ensureUTF8(raw) // GBK/GB2312 → UTF-8
	if !utf8.Valid(raw) || bytesContainsNUL(raw) {
		return "", fmt.Sprintf("二进制内容 %d 字节（%s）", total, ct)
	}
	s := string(raw)
	note := ""
	if truncated {
		note = fmt.Sprintf("（响应体已截断：仅保留前 %d 字节，实际更大）", limit)
	}
	return s, note
}

func bytesContainsNUL(b []byte) bool {
	return len(b) > 0 && bytes.IndexByte(b, 0) >= 0
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

// TraceDigest 给模型看的完整报文。
//
// 历史版本把请求头截到 600、响应头 800、响应体 1500 字，结果是模型只看到
// 一小截 HTML head —— 既看不到版权行，也看不到登录框里的产品名，
// "识别不准"很大一部分是这里喂的证据不全导致的。
//
// 现在：请求行 / 请求头 / 响应行 / 响应头 全量；响应体按 bodyLimit 给
// （默认 16KB，超出明确标注截断位置）。识别一次多花一点 token，换取判得准。
func TraceDigest(hops []Hop) string {
	return traceText(hops, 16*1024)
}

// TraceRaw 完整报文（落盘/会话里给人看），不截断 body（抓取阶段已限 32KB）
func TraceRaw(hops []Hop) string {
	return traceText(hops, 0)
}

func traceText(hops []Hop, bodyLimit int) string {
	var b strings.Builder
	for _, h := range hops {
		fmt.Fprintf(&b, "===== 第 %d 跳 =====\n", h.Index)
		fmt.Fprintf(&b, ">>> 请求\n%s\n", h.RequestLine)
		fmt.Fprintf(&b, "%s", h.RequestHeaders)
		fmt.Fprintf(&b, "\n<<< 响应\n%s\n", h.ResponseLine)
		fmt.Fprintf(&b, "%s", h.ResponseHeaders)
		b.WriteString("\n--- 响应体 ---\n")
		switch {
		case h.Body != "":
			if bodyLimit > 0 && len(h.Body) > bodyLimit {
				b.WriteString(cutBytes(h.Body, bodyLimit))
				fmt.Fprintf(&b, "\n…（响应体共 %d 字节，以上为前 %d 字节）\n", len(h.Body), bodyLimit)
			} else {
				b.WriteString(h.Body)
				b.WriteString("\n")
			}
			if h.BodyNote != "" {
				fmt.Fprintf(&b, "%s\n", h.BodyNote)
			}
		case h.BodyNote != "":
			fmt.Fprintf(&b, "%s\n", h.BodyNote)
		default:
			b.WriteString("（空响应体）\n")
		}
	}
	return b.String()
}

// cutBytes 按字节切但保证不切坏 UTF-8
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// 供 AI/会话使用：headers 全量，body 见 TraceDigest
