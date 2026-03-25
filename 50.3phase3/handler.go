package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

)

func tryStreamOnce(w http.ResponseWriter, req *http.Request, client http.Client, keypool *KeyPool) bool {
	// 1.3 轮询拿到api
	apiKey := keypool.GetNextKey()

	// 2.2.1
	req.Header.Set("Authorization", apiKey)

	// 2.3
	resp, err := client.Do(req) // api被限流了err管不着
	if err != nil {
		log.Println("请求deepseek失败:", err)
		// http.Error(w, "调用上游AI接口失败", 502)
		return false
	}
	defer resp.Body.Close()

	// 2.4
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Println("不支持流式传输")
		// http.Error(w, "不支持流式传输", 500)
		return false
	}

	if resp.StatusCode == 200 {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()

			if line == "" {
				continue
			}

			s := strings.TrimPrefix(line, "data:")
			if s == " [DONE]" {
				return true
			}

			var respData ChatResponse
			err := json.Unmarshal([]byte(s), &respData)
			if err != nil {
				log.Println("解析响应json失败:", err)
				fmt.Fprintf(w, "event: error\ndata: 解析响应失败\n\n")
				flusher.Flush()
				return false
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
			return false
		}
	}

	log.Printf("Key %s 请求失败，状态码: %d,准备重试...", apiKey[len(apiKey)-6:], resp.StatusCode)

	return false
}

// apiChatHandler 返回一个处理 /api/chat 请求的函数
func apiChatHandler(keypool *KeyPool) http.HandlerFunc {

	client := http.Client{} //	全局client

	return func(w http.ResponseWriter, r *http.Request) {

		w.Header().Set("Content-Type", "text/event-stream;charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			return
		}

		// 1.1
		var reqData ChatRequest
		err := json.NewDecoder(r.Body).Decode(&reqData)
		if err != nil {
			log.Println("解码json失败", err)
			http.Error(w, "前端参数不对", 400)
			return
		}
		// reqData.Model = "deepseek-chat" 可以自选
		reqData.Stream = true

		// 1.2
		jsonData, err := json.Marshal(reqData)
		if err != nil {
			log.Println("转json错误:", err)
			http.Error(w, "服务器内部错误", 500)
			return
		}

		// 2.1
		url := "https://api.deepseek.com/chat/completions"
		fmt.Println("准备向deepseek发起请求!")

		// 2.2
		req, err := http.NewRequestWithContext(r.Context(),"POST", url, bytes.NewReader(jsonData))
		if err != nil {
			log.Println("创建请求失败:", err)
			http.Error(w, "服务器内部错误", 500)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		for i := 0; i < len(keypool.keys); i++ {
			// 传递5秒的时间
			ctx,cancel:=context.WithTimeout(r.Context(), 5*time.Second)
			reqC:=req.Clone(ctx)
			success:=tryStreamOnce(w, reqC, client, keypool)

			cancel()	// 手动关闭

			if success{
				return 
			}

			// 确保 Body 每次都是满的
			req.Body = io.NopCloser(bytes.NewReader(jsonData))
		}

		http.Error(w, "服务器内部问题, 无api可用", 500)
	}
}
