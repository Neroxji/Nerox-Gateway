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

	"aigateway/internal/config"
	"aigateway/internal/httpapi"
	"aigateway/internal/provider"
	"aigateway/internal/storage"
	"github.com/gin-gonic/gin"
)

func main() {
	cfg, err := config.LoadConfig("config.json")
	if err != nil {
		log.Fatal("读取配置文件失败:", err)
	}

	for i := range cfg.Providers {
		cfg.Providers[i].Pool = provider.NewKeyPool(cfg.Providers[i].Keys)
	}
	if dsn := os.Getenv("DSN"); dsn != "" {
		cfg.DSN = dsn
		log.Println("使用环境变量 DSN")
	}

	database, err := storage.OpenDatabase(cfg.DSN)
	if err != nil {
		log.Fatal(err)
	}
	cache, err := storage.OpenCacheFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	defer cache.Close()

	var workers sync.WaitGroup
	database.StartLogWorkers(&workers, 5)

	router := gin.Default()
	server := httpapi.NewServer(cfg, database, cache)
	server.Register(router)

	serverHTTP := &http.Server{Addr: ":8080", Handler: router}
	go func() {
		if err := serverHTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("网关启动异常: %s", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("检测到退出信号，正在关闭服务...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := serverHTTP.Shutdown(ctx); err != nil {
		log.Fatal("服务器强行关闭异常:", err)
	}

	database.CloseLogs()
	workers.Wait()
	log.Println("所有日志已安全入库。")
}
