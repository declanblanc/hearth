package posts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

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

func TestValidate_Boundaries(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"empty", "", ErrEmpty},
		{"whitespace only", "   \n\t ", ErrEmpty},
		{"single char", "a", nil},
		{"exactly max", strings.Repeat("x", MaxContentLen), nil},
		{"over max", strings.Repeat("x", MaxContentLen+1), ErrTooLong},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Validate(c.input)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("Validate(%q): got %v, want %v", c.name, err, c.wantErr)
			}
		})
	}
}

func TestValidate_CountsRunesNotBytes(t *testing.T) {
	// 1000 multi-byte runes is well over 1000 bytes but exactly at the limit.
	emoji := strings.Repeat("é", MaxContentLen)
	if _, err := Validate(emoji); err != nil {
		t.Errorf("1000 runes should be valid, got %v", err)
	}
}

func TestCreateReadDelete(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")

	id, err := svc.Create(ctx, author, "  hello world  ")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.ListByAuthor(ctx, author)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 post, got %d", len(got))
	}
	// Content is trimmed but otherwise stored verbatim.
	if got[0].Content != "hello world" {
		t.Errorf("content: got %q", got[0].Content)
	}

	if err := svc.Delete(ctx, id, author); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ = svc.ListByAuthor(ctx, author)
	if len(got) != 0 {
		t.Errorf("deleted post should not appear, got %d", len(got))
	}
}

func TestDelete_NonAuthorForbidden(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	other := seedUser(t, d, "other")

	id, _ := svc.Create(ctx, author, "mine")
	err := svc.Delete(ctx, id, other)
	if !errors.Is(err, ErrNotAuthor) {
		t.Fatalf("want ErrNotAuthor, got %v", err)
	}
	// Post should still be there.
	got, _ := svc.ListByAuthor(ctx, author)
	if len(got) != 1 {
		t.Error("post should survive a non-author delete attempt")
	}
}

func TestDelete_NotFound(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	id, _ := svc.Create(ctx, author, "x")

	if err := svc.Delete(ctx, id, author); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	// Second delete: already deleted → not found.
	if err := svc.Delete(ctx, id, author); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound on second delete, got %v", err)
	}
}
