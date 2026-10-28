//go:build integration

package storage

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"aigateway/internal/domain"
	"github.com/redis/go-redis/v9"
)

func openTokenCacheRedis(t *testing.T) *Cache {
	t.Helper()
	addr := os.Getenv("AIGATEWAY_TEST_REDIS_ADDR")
	if addr == "" {
		t.Fatal("AIGATEWAY_TEST_REDIS_ADDR must point to a test Redis instance")
	}
	db, err := strconv.Atoi(os.Getenv("AIGATEWAY_TEST_REDIS_DB"))
	if err != nil {
		db = 0
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DB: db})
	if err := client.Ping(context.Background()).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("connect to test Redis: %v", err)
	}
	cache := &Cache{Client: client}
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}

func TestTokenCacheRoundTripAndRevocation(t *testing.T) {
	cache := openTokenCacheRedis(t)
	ctx := context.Background()
	rawToken := "sk-integration-redis-token"
	token := domain.Token{
		ID:        1,
		UserID:    1,
		TokenHash: "hash-only",
		Status:    1,
		ExpiresAt: time.Now().Add(2 * time.Minute),
	}
	key := tokenKey(rawToken)
	defer cache.Client.Del(ctx, key)

	cache.SetToken(ctx, rawToken, token)
	cachedValue, err := cache.Client.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("read token cache: %v", err)
	}
	if strings.Contains(cachedValue, rawToken) || strings.Contains(key, rawToken) {
		t.Fatal("Redis token cache contains the raw token")
	}

	got, hit := cache.GetToken(ctx, rawToken)
	if !hit {
		t.Fatal("expected token cache hit")
	}
	if got.ID != token.ID || got.UserID != token.UserID || got.Status != token.Status {
		t.Fatalf("cached token identity/state = %#v; want ID=%d UserID=%d Status=%d", got, token.ID, token.UserID, token.Status)
	}
	ttl, err := cache.Client.TTL(ctx, key).Result()
	if err != nil {
		t.Fatalf("read token cache TTL: %v", err)
	}
	if ttl <= 0 || ttl > time.Until(token.ExpiresAt)+time.Second {
		t.Fatalf("token cache TTL = %v; exceeds token expiry", ttl)
	}

	cache.DeleteToken(ctx, rawToken)
	if _, err := cache.Client.Get(ctx, key).Result(); err != redis.Nil {
		t.Fatalf("token cache after revocation error = %v; want redis.Nil", err)
	}
}
