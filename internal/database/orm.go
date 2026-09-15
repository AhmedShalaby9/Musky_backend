package database

import (
	"database/sql"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ORM shares the configured pool with the versioned migration runner.
// Application mutations involving multiple records use explicit transactions.
func ORM(pool *sql.DB) (*gorm.DB, error) {
	return gorm.Open(mysql.New(mysql.Config{Conn: pool}), &gorm.Config{
		SkipDefaultTransaction: true,
		NowFunc:                func() time.Time { return time.Now().UTC() },
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
}
