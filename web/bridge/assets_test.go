package bridge

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRootServesFlutter pins the swap (F8 decision 1): `/` is the Flutter
// Studio's index, never cached, so a reload of the root opens it.
func TestRootServesFlutter(t *testing.T) {
	for _, p := range []string{"/", "/index.html"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		studioHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", p, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "Marshal Studio") {
			t.Errorf("GET %s: body is not the Studio's index; got %q", p, body)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("GET %s: cache-control = %q, want no-cache", p, cc)
		}
	}
}

// TestRootFallsBackToIndex pins the Studio's own routing: a place in the URL
// is not a file on disk, so the index answers and the Studio renders it.
func TestRootFallsBackToIndex(t *testing.T) {
	for _, p := range []string{"/inbox", "/thread/local/a-1", "/settings/secrets"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		studioHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", p, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "Marshal Studio") {
			t.Errorf("GET %s: body missing the Studio's index; got %q", p, body)
		}
	}
}

// TestClassicServesSvelte pins the trial's fallback (F8 decision 1): the
// Svelte Studio answers under /classic/, so a link to it during the trial
// release still works.
func TestClassicServesSvelte(t *testing.T) {
	for _, p := range []string{"/classic", "/classic/", "/classic/index.html"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		classicHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", p, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "Marshal Web UI") {
			t.Errorf("GET %s: body missing the Svelte index; got %q", p, body)
		}
	}
}

// TestClassicServesItsAssets pins the prefix stripping: the Svelte build is
// made with `--base=/classic/`, so its asset URLs carry the prefix and the
// handler reads them from the tree rooted at `static`.
func TestClassicServesItsAssets(t *testing.T) {
	name := hashedAssetName(t, classicRoot)
	if name == "" {
		t.Skip("no hashed asset in the embedded Svelte tree")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, classicPrefix+"/"+name, nil)
	classicHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s/%s: status = %d, want 200", classicPrefix, name, rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("cache-control for %s = %q, want immutable", name, cc)
	}
}

// TestNextRedirectsToRoot pins the permanent redirect (F8 decision 2): a
// bookmark made while the Studio lived under /next/ opens the same place at
// the root, query and all.
func TestNextRedirectsToRoot(t *testing.T) {
	cases := []struct{ from, to string }{
		{"/next", "/"},
		{"/next/", "/"},
		{"/next/inbox", "/inbox"},
		{"/next/thread/local/a-1", "/thread/local/a-1"},
		{"/next/settings/secrets?tab=models", "/settings/secrets?tab=models"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.from, nil)
		redirectNext(rec, req)

		if rec.Code != http.StatusMovedPermanently {
			t.Errorf("GET %s: status = %d, want 301", tc.from, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != tc.to {
			t.Errorf("GET %s: Location = %q, want %q", tc.from, got, tc.to)
		}
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
		{"studio", studioHandler(), "/api/agents"},
		{"classic", classicHandler(), "/classic/api/agents"},
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

// TestIsClassicPath pins the prefix the router matches on: /classic and
// everything under it, and nothing that merely starts with the same letters.
func TestIsClassicPath(t *testing.T) {
	for _, p := range []string{"/classic", "/classic/", "/classic/assets/index-x.js"} {
		if !isClassicPath(p) {
			t.Errorf("isClassicPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"/", "/classical", "/api/agents", "/next"} {
		if isClassicPath(p) {
			t.Errorf("isClassicPath(%q) = true, want false", p)
		}
	}
}

// TestIsNextPath pins the old prefix the router redirects on.
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
