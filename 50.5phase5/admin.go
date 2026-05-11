package main

import (
	_ "fmt"
	"log"
	"strconv"

	"github.com/gin-gonic/gin"
)

// delete user
func DeleteUserHandler() gin.HandlerFunc {
	return func(c *gin.Context) {

		// 1
		idStr:=c.Param(":id")
		id,err:=strconv.ParseUint(idStr, 10, 64)
		if err!=nil{
			c.JSON(400, gin.H{"error":"Invalid ID format. ID must be a positive integer!"})
			return 
		}
		ID:=uint(id)

		// 2
		result:=DB.Model(&User{}).Where("id=?", ID).Update("status", false)
		if result.Error!=nil{
			log.Printf("datebase conduct failed! error:%s", result.Error)
			c.JSON(500, gin.H{"error":"something wrong with my datebase"})
			return
		}
		if result.RowsAffected==0{
			c.JSON(404,gin.H{"error":"this user does not exist !?"})
			return 
		}

		// 3
		deleteUserCache(c.Request.Context(), ID)
	}
}
