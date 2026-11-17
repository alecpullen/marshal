package browser

import (
	"context"
	"errors"
	"testing"
)

func TestSessionLazyCreation(t *testing.T) {
	backend := &FakeBackend{}
	s := NewSession(backend)

	_, _ = s.Page(context.Background(), PageOptions{})
	p, err := s.Page(context.Background(), PageOptions{})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if p == nil {
		t.Fatal("page should be non-nil after first call")
	}

	p2, err := s.Page(context.Background(), PageOptions{})
	if err != nil {
		t.Fatalf("Page second call: %v", err)
	}
	if p != p2 {
		t.Fatal("session should return the same page handle on subsequent calls")
	}
}

func TestSessionClose(t *testing.T) {
	backend := &FakeBackend{CloseErr: errors.New("close failed")}
	s := NewSession(backend)
	_, _ = s.Page(context.Background(), PageOptions{})

	if err := s.Close(); err == nil {
		t.Fatal("expected close error from backend")
	}
}

func TestSessionCloseWithoutPage(t *testing.T) {
	backend := &FakeBackend{}
	s := NewSession(backend)
	if err := s.Close(); err != nil {
		t.Fatalf("close without page: %v", err)
	}
}

func TestSessionCloseIsIdempotent(t *testing.T) {
	backend := &FakeBackend{}
	s := NewSession(backend)
	_ = s.Close()
	if err := s.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// TestPageReturnsSamePageWithoutOptions: asking for the page without a viewport
// is the plain read every tool does between turns and must never recreate it.
func TestPageReturnsSamePageWithoutOptions(t *testing.T) {
	backend := &FakeBackend{}
	s := NewSession(backend)

	first, err := s.Page(context.Background(), PageOptions{})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	second, err := s.Page(context.Background(), PageOptions{})
	if err != nil {
		t.Fatalf("Page second call: %v", err)
	}
	if first != second {
		t.Fatal("a call without options must return the existing page")
	}
	if backend.NewPageCalls != 1 {
		t.Fatalf("NewPage calls = %d, want 1", backend.NewPageCalls)
	}
}

// TestNavigateRecordsLastURL: the session's own Navigate is what a recreation
// re-navigates to, so it has to keep the last successful URL.
func TestNavigateRecordsLastURL(t *testing.T) {
	backend := &FakeBackend{}
	s := NewSession(backend)

	if got := s.LastURL(); got != "" {
		t.Fatalf("LastURL before any navigation = %q, want empty", got)
	}
	if err := s.Navigate(context.Background(), "https://example.com/docs"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if got := s.LastURL(); got != "https://example.com/docs" {
		t.Fatalf("LastURL = %q, want https://example.com/docs", got)
	}
	if backend.Page.NavigatedTo != "https://example.com/docs" {
		t.Fatalf("page navigated to %q, want https://example.com/docs", backend.Page.NavigatedTo)
	}
}

// TestNavigateDoesNotRecordFailedURL: a navigation that failed left the page
// where it was, so a recreation must not chase the failed URL.
func TestNavigateDoesNotRecordFailedURL(t *testing.T) {
	backend := &FakeBackend{}
	s := NewSession(backend)
	if err := s.Navigate(context.Background(), "https://example.com/first"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	backend.Page.NavigateErr = errors.New("connection lost")
	if err := s.Navigate(context.Background(), "https://example.com/second"); err == nil {
		t.Fatal("expected navigation error")
	}
	if got := s.LastURL(); got != "https://example.com/first" {
		t.Fatalf("LastURL = %q, want https://example.com/first", got)
	}
}
