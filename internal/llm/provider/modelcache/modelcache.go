// Package modelcache persists discovered model lists across restarts so
// opening /models does not re-probe every provider on every launch.
//
// The cache is an optimization and never a source of truth: a missing,
// unreadable, or corrupt file is treated as empty rather than as an error.
// It sits beside the limits cache in the shared data directory, because
// provider configuration is not project-scoped and caching per project
// would re-probe the same endpoint once per repository.
package modelcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
)

const (
	cacheDir  = "models"
	cacheFile = "discovered.json"
	// DefaultTTL bounds how long a cached list is trusted. Config hashing
	// catches local edits; the TTL is what catches models added upstream.
	DefaultTTL = 24 * time.Hour
)

// Entry is one provider's cached discovery result.
type Entry struct {
	ConfigHash string             `json:"config_hash"`
	Models     []schema.ModelInfo `json:"models"`
	FetchedAt  time.Time          `json:"fetched_at"`
}

// Cache is the on-disk shape: provider name to entry.
type Cache struct {
	Providers map[string]Entry `json:"providers"`
}

// HashProvider fingerprints the configuration that determines which models
// a provider reports. Key material is digested, never stored: the hash
// answers "did the key change", not "what is the key".
func HashProvider(pc config.ProviderConfig) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s", pc.Type, pc.BaseURL, pc.APIKeyEnv)
	if pc.APIKey != "" {
		// Include a hash of the literal key (never the key itself).
		sum := sha256.Sum256([]byte(pc.APIKey))
		fmt.Fprintf(h, "\x00%x", sum)
	}
	// Also include the resolved env var value so repointing an env var
	// to a different key invalidates the cache.
	if pc.APIKeyEnv != "" {
		if val := os.Getenv(pc.APIKeyEnv); val != "" {
			sum := sha256.Sum256([]byte(val))
			fmt.Fprintf(h, "\x00env:%x", sum)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Load reads the cache. Every failure mode yields an empty cache: there is
// no caller who could usefully act on a cache-read error.
func Load(dataDir string) Cache {
	empty := Cache{Providers: map[string]Entry{}}
	data, err := os.ReadFile(cachePath(dataDir))
	if err != nil {
		return empty
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return empty
	}
	if c.Providers == nil {
		c.Providers = map[string]Entry{}
	}
	return c
}

// Save writes the cache, creating the directory if needed.
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

// Lookup returns a provider's cached models when the entry matches the
// current config and is within the TTL.
func (c Cache) Lookup(name string, pc config.ProviderConfig, ttl time.Duration, now time.Time) ([]schema.ModelInfo, bool) {
	e, ok := c.Providers[name]
	if !ok || len(e.Models) == 0 {
		return nil, false
	}
	if e.ConfigHash != HashProvider(pc) {
		return nil, false
	}
	if now.Sub(e.FetchedAt) > ttl {
		return nil, false
	}
	return e.Models, true
}

func cachePath(dataDir string) string {
	return filepath.Join(dataDir, cacheDir, cacheFile)
}
