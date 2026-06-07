// Package media defines the Store interface for object storage (R2 in production)
// and provides helpers for key generation and image type detection.
package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

var (
	ErrUnsupportedType = errors.New("media: unsupported image type")
	ErrTooLarge        = errors.New("media: file too large")
)

// MaxImageSize is the server-enforced upload limit for a single image, applied
// to both profile photos and post images.
const MaxImageSize int64 = 200 * 1024 * 1024

// MaxProfilePhotoSize is retained as an alias for MaxImageSize so existing
// profile-photo call sites read clearly.
const MaxProfilePhotoSize int64 = MaxImageSize

// MaxImagesPerPost caps how many images a single post may carry.
const MaxImagesPerPost = 5

// allowedImageTypes maps accepted MIME types to canonical file extensions.
var allowedImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
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
// and returns the normalised MIME type and file extension.
// Returns ErrUnsupportedType for anything not in the allow-list.
func DetectType(data []byte) (ct string, ext string, err error) {
	raw := http.DetectContentType(data)
	for mime, e := range allowedImageTypes {
		if len(raw) >= len(mime) && raw[:len(mime)] == mime {
			return mime, e, nil
		}
	}
	return "", "", ErrUnsupportedType
}
