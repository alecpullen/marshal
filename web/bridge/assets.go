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
// (e.g. assets/main-a3f1c2.js). Hashed assets can be cached forever.
func looksHashed(name string) bool {
	base := path.Base(name)
	// A typical Vite hashed chunk contains an 8+ char hex/alphanumeric hash
	// delimited by dots or dashes: main-a1b2c3d4.js or main.a1b2c3d4.js.
	for _, sep := range []string{"-", "."} {
		parts := strings.Split(base, sep)
		for _, part := range parts[:max(0, len(parts)-1)] {
			if len(part) >= 8 && isHash(part) {
				return true
			}
		}
	}
	return false
}

func isHash(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'f') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// openStatic is exposed for tests that need to inspect the embedded tree.
func openStatic(name string) (fs.File, error) {
	return staticRoot.Open(name)
}
