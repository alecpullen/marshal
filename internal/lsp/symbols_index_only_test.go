package lsp

import "testing"

// TestIndexOnlyDropsTheLanguageServer pins the seam a child in its own worktree
// uses: the copy keeps the project index and the project scope, and drops only
// the handle whose root would otherwise decide both the servers asked and the
// paths the answers are relativized to.
func TestIndexOnlyDropsTheLanguageServer(t *testing.T) {
	original := NewSymbolQuery(&Handle{}, nil, 7)
	only := original.IndexOnly()
	if only == nil || only == original {
		t.Fatal("IndexOnly returned no distinct query")
	}
	if only.h != nil {
		t.Fatal("IndexOnly kept the language-server handle")
	}
	if only.projectID != 7 {
		t.Fatalf("project id = %d, want the project scope kept (7)", only.projectID)
	}
	if original.h == nil {
		t.Fatal("IndexOnly mutated the original query")
	}
	if q := (*SymbolQuery)(nil).IndexOnly(); q != nil {
		t.Fatalf("a nil query returned %#v", q)
	}
}
