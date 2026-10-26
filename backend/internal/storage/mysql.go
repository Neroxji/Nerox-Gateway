package storage

import (
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"aigateway/internal/domain"
	"aigateway/internal/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

type Database struct {
	DB      *gorm.DB
	LogChan chan domain.RequestLog
}

func OpenDatabase(dsn string) (*Database, error) {
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormLogger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), gormLogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormLogger.Warn,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
		}),
	})
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

	if err := migrateLegacyTokenSchema(db); err != nil {
		return nil, fmt.Errorf("迁移 Token 表失败: %w", err)
	}
	if err := db.AutoMigrate(&domain.User{}, &domain.RequestLog{}, &domain.Token{}); err != nil {
		return nil, fmt.Errorf("自动建表失败: %w", err)
	}
	if err := dropLegacyTokenColumn(db); err != nil {
		return nil, fmt.Errorf("清理旧 Token 明文列失败: %w", err)
	}

	return &Database{
		DB:      db,
		LogChan: make(chan domain.RequestLog, 1000),
	}, nil
}

func (db *Database) FindToken(apiKey string) (domain.Token, error) {
	var token domain.Token
	result := db.DB.Where("token_hash=? AND status=? AND expires_at>?", security.HashToken(apiKey), 1, time.Now()).First(&token)
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

func (db *Database) RevokeToken(id uint) (domain.Token, bool, error) {
	var token domain.Token
	result := db.DB.Where("id=? AND status=?", id, 1).First(&token)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return domain.Token{}, false, nil
	}
	if result.Error != nil {
		return domain.Token{}, false, result.Error
	}

	result = db.DB.Model(&domain.Token{}).
		Where("id=? AND status=?", id, 1).
		Update("status", 0)
	if result.Error != nil {
		return domain.Token{}, false, result.Error
	}
	return token, result.RowsAffected > 0, nil
}

func (db *Database) DeactivateUser(id uint) (bool, error) {
	result := db.DB.Model(&domain.User{}).Where("id=?", id).Update("status", false)
	return result.RowsAffected > 0, result.Error
}

func migrateLegacyTokenSchema(db *gorm.DB) error {
	if !db.Migrator().HasTable(&domain.Token{}) {
		return nil
	}

	hasHash, err := hasTokenColumn(db, "token_hash")
	if err != nil {
		return err
	}
	if !hasHash {
		if err := db.Exec("ALTER TABLE tokens ADD COLUMN token_hash VARCHAR(64) NULL").Error; err != nil {
			return err
		}
	}

	hasExpiry, err := hasTokenColumn(db, "expires_at")
	if err != nil {
		return err
	}
	if !hasExpiry {
		if err := db.Exec("ALTER TABLE tokens ADD COLUMN expires_at DATETIME NULL").Error; err != nil {
			return err
		}
	}

	hasLegacyKey, err := hasTokenColumn(db, "token_key")
	if err != nil {
		return err
	}
	if hasLegacyKey {
		if err := db.Exec("UPDATE tokens SET token_hash=SHA2(token_key,256) WHERE token_hash IS NULL OR token_hash='' ").Error; err != nil {
			return err
		}
		if err := db.Exec("UPDATE tokens SET expires_at=DATE_ADD(created_at, INTERVAL 90 DAY) WHERE expires_at IS NULL").Error; err != nil {
			return err
		}
	}

	var missing int64
	if err := db.Raw("SELECT COUNT(*) FROM tokens WHERE token_hash IS NULL OR token_hash='' OR expires_at IS NULL").Scan(&missing).Error; err != nil {
		return err
	}
	if missing > 0 {
		return fmt.Errorf("存在 %d 条无法迁移的 Token 记录", missing)
	}
	return nil
}

func dropLegacyTokenColumn(db *gorm.DB) error {
	if !db.Migrator().HasTable(&domain.Token{}) {
		return nil
	}
	hasLegacyKey, err := hasTokenColumn(db, "token_key")
	if err != nil || !hasLegacyKey {
		return err
	}
	return db.Exec("ALTER TABLE tokens DROP COLUMN token_key").Error
}

func hasTokenColumn(db *gorm.DB, column string) (bool, error) {
	var count int64
	err := db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_schema = DATABASE()
		  AND table_name = 'tokens'
		  AND column_name = ?`, column).Scan(&count).Error
	return count > 0, err
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
