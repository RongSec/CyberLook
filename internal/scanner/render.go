package scanner

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// renderSem 限制同时运行的 chromium 实例数。
// 实测（2026-11-09 审计）：6 路并发渲染时 72 次里失败 61 次，几乎全是 chromium 抢内存崩掉的；
// 渲染本身单次就要 200MB 上下，必须限流。
var renderSem = make(chan struct{}, 3)

// RenderPage 用 headless chromium 真渲染页面，取 title / meta description / 正文摘要。
// 用途：扫描只拿到 banner（如 websocket-sharp/1.0）根本判断不出是什么系统，
// 必须让浏览器真的打开一次页面才能看到业务系统名。
func RenderPage(url string, wait time.Duration) (title, desc, text string, err error) {
	if wait <= 0 {
		wait = 8 * time.Second
	}
	renderSem <- struct{}{}
	defer func() { <-renderSem }()
	ctx, cancel := context.WithTimeout(context.Background(), wait+15*time.Second)
	defer cancel()

	budget := int(wait.Milliseconds())
	args := []string{
		"--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage",
		"--disable-extensions", "--hide-scrollbars", "--mute-audio",
		"--ignore-certificate-errors",
		"--user-agent=Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36",
		"--virtual-time-budget=" + itoa(budget),
		"--dump-dom", url,
	}
	// 渲染失败多半是偶发（内存/Crashpad），重试一次能救回不少
	var html string
	for attempt := 0; attempt < 2; attempt++ {
		cmd := exec.CommandContext(ctx, chromeBinary(), args...)
		var out []byte
		out, err = cmd.Output()
		if err == nil && len(out) > 0 {
			html = string(out)
			break
		}
	}
	if err != nil && html == "" {
		return "", "", "", err
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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
