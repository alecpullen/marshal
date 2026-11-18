package bridge

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// studioFS embeds the Flutter Studio's build output, which `/` serves since
// F8's cutover retired the Svelte Studio. It is written here by
// `flutter build web --base-href / --no-web-resources-cdn` in app/, whose asset
// URLs are rooted at `/`.
//
// `--no-web-resources-cdn` is not optional: without it the build fetches the
// CanvasKit engine from gstatic.com, and a private network that cannot reach
// that CDN never starts the Studio at all.
//
// During Go-only development this holds a placeholder index.html — the real
// build is a 10 MB engine plus assets, produced in CI and by the release's
// studio-web job — so `go build ./cmd/webbridge` still embeds a page.
//
//go:embed static
var studioFS embed.FS

// studioRoot is the sub-tree rooted at "static". It is created once so path
// cleaning is handled by the fs.Sub layer.
var studioRoot, _ = fs.Sub(studioFS, "static")

// nextPrefix is where the Flutter Studio was served while the Svelte Studio
// held `/`. The cutover moved it to the root, so the prefix now only redirects
// an old link to the place it names.
const nextPrefix = "/next"

// isNextPath reports whether a request is for the retired /next/ prefix.
func isNextPath(p string) bool {
	return p == nextPrefix || strings.HasPrefix(p, nextPrefix+"/")
}

// nextRedirect is the permanent move off the retired prefix: /next and /next/
// open the Studio, and /next/inbox carries its path to /inbox. The query rides
// along, because a place may name its tab or filter there. The Studio's router
// reads the path, so every place the old prefix addressed still resolves.
func nextRedirect(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimPrefix(r.URL.Path, nextPrefix)
	if target == "" {
		target = "/"
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusPermanentRedirect)
}

// studioHandler serves the Flutter Studio at the root. A path that names no
// asset falls back to its index.html, so the Studio's own router renders the
// place (/inbox, /thread/local/a-1).
func studioHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The bridge's API is the mux's: a static handler must never answer
		// for it.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		serveFrom(w, r, studioRoot, r.URL.Path, "index.html not found in the embedded Studio assets")
	})
}

// serveFrom writes the embedded file [urlPath] names in [root], falling back
// to the tree's index.html so a client-side route renders.
func serveFrom(w http.ResponseWriter, r *http.Request, root fs.FS, urlPath, missing string) {
	p := path.Clean("/" + urlPath)[1:]
	if p == "" || p == "." {
		p = "index.html"
	}
	if f, err := root.Open(p); err == nil {
		serveFile(w, r, p, f)
		return
	}
	f, err := root.Open("index.html")
	if err != nil {
		http.Error(w, missing, http.StatusInternalServerError)
		return
	}
	serveFile(w, r, "index.html", f)
}

// serveFile writes a single embedded file with a sensible content type and
// cache policy. index.html is never cached; hashed asset filenames are
// cached immutably.
func serveFile(w http.ResponseWriter, r *http.Request, name string, f fs.File) {
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)

	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	} else if looksHashed(name) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Expires", time.Now().Add(365*24*time.Hour).Format(http.TimeFormat))
	}

	// Embedded files do not necessarily implement io.ReaderAt, so read the
	// whole body into memory. Static assets are small enough for this.
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(data))
}

// looksHashed reports whether a filename appears to contain a content hash
// (e.g. assets/index-a3f1c2.js). Hashed assets can be cached forever.
//
// Vite's hashes are base64url, so they carry upper-case letters, digits and
// the URL-safe "-"/"_" (index-CPIn_Y0W.js, index-zKuPc3MY.css). An earlier
// version accepted only a run of [a-f0-9] and therefore matched none of the
// assets Vite actually emits: every chunk fell to the revalidate branch, so
// the immutable cache was never set and browsers re-requested the whole
// bundle on every load.
func looksHashed(name string) bool {
	base := path.Base(name)
	// The extension carries no hash; drop it so the dot convention
	// (main.a1b2c3d4.js) does not mistake the suffix for one.
	stem := base
	if i := strings.LastIndex(base, "."); i > 0 {
		stem = base[:i]
	}
	for _, sep := range []string{"-", "."} {
		// The tail after the last separator is the usual form:
		// index-CPIn_Y0W, main.a1b2c3d4.
		if i := strings.LastIndex(stem, sep); i >= 0 && isHashTail(stem[i+1:]) {
			return true
		}
		// A hash may itself contain "-" (go-C27-OAKa), so the tail after
		// the first separator is a candidate too. It is looser — a
		// descriptive name like "some-long-name" tails here — so it must
		// carry a digit to qualify.
		if i := strings.Index(stem, sep); i >= 0 && isHashWithDigit(stem[i+1:]) {
			return true
		}
	}
	return false
}

// isHashTail reports whether s is shaped like a Vite hash: 8+ base64url
// characters that are not all lower-case letters. "BgDaEnEv" qualifies (a
// real Vite hash need not contain a digit) while a word like "documentation"
// does not.
func isHashTail(s string) bool {
	if len(s) < 8 || !base64url(s) {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') {
			return true
		}
	}
	return false
}

// isHashWithDigit reports whether s could be a hash containing separators.
func isHashWithDigit(s string) bool {
	return len(s) >= 8 && base64url(s) && strings.ContainsAny(s, "0123456789")
}

// base64url reports whether s is drawn from the base64url alphabet.
func base64url(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
