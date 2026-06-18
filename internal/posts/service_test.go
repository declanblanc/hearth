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

// twoImages returns two already-validated images. The service trusts NewMedia
// (the handler does the size/MIME-sniff checks), so the bytes can be arbitrary.
func twoImages() []NewMedia {
	return []NewMedia{
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

	many := make([]NewMedia, media.MaxImagesPerPost+1)
	for i := range many {
		many[i] = NewMedia{Data: []byte("x"), ContentType: "image/png", Ext: ".png"}
	}
	if _, err := svc.Create(ctx, author, "", many); !errors.Is(err, ErrTooManyImages) {
		t.Errorf("want ErrTooManyImages, got %v", err)
	}
}

// oneVideo returns a validated video attachment with a poster, as the handler
// would hand to the service after sniffing and extracting it.
func oneVideo() []NewMedia {
	return []NewMedia{{
		Data:        []byte("fake-mp4"),
		ContentType: "video/mp4",
		Ext:         ".mp4",
		Width:       1280,
		Height:      720,
		PosterData:  []byte("fake-jpeg-poster"),
		PosterExt:   ".jpg",
	}}
}

func TestCreate_WithVideoPersistsPoster(t *testing.T) {
	d := newTestDB(t)
	store := media.NewStub()
	svc := New(d)
	svc.Media = store
	ctx := context.Background()
	author := seedUser(t, d, "author")

	if _, err := svc.Create(ctx, author, "a clip", oneVideo()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.ListByAuthor(ctx, author)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(got) != 1 || len(got[0].Media) != 1 {
		t.Fatalf("want 1 post with 1 attachment, got %d posts", len(got))
	}
	m := got[0].Media[0]
	if !m.IsVideo() {
		t.Errorf("attachment should be a video, got %q", m.ContentType)
	}
	if m.PosterKey == "" {
		t.Fatal("video should have a poster_key persisted")
	}
	// Both the video object and the poster object landed in storage.
	if !store.Has(m.Key) {
		t.Errorf("video object missing from store: %s", m.Key)
	}
	if !store.Has(m.PosterKey) {
		t.Errorf("poster object missing from store: %s", m.PosterKey)
	}

	// Signing fills both URLs without mutating the keys.
	if err := SignMediaURLs(ctx, store, got); err != nil {
		t.Fatalf("SignMediaURLs: %v", err)
	}
	if got[0].Media[0].URL == "" || got[0].Media[0].PosterURL == "" {
		t.Errorf("expected both video and poster URLs signed, got %+v", got[0].Media[0])
	}
}

func TestDelete_RemovesVideoAndPoster(t *testing.T) {
	d := newTestDB(t)
	store := media.NewStub()
	svc := New(d)
	svc.Media = store
	ctx := context.Background()
	author := seedUser(t, d, "author")

	id, err := svc.Create(ctx, author, "a clip", oneVideo())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	keys, _ := mediaKeys(ctx, d, id)
	if len(keys) != 2 { // the video and its poster
		t.Fatalf("want 2 keys (video + poster), got %d", len(keys))
	}

	if err := svc.Delete(ctx, id, author); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, k := range keys {
		if store.Has(k) {
			t.Errorf("object not removed from storage: %s", k)
		}
	}
}

func TestCreate_PhotosAndVideoTogether(t *testing.T) {
	d := newTestDB(t)
	store := media.NewStub()
	svc := New(d)
	svc.Media = store
	ctx := context.Background()
	author := seedUser(t, d, "author")

	mixed := append(twoImages(), oneVideo()...)
	if _, err := svc.Create(ctx, author, "", mixed); err != nil {
		t.Fatalf("a post may mix photos and a video, got %v", err)
	}

	got, err := svc.ListByAuthor(ctx, author)
	if err != nil {
		t.Fatalf("ListByAuthor: %v", err)
	}
	if len(got) != 1 || len(got[0].Media) != 3 {
		t.Fatalf("want 1 post with 3 attachments, got %d posts", len(got))
	}
	var images, videos int
	for _, m := range got[0].Media {
		if m.IsVideo() {
			videos++
			if m.PosterKey == "" {
				t.Errorf("video should keep its poster_key")
			}
		} else {
			images++
		}
	}
	if images != 2 || videos != 1 {
		t.Errorf("want 2 images + 1 video, got %d images + %d videos", images, videos)
	}
}

func TestCreate_TooManyVideosRejected(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	svc.Media = media.NewStub()
	ctx := context.Background()
	author := seedUser(t, d, "author")

	two := append(oneVideo(), oneVideo()...)
	if _, err := svc.Create(ctx, author, "", two); !errors.Is(err, ErrTooManyVideos) {
		t.Errorf("want ErrTooManyVideos, got %v", err)
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

// TestSignAvatarURLs covers issue #40: signing mints a short-lived URL for each
// author's photo in place, leaves authors without a photo untouched, and never
// mutates the underlying key. A nil store is a safe no-op.
func TestSignAvatarURLs(t *testing.T) {
	ctx := context.Background()
	store := media.NewStub()

	ps := []Post{
		{AuthorPhotoKey: "profile/has-photo.jpg"},
		{AuthorPhotoKey: ""},
	}
	if err := SignAvatarURLs(ctx, store, ps); err != nil {
		t.Fatalf("SignAvatarURLs: %v", err)
	}
	if ps[0].AuthorPhotoURL == "" {
		t.Error("author with a photo should get a signed avatar URL")
	}
	if ps[0].AuthorPhotoKey != "profile/has-photo.jpg" {
		t.Errorf("signing must not mutate the key, got %q", ps[0].AuthorPhotoKey)
	}
	if ps[1].AuthorPhotoURL != "" {
		t.Errorf("author without a photo should have no URL, got %q", ps[1].AuthorPhotoURL)
	}

	// A nil store (dev without R2) leaves everything empty rather than erroring.
	none := []Post{{AuthorPhotoKey: "profile/x.jpg"}}
	if err := SignAvatarURLs(ctx, nil, none); err != nil {
		t.Fatalf("SignAvatarURLs(nil store): %v", err)
	}
	if none[0].AuthorPhotoURL != "" {
		t.Error("nil store should leave AuthorPhotoURL empty")
	}
}
