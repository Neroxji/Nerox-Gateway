package main

import (
	"time"
)

// 发给大模型的请求体
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`

	StreamOptions *StreamOptions `json:"stream_options,omitempty"`

	Temperature     *float32 `json:"temperature,omitempty"`
	TopP            *float32 `json:"top_p,omitempty"`
	MaxTokens       *int     `json:"max_tokens,omitempty"`
	PresencePenalty *float32 `json:"presence_penalty,omitempty"`
}
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// dereference
func (r *ChatRequest) GetTemperature() float32 {
	if r.Temperature != nil {
		return *r.Temperature
	}
	return 1.0 // default
}
func (r *ChatRequest) GetTopP() float32 {
	if r.TopP != nil {
		return *r.TopP
	}
	return 1.0
}

// 大模型发回来的请求体
type OpenAIStreamResponse struct {
	ID                string   `json:"id"`
	Object            string   `json:"object"`
	Created           int64    `json:"created"`
	Model             string   `json:"model"`
	SystemFingerprint string   `json:"system_fingerprint,omitempty"`
	Choices           []Choice `json:"choices"`
	Usage             *Usage   `json:"usage,omitempty"`
}
type Choice struct {
	Index        int     `json:"index"`
	FinishReason *string `json:"finish_reason,omitempty"`
	Delta        Delta   `json:"delta"`
}
type Delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// 发给前端的报错(sse流开始时)
type OpenAIErrorMsg struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   string `json:"param,omitempty"`
		Code    string `json:"code,omitempty"`
	} `json:"error"`
}

// 数据库用户
type User struct {
	ID        uint   `gorm:"primarykey"`
	Username  string `gorm:"uniqueIndex;size:50"`
	Balance   int64  `gorm:"type:bigint;default:0"`
	Status    int    `gorm:"default:1"`
	CreatedAt time.Time
}

// 令牌表
type Token struct {
	ID        uint   `gorm:"primarykey"`
	UserID    uint   `gorm:"index;not null"`
	Name      string `gorm:"size:50;default:'默认密钥'"`
	TokenKey  string `gorm:"uniqueIndex;size:100;not null"`
	Status    int    `gorm:"default:1"`
	CreatedAt time.Time
}

// 数据库日志
type RequestLog struct {
	ID               uint   `gorm:"primarykey"`
	UserID           uint   `gorm:"index"`
	TokenID          uint   `gorm:"index"`
	TargetProvider   string `gorm:"size:20"`
	TargetModel      string `gorm:"size:50"`
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Latency          int64
	StatusCode       int
	ErrorMessage     string    `gorm:"type:text"`
	CreatedAt        time.Time `gorm:"index"`
	CacheHIT         bool      `gorm:"default:0"`
}
