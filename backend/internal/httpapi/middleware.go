package httpapi

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func CorsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Headers", "Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func (s *Server) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetHeader("Authorization")
		if apiKey == "" {
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
		log.Println("store user,token")
		c.Next()
	}
}
