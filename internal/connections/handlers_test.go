package connections

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dblanc/hearth/internal/notifications"
	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

// mustReadTemplate loads a real page template from web/templates so the handler
// tests assert against the exact markup shipped to users, not a stand-in. The
// path is relative to this package (internal/connections).
func mustReadTemplate(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", name))
	if err != nil {
		t.Fatalf("read template %s: %v", name, err)
	}
	return data
}

// newConnectionsHandlers wires a Handlers backed by an in-memory DB and a
// renderer that loads the *real* connections.html template. Loading the actual
// template (rather than a stub) is what lets these tests assert on the markup
// the user sees, including the invite-generation control.
func newConnectionsHandlers(t *testing.T) (*Handlers, *Service) {
	t.Helper()
	d := newTestDB(t)
	svc := New(d, notifications.New(d), "https://hearth.test")

	// base.html mirrors the production layout closely enough to render any page
	// template that extends "base"; connections.html provides the "content"
	// block. error.html covers the renderer's failure path.
	fsys := fstest.MapFS{
		"base.html": {Data: []byte(
			`{{define "base"}}{{block "content" .}}{{end}}{{end}}`,
		)},
		"connections.html": {Data: mustReadTemplate(t, "connections.html")},
		"error.html": {Data: []byte(
			`{{define "error.html"}}error {{.Status}}{{end}}`,
		)},
	}
	r, err := render.New(fsys, false)
	if err != nil {
		t.Fatalf("render.New: %v", err)
	}

	// Media is nil (dev/test) so photoURL returns "" and no signed URLs are needed.
	h := NewHandlers(svc, r, nil, []byte("test-secret"), false)
	return h, svc
}

// renderConnectionsPage drives listConnections for the given user and returns
// the rendered HTML body, simulating the session-loader middleware by placing
// the user in the request context.
func renderConnectionsPage(t *testing.T, h *Handlers, userID int64, username string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/connections", nil)
	u := &middleware.User{ID: userID, Username: username, DisplayName: username, Verified: true}
	req = req.WithContext(middleware.WithUser(req.Context(), u))

	rec := httptest.NewRecorder()
	h.listConnections(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /connections: status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// The invite-generation control must always be present, regardless of how many
// connections the user already has. This is the regression guard for issue #4:
// previously the affordance was gated on the empty state, so anyone with a
// connection could never invite again.
const inviteControlMarker = `href="/settings/invites"`

func TestConnectionsPage_ShowsInviteControl_WhenUserHasNoConnections(t *testing.T) {
	h, _ := newConnectionsHandlers(t)
	d := h.Svc.DB
	lonely := seedUser(t, d, "lonely")

	body := renderConnectionsPage(t, h, lonely, "lonely")

	if !strings.Contains(body, inviteControlMarker) {
		t.Fatalf("connections page (no connections) is missing the invite control;\nbody:\n%s", body)
	}
	if !strings.Contains(body, "Generate invitation link") {
		t.Errorf("expected a 'Generate invitation link' label on the empty connections page")
	}
}

func TestConnectionsPage_ShowsInviteControl_WhenUserHasConnections(t *testing.T) {
	h, _ := newConnectionsHandlers(t)
	d := h.Svc.DB
	owner := seedUser(t, d, "owner")
	friend := seedUser(t, d, "friend")

	// Store the connection canonically as (min_id, max_id) per the data model.
	if _, err := d.Exec(
		`INSERT INTO connections (user_a_id, user_b_id) VALUES (?, ?)`,
		min64(owner, friend), max64(owner, friend),
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	body := renderConnectionsPage(t, h, owner, "owner")

	// The connection itself renders...
	if !strings.Contains(body, "@friend") {
		t.Fatalf("expected the existing connection to render;\nbody:\n%s", body)
	}
	// ...and crucially the invite control is *still* present alongside it.
	if !strings.Contains(body, inviteControlMarker) {
		t.Fatalf("connections page (with connections) is missing the invite control;\nbody:\n%s", body)
	}
	if !strings.Contains(body, "Generate invitation link") {
		t.Errorf("expected a 'Generate invitation link' label when the user has connections")
	}
}
