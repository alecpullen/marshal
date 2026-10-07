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

// staticFS embeds the SPA build output. During Go-only development it
// contains a placeholder index.html; the real SPA is written here by
// `npm run build` in web/ui.
//
//go:embed static
var staticFS embed.FS

// staticRoot is the sub-tree rooted at "static". It is created once so
// path cleaning and prefix stripping are handled by the fs.Sub layer.
var staticRoot, _ = fs.Sub(staticFS, "static")

// staticHandler serves the embedded SPA. Non-API paths that do not match a
// real static asset fall back to index.html so client-side routing works.
func staticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		// Clean the path and strip the leading slash.
		p := path.Clean("/" + r.URL.Path)[1:]
		if p == "" {
			p = "index.html"
		}

		// Try the exact file first.
		f, err := staticRoot.Open(p)
		if err == nil {
			serveFile(w, r, p, f)
			return
		}

		// Fall back to index.html for client-side routes; the SPA's own
		// router will render based on window.location.hash.
		f, err = staticRoot.Open("index.html")
		if err != nil {
			http.Error(w, "index.html not found in embedded assets", http.StatusInternalServerError)
			return
		}
		serveFile(w, r, "index.html", f)
	})
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

// openStatic is exposed for tests that need to inspect the embedded tree.
func openStatic(name string) (fs.File, error) {
	return staticRoot.Open(name)
}
