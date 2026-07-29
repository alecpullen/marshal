package lsp

import "testing"

func TestHandleRestartSwapsManager(t *testing.T) {
	h := NewHandle(NewManager("/a", map[string]ServerSpec{}, nil), map[string]ServerSpec{}, nil)
	first := h.Get()
	if first == nil {
		t.Fatal("Get returned nil before Restart")
	}
	newM, old := h.Restart("/b")
	if old != first {
		t.Fatalf("Restart old = %p, want previous manager %p", old, first)
	}
	if newM == first {
		t.Fatal("Restart returned the same manager")
	}
	if h.Get() != newM {
		t.Fatal("Get after Restart did not return the new manager")
	}
}
