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
	ErrSuccess        = (error)(nil) // no error
	ErrRetryNextKey   = errors.New("try next KEY")
	ErrRetryNextModel = errors.New("try next COMPANY")
)

// get api and try a streaming session
func tryStreamOnce(w http.ResponseWriter, req *http.Request, client http.Client, keypool *KeyPool) (*Usage, string, error) {
	var finalUsage *Usage
	var builder strings.Builder

	// 1
	apiKey := keypool.GetNextKey()

	// 2.1
	req.Header.Set("Authorization", apiKey)

	// 2.2	send a request
	resp, err := client.Do(req)
	if err != nil { // 5-sec timeout OR network disconnected
		log.Println("Request fail:", err)
		return nil, "", ErrRetryNextModel
	}
	defer resp.Body.Close()

	// 2.3	准备流式传输
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Println("不支持流式传输")
		http.Error(w, "不支持流式传输", 500)
		return nil, "", ErrSuccess
	}

	// 2.4	非200状态码返回
	if resp.StatusCode != 200 {
		log.Printf("key错误,状态吗:%d", resp.StatusCode)

		if resp.StatusCode == 429 || resp.StatusCode == 401 {
			return nil, "", ErrRetryNextKey
		}

		// - 5xx直接换厂家
		return nil, "", ErrRetryNextModel
	}

	// 3	sse流式传输 (openai格式)
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			continue
		}

		s := strings.TrimPrefix(line, "data:")
		s = strings.TrimSpace(s)
		if strings.Contains(s, "[DONE]") {
			return finalUsage, builder.String(), ErrSuccess
		}

		// unmarshal response data
		var respData OpenAIStreamResponse
		err := json.Unmarshal([]byte(s), &respData)
		if err != nil {
			log.Println("解析响应json失败:", err)
			errMsg := OpenAIErrorMsg{}
			errMsg.Error.Message = "解析相应失败"
			errMsg.Error.Type = "gateway_error"
			errMsg.Error.Code = "internal_parse_error"
			errBytes, _ := json.Marshal(errMsg)
			fmt.Fprintf(w, "data: %s\n\n", string(errBytes))
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			return nil, "", ErrSuccess
		}

		// - 防守式编程!
		if respData.Usage != nil {
			finalUsage = respData.Usage
		}

		if len(respData.Choices) > 0 { // 防直接panic
			fmt.Fprintf(w, "data: %s\n\n", line)
			builder.WriteString(respData.Choices[0].Delta.Content)
			flusher.Flush()
		}
	}
	if err := scanner.Err(); err != nil {
		log.Println("读取响应流失败:", err)
		errMsg := OpenAIErrorMsg{}
		errMsg.Error.Message = "读取响应流失败"
		errMsg.Error.Type = "gateway_error"
		errMsg.Error.Code = "internal_scanner_error"
		errBytes, _ := json.Marshal(errMsg)
		fmt.Fprintf(w, "data: %s\n\n", string(errBytes))
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()

		// TODO(neroji): 处理流式响应中途异常断开的计费兜底。
		// 风险：若不处理，用户可能通过主动断连来规避大模型长回复的费用。
		return nil, "", ErrSuccess
	}

	// - 网络断开或 Body 读完而结束
	return finalUsage, "", ErrSuccess
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

		user := c.MustGet("currentUser").(User)
		token := c.MustGet("currentToken").(Token)

		// global ratelimiting
		ok, err := checkRateLimit(c.Request.Context(), user.ID)
		if err != nil {
			log.Println(err)
			c.JSON(500, gin.H{"error": "Redis ratelimiting script fail"})
			return
		}
		if !ok {
			log.Println("ratelimiting successfully")
			c.JSON(429, gin.H{"error": "too many request! please try againg for a few seconds"})
			return
		}

		// - SSE 三要素：声明流、禁缓存、保连接
		c.Header("Content-Type", "text/event-stream;charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")

		// 1	解析请求体
		var reqData ChatRequest
		err = c.ShouldBindJSON(&reqData)
		if err != nil {
			log.Println("解码json失败", err)
			c.JSON(400, gin.H{"error": "前端参数不对"})
			return
		}
		reqData.Stream = true

		// -search Redis
		str, hit := getContentCache(c.Request.Context(), reqData)
		if hit {
			flusher, ok := c.Writer.(http.Flusher)
			if !ok {
				log.Println("not supported SSE format")
				http.Error(c.Writer, "not supported SSE format", 500)
				return
			}

			runes := []rune(str)
			chunkSize := 20

			// -public info
			baseResp := OpenAIStreamResponse{
				ID:      "chatcmpl-cache",
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   reqData.Model,
				Choices: []Choice{
					{Index: 0},
				},
			}

			// -setup
			baseResp.Choices[0].Delta = Delta{Role: "assistant"}
			baseResp.Choices[0].FinishReason = nil
			firstChunkByte, _ := json.Marshal(baseResp)
			fmt.Fprintf(c.Writer, "data: %s\n\n", string(firstChunkByte))
			flusher.Flush()

			// -send intermediate block
			for i := 0; i < len(runes); i += chunkSize {
				end := i + chunkSize
				if end > len(runes) {
					end = len(runes)
				}
				chunkStr := string(runes[i:end])
				baseResp.Choices[0].Delta = Delta{Content: chunkStr}
				chunkBytes, _ := json.Marshal(baseResp)
				fmt.Fprintf(c.Writer, "data: %s\n\n", string(chunkBytes))
				flusher.Flush()
			}

			// -send the last block
			stopStr := "stop"
			baseResp.Choices[0].Delta = Delta{}
			baseResp.Choices[0].FinishReason = &stopStr
			lastChunk, _ := json.Marshal(baseResp)
			fmt.Fprintf(c.Writer, "data: %s\n\n", string(lastChunk))
			fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
			flusher.Flush()

			// -send requestlog
			logEntry := RequestLog{
				UserID:      user.ID,
				TargetModel: reqData.Model,
				StatusCode:  200,
				CreatedAt:   time.Now(),
				CacheHIT:    true,
			}
			select {
			case LogChan <- logEntry:
				return
			default:
				log.Println("[警告] 日志队列已满，本次日志被丢弃!")
			}
			return
		}

		// -强制要求厂家在流式下返回 Token 消耗
		reqData.StreamOptions = &StreamOptions{IncludeUsage: true}

		// 2	找到容灾链
		UserModel := reqData.Model // -ensure Cache consistency
		requestModel := reqData.Model
		chain, ok := config.Fallbacks[requestModel]
		if !ok {
			log.Println("没有此模型")
			c.JSON(404, gin.H{"error": "网关没有此模型"})
			return
		}

		// 3  循环容灾链条		循环 1
		for _, targetName := range chain {

			// 3.1	找相关厂家的大模型配置
			currentProvider := config.findProvider(targetName)
			if currentProvider == nil {
				log.Println("没找到厂家的配置?")
				continue
			}
			reqData.Model = currentProvider.Model

			// 3.2	序列化
			jsonData, err := json.Marshal(reqData)
			if err != nil {
				log.Println("转json错误:", err)
				c.JSON(500, gin.H{"error": "服务器内部错误"})
				return
			}

			log.Printf("准备向%s发起请求!\n", currentProvider.Name)

			// 3.3	准备请求
			req, err := http.NewRequestWithContext(c.Request.Context(), "POST", currentProvider.Url, bytes.NewReader(jsonData))
			if err != nil {
				log.Println("创建请求失败:", err)
				c.JSON(500, gin.H{"error": "服务器内部错误"})
				return
			}
			req.Header.Set("Content-Type", "application/json")

			// 4		循环 1.1 循环相关大模型的apiKey
		KeyLoop:
			for i := 0; i < len(currentProvider.Pool.keys); i++ {

				// 4.1计时
				startTime := time.Now()
				usage, str, status := tryStreamOnce(c.Writer, req, client, currentProvider.Pool)
				latancy := time.Since(startTime)

				// 4.2 初始化日志
				var logEntry RequestLog
				logEntry = RequestLog{
					UserID:         user.ID,
					TokenID:        token.ID,
					TargetProvider: currentProvider.Name,
					TargetModel:    currentProvider.Model,
					CreatedAt:      startTime,
					Latency:        int64(latancy.Milliseconds()),
				}

				switch status {
				// -case1
				case ErrSuccess:
					log.Println("请求成功!")

					// -set redis
					reqData.Model = UserModel
					if strings.TrimSpace(str) != "" {
						setContentCache(c.Request.Context(), reqData, str)
					}

					// -计费、写日志 并且 更新数据库
					logEntry.StatusCode = 200 // TODO(neroji):计费没成功没区分开
					if usage != nil {

						// —calculate the multiplier
						cost := (int64(usage.TotalTokens) * currentProvider.PricePerK) / 1000
						if cost == 0 && usage.TotalTokens > 0 {
							cost = 1
						}

						// -calculate balance
						err := DeductBalance(user.ID, cost)
						if err != nil {
							log.Printf("用户ID[%d]扣费失败: %v \n", user.ID, err)
							logEntry.ErrorMessage = "扣费失败!!?"
						} else {
							log.Printf("用户ID[%d]扣费成功: %d \n", user.ID, cost)
						}
						logEntry.PromptTokens = usage.PromptTokens
						logEntry.CompletionTokens = usage.CompletionTokens
						logEntry.TotalTokens = usage.TotalTokens

					} else {
						log.Println("usage 为 nil, 无法计费, 请检查厂商响应格式")
						logEntry.ErrorMessage = "usage没收到!!?"
					}

					// -send "success" log
					select {
					case LogChan <- logEntry:
					default:
						log.Println("[警告] 日志队列已满，本次日志被丢弃!")
					}
					return

				// -case2
				case ErrRetryNextKey:
					log.Println("当前key受限, 换本厂商的下一个key试一下...")
					logEntry.StatusCode = 429
					logEntry.ErrorMessage = "当前apikey受限!!?"
				// -case3
				case ErrRetryNextModel:
					log.Println("当前厂商的服务不稳定，切换到备用厂商...")
					logEntry.StatusCode = 500
					logEntry.ErrorMessage = "当前厂商的服务不稳定!!?"

					// -“不成功”的日志
					select {
					case LogChan <- logEntry:
					default:
						log.Println("[警告] 日志队列已满，本次日志被丢弃!")
					}
					break KeyLoop
				// -default保底
				default:
					log.Println("未知错误，换 Key 试试")
					logEntry.StatusCode = 500
					logEntry.ErrorMessage = "未知错误!!?"
				}

				// -“不成功”的日志
				select {
				case LogChan <- logEntry:
				default:
					log.Println("[警告] 日志队列已满，本次日志被丢弃!")
				}

				// - 确保 Body 每次都是满的
				req.Body = io.NopCloser(bytes.NewReader(jsonData))
			}
			log.Printf("%s 厂商彻底挂了，网关准备启用无缝切换...", targetName)
		}

		c.JSON(500, gin.H{"error": "所有厂商通道全部阻塞，请稍后再试"})
	}
}
