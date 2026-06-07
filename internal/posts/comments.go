package posts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/dblanc/hearth/internal/notifications"
	"github.com/dblanc/hearth/internal/shared/db"
)

// MaxCommentLen caps a comment's length in runes after trimming, matching the
// post content cap.
const MaxCommentLen = 1000

// DeletedCommentPlaceholder is shown in place of a soft-deleted comment's
// content, so a deleted parent still anchors its surviving replies (Build Plan
// §2.2).
const DeletedCommentPlaceholder = "[deleted]"

var (
	ErrCommentEmpty     = errors.New("comments: content is empty")
	ErrCommentTooLong   = errors.New("comments: content exceeds 1000 characters")
	ErrCommentNotFound  = errors.New("comments: not found")
	ErrCommentForbidden = errors.New("comments: not permitted")
)

// Connections is the slice of the connection graph comments need: the single
// privacy chokepoint (CLAUDE.md §1). *connections.Service satisfies it. Declared
// locally (rather than importing the connections package) to keep the import
// graph acyclic, mirroring the likes package.
type Connections interface {
	IsConnected(ctx context.Context, viewerID, authorID int64) (bool, error)
}

// CommentService owns threaded comments: creating top-level comments and
// replies, deleting them under the author/post-owner rules, and loading threads
// for rendering. It is separate from the post Service because it needs the
// connection check and the notification fan-out.
type CommentService struct {
	DB     *sql.DB
	Conns  Connections
	Notifs *notifications.Service
	Now    func() time.Time
}

func NewCommentService(d *sql.DB, conns Connections, notifs *notifications.Service) *CommentService {
	return &CommentService{DB: d, Conns: conns, Notifs: notifs, Now: time.Now}
}

// Comment is one comment prepared for rendering, with the author's display
// fields joined in and its replies nested under Children.
type Comment struct {
	ID             int64
	PostID         int64
	ParentID       int64 // 0 for a top-level comment
	AuthorID       int64
	AuthorUsername string
	AuthorName     string
	Content        string
	Deleted        bool
	CreatedAt      time.Time
	Children       []*Comment
	// CanDelete is true when the current viewer may delete this comment (its
	// author, or the post's author). Set per-request when a thread is built.
	CanDelete bool
}

// validateComment trims and length-checks comment content.
func validateComment(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", ErrCommentEmpty
	}
	if len([]rune(trimmed)) > MaxCommentLen {
		return "", ErrCommentTooLong
	}
	return trimmed, nil
}

// requirePostAccess loads an active post's author and confirms the viewer may
// see it: they are the author, or they are connected to the author (CLAUDE.md
// §1). A missing/non-active post and a disallowed viewer both surface as
// ErrCommentNotFound / ErrCommentForbidden respectively.
func (s *CommentService) requirePostAccess(ctx context.Context, postID, viewerID int64) (authorID int64, err error) {
	err = s.DB.QueryRowContext(ctx,
		`SELECT author_id FROM posts WHERE id = ? AND status = 'active'`, postID,
	).Scan(&authorID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrCommentNotFound
	}
	if err != nil {
		return 0, err
	}
	if viewerID == authorID {
		return authorID, nil
	}
	connected, err := s.Conns.IsConnected(ctx, viewerID, authorID)
	if err != nil {
		return 0, err
	}
	if !connected {
		return 0, ErrCommentForbidden
	}
	return authorID, nil
}

// Create adds a top-level comment to a post and notifies the post author
// (unless they are commenting on their own post). The commenter must be allowed
// to see the post.
func (s *CommentService) Create(ctx context.Context, postID, authorID int64, content string) (*Comment, error) {
	trimmed, err := validateComment(content)
	if err != nil {
		return nil, err
	}
	postAuthorID, err := s.requirePostAccess(ctx, postID, authorID)
	if err != nil {
		return nil, err
	}

	var commentID int64
	now := s.Now()
	err = db.WithImmediate(ctx, s.DB, func(conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx,
			`INSERT INTO comments (post_id, parent_comment_id, author_id, content, status, created_at)
			 VALUES (?, NULL, ?, ?, 'active', ?)`,
			postID, authorID, trimmed, now)
		if err != nil {
			return err
		}
		commentID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if postAuthorID == authorID {
			return nil // no self-notification (Build Plan §2.2)
		}
		return s.Notifs.Create(ctx, conn, notifications.Params{
			UserID:    postAuthorID,
			Type:      notifications.TypeCommentOnPost,
			ActorID:   authorID,
			PostID:    postID,
			CommentID: commentID,
		})
	})
	if err != nil {
		return nil, err
	}
	return s.loadOne(ctx, commentID)
}

// Reply adds a reply to an existing comment and notifies that comment's author
// (unless replying to oneself). The replier must be allowed to see the post the
// parent belongs to, and the parent must be an active comment.
func (s *CommentService) Reply(ctx context.Context, parentID, authorID int64, content string) (*Comment, error) {
	trimmed, err := validateComment(content)
	if err != nil {
		return nil, err
	}

	var (
		postID         int64
		parentAuthorID int64
		parentStatus   string
	)
	err = s.DB.QueryRowContext(ctx,
		`SELECT post_id, author_id, status FROM comments WHERE id = ?`, parentID,
	).Scan(&postID, &parentAuthorID, &parentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCommentNotFound
	}
	if err != nil {
		return nil, err
	}
	if parentStatus != "active" {
		// A deleted comment is a tombstone; you can't reply to it.
		return nil, ErrCommentNotFound
	}
	if _, err := s.requirePostAccess(ctx, postID, authorID); err != nil {
		return nil, err
	}

	var commentID int64
	now := s.Now()
	err = db.WithImmediate(ctx, s.DB, func(conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx,
			`INSERT INTO comments (post_id, parent_comment_id, author_id, content, status, created_at)
			 VALUES (?, ?, ?, ?, 'active', ?)`,
			postID, parentID, authorID, trimmed, now)
		if err != nil {
			return err
		}
		commentID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if parentAuthorID == authorID {
			return nil // no self-notification
		}
		return s.Notifs.Create(ctx, conn, notifications.Params{
			UserID:    parentAuthorID,
			Type:      notifications.TypeReplyToComment,
			ActorID:   authorID,
			PostID:    postID,
			CommentID: commentID,
		})
	})
	if err != nil {
		return nil, err
	}
	return s.loadOne(ctx, commentID)
}

// Delete removes a comment. It is permitted for the comment's author or the
// post's author (Build Plan §2.2). A comment that has replies is soft-deleted —
// status='deleted', content cleared — so its thread survives as a "[deleted]"
// placeholder; a leaf comment is hard-deleted. Returns the post id so callers
// can re-render the affected thread.
func (s *CommentService) Delete(ctx context.Context, commentID, userID int64) (postID int64, err error) {
	var commentAuthorID int64
	err = s.DB.QueryRowContext(ctx,
		`SELECT post_id, author_id FROM comments WHERE id = ?`, commentID,
	).Scan(&postID, &commentAuthorID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrCommentNotFound
	}
	if err != nil {
		return 0, err
	}

	var postAuthorID int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT author_id FROM posts WHERE id = ?`, postID,
	).Scan(&postAuthorID); err != nil {
		return 0, err
	}
	if userID != commentAuthorID && userID != postAuthorID {
		return 0, ErrCommentForbidden
	}

	var hasChild int
	err = s.DB.QueryRowContext(ctx,
		`SELECT 1 FROM comments WHERE parent_comment_id = ? LIMIT 1`, commentID,
	).Scan(&hasChild)
	hasReplies := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	if hasReplies {
		_, err = s.DB.ExecContext(ctx,
			`UPDATE comments SET status = 'deleted', content = '' WHERE id = ?`, commentID)
	} else {
		_, err = s.DB.ExecContext(ctx, `DELETE FROM comments WHERE id = ?`, commentID)
	}
	if err != nil {
		return 0, err
	}
	return postID, nil
}

// ListThread returns a post's comments as a tree of top-level roots with nested
// replies, oldest-first, after confirming the viewer may see the post. Used to
// render a single post's thread (e.g. the htmx fragment after posting).
func (s *CommentService) ListThread(ctx context.Context, postID, viewerID int64) ([]*Comment, error) {
	postAuthorID, err := s.requirePostAccess(ctx, postID, viewerID)
	if err != nil {
		return nil, err
	}
	flat, err := s.queryForPosts(ctx, []int64{postID})
	if err != nil {
		return nil, err
	}
	roots := buildThreads(flat[postID], viewerID, postAuthorID)
	return roots, nil
}

// AttachToPosts batch-loads every comment for the given posts and assigns each
// post's top-level comments (with nested replies) to its Comments field. Like
// SignMediaURLs, it assumes the caller has already confirmed the viewer may see
// these posts (the feed and profile both do), so it performs no per-post
// connection check. One query covers the whole slice.
func (s *CommentService) AttachToPosts(ctx context.Context, viewerID int64, ps []Post) error {
	if len(ps) == 0 {
		return nil
	}
	ids := make([]int64, len(ps))
	for i := range ps {
		ids[i] = ps[i].ID
	}
	flat, err := s.queryForPosts(ctx, ids)
	if err != nil {
		return err
	}
	for i := range ps {
		ps[i].Comments = buildThreads(flat[ps[i].ID], viewerID, ps[i].AuthorID)
	}
	return nil
}

// loadOne reads a single comment with its author display fields. Newly created
// comments are always active and viewer-deletable by their author, so CanDelete
// is set true here for the immediate re-render.
func (s *CommentService) loadOne(ctx context.Context, commentID int64) (*Comment, error) {
	var (
		c           Comment
		parent      sql.NullInt64
		status      string
		first, last string
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT c.id, c.post_id, c.parent_comment_id, c.author_id,
		       u.username, u.first_name, u.last_name, c.content, c.status, c.created_at
		  FROM comments c
		  JOIN users u ON u.id = c.author_id
		 WHERE c.id = ?`, commentID,
	).Scan(&c.ID, &c.PostID, &parent, &c.AuthorID, &c.AuthorUsername,
		&first, &last, &c.Content, &status, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	if parent.Valid {
		c.ParentID = parent.Int64
	}
	c.AuthorName = fullName(first, last)
	c.Deleted = status == "deleted"
	c.CanDelete = true
	return &c, nil
}

// queryForPosts loads all comments for the given post ids in one query, grouped
// by post id and ordered oldest-first so buildThreads can assemble each tree in
// a single pass.
func (s *CommentService) queryForPosts(ctx context.Context, postIDs []int64) (map[int64][]*Comment, error) {
	placeholders := make([]string, len(postIDs))
	args := make([]any, len(postIDs))
	for i, id := range postIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT c.id, c.post_id, c.parent_comment_id, c.author_id,
		       u.username, u.first_name, u.last_name, c.content, c.status, c.created_at
		  FROM comments c
		  JOIN users u ON u.id = c.author_id
		 WHERE c.post_id IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY c.created_at ASC, c.id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byPost := make(map[int64][]*Comment)
	for rows.Next() {
		var (
			c           Comment
			parent      sql.NullInt64
			status      string
			first, last string
		)
		if err := rows.Scan(&c.ID, &c.PostID, &parent, &c.AuthorID, &c.AuthorUsername,
			&first, &last, &c.Content, &status, &c.CreatedAt); err != nil {
			return nil, err
		}
		if parent.Valid {
			c.ParentID = parent.Int64
		}
		c.AuthorName = fullName(first, last)
		c.Deleted = status == "deleted"
		if c.Deleted {
			c.Content = DeletedCommentPlaceholder
		}
		byPost[c.PostID] = append(byPost[c.PostID], &c)
	}
	return byPost, rows.Err()
}

// buildThreads assembles a flat, time-ordered comment slice into a tree of
// top-level roots with replies nested under each parent, preserving order.
// Replies whose parent is missing are skipped defensively. viewerID/postAuthorID
// drive the per-comment CanDelete flag (the comment's author or the post author).
func buildThreads(flat []*Comment, viewerID, postAuthorID int64) []*Comment {
	byID := make(map[int64]*Comment, len(flat))
	for _, c := range flat {
		c.Children = nil
		byID[c.ID] = c
	}
	var roots []*Comment
	for _, c := range flat {
		// A deleted comment can't be acted on; otherwise the author or the post
		// owner may delete it.
		c.CanDelete = !c.Deleted && (viewerID == c.AuthorID || viewerID == postAuthorID)
		if c.ParentID == 0 {
			roots = append(roots, c)
			continue
		}
		if parent, ok := byID[c.ParentID]; ok {
			parent.Children = append(parent.Children, c)
		}
	}
	return roots
}
