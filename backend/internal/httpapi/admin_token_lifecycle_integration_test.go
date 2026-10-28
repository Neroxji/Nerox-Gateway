//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aigateway/internal/config"
	"aigateway/internal/domain"
	"aigateway/internal/provider"
	"aigateway/internal/security"
	"aigateway/internal/storage"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const integrationAdminKey = "integration-test-admin-key"

type adminTokenIntegrationEnvironment struct {
	database      *storage.Database
	cache         *storage.Cache
	router        *gin.Engine
	provider      *httptest.Server
	providerCalls atomic.Int32
}

func newAdminTokenIntegrationEnvironment(t *testing.T) *adminTokenIntegrationEnvironment {
	t.Helper()

	dsn := os.Getenv("AIGATEWAY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("AIGATEWAY_TEST_MYSQL_DSN must point to a dedicated test MySQL database")
	}
	redisAddr := os.Getenv("AIGATEWAY_TEST_REDIS_ADDR")
	if redisAddr == "" {
		t.Fatal("AIGATEWAY_TEST_REDIS_ADDR must point to a test Redis instance")
	}
	redisDB, err := strconv.Atoi(os.Getenv("AIGATEWAY_TEST_REDIS_DB"))
	if err != nil {
		redisDB = 0
	}

	database, err := storage.OpenDatabase(dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	sqlDB, err := database.DB.DB()
	if err != nil {
		t.Fatalf("get test sql database: %v", err)
	}

	redisClient := redis.NewClient(&redis.Options{Addr: redisAddr, DB: redisDB})
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		_ = sqlDB.Close()
		_ = redisClient.Close()
		t.Fatalf("connect to test Redis: %v", err)
	}
	cache := &storage.Cache{Client: redisClient}

	environment := &adminTokenIntegrationEnvironment{database: database, cache: cache}
	environment.provider = httptest.NewServer(http.HandlerFunc(environment.fakeProviderHandler))

	cfg := &config.Config{
		Providers: []config.Provider{{
			Name:      "fake",
			Url:       environment.provider.URL,
			Model:     "fake-model",
			Keys:      []string{"fake-provider-key"},
			PricePerK: 1,
		}},
		Fallbacks: map[string][]string{"test-model": {"fake"}},
	}
	cfg.Providers[0].Pool = provider.NewKeyPool(cfg.Providers[0].Keys)

	server := NewServer(cfg, database, cache)
	server.AdminAPIKey = integrationAdminKey
	gin.SetMode(gin.TestMode)
	environment.router = gin.New()
	server.Register(environment.router)

	resetAdminTokenIntegrationData(t, environment)
	t.Cleanup(func() {
		resetAdminTokenIntegrationData(t, environment)
		environment.provider.Close()
		_ = cache.Close()
		_ = sqlDB.Close()
	})
	return environment
}

func (e *adminTokenIntegrationEnvironment) fakeProviderHandler(w http.ResponseWriter, _ *http.Request) {
	e.providerCalls.Add(1)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "data: {\"id\":\"fake\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
	_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{}}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func resetAdminTokenIntegrationData(t *testing.T, environment *adminTokenIntegrationEnvironment) {
	t.Helper()
	for _, table := range []string{"request_logs", "tokens", "users"} {
		if err := environment.database.DB.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	if err := environment.cache.Client.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("clear Redis test database: %v", err)
	}
	environment.providerCalls.Store(0)
}

func adminTokenRequest(t *testing.T, environment *adminTokenIntegrationEnvironment, method, path, authorization string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	environment.router.ServeHTTP(response, req)
	return response
}

func adminAuthorization() string {
	return "Bearer " + integrationAdminKey
}

func createUserAndToken(t *testing.T, environment *adminTokenIntegrationEnvironment, suffix string) (uint, uint, string) {
	t.Helper()
	userResponse := adminTokenRequest(t, environment, http.MethodPost, "/admin/users/create", adminAuthorization(), map[string]any{
		"username": fmt.Sprintf("integration-%s-%d", suffix, time.Now().UnixNano()),
		"balance":  1000,
	})
	if userResponse.Code != http.StatusOK {
		t.Fatalf("create user status = %d; body = %s", userResponse.Code, userResponse.Body.String())
	}
	var userPayload struct {
		UserID uint `json:"user_id"`
	}
	if err := json.Unmarshal(userResponse.Body.Bytes(), &userPayload); err != nil {
		t.Fatalf("decode user response: %v", err)
	}

	tokenResponse := adminTokenRequest(t, environment, http.MethodPost, "/admin/tokens/create", adminAuthorization(), map[string]any{
		"user_id": userPayload.UserID,
		"name":    "integration test token",
	})
	if tokenResponse.Code != http.StatusOK {
		t.Fatalf("create token status = %d; body = %s", tokenResponse.Code, tokenResponse.Body.String())
	}
	var tokenPayload struct {
		TokenID  uint   `json:"token_id"`
		TokenKey string `json:"token_key"`
	}
	if err := json.Unmarshal(tokenResponse.Body.Bytes(), &tokenPayload); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	return userPayload.UserID, tokenPayload.TokenID, tokenPayload.TokenKey
}

func chatBody(content string) map[string]any {
	return map[string]any{
		"model": "test-model",
		"messages": []map[string]string{{
			"role":    "user",
			"content": content,
		}},
	}
}

func TestAdminLifecycleAndTokenStorage(t *testing.T) {
	environment := newAdminTokenIntegrationEnvironment(t)
	userID, tokenID, rawToken := createUserAndToken(t, environment, "lifecycle")

	var storedToken domain.Token
	if err := environment.database.DB.First(&storedToken, tokenID).Error; err != nil {
		t.Fatalf("load created token: %v", err)
	}
	if storedToken.TokenHash != security.HashToken(rawToken) {
		t.Fatalf("stored token hash = %q; want hash of issued token", storedToken.TokenHash)
	}
	if storedToken.TokenHash == rawToken {
		t.Fatal("database stored the raw token")
	}
	if !storedToken.ExpiresAt.After(time.Now().Add(89 * 24 * time.Hour)) {
		t.Fatalf("token expiry = %v; want approximately 90 days from now", storedToken.ExpiresAt)
	}

	var legacyColumnCount int64
	if err := environment.database.DB.Raw(`
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_schema = DATABASE()
		  AND table_name = 'tokens'
		  AND column_name = 'token_key'`).Scan(&legacyColumnCount).Error; err != nil {
		t.Fatalf("check legacy token_key column: %v", err)
	}
	if legacyColumnCount != 0 {
		t.Fatalf("tokens table still contains token_key; count = %d", legacyColumnCount)
	}

	validResponse := adminTokenRequest(t, environment, http.MethodPost, "/api/v1/chat", "Bearer "+rawToken, chatBody("valid-lifecycle"))
	if validResponse.Code != http.StatusOK {
		t.Fatalf("valid chat status = %d; body = %s", validResponse.Code, validResponse.Body.String())
	}
	if got := environment.providerCalls.Load(); got != 1 {
		t.Fatalf("provider calls after valid chat = %d; want 1", got)
	}

	tokenCacheKey := "cache:token:" + security.HashToken(rawToken)
	cachedValue, err := environment.cache.Client.Get(context.Background(), tokenCacheKey).Result()
	if err != nil {
		t.Fatalf("read token cache: %v", err)
	}
	if strings.Contains(cachedValue, rawToken) || strings.Contains(tokenCacheKey, rawToken) {
		t.Fatal("Redis token cache contains the raw token")
	}
	ttl, err := environment.cache.Client.TTL(context.Background(), tokenCacheKey).Result()
	if err != nil {
		t.Fatalf("read token cache TTL: %v", err)
	}
	if ttl <= 0 || ttl > time.Until(storedToken.ExpiresAt)+time.Second {
		t.Fatalf("token cache TTL = %v; exceeds token expiry", ttl)
	}

	rawHeaderResponse := adminTokenRequest(t, environment, http.MethodPost, "/api/v1/chat", rawToken, chatBody("raw-header"))
	if rawHeaderResponse.Code != http.StatusUnauthorized {
		t.Fatalf("raw token header status = %d; want 401", rawHeaderResponse.Code)
	}
	if got := environment.providerCalls.Load(); got != 1 {
		t.Fatalf("provider calls after raw token header = %d; want 1", got)
	}

	revokeResponse := adminTokenRequest(t, environment, http.MethodDelete, "/admin/tokens/"+strconv.FormatUint(uint64(tokenID), 10), adminAuthorization(), nil)
	if revokeResponse.Code != http.StatusOK {
		t.Fatalf("revoke token status = %d; body = %s", revokeResponse.Code, revokeResponse.Body.String())
	}
	if _, err := environment.cache.Client.Get(context.Background(), tokenCacheKey).Result(); err != redis.Nil {
		t.Fatalf("token cache after revocation error = %v; want redis.Nil", err)
	}
	revokedResponse := adminTokenRequest(t, environment, http.MethodPost, "/api/v1/chat", "Bearer "+rawToken, chatBody("revoked-token"))
	if revokedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d; want 401", revokedResponse.Code)
	}
	if got := environment.providerCalls.Load(); got != 1 {
		t.Fatalf("provider calls after revoked token = %d; want 1", got)
	}

	if userID == 0 {
		t.Fatal("created user ID is zero")
	}
}

func TestExpiredAndDisabledCredentialsDoNotCallProvider(t *testing.T) {
	environment := newAdminTokenIntegrationEnvironment(t)
	userID, _, rawToken := createUserAndToken(t, environment, "disabled")

	deleteUserResponse := adminTokenRequest(t, environment, http.MethodDelete, "/admin/users/"+strconv.FormatUint(uint64(userID), 10), adminAuthorization(), nil)
	if deleteUserResponse.Code != http.StatusOK {
		t.Fatalf("disable user status = %d; body = %s", deleteUserResponse.Code, deleteUserResponse.Body.String())
	}
	disabledResponse := adminTokenRequest(t, environment, http.MethodPost, "/api/v1/chat", "Bearer "+rawToken, chatBody("disabled-user"))
	if disabledResponse.Code != http.StatusForbidden {
		t.Fatalf("disabled user status = %d; want 403", disabledResponse.Code)
	}
	if got := environment.providerCalls.Load(); got != 0 {
		t.Fatalf("provider calls for disabled user = %d; want 0", got)
	}

	expiredRawToken, err := security.GenerateToken()
	if err != nil {
		t.Fatalf("generate expired token: %v", err)
	}
	if err := environment.database.CreateToken(&domain.Token{
		UserID:    userID,
		Name:      "expired token",
		TokenHash: security.HashToken(expiredRawToken),
		Status:    1,
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("create expired token: %v", err)
	}

	// Re-enable the user only through the test database so this case isolates expiry.
	if err := environment.database.DB.Model(&domain.User{}).Where("id = ?", userID).Update("status", 1).Error; err != nil {
		t.Fatalf("re-enable test user: %v", err)
	}
	expiredResponse := adminTokenRequest(t, environment, http.MethodPost, "/api/v1/chat", "Bearer "+expiredRawToken, chatBody("expired-token"))
	if expiredResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d; want 401", expiredResponse.Code)
	}
	if got := environment.providerCalls.Load(); got != 0 {
		t.Fatalf("provider calls for expired token = %d; want 0", got)
	}
}
