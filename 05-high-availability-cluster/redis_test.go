package main

import (
    "context"
    "os"
    "testing"
)

// TestMain：整个测试包只初始化一次 Redis，避免多次连接日志
func TestMain(m *testing.M) {
    initRedis()
    os.Exit(m.Run())
}

// 普通功能测试：限流是否正常工作
func TestCheckRateLimit(t *testing.T) {
    ctx := context.Background()

    // 连续打 10 次，应该在第 N 次触发限流
    for i := 0; i < 10; i++ {
        ok, err := checkRateLimit(ctx, 9999)
        if err != nil {
            t.Fatalf("第 %d 次调用出错: %v", i, err)
        }
        t.Logf("第 %d 次：allowed=%v", i+1, ok)
    }
}

// 基准测试：测试限流函数本身的吞吐量（ns/op）
func BenchmarkCheckRateLimit(b *testing.B) {
    ctx := context.Background()

    b.ResetTimer()
    b.RunParallel(func(pb *testing.PB) {
        userID := uint(42)
        for pb.Next() {
            checkRateLimit(ctx, userID)
        }
    })
}

// 对比测试 A：缓存命中（直接从 Redis 读）
func BenchmarkGetTokenCache_Hit(b *testing.B) {
    ctx := context.Background()
    testKey := "test-token-key-benchmark"

    // 预热：先写入一条缓存
    var fakeToken Token
    fakeToken.ID = 1
    fakeToken.UserID = 1
    setTokenCache(ctx, testKey, fakeToken)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        getTokenCache(ctx, testKey)
    }
}

// 对比测试 B：缓存未命中（key 根本不存在）
// 与 Hit 对比，可以量化「缓存穿透」时的额外开销
func BenchmarkGetTokenCache_Miss(b *testing.B) {
    ctx := context.Background()
    // 使用一个绝对不存在的 key
    missKey := "bench-miss-key-that-never-exists"

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        getTokenCache(ctx, missKey)
    }
}
