package limits

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	defaultCacheDir = "limits"
	mergedCacheFile = "merged.json"
	// DefaultTTL is the default time-to-live for the on-disk limit cache.
	DefaultTTL = 24 * time.Hour
)

// Cache holds a merged limit table loaded from disk with a timestamp.
type Cache struct {
	Table     map[string]Limit
	FetchedAt time.Time
}

// Load reads the merged cache from disk. It returns an empty cache and no
// error if the cache file does not exist.
func Load(dataDir string) (Cache, error) {
	path := cachePath(dataDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Cache{Table: map[string]Limit{}}, nil
		}
		return Cache{}, fmt.Errorf("read cache: %w", err)
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return Cache{}, fmt.Errorf("parse cache: %w", err)
	}
	if c.Table == nil {
		c.Table = map[string]Limit{}
	}
	return c, nil
}

// Save writes the merged cache to disk, creating the directory if needed.
func Save(dataDir string, c Cache) error {
	path := cachePath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	return nil
}

func cachePath(dataDir string) string {
	return filepath.Join(dataDir, defaultCacheDir, mergedCacheFile)
}
