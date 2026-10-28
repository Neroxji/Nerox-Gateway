package httpapi

import (
	"net/http/httptest"
	"testing"

	"aigateway/internal/domain"
	"aigateway/internal/storage"
	"github.com/gin-gonic/gin"
)

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

func BenchmarkWriteCachedStream(b *testing.B) {
	database := &storage.Database{LogChan: make(chan domain.RequestLog, 1)}
	server := &Server{Database: database}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		server.writeCachedStream(context, "gpt-4o", "cached response", 1)
		select {
		case <-database.LogChan:
		default:
		}
	}
}
