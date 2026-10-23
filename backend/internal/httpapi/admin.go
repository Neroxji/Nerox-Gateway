package httpapi

import (
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"time"

	"aigateway/internal/domain"
	"github.com/gin-gonic/gin"
)

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
		token := domain.Token{
			UserID:   req.UserID,
			Name:     name,
			TokenKey: fmt.Sprintf("sk-%s%d", strconv.FormatInt(rand.New(rand.NewSource(time.Now().UnixNano())).Int63(), 36), time.Now().Unix()),
			Status:   1,
		}
		if err := s.Database.CreateToken(&token); err != nil {
			log.Printf("database operation failed: %s", err)
			c.JSON(500, gin.H{"error": "something wrong with my database"})
			return
		}

		c.JSON(200, gin.H{
			"message":   "create token successfully!",
			"token_id":  token.ID,
			"token_key": token.TokenKey,
		})
	}
}
