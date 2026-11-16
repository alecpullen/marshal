package imagepayload

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// sniff reports the real type from magic bytes. The declared MIME is never
// trusted: a .png that is really a JPEG must decode as a JPEG.
func sniff(src []byte) (string, string, error) {
	switch {
	case bytes.HasPrefix(src, []byte("\x89PNG\r\n\x1a\n")):
		return "png", "image/png", nil
	case len(src) >= 3 && bytes.HasPrefix(src, []byte{0xFF, 0xD8, 0xFF}):
		return "jpeg", "image/jpeg", nil
	case bytes.HasPrefix(src, []byte("GIF87a")) || bytes.HasPrefix(src, []byte("GIF89a")):
		return "gif", "image/gif", nil
	case len(src) >= 12 && bytes.HasPrefix(src, []byte("RIFF")) && string(src[8:12]) == "WEBP":
		return "webp", "image/webp", nil
	}
	return "", "", fmt.Errorf("%w: not PNG, JPEG, GIF or WebP", ErrUnsupportedFormat)
}

func decode(src []byte) (image.Image, string, error) {
	format, _, err := sniff(src)
	if err != nil {
		return nil, "", err
	}
	var img image.Image
	switch format {
	case "png":
		img, err = png.Decode(bytes.NewReader(src))
	case "jpeg":
		img, err = jpeg.Decode(bytes.NewReader(src))
	case "gif":
		img, err = gif.Decode(bytes.NewReader(src)) // first frame
	case "webp":
		img, err = webp.Decode(bytes.NewReader(src))
	}
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrCorruptImage, err)
	}
	return img, format, nil
}

// downscale shrinks img so its long edge is at most maxEdge, preserving
// aspect and never upscaling. Catmull-Rom keeps UI text legible.
func downscale(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxEdge <= 0 || (w <= maxEdge && h <= maxEdge) {
		return img
	}
	scale := math.Min(float64(maxEdge)/float64(w), float64(maxEdge)/float64(h))
	nw, nh := int(math.Round(float64(w)*scale)), int(math.Round(float64(h)*scale))
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// flattenOnWhite drops alpha by compositing onto white. JPEG has no alpha
// channel, so an alpha image that is too large for PNG would otherwise lose
// its transparency arbitrarily.
func flattenOnWhite(img image.Image) image.Image {
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Over)
	return dst
}

// normalize is the whole pipeline: decode, downscale, re-encode, then prove
// the result fits. It is deterministic — the same source and options give the
// same bytes, so Put's hash is stable and a repeated view dedupes.
func normalize(src []byte, cfg Config, o Options) ([]byte, string, int, int, error) {
	img, _, err := decode(src)
	if err != nil {
		return nil, "", 0, 0, err
	}
	maxEdge := cfg.MaxEdge
	if o.MaxEdge > 0 {
		maxEdge = o.MaxEdge
	}
	limit := cfg.MaxStoredBytes
	if limit <= 0 {
		limit = 4 << 20
	}
	quality := cfg.JPEGQuality
	if quality <= 0 {
		quality = 85
	}
	if o.Quality > 0 {
		quality = o.Quality
	}

	current := img
	for attempt := 0; ; attempt++ {
		current = downscale(current, maxEdge)
		data, mime, err := encodeOnce(current, o.Format, quality, limit)
		if err != nil {
			return nil, "", 0, 0, err
		}
		b := current.Bounds()
		if len(data) <= limit {
			return data, mime, b.Dx(), b.Dy(), nil
		}
		if attempt >= 2 || maxEdge <= 32 {
			return nil, "", 0, 0, fmt.Errorf("%w: %d bytes at %dx%d still exceeds %d bytes",
				ErrTooLarge, len(data), b.Dx(), b.Dy(), limit)
		}
		maxEdge /= 2
	}
}

// encodeOnce applies the format rule: an explicit format wins; otherwise PNG
// when it fits, JPEG after it. Screenshots are text, so PNG is the baseline
// and JPEG is the fallback ladder (spec §12.4).
func encodeOnce(img image.Image, format string, quality, limit int) ([]byte, string, error) {
	var buf bytes.Buffer
	pngEncode := func() ([]byte, string, error) {
		buf.Reset()
		if err := png.Encode(&buf, img); err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrCorruptImage, err)
		}
		return buf.Bytes(), "image/png", nil
	}
	jpegEncode := func(q int) ([]byte, string, error) {
		buf.Reset()
		if err := jpeg.Encode(&buf, flattenOnWhite(img), &jpeg.Options{Quality: q}); err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrCorruptImage, err)
		}
		return buf.Bytes(), "image/jpeg", nil
	}
	switch format {
	case "png":
		return pngEncode()
	case "jpeg":
		return jpegEncode(quality)
	}
	data, mime, err := pngEncode()
	if err != nil || len(data) <= limit {
		return data, mime, err
	}
	for _, q := range []int{quality, 75, 60} {
		if data, mime, err = jpegEncode(q); err != nil || len(data) <= limit {
			return data, mime, err
		}
	}
	return data, mime, nil
}
