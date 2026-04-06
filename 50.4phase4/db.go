package main

import (
	"errors"
	"fmt"
	"log"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

var DB *gorm.DB

// 初始化数据库
func InitDB(dsn string) {

	// 用 GORM 连上数据库
	var err error
	DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("连接 MySQL 失败: %v", err)
	}
	log.Println("成功连接 MySQL 数据库!")

	// 拿到底层的对象
	sqlDB, err := DB.DB()
	if err != nil {
		log.Fatalf("获取底层 sqlDB 失败: %v", err)
	}
	sqlDB.SetMaxIdleConns(20)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// 自动迁移
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
