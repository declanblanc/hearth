// Package posts owns post creation, deletion, and the per-author listing used
// by profile pages. As of Phase 2 a post may carry up to MaxImagesPerPost
// images stored in R2; edits, comments, and archive arrive later in Phase 2.
package posts

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dblanc/hearth/internal/media"
	"github.com/dblanc/hearth/internal/shared/db"
)

// MaxContentLen is the post length cap, measured in runes after trimming.
const MaxContentLen = 1000

// MediaURLTTL is how long a signed post-image URL stays valid (CLAUDE.md §6).
const MediaURLTTL = time.Hour

var (
	ErrEmpty         = errors.New("posts: content is empty")
	ErrTooLong       = errors.New("posts: content exceeds 1000 characters")
	ErrTooManyImages = errors.New("posts: too many images")
	ErrNotFound      = errors.New("posts: not found")
	ErrNotAuthor     = errors.New("posts: not the author")
)

type Service struct {
	DB    *sql.DB
	Now   func() time.Time
	Media media.Store // may be nil in dev/test (image uploads disabled)
}

func New(d *sql.DB) *Service { return &Service{DB: d, Now: time.Now} }

// Post is a post prepared for rendering, with the author's display fields
// joined in. Content is stored and returned verbatim — escaping happens at
// render time (CLAUDE.md / §1.5).
type Post struct {
	ID             int64
	AuthorID       int64
	AuthorUsername string
	AuthorName     string
	Content        string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Media          []PostMedia
}

// PostMedia is one image attached to a post. Key/ContentType come from the
// database; URL is left empty until SignMediaURLs mints a signed link at render
// time, so unrendered posts never hold a usable URL.
type PostMedia struct {
	Key         string
	ContentType string
	Position    int
	URL         string
}

// NewImage is a validated image ready to be stored with a post. The handler
// performs size and MIME-sniff checks before constructing one.
type NewImage struct {
	Data        []byte
	ContentType string
	Ext         string
}

// Validate trims and checks content length, returning the trimmed value.
// It is used for the text-only path; image-bearing posts may have empty text,
// which Create allows directly.
func Validate(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", ErrEmpty
	}
	if len([]rune(trimmed)) > MaxContentLen {
		return "", ErrTooLong
	}
	return trimmed, nil
}

// Create stores a new active post for the author and returns its ID. A post
// must have either text or at least one image. Images are uploaded to object
// storage first; the post row and its post_media rows are then written together
// in a single BEGIN IMMEDIATE transaction. If the transaction fails, any
// objects already uploaded are removed so storage doesn't leak.
func (s *Service) Create(ctx context.Context, authorID int64, content string, images []NewImage) (int64, error) {
	trimmed := strings.TrimSpace(content)
	if len([]rune(trimmed)) > MaxContentLen {
		return 0, ErrTooLong
	}
	if trimmed == "" && len(images) == 0 {
		return 0, ErrEmpty
	}
	if len(images) > media.MaxImagesPerPost {
		return 0, ErrTooManyImages
	}
	if len(images) > 0 && s.Media == nil {
		return 0, errors.New("posts: media store not configured")
	}

	now := s.Now()

	// Upload objects before opening the write transaction — R2 is not part of
	// the SQL transaction, so we keep its slow network calls out of the lock.
	uploadedKeys := make([]string, 0, len(images))
	for _, img := range images {
		key := media.NewPostKey(now, img.Ext)
		if err := s.Media.Upload(ctx, key, bytes.NewReader(img.Data), img.ContentType); err != nil {
			s.cleanupKeys(ctx, uploadedKeys)
			return 0, fmt.Errorf("posts: upload image: %w", err)
		}
		uploadedKeys = append(uploadedKeys, key)
	}

	var postID int64
	err := db.WithImmediate(ctx, s.DB, func(conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx,
			`INSERT INTO posts (author_id, content, status, created_at, updated_at)
			 VALUES (?, ?, 'active', ?, ?)`,
			authorID, trimmed, now, now)
		if err != nil {
			return err
		}
		postID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		for i, img := range images {
			if _, err := conn.ExecContext(ctx,
				`INSERT INTO post_media (post_id, object_key, content_type, position, created_at)
				 VALUES (?, ?, ?, ?, ?)`,
				postID, uploadedKeys[i], img.ContentType, i, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.cleanupKeys(ctx, uploadedKeys)
		return 0, err
	}
	return postID, nil
}

// cleanupKeys best-effort removes orphaned objects after a failed Create.
func (s *Service) cleanupKeys(ctx context.Context, keys []string) {
	if s.Media == nil {
		return
	}
	for _, k := range keys {
		_ = s.Media.Delete(ctx, k)
	}
}

// ListByAuthor returns the author's active posts, newest first. Used by profile
// pages; the caller must have already verified the viewer may see this author.
func (s *Service) ListByAuthor(ctx context.Context, authorID int64) ([]Post, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT p.id, p.author_id, u.username, u.first_name, u.last_name,
		       p.content, p.created_at, p.updated_at
		  FROM posts p
		  JOIN users u ON u.id = p.author_id
		 WHERE p.author_id = ? AND p.status = 'active'
		 ORDER BY p.created_at DESC, p.id DESC`, authorID)
	if err != nil {
		return nil, err
	}
	out, err := ScanRows(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := LoadMedia(ctx, s.DB, out); err != nil {
		return nil, err
	}
	return out, nil
}

// LoadMedia batch-loads attached images for the given posts and assigns them to
// each post's Media field, ordered by position. It issues a single query for
// the whole slice, so the feed's 50-post page stays at one round-trip. Posts
// without media are left with a nil Media slice.
//
// It lives as a package function (rather than a Service method) so both the
// posts and feed packages — the feed has only a *sql.DB, not a Service — can
// share the same row shape.
func LoadMedia(ctx context.Context, sqldb *sql.DB, ps []Post) error {
	if len(ps) == 0 {
		return nil
	}
	byID := make(map[int64]*Post, len(ps))
	placeholders := make([]string, len(ps))
	args := make([]any, len(ps))
	for i := range ps {
		byID[ps[i].ID] = &ps[i]
		placeholders[i] = "?"
		args[i] = ps[i].ID
	}

	rows, err := sqldb.QueryContext(ctx, `
		SELECT post_id, object_key, content_type, position
		  FROM post_media
		 WHERE post_id IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY post_id, position`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			postID int64
			m      PostMedia
		)
		if err := rows.Scan(&postID, &m.Key, &m.ContentType, &m.Position); err != nil {
			return err
		}
		if p := byID[postID]; p != nil {
			p.Media = append(p.Media, m)
		}
	}
	return rows.Err()
}

// SignMediaURLs mints a short-lived signed URL for every image on every post,
// in place. The caller MUST have already confirmed the viewer is connected to
// the author(s) — signing is the last step before render, never an access
// check in itself (CLAUDE.md §6). A nil store (dev without R2) is a no-op,
// leaving URLs empty so images simply don't render.
func SignMediaURLs(ctx context.Context, store media.Store, ps []Post) error {
	if store == nil {
		return nil
	}
	for i := range ps {
		for j := range ps[i].Media {
			url, err := store.PresignGet(ctx, ps[i].Media[j].Key, MediaURLTTL)
			if err != nil {
				return err
			}
			ps[i].Media[j].URL = url
		}
	}
	return nil
}

// Delete marks a post deleted: status='deleted', content nulled, and any
// attached media removed — both the post_media rows and the underlying objects
// in storage (CLAUDE.md §10, Build Plan §2.5). Only the author may delete.
func (s *Service) Delete(ctx context.Context, postID, requesterID int64) error {
	var authorID int64
	err := s.DB.QueryRowContext(ctx,
		`SELECT author_id FROM posts WHERE id = ? AND status != 'deleted'`, postID,
	).Scan(&authorID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if authorID != requesterID {
		return ErrNotAuthor
	}

	// Collect object keys before dropping the rows so we know what to remove
	// from storage after the DB writes commit.
	keys, err := mediaKeys(ctx, s.DB, postID)
	if err != nil {
		return err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM post_media WHERE post_id = ?`, postID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE posts SET content = '', status = 'deleted', updated_at = ? WHERE id = ?`,
		s.Now(), postID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Best-effort storage cleanup after commit. A leaked object is recoverable;
	// a dangling DB row pointing at a missing object is not, so order matters.
	s.cleanupKeys(ctx, keys)
	return nil
}

// mediaKeys returns the object keys for a post's images.
func mediaKeys(ctx context.Context, sqldb *sql.DB, postID int64) ([]string, error) {
	rows, err := sqldb.QueryContext(ctx,
		`SELECT object_key FROM post_media WHERE post_id = ?`, postID)
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

// ScanRows reads a posts query result into a slice. The SELECT column order
// must be: id, author_id, username, first_name, last_name, content,
// created_at, updated_at. Shared with the feed package so the row shape stays
// consistent across both queries.
func ScanRows(rows *sql.Rows) ([]Post, error) {
	var out []Post
	for rows.Next() {
		var (
			p           Post
			first, last string
		)
		if err := rows.Scan(&p.ID, &p.AuthorID, &p.AuthorUsername, &first, &last,
			&p.Content, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.AuthorName = fullName(first, last)
		out = append(out, p)
	}
	return out, rows.Err()
}

func fullName(first, last string) string {
	if last == "" {
		return first
	}
	if first == "" {
		return last
	}
	return first + " " + last
}
