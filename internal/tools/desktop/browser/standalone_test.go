package browser

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// hasArg reports whether opts.Args contains the exact string a.
func hasArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

// TestStandaloneLaunchArgs verifies that standaloneLaunchOptions hardens
// the Chromium launch with --no-sandbox and disables the Chromium sandbox
// (TOOLS-MOD-F12). A --user-data-dir is intentionally NOT passed: playwright
// manages its own temp profile for Launch.
func TestStandaloneLaunchArgs(t *testing.T) {
	opts := standaloneLaunchOptions(true)
	if !hasArg(opts.Args, "--no-sandbox") {
		t.Error("expected --no-sandbox in launch args")
	}
	for _, a := range opts.Args {
		if strings.HasPrefix(a, "--user-data-dir=") {
			t.Error("expected no --user-data-dir in launch args (playwright manages its own temp profile)")
		}
	}
	if opts.ChromiumSandbox == nil || *opts.ChromiumSandbox {
		t.Error("expected ChromiumSandbox to be explicitly false")
	}
	if opts.Headless == nil || !*opts.Headless {
		t.Error("expected Headless to be true when requested")
	}
}

// skipWithoutDriver converts a missing-Playwright-driver error into a skip.
// The standalone backend tests are integration tests: they need the Playwright
// driver and a browser binary on disk (`playwright install`), which stock CI
// images do not carry. Only the "driver is absent" case is skipped — a driver
// that is present but broken still fails, so this cannot mask a real
// regression.
func skipWithoutDriver(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "please install the driver") {
		t.Skipf("playwright driver not installed: %v", err)
	}
}

// newFixtureServer starts an httptest server serving a static, test-owned page
// so these tests never depend on the public internet. The page title and body
// are fixed constants the tests assert against.
func newFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	const page = `<!DOCTYPE html><html><head><title>Marshal fixture</title></head>` +
		`<body><p>owned browser fixture</p></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	return server
}

func TestStandaloneBackendNavigateAndRead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in short mode")
	}
	backend, err := NewStandaloneBackend(true, 30*time.Second)
	skipWithoutDriver(t, err)
	if err != nil {
		t.Fatalf("NewStandaloneBackend: %v", err)
	}
	defer backend.Close()

	if backend.Mode() != "standalone" {
		t.Errorf("mode = %q, want standalone", backend.Mode())
	}

	page, err := backend.NewPage(context.Background(), PageOptions{})
	skipWithoutDriver(t, err)
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	defer page.Close()

	server := newFixtureServer(t)
	defer server.Close()

	if err := page.Navigate(context.Background(), server.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	title, err := page.Title(context.Background())
	if err != nil {
		t.Fatalf("Title: %v", err)
	}
	if title != "Marshal fixture" {
		t.Errorf("title = %q, want %q", title, "Marshal fixture")
	}

	text, err := page.ReadableText(context.Background())
	if err != nil {
		t.Fatalf("ReadableText: %v", err)
	}
	if !strings.Contains(text, "owned browser fixture") {
		t.Errorf("text = %q, want something containing %q", text, "owned browser fixture")
	}
}

func TestStandaloneBackendScreenshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in short mode")
	}
	backend, err := NewStandaloneBackend(true, 30*time.Second)
	skipWithoutDriver(t, err)
	if err != nil {
		t.Fatalf("NewStandaloneBackend: %v", err)
	}
	defer backend.Close()

	page, err := backend.NewPage(context.Background(), PageOptions{})
	skipWithoutDriver(t, err)
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	defer page.Close()

	server := newFixtureServer(t)
	defer server.Close()

	if err := page.Navigate(context.Background(), server.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	data, err := page.Screenshot(context.Background(), ScreenshotOpts{Format: "png"})
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if len(data) < 100 {
		t.Errorf("screenshot data too small: %d bytes", len(data))
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Screenshot is not valid PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 {
		t.Errorf("screenshot has empty bounds: %v", b)
	}
}

// newSlowFontServer starts an httptest server whose page sets its heading in a
// webfont that only answers after a delay, and answers the font request with a
// failure. Two properties of that page carry the test:
//
//   - font-display: block hides the text while the font loads, so glyph pixels
//     in the capture mean the text had already been laid out — i.e. the
//     screenshot waited for the font load rather than racing it.
//   - the failure is deliberate: the load is what document.fonts.ready waits
//     for, and a failed load ends the block with fallback glyphs on screen, so
//     the test needs no font file of its own.
func newSlowFontServer(t *testing.T) *httptest.Server {
	t.Helper()
	const page = `<!DOCTYPE html><html><head><style>
@font-face {
  font-family: "MarshalSlow";
  src: url("/slow-font.woff2") format("woff2");
  font-display: block;
}
body { margin: 0; background: #ffffff; }
h1 { font-family: "MarshalSlow", monospace; font-size: 64px; color: #000000; margin: 0; padding: 16px; }
</style></head><body><h1>Marshal</h1></body></html>`
	mux := http.NewServeMux()
	mux.HandleFunc("/slow-font.woff2", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "no font here", http.StatusNotFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(page))
	})
	return httptest.NewServer(mux)
}

// darkPixels counts clearly-inked pixels, so a capture of a blank or not-yet
// laid-out page is distinguishable from one that has glyphs.
func darkPixels(img image.Image) int {
	box := img.Bounds()
	count := 0
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a > 0x8000 && r+g+b < 3*0x4000 {
				count++
			}
		}
	}
	return count
}

// TestScreenshotSettlesAfterFonts: a screenshot of a page whose webfont is
// still in flight must not capture the blocked (glyph-less) state.
func TestScreenshotSettlesAfterFonts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in short mode")
	}
	backend, err := NewStandaloneBackend(true, 30*time.Second)
	skipWithoutDriver(t, err)
	if err != nil {
		t.Fatalf("NewStandaloneBackend: %v", err)
	}
	defer backend.Close()

	page, err := backend.NewPage(context.Background(), PageOptions{Viewport: &Viewport{Width: 400, Height: 200}})
	skipWithoutDriver(t, err)
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	defer page.Close()

	server := newSlowFontServer(t)
	defer server.Close()

	if err := page.Navigate(context.Background(), server.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	data, err := page.Screenshot(context.Background(), ScreenshotOpts{Format: "png"})
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Screenshot is not valid PNG: %v", err)
	}
	// "Marshal" at 64px is far more ink than any antialiasing noise.
	if dark := darkPixels(img); dark < 200 {
		t.Fatalf("screenshot has %d inked pixels, want the settled heading's glyphs", dark)
	}
}

// newBoxServer starts an httptest server with one known-size element on a page
// much larger than the viewport, so a full-viewport capture and a clip to the
// element cannot be confused.
func newBoxServer(t *testing.T) *httptest.Server {
	t.Helper()
	const page = `<!DOCTYPE html><html><head><style>
body { margin: 0; background: #ffffff; }
#box { width: 120px; height: 80px; background: #000000; }
</style></head><body><div id="box"></div>` +
		`<div style="height: 2000px"></div></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(page))
	}))
	return server
}

// TestScreenshotClipsSelector: a clipped capture is the element's box, not the
// viewport's.
func TestScreenshotClipsSelector(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in short mode")
	}
	backend, err := NewStandaloneBackend(true, 30*time.Second)
	skipWithoutDriver(t, err)
	if err != nil {
		t.Fatalf("NewStandaloneBackend: %v", err)
	}
	defer backend.Close()

	page, err := backend.NewPage(context.Background(), PageOptions{Viewport: &Viewport{Width: 800, Height: 600}})
	skipWithoutDriver(t, err)
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	defer page.Close()

	server := newBoxServer(t)
	defer server.Close()

	if err := page.Navigate(context.Background(), server.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	data, err := page.Screenshot(context.Background(), ScreenshotOpts{Format: "png", Selector: "#box"})
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Screenshot is not valid PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 120 || b.Dy() != 80 {
		t.Fatalf("clipped capture = %dx%d, want the element box 120x80 (not the 800x600 viewport)", b.Dx(), b.Dy())
	}
}

// TestScreenshotMissingSelectorNamesIt: clipping to an element that is not
// there is a caller mistake, and the error has to name the selector.
func TestScreenshotMissingSelectorNamesIt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in short mode")
	}
	backend, err := NewStandaloneBackend(true, 30*time.Second)
	skipWithoutDriver(t, err)
	if err != nil {
		t.Fatalf("NewStandaloneBackend: %v", err)
	}
	defer backend.Close()

	page, err := backend.NewPage(context.Background(), PageOptions{})
	skipWithoutDriver(t, err)
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	defer page.Close()

	server := newBoxServer(t)
	defer server.Close()

	if err := page.Navigate(context.Background(), server.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	_, err = page.Screenshot(context.Background(), ScreenshotOpts{Format: "png", Selector: "#missing"})
	if err == nil {
		t.Fatal("expected an error for a missing clip selector")
	}
	if !strings.Contains(err.Error(), "#missing") {
		t.Fatalf("error %q should name the selector", err)
	}
}
