//go:build integration

package storage

import (
	"os"
	"testing"
	"time"

	"aigateway/internal/domain"
	"aigateway/internal/security"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type legacyToken struct {
	ID        uint   `gorm:"primarykey"`
	UserID    uint   `gorm:"not null"`
	Name      string `gorm:"size:50"`
	TokenKey  string `gorm:"uniqueIndex;size:100;not null"`
	Status    int    `gorm:"default:1"`
	CreatedAt time.Time
}

func (legacyToken) TableName() string {
	return "tokens"
}

func TestLegacyTokenSchemaMigration(t *testing.T) {
	dsn := os.Getenv("AIGATEWAY_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Fatal("AIGATEWAY_TEST_MYSQL_DSN must point to a dedicated test MySQL database")
	}

	legacyDB, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy test database: %v", err)
	}
	legacySQLDB, err := legacyDB.DB()
	if err != nil {
		t.Fatalf("get legacy sql database: %v", err)
	}
	defer legacySQLDB.Close()

	if err := legacyDB.Exec("DROP TABLE IF EXISTS tokens").Error; err != nil {
		t.Fatalf("drop old tokens table: %v", err)
	}
	if err := legacyDB.AutoMigrate(&legacyToken{}); err != nil {
		t.Fatalf("create legacy tokens table: %v", err)
	}
	createdAt := time.Now().Add(-24 * time.Hour).Truncate(time.Millisecond)
	legacyRawToken := "sk-integration-legacy-token"
	if err := legacyDB.Create(&legacyToken{
		UserID:    1,
		Name:      "legacy",
		TokenKey:  legacyRawToken,
		Status:    1,
		CreatedAt: createdAt,
	}).Error; err != nil {
		t.Fatalf("insert legacy token: %v", err)
	}

	database, err := OpenDatabase(dsn)
	if err != nil {
		t.Fatalf("run Token migration: %v", err)
	}
	databaseSQLDB, err := database.DB.DB()
	if err != nil {
		t.Fatalf("get migrated sql database: %v", err)
	}
	defer databaseSQLDB.Close()
	defer database.DB.Exec("DROP TABLE IF EXISTS tokens")

	var migrated domain.Token
	if err := database.DB.First(&migrated).Error; err != nil {
		t.Fatalf("load migrated token: %v", err)
	}
	if migrated.TokenHash != security.HashToken(legacyRawToken) {
		t.Fatalf("token hash = %q; want hash of legacy token", migrated.TokenHash)
	}
	if migrated.ExpiresAt.Before(createdAt.Add(89*24*time.Hour)) || migrated.ExpiresAt.After(createdAt.Add(91*24*time.Hour)) {
		t.Fatalf("expires_at = %v; want approximately created_at + 90 days", migrated.ExpiresAt)
	}

	for _, column := range []string{"token_hash", "expires_at"} {
		hasColumn, err := hasTokenColumn(database.DB, column)
		if err != nil {
			t.Fatalf("check %s column: %v", column, err)
		}
		if !hasColumn {
			t.Fatalf("migrated tokens table is missing %s", column)
		}
	}
	hasLegacyColumn, err := hasTokenColumn(database.DB, "token_key")
	if err != nil {
		t.Fatalf("check legacy token_key column: %v", err)
	}
	if hasLegacyColumn {
		t.Fatal("migrated tokens table still contains token_key")
	}
}
