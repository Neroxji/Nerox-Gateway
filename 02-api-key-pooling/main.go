package main

import (
	"log"
	"net/http"
	_"net/http/pprof"
)

func main() {

	// 系统初始化
	config, err := LoadConfig("config.json")
	if err != nil {
		log.Fatal("读文件错误:", err)
		return
	}

	keypool := NewKeyPool(config.ApiKeys)

	// 注册路由
	http.HandleFunc("/api/chat", apiChatHandler(keypool))

	// 启动服务
	log.Println("服务器启动在 :8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))

}
