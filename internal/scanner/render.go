package scanner

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// renderSem 限制同时打开的标签页数。
// 实测（2026-11-09 审计）：6 路并发渲染时 72 次里失败 61 次，几乎全是 chromium 抢内存崩掉的。
// 现在共用一个常驻实例（见 browser.go），并发=标签页数，默认 2，可用 CHROME_MAX_TABS 调整。
var (
	renderSemOnce sync.Once
	renderSem     chan struct{}
)

func renderSlot() chan struct{} {
	renderSemOnce.Do(func() { renderSem = make(chan struct{}, chromeTabLimit()) })
	return renderSem
}

// RenderPage 用 headless 浏览器真渲染页面，取 title / meta description / 正文摘要。
// 用途：扫描只拿到 banner（如 websocket-sharp/1.0）根本判断不出是什么系统，
// 必须让浏览器真的打开一次页面才能看到业务系统名。
func RenderPage(url string, wait time.Duration) (title, desc, text string, err error) {
	if wait <= 0 {
		wait = 8 * time.Second
	}
	sem := renderSlot()
	sem <- struct{}{}
	defer func() { <-sem }()

	// 超时保护：目标不响应/页面挂起时必须收敛，否则识别 worker 被拖死
	ctx, cancel := context.WithTimeout(context.Background(), wait+20*time.Second)
	defer cancel()

	html, err := FetchHTML(ctx, url, wait)
	if err != nil {
		// 一次重试：实例刚被空闲回收、或首次启动抖动，重建后通常就好
		html, err = FetchHTML(ctx, url, wait)
		if err != nil {
			return "", "", "", err
		}
	}
	if html == "" {
		return "", "", "", context.DeadlineExceeded
	}
	if m := reTitle.FindStringSubmatch(html); len(m) > 1 {
		title = cleanStr(m[1])
	}
	if m := reMetaDesc.FindStringSubmatch(html); len(m) > 1 {
		desc = cleanStr(m[1])
	}
	body := html
	if m := reBody.FindStringSubmatch(html); len(m) > 1 {
		body = m[1]
	}
	text = firstRunes(cleanStr(body), 800)
	return title, desc, text, nil
}

// ChromeAvailable 探测 chromium 是否可用（容器里装了才走渲染兜底）
func ChromeAvailable() bool {
	_, err := exec.LookPath(chromeBinary())
	return err == nil
}

func chromeBinary() string {
	if p := os.Getenv("CHROME_PATH"); p != "" {
		return p
	}
	for _, c := range []string{"chromium", "chromium-browser", "google-chrome", "chrome"} {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return "chromium"
}

var (
	reTitle    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reMetaDesc = regexp.MustCompile(`(?is)<meta[^>]+name=["']description["'][^>]+content=["']([^"']*)`)
	reBody     = regexp.MustCompile(`(?is)<body[^>]*>(.*?)</body>`)
	reTags     = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`)
	reSpaces   = regexp.MustCompile(`\s+`)
)

func stripTags(s string) string { return reTags.ReplaceAllString(s, " ") }

// cleanStr 去标签/压空白
func cleanStr(s string) string {
	return reSpaces.ReplaceAllString(strings.TrimSpace(stripTags(s)), " ")
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
