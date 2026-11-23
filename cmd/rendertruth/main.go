package main

// rendertruth 用真实浏览器完整渲染一批 web 目标，输出页面标题 / 描述 / 正文摘要，
// 作为识别召回率测试的基准答案（ground truth）。
//
// 为什么不复用平台的识别链路：那样就变成"用被测量本身当标准答案"。
// 这里只做渲染、不做任何判断，产出的就是"浏览器打开这一页能看到什么"。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"asset-platform/internal/scanner"
)

type result struct {
	Target string `json:"target"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Desc   string `json:"desc"`
	Text   string `json:"text"`
	Err    string `json:"err,omitempty"`
	Ms     int64  `json:"ms"`
}

func cut(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: rendertruth <targets.txt>")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}

	out := make([]result, len(lines))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for idx, line := range lines {
		wg.Add(1)
		go func(idx int, line string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			parts := strings.Split(line, ",")
			ip, port, scheme := parts[0], "80", "http"
			if len(parts) > 1 {
				port = parts[1]
			}
			if len(parts) > 2 && strings.EqualFold(parts[2], "https") {
				scheme = "https"
			}
			url := fmt.Sprintf("%s://%s:%s/", scheme, ip, port)

			t0 := time.Now()
			title, desc, text, rerr := scanner.RenderPageCtx(context.Background(), url, 10*time.Second)
			r := result{Target: line, URL: url, Title: title, Desc: desc, Text: cut(text, 900), Ms: time.Since(t0).Milliseconds()}
			if rerr != nil {
				r.Err = rerr.Error()
			}
			out[idx] = r
			fmt.Fprintf(os.Stderr, "%-42s %6dms title=%q err=%v\n", url, r.Ms, cut(title, 40), rerr)
		}(idx, line)
	}
	wg.Wait()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	_ = enc.Encode(out)
}
