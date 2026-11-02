package model

import "time"

// LLMProfile 一个 LLM 配置
type LLMProfile struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"column:name;size:100" json:"name"`
	Provider string `gorm:"column:provider;size:20;default:openai" json:"provider"` // openai | anthropic | ollama | azure
	BaseURL  string `gorm:"column:base_url;size:500" json:"baseUrl"`
	APIKey   string `gorm:"column:api_key;size:500" json:"-"`       // 不序列化
	APIKeyHint string `gorm:"column:api_key_hint;size:50" json:"apiKeyHint"` // sk-xxx...abcd
	Model    string `gorm:"column:model;size:100" json:"model"`

	// 生成参数
	Temperature float64 `gorm:"column:temperature;default:0.3" json:"temperature"`
	MaxTokens   int     `gorm:"column:max_tokens" json:"maxTokens"`  // 0 = 用默认 8192
	// ContextWindowK 上下文窗口（K tokens），0 = 默认 128K
	ContextWindowK int `gorm:"column:context_window_k;default:128" json:"contextWindowK"`

	// 轮询与熔断
	Priority        int  `gorm:"column:priority" json:"priority"`  // 0-10，越小越优先（0 最先尝试）
	Enabled         bool `gorm:"column:enabled" json:"enabled"`                        // 是否启用
	PoolExclude     bool `gorm:"column:pool_exclude" json:"poolExclude"`              // 排除出故障轮询链
	FailThreshold   int  `gorm:"column:fail_threshold;default:3" json:"failThreshold"`   // 连续失败熔断阈值
	CooldownSeconds int  `gorm:"column:cooldown_seconds;default:60" json:"cooldownSeconds"` // 熔断冷却秒数
	TimeoutSeconds  int  `gorm:"column:timeout_seconds" json:"timeoutSeconds"`

	// TLS 兼容（内网 LLM 网关常见需要）
	TLSSkipVerify bool `gorm:"column:tls_skip_verify" json:"tlsSkipVerify"` // 跳过证书校验
	// ViaProxy 应用层代理地址（如 http://host.docker.internal:8898）
	// 用途：Docker Desktop 等环境下 TLS 握手会被 VM 网络丢弃，此时让宿主机代理完成 TLS
	ViaProxy string `gorm:"column:via_proxy;size:200" json:"viaProxy"`
	TLSMinVersion string `gorm:"column:tls_min_version;size:10" json:"tlsMinVersion"` // ""=默认，tls1.0/tls1.2

	// 运行时状态（不入库，由熔断器填充）
	BreakerOpen   bool   `gorm:"-" json:"breakerOpen"`
	OpenUntil     string `gorm:"-" json:"openUntil,omitempty"`
	ConsecFails   int    `gorm:"-" json:"consecFails"`
	LastError     string `gorm:"-" json:"lastError,omitempty"`
	LastLatencyMs int    `gorm:"-" json:"lastLatencyMs,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (LLMProfile) TableName() string { return "llm_profiles" }

// LLMUsage 每次 LLM 调用的用量记录
type LLMUsage struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	ProfileID    uint      `gorm:"column:profile_id;index" json:"profileId"`
	ProfileName  string    `gorm:"column:profile_name;size:100" json:"profileName"`
	Model        string    `gorm:"column:model;size:100" json:"model"`
	InputTokens  int       `gorm:"column:input_tokens;default:0" json:"inputTokens"`
	OutputTokens int       `gorm:"column:output_tokens;default:0" json:"outputTokens"`
	TotalTokens  int       `gorm:"column:total_tokens;default:0" json:"totalTokens"`
	LatencyMs    int       `gorm:"column:latency_ms;default:0" json:"latencyMs"`
	Status       string    `gorm:"column:status;size:20" json:"status"` // ok | error
	ErrorMsg     string    `gorm:"column:error_msg;size:1000" json:"errorMsg,omitempty"`
	// Round 是对话内的第几轮工具调用（便于分析）
	Round     int       `gorm:"column:round;default:0" json:"round"`
	CreatedAt time.Time `gorm:"column:created_at;index" json:"createdAt"`
}

func (LLMUsage) TableName() string { return "llm_usages" }

// LLMConversation 会话
type LLMConversation struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Title        string    `gorm:"column:title;size:200" json:"title"`
	ProfileName  string    `gorm:"column:profile_name;size:100" json:"profileName"`
	MessageCount int       `gorm:"column:message_count;default:0" json:"messageCount"`
	TotalTokens  int       `gorm:"column:total_tokens;default:0" json:"totalTokens"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func (LLMConversation) TableName() string { return "llm_conversations" }

// LLMMessage 会话消息
type LLMMessage struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ConversationID uint      `gorm:"column:conversation_id;index" json:"conversationId"`
	Role           string    `gorm:"column:role;size:20" json:"role"` // user / assistant
	Content        string    `gorm:"column:content;type:text" json:"content"`
	ToolCalls      string    `gorm:"column:tool_calls;type:text" json:"toolCalls"` // JSON 数组
	Tokens         int       `gorm:"column:tokens;default:0" json:"tokens"`
	CreatedAt      time.Time `json:"createdAt"`
}

func (LLMMessage) TableName() string { return "llm_messages" }
