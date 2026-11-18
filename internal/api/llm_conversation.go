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

// ============ 会话索引 ============
//
// 口径（v0.0.47 起）：**一次问答 = 一条会话，一次识别 = 一条会话**。
// 不再把整个任务的识别记录塞进同一条会话——那种大会话既没法看（几千条），
// 也没必要（识别记录本来就不进 LLM 上下文）。
//
// 因此左侧树只到「任务 → 类型」两层，类型是叶子；点类型进去看该类型下的记录，
// 一条一条翻（最新在前）。历史遗留的大会话在迁移 046 里按「两两一问一答」拆开。

// KindLabel 会话类型的中文名
func KindLabel(k string) string {
	switch k {
	case "chat":
		return "对话"
	case "svc_ident":
		return "服务识别"
	case "svc_review":
		return "服务准确性判别"
	case "render":
		return "渲染复核"
	case "task":
		return "任务会话"
	}
	return k
}

// ListConversations 无 keyword 返回聚合；有 keyword 返回扁平命中列表
func ListConversations(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	keyword := strings.TrimSpace(c.Query("keyword"))
	kind := strings.TrimSpace(c.Query("kind"))
	taskID, _ := strconv.Atoi(c.DefaultQuery("taskId", "0"))

	if keyword != "" {
		listConversationsByKeyword(c, keyword, kind, taskID, limit)
		return
	}

	type grow struct {
		TaskID uint      `json:"taskId"`
		Kind   string    `json:"kind"`
		Count  int       `json:"count"`
		LastID uint      `json:"lastId"`
		LastAt time.Time `json:"lastAt"`
	}
	q := config.DB.Model(&model.LLMConversation{}).
		Select("task_id, kind, count(*) AS count, max(id) AS last_id, max(updated_at) AS last_at")
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if taskID > 0 {
		q = q.Where("task_id = ?", taskID)
	}
	var rows []grow
	q.Group("task_id, kind").Order("max(updated_at) DESC").Scan(&rows)

	// 任务名一次查回来
	taskNames := map[uint]string{}
	var ids []uint
	for _, r := range rows {
		if r.TaskID > 0 {
			ids = append(ids, r.TaskID)
		}
	}
	if len(ids) > 0 {
		type trow struct {
			ID   uint
			Name string
		}
		var trs []trow
		config.DB.Table("scan_tasks").Select("id, name").Where("id IN ?", ids).Scan(&trs)
		for _, t := range trs {
			taskNames[t.ID] = t.Name
		}
	}

	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		if r.Kind == "" {
			r.Kind = "chat"
		}
		out = append(out, gin.H{
			"taskId":   r.TaskID,
			"taskName": taskNames[r.TaskID],
			"kind":     r.Kind,
			"label":    KindLabel(r.Kind),
			"count":    r.Count,
			"lastId":   r.LastID,
			"lastAt":   r.LastAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}

// convHit 搜索命中的那条消息
type convHit struct {
	ID      uint
	Snippet string
}

func listConversationsByKeyword(c *gin.Context, keyword, kind string, taskID int, limit int) {
	q := config.DB.Model(&model.LLMConversation{})
	q = q.Where("title ILIKE ? OR id IN (SELECT conversation_id FROM llm_messages WHERE content ILIKE ?)",
		"%"+keyword+"%", "%"+keyword+"%")
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if taskID > 0 {
		q = q.Where("task_id = ?", taskID)
	}
	var list []model.LLMConversation
	q.Order("updated_at DESC").Limit(limit).Find(&list)

	// 命中消息：点开直接跳到那一条
	hits := map[uint]convHit{}
	if len(list) > 0 {
		ids := make([]uint, 0, len(list))
		for _, it := range list {
			ids = append(ids, it.ID)
		}
		type hrow struct {
			ConversationID uint
			ID             uint
			Content        string
		}
		var hrs []hrow
		config.DB.Raw(`SELECT conversation_id, id, content FROM llm_messages
			WHERE conversation_id IN ? AND content ILIKE ? ORDER BY conversation_id, id`,
			ids, "%"+keyword+"%").Scan(&hrs)
		for _, r := range hrs {
			if _, ok := hits[r.ConversationID]; ok {
				continue
			}
			hits[r.ConversationID] = convHit{ID: r.ID, Snippet: snippetAround(r.Content, keyword, 60)}
		}
	}

	taskNames := map[uint]string{}
	var tids []uint
	for _, it := range list {
		if it.TaskID > 0 {
			tids = append(tids, it.TaskID)
		}
	}
	if len(tids) > 0 {
		type trow struct {
			ID   uint
			Name string
		}
		var trs []trow
		config.DB.Table("scan_tasks").Select("id, name").Where("id IN ?", tids).Scan(&trs)
		for _, t := range trs {
			taskNames[t.ID] = t.Name
		}
	}

	items := make([]gin.H, 0, len(list))
	for _, it := range list {
		k := it.Kind
		if k == "" {
			k = "chat"
		}
		row := gin.H{
			"id": it.ID, "title": it.Title, "kind": k, "label": KindLabel(k),
			"taskId": it.TaskID, "taskName": taskNames[it.TaskID],
			"messageCount": it.MessageCount, "updatedAt": it.UpdatedAt,
		}
		if h, ok := hits[it.ID]; ok {
			row["hitId"] = h.ID
			row["hitSnippet"] = h.Snippet
		}
		items = append(items, row)
	}
	c.JSON(http.StatusOK, gin.H{"mode": "search", "count": len(items), "items": items})
}

// ListConversationGroup 某任务某类型下的记录（最新在前，分页）
// GET /llm/conversation-group?taskId=&kind=&offset=&limit=
func ListConversationGroup(c *gin.Context) {
	taskID, _ := strconv.Atoi(c.DefaultQuery("taskId", "0"))
	kind := strings.TrimSpace(c.DefaultQuery("kind", "chat"))
	if kind == "" {
		kind = "chat"
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	q := config.DB.Model(&model.LLMConversation{}).Where("task_id = ? AND kind = ?", taskID, kind)
	var total int64
	q.Count(&total)

	var list []model.LLMConversation
	q.Order("id DESC").Offset(offset).Limit(limit).Find(&list)

	// 每条的一行摘要（取该会话的第一条 user 消息）
	sums := map[uint]string{}
	if len(list) > 0 {
		ids := make([]uint, 0, len(list))
		for _, it := range list {
			ids = append(ids, it.ID)
		}
		type srow struct {
			ConversationID uint
			Content        string
		}
		var srs []srow
		config.DB.Raw(`SELECT DISTINCT ON (conversation_id) conversation_id, content
			FROM llm_messages WHERE conversation_id IN ? AND role = 'user'
			ORDER BY conversation_id, id`, ids).Scan(&srs)
		for _, s := range srs {
			sums[s.ConversationID] = oneLineSnippet(s.Content, 90)
		}
	}

	items := make([]gin.H, 0, len(list))
	for _, it := range list {
		items = append(items, gin.H{
			"id": it.ID, "title": it.Title, "kind": kind, "label": KindLabel(kind),
			"taskId": it.TaskID, "messageCount": it.MessageCount,
			"updatedAt": it.UpdatedAt, "summary": sums[it.ID],
		})
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "offset": offset, "limit": limit, "items": items})
}

// oneLineSnippet 取首行/前 N 个字符
func oneLineSnippet(s string, n int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
	rs := []rune(s)
	if len(rs) > n {
		return string(rs[:n]) + "…"
	}
	return string(rs)
}

// snippetAround 取关键词前后的上下文，压成一行给列表用
func snippetAround(s, kw string, width int) string {
	rs := []rune(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	idx := -1
	lowS, lowK := strings.ToLower(string(rs)), strings.ToLower(kw)
	if p := strings.Index(lowS, lowK); p >= 0 {
		idx = len([]rune(lowS[:p]))
	}
	if idx < 0 {
		if len(rs) > width {
			return string(rs[:width]) + "…"
		}
		return string(rs)
	}
	start := idx - width/2
	if start < 0 {
		start = 0
	}
	end := start + width
	if end > len(rs) {
		end = len(rs)
	}
	out := string(rs[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(rs) {
		out += "…"
	}
	return out
}

// CreateConversation 新建会话
func CreateConversation(c *gin.Context) {
	var req struct {
		Title  string `json:"title"`
		Kind   string `json:"kind"`
		TaskID uint   `json:"taskId"`
	}
	_ = c.ShouldBindJSON(&req)
	conv := model.LLMConversation{Title: req.Title}
	if conv.Title == "" {
		conv.Title = "新会话"
	}
	conv.Kind = strings.TrimSpace(req.Kind)
	if conv.Kind == "" {
		conv.Kind = "chat"
	}
	conv.TaskID = req.TaskID
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

// ============ 对话（一次提问一条会话）============
//
// 不带上下文：每次提问独立成会话，system + 本条消息直接发给模型。
// 单次 token 稳定、不受历史污染，也不会攒出几千条消息的巨型会话。

// LlmChatV2 一次性问答
//
// 请求：{ message, maxRounds? }
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
		ConversationID uint   `json:"conversationId"` // 兼容旧调用方，忽略
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

	// 一次提问 = 一条会话
	title := strings.TrimSpace(req.Message)
	if len([]rune(title)) > 30 {
		title = string([]rune(title)[:30]) + "…"
	}
	conv := model.LLMConversation{Title: title, Kind: "chat"}
	if err := config.DB.Create(&conv).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	userMsg := model.LLMMessage{ConversationID: conv.ID, Role: "user", Content: req.Message}
	config.DB.Create(&userMsg)

	msgs := []llm.Message{
		{Role: "system", Content: llm.SystemPrompt()},
		{Role: "user", Content: req.Message},
	}

	tools := llm.Tools()
	var trace []gin.H
	totalTokens := 0
	usedProfile := ""

	for round := 0; round < req.MaxRounds; round++ {
		result, err := llm.ChatWithFailover(llm.ChatRequest{Messages: msgs, Tools: tools}, round)
		if err != nil {
			config.DB.Create(&model.LLMMessage{
				ConversationID: conv.ID, Role: "assistant", Content: "❌ " + err.Error(),
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
			traceJSON, _ := json.Marshal(trace)
			config.DB.Create(&model.LLMMessage{
				ConversationID: conv.ID, Role: "assistant",
				Content: msg.Content, ToolCalls: string(traceJSON), Tokens: totalTokens,
			})
			config.DB.Model(&model.LLMConversation{}).Where("id = ?", conv.ID).Updates(map[string]interface{}{
				"message_count": 2,
				"total_tokens":  totalTokens,
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
