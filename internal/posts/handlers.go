package posts

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/dblanc/hearth/internal/media"
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
	profileURL := "/" + u.Username

	// ParseMultipartForm covers both image-bearing posts (multipart) and
	// text-only posts submitted as multipart by the compose form. maxMemory is
	// modest; files larger than it spill to temp files, and per-file size is
	// enforced explicitly below.
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}

	images, errCode := h.collectImages(r)
	if errCode != "" {
		http.Redirect(w, r, profileURL+"?post_error="+errCode, http.StatusSeeOther)
		return
	}

	_, err := h.Svc.Create(r.Context(), u.ID, r.FormValue("content"), images)
	if err != nil {
		switch {
		case errors.Is(err, ErrEmpty):
			http.Redirect(w, r, profileURL+"?post_error=empty", http.StatusSeeOther)
		case errors.Is(err, ErrTooLong):
			http.Redirect(w, r, profileURL+"?post_error=toolong", http.StatusSeeOther)
		case errors.Is(err, ErrTooManyImages):
			http.Redirect(w, r, profileURL+"?post_error=too_many_images", http.StatusSeeOther)
		default:
			slog.Error("posts: create", "user_id", u.ID, "err", err)
			h.Renderer.Error(w, http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, profileURL, http.StatusSeeOther)
}

// collectImages reads, size-limits, and MIME-sniffs each uploaded "images"
// file. It returns the validated images or a post_error code suitable for the
// redirect-and-display pattern the profile page uses. The content type is
// derived by sniffing the bytes, never trusted from the client (Build Plan
// §2.1: "don't trust client-provided content-type").
func (h *Handlers) collectImages(r *http.Request) ([]NewImage, string) {
	if r.MultipartForm == nil {
		return nil, ""
	}
	files := r.MultipartForm.File["images"]
	if len(files) == 0 {
		return nil, ""
	}
	if len(files) > media.MaxImagesPerPost {
		return nil, "too_many_images"
	}

	images := make([]NewImage, 0, len(files))
	for _, fh := range files {
		file, err := fh.Open()
		if err != nil {
			return nil, "image_unreadable"
		}
		// Read one byte past the limit so we can tell "exactly at limit" from
		// "over limit".
		data, err := io.ReadAll(io.LimitReader(file, media.MaxImageSize+1))
		file.Close()
		if err != nil {
			return nil, "image_unreadable"
		}
		if int64(len(data)) > media.MaxImageSize {
			return nil, "image_too_large"
		}
		ct, ext, err := media.DetectType(data)
		if err != nil {
			return nil, "image_type"
		}
		images = append(images, NewImage{Data: data, ContentType: ct, Ext: ext})
	}
	return images, ""
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
