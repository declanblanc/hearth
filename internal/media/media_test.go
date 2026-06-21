package media

import (
	"errors"
	"testing"
)

// ftypBox builds a minimal MP4-style "ftyp" box with the given brands. It's
// enough for http.DetectContentType's mp4 matcher, which keys on the box size,
// the "ftyp" tag, and a compatible brand beginning with "mp4".
func ftypBox(brands ...string) []byte {
	// 8-byte header (size + "ftyp") plus one 4-byte slot per brand.
	size := 8 + 4*len(brands)
	b := make([]byte, size)
	b[0] = byte(size >> 24)
	b[1] = byte(size >> 16)
	b[2] = byte(size >> 8)
	b[3] = byte(size)
	copy(b[4:8], "ftyp")
	for i, brand := range brands {
		copy(b[8+i*4:], brand)
	}
	return b
}

func TestDetectMediaType_AcceptsMP4(t *testing.T) {
	// Major brand "mp42", minor version slot, then compatible brands.
	data := ftypBox("mp42", "\x00\x00\x00\x00", "mp41", "isom")
	ct, ext, isVideo, err := DetectMediaType(data)
	if err != nil {
		t.Fatalf("DetectMediaType(mp4): unexpected error %v", err)
	}
	if ct != "video/mp4" || ext != ".mp4" || !isVideo {
		t.Fatalf("got ct=%q ext=%q isVideo=%v, want video/mp4 .mp4 true", ct, ext, isVideo)
	}
}

func TestDetectMediaType_RejectsHTMLPretendingToBeMP4(t *testing.T) {
	// HTML sniffs as text/html regardless of the filename a client claimed.
	data := []byte("<!DOCTYPE html><html><body>not a video</body></html>")
	if _, _, _, err := DetectMediaType(data); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("want ErrUnsupportedType for HTML, got %v", err)
	}
}

func TestDetectMediaType_AcceptsQuickTimeMOV(t *testing.T) {
	// An iPhone .mov carries the "qt  " major brand. Go's stdlib sniffer has no
	// QuickTime signature, so DetectMediaType detects it from the "ftyp" box.
	data := ftypBox("qt  ", "\x00\x00\x00\x00", "qt  ")
	ct, ext, isVideo, err := DetectMediaType(data)
	if err != nil {
		t.Fatalf("DetectMediaType(mov): unexpected error %v", err)
	}
	if ct != "video/quicktime" || ext != ".mov" || !isVideo {
		t.Fatalf("got ct=%q ext=%q isVideo=%v, want video/quicktime .mov true", ct, ext, isVideo)
	}
}

func TestDetectMediaType_AcceptsMOVWithCompatibleBrandOnly(t *testing.T) {
	// Some .mov files list "qt  " only among the compatible brands (after the
	// major brand and minor-version slots), with extra brands padding the box.
	// Detection must scan the whole brand list, not just the major-brand slot.
	data := ftypBox("isom", "\x00\x00\x00\x00", "isom", "iso2", "qt  ")
	ct, ext, isVideo, err := DetectMediaType(data)
	if err != nil {
		t.Fatalf("DetectMediaType(mov): unexpected error %v", err)
	}
	if ct != "video/quicktime" || ext != ".mov" || !isVideo {
		t.Fatalf("got ct=%q ext=%q isVideo=%v, want video/quicktime .mov true", ct, ext, isVideo)
	}
}

func TestDetectMediaType_RejectsUnsupportedVideoContainer(t *testing.T) {
	// A 3GP "ftyp" box matches neither the mp4 brand (stdlib) nor "qt  " (ours),
	// so an unsupported container is still refused — the allow-list is closed.
	data := ftypBox("3gp4", "\x00\x00\x00\x00", "3gp4")
	if _, _, _, err := DetectMediaType(data); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("want ErrUnsupportedType for 3gp, got %v", err)
	}
}

func TestDetectMediaType_AcceptsImages(t *testing.T) {
	// A PNG header still classifies as a (non-video) image through the combined
	// detector, so the post path treats photos exactly as before.
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	ct, ext, isVideo, err := DetectMediaType(png)
	if err != nil {
		t.Fatalf("DetectMediaType(png): unexpected error %v", err)
	}
	if ct != "image/png" || ext != ".png" || isVideo {
		t.Fatalf("got ct=%q ext=%q isVideo=%v, want image/png .png false", ct, ext, isVideo)
	}
}
