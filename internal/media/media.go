// Package media defines the Store interface for object storage (R2 in production)
// and provides helpers for key generation and image type detection.
package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrUnsupportedType = errors.New("media: unsupported image type")
	ErrTooLarge        = errors.New("media: file too large")
)

// MaxProfilePhotoSize is the server-enforced upload limit for a single profile
// photo.
const MaxProfilePhotoSize int64 = 200 * 1024 * 1024

// MaxPostImageFileSize caps a single post image *after* the client-side
// compression that every upload passes through (Technical Plan §4.5: max 1600px
// long edge, 80% JPEG, typically 100–400 KB). It is a generous guardrail, not the
// compressor — a file this large means the client compression was skipped or
// failed, and we'd rather reject it than re-serve a multi-megabyte original.
const MaxPostImageFileSize int64 = 5 * 1024 * 1024

// MaxVideoFileSize caps a single post video. We don't transcode server-side
// (cost constraint), so this is the authoritative size limit; the client also
// enforces a duration cap, but size is the one we can verify.
const MaxVideoFileSize int64 = 50 * 1024 * 1024

// MaxImagesPerPost caps how many images a single post may carry.
const MaxImagesPerPost = 5

// MaxVideosPerPost caps how many videos a single post may carry. Independent of
// MaxImagesPerPost: a post may hold up to MaxImagesPerPost photos and up to
// MaxVideosPerPost videos, in any combination.
const MaxVideosPerPost = 1

// allowedImageTypes maps accepted MIME types to canonical file extensions.
var allowedImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// allowedVideoTypes maps accepted video MIME types to canonical file extensions.
// MP4 and QuickTime/MOV: we still do no server-side transcoding (the hosting
// budget rules out ffmpeg), so the container is stored as uploaded. MOV is
// accepted because it's the iPhone camera's native format; note that an HEVC
// stream inside it plays in Safari but not in every browser — see Technical
// Plan §4.5. Codecs we can neither store playably nor convert remain rejected.
var allowedVideoTypes = map[string]string{
	"video/mp4":       ".mp4",
	"video/quicktime": ".mov",
}

// Store is the object storage abstraction. R2Store is the production
// implementation; Stub is used in tests and dev.
type Store interface {
	Upload(ctx context.Context, key string, r io.Reader, contentType string) error
	Delete(ctx context.Context, key string) error
	// URL returns a public URL for key. Returns "" if the store is unconfigured.
	URL(key string) string
	// PresignGet returns a short-lived signed GET URL for key, valid for ttl.
	// Used for post media, which is private: the URL is minted at render time
	// only after the viewer's connection to the author is confirmed (CLAUDE.md §6).
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// NewProfileKey returns a unique object key for a profile photo.
// Keys intentionally do not embed user_id (architectural rule from CLAUDE.md §6).
func NewProfileKey(ext string) string {
	return fmt.Sprintf("profile/%s%s", uuid.New().String(), ext)
}

// NewPostKey returns a unique object key for a post image, namespaced by the
// current year and month for browsability. Like profile keys, it embeds neither
// user_id nor post_id (CLAUDE.md §6).
func NewPostKey(now time.Time, ext string) string {
	return fmt.Sprintf("posts/%04d/%02d/%s%s", now.Year(), now.Month(), uuid.New().String(), ext)
}

// DetectType sniffs the content type from the leading bytes of data (up to 512)
// and returns the normalised MIME type and file extension. It accepts images
// only — the chokepoint for profile photos and video posters, where a video is
// never valid.
// Returns ErrUnsupportedType for anything not in the image allow-list.
func DetectType(data []byte) (ct string, ext string, err error) {
	raw := http.DetectContentType(data)
	for mime, e := range allowedImageTypes {
		if strings.HasPrefix(raw, mime) {
			return mime, e, nil
		}
	}
	return "", "", ErrUnsupportedType
}

// DetectMediaType sniffs the content type from the leading bytes of data (up to
// 512) and classifies it as an image or a video, returning the normalised MIME
// type, canonical extension, and whether it's a video. Used by the post-upload
// path, which accepts both. Returns ErrUnsupportedType for anything outside both
// allow-lists.
func DetectMediaType(data []byte) (ct string, ext string, isVideo bool, err error) {
	raw := http.DetectContentType(data)
	for mime, e := range allowedImageTypes {
		if strings.HasPrefix(raw, mime) {
			return mime, e, false, nil
		}
	}
	for mime, e := range allowedVideoTypes {
		if strings.HasPrefix(raw, mime) {
			return mime, e, true, nil
		}
	}
	// Go's content sniffer recognises the MP4 "ftyp" brand but has no signature
	// for QuickTime/MOV, so it reports those as application/octet-stream. Detect
	// MOV ourselves from its "ftyp" box; iPhone videos are commonly .mov.
	if sniffQuickTime(data) {
		return "video/quicktime", allowedVideoTypes["video/quicktime"], true, nil
	}
	return "", "", false, ErrUnsupportedType
}

var (
	ftypTag        = []byte("ftyp")
	quickTimeBrand = []byte("qt  ")
)

// sniffQuickTime reports whether data begins with a QuickTime/MOV "ftyp" box.
// The structure mirrors the WHATWG mp4 signature that Go's stdlib sniffer uses
// (https://mimesniff.spec.whatwg.org/#signature-for-mp4): a big-endian box size,
// the "ftyp" tag, a major brand, a 4-byte minor version, then zero or more
// compatible brands. We match the "qt  " brand in the major slot or any
// compatible slot — the field stdlib's mp4 matcher checks for "mp4" instead.
func sniffQuickTime(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	boxSize := int(binary.BigEndian.Uint32(data[:4]))
	if boxSize%4 != 0 || len(data) < boxSize {
		return false
	}
	if !bytes.Equal(data[4:8], ftypTag) {
		return false
	}
	for offset := 8; offset+4 <= boxSize; offset += 4 {
		if offset == 12 {
			continue // skip the minor-version field between major and compatible brands
		}
		if bytes.Equal(data[offset:offset+4], quickTimeBrand) {
			return true
		}
	}
	return false
}
