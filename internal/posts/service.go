// Package posts owns text post creation, deletion, and the per-author listing
// used by profile pages. Images, edits, comments, and archive arrive in Phase 2.
package posts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// MaxContentLen is the post length cap, measured in runes after trimming.
const MaxContentLen = 1000

var (
	ErrEmpty     = errors.New("posts: content is empty")
	ErrTooLong   = errors.New("posts: content exceeds 1000 characters")
	ErrNotFound  = errors.New("posts: not found")
	ErrNotAuthor = errors.New("posts: not the author")
)

type Service struct {
	DB  *sql.DB
	Now func() time.Time
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
}

// Validate trims and checks content length, returning the trimmed value.
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

// Create stores a new active post for the author and returns its ID.
func (s *Service) Create(ctx context.Context, authorID int64, content string) (int64, error) {
	trimmed, err := Validate(content)
	if err != nil {
		return 0, err
	}
	now := s.Now()
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO posts (author_id, content, status, created_at, updated_at)
		 VALUES (?, ?, 'active', ?, ?)`,
		authorID, trimmed, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
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
	defer rows.Close()
	return ScanRows(rows)
}

// Delete marks a post deleted: status='deleted', content nulled. Only the
// author may delete. In Phase 1 there are no post_edits or media rows to clear
// (those land in Phase 2's delete cascade).
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
	_, err = s.DB.ExecContext(ctx,
		`UPDATE posts SET content = '', status = 'deleted', updated_at = ? WHERE id = ?`,
		s.Now(), postID)
	return err
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
