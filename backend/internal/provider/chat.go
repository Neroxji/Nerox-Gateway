package provider

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"aigateway/internal/domain"
)

var (
	ErrSuccess        = (error)(nil)
	ErrRetryNextKey   = errors.New("try next KEY")
	ErrRetryNextModel = errors.New("try next COMPANY")
)

// StreamOnce sends one request to an upstream provider and forwards its SSE
// response to the client. The caller decides whether to retry the key or the
// provider based on the returned sentinel error.
func StreamOnce(w http.ResponseWriter, req *http.Request, client http.Client, keyPool *KeyPool) (*domain.Usage, string, error) {
	var finalUsage *domain.Usage
	var builder strings.Builder

	req.Header.Set("Authorization", keyPool.GetNextKey())
	resp, err := client.Do(req)
	if err != nil {
		log.Println("Request fail:", err)
		return nil, "", ErrRetryNextModel
	}
	defer resp.Body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Println("不支持流式传输")
		http.Error(w, "不支持流式传输", http.StatusInternalServerError)
		return nil, "", ErrSuccess
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("key错误,状态码:%d", resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusUnauthorized {
			return nil, "", ErrRetryNextKey
		}
		return nil, "", ErrRetryNextModel
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if strings.Contains(data, "[DONE]") {
			return finalUsage, builder.String(), ErrSuccess
		}

		var response domain.OpenAIStreamResponse
		if err := json.Unmarshal([]byte(data), &response); err != nil {
			log.Println("解析响应 JSON 失败:", err)
			writeStreamError(w, flusher, "解析相应失败", "internal_parse_error")
			return nil, "", ErrSuccess
		}

		if response.Usage != nil {
			finalUsage = response.Usage
		}
		if len(response.Choices) > 0 {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
			builder.WriteString(response.Choices[0].Delta.Content)
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		log.Println("读取响应流失败:", err)
		writeStreamError(w, flusher, "读取响应流失败", "internal_scanner_error")
	}
	return finalUsage, "", ErrSuccess
}

func writeStreamError(w http.ResponseWriter, flusher http.Flusher, message, code string) {
	errMsg := domain.OpenAIErrorMsg{}
	errMsg.Error.Message = message
	errMsg.Error.Type = "gateway_error"
	errMsg.Error.Code = code
	errBytes, _ := json.Marshal(errMsg)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", string(errBytes))
	_, _ = fmt.Fprintln(w, "data: [DONE]\n")
	flusher.Flush()
}
