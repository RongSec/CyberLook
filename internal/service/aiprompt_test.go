package service

// AI Agent 提示词与取消语义的单测。
// 这两块都不依赖网络：提示词是纯字符串校验，取消是 channel 选择。

import (
	"context"
	"strings"
	"testing"
	"time"

	"asset-platform/internal/llm"
)

func TestDefaultPromptRoles(t *testing.T) {
	if DefaultPrompt(RoleJudge) == "" || DefaultPrompt(RoleIdentify) == "" {
		t.Fatal("两个角色的默认提示词都必须非空")
	}
	if DefaultPrompt("unknown") != "" {
		t.Fatal("未知角色应返回空串")
	}
	// 默认提示词必须约束输出格式，否则模型会回散文，extractJSON 解析失败
	for _, role := range []string{RoleJudge, RoleIdentify} {
		p := DefaultPrompt(role)
		if !strings.Contains(p, "只输出一个 JSON") {
			t.Fatalf("%s 提示词缺少输出格式约束", role)
		}
	}
}

func TestSetPromptRejectsBadRole(t *testing.T) {
	if err := SetPrompt("nobody", "x"); err == nil {
		t.Fatal("未知角色必须报错")
	}
}

func TestSetPromptRejectsTooLong(t *testing.T) {
	long := strings.Repeat("很", 8001)
	if err := SetPrompt(RoleJudge, long); err == nil {
		t.Fatal("超长提示词必须报错")
	}
}

// chatWithCtx 必须在 ctx 取消时立即返回，不等 LLM 的 120 秒 HTTP 超时。
// 这条是"单条识别 125 秒"那个事故的回归用例。
func TestChatWithCtxReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 进函数前就已取消

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := chatWithCtx(ctx, llm.ChatRequest{})
		done <- err
	}()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("期望 context.Canceled，实际 %v", err)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("取消应立即返回，实际耗时 %s", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 已取消但 chatWithCtx 没有返回")
	}
}
