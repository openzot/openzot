package provider

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Image is one image in a conversation: the bytes a model is shown, plus the
// identity that lets a log refer to them without carrying them.
//
// The bytes are deliberately not serialised. A session log is JSON Lines
// precisely so it can be tailed while a run is going and survive a crash
// mid-line; a megabyte of base64 on one line defeats both, and the arcade-style
// caller that caches its session directory between runs would be caching
// screenshots. So a record keeps the digest and the shape, the recorder writes
// the bytes to a blob beside the log, and a resumed run rehydrates by digest.
//
// Digest is the identity rather than the path: the same screenshot read twice
// stores once, and a record can never disagree with the bytes it names.
type Image struct {
	// MediaType is the IANA type of the encoded bytes.
	MediaType string `json:"media_type"`

	// Digest is "sha256:<hex>" over the encoded bytes.
	Digest string `json:"digest"`

	// Width and Height are the pixel dimensions after normalisation, zero when
	// the format could not be decoded to measure.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`

	// Size is the encoded byte length.
	Size int `json:"size,omitempty"`

	// Tokens is what the image is estimated to cost, stamped once when it is
	// attached so accounting, compaction and the log all quote one number.
	Tokens int `json:"tokens,omitempty"`

	// Origin is where the image came from, for whoever reads the log later.
	Origin string `json:"origin,omitempty"`

	// Detail is the provider's resolution hint: "auto", "low" or "high".
	Detail string `json:"detail,omitempty"`

	// Source says where the bytes are when they are not in memory: "blob" for a
	// file beside the session log, "inline" for Data below.
	Source string `json:"source,omitempty"`

	// Data is the base64 payload, set only for an inline image.
	Data string `json:"data,omitempty"`

	// Bytes is the encoded image in memory. Never serialised.
	Bytes []byte `json:"-"`
}

// Image sources.
const (
	// SourceBlob keeps the bytes in a file beside the session log.
	SourceBlob = "blob"

	// SourceInline keeps them in the record, for a log that has to be one
	// self-contained file.
	SourceInline = "inline"
)

// NewImage describes encoded image bytes, computing the digest and the estimate.
func NewImage(data []byte, mediaType string, width, height int) Image {
	return Image{
		MediaType: mediaType,
		Digest:    Digest(data),
		Width:     width,
		Height:    height,
		Size:      len(data),
		Tokens:    EstimateImageTokens(width, height),
		Bytes:     data,
	}
}

// Digest is the content address of image bytes.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)

	return "sha256:" + hex.EncodeToString(sum[:])
}

// Extension is the file extension for the image's media type, without the dot.
// Used to name its blob; unknown types fall back to "bin" so a blob is still
// written rather than silently dropped.
func (i Image) Extension() string {
	switch i.MediaType {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return "bin"
	}
}

// Encoded returns the base64 payload, from memory or from an inline record.
func (i Image) Encoded() string {
	if len(i.Bytes) > 0 {
		return base64.StdEncoding.EncodeToString(i.Bytes)
	}

	return i.Data
}

// DataURL renders the image as a data: URL, which is how both wire formats
// carry an image that is not hosted anywhere.
func (i Image) DataURL() string {
	return fmt.Sprintf("data:%s;base64,%s", i.MediaType, i.Encoded())
}

// Ready reports whether the image has bytes to send. A record whose blob has
// gone missing decodes into an Image with neither, and is dropped rather than
// sent as an empty attachment the model would be asked to describe.
func (i Image) Ready() bool { return len(i.Bytes) > 0 || i.Data != "" }

// EstimateImageTokens approximates what an image costs a model.
//
// Nobody publishes one formula for this and providers differ, so it follows the
// widely documented tiling shape: a base cost plus a cost per 512-pixel tile.
// It exists so the budget is not blind to images, not to be exact - the run's
// real accounting still comes from the usage the provider reports.
func EstimateImageTokens(width, height int) int {
	if width <= 0 || height <= 0 {
		// unmeasurable: assume a middling screenshot rather than nothing, so an
		// undecodable format cannot silently cost the budget zero
		return 1_000
	}

	tiles := ((width + 511) / 512) * ((height + 511) / 512)

	return 85 + 170*tiles
}
