package scanner

// 跳转跟随与占位标题判定的表驱动单测。
// 这些行为直接决定「识别准不准」，且都是踩过坑的地方，固化成用例防止回归：
//   - JS 跳转（location.replace / meta refresh）必须跟随，否则永远停在壳页
//   - 相对 Location 必须按当前 URL 解析（./login → /login）
//   - 端口缺省沿用当前端口，不能硬补 443（否则 https://ip:8110 被打到 443）
//   - 跨主机不跟、畸形 Location 不崩、环路在跳数上限内收敛

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 起一个本地 HTTP 服务，模拟各种跳转形态
func newJumpServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// 1) JS 跳转（英文壳页，深信服风格）：跳到 /login
	mux.HandleFunc("/js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Redirection……</title></head><body>
<p>Redirecting to HTTPS……</p>
<script>var p=location.protocol; if(p!=='https:'){var d='https:'+location.href.substring(p.length); location.replace(d);}</script>
</body></html>`)
	})
	// 2) JS 跳转到相对路径
	mux.HandleFunc("/js-rel", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><script>location.replace('/final')</script></body></html>`)
	})
	// 3) meta refresh
	mux.HandleFunc("/meta", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><meta http-equiv="refresh" content="0;url=/final"></head><body></body></html>`)
	})
	// 4) 相对 Location，从 /sub/dir 出发
	mux.HandleFunc("/sub/dir", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "./login")
		w.WriteHeader(302)
	})
	mux.HandleFunc("/sub/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Login</title></head><body>登 录 页 面 正 文，用于确认跳转已经落到真实页面，而不是停在壳页上</body></html>`)
	})
	// 5) 无斜杠相对 Location（login 而不是 ./login）
	mux.HandleFunc("/bare", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "final")
		w.WriteHeader(302)
	})
	// 6) 跨主机跳转：不该跟
	mux.HandleFunc("/cross", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://example.com/x")
		w.WriteHeader(302)
	})
	// 7) 畸形 Location：端口写成 :80:443
	mux.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://127.0.0.1:80:443/")
		w.WriteHeader(302)
	})
	// 8) 环路：a→b→a
	mux.HandleFunc("/loop-a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/loop-b")
		w.WriteHeader(302)
	})
	mux.HandleFunc("/loop-b", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/loop-a")
		w.WriteHeader(302)
	})
	// 9) 终点页
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>最终业务系统</title></head><body>这里是真实的业务系统登录页面，包含足够的正文文字用于判定</body></html>`)
	})
	return httptest.NewServer(mux)
}

func TestHTTPTraceFollowsJumps(t *testing.T) {
	srv := newJumpServer(t)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	host := u.Hostname()
	var port int
	fmt.Sscanf(u.Port(), "%d", &port)

	cases := []struct {
		name     string
		path     string
		wantHops int // 0 表示 >=2
		lastHas  string
		// wantJumpMark 期望在第 1 跳的 BodyNote 里出现「页面内跳转」标记。
		// 用于纯协议改写（http→https）这种测试服务器给不出 https 的情况：
		// 只要识别出跳转并尝试跟随，就算通过（实机 198.18.8.65 已验证能跟到 3 跳）。
		wantJumpMark bool
	}{
		{"JS 纯协议改写跳转", "/js", 1, "", true},
		{"JS 相对路径跳转", "/js-rel", 2, "最终业务系统", false},
		{"meta refresh 跳转", "/meta", 2, "最终业务系统", false},
		{"相对 Location ./login", "/sub/dir", 2, "Login", false},
		{"无斜杠相对 Location", "/bare", 2, "最终业务系统", false},
		{"跨主机不跟随", "/cross", 1, "", false},
		{"畸形 Location 不崩", "/broken", 1, "", false},
		{"环路在跳数上限内停止", "/loop-a", 6, "", false},
	}
	for _, c := range cases {
		hops := traceFrom(t, host, port, srv.URL+c.path)
		if len(hops) < 2 && c.wantHops == 0 {
			t.Errorf("%s: 期望至少 2 跳，实际 %d 跳", c.name, len(hops))
			continue
		}
		if c.wantHops > 0 && len(hops) != c.wantHops {
			t.Errorf("%s: 期望 %d 跳，实际 %d 跳", c.name, c.wantHops, len(hops))
			continue
		}
		if c.wantJumpMark {
			if !strings.Contains(hops[0].BodyNote, "页面内跳转") {
				t.Errorf("%s: 第 1 跳应标记「页面内跳转」，实际 BodyNote=%q", c.name, hops[0].BodyNote)
				continue
			}
		}
		if c.lastHas != "" {
			last := hops[len(hops)-1]
			if !strings.Contains(last.Body, c.lastHas) {
				t.Errorf("%s: 末跳正文应包含 %q，实际 %.80s", c.name, c.lastHas, last.Body)
				continue
			}
		}
		t.Logf("PASS %-22s hops=%d last=%s", c.name, len(hops), hops[len(hops)-1].ResponseLine)
	}
}

// traceFrom 以指定起始 URL 跑同样的抓取逻辑（HTTPTrace 固定 / 起点，这里复用内部实现）
func traceFrom(t *testing.T, ip string, port int, start string) []Hop {
	t.Helper()
	hops, err := HTTPTracePath(ip, port, start, 6)
	if err != nil {
		t.Logf("  抓取错误: %v", err)
	}
	return hops
}

func TestPlaceholderTitle(t *testing.T) {
	cases := []struct {
		title, status string
		want          bool
	}{
		{"Redirection……", "200", true},
		{"Redirecting to HTTPS", "200", true},
		{"重定向中...", "200", true},
		{"正在跳转", "200", true},
		{"307 TEMPORARY REDIRECT", "307", true},
		{"404 Not Found", "404", true},
		{"HTTP", "200", true},
		{"Generic HTTP Server", "200", true},
		{"Login", "200", true},
		{"Welcome to nginx!", "200", true},
		{"Index of /", "200", true},
		{"", "", true},
		{"深信服超融合平台", "200", false},
		{"SAP Message Server,release 722 (SP8)", "307", false},
		{"VMware vSphere", "200", false},
		{"HTTP Status 404 – Not Found", "404", true},
	}
	for _, c := range cases {
		if got := PlaceholderTitle(c.title, c.status); got != c.want {
			t.Errorf("PlaceholderTitle(%q,%q)=%v, 期望 %v", c.title, c.status, got, c.want)
		}
	}
}

func TestTraceWeak(t *testing.T) {
	weak := []Hop{{Body: `<html><head><title>Redirection……</title></head><body><p>Redirecting</p><script>location.replace('https:')</script></body></html>`}}
	if !TraceWeak(weak) {
		t.Error("跳转壳页应判为弱证据")
	}
	strong := []Hop{{Body: "<html><body>" + strings.Repeat("这是真实的业务系统页面内容。", 40) + "</body></html>"}}
	if TraceWeak(strong) {
		t.Error("有正文的页面不应判为弱证据")
	}
	if !TraceWeak(nil) {
		t.Error("无跳数应判为弱证据")
	}
}
