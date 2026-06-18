package posts

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dblanc/hearth/internal/media"
	"github.com/dblanc/hearth/internal/shared/middleware"
)

// smallJPEG returns the bytes of a tiny valid JPEG so DetectType accepts it.
func smallJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		panic("encode test jpeg: " + err.Error())
	}
	return buf.Bytes()
}

// smallMP4 returns the bytes of a minimal MP4 "ftyp" box so DetectMediaType
// classifies it as video/mp4. It carries no actual video stream — collectMedia
// only sniffs the type and size, never decodes.
func smallMP4() []byte {
	b := []byte{0x00, 0x00, 0x00, 0x18} // box size = 24
	b = append(b, "ftyp"...)
	b = append(b, "mp42"...)
	b = append(b, 0x00, 0x00, 0x00, 0x00) // minor version
	b = append(b, "mp41"...)
	b = append(b, "isom"...)
	return b
}

// filePart is one multipart file field for multipartRequest.
type filePart struct {
	field    string
	filename string
	content  []byte
}

// imagesRequest builds a parsed multipart request carrying each entry of
// contents as a separate "images" file upload, ready for collectMedia.
func imagesRequest(t *testing.T, contents [][]byte) *http.Request {
	t.Helper()
	parts := make([]filePart, len(contents))
	for i, content := range contents {
		parts[i] = filePart{field: "images", filename: "img" + string(rune('a'+i)) + ".jpg", content: content}
	}
	return multipartRequest(t, parts)
}

// multipartRequest builds a parsed multipart POST /posts from arbitrary file
// fields, so tests can mix "images" and a "video_poster" the way the compose
// form does.
func multipartRequest(t *testing.T, parts []filePart) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, p := range parts {
		fw, err := w.CreateFormFile(p.field, p.filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := fw.Write(p.content); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/posts", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	// Match the production handler's parse budget so files spill to temp the
	// same way they would in a real request.
	if err := req.ParseMultipartForm(8 << 20); err != nil {
		t.Fatalf("ParseMultipartForm: %v", err)
	}
	return req
}

func TestCollectMedia_MultipleSmallImagesAccepted(t *testing.T) {
	h := &Handlers{}
	req := imagesRequest(t, [][]byte{smallJPEG(), smallJPEG(), smallJPEG()})

	images, code := h.collectMedia(req)
	if code != "" {
		t.Fatalf("unexpected error code: %q", code)
	}
	if len(images) != 3 {
		t.Fatalf("want 3 images, got %d", len(images))
	}
}

// TestCollectMedia_PerFileImageCap proves each image is capped individually
// (post client-side compression). A single file just over the per-file limit is
// rejected; its bytes open with a real JPEG header so the size check, not the
// type check, is what rejects it.
func TestCollectMedia_PerFileImageCap(t *testing.T) {
	oversize := make([]byte, media.MaxPostImageFileSize+1)
	copy(oversize, smallJPEG())

	req := imagesRequest(t, [][]byte{oversize})
	h := &Handlers{}

	images, code := h.collectMedia(req)
	if code != "image_too_large" {
		t.Fatalf("want code image_too_large, got %q (images=%d)", code, len(images))
	}
}

func TestCollectMedia_TooManyImages(t *testing.T) {
	contents := make([][]byte, media.MaxImagesPerPost+1)
	for i := range contents {
		contents[i] = smallJPEG()
	}
	req := imagesRequest(t, contents)
	h := &Handlers{}

	if _, code := h.collectMedia(req); code != "too_many_images" {
		t.Fatalf("want code too_many_images, got %q", code)
	}
}

// TestCollectMedia_VideoWithPoster proves a video and its poster come through as
// a single video attachment whose display box is taken from the poster image's
// dimensions (8×8 here), never from a client-supplied number.
func TestCollectMedia_VideoWithPoster(t *testing.T) {
	req := multipartRequest(t, []filePart{
		{field: "images", filename: "clip.mp4", content: smallMP4()},
		{field: "video_poster", filename: "poster.jpg", content: smallJPEG()},
	})
	h := &Handlers{}

	items, code := h.collectMedia(req)
	if code != "" {
		t.Fatalf("unexpected error code: %q", code)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 attachment, got %d", len(items))
	}
	m := items[0]
	if !m.IsVideo() || m.ContentType != "video/mp4" || m.Ext != ".mp4" {
		t.Fatalf("attachment is not the mp4: %+v", m)
	}
	if len(m.PosterData) == 0 || m.PosterExt != ".jpg" {
		t.Fatalf("poster not attached: %+v", m)
	}
	if m.Width != 8 || m.Height != 8 {
		t.Fatalf("video box should come from the poster (8×8), got %dx%d", m.Width, m.Height)
	}
}

// A video without a poster (the no-JS path) is still accepted; it just stores no
// poster and renders without one.
func TestCollectMedia_VideoWithoutPosterAccepted(t *testing.T) {
	req := multipartRequest(t, []filePart{
		{field: "images", filename: "clip.mp4", content: smallMP4()},
	})
	h := &Handlers{}

	items, code := h.collectMedia(req)
	if code != "" {
		t.Fatalf("unexpected error code: %q", code)
	}
	if len(items) != 1 || !items[0].IsVideo() || len(items[0].PosterData) != 0 {
		t.Fatalf("want one poster-less video, got %+v", items)
	}
}

// Photos and a video may share a post. collectMedia returns both, with the
// poster attached to the video.
func TestCollectMedia_PhotosAndVideoTogether(t *testing.T) {
	req := multipartRequest(t, []filePart{
		{field: "images", filename: "a.jpg", content: smallJPEG()},
		{field: "images", filename: "b.jpg", content: smallJPEG()},
		{field: "images", filename: "clip.mp4", content: smallMP4()},
		{field: "video_poster", filename: "poster.jpg", content: smallJPEG()},
	})
	h := &Handlers{}

	items, code := h.collectMedia(req)
	if code != "" {
		t.Fatalf("unexpected error code: %q", code)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 attachments, got %d", len(items))
	}
	var images, videos int
	for _, m := range items {
		if m.IsVideo() {
			videos++
			if len(m.PosterData) == 0 {
				t.Errorf("video should have its poster attached")
			}
		} else {
			images++
		}
	}
	if images != 2 || videos != 1 {
		t.Fatalf("want 2 images + 1 video, got %d images + %d videos", images, videos)
	}
}

func TestCollectMedia_TooManyVideosRejected(t *testing.T) {
	req := multipartRequest(t, []filePart{
		{field: "images", filename: "one.mp4", content: smallMP4()},
		{field: "images", filename: "two.mp4", content: smallMP4()},
	})
	h := &Handlers{}

	if _, code := h.collectMedia(req); code != "too_many_videos" {
		t.Fatalf("want code too_many_videos, got %q", code)
	}
}

// contentRequest builds a multipart POST /posts carrying a single text "content"
// field, the way the compose form submits a text-only post.
func contentRequest(t *testing.T, content string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("content", content); err != nil {
		t.Fatalf("WriteField: %v", err)
	}
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/posts", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// Creating a post must redirect to the author's profile under the /u/ prefix.
// A bare "/" + username would 404 now that profiles live at /u/{username}
// (regression guard for the post-create redirect).
func TestCreate_RedirectsToPrefixedProfile(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	author := seedUser(t, d, "author")
	// Renderer is nil: the success path redirects and never renders.
	h := NewHandlers(svc, nil)

	req := contentRequest(t, "hello world")
	u := &middleware.User{ID: author, Username: "author", DisplayName: "Author", Verified: true}
	req = req.WithContext(middleware.WithUser(req.Context(), u))

	rec := httptest.NewRecorder()
	h.create(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /posts: status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/u/author" {
		t.Errorf("redirect Location = %q, want %q", got, "/u/author")
	}
}
