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
// graph acyclic.
type Connections interface {
	IsConnected(ctx context.Context, viewerID, authorID int64) (bool, error)
	// ConnectionIDs returns the user IDs the given user is connected to, used to
	// decide which other people's comments a viewer may see (issue #23).
	ConnectionIDs(ctx context.Context, userID int64) ([]int64, error)
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
	// AuthorSharesExternally mirrors the author's
	// share_comments_with_non_connections flag. It lets visibleToViewer decide
	// whether this comment may cross to a viewer who isn't connected to the author
	// (issue: cross-network comment visibility). Joined in from users.
	AuthorSharesExternally bool
	// ReplyToName is the display name of the comment this one replies to; empty
	// for top-level or when the parent is hidden/deleted. Drives the "@name"
	// prefix once all descendants are flattened into one tier under their root.
	ReplyToName string
	// ReplyToSelf is true when the parent comment is the viewer's own, so the
	// mention reads "replying to you" instead of the viewer's own name.
	ReplyToSelf bool
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
// parent belongs to, and the parent must be an active comment. Replies may sit
// at any depth in the data tree; rendering flattens them into a single tier (see
// buildThreads).
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
	connected, err := s.connectedSet(ctx, viewerID)
	if err != nil {
		return nil, err
	}
	showNonConnections, err := s.viewerShowsNonConnections(ctx, viewerID)
	if err != nil {
		return nil, err
	}
	flat, err := s.queryForPosts(ctx, []int64{postID})
	if err != nil {
		return nil, err
	}
	roots := buildThreads(flat[postID], viewerID, postAuthorID, connected, showNonConnections)
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
	connected, err := s.connectedSet(ctx, viewerID)
	if err != nil {
		return err
	}
	showNonConnections, err := s.viewerShowsNonConnections(ctx, viewerID)
	if err != nil {
		return err
	}
	flat, err := s.queryForPosts(ctx, ids)
	if err != nil {
		return err
	}
	for i := range ps {
		ps[i].Comments = buildThreads(flat[ps[i].ID], viewerID, ps[i].AuthorID, connected, showNonConnections)
	}
	return nil
}

// connectedSet loads the viewer's connections as a lookup set, so buildThreads
// can decide in O(1) whether each comment's author is visible to them (issue
// #23). Fetched once per request rather than per comment.
func (s *CommentService) connectedSet(ctx context.Context, viewerID int64) (map[int64]bool, error) {
	ids, err := s.Conns.ConnectionIDs(ctx, viewerID)
	if err != nil {
		return nil, err
	}
	set := make(map[int64]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// viewerShowsNonConnections reports whether the viewer has opted in to seeing
// comments from people they aren't connected with (the show_non_connection_comments
// flag). Loaded once per request; combined per-comment with the author's own
// share flag in visibleToViewer. A zero viewerID (anonymous) never opts in.
func (s *CommentService) viewerShowsNonConnections(ctx context.Context, viewerID int64) (bool, error) {
	if viewerID == 0 {
		return false, nil
	}
	var show bool
	err := s.DB.QueryRowContext(ctx,
		`SELECT show_non_connection_comments FROM users WHERE id = ?`, viewerID,
	).Scan(&show)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return show, nil
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
		       u.username, u.first_name, u.last_name, c.content, c.status, c.created_at,
		       u.share_comments_with_non_connections
		  FROM comments c
		  JOIN users u ON u.id = c.author_id
		 WHERE c.id = ?`, commentID,
	).Scan(&c.ID, &c.PostID, &parent, &c.AuthorID, &c.AuthorUsername,
		&first, &last, &c.Content, &status, &c.CreatedAt, &c.AuthorSharesExternally)
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
		       u.username, u.first_name, u.last_name, c.content, c.status, c.created_at,
		       u.share_comments_with_non_connections
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
			&first, &last, &c.Content, &status, &c.CreatedAt, &c.AuthorSharesExternally); err != nil {
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

// buildThreads assembles a flat, time-ordered comment slice into top-level roots
// with all of their descendants FLATTENED into one time-ordered tier beneath
// each root — the data is a true tree (each reply's ParentID is the comment it
// answers), but rendering collapses arbitrary depth into two visual tiers so
// indentation never compounds. Each flattened reply carries ReplyToName, the
// display name of its immediate parent, to show who it answers ("@name").
//
// Comments authored by someone the viewer may not see are filtered out first
// (issue #23, CLAUDE.md §1); a reply whose parent chain can't be fully resolved
// through visible comments is skipped, so a hidden comment takes its sub-thread
// with it. viewerID/postAuthorID drive the per-comment CanDelete flag (the
// comment's author or the post author).
func buildThreads(flat []*Comment, viewerID, postAuthorID int64, connected map[int64]bool, viewerShowsNonConnections bool) []*Comment {
	byID := make(map[int64]*Comment, len(flat))
	visible := make([]*Comment, 0, len(flat))
	for _, c := range flat {
		if !visibleToViewer(c, viewerID, postAuthorID, connected, viewerShowsNonConnections) {
			continue
		}
		c.Children = nil
		byID[c.ID] = c
		visible = append(visible, c)
	}

	// rootCache memoizes each comment's top-level ancestor so resolving roots
	// stays O(n) amortized across the slice.
	rootCache := make(map[int64]int64, len(visible))
	var roots []*Comment
	for _, c := range visible {
		// A deleted comment can't be acted on; otherwise the author or the post
		// owner may delete it.
		c.CanDelete = !c.Deleted && (viewerID == c.AuthorID || viewerID == postAuthorID)
		if c.ParentID == 0 {
			roots = append(roots, c)
			continue
		}
		rootID, ok := resolveRoot(c.ID, byID, rootCache)
		if !ok {
			// A hidden ancestor broke the chain; drop the reply with its lost
			// sub-thread (matches issue #23's hide-the-subtree behavior).
			continue
		}
		// "@name" points at the immediate parent — but a deleted parent is a
		// nameless tombstone, so leave the mention empty there.
		if parent, ok := byID[c.ParentID]; ok && !parent.Deleted {
			c.ReplyToName = parent.AuthorName
			c.ReplyToSelf = parent.AuthorID == viewerID
		}
		// Input is already time-ordered and we append in iteration order, so each
		// root's flat Children come out oldest-first — no re-sort needed.
		root := byID[rootID]
		root.Children = append(root.Children, c)
	}
	return pruneEmptyTombstones(roots)
}

// resolveRoot walks up the parent chain via byID until it reaches a top-level
// comment (ParentID == 0), returning that comment's id. ok is false if any link
// in the chain is missing from byID (a hidden ancestor). Results are memoized in
// cache so repeated walks over a shared chain stay cheap.
func resolveRoot(id int64, byID map[int64]*Comment, cache map[int64]int64) (int64, bool) {
	if rootID, found := cache[id]; found {
		return rootID, true
	}
	c, ok := byID[id]
	if !ok {
		return 0, false
	}
	if c.ParentID == 0 {
		cache[id] = id
		return id, true
	}
	rootID, ok := resolveRoot(c.ParentID, byID, cache)
	if !ok {
		return 0, false
	}
	cache[id] = rootID
	return rootID, true
}

// visibleToViewer reports whether a comment may be shown to the viewer. A viewer
// always sees their own comments and the post author's; anyone else's are visible
// if the viewer is connected to that author (issue #23). A deleted comment is a
// tombstone carrying no author or content, so it stays visible to anchor any
// replies the viewer *can* see — pruneEmptyTombstones drops it later if none do.
//
// Beyond direct connections, a non-connection's comment crosses to the viewer only
// when both sides have opted in: the viewer enabled "display comments from
// non-connections" (viewerShowsNonConnections) AND the author enabled "share my
// comments with non-connections" (AuthorSharesExternally). Symmetric consent.
func visibleToViewer(c *Comment, viewerID, postAuthorID int64, connected map[int64]bool, viewerShowsNonConnections bool) bool {
	if c.Deleted {
		return true
	}
	if c.AuthorID == viewerID || c.AuthorID == postAuthorID || connected[c.AuthorID] {
		return true
	}
	return viewerShowsNonConnections && c.AuthorSharesExternally
}

// pruneEmptyTombstones removes deleted placeholder comments that have no visible
// replies left beneath them, so hiding a non-connected author's comment doesn't
// leave a bare "[deleted]" node behind (issue #23). It works bottom-up so a
// tombstone whose only children were themselves pruned is removed too.
func pruneEmptyTombstones(nodes []*Comment) []*Comment {
	kept := nodes[:0]
	for _, n := range nodes {
		n.Children = pruneEmptyTombstones(n.Children)
		if n.Deleted && len(n.Children) == 0 {
			continue
		}
		kept = append(kept, n)
	}
	return kept
}
