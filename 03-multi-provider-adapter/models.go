package main

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
}
type Choice struct {
	Delta Delta `json:"delta"`
}
type Delta struct {
	Content string `json:"content"`
}
