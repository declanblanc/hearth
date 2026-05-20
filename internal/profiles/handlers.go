package profiles

import (
	"errors"
	"net/http"
	"strings"

	"github.com/dblanc/hearth/internal/auth"
	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

type Handlers struct {
	Svc      *Service
	Auth     *auth.Service
	Renderer *render.Renderer
	Secure   bool
}

func NewHandlers(svc *Service, authSvc *auth.Service, r *render.Renderer, secure bool) *Handlers {
	return &Handlers{Svc: svc, Auth: authSvc, Renderer: r, Secure: secure}
}

// Mount registers profile routes. Routes requiring auth are wrapped at the
// router level — see cmd/server.
func (h *Handlers) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings/profile", h.editForm)
	mux.HandleFunc("POST /settings/profile", h.editSubmit)
	mux.HandleFunc("GET /settings/account", h.accountForm)
	mux.HandleFunc("POST /settings/account/delete", h.deleteAccount)
	mux.HandleFunc("GET /{username}", h.viewProfile)
}

type editForm struct {
	Profile *Profile
	Errors  auth.FieldErrors
	Saved   bool
}

func (h *Handlers) editForm(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	p, err := h.Svc.Get(r.Context(), u.ID)
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.Renderer.HTML(w, "profile_edit.html", editForm{Profile: p, Errors: auth.FieldErrors{}})
}

func (h *Handlers) editSubmit(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	in := UpdateInput{
		DisplayName: strings.TrimSpace(r.FormValue("display_name")),
		Bio:         strings.TrimSpace(r.FormValue("bio")),
		Pronouns:    strings.TrimSpace(r.FormValue("pronouns")),
	}
	errs := auth.FieldErrors{}
	if m := auth.ValidateDisplayName(in.DisplayName); m != "" {
		errs.Add("display_name", m)
	}
	if m := auth.ValidateBio(in.Bio); m != "" {
		errs.Add("bio", m)
	}
	if m := auth.ValidatePronouns(in.Pronouns); m != "" {
		errs.Add("pronouns", m)
	}
	if errs.Has() {
		p, _ := h.Svc.Get(r.Context(), u.ID)
		// Overlay the user's edits so they're not lost on re-render.
		if p != nil {
			p.DisplayName = in.DisplayName
			p.Bio = in.Bio
			p.Pronouns = in.Pronouns
		}
		h.Renderer.Status(w, http.StatusUnprocessableEntity, "profile_edit.html", editForm{Profile: p, Errors: errs})
		return
	}
	if err := h.Svc.Update(r.Context(), u.ID, in); err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	p, _ := h.Svc.Get(r.Context(), u.ID)
	h.Renderer.HTML(w, "profile_edit.html", editForm{Profile: p, Errors: auth.FieldErrors{}, Saved: true})
}

func (h *Handlers) accountForm(w http.ResponseWriter, _ *http.Request) {
	h.Renderer.HTML(w, "account.html", nil)
}

// deleteAccount requires the user to re-enter their password. On success the
// account is soft-deleted and the session is cleared.
func (h *Handlers) deleteAccount(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	password := r.FormValue("password")
	if password == "" {
		h.Renderer.Status(w, http.StatusUnprocessableEntity, "account.html", map[string]any{"Error": "Password is required to delete your account."})
		return
	}
	ok, err := h.Auth.CheckPassword(r.Context(), u.ID, password)
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	if !ok {
		h.Renderer.Status(w, http.StatusUnprocessableEntity, "account.html", map[string]any{"Error": "Password is incorrect."})
		return
	}
	if err := h.Svc.SoftDelete(r.Context(), u.ID); err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	auth.ClearSessionCookie(w, h.Secure)
	h.Renderer.HTML(w, "account_deleted.html", nil)
}

// viewProfile handles /{username}. In Phase 0 only the user's own profile is
// viewable; non-owners get a 404 (privacy by non-existence). Phase 1 extends
// this to connected viewers.
func (h *Handlers) viewProfile(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	// Reserved paths are routed before this; if we end up here with an
	// unknown user, return 404.
	p, err := h.Svc.GetByUsername(r.Context(), username)
	if errors.Is(err, ErrNotFound) {
		h.Renderer.Error(w, http.StatusNotFound)
		return
	}
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	u := middleware.UserFrom(r.Context())
	if u == nil || u.ID != p.ID {
		// Phase 0: privacy by 404 for non-owners.
		h.Renderer.Error(w, http.StatusNotFound)
		return
	}
	h.Renderer.HTML(w, "profile_view.html", map[string]any{"Profile": p, "Owner": true})
}
