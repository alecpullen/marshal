package bridge

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRootServesSvelte pins the deferred cutover (F8 decision 1, revised
// 2026-11-17): `/` is still the Svelte Studio's index, never cached, so a
// reload of the root opens it.
func TestRootServesSvelte(t *testing.T) {
	for _, p := range []string{"/", "/index.html"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		svelteHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", p, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "Marshal Web UI") {
			t.Errorf("GET %s: body is not the Svelte index; got %q", p, body)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("GET %s: cache-control = %q, want no-cache", p, cc)
		}
	}
}

// TestRootFallsBackToIndex pins the Svelte Studio's fallback: a path that is
// not a file on disk answers with its index, which reads the hash route.
func TestRootFallsBackToIndex(t *testing.T) {
	for _, p := range []string{"/inbox", "/thread/local/a-1", "/settings/secrets"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		svelteHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", p, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "Marshal Web UI") {
			t.Errorf("GET %s: body missing the Svelte index; got %q", p, body)
		}
	}
}

// TestNextServesFlutter pins the Flutter Studio's prefix (F8 decision 1,
// revised 2026-11-17): the trial build lives at /next/, never cached, so a
// reload opens it.
func TestNextServesFlutter(t *testing.T) {
	for _, p := range []string{"/next/", "/next/index.html"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		flutterHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", p, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "Marshal Studio") {
			t.Errorf("GET %s: body missing the Flutter index; got %q", p, body)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("GET %s: cache-control = %q, want no-cache", p, cc)
		}
	}
}

// TestNextFallsBackToIndex pins the Flutter Studio's own routing: a place in
// the URL is not an asset on disk, so the index answers and the Studio renders
// the place.
func TestNextFallsBackToIndex(t *testing.T) {
	for _, p := range []string{"/next/inbox", "/next/thread/local/a-1", "/next/settings/secrets"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		flutterHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", p, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "Marshal Studio") {
			t.Errorf("GET %s: body missing the Flutter index; got %q", p, body)
		}
	}
}

// TestNextBarePrefixRedirects pins the trailing slash: the Flutter build's
// base href is /next/, so serving its index at /next would resolve every
// relative asset URL a level too high.
func TestNextBarePrefixRedirects(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, nextPrefix, nil)
	flutterHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusPermanentRedirect {
		t.Errorf("GET %s: status = %d, want 308", nextPrefix, rec.Code)
	}
	if got := rec.Header().Get("Location"); got != nextPrefix+"/" {
		t.Errorf("GET %s: Location = %q, want %q", nextPrefix, got, nextPrefix+"/")
	}
}

// TestRootServesItsAssets pins the un-prefixed asset path: the Svelte build is
// made with the default base, so its asset URLs are rooted at `/`.
func TestRootServesItsAssets(t *testing.T) {
	name := hashedAssetName(t, svelteRoot)
	if name == "" {
		t.Skip("no hashed asset in the embedded Svelte tree")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+name, nil)
	svelteHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /%s: status = %d, want 200", name, rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("cache-control for %s = %q, want immutable", name, cc)
	}
}

// TestNoAPIRouteIsShadowed pins the boundary: the bridge's API is the mux's,
// and neither Studio's handler answers for a path under /api/.
func TestNoAPIRouteIsShadowed(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    http.Handler
		path string
	}{
		{"svelte", svelteHandler(), "/api/agents"},
		{"flutter", flutterHandler(), "/next/api/agents"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		tc.h.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: GET %s: status = %d, want 404", tc.name, tc.path, rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "Marshal Studio") ||
			strings.Contains(body, "Marshal Web UI") {
			t.Errorf("%s: an API path was answered with a Studio page", tc.name)
		}
	}
}

// TestIsNextPath pins the prefix the router matches on.
func TestIsNextPath(t *testing.T) {
	for _, p := range []string{"/next", "/next/", "/next/inbox", "/next/thread/local/a-1"} {
		if !isNextPath(p) {
			t.Errorf("isNextPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"/", "/nextdoor", "/api/agents", "/classic/next"} {
		if isNextPath(p) {
			t.Errorf("isNextPath(%q) = true, want false", p)
		}
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

// hashedAssetName returns a real embedded asset whose name looks hashed.
func hashedAssetName(t *testing.T, root fs.FS) string {
	t.Helper()
	var found string
	err := fs.WalkDir(root, ".", func(p string, _ fs.DirEntry, err error) error {
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
