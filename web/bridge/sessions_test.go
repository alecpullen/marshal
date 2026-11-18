package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

// Listing sessions for a project with no live agent answers on the control
// child. That child is the bridge's own, and other code holds hooks on it, so
// listing must ask it directly: building a Registry over it would install
// that Registry's OnRequest on the live child, and each listing would replace
// whatever the previous owner had put there.
func TestListingLeavesTheControlChildsHooksAlone(t *testing.T) {
	root := t.TempDir()
	f, _ := testFleetWithContainerizedAgent(t)
	ctl := newFakeAgent()
	child := &Child{Transport: ctl}
	f.newControl = func() (*Child, error) { return child, nil }
	srv := NewServer(f, "")

	path := "/api/sessions?cwd=" + url.QueryEscape(root)
	if rec := doReq(t, srv, http.MethodGet, path, nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("first GET /api/sessions = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// The control agent is live now, so from here its hooks belong to
	// whoever set them, not to the listing.
	sentinel := func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return map[string]any{}, nil
	}
	child.OnRequest = sentinel
	want := reflect.ValueOf(sentinel).Pointer()

	for i := 0; i < 2; i++ {
		if rec := doReq(t, srv, http.MethodGet, path, nil, nil); rec.Code != http.StatusOK {
			t.Fatalf("GET /api/sessions = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
	}
	if child.OnRequest == nil {
		t.Fatal("listing sessions cleared the control child's OnRequest")
	}
	if got := reflect.ValueOf(child.OnRequest).Pointer(); got != want {
		t.Fatalf("listing sessions replaced the control child's OnRequest: got %p, want %p", child.OnRequest, sentinel)
	}
}
