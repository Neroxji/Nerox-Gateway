package main

import (
	"time"
)

// 发给大模型的请求体
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// 大模型发回来的请求体
type ChatResponse struct {
	Choices []Choice `json:"choices"`
	Usage   *Usage
}
type Choice struct {
	Delta Delta `json:"delta"`
}
type Delta struct {
	Content string `json:"content"`
}
type Usage struct { // 网关的计费
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
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
}
