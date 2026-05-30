package feed

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

type Handlers struct {
	Svc      *Service
	Renderer *render.Renderer
}

func NewHandlers(svc *Service, r *render.Renderer) *Handlers {
	return &Handlers{Svc: svc, Renderer: r}
}

// Mount registers the feed at the site root and the "load older" partial.
// Caller wraps with RequireAuth.
func (h *Handlers) Mount(mux *http.ServeMux) {
	mux.Handle("GET /{$}", middleware.RequireAuth(http.HandlerFunc(h.index)))
	mux.Handle("GET /feed/older", middleware.RequireAuth(http.HandlerFunc(h.older)))
}

func (h *Handlers) index(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	res, err := h.Svc.FirstPage(r.Context(), u.ID)
	if err != nil {
		slog.Error("feed: first page", "user_id", u.ID, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.Renderer.HTML(w, "home.html", render.Page(u, render.M{
		"New":        res.New,
		"Old":        res.Old,
		"HasMore":    res.HasMore,
		"NextBefore": res.NextBefore,
		"PostError":  postError(r.URL.Query().Get("post_error")),
	}))
}

// older renders the next page of historical posts as a standalone fragment
// (htmx swaps it in below the current list).
func (h *Handlers) older(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	rows, next, err := h.Svc.OlderPage(r.Context(), u.ID, before)
	if err != nil {
		slog.Error("feed: older page", "user_id", u.ID, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.Renderer.HTML(w, "feed_older.html", render.Page(u, render.M{
		"Posts":      rows,
		"HasMore":    next > 0,
		"NextBefore": next,
	}))
}

// postError maps the redirect query flag from a rejected post into a message.
func postError(code string) string {
	switch code {
	case "empty":
		return "Your post can't be empty."
	case "toolong":
		return "Your post is too long (1000 characters max)."
	default:
		return ""
	}
}
