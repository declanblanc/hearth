package media

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // register PNG decoder

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register WebP decoder
)

// ErrUnreadableImage is returned when image data cannot be decoded — e.g. a
// truncated upload or a file whose declared type doesn't match its bytes.
var ErrUnreadableImage = errors.New("media: unreadable image")

// ProcessProfilePhoto validates and normalizes the raw bytes of an uploaded
// profile photo, returning the re-encoded square JPEG ready for storage along
// with its content type and extension. It is the single chokepoint shared by the
// signup and profile-edit flows so the rules stay identical in both places.
//
// When the upload is rejected for a reason the user can fix — too large, an
// unsupported type, or undecodable image data — it returns a non-empty userMsg
// describing the problem and leaves err nil; callers surface userMsg as an inline
// field error. A non-nil err signals an unexpected internal failure.
func ProcessProfilePhoto(data []byte) (out []byte, contentType, ext, userMsg string, err error) {
	if int64(len(data)) > MaxProfilePhotoSize {
		return nil, "", "", fmt.Sprintf("Photo must be %d MB or smaller.", MaxProfilePhotoSize/(1024*1024)), nil
	}
	if _, _, derr := DetectType(data); errors.Is(derr, ErrUnsupportedType) {
		return nil, "", "", "Only JPEG, PNG, and WebP photos are supported.", nil
	} else if derr != nil {
		return nil, "", "", "", derr
	}
	normalized, ct, e, nerr := NormalizeProfilePhoto(data)
	if errors.Is(nerr, ErrUnreadableImage) {
		return nil, "", "", "That photo couldn't be read. Please try another.", nil
	} else if nerr != nil {
		return nil, "", "", "", nerr
	}
	return normalized, ct, e, "", nil
}

// ProfilePhotoSize is the edge length, in pixels, of the square we store for
// every profile photo. Photos are center-cropped to a square and scaled to this
// size so display is always a clean circle and storage stays small.
const ProfilePhotoSize = 512

// profilePhotoQuality is the JPEG quality used when re-encoding profile photos.
// 85 is visually indistinguishable from the source at avatar sizes while keeping
// objects well under R2's free tier.
const profilePhotoQuality = 85

// NormalizeProfilePhoto decodes an uploaded image (JPEG, PNG, or WebP),
// center-crops it to a square, scales it to ProfilePhotoSize, and re-encodes it
// as JPEG over a white background.
//
// This is the server-side half of the crop pipeline: even when the browser has
// already produced a square crop, we re-process here so the stored object is
// always a known-good, fixed-size JPEG and the server never trusts client output.
// Transparent regions are flattened onto white because the avatar is displayed
// as an opaque circle.
//
// It returns the encoded bytes plus the canonical content type and extension to
// store alongside them.
func NormalizeProfilePhoto(data []byte) (out []byte, contentType string, ext string, err error) {
	src, _, decodeErr := image.Decode(bytes.NewReader(data))
	if decodeErr != nil {
		return nil, "", "", ErrUnreadableImage
	}

	square := cropToSquare(src)

	// Scale the square down (or up) to the canonical avatar size. CatmullRom is
	// a high-quality resampling kernel — worth the extra cost for a one-off,
	// long-lived avatar.
	resized := image.NewRGBA(image.Rect(0, 0, ProfilePhotoSize, ProfilePhotoSize))
	white := image.NewUniform(image.White)
	draw.Draw(resized, resized.Bounds(), white, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(resized, resized.Bounds(), square, square.Bounds(), xdraw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: profilePhotoQuality}); err != nil {
		return nil, "", "", err
	}
	return buf.Bytes(), "image/jpeg", ".jpg", nil
}

// cropToSquare returns the largest centered square sub-image of src. If src is
// already square the bounds are returned unchanged.
func cropToSquare(src image.Image) image.Image {
	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width == height {
		return src
	}

	edge := width
	if height < width {
		edge = height
	}
	offsetX := bounds.Min.X + (width-edge)/2
	offsetY := bounds.Min.Y + (height-edge)/2
	cropRect := image.Rect(offsetX, offsetY, offsetX+edge, offsetY+edge)

	// image.Image doesn't expose SubImage, but every concrete type we decode
	// (and the RGBA fallback) does. Fall back to a copy if a decoder ever
	// returns something exotic.
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	if si, ok := src.(subImager); ok {
		return si.SubImage(cropRect)
	}
	copied := image.NewRGBA(image.Rect(0, 0, edge, edge))
	draw.Draw(copied, copied.Bounds(), src, cropRect.Min, draw.Src)
	return copied
}
