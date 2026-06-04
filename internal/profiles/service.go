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
	ID        int64
	Username  string
	FirstName string
	LastName  string
	Bio       string
	Pronouns  string
	PhotoKey  string
}

// FullName returns the display name shown to users.
func (p *Profile) FullName() string {
	if p.LastName == "" {
		return p.FirstName
	}
	return p.FirstName + " " + p.LastName
}

var ErrNotFound = errors.New("profiles: not found")

func (s *Service) Get(ctx context.Context, userID int64) (*Profile, error) {
	var (
		p        Profile
		bio, pro sql.NullString
		photo    sql.NullString
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, username, first_name, last_name, bio, pronouns, photo_key
		   FROM users WHERE id = ? AND deleted_at IS NULL`, userID,
	).Scan(&p.ID, &p.Username, &p.FirstName, &p.LastName, &bio, &pro, &photo)
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
		`SELECT id, username, first_name, last_name, bio, pronouns, photo_key
		   FROM users WHERE username = ? AND deleted_at IS NULL`, username,
	).Scan(&p.ID, &p.Username, &p.FirstName, &p.LastName, &bio, &pro, &photo)
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
	FirstName string
	LastName  string
	Bio       string
	Pronouns  string
}

func (s *Service) Update(ctx context.Context, userID int64, in UpdateInput) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE users SET first_name = ?, last_name = ?, bio = ?, pronouns = ? WHERE id = ? AND deleted_at IS NULL`,
		in.FirstName, in.LastName, nullable(in.Bio), nullable(in.Pronouns), userID,
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
		       first_name = '[deleted]',
		       last_name = '',
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
//
// Profile photos are already removed from R2 at soft-delete time. Post images,
// however, persist until hard delete, so we gather their object keys before the
// cascading row delete and remove them from storage afterwards (Build Plan
// §0.5/§2.5). The ON DELETE CASCADE chain (users → posts → post_media) clears
// the rows; only the objects need explicit cleanup.
func (s *Service) HardDeleteExpired(ctx context.Context) (int64, error) {
	cutoff := s.Now().Add(-30 * 24 * time.Hour)

	mediaKeys, err := s.expiredPostMediaKeys(ctx, cutoff)
	if err != nil {
		return 0, err
	}

	res, err := s.DB.ExecContext(ctx, `DELETE FROM users WHERE deleted_at IS NOT NULL AND deleted_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}

	// Best-effort storage cleanup after the rows are gone. A leaked object is
	// recoverable by a later sweep; failing the whole delete over it is not
	// worth it.
	if s.Media != nil {
		for _, key := range mediaKeys {
			_ = s.Media.Delete(ctx, key)
		}
	}
	return res.RowsAffected()
}

// expiredPostMediaKeys returns the R2 object keys for all post images belonging
// to users whose soft-delete has passed the cutoff.
func (s *Service) expiredPostMediaKeys(ctx context.Context, cutoff time.Time) ([]string, error) {
	if s.Media == nil {
		return nil, nil
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT pm.object_key
		  FROM post_media pm
		  JOIN posts p ON p.id = pm.post_id
		  JOIN users u ON u.id = p.author_id
		 WHERE u.deleted_at IS NOT NULL AND u.deleted_at < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
