package storage

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	"aigateway/internal/domain"
	"github.com/redis/go-redis/v9"
)

type Cache struct {
	Client *redis.Client
}

func OpenCacheFromEnv() (*Cache, error) {
	redisAddr := os.Getenv("REDIS_HOST")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	client := redis.NewClient(&redis.Options{Addr: redisAddr, DB: 0})
	if _, err := client.Ping(context.Background()).Result(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("Redis 初始化失败: %w", err)
	}
	log.Printf("Redis connected: %s", redisAddr)
	return &Cache{Client: client}, nil
}

func (c *Cache) Close() error {
	return c.Client.Close()
}

func (c *Cache) GetContent(ctx context.Context, req domain.ChatRequest) (string, bool) {
	key, err := contentKey(req)
	if err != nil {
		log.Printf("content marshal messages error: %s", err)
		return "", false
	}

	value, err := c.Client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", false
	}
	if err != nil {
		log.Printf("content redis read error: %s", err)
		return "", false
	}
	return value, true
}

func (c *Cache) SetContent(ctx context.Context, req domain.ChatRequest, content string) {
	key, err := contentKey(req)
	if err != nil {
		log.Printf("content marshal messages error: %s", err)
		return
	}
	ttl := time.Hour + time.Duration(rand.Intn(10))*time.Minute
	if err := c.Client.Set(ctx, key, content, ttl).Err(); err != nil {
		log.Printf("content redis set error: %s", err)
	}
}

func (c *Cache) GetToken(ctx context.Context, apiKey string) (domain.Token, bool) {
	value, err := c.Client.Get(ctx, tokenKey(apiKey)).Result()
	if err == redis.Nil {
		return domain.Token{}, false
	}
	if err != nil {
		log.Printf("token redis read error: %s", err)
		return domain.Token{}, false
	}

	var token domain.Token
	if err := json.Unmarshal([]byte(value), &token); err != nil {
		log.Printf("token unmarshal error: %s", err)
		return domain.Token{}, false
	}
	return token, true
}

func (c *Cache) SetToken(ctx context.Context, apiKey string, token domain.Token) {
	value, err := json.Marshal(token)
	if err != nil {
		log.Printf("token marshal error: %s", err)
		return
	}
	ttl := 5*time.Minute + time.Duration(rand.Intn(60))*time.Second
	if err := c.Client.Set(ctx, tokenKey(apiKey), value, ttl).Err(); err != nil {
		log.Printf("token redis set error: %s", err)
	}
}

func (c *Cache) DeleteToken(ctx context.Context, apiKey string) {
	if err := c.Client.Del(ctx, tokenKey(apiKey)).Err(); err != nil {
		log.Printf("token redis delete error: %s", err)
	}
}

func (c *Cache) GetUser(ctx context.Context, id uint) (domain.User, bool) {
	value, err := c.Client.Get(ctx, userKey(id)).Result()
	if err == redis.Nil {
		return domain.User{}, false
	}
	if err != nil {
		log.Printf("user redis read error: %s", err)
		return domain.User{}, false
	}

	var user domain.User
	if err := json.Unmarshal([]byte(value), &user); err != nil {
		log.Printf("user unmarshal error: %s", err)
		return domain.User{}, false
	}
	return user, true
}

func (c *Cache) SetUser(ctx context.Context, id uint, user domain.User) {
	value, err := json.Marshal(user)
	if err != nil {
		log.Printf("user marshal error: %s", err)
		return
	}
	ttl := 5*time.Minute + time.Duration(rand.Intn(60))*time.Second
	if err := c.Client.Set(ctx, userKey(id), value, ttl).Err(); err != nil {
		log.Printf("user redis set error: %s", err)
	}
}

func (c *Cache) DeleteUser(ctx context.Context, id uint) {
	if err := c.Client.Del(ctx, userKey(id)).Err(); err != nil {
		log.Printf("user redis delete error: %s", err)
	}
}

var rateLimitScript = redis.NewScript(`
local count = redis.call("INCR",KEYS[1])
if count == 1 then
	redis.call("EXPIRE",KEYS[1],ARGV[1])
end
return count
`)

func (c *Cache) CheckRateLimit(ctx context.Context, userID uint) (bool, error) {
	now := time.Now().Unix() / 60
	key := fmt.Sprintf("ratelimit:%d:%d", userID, now)
	count, err := rateLimitScript.Run(ctx, c.Client, []string{key}, 60).Int()
	if err != nil {
		return false, fmt.Errorf("Redis ratelimiting script fail: %w", err)
	}
	return count <= 10, nil
}

func contentKey(req domain.ChatRequest) (string, error) {
	messages, err := json.Marshal(req.Messages)
	if err != nil {
		return "", err
	}
	raw := fmt.Sprintf("%s|%f|%f|%s", req.Model, req.GetTemperature(), req.GetTopP(), string(messages))
	return "cache:content:" + md5Hex(raw), nil
}

func tokenKey(apiKey string) string {
	return "cache:token:" + md5Hex(apiKey)
}

func userKey(id uint) string {
	return fmt.Sprintf("cache:user:%d", id)
}

func md5Hex(value string) string {
	hash := md5.Sum([]byte(value))
	return hex.EncodeToString(hash[:])
}
