package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// ChatRequest 是发给大模型的请求体
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// 大模型发回来的请求体
type ChatResponse struct {
	Choices []Choice `json:"choices"`
}
type Choice struct {
	Delta Delta `json:"delta"`
}
type Delta struct {
	Content string `json:"content"`
}

func main() {
	apiChat := func(w http.ResponseWriter, r *http.Request) {

		// 新协议对接，发给前端
		w.Header().Set("Content-Type", "text/event-stream;charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*") //本地写html用
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions { // 预防浏览器的跨域安全机制(cors)
			return
		}

		// 1.1
		var reqData ChatRequest
		err := json.NewDecoder(r.Body).Decode(&reqData) //接前端的数据
		if err != nil {
			log.Println("解码json失败", err) // log是对内的
			http.Error(w, "前端参数不对", 400) // 这个是对用户/前端的
			return
		}
		reqData.Model = "deepseek-chat"
		reqData.Stream = true

		// 1.2	*序列化转成json
		jsonData, err := json.Marshal(reqData)
		if err != nil {
			log.Println("转json错误:", err)
			http.Error(w, "服务器内部错误", 500)
			return
		}

		// 2.1
		url := "https://api.deepseek.com/chat/completions"
		apiKey := "---"
		fmt.Println("准备向deepseek发起请求!")

		// 2.2
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			log.Println("创建请求失败:", err)
			http.Error(w, "服务器内部错误", 500)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", apiKey)

		// 2.3	发车并把大模型回信带回来
		client := http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			log.Println("请求deepseek失败:", err)
			http.Error(w, "调用上游AI接口失败", 502)
			return
		}
		defer resp.Body.Close() // 关掉通信管子

		// 2.4	开启flusher模式，一步步吐给前端
		flusher, ok := w.(http.Flusher)
		if !ok {
			log.Println("不支持流式传输")
			http.Error(w, "不支持流式传输", 500)
			return // 记得加记得加记得加！！！！！！
		}

		// 2.5
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()

			if line == "" {
				continue
			}

			s := strings.TrimPrefix(line, "data:")
			if s == " [DONE]" {
				return
			}

			var respData ChatResponse
			err := json.Unmarshal([]byte(s), &respData)
			if err != nil {
				log.Println("解析响应json失败:", err)
				// SSE 流已开始，Header 发不出去了，用 SSE 事件格式通知前端
				fmt.Fprintf(w, "event: error\ndata: 解析响应失败\n\n")
				flusher.Flush()
				return
			}
			// fmt.Print(respData.Choices[0].Delta.Content) 不是cs模式
			// w.Write([]byte(respData.Choices[0].Delta.Content))
			io.WriteString(w, respData.Choices[0].Delta.Content)

			flusher.Flush()

		}
		if err := scanner.Err(); err != nil {
			log.Println("读取响应流失败:", err)
			// 同上用sse传回
			fmt.Fprintf(w, "event: error\ndata: 读取响应流失败\n\n")
			flusher.Flush()
		}
		// 正常结束：deepseek 已发完 [DONE]，或 scanner 无错误地读完

	}

	// 3.1
	http.HandleFunc("/api/chat", apiChat)
	log.Fatal(http.ListenAndServe(":8080", nil))

}
