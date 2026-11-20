package bridge

import (
	"html"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeStudio writes a small Studio into a temporary directory and points a
// server at it: the index, the engine's script and one hashed asset. It is the
// directory the asset tests below are served from, where the embedded
// placeholder used to stand.
func writeStudio(t *testing.T) (string, *Server) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "studio")
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatalf("make the Studio directory: %v", err)
	}
	writeIndex(t, dir, studioIndex)
	for name, body := range map[string]string{
		"main.dart.js":             "// the engine",
		"assets/index-CPIn_Y0W.js": "// a hashed chunk",
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	srv := NewServer(testFleet(t))
	srv.SetStudioDir(dir, []string{dir})
	return dir, srv
}

// studioIndex is the index a test Studio carries. Every asset test below
// recognises it by its title.
const studioIndex = "<!doctype html><html><head><title>Marshal Studio</title></head>" +
	"<body><h1>Marshal Studio</h1></body></html>"

// writeIndex writes body as dir's index.html.
func writeIndex(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(body), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
}

// writeStudioDir makes dir and writes an index into it: a studio the search
// can find, answered as the path that was written.
func writeStudioDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make %s: %v", dir, err)
	}
	writeIndex(t, dir, studioIndex)
	return dir
}

// resolved resolves a path the way FindStudioDir resolves the binary it was
// given, so a temporary directory behind a symlink (macOS's /var) compares
// equal to the answer.
func resolved(t *testing.T, path string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return got
}

// serve answers a request for one path from the server's Studio handler.
func serve(srv *Server, urlPath string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.studioHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, urlPath, nil))
	return rec
}

// TestRootServesFlutter pins the cutover: `/` is the Flutter Studio's index,
// never cached, so a reload of the root opens it.
func TestRootServesFlutter(t *testing.T) {
	_, srv := writeStudio(t)
	for _, p := range []string{"/", "/index.html"} {
		rec := serve(srv, p)

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
	_, srv := writeStudio(t)
	for _, p := range []string{"/inbox", "/thread/local/a-1", "/settings/secrets"} {
		rec := serve(srv, p)

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
	_, srv := writeStudio(t)
	rec := serve(srv, "/classic/inbox")

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
// made with base href `/`, so its asset URLs are rooted at the root. A hashed
// chunk is served immutably, because its name changes when it does.
func TestRootServesItsAssets(t *testing.T) {
	dir, srv := writeStudio(t)
	name := hashedAssetName(t, os.DirFS(dir))
	if name == "" {
		t.Fatal("the test Studio holds no hashed asset")
	}
	rec := serve(srv, "/"+name)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /%s: status = %d, want 200", name, rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("cache-control for %s = %q, want immutable", name, cc)
	}
}

// TestADirectoryInTheStudioIsAMiss pins that a directory opens but is not an
// asset. Without the regular-file check, the opened *os.File satisfies
// io.ReadSeeker, so http.ServeContent set a Content-Length from Seek(End) and
// then failed its first Read with EISDIR mid-copy: the client got a 200 whose
// body never arrived, where the embedded Studio answered a clean 500. It is a
// miss, so the index answers.
func TestADirectoryInTheStudioIsAMiss(t *testing.T) {
	_, srv := writeStudio(t)
	rec := serve(srv, "/assets")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /assets: status = %d, want the index's 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Marshal Studio") {
		t.Fatalf("GET /assets: body is not the Studio's index; got %q", body)
	}
	// The request completed: the length promised is the body written. A
	// directory served in place promises the directory's size and writes
	// nothing.
	if cl := rec.Header().Get("Content-Length"); cl != "" && cl != strconv.Itoa(len(body)) {
		t.Errorf("GET /assets: Content-Length = %s, body is %d bytes", cl, len(body))
	}
}

// TestAStudioFileAnswersIfModifiedSince pins the modtime http.ServeContent is
// handed for a file served from the directory: a zero time would keep a client
// from ever revalidating, and every other case here would still pass.
func TestAStudioFileAnswersIfModifiedSince(t *testing.T) {
	dir, srv := writeStudio(t)
	const name = "main.dart.js"
	when := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := os.Chtimes(filepath.Join(dir, name), when, when); err != nil {
		t.Fatalf("set %s's modtime: %v", name, err)
	}

	first := serve(srv, "/"+name)
	if first.Code != http.StatusOK {
		t.Fatalf("GET /%s: status = %d, want 200", name, first.Code)
	}
	lastMod := first.Header().Get("Last-Modified")
	if lastMod == "" {
		t.Fatalf("GET /%s: no Last-Modified header", name)
	}
	if want := when.Format(http.TimeFormat); lastMod != want {
		t.Errorf("Last-Modified = %q, want the file's modtime %q", lastMod, want)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+name, nil)
	req.Header.Set("If-Modified-Since", lastMod)
	srv.studioHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("GET /%s if-modified-since: status = %d, want 304", name, rec.Code)
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("a 304 carried a body: %q", body)
	}
}

// TestTheRouteServesTheStudio pins the wiring, not only the handler: a request
// that came through ServeHTTP — as the web listener's does — for a Studio file
// reaches studioHandler. Calling studioHandler() directly, as the other tests
// here do, would pass with route no longer calling it.
func TestTheRouteServesTheStudio(t *testing.T) {
	_, srv := writeStudio(t)
	req := httptest.NewRequest(http.MethodGet, "/main.dart.js", nil)
	req.Host = "localhost" // an address this daemon answers to
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /main.dart.js through ServeHTTP: status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "// the engine" {
		t.Errorf("GET /main.dart.js through ServeHTTP: body = %q, want the engine", body)
	}
}

// TestNoAPIRouteIsShadowed pins the boundary: the bridge's API is the mux's,
// and the Studio's handler must not answer for a path under /api/.
func TestNoAPIRouteIsShadowed(t *testing.T) {
	_, srv := writeStudio(t)
	rec := serve(srv, "/api/agents")

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

// TestFindStudioDirPrefersBesideTheBinary pins the first candidate: the
// release archive and a checkout put the Studio beside `webbridge`, so that
// copy wins over the one above the binary's directory.
func TestFindStudioDirPrefersBesideTheBinary(t *testing.T) {
	prefix := t.TempDir()
	exe := filepath.Join(prefix, "bin", "webbridge")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatalf("make bin: %v", err)
	}
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write webbridge: %v", err)
	}
	beside := writeStudioDir(t, filepath.Join(prefix, "bin", studioSubdir))
	above := writeStudioDir(t, filepath.Join(prefix, studioSubdir))

	dir, looked := FindStudioDir(exe)

	if want := resolved(t, beside); dir != want {
		t.Fatalf("FindStudioDir = %q, want %q", dir, want)
	}
	if len(looked) != 2 {
		t.Fatalf("looked = %v, want both candidates", looked)
	}
	if looked[0] != resolved(t, beside) || looked[1] != resolved(t, above) {
		t.Errorf("looked = %v, want [%s %s]", looked, resolved(t, beside), resolved(t, above))
	}
}

// TestFindStudioDirFallsBackToTheSharePrefix pins the second candidate: a
// prefix such as Homebrew's, install.sh's or an app bundle's keeps the binaries
// in bin/ and the Studio beside it.
func TestFindStudioDirFallsBackToTheSharePrefix(t *testing.T) {
	prefix := t.TempDir()
	exe := filepath.Join(prefix, "bin", "webbridge")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatalf("make bin: %v", err)
	}
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write webbridge: %v", err)
	}
	above := writeStudioDir(t, filepath.Join(prefix, studioSubdir))

	dir, looked := FindStudioDir(exe)

	if want := resolved(t, above); dir != want {
		t.Fatalf("FindStudioDir = %q, want %q", dir, want)
	}
	if len(looked) != 2 || looked[1] != resolved(t, above) {
		t.Errorf("looked = %v, want the share prefix second", looked)
	}
}

// TestFindStudioDirFollowsASymlinkedBinary pins why the search resolves the
// binary's symlinks first: a link on PATH to a prefix's bin/webbridge names the
// prefix's Studio, not the one beside the link.
func TestFindStudioDirFollowsASymlinkedBinary(t *testing.T) {
	prefix := t.TempDir()
	realExe := filepath.Join(prefix, "bin", "webbridge")
	if err := os.MkdirAll(filepath.Dir(realExe), 0o755); err != nil {
		t.Fatalf("make bin: %v", err)
	}
	if err := os.WriteFile(realExe, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write webbridge: %v", err)
	}
	studio := writeStudioDir(t, filepath.Join(prefix, studioSubdir))

	link := filepath.Join(t.TempDir(), "webbridge")
	if err := os.Symlink(realExe, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}

	dir, _ := FindStudioDir(link)

	if want := resolved(t, studio); dir != want {
		t.Fatalf("FindStudioDir(link) = %q, want %q", dir, want)
	}
}

// TestFindStudioDirSkipsADirectoryWithoutAnIndex pins what makes a candidate
// the Studio: an index.html. A directory holding only some of a build is not
// one, so the search moves on, and a search that finds none answers "".
func TestFindStudioDirSkipsADirectoryWithoutAnIndex(t *testing.T) {
	prefix := t.TempDir()
	exe := filepath.Join(prefix, "bin", "webbridge")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatalf("make bin: %v", err)
	}
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write webbridge: %v", err)
	}
	incomplete := filepath.Join(prefix, "bin", studioSubdir)
	if err := os.MkdirAll(incomplete, 0o755); err != nil {
		t.Fatalf("make the incomplete Studio: %v", err)
	}
	if err := os.WriteFile(filepath.Join(incomplete, "main.dart.js"), []byte("// the engine"), 0o644); err != nil {
		t.Fatalf("write main.dart.js: %v", err)
	}
	above := writeStudioDir(t, filepath.Join(prefix, studioSubdir))

	dir, _ := FindStudioDir(exe)
	if want := resolved(t, above); dir != want {
		t.Fatalf("FindStudioDir = %q, want the share prefix %q", dir, want)
	}

	// Nowhere holds an index: there is no Studio.
	bare := filepath.Join(t.TempDir(), "bin", "webbridge")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatalf("make bin: %v", err)
	}
	if err := os.WriteFile(bare, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write webbridge: %v", err)
	}
	dir, looked := FindStudioDir(bare)
	if dir != "" {
		t.Fatalf("FindStudioDir found %q, want none", dir)
	}
	if len(looked) != 2 {
		t.Errorf("looked = %v, want both candidates", looked)
	}
}

// TestFindStudioDirAnswersAnAbsoluteDirectoryForARelativeBinary pins that a
// relative exe names an absolute directory: os.DirFS re-resolves a relative
// root on each request, so a relative candidate would serve whatever the
// working directory leads to then, not the tree the search found.
func TestFindStudioDirAnswersAnAbsoluteDirectoryForARelativeBinary(t *testing.T) {
	prefix := t.TempDir()
	exe := filepath.Join(prefix, "bin", "webbridge")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatalf("make bin: %v", err)
	}
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write webbridge: %v", err)
	}
	beside := writeStudioDir(t, filepath.Join(prefix, "bin", studioSubdir))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	rel, err := filepath.Rel(cwd, exe)
	if err != nil {
		t.Skipf("no relative form of %s from %s: %v", exe, cwd, err)
	}
	if filepath.IsAbs(rel) {
		t.Fatalf("%q is not relative to %q", rel, cwd)
	}

	dir, looked := FindStudioDir(rel)

	if !filepath.IsAbs(dir) {
		t.Fatalf("FindStudioDir(%q) = %q, want an absolute directory", rel, dir)
	}
	if want := resolved(t, beside); dir != want {
		t.Fatalf("FindStudioDir(%q) = %q, want %q", rel, dir, want)
	}
	for _, candidate := range looked {
		if !filepath.IsAbs(candidate) {
			t.Errorf("looked = %v: %q is relative", looked, candidate)
		}
	}
}

// TestTheStudioWithoutFilesNamesWhereItLooked pins the daemon that found no
// files: it answers 503 with the page that says where it looked and how to
// build them, and it does not cache it — a build fixes it without a restart.
func TestTheStudioWithoutFilesNamesWhereItLooked(t *testing.T) {
	srv := NewServer(testFleet(t))
	looked := []string{
		"/opt/marshal/share/marshal/studio",
		"/usr/local/share/marshal/studio",
		"/opt/<marshal>/share/marshal/studio",
	}
	srv.SetStudioDir("", looked)

	rec := serve(srv, "/")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /: status = %d, want 503", rec.Code)
	}
	if ct, want := rec.Header().Get("Content-Type"), "text/html; charset=utf-8"; ct != want {
		t.Errorf("content-type = %q, want %q", ct, want)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("cache-control = %q, want no-store", cc)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Marshal's Studio files are not installed") {
		t.Errorf("body does not say the files are missing; got %q", body)
	}
	for _, dir := range looked {
		if !strings.Contains(body, html.EscapeString(dir)) {
			t.Errorf("body does not name %q; got %q", dir, body)
		}
	}
	if strings.Contains(body, looked[2]) {
		t.Errorf("a looked-in path was not HTML-escaped: %q", body)
	}
	if !strings.Contains(body, "flutter build web --base-href / --no-web-resources-cdn") {
		t.Errorf("body does not say how to build the Studio; got %q", body)
	}
	if !strings.Contains(body, "reinstall Marshal") {
		t.Errorf("body does not offer a reinstall; got %q", body)
	}

	// A server built with no search has no candidates to name: the page must
	// not claim a search that never ran.
	bare := NewServer(testFleet(t))
	bare.SetStudioDir("", nil)
	rec = serve(bare, "/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET / with no candidates: status = %d, want 503", rec.Code)
	}
	body = rec.Body.String()
	if strings.Contains(body, "They were looked for in:") {
		t.Errorf("the no-files page claims a search with no candidates; got %q", body)
	}
	if !strings.Contains(body, "flutter build web --base-href / --no-web-resources-cdn") {
		t.Errorf("the page does not say how to build the Studio; got %q", body)
	}
	if !strings.Contains(body, "Marshal's Studio files are not installed") {
		t.Errorf("body does not say the files are missing; got %q", body)
	}
}

// TestTheStudioIsReadOnEachRequest pins that the directory is read per
// request: a rebuilt Studio is served at once, with no restart of the daemon.
func TestTheStudioIsReadOnEachRequest(t *testing.T) {
	dir, srv := writeStudio(t)
	if body := serve(srv, "/").Body.String(); !strings.Contains(body, "Marshal Studio") {
		t.Fatalf("the first answer is not the Studio's index; got %q", body)
	}

	writeIndex(t, dir, "<title>the rebuilt Studio</title>")

	rec := serve(srv, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "the rebuilt Studio") {
		t.Errorf("the rebuilt index was not served; got %q", body)
	}
}

// TestTheStudioServesNothingOutsideItsDirectory pins that a walk out of the
// Studio's directory names no file: a place with `..` in it is an ordinary
// unknown path, and the index answers.
func TestTheStudioServesNothingOutsideItsDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "studio")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make the Studio directory: %v", err)
	}
	writeIndex(t, dir, studioIndex)
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("beside the Studio"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	srv := NewServer(testFleet(t))
	srv.SetStudioDir(dir, []string{dir})

	for _, p := range []string{"/../secret", "/%2e%2e/secret", "/assets/../../secret"} {
		rec := serve(srv, p)

		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want the index's 200", p, rec.Code)
			continue
		}
		body := rec.Body.String()
		if strings.Contains(body, "beside the Studio") {
			t.Errorf("GET %s: served the file beside the Studio", p)
		}
		if !strings.Contains(body, "Marshal Studio") {
			t.Errorf("GET %s: body is not the Studio's index; got %q", p, body)
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

// hashedAssetName returns a real asset in the Studio directory whose name looks
// hashed.
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
