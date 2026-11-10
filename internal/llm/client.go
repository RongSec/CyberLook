package llm

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// ---- 协议类型 ----

type Message struct {
	Role       string     `json:"role"` // system / user / assistant / tool
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

type ChatResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Index   int     `json:"index"`
		Message Message `json:"message"`
		Reason  string  `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// CallResult 一次调用的完整结果
type CallResult struct {
	Resp      *ChatResponse
	Profile   *model.LLMProfile
	Usage     *model.LLMUsage
	LatencyMs int
}

// ---- 轮询池 ----

var rrCounter atomic.Uint64

// AvailableProfiles 返回按轮询顺序排列的可用配置
//
// 排序规则：
//  1. 只取 enabled=true 且未熔断的
//  2. priority 高的优先
//  3. 同 priority 的按轮询计数器轮换（负载均衡）
//  4. PoolExclude=true 的排除
func AvailableProfiles() []model.LLMProfile {
	var all []model.LLMProfile
	config.DB.Where("enabled = ?", true).Order("priority DESC, id").Find(&all)

	breaker := GlobalBreaker()
	var avail []model.LLMProfile
	var excluded []model.LLMProfile

	for _, p := range all {
		if p.PoolExclude {
			excluded = append(excluded, p)
			continue
		}
		if !breaker.Available(p.ID) {
			continue
		}
		avail = append(avail, p)
	}

	if len(avail) == 0 {
		return nil
	}

	// 同 priority 组内轮换
	sort.SliceStable(avail, func(i, j int) bool { return avail[i].Priority > avail[j].Priority })

	// 找到最高 priority 的组，轮换
	maxPri := avail[0].Priority
	var head, rest []model.LLMProfile
	for _, p := range avail {
		if p.Priority == maxPri {
			head = append(head, p)
		} else {
			rest = append(rest, p)
		}
	}
	if len(head) > 1 {
		start := int(rrCounter.Add(1)) % len(head)
		head = append(head[start:], head[:start]...)
	}
	return append(head, rest...)
}

// ChatWithFailover 按轮询链依次尝试，成功即返回
func ChatWithFailover(req ChatRequest, round int) (*CallResult, error) {
	profiles := AvailableProfiles()
	if len(profiles) == 0 {
		return nil, ErrNoProfile
	}

	var lastErr error
	for i := range profiles {
		p := &profiles[i]
		result, err := callOne(p, req, round)
		if err == nil {
			return result, nil
		}
		lastErr = err
		// 继续下一个
	}
	return nil, fmt.Errorf("所有 LLM 配置均失败，最后一个错误: %w", lastErr)
}

// buildClient 构造 HTTP 客户端
func buildClient(p *model.LLMProfile) *http.Client {
	timeout := time.Duration(p.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   timeout,
	}
}

// normalizeBaseURL 补全 BaseURL
//
// 支持这些写法：
//   https://api.xxx.com          → https://api.xxx.com/v1
//   https://api.xxx.com/v1       → 原样
//   https://api.xxx.com/v1/      → 去掉末尾斜杠
//   https://api.xxx.com/chat/completions → 去掉末尾路径
func normalizeBaseURL(raw string) string {
	u := strings.TrimSpace(raw)
	u = strings.TrimRight(u, "/")
	// 已包含 /chat/completions 的去掉
	u = strings.TrimSuffix(u, "/chat/completions")
	if u == "" {
		return u
	}
	// 没有 /v1、/v2 等版本路径的补 /v1
	lower := strings.ToLower(u)
	if !strings.Contains(lower, "/v1") && !strings.Contains(lower, "/v2") &&
		!strings.Contains(lower, "/v3") && !strings.Contains(lower, "/openai") {
		u += "/v1"
	}
	return u
}

// callOne 调用单个 profile，记录用量与熔断
func callOne(p *model.LLMProfile, req ChatRequest, round int) (*CallResult, error) {
	start := time.Now()
	breaker := GlobalBreaker()

	req.Model = p.Model
	if req.Temperature == 0 {
		req.Temperature = p.Temperature
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = p.MaxTokens
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 8192 // 默认 8K，推理模型需要足够空间
	}

	body, _ := json.Marshal(req)
	url := normalizeBaseURL(p.BaseURL) + "/chat/completions"

	headers := map[string]string{"Content-Type": "application/json"}
	if p.APIKey != "" {
		headers["Authorization"] = "Bearer " + p.APIKey
	}
	if p.Provider == "anthropic" {
		headers["x-api-key"] = p.APIKey
		headers["anthropic-version"] = "2023-06-01"
	}

	statusCode, respBody, err := postChat(p, url, headers, body)
	if err != nil {
		breaker.Fail(p.ID, err, p.FailThreshold, p.CooldownSeconds, false)
		recordUsage(p, 0, 0, 0, int(time.Since(start).Milliseconds()), "error", err.Error(), round)
		return nil, fmt.Errorf("[%s] 请求失败: %w", p.Name, err)
	}
	latency := int(time.Since(start).Milliseconds())

	if statusCode != 200 {
		msg := string(respBody)
		if len(msg) > 500 {
			msg = msg[:500]
		}
		deterministic := IsDeterministic(statusCode, msg)
		breaker.Fail(p.ID, fmt.Errorf("HTTP %d", statusCode), p.FailThreshold, p.CooldownSeconds, deterministic)
		recordUsage(p, 0, 0, latency, latency, "error", msg, round)
		return nil, fmt.Errorf("[%s] HTTP %d: %s", p.Name, statusCode, msg)
	}

	var out ChatResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		breaker.Fail(p.ID, err, p.FailThreshold, p.CooldownSeconds, false)
		return nil, fmt.Errorf("[%s] 解析响应失败: %w", p.Name, err)
	}
	if out.Error != nil {
		deterministic := IsDeterministic(400, out.Error.Message)
		breaker.Fail(p.ID, fmt.Errorf("%s", out.Error.Message), p.FailThreshold, p.CooldownSeconds, deterministic)
		recordUsage(p, 0, 0, latency, latency, "error", out.Error.Message, round)
		return nil, fmt.Errorf("[%s] %s", p.Name, out.Error.Message)
	}

	// 成功
	breaker.Success(p.ID, time.Since(start))

	in, outTok := 0, 0
	if out.Usage != nil {
		in = out.Usage.PromptTokens
		outTok = out.Usage.CompletionTokens
	}
	usage := recordUsage(p, in, outTok, latency, latency, "ok", "", round)

	return &CallResult{Resp: &out, Profile: p, Usage: usage, LatencyMs: latency}, nil
}

// recordUsage 写用量记录
func recordUsage(p *model.LLMProfile, inTok, outTok, latency1, latencyMs int, status, errMsg string, round int) *model.LLMUsage {
	u := &model.LLMUsage{
		ProfileID:    p.ID,
		ProfileName:  p.Name,
		Model:        p.Model,
		InputTokens:  inTok,
		OutputTokens: outTok,
		TotalTokens:  inTok + outTok,
		LatencyMs:    latencyMs,
		Status:       status,
		ErrorMsg:     errMsg,
		Round:        round,
	}
	if err := config.DB.Create(u).Error; err != nil {
		// 用量记录失败不影响主流程
		_ = err
	}
	return u
}

// TestProfile 测试单个配置连通性（不写用量、不影响熔断）
func TestProfile(p *model.LLMProfile) (int, string, error) {
	req := ChatRequest{
		Model: p.Model,
		Messages: []Message{
			{Role: "user", Content: "只回复两个字母：OK"},
		},
		MaxTokens: 200,
	}
	body, _ := json.Marshal(req)
	url := normalizeBaseURL(p.BaseURL) + "/chat/completions"
	headers := map[string]string{"Content-Type": "application/json"}
	if p.APIKey != "" {
		headers["Authorization"] = "Bearer " + p.APIKey
	}

	start := time.Now()
	statusCode, b, err := postChat(p, url, headers, body)
	if err != nil {
		return 0, "", err
	}
	latency := int(time.Since(start).Milliseconds())

	if statusCode != 200 {
		msg := string(b)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return latency, msg, fmt.Errorf("HTTP %d", statusCode)
	}
	var out ChatResponse
	json.Unmarshal(b, &out)
	reply := ""
	if len(out.Choices) > 0 {
		reply = out.Choices[0].Message.Content
	}
	return latency, reply, nil
}
