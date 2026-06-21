package posts

import (
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
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
	// Profiles live under the /u/ prefix (see profiles.Handlers.Mount); a bare
	// "/" + username would 404 now that usernames have their own namespace.
	profileURL := "/u/" + u.Username

	// ParseMultipartForm covers both image-bearing posts (multipart) and
	// text-only posts submitted as multipart by the compose form. maxMemory is
	// modest; files larger than it spill to temp files, and per-file size is
	// enforced explicitly below.
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}

	items, errCode, errDetail := h.collectMedia(r)
	if errCode != "" {
		redirect := profileURL + "?post_error=" + errCode
		if errDetail != "" {
			// Carry the offending file's extension so the message can name it
			// (e.g. "This file is .mkv"). It's reflected into the page, so the
			// display side validates it before showing it.
			redirect += "&post_error_detail=" + url.QueryEscape(errDetail)
		}
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	_, err := h.Svc.Create(r.Context(), u.ID, r.FormValue("content"), items)
	if err != nil {
		switch {
		case errors.Is(err, ErrEmpty):
			http.Redirect(w, r, profileURL+"?post_error=empty", http.StatusSeeOther)
		case errors.Is(err, ErrTooLong):
			http.Redirect(w, r, profileURL+"?post_error=toolong", http.StatusSeeOther)
		case errors.Is(err, ErrTooManyImages):
			http.Redirect(w, r, profileURL+"?post_error=too_many_images", http.StatusSeeOther)
		case errors.Is(err, ErrTooManyVideos):
			http.Redirect(w, r, profileURL+"?post_error=too_many_videos", http.StatusSeeOther)
		default:
			slog.Error("posts: create", "user_id", u.ID, "err", err)
			h.Renderer.Error(w, http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, profileURL, http.StatusSeeOther)
}

// collectMedia reads, size-limits, and MIME-sniffs each uploaded "images" file
// (the field carries both photos and a video). It returns the validated
// attachments or a post_error code suitable for the redirect-and-display pattern
// the profile page uses. Content types are derived by sniffing the bytes, never
// trusted from the client (Build Plan §2.1: "don't trust client-provided
// content-type").
//
// A post may carry up to MaxImagesPerPost images and up to MaxVideosPerPost
// videos, in any combination.
//
// On failure it returns a post_error code and an optional detail string. For an
// unsupported type the detail is the offending file's extension (e.g. ".mkv"),
// so the message can name what was rejected.
func (h *Handlers) collectMedia(r *http.Request) (items []NewMedia, errCode, detail string) {
	if r.MultipartForm == nil {
		return nil, "", ""
	}
	files := r.MultipartForm.File["images"]
	if len(files) == 0 {
		return nil, "", ""
	}

	items = make([]NewMedia, 0, len(files))
	var images, videos int
	for _, fh := range files {
		data, ct, ext, isVideo, code := readMediaFile(fh)
		if code != "" {
			// Name the rejected file by its extension when the type is the problem.
			if code == "image_type" {
				return nil, code, filepath.Ext(fh.Filename)
			}
			return nil, code, ""
		}
		if isVideo {
			videos++
			// Width/height for a video come from its poster (attached below), so
			// the player's box is reserved without trusting client dimensions.
			items = append(items, NewMedia{Data: data, ContentType: ct, Ext: ext})
			continue
		}
		width, height, err := media.ImageDimensions(data)
		if err != nil {
			return nil, "image_unreadable", ""
		}
		images++
		items = append(items, NewMedia{Data: data, ContentType: ct, Ext: ext, Width: width, Height: height})
	}

	if images > media.MaxImagesPerPost {
		return nil, "too_many_images", ""
	}
	if videos > media.MaxVideosPerPost {
		return nil, "too_many_videos", ""
	}

	// Attach the client-extracted poster frame to the video. Without JavaScript
	// no poster is sent and the video is stored without one (it still plays).
	if videos == 1 {
		if code := attachPoster(r, items); code != "" {
			return nil, code, ""
		}
	}

	return items, "", ""
}

// readMediaFile reads one uploaded file, sniffing its type from the leading bytes
// so the right size cap applies — images and videos have very different limits —
// without buffering an oversized file in full. It returns the bytes and the
// sniffed type, or a post_error code.
func readMediaFile(fh *multipart.FileHeader) (data []byte, ct, ext string, isVideo bool, errCode string) {
	file, err := fh.Open()
	if err != nil {
		return nil, "", "", false, "image_unreadable"
	}
	defer file.Close()

	// Sniff from a 512-byte prefix (all http.DetectContentType ever inspects) so
	// we know which cap to enforce before reading the rest.
	header := make([]byte, 512)
	n, err := io.ReadFull(file, header)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, "", "", false, "image_unreadable"
	}
	header = header[:n]

	ct, ext, isVideo, derr := media.DetectMediaType(header)
	if derr != nil {
		return nil, "", "", false, "image_type"
	}

	limit := media.MaxPostImageFileSize
	if isVideo {
		limit = media.MaxVideoFileSize
	}
	// Read one byte past the cap (minus the header already consumed) so we can
	// tell "exactly at the limit" from "over the limit".
	rest, err := io.ReadAll(io.LimitReader(file, limit-int64(n)+1))
	if err != nil {
		return nil, "", "", false, "image_unreadable"
	}
	data = append(header, rest...)
	if int64(len(data)) > limit {
		if isVideo {
			return nil, "", "", false, "video_too_large"
		}
		return nil, "", "", false, "image_too_large"
	}
	return data, ct, ext, isVideo, ""
}

// attachPoster reads the optional "video_poster" file — a JPEG still frame the
// client extracted from the chosen video — and attaches it to the post's video,
// using the poster's own pixel dimensions as the video's display box. Returns a
// post_error code if a poster was sent but isn't a readable image.
func attachPoster(r *http.Request, items []NewMedia) string {
	posters := r.MultipartForm.File["video_poster"]
	if len(posters) == 0 {
		return ""
	}
	data, _, ext, isVideo, errCode := readMediaFile(posters[0])
	if errCode != "" {
		return errCode
	}
	if isVideo {
		return "image_type" // a poster must be an image
	}
	width, height, err := media.ImageDimensions(data)
	if err != nil {
		return "image_unreadable"
	}
	for i := range items {
		if items[i].IsVideo() {
			items[i].PosterData = data
			items[i].PosterExt = ext
			items[i].Width = width
			items[i].Height = height
			break
		}
	}
	return ""
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
