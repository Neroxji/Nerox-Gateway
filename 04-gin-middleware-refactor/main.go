package main

import (
	"context"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

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
	for i := 0; i < len(config.Providers); i++ {
		config.Providers[i].Pool = NewKeyPool(config.Providers[i].Keys)
	}

	// 启动并连接数据库
	InitDB(config.DSN)

	// 启动日志系统
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go logworker(&wg)
	}

	// 注册gin引擎
	r := gin.Default()

	// 挂载中间件
	r.Use(CorsMiddleware())
	r.Use(AuthMiddleware())

	// 注册路由
	r.POST("/api/chat", apiChatHandler(config))

	// 启动服务
	srv := &http.Server{
		Addr:    ":8080",
		Handler: r,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("网关启动异常: %s\n", err)
		}
	}()

	// 主线程等关闭	 
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM) 
	<-quit	// 阻塞等待
	log.Println("检测到退出信号，正在关闭服务...")
	// 优雅关闭
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("服务器强行关闭异常:", err)
	}
	
	close(LogChan)
	wg.Wait()
	log.Println("所有日志已安全入库。")

	log.Println("✅ 网关已平安退出，再见！")
}
