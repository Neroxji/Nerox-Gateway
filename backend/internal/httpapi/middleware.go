package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"aigateway/internal/security"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func CorsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func (s *Server) AdminAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		provided, ok := security.ParseBearerToken(c.GetHeader("Authorization"))
		if !ok || s.AdminAPIKey == "" || !sameSecret(provided, s.AdminAPIKey) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未授权"})
			return
		}
		c.Next()
	}
}

func (s *Server) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey, ok := security.ParseBearerToken(c.GetHeader("Authorization"))
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "缺少apiKey!"})
			return
		}

		token, hit := s.Cache.GetToken(c.Request.Context(), apiKey)
		if !hit {
			var err error
			token, err = s.Database.FindToken(apiKey)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "查不到该apiKey!"})
				return
			}
			if err != nil {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "鉴权服务异常"})
				return
			}
			s.Cache.SetToken(c.Request.Context(), apiKey, token)
		}
		if token.Status != 1 || (!token.ExpiresAt.IsZero() && !time.Now().Before(token.ExpiresAt)) {
			s.Cache.DeleteToken(c.Request.Context(), apiKey)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "apiKey 已失效!"})
			return
		}

		user, hit := s.Cache.GetUser(c.Request.Context(), token.UserID)
		if !hit {
			var err error
			user, err = s.Database.FindUser(token.UserID)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "查不到该用户!"})
				return
			}
			if err != nil {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "用户服务异常"})
				return
			}
			s.Cache.SetUser(c.Request.Context(), token.UserID, user)
		}
		if user.Status == 0 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "用户被封禁!"})
			return
		}
		if user.Balance <= 0 {
			c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{"error": "用户已欠费!"})
			return
		}

		c.Set("currentToken", token)
		c.Set("currentUser", user)
		c.Next()
	}
}

func sameSecret(provided, expected string) bool {
	providedHash := sha256.Sum256([]byte(provided))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}
