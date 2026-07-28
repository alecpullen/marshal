package modelcache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
)

func sample() []schema.ModelInfo {
	return []schema.ModelInfo{
		{ID: "gpt-4o", ContextWindow: 128000, MaxOutputTokens: 16384},
		{ID: "gpt-4o-mini", ContextWindow: 128000, MaxOutputTokens: 16384},
	}
}

func pc(baseURL string) config.ProviderConfig {
	return config.ProviderConfig{Type: "openai_compatible", BaseURL: baseURL}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := Cache{Providers: map[string]Entry{
		"openai": {ConfigHash: HashProvider(pc("https://a/v1")), Models: sample(), FetchedAt: time.Now()},
	}}
	if err := Save(dir, c); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := Load(dir)
	models, ok := got.Lookup("openai", pc("https://a/v1"), DefaultTTL, time.Now())
	if !ok {
		t.Fatal("cache miss after round trip")
	}
	if len(models) != 2 || models[0].ContextWindow != 128000 {
		t.Errorf("limits lost in round trip: %+v", models)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	c := Load(t.TempDir())
	if len(c.Providers) != 0 {
		t.Errorf("want empty cache, got %d entries", len(c.Providers))
	}
}

func TestLoadCorruptFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Cache{Providers: map[string]Entry{}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(cachePath(dir), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if c := Load(dir); len(c.Providers) != 0 {
		t.Errorf("corrupt cache should load empty, got %d entries", len(c.Providers))
	}
}

func TestLookupMissesWhenBaseURLChanges(t *testing.T) {
	c := Cache{Providers: map[string]Entry{
		"p": {ConfigHash: HashProvider(pc("https://old/v1")), Models: sample(), FetchedAt: time.Now()},
	}}
	if _, ok := c.Lookup("p", pc("https://new/v1"), DefaultTTL, time.Now()); ok {
		t.Error("changing the base URL must invalidate the entry")
	}
}

func TestLookupMissesWhenKeyChanges(t *testing.T) {
	withKey := func(k string) config.ProviderConfig {
		p := pc("https://a/v1")
		p.APIKey = k
		return p
	}
	c := Cache{Providers: map[string]Entry{
		"p": {ConfigHash: HashProvider(withKey("sk-old")), Models: sample(), FetchedAt: time.Now()},
	}}
	if _, ok := c.Lookup("p", withKey("sk-new"), DefaultTTL, time.Now()); ok {
		t.Error("changing the API key must invalidate the entry")
	}
}

func TestLookupMissesWhenKeyEnvChanges(t *testing.T) {
	withEnv := func(e string) config.ProviderConfig {
		p := pc("https://a/v1")
		p.APIKeyEnv = e
		return p
	}
	c := Cache{Providers: map[string]Entry{
		"p": {ConfigHash: HashProvider(withEnv("OLD_KEY")), Models: sample(), FetchedAt: time.Now()},
	}}
	if _, ok := c.Lookup("p", withEnv("NEW_KEY"), DefaultTTL, time.Now()); ok {
		t.Error("changing api_key_env must invalidate the entry")
	}
}

func TestLookupMissesAfterTTL(t *testing.T) {
	now := time.Now()
	c := Cache{Providers: map[string]Entry{
		"p": {ConfigHash: HashProvider(pc("https://a/v1")), Models: sample(), FetchedAt: now.Add(-25 * time.Hour)},
	}}
	if _, ok := c.Lookup("p", pc("https://a/v1"), DefaultTTL, now); ok {
		t.Error("entry older than the TTL must be treated as missing")
	}
}

func TestLookupOtherProvidersUnaffected(t *testing.T) {
	now := time.Now()
	c := Cache{Providers: map[string]Entry{
		"stale": {ConfigHash: HashProvider(pc("https://old/v1")), Models: sample(), FetchedAt: now},
		"fresh": {ConfigHash: HashProvider(pc("https://b/v1")), Models: sample(), FetchedAt: now},
	}}
	if _, ok := c.Lookup("stale", pc("https://new/v1"), DefaultTTL, now); ok {
		t.Error("stale entry should miss")
	}
	if _, ok := c.Lookup("fresh", pc("https://b/v1"), DefaultTTL, now); !ok {
		t.Error("unrelated provider must still hit")
	}
}

func TestCacheFileNeverContainsKeyMaterial(t *testing.T) {
	dir := t.TempDir()
	p := pc("https://a/v1")
	p.APIKey = "sk-super-secret-value"
	c := Cache{Providers: map[string]Entry{
		"p": {ConfigHash: HashProvider(p), Models: sample(), FetchedAt: time.Now()},
	}}
	if err := Save(dir, c); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(cachePath(dir))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "sk-super-secret-value") {
		t.Fatalf("cache file contains key material:\n%s", data)
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deeper")
	if err := Save(dir, Cache{Providers: map[string]Entry{}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(cachePath(dir)); err != nil {
		t.Errorf("cache file not created: %v", err)
	}
}
