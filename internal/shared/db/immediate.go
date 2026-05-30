package db

import (
	"context"
	"database/sql"
	"fmt"
)

// WithImmediate runs fn inside a SQLite `BEGIN IMMEDIATE` transaction on a
// single pinned connection. IMMEDIATE acquires the write lock at BEGIN time
// rather than on first write, which closes the check-then-insert race window
// that rate limits depend on (CLAUDE.md §7, Technical Plan §4.6).
//
// We can't use sql.DB.BeginTx for this because database/sql always issues a
// plain deferred BEGIN; pinning a *sql.Conn and issuing BEGIN IMMEDIATE by
// hand is the portable way to get an immediate-mode transaction without
// forcing every transaction in the app into immediate mode via the DSN.
func WithImmediate(ctx context.Context, d *sql.DB, fn func(conn *sql.Conn) error) error {
	conn, err := d.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("db: begin immediate: %w", err)
	}
	if err := fn(conn); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		return fmt.Errorf("db: commit: %w", err)
	}
	return nil
}
