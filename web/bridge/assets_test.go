package bridge

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRootServesFlutter pins the cutover: `/` is the Flutter Studio's index,
// never cached, so a reload of the root opens it.
func TestRootServesFlutter(t *testing.T) {
	for _, p := range []string{"/", "/index.html"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		studioHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", p, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "Marshal Studio") {
			t.Errorf("GET %s: body is not the Flutter index; got %q", p, body)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("GET %s: cache-control = %q, want no-cache", p, cc)
		}
	}
}

// TestRootFallsBackToIndex pins the Studio's own routing: a place in the URL is
// not an asset on disk, so the index answers and the Studio renders the place.
func TestRootFallsBackToIndex(t *testing.T) {
	for _, p := range []string{"/inbox", "/thread/local/a-1", "/settings/secrets"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		studioHandler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", p, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "Marshal Studio") {
			t.Errorf("GET %s: body missing the Flutter index; got %q", p, body)
		}
	}
}

// TestClassicIsGone pins that no second Studio is served: the trial's `/classic/`
// prefix held the Svelte build, and with it removed `/classic/…` is an ordinary
// unknown path, answered by the one Studio's index like any other place. A
// request for it must not reach a second tree.
func TestClassicIsGone(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/classic/inbox", nil)
	studioHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /classic/inbox: status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Marshal Studio") {
		t.Errorf("GET /classic/inbox: body is not the one Studio's index; got %q", body)
	}
}

// TestNextRedirectsToRoot pins the retired prefix: an old /next/ link — and the
// query it carried — opens the place it names.
func TestNextRedirectsToRoot(t *testing.T) {
	cases := []struct{ from, want string }{
		{"/next", "/"},
		{"/next/", "/"},
		{"/next/inbox", "/inbox"},
		{"/next/thread/local/a-1", "/thread/local/a-1"},
		{"/next/settings?tab=secrets", "/settings?tab=secrets"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.from, nil)
		nextRedirect(rec, req)

		if rec.Code != http.StatusPermanentRedirect {
			t.Errorf("GET %s: status = %d, want 308", tc.from, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != tc.want {
			t.Errorf("GET %s: Location = %q, want %q", tc.from, got, tc.want)
		}
	}
}

// TestRootServesItsAssets pins the un-prefixed asset path: the Flutter build is
// made with base href `/`, so its asset URLs are rooted at the root. A Go-only
// checkout embeds the placeholder, which has no hashed asset, so the check
// skips there and bites wherever a real build is embedded.
func TestRootServesItsAssets(t *testing.T) {
	name := hashedAssetName(t, studioRoot)
	if name == "" {
		t.Skip("no hashed asset in the embedded Studio tree")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+name, nil)
	studioHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /%s: status = %d, want 200", name, rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("cache-control for %s = %q, want immutable", name, cc)
	}
}

// TestNoAPIRouteIsShadowed pins the boundary: the bridge's API is the mux's,
// and the Studio's handler must not answer for a path under /api/.
func TestNoAPIRouteIsShadowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	studioHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/agents: status = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "Marshal Studio") {
		t.Errorf("an API path was answered with the Studio page")
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

// TestLooksHashed covers the names Flutter and Vite actually emit. An earlier
// cut of this predicate only accepted a part made solely of [a-f0-9], but Vite
// hashes are mixed-case base64url (index-CPIn_Y0W.js, index-zKuPc3MY.css), so
// it returned false for every real asset: the immutable branch of serveFile was
// dead code and browsers revalidated every chunk on every load. A purely
// lowercase hex name is still hashed, and a hand-written name with no hash must
// not be treated as immutable.
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
