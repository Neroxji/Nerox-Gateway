package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"time"
	"math/rand"

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

// get Content
func getContentCache(ctx context.Context, req ChatRequest) (string, bool) {

	// 1
	msgBytes, err := json.Marshal(req.Messages)
	if err != nil {
		log.Printf("content marshal messages error:%s", err)
		return "", false
	}
	rawContent := fmt.Sprintf("%s|%f|%f|%s", req.Model, req.GetTemperature(), req.GetTopP(), string(msgBytes))

	// 2	tranfer to hex
	hash := md5.Sum([]byte(rawContent))
	md5str := hex.EncodeToString(hash[:])
	key := fmt.Sprintf("cache:content:%s", md5str)

	// 3
	str, err := RDB.Get(ctx, key).Result()
	if err == redis.Nil {
		log.Println("No content cache info!")
		return "", false
	}
	if err != nil {
		log.Printf("content redis dead?! err:%s", err)
		return "", false
	}
	log.Println("HIT cache:Content")
	return str, true
}

// set Content
func setContentCache(ctx context.Context, req ChatRequest, content string) {

	// 1
	msgBytes, err := json.Marshal(req.Messages)
	if err != nil {
		log.Printf("content marshal messages error:%s", err)
		return
	}
	rawContent := fmt.Sprintf("%s|%f|%f|%s", req.Model, req.GetTemperature(), req.GetTopP(), string(msgBytes))

	// 2 transfer to hex
	hash := md5.Sum([]byte(rawContent))
	md5str := hex.EncodeToString(hash[:])
	key := fmt.Sprintf("cache:content:%s", md5str)

	// 3
	jitter:=time.Duration(rand.Intn(10))*time.Minute
	ttl:=1*time.Hour+jitter

	// 4
	err = RDB.Set(ctx, key, content, ttl).Err()
	if err != nil {
		log.Printf("content redis set error:%s", err)
		return
	}
	log.Println("successfully set Content redis")
}

// get Token
func getTokenCache(ctx context.Context, apiKey string) (Token, bool) {

	// 1	transfer to hex
	hash := md5.Sum([]byte(apiKey))
	md5str := hex.EncodeToString(hash[:])
	key := fmt.Sprintf("cache:token:%s", md5str)

	// 2
	val, err := RDB.Get(ctx, key).Result()
	if err == redis.Nil {
		log.Println("No token cache info!")
		return Token{}, false
	}
	if err != nil {
		log.Printf("token redis dead?! err:%s", err)
		return Token{}, false
	}

	// 3
	var token Token
	err = json.Unmarshal([]byte(val), &token)
	if err != nil {
		log.Printf("token unmarshal fail! please check the format?! err:%s", err)
		return Token{}, false
	}
	log.Println("HIT cache:Token")
	return token, true
}

// set Token
func setTokenCache(ctx context.Context, apiKey string, token Token) {

	// 1	transfer to hex
	hash := md5.Sum([]byte(apiKey))
	md5str := hex.EncodeToString(hash[:])
	key := fmt.Sprintf("cache:token:%s", md5str)

	// 2
	tokenBytes, err := json.Marshal(token)
	if err != nil {
		log.Printf("token marshal error: %s", err)
		return
	}

	// 3
	jitter:=time.Duration(rand.Intn(60))*time.Second
	ttl:=5*time.Minute+jitter

	// 4
	err = RDB.Set(ctx, key, tokenBytes, ttl).Err()
	if err != nil {
		log.Printf("token redis set error:%s", err)
		return
	}
	log.Printf("successfully set Token redis. apikey:%s",apiKey)
}

// delete Token
func deleteTokenCache(ctx context.Context, apiKey string) {

	// 1
	hash := md5.Sum([]byte(apiKey))
	md5str := hex.EncodeToString(hash[:])
	key := fmt.Sprintf("cache:token:%s", md5str)

	// 2
	err := RDB.Del(ctx, key).Err()
	if err != nil {
		log.Printf("token redis delete error:%s", err)
		return
	}
}

// get User
func getUserCache(ctx context.Context, ID uint) (User, bool) {

	// 1
	key := fmt.Sprintf("cache:user:%d", ID)

	// 2
	val, err := RDB.Get(ctx, key).Result()
	if err == redis.Nil {
		log.Println("no user cache info!")
		return User{}, false
	}
	if err != nil {
		log.Printf("user redis dead?! err:%s", err)
		return User{}, false
	}

	// 3
	var user User
	err = json.Unmarshal([]byte(val), &user)
	if err != nil {
		log.Printf("user unmarshal fail! please check the format?! err:%s", err)
		return User{}, false
	}
	log.Println("HIT cache:User")
	return user, true
}

// set User
func setUserCache(ctx context.Context, ID uint, user User) {

	// 1
	key := fmt.Sprintf("cache:user:%d", ID)

	// 2
	userBytes, err := json.Marshal(user)
	if err != nil {
		log.Printf("token marshal error: %s", err)
		return
	}

	// 3
	jitter:=time.Duration(rand.Intn(60))*time.Second
	ttl:=5*time.Minute+jitter

	// 4
	err = RDB.Set(ctx, key, userBytes, ttl).Err()
	if err != nil {
		log.Printf("user redis set error:%s", err)
		return
	}
	log.Printf("successfully set User redis. ID:%d", ID)
}

// delete User
func deleteUserCache(ctx context.Context, ID uint) {

	// 1
	key := fmt.Sprintf("cache:user:%d", ID)

	// 2
	err := RDB.Del(ctx, key).Err()
	if err != nil {
		log.Printf("user redis delete error:%s", err)
		return
	}
	log.Printf("delete user successfully! id:%d", ID)
}

// rateLimit lua script
var rateLimitScript = redis.NewScript(`
local count = redis.call("INCR",KEYS[1])
if count == 1 then
	redis.call("EXPIRE",KEYS[1],ARGV[1])
end
return count
`)

// ratelimiting
func checkRateLimit(ctx context.Context, userID uint) (bool, error) {
	now := time.Now().Unix() / 60
	key := fmt.Sprintf("ratelimit:%d:%d", userID, now)
	count, err := rateLimitScript.Run(ctx, RDB, []string{key}, 60).Int()
	if err != nil {
		return false, fmt.Errorf("Redis ratelimiting script fail:%w", err)
	}
	if count > 10 {
		return false, nil
	}

	return true, nil
}
