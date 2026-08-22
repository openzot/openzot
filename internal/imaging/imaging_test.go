package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// pngOf renders a test image with a single off-centre marker, so a resize that
// silently dropped detail is visible as a missing colour rather than passing on
// dimensions alone.
func pngOf(t *testing.T, width, height int) []byte {
	t.Helper()

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))

	for y := range height {
		for x := range width {
			canvas.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 40, A: 255})
		}
	}

	var buffer bytes.Buffer

	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatalf("encode: %v", err)
	}

	return buffer.Bytes()
}

func TestMediaTypeIdentifiesFormatsFromTheirBytes(t *testing.T) {
	jpegBytes := func() []byte {
		var buffer bytes.Buffer

		if err := jpeg.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
			t.Fatalf("encode: %v", err)
		}

		return buffer.Bytes()
	}()

	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"png", pngOf(t, 4, 4), "image/png"},
		{"jpeg", jpegBytes, "image/jpeg"},
		{"gif", []byte("GIF89a" + "rest of the file"), "image/gif"},
		{"webp", append([]byte("RIFF\x00\x00\x00\x00WEBP"), 'V', 'P'), "image/webp"},
		{"text", []byte("#!/bin/sh\necho hello\n"), ""},
		{"empty", nil, ""},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := MediaType(test.data); got != test.want {
				t.Errorf("MediaType = %q, want %q", got, test.want)
			}

			if got := IsImage(test.data); got != (test.want != "") {
				t.Errorf("IsImage = %v for %q", got, test.want)
			}
		})
	}
}

func TestDimensionsReadsSizeWithoutFullyDecoding(t *testing.T) {
	width, height, ok := Dimensions(pngOf(t, 120, 45))

	if !ok {
		t.Fatal("a PNG should report its dimensions")
	}

	if width != 120 || height != 45 {
		t.Errorf("Dimensions = %dx%d, want 120x45", width, height)
	}

	if _, _, ok := Dimensions([]byte("not an image")); ok {
		t.Error("unreadable bytes must not claim dimensions")
	}
}

func TestNormalizeLeavesASmallImageAlone(t *testing.T) {
	original := pngOf(t, 100, 80)

	got, err := Normalize(original, DefaultMaxEdge)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	if got.Resized {
		t.Error("an image already within the limit must not be re-encoded")
	}

	if !bytes.Equal(got.Data, original) {
		t.Error("the bytes must be the file's own, so the digest describes what is on disk")
	}

	if got.Width != 100 || got.Height != 80 {
		t.Errorf("dimensions = %dx%d, want 100x80", got.Width, got.Height)
	}
}

func TestNormalizeReducesALargeImageAndReportsIt(t *testing.T) {
	got, err := Normalize(pngOf(t, 3000, 1500), 300)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	if !got.Resized {
		t.Error("a reduced image must say so, or the caller implies the model saw the original")
	}

	if got.Width != 300 {
		t.Errorf("width = %d, want the longest side reduced to 300", got.Width)
	}

	if got.Height != 150 {
		t.Errorf("height = %d, want the aspect ratio preserved", got.Height)
	}

	// the returned bytes must decode as what they claim to be: the digest, the
	// blob and the wire all use them
	config, _, err := image.DecodeConfig(bytes.NewReader(got.Data))
	if err != nil {
		t.Fatalf("normalised bytes do not decode: %v", err)
	}

	if config.Width != got.Width || config.Height != got.Height {
		t.Errorf("bytes are %dx%d but the report says %dx%d",
			config.Width, config.Height, got.Width, got.Height)
	}
}

func TestNormalizeScalesByTheLongestSide(t *testing.T) {
	got, err := Normalize(pngOf(t, 400, 1200), 600)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	if got.Height != 600 || got.Width != 200 {
		t.Errorf("got %dx%d, want 200x600 - a tall image is bounded by its height",
			got.Width, got.Height)
	}
}

func TestNormalizeAveragesRatherThanSamples(t *testing.T) {
	// half black, half white: a box filter returns grey at the seam, where
	// nearest-neighbour returns one of the two originals
	canvas := image.NewRGBA(image.Rect(0, 0, 200, 200))

	for y := range 200 {
		for x := range 200 {
			shade := uint8(0)

			if x%2 == 0 {
				shade = 255
			}

			canvas.Set(x, y, color.RGBA{R: shade, G: shade, B: shade, A: 255})
		}
	}

	var buffer bytes.Buffer

	if err := png.Encode(&buffer, canvas); err != nil {
		t.Fatalf("encode: %v", err)
	}

	got, err := Normalize(buffer.Bytes(), 100)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	decoded, err := png.Decode(bytes.NewReader(got.Data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	red, _, _, _ := decoded.At(10, 10).RGBA()

	if shade := red >> 8; shade < 100 || shade > 155 {
		t.Errorf("pixel is %d; a one-pixel stripe pattern must average to mid grey, not to one of its two colours", shade)
	}
}

func TestNormalizeRefusesWhatIsNotAnImage(t *testing.T) {
	if _, err := Normalize([]byte("<!doctype html>"), DefaultMaxEdge); err == nil {
		t.Error("normalising a non-image must fail rather than send it as one")
	}
}

func TestNormalizePassesThroughAFormatItCannotDecode(t *testing.T) {
	// a WebP header with no decoder behind it: recognised, unmeasurable, and
	// still worth sending to a model that understands it
	webp := append([]byte("RIFF\x20\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{0}, 16)...)

	got, err := Normalize(webp, DefaultMaxEdge)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	if got.MediaType != "image/webp" {
		t.Errorf("media type = %q, want image/webp", got.MediaType)
	}

	if got.Width != 0 || got.Height != 0 {
		t.Error("an undecodable image must report no dimensions rather than guess")
	}

	if !bytes.Equal(got.Data, webp) {
		t.Error("bytes must pass through untouched")
	}
}

func TestNormalizeDefaultsTheEdgeWhenUnset(t *testing.T) {
	got, err := Normalize(pngOf(t, 2000, 2000), 0)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	if got.Width != DefaultMaxEdge {
		t.Errorf("width = %d, want the default edge %d", got.Width, DefaultMaxEdge)
	}
}

func TestNormalizeRefusesAnUndecodableImageThatIsTooLarge(t *testing.T) {
	// a recognised header with nothing behind it, past the byte ceiling: it
	// cannot be reduced, so it must be refused rather than sent whole
	oversize := append([]byte("RIFF\x00\x00\x00\x00WEBP"), bytes.Repeat([]byte{0}, DefaultMaxBytes)...)

	if _, err := Normalize(oversize, DefaultMaxEdge); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

func TestNormalizeDecodesEveryFormatItClaimsTo(t *testing.T) {
	// GIF and JPEG travel the same path as PNG; a decoder wired to the wrong
	// media type would only show up here
	var gifBuffer bytes.Buffer

	if err := gif.Encode(&gifBuffer, image.NewRGBA(image.Rect(0, 0, 600, 300)), nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}

	got, err := Normalize(gifBuffer.Bytes(), 100)
	if err != nil {
		t.Fatalf("Normalize gif: %v", err)
	}

	if got.Width != 100 || got.MediaType != "image/png" {
		t.Errorf("gif normalised to %dx%d %s, want a 100px PNG", got.Width, got.Height, got.MediaType)
	}

	var jpegBuffer bytes.Buffer

	if err := jpeg.Encode(&jpegBuffer, image.NewRGBA(image.Rect(0, 0, 400, 400)), nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}

	got, err = Normalize(jpegBuffer.Bytes(), 50)
	if err != nil {
		t.Fatalf("Normalize jpeg: %v", err)
	}

	if got.Width != 50 || !got.Resized {
		t.Errorf("jpeg normalised to %dx%d resized=%v, want a reduced image", got.Width, got.Height, got.Resized)
	}
}

func TestNormalizeHandlesAnImageSmallerThanOnePixelPerBlock(t *testing.T) {
	// the edge is larger than the image in one dimension: every destination
	// pixel must still sample at least one source pixel
	got, err := Normalize(pngOf(t, 900, 3), 100)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	if got.Width != 100 || got.Height < 1 {
		t.Errorf("got %dx%d, want a 100px-wide image at least one pixel tall", got.Width, got.Height)
	}
}
