package likes

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

// mustReadTemplate loads a real template from web/templates so these tests
// assert against the exact markup shipped to users. Path is relative to this
// package (internal/likes).
func mustReadTemplate(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", name))
	if err != nil {
		t.Fatalf("read template %s: %v", name, err)
	}
	return data
}

// newTestHandlers wires likes.Handlers with the real base.html (which carries
// the "likecontrol" define) and the real like_control_fragment.html, so the
// htmx fragment path renders exactly the markup users receive.
func newTestHandlers(t *testing.T) (*Handlers, *Service, *sql.DB) {
	t.Helper()
	svc, d := newTestService(t)
	fsys := fstest.MapFS{
		"base.html":                  {Data: mustReadTemplate(t, "base.html")},
		"like_control_fragment.html": {Data: mustReadTemplate(t, "like_control_fragment.html")},
		"error.html":                 {Data: []byte(`{{define "error.html"}}error {{.Status}}{{end}}`)},
	}
	r, err := render.New(fsys, false)
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}
	return NewHandlers(svc, r), svc, d
}

// likeRequest drives the like handler for postID as the given user, optionally
// as an htmx request, and returns the recorder.
func likeRequest(t *testing.T, h *Handlers, postID, userID int64, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/posts/"+strconv.FormatInt(postID, 10)+"/like", nil)
	req.SetPathValue("id", strconv.FormatInt(postID, 10))
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	u := &middleware.User{ID: userID, Verified: true}
	req = req.WithContext(middleware.WithUser(req.Context(), u))
	rec := httptest.NewRecorder()
	h.like(rec, req)
	return rec
}

func TestLike_HTMXReturnsToggledFragment(t *testing.T) {
	h, _, d := newTestHandlers(t)
	author := seedUser(t, d, "author")
	viewer := seedUser(t, d, "viewer")
	connect(t, d, author, viewer)
	post := seedPost(t, d, author)

	rec := likeRequest(t, h, post, viewer, true)

	if rec.Code != http.StatusOK {
		t.Fatalf("htmx like: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// The fragment now shows the *liked* state: an unlike form and "Liked".
	if !strings.Contains(body, `action="/posts/`+strconv.FormatInt(post, 10)+`/unlike"`) {
		t.Errorf("htmx like fragment should post to unlike next; got:\n%s", body)
	}
	if !strings.Contains(body, "Liked") || !strings.Contains(body, `aria-pressed="true"`) {
		t.Errorf("htmx like fragment should render the liked control; got:\n%s", body)
	}
	// It is a bare fragment, not a full page.
	if strings.Contains(body, "<html") {
		t.Errorf("htmx response should be a fragment, not a full page; got:\n%s", body)
	}
}

func TestUnlike_HTMXReturnsToggledFragment(t *testing.T) {
	h, svc, d := newTestHandlers(t)
	author := seedUser(t, d, "author")
	viewer := seedUser(t, d, "viewer")
	connect(t, d, author, viewer)
	post := seedPost(t, d, author)
	if err := svc.Like(context.Background(), post, viewer); err != nil {
		t.Fatalf("seed like: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/posts/"+strconv.FormatInt(post, 10)+"/unlike", nil)
	req.SetPathValue("id", strconv.FormatInt(post, 10))
	req.Header.Set("HX-Request", "true")
	u := &middleware.User{ID: viewer, Verified: true}
	req = req.WithContext(middleware.WithUser(req.Context(), u))
	rec := httptest.NewRecorder()
	h.unlike(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("htmx unlike: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// Back to the un-liked state: a like form and "Like".
	if !strings.Contains(body, `action="/posts/`+strconv.FormatInt(post, 10)+`/like"`) {
		t.Errorf("htmx unlike fragment should post to like next; got:\n%s", body)
	}
	if !strings.Contains(body, `aria-pressed="false"`) {
		t.Errorf("htmx unlike fragment should render the un-liked control; got:\n%s", body)
	}
}

// Without the HX-Request header the handler keeps the no-JS behaviour: a 303
// redirect back to the page the user acted from.
func TestLike_NoJSRedirects(t *testing.T) {
	h, _, d := newTestHandlers(t)
	author := seedUser(t, d, "author")
	viewer := seedUser(t, d, "viewer")
	connect(t, d, author, viewer)
	post := seedPost(t, d, author)

	rec := likeRequest(t, h, post, viewer, false)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("no-JS like: status = %d, want 303", rec.Code)
	}
}
