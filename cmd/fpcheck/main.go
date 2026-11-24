// 验证：把最后一跳的响应直接喂给引擎的规则集（MatchWeb），看能否给出产品名。
//
// 目的：判断"引擎指纹模块是否对这个目标无结果"，以及值不值得在 AI 识别前
// 插一层"引擎规则匹配"（确定性、零成本、不受网关影响）。
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"asset-platform/internal/scanner"
	svcid "github.com/RongSec/CyberLook-Scan/svcid"
)

func main() {
	path := os.Args[1]
	rs, _, err := loadRules("data/fingerprint")
	if err != nil {
		panic(err)
	}
	if len(os.Args) > 2 && os.Args[2] == "bench" {
		bench(rs)
		return
	}
	f, _ := os.Open(path)
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		p := strings.Split(ln, ",")
		if len(p) < 2 {
			continue
		}
		ip := p[0]
		port, _ := strconv.Atoi(p[1])
		scheme := "http"
		if strings.Contains(ln, "https") {
			scheme = "https"
		}
		hops, e := scanner.HTTPTrace(ip, port, scheme, 4)
		if e != nil || len(hops) == 0 {
			fmt.Printf("%-21s 抓包失败 %v\n", fmt.Sprintf("%s:%d", ip, port), e)
			continue
		}
		last := hops[len(hops)-1]
		title := scanner.HTMLTitle(last.Body)
		hits := rs.MatchWeb(last.ResponseHeaders, last.Body, title, "", "")
		var top []string
		for i, h := range hits {
			if i >= 2 {
				break
			}
			top = append(top, fmt.Sprintf("%s(%d,src=%s)", h.Product, h.Score, h.Source))
		}
		fmt.Printf("%-21s status=%-22s title=%-24.24q fp=%v\n",
			fmt.Sprintf("%s:%d", ip, port), last.ResponseLine, title, top)
	}
	_ = svcid.Hit{}
}

// loadRules 复用引擎的规则加载（与 scanner.loadRulesCached 同一份文件）。
func loadRules(dir string) (*svcid.RuleSet, *svcid.ProbeIndex, error) {
	rs, err := svcid.Load(dir)
	if err != nil {
		return nil, nil, err
	}
	return rs, rs.BuildServiceProbes(), nil
}

// bench 量化 MatchWeb 对一份 3KB 响应的耗时：这决定它能不能放在每条识别链路上。
func bench(rs *svcid.RuleSet) {
	hdrs := "HTTP/1.1 200 OK\r\nServer: Webs\r\nContent-Type: text/html\r\nContent-Length: 480\r\n"
	body := strings.Repeat("<div class=\"login\"><input id=user><script src=/app.js></script></div>\n", 30)
	n := 200
	t0 := time.Now()
	for i := 0; i < n; i++ {
		rs.MatchWeb(hdrs, body, "", "", "")
	}
	per := time.Since(t0) / time.Duration(n)
	fmt.Printf("MatchWeb %d 次，单次平均 %v（正文 %d 字节）\n", n, per, len(body))
}
