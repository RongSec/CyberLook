package service

import (
	"os"
	"strconv"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
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

// 默认页/泛词标题：等于没标题
var genericTitleRe = regexp.MustCompile(`(?i)^(` +
	`index of /?|welcome to .*|welcome|apache.*|nginx.*|iis.*|microsoft-iis|test page|` +
	`untitled|untitled document|home|homepage|login|log in|sign in|登录|登陆|首页|主页|欢迎|` +
	`目录|新建文件夹|document|default page|it works!|站点创建成功|恭喜.*|403 forbidden|404 not found|` +
	`error|forbidden|unauthorized|` +
	`https?://.*` +
	`)$`)

// 组件名/库名特征：全小写 ASCII 单词、带版本号
var (
	pureLowerRe = regexp.MustCompile(`^[a-z][a-z0-9\-\._ ]*$`)
	hasCJKRe    = regexp.MustCompile(`[\x{4e00}-\x{9fff}]`)
	pascalRe    = regexp.MustCompile(`^[A-Z][A-Za-z0-9][A-Za-z0-9\-\._ ]*$`)
)

// TitleClear 判断标题是否"清晰明了地体现了业务系统"
func TitleClear(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return false
	}
	if regexp.MustCompile(`^[\d\.\:\-/ ]+$`).MatchString(t) {
		return false
	}
	if len([]rune(t)) < 2 {
		return false
	}
	if genericTitleRe.MatchString(t) {
		return false
	}
	switch strings.ToLower(t) {
	case "未知", "未知系统", "无法识别", "不清楚", "无", "none", "null", "n/a", "na",
		"unknown", "unknown system", "unidentified", "not identified",
		"未知业务系统", "未知系统（无法识别）", "业务系统":
		return false
	}
	// 含「未知/无法识别」的一律不算清晰（模型常输出「未知业务系统（Spring Boot 默认错误页）」
	// 这种带括号解释的，精确匹配拦不住，实测 130 条审计里有 10 条被当成"识别成功"）
	if strings.Contains(t, "未知") || strings.Contains(t, "无法识别") || strings.Contains(t, "未识别") {
		return false
	}
	if strings.ContainsAny(t, "/|\\") {
		return false
	}
	if strings.Contains(t, "://") {
		return false
	}
	if pureLowerRe.MatchString(t) {
		return false
	}
	if hasCJKRe.MatchString(t) {
		return true
	}
	if pascalRe.MatchString(t) && len(t) >= 4 {
		return true
	}
	return len([]rune(t)) >= 8
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

// IdentifyService 跑完整识别链路，返回 title / fingerprint / 各阶段轨迹 / 抓到的请求响应包
func IdentifyService(svc model.Service) (string, string, []IdentStage, []scanner.Hop) {
	stages := []IdentStage{}
	title := strings.TrimSpace(svc.Title)
	fp := strings.TrimSpace(svc.Fingerprint)
	baseURL := buildURL(svc)
	var hops []scanner.Hop

	// 第 0 级：tscan 的结果是否清晰
	stage0OK := TitleClear(title)
	stages = append(stages, IdentStage{
		Stage: "规则判定（tscan 标题/指纹）", Input: "标题=" + orNone(title) + "；指纹=" + orNone(fp),
		Output: map[bool]string{true: "清晰，无需进一步识别", false: "不清晰，进入请求响应包分析"}[stage0OK],
		OK:     stage0OK,
	})
	if stage0OK {
		return title, fp, stages, hops
	}
	if baseURL == "" {
		stages = append(stages, IdentStage{Stage: "抓取请求响应包", Input: "-", Output: "非 http(s) 协议，跳过"})
		return title, fp, stages, hops
	}

	// 第 0.5 级：本地产品指纹库（零成本、零发包）
	// 实测：不清晰样本里 ESXi / Spring Boot / Kubelet / Tomcat / Lotus / SAP MC 这类
	// 特征极明显，用规则就能定，没必要为它们抓包 + 花两次 LLM。
	if name := scanner.MatchProduct(strings.Join([]string{title, fp, svc.Banner, svc.Service, svc.Protocol}, " ")); name != "" {
		stages = append(stages, IdentStage{
			Stage:  "本地产品库",
			Input:  "标题=" + orNone(title) + "；指纹=" + orNone(fp) + "；Banner=" + orNone(truncateStr(svc.Banner, 100)),
			Output: "命中：" + name, OK: true,
		})
		return name, fp, stages, hops
	}
	stages = append(stages, IdentStage{
		Stage:  "本地产品库",
		Input:  "标题=" + orNone(title) + "；指纹=" + orNone(fp),
		Output: "未命中，进入请求响应包分析", OK: false,
	})

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
		// 抓到的包里可能就有产品特征（Server 头 / 页面关键字），再给本地库一次机会，命中就不必问模型
		if name := scanner.MatchProduct(digest + " " + hops[0].ResponseHeaders); name != "" {
			stages = append(stages, IdentStage{Stage: "本地产品库（响应包）", Input: "响应头 + 页面摘要", Output: "命中：" + name, OK: true})
			return name, fp, stages, hops
		}
		aiTitle, aiFP, enough, raw := askAI("请求响应包", digest, false)
		stages = append(stages, IdentStage{
			Stage: "AI·请求响应包", Input: fmt.Sprintf("%d 跳 / %d 字节", len(hops), len(digest)),
			Output: orNone(raw), OK: TitleClear(aiTitle),
		})
		if TitleClear(aiTitle) {
			if fp == "" && aiFP != "" {
				fp = aiFP
			}
			return aiTitle, fp, stages, hops
		}
		// 模型明确表示「这些信息已经够了、只是给不出名字」→ 再渲染也没用，直接收手
		if enough {
			return title, fp, stages, hops
		}
	}

	// 第 2 级：浏览器真渲染
	if !scanner.ChromeAvailable() {
		stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: "容器内无 chromium，跳过"})
		return title, fp, stages, hops
	}
	pageTitle, pageDesc, pageText, rerr := scanner.RenderPage(baseURL, 8*time.Second)
	if rerr != nil {
		stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: "渲染失败：" + rerr.Error()})
		return title, fp, stages, hops
	}
	rendered := fmt.Sprintf("页面标题: %s\nmeta描述: %s\n正文摘要: %s", orNone(pageTitle), orNone(pageDesc), orNone(pageText))
	stages = append(stages, IdentStage{Stage: "浏览器渲染", Input: baseURL, Output: rendered, OK: TitleClear(pageTitle)})
	if TitleClear(pageTitle) {
		return strings.TrimSpace(pageTitle), fp, stages, hops
	}

	// 第 3 级：AI 用渲染结果再判
	aiTitle2, aiFP2, _, raw2 := askAI("渲染结果", rendered, true)
	stages = append(stages, IdentStage{Stage: "AI·渲染结果", Input: rendered, Output: orNone(raw2), OK: TitleClear(aiTitle2)})
	if TitleClear(aiTitle2) {
		if fp == "" && aiFP2 != "" {
			fp = aiFP2
		}
		return aiTitle2, fp, stages, hops
	}
	return title, fp, stages, hops
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

// askAI 让模型判断这是什么系统
func askAI(source, info string, rendered bool) (title, product string, sufficient bool, raw string) {
	hint := "信息可能不足，判断不出来就把字段留空"
	if rendered {
		hint = "这些是浏览器真实渲染后的页面信息，请据此判断业务系统名称"
	}
	prompt := fmt.Sprintf(`根据下面的信息判断这是什么业务系统，只输出 JSON，不要解释：
{"title":"业务系统名称","product":"产品/组件名","category":"分类（如 OA/财务/监控/中间件）","sufficient":true}
sufficient 表示「这些信息是否已经足以确定业务系统」；信息残缺、只有默认页/错误页时填 false。
%s
来源：%s
%s`, hint, source, info)

	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "你是资产指纹识别助手，只能输出 JSON。"},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.1,
		MaxTokens:   300,
	}
	res, err := llm.ChatWithFailover(req, 0)
	if err != nil || res == nil || res.Resp == nil || len(res.Resp.Choices) == 0 {
		if err != nil {
			log.Printf("[ai-ident] 调用失败: %v", err)
		}
		return "", "", true, ""
	}
	answer := strings.TrimSpace(res.Resp.Choices[0].Message.Content)
	var out struct {
		Title      string `json:"title"`
		Product    string `json:"product"`
		Category   string `json:"category"`
		Sufficient bool   `json:"sufficient"`
	}
	_ = json.Unmarshal([]byte(extractJSON(answer)), &out)
	t := strings.TrimSpace(out.Title)
	if t == "" {
		t = strings.TrimSpace(out.Product)
	}
	if !TitleClear(t) {
		t = ""
	}
	return t, strings.TrimSpace(out.Product), out.Sufficient, answer
}

func runAIIdentify(job aiIdentifyJob) {
	var svc model.Service
	if err := config.DB.Where("ip = ? AND port = ?", job.IP, job.Port).First(&svc).Error; err != nil {
		return
	}
	if !strings.HasPrefix(strings.ToLower(svc.Protocol), "http") {
		return
	}
	if TitleClear(svc.Title) {
		return
	}

	title, fp, stages, hops := IdentifyService(svc)
	changed := false
	updates := map[string]interface{}{}
	if TitleClear(title) && title != svc.Title {
		updates["title"] = truncateStr(title, 300)
		updates["title_source"] = "ai"
		if strings.TrimSpace(svc.Title) != "" {
			updates["prev_title"] = svc.Title // 供「新变更」标签
		}
		changed = true
	}
	if fp != "" && svc.Fingerprint == "" {
		updates["fingerprint"] = truncateStr(fp, 300)
		changed = true
	}
	if changed {
		config.DB.Model(&model.Service{}).Where("id = ?", svc.ID).Updates(updates)
	}
	recordIdentConversation(job.TaskID, svc, title, stages, changed, hops)
}

// recordIdentConversation 把识别链路写进该扫描任务的会话；
// 完整请求响应包放进 meta（前端折叠成按钮 + 四栏弹窗），不铺满对话。
func recordIdentConversation(taskID uint, svc model.Service, finalTitle string, stages []IdentStage, changed bool, hops []scanner.Hop) {
	conv := taskConversation(taskID)
	if conv == nil {
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("目标：%s:%d（%s）\n初始标题：%s\n\n识别链路：\n",
		svc.IP, svc.Port, svc.Protocol, orNone(svc.Title)))
	for i, st := range stages {
		sb.WriteString(fmt.Sprintf("%d) %s：%s\n   输入：%s\n", i+1, st.Stage,
			map[bool]string{true: "✅ 命中", false: "✗ 未命中"}[st.OK], oneLine(st.Input)))
		if st.Output != "" {
			sb.WriteString(fmt.Sprintf("   输出：%s\n", oneLine(st.Output)))
		}
	}
	sb.WriteString(fmt.Sprintf("\n结论：%s（%s）", orNone(finalTitle),
		map[bool]string{true: "已写回服务", false: "识别失败，保持原值"}[changed]))

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
	config.DB.Create(&model.LLMMessage{ConversationID: conv.ID, Role: "assistant", Content: orNone(finalTitle)})
	config.DB.Model(&model.LLMConversation{}).Where("id = ?", conv.ID).
		Updates(map[string]interface{}{
			"message_count": gorm.Expr("message_count + 2"),
			"updated_at":    time.Now(),
		})
}

// RecordIdentConversation 供手动识别复用：把识别链路写进任务会话
func RecordIdentConversation(taskID uint, svc model.Service, title string, stages []IdentStage, changed bool, hops []scanner.Hop) {
	recordIdentConversation(taskID, svc, title, stages, changed, hops)
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
	conv = model.LLMConversation{Title: name, TaskID: taskID}
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
