package notifications

import (
	"log/slog"
	"net/http"

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

// Mount registers the notifications routes. Caller is responsible for wrapping
// with RequireAuth.
func (h *Handlers) Mount(mux *http.ServeMux) {
	mux.Handle("GET /notifications", middleware.RequireAuth(http.HandlerFunc(h.list)))
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	items, err := h.Svc.List(r.Context(), u.ID)
	if err != nil {
		slog.Error("notifications: list", "user_id", u.ID, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	// Clear the unread state now that the user has opened the inbox. Do this
	// after fetching so this same view still renders the just-cleared items.
	if err := h.Svc.MarkAllRead(r.Context(), u.ID); err != nil {
		slog.Error("notifications: mark read", "user_id", u.ID, "err", err)
	}
	// The dot is gone for the rest of this page render too.
	u.UnreadDot = false
	h.Renderer.HTML(w, "notifications.html", render.Page(u, render.M{"Items": items}))
}

// LoadUnread is middleware that flags the header dot for authenticated users.
// It mutates the context User (a pointer set by the session loader) so every
// page's base template can show the dot without each handler threading it
// through. Runs only for logged-in users; errors are swallowed (a missing dot
// is not worth a 500).
func (h *Handlers) LoadUnread(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := middleware.UserFrom(r.Context()); u != nil {
			if unread, err := h.Svc.HasUnread(r.Context(), u.ID); err == nil {
				u.UnreadDot = unread
			}
		}
		next.ServeHTTP(w, r)
	})
}
