package likes

import (
	"errors"
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

// Mount registers the like routes. Caller wraps with RequireAuth. Browsers
// can't issue DELETE from a form, so unlike is reachable via both the RESTful
// DELETE and a POST fallback, mirroring the posts package.
func (h *Handlers) Mount(mux *http.ServeMux) {
	mux.Handle("POST /posts/{id}/like", middleware.RequireAuth(http.HandlerFunc(h.like)))
	mux.Handle("DELETE /posts/{id}/like", middleware.RequireAuth(http.HandlerFunc(h.unlike)))
	mux.Handle("POST /posts/{id}/unlike", middleware.RequireAuth(http.HandlerFunc(h.unlike)))
	mux.Handle("GET /posts/{id}/likes", middleware.RequireAuth(http.HandlerFunc(h.likers)))
}

func (h *Handlers) like(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	if !u.Verified {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	id, ok := postID(w, r, h)
	if !ok {
		return
	}
	err := h.Svc.Like(r.Context(), id, u.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		h.Renderer.Error(w, http.StatusNotFound)
		return
	case err != nil:
		slog.Error("likes: like", "user_id", u.ID, "post_id", id, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	redirectBack(w, r)
}

func (h *Handlers) unlike(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	id, ok := postID(w, r, h)
	if !ok {
		return
	}
	if err := h.Svc.Unlike(r.Context(), id, u.ID); err != nil {
		slog.Error("likes: unlike", "user_id", u.ID, "post_id", id, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	redirectBack(w, r)
}

func (h *Handlers) likers(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	id, ok := postID(w, r, h)
	if !ok {
		return
	}
	likers, err := h.Svc.ListLikers(r.Context(), id, u.ID)
	switch {
	// Both "no such post" and "not your post" render as 404: the liker list's
	// existence is hidden from everyone but the author (Technical Plan §4.4.5).
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrNotAuthor):
		h.Renderer.Error(w, http.StatusNotFound)
		return
	case err != nil:
		slog.Error("likes: list", "user_id", u.ID, "post_id", id, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	// likes-modal.js requests the bare list (X-Fragment) to drop into a dialog;
	// a direct visit renders the standalone page (the no-JS fallback).
	template := "post_likes.html"
	if r.Header.Get("X-Fragment") == "1" {
		template = "post_likes_fragment.html"
	}
	h.Renderer.HTML(w, template, render.Page(u, render.M{"Likers": likers}))
}

// postID parses the {id} path value, writing a 400 and returning ok=false when
// it's missing or malformed.
func postID(w http.ResponseWriter, r *http.Request, h *Handlers) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.Renderer.Error(w, http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// redirectBack returns the user to the page they liked from (feed or profile),
// so the like control re-renders in its new state without any client JS.
func redirectBack(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("Referer")
	if target == "" {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
