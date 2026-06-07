package connections

import (
	"context"
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
		"connections.html":          {Data: mustReadTemplate(t, "connections.html")},
		"requests.html":             {Data: mustReadTemplate(t, "requests.html")},
		"invite_link_fragment.html": {Data: mustReadTemplate(t, "invite_link_fragment.html")},
		"invite_created.html":       {Data: mustReadTemplate(t, "invite_created.html")},
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
// connection could never invite again. The control is a form that POSTs to
// /invites; invite-modal.js intercepts it to show the link in a dialog.
const inviteControlMarker = `action="/invites"`

// createInvite serves the bare link fragment when the connections-page modal
// asks for it (X-Fragment), so invite-modal.js can drop a copyable link into the
// dialog without a page navigation.
func TestCreateInvite_FragmentReturnsCopyableLink(t *testing.T) {
	h, _ := newConnectionsHandlers(t)
	user := seedUser(t, h.Svc.DB, "inviter")

	req := httptest.NewRequest(http.MethodPost, "/invites", nil)
	req.Header.Set("X-Fragment", "1")
	u := &middleware.User{ID: user, Username: "inviter", DisplayName: "inviter", Verified: true}
	req = req.WithContext(middleware.WithUser(req.Context(), u))

	rec := httptest.NewRecorder()
	h.createInvite(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /invites (fragment): status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/i/") {
		t.Errorf("fragment should contain the invite URL;\nbody:\n%s", body)
	}
	if !strings.Contains(body, "data-invite-copy") {
		t.Errorf("fragment should contain a copy button;\nbody:\n%s", body)
	}
	// The fragment is a bare snippet — it must not drag in the full page chrome.
	if strings.Contains(body, "Back to connections") {
		t.Errorf("fragment should not include the standalone-page chrome;\nbody:\n%s", body)
	}
}

// Without the X-Fragment header (no JavaScript), the same POST renders the
// standalone invite_created.html page so the flow still works.
func TestCreateInvite_NoFragmentRendersStandalonePage(t *testing.T) {
	h, _ := newConnectionsHandlers(t)
	user := seedUser(t, h.Svc.DB, "inviter")

	req := httptest.NewRequest(http.MethodPost, "/invites", nil)
	u := &middleware.User{ID: user, Username: "inviter", DisplayName: "inviter", Verified: true}
	req = req.WithContext(middleware.WithUser(req.Context(), u))

	rec := httptest.NewRecorder()
	h.createInvite(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /invites (no fragment): status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/i/") {
		t.Errorf("standalone page should contain the invite URL;\nbody:\n%s", body)
	}
	if !strings.Contains(body, "Back to connections") {
		t.Errorf("standalone page should link back to connections;\nbody:\n%s", body)
	}
}

// The requester's name on the /requests page must be a link to their profile
// preview, so a recipient can look before they confirm (issue #7). The link
// uses the /u/ profile prefix (issue #6).
func TestRequestsPage_RequesterNameLinksToProfile(t *testing.T) {
	h, svc := newConnectionsHandlers(t)
	ctx := context.Background()
	recipient := seedUser(t, h.Svc.DB, "recipient")
	requesterID := seedUser(t, h.Svc.DB, "requester")

	// recipient invites; requester accepts, creating a pending incoming request.
	token, err := svc.CreateInvite(ctx, recipient)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := svc.CreateRequest(ctx, token, requesterID); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/requests", nil)
	u := &middleware.User{ID: recipient, Username: "recipient", DisplayName: "recipient", Verified: true}
	req = req.WithContext(middleware.WithUser(req.Context(), u))
	rec := httptest.NewRecorder()
	h.listRequests(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /requests: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/u/requester"`) {
		t.Errorf("requester name should link to /u/requester;\nbody:\n%s", body)
	}
}

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
