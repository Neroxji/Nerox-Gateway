package httpapi

import (
	"log"
	"strconv"
	"time"

	"aigateway/internal/domain"
	"aigateway/internal/security"
	"github.com/gin-gonic/gin"
)

const defaultTokenTTL = 90 * 24 * time.Hour

func (s *Server) DeleteUserHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid ID format. ID must be a positive integer!"})
			return
		}

		updated, err := s.Database.DeactivateUser(uint(id))
		if err != nil {
			log.Printf("database operation failed: %s", err)
			c.JSON(500, gin.H{"error": "something wrong with my database"})
			return
		}
		if !updated {
			c.JSON(404, gin.H{"error": "this user does not exist !?"})
			return
		}

		s.Cache.DeleteUser(c.Request.Context(), uint(id))
		c.JSON(200, gin.H{"message": "delete user successfully!"})
	}
}

func (s *Server) CreateUserHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Username string `json:"username"`
			Balance  int64  `json:"balance"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
			c.JSON(400, gin.H{"error": "Invalid request. Username is required!"})
			return
		}

		user := domain.User{Username: req.Username, Balance: req.Balance, Status: 1}
		if err := s.Database.CreateUser(&user); err != nil {
			log.Printf("database operation failed: %s", err)
			c.JSON(500, gin.H{"error": "something wrong with my database"})
			return
		}

		c.JSON(200, gin.H{
			"message": "create user successfully!",
			"user_id": user.ID,
			"balance": user.Balance,
		})
	}
}

func (s *Server) CreateTokenHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			UserID uint   `json:"user_id"`
			Name   string `json:"name"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.UserID == 0 {
			c.JSON(400, gin.H{"error": "Invalid request. UserID is required!"})
			return
		}

		if _, err := s.Database.FindActiveUser(req.UserID); err != nil {
			c.JSON(404, gin.H{"error": "this user does not exist !?"})
			return
		}

		name := req.Name
		if name == "" {
			name = "默认密钥"
		}
		rawToken, err := security.GenerateToken()
		if err != nil {
			log.Printf("token generation failed: %s", err)
			c.JSON(500, gin.H{"error": "something wrong with token generation"})
			return
		}
		token := domain.Token{
			UserID:    req.UserID,
			Name:      name,
			TokenHash: security.HashToken(rawToken),
			Status:    1,
			ExpiresAt: time.Now().Add(defaultTokenTTL),
		}
		if err := s.Database.CreateToken(&token); err != nil {
			log.Printf("database operation failed: %s", err)
			c.JSON(500, gin.H{"error": "something wrong with my database"})
			return
		}

		c.JSON(200, gin.H{
			"message":   "create token successfully!",
			"token_id":  token.ID,
			"token_key": rawToken,
		})
	}
}

func (s *Server) RevokeTokenHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid ID format. ID must be a positive integer!"})
			return
		}

		token, revoked, err := s.Database.RevokeToken(uint(id))
		if err != nil {
			log.Printf("database operation failed: %s", err)
			c.JSON(500, gin.H{"error": "something wrong with my database"})
			return
		}
		if !revoked {
			c.JSON(404, gin.H{"error": "this token does not exist !?"})
			return
		}

		s.Cache.DeleteTokenHash(c.Request.Context(), token.TokenHash)
		c.JSON(200, gin.H{"message": "revoke token successfully!"})
	}
}
