package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"aigateway/internal/config"
	"aigateway/internal/domain"
	"github.com/gin-gonic/gin"
)

func setupTestRouter(server *Server) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CorsMiddleware())
	router.Use(func(c *gin.Context) {
		c.Set("currentUser", domain.User{ID: 1, Balance: 9999, Status: 1})
		c.Set("currentToken", domain.Token{ID: 1, UserID: 1})
		c.Next()
	})
	router.POST("/api/v1/chat", server.ChatHandler())
	return router
}

func TestAuthMiddleware_NoKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	server := &Server{}
	router.Use(server.AuthMiddleware())
	router.POST("/api/v1/chat", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	req := httptest.NewRequest("POST", "/api/v1/chat", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != 401 {
		t.Errorf("期望 401，实际 %d", response.Code)
	}
}

func BenchmarkChatHandler_CacheHit(b *testing.B) {
	server := &Server{Config: &config.Config{Fallbacks: map[string][]string{}}}
	router := setupTestRouter(server)
	body, _ := json.Marshal(map[string]interface{}{
		"model": "gpt-4o",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("POST", "/api/v1/chat", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "sk-test-token")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
		}
	})
}

func loadTestDSN() string {
	return "root:123456@tcp(127.0.0.1:3306)/aigateway_test?charset=utf8mb4&parseTime=True&loc=Local"
}
