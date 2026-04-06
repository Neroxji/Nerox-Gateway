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

// 拿到api尝试一次流式传输
func tryStreamOnce(w http.ResponseWriter, req *http.Request, client http.Client, keypool *KeyPool) (*Usage, error) {
	var finalUsage *Usage

	// 1.1
	apiKey := keypool.GetNextKey()

	// 2.2.1
	req.Header.Set("Authorization", apiKey)

	// 2.3	发请求
	resp, err := client.Do(req)
	if err != nil { // 5秒超时到了，或者网络全断
		log.Println("请求失败:", err)
		return nil, ErrRetryNextModel
	}
	defer resp.Body.Close()

	// 2.4	准备流式传输
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Println("不支持流式传输")
		http.Error(w, "不支持流式传输", 500)
		return nil, ErrSuccess
	}

	// 2.5	非200状态码返回
	if resp.StatusCode != 200 {
		log.Printf("key错误,状态吗:%d", resp.StatusCode)

		if resp.StatusCode == 429 || resp.StatusCode == 401 {
			return nil, ErrRetryNextKey
		}

		// - 5xx直接换厂家
		return nil, ErrRetryNextModel
	}

	// 2.6	流式传输
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			continue
		}

		s := strings.TrimPrefix(line, "data:")
		if strings.Contains(s, "[DONE]") {
			return finalUsage, ErrSuccess
		}

		// 反序列化
		var respData ChatResponse
		err := json.Unmarshal([]byte(s), &respData)
		if err != nil {
			log.Println("解析响应json失败:", err)
			fmt.Fprintf(w, "event: error\ndata: 解析响应失败\n\n")
			flusher.Flush()
			return nil, ErrSuccess
		}

		// - 防守式编程!
		if respData.Usage != nil {
			finalUsage = respData.Usage
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

		// TODO(neroji): 处理流式响应中途异常断开的计费兜底。
		// 风险：若不处理，用户可能通过主动断连来规避大模型长回复的费用。
		return nil, ErrSuccess
	}

	// - 网络断开或 Body 读完而结束
	return finalUsage, ErrSuccess
}

// apiChatHandler 返回一个处理 /api/chat 请求的函数
func apiChatHandler(config *Config) gin.HandlerFunc {

	tr := http.Transport{
		ResponseHeaderTimeout: 5 * time.Second,
	}
	client := http.Client{
		Transport: &tr,
	} // - 全局client

	return func(c *gin.Context) {
		// 1.1	解析请求体
		var reqData ChatRequest
		err := c.ShouldBindJSON(&reqData)
		if err != nil {
			log.Println("解码json失败", err)
			c.JSON(400, gin.H{"error": "前端参数不对"})
			return
		}
		reqData.Stream = true

		// 1.1.1	找到容灾链
		requestModel := reqData.Model
		chain, ok := config.Fallbacks[requestModel]
		if !ok {
			log.Println("没有此模型")
			c.JSON(404, gin.H{"error": "网关没有此模型"})
			return
		}

		// - SSE 三要素：声明流、禁缓存、保连接
		c.Header("Content-Type", "text/event-stream;charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")

		// 循环容灾链条		循环 1
		for _, targetName := range chain {

			// 1.1.2	找相关厂家的大模型配置
			currentProvider := config.findProvider(targetName)
			if currentProvider == nil {
				log.Println("没找到厂家的配置?")
				continue
			}
			reqData.Model = currentProvider.Model

			// 1.2	序列化
			jsonData, err := json.Marshal(reqData)
			if err != nil {
				log.Println("转json错误:", err)
				c.JSON(500, gin.H{"error": "服务器内部错误"})
				return
			}

			// 2.1
			log.Printf("准备向%s发起请求!\n", currentProvider.Name)

			// 2.2	准备请求
			req, err := http.NewRequestWithContext(c.Request.Context(), "POST", currentProvider.Url, bytes.NewReader(jsonData))
			if err != nil {
				log.Println("创建请求失败:", err)
				c.JSON(500, gin.H{"error": "服务器内部错误"})
				return
			}
			req.Header.Set("Content-Type", "application/json")

			// 3.1		循环 1.1 循环相关大模型的apiKey
		KeyLoop:
			for i := 0; i < len(currentProvider.Pool.keys); i++ {

				usage, status := tryStreamOnce(c.Writer, req, client, currentProvider.Pool)

				switch status {
				case ErrSuccess:
					log.Println("请求成功!")

					// 计费并且更新数据库
					if usage != nil {
						// 4.1
						user := c.MustGet("currentUser").(User)

						// 4.2.1
						cost := (int64(usage.TotalTokens) * currentProvider.PricePerK) / 1000
						if cost == 0 && usage.TotalTokens > 0 {
							cost = 1
						}

						// 4.2.2
						err := DeductBalance(user.ID, cost)
						if err != nil {
							log.Printf("用户ID[%d]扣费失败: %v \n", user.ID, err)
						} else {
							log.Printf("用户ID[%d]扣费成功: %d \n", user.ID, cost)
						}
					} else {
						log.Println("usage 为 nil，无法计费，请检查厂商响应格式")
					}
					return
				case ErrRetryNextKey:
					log.Println("当前key受限, 换本厂商的下一个key试一下...")
				case ErrRetryNextModel:
					log.Println("当前厂商的服务不稳定，切换到备用厂商...")
					break KeyLoop
				default:
					log.Println("未知错误，换 Key 试试")
				}

				// - 确保 Body 每次都是满的
				req.Body = io.NopCloser(bytes.NewReader(jsonData))
			}
			log.Printf("%s 厂商彻底挂了，网关准备启用无缝切换...", targetName)
		}

		c.JSON(500, gin.H{"error": "所有厂商通道全部阻塞，请稍后再试"})
	}
}
