package service

import (
	"testing"

	"marshal/internal/llm/provider"
)

// TestAuthResolverDefaultsToProduction stays here: it reads the in-process
// host's own resolver field, which the seam does not carry.
func TestAuthResolverDefaultsToProduction(t *testing.T) {
	h, _ := New(Deps{State: newState(t, "s1")})
	if got, want := h.AuthResolver(), provider.DefaultAuthResolver(); got != want {
		t.Fatalf("AuthResolver() = %v, want the production default", got)
	}

	injected := newTestAuth()
	h, _ = New(Deps{State: newState(t, "s1"), Auth: injected})
	if got := h.AuthResolver(); got != provider.AuthResolver(injected) {
		t.Fatalf("AuthResolver() = %v, want the injected resolver", got)
	}
}
