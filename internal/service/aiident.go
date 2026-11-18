package service

import (
	"os"
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

	"gorm.io/gorm"
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
	aiIdentifyWorkers   = 3
	aiIdentifyQueueSize = 2000
)

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
	for i := 0; i < aiIdentifyWorkers; i++ {
		go func() {
			for job := range aiQueue {
				runAIIdentify(job)
			}
		}()
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
		if verdict.dotConfident() {
			res.apply(verdict)
			res.Stages, res.Hops = stages, hops
			return res
		}
		// 模型明确说"信息已经够、只是给不出确切名字"→ 再渲染也没用
		if verdict.Sufficient {
			res.Sufficient, res.Reason = true, verdict.Reason
			res.Stages, res.Hops = stages, hops
			return res
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
	} else if strings.TrimSpace(pageTitle) != "" && TitleClear(pageTitle) {
		// 模型没给出高置信名字，但渲染后的标题本身是明确文字：作为低置信候选带上
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
   confidence 要给高——不要因为名字不符合常见格式就否定它。`

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
	updates := map[string]interface{}{"ai_checked_at": time.Now(), "ai_need": false}
	changed := false
	if r.Confident && TitleClear(r.Title) && r.Title != svc.Title {
		updates["title"] = truncateStr(r.Title, 300)
		updates["title_source"] = "ai"
		if strings.TrimSpace(svc.Title) != "" {
			updates["prev_title"] = svc.Title // 供「新变更」标签
		}
		changed = true
	}
	if r.Confident && strings.TrimSpace(r.Product) != "" && r.Product != svc.Fingerprint {
		updates["fingerprint"] = truncateStr(r.Product, 300)
		changed = true
	}
	config.DB.Model(&model.Service{}).Where("id = ?", svc.ID).Updates(updates)
	recordIdentConversation(job.TaskID, svc, r, changed)
}

// recordIdentConversation 把识别链路写进该扫描任务的会话；
// 完整请求响应包放进 meta（前端折叠成按钮 + 四栏弹窗），不铺满对话。
func recordIdentConversation(taskID uint, svc model.Service, r IdentResult, changed bool) {
	conv := taskConversation(taskID)
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

	meta := ""
	if len(hops) > 0 {
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
		Updates(map[string]interface{}{
			"message_count": gorm.Expr("message_count + 2"),
			"updated_at":    time.Now(),
		})
}

// RecordIdentConversation 供手动识别复用：把识别链路写进任务会话
func RecordIdentConversation(taskID uint, svc model.Service, r IdentResult, changed bool) {
	recordIdentConversation(taskID, svc, r, changed)
}

// taskConversation 取（或创建）某个扫描任务对应的会话
func taskConversation(taskID uint) *model.LLMConversation {
	if config.DB == nil || taskID == 0 {
		return nil
	}
	var conv model.LLMConversation
	if err := config.DB.Where("task_id = ?", taskID).Order("id").First(&conv).Error; err == nil {
		return &conv
	}
	var task model.ScanTask
	name := fmt.Sprintf("任务 #%d", taskID)
	if err := config.DB.First(&task, taskID).Error; err == nil && task.Name != "" {
		name = fmt.Sprintf("扫描任务 #%d · %s", taskID, task.Name)
	}
	conv = model.LLMConversation{Title: name, TaskID: taskID, Kind: "svc_ident"}
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
