package main

import (
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// delete user
func DeleteUserHandler() gin.HandlerFunc {
	return func(c *gin.Context) {

		// 1
		idStr := c.Param(":id")
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid ID format. ID must be a positive integer!"})
			return
		}
		ID := uint(id)

		// 2
		result := DB.Model(&User{}).Where("id=?", ID).Update("status", false)
		if result.Error != nil {
			log.Printf("datebase conduct failed! error:%s", result.Error)
			c.JSON(500, gin.H{"error": "something wrong with my datebase"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(404, gin.H{"error": "this user does not exist !?"})
			return
		}

		// 3
		deleteUserCache(c.Request.Context(), ID)
	}
}

// create user
func CreateUserHandler() gin.HandlerFunc {
	return func(c *gin.Context) {

		// 1
		var req struct {
			Username string `json:"username"`
			Balance  int64  `json:"balance"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
			c.JSON(400, gin.H{"error": "Invalid request. Username is required!"})
			return
		}

		// 2
		user := User{
			Username: req.Username,
			Balance:  req.Balance,
			Status:   1,
		}
		result := DB.Create(&user)
		if result.Error != nil {
			log.Printf("datebase conduct failed! error:%s", result.Error)
			c.JSON(500, gin.H{"error": "something wrong with my datebase"})
			return
		}

		// 3
		c.JSON(200, gin.H{
			"message": "create user successfully!",
			"user_id": user.ID,
			"balance": req.Balance,
		})
	}
}

// create token
func CreateTokenHandler() gin.HandlerFunc {
	return func(c *gin.Context) {

		// 1
		var req struct {
			UserID uint   `json:"user_id"`
			Name   string `json:"name"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.UserID == 0 {
			c.JSON(400, gin.H{"error": "Invalid request. UserID is required!"})
			return
		}

		// 2  check user exist
		var user User
		result := DB.Where("id=? AND status=?", req.UserID, 1).First(&user)
		if result.Error != nil {
			c.JSON(404, gin.H{"error": "this user does not exist !?"})
			return
		}

		// 3  generate token key
		name := req.Name
		if name == "" {
			name = "默认密钥"
		}
		tokenKey := fmt.Sprintf("sk-%s%d", strconv.FormatInt(rand.New(rand.NewSource(time.Now().UnixNano())).Int63(), 36), time.Now().Unix())

		// 4
		token := Token{
			UserID:   req.UserID,
			Name:     name,
			TokenKey: tokenKey,
			Status:   1,
		}
		result = DB.Create(&token)
		if result.Error != nil {
			log.Printf("datebase conduct failed! error:%s", result.Error)
			c.JSON(500, gin.H{"error": "something wrong with my datebase"})
			return
		}

		// 5
		c.JSON(200, gin.H{
			"message":   "create token successfully!",
			"token_id":  token.ID,
			"token_key": token.TokenKey,
		})
	}
}
