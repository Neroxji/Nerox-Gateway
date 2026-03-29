package main

import (
	"log"
	_"net/http"
	_"net/http/pprof"

	"github.com/gin-gonic/gin"
)

func main() {

	// 读取json文件
	config, err := LoadConfig("config.json")
	if err != nil {
		log.Fatal("读文件错误:", err)
		return
	}

	// 初始化keypool
	for i:=0;i<len(config.Providers);i++{
		config.Providers[i].Pool=NewKeyPool(config.Providers[i].Keys)
	}

	// 注册gin引擎
	r:=gin.Default()

	// 挂载中间件
	r.Use(CorsMiddleware())

	// 注册路由
	r.POST("/api/chat", apiChatHandler(config))	

	// 启动服务
	log.Println("服务器启动在 :8080...")
	r.Run(":8080")

}
