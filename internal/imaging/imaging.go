// Package imaging recognises image files and prepares them to be shown to a
// model.
//
// It does two jobs and no more: say whether a file is an image, and normalise
// one to something worth sending. Normalising matters because an image is
// charged by area - a 4K screenshot costs several thousand tokens to convey
// what a 1568-pixel one conveys exactly as well - and because the bytes on the
// wire, the digest in the log and the blob on disk have to be the same object.
// Downscaling anywhere later would break that.
//
// Standard library only: PNG, JPEG and GIF decode, and anything else is passed
// through unmeasured rather than refused, so a format Go cannot open still
// reaches a model that can.
package imaging

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
)

// DefaultMaxEdge is the longest side an image is reduced to.
//
// Well below a screenshot's natural size and comfortably above the point where
// detail stops surviving the model's own tiling, so text in a UI screenshot is
// still legible while the cost stays bounded.
const DefaultMaxEdge = 1568

// DefaultMaxBytes is the largest encoded image accepted, before or after
// normalisation. A guard against handing a run's whole context window to one
// file, not a statement about what models accept.
const DefaultMaxBytes = 5 << 20

// ErrTooLarge is returned when an image is past DefaultMaxBytes and cannot be
// reduced under it.
var ErrTooLarge = errors.New("image is too large")

// MediaType identifies an image from its leading bytes, returning "" for
// anything that is not one.
//
// Sniffed rather than taken from the extension: the extension is a claim by
// whoever named the file, and a mislabelled file would otherwise be sent to a
// provider as a type it is not.
func MediaType(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(data) >= 3 && bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case len(data) >= 6 && (bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))):
		return "image/gif"
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return ""
	}
}

// IsImage reports whether the bytes are a recognised image.
func IsImage(data []byte) bool { return MediaType(data) != "" }

// Dimensions reports an image's pixel size without decoding it fully, and
// whether that was possible - a format Go cannot open reports nothing rather
// than guessing.
func Dimensions(data []byte) (width, height int, ok bool) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))

	if err != nil {
		return 0, 0, false
	}

	return config.Width, config.Height, true
}

// Normalized is an image prepared for a model.
type Normalized struct {
	Data      []byte
	MediaType string
	Width     int
	Height    int

	// Resized records whether the bytes differ from the input, so a caller can
	// say so rather than implying the model saw the file as it is on disk.
	Resized bool
}

// Normalize prepares image bytes to be sent.
//
// A decodable image larger than maxEdge is reduced and re-encoded as PNG; one
// already small enough is returned untouched, because re-encoding an image
// nobody asked to change would cost quality for nothing. A format the standard
// library cannot decode - WebP today - passes through with its dimensions
// unknown, since a model that understands it will do better with the file than
// with a refusal.
func Normalize(data []byte, maxEdge int) (Normalized, error) {
	mediaType := MediaType(data)

	if mediaType == "" {
		return Normalized{}, fmt.Errorf("not a recognised image format")
	}

	if maxEdge <= 0 {
		maxEdge = DefaultMaxEdge
	}

	decoded, err := decode(data, mediaType)

	if err != nil {
		// undecodable but recognised: send it as it is, if it is small enough
		if len(data) > DefaultMaxBytes {
			return Normalized{}, ErrTooLarge
		}

		return Normalized{Data: data, MediaType: mediaType}, nil
	}

	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	if width <= maxEdge && height <= maxEdge {
		if len(data) > DefaultMaxBytes {
			// too heavy for its size: re-encode rather than refuse
			return encode(decoded, width, height, true)
		}

		return Normalized{Data: data, MediaType: mediaType, Width: width, Height: height}, nil
	}

	scaled, scaledWidth, scaledHeight := downscale(decoded, maxEdge)

	return encode(scaled, scaledWidth, scaledHeight, true)
}

// decode opens an image with the codec for its type.
func decode(data []byte, mediaType string) (image.Image, error) {
	reader := bytes.NewReader(data)

	switch mediaType {
	case "image/png":
		return png.Decode(reader)
	case "image/jpeg":
		return jpeg.Decode(reader)
	case "image/gif":
		return gif.Decode(reader)
	default:
		return nil, fmt.Errorf("no decoder for %s", mediaType)
	}
}

// encode writes an image back out as PNG.
//
// PNG rather than JPEG because what gets attached is overwhelmingly a
// screenshot or a rendering - flat colour and text, which JPEG smears exactly
// where a model needs to read it.
func encode(source image.Image, width, height int, resized bool) (Normalized, error) {
	var buffer bytes.Buffer

	if err := png.Encode(&buffer, source); err != nil {
		return Normalized{}, err
	}

	if buffer.Len() > DefaultMaxBytes {
		return Normalized{}, ErrTooLarge
	}

	return Normalized{
		Data:      buffer.Bytes(),
		MediaType: "image/png",
		Width:     width,
		Height:    height,
		Resized:   resized,
	}, nil
}

// downscale reduces an image so its longest side is maxEdge, averaging each
// source block into one destination pixel.
//
// A box filter rather than nearest-neighbour: sampling every nth pixel of a
// screenshot drops thin strokes entirely, which is precisely the detail - one
// pixel of UI text, a hairline in a chart - that the image was attached for.
func downscale(source image.Image, maxEdge int) (image.Image, int, int) {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	scale := float64(maxEdge) / float64(width)

	if height > width {
		scale = float64(maxEdge) / float64(height)
	}

	target := image.Rect(0, 0, max(1, int(float64(width)*scale)), max(1, int(float64(height)*scale)))
	destination := image.NewRGBA(target)

	// work from an RGBA copy so pixel access does not go through the source's
	// colour model conversion for every sample
	rgba, ok := source.(*image.RGBA)

	if !ok {
		rgba = image.NewRGBA(bounds)
		draw.Draw(rgba, bounds, source, bounds.Min, draw.Src)
	}

	targetWidth, targetHeight := target.Dx(), target.Dy()

	for y := range targetHeight {
		startY := bounds.Min.Y + y*height/targetHeight
		endY := bounds.Min.Y + (y+1)*height/targetHeight

		if endY <= startY {
			endY = startY + 1
		}

		for x := range targetWidth {
			startX := bounds.Min.X + x*width/targetWidth
			endX := bounds.Min.X + (x+1)*width/targetWidth

			if endX <= startX {
				endX = startX + 1
			}

			var red, green, blue, alpha, count uint32

			for sampleY := startY; sampleY < endY; sampleY++ {
				for sampleX := startX; sampleX < endX; sampleX++ {
					offset := rgba.PixOffset(sampleX, sampleY)

					red += uint32(rgba.Pix[offset])
					green += uint32(rgba.Pix[offset+1])
					blue += uint32(rgba.Pix[offset+2])
					alpha += uint32(rgba.Pix[offset+3])
					count++
				}
			}

			if count == 0 {
				continue
			}

			offset := destination.PixOffset(x, y)

			destination.Pix[offset] = uint8(red / count)
			destination.Pix[offset+1] = uint8(green / count)
			destination.Pix[offset+2] = uint8(blue / count)
			destination.Pix[offset+3] = uint8(alpha / count)
		}
	}

	return destination, targetWidth, targetHeight
}
