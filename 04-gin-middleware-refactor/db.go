package main

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var DB *gorm.DB
var LogChan chan RequestLog

// 初始化数据库
func InitDB(dsn string) {

	// 1 初始化日志通道
	LogChan = make(chan RequestLog, 1000)

	// 2.1用 GORM 连上数据库
	var err error
	DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("连接 MySQL 失败: %v", err)
	}
	log.Println("成功连接 MySQL 数据库!")

	// 2.2拿到底层的对象
	sqlDB, err := DB.DB()
	if err != nil {
		log.Fatalf("获取底层 sqlDB 失败: %v", err)
	}
	sqlDB.SetMaxIdleConns(20)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// 2.3自动迁移
	err = DB.AutoMigrate(&User{}, &RequestLog{}, &Token{})
	if err != nil {
		log.Fatalf("自动建表失败: %v", err)
	}
	log.Println("数据库表结构同步完成！")

}

// 扣费函数
func DeductBalance(userID uint, cost int64) error {

	// 乐观校验
	result := DB.Model(&User{}).Where("id=? AND balance>=?", userID, cost).Update("balance", gorm.Expr("balance-?", cost))
	if result.Error != nil { // 数据库本身挂了
		return fmt.Errorf("数据库执行失败：%w", result.Error)
	}
	if result.RowsAffected == 0 {
		return errors.New("扣费失败：用户不存在或余额不足")
	}
	return nil

	// TODO(neroji):后面逻辑链条多了可能要加事务

}

// 日志写入辅助函数
func flushBatch(batch []RequestLog) {
	if len(batch) == 0 {
		return
	}
	result := DB.CreateInBatches(&batch, len(batch))
	if result.Error != nil {
		log.Printf("数据库执行失败：%s", result.Error)
	}
}

// 日志函数
func logworker(wg *sync.WaitGroup) {
	// 1
	defer wg.Done()

	// -缓冲切片
	batch := make([]RequestLog, 0, 50)

	// -定时器
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	// 2
	for {
		select {
		case entry, ok := <-LogChan:
			if !ok { // channel被关闭
				flushBatch(batch)
				return
			}
			batch = append(batch, entry)
			if len(batch) >= 50 {
				flushBatch(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			flushBatch(batch)
			batch = batch[:0]
		}
	}

}
