package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"asset-platform/internal/model"
)

// ---- 应用层代理（解决 Docker Desktop 等环境下 TLS 握手被中断的问题）----

type proxyRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type proxyResponse struct {
	Status int               `json:"status"`
	Body   string            `json:"body"`
	Error  string            `json:"error,omitempty"`
}

// postChat 统一的 POST 入口：配了 ViaProxy 就走代理，否则直连
func postChat(p *model.LLMProfile, url string, headers map[string]string, body []byte) (int, []byte, error) {
	if p.ViaProxy != "" {
		preq := proxyRequest{URL: url, Method: "POST", Headers: headers, Body: string(body)}
		payload, _ := json.Marshal(preq)
		client := &http.Client{Timeout: 180 * time.Second}
		resp, err := client.Post(strings.TrimRight(p.ViaProxy, "/")+"/fetch", "application/json", bytes.NewReader(payload))
		if err != nil {
			return 0, nil, fmt.Errorf("代理请求失败: %w", err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var pr proxyResponse
		if err := json.Unmarshal(data, &pr); err != nil {
			return 0, nil, fmt.Errorf("代理响应解析失败: %w", err)
		}
		if pr.Error != "" {
			return 0, nil, fmt.Errorf("代理执行失败: %s", pr.Error)
		}
		return pr.Status, []byte(pr.Body), nil
	}

	client := buildClient(p)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}
