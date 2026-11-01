package main

import (
	"context"
	"expvar"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
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

	if dsn := os.Getenv("DSN"); dsn != "" {
		cfg.DSN = dsn
		log.Println("使用环境变量 DSN")
	}
	if cfg.DSN == "" {
		log.Fatal("DSN 环境变量或配置文件是必需的")
	}
	if os.Getenv("ADMIN_API_KEY") == "" {
		log.Fatal("ADMIN_API_KEY 环境变量是必需的")
	}
	if err := cfg.ValidateProviderURLs(os.Getenv("ALLOW_INSECURE_PROVIDER_HTTP") == "1"); err != nil {
		log.Fatal("Provider 配置无效:", err)
	}

	for i := range cfg.Providers {
		cfg.Providers[i].Pool = provider.NewKeyPool(cfg.Providers[i].Keys)
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

	// 诊断入口：pprof 和 expvar 注册在 DefaultServeMux 上，业务 gin router 不承载。
	// 容器内绑 :6060（绑 127.0.0.1 会使 Compose 端口映射无法转发到容器回环），
	// 对外仅通过 Compose 发布的 127.0.0.1 地址访问。
	// 阻塞 profile 默认关闭（采样率为 0），必须显式开启，否则 /debug/pprof/block 采不到数据。
	runtime.SetBlockProfileRate(1)
	if sqlDB, err := database.DB.DB(); err == nil {
		expvar.Publish("db_pool", expvar.Func(func() any { return sqlDB.Stats() }))
	} else {
		log.Printf("db_pool 指标不可用: %s", err)
	}
	diagServer := &http.Server{Addr: ":6060", ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := diagServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("诊断端口启动失败: %s", err)
		}
	}()

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
