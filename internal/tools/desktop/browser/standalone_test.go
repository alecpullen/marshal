package browser

import (
	"bytes"
	"context"
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

	page, err := backend.NewPage(context.Background())
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

	page, err := backend.NewPage(context.Background())
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
