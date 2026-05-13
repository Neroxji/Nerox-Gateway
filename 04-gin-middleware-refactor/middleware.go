package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 跨域中间件
func CorsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 全局通用跨域
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Headers", "Content-Type")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// 鉴权中间件
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1
		apiKey := c.GetHeader("Authorization")
		if apiKey == "" {
			c.AbortWithStatusJSON(401, gin.H{"error": "缺少apiKey!"})
			return
		}

		// 2	token鉴权
		var token Token
		result := DB.Where("token_key=? AND status=?", apiKey, 1).First(&token)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			c.AbortWithStatusJSON(401, gin.H{"error": "查不到该apiKey!"})
			return
		}

		// 2.1	user鉴权
		var user User
		result = DB.Where("id=?", token.UserID).First(&user)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			c.AbortWithStatusJSON(500, gin.H{"error": "查不到该用户!"})
			return
		}
		if user.Status == 0 {
			c.AbortWithStatusJSON(403, gin.H{"error": "用户被封禁!"})
			return
		}
		if user.Balance <= 0 {
			c.AbortWithStatusJSON(402, gin.H{"error": "用户已欠费!"})
			return
		}

		// 3
		c.Set("currentToken", token)
		c.Set("currentUser", user)
		fmt.Println("已存入user")

		c.Next()

	}
}
