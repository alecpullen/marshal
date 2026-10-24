package modelcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testNow is the fixed clock every test installs on Cache.Now.
var testNow = time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)

// testDoc renders a minimal models.dev document with one provider ("test")
// holding one model.
func testDoc(modelID string) string {
	return `{"test":{"id":"test","models":{"` + modelID +
		`":{"id":"` + modelID + `","limit":{"context":128000,"output":8192},` +
		`"cost":{"input":1,"output":2}}}}}`
}

func mustDecode(t *testing.T, doc string) *Catalog {
	t.Helper()
	cat, err := Decode(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("decode %q: %v", doc, err)
	}
	return cat
}

// newTestCache returns a cache in a temp dir with a fixed clock and the given
// endpoint.
func newTestCache(t *testing.T, url string) *Cache {
	t.Helper()
	c := NewCache(t.TempDir())
	c.URL = url
	c.Now = func() time.Time { return testNow }
	return c
}

func cachePath(c *Cache) string {
	return filepath.Join(c.Dir, "catalog.json")
}

func writeTestCache(t *testing.T, c *Cache, fetchedAt time.Time, etag string, cat *Catalog) {
	t.Helper()
	if err := writeCache(cachePath(c), &cacheFile{
		Version:   cacheVersion,
		FetchedAt: fetchedAt,
		ETag:      etag,
		Catalog:   cat,
	}); err != nil {
		t.Fatalf("write cache: %v", err)
	}
}

func readTestCache(t *testing.T, c *Cache) *cacheFile {
	t.Helper()
	f, err := readCache(cachePath(c))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	return f
}

func TestLoadUsesFreshCacheWithoutRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL)
	}))
	defer srv.Close()

	c := newTestCache(t, srv.URL)
	writeTestCache(t, c, testNow, "v1", mustDecode(t, testDoc("cached-model")))

	cat, err := c.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cat.Lookup("test", "cached-model"); !ok {
		t.Fatalf("cached model missing from %+v", cat.Providers)
	}
}

func TestLoadRefreshesStaleCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v2"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testDoc("new-model")))
	}))
	defer srv.Close()

	c := newTestCache(t, srv.URL)
	writeTestCache(t, c, testNow.Add(-25*time.Hour), `"v1"`, mustDecode(t, testDoc("old-model")))

	cat, err := c.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cat.Lookup("test", "new-model"); !ok {
		t.Fatalf("refreshed model missing from %+v", cat.Providers)
	}
	if _, ok := cat.Lookup("test", "old-model"); ok {
		t.Fatalf("stale model still present in %+v", cat.Providers)
	}
	if got := readTestCache(t, c).ETag; got != `"v2"` {
		t.Fatalf("cache ETag = %q, want %q", got, `"v2"`)
	}
}

func TestLoadRevalidatesWithETag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != `"v1"` {
			t.Errorf("If-None-Match = %q, want %q", got, `"v1"`)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	c := newTestCache(t, srv.URL)
	writeTestCache(t, c, testNow.Add(-25*time.Hour), `"v1"`, mustDecode(t, testDoc("cached-model")))

	cat, err := c.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cat.Lookup("test", "cached-model"); !ok {
		t.Fatalf("cached model missing from %+v", cat.Providers)
	}
	if got := readTestCache(t, c).FetchedAt; !got.Equal(testNow) {
		t.Fatalf("cache FetchedAt = %v, want %v", got, testNow)
	}
}

func TestLoadFallsBackToStaleCacheOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestCache(t, srv.URL)
	writeTestCache(t, c, testNow.Add(-25*time.Hour), `"v1"`, mustDecode(t, testDoc("stale-model")))

	cat, err := c.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cat.Lookup("test", "stale-model"); !ok {
		t.Fatalf("stale model missing from %+v", cat.Providers)
	}
}

func TestLoadReturnsErrorWithoutCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestCache(t, srv.URL)

	if _, err := c.Load(context.Background()); err == nil {
		t.Fatal("Load succeeded, want error")
	}
}

func TestLoadRejectsCorruptCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testDoc("fresh-model")))
	}))
	defer srv.Close()

	c := newTestCache(t, srv.URL)
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cachePath(c), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}

	cat, err := c.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cat.Lookup("test", "fresh-model"); !ok {
		t.Fatalf("fresh model missing from %+v", cat.Providers)
	}
}

func TestLoadRejectsOversizeBody(t *testing.T) {
	// A body larger than maxCatalogBytes is truncated by the limit reader and
	// fails to decode, so the stale cache is used instead of a partial one.
	big := strings.Repeat("x", maxCatalogBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()

	c := newTestCache(t, srv.URL)
	writeTestCache(t, c, testNow.Add(-25*time.Hour), `"v1"`, mustDecode(t, testDoc("stale-model")))

	cat, err := c.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cat.Lookup("test", "stale-model"); !ok {
		t.Fatalf("stale model missing from %+v", cat.Providers)
	}
}

func TestLoadFailedRefreshLeavesCacheFileUntouched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestCache(t, srv.URL)
	staleAt := testNow.Add(-25 * time.Hour)
	writeTestCache(t, c, staleAt, `"v1"`, mustDecode(t, testDoc("stale-model")))

	if _, err := c.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := readTestCache(t, c)
	if !got.FetchedAt.Equal(staleAt) {
		t.Errorf("FetchedAt = %v, want %v (a failed refresh must not rewrite the cache)", got.FetchedAt, staleAt)
	}
	if got.ETag != `"v1"` {
		t.Errorf("ETag = %q, want %q", got.ETag, `"v1"`)
	}
}

func TestLoadRejectsUnknownCacheVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testDoc("fresh-model")))
	}))
	defer srv.Close()

	c := newTestCache(t, srv.URL)
	if err := writeCache(cachePath(c), &cacheFile{
		Version:   cacheVersion + 1,
		FetchedAt: testNow,
		Catalog:   mustDecode(t, testDoc("future-model")),
	}); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	cat, err := c.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cat.Lookup("test", "fresh-model"); !ok {
		t.Fatalf("fresh model missing from %+v", cat.Providers)
	}
}
