// Package llm 提供 LLM 对话能力，支持 Function Calling 操作资产平台全部数据
package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Config LLM 配置（从环境变量读取）
type Config struct {
	BaseURL string // LLM API 地址，如 http://llm.internal:8000/v1
	APIKey  string
	Model   string
	Timeout time.Duration
}

// LoadConfig 从环境变量加载
func LoadConfig() Config {
	cfg := Config{
		BaseURL: os.Getenv("LLM_BASE_URL"),
		APIKey:  os.Getenv("LLM_API_KEY"),
		Model:   os.Getenv("LLM_MODEL"),
		Timeout: 120 * time.Second,
	}
	if cfg.Model == "" {
		cfg.Model = "qwen2.5-32b-instruct"
	}
	return cfg
}

// Enabled 是否已配置
func (c Config) Enabled() bool {
	return c.BaseURL != ""
}

// ---- OpenAI 兼容协议 ----

type Message struct {
	Role       string     `json:"role"` // system / user / assistant / tool
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // function
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
}

type ChatResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Index   int     `json:"index"`
		Message Message `json:"message"`
		Reason  string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Chat 调用 LLM
func Chat(cfg Config, req ChatRequest) (*ChatResponse, error) {
	if !cfg.Enabled() {
		return nil, fmt.Errorf("LLM 未配置（设置 LLM_BASE_URL 环境变量）")
	}
	req.Model = cfg.Model

	body, _ := json.Marshal(req)
	url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("LLM 请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("LLM 返回 %d: %s", resp.StatusCode, string(respBody[:min(500, len(respBody))]))
	}

	var out ChatResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("解析 LLM 响应失败: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("LLM 错误: %s", out.Error.Message)
	}
	return &out, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
