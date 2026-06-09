package media

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// encodePNG renders a w×h image filled with fill and returns the PNG bytes.
func encodePNG(t *testing.T, w, h int, fill color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, fill)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestNormalizeProfilePhotoProducesSquareJPEG(t *testing.T) {
	// A wide, non-square source — the case that used to render stretched.
	src := encodePNG(t, 800, 300, color.RGBA{R: 200, G: 100, B: 50, A: 255})

	out, ct, ext, err := NormalizeProfilePhoto(src)
	if err != nil {
		t.Fatalf("NormalizeProfilePhoto: %v", err)
	}
	if ct != "image/jpeg" || ext != ".jpg" {
		t.Fatalf("got type %q ext %q, want image/jpeg .jpg", ct, ext)
	}

	decoded, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	b := decoded.Bounds()
	if b.Dx() != ProfilePhotoSize || b.Dy() != ProfilePhotoSize {
		t.Fatalf("output is %dx%d, want %dx%d square", b.Dx(), b.Dy(), ProfilePhotoSize, ProfilePhotoSize)
	}
}

func TestNormalizeProfilePhotoSquareInputUnchangedSize(t *testing.T) {
	src := encodePNG(t, 1000, 1000, color.RGBA{R: 10, G: 200, B: 90, A: 255})

	out, _, _, err := NormalizeProfilePhoto(src)
	if err != nil {
		t.Fatalf("NormalizeProfilePhoto: %v", err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if b := decoded.Bounds(); b.Dx() != ProfilePhotoSize || b.Dy() != ProfilePhotoSize {
		t.Fatalf("output is %dx%d, want %dx%d", b.Dx(), b.Dy(), ProfilePhotoSize, ProfilePhotoSize)
	}
}

func TestNormalizeProfilePhotoRejectsGarbage(t *testing.T) {
	_, _, _, err := NormalizeProfilePhoto([]byte("not an image"))
	if err != ErrUnreadableImage {
		t.Fatalf("got %v, want ErrUnreadableImage", err)
	}
}

func TestProcessProfilePhotoAcceptsValidImage(t *testing.T) {
	src := encodePNG(t, 600, 400, color.RGBA{R: 30, G: 60, B: 90, A: 255})

	out, ct, ext, userMsg, err := ProcessProfilePhoto(src)
	if err != nil {
		t.Fatalf("ProcessProfilePhoto: %v", err)
	}
	if userMsg != "" {
		t.Fatalf("valid image rejected: %q", userMsg)
	}
	if ct != "image/jpeg" || ext != ".jpg" || len(out) == 0 {
		t.Fatalf("got type %q ext %q len %d, want a non-empty image/jpeg .jpg", ct, ext, len(out))
	}
}

func TestProcessProfilePhotoReportsFixableProblems(t *testing.T) {
	// An unsupported type and undecodable data both come back as a user-facing
	// message with no error, so callers can show them inline rather than 500.
	if _, _, _, msg, err := ProcessProfilePhoto([]byte("GIF89a not allowed")); err != nil || msg == "" {
		t.Errorf("unsupported type: got msg %q err %v, want a message and no error", msg, err)
	}

	// PNG-typed but truncated bytes: passes type sniffing, fails to decode.
	valid := encodePNG(t, 40, 40, color.RGBA{A: 255})
	truncated := valid[:len(valid)/2]
	if _, _, _, msg, err := ProcessProfilePhoto(truncated); err != nil || msg == "" {
		t.Errorf("unreadable image: got msg %q err %v, want a message and no error", msg, err)
	}
}
