package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultAddr       = ":8081"
	defaultDelay      = 6 * time.Second
	defaultLongLine   = 70 * 1024
	defaultStatusCode = http.StatusInternalServerError
)

type endpointConfig struct {
	Scenario          string `json:"scenario"`
	HeaderDelayMS     int    `json:"header_delay_ms,omitempty"`
	FirstChunkDelayMS int    `json:"first_chunk_delay_ms,omitempty"`
	ChunkDelayMS      int    `json:"chunk_delay_ms,omitempty"`
	StatusCode        int    `json:"status_code,omitempty"`
	FailFirst         int    `json:"fail_first,omitempty"`
}

type configRequest struct {
	Provider string `json:"provider"`
	endpointConfig
}

type requestRecord struct {
	ID               string    `json:"id"`
	Provider         string    `json:"provider"`
	Scenario         string    `json:"scenario"`
	Authorization    string    `json:"authorization"`
	CallNumber       int       `json:"call_number"`
	StatusCode       int       `json:"status_code"`
	Chunks           int       `json:"chunks"`
	Error            string    `json:"error,omitempty"`
	CancelObserved   bool      `json:"cancel_observed,omitempty"`
	CancelObservedAt time.Time `json:"cancel_observed_at,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	HeaderAt         time.Time `json:"header_at,omitempty"`
	EndedAt          time.Time `json:"ended_at"`
}

type fakeProvider struct {
	mu      sync.Mutex
	configs map[string]endpointConfig
	calls   map[string]int
	records []requestRecord
	nextID  uint64
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		configs: map[string]endpointConfig{
			"primary":  {Scenario: "normal"},
			"fallback": {Scenario: "normal"},
		},
		calls: make(map[string]int),
	}
}

func main() {
	provider := newFakeProvider()
	mux := http.NewServeMux()

	mux.HandleFunc("/primary/chat/completions", provider.completions("primary"))
	mux.HandleFunc("/fallback/chat/completions", provider.completions("fallback"))
	mux.HandleFunc("/__test/health", provider.health)
	mux.HandleFunc("/__test/reset", provider.reset)
	mux.HandleFunc("/__test/stats", provider.stats)
	mux.HandleFunc("/__test/config", provider.config)

	addr := os.Getenv("FAKE_PROVIDER_ADDR")
	if addr == "" {
		addr = defaultAddr
	}

	server := &http.Server{Addr: addr, Handler: loggingMiddleware(mux)}
	log.Printf("fake provider listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}

func (p *fakeProvider) health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func (p *fakeProvider) reset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	p.mu.Lock()
	p.calls = make(map[string]int)
	p.records = nil
	p.nextID = 0
	p.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func (p *fakeProvider) stats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	p.mu.Lock()
	response := struct {
		Configs map[string]endpointConfig `json:"configs"`
		Calls   map[string]int            `json:"calls"`
		Records []requestRecord           `json:"records"`
	}{
		Configs: cloneConfigs(p.configs),
		Calls:   cloneCalls(p.calls),
		Records: append([]requestRecord(nil), p.records...),
	}
	p.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (p *fakeProvider) config(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var request configRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Provider == "" {
		http.Error(w, "provider and valid JSON are required", http.StatusBadRequest)
		return
	}
	if request.endpointConfig.Scenario == "" {
		request.endpointConfig.Scenario = "normal"
	}
	if request.endpointConfig.StatusCode == 0 {
		request.endpointConfig.StatusCode = defaultStatusCode
	}

	p.mu.Lock()
	p.configs[request.Provider] = request.endpointConfig
	p.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(request.endpointConfig)
}

func (p *fakeProvider) completions(providerName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		_, _ = io.Copy(io.Discard, r.Body)

		config, callNumber, requestID := p.beginRequest(providerName)
		if scenario := r.URL.Query().Get("scenario"); scenario != "" {
			config.Scenario = scenario
		}
		if config.Scenario == "" {
			config.Scenario = "normal"
		}

		result := p.serveScenario(w, r.Context(), config, requestID, callNumber)
		record := requestRecord{
			ID:               requestID,
			Provider:         providerName,
			Scenario:         config.Scenario,
			Authorization:    r.Header.Get("Authorization"),
			CallNumber:       callNumber,
			StatusCode:       result.statusCode,
			Chunks:           result.chunks,
			Error:            result.err,
			CancelObserved:   result.cancelObserved,
			CancelObservedAt: result.cancelObservedAt,
			StartedAt:        started,
			HeaderAt:         result.headerAt,
			EndedAt:          time.Now(),
		}
		p.finishRequest(record)

		log.Printf("request id=%s provider=%s scenario=%s call=%d status=%d auth=%q elapsed=%s", requestID, providerName, config.Scenario, callNumber, result.statusCode, record.EndedAt.Sub(started), record.Authorization)
	}
}

type scenarioResult struct {
	statusCode       int
	chunks           int
	err              string
	headerAt         time.Time
	cancelObserved   bool
	cancelObservedAt time.Time
}

func (p *fakeProvider) serveScenario(w http.ResponseWriter, ctx context.Context, config endpointConfig, requestID string, callNumber int) scenarioResult {
	if config.FailFirst > 0 && callNumber <= config.FailFirst {
		return writeStatus(w, statusOrDefault(config.StatusCode), "configured first-call failure")
	}

	switch strings.ToLower(config.Scenario) {
	case "normal":
		return writeNormalStream(w, config.ChunkDelayMS)
	case "header-delay":
		sleepMillis(config.HeaderDelayMS, defaultDelay)
		return writeNormalStream(w, config.ChunkDelayMS)
	case "cancel-aware-header-delay":
		return waitForCancelBeforeHeaders(w, ctx, config.HeaderDelayMS)
	case "first-chunk-delay":
		return writeFirstChunkDelayed(w, config.FirstChunkDelayMS)
	case "chunk-delay":
		return writeNormalStream(w, config.ChunkDelayMS)
	case "429", "status-429":
		return writeStatus(w, http.StatusTooManyRequests, "fake provider rate limit")
	case "401", "status-401":
		return writeStatus(w, http.StatusUnauthorized, "fake provider unauthorized")
	case "500", "status-500":
		return writeStatus(w, http.StatusInternalServerError, "fake provider failure")
	case "partial", "partial-disconnect":
		return writePartialDisconnect(w)
	case "long-line":
		return writeLongLine(w)
	default:
		return writeStatus(w, http.StatusBadRequest, "unknown fake provider scenario")
	}
}

func waitForCancelBeforeHeaders(w http.ResponseWriter, ctx context.Context, delayMS int) scenarioResult {
	delay := defaultDelay
	if delayMS > 0 {
		delay = time.Duration(delayMS) * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return scenarioResult{
			err:              ctx.Err().Error(),
			cancelObserved:   true,
			cancelObservedAt: time.Now(),
		}
	case <-timer.C:
		return writeNormalStream(w, 0)
	}
}

func (p *fakeProvider) beginRequest(providerName string) (endpointConfig, int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[providerName]++
	p.nextID++
	return p.configs[providerName], p.calls[providerName], fmt.Sprintf("fake-%06d", p.nextID)
}

func (p *fakeProvider) finishRequest(record requestRecord) {
	p.mu.Lock()
	p.records = append(p.records, record)
	p.mu.Unlock()
}

func writeNormalStream(w http.ResponseWriter, chunkDelayMS int) scenarioResult {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return writeStatus(w, http.StatusInternalServerError, "streaming is not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	headerAt := time.Now()

	chunks := []string{
		`{"id":"fake-chat","object":"chat.completion.chunk","created":1,"model":"fake-model","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"fake-chat","object":"chat.completion.chunk","created":1,"model":"fake-model","choices":[{"index":0,"delta":{"content":"fake response"}}]}`,
	}
	for _, chunk := range chunks {
		if chunkDelayMS > 0 {
			time.Sleep(time.Duration(chunkDelayMS) * time.Millisecond)
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		flusher.Flush()
	}
	usage := `{"id":"fake-chat","object":"chat.completion.chunk","created":1,"model":"fake-model","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`
	_, _ = fmt.Fprintf(w, "data: %s\n\n", usage)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()

	return scenarioResult{statusCode: http.StatusOK, chunks: len(chunks), headerAt: headerAt}
}

func writeFirstChunkDelayed(w http.ResponseWriter, delayMS int) scenarioResult {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return writeStatus(w, http.StatusInternalServerError, "streaming is not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	headerAt := time.Now()
	sleepMillis(delayMS, defaultDelay)
	_, _ = fmt.Fprint(w, `data: {"id":"fake-chat","object":"chat.completion.chunk","created":1,"model":"fake-model","choices":[{"index":0,"delta":{"content":"delayed"}}]}`+"\n\n")
	flusher.Flush()
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
	return scenarioResult{statusCode: http.StatusOK, chunks: 1, headerAt: headerAt}
}

func writePartialDisconnect(w http.ResponseWriter) scenarioResult {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return writeStatus(w, http.StatusInternalServerError, "streaming is not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	headerAt := time.Now()
	_, _ = fmt.Fprint(w, `data: {"id":"fake-chat","object":"chat.completion.chunk","created":1,"model":"fake-model","choices":[{"index":0,"delta":{"content":"partial"}}]}`+"\n\n")
	flusher.Flush()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return scenarioResult{statusCode: http.StatusInternalServerError, chunks: 1, err: "hijacking is not supported", headerAt: headerAt}
	}
	connection, _, err := hijacker.Hijack()
	if err != nil {
		return scenarioResult{statusCode: http.StatusOK, chunks: 1, err: err.Error(), headerAt: headerAt}
	}
	_ = connection.Close()
	return scenarioResult{statusCode: http.StatusOK, chunks: 1, err: "intentional disconnect", headerAt: headerAt}
}

func writeLongLine(w http.ResponseWriter) scenarioResult {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", strings.Repeat("x", defaultLongLine))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return scenarioResult{statusCode: http.StatusOK, chunks: 1, headerAt: time.Now()}
}

func writeStatus(w http.ResponseWriter, status int, message string) scenarioResult {
	http.Error(w, message+"\n", status)
	return scenarioResult{statusCode: status}
}

func statusOrDefault(status int) int {
	if status == 0 {
		return defaultStatusCode
	}
	return status
}

func sleepMillis(value int, fallback time.Duration) {
	if value <= 0 {
		time.Sleep(fallback)
		return
	}
	time.Sleep(time.Duration(value) * time.Millisecond)
}

func cloneConfigs(input map[string]endpointConfig) map[string]endpointConfig {
	output := make(map[string]endpointConfig, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneCalls(input map[string]int) map[string]int {
	output := make(map[string]int, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("http method=%s path=%s status=completed elapsed=%s", r.Method, r.URL.Path, time.Since(started))
	})
}
