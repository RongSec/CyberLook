package api

import (
	"encoding/json"
	"strings"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/llm"
	"asset-platform/internal/model"
	"github.com/gin-gonic/gin"
)

// ============ 配置 CRUD ============

// LlmStatus 整体状态
func LlmStatus(c *gin.Context) {
	var total, enabled int64
	config.DB.Model(&model.LLMProfile{}).Count(&total)
	config.DB.Model(&model.LLMProfile{}).Where("enabled = ?", true).Count(&enabled)

	var todayTokens, todayCalls int64
	today := time.Now().Truncate(24 * time.Hour)
	config.DB.Model(&model.LLMUsage{}).Where("created_at >= ?", today).
		Select("COALESCE(SUM(total_tokens),0)").Scan(&todayTokens)
	config.DB.Model(&model.LLMUsage{}).Where("created_at >= ?", today).Count(&todayCalls)

	avail := llm.AvailableProfiles()
	c.JSON(http.StatusOK, gin.H{
		"enabled":      enabled > 0 && len(avail) > 0,
		"profileTotal": total,
		"profileUp":    enabled,
		"available":    len(avail),
		"tools":        len(llm.Tools()),
		"todayTokens":  todayTokens,
		"todayCalls":   todayCalls,
	})
}

// ListLlmProfiles 配置列表（含熔断状态）
func ListLlmProfiles(c *gin.Context) {
	var list []model.LLMProfile
	config.DB.Order("priority DESC, id").Find(&list)

	breaker := llm.GlobalBreaker()
	for i := range list {
		st := breaker.Snapshot(list[i].ID)
		list[i].BreakerOpen = st.Open()
		list[i].ConsecFails = st.Fails
		list[i].LastError = st.LastError
		list[i].LastLatencyMs = st.LatencyMs
		if st.Open() {
			list[i].OpenUntil = st.OpenUntil.Format("2006-01-02 15:04:05")
		}
	}
	c.JSON(http.StatusOK, gin.H{"count": len(list), "items": list})
}

type llmProfileReq struct {
	Name            string  `json:"name"`
	Provider        string  `json:"provider"`
	BaseURL         string  `json:"baseUrl"`
	APIKey          string  `json:"apiKey"`
	Model           string  `json:"model"`
	Temperature     float64 `json:"temperature"`
	MaxTokens       int     `json:"maxTokens"`
	ContextWindowK  int     `json:"contextWindowK"`
	Priority        int     `json:"priority"`
	Enabled         *bool   `json:"enabled"`
	PoolExclude     bool    `json:"poolExclude"`
	FailThreshold   int     `json:"failThreshold"`
	CooldownSeconds int     `json:"cooldownSeconds"`
	TimeoutSeconds  int     `json:"timeoutSeconds"`
	ViaProxy        string  `json:"viaProxy"`
	ThinkingType    string  `json:"thinkingType"`
	ReasoningEffort string  `json:"reasoningEffort"`
	RatePerSecond   float64 `json:"ratePerSecond"`
	RatePerMinute   float64 `json:"ratePerMinute"`
}

func keyHint(k string) string {
	if len(k) <= 8 {
		if k == "" {
			return ""
		}
		return "***"
	}
	return k[:4] + "..." + k[len(k)-4:]
}

// CreateLlmProfile 新增
func CreateLlmProfile(c *gin.Context) {
	var req llmProfileReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" || req.BaseURL == "" || req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需要 name、baseUrl、model"})
		return
	}
	if req.Provider == "" {
		req.Provider = "openai"
	}
	// priority: 0-10，0 最优先；未填时用 5（中间值）
	if req.Priority == 0 && req.Enabled == nil {
		req.Priority = 5
	}
	if req.FailThreshold == 0 {
		req.FailThreshold = 3
	}
	if req.CooldownSeconds == 0 {
		req.CooldownSeconds = 60
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 120
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 8192 // 推理模型需要足够空间
	}
	if req.ContextWindowK == 0 {
		req.ContextWindowK = 128
	}

	p := model.LLMProfile{
		Name: req.Name, Provider: req.Provider, BaseURL: req.BaseURL,
		APIKey: req.APIKey, APIKeyHint: keyHint(req.APIKey), Model: req.Model,
		Temperature: req.Temperature, MaxTokens: req.MaxTokens, ContextWindowK: req.ContextWindowK,
		Priority: req.Priority, Enabled: req.Enabled == nil || *req.Enabled,
		PoolExclude: req.PoolExclude, FailThreshold: req.FailThreshold,
		CooldownSeconds: req.CooldownSeconds, TimeoutSeconds: req.TimeoutSeconds,
				ViaProxy: req.ViaProxy,
		ThinkingType: req.ThinkingType, ReasoningEffort: req.ReasoningEffort,
		RatePerSecond: req.RatePerSecond, RatePerMinute: req.RatePerMinute,
	}
	// Select("*") 强制写入零值字段，避免 gorm default 标签覆盖 false
	if err := config.DB.Select("*").Create(&p).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": p.ID})
}

// UpdateLlmProfile 更新
func UpdateLlmProfile(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var p model.LLMProfile
	if err := config.DB.First(&p, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "配置不存在"})
		return
	}

	var req llmProfileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	updates := map[string]interface{}{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Provider != "" {
		updates["provider"] = req.Provider
	}
	if req.BaseURL != "" {
		updates["base_url"] = req.BaseURL
	}
	// 空字符串 = 不修改密钥；"__CLEAR__" = 清空
	if req.APIKey == "__CLEAR__" {
		updates["api_key"] = ""
		updates["api_key_hint"] = ""
	} else if req.APIKey != "" {
		updates["api_key"] = req.APIKey
		updates["api_key_hint"] = keyHint(req.APIKey)
	}
	if req.Model != "" {
		updates["model"] = req.Model
	}
	if req.Temperature > 0 {
		updates["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		updates["max_tokens"] = req.MaxTokens
	}
	if req.ContextWindowK > 0 {
		updates["context_window_k"] = req.ContextWindowK
	}
	if req.Priority != 0 {
		updates["priority"] = req.Priority
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	updates["pool_exclude"] = req.PoolExclude
	if req.FailThreshold > 0 {
		updates["fail_threshold"] = req.FailThreshold
	}
	if req.CooldownSeconds > 0 {
		updates["cooldown_seconds"] = req.CooldownSeconds
	}
	if req.TimeoutSeconds > 0 {
		updates["timeout_seconds"] = req.TimeoutSeconds
	}
	updates["via_proxy"] = req.ViaProxy
	updates["provider"] = req.Provider
	updates["thinking_type"] = req.ThinkingType
	updates["reasoning_effort"] = req.ReasoningEffort
	updates["rate_per_second"] = req.RatePerSecond
	updates["rate_per_minute"] = req.RatePerMinute

	if err := config.DB.Model(&p).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 配置变了，熔断状态重置
	llm.GlobalBreaker().Reset(uint(id))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DeleteLlmProfile 删除
func DeleteLlmProfile(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	config.DB.Delete(&model.LLMProfile{}, id)
	llm.GlobalBreaker().Reset(uint(id))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// TestLlmProfile 测试连通性
func TestLlmProfile(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var p model.LLMProfile
	if err := config.DB.First(&p, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "配置不存在"})
		return
	}
	latency, reply, err := llm.TestProfile(&p)
	if err != nil {
		msg := err.Error()
		hint := ""
		if strings.Contains(msg, "EOF") || strings.Contains(msg, "connection reset") {
			hint = "TLS 握手被中断。常见原因：1) 内网网关只支持 TLS 1.0/1.1，试试把「TLS 最低版本」设为 tls1.0；2) 证书自签，勾选「跳过证书校验」；3) Docker 容器网络限制（本地开发环境常见，生产服务器通常正常）"
		} else if strings.Contains(msg, "connection refused") {
			hint = "目标端口拒绝连接，检查 Base URL 的地址和端口是否正确"
		} else if strings.Contains(msg, "no such host") {
			hint = "域名解析失败，检查容器内 DNS 配置"
		} else if strings.Contains(msg, "401") {
			hint = "认证失败，检查 API Key 是否正确"
		} else if strings.Contains(msg, "404") {
			hint = "路径不存在，检查 Base URL 是否需要包含 /v1"
		}
		c.JSON(http.StatusOK, gin.H{"ok": false, "latencyMs": latency, "error": msg, "hint": hint})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "latencyMs": latency, "reply": reply})
}

// ResetLlmBreaker 手动恢复熔断
func ResetLlmBreaker(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	llm.GlobalBreaker().Reset(uint(id))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ResetAllLlmBreakers 全部恢复
func ResetAllLlmBreakers(c *gin.Context) {
	llm.GlobalBreaker().ResetAll()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ============ 用量统计 ============

// LlmUsageStats 用量汇总
func LlmUsageStats(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	if days <= 0 || days > 90 {
		days = 7
	}
	since := time.Now().AddDate(0, 0, -days)

	// 总计
	var total struct {
		Calls    int64
		InTok    int64
		OutTok   int64
		TotalTok int64
		Errors   int64
		AvgMs    float64
	}
	config.DB.Model(&model.LLMUsage{}).Where("created_at >= ?", since).
		Select("COUNT(*) as calls, COALESCE(SUM(input_tokens),0) as in_tok, COALESCE(SUM(output_tokens),0) as out_tok, COALESCE(SUM(total_tokens),0) as total_tok, AVG(latency_ms) as avg_ms").
		Scan(&total)
	config.DB.Model(&model.LLMUsage{}).Where("created_at >= ? AND status = ?", since, "error").Count(&total.Errors)

	// 按 profile
	type byProfile struct {
		ProfileName string `json:"profileName"`
		Model       string `json:"model"`
		Calls       int64  `json:"calls"`
		TotalTokens int64  `json:"totalTokens"`
		Errors      int64  `json:"errors"`
		AvgMs       int    `json:"avgMs"`
	}
	var profiles []byProfile
	config.DB.Model(&model.LLMUsage{}).Where("created_at >= ?", since).
		Select("profile_name, model, COUNT(*) as calls, COALESCE(SUM(total_tokens),0) as total_tokens, SUM(CASE WHEN status='error' THEN 1 ELSE 0 END) as errors, COALESCE(AVG(latency_ms),0) as avg_ms").
		Group("profile_name, model").Order("calls DESC").Scan(&profiles)

	// 按天
	type byDay struct {
		Day    string `json:"day"`
		Calls  int64  `json:"calls"`
		Tokens int64  `json:"tokens"`
	}
	var daily []byDay
	config.DB.Model(&model.LLMUsage{}).Where("created_at >= ?", since).
		Select("TO_CHAR(created_at, 'MM-DD') as day, COUNT(*) as calls, COALESCE(SUM(total_tokens),0) as tokens").
		Group("day").Order("day").Scan(&daily)

	// 最近记录
	var recent []model.LLMUsage
	config.DB.Order("id DESC").Limit(20).Find(&recent)

	c.JSON(http.StatusOK, gin.H{
		"days": days,
		"total": gin.H{
			"calls": total.Calls, "inputTokens": total.InTok, "outputTokens": total.OutTok,
			"totalTokens": total.TotalTok, "errors": total.Errors, "avgMs": int(total.AvgMs),
		},
		"byProfile": profiles,
		"daily":     daily,
		"recent":    recent,
	})
}

// ============ 对话 ============

// LlmChat 对话（含 Function Calling + 故障轮询）
func LlmChat(c *gin.Context) {
	avail := llm.AvailableProfiles()
	if len(avail) == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "没有可用的 LLM 配置。请在「LLM 配置」页面添加并启用至少一个配置。",
		})
		return
	}

	var req struct {
		Messages  []llm.Message `json:"messages"`
		MaxRounds int           `json:"maxRounds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需要 messages 数组"})
		return
	}
	if req.MaxRounds <= 0 || req.MaxRounds > 8 {
		req.MaxRounds = 5
	}

	msgs := []llm.Message{{Role: "system", Content: llm.SystemPrompt()}}
	msgs = append(msgs, req.Messages...)

	tools := llm.Tools()
	var trace []gin.H
	totalTokens := 0
	usedProfiles := map[string]bool{}

	for round := 0; round < req.MaxRounds; round++ {
		result, err := llm.ChatWithFailover(llm.ChatRequest{
			Messages: msgs,
			Tools:    tools,
		}, round)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "trace": trace})
			return
		}

		if result.Usage != nil {
			totalTokens += result.Usage.TotalTokens
		}
		usedProfiles[result.Profile.Name] = true

		resp := result.Resp
		if len(resp.Choices) == 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": "LLM 无响应", "trace": trace})
			return
		}

		msg := resp.Choices[0].Message
		msgs = append(msgs, msg)

		if len(msg.ToolCalls) == 0 {
			c.JSON(http.StatusOK, gin.H{
				"reply":        msg.Content,
				"rounds":       round + 1,
				"toolCalls":    trace,
				"totalTokens":  totalTokens,
				"usedProfiles": keys(usedProfiles),
			})
			return
		}

		for _, tc := range msg.ToolCalls {
			toolResult := llm.Execute(tc.Function.Name, tc.Function.Arguments)
			trace = append(trace, gin.H{"tool": tc.Function.Name, "args": tc.Function.Arguments})
			msgs = append(msgs, llm.Message{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"reply":       "已达到最大工具调用轮数，请缩小问题范围后重试",
		"rounds":      req.MaxRounds,
		"toolCalls":   trace,
		"totalTokens": totalTokens,
	})
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// LlmTools 工具清单
func LlmTools(c *gin.Context) {
	tools := llm.Tools()
	type item struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Category    string `json:"category"`
	}
	var items []item
	for _, t := range tools {
		cat := "read"
		if isWriteTool(t.Function.Name) {
			cat = "write"
		}
		items = append(items, item{Name: t.Function.Name, Description: t.Function.Description, Category: cat})
	}
	c.JSON(http.StatusOK, gin.H{"count": len(items), "tools": items})
}

func isWriteTool(name string) bool {
	switch name {
	case "create_task", "control_task", "delete_task", "confirm_asset",
		"update_asset", "delete_asset", "batch_update_assets",
		"create_department", "update_department", "delete_department",
		"create_segment", "delete_segment", "create_template", "delete_template",
		"import_ledger", "delete_service", "resolve_conflict",
		"delete_disposal", "batch_delete_disposal", "delete_node", "upgrade_node",
		"create_exclude", "delete_exclude":
		return true
	}
	return false
}

// LlmToolCall 直接调用工具
func LlmToolCall(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
		Args string `json:"args"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需要 name 参数"})
		return
	}
	result := llm.Execute(req.Name, req.Args)
	var parsed interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		c.JSON(http.StatusOK, gin.H{"raw": result})
		return
	}
	c.JSON(http.StatusOK, gin.H{"result": parsed})
}

// LlmDoc 接口手册
func LlmDoc(c *gin.Context) {
	tools := llm.Tools()
	sb := "# 资产测绘平台 LLM 接口手册\n\n"
	sb += "## 系统提示词\n\n```\n" + llm.SystemPrompt() + "\n```\n\n"
	sb += "## 工具清单\n\n"
	for _, t := range tools {
		sb += fmt.Sprintf("### `%s`\n\n%s\n\n", t.Function.Name, t.Function.Description)
		if params, ok := t.Function.Parameters["properties"].(map[string]interface{}); ok && len(params) > 0 {
			sb += "参数：\n\n"
			for k, v := range params {
				if m, ok := v.(map[string]interface{}); ok {
					sb += fmt.Sprintf("- `%s`: %v\n", k, m["description"])
				}
			}
			sb += "\n"
		}
	}
	c.Header("Content-Type", "text/markdown; charset=utf-8")
	c.String(http.StatusOK, sb)
}
