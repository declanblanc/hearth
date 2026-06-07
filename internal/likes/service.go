// Package likes owns the private "like" signal. Liking a post notifies its
// author and records a row here; the author — and only the author — can see the
// list of who liked. There is no public indicator and no like count anywhere
// (CLAUDE.md §2, §11; Technical Plan §4.4.5).
package likes

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/dblanc/hearth/internal/notifications"
	"github.com/dblanc/hearth/internal/posts"
	"github.com/dblanc/hearth/internal/shared/db"
)

var (
	// ErrNotFound is returned when the post doesn't exist, isn't active, or the
	// viewer isn't allowed to see it. Handlers map it to 404 — non-existence is
	// part of the privacy model (CLAUDE.md §1).
	ErrNotFound = errors.New("likes: post not found")
	// ErrNotAuthor is returned when a non-author asks for a post's liker list.
	// Handlers also map this to 404, never 403, so the list's existence stays
	// hidden from anyone but the author.
	ErrNotAuthor = errors.New("likes: not the post author")
)

// Connections is the slice of the connection graph this package needs: the
// single privacy chokepoint (CLAUDE.md §1). *connections.Service satisfies it.
type Connections interface {
	IsConnected(ctx context.Context, viewerID, authorID int64) (bool, error)
}

type Service struct {
	DB     *sql.DB
	Conns  Connections
	Notifs *notifications.Service
}

func New(d *sql.DB, conns Connections, notifs *notifications.Service) *Service {
	return &Service{DB: d, Conns: conns, Notifs: notifs}
}

// Like records that userID likes the post and notifies the author. It is
// idempotent: liking an already-liked post is a no-op and never sends a second
// notification. Liking your own post is allowed but never notifies you. A viewer
// who can't see the post (not connected to the author) gets ErrNotFound.
func (s *Service) Like(ctx context.Context, postID, userID int64) error {
	authorID, err := s.postAuthor(ctx, postID)
	if err != nil {
		return err
	}
	// Self-likes skip the connection check (you can always see your own post);
	// everyone else must be connected to the author.
	if authorID != userID {
		connected, err := s.Conns.IsConnected(ctx, userID, authorID)
		if err != nil {
			return err
		}
		if !connected {
			return ErrNotFound
		}
	}

	return db.WithImmediate(ctx, s.DB, func(conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx,
			`INSERT OR IGNORE INTO likes (post_id, user_id) VALUES (?, ?)`,
			postID, userID)
		if err != nil {
			return err
		}
		// 0 rows means the like already existed: stay idempotent and don't
		// re-notify.
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if authorID == userID {
			return nil // no self-notification
		}
		return s.Notifs.Create(ctx, conn, notifications.Params{
			UserID:  authorID,
			Type:    notifications.TypeLikeOnPost,
			ActorID: userID,
			PostID:  postID,
		})
	})
}

// Unlike removes userID's like and the notification it generated, so an unliked
// post leaves no stale "{name} liked your post" entry in the author's inbox. It
// is idempotent — unliking a post you haven't liked is a harmless no-op — so it
// needs no connection check.
func (s *Service) Unlike(ctx context.Context, postID, userID int64) error {
	return db.WithImmediate(ctx, s.DB, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx,
			`DELETE FROM likes WHERE post_id = ? AND user_id = ?`, postID, userID); err != nil {
			return err
		}
		return s.Notifs.DeleteForActorOnPost(ctx, conn, userID, postID, notifications.TypeLikeOnPost)
	})
}

// Liker is one entry in a post's liker list. Names only — never a count
// (CLAUDE.md §2).
type Liker struct {
	Username string
	Name     string
}

// ListLikers returns the display names of users who liked the post, newest
// first. It is authorized for the post author only: any other caller (connected
// or not) gets ErrNotAuthor, and a missing post gets ErrNotFound — both of which
// handlers render as 404 so the list never leaks (Technical Plan §4.4.5).
func (s *Service) ListLikers(ctx context.Context, postID, requesterID int64) ([]Liker, error) {
	authorID, err := s.postAuthor(ctx, postID)
	if err != nil {
		return nil, err
	}
	if authorID != requesterID {
		return nil, ErrNotAuthor
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT u.username, u.first_name, u.last_name
		  FROM likes l
		  JOIN users u ON u.id = l.user_id AND u.deleted_at IS NULL
		 WHERE l.post_id = ?
		 ORDER BY l.created_at DESC, l.id DESC`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var likers []Liker
	for rows.Next() {
		var (
			l           Liker
			first, last string
		)
		if err := rows.Scan(&l.Username, &first, &last); err != nil {
			return nil, err
		}
		l.Name = fullName(first, last)
		likers = append(likers, l)
	}
	return likers, rows.Err()
}

// MarkViewerLikes sets LikedByViewer on each post the viewer has liked, with a
// single query for the whole slice. It tells the caller only about the viewer's
// own likes — nothing about anyone else's, and never a count. A zero viewer
// (anonymous) or empty slice is a no-op.
func (s *Service) MarkViewerLikes(ctx context.Context, viewerID int64, ps []posts.Post) error {
	if viewerID == 0 || len(ps) == 0 {
		return nil
	}
	placeholders := make([]string, len(ps))
	args := make([]any, 0, len(ps)+1)
	args = append(args, viewerID)
	for i := range ps {
		placeholders[i] = "?"
		args = append(args, ps[i].ID)
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT post_id FROM likes WHERE user_id = ? AND post_id IN (`+strings.Join(placeholders, ",")+`)`,
		args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	liked := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		liked[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range ps {
		if liked[ps[i].ID] {
			ps[i].LikedByViewer = true
		}
	}
	return nil
}

// postAuthor returns the author of an active post, or ErrNotFound if the post
// doesn't exist or isn't active (deleted/archived posts can't be liked).
func (s *Service) postAuthor(ctx context.Context, postID int64) (int64, error) {
	var authorID int64
	err := s.DB.QueryRowContext(ctx,
		`SELECT author_id FROM posts WHERE id = ? AND status = 'active'`, postID,
	).Scan(&authorID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return authorID, nil
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
