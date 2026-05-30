package posts

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

func (h *Handlers) Mount(mux *http.ServeMux) {
	mux.Handle("POST /posts", middleware.RequireAuth(http.HandlerFunc(h.create)))
	mux.Handle("DELETE /posts/{id}", middleware.RequireAuth(http.HandlerFunc(h.delete)))
	// Browsers can't issue DELETE from a form; accept POST with a method
	// override for the no-JS / htmx-less path.
	mux.Handle("POST /posts/{id}/delete", middleware.RequireAuth(http.HandlerFunc(h.delete)))
}

func (h *Handlers) create(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	if !u.Verified {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	_, err := h.Svc.Create(r.Context(), u.ID, r.FormValue("content"))
	if err != nil {
		switch {
		case errors.Is(err, ErrEmpty):
			http.Redirect(w, r, "/?post_error=empty", http.StatusSeeOther)
		case errors.Is(err, ErrTooLong):
			http.Redirect(w, r, "/?post_error=toolong", http.StatusSeeOther)
		default:
			slog.Error("posts: create", "user_id", u.ID, "err", err)
			h.Renderer.Error(w, http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handlers) delete(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	err = h.Svc.Delete(r.Context(), id, u.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		h.Renderer.Error(w, http.StatusNotFound)
		return
	case errors.Is(err, ErrNotAuthor):
		h.Renderer.Error(w, http.StatusForbidden)
		return
	case err != nil:
		slog.Error("posts: delete", "user_id", u.ID, "post_id", id, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	// Send the user back where they came from (profile or feed).
	target := r.Header.Get("Referer")
	if target == "" {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
