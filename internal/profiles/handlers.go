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
	"github.com/dblanc/hearth/internal/likes"
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
	Likes    *likes.Service
	Renderer *render.Renderer
	Media    media.Store // may be nil in dev (photo uploads silently skipped)
	Secure   bool
}

func NewHandlers(svc *Service, authSvc *auth.Service, conns *connections.Service, postsSvc *posts.Service, likeSvc *likes.Service, r *render.Renderer, m media.Store, secure bool) *Handlers {
	return &Handlers{Svc: svc, Auth: authSvc, Conns: conns, Posts: postsSvc, Likes: likeSvc, Renderer: r, Media: m, Secure: secure}
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
				errs.Add("photo", fmt.Sprintf("Photo must be %d MB or smaller.", media.MaxProfilePhotoSize/(1024*1024)))
			} else if _, _, err := media.DetectType(data); errors.Is(err, media.ErrUnsupportedType) {
				errs.Add("photo", "Only JPEG, PNG, and WebP photos are supported.")
			} else if err != nil {
				h.Renderer.Error(w, http.StatusInternalServerError)
				return
			} else {
				// Center-crop to a square and re-encode server-side, so the
				// stored object is always a known-good fixed-size JPEG — whether
				// or not the browser pre-cropped it (CLAUDE.md §6: media access
				// control and storage stay server-controlled).
				normalized, ct, ext, nerr := media.NormalizeProfilePhoto(data)
				if errors.Is(nerr, media.ErrUnreadableImage) {
					errs.Add("photo", "That photo couldn't be read. Please try another.")
				} else if nerr != nil {
					h.Renderer.Error(w, http.StatusInternalServerError)
					return
				} else {
					photoData = normalized
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
		// No such account. Serve the same descriptive page as the
		// "not connected" branch below so the two cases are byte-for-byte
		// identical — otherwise probing /{username} would leak whether an
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
		// A viewer who isn't connected may still legitimately need to see a
		// limited preview: when this profile's owner has sent *them* a pending
		// connection request, they should be able to weigh it before
		// accepting (issue #3). This is scoped strictly to that relationship —
		// PendingRequestFrom only matches status='pending' requests from this
		// exact owner to this exact viewer — so it never becomes a general
		// profile-view bypass. Anyone without that pending request falls through
		// to the identical privacy page below (CLAUDE.md §1).
		if u != nil {
			requestID, hasPending, err := h.Conns.PendingRequestFrom(r.Context(), u.ID, p.ID)
			if err != nil {
				h.Renderer.Error(w, http.StatusInternalServerError)
				return
			}
			if hasPending {
				h.renderProfilePreview(w, r, u, p, requestID)
				return
			}
		}
		// Privacy by 404 for anyone not connected to this user. Identical
		// output to the "no such account" branch above keeps existence hidden.
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
	// A connected viewer looking at someone else's posts can like them, so
	// reflect which they've already liked. The owner sees a liker list instead
	// of a like button, so their own like state is irrelevant here.
	if connected {
		if err := h.Likes.MarkViewerLikes(r.Context(), u.ID, authorPosts); err != nil {
			h.Renderer.Error(w, http.StatusInternalServerError)
			return
		}
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

// renderProfilePreview shows the limited, request-decision view of a profile to
// a viewer who has a pending incoming connection request from its owner (issue
// #3). It exposes only the public-facing profile fields (name, bio, pronouns,
// avatar) plus Accept/Decline controls — never the owner's posts.
//
// Crucially, this path mints no signed post-media URLs and never lists posts:
// the preview must not leak any connected-only content, and per CLAUDE.md §6 we
// only do media work after access to that content is actually granted (which,
// for posts, happens only once the connection is accepted). The Accept/Decline
// forms post to the same /requests/{id}/accept and /requests/{id}/deny
// endpoints used by the notifications/requests page, so the actions stay
// authoritative in one place.
func (h *Handlers) renderProfilePreview(w http.ResponseWriter, r *http.Request, viewer *middleware.User, p *Profile, requestID int64) {
	h.Renderer.HTML(w, "profile_preview.html", render.Page(viewer, render.M{
		"Profile":   p,
		"PhotoURL":  h.photoURL(p.PhotoKey),
		"RequestID": requestID,
	}))
}

// renderProfileUnavailable serves the descriptive "this profile isn't
// available" page with a 404 status. Both inaccessible cases — the username has
// no account, and the username exists but the viewer isn't connected — funnel
// through this single helper so their responses are byte-for-byte identical.
// That identity is the privacy property: it stops anyone from probing
// /{username} to learn whether an account exists (CLAUDE.md §1).
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
	case "images_too_large":
		return fmt.Sprintf("Your images can total at most %d MB.", media.MaxPostImagesTotalSize/(1024*1024))
	case "image_type":
		return "Only JPEG, PNG, and WebP images are supported."
	case "image_unreadable":
		return "One of your images couldn't be read. Please try again."
	default:
		return ""
	}
}
