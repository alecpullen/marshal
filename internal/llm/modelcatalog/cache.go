package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultURL is the only endpoint this package ever requests.
	DefaultURL = "https://models.dev/api.json"
	// CacheTTL is how long a cached catalog is used without a request.
	CacheTTL = 24 * time.Hour
	// maxCatalogBytes bounds the response body.
	maxCatalogBytes = 32 << 20
	// cacheVersion is the on-disk cache schema version. A file written by a
	// different version is treated as corrupt and refetched.
	cacheVersion = 1
)

// Cache loads the models.dev catalog from a local file, refreshing it over
// HTTP when it is missing or older than CacheTTL.
type Cache struct {
	Dir    string
	URL    string
	Now    func() time.Time
	Client *http.Client
}

// NewCache returns a cache rooted at dataDir/models-dev.
func NewCache(dataDir string) *Cache {
	return &Cache{
		Dir:    filepath.Join(dataDir, "models-dev"),
		URL:    DefaultURL,
		Now:    time.Now,
		Client: &http.Client{Timeout: 30 * time.Second},
	}
}

type cacheFile struct {
	Version   int       `json:"version"`
	FetchedAt time.Time `json:"fetched_at"`
	ETag      string    `json:"etag,omitempty"`
	Catalog   *Catalog  `json:"catalog"`
}

// Load returns the catalog, using a fresh cache without a request. A stale
// cache triggers a conditional request; a failed refresh falls back to the
// stale copy. Without a usable cache the error is returned.
func (c *Cache) Load(ctx context.Context) (*Catalog, error) {
	path := filepath.Join(c.Dir, "catalog.json")
	cached, cacheErr := readCache(path)
	if cacheErr == nil && cached.Catalog != nil && c.Now().Sub(cached.FetchedAt) < CacheTTL {
		return cached.Catalog, nil
	}
	etag := ""
	if cacheErr == nil {
		etag = cached.ETag
	}
	fresh, notModified, err := c.fetch(ctx, etag)
	if err != nil {
		if cacheErr == nil && cached.Catalog != nil {
			return cached.Catalog, nil
		}
		return nil, err
	}
	if notModified {
		cached.FetchedAt = c.Now()
		// A cache write failure is not fatal: the catalog is usable for this
		// run, and the next start simply revalidates over HTTP again.
		_ = writeCache(path, cached)
		return cached.Catalog, nil
	}
	// Same here: an unwritable data dir costs a refetch next start, not this
	// run's enrichment.
	_ = writeCache(path, fresh)
	return fresh.Catalog, nil
}

func (c *Cache) fetch(ctx context.Context, etag string) (*cacheFile, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("models.dev request: %w", err)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("models.dev request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("models.dev request: %s", resp.Status)
	}
	cat, err := Decode(io.LimitReader(resp.Body, maxCatalogBytes))
	if err != nil {
		return nil, false, err
	}
	return &cacheFile{
		Version:   1,
		FetchedAt: c.Now(),
		ETag:      resp.Header.Get("ETag"),
		Catalog:   cat,
	}, false, nil
}

func readCache(path string) (*cacheFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Version != cacheVersion {
		return nil, fmt.Errorf("catalog cache version %d, want %d", f.Version, cacheVersion)
	}
	if f.Catalog == nil {
		return nil, errors.New("catalog cache has no catalog")
	}
	return &f, nil
}

// writeCache replaces the cache file atomically, so a crash mid-write leaves
// the previous catalog intact instead of a truncated one.
func writeCache(path string, f *cacheFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
