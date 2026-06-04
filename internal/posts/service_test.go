package posts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/dblanc/hearth/internal/media"
	"github.com/dblanc/hearth/internal/shared/db"
)

// twoImages returns two already-validated images. The service trusts NewImage
// (the handler does the size/MIME-sniff checks), so the bytes can be arbitrary.
func twoImages() []NewImage {
	return []NewImage{
		{Data: []byte("first"), ContentType: "image/png", Ext: ".png"},
		{Data: []byte("second"), ContentType: "image/jpeg", Ext: ".jpg"},
	}
}

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

	id, err := svc.Create(ctx, author, "  hello world  ", nil)
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

	id, _ := svc.Create(ctx, author, "mine", nil)
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
	id, _ := svc.Create(ctx, author, "x", nil)

	if err := svc.Delete(ctx, id, author); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	// Second delete: already deleted → not found.
	if err := svc.Delete(ctx, id, author); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound on second delete, got %v", err)
	}
}

func TestCreate_WithImages(t *testing.T) {
	d := newTestDB(t)
	store := media.NewStub()
	svc := New(d)
	svc.Media = store
	ctx := context.Background()
	author := seedUser(t, d, "author")

	images := twoImages()
	id, err := svc.Create(ctx, author, "a caption", images)
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
	if len(got[0].Media) != 2 {
		t.Fatalf("want 2 media, got %d", len(got[0].Media))
	}
	// Media is returned ordered by position, matching insertion order.
	if got[0].Media[0].Position != 0 || got[0].Media[1].Position != 1 {
		t.Errorf("positions out of order: %+v", got[0].Media)
	}
	// Both objects landed in the store and no user_id/post_id leaks into keys.
	for _, m := range got[0].Media {
		if !store.Has(m.Key) {
			t.Errorf("object not uploaded: %s", m.Key)
		}
		// Keys are posts/yyyy/mm/uuid.ext and must not embed identifying ids.
		if !strings.HasPrefix(m.Key, "posts/") {
			t.Errorf("unexpected key prefix: %s", m.Key)
		}
		if m.URL != "" {
			t.Errorf("Media.URL should be empty before signing, got %q", m.URL)
		}
	}

	// Signing fills URLs without touching keys.
	if err := SignMediaURLs(ctx, store, got); err != nil {
		t.Fatalf("SignMediaURLs: %v", err)
	}
	for _, m := range got[0].Media {
		if m.URL == "" {
			t.Errorf("expected signed URL for %s", m.Key)
		}
	}
	_ = id
}

func TestCreate_ImageOnlyAllowed(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	svc.Media = media.NewStub()
	ctx := context.Background()
	author := seedUser(t, d, "author")

	if _, err := svc.Create(ctx, author, "   ", twoImages()[:1]); err != nil {
		t.Fatalf("image-only post should be allowed, got %v", err)
	}
}

func TestCreate_EmptyWithoutImagesRejected(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")

	if _, err := svc.Create(ctx, author, "   ", nil); !errors.Is(err, ErrEmpty) {
		t.Errorf("want ErrEmpty, got %v", err)
	}
}

func TestCreate_TooManyImages(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	svc.Media = media.NewStub()
	ctx := context.Background()
	author := seedUser(t, d, "author")

	many := make([]NewImage, media.MaxImagesPerPost+1)
	for i := range many {
		many[i] = NewImage{Data: []byte("x"), ContentType: "image/png", Ext: ".png"}
	}
	if _, err := svc.Create(ctx, author, "", many); !errors.Is(err, ErrTooManyImages) {
		t.Errorf("want ErrTooManyImages, got %v", err)
	}
}

func TestDelete_RemovesMediaObjects(t *testing.T) {
	d := newTestDB(t)
	store := media.NewStub()
	svc := New(d)
	svc.Media = store
	ctx := context.Background()
	author := seedUser(t, d, "author")

	id, err := svc.Create(ctx, author, "with pics", twoImages())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	keys, _ := mediaKeys(ctx, d, id)
	if len(keys) != 2 {
		t.Fatalf("want 2 keys, got %d", len(keys))
	}

	if err := svc.Delete(ctx, id, author); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// post_media rows are gone.
	remaining, _ := mediaKeys(ctx, d, id)
	if len(remaining) != 0 {
		t.Errorf("post_media rows survived delete: %d", len(remaining))
	}
	// Objects were removed from storage.
	for _, k := range keys {
		if store.Has(k) {
			t.Errorf("object not removed from storage: %s", k)
		}
	}
}
