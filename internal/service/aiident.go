package service

import (
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
	// 每 2 核 1 路，且至少空出一个核给 web/DB：4 核 → 1 路，8 核 → 3 路。
	n := (runtime.NumCPU() - 1) / 2
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
	default:
		log.Printf("[ai-ident] 任务 #%d 识别队列已满（%d），丢弃 %s:%d", taskID, aiIdentifyQueueSize, ip, port)
	}
}

func startAIWorkers() {
	aiQueue = make(chan aiIdentifyJob, aiIdentifyQueueSize)
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
	timer := time.AfterFunc(aiIdentifyTimeout, func() {
		log.Printf("[ai-ident] %s:%d 超过 %s 未完成，销毁浏览器实例强制收敛",
			job.IP, job.Port, aiIdentifyTimeout)
		scanner.ResetBrowser()
	})
	defer timer.Stop()
	runAIIdentify(job)
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
}

// IdentifyService 跑完整识别链路：请求响应包 → AI →（AI 说不够）→ 浏览器渲染 → AI。
//
// 这里不设"标题正则判清晰就跳过"的前置闸门——那会把"标题恰好像样、其实是默认页"
// 的情况漏掉，也会把合法产品名当垃圾。是否足够判断，由模型自己说（Sufficient）。
func IdentifyService(svc model.Service) IdentResult {
	res := IdentResult{Title: strings.TrimSpace(svc.Title), Product: strings.TrimSpace(svc.Fingerprint)}
	stages := []IdentStage{}
	baseURL := buildURL(svc)
	var hops []scanner.Hop

	if baseURL == "" {
		stages = append(stages, IdentStage{Stage: "抓取请求响应包", Input: "-", Output: "非 http(s) 协议，不适用 Web 识别"})
		res.Stages = stages
		return res
	}

	// 第 1 级：抓完整请求响应包 → AI
	scheme := "http"
	if strings.HasPrefix(strings.ToLower(svc.Protocol), "https") {
		scheme = "https"
	}
	var trErr error
	hops, trErr = scanner.HTTPTrace(svc.IP, svc.Port, scheme, 4)
	switch {
	case trErr != nil:
		stages = append(stages, IdentStage{Stage: "抓取请求响应包", Input: baseURL, Output: "抓取失败：" + trErr.Error()})
	case len(hops) == 0:
		stages = append(stages, IdentStage{Stage: "抓取请求响应包", Input: baseURL, Output: "无响应"})
	default:
		digest := scanner.TraceDigest(hops)
		stages = append(stages, IdentStage{
			Stage:  "抓取请求响应包",
			Input:  fmt.Sprintf("%s（共 %d 跳）", baseURL, len(hops)),
			Output: hops[0].ResponseLine + " / " + orNone(shortHeader(hops[0].ResponseHeaders)),
			OK:     true,
		})
		verdict := askAI("请求响应包", digest, false)
		stages = append(stages, IdentStage{
			Stage: "AI·请求响应包", Input: fmt.Sprintf("%d 跳 / %d 字节", len(hops), len(digest)),
			Output: orNone(verdict.Raw), OK: verdict.dotConfident(),
		})
		if verdict.dotConfident() && verdict.Confidence >= confidentEnough {
			res.apply(verdict)
			res.Stages, res.Hops = stages, hops
			return res
		}
		// 置信 60-79 属于「特征对上了，但只到厂商/类别一级」。
		// 实测 198.18.8.65:80：模型凭 window.VMP + RSAModulus 判成
		// 「深信服 SSL VPN/安全网关」置信 62 就收工，而渲染后的页面里
		// 明明写着「深信服超融合平台 / SANGFOR HCI」——厂商对了、产品线错了。
		// 这类结论不写回、继续渲染，让页面文字自己说话。
		if verdict.dotConfident() {
			stages = append(stages, IdentStage{
				Stage:  "复核·结论粒度",
				Input:  fmt.Sprintf("置信 %d", verdict.Confidence),
				Output: fmt.Sprintf("只到厂商/类别一级（< %d），继续渲染复核产品线", confidentEnough),
			})
		}
		// 模型明确说"信息已经够、只是给不出确切名字"→ 再渲染也没用。
		// 但前提是它看到的确实是"完整证据"：如果抓到的全是壳页/跳转页/错误页
		// （visibleText < 200 字），那句 sufficient 是被烂证据骗出来的，
		// 必须继续渲染——浏览器会执行 JS 跳转，这是唯一能看到真实登录页的途径。
		if verdict.Sufficient && !scanner.TraceWeak(hops) {
			res.Sufficient, res.Reason = true, verdict.Reason
			res.Stages, res.Hops = stages, hops
			return res
		}
		if verdict.Sufficient {
			stages = append(stages, IdentStage{
				Stage:  "复核·证据强度",
				Input:  fmt.Sprintf("%d 跳", len(hops)),
				Output: "抓到的是壳页/跳转页（可见文字不足 200 字），模型判 sufficient 不可信，继续渲染",
			})
		}
	}

	// 第 2 级：浏览器真渲染
	if !scanner.ChromeAvailable() {
		stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: "容器内无 chromium，跳过"})
		res.Stages, res.Hops = stages, hops
		return res
	}
	pageTitle, pageDesc, pageText, rerr := scanner.RenderPage(baseURL, 8*time.Second)
	if rerr != nil {
		stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: "渲染失败：" + rerr.Error()})
		res.Stages, res.Hops = stages, hops
		return res
	}
	rendered := fmt.Sprintf("页面标题: %s\nmeta描述: %s\n正文摘要: %s", orNone(pageTitle), orNone(pageDesc), orNone(pageText))
	stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: rendered, OK: strings.TrimSpace(pageTitle) != ""})

	// 第 3 级：AI 用渲染结果再判（渲染标题本身不当结论，仍由模型判断）
	verdict2 := askAI("渲染结果", rendered, true)
	stages = append(stages, IdentStage{Stage: "AI·渲染结果", Input: rendered, Output: orNone(verdict2.Raw), OK: verdict2.dotConfident()})
	if verdict2.dotConfident() {
		res.apply(verdict2)
	} else if strings.TrimSpace(pageTitle) != "" && TitleClear(pageTitle) &&
		!scanner.PlaceholderTitle(pageTitle, "") {
		// 模型没给出高置信名字，但渲染后的标题本身是明确文字：作为低置信候选带上。
		// 注意排除 `Login` / `Redirection……` 这类占位标题——浏览器的 <title>
		// 经常就是 "Login"，直接拿来用等于把壳页标题又写回一遍。
		res.Title = strings.TrimSpace(pageTitle)
		res.Reason = "渲染标题（未经模型确认）"
	}
	res.Stages, res.Hops = stages, hops
	return res
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

const aiIdentSystem = `你是内网资产识别专家。用户给你一个 HTTP 端口的请求/响应证据，你要判断上面跑的是什么业务系统。

只输出一个 JSON 对象，不要解释、不要 markdown 代码块：
{"title":"业务系统名称","product":"产品/组件名（含厂商更好）","category":"分类，如 OA/财务/审计/中间件","confidence":0,"sufficient":true,"reason":"20 字以内依据"}

判定要求：
1. 只依据证据判断。证据里没有的不要编，宁可不给名字。
2. title 写人看得懂的系统名（例如"明御数据库审计与风险控制系统"），不要写 nginx/Apache 这种服务器名。
3. confidence 0-100：
   - 90+：页面标题/接口/版权里直接写出产品名
   - 60-89：有明确特征（特有路径、错误页样式、证书、界面元素）
   - 30-59：只能判断类别
   - <30：基本靠猜
4. sufficient=true 表示"现有证据已经足够，但你只能给到类别或给不出名字"——
   这种情况不要再要求渲染，渲染也拿不到更多信息。
5. sufficient=false 表示"证据不足，需要看渲染后的完整页面"。
   典型：标题为空或只有 Login/Welcome、正文几乎没有文字、页面是空壳 SPA。
6. 如果现有证据里明明有产品名（哪怕长得怪，例如 "SAP Message Server,release 722 (SP8)"），
   confidence 要给高——不要因为名字不符合常见格式就否定它。
7. 证据可能是**多跳**：前几跳常常是跳转壳页（200 + <title>Redirection……</title> +
   location.replace）。一律以**最后一跳**的页面内容为准，不要把中间跳转页的标题、
   状态文本当成系统名。
8. 下列内容**一律不是系统名**，禁止作为 title 输出：Redirection……、307 TEMPORARY REDIRECT、
   Login、Welcome、HTTP、Index of /、Error 404、以及 Apache/nginx/IIS 这类服务器名。
   只看到这些就说明证据不足，sufficient 要给 false，让流程继续去看渲染后的页面。
9. 页面里的厂商特有对象/变量也可以作为依据（例如 window.VMP + RSAModulus +
   ukey_service_version 是深信服系产品的登录页特征；Sangfor / H3C / Huawei 版权行同理），
   但必须在 reason 里点明你依据的是哪个特征。
10. 站点图标、公共资源路径（/common/resources/icon/enterprise_custom/）这类线索
   只能辅助判断，单独出现不足以定名。
11. **严格区分「厂商」和「产品线」**。同一厂商的多个产品常共用同一套登录框架：
   例如深信服的 aTrust、SSL VPN、超融合 HCI、EDR 都用 window.VMP + RSAModulus 这套前端。
   如果证据里只有厂商特征、没有产品线文字（HCI / 超融合 / aTrust / SSL VPN / EDR 等），
   confidence 不得超过 55，sufficient 要给 false —— 页面渲染后通常能直接看到产品名。
   把厂商名当成产品名输出，比不给名字更糟。`

// askAI 让模型判断这是什么系统
func askAI(source, info string, rendered bool) aiVerdict {
	hint := "以下是从该端口抓到的真实请求/响应包（含重定向）"
	if rendered {
		hint = "以下是浏览器真正渲染后的页面信息"
	}
	prompt := fmt.Sprintf("%s。\n来源：%s\n\n%s", hint, source, info)

	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: aiIdentSystem},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.1,
		MaxTokens:   400,
	}
	res, err := llm.ChatWithFailover(req, 0)
	if err != nil || res == nil || res.Resp == nil || len(res.Resp.Choices) == 0 {
		if err != nil {
			log.Printf("[ai-ident] 调用失败: %v", err)
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

func runAIIdentify(job aiIdentifyJob) {
	var svc model.Service
	if err := config.DB.Where("ip = ? AND port = ?", job.IP, job.Port).First(&svc).Error; err != nil {
		return
	}
	if !strings.HasPrefix(strings.ToLower(svc.Protocol), "http") {
		// AI 识别链路目前只覆盖 HTTP(S)；非 HTTP 的交给引擎侧规则
		config.DB.Model(&model.Service{}).Where("id = ?", svc.ID).Update("ai_checked_at", time.Now())
		return
	}

	r := IdentifyService(svc)
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
