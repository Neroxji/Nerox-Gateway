package main

import (
    "bytes"
    "encoding/json"
    "net/http/httptest"
    "testing"

    "github.com/gin-gonic/gin"
)

// setupTestRouter 构建和生产完全相同的路由
func setupTestRouter() *gin.Engine {
    gin.SetMode(gin.TestMode)
    r := gin.New() // 用 gin.New() 避免默认日志干扰
    r.Use(CorsMiddleware())

    // 模拟鉴权：直接注入 user（绕过真实 Redis/DB）
    r.Use(func(c *gin.Context) {
        c.Set("currentUser", User{ID: 1, Balance: 9999, Status: 1})
        c.Set("currentToken", Token{ID: 1, UserID: 1})
        c.Next()
    })

    config, _ := LoadConfig("config.json")
    r.POST("/api/v1/chat", apiChatHandler(config))
    return r
}

// TestAuthMiddleware_NoKey：没有 APIKey 应返回 401
func TestAuthMiddleware_NoKey(t *testing.T) {
    initRedis()
    InitDB(loadTestDSN()) // 加载测试 DSN

    gin.SetMode(gin.TestMode)
    r := gin.New()
    r.Use(AuthMiddleware())
    r.POST("/api/v1/chat", func(c *gin.Context) {
        c.JSON(200, gin.H{"ok": true})
    })

    req := httptest.NewRequest("POST", "/api/v1/chat", nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)

    if w.Code != 401 {
        t.Errorf("期望 401，实际 %d", w.Code)
    }
}

// BenchmarkChatHandler_CacheHit：缓存命中路径的基准测试
func BenchmarkChatHandler_CacheHit(b *testing.B) {
    initRedis()
    r := setupTestRouter()

    // 构造请求体
    body, _ := json.Marshal(map[string]interface{}{
        "model": "gpt-4o",
        "messages": []map[string]string{
            {"role": "user", "content": "hello"},
        },
    })

    b.ResetTimer()
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            req := httptest.NewRequest("POST", "/api/v1/chat",
                bytes.NewReader(body))
            req.Header.Set("Content-Type", "application/json")
            req.Header.Set("Authorization", "sk-test-token")
            w := httptest.NewRecorder()
            r.ServeHTTP(w, req)
        }
    })
}


// loadTestDSN 返回用于测试的数据库连接字符串
func loadTestDSN() string {
    return "root:123456@tcp(127.0.0.1:3306)/aigateway_test?charset=utf8mb4&parseTime=True&loc=Local"
}
