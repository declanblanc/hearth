package likes

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/dblanc/hearth/internal/connections"
	"github.com/dblanc/hearth/internal/notifications"
	"github.com/dblanc/hearth/internal/posts"
	"github.com/dblanc/hearth/internal/shared/db"
)

// newTestService wires a likes.Service against an in-memory DB, using the real
// connections and notifications services so the privacy check and notification
// fan-out are exercised end to end.
func newTestService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(d); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	notifSvc := notifications.New(d)
	connSvc := connections.New(d, notifSvc, "https://hearth.test")
	return New(d, connSvc, notifSvc), d
}

func seedUser(t *testing.T, d *sql.DB, username string) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO users (username, email, password_hash, first_name, last_name)
		 VALUES (?, ?, 'x', ?, 'User')`,
		username, username+"@example.com", username)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

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

// connect inserts the canonical (lower, higher) connection row directly.
func connect(t *testing.T, d *sql.DB, x, y int64) {
	t.Helper()
	a, b := x, y
	if a > b {
		a, b = b, a
	}
	if _, err := d.Exec(
		`INSERT INTO connections (user_a_id, user_b_id) VALUES (?, ?)`, a, b); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

func countLikes(t *testing.T, d *sql.DB, postID int64) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM likes WHERE post_id = ?`, postID).Scan(&n); err != nil {
		t.Fatalf("count likes: %v", err)
	}
	return n
}

func countLikeNotifs(t *testing.T, d *sql.DB, recipientID int64) int {
	t.Helper()
	var n int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND type = 'like_on_post'`,
		recipientID).Scan(&n); err != nil {
		t.Fatalf("count notifs: %v", err)
	}
	return n
}

func TestLike_NotifiesAuthorOnce(t *testing.T) {
	svc, d := newTestService(t)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	liker := seedUser(t, d, "liker")
	connect(t, d, author, liker)
	post := seedPost(t, d, author)

	if err := svc.Like(ctx, post, liker); err != nil {
		t.Fatalf("Like: %v", err)
	}
	if got := countLikes(t, d, post); got != 1 {
		t.Errorf("likes = %d, want 1", got)
	}
	if got := countLikeNotifs(t, d, author); got != 1 {
		t.Errorf("like notifications = %d, want 1", got)
	}
}

func TestLike_Idempotent(t *testing.T) {
	svc, d := newTestService(t)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	liker := seedUser(t, d, "liker")
	connect(t, d, author, liker)
	post := seedPost(t, d, author)

	for i := 0; i < 3; i++ {
		if err := svc.Like(ctx, post, liker); err != nil {
			t.Fatalf("Like #%d: %v", i, err)
		}
	}
	if got := countLikes(t, d, post); got != 1 {
		t.Errorf("likes = %d, want 1 (idempotent)", got)
	}
	if got := countLikeNotifs(t, d, author); got != 1 {
		t.Errorf("like notifications = %d, want 1 (no duplicate notify)", got)
	}
}

func TestUnlike_RemovesLikeAndNotification(t *testing.T) {
	svc, d := newTestService(t)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	liker := seedUser(t, d, "liker")
	connect(t, d, author, liker)
	post := seedPost(t, d, author)

	if err := svc.Like(ctx, post, liker); err != nil {
		t.Fatalf("Like: %v", err)
	}
	if err := svc.Unlike(ctx, post, liker); err != nil {
		t.Fatalf("Unlike: %v", err)
	}
	if got := countLikes(t, d, post); got != 0 {
		t.Errorf("likes = %d, want 0 after unlike", got)
	}
	if got := countLikeNotifs(t, d, author); got != 0 {
		t.Errorf("like notifications = %d, want 0 after unlike", got)
	}
	// Unliking again is a harmless no-op.
	if err := svc.Unlike(ctx, post, liker); err != nil {
		t.Errorf("second Unlike: %v", err)
	}
}

func TestLike_SelfDoesNotNotify(t *testing.T) {
	svc, d := newTestService(t)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	post := seedPost(t, d, author)

	if err := svc.Like(ctx, post, author); err != nil {
		t.Fatalf("self Like: %v", err)
	}
	if got := countLikes(t, d, post); got != 1 {
		t.Errorf("likes = %d, want 1", got)
	}
	if got := countLikeNotifs(t, d, author); got != 0 {
		t.Errorf("self-like notifications = %d, want 0", got)
	}
}

func TestLike_NotConnectedIsNotFound(t *testing.T) {
	svc, d := newTestService(t)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	stranger := seedUser(t, d, "stranger") // never connected
	post := seedPost(t, d, author)

	if err := svc.Like(ctx, post, stranger); !errors.Is(err, ErrNotFound) {
		t.Errorf("Like by stranger = %v, want ErrNotFound", err)
	}
	if got := countLikes(t, d, post); got != 0 {
		t.Errorf("likes = %d, want 0", got)
	}
}

func TestLike_MissingPostIsNotFound(t *testing.T) {
	svc, d := newTestService(t)
	liker := seedUser(t, d, "liker")
	if err := svc.Like(context.Background(), 9999, liker); !errors.Is(err, ErrNotFound) {
		t.Errorf("Like missing post = %v, want ErrNotFound", err)
	}
}

func TestListLikers_AuthorOnly(t *testing.T) {
	svc, d := newTestService(t)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	liker := seedUser(t, d, "liker")
	stranger := seedUser(t, d, "stranger")
	connect(t, d, author, liker)
	connect(t, d, author, stranger)
	post := seedPost(t, d, author)

	if err := svc.Like(ctx, post, liker); err != nil {
		t.Fatalf("Like: %v", err)
	}

	// The author sees the liker by name.
	likers, err := svc.ListLikers(ctx, post, author)
	if err != nil {
		t.Fatalf("ListLikers(author): %v", err)
	}
	if len(likers) != 1 || likers[0].Username != "liker" {
		t.Errorf("likers = %+v, want one entry for 'liker'", likers)
	}

	// A connected non-author may not see the list (ErrNotAuthor → 404).
	if _, err := svc.ListLikers(ctx, post, stranger); !errors.Is(err, ErrNotAuthor) {
		t.Errorf("ListLikers(stranger) = %v, want ErrNotAuthor", err)
	}
}

func TestListLikers_MissingPostIsNotFound(t *testing.T) {
	svc, d := newTestService(t)
	author := seedUser(t, d, "author")
	if _, err := svc.ListLikers(context.Background(), 9999, author); !errors.Is(err, ErrNotFound) {
		t.Errorf("ListLikers missing post = %v, want ErrNotFound", err)
	}
}

func TestMarkViewerLikes(t *testing.T) {
	svc, d := newTestService(t)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	viewer := seedUser(t, d, "viewer")
	connect(t, d, author, viewer)
	liked := seedPost(t, d, author)
	unliked := seedPost(t, d, author)

	if err := svc.Like(ctx, liked, viewer); err != nil {
		t.Fatalf("Like: %v", err)
	}

	ps := []posts.Post{{ID: liked}, {ID: unliked}}
	if err := svc.MarkViewerLikes(ctx, viewer, ps); err != nil {
		t.Fatalf("MarkViewerLikes: %v", err)
	}
	if !ps[0].LikedByViewer {
		t.Error("liked post should have LikedByViewer = true")
	}
	if ps[1].LikedByViewer {
		t.Error("unliked post should have LikedByViewer = false")
	}
}
