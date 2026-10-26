package storage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"aigateway/internal/domain"
)

func testCache(t *testing.T) *Cache {
	t.Helper()
	cache, err := OpenCacheFromEnv()
	if err != nil {
		t.Skipf("Redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}

func TestCheckRateLimit(t *testing.T) {
	cache := testCache(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := cache.CheckRateLimit(ctx, 9999); err != nil {
			t.Fatalf("第 %d 次调用出错: %v", i, err)
		}
	}
}

func TestTokenCacheDoesNotExposeRawToken(t *testing.T) {
	rawToken := "sk-test-token-value"
	token := domain.Token{ID: 1, UserID: 1, TokenHash: "hash-only"}
	value, err := json.Marshal(token)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(value), rawToken) {
		t.Fatalf("cache payload contains raw token: %s", value)
	}
	if strings.Contains(tokenKey(rawToken), rawToken) {
		t.Fatalf("cache key contains raw token: %s", tokenKey(rawToken))
	}
}

func BenchmarkCheckRateLimit(b *testing.B) {
	cache, err := OpenCacheFromEnv()
	if err != nil {
		b.Skipf("Redis unavailable: %v", err)
	}
	b.Cleanup(func() { _ = cache.Close() })

	ctx := context.Background()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = cache.CheckRateLimit(ctx, 42)
		}
	})
}

func BenchmarkGetTokenCache_Hit(b *testing.B) {
	cache, err := OpenCacheFromEnv()
	if err != nil {
		b.Skipf("Redis unavailable: %v", err)
	}
	b.Cleanup(func() { _ = cache.Close() })

	ctx := context.Background()
	cache.SetToken(ctx, "test-token-key-benchmark", domain.Token{ID: 1, UserID: 1})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cache.GetToken(ctx, "test-token-key-benchmark")
	}
}

func BenchmarkGetTokenCache_Miss(b *testing.B) {
	cache, err := OpenCacheFromEnv()
	if err != nil {
		b.Skipf("Redis unavailable: %v", err)
	}
	b.Cleanup(func() { _ = cache.Close() })

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cache.GetToken(ctx, "bench-miss-key-that-never-exists")
	}
}
