package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"asset-platform/internal/llm"
	"github.com/gin-gonic/gin"
)

// LlmStatus 检查 LLM 是否已配置
func LlmStatus(c *gin.Context) {
	cfg := llm.LoadConfig()
	c.JSON(http.StatusOK, gin.H{
		"enabled": cfg.Enabled(),
		"model":   cfg.Model,
		"baseURL": cfg.BaseURL,
		"tools":   len(llm.Tools()),
	})
}

// LlmChat 对话入口（同步返回，含工具调用）
//
// 请求：{ "messages": [{"role":"user","content":"..."}], "maxRounds": 5 }
// 响应：{ "reply": "...", "rounds": 2, "toolCalls": [...] }
func LlmChat(c *gin.Context) {
	cfg := llm.LoadConfig()
	if !cfg.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "LLM 未配置。请设置环境变量 LLM_BASE_URL（如 http://llm.internal:8000/v1），可选 LLM_API_KEY、LLM_MODEL",
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

	// 组装消息：system + 历史
	msgs := []llm.Message{{Role: "system", Content: llm.SystemPrompt()}}
	msgs = append(msgs, req.Messages...)

	tools := llm.Tools()
	var trace []gin.H

	for round := 0; round < req.MaxRounds; round++ {
		resp, err := llm.Chat(cfg, llm.ChatRequest{
			Messages:    msgs,
			Tools:       tools,
			Temperature: 0.3,
		})
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "trace": trace})
			return
		}
		if len(resp.Choices) == 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": "LLM 无响应", "trace": trace})
			return
		}

		choice := resp.Choices[0]
		msg := choice.Message
		msgs = append(msgs, msg)

		// 无工具调用 = 最终回答
		if len(msg.ToolCalls) == 0 {
			c.JSON(http.StatusOK, gin.H{
				"reply":     msg.Content,
				"rounds":    round + 1,
				"toolCalls": trace,
			})
			return
		}

		// 执行工具
		for _, tc := range msg.ToolCalls {
			result := llm.Execute(tc.Function.Name, tc.Function.Arguments)
			trace = append(trace, gin.H{
				"tool": tc.Function.Name,
				"args": tc.Function.Arguments,
			})
			msgs = append(msgs, llm.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"reply":     "已达到最大工具调用轮数，请缩小问题范围后重试",
		"rounds":    req.MaxRounds,
		"toolCalls": trace,
	})
}

// LlmTools 返回工具清单（给前端展示能力）
func LlmTools(c *gin.Context) {
	tools := llm.Tools()
	type item struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	var items []item
	for _, t := range tools {
		items = append(items, item{Name: t.Function.Name, Description: t.Function.Description})
	}
	c.JSON(http.StatusOK, gin.H{"count": len(items), "tools": items})
}

// LlmToolCall 直接调用某个工具（调试/手动执行用）
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

// LlmDoc 返回 LLM 可用的完整接口手册（Markdown）
func LlmDoc(c *gin.Context) {
	tools := llm.Tools()
	var sb string
	sb += "# 资产测绘平台 LLM 接口手册\n\n"
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
