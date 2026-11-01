// e13_loadgen.go — E13 并发压测负载生成器（实验脚本，不属于业务代码）
//
// 用法示例：
//
//	go run e13_loadgen.go -mode hit  -c 4  -n 200 -token $GATEWAY_TEST_TOKEN -label e13-hit-c4
//	go run e13_loadgen.go -mode miss -c 16 -n 200 -token $GATEWAY_TEST_TOKEN -label e13-miss-c16 -csv miss_c16.csv
//
// 四种模式：
//
//	hit       先发 1 条预热（不计入统计），之后所有请求使用同一内容 → 缓存命中路径
//	miss      每个请求内容唯一 → 缓存未命中，走 Provider 回源
//	fallback  客户端行为同 miss；路径差异由 Fake Provider 配置（primary=500）决定
//	ratelimit 客户端行为同 miss；配合预置限流计数使用，预期全部 429
//
// 说明：
//   - TTFB：从发出请求到收到第一个响应字节，与结果文档的 TTFB 口径一致
//   - 期望状态码：hit/miss/fallback 期望 200；ratelimit 期望 429
//   - miss/fallback 每个请求会扣费、写日志、写缓存；hit 模式预热会扣 1 次
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	status  int
	ttfbMs  float64
	totalMs float64
	err     string
}

func main() {
	url := flag.String("url", "http://127.0.0.1:18080/api/v1/chat", "网关 chat 接口")
	token := flag.String("token", os.Getenv("GATEWAY_TEST_TOKEN"), "Gateway Token")
	mode := flag.String("mode", "miss", "hit|miss|fallback|ratelimit")
	conc := flag.Int("c", 1, "并发数")
	total := flag.Int("n", 100, "总请求数")
	model := flag.String("model", "e1-model", "模型名")
	label := flag.String("label", defaultLabel(), "内容前缀，保证跨轮内容唯一")
	tokensFile := flag.String("tokens-file", "", "多用户 Token 文件（每行一个），轮转使用以分摊限流；ratelimit 轮不要传")
	csvPath := flag.String("csv", "", "逐请求明细 CSV 输出文件（可选）")
	timeout := flag.Int("timeout", 30, "单请求超时秒数")
	flag.Parse()

	tokens := []string{*token}
	if *tokensFile != "" {
		data, err := os.ReadFile(*tokensFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "读取 tokens 文件失败:", err)
			os.Exit(1)
		}
		tokens = nil
		for _, line := range strings.Split(string(data), "\n") {
			if t := strings.TrimSpace(line); t != "" {
				tokens = append(tokens, t)
			}
		}
		if len(tokens) == 0 {
			fmt.Fprintln(os.Stderr, "tokens 文件为空")
			os.Exit(1)
		}
	} else if *token == "" {
		fmt.Fprintln(os.Stderr, "缺少 token：用 -token 传入或设置环境变量 GATEWAY_TEST_TOKEN")
		os.Exit(1)
	}

	client := &http.Client{
		Timeout: time.Duration(*timeout) * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: *conc + 16,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	content := fmt.Sprintf("%s-%s", *label, *mode)
	if *mode == "hit" {
		r := doRequest(client, *url, tokens[0], *model, content)
		if r.err != "" || r.status != 200 {
			fmt.Fprintf(os.Stderr, "预热失败：%+v\n", r)
			os.Exit(1)
		}
		fmt.Println("预热完成（1 条未命中，不计入统计），开始正式压测")
	}

	var tokenIdx atomic.Int64
	pickToken := func() string {
		if len(tokens) == 1 {
			return tokens[0]
		}
		return tokens[int(tokenIdx.Add(1)-1)%len(tokens)]
	}

	results := make([]result, *total)
	seqs := make(chan int, *total)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < *conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for seq := range seqs {
				body := content
				if *mode != "hit" {
					body = fmt.Sprintf("%s-%s-%d", *label, *mode, seq)
				}
				results[seq] = doRequest(client, *url, pickToken(), *model, body)
			}
		}()
	}
	for i := 0; i < *total; i++ {
		seqs <- i
	}
	close(seqs)
	wg.Wait()
	wall := time.Since(start)

	printSummary(*mode, *conc, *total, len(tokens), results, wall)
	if *csvPath != "" {
		writeCSV(*csvPath, results)
	}
}

func doRequest(client *http.Client, url, token, model, content string) result {
	payload := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"%s"}]}`, model, content)
	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	if err != nil {
		return result{err: "build request: " + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return result{err: "do: " + err.Error()}
	}
	defer resp.Body.Close()

	r := result{status: resp.StatusCode}
	var first [1]byte
	if _, err := io.ReadFull(resp.Body, first[:]); err != nil {
		r.err = "read first byte: " + err.Error()
		r.totalMs = ms(time.Since(start))
		return r
	}
	r.ttfbMs = ms(time.Since(start))
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		r.err = "read rest: " + err.Error()
	}
	r.totalMs = ms(time.Since(start))
	return r
}

func printSummary(mode string, c, n, tokenCount int, results []result, wall time.Duration) {
	ttfb := make([]float64, 0, n)
	tot := make([]float64, 0, n)
	statuses := map[int]int{}
	errs := 0
	for _, r := range results {
		statuses[r.status]++
		if r.err != "" {
			errs++
			continue
		}
		ttfb = append(ttfb, r.ttfbMs)
		tot = append(tot, r.totalMs)
	}
	expect := 200
	if mode == "ratelimit" {
		expect = 429
	}
	bad := 0
	for st, cnt := range statuses {
		if st != expect {
			bad += cnt
		}
	}

	fmt.Println()
	fmt.Println("=== E13 负载结果 ===")
	fmt.Printf("mode=%s c=%d n=%d tokens=%d wall=%.0fms throughput=%.1f req/s\n",
		mode, c, n, tokenCount, ms(wall), float64(n)/wall.Seconds())
	fmt.Printf("状态码分布: %v (期望 %d)\n", statuses, expect)
	fmt.Printf("读错误=%d 非期望状态码=%d\n", errs, bad)
	fmt.Printf("TTFB  ms: P50=%.2f P95=%.2f P99=%.2f (样本 %d)\n",
		pct(ttfb, 50), pct(ttfb, 95), pct(ttfb, 99), len(ttfb))
	fmt.Printf("Total ms: P50=%.2f P95=%.2f P99=%.2f\n",
		pct(tot, 50), pct(tot, 95), pct(tot, 99))
}

func pct(list []float64, p float64) float64 {
	if len(list) == 0 {
		return 0
	}
	sort.Float64s(list)
	idx := int(math.Ceil(p/100*float64(len(list)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(list) {
		idx = len(list) - 1
	}
	return list[idx]
}

func writeCSV(path string, results []result) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "csv:", err)
		return
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"seq", "status", "ttfb_ms", "total_ms", "error"})
	for i, r := range results {
		_ = w.Write([]string{
			fmt.Sprint(i), fmt.Sprint(r.status),
			fmt.Sprintf("%.3f", r.ttfbMs), fmt.Sprintf("%.3f", r.totalMs), r.err,
		})
	}
	w.Flush()
}

func defaultLabel() string {
	return time.Now().Format("20061002-150405")
}

func ms(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}
