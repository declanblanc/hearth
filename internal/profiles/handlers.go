package profiles

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/dblanc/hearth/internal/auth"
	"github.com/dblanc/hearth/internal/media"
	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

type Handlers struct {
	Svc      *Service
	Auth     *auth.Service
	Renderer *render.Renderer
	Media    media.Store // may be nil in dev (photo uploads silently skipped)
	Secure   bool
}

func NewHandlers(svc *Service, authSvc *auth.Service, r *render.Renderer, m media.Store, secure bool) *Handlers {
	return &Handlers{Svc: svc, Auth: authSvc, Renderer: r, Media: m, Secure: secure}
}

func (h *Handlers) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings/profile", h.editForm)
	mux.HandleFunc("POST /settings/profile", h.editSubmit)
	mux.HandleFunc("GET /settings/account", h.accountForm)
	mux.HandleFunc("POST /settings/account/delete", h.deleteAccount)
	mux.HandleFunc("GET /{username}", h.viewProfile)
}

func (h *Handlers) editForm(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	p, err := h.Svc.Get(r.Context(), u.ID)
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.Renderer.HTML(w, "profile_edit.html", render.Page(u, render.M{
		"Profile": p, "Errors": auth.FieldErrors{}, "PhotoURL": h.photoURL(p.PhotoKey),
	}))
}

func (h *Handlers) editSubmit(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())

	// ParseMultipartForm handles both multipart (photo present) and plain
	// URL-encoded forms (no photo). Max 6MB in memory — just above photo limit.
	if err := r.ParseMultipartForm(6 << 20); err != nil {
		if err2 := r.ParseForm(); err2 != nil {
			h.Renderer.Error(w, http.StatusBadRequest)
			return
		}
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

	// Validate photo if one was submitted.
	var (
		photoData []byte
		photoType string
		photoExt  string
	)
	if h.Media != nil {
		file, fh, ferr := r.FormFile("photo")
		if ferr == nil {
			defer file.Close()
			_ = fh
			data, err := io.ReadAll(io.LimitReader(file, media.MaxProfilePhotoSize+1))
			if err != nil {
				h.Renderer.Error(w, http.StatusInternalServerError)
				return
			}
			if int64(len(data)) > media.MaxProfilePhotoSize {
				errs.Add("photo", "Photo must be 5 MB or smaller.")
			} else {
				ct, ext, err := media.DetectType(data)
				if errors.Is(err, media.ErrUnsupportedType) {
					errs.Add("photo", "Only JPEG, PNG, and WebP photos are supported.")
				} else if err != nil {
					h.Renderer.Error(w, http.StatusInternalServerError)
					return
				} else {
					photoData = data
					photoType = ct
					photoExt = ext
				}
			}
		}
		// ErrMissingFile is normal — no photo submitted.
	}

	if errs.Has() {
		p, _ := h.Svc.Get(r.Context(), u.ID)
		if p != nil {
			p.DisplayName = in.DisplayName
			p.Bio = in.Bio
			p.Pronouns = in.Pronouns
		}
		h.Renderer.Status(w, http.StatusUnprocessableEntity, "profile_edit.html", render.Page(u, render.M{
			"Profile": p, "Errors": errs, "PhotoURL": h.photoURL(p.PhotoKey),
		}))
		return
	}

	if err := h.Svc.Update(r.Context(), u.ID, in); err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}

	if len(photoData) > 0 {
		key := media.NewProfileKey(photoExt)
		if err := h.Media.Upload(r.Context(), key, bytes.NewReader(photoData), photoType); err != nil {
			slog.Error("profile photo upload", "user_id", u.ID, "err", err)
			h.Renderer.Error(w, http.StatusInternalServerError)
			return
		}
		oldKey, err := h.Svc.UpdatePhoto(r.Context(), u.ID, key)
		if err != nil {
			_ = h.Media.Delete(r.Context(), key)
			h.Renderer.Error(w, http.StatusInternalServerError)
			return
		}
		if oldKey != "" {
			_ = h.Media.Delete(r.Context(), oldKey)
		}
	}

	p, _ := h.Svc.Get(r.Context(), u.ID)
	h.Renderer.HTML(w, "profile_edit.html", render.Page(u, render.M{
		"Profile": p, "Errors": auth.FieldErrors{}, "Saved": true, "PhotoURL": h.photoURL(p.PhotoKey),
	}))
}

func (h *Handlers) accountForm(w http.ResponseWriter, r *http.Request) {
	h.Renderer.HTML(w, "account.html", render.Page(middleware.UserFrom(r.Context()), nil))
}

func (h *Handlers) deleteAccount(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	password := r.FormValue("password")
	if password == "" {
		h.Renderer.Status(w, http.StatusUnprocessableEntity, "account.html", render.Page(u, render.M{
			"Error": "Password is required to delete your account.",
		}))
		return
	}
	ok, err := h.Auth.CheckPassword(r.Context(), u.ID, password)
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	if !ok {
		h.Renderer.Status(w, http.StatusUnprocessableEntity, "account.html", render.Page(u, render.M{
			"Error": "Password is incorrect.",
		}))
		return
	}
	if err := h.Svc.SoftDelete(r.Context(), u.ID); err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	auth.ClearSessionCookie(w, h.Secure)
	h.Renderer.HTML(w, "account_deleted.html", render.Page(nil, nil))
}

// viewProfile handles /{username}. In Phase 0 only the user's own profile is
// viewable; non-owners get a 404 (privacy by non-existence). Phase 1 extends
// this to connected viewers via IsConnected.
func (h *Handlers) viewProfile(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
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
	h.Renderer.HTML(w, "profile_view.html", render.Page(u, render.M{
		"Profile": p, "Owner": true, "PhotoURL": h.photoURL(p.PhotoKey),
	}))
}

func (h *Handlers) photoURL(key string) string {
	if h.Media == nil || key == "" {
		return ""
	}
	return h.Media.URL(key)
}
