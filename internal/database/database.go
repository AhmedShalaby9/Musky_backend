package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid MYSQL_DSN configuration")
	}
	if cfg.DBName == "" {
		return nil, fmt.Errorf("MYSQL_DSN must name a database")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.MultiStatements = false
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(3 * time.Minute)
	db.SetConnMaxIdleTime(90 * time.Second)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// MySQL DDL commits implicitly. Statements are restartable, and migration
// execution is serialized on a dedicated connection before recording a version.
func Migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(CONCAT(DATABASE(), ':musky:migrate'), 10)").Scan(&locked); err != nil {
		return err
	}
	if locked != 1 {
		return fmt.Errorf("migration lock unavailable")
	}
	defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(CONCAT(DATABASE(), ':musky:migrate'))")
	if _, err = conn.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version VARCHAR(100) PRIMARY KEY, applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6))"); err != nil {
		return err
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, file := range files {
		var exists int
		if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", file.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			continue
		}
		if file.Name() == "002_one_trader_per_tenant.sql" {
			var incompatible int
			if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM tenants t WHERE (SELECT COUNT(*) FROM users u WHERE u.tenant_id=t.id AND u.role='trader') <> 1").Scan(&incompatible); err != nil {
				return err
			}
			if incompatible != 0 {
				return fmt.Errorf("migration requires exactly one trader per existing tenant; assign missing owners or split shared tenants explicitly before upgrading")
			}
		}
		body, err := migrations.ReadFile("migrations/" + file.Name())
		if err != nil {
			return err
		}
		for _, statement := range strings.Split(string(body), ";") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err = conn.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration %s: %w", file.Name(), err)
			}
		}
		if _, err = conn.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES (?)", file.Name()); err != nil {
			return err
		}
	}
	return nil
}
