// Package profiles handles profile read/edit and account deletion.
package profiles

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/dblanc/hearth/internal/media"
)

type Service struct {
	DB    *sql.DB
	Now   func() time.Time
	Media media.Store // may be nil in dev/test
}

func New(db *sql.DB) *Service { return &Service{DB: db, Now: time.Now} }

type Profile struct {
	ID          int64
	Username    string
	DisplayName string
	Bio         string
	Pronouns    string
	PhotoKey    string
}

var ErrNotFound = errors.New("profiles: not found")

func (s *Service) Get(ctx context.Context, userID int64) (*Profile, error) {
	var (
		p        Profile
		bio, pro sql.NullString
		photo    sql.NullString
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, username, display_name, bio, pronouns, photo_key
		   FROM users WHERE id = ? AND deleted_at IS NULL`, userID,
	).Scan(&p.ID, &p.Username, &p.DisplayName, &bio, &pro, &photo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Bio = bio.String
	p.Pronouns = pro.String
	p.PhotoKey = photo.String
	return &p, nil
}

func (s *Service) GetByUsername(ctx context.Context, username string) (*Profile, error) {
	var (
		p        Profile
		bio, pro sql.NullString
		photo    sql.NullString
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, username, display_name, bio, pronouns, photo_key
		   FROM users WHERE username = ? AND deleted_at IS NULL`, username,
	).Scan(&p.ID, &p.Username, &p.DisplayName, &bio, &pro, &photo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Bio = bio.String
	p.Pronouns = pro.String
	p.PhotoKey = photo.String
	return &p, nil
}

type UpdateInput struct {
	DisplayName string
	Bio         string
	Pronouns    string
}

func (s *Service) Update(ctx context.Context, userID int64, in UpdateInput) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE users SET display_name = ?, bio = ?, pronouns = ? WHERE id = ? AND deleted_at IS NULL`,
		in.DisplayName, nullable(in.Bio), nullable(in.Pronouns), userID,
	)
	return err
}

// UpdatePhoto swaps the stored photo_key for userID and returns the previous
// key (empty string if none). The caller is responsible for deleting the old
// key from R2 after this returns successfully.
func (s *Service) UpdatePhoto(ctx context.Context, userID int64, newKey string) (string, error) {
	var old sql.NullString
	if err := s.DB.QueryRowContext(ctx,
		`SELECT photo_key FROM users WHERE id = ? AND deleted_at IS NULL`, userID,
	).Scan(&old); err != nil {
		return "", err
	}
	if _, err := s.DB.ExecContext(ctx,
		`UPDATE users SET photo_key = ? WHERE id = ? AND deleted_at IS NULL`,
		newKey, userID,
	); err != nil {
		return "", err
	}
	return old.String, nil
}

// SoftDelete anonymises the user, terminates their sessions, and removes their
// profile photo from R2 (if Media is set). A nightly sweep performs the hard
// delete after 30 days.
func (s *Service) SoftDelete(ctx context.Context, userID int64) error {
	// Fetch photo key before the transaction so we can clean up R2 after commit.
	var photoKey sql.NullString
	_ = s.DB.QueryRowContext(ctx,
		`SELECT photo_key FROM users WHERE id = ? AND deleted_at IS NULL`, userID,
	).Scan(&photoKey)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	now := s.Now()
	anonEmail := fmt.Sprintf("deleted-%d-%d@example.invalid", userID, now.UnixNano())
	if _, err := tx.ExecContext(ctx, `
		UPDATE users
		   SET deleted_at = ?,
		       email = ?,
		       display_name = '[deleted user]',
		       bio = NULL,
		       pronouns = NULL,
		       photo_key = NULL
		 WHERE id = ?`, now, anonEmail, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Best-effort R2 cleanup after commit — per spec §0.7, photo removed at soft-delete time.
	if s.Media != nil && photoKey.Valid && photoKey.String != "" {
		_ = s.Media.Delete(ctx, photoKey.String)
	}
	return nil
}

// HardDeleteExpired removes users whose soft-delete is older than 30 days.
// Returns the count removed. The Technical Plan calls for this to run hourly
// (cron or Fly scheduled machine).
// Profile photos are already removed from R2 at soft-delete time, so no R2
// cleanup is needed here. Post/comment media cleanup will be added in Phase 1/2.
func (s *Service) HardDeleteExpired(ctx context.Context) (int64, error) {
	cutoff := s.Now().Add(-30 * 24 * time.Hour)
	// ON DELETE CASCADE on sessions handles cleanup; posts/comments/media will
	// be added in Phase 1/2 and must be cascade-deleted too.
	res, err := s.DB.ExecContext(ctx, `DELETE FROM users WHERE deleted_at IS NOT NULL AND deleted_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
