package browser

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAttachBackendEmptyCDPURL(t *testing.T) {
	_, err := NewAttachBackend("", 30*time.Second)
	if err == nil {
		t.Fatal("expected error for empty cdpURL")
	}
}

func TestAttachBackendMode(t *testing.T) {
	b, err := NewAttachBackend("http://localhost:9222", 30*time.Second)
	if err != nil {
		t.Fatalf("NewAttachBackend: %v", err)
	}
	if b.Mode() != "attach" {
		t.Errorf("mode = %q, want attach", b.Mode())
	}
	_ = b.Close()
}

// TestAttachBackendUsesConfiguredTimeout verifies that ensureConnected
// bounds the CDP connection with the configured timeout rather than
// hanging at playwright's default (TOOLS-MIN-F22). A connection to a
// non-responsive endpoint should fail within the configured duration.
func TestAttachBackendUsesConfiguredTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("requires network attempt")
	}
	b := &AttachBackend{
		cdpURL:  "http://127.0.0.1:1/no-such-endpoint",
		timeout: 200 * time.Millisecond,
	}
	start := time.Now()
	err := b.ensureConnected(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error connecting to non-existent endpoint")
	}
	// Should fail within ~200ms, not the playwright default (30s).
	if elapsed > 5*time.Second {
		t.Fatalf("attach took %v, should have timed out within ~200ms", elapsed)
	}
}

func TestAttachBackendNewPageRequiresRunningBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	b, err := NewAttachBackend("http://localhost:9999", 2*time.Second)
	if err != nil {
		t.Fatalf("NewAttachBackend: %v", err)
	}
	defer b.Close()
	_, err = b.NewPage(context.Background())
	if err == nil {
		t.Fatal("expected error connecting to non-existent CDP endpoint")
	}
	if !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "websocket") && !strings.Contains(err.Error(), "refused") && !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "context") {
		t.Logf("error (acceptable): %v", err)
	}
}
