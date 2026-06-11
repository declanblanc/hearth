// Package pages serves the small set of public, content-only pages that aren't
// tied to a feature domain: the marketing landing page shown at the site root to
// logged-out visitors, and the "About Hearth" page that spells out the product's
// philosophy. The mission statement copy these share lives in a single template
// partial (base.html's "mission") so the two pages can't drift.
package pages

import (
	"net/http"

	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

type Handlers struct {
	Renderer *render.Renderer
	// FeedRoot is the authenticated home feed. The site root branches to it for
	// signed-in viewers and shows the landing page to everyone else, so the same
	// "/" URL means "your feed" once you're in and "what is this place" before.
	FeedRoot http.Handler
}

func NewHandlers(r *render.Renderer, feedRoot http.Handler) *Handlers {
	return &Handlers{Renderer: r, FeedRoot: feedRoot}
}

// Mount registers the public root and the About page. Both are bare (no
// RequireAuth): root does its own auth branch, and About is readable by anyone.
func (h *Handlers) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", h.root)
	mux.HandleFunc("GET /about", h.about)
}

// root serves the home feed to signed-in viewers and the landing page to
// logged-out ones. Unlike most root handlers it is not wrapped in RequireAuth —
// a logged-out visit is the landing page's whole reason to exist, not a
// redirect to /login.
func (h *Handlers) root(w http.ResponseWriter, r *http.Request) {
	if middleware.UserFrom(r.Context()) != nil {
		h.FeedRoot.ServeHTTP(w, r)
		return
	}
	// A plain logged-out visit: no invite context, just the mission statement.
	h.Renderer.HTML(w, "landing.html", render.Page(nil, nil))
}

func (h *Handlers) about(w http.ResponseWriter, r *http.Request) {
	// Pass the viewer (may be nil) so the shared header nav renders correctly
	// whether or not the reader is signed in.
	u := middleware.UserFrom(r.Context())
	h.Renderer.HTML(w, "about.html", render.Page(u, nil))
}
