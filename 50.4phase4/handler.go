package main

import (
	"bufio"
	"bytes"
	_ "context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	ErrSuccess        = (error)(nil) // 没错误
	ErrRetryNextKey   = errors.New("试试下一个key")
	ErrRetryNextModel = errors.New("试是下一个厂家")
)

// 跨域中间件
func CorsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 全局通用跨域
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Headers", "Content-Type")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// 拿到api尝试一次流式传输
func tryStreamOnce(w http.ResponseWriter, req *http.Request, client http.Client, keypool *KeyPool) error {
	// 1.1
	apiKey := keypool.GetNextKey()

	// 2.2.1
	req.Header.Set("Authorization", apiKey)

	// 2.3
	resp, err := client.Do(req)
	if err != nil { // 5秒超时到了，或者网络全断
		log.Println("请求失败:", err)
		return ErrRetryNextModel
	}
	defer resp.Body.Close()

	// 2.4
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Println("不支持流式传输")
		http.Error(w, "不支持流式传输", 500)
		return ErrSuccess
	}

	// 2.5
	if resp.StatusCode != 200 {
		log.Printf("key错误,状态吗:%d", resp.StatusCode)

		if resp.StatusCode == 429 || resp.StatusCode == 401 {
			return ErrRetryNextKey
		}

		// 5xx直接换厂家
		return ErrRetryNextModel
	}

	// 2.6
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			continue
		}

		s := strings.TrimPrefix(line, "data:")
		if s == " [DONE]" {
			return ErrSuccess
		}

		var respData ChatResponse
		err := json.Unmarshal([]byte(s), &respData)
		if err != nil {
			log.Println("解析响应json失败:", err)
			fmt.Fprintf(w, "event: error\ndata: 解析响应失败\n\n")
			flusher.Flush()
			return ErrSuccess
		}

		if len(respData.Choices) > 0 { // 防直接panic
			io.WriteString(w, respData.Choices[0].Delta.Content)
			flusher.Flush()
		}
	}
	if err := scanner.Err(); err != nil {
		log.Println("读取响应流失败:", err)
		fmt.Fprintf(w, "event: error\ndata: 读取响应流失败\n\n")
		flusher.Flush()
		return ErrSuccess
	}

	return ErrSuccess
}

// apiChatHandler 返回一个处理 /api/chat 请求的函数
func apiChatHandler(config *Config) gin.HandlerFunc {

	tr := http.Transport{
		ResponseHeaderTimeout: 5 * time.Second,
	}
	client := http.Client{
		Transport: &tr,
	} //	全局client

	return func(c *gin.Context) {
		// 1.1
		var reqData ChatRequest
		err := c.ShouldBindJSON(&reqData)
		if err != nil {
			log.Println("解码json失败", err)
			c.JSON(400, gin.H{"error": "前端参数不对"})
			return
		}
		reqData.Stream = true

		// 1.1.1
		requestModel := reqData.Model
		chain, ok := config.Fallbacks[requestModel]
		if !ok {
			log.Println("没有此模型")
			c.JSON(404, gin.H{"error": "网关没有此模型"})
			return
		}

		// SSE 三要素：声明流、禁缓存、保连接
		c.Header("Content-Type", "text/event-stream;charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")

		// 循环链条		循环 1
		for _, targetName := range chain {

			// 1.1.2
			currentProvider := config.findProvider(targetName)
			if currentProvider == nil {
				log.Println("没找到厂家的配置?")
				continue
			}
			reqData.Model = currentProvider.Model

			// 1.2
			jsonData, err := json.Marshal(reqData)
			if err != nil {
				log.Println("转json错误:", err)
				c.JSON(500, gin.H{"error": "服务器内部错误"})
				return
			}

			// 2.1
			log.Printf("准备向%s发起请求!\n", currentProvider.Name)

			// 2.2
			req, err := http.NewRequestWithContext(c.Request.Context(), "POST", currentProvider.Url, bytes.NewReader(jsonData))
			if err != nil {
				log.Println("创建请求失败:", err)
				c.JSON(500, gin.H{"error": "服务器内部错误"})
				return
			}
			req.Header.Set("Content-Type", "application/json")

			// 3.1		循环 1.1
		KeyLoop:
			for i := 0; i < len(currentProvider.Pool.keys); i++ {

				status := tryStreamOnce(c.Writer, req, client, currentProvider.Pool)

				switch status {
				case ErrSuccess:
					log.Println("请求成功!")
					return
				case ErrRetryNextKey:
					log.Println("当前key受限, 换本厂商的下一个key试一下...")
				case ErrRetryNextModel:
					// log.Println("当前厂商的服务不稳定，切换到备用厂商...")
					break KeyLoop
				default:
					log.Println("未知错误，换 Key 试试")
				}

				// 确保 Body 每次都是满的
				req.Body = io.NopCloser(bytes.NewReader(jsonData))
			}
			log.Printf("%s 厂商彻底挂了，网关准备启用无缝切换...", targetName)
		}

		c.JSON(500, gin.H{"error": "所有厂商通道全部阻塞，请稍后再试"})
	}
}
