package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/redis/go-redis/v9"
)

var RDB *redis.Client

// initialize redis
func initRedis() {

	// 1	connect to redis
	RDB = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // production environment must enter pw！
		DB:       0,
	})

	// 2
	_, err := RDB.Ping(context.Background()).Result()
	if err != nil {
		log.Fatalf("redis initialization err!! %s", err)
	}
	log.Println("redis connects successfully!!")
}

// get content from cache
func getExactCache(ctx context.Context, prompt string) (string, bool) {

	// 1	tranfer to hex
	hash := md5.Sum([]byte(prompt))
	md5str := hex.EncodeToString(hash[:])
	key := fmt.Sprintf("cache:exact:%s", md5str)

	// 2
	str, err := RDB.Get(ctx, key).Result()
	if err == redis.Nil {
		log.Println("No cache info!")
		return "", false
	}
	if err != nil {
		log.Printf("redis dead?! err:%s", err)
		return "", false
	}
	return str, true
}
