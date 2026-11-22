package service

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/llm"
	"asset-platform/internal/model"
	"asset-platform/internal/scanner"
)

// ===== AI 服务识别工作流 =====
//
// 四级逐级加信息量（tscan 结果 → 完整请求响应包 → 浏览器渲染 → 再判）：
//
//	第 0 级：tscan 拿到的标题/指纹是否"清晰明了"（规则判定，不花 token）
//	第 1 级：主动抓完整「请求 + 响应」包（含 302，最多 4 跳）交给 AI 分析
//	第 2 级：AI 仍判不出来，才用 headless 浏览器真渲染页面
//	第 3 级：AI 再用渲染结果判一次
//
// 第 1 级是关键：只给 banner（UnKnown / websocket-sharp/1.0）这种信息模型只能瞎猜，
// 必须把真实请求行、响应头、页面内容喂进去才有意义。

const (
	aiIdentifyQueueSize = 2000
)

// aiIdentifyWorkers 并发识别数。每条可能拉起 chromium 渲染一个页面，
// 单个渲染进程就能吃掉一个核，所以不能写死一个"看起来不大"的数字。
//
// v0.0.50：原来固定 3，4 核机器上 3 路渲染 + 扫描节点同时跑 = 整机吃满、web 打不开。
// 现在默认 cpuN/2（下限 1、上限 3）：4 核 → 2，2 核 → 1，8 核及以上 → 3。
// 可用 AI_IDENT_CONCURRENCY 显式覆盖（1~16）。
func aiIdentifyWorkers() int {
	if v := strings.TrimSpace(os.Getenv("AI_IDENT_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 16 {
			return n
		}
	}
	// 每 2 核 1 路（上限 3）：4 核 → 2 路。渲染闸门上线后单条只需 1~3 秒，
	// 2 路的 CPU 由 identCPUGate 兜住，比"为了省 CPU 只跑 1 路"更合理——
	// 积压是真问题，空转也是真浪费。
	n := runtime.NumCPU() / 2
	if n < 1 {
		n = 1
	}
	if n > 3 {
		n = 3
	}
	return n
}

// confidentEnough 第一轮（只看请求响应包）要多少分才直接收工。
// 低于这个分数说明只判到了厂商/类别一级，继续走浏览器渲染拿页面文字。
// 实测依据：62 分给出的「深信服 SSL VPN」渲染后是「深信服超融合平台 SANGFOR HCI」。
const confidentEnough = 80

// aiIdentifyTimeout 单条识别的硬上限。正常一条 5-15 秒，走渲染最多 30 秒；
// 超过这个值基本可以认定 CDP 卡死，销毁浏览器实例收敛。
const aiIdentifyTimeout = 90 * time.Second

// aiIdentifyMaxPerTask 每个任务最多识别多少条服务。
// 历史值 100 导致上万 web 端口只有前 100 条走识别，其余静默丢弃。
// 现在默认 2000，可用环境变量 AI_IDENTIFY_MAX_PER_TASK 覆盖。
func aiIdentifyMaxPerTask() int {
	if v := os.Getenv("AI_IDENTIFY_MAX_PER_TASK"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 2000
}

// IdentStage 识别链路的一步
type IdentStage struct {
	Stage  string `json:"stage"`
	Input  string `json:"input"`
	Output string `json:"output"`
	OK     bool   `json:"ok"`
}

type aiIdentifyJob struct {
	TaskID uint
	IP     string
	Port   int
	// ServiceID 补给协程带上主键，扫完直接按 id 更新，省一次 ip+port 查询
	ServiceID uint
}

var (
	aiQueueOnce sync.Once
	aiQueue     chan aiIdentifyJob

	aiCountMu sync.Mutex
	aiCount   = map[uint]int{}
)

// TitleClear 只做"输入是否为空"这种结构性判断，**不做内容判定**。
//
// 曾经这里是一大坨正则：默认页清单（index of / welcome / login / 登录 …）、
// 字符形态（纯小写=组件名、首字母大写=产品名）、长度门槛、含"未知"就否决。
// 这套东西的问题是必然的：
//   - 白名单外的真系统被当成"不清晰"（`Login` 是壳，但 `SAP Enterprise Portal` 也可能只有一页）；
//   - 灰度永远调不完，新默认页一出现就得补规则；
//   - 更糟的是它同时被当成"识别成功"的闸门，把模型给出的合法名字截断掉。
//
// 现在"这个名字对不对"只由模型判断（见 askAI / IdentifyService），
// 这里只拦空值和单字符——那不是判断，是脏数据。
func TitleClear(title string) bool {
	t := strings.TrimSpace(title)
	if len([]rune(t)) < 2 {
		return false
	}
	// 纯标点/纯数字：不是名字
	hasWord := false
	for _, r := range t {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 0x2E80 {
			hasWord = true
			break
		}
	}
	return hasWord
}

// EnqueueAIIdentify 把一次待识别的服务塞进队列（异步，不阻塞扫描）
func EnqueueAIIdentify(taskID uint, ip string, port int) {
	if len(llm.AvailableProfiles()) == 0 || !AIServiceIdentifyEnabled() {
		return
	}
	aiCountMu.Lock()
	if aiCount[taskID] >= aiIdentifyMaxPerTask() {
		n := aiCount[taskID]
		aiCountMu.Unlock()
		// 不再静默丢弃：显式记日志，便于发现"识别只跑了前 N 条"
		log.Printf("[ai-ident] 任务 #%d 已达识别上限 %d 条，跳过 %s:%d（可用 AI_IDENTIFY_MAX_PER_TASK 调整）",
			taskID, n, ip, port)
		return
	}
	aiCount[taskID]++
	aiCountMu.Unlock()

	aiQueueOnce.Do(startAIWorkers)
	select {
	case aiQueue <- aiIdentifyJob{TaskID: taskID, IP: ip, Port: port}:
		aiMetricEnqueued()
	default:
		aiMetricDropped()
		log.Printf("[ai-ident] 任务 #%d 识别队列已满（%d），丢弃 %s:%d", taskID, aiIdentifyQueueSize, ip, port)
	}
}

// aiInFlight 已入队待处理的 ip:port，防止补给协程重复入队。
var aiInFlight sync.Map

// aiQueueFeeder 每 15 秒从库里补一批待识别（仅 HTTP(S)），队列过半就歇着。
//
// 队列持久化的取舍：不引入 Redis/表锁，直接以 services.ai_need 作为"待办标记"，
// 内存队列只是加速通道。重启后 inFlight 清空，同一批会在下一轮补给里重新入队，
// 语义安全（识别是幂等的：重复跑最多多花几秒，不会写错数据）。
func aiQueueFeeder() {
	tk := time.NewTicker(15 * time.Second)
	defer tk.Stop()
	for range tk.C {
		if aiQueue == nil || len(aiQueue) > aiIdentifyQueueSize/2 {
			continue
		}
		fillQueueFromDB(200)
	}
}

// fillQueueFromDB 把库里 ai_need 的 HTTP(S) 服务补进内存队列，返回入队条数。
func fillQueueFromDB(limit int) int {
	var svcs []model.Service
	config.DB.Model(&model.Service{}).
		Where("ai_need = ? AND ai_checked_at IS NULL AND protocol ILIKE 'http%'", true).
		Order("last_seen DESC NULLS LAST").Limit(limit).Find(&svcs)
	queued := 0
	for _, s := range svcs {
		key := s.IP + ":" + strconv.Itoa(s.Port)
		if _, loaded := aiInFlight.LoadOrStore(key, time.Now()); loaded {
			continue
		}
		select {
		case aiQueue <- aiIdentifyJob{TaskID: s.LastTaskID, IP: s.IP, Port: s.Port, ServiceID: s.ID}:
			aiMetricEnqueued()
			queued++
		default:
			aiInFlight.Delete(key)
			return queued
		}
	}
	return queued
}

func startAIWorkers() {
	aiQueue = make(chan aiIdentifyJob, aiIdentifyQueueSize)
	// 补给协程：队列不再只靠"扫描时逐个 Enqueue"，而是持续从库里 ai_need 的
	// HTTP(S) 条目补齐。这样进程重启不会丢掉"还没识别"的那批（内存队列本来是空的），
	// 用户点一次重识别也只是"把库里标出来"，不需要一次塞进内存。
	go aiQueueFeeder()
	n := aiIdentifyWorkers()
	log.Printf("[ai-ident] 识别工作池启动：%d 路（本机 %d 核，AI_IDENT_CONCURRENCY 可调）", n, runtime.NumCPU())
	for i := 0; i < n; i++ {
		go func() {
			for job := range aiQueue {
				// CPU 闸门：整机超预算就先等一下——渲染比网页更耐等，
				// 而页面卡住是用户立刻能感知的（v0.0.50 实测容器峰值 445%）。
				if d := identCPUGate(); d > 0 {
					log.Printf("[ai-ident] 整机 CPU 超预算，识别前等待 %s", d)
				}
				runAIIdentifySafe(job)
			}
		}()
	}
}

// runAIIdentifySafe 单条识别的兜底：panic 不杀 worker，超时强制收敛。
//
// 实测问题（2026-11-20）：批量补识别 200 条后整条队列停摆，ai_checked_at 不再更新，
// 重启服务才恢复——chromium 页面挂住时 CDP 调用迟迟不返回，3 个 worker 被占死。
// 现在给每条识别套一个看门狗：超过 aiIdentifyTimeout 就销毁浏览器实例，
// 在途调用立即失败返回，worker 继续消费下一条。
func runAIIdentifySafe(job aiIdentifyJob) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[ai-ident] panic %s:%d: %v", job.IP, job.Port, r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), aiIdentifyTimeout)
	defer cancel()
	timer := time.AfterFunc(aiIdentifyTimeout, func() {
		// 两件事一起做：cancel 让识别协程立刻从 CDP/LLM 等点退出（worker 立即归还），
		// ResetBrowser 处理实例本身已经坏了的情况（后续调用重建）。
		// 只杀浏览器不 cancel，协程还会傻等 HTTP/CDP 自己的长超时——这就是那条 125 秒。
		log.Printf("[ai-ident] %s:%d 超过 %s 未完成，取消识别并销毁浏览器实例",
			job.IP, job.Port, aiIdentifyTimeout)
		scanner.ResetBrowser()
	})
	defer timer.Stop()
	runAIIdentifyCtx(ctx, job)
	if d := time.Since(start); d > 30*time.Second {
		log.Printf("[ai-ident] %s:%d 识别耗时 %s（偏慢，多半是渲染等待）", job.IP, job.Port, d.Round(time.Second))
	}
}

// IdentResult 一次识别链路的结论
type IdentResult struct {
	Title      string
	Product    string
	Confident  bool   // 模型给出名字且置信度达标（>=60）
	Sufficient bool   // 模型认为"现有证据已经足够判断，只是给不出确切名字"
	Reason     string // 模型给的依据（20 字以内）
	Stages     []IdentStage
	Hops       []scanner.Hop
	// Timing 分阶段耗时（毫秒）：trace / judge / render / identify。
	// 没有这一步就只能看到"总共 43 秒"，看不出是渲染慢还是模型慢。
	Timing map[string]int64
}

// hopStatus 从 "HTTP/1.1 502 Bad Gateway" 里取状态码；取不到返回 0。
func hopStatus(respLine string) int {
	f := strings.Fields(respLine)
	for _, x := range f {
		if n, err := strconv.Atoi(x); err == nil && n >= 100 && n <= 599 {
			return n
		}
	}
	return 0
}

// renderWorthwhile 判断「这一步渲染值不值得」——这是识别里最贵的一步（实测 8.4~28s，
// 占单条总耗时 77%~85%），不能对必然拿不到答案的响应无脑渲染。
//
// 实测（2026-11-22，30 条占位样本）：13 条全部进渲染，平均 9.8s；渲染后能给出结论的
// 比例是 **0%**——证据全是 502/403/404/302 这类错误页或跳转页，浏览器打开它们只是把
// 同一段错误文本再读一遍。这就是「前置阶段没处理就丢给浏览器」的具体表现。
//
// 规则：
//   - 非 HTML（JSON/纯文本/二进制）→ 不渲染，渲染也只会拿到 JSON 字符串；
//   - 最后一跳 404/405/5xx → 不渲染，错误页没有身份信息；
//   - 401/403 且正文很小（<1500 字节）→ 不渲染，那只是一个 "403 Forbidden" 文本；
//   - 其它 → 渲染（含 JS 加密登录页、SPA 空壳，这些只有浏览器能出内容）。
func renderWorthwhile(hops []scanner.Hop) (bool, string) {
	if len(hops) == 0 {
		return false, "无响应"
	}
	last := hops[len(hops)-1]
	hdr := strings.ToLower(last.ResponseHeaders)
	bodyLow := strings.ToLower(last.Body)
	looksHTML := strings.Contains(hdr, "text/html") ||
		strings.Contains(bodyLow, "<html") || strings.Contains(bodyLow, "<!doctype html")
	if !looksHTML {
		ct := ""
		for _, ln := range strings.Split(last.ResponseHeaders, "\n") {
			if strings.HasPrefix(strings.ToLower(ln), "content-type:") {
				ct = strings.TrimSpace(ln[len("content-type:"):])
				break
			}
		}
		if ct == "" {
			ct = "非 HTML"
		}
		return false, "非 HTML 响应（" + truncateStr(ct, 40) + "），浏览器也只拿到同样的字节"
	}
	st := hopStatus(last.ResponseLine)
	switch {
	case st == 401 || st == 403:
		if len(strings.TrimSpace(last.Body)) < 1500 {
			return false, fmt.Sprintf("%d 拒绝页且正文仅 %d 字节，渲染拿不到系统名", st, len(last.Body))
		}
	case st >= 400:
		return false, fmt.Sprintf("最后一跳是 %d 错误页，错误页里没有身份信息", st)
	}
	return true, ""
}

// traceWithFallback 抓包，协议判错时用另一个 scheme 重试一次。
//
// 为什么需要：入库的 protocol 来自引擎的端口探测，在"302 跳到 https"或"双栈端口"
// 场景下经常判反。实测 198.18.6.114:80 被标成 HTTPS，识别时直接
// `tls: first record does not look like a TLS handshake`，整条链路 0 秒空跑，
// 白排一次队。识别是拿证据判断身份，协议错了就用另一个再试一次，代价只是多一次请求。
func traceWithFallback(ip string, port int, scheme string) ([]scanner.Hop, error, string) {
	hops, err := scanner.HTTPTrace(ip, port, scheme, 4)
	if err == nil && len(hops) > 0 {
		return hops, nil, scheme
	}
	alt := "https"
	if scheme == "https" {
		alt = "http"
	}
	if h2, e2 := scanner.HTTPTrace(ip, port, alt, 4); e2 == nil && len(h2) > 0 {
		return h2, nil, alt
	}
	return hops, err, scheme
}

// IdentifyService 跑完整识别链路（不带 ctx，供非关键路径使用）。
func IdentifyService(svc model.Service) IdentResult {
	return IdentifyServiceCtx(context.Background(), svc)
}

// IdentifyServiceCtx 跑完整识别链路：
//
//	抓请求响应包 → 研判 agent（这页是否异常、要不要渲染）→ 浏览器渲染 → 识别 agent（给名字）
//
// ctx 贯穿全程。看门狗到点 cancel 后：渲染中的 CDP 调用立刻返回 context.Canceled，
// LLM 调用走 chatWithCtx 也不再占着 worker 等 HTTP 超时，于是整条链路顺着错误退栈、
// worker 立即归还。以前不传 ctx，看门狗只能杀浏览器，协程仍卡在等回复上——
// 实测有一条拖到 125 秒，等于白占一个 worker 半分钟。
func IdentifyServiceCtx(ctx context.Context, svc model.Service) IdentResult {
	res := IdentResult{Title: strings.TrimSpace(svc.Title), Product: strings.TrimSpace(svc.Fingerprint),
		Timing: map[string]int64{}}
	stages := []IdentStage{}
	mark := func(stage string, t0 time.Time) { res.Timing[stage] = time.Since(t0).Milliseconds() }
	baseURL := buildURL(svc)
	var hops []scanner.Hop

	done := func() IdentResult {
		res.Stages, res.Hops = stages, hops
		return res
	}

	if baseURL == "" {
		stages = append(stages, IdentStage{Stage: "抓取请求响应包", Input: "-", Output: "非 http(s) 协议，不适用 Web 识别"})
		return done()
	}

	// 第 1 步：抓完整请求响应包（含跳转，最多 4 跳）
	scheme := "http"
	if strings.HasPrefix(strings.ToLower(svc.Protocol), "https") {
		scheme = "https"
	}
	tTrace := time.Now()
	var trErr error
	hops, trErr, scheme = traceWithFallback(svc.IP, svc.Port, scheme)
	mark("trace", tTrace)
	switch {
	case trErr != nil:
		stages = append(stages, IdentStage{Stage: "抓取请求响应包", Input: baseURL, Output: "抓取失败：" + trErr.Error()})
		return done()
	case len(hops) == 0:
		stages = append(stages, IdentStage{Stage: "抓取请求响应包", Input: baseURL, Output: "无响应"})
		return done()
	}
	digest := scanner.TraceDigest(hops)
	stages = append(stages, IdentStage{
		Stage:  "抓取请求响应包",
		Input:  fmt.Sprintf("%s（共 %d 跳）", baseURL, len(hops)),
		Output: hops[0].ResponseLine + " / " + orNone(shortHeader(hops[0].ResponseHeaders)),
		OK:     true,
	})
	if ctx.Err() != nil {
		stages = append(stages, IdentStage{Stage: "中止", Output: "超过单条时限，放弃本次识别"})
		return done()
	}

	// 第 2 步：研判 agent —— 这一页是否异常、要不要开浏览器
	tJudge := time.Now()
	jv := askJudge(ctx, digest)
	mark("judge", tJudge)
	stages = append(stages, IdentStage{
		Stage: "AI·研判", Input: fmt.Sprintf("%d 跳 / %d 字节", len(hops), len(digest)),
		Output: fmt.Sprintf("kind=%s render=%v %s", orNone(jv.Kind), jv.Render, orNone(jv.Reason)),
		OK:     jv.Kind != "",
	})

	// 研判失败（模型不可用、输出不是 JSON）时退回本地规则，不让整条链路停摆
	needRender, renderWhy := jv.Render, "研判 kind="+orNone(jv.Kind)
	if jv.Kind == "" {
		ok, why := renderWorthwhile(hops)
		needRender, renderWhy = ok, "本地规则："+why
	}
	if jv.Kind == "error" {
		needRender, renderWhy = false, "研判：错误页，无身份信息"
	}

	// 第 3 步：按需渲染（全链路最贵的一步）
	pageTitle, rendered := "", ""
	if !needRender {
		stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: "跳过：" + renderWhy})
		aiMetricRenderSkipped()
	} else if !scanner.ChromeAvailable() {
		stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: "容器内无 chromium，跳过"})
	} else {
		tRender := time.Now()
		var pageDesc, pageText string
		var rerr error
		pageTitle, pageDesc, pageText, rerr = scanner.RenderPageCtx(ctx, baseURL, 8*time.Second)
		mark("render", tRender)
		switch {
		case rerr != nil && ctx.Err() != nil:
			stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: "被中止：" + rerr.Error()})
			return done()
		case rerr != nil:
			stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: "渲染失败：" + rerr.Error()})
		default:
			rendered = fmt.Sprintf("页面标题: %s\nmeta描述: %s\n正文摘要: %s",
				orNone(pageTitle), orNone(pageDesc), orNone(pageText))
			stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: rendered,
				OK: strings.TrimSpace(pageTitle) != ""})
		}
	}

	// 第 4 步：识别 agent —— 有渲染结果就用渲染结果，否则用抓包证据
	evidence, source := digest, "请求响应包"
	if rendered != "" {
		evidence, source = rendered, "渲染结果"
	}
	tId := time.Now()
	iv := askIdentify(ctx, source, evidence, rendered != "")
	mark("identify", tId)
	stages = append(stages, IdentStage{
		Stage: "AI·识别·" + source, Input: truncateStr(evidence, 120),
		Output: orNone(iv.Raw), OK: iv.dotConfident(),
	})

	switch {
	case iv.dotConfident():
		res.apply(iv)
	case strings.TrimSpace(pageTitle) != "" && TitleClear(pageTitle) &&
		!scanner.PlaceholderTitle(pageTitle, ""):
		// 模型没给出高置信名字，但渲染后的标题本身是明确文字：作为低置信候选带上。
		// 排除 Login / Redirection…… 这类占位标题——浏览器的 <title> 经常就是它们。
		res.Title = strings.TrimSpace(pageTitle)
		res.Reason = "渲染标题（未经模型确认）"
	case strings.TrimSpace(iv.Reason) != "":
		res.Reason = iv.Reason
	}
	return done()
}

// apply 采纳模型结论
func (r *IdentResult) apply(v aiVerdict) {
	if strings.TrimSpace(v.Title) != "" {
		r.Title = strings.TrimSpace(v.Title)
	} else if strings.TrimSpace(v.Product) != "" {
		r.Title = strings.TrimSpace(v.Product)
	}
	if strings.TrimSpace(v.Product) != "" {
		r.Product = strings.TrimSpace(v.Product)
	}
	r.Confident = v.Confidence >= 60
	r.Sufficient = v.Sufficient
	r.Reason = v.Reason
}

// shortHeader 取响应头里的 Location / Server 行，用于阶段摘要
func shortHeader(h string) string {
	for _, ln := range strings.Split(h, "\n") {
		low := strings.ToLower(ln)
		if strings.HasPrefix(low, "location:") || strings.HasPrefix(low, "server:") {
			return strings.TrimSpace(ln)
		}
	}
	return ""
}

// aiVerdict 模型对一次识别证据的判断
type aiVerdict struct {
	Title      string `json:"title"`
	Product    string `json:"product"`
	Category   string `json:"category"`
	Confidence int    `json:"confidence"`
	Sufficient bool   `json:"sufficient"`
	Reason     string `json:"reason"`
	Raw        string `json:"-"`
}

// dotConfident 名字可用：有名字且置信度达标
func (v aiVerdict) dotConfident() bool {
	return v.Confidence >= 60 && (strings.TrimSpace(v.Title) != "" || strings.TrimSpace(v.Product) != "")
}

// judgeVerdict 研判 agent 的结论
type judgeVerdict struct {
	Kind   string `json:"kind"`   // normal / error / shell / jump / weak
	Render bool   `json:"render"` // 要不要开浏览器
	Reason string `json:"reason"`
	Raw    string `json:"-"`
}

// askJudge 研判这一页是否异常、要不要深度判定（渲染）。
//
// 输入只给"页面长相"相关的证据（TraceDigest 已含状态码、标题、跳转链、正文摘要），
// 不要求它给名字——那是识别 agent 的事。两个角色分开，模型不会为了凑名字而忽略
// "这其实是错误页"，也不会为了说"证据不足"而漏掉已经写在页面上的产品名。
func askJudge(ctx context.Context, evidence string) judgeVerdict {
	res, err := chatWithCtx(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: PromptFor(RoleJudge)},
			{Role: "user", Content: "证据：\n" + evidence},
		},
		Temperature: 0.1,
		MaxTokens:   200,
	})
	if err != nil || res == nil || res.Resp == nil || len(res.Resp.Choices) == 0 {
		if err != nil && ctx.Err() == nil {
			aiMetricLLMFail()
			log.Printf("[ai-ident] 研判调用失败: %v", err)
		}
		return judgeVerdict{}
	}
	answer := strings.TrimSpace(res.Resp.Choices[0].Message.Content)
	var v judgeVerdict
	if err := json.Unmarshal([]byte(extractJSON(answer)), &v); err != nil {
		return judgeVerdict{Raw: answer}
	}
	switch v.Kind {
	case "normal", "error", "shell", "jump", "weak":
	default:
		// 模型回了别的词：当作没判，退回本地规则
		v.Kind = ""
	}
	v.Raw = answer
	return v
}

// askIdentify 识别 agent：给出业务系统名。
func askIdentify(ctx context.Context, source, evidence string, rendered bool) aiVerdict {
	hint := "以下是从该端口抓到的真实请求/响应包（含重定向）"
	if rendered {
		hint = "以下是浏览器真正渲染后的页面信息"
	}
	res, err := chatWithCtx(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: PromptFor(RoleIdentify)},
			{Role: "user", Content: fmt.Sprintf("%s。\n来源：%s\n\n%s", hint, source, evidence)},
		},
		Temperature: 0.1,
		MaxTokens:   400,
	})
	if err != nil || res == nil || res.Resp == nil || len(res.Resp.Choices) == 0 {
		if err != nil && ctx.Err() == nil {
			aiMetricLLMFail()
			log.Printf("[ai-ident] 识别调用失败: %v", err)
		}
		return aiVerdict{Sufficient: true, Raw: "模型调用失败"}
	}
	answer := strings.TrimSpace(res.Resp.Choices[0].Message.Content)
	var v aiVerdict
	if err := json.Unmarshal([]byte(extractJSON(answer)), &v); err != nil {
		return aiVerdict{Sufficient: true, Raw: answer}
	}
	v.Raw = answer
	return v
}

// chatWithCtx 让 LLM 调用可被 ctx 打断。
//
// llm.ChatWithFailover 内部用 http.Client 的固定超时（默认 120s），没有 ctx 入口。
// 直接改它的签名会影响所有调用点，所以在外面包一层：调用放 goroutine，主协程 select
// ctx.Done()。ctx 取消后主协程立刻返回，goroutine 由 HTTP 超时自然结束，
// channel 带缓冲所以不会永久泄漏。
func chatWithCtx(ctx context.Context, req llm.ChatRequest) (*llm.CallResult, error) {
	// 已经取消了就别起 goroutine：省一次无意义的调用，也让"取消后立即返回"
	// 这条语义可以被单测直接覆盖。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		res *llm.CallResult
		err error
	}
	ch := make(chan result, 1)
	go func() {
		r, e := llm.ChatWithFailover(req, 0)
		ch <- result{r, e}
	}()
	select {
	case r := <-ch:
		return r.res, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func runAIIdentifyCtx(ctx context.Context, job aiIdentifyJob) {
	var svc model.Service
	if err := config.DB.Where("ip = ? AND port = ?", job.IP, job.Port).First(&svc).Error; err != nil {
		aiInFlight.Delete(job.IP + ":" + strconv.Itoa(job.Port))
		return
	}
	// 无论走哪条分支都要把"在途"标记清掉，否则补给协程永远不再碰这一条
	defer aiInFlight.Delete(svc.IP + ":" + strconv.Itoa(svc.Port))
	if !strings.HasPrefix(strings.ToLower(svc.Protocol), "http") {
		// AI 识别链路目前只覆盖 HTTP(S)；非 HTTP 的交给引擎侧规则
		aiMetricNonHTTP()
		config.DB.Model(&model.Service{}).Where("id = ?", svc.ID).Update("ai_checked_at", time.Now())
		return
	}

	t0 := time.Now()
	r := IdentifyServiceCtx(ctx, svc)
	total := time.Since(t0)
	aiMetricNote(r, total)
	// 一条一行，把"慢在哪一步"写清楚：渲染占比是判断"该不该少渲染"的唯一依据。
	log.Printf("[ai-ident] %s:%d 用时 %s（抓包 %dms / 研判 %dms / 渲染 %dms / 识别 %dms）渲染=%v 置信=%v 结论=%s",
		job.IP, job.Port, total.Round(100*time.Millisecond).String(),
		r.Timing["trace"], r.Timing["judge"], r.Timing["render"], r.Timing["identify"],
		r.Timing["render"] > 0, r.Confident, truncateStr(r.Title, 40))
	changed := ApplyIdentResult(svc, r)
	recordIdentConversation(job.TaskID, svc, r, changed)
}

// IsUnidentified 模型结论是否是「没认出来」这种否定语义。
//
// 实测倒退案例：198.19.253.131:50306 原标题是「阿里云对象存储 OSS」，
// 重识别后写成「对象存储访问拒绝页（未识别出业务系统）」，置信还>=60。
// 这类结论本身没错，但它不包含身份信息，拿来覆盖已有有效标题就是净损失。
func IsUnidentified(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return true
	}
	low := strings.ToLower(t)
	for _, k := range []string{"未识别", "无法识别", "未知", "无法判断", "不确定", "未确定",
		"unidentified", "unknown", "not identified", "n/a"} {
		if strings.Contains(low, k) {
			return true
		}
	}
	// 「访问拒绝页」「登录页」这类纯页面形态描述，也不构成系统名
	for _, k := range []string{"访问被拒绝", "访问拒绝页", "错误页", "默认页"} {
		if strings.Contains(t, k) && !strings.Contains(t, "系统") {
			return true
		}
	}
	return false
}

// ApplyIdentResult 把识别结论写回服务记录，返回是否发生改动。
//
// 写回口径（批量识别与手动识别共用，避免两处漂移）：
//   - title：置信 >=60 且不是占位串（`Redirection……` / `Login` / `HTTP` / 状态文本）
//   - fingerprint：置信 >=60 且非空，允许覆盖旧值，旧值留在 prev_fingerprint
//   - 无论是否改动，都记 ai_checked_at 并把 ai_need 清掉，避免重复排队
func ApplyIdentResult(svc model.Service, r IdentResult) bool {
	updates := map[string]interface{}{"ai_checked_at": time.Now(), "ai_need": false}
	changed := false
	usableTitle := TitleClear(r.Title) && !scanner.PlaceholderTitle(r.Title, "")
	// 已有有效标题时，不允许用"未识别/未知"这类否定结论覆盖它
	if usableTitle && IsUnidentified(r.Title) && !scanner.PlaceholderTitle(svc.Title, svc.StatusCode) {
		usableTitle = false
	}
	if r.Confident && usableTitle && r.Title != svc.Title {
		updates["title"] = truncateStr(r.Title, 300)
		updates["title_source"] = "ai"
		if strings.TrimSpace(svc.Title) != "" {
			updates["prev_title"] = svc.Title // 供「新变更」标签
		}
		changed = true
	}
	if r.Confident && strings.TrimSpace(r.Product) != "" && r.Product != svc.Fingerprint {
		updates["fingerprint"] = truncateStr(r.Product, 300)
		if strings.TrimSpace(svc.Fingerprint) != "" {
			updates["prev_fingerprint"] = svc.Fingerprint
		}
		changed = true
	}
	config.DB.Model(&model.Service{}).Where("id = ?", svc.ID).Updates(updates)
	return changed
}

// recordIdentConversation 一次识别 = 一条会话（v0.0.47 起）。
// 完整请求响应包放进 meta（前端折叠成按钮 + 四栏弹窗），不铺满对话。
func recordIdentConversation(taskID uint, svc model.Service, r IdentResult, changed bool) {
	conv := newRecordConv(taskID, "svc_ident", recordTitle(svc, r))
	if conv == nil {
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("目标：%s:%d（%s）\n初始标题：%s\n\n识别链路：\n",
		svc.IP, svc.Port, svc.Protocol, orNone(svc.Title)))
	stages := r.Stages
	hops := r.Hops
	for i, st := range stages {
		sb.WriteString(fmt.Sprintf("%d) %s：%s\n   输入：%s\n", i+1, st.Stage,
			map[bool]string{true: "✅ 命中", false: "✗ 未命中"}[st.OK], oneLine(st.Input)))
		if st.Output != "" {
			sb.WriteString(fmt.Sprintf("   输出：%s\n", oneLine(st.Output)))
		}
	}
	sb.WriteString(fmt.Sprintf("\n结论：%s（置信=%v · %s · %s）",
		orNone(r.Title),
		r.Confident,
		map[bool]string{true: "已写回服务", false: "未达写回门槛，保持原值"}[changed],
		orNone(r.Reason)))

	// 完整请求/响应直接落在消息正文里：这是识别结论的依据，
	// 只给阶段摘要等于让人（和后续复核）无法自己判断。前端折叠展示，不占屏。
	meta := ""
	if len(hops) > 0 {
		sb.WriteString("\n\n========== 原始请求 / 响应（完整）==========\n")
		sb.WriteString(scanner.TraceRaw(hops))
		if b, e := json.Marshal(map[string]interface{}{
			"kind":     "http_trace",
			"target":   fmt.Sprintf("%s:%d", svc.IP, svc.Port),
			"protocol": svc.Protocol,
			"hops":     hops,
		}); e == nil {
			meta = string(b)
		}
	}
	config.DB.Create(&model.LLMMessage{ConversationID: conv.ID, Role: "user", Content: sb.String(), Meta: meta})
	config.DB.Create(&model.LLMMessage{ConversationID: conv.ID, Role: "assistant", Content: orNone(r.Title)})
	config.DB.Model(&model.LLMConversation{}).Where("id = ?", conv.ID).
		Update("message_count", 2)

	// 只有真走了浏览器渲染的才额外记一条「渲染复核」
	if renderedSucceeded(r.Stages) {
		recordRenderReview(taskID, svc, r, changed)
	}
}

// RecordIdentConversation 供手动识别复用
func RecordIdentConversation(taskID uint, svc model.Service, r IdentResult, changed bool) {
	recordIdentConversation(taskID, svc, r, changed)
}

// renderedSucceeded 渲染阶段是否真的拿到了页面
func renderedSucceeded(stages []IdentStage) bool {
	for _, st := range stages {
		if st.Stage == "浏览器渲染" && st.OK {
			return true
		}
	}
	return false
}

// recordRenderReview 写一条渲染复核记录（同样是一次识别一条会话）
func recordRenderReview(taskID uint, svc model.Service, r IdentResult, changed bool) {
	conv := newRecordConv(taskID, "render", recordTitle(svc, r))
	if conv == nil {
		return
	}
	var title, verdict, reason string
	for _, st := range r.Stages {
		switch st.Stage {
		case "浏览器渲染":
			title = oneLine(st.Output)
		case "AI·渲染结果":
			verdict = oneLine(st.Output)
		}
	}
	reason = r.Reason
	body := fmt.Sprintf("目标：%s:%d（%s）\n渲染结果：%s\nAI 判定：%s\n依据：%s\n结论：%s（置信=%v）",
		svc.IP, svc.Port, svc.Protocol, orNone(title), orNone(verdict), orNone(reason),
		orNone(r.Title), r.Confident)
	if len(r.Stages) > 0 {
		body += "\n\n========== 渲染链路明细 ==========\n"
		for _, st := range r.Stages {
			body += fmt.Sprintf("\n[%s]\n输入：%s\n输出：%s\n", st.Stage, st.Input, st.Output)
		}
	}
	config.DB.Create(&model.LLMMessage{ConversationID: conv.ID, Role: "user", Content: body})
	config.DB.Create(&model.LLMMessage{ConversationID: conv.ID, Role: "assistant", Content: orNone(r.Title)})
	config.DB.Model(&model.LLMConversation{}).Where("id = ?", conv.ID).
		Update("message_count", 2)
}

// recordTitle 会话标题：ip:port · 结论（够在列表/标题栏分辨是哪一条）
func recordTitle(svc model.Service, r IdentResult) string {
	t := fmt.Sprintf("%s:%d", svc.IP, svc.Port)
	if s := strings.TrimSpace(r.Title); s != "" {
		t += " · " + truncateStr(s, 40)
	}
	return t
}

// newRecordConv 每次识别新建一条会话，不做"任务级聚合"
func newRecordConv(taskID uint, kind, title string) *model.LLMConversation {
	if config.DB == nil || taskID == 0 {
		return nil
	}
	if kind == "" {
		kind = "svc_ident"
	}
	conv := model.LLMConversation{Title: title, TaskID: taskID, Kind: kind}
	if err := config.DB.Create(&conv).Error; err != nil {
		return nil
	}
	return &conv
}

func buildURL(svc model.Service) string {
	proto := strings.ToLower(svc.Protocol)
	switch {
	case strings.HasPrefix(proto, "https"):
		return fmt.Sprintf("https://%s:%d/", svc.IP, svc.Port)
	case strings.HasPrefix(proto, "http"):
		return fmt.Sprintf("http://%s:%d/", svc.IP, svc.Port)
	}
	return ""
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（空）"
	}
	return s
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " / ")
	if len([]rune(s)) > 300 {
		s = string([]rune(s)[:300]) + "…"
	}
	return s
}

// extractJSON 从模型输出里抠出第一个 JSON 对象
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}
