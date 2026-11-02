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
	MaxTokens   int     `gorm:"column:max_tokens;default:4096" json:"maxTokens"`
	// ContextWindowK 上下文窗口（K tokens），0 = 默认 128K
	ContextWindowK int `gorm:"column:context_window_k;default:128" json:"contextWindowK"`

	// 轮询与熔断
	Priority        int  `gorm:"column:priority;default:100" json:"priority"`         // 越高越优先
	Enabled         bool `gorm:"column:enabled" json:"enabled"`                        // 是否启用
	PoolExclude     bool `gorm:"column:pool_exclude" json:"poolExclude"`              // 排除出故障轮询链
	FailThreshold   int  `gorm:"column:fail_threshold;default:3" json:"failThreshold"`   // 连续失败熔断阈值
	CooldownSeconds int  `gorm:"column:cooldown_seconds;default:60" json:"cooldownSeconds"` // 熔断冷却秒数
	TimeoutSeconds  int  `gorm:"column:timeout_seconds;default:120" json:"timeoutSeconds"`

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
