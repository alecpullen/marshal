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

func TestHandleSetSwapsInAnOwnedManager(t *testing.T) {
	h := NewHandle(nil, nil, nil)
	if h.Get() != nil || h.Root() != "" {
		t.Fatal("an empty handle should have no manager and no root")
	}
	m := NewManager("/a", map[string]ServerSpec{}, nil)
	if old := h.Set(m); old != nil {
		t.Fatalf("Set on an empty handle returned old = %p, want nil", old)
	}
	if h.Get() != m || h.Root() != "/a" {
		t.Fatalf("after Set, Get = %p root %q; want %p root /a", h.Get(), h.Root(), m)
	}
	if old := h.Set(nil); old != m {
		t.Fatalf("Set(nil) returned old = %p, want %p", old, m)
	}
	if _, ok := h.ServerFor("go"); ok {
		t.Fatal("ServerFor on a cleared handle reported a server")
	}
}
