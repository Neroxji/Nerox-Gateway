package storage

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"aigateway/internal/domain"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type Database struct {
	DB      *gorm.DB
	LogChan chan domain.RequestLog
}

func OpenDatabase(dsn string) (*Database, error) {
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层 sqlDB 失败: %w", err)
	}
	sqlDB.SetMaxIdleConns(20)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := db.AutoMigrate(&domain.User{}, &domain.RequestLog{}, &domain.Token{}); err != nil {
		return nil, fmt.Errorf("自动建表失败: %w", err)
	}

	return &Database{
		DB:      db,
		LogChan: make(chan domain.RequestLog, 1000),
	}, nil
}

func (db *Database) FindToken(apiKey string) (domain.Token, error) {
	var token domain.Token
	result := db.DB.Where("token_key=? AND status=?", apiKey, 1).First(&token)
	return token, result.Error
}

func (db *Database) FindUser(id uint) (domain.User, error) {
	var user domain.User
	result := db.DB.Where("id=?", id).First(&user)
	return user, result.Error
}

func (db *Database) FindActiveUser(id uint) (domain.User, error) {
	var user domain.User
	result := db.DB.Where("id=? AND status=?", id, 1).First(&user)
	return user, result.Error
}

func (db *Database) CreateUser(user *domain.User) error {
	return db.DB.Create(user).Error
}

func (db *Database) CreateToken(token *domain.Token) error {
	return db.DB.Create(token).Error
}

func (db *Database) DeactivateUser(id uint) (bool, error) {
	result := db.DB.Model(&domain.User{}).Where("id=?", id).Update("status", false)
	return result.RowsAffected > 0, result.Error
}

func (db *Database) DeductBalance(userID uint, cost int64) error {
	result := db.DB.Model(&domain.User{}).
		Where("id=? AND balance>=?", userID, cost).
		Update("balance", gorm.Expr("balance-?", cost))
	if result.Error != nil {
		return fmt.Errorf("数据库执行失败：%w", result.Error)
	}
	if result.RowsAffected == 0 {
		return errors.New("扣费失败：用户不存在或余额不足")
	}
	return nil
}

func (db *Database) EnqueueLog(entry domain.RequestLog) {
	select {
	case db.LogChan <- entry:
	default:
		log.Println("[警告] 日志队列已满，本次日志被丢弃!")
	}
}

func (db *Database) StartLogWorkers(wg *sync.WaitGroup, count int) {
	for i := 0; i < count; i++ {
		wg.Add(1)
		go db.logWorker(wg)
	}
}

func (db *Database) CloseLogs() {
	close(db.LogChan)
}

func (db *Database) logWorker(wg *sync.WaitGroup) {
	defer wg.Done()

	batch := make([]domain.RequestLog, 0, 50)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		result := db.DB.CreateInBatches(&batch, len(batch))
		if result.Error != nil {
			log.Printf("数据库执行失败：%s", result.Error)
		}
		batch = batch[:0]
	}

	for {
		select {
		case entry, ok := <-db.LogChan:
			if !ok {
				flush()
				return
			}
			batch = append(batch, entry)
			if len(batch) >= 50 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
