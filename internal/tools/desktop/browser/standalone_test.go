package browser

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStandaloneBackendNavigateAndRead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in short mode")
	}
	backend, err := NewStandaloneBackend(true, 30*time.Second)
	if err != nil {
		t.Fatalf("NewStandaloneBackend: %v", err)
	}
	defer backend.Close()

	if backend.Mode() != "standalone" {
		t.Errorf("mode = %q, want standalone", backend.Mode())
	}

	page, err := backend.NewPage(context.Background())
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	defer page.Close()

	target := "https://example.com"
	if err := page.Navigate(context.Background(), target); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	title, err := page.Title(context.Background())
	if err != nil {
		t.Fatalf("Title: %v", err)
	}
	if !strings.Contains(title, "Example") {
		t.Errorf("title = %q, want something containing 'Example'", title)
	}

	text, err := page.ReadableText(context.Background())
	if err != nil {
		t.Fatalf("ReadableText: %v", err)
	}
	if !strings.Contains(text, "Example") {
		t.Errorf("text = %q, want something containing 'Example'", text)
	}
}

func TestStandaloneBackendScreenshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser integration test in short mode")
	}
	backend, err := NewStandaloneBackend(true, 30*time.Second)
	if err != nil {
		t.Fatalf("NewStandaloneBackend: %v", err)
	}
	defer backend.Close()

	page, err := backend.NewPage(context.Background())
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	defer page.Close()

	_ = page.Navigate(context.Background(), "https://example.com")

	data, err := page.Screenshot(context.Background(), ScreenshotOpts{Format: "png"})
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if len(data) < 100 {
		t.Errorf("screenshot data too small: %d bytes", len(data))
	}
}
