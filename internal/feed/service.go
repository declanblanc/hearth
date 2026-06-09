// Package feed builds the chronological feed for a user: their connections'
// active posts, split into "new" and "old" by users.last_feed_loaded_at
// (Technical Plan §4.1). The split timestamp is read before the query and the
// stored value is advanced after it, so a load never reclassifies its own
// results.
package feed

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/dblanc/hearth/internal/connections"
	"github.com/dblanc/hearth/internal/posts"
)

// PageSize is the number of posts fetched per feed query (Technical Plan §4.1).
const PageSize = 50

type Service struct {
	DB    *sql.DB
	Conns *connections.Service
	Now   func() time.Time
}

func New(d *sql.DB, conns *connections.Service) *Service {
	return &Service{DB: d, Conns: conns, Now: time.Now}
}

// Result is the first-page feed: posts since the last visit ("New") and earlier
// ones ("Old", shown behind a toggle), plus a pagination cursor.
type Result struct {
	New     []posts.Post
	Old     []posts.Post
	HasMore bool
	// NextBefore is the cursor for "load older posts" (the id of the last post
	// returned). Zero when there are no more.
	NextBefore int64
}

// FirstPage returns the user's feed and advances last_feed_loaded_at. The
// update runs only after the rows are fetched and the split timestamp captured,
// per §4.1 step ordering.
func (s *Service) FirstPage(ctx context.Context, userID int64) (Result, error) {
	lastLoaded, err := s.lastFeedLoadedAt(ctx, userID)
	if err != nil {
		return Result{}, err
	}

	rows, err := s.queryPosts(ctx, userID, 0)
	if err != nil {
		return Result{}, err
	}

	var res Result
	for _, p := range rows {
		// A NULL last_feed_loaded_at (brand-new account) means everything is new.
		if !lastLoaded.Valid || p.CreatedAt.After(lastLoaded.Time) {
			res.New = append(res.New, p)
		} else {
			res.Old = append(res.Old, p)
		}
	}
	if len(rows) == PageSize {
		res.HasMore = true
		res.NextBefore = rows[len(rows)-1].ID
	}

	// Advance the marker AFTER classifying this page (§4.1 step 4).
	if _, err := s.DB.ExecContext(ctx,
		`UPDATE users SET last_feed_loaded_at = ? WHERE id = ?`, s.Now(), userID); err != nil {
		return Result{}, err
	}
	return res, nil
}

// OlderPage returns posts older than the given cursor. It does not touch
// last_feed_loaded_at — paging through history isn't "seeing new posts".
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

func (s *Service) lastFeedLoadedAt(ctx context.Context, userID int64) (sql.NullTime, error) {
	var t sql.NullTime
	err := s.DB.QueryRowContext(ctx,
		`SELECT last_feed_loaded_at FROM users WHERE id = ?`, userID).Scan(&t)
	return t, err
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
	return out, nil
}
