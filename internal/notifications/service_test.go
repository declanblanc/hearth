package notifications

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/dblanc/hearth/internal/shared/db"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(d); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return d
}

// seedUser inserts a bare user row and returns its ID.
func seedUser(t *testing.T, d *sql.DB, username string) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO users (username, email, password_hash, first_name, last_name)
		 VALUES (?, ?, 'x', ?, 'User')`,
		username, username+"@example.com", username)
	if err != nil {
		t.Fatalf("seed user %s: %v", username, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestCreateAndList(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()

	recipient := seedUser(t, d, "recipient")
	actor := seedUser(t, d, "actor")

	if err := svc.Create(ctx, nil, Params{UserID: recipient, Type: TypeConnectionRequest, ActorID: actor}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	items, err := svc.List(ctx, recipient)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 notification, got %d", len(items))
	}
	if items[0].Type != TypeConnectionRequest {
		t.Errorf("type: got %q", items[0].Type)
	}
	if items[0].ActorUsername != "actor" {
		t.Errorf("actor username: got %q", items[0].ActorUsername)
	}
	if items[0].Read {
		t.Error("new notification should be unread")
	}
}

// seedPost inserts a bare post row authored by the given user and returns its
// ID. Post-targeted notifications reference a real post so the FK and the read
// path both have something to resolve.
func seedPost(t *testing.T, d *sql.DB, authorID int64) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO posts (author_id, content, status) VALUES (?, 'hello', 'active')`,
		authorID)
	if err != nil {
		t.Fatalf("seed post: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestList_CarriesPostAndCommentIDs guards the issue #22 regression: List must
// surface post_id/comment_id so the template can deep-link to the liked or
// commented-on post. Before the fix these columns were never selected, leaving
// every post-targeted notification without a link.
func TestList_CarriesPostAndCommentIDs(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()

	author := seedUser(t, d, "author")
	actor := seedUser(t, d, "commenter")
	postID := seedPost(t, d, author)

	if err := svc.Create(ctx, nil, Params{
		UserID:    author,
		Type:      TypeCommentOnPost,
		ActorID:   actor,
		PostID:    postID,
		CommentID: 99,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	items, err := svc.List(ctx, author)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 notification, got %d", len(items))
	}
	got := items[0]
	if got.Type != TypeCommentOnPost {
		t.Errorf("type: got %q, want %q", got.Type, TypeCommentOnPost)
	}
	if got.PostID != postID {
		t.Errorf("PostID: got %d, want %d", got.PostID, postID)
	}
	if got.CommentID != 99 {
		t.Errorf("CommentID: got %d, want 99", got.CommentID)
	}
	if got.ActorUsername != "commenter" {
		t.Errorf("actor username: got %q", got.ActorUsername)
	}
	// The deep link is built from the post author's username, not the
	// recipient's — they coincide here (comment notifications go to the
	// author) but the read path must surface it regardless.
	if got.PostAuthorUsername != "author" {
		t.Errorf("post author username: got %q, want %q", got.PostAuthorUsername, "author")
	}
}

// TestList_ReplyLinksToPostAuthorNotRecipient guards the subtle case behind
// issue #22: a reply_to_comment notification goes to the parent comment's
// author, who is NOT the post author. The deep link must still resolve to the
// post on its real author's profile, so PostAuthorUsername must reflect the
// post's owner, not the notification recipient.
func TestList_ReplyLinksToPostAuthorNotRecipient(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()

	postAuthor := seedUser(t, d, "postauthor")
	commenter := seedUser(t, d, "commenter") // recipient of the reply notification
	replier := seedUser(t, d, "replier")     // actor
	postID := seedPost(t, d, postAuthor)

	// The reply notification's recipient is the commenter, not the post author.
	if err := svc.Create(ctx, nil, Params{
		UserID:    commenter,
		Type:      TypeReplyToComment,
		ActorID:   replier,
		PostID:    postID,
		CommentID: 5,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	items, err := svc.List(ctx, commenter)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1, got %d", len(items))
	}
	if items[0].PostAuthorUsername != "postauthor" {
		t.Errorf("link must target the post author's profile; got %q, want %q",
			items[0].PostAuthorUsername, "postauthor")
	}
}

// TestList_NoTargetLeavesIDsZero confirms connection notifications (which carry
// no post) report zero IDs, so the template renders no broken link.
func TestList_NoTargetLeavesIDsZero(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()
	recipient := seedUser(t, d, "r")

	if err := svc.Create(ctx, nil, Params{UserID: recipient, Type: TypeConnectionAccepted}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	items, _ := svc.List(ctx, recipient)
	if len(items) != 1 {
		t.Fatalf("want 1, got %d", len(items))
	}
	if items[0].PostID != 0 || items[0].CommentID != 0 {
		t.Errorf("connection notification should have zero IDs, got post=%d comment=%d",
			items[0].PostID, items[0].CommentID)
	}
}

func TestList_NewestFirst(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()
	recipient := seedUser(t, d, "r")

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return base }
	_ = svc.Create(ctx, nil, Params{UserID: recipient, Type: TypeConnectionRequest})
	svc.Now = func() time.Time { return base.Add(time.Hour) }
	_ = svc.Create(ctx, nil, Params{UserID: recipient, Type: TypeConnectionAccepted})

	items, _ := svc.List(ctx, recipient)
	if len(items) != 2 {
		t.Fatalf("want 2, got %d", len(items))
	}
	if items[0].Type != TypeConnectionAccepted {
		t.Errorf("newest should be first; got %q", items[0].Type)
	}
}

func TestHasUnreadAndMarkRead(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()
	recipient := seedUser(t, d, "r")

	if unread, _ := svc.HasUnread(ctx, recipient); unread {
		t.Error("no notifications yet — HasUnread should be false")
	}
	_ = svc.Create(ctx, nil, Params{UserID: recipient, Type: TypeConnectionRequest})
	if unread, _ := svc.HasUnread(ctx, recipient); !unread {
		t.Error("HasUnread should be true after create")
	}

	if err := svc.MarkAllRead(ctx, recipient); err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	if unread, _ := svc.HasUnread(ctx, recipient); unread {
		t.Error("HasUnread should be false after MarkAllRead")
	}
	items, _ := svc.List(ctx, recipient)
	if len(items) != 1 || !items[0].Read {
		t.Error("notification should still exist and be marked read")
	}
}
