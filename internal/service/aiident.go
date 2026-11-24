package service

import (
	"context"
	"errors"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

const (
	// aiBatchSize 一次模型调用最多处理多少条目标。
	//
	// 实测（deepseek-v4.1-flash，真实证据每条约 3KB）：
	//   1 条 9.2s · 2 条 9.7s · 4 条 13.7s · 8 条 18.9s（极简证据探测）
	//   真实证据下 4 条约 20s，单条等效成本与逐条持平——网关是按输入 token
	//   线性计时的，"省冷启动"省下来的那几秒被输入膨胀吃掉了。
	// 所以默认 2：既能省掉一半冷启动，又不会把网关压到排队。
	// 可用 AI_BATCH_SIZE 调（1 = 完全逐条）。
	aiBatchSizeDefault = 2
)

// aiInflight 当前在处理的批次数，上限由 aiParallel() 每次实时读取（设置页可改）。
var aiInflight atomic.Int64

// aiThinkUnsupported 网关不认 chat_template_kwargs 时置位，之后不再携带。
//
// 关闭思考链的依据（实测 2026-11-23，deepseek-v4.1-flash @ provider-b，同一份渲染证据）：
//   开思考             21784ms  out=386  结论：海康威视综合安防管理平台
//   reasoning_effort=low 10885ms out=192 结论：Hikvision Web Client
//   enable_thinking=false 5402ms out=118 结论：Hikvision（海康威视）设备/平台登录页
// 结论一致，延迟降到 1/4。识别是"读证据→填字段"，思考链不产生额外信息，
// 只产生 token 和时间。所以识别链路默认关思考；网关不支持时自动退回默认行为。
var aiThinkUnsupported atomic.Bool

// identChatExtra 关闭思考链的模板参数；网关不支持过一次后返回 nil。
func identChatExtra() map[string]interface{} {
	if aiThinkUnsupported.Load() {
		return nil
	}
	return map[string]interface{}{"enable_thinking": false}
}

// aiBatchSize 一次模型调用处理的目标数，可用 AI_BATCH_SIZE 覆盖（1~16）。
func aiBatchSize() int {
	if v := strings.TrimSpace(os.Getenv("AI_BATCH_SIZE")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 16 {
			return n
		}
	}
	return aiBatchSizeDefault
}

// 写回门槛：fillBar 填空（不会覆盖任何东西），overwriteBar 覆盖已有有效标题。
const (
	// fillBar 填空门槛压到 15：填空不会覆盖任何既有信息，最坏情况是填进一个
	// 技术栈名（Microsoft IIS / Kubernetes），也比列表上一片空白有信息量。
	// overwriteBar 保持 60：已有有效标题时，只有模型足够确定才允许改写。
	fillBar        = 15
	overwriteBar   = 60
	fingerprintBar = 15
)

// confidentEnough 第一轮（只看请求响应包）要多少分才直接收工。
// 低于这个分数说明只判到了厂商/类别一级，继续走浏览器渲染拿页面文字。
// 实测依据：62 分给出的「深信服 SSL VPN」渲染后是「深信服超融合平台 SANGFOR HCI」。
const confidentEnough = 80

// aiIdentifyTimeout 单条识别的硬上限。正常一条 5-15 秒，走渲染最多 30 秒；
// 超过这个值基本可以认定 CDP 卡死，销毁浏览器实例收敛。
const aiIdentifyTimeout = 150 * time.Second

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

// aiRetryDelay 网关失败后的重试间隔。
//
// 不设退避会死循环：失败 → ai_need 保持 true → 15 秒后 feeder 又补进来 →
// 仍失败。5 分钟足够让网关的排队自愈，也避免把不健康的后端持续打满。
const aiRetryDelay = 5 * time.Minute

// aiRetryAfter 失败条目的下次可重试时间（进程内，重启即清空）。
var aiRetryAfter sync.Map

// aiQueueFeeder 每 15 秒从库里补一批待识别（仅 HTTP(S)），队列过半就歇着。
//
// 队列持久化的取舍：不引入 Redis/表锁，直接以 services.ai_need 作为"待办标记"，
// 内存队列只是加速通道。重启后 inFlight 清空，同一批会在下一轮补给里重新入队，
// 语义安全（识别是幂等的：重复跑最多多花几秒，不会写错数据）。
func aiQueueFeeder() {
	// 只负责补给。识别进度不再写进日志终端——它是状态，不是事件：
	// 写日志会每 30 秒刷一行、把真正的扫描输出顶掉，而且用户不容易在滚动
	// 的终端里读到。现在由前端在任务日志弹窗里单独画一条横条，
	// 数据来自 /api/services/ai-stats（backlog / pendingRender / rendering / inLLM / etaSeconds）。
	tk := time.NewTicker(5 * time.Second)
	defer tk.Stop()
	for range tk.C {
		if aiQueue != nil && len(aiQueue) <= aiIdentifyQueueSize/2 {
			fillQueueFromDB(200)
		}
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
		if v, ok := aiRetryAfter.Load(key); ok {
			if until, ok := v.(time.Time); ok && time.Now().Before(until) {
				continue // 网关刚失败过，等退避到点再试
			}
			aiRetryAfter.Delete(key)
		}
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

// StartAIWorkers 服务启动时拉起识别工作池与补给协程。
// 没有可用 LLM 配置时不做任何事（后续 Enqueue 仍会尝试启动）。
func StartAIWorkers() {
	if len(llm.AvailableProfiles()) == 0 {
		return
	}
	aiQueueOnce.Do(startAIWorkers)
}

func startAIWorkers() {
	aiQueue = make(chan aiIdentifyJob, aiIdentifyQueueSize)
	// 补给协程：队列不再只靠"扫描时逐个 Enqueue"，而是持续从库里 ai_need 的
	// HTTP(S) 条目补齐。这样进程重启不会丢掉"还没识别"的那批（内存队列本来是空的），
	// 用户点一次重识别也只是"把库里标出来"，不需要一次塞进内存。
	go aiQueueFeeder()
	// 派发协程固定 4 个：实际并发由 aiParallel() 动态控制（设置页可改），
	// 不把 worker 数量写死在启动参数里——改完设置不该要求重启进程。
	n := aiParallel()
	log.Printf("[ai-ident] 识别工作池启动：并发 %d（可在 LLM 配置页调整），批量 %d 条/请求，本机 %d 核",
		n, aiBatchSize(), runtime.NumCPU())
	for i := 0; i < 4; i++ {
		go aiDispatchLoop()
	}
}

// aiDispatchLoop 取一条任务，再凑够一批，交给批量识别。
//
// v0.0.56：识别从"一条一次模型调用"改成"一批一次"。每次调用都是一次冷启动
// （新会话、无缓存、首 token 1~3 秒），45 条串起来光首 token 就一百多秒；
// 8 条放进同一个请求，首 token 只付一次。
func aiDispatchLoop() {
	for {
		job, ok := <-aiQueue
		if !ok {
			return
		}
		for !aiTryAcquire() {
			// 并发已满：等一下再抢。50ms 粒度足够，也不会空转烧 CPU。
			if d := identCPUGate(); d > 0 {
				_ = d
			}
			time.Sleep(50 * time.Millisecond)
		}
		batch := []aiIdentifyJob{job}
	collect:
		for len(batch) < aiBatchSize() {
			select {
			case j, ok := <-aiQueue:
				if !ok {
					break collect
				}
				batch = append(batch, j)
			default:
				break collect
			}
		}
		go func(b []aiIdentifyJob) {
			defer aiRelease()
			runAIIdentifyBatchSafe(b)
		}(batch)
	}
}

// aiTryAcquire 抢一个并发名额；并发上限每次实时读设置，改设置立即生效。
func aiTryAcquire() bool {
	limit := int64(aiParallel())
	for {
		cur := aiInflight.Load()
		if cur >= limit {
			return false
		}
		if aiInflight.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

func aiRelease() { aiInflight.Add(-1) }

// aiParallel 识别并发数：DB 设置 > 环境变量 > 3。
func aiParallel() int {
	if config.DB != nil {
		var s model.AISettings
		if err := config.DB.Select("parallel").First(&s, 1).Error; err == nil && s.Parallel > 0 {
			return clampParallel(s.Parallel)
		}
	}
	if v := strings.TrimSpace(os.Getenv("AI_IDENT_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return clampParallel(n)
		}
	}
	return 3
}

// AIParallel 对外暴露当前识别并发（设置页读）
func AIParallel() int { return aiParallel() }

// SetAIParallel 保存识别并发（1~16），下一条任务立即按新值限流。
func SetAIParallel(n int) (int, error) {
	if n < 1 || n > 16 {
		return 0, fmt.Errorf("并发数必须在 1~16 之间")
	}
	var s model.AISettings
	if err := config.DB.First(&s, 1).Error; err != nil {
		return 0, err
	}
	if err := config.DB.Model(&model.AISettings{}).Where("id = ?", 1).Update("parallel", n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

func clampParallel(n int) int {
	if n < 1 {
		return 1
	}
	if n > 16 {
		return 16
	}
	return n
}

// runAIIdentifyBatchSafe 一批识别的兜底：panic 不杀进程，超时强制收敛。
//
// 看门狗按"批"算而不是按"条"：批内多条目标共用一次模型调用，按条计时会把
// 正常的长批误杀。超时后销毁浏览器并 cancel，整批按失败落库（下次补给会重试）。
func runAIIdentifyBatchSafe(jobs []aiIdentifyJob) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[ai-ident] 批量识别 panic（%d 条）: %v", len(jobs), r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), aiIdentifyTimeout)
	defer cancel()
	timer := time.AfterFunc(aiIdentifyTimeout, func() {
		log.Printf("[ai-ident] 批量识别（%d 条）超过 %s 未完成，取消并销毁浏览器实例",
			len(jobs), aiIdentifyTimeout)
		scanner.ResetBrowser()
	})
	defer timer.Stop()

	svcs := make([]model.Service, 0, len(jobs))
	for _, j := range jobs {
		var svc model.Service
		if err := config.DB.Where("ip = ? AND port = ?", j.IP, j.Port).First(&svc).Error; err != nil {
			aiInFlight.Delete(j.IP + ":" + strconv.Itoa(j.Port))
			continue
		}
		svcs = append(svcs, svc)
	}
	if len(svcs) == 0 {
		return
	}
	t0 := time.Now()
	results := IdentifyBatchCtx(ctx, svcs)
	// 统计分摊：整批耗时按条数均摊。不记账的话 ai-stats 的 doneTotal/avgTotalMs
	// 永远是 0，界面上的 ETA 会一直显示"估算中"。
	per := time.Since(t0) / time.Duration(len(svcs))
	for i, svc := range svcs {
		r := results[i]
		aiMetricNote(r, per)
		changed := ApplyIdentResult(svc, r)
		recordIdentConversation(svc.LastTaskID, svc, r, changed)
		aiInFlight.Delete(svc.IP + ":" + strconv.Itoa(svc.Port))
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
	Confidence int    // 模型自评置信（0-100），写回门槛见 ApplyIdentResult
	Confident  bool   // 模型给出名字且置信度达标（>=60）
	Sufficient bool   // 模型认为"现有证据已经足够判断，只是给不出确切名字"
	Reason     string // 模型给的依据（20 字以内）
	// Source 标题从哪来：html（原始 <title> 直接采纳）/ render（渲染后 document.title）
	// / ai（模型判定）。写回时落到 services.title_source，便于事后区分置信层级。
	Source string
	// Failed 链路没跑完（网关超时/取消），结论不可信：不落"待分析"，保留待办重试。
	Failed bool
	Stages []IdentStage
	Hops   []scanner.Hop
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

// renderWorthwhile 唯一的本地渲染闸门：只拦"物理上拿不到更多信息"的情况。
//
// v0.0.56：这里只剩一条判据——**响应根本不是 HTML**（JSON / 纯文本 / 图片 / 二进制）。
// 这是物理事实：浏览器打开 JSON 拿到的还是那段 JSON。除了它，要不要渲染完全由
// 识别 agent 的 need_render 决定。
//
// 之前这里的 401/403 正文 <1500 字节、4xx/5xx 一律不渲染等分支都删掉了：
// 那些阈值是围着上一批内网站点调出来的，换环境就不成立（同一段代码在不同环境
// 会得出相反结论），属于必须清除的特调。判断权归模型，规则只管物理边界。
func renderWorthwhile(hops []scanner.Hop) (bool, string) {
	if len(hops) == 0 {
		return false, "无响应"
	}
	last := hops[len(hops)-1]
	if hopStatus(last.ResponseLine) == 0 {
		return false, "没有可解析的 HTTP 状态行"
	}
	hdr := strings.ToLower(last.ResponseHeaders)
	bodyLow := strings.ToLower(last.Body)
	looksHTML := strings.Contains(hdr, "text/html") ||
		strings.Contains(bodyLow, "<html") || strings.Contains(bodyLow, "<!doctype html")
	if looksHTML {
		return true, ""
	}
	// 有些服务不写 Content-Type，只返回一段 HTML 片段或一个空 body + 脚本。
	// 这两种情况下浏览器仍可能渲染出内容（典型：Vue/React 的 index.html）。
	if hasScriptBundle(last.Body) || strings.TrimSpace(last.Body) == "" && strings.Contains(hdr, "text/") {
		return true, ""
	}
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

// chromeErrorMarkers 浏览器内置错误页的特征文本。
//
// 这些文字不是目标站点返回的内容，而是 chromium 自己生成的：DNS 解析失败、
// 连接被拒、重定向环、协议错配。拿它当证据会把主机名（sctst03、scecc03a）、
// "webserver" 这种词当成系统名——实测 45 条样本里就踩了 5 次。
var chromeErrorMarkers = []string{
	"this page can’t be found", "this page can't be found",
	"this site can’t be reached", "this site can't be reached",
	"this page isn’t working", "this page isn't working",
	"refused to connect", "redirected you too many times",
	"client sent an http request to an https server",
	"you don’t have authorization to view this page",
	"you don't have authorization to view this page",
	"dns_probe", "err_connection", "err_empty_response",
}

// hasScriptBundle 响应体是否引用了外部脚本（典型 SPA 骨架的特征）。
//
// 为什么单独看这个：Vue/React 构建出的 index.html 正文几乎是空的（只有一个
// `<div id="app">`），所有可见内容都在 bundle 里。这类页面 title 常常有，但
// 是构建时的默认值（"Vue App"、"Webpack App"），必须交给渲染兜底。
func hasScriptBundle(body string) bool {
	low := strings.ToLower(body)
	return strings.Contains(low, "<script") && strings.Contains(low, "src=")
}

// titleNoiseWord 整条标题（或分割后的某一段）就是这些词时，不含任何业务身份。
//
// 与 scanner.PlaceholderTitle 的分工：那边只认"结构性占位"（状态码文本、跳转壳、
// 服务器名），这里补的是**中文登录壳与导航词**——`登录`、`首页`、`控制台`。
// 混在一条规则里会让 scanner 包承担语义判断，所以放在识别侧。
var titleNoiseWord = map[string]bool{
	"登录": true, "登陆": true, "登录页": true, "用户登录": true, "统一登录": true,
	"首页": true, "主页": true, "欢迎": true, "欢迎页": true, "欢迎使用": true,
	"后台": true, "管理后台": true, "控制台": true, "管理控制台": true,
	"系统": true, "管理系统": true, "平台": true, "门户": true, "门户首页": true,
	"login": true, "log in": true, "sign in": true, "signin": true,
	"home": true, "home page": true, "homepage": true, "welcome": true,
	"index": true, "index page": true, "portal": true, "console": true,
	"dashboard": true, "admin": true, "admin console": true, "system": true,
	"default": true, "untitled": true, "document": true, "web app": true,
}

func normNoise(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// titleIsAllNoise 整条标题就是登录壳/导航词。
func titleIsAllNoise(title string) bool {
	return titleNoiseWord[normNoise(title)]
}

// titleHasNoisySegment 标题里有分隔符切开后的一段是登录壳/导航词。
//
// 命中说明这是「系统名 | 首页」「示例平台 - 登录」这类长标题：
// 名字本身有信息量，但需要截掉后缀，交给识别 agent 归整比规则硬切更稳。
func titleHasNoisySegment(title string) bool {
	segs := strings.FieldsFunc(title, func(r rune) bool {
		switch r {
		case '-', '–', '—', '－', '|', '｜', '_', '·', ':', '：', '/':
			return true
		}
		return false
	})
	if len(segs) < 2 {
		return false
	}
	for _, s := range segs {
		if titleNoiseWord[normNoise(s)] {
			return true
		}
	}
	return false
}

// stripTags 粗略去掉标签与脚本，用来估算可见正文长度
func stripTags(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	depth := 0
	inScript := false
	low := strings.ToLower(s)
	for i := 0; i < len(s); i++ {
		if s[i] == '<' {
			rest := low[i:]
			if strings.HasPrefix(rest, "<script") || strings.HasPrefix(rest, "<style") {
				inScript = true
			}
			depth++
			continue
		}
		if s[i] == '>' {
			rest := low[i:]
			if inScript && (strings.Contains(rest, "</script") || strings.Contains(rest, "</style")) {
				inScript = false
			}
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 && !inScript {
			b.WriteByte(s[i])
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// renderLooksBroken 判断渲染结果是不是浏览器错误页。
func renderLooksBroken(title, text string) bool {
	low := strings.ToLower(title + " " + text)
	for _, m := range chromeErrorMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// traceDual 两个协议各试一次，取"能给出有效响应"的那个。
//
// v0.0.56：不再猜协议。旧做法信任引擎入库的 protocol，判错时靠 400 响应里的
// 文本特征回退——那套逻辑本身就是"针对已知错法"的特调，换一种错法就失效：
// 实测 198.18.20.124:8083 入库 http，首跳 302 跳到 https:9091，抓包"成功"了，
// 回退没触发，渲染拿到的是 `400 The plain HTTP request was sent to HTTPS port`。
//
// 现在按结果选，不按声明选：先抓入库协议，拿不到有效响应就抓另一个。
// "有效"只有两条：有可解析的状态行；响应不是协议栈的拒绝应答。
func traceDual(ip string, port int, prefer string) ([]scanner.Hop, error, string) {
	if prefer != "https" {
		prefer = "http"
	}
	alt := "https"
	if prefer == "https" {
		alt = "http"
	}
	if h1, e1 := scanner.HTTPTrace(ip, port, prefer, 4); e1 == nil && traceUsable(h1) {
		return h1, nil, prefer
	}
	if h2, e2 := scanner.HTTPTrace(ip, port, alt, 4); e2 == nil && traceUsable(h2) {
		return h2, nil, alt
	}
	// 两个都不可用时，把首选协议抓到的原始结果交回去：里面至少还有状态行和
	// 响应头，模型能据此判断"这端口是个 https 栈 / 是个拒绝应答"。
	h1, e1 := scanner.HTTPTrace(ip, port, prefer, 4)
	if len(h1) > 0 {
		return h1, nil, prefer
	}
	return nil, e1, prefer
}

// traceUsable 这次抓包是否给出了"有信息量"的响应。
//
// 唯一排除的是协议栈的拒绝应答：用 http 连 https 端口（或反过来）时，服务器
// 不回连接错误，而是正常回 400 + 一段 "plain HTTP request was sent to HTTPS port"。
// 这不是站点内容，是协议栈在说"你走错门了"——把它当证据只会让模型把
// Kubernetes 认成 nginx（响应头里确实有 Server: nginx）。
//
// 这是 HTTP 语义，不是某个环境的特调：任何服务器被错协议访问时都这么回。
func traceUsable(hops []scanner.Hop) bool {
	if len(hops) == 0 {
		return false
	}
	return !isProtocolReject(hops[len(hops)-1])
}

func isProtocolReject(last scanner.Hop) bool {
	if hopStatus(last.ResponseLine) != 400 {
		return false
	}
	if len(last.Body) > 2048 {
		return false // 正文这么长的 400 是真错误页，不是协议栈应答
	}
	low := strings.ToLower(last.Body)
	return strings.Contains(low, "http request to an https server") ||
		strings.Contains(low, "plain http request was sent") ||
		strings.Contains(low, "https port")
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
	hops, trErr, scheme = traceDual(svc.IP, svc.Port, scheme)
	mark("trace", tTrace)
	switch {
	case trErr != nil:
		stages = append(stages, IdentStage{Stage: "抓取请求响应包", Input: baseURL, Output: "抓取失败：" + trErr.Error()})
		return done()
	case len(hops) == 0:
		stages = append(stages, IdentStage{Stage: "抓取请求响应包", Input: baseURL, Output: "无响应"})
		return done()
	}
	// 协议判错时抓包已切到另一个 scheme，渲染必须跟着走——
	// 否则 6443 / 10250 这类 HTTPS 端口会被渲染成
	// "Client sent an HTTP request to an HTTPS server" 的错误页。
	baseURL = fmt.Sprintf("%s://%s:%d/", scheme, svc.IP, svc.Port)

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

	// 第 2 步：L0 规则判定——HTML 自己声明的标题能不能直接用。
	//
	// v0.0.55：以前识别链完全不看 <title>，把原始字节整个交给模型让它自己读标题。
	// 等于把"这页有没有明确身份"这件机械判断交给了最贵的一环：判断 agent + 渲染
	// + 识别 agent 三笔开销，全花在一条已经写着"数据智能平台"的响应上。
	// 现在先解析 title，分三档：
	//   - 明确且无导航后缀 → 直接采纳，0 次模型调用、0 次渲染；
	//   - 明确但带「 | 首页」「- 登录」这类后缀 → 跳过判断与渲染，只让识别 agent 归整；
	//   - 缺失/占位/错误页 → 走原链路（判断 → 渲染 → 识别）。
	lastHop := hops[len(hops)-1]
	htmlTitle := scanner.HTMLTitle(lastHop.Body)
	stCode := hopStatus(lastHop.ResponseLine)
	titleUsable := stCode >= 200 && stCode < 300 &&
		TitleClear(htmlTitle) && !scanner.PlaceholderTitle(htmlTitle, "") &&
		!titleIsAllNoise(htmlTitle)
	titleClean := titleUsable && !titleHasNoisySegment(htmlTitle)

	if titleClean {
		res.Title = htmlTitle
		res.Confidence = 100
		res.Confident = true
		res.Sufficient = true
		res.Source = "html"
		res.Reason = "HTML <title> 直接可用"
		stages = append(stages, IdentStage{
			Stage:  "规则·HTML 标题",
			Input:  truncateStr(htmlTitle, 60),
			Output: "标题明确，跳过判断/渲染/识别（0 次模型调用）",
			OK:     true,
		})
		return done()
	}
	if titleUsable {
		stages = append(stages, IdentStage{
			Stage:  "规则·HTML 标题",
			Input:  truncateStr(htmlTitle, 60),
			Output: "标题可用但带导航后缀，交给识别 agent 归整（不渲染）",
			OK:     true,
		})
	}

	// 第 3 步：识别 agent（一次往返，自带"要不要渲染"的判断）。
	//
	// v0.0.56：不再单独调判断 agent。判断需要的信息（页面长什么样）和识别需要的
	// 信息（页面上写了什么）来自同一份证据，拆成两次往返等于把同一份证据读两遍，
	// 而且每次往返都是一次冷启动（首 token 1~3 秒）。现在识别 agent 一次输出
	// 名字 + need_render：证据够就直接定论，不够就在同一个回复里要求渲染，
	// 链路据此决定下一步，没有第二次独立往返。
	brief := scanner.TraceDigestBrief(hops)
	if titleUsable {
		// 带导航后缀的标题单独点出来：模型要做的只是截掉后缀，不需要重新读整页。
		brief = "原始 <title>: " + htmlTitle + "\n" + brief
	}
	// 引擎规则集对同一份响应的静态命中：零成本、确定性，作为线索放在证据最前面。
	// 只做参考不做定论——实测有 spring_cloud_config 这种误报。
	if len(hops) > 0 {
		last := hops[len(hops)-1]
		if fp := scanner.FingerprintHits(last.ResponseHeaders, last.Body, htmlTitle); len(fp) > 0 {
			brief = "引擎指纹（规则库静态匹配，可能有误报）：" + strings.Join(fp, ", ") + "\n" + brief
		}
	}
	tId := time.Now()
	iv := askIdentify(ctx, brief, "请求响应包", false)
	mark("identify", tId)
	stages = append(stages, IdentStage{
		Stage: "AI·识别·抓包证据", Input: truncateStr(brief, 120),
		Output: orNone(iv.Raw), OK: iv.dotConfident(),
	})
	// 只在"调用成功但返回空对象"时重试一次。判据用 Raw：调用失败时 Raw 是
	// "模型调用失败"，空内容时 Raw 为空串。超时不重试——网关不健康时重试会把
	// 15 秒翻倍成 30 秒，worker 被长期占住、整队吞吐塌掉。
	if strings.TrimSpace(iv.Raw) == "" && !iv.hasName() && ctx.Err() == nil {
		if retry := askIdentify(ctx, brief+"\n\n注意：title 和 product 不能都为空。", "请求响应包", false); retry.hasName() {
			iv = retry
			stages = append(stages, IdentStage{Stage: "AI·识别·空结果重试", Output: orNone(retry.Raw), OK: retry.dotConfident()})
		}
	}

	// 第 4 步：渲染（只在两个条件同时成立时：识别 agent 要求渲染，且物理上不是白页）
	//
	// 本地只剩"物理事实"闸门：非 HTML 且无脚本（JSON / 图片 / 二进制），渲染只会
	// 拿到同样的字节。其余一律听模型的——这里是判断权从规则搬回模型的唯一改动点。
	pageTitle, rendered := "", ""
	localOK, localWhy := renderWorthwhile(hops)
	// 渲染条件：模型自己要求（need_render）或它明确说"在猜"（confidence < 40）。
	// 后者是通用策略而非阈值特调——提示词已定义 <30 是靠猜、30~59 只到类别，
	// 这类结论值得再花一次浏览器把页面打开。
	// 实测代价：不这样做时，`Browser Error`（真名 HDM）、`Login`（深信海超融合）
	// 这类"HTML 里写着占位词、真名由 JS 写入"的页面全部落成空标题。
	needRender := !iv.dotConfident() && (iv.NeedRender || iv.conf() < 40)
	renderWhy := ""
	switch {
	case iv.Failed:
		needRender, renderWhy = false, "识别调用未完成（网关未返回），跳过渲染，保留重试"
	case !needRender:
		renderWhy = "识别 agent 未要求渲染"
		if iv.dotConfident() {
			renderWhy = "已有高置信结论，无需渲染"
		}
	case !localOK:
		needRender, renderWhy = false, "本地硬闸门："+localWhy
		aiMetricRenderSkipped()
	}

	if needRender && !scanner.ChromeAvailable() {
		stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: "容器内无 chromium，跳过"})
		needRender = false
	}
	if needRender {
		tRender := time.Now()
		var pageDesc, pageText string
		var rerr error
		pageTitle, pageDesc, pageText, rerr = scanner.RenderPageCtx(ctx, baseURL, 6*time.Second)
		mark("render", tRender)
		switch {
		case rerr != nil && ctx.Err() != nil:
			stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: "被中止：" + rerr.Error()})
			return done()
		case rerr != nil:
			stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: "渲染失败：" + rerr.Error()})
		case renderLooksBroken(pageTitle, pageText):
			// 浏览器错误页不是目标内容：不能拿它当证据，也不能把它当候选标题
			pageTitle = ""
			stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL,
				Output: "渲染得到浏览器错误页（DNS/连接/重定向失败），不作为证据，退回抓包结果"})
		default:
			rendered = fmt.Sprintf("页面标题: %s\nmeta描述: %s\n正文摘要: %s",
				orNone(pageTitle), orNone(pageDesc), orNone(pageText))
			stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: rendered,
				OK: strings.TrimSpace(pageTitle) != ""})
		}
	} else {
		stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: "跳过：" + renderWhy})
	}

	// 第 5 步：渲染后再识别一次（仍是一次往返）。渲染结果与抓包证据是两份不同材料，
	// 必须让模型重看一遍；这一步不能省，也不能和上一步合并——合并就要把浏览器
	// 的等待塞进同一次模型调用里，网关超时会同时废掉两条。
	if rendered != "" {
		tId2 := time.Now()
		iv2 := askIdentify(ctx, rendered, "渲染结果", true)
		mark("identify_render", tId2)
		stages = append(stages, IdentStage{
			Stage: "AI·识别·渲染证据", Input: truncateStr(rendered, 120),
			Output: orNone(iv2.Raw), OK: iv2.dotConfident(),
		})
		switch {
		case iv2.hasName():
			iv = iv2
		case iv2.Failed && !iv.dotConfident():
			// 同批量路径：渲染证据没答出来 + 上一轮是猜，则整条判失败重试，
			// 不把猜测写进库。
			iv.Failed = true
		}
	}

	switch {
	case iv.hasName():
		res.apply(iv)
	case strings.TrimSpace(pageTitle) != "" && TitleClear(pageTitle) &&
		!scanner.PlaceholderTitle(pageTitle, "") && !titleIsAllNoise(pageTitle):
		// 模型没给出名字，但渲染后的标题本身是明确文字：作为低置信候选带上。
		res.Title = strings.TrimSpace(pageTitle)
		res.Source = "render"
		res.Reason = "渲染标题（未经模型确认）"
	case strings.TrimSpace(iv.Reason) != "":
		res.Reason = iv.Reason
	}
	return done()
}

// ===== 批量识别（v0.0.56） =====
//
// 队列消费从"一条一次模型调用"改成"一批一次"。单条调用里真正贵的是冷启动
// （新会话、首 token 1~3 秒），证据本身只有几百 token；8 条共用一次冷启动，
// 总耗时接近 1 条。下方 IdentifyBatchCtx 是批量版链路，与 IdentifyServiceCtx
// 语义一致：抓包 → L0 标题直取 → 识别（批量）→ 渲染（按需）→ 复识别（批量）。

type identPrep struct {
	svc         model.Service
	hops        []scanner.Hop
	baseURL     string
	htmlTitle   string
	titleUsable bool
	settled     bool
	res         IdentResult
	stages      []IdentStage
	timing      map[string]int64
}

// identPrepare 抓包 + L0 判定。能定论的（标题直取 / 抓包失败）直接置 settled。
func identPrepare(ctx context.Context, svc model.Service) identPrep {
	p := identPrep{svc: svc, timing: map[string]int64{}}
	p.res = IdentResult{
		Title: strings.TrimSpace(svc.Title), Product: strings.TrimSpace(svc.Fingerprint),
		Timing: p.timing,
	}
	baseURL := buildURL(svc)
	if baseURL == "" {
		p.settled = true
		p.stages = append(p.stages, IdentStage{Stage: "抓取请求响应包", Output: "非 http(s) 协议，不适用 Web 识别"})
		return p
	}
	scheme := "http"
	if strings.HasPrefix(strings.ToLower(svc.Protocol), "https") {
		scheme = "https"
	}
	t0 := time.Now()
	hops, err, scheme := traceDual(svc.IP, svc.Port, scheme)
	p.timing["trace"] = time.Since(t0).Milliseconds()
	if err != nil || len(hops) == 0 {
		p.settled = true
		msg := "无响应"
		if err != nil {
			msg = "抓取失败：" + err.Error()
		}
		p.stages = append(p.stages, IdentStage{Stage: "抓取请求响应包", Input: baseURL, Output: msg})
		return p
	}
	p.hops = hops
	p.baseURL = fmt.Sprintf("%s://%s:%d/", scheme, svc.IP, svc.Port)
	last := hops[len(hops)-1]
	p.htmlTitle = scanner.HTMLTitle(last.Body)
	st := hopStatus(last.ResponseLine)
	p.titleUsable = st >= 200 && st < 300 &&
		TitleClear(p.htmlTitle) && !scanner.PlaceholderTitle(p.htmlTitle, "") &&
		!titleIsAllNoise(p.htmlTitle)
	p.stages = append(p.stages, IdentStage{
		Stage: "抓取请求响应包", Input: fmt.Sprintf("%s（共 %d 跳）", p.baseURL, len(hops)),
		Output: last.ResponseLine, OK: true,
	})
	if p.titleUsable && !titleHasNoisySegment(p.htmlTitle) {
		p.res.Title = p.htmlTitle
		p.res.Confidence = 100
		p.res.Confident = true
		p.res.Sufficient = true
		p.res.Source = "html"
		p.res.Reason = "HTML <title> 直接可用"
		p.stages = append(p.stages, IdentStage{
			Stage: "规则·HTML 标题", Input: truncateStr(p.htmlTitle, 60),
			Output: "标题明确，跳过模型调用", OK: true,
		})
		p.settled = true
	}
	return p
}

// IdentifyBatchCtx 批量识别：同阶段的模型调用合并成一次请求。
func IdentifyBatchCtx(ctx context.Context, svcs []model.Service) []IdentResult {
	preps := make([]identPrep, len(svcs))
	runParallel(len(svcs), 4, func(i int) {
		preps[i] = identPrepare(ctx, svcs[i])
	})

	// ---- 第一次批量识别：抓包证据 ----
	var pending []int
	var evs []identEvidence
	for i := range preps {
		if preps[i].settled || ctx.Err() != nil {
			continue
		}
		body := scanner.TraceDigestBrief(preps[i].hops)
		if preps[i].titleUsable {
			body = "原始 <title>: " + preps[i].htmlTitle + "\n" + body
		}
		if h := preps[i].hops; len(h) > 0 {
			last := h[len(h)-1]
			if fp := scanner.FingerprintHits(last.ResponseHeaders, last.Body, preps[i].htmlTitle); len(fp) > 0 {
				body = "引擎指纹（规则库静态匹配，可能有误报）：" + strings.Join(fp, ", ") + "\n" + body
			}
		}
		pending = append(pending, i)
		evs = append(evs, identEvidence{
			Label:  fmt.Sprintf("%s:%d", preps[i].svc.IP, preps[i].svc.Port),
			Source: "请求响应包", Body: body,
		})
	}
	verdict := map[int]aiVerdict{}
	if len(evs) > 0 {
		aiMetricsRef.inLLM.Add(int64(len(evs)))
		t0 := time.Now()
		got := askIdentifyBatch(ctx, evs)
		aiMetricsRef.inLLM.Add(-int64(len(evs)))
		ms := time.Since(t0).Milliseconds()
		for k, i := range pending {
			verdict[i] = got[k]
			preps[i].timing["identify"] = ms // 批量共享：整批耗时记到每一条
			preps[i].stages = append(preps[i].stages, IdentStage{
				Stage: "AI·识别·批量", Input: fmt.Sprintf("第 %d/%d 条", k+1, len(pending)),
				Output: orNone(got[k].Raw), OK: got[k].dotConfident(),
			})
		}
	}

	// ---- 渲染（只处理识别 agent 明确要求、且物理上不是白页的）----
	var ridx []int
	for _, i := range pending {
		v := verdict[i]
		if v.dotConfident() || (!v.NeedRender && v.conf() >= 40) {
			continue
		}
		if ok, why := renderWorthwhile(preps[i].hops); !ok {
			preps[i].stages = append(preps[i].stages, IdentStage{
				Stage: "浏览器渲染", Input: preps[i].baseURL, Output: "跳过：本地硬闸门：" + why,
			})
			continue
		}
		ridx = append(ridx, i)
	}
	rendered := map[int]string{}
	pageTitle := map[int]string{}
	if len(ridx) > 0 && scanner.ChromeAvailable() {
		aiMetricsRef.pendingRender.Add(int64(len(ridx)))
		runParallel(len(ridx), 2, func(k int) {
			aiMetricsRef.pendingRender.Add(-1)
			aiMetricsRef.rendering.Add(1)
			defer aiMetricsRef.rendering.Add(-1)
			i := ridx[k]
			t0 := time.Now()
			title, desc, text, err := scanner.RenderPageCtx(ctx, preps[i].baseURL, 6*time.Second)
			preps[i].timing["render"] = time.Since(t0).Milliseconds()
			switch {
			case err != nil && ctx.Err() != nil:
				preps[i].stages = append(preps[i].stages, IdentStage{Stage: "浏览器渲染", Output: "被中止：" + err.Error()})
			case err != nil:
				preps[i].stages = append(preps[i].stages, IdentStage{Stage: "浏览器渲染", Output: "渲染失败：" + err.Error()})
			case renderLooksBroken(title, text):
				preps[i].stages = append(preps[i].stages, IdentStage{Stage: "浏览器渲染",
					Output: "渲染得到浏览器错误页，不作为证据"})
			default:
				pageTitle[i] = title
				rendered[i] = fmt.Sprintf("页面标题: %s\nmeta描述: %s\n正文摘要: %s",
					orNone(title), orNone(desc), orNone(text))
				preps[i].stages = append(preps[i].stages, IdentStage{Stage: "浏览器渲染",
					Input: preps[i].baseURL, Output: rendered[i], OK: strings.TrimSpace(title) != ""})
			}
		})
	} else if len(ridx) > 0 {
		for _, i := range ridx {
			preps[i].stages = append(preps[i].stages, IdentStage{Stage: "浏览器渲染", Output: "容器内无 chromium，跳过"})
		}
	}

	// ---- 第二次批量识别：渲染结果 ----
	var rpend []int
	var revs []identEvidence
	for _, i := range ridx {
		if rendered[i] == "" {
			continue
		}
		rpend = append(rpend, i)
		revs = append(revs, identEvidence{
			Label:  fmt.Sprintf("%s:%d", preps[i].svc.IP, preps[i].svc.Port),
			Source: "渲染结果", Body: rendered[i],
		})
	}
	if len(revs) > 0 {
		aiMetricsRef.inLLM.Add(int64(len(revs)))
		t0 := time.Now()
		got := askIdentifyBatch(ctx, revs)
		aiMetricsRef.inLLM.Add(-int64(len(revs)))
		ms := time.Since(t0).Milliseconds()
		for k, i := range rpend {
			preps[i].timing["identify_render"] = ms
			preps[i].stages = append(preps[i].stages, IdentStage{
				Stage: "AI·识别·渲染证据", Input: fmt.Sprintf("第 %d/%d 条", k+1, len(rpend)),
				Output: orNone(got[k].Raw), OK: got[k].dotConfident(),
			})
			switch {
			case got[k].hasName():
				verdict[i] = got[k]
			case got[k].Failed && !verdict[i].dotConfident():
				// 渲染证据没答出来，而上一轮只是低置信猜测（模型自己都要求渲染）：
				// 不能让猜测落库。整条标失败，保留 ai_need 重试。
				// 198.19.160.4:80 就是这条：首轮把 Server: Webs 猜成"华为"（conf 45），
				// 渲染页里写着 ©2018 Hikvision，但二次识别被 max_tokens 截断返回空，
				// 结果"华为"被写进库，而真名就摆在渲染结果里。
				v2 := verdict[i]
				v2.Failed = true
				verdict[i] = v2
			}
		}
	}

	// ---- 组装 ----
	out := make([]IdentResult, len(svcs))
	for i := range preps {
		r := preps[i].res
		if !preps[i].settled {
			v := verdict[i]
			if v.Failed {
				r.Failed = true
			}
			switch {
			case v.hasName():
				r.apply(v)
			case pageTitle[i] != "" && TitleClear(pageTitle[i]) &&
				!scanner.PlaceholderTitle(pageTitle[i], "") && !titleIsAllNoise(pageTitle[i]):
				r.Title = strings.TrimSpace(pageTitle[i])
				r.Source = "render"
				r.Reason = "渲染标题（未经模型确认）"
			case strings.TrimSpace(v.Reason) != "":
				r.Reason = v.Reason
			}
		}
		r.Stages, r.Hops, r.Timing = preps[i].stages, preps[i].hops, preps[i].timing
		out[i] = r
	}
	return out
}

// runParallel 以固定并发跑 n 个下标任务，用于把抓包/渲染的重叠起来。
func runParallel(n, conc int, fn func(i int)) {
	if n == 0 {
		return
	}
	if conc < 1 {
		conc = 1
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
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
	r.Confidence = v.conf()
	r.Confident = v.conf() >= 60
	r.Sufficient = v.Sufficient
	r.Reason = v.Reason
	r.Source = "ai"
	r.Failed = v.Failed
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
	Category string `json:"category"`
	// Confidence 用 float64 接：网关/模型在不同上下文里既会输出 0~100 的整数，
	// 也会输出 0~1 的小数（实测批量模式下 8 条全是 0.1/0.2）。用 int 接会让
	// 整个 JSON 反序列化失败——一条不匹配，整批结论全部丢失，最后全落"待分析"。
	Confidence float64 `json:"confidence"`
	Sufficient bool   `json:"sufficient"`
	// NeedRender 识别 agent 自己判断"这份证据不够，需要浏览器渲染后再看一次"。
	// v0.0.56 起判断权交给它，不再单独调判断 agent——两次往返读的是同一份证据。
	NeedRender bool   `json:"need_render"`
	Reason     string `json:"reason"`
	Raw        string `json:"-"`
	// Failed 这次结论是"网关没答出来"，不是"模型看过但没认出来"。
	// 两者必须分开：前者要保留待办下轮重试，后者才是真正的终态（待分析）。
	Failed bool `json:"-"`
}

// conf 归一化后的 0~100 置信度。
//
// 两种尺度都要认：<=1 视为比例（0.1 → 10 分）；否则视为百分制。
// 不归一化就会出现"模型给 0.9 分却被当成 0 分丢掉"。
func (v aiVerdict) conf() int {
	c := v.Confidence
	if c > 0 && c <= 1 {
		c *= 100
	}
	if c < 0 {
		c = 0
	}
	if c > 100 {
		c = 100
	}
	return int(c)
}

// dotConfident 名字可用：有名字且置信度达标
func (v aiVerdict) dotConfident() bool {
	return v.conf() >= 60 && (strings.TrimSpace(v.Title) != "" || strings.TrimSpace(v.Product) != "")
}

// hasName 模型给出了任何名字（不论置信）。
//
// 为什么采纳门槛不再卡 60：写回侧本来就有三档门槛（填空 15 / 指纹 15 / 覆盖 60），
// 这里再卡 60 等于把"填空 15"这一档废掉。实测 198.19.160.4:80 模型给 conf=55 的
// 正确结论「海康威视设备Web管理」被丢弃，链路退到渲染标题「登录」入库——
// 既白跑了一次模型，又把更好的结论换成了占位词。
func (v aiVerdict) hasName() bool {
	return strings.TrimSpace(v.Title) != "" || strings.TrimSpace(v.Product) != ""
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
		Temperature: 0,
		MaxTokens:   600,
	})
	if err != nil || res == nil || res.Resp == nil || len(res.Resp.Choices) == 0 {
		if err != nil && ctx.Err() == nil {
			aiMetricLLMFail()
			log.Printf("[ai-ident] 研判调用失败: %v", err)
		}
		return judgeVerdict{}
	}
	answer := strings.TrimSpace(res.Resp.Choices[0].Message.Content)
	if answer == "" {
		// 空 content 的两种成因必须分得清：模型真没答（finish=length 说明思考把
		// 预算耗尽），还是答了但被拦。不记 finish_reason 就只能靠猜。
		log.Printf("[ai-ident] 研判返回空内容（finish=%s tokens=%s）",
			res.Resp.Choices[0].Reason, usageBrief(res.Resp))
	}
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
	return askIdentifyWith(ctx, source, evidence, rendered, identChatExtra())
}

func askIdentifyWith(ctx context.Context, source, evidence string, rendered bool, kwargs map[string]interface{}) aiVerdict {
	hint := "以下是从该端口抓到的真实请求/响应包（含重定向）"
	if rendered {
		hint = "以下是浏览器真正渲染后的页面信息"
	}
	res, err := chatWithCtx(ctx, llm.ChatRequest{
		ChatTemplateKwargs: kwargs,
		Messages: []llm.Message{
			{Role: "system", Content: PromptFor(RoleIdentify)},
			{Role: "user", Content: fmt.Sprintf("%s。\n来源：%s\n\n%s", hint, source, evidence)},
		},
		Temperature: 0,
		// 2000 而不是 600：这个网关上跑的是思考模型（deepseek-v4.1-flash），
		// 思考 token 计入 completion。600 的预算下实测 finish=length、out=600、
		// content 为空——不是"看过但没认出来"，是"话没说完就被截断"。
		// 这类空回复此前被当成"模型没有结论"，直接让上一步的低置信猜测落库
		// （198.19.160.4:80 就是被这条吃掉的：渲染结果里明明写着 Hikvision）。
		MaxTokens: 2000,
	})
	if err != nil && kwargs != nil && ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) {
		// 网关不认 chat_template_kwargs（400/422）：永久关掉这个参数再试一次。
		// 加速项绝不能变成单点故障——换个网关就整条链路瘫掉是不可接受的。
		aiThinkUnsupported.Store(true)
		log.Printf("[ai-ident] 网关不接受 enable_thinking 参数，回退默认：%v", err)
		return askIdentifyWith(ctx, source, evidence, rendered, nil)
	}
	if err != nil || res == nil || res.Resp == nil || len(res.Resp.Choices) == 0 {
		if err != nil && ctx.Err() == nil {
			aiMetricLLMFail()
			log.Printf("[ai-ident] 识别调用失败: %v", err)
		}
		return aiVerdict{Sufficient: true, Raw: "模型调用失败", Failed: true}
	}
	answer := strings.TrimSpace(res.Resp.Choices[0].Message.Content)
	if answer == "" {
		reason := res.Resp.Choices[0].Reason
		log.Printf("[ai-ident] 识别返回空内容（finish=%s tokens=%s）", reason, usageBrief(res.Resp))
		// 空内容一律算失败（保留待办重试），不当成"看过但没结论"。
		// 两种成因：finish=length（思考吃光预算）与 finish=stop（模型确实没输出），
		// 前者提高预算后可重试成功，后者重试也无害（有退避，不会打爆网关）。
		return aiVerdict{Raw: "模型返回空内容（finish=" + orNone(reason) + "）", Failed: true}
	}
	var v aiVerdict
	if err := json.Unmarshal([]byte(extractJSON(answer)), &v); err != nil {
		return aiVerdict{Sufficient: true, Raw: answer}
	}
	v.Raw = answer
	return v
}

// identEvidence 批量识别里的一条证据
type identEvidence struct {
	Label  string // "198.18.7.30:443"
	Source string // 请求响应包 / 渲染结果
	Body   string
}

// askIdentifyBatch 一次请求识别多条目标。
//
// 为什么要批量：每次调用都是一次冷启动（新会话、无缓存、首 token 1~3 秒）。
// 45 条串起来光首 token 就要一百多秒，而证据本身只有几百 token。
// 8 条放进同一个请求，首 token 只付一次。
//
// 批量是加速手段，不能变成新的故障点：格式不符、条数不匹配、网关报错时
// 一律回退成逐条调用，结果与逐条路径完全一致。
func askIdentifyBatch(ctx context.Context, items []identEvidence) []aiVerdict {
	out := make([]aiVerdict, len(items))
	if len(items) == 0 {
		return out
	}
	if len(items) == 1 {
		out[0] = askIdentify(ctx, items[0].Body, orNone(items[0].Source), false)
		return out
	}
	var sb strings.Builder
	for i, it := range items {
		fmt.Fprintf(&sb, "【第 %d 条】%s（来源：%s）\n%s\n\n", i+1, it.Label, orNone(it.Source), it.Body)
	}
	fmt.Fprintf(&sb, "共 %d 条。只输出一个 JSON 数组，长度必须是 %d，顺序与上面一致，"+
		"不要解释、不要 markdown：\n"+
		`[{"title":"","product":"","category":"","confidence":0,"need_render":false,"reason":""}]`, len(items), len(items))

	res, err := chatWithCtxTimeout(ctx, llm.ChatRequest{
		ChatTemplateKwargs: identChatExtra(),
		Messages: []llm.Message{
			{Role: "system", Content: PromptFor(RoleIdentify) + "\n\n本次输入是多条目标，输出必须是 JSON 数组，长度与输入条数一致。"},
			{Role: "user", Content: sb.String()},
		},
		Temperature: 0,
		// 每条 600 + 固定 1200：批量输出是 JSON 数组，思考链比单条更长，
		// 220*n 的旧预算在思考模型上稳定被截断（实测 finish=length、响应半截数组）。
		MaxTokens: 600*len(items) + 1200,
	}, batchLLMTimeout(len(items)))
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) && identChatExtra() != nil {
		// 同单条路径：参数被网关拒绝时永久关掉它，退回逐条（逐条请求不带该参数）。
		aiThinkUnsupported.Store(true)
		log.Printf("[ai-ident] 网关不接受 enable_thinking 参数，批量回退逐条：%v", err)
		return askIdentifyEach(ctx, items)
	}
	if err != nil || res == nil || res.Resp == nil || len(res.Resp.Choices) == 0 {
		if err != nil {
			aiMetricLLMFail()
			log.Printf("[ai-ident] 批量识别（%d 条）失败: %v", len(items), err)
		}
		// 超时/取消不回退逐条：逐条只是把同一个已经排队的网关再打一遍，
		// 实测把整轮时长从 40 秒拖到 158 秒，且最终结论更差。整批标失败，
		// 保留 ai_need 让下一轮重试——这比"当场拿到一句空结论"有价值。
		if err != nil || ctx.Err() != nil {
			for i := range out {
				out[i] = aiVerdict{Raw: "模型调用失败", Failed: true, Sufficient: false}
			}
			return out
		}
		return askIdentifyEach(ctx, items)
	}
	answer := strings.TrimSpace(res.Resp.Choices[0].Message.Content)
	if answer == "" {
		// 截断/空回复整批标失败，不回退逐条：同一份预算下逐条只会得到同样的空，
		// 白白多付 N 次往返。失败会保留 ai_need，等退避后重试。
		log.Printf("[ai-ident] 批量识别（%d 条）返回空内容（finish=%s tokens=%s）",
			len(items), res.Resp.Choices[0].Reason, usageBrief(res.Resp))
		for i := range out {
			out[i] = aiVerdict{Raw: "模型返回空内容", Failed: true}
		}
		return out
	}
	var arr []aiVerdict
	if e := json.Unmarshal([]byte(extractJSONArray(answer)), &arr); e == nil && len(arr) == len(items) {
		for i := range arr {
			arr[i].Raw = answer
		}
		return arr
	}
	// 数组解析失败或条数对不上：整批回退。宁可多花几秒，也不能把 A 的结论写进 B。
	log.Printf("[ai-ident] 批量识别返回不可用（%d 条，响应 %d 字节），回退逐条", len(items), len(answer))
	return askIdentifyEach(ctx, items)
}

func askIdentifyEach(ctx context.Context, items []identEvidence) []aiVerdict {
	out := make([]aiVerdict, len(items))
	for i, it := range items {
		if ctx.Err() != nil {
			break
		}
		out[i] = askIdentify(ctx, it.Body, orNone(it.Source), false)
	}
	return out
}

// batchLLMTimeout 批量调用的子超时（依据见函数体内的实测数据）。
func batchLLMTimeout(n int) time.Duration {
	// 实测（deepseek-v4.1-flash，同构证据）：1 条 9.2s、2 条 9.7s、4 条 13.7s、8 条 18.9s。
	// 批量的单条等效成本随条数下降（9.2s → 2.4s），但总时长仍在涨，所以超时必须
	// 按条数放宽，否则正常批量会被判成超时、整批回退逐条，比不批量还慢。
	// 与 max_tokens 同步放宽：预算涨到 600*n+1200 之后，思考链也变长。
	// 实测 2 条 32 秒不够用，按 40s + 15s/条给，上限 150s。
	d := 40*time.Second + time.Duration(n)*15*time.Second
	if d > 150*time.Second {
		d = 150 * time.Second
	}
	return d
}

// usageBrief 输出 usage 摘要，用于诊断空回复（推理 token 是否吃掉了预算）
func usageBrief(r *llm.ChatResponse) string {
	if r == nil || r.Usage == nil {
		return "无"
	}
	return fmt.Sprintf("in=%d out=%d total=%d", r.Usage.PromptTokens, r.Usage.CompletionTokens, r.Usage.TotalTokens)
}

// chatWithCtx 让 LLM 调用可被 ctx 打断。
//
// llm.ChatWithFailover 内部用 http.Client 的固定超时（默认 120s），没有 ctx 入口。
// 直接改它的签名会影响所有调用点，所以在外面包一层：调用放 goroutine，主协程 select
// ctx.Done()。ctx 取消后主协程立刻返回，goroutine 由 HTTP 超时自然结束，
// channel 带缓冲所以不会永久泄漏。
func chatWithCtx(ctx context.Context, req llm.ChatRequest) (*llm.CallResult, error) {
	// 60 秒：思考模型在 2000 token 预算下单条实测 25~45 秒（长时间思考常见），
	// 30 秒会把正常调用判成超时——实测 198.19.160.4:80 连续两次 context deadline。
	return chatWithCtxTimeout(ctx, req, 60*time.Second)
}

// chatWithCtxTimeout 同 chatWithCtx，但子超时可指定（批量调用输出更长，需要更宽的预算）。
func chatWithCtxTimeout(ctx context.Context, req llm.ChatRequest, d time.Duration) (*llm.CallResult, error) {
	// 已经取消了就别起 goroutine：省一次无意义的调用，也让"取消后立即返回"
	// 这条语义可以被单测直接覆盖。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		res *llm.CallResult
		err error
	}
	// 单次调用子超时：网关偶发把一条请求挂到 40~70 秒（实测 198.19.160.4
	// 单次识别 46 秒、整条 69 秒），占着 worker 拖垮整队吞吐。正常响应 1~3 秒，
	// 12 秒已经宽松；超时按失败处理，走兜底逻辑，不再无限等。
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	ch := make(chan result, 1)
	go func() {
		r, e := llm.ChatWithFailover(req, 0)
		ch <- result{r, e}
	}()
	select {
	case r := <-ch:
		return r.res, r.err
	case <-cctx.Done():
		return nil, cctx.Err()
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
	log.Printf("[ai-ident] %s:%d 用时 %s（抓包 %dms / 识别 %dms / 渲染 %dms / 复识别 %dms）渲染=%v 置信=%v 结论=%s",
		job.IP, job.Port, total.Round(100*time.Millisecond).String(),
		r.Timing["trace"], r.Timing["identify"], r.Timing["render"], r.Timing["identify_render"],
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
	// 网关没答出来 ≠ 识别不出。前者保留待办（5 分钟后重排），后者才是终态。
	// 不区分就会把一大片"网关超时"写成"待分析"，用户看到的是假结论。
	if r.Failed {
		config.DB.Model(&model.Service{}).Where("id = ?", svc.ID).
			Updates(map[string]interface{}{"ai_need": true, "ai_checked_at": nil})
		aiRetryAfter.Store(svc.IP+":"+strconv.Itoa(svc.Port), time.Now().Add(aiRetryDelay))
		return false
	}
	updates := map[string]interface{}{"ai_checked_at": time.Now(), "ai_need": false}
	changed := false
	usableTitle := TitleClear(r.Title) && !scanner.PlaceholderTitle(r.Title, "")

	// 写回门槛分两档（v0.0.54）。
	//
	// 实测 45 条真值样本：模型给对名字但置信 40~55 的情况很常见——「身份认证系统」
	// 「IIS Windows Server」「Integrations」「SAP 消息服务器监控」都是这样，
	// 统一按 60 卡掉等于把这些能认出来的全丢掉，正是"该识别到的没识别到"。
	// 但 40 分也覆盖已有的、正确的旧标题就太冒险。
	//
	// 所以：填空（当前没有有效标题）40 分即可，覆盖（已有有效标题）仍要 60 分。
	haveValidOld := TitleClear(svc.Title) && !scanner.PlaceholderTitle(svc.Title, svc.StatusCode)
	bar := overwriteBar
	if !haveValidOld {
		bar = fillBar
	}
	// 已有有效标题时，不允许用"未识别/未知"这类否定结论覆盖它
	if usableTitle && IsUnidentified(r.Title) && haveValidOld {
		usableTitle = false
	}
	// 填空时也不写"未知/未识别"：那等于把空标题换成一句废话，还占掉了
	// title_source 的位置。有产品名就写进 fingerprint，同样能看出技术栈。
	if usableTitle && IsUnidentified(r.Title) {
		usableTitle = false
	}
	titleWritten := false
	if r.Confidence >= bar && usableTitle && r.Title != svc.Title {
		updates["title"] = truncateStr(r.Title, 300)
		updates["title_source"] = titleSourceOf(r)
		if strings.TrimSpace(svc.Title) != "" {
			updates["prev_title"] = svc.Title // 供「新变更」标签
		}
		titleWritten = true
		changed = true
	}
	// 指纹门槛单独一档（v0.0.54）：模型明确给出产品名（哪怕置信只有 15，例如
	// IIS/nginx 默认页）就记进 fingerprint。title 是"业务系统名"，模糊时宁可不写；
	// fingerprint 是"技术栈"，写下来对排查有用，也不会覆盖业务身份。
	if r.Confidence >= fingerprintBar && strings.TrimSpace(r.Product) != "" && r.Product != svc.Fingerprint {
		updates["fingerprint"] = truncateStr(r.Product, 300)
		if strings.TrimSpace(svc.Fingerprint) != "" {
			updates["prev_fingerprint"] = svc.Fingerprint
		}
		changed = true
	}
	// 终态：识别跑完仍没有任何标题时写「待分析」，不再留白。
	//
	// 为什么不是「无法识别」：留白在列表上分不清"还没识别"和"识别过没结论"，
	// 「待分析」是一个明确的人工待办——这条已经跑完整条链路（抓包→识别→渲染→再识别），
	// 机器给不出名字，需要人打开看一眼。
	//
	// 已有原文的不动：`502 Bad Gateway`、`Not Found` 这类本身就是信息，
	// 换成「待分析」是净损失（0.0.52 曾经把占位标题置空，导致列表整片空白）。
	if !titleWritten && strings.TrimSpace(svc.Title) == "" {
		updates["title"] = "待分析"
		updates["title_source"] = "none"
		changed = true
	}
	config.DB.Model(&model.Service{}).Where("id = ?", svc.ID).Updates(updates)
	return changed
}

// titleSourceOf 标题来源标记，落库后用于区分「HTML 直取 / 渲染后读 / 模型判定」。
// 空值按 ai 处理：历史数据里的 title_source 只有 fingerprint/tscan/ai 三种。
func titleSourceOf(r IdentResult) string {
	switch r.Source {
	case "html", "render", "ai":
		return r.Source
	}
	return "ai"
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
// extractJSONArray 取第一个 '[' 到最后一个 ']' 之间的内容（批量识别用）。
func extractJSONArray(s string) string {
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

func extractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}
