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
