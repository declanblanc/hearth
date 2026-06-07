package feed

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/dblanc/hearth/internal/likes"
	"github.com/dblanc/hearth/internal/media"
	"github.com/dblanc/hearth/internal/posts"
	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

type Handlers struct {
	Svc      *Service
	Renderer *render.Renderer
	Media    media.Store // may be nil in dev (post images won't render)
	Likes    *likes.Service
}

func NewHandlers(svc *Service, r *render.Renderer, m media.Store, likeSvc *likes.Service) *Handlers {
	return &Handlers{Svc: svc, Renderer: r, Media: m, Likes: likeSvc}
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
	// The feed only contains posts by the viewer's connections, so the
	// connection check is implicit; sign image URLs for rendering.
	if err := posts.SignMediaURLs(r.Context(), h.Media, res.New); err != nil {
		slog.Error("feed: sign media", "user_id", u.ID, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	if err := posts.SignMediaURLs(r.Context(), h.Media, res.Old); err != nil {
		slog.Error("feed: sign media", "user_id", u.ID, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	// Reflect which of these posts the viewer has already liked so the like
	// control renders in its toggled state. Every feed post is a connection's,
	// never the viewer's own.
	for _, group := range [][]posts.Post{res.New, res.Old} {
		if err := h.Likes.MarkViewerLikes(r.Context(), u.ID, group); err != nil {
			slog.Error("feed: mark likes", "user_id", u.ID, "err", err)
			h.Renderer.Error(w, http.StatusInternalServerError)
			return
		}
	}
	h.Renderer.HTML(w, "home.html", render.Page(u, render.M{
		"New":        res.New,
		"Old":        res.Old,
		"HasMore":    res.HasMore,
		"NextBefore": res.NextBefore,
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
	if err := posts.SignMediaURLs(r.Context(), h.Media, rows); err != nil {
		slog.Error("feed: sign media", "user_id", u.ID, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	if err := h.Likes.MarkViewerLikes(r.Context(), u.ID, rows); err != nil {
		slog.Error("feed: mark likes", "user_id", u.ID, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.Renderer.HTML(w, "feed_older.html", render.Page(u, render.M{
		"Posts":      rows,
		"HasMore":    next > 0,
		"NextBefore": next,
	}))
}
