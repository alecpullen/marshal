package browser

import (
	"context"
	"errors"
	"testing"
)

func TestSessionLazyCreation(t *testing.T) {
	backend := &FakeBackend{}
	s := NewSession(backend)

	_, _ = s.Page(context.Background())
	p, err := s.Page(context.Background())
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if p == nil {
		t.Fatal("page should be non-nil after first call")
	}

	p2, err := s.Page(context.Background())
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
	_, _ = s.Page(context.Background())

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
