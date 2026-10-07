package bridge

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticHandlerServesIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	staticHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Marshal Web UI") {
		t.Errorf("index.html body missing title; got %q", body)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("index.html cache-control = %q, want no-cache", cc)
	}
}

// TestLooksHashed covers the names Vite actually emits. The first cut of
// this predicate only accepted a part made solely of [a-f0-9], but Vite
// hashes are mixed-case base64url (index-CPIn_Y0W.js, index-zKuPc3MY.css),
// so it returned false for every real asset: the immutable branch of
// serveFile was dead code and browsers revalidated every chunk on every
// load. A purely lowercase hex name is still hashed, and a hand-written
// name with no hash must not be treated as immutable.
func TestLooksHashed(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"assets/index-CPIn_Y0W.js", true},
		{"assets/index-zKuPc3MY.css", true},
		{"assets/index-D32VzAee.js", true},
		{"assets/main-a1b2c3d4.js", true},
		{"assets/go-C27-OAKa.js", true},
		{"index.html", false},
		{"assets/logo.svg", false},
		{"assets/geist-latin-wght-normal-BgDaEnEv.woff2", true},
		{"style.css", false},
	}
	for _, tc := range cases {
		if got := looksHashed(tc.name); got != tc.want {
			t.Errorf("looksHashed(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestStaticHandlerHashedAssetIsImmutable pins the cache policy a browser
// actually sees for a content-hashed asset.
func TestStaticHandlerHashedAssetIsImmutable(t *testing.T) {
	name := hashedAssetName(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+name, nil)
	staticHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Skipf("no hashed asset %q in the embedded tree", name)
	}
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") {
		t.Errorf("cache-control for %s = %q, want immutable", name, cc)
	}
}

// hashedAssetName returns a real embedded asset whose name looks hashed.
func hashedAssetName(t *testing.T) string {
	t.Helper()
	var found string
	err := fs.WalkDir(staticRoot, ".", func(p string, _ fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if looksHashed(p) {
			found = p
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return found
}

func TestStaticHandlerApiNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	staticHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/sessions on static handler: status = %d, want 404", rec.Code)
	}
}

func TestStaticHandlerFallbackToIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sessions/s-1", nil)
	staticHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /sessions/s-1: status = %d, want 200 (index fallback)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Marshal Web UI") {
		t.Errorf("fallback did not serve index.html")
	}
}
