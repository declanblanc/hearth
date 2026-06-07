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

// imagesRequest builds a parsed multipart request carrying each entry of
// contents as a separate "images" file upload, ready for collectImages.
func imagesRequest(t *testing.T, contents [][]byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for i, content := range contents {
		fw, err := w.CreateFormFile("images", "img"+string(rune('a'+i))+".jpg")
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := fw.Write(content); err != nil {
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

func TestCollectImages_MultipleSmallImagesAccepted(t *testing.T) {
	h := &Handlers{}
	req := imagesRequest(t, [][]byte{smallJPEG(), smallJPEG(), smallJPEG()})

	images, code := h.collectImages(req)
	if code != "" {
		t.Fatalf("unexpected error code: %q", code)
	}
	if len(images) != 3 {
		t.Fatalf("want 3 images, got %d", len(images))
	}
}

// TestCollectImages_TotalSizeBudget proves the 200 MB cap is a *total* across
// all files, not a per-file limit: two files that are each well under the full
// budget but together exceed it are rejected.
func TestCollectImages_TotalSizeBudget(t *testing.T) {
	// One slice reused for both files keeps the test's memory footprint to a
	// single large allocation. Half the budget plus one byte each → just over
	// the total when combined; each file alone stays under the full budget.
	// The chunk opens with a real JPEG header so content-type sniffing passes
	// and the size check (not the type check) is what rejects the second file.
	halfPlusOne := media.MaxPostImagesTotalSize/2 + 1
	chunk := make([]byte, halfPlusOne)
	copy(chunk, smallJPEG())

	req := imagesRequest(t, [][]byte{chunk, chunk})
	h := &Handlers{}

	images, code := h.collectImages(req)
	if code != "images_too_large" {
		t.Fatalf("want code images_too_large, got %q (images=%d)", code, len(images))
	}
}

func TestCollectImages_TooManyImages(t *testing.T) {
	contents := make([][]byte, media.MaxImagesPerPost+1)
	for i := range contents {
		contents[i] = smallJPEG()
	}
	req := imagesRequest(t, contents)
	h := &Handlers{}

	if _, code := h.collectImages(req); code != "too_many_images" {
		t.Fatalf("want code too_many_images, got %q", code)
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
