// Package db opens the SQLite database with the pragmas Hearth requires and
// applies embedded goose migrations on startup.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open initialises the SQLite connection pool. The pool is intentionally small
// (SQLite serialises writes anyway) and WAL + foreign-key pragmas are set on
// every connection via the DSN.
func Open(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("db: empty path")
	}
	if path != ":memory:" {
		if err := ensureDir(path); err != nil {
			return nil, err
		}
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	d.SetMaxOpenConns(5)
	d.SetMaxIdleConns(5)
	if err := d.PingContext(context.Background()); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return d, nil
}

// Migrate applies all embedded migrations using goose.
func Migrate(d *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("db: set dialect: %w", err)
	}
	if err := goose.Up(d, "migrations"); err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	return nil
}

func ensureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return mkdirAll(dir)
}
