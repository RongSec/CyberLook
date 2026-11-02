package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/llm"
	"asset-platform/internal/model"
	"github.com/gin-gonic/gin"
)

// ============ 会话管理 ============

// ListConversations 列出历史会话
func ListConversations(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	keyword := strings.TrimSpace(c.Query("keyword"))

	q := config.DB.Model(&model.LLMConversation{})
	if keyword != "" {
		q = q.Where("title ILIKE ?", "%"+keyword+"%")
	}
	var list []model.LLMConversation
	q.Order("updated_at DESC").Limit(limit).Find(&list)
	c.JSON(http.StatusOK, gin.H{"count": len(list), "items": list})
}

// CreateConversation 新建会话
func CreateConversation(c *gin.Context) {
	var req struct {
		Title string `json:"title"`
	}
	_ = c.ShouldBindJSON(&req)
	conv := model.LLMConversation{Title: req.Title}
	if conv.Title == "" {
		conv.Title = "新会话"
	}
	if err := config.DB.Create(&conv).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "conversation": conv})
}

// GetConversation 获取会话 + 所有消息
func GetConversation(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var conv model.LLMConversation
	if err := config.DB.First(&conv, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
		return
	}
	var msgs []model.LLMMessage
	config.DB.Where("conversation_id = ?", id).Order("id").Find(&msgs)

	// 解析 toolCalls JSON
	type msgOut struct {
		model.LLMMessage
		ToolCallsParsed []gin.H `json:"toolCallsParsed,omitempty"`
	}
	out := make([]msgOut, 0, len(msgs))
	for _, m := range msgs {
		mo := msgOut{LLMMessage: m}
		if m.ToolCalls != "" && m.ToolCalls != "[]" {
			var arr []gin.H
			if json.Unmarshal([]byte(m.ToolCalls), &arr) == nil {
				mo.ToolCallsParsed = arr
			}
		}
		out = append(out, mo)
	}
	c.JSON(http.StatusOK, gin.H{"conversation": conv, "messages": out})
}

// RenameConversation 重命名
func RenameConversation(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需要 title"})
		return
	}
	config.DB.Model(&model.LLMConversation{}).Where("id = ?", id).Update("title", req.Title)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DeleteConversation 删除会话
func DeleteConversation(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	config.DB.Where("conversation_id = ?", id).Delete(&model.LLMMessage{})
	config.DB.Delete(&model.LLMConversation{}, id)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ClearAllConversations 清空所有会话
func ClearAllConversations(c *gin.Context) {
	config.DB.Exec("DELETE FROM llm_messages")
	config.DB.Exec("DELETE FROM llm_conversations")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ============ 对话（带会话持久化）============

// LlmChatV2 支持会话的对话
//
// 请求：{ conversationId?, message }
// 响应：{ conversationId, reply, rounds, toolCalls, totalTokens }
func LlmChatV2(c *gin.Context) {
	avail := llm.AvailableProfiles()
	if len(avail) == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "没有可用的 LLM 配置。请在「LLM 配置」页面添加并启用至少一个配置。",
		})
		return
	}

	var req struct {
		ConversationID uint   `json:"conversationId"`
		Message        string `json:"message"`
		MaxRounds      int    `json:"maxRounds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "需要 message"})
		return
	}
	if req.MaxRounds <= 0 || req.MaxRounds > 8 {
		req.MaxRounds = 5
	}

	// 1) 会话：有则复用，无则新建
	var conv model.LLMConversation
	if req.ConversationID > 0 {
		if err := config.DB.First(&conv, req.ConversationID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "会话不存在"})
			return
		}
	} else {
		title := strings.TrimSpace(req.Message)
		if len([]rune(title)) > 30 {
			title = string([]rune(title)[:30]) + "…"
		}
		conv = model.LLMConversation{Title: title}
		config.DB.Create(&conv)
	}

	// 2) 保存用户消息
	userMsg := model.LLMMessage{
		ConversationID: conv.ID, Role: "user", Content: req.Message,
	}
	config.DB.Create(&userMsg)

	// 3) 组装历史（最近 20 条，避免超长）
	var history []model.LLMMessage
	config.DB.Where("conversation_id = ? AND role IN ?", conv.ID, []string{"user", "assistant"}).
		Order("id DESC").Limit(20).Find(&history)
	// 反转为正序
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}

	msgs := []llm.Message{{Role: "system", Content: llm.SystemPrompt()}}
	for _, h := range history {
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		msgs = append(msgs, llm.Message{Role: h.Role, Content: h.Content})
	}

	// 4) 调用 LLM（含工具循环）
	tools := llm.Tools()
	var trace []gin.H
	totalTokens := 0
	usedProfile := ""

	for round := 0; round < req.MaxRounds; round++ {
		result, err := llm.ChatWithFailover(llm.ChatRequest{Messages: msgs, Tools: tools}, round)
		if err != nil {
			// 保存错误信息到会话，便于用户回看
			config.DB.Create(&model.LLMMessage{
				ConversationID: conv.ID, Role: "assistant",
				Content: "❌ " + err.Error(),
			})
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "trace": trace, "conversationId": conv.ID})
			return
		}

		if result.Usage != nil {
			totalTokens += result.Usage.TotalTokens
		}
		usedProfile = result.Profile.Name

		resp := result.Resp
		if len(resp.Choices) == 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": "LLM 无响应", "conversationId": conv.ID})
			return
		}

		msg := resp.Choices[0].Message
		msgs = append(msgs, msg)

		if len(msg.ToolCalls) == 0 {
			// 最终回答 → 保存
			traceJSON, _ := json.Marshal(trace)
			config.DB.Create(&model.LLMMessage{
				ConversationID: conv.ID, Role: "assistant",
				Content: msg.Content, ToolCalls: string(traceJSON),
				Tokens: totalTokens,
			})
			// 更新会话统计
			var cnt int64
			var sumTokens int64
			config.DB.Model(&model.LLMMessage{}).Where("conversation_id = ?", conv.ID).Count(&cnt)
			config.DB.Model(&model.LLMMessage{}).Where("conversation_id = ?", conv.ID).
				Select("COALESCE(SUM(tokens),0)").Scan(&sumTokens)
			config.DB.Model(&model.LLMConversation{}).Where("id = ?", conv.ID).Updates(map[string]interface{}{
				"message_count": cnt,
				"total_tokens":  sumTokens,
				"profile_name":  usedProfile,
				"updated_at":    time.Now(),
			})

			c.JSON(http.StatusOK, gin.H{
				"conversationId": conv.ID,
				"reply":          msg.Content,
				"rounds":         round + 1,
				"toolCalls":      trace,
				"totalTokens":    totalTokens,
				"usedProfile":    usedProfile,
			})
			return
		}

		for _, tc := range msg.ToolCalls {
			toolResult := llm.Execute(tc.Function.Name, tc.Function.Arguments)
			trace = append(trace, gin.H{"tool": tc.Function.Name, "args": tc.Function.Arguments})
			msgs = append(msgs, llm.Message{
				Role: "tool", Content: toolResult,
				ToolCallID: tc.ID, Name: tc.Function.Name,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"conversationId": conv.ID,
		"reply":          "已达到最大工具调用轮数，请缩小问题范围后重试",
		"rounds":         req.MaxRounds,
		"toolCalls":      trace,
		"totalTokens":    totalTokens,
	})
}
