package imagepayload

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"testing"

	"marshal/internal/llm/schema"
)

// newStore opens a store on a test-private root.
func newStore(t *testing.T, cfg Config) *Store {
	t.Helper()
	st, err := Open(t.TempDir(), cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

// put stores src and fails the test on any error.
func put(t *testing.T, st *Store, src []byte, o Options) schema.ImageRef {
	t.Helper()
	ref, err := st.Put(context.Background(), bytes.NewReader(src), o)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	return ref
}

// encodePNG is the fixture encoder: tests hand Put real PNG bytes, never a
// hand-built header, so a fixture can never be "valid" only by accident.
func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buf.Bytes()
}

// solidPNG is an opaque flat-colour PNG of the given size.
func solidPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	return encodePNG(t, img)
}

// noisyImage is a deterministic, opaque high-entropy image: PNG cannot
// compress it, which is what makes the JPEG fallback observable.
func noisyImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	state := uint32(0x9e3779b9)
	next := func() uint8 {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		return uint8(state)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: next(), G: next(), B: next(), A: 0xFF})
		}
	}
	return img
}

func TestPutIsDeterministic(t *testing.T) {
	st := newStore(t, Config{})
	src := solidPNG(t, 10, 10, color.RGBA{R: 0x30, G: 0x60, B: 0x90, A: 0xFF})

	first := put(t, st, src, Options{})
	second := put(t, st, src, Options{})
	if first.Hash != second.Hash {
		t.Fatalf("same source and options gave different hashes: %s vs %s", first.Hash, second.Hash)
	}
	_, count, err := st.Size()
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if count != 1 {
		t.Fatalf("repeat put stored %d entries, want 1", count)
	}
}

func TestDownscalesLongEdge(t *testing.T) {
	st := newStore(t, Config{MaxEdge: 1568})
	ref := put(t, st, solidPNG(t, 3000, 10, color.RGBA{A: 0xFF}), Options{})
	if ref.Width != 1568 || ref.Height != 5 {
		t.Fatalf("stored %dx%d, want 1568x5", ref.Width, ref.Height)
	}
}

func TestNeverUpscales(t *testing.T) {
	st := newStore(t, Config{MaxEdge: 1568})
	ref := put(t, st, solidPNG(t, 10, 10, color.RGBA{A: 0xFF}), Options{MaxEdge: 1568})
	if ref.Width != 10 || ref.Height != 10 {
		t.Fatalf("stored %dx%d, want 10x10", ref.Width, ref.Height)
	}
}

func TestKeepsPNGForScreenshot(t *testing.T) {
	st := newStore(t, Config{MaxEdge: 1568})
	ref := put(t, st, solidPNG(t, 200, 200, color.RGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xFF}), Options{})
	if ref.MIME != "image/png" {
		t.Fatalf("stored %q, want image/png", ref.MIME)
	}
}

func TestFallsBackToJPEGWhenPNGExceedsLimit(t *testing.T) {
	img := noisyImage(400, 400)
	var pngBuf, jpegBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatalf("encode fixture as PNG: %v", err)
	}
	if err := jpeg.Encode(&jpegBuf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode fixture as JPEG: %v", err)
	}
	if jpegBuf.Len() >= pngBuf.Len() {
		t.Fatalf("fixture is not JPEG-friendly: png=%d jpeg=%d", pngBuf.Len(), jpegBuf.Len())
	}
	// A limit the PNG encoding exceeds and the JPEG encoding fits.
	limit := (pngBuf.Len() + jpegBuf.Len()) / 2

	st := newStore(t, Config{MaxEdge: 1568, MaxStoredBytes: limit})
	ref := put(t, st, pngBuf.Bytes(), Options{})
	if ref.MIME != "image/jpeg" {
		t.Fatalf("stored %q (%d bytes, limit %d), want image/jpeg", ref.MIME, ref.Bytes, limit)
	}
}

func TestRejectsUnsupportedFormat(t *testing.T) {
	st := newStore(t, Config{})
	_, err := st.Put(context.Background(), bytes.NewReader([]byte("BM\x00\x00\x00\x00\x00\x00")), Options{})
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("put returned %v, want ErrUnsupportedFormat", err)
	}
}

func TestRejectsCorruptImage(t *testing.T) {
	st := newStore(t, Config{})
	src := append([]byte("\x89PNG\r\n\x1a\n"), []byte("not a png body at all")...)
	_, err := st.Put(context.Background(), bytes.NewReader(src), Options{})
	if !errors.Is(err, ErrCorruptImage) {
		t.Fatalf("put returned %v, want ErrCorruptImage", err)
	}
}

func TestRejectsOversizeInput(t *testing.T) {
	st := newStore(t, Config{MaxInputBytes: 16})
	_, err := st.Put(context.Background(), bytes.NewReader(solidPNG(t, 10, 10, color.RGBA{A: 0xFF})), Options{})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("put returned %v, want ErrTooLarge", err)
	}
}

func TestEstimateTokensPinned(t *testing.T) {
	if got := EstimateTokens(1568, 882); got != 1929 {
		t.Fatalf("EstimateTokens(1568, 882) = %d, want 1929", got)
	}
	if got := EstimateTokens(0, 10); got != 0 {
		t.Fatalf("EstimateTokens(0, 10) = %d, want 0", got)
	}
}

func TestFormatOverrideWins(t *testing.T) {
	st := newStore(t, Config{MaxEdge: 1568})
	src := solidPNG(t, 200, 200, color.RGBA{R: 0x40, G: 0x50, B: 0x60, A: 0xFF})

	plain := put(t, st, src, Options{})
	forced := put(t, st, src, Options{Format: "jpeg"})
	if forced.MIME != "image/jpeg" {
		t.Fatalf("forced format stored %q, want image/jpeg", forced.MIME)
	}
	if forced.Hash == plain.Hash {
		t.Fatalf("forced jpeg kept the default call's hash %s", forced.Hash)
	}
}
