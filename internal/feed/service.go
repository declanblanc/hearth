// Package feed builds the chronological feed for a user: their connections'
// (and their own) active posts in reverse-chronological order, paged via a
// post-id cursor (Technical Plan §4.1).
package feed

import (
	"context"
	"database/sql"
	"strings"

	"github.com/dblanc/hearth/internal/connections"
	"github.com/dblanc/hearth/internal/posts"
)

// PageSize is the number of posts fetched per feed query (Technical Plan §4.1).
const PageSize = 50

type Service struct {
	DB    *sql.DB
	Conns *connections.Service
}

func New(d *sql.DB, conns *connections.Service) *Service {
	return &Service{DB: d, Conns: conns}
}

// Result is the first page of the feed: the most recent posts plus a pagination
// cursor for loading older ones.
type Result struct {
	Posts   []posts.Post
	HasMore bool
	// NextBefore is the cursor for "load older posts" (the id of the last post
	// returned). Zero when there are no more.
	NextBefore int64
}

// FirstPage returns the most recent page of the viewer's feed.
func (s *Service) FirstPage(ctx context.Context, userID int64) (Result, error) {
	rows, err := s.queryPosts(ctx, userID, 0)
	if err != nil {
		return Result{}, err
	}

	res := Result{Posts: rows}
	if len(rows) == PageSize {
		res.HasMore = true
		res.NextBefore = rows[len(rows)-1].ID
	}
	return res, nil
}

// OlderPage returns posts older than the given cursor.
func (s *Service) OlderPage(ctx context.Context, userID, before int64) ([]posts.Post, int64, error) {
	rows, err := s.queryPosts(ctx, userID, before)
	if err != nil {
		return nil, 0, err
	}
	var next int64
	if len(rows) == PageSize {
		next = rows[len(rows)-1].ID
	}
	return rows, next, nil
}

// queryPosts runs the feed query for a user's connections plus the viewer's own
// posts (issue #41). before==0 means the first page (no cursor).
func (s *Service) queryPosts(ctx context.Context, userID, before int64) ([]posts.Post, error) {
	ids, err := s.Conns.ConnectionIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Always include the viewer so their own posts appear in the home feed
	// alongside their connections' posts. This also means a user with no
	// connections still sees their own posts.
	ids = append(ids, userID)

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}

	query := `
		SELECT p.id, p.author_id, u.username, u.first_name, u.last_name,
		       u.photo_key, p.content, p.created_at, p.updated_at
		  FROM posts p
		  JOIN users u ON u.id = p.author_id
		 WHERE p.author_id IN (` + placeholders + `)
		   AND p.status = 'active'`
	if before > 0 {
		query += ` AND p.id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY p.created_at DESC, p.id DESC LIMIT ?`
	args = append(args, PageSize)

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out, err := posts.ScanRows(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := posts.LoadMedia(ctx, s.DB, out); err != nil {
		return nil, err
	}
	// Mark the viewer's own posts so the feed shows a delete control on them
	// (issue #47).
	for i := range out {
		out[i].CanDelete = out[i].AuthorID == userID
	}
	return out, nil
}
