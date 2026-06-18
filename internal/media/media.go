// Package media defines the Store interface for object storage (R2 in production)
// and provides helpers for key generation and image type detection.
package media

import (
	"context"
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
// MP4 only: we can't transcode within the hosting budget, so anything else (e.g.
// an iPhone .mov/HEVC) is rejected rather than stored as an unplayable file.
var allowedVideoTypes = map[string]string{
	"video/mp4": ".mp4",
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
	return "", "", false, ErrUnsupportedType
}
