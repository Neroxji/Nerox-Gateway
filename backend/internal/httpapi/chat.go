package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"aigateway/internal/config"
	"aigateway/internal/domain"
	"aigateway/internal/provider"
	"aigateway/internal/storage"
	"github.com/gin-gonic/gin"
)

type Server struct {
	Config   *config.Config
	Database *storage.Database
	Cache    *storage.Cache
	Client   http.Client
}

func NewServer(cfg *config.Config, database *storage.Database, cache *storage.Cache) *Server {
	transport := &http.Transport{ResponseHeaderTimeout: 5 * time.Second}
	return &Server{
		Config:   cfg,
		Database: database,
		Cache:    cache,
		Client:   http.Client{Transport: transport},
	}
}

func (s *Server) Register(router *gin.Engine) {
	router.Use(CorsMiddleware())

	v1 := router.Group("/api/v1")
	v1.Use(s.AuthMiddleware())
	v1.POST("/chat", s.ChatHandler())

	admin := router.Group("/admin")
	admin.DELETE("/users/:id", s.DeleteUserHandler())
	admin.POST("/users/create", s.CreateUserHandler())
	admin.POST("/tokens/create", s.CreateTokenHandler())
}

func (s *Server) ChatHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := c.MustGet("currentUser").(domain.User)
		token := c.MustGet("currentToken").(domain.Token)

		allowed, err := s.Cache.CheckRateLimit(c.Request.Context(), user.ID)
		if err != nil {
			log.Println(err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Redis ratelimiting script fail"})
			return
		}
		if !allowed {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many request! please try againg for a few seconds"})
			return
		}

		c.Header("Content-Type", "text/event-stream;charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")

		var request domain.ChatRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			log.Println("解码json失败", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "前端参数不对"})
			return
		}
		request.Stream = true

		if content, hit := s.Cache.GetContent(c.Request.Context(), request); hit {
			s.writeCachedStream(c, request.Model, content, user.ID)
			return
		}

		request.StreamOptions = &domain.StreamOptions{IncludeUsage: true}
		userModel := request.Model
		fallbacks, ok := s.Config.Fallbacks[request.Model]
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "网关没有此模型"})
			return
		}

		for _, providerName := range fallbacks {
			current := s.Config.FindProvider(providerName)
			if current == nil || current.Pool == nil {
				continue
			}
			request.Model = current.Model

			payload, err := json.Marshal(request)
			if err != nil {
				log.Println("转json错误:", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
				return
			}

			upstreamRequest, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, current.Url, bytes.NewReader(payload))
			if err != nil {
				log.Println("创建请求失败:", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "服务器内部错误"})
				return
			}
			upstreamRequest.Header.Set("Content-Type", "application/json")

			tryProvider := false
			for i := 0; i < current.Pool.Len(); i++ {
				start := time.Now()
				usage, content, status := provider.StreamOnce(c.Writer, upstreamRequest, s.Client, current.Pool)
				logEntry := domain.RequestLog{
					UserID:         user.ID,
					TokenID:        token.ID,
					TargetProvider: current.Name,
					TargetModel:    current.Model,
					CreatedAt:      start,
					Latency:        time.Since(start).Milliseconds(),
				}

				switch status {
				case provider.ErrSuccess:
					request.Model = userModel
					if strings.TrimSpace(content) != "" {
						s.Cache.SetContent(c.Request.Context(), request, content)
					}
					logEntry.StatusCode = http.StatusOK
					if usage != nil {
						cost := int64(usage.TotalTokens) * current.PricePerK / 1000
						if cost == 0 && usage.TotalTokens > 0 {
							cost = 1
						}
						if err := s.Database.DeductBalance(user.ID, cost); err != nil {
							logEntry.ErrorMessage = "扣费失败!!?"
							log.Printf("用户ID[%d]扣费失败: %v", user.ID, err)
						}
						logEntry.PromptTokens = usage.PromptTokens
						logEntry.CompletionTokens = usage.CompletionTokens
						logEntry.TotalTokens = usage.TotalTokens
					} else {
						logEntry.ErrorMessage = "usage没收到!!?"
					}
					s.Database.EnqueueLog(logEntry)
					return

				case provider.ErrRetryNextKey:
					logEntry.StatusCode = http.StatusTooManyRequests
					logEntry.ErrorMessage = "当前apikey受限!!?"
				case provider.ErrRetryNextModel:
					logEntry.StatusCode = http.StatusInternalServerError
					logEntry.ErrorMessage = "当前厂商的服务不稳定!!?"
					tryProvider = true
				default:
					logEntry.StatusCode = http.StatusInternalServerError
					logEntry.ErrorMessage = "未知错误!!?"
				}

				s.Database.EnqueueLog(logEntry)
				if tryProvider {
					break
				}
				upstreamRequest.Body = io.NopCloser(bytes.NewReader(payload))
			}
		}

		c.JSON(http.StatusInternalServerError, gin.H{"error": "所有厂商通道全部阻塞，请稍后再试"})
	}
}

func (s *Server) writeCachedStream(c *gin.Context, model, content string, userID uint) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		http.Error(c.Writer, "not supported SSE format", http.StatusInternalServerError)
		return
	}

	response := domain.OpenAIStreamResponse{
		ID:      "chatcmpl-cache",
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []domain.Choice{{Index: 0}},
	}
	response.Choices[0].Delta = domain.Delta{Role: "assistant"}
	writeChunk := func() {
		data, _ := json.Marshal(response)
		_, _ = c.Writer.Write([]byte("data: " + string(data) + "\n\n"))
		flusher.Flush()
	}
	writeChunk()

	runes := []rune(content)
	for i := 0; i < len(runes); i += 20 {
		end := i + 20
		if end > len(runes) {
			end = len(runes)
		}
		response.Choices[0].Delta = domain.Delta{Content: string(runes[i:end])}
		writeChunk()
	}

	stop := "stop"
	response.Choices[0].Delta = domain.Delta{}
	response.Choices[0].FinishReason = &stop
	writeChunk()
	_, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()

	s.Database.EnqueueLog(domain.RequestLog{
		UserID:      userID,
		TargetModel: model,
		StatusCode:  http.StatusOK,
		CreatedAt:   time.Now(),
		CacheHIT:    true,
	})
}
