package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestApplyAttachedViewportRejectsDeviceScaleFactor: an attached page's context
// scale factor is not this process's to set, so a request for a different one
// must fail loudly instead of being swallowed — a swallowed refusal leaves the
// session recording a viewport the page never took, and recreating it on every
// later call.
func TestApplyAttachedViewportRejectsDeviceScaleFactor(t *testing.T) {
	page := &FakePage{SetViewportErr: ErrViewportNeedsRecreate}
	err := applyAttachedViewport(context.Background(), page, &Viewport{Width: 800, Height: 600, DeviceScaleFactor: 2})
	if err == nil {
		t.Fatal("a device scale factor request against an attached page must fail")
	}
	for _, want := range []string{"device_scale_factor 2", "omit it", "attached"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
	if !page.Closed {
		t.Error("the page it could not configure should be closed, not left open in the user's browser")
	}
}

// TestApplyAttachedViewportResizeIsBestEffort: a plain resize failure keeps
// today's behaviour — the page stays on the size the browser already had
// rather than failing creation.
func TestApplyAttachedViewportResizeIsBestEffort(t *testing.T) {
	page := &FakePage{SetViewportErr: errors.New("cannot resize")}
	if err := applyAttachedViewport(context.Background(), page, &Viewport{Width: 800, Height: 600}); err != nil {
		t.Fatalf("a resize failure should be best-effort, got: %v", err)
	}
	if page.Closed {
		t.Error("a resize failure must not close the page")
	}
}

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
	_, err = b.NewPage(context.Background(), PageOptions{})
	if err == nil {
		t.Fatal("expected error connecting to non-existent CDP endpoint")
	}
	if !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "websocket") && !strings.Contains(err.Error(), "refused") && !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "context") {
		t.Logf("error (acceptable): %v", err)
	}
}
