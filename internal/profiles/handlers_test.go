package profiles

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dblanc/hearth/internal/auth"
	"github.com/dblanc/hearth/internal/connections"
	"github.com/dblanc/hearth/internal/media"
	"github.com/dblanc/hearth/internal/notifications"
	"github.com/dblanc/hearth/internal/posts"
	"github.com/dblanc/hearth/internal/shared/db"
	"github.com/dblanc/hearth/internal/shared/email"
	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

// --- test helpers ---

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

func newTestRenderer(t *testing.T) *render.Renderer {
	t.Helper()
	fsys := fstest.MapFS{
		"base.html": {Data: []byte(
			`{{define "base"}}{{block "content" .}}{{end}}{{end}}`,
		)},
		"profile_edit.html": {Data: []byte(
			`{{define "profile_edit.html"}}{{template "base" .}}{{end}}` +
				`{{define "content"}}edit profile saved={{.Saved}} photo_err={{index .Errors "photo"}}{{end}}`,
		)},
		"profile_view.html": {Data: []byte(
			`{{define "profile_view.html"}}{{template "base" .}}{{end}}` +
				`{{define "content"}}view profile{{end}}`,
		)},
		"account.html": {Data: []byte(
			`{{define "account.html"}}{{template "base" .}}{{end}}` +
				`{{define "content"}}account{{end}}`,
		)},
		"account_deleted.html": {Data: []byte(
			`{{define "account_deleted.html"}}{{template "base" .}}{{end}}` +
				`{{define "content"}}deleted{{end}}`,
		)},
		"error.html": {Data: []byte(
			`{{define "error.html"}}error {{.Status}}{{end}}`,
		)},
	}
	r, err := render.New(fsys, false)
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	return r
}

// createUser inserts a user via the auth service and returns their ID.
func createUser(t *testing.T, authSvc *auth.Service, username string) int64 {
	t.Helper()
	uid, err := authSvc.Signup(context.Background(), auth.SignupInput{
		Username:  username,
		Email:     username + "@example.com",
		Password:  "password-is-very-long",
		FirstName: "Test",
		LastName:  "User",
	})
	if err != nil {
		t.Fatalf("Signup(%s): %v", username, err)
	}
	return uid
}

// authedRequest creates an HTTP request with a user already in the context,
// simulating what the session loader middleware does in production.
func authedRequest(method, target string, body io.Reader, userID int64, username string) *http.Request {
	r := httptest.NewRequest(method, target, body)
	u := &middleware.User{ID: userID, Username: username, DisplayName: "Test User", Verified: true}
	return r.WithContext(middleware.WithUser(r.Context(), u))
}

func newHandlers(t *testing.T, d *sql.DB, m media.Store) (*Handlers, *auth.Service, *Service) {
	t.Helper()
	stub := &email.Stub{}
	authSvc := auth.New(d, stub, "https://hearth.test")
	notifSvc := notifications.New(d)
	connSvc := connections.New(d, notifSvc, "https://hearth.test")
	postSvc := posts.New(d)
	profileSvc := New(d)
	profileSvc.Media = m
	r := newTestRenderer(t)
	h := NewHandlers(profileSvc, authSvc, connSvc, postSvc, r, m, false)
	return h, authSvc, profileSvc
}

// jpegHeader returns a minimal 512-byte slice that Go's http.DetectContentType
// identifies as image/jpeg.
func jpegHeader() []byte {
	data := make([]byte, 512)
	// JPEG magic bytes: FF D8 FF
	data[0] = 0xFF
	data[1] = 0xD8
	data[2] = 0xFF
	data[3] = 0xE0
	return data
}

// buildMultipartForm returns a body and content-type for a multipart form with
// optional file upload.
func buildMultipartForm(t *testing.T, fields map[string]string, filename string, fileContent []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if filename != "" {
		fw, err := w.CreateFormFile("photo", filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := fw.Write(fileContent); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

// --- service-level tests ---

// TestSoftDelete_DeletesPhotoFromR2 verifies that SoftDelete removes the
// user's profile photo from R2 immediately — not deferred to hard-delete.
// Per spec §0.7: photo_key = NULL and old photo removed at soft-delete time.
func TestSoftDelete_DeletesPhotoFromR2(t *testing.T) {
	d := newTestDB(t)
	m := media.NewStub()
	stub := &email.Stub{}
	authSvc := auth.New(d, stub, "https://hearth.test")
	profileSvc := New(d)
	profileSvc.Media = m
	profileSvc.Now = time.Now

	uid := createUser(t, authSvc, "ghost")
	ctx := context.Background()

	const photoKey = "profile/old-ghost.jpg"
	if _, err := d.ExecContext(ctx, `UPDATE users SET photo_key = ? WHERE id = ?`, photoKey, uid); err != nil {
		t.Fatalf("seed photo_key: %v", err)
	}
	_ = m.Upload(ctx, photoKey, strings.NewReader("fake"), "image/jpeg")

	if err := profileSvc.SoftDelete(ctx, uid); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	if m.Has(photoKey) {
		t.Error("expected photo to be deleted from R2 at soft-delete time")
	}
}

func TestHardDeleteSweep_RemovesExpired(t *testing.T) {
	d := newTestDB(t)
	stub := &email.Stub{}
	authSvc := auth.New(d, stub, "https://hearth.test")
	profileSvc := New(d)
	profileSvc.Now = time.Now

	uid := createUser(t, authSvc, "expired")
	ctx := context.Background()

	if err := profileSvc.SoftDelete(ctx, uid); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	// Backdate deleted_at past the 30-day retention window.
	if _, err := d.ExecContext(ctx, `UPDATE users SET deleted_at = ? WHERE id = ?`,
		time.Now().Add(-31*24*time.Hour), uid); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	n, err := profileSvc.HardDeleteExpired(ctx)
	if err != nil {
		t.Fatalf("HardDeleteExpired: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 removed, got %d", n)
	}

	var count int
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, uid).Scan(&count)
	if count != 0 {
		t.Error("expected user row to be hard-deleted")
	}
}

func TestHardDeleteSweep_SkipsRecent(t *testing.T) {
	d := newTestDB(t)
	stub := &email.Stub{}
	authSvc := auth.New(d, stub, "https://hearth.test")
	profileSvc := New(d)
	profileSvc.Now = time.Now

	uid := createUser(t, authSvc, "recent")
	ctx := context.Background()

	if err := profileSvc.SoftDelete(ctx, uid); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	// deleted_at is recent (just now), so should not be swept.
	n, err := profileSvc.HardDeleteExpired(ctx)
	if err != nil {
		t.Fatalf("HardDeleteExpired: %v", err)
	}
	if n != 0 {
		t.Fatalf("want 0 removed, got %d", n)
	}
}

func TestHardDeleteSweep_RemovesPostMediaFromR2(t *testing.T) {
	d := newTestDB(t)
	stub := &email.Stub{}
	store := media.NewStub()
	authSvc := auth.New(d, stub, "https://hearth.test")
	profileSvc := New(d)
	profileSvc.Media = store
	profileSvc.Now = time.Now

	postSvc := posts.New(d)
	postSvc.Media = store

	uid := createUser(t, authSvc, "expired")
	ctx := context.Background()

	// Author posts an image, then deletes their account; the post (and its
	// image) survive soft-delete and must be cleaned up at hard-delete.
	if _, err := postSvc.Create(ctx, uid, "bye", []posts.NewImage{
		{Data: []byte("img"), ContentType: "image/png", Ext: ".png"},
	}); err != nil {
		t.Fatalf("Create post: %v", err)
	}
	var key string
	if err := d.QueryRowContext(ctx, `SELECT object_key FROM post_media LIMIT 1`).Scan(&key); err != nil {
		t.Fatalf("read key: %v", err)
	}
	if !store.Has(key) {
		t.Fatalf("expected object %s in store before delete", key)
	}

	if err := profileSvc.SoftDelete(ctx, uid); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE users SET deleted_at = ? WHERE id = ?`,
		time.Now().Add(-31*24*time.Hour), uid); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if _, err := profileSvc.HardDeleteExpired(ctx); err != nil {
		t.Fatalf("HardDeleteExpired: %v", err)
	}
	if store.Has(key) {
		t.Errorf("post image %s should have been removed from storage", key)
	}
}

// --- handler-level tests ---

func TestEditProfile_TextPersistsAcrossRequests(t *testing.T) {
	d := newTestDB(t)
	h, authSvc, _ := newHandlers(t, d, nil)
	uid := createUser(t, authSvc, "alice")

	body := url.Values{
		"first_name": {"Alice"},
		"last_name":  {"Anderson"},
		"bio":        {"Hello world"},
		"pronouns":   {"she/her"},
	}
	req := authedRequest(http.MethodPost, "/settings/profile",
		strings.NewReader(body.Encode()), uid, "alice")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.editSubmit(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("editSubmit: want 200, got %d", rr.Code)
	}

	// Fetch profile and verify.
	p, err := h.Svc.Get(context.Background(), uid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.FirstName != "Alice" || p.LastName != "Anderson" {
		t.Errorf("Name: got %q %q, want Alice Anderson", p.FirstName, p.LastName)
	}
	if p.Bio != "Hello world" {
		t.Errorf("Bio: got %q, want %q", p.Bio, "Hello world")
	}
	if p.Pronouns != "she/her" {
		t.Errorf("Pronouns: got %q, want %q", p.Pronouns, "she/her")
	}
}

func TestEditProfile_ValidationError(t *testing.T) {
	d := newTestDB(t)
	h, authSvc, _ := newHandlers(t, d, nil)
	uid := createUser(t, authSvc, "bob")

	body := url.Values{
		"first_name": {""}, // too short — validation should fail
		"last_name":  {""},
		"bio":        {""},
		"pronouns":   {""},
	}
	req := authedRequest(http.MethodPost, "/settings/profile",
		strings.NewReader(body.Encode()), uid, "bob")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.editSubmit(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", rr.Code)
	}
}

func TestEditProfile_PhotoUploaded(t *testing.T) {
	d := newTestDB(t)
	m := media.NewStub()
	h, authSvc, profileSvc := newHandlers(t, d, m)
	uid := createUser(t, authSvc, "cam")

	buf, ct := buildMultipartForm(t, map[string]string{
		"first_name": "Cam", "last_name": "C",
		"bio":      "",
		"pronouns": "",
	}, "photo.jpg", jpegHeader())

	req := authedRequest(http.MethodPost, "/settings/profile", buf, uid, "cam")
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h.editSubmit(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("editSubmit: want 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}

	p, err := profileSvc.Get(context.Background(), uid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.PhotoKey == "" {
		t.Fatal("expected photo_key to be set after upload")
	}
	if !m.Has(p.PhotoKey) {
		t.Errorf("photo key %q not found in stub store", p.PhotoKey)
	}
}

func TestEditProfile_PhotoTooLarge(t *testing.T) {
	d := newTestDB(t)
	m := media.NewStub()
	h, authSvc, _ := newHandlers(t, d, m)
	uid := createUser(t, authSvc, "dana")

	// Build a file slightly larger than the per-image limit.
	oversized := make([]byte, media.MaxProfilePhotoSize+1)
	// Set JPEG magic at the start so type detection isn't the failure point.
	copy(oversized, jpegHeader())

	buf, ct := buildMultipartForm(t, map[string]string{
		"first_name": "Dana", "last_name": "D",
		"bio":      "",
		"pronouns": "",
	}, "big.jpg", oversized)

	req := authedRequest(http.MethodPost, "/settings/profile", buf, uid, "dana")
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h.editSubmit(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized photo: want 422, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "25 MB") {
		t.Errorf("expected size error in response body, got: %s", rr.Body.String())
	}
	if len(m.Deleted()) != 0 {
		t.Error("expected no uploads or deletes for rejected photo")
	}
}

func TestEditProfile_NonImageRejected(t *testing.T) {
	d := newTestDB(t)
	m := media.NewStub()
	h, authSvc, _ := newHandlers(t, d, m)
	uid := createUser(t, authSvc, "evan")

	// Submit a text file with a .jpg extension — type sniff should catch it.
	textContent := []byte("this is not an image, no matter the extension")
	buf, ct := buildMultipartForm(t, map[string]string{
		"first_name": "Evan", "last_name": "E",
		"bio":      "",
		"pronouns": "",
	}, "fake.jpg", textContent)

	req := authedRequest(http.MethodPost, "/settings/profile", buf, uid, "evan")
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h.editSubmit(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("non-image: want 422, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "JPEG") {
		t.Errorf("expected type error in response body, got: %s", rr.Body.String())
	}
}

func TestEditProfile_OldPhotoDeletedOnReplacement(t *testing.T) {
	d := newTestDB(t)
	m := media.NewStub()
	h, authSvc, profileSvc := newHandlers(t, d, m)
	uid := createUser(t, authSvc, "fern")
	ctx := context.Background()

	// Seed an existing photo key in the DB and stub store.
	oldKey := "profile/old-fern.jpg"
	_ = m.Upload(ctx, oldKey, strings.NewReader("old"), "image/jpeg")
	if _, err := d.ExecContext(ctx, `UPDATE users SET photo_key = ? WHERE id = ?`, oldKey, uid); err != nil {
		t.Fatalf("seed photo_key: %v", err)
	}

	// Upload a new photo.
	buf, ct := buildMultipartForm(t, map[string]string{
		"first_name": "Fern", "last_name": "F",
		"bio":      "",
		"pronouns": "",
	}, "new.jpg", jpegHeader())
	req := authedRequest(http.MethodPost, "/settings/profile", buf, uid, "fern")
	req.Header.Set("Content-Type", ct)
	rr := httptest.NewRecorder()
	h.editSubmit(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}

	// Old key should be deleted from R2; new key should be present.
	if m.Has(oldKey) {
		t.Error("expected old photo to be deleted from R2 store")
	}
	p, _ := profileSvc.Get(ctx, uid)
	if p.PhotoKey == "" || p.PhotoKey == oldKey {
		t.Errorf("expected a new photo_key, got %q", p.PhotoKey)
	}
	if !m.Has(p.PhotoKey) {
		t.Errorf("new photo key %q not in store", p.PhotoKey)
	}
}

func TestDeleteAccount_SoftDeletes(t *testing.T) {
	d := newTestDB(t)
	h, authSvc, _ := newHandlers(t, d, nil)
	uid := createUser(t, authSvc, "greg")

	body := url.Values{"password": {"password-is-very-long"}}
	req := authedRequest(http.MethodPost, "/settings/account/delete",
		strings.NewReader(body.Encode()), uid, "greg")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.deleteAccount(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("deleteAccount: want 200, got %d", rr.Code)
	}

	// Account should be inaccessible after soft-delete.
	_, err := h.Svc.GetByUsername(context.Background(), "greg")
	if err == nil {
		t.Error("expected ErrNotFound after soft-delete, got nil")
	}
}

func TestDeleteAccount_WrongPassword(t *testing.T) {
	d := newTestDB(t)
	h, authSvc, _ := newHandlers(t, d, nil)
	uid := createUser(t, authSvc, "hana")

	body := url.Values{"password": {"wrong-password-here"}}
	req := authedRequest(http.MethodPost, "/settings/account/delete",
		strings.NewReader(body.Encode()), uid, "hana")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.deleteAccount(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong password: want 422, got %d", rr.Code)
	}
}

func TestViewProfile_NonOwnerGets404(t *testing.T) {
	d := newTestDB(t)
	h, authSvc, _ := newHandlers(t, d, nil)
	_ = createUser(t, authSvc, "iris")
	otherUID := createUser(t, authSvc, "jack")

	// Logged in as jack, viewing iris's profile.
	req := authedRequest(http.MethodGet, "/iris", nil, otherUID, "jack")
	req = req.WithContext(req.Context())
	// Simulate path value extraction (normally done by http.ServeMux).
	req.SetPathValue("username", "iris")
	rr := httptest.NewRecorder()
	h.viewProfile(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("non-owner view: want 404, got %d", rr.Code)
	}
}

func TestViewProfile_OwnerSeesProfile(t *testing.T) {
	d := newTestDB(t)
	h, authSvc, _ := newHandlers(t, d, nil)
	uid := createUser(t, authSvc, "kai")

	req := authedRequest(http.MethodGet, "/kai", nil, uid, "kai")
	req.SetPathValue("username", "kai")
	rr := httptest.NewRecorder()
	h.viewProfile(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("owner view: want 200, got %d", rr.Code)
	}
}

func TestUpdatePhoto_ReturnsOldKey(t *testing.T) {
	d := newTestDB(t)
	stub := &email.Stub{}
	authSvc := auth.New(d, stub, "https://hearth.test")
	profileSvc := New(d)
	uid := createUser(t, authSvc, "leo")
	ctx := context.Background()

	// First photo.
	old, err := profileSvc.UpdatePhoto(ctx, uid, "profile/first.jpg")
	if err != nil {
		t.Fatalf("UpdatePhoto: %v", err)
	}
	if old != "" {
		t.Errorf("expected no old key on first update, got %q", old)
	}

	// Replace with second photo.
	old2, err := profileSvc.UpdatePhoto(ctx, uid, "profile/second.jpg")
	if err != nil {
		t.Fatalf("UpdatePhoto second: %v", err)
	}
	if old2 != "profile/first.jpg" {
		t.Errorf("expected old key %q, got %q", "profile/first.jpg", old2)
	}

	// Confirm DB has the new key.
	p, err := profileSvc.Get(ctx, uid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.PhotoKey != "profile/second.jpg" {
		t.Errorf("photo_key: got %q, want %q", p.PhotoKey, "profile/second.jpg")
	}
}

// Compile-time check: Stub satisfies the Store interface.
var _ media.Store = (*media.Stub)(nil)

// Verify the error message format used in the template response.
func TestEditProfile_OversizedErrorMessage(t *testing.T) {
	msg := fmt.Sprintf("Photo must be %d MB or smaller.", media.MaxProfilePhotoSize/(1024*1024))
	_ = msg // just a constant-check; actual text comes from the handler
}
