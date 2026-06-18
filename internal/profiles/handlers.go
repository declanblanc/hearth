package profiles

import (
	"bytes"
	"errors"
	"fmt"
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
	Comments *posts.CommentService
	Renderer *render.Renderer
	Media    media.Store // may be nil in dev (photo uploads silently skipped)
	Secure   bool
}

func NewHandlers(svc *Service, authSvc *auth.Service, conns *connections.Service, postsSvc *posts.Service, commentSvc *posts.CommentService, r *render.Renderer, m media.Store, secure bool) *Handlers {
	return &Handlers{Svc: svc, Auth: authSvc, Conns: conns, Posts: postsSvc, Comments: commentSvc, Renderer: r, Media: m, Secure: secure}
}

func (h *Handlers) Mount(mux *http.ServeMux) {
	authed := func(fn http.HandlerFunc) http.Handler {
		return middleware.RequireAuth(http.HandlerFunc(fn))
	}
	mux.Handle("GET /settings", authed(h.settingsForm))
	mux.Handle("POST /settings", authed(h.settingsSubmit))
	mux.Handle("GET /settings/profile", authed(h.editForm))
	mux.Handle("POST /settings/profile", authed(h.editSubmit))
	mux.Handle("GET /settings/account", authed(h.accountForm))
	mux.Handle("POST /settings/account/delete", authed(h.deleteAccount))
	// /u/{username} stays public — the handler enforces 404-unless-connected.
	// The /u/ prefix keeps usernames in their own namespace, clear of the app's
	// top-level pages (/connections, /settings, …) so the two can't collide.
	mux.HandleFunc("GET /u/{username}", h.viewProfile)
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
		file, _, ferr := r.FormFile("photo")
		if ferr == nil {
			defer file.Close()
			data, err := io.ReadAll(io.LimitReader(file, media.MaxProfilePhotoSize+1))
			if err != nil {
				h.Renderer.Error(w, http.StatusInternalServerError)
				return
			}
			// One shared chokepoint validates, center-crops, and re-encodes the
			// upload server-side, so the stored object is always a known-good
			// fixed-size JPEG whether or not the browser pre-cropped it (CLAUDE.md
			// §6). A non-empty userMsg is a fixable problem shown inline.
			normalized, ct, ext, userMsg, perr := media.ProcessProfilePhoto(data)
			if perr != nil {
				h.Renderer.Error(w, http.StatusInternalServerError)
				return
			}
			if userMsg != "" {
				errs.Add("photo", userMsg)
			} else {
				photoData = normalized
				photoType = ct
				photoExt = ext
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

// settingsForm renders the main settings hub: the comment-visibility (privacy)
// toggles, links to the other settings pages, and the log-out action.
func (h *Handlers) settingsForm(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	v, err := h.Svc.GetCommentVisibility(r.Context(), u.ID)
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.Renderer.HTML(w, "settings.html", render.Page(u, render.M{"Visibility": v}))
}

// settingsSubmit saves the comment-visibility toggles from the settings hub and
// re-renders it with a confirmation banner.
func (h *Handlers) settingsSubmit(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	// Unchecked checkboxes are simply absent from the form, so "on" means enabled
	// and anything else (including missing) means disabled.
	v := CommentVisibility{
		ShareWithNonConnections:   r.FormValue("share_comments_with_non_connections") == "on",
		ShowNonConnectionComments: r.FormValue("show_non_connection_comments") == "on",
	}
	if err := h.Svc.UpdateCommentVisibility(r.Context(), u.ID, v); err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.Renderer.HTML(w, "settings.html", render.Page(u, render.M{"Visibility": v, "Saved": true}))
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

// viewProfile handles /u/{username}. The profile is visible only to its owner or
// to a connected viewer; everyone else gets a 404 — non-existence is part of
// the privacy model, so we never reveal a profile exists (CLAUDE.md §1).
func (h *Handlers) viewProfile(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	p, err := h.Svc.GetByUsername(r.Context(), username)
	if errors.Is(err, ErrNotFound) {
		// No such account. Serve the same descriptive page as the
		// "not connected" branch below so the two cases are byte-for-byte
		// identical — otherwise probing /u/{username} would leak whether an
		// account exists (CLAUDE.md §1).
		h.renderProfileUnavailable(w, r)
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
		// Privacy by 404 for anyone not connected to this user. Identical
		// output to the "no such account" branch above keeps existence hidden
		// (CLAUDE.md §1). There is no longer a pending-request preview: accepting
		// an invite connects the two users immediately, so a viewer is either
		// connected (and sees the full profile) or not (and sees this page).
		h.renderProfileUnavailable(w, r)
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
	// Attach comment threads for rendering. Access was settled above (owner or
	// connected viewer), so this needs no further connection check.
	if err := h.Comments.AttachToPosts(r.Context(), u.ID, authorPosts); err != nil {
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

// renderProfileUnavailable serves the descriptive "this profile isn't
// available" page with a 404 status. Both inaccessible cases — the username has
// no account, and the username exists but the viewer isn't connected — funnel
// through this single helper so their responses are byte-for-byte identical.
// That identity is the privacy property: it stops anyone from probing
// /u/{username} to learn whether an account exists (CLAUDE.md §1).
//
// The page data deliberately depends only on the viewer (for the nav/footer
// chrome), never on the requested profile, so nothing about the target account
// can leak into the rendered bytes.
func (h *Handlers) renderProfileUnavailable(w http.ResponseWriter, r *http.Request) {
	viewer := middleware.UserFrom(r.Context())
	h.Renderer.Status(w, http.StatusNotFound, "profile_unavailable.html", render.Page(viewer, nil))
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
	case "too_many_videos":
		return "A post can have at most one video."
	case "image_too_large":
		return fmt.Sprintf("Each image must be %d MB or smaller.", media.MaxPostImageFileSize/(1024*1024))
	case "video_too_large":
		return fmt.Sprintf("A video must be %d MB or smaller.", media.MaxVideoFileSize/(1024*1024))
	case "image_type":
		return "Only JPEG, PNG, and WebP images, and MP4 video, are supported."
	case "image_unreadable":
		return "One of your attachments couldn't be read. Please try again."
	default:
		return ""
	}
}
