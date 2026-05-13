package main

import (
	"log"
	"net/http"
	_"net/http/pprof"
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

	// 注册路由
	http.HandleFunc("/api/chat", apiChatHandler(config))

	// 启动服务
	log.Println("服务器启动在 :8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))

}
