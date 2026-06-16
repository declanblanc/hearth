package feed

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/dblanc/hearth/internal/connections"
	"github.com/dblanc/hearth/internal/notifications"
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

func connect(t *testing.T, d *sql.DB, x, y int64) {
	t.Helper()
	a, b := x, y
	if a > b {
		a, b = b, a
	}
	if _, err := d.Exec(`INSERT INTO connections (user_a_id, user_b_id) VALUES (?, ?)`, a, b); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

// insertPost writes a post directly with an explicit created_at and status.
func insertPost(t *testing.T, d *sql.DB, author int64, content string, at time.Time, status string) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO posts (author_id, content, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		author, content, status, at, at)
	if err != nil {
		t.Fatalf("insert post: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func newFeed(d *sql.DB) *Service {
	conns := connections.New(d, notifications.New(d), "https://hearth.test")
	return New(d, conns)
}

func setPhotoKey(t *testing.T, d *sql.DB, userID int64, key string) {
	t.Helper()
	if _, err := d.Exec(`UPDATE users SET photo_key = ? WHERE id = ?`, key, userID); err != nil {
		t.Fatalf("set photo_key: %v", err)
	}
}

// TestFeed_ExposesAuthorPhotoKey covers issue #40: the feed query joins in each
// author's profile-photo key so the template can render the avatar next to the
// author's name. Authors without a photo carry an empty key (not an error).
func TestFeed_ExposesAuthorPhotoKey(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	viewer := seedUser(t, d, "viewer")
	withPhoto := seedUser(t, d, "withphoto")
	noPhoto := seedUser(t, d, "nophoto")
	connect(t, d, viewer, withPhoto)
	connect(t, d, viewer, noPhoto)
	setPhotoKey(t, d, withPhoto, "profile/abc.jpg")

	now := time.Now()
	insertPost(t, d, withPhoto, "has avatar", now, "active")
	insertPost(t, d, noPhoto, "no avatar", now.Add(-time.Minute), "active")

	res, err := newFeed(d).FirstPage(ctx, viewer)
	if err != nil {
		t.Fatalf("FirstPage: %v", err)
	}
	byContent := map[string]string{}
	for _, p := range res.Posts {
		byContent[p.Content] = p.AuthorPhotoKey
	}
	if got := byContent["has avatar"]; got != "profile/abc.jpg" {
		t.Errorf("author with photo: want key %q, got %q", "profile/abc.jpg", got)
	}
	if got := byContent["no avatar"]; got != "" {
		t.Errorf("author without photo: want empty key, got %q", got)
	}
}

func TestFeed_OnlyConnectedActivePosts(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	viewer := seedUser(t, d, "viewer")
	friend := seedUser(t, d, "friend")
	stranger := seedUser(t, d, "stranger")
	connect(t, d, viewer, friend)

	now := time.Now()
	insertPost(t, d, friend, "from friend", now, "active")
	insertPost(t, d, friend, "archived", now, "archived")
	insertPost(t, d, friend, "deleted", now, "deleted")
	insertPost(t, d, stranger, "from stranger", now, "active")

	res, err := newFeed(d).FirstPage(ctx, viewer)
	if err != nil {
		t.Fatalf("FirstPage: %v", err)
	}
	all := res.Posts
	if len(all) != 1 {
		t.Fatalf("want exactly 1 visible post, got %d", len(all))
	}
	if all[0].Content != "from friend" {
		t.Errorf("got %q; archived/deleted/stranger posts must be excluded", all[0].Content)
	}
}

func TestFeed_ReverseChronological(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	viewer := seedUser(t, d, "viewer")
	friend := seedUser(t, d, "friend")
	connect(t, d, viewer, friend)

	mid := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	insertPost(t, d, friend, "older post", mid.Add(-time.Hour), "active")
	insertPost(t, d, friend, "newer post", mid.Add(time.Hour), "active")

	res, err := newFeed(d).FirstPage(ctx, viewer)
	if err != nil {
		t.Fatalf("FirstPage: %v", err)
	}
	if len(res.Posts) != 2 {
		t.Fatalf("want 2 posts, got %d", len(res.Posts))
	}
	if res.Posts[0].Content != "newer post" || res.Posts[1].Content != "older post" {
		t.Errorf("posts should be newest-first, got %+v", res.Posts)
	}
}

func TestFeed_Pagination(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	viewer := seedUser(t, d, "viewer")
	friend := seedUser(t, d, "friend")
	connect(t, d, viewer, friend)

	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	total := PageSize + 10
	for i := 0; i < total; i++ {
		insertPost(t, d, friend, "post", base.Add(time.Duration(i)*time.Minute), "active")
	}

	svc := newFeed(d)
	res, err := svc.FirstPage(ctx, viewer)
	if err != nil {
		t.Fatalf("FirstPage: %v", err)
	}
	first := res.Posts
	if len(first) != PageSize {
		t.Fatalf("first page: want %d, got %d", PageSize, len(first))
	}
	if !res.HasMore || res.NextBefore == 0 {
		t.Fatal("expected HasMore with a cursor")
	}

	older, next, err := svc.OlderPage(ctx, viewer, res.NextBefore)
	if err != nil {
		t.Fatalf("OlderPage: %v", err)
	}
	if len(older) != 10 {
		t.Errorf("second page: want 10, got %d", len(older))
	}
	if next != 0 {
		t.Errorf("no further pages expected, got cursor %d", next)
	}
	// No overlap: the cursor post id must not reappear.
	for _, p := range older {
		if p.ID >= res.NextBefore {
			t.Errorf("page overlap: post %d >= cursor %d", p.ID, res.NextBefore)
		}
	}
}

func TestFeed_EmptyWhenNoConnections(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	viewer := seedUser(t, d, "loner")
	other := seedUser(t, d, "other")
	insertPost(t, d, other, "unreachable", time.Now(), "active")

	res, err := newFeed(d).FirstPage(ctx, viewer)
	if err != nil {
		t.Fatalf("FirstPage: %v", err)
	}
	if len(res.Posts) != 0 {
		t.Error("a user with no connections and no own posts should have an empty feed")
	}
}

// TestFeed_IncludesOwnPosts covers issue #41: the viewer's own posts appear in
// the home feed even with no connections, while a stranger's posts stay hidden.
func TestFeed_IncludesOwnPosts(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	viewer := seedUser(t, d, "viewer")
	stranger := seedUser(t, d, "stranger")

	now := time.Now()
	insertPost(t, d, viewer, "my own post", now, "active")
	insertPost(t, d, stranger, "from stranger", now, "active")

	res, err := newFeed(d).FirstPage(ctx, viewer)
	if err != nil {
		t.Fatalf("FirstPage: %v", err)
	}
	all := res.Posts
	if len(all) != 1 {
		t.Fatalf("want exactly 1 visible post, got %d", len(all))
	}
	if all[0].Content != "my own post" {
		t.Errorf("got %q; viewer should see their own post and not the stranger's", all[0].Content)
	}
}

// TestFeed_OwnPostsAlongsideConnections verifies the viewer's own posts are
// interleaved with connections' posts in reverse-chronological order.
func TestFeed_OwnPostsAlongsideConnections(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	viewer := seedUser(t, d, "viewer")
	friend := seedUser(t, d, "friend")
	connect(t, d, viewer, friend)

	mid := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	insertPost(t, d, viewer, "my old post", mid.Add(-time.Hour), "active")
	insertPost(t, d, friend, "friend post", mid.Add(time.Hour), "active")
	insertPost(t, d, viewer, "my new post", mid.Add(2*time.Hour), "active")

	res, err := newFeed(d).FirstPage(ctx, viewer)
	if err != nil {
		t.Fatalf("FirstPage: %v", err)
	}

	var got []string
	for _, p := range res.Posts {
		got = append(got, p.Content)
	}
	want := []string{"my new post", "friend post", "my old post"}
	if len(got) != len(want) {
		t.Fatalf("want %d posts, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: want %q, got %q (full: %v)", i, want[i], got[i], got)
		}
	}
}
