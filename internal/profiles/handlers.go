package profiles

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/dblanc/hearth/internal/auth"
	"github.com/dblanc/hearth/internal/connections"
	"github.com/dblanc/hearth/internal/media"
	"github.com/dblanc/hearth/internal/posts"
	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

type Handlers struct {
	Svc      *Service
	Auth     *auth.Service
	Conns    *connections.Service
	Posts    *posts.Service
	Renderer *render.Renderer
	Media    media.Store // may be nil in dev (photo uploads silently skipped)
	Secure   bool
}

func NewHandlers(svc *Service, authSvc *auth.Service, conns *connections.Service, postsSvc *posts.Service, r *render.Renderer, m media.Store, secure bool) *Handlers {
	return &Handlers{Svc: svc, Auth: authSvc, Conns: conns, Posts: postsSvc, Renderer: r, Media: m, Secure: secure}
}

func (h *Handlers) Mount(mux *http.ServeMux) {
	authed := func(fn http.HandlerFunc) http.Handler {
		return middleware.RequireAuth(http.HandlerFunc(fn))
	}
	mux.Handle("GET /settings/profile", authed(h.editForm))
	mux.Handle("POST /settings/profile", authed(h.editSubmit))
	mux.Handle("GET /settings/account", authed(h.accountForm))
	mux.Handle("POST /settings/account/delete", authed(h.deleteAccount))
	// /{username} stays public — the handler enforces 404-unless-connected.
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
	// URL-encoded forms (no photo). maxMemory is modest; a larger photo spills
	// to a temp file, and the real size limit is enforced by the LimitReader
	// below.
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		if err2 := r.ParseForm(); err2 != nil {
			h.Renderer.Error(w, http.StatusBadRequest)
			return
		}
	}

	in := UpdateInput{
		FirstName: strings.TrimSpace(r.FormValue("first_name")),
		LastName:  strings.TrimSpace(r.FormValue("last_name")),
		Bio:       strings.TrimSpace(r.FormValue("bio")),
		Pronouns:  strings.TrimSpace(r.FormValue("pronouns")),
	}
	errs := auth.FieldErrors{}
	if m := auth.ValidateFirstName(in.FirstName); m != "" {
		errs.Add("first_name", m)
	}
	if m := auth.ValidateLastName(in.LastName); m != "" {
		errs.Add("last_name", m)
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
				errs.Add("photo", "Photo must be 25 MB or smaller.")
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
			p.FirstName = in.FirstName
			p.LastName = in.LastName
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

// viewProfile handles /{username}. The profile is visible only to its owner or
// to a connected viewer; everyone else gets a 404 — non-existence is part of
// the privacy model, so we never reveal a profile exists (CLAUDE.md §1).
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
	owner := u != nil && u.ID == p.ID
	connected := false
	if u != nil && !owner {
		connected, err = h.Conns.IsConnected(r.Context(), u.ID, p.ID)
		if err != nil {
			h.Renderer.Error(w, http.StatusInternalServerError)
			return
		}
	}
	if !owner && !connected {
		// Privacy by 404 for anyone not connected to this user.
		h.Renderer.Error(w, http.StatusNotFound)
		return
	}

	authorPosts, err := h.Posts.ListByAuthor(r.Context(), p.ID)
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	// Owner or connected viewer confirmed above — only now mint signed image
	// URLs for this author's posts (CLAUDE.md §6).
	if err := posts.SignMediaURLs(r.Context(), h.Media, authorPosts); err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.Renderer.HTML(w, "profile_view.html", render.Page(u, render.M{
		"Profile":   p,
		"Owner":     owner,
		"Connected": connected,
		"Posts":     authorPosts,
		"PhotoURL":  h.photoURL(p.PhotoKey),
		"PostError": postError(r.URL.Query().Get("post_error")),
	}))
}

func (h *Handlers) photoURL(key string) string {
	if h.Media == nil || key == "" {
		return ""
	}
	return h.Media.URL(key)
}

func postError(code string) string {
	switch code {
	case "empty":
		return "Your post can't be empty."
	case "toolong":
		return "Your post is too long (1000 characters max)."
	case "too_many_images":
		return "A post can have at most 5 images."
	case "image_too_large":
		return "Each image must be 25 MB or smaller."
	case "image_type":
		return "Only JPEG, PNG, and WebP images are supported."
	case "image_unreadable":
		return "One of your images couldn't be read. Please try again."
	default:
		return ""
	}
}
