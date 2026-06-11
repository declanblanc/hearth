// Package notifications owns the in-app notification feed: creating
// notifications, listing them, marking them read, and the header unread
// indicator. Email dispatch is deferred to Phase 3 — in Phase 1 every
// notifiable event always inserts a row here, unconditionally
// (Technical Plan §4.7).
package notifications

import (
	"context"
	"database/sql"
	"time"
)

// Notification types. Phase 1 uses the first two; the comment types arrive in
// Phase 2 but are listed here so callers share one vocabulary.
const (
	TypeConnectionRequest  = "connection_request"
	TypeConnectionAccepted = "connection_accepted"
	TypeCommentOnPost      = "comment_on_post"
	TypeReplyToComment     = "reply_to_comment"
)

type Service struct {
	DB  *sql.DB
	Now func() time.Time
}

func New(db *sql.DB) *Service { return &Service{DB: db, Now: time.Now} }

// Params describes a notification to create. actor_id/post_id/comment_id are
// optional depending on the type.
type Params struct {
	UserID    int64 // recipient
	Type      string
	ActorID   int64 // 0 = none
	PostID    int64 // 0 = none
	CommentID int64 // 0 = none
}

// Create inserts a notification row. It is the single entry point the rest of
// the codebase uses (CLAUDE.md §8: in-app is universal). It accepts an
// optional execer so callers can include the insert in an existing
// transaction; pass nil to use the service's own DB handle.
func (s *Service) Create(ctx context.Context, e Execer, p Params) error {
	if e == nil {
		e = s.DB
	}
	_, err := e.ExecContext(ctx,
		`INSERT INTO notifications (user_id, type, actor_id, post_id, comment_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		p.UserID, p.Type, nullID(p.ActorID), nullID(p.PostID), nullID(p.CommentID), s.Now(),
	)
	return err
}

// Execer is satisfied by *sql.DB, *sql.Tx, and *sql.Conn, letting Create
// participate in a caller's transaction.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Item is a notification prepared for rendering, with the actor's display
// fields joined in. PostID and CommentID are zero when the notification type
// has no associated target (e.g. connection events); the template uses them to
// build a deep link to the post that was liked or commented on.
//
// PostAuthorUsername is the username of the post's author, joined in so the
// template can deep-link to where the post actually lives (/u/{author}#post-N).
// This must come from the post — not the notification recipient — because for
// reply_to_comment the recipient is the parent comment's author, who is not
// necessarily the post author.
type Item struct {
	ID                 int64
	Type               string
	ActorUsername      string
	ActorName          string
	PostID             int64
	PostAuthorUsername string
	CommentID          int64
	CreatedAt          time.Time
	Read               bool
}

// List returns the user's notifications, newest first.
func (s *Service) List(ctx context.Context, userID int64) ([]Item, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT n.id, n.type, n.created_at, n.read_at, n.post_id, n.comment_id,
		       COALESCE(a.username, ''), COALESCE(a.first_name, ''), COALESCE(a.last_name, ''),
		       COALESCE(pa.username, '')
		  FROM notifications n
		  LEFT JOIN users a ON a.id = n.actor_id AND a.deleted_at IS NULL
		  LEFT JOIN posts p ON p.id = n.post_id
		  LEFT JOIN users pa ON pa.id = p.author_id AND pa.deleted_at IS NULL
		 WHERE n.user_id = ?
		 ORDER BY n.created_at DESC, n.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var (
			it                    Item
			readAt                sql.NullTime
			postID, commentID     sql.NullInt64
			username, first, last string
			postAuthorUsername    string
		)
		if err := rows.Scan(&it.ID, &it.Type, &it.CreatedAt, &readAt, &postID, &commentID, &username, &first, &last, &postAuthorUsername); err != nil {
			return nil, err
		}
		it.ActorUsername = username
		it.ActorName = fullName(first, last)
		it.PostID = postID.Int64
		it.PostAuthorUsername = postAuthorUsername
		it.CommentID = commentID.Int64
		it.Read = readAt.Valid
		items = append(items, it)
	}
	return items, rows.Err()
}

// MarkAllRead clears the unread state for every one of the user's
// notifications. Called when they visit /notifications.
func (s *Service) MarkAllRead(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`,
		s.Now(), userID)
	return err
}

// HasUnread reports whether the user has any unread notifications. This drives
// the header dot — deliberately a boolean, never a count (CLAUDE.md §2).
func (s *Service) HasUnread(ctx context.Context, userID int64) (bool, error) {
	var one int
	err := s.DB.QueryRowContext(ctx,
		`SELECT 1 FROM notifications WHERE user_id = ? AND read_at IS NULL LIMIT 1`, userID,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
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
