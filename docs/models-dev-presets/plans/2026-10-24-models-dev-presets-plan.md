# models.dev preset enrichment — implementation plan

Spec: [`../feature-spec.md`](../feature-spec.md)
Base commit: `2325ca739312aa1317e27c2de666ea6bceab8d4e` (`main`)
Execution: inline, task-by-task, one commit per task.

## Goal

A successful `/connect` creates a preset for **every model the provider
reports**, enriched from the public models.dev catalog, and reconnecting
refreshes catalog-managed fields without overwriting user edits.

## Non-goals

No searchable catalog, recommendations, background sync, automatic role
assignment, bulk `/api/show` probing, preset deletion, tiered/audio pricing, or
runtime third-party limits table. models.dev never clamps a live request.

## Assumptions

- models.dev's `api.json` shape is as observed on 2026-10-24: a top-level
  object keyed by provider id, each with `models` keyed by model id, and each
  model carrying `limit.{context,output}`, `cost.{input,output,reasoning,
  cache_read,cache_write}`, and `tool_call`.
- The reviewed binding table is exactly: `gemini`→`google`,
  `together`→`togetherai`, `fireworks`→`fireworks-ai`,
  `kimi` (base URL prefix `https://api.kimi.com/coding`)→`kimi-code-plan-cn`,
  `opencode-go`→`opencode-go`, `ollama-cloud`→`ollama-cloud`. All six ids were
  confirmed present in the live document.
- `pricing.ModelPricing` rates are US cents per million tokens, so a
  USD-per-million rate converts with `round(usd * 100)`.
- The connect wizard's `Opts.DataDir` is the same data directory the TUI uses
  for the model cache (`m.modelCacheDir`).

## Contracts

These declarations are shared across tasks. Later tasks may rely on them
verbatim.

```go
// internal/llm/modelcatalog/catalog.go
package modelcatalog

type Catalog struct {
	Providers map[string]Provider `json:"providers"`
}

type Provider struct {
	ID     string           `json:"id"`
	Models map[string]Model `json:"models"`
}

type Model struct {
	ID       string `json:"id"`
	Limit    Limit  `json:"limit"`
	Cost     Cost   `json:"cost"`
	ToolCall *bool  `json:"tool_call,omitempty"`
}

type Limit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	Reasoning  float64 `json:"reasoning"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

func Decode(r io.Reader) (*Catalog, error)
func (c *Catalog) Lookup(catalogProvider, modelID string) (Model, bool)

type Binding struct {
	Provider string
	Catalog  string
	BaseURL  string
}

func Bindings() []Binding
func ResolveBinding(providerName, baseURL string) (string, bool)
```

```go
// internal/llm/modelcatalog/cache.go
package modelcatalog

const (
	DefaultURL      = "https://models.dev/api.json"
	CacheTTL        = 24 * time.Hour
	maxCatalogBytes = 32 << 20
)

type Cache struct {
	Dir    string
	URL    string
	Now    func() time.Time
	Client *http.Client
}

func NewCache(dataDir string) *Cache
func (c *Cache) Load(ctx context.Context) (*Catalog, error)
```

```go
// internal/app/presetsetup/resolve.go
package presetsetup

type Source string

const (
	SourceProvider  Source = "provider"
	SourceModelsDev Source = "models.dev"
	SourceDefault   Source = "default"
	SourceUnknown   Source = "unknown"
)

type Metadata struct {
	ContextWindow   int
	ContextSource   Source
	MaxOutputTokens int
	OutputSource    Source
	ToolCalling     *bool
	ToolSource      Source
	Pricing         *pricing.ModelPricing
	PricingSource   Source
}

func Resolve(providerName, baseURL, modelID string, reported schema.ModelInfo, cat *modelcatalog.Catalog) Metadata
func CentsFromUSDPerMTok(usd float64) (int64, bool)
```

```go
// internal/app/presetsetup/provenance.go
package presetsetup

const ProvenanceVersion = 1

type Provenance struct {
	Version int                         `json:"version"`
	Presets map[string]PresetProvenance `json:"presets"`
}

type PresetProvenance struct {
	CatalogProvider string                `json:"catalog_provider"`
	CatalogModel    string                `json:"catalog_model"`
	Fields          map[string]FieldState `json:"fields"`
}

type FieldState struct {
	Source string `json:"source"`
	Value  string `json:"value"`
}

func ProvenancePath(dataDir string) string
func LoadProvenance(dataDir string) (*Provenance, error)
func SaveProvenance(dataDir string, p *Provenance) error
```

```go
// internal/app/presetsetup/apply.go
package presetsetup

type Result struct {
	Prepared int
	Matched  int
	Fallback int
}

func Apply(cfg *config.Config, providerName, baseURL string, models []schema.ModelInfo, cat *modelcatalog.Catalog, prov *Provenance) Result
```

```go
// internal/app/config/types.go
type PrivacyConfig struct {
	RemoteProvidersAllowed bool  `toml:"remote_providers_allowed"`
	RedactSecrets          bool  `toml:"redact_secrets"`
	IncludeGitignoredFiles bool  `toml:"include_gitignored_files"`
	// ModelsDevEnrichment is tri-state: nil asks before the first fetch,
	// true allows it, false denies it.
	ModelsDevEnrichment *bool `toml:"models_dev_enrichment"`
}
```

```go
// internal/app/tui/connect/connect.go
type DoneMsg struct {
	Provider          string
	Model             string
	ProviderCfg       config.ProviderConfig
	EnabledRemote     bool
	ContextWindow     int
	MaxOutputTokens   int
	ToolCalling       *bool
	Presets           map[string]routing.ModelPreset
	Provenance        *presetsetup.Provenance
	EnrichmentConsent *bool
}
```

---

## Task 1: modelcatalog — decode, bindings, exact lookup

**Goal.** A new package decodes the models.dev document, maps Marshal provider
names to catalog ids, and looks models up exactly.

**Files.**
- `internal/llm/modelcatalog/catalog.go` (new)
- `internal/llm/modelcatalog/catalog_test.go` (new)

**Steps.** Create `catalog.go` with exactly this content:

```go
// Package modelcatalog decodes the public models.dev catalog and maps Marshal
// provider names onto its provider ids. It is a setup-time service: nothing
// here feeds runtime limits or prompts.
package modelcatalog

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Catalog is the decoded models.dev api.json document.
type Catalog struct {
	Providers map[string]Provider `json:"providers"`
}

// Provider is one models.dev provider entry.
type Provider struct {
	ID     string           `json:"id"`
	Models map[string]Model `json:"models"`
}

// Model is one models.dev model entry.
type Model struct {
	ID       string `json:"id"`
	Limit    Limit  `json:"limit"`
	Cost     Cost   `json:"cost"`
	ToolCall *bool  `json:"tool_call,omitempty"`
}

// Limit is a model's context and output ceilings in tokens.
type Limit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// Cost holds USD per million tokens, exactly as models.dev publishes them.
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	Reasoning  float64 `json:"reasoning"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

type rawCatalog map[string]rawProvider

type rawProvider struct {
	ID     string              `json:"id"`
	Models map[string]rawModel `json:"models"`
}

type rawModel struct {
	ID       string   `json:"id"`
	Limit    rawLimit `json:"limit"`
	Cost     rawCost  `json:"cost"`
	ToolCall *bool    `json:"tool_call"`
}

type rawLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

type rawCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	Reasoning  float64 `json:"reasoning"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// Decode reads a models.dev api.json document. Unknown fields are ignored and
// empty provider or model ids are skipped; a malformed document is an error.
func Decode(r io.Reader) (*Catalog, error) {
	var raw rawCatalog
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode models.dev catalog: %w", err)
	}
	cat := &Catalog{Providers: make(map[string]Provider, len(raw))}
	for id, rp := range raw {
		if id == "" {
			continue
		}
		p := Provider{ID: id, Models: make(map[string]Model, len(rp.Models))}
		if rp.ID != "" {
			p.ID = rp.ID
		}
		for mid, rm := range rp.Models {
			if mid == "" {
				continue
			}
			m := Model{
				ID: mid,
				Limit: Limit{
					Context: rm.Limit.Context,
					Output:  rm.Limit.Output,
				},
				Cost: Cost{
					Input:      rm.Cost.Input,
					Output:     rm.Cost.Output,
					Reasoning:  rm.Cost.Reasoning,
					CacheRead:  rm.Cost.CacheRead,
					CacheWrite: rm.Cost.CacheWrite,
				},
				ToolCall: rm.ToolCall,
			}
			if rm.ID != "" {
				m.ID = rm.ID
			}
			p.Models[mid] = m
		}
		cat.Providers[id] = p
	}
	return cat, nil
}

// Lookup returns the model for an exact, case-sensitive provider and model id.
func (c *Catalog) Lookup(catalogProvider, modelID string) (Model, bool) {
	if c == nil {
		return Model{}, false
	}
	p, ok := c.Providers[catalogProvider]
	if !ok {
		return Model{}, false
	}
	m, ok := p.Models[modelID]
	return m, ok
}

// Binding maps a Marshal provider name to a models.dev provider id. BaseURL,
// when set, is a required prefix of the configured endpoint.
type Binding struct {
	Provider string
	Catalog  string
	BaseURL  string
}

// bindings is the reviewed mapping table. Add an entry only after checking the
// models.dev provider id by hand; never infer one from a name.
var bindings = []Binding{
	{Provider: "gemini", Catalog: "google"},
	{Provider: "together", Catalog: "togetherai"},
	{Provider: "fireworks", Catalog: "fireworks-ai"},
	{Provider: "kimi", Catalog: "kimi-code-plan-cn", BaseURL: "https://api.kimi.com/coding"},
	{Provider: "opencode-go", Catalog: "opencode-go"},
	{Provider: "ollama-cloud", Catalog: "ollama-cloud"},
}

// Bindings returns a copy of the reviewed provider binding table.
func Bindings() []Binding {
	out := make([]Binding, len(bindings))
	copy(out, bindings)
	return out
}

// ResolveBinding returns the models.dev provider id for a Marshal provider. A
// binding with a BaseURL applies only when the configured endpoint starts with
// it. A provider with no binding, or with conflicting bindings, is unbound.
func ResolveBinding(providerName, baseURL string) (string, bool) {
	found := ""
	for _, b := range bindings {
		if b.Provider != providerName {
			continue
		}
		if b.BaseURL != "" && !strings.HasPrefix(baseURL, b.BaseURL) {
			continue
		}
		if found != "" && found != b.Catalog {
			return "", false
		}
		found = b.Catalog
	}
	return found, found != ""
}
```

Create `catalog_test.go` with these tests:

- `TestDecodeReadsLimitsCostAndToolCall` — decode a two-provider fixture and
  assert `Lookup("google", "gemini-3-pro")` returns context 1048576, output
  65536, input 1.25, `ToolCall` true.
- `TestDecodeIgnoresUnknownFields` — a model with `"reasoning":true` and
  `"modalities":{…}` decodes without error.
- `TestLookupIsExactAndCaseSensitive` — `Lookup("google", "Gemini-3-Pro")`
  misses; `Lookup("Google", …)` misses.
- `TestLookupKeepsTaggedModelIDs` — a model id `deepseek-v4.1-flash:1017`
  resolves only with the exact tag.
- `TestResolveBindingUsesReviewedTable` — `gemini`→`google`,
  `together`→`togetherai`, `fireworks`→`fireworks-ai`,
  `opencode-go`→`opencode-go`, `ollama-cloud`→`ollama-cloud`.
- `TestResolveBindingRequiresBaseURLForKimi` — `kimi` with
  `https://api.kimi.com/coding/v1` resolves to `kimi-code-plan-cn`; `kimi`
  with `https://api.moonshot.ai/v1` is unbound.
- `TestResolveBindingRejectsUnknownProvider` — `my-custom-endpoint` is unbound.

**Verify.**

```bash
go test ./internal/llm/modelcatalog/ -count=1
```

Expected: all tests pass.

---

## Task 2: modelcatalog cache — 24h TTL, ETag, stale fallback

**Goal.** The catalog is cached under `<dataDir>/models-dev/catalog.json`,
reused for 24 hours, revalidated with `If-None-Match`, and a failed refresh
falls back to a valid stale copy.

**Files.**
- `internal/llm/modelcatalog/cache.go` (new)
- `internal/llm/modelcatalog/cache_test.go` (new)

**Steps.** Create `cache.go` with exactly this content:

```go
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
		_ = writeCache(path, cached)
		return cached.Catalog, nil
	}
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
	if f.Catalog == nil {
		return nil, errors.New("catalog cache has no catalog")
	}
	return &f, nil
}

func writeCache(path string, f *cacheFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
```

Create `cache_test.go` using `httptest.NewServer` and a fixed `Now`. Tests:

- `TestLoadUsesFreshCacheWithoutRequest` — write a cache stamped `now`, point
  `URL` at a server that fails the test if called, assert `Load` returns it.
- `TestLoadRefreshesStaleCache` — cache stamped `now-25h`, server returns a
  new document with `ETag: "v2"`, assert the new model is present and the
  cache file's `ETag` is `v2`.
- `TestLoadRevalidatesWithETag` — stale cache with `ETag: "v1"`, server
  asserts `If-None-Match: v1` and returns `304`, assert the cached catalog is
  returned and `FetchedAt` moved forward.
- `TestLoadFallsBackToStaleCacheOnError` — stale cache, server returns `500`,
  assert the stale catalog is returned with no error.
- `TestLoadReturnsErrorWithoutCache` — empty dir, server returns `500`,
  assert an error.
- `TestLoadRejectsCorruptCache` — write `{not json`, server returns a valid
  document, assert the fresh document is used.

**Verify.**

```bash
go test ./internal/llm/modelcatalog/ -count=1
```

Expected: all tests pass.

---

## Task 3: presetsetup — metadata resolution and pricing conversion

**Goal.** One function resolves a model's context, output, tool support and
pricing with per-field provenance, using provider → models.dev → defaults.

**Files.**
- `internal/app/presetsetup/resolve.go` (new)
- `internal/app/presetsetup/resolve_test.go` (new)

**Steps.** Create `resolve.go` with exactly this content:

```go
// Package presetsetup resolves model metadata for bulk preset creation and
// records which fields models.dev manages, so reconnects refresh catalog
// values without overwriting user edits.
package presetsetup

import (
	"math"

	"marshal/internal/llm/modelcatalog"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// Source labels where a resolved field came from.
type Source string

const (
	SourceProvider  Source = "provider"
	SourceModelsDev Source = "models.dev"
	SourceDefault   Source = "default"
	SourceUnknown   Source = "unknown"
)

// Metadata is one model's resolved metadata with per-field provenance.
type Metadata struct {
	ContextWindow   int
	ContextSource   Source
	MaxOutputTokens int
	OutputSource    Source
	ToolCalling     *bool
	ToolSource      Source
	Pricing         *pricing.ModelPricing
	PricingSource   Source
}

// Resolve computes metadata for modelID. Provider-reported values win, then
// the models.dev catalog, then Marshal's documented defaults. Fields resolve
// independently.
func Resolve(providerName, baseURL, modelID string, reported schema.ModelInfo, cat *modelcatalog.Catalog) Metadata {
	local := routing.IsLocalProvider(baseURL)
	defCtx, defOut := routing.DefaultModelLimits(local)

	m := Metadata{
		ContextWindow:   reported.ContextWindow,
		ContextSource:   SourceProvider,
		MaxOutputTokens: reported.MaxOutputTokens,
		OutputSource:    SourceProvider,
		ToolCalling:     reported.ToolCalling,
		ToolSource:      SourceUnknown,
	}
	if m.ContextWindow == 0 {
		m.ContextWindow, m.ContextSource = defCtx, SourceDefault
	}
	if m.MaxOutputTokens == 0 {
		m.MaxOutputTokens, m.OutputSource = defOut, SourceDefault
	}
	if m.ToolCalling != nil {
		m.ToolSource = SourceProvider
	}

	catalogProvider, ok := modelcatalog.ResolveBinding(providerName, baseURL)
	if !ok {
		return m
	}
	cm, ok := cat.Lookup(catalogProvider, modelID)
	if !ok {
		return m
	}
	if m.ContextSource != SourceProvider && cm.Limit.Context > 0 {
		m.ContextWindow, m.ContextSource = cm.Limit.Context, SourceModelsDev
	}
	if m.OutputSource != SourceProvider && cm.Limit.Output > 0 {
		m.MaxOutputTokens, m.OutputSource = cm.Limit.Output, SourceModelsDev
	}
	if m.ToolCalling == nil && cm.ToolCall != nil {
		v := *cm.ToolCall
		m.ToolCalling, m.ToolSource = &v, SourceModelsDev
	}
	if p, ok := pricingFromCost(cm.Cost); ok {
		m.Pricing, m.PricingSource = p, SourceModelsDev
	}
	return m
}

// CentsFromUSDPerMTok converts a USD-per-million-token rate to Marshal's
// integer cents per million, rounding to the nearest cent. Negative, NaN, and
// infinite values are rejected.
func CentsFromUSDPerMTok(usd float64) (int64, bool) {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return 0, false
	}
	return int64(math.Round(usd * 100)), true
}

func pricingFromCost(c modelcatalog.Cost) (*pricing.ModelPricing, bool) {
	if c.Input == 0 && c.Output == 0 && c.Reasoning == 0 && c.CacheRead == 0 && c.CacheWrite == 0 {
		return nil, false
	}
	in, okIn := CentsFromUSDPerMTok(c.Input)
	out, okOut := CentsFromUSDPerMTok(c.Output)
	reason, okReason := CentsFromUSDPerMTok(c.Reasoning)
	read, okRead := CentsFromUSDPerMTok(c.CacheRead)
	write, okWrite := CentsFromUSDPerMTok(c.CacheWrite)
	if !okIn && !okOut && !okReason && !okRead && !okWrite {
		return nil, false
	}
	p := &pricing.ModelPricing{}
	if okIn {
		p.InputPerMTokCents = in
	}
	if okOut {
		p.OutputPerMTokCents = out
	}
	if okReason {
		p.ReasoningPerMTokCents = reason
	}
	if okRead {
		p.CacheReadPerMTokCents = read
	}
	if okWrite {
		p.CacheWritePerMTokCents = write
	}
	return p, true
}
```

Create `resolve_test.go` with these tests:

- `TestResolvePrefersProviderReported` — reported context 200000, catalog
  1048576; assert 200000 and `SourceProvider`.
- `TestResolveFallsBackToCatalog` — reported zero, catalog 1048576/65536;
  assert both `SourceModelsDev`.
- `TestResolveFallsBackToDefaults` — no reported values, no catalog match;
  assert `SourceDefault` and the remote pair (1_000_000 / 128_000).
- `TestResolveUsesLocalDefaults` — base URL `http://localhost:11434/v1`, no
  reported values; assert 8_192 / 4_096.
- `TestResolveUnboundProviderSkipsCatalog` — provider `my-endpoint` with a
  catalog that has a same-named model; assert `SourceDefault`.
- `TestResolveKeepsUnknownToolSupport` — no reported and no catalog
  `tool_call`; assert `ToolCalling == nil` and `SourceUnknown`.
- `TestResolveTakesFalseToolSupport` — catalog `tool_call: false`; assert
  non-nil false and `SourceModelsDev`.
- `TestCentsFromUSDPerMTokRounds` — 1.25→125, 0.0004→0, 2.5→250.
- `TestCentsFromUSDPerMTokRejectsInvalid` — -1, NaN, +Inf all return false.
- `TestResolveConvertsPricing` — catalog cost input 1.4, output 4.4,
  cache_read 0.26; assert 140, 440, 26 cents.
- `TestResolveLeavesPricingUnsetWhenAbsent` — catalog model with no cost;
  assert `Pricing == nil`.

**Verify.**

```bash
go test ./internal/app/presetsetup/ -count=1
```

Expected: all tests pass.

---

## Task 4: presetsetup — provenance sidecar and bulk apply

**Goal.** `Apply` creates a preset for every reported model, refreshes only
fields whose current value still equals the last catalog-applied value, and
never touches a preset without provenance.

**Files.**
- `internal/app/presetsetup/provenance.go` (new)
- `internal/app/presetsetup/apply.go` (new)
- `internal/app/presetsetup/apply_test.go` (new)

**Steps.** Create `provenance.go` with exactly this content:

```go
package presetsetup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ProvenanceVersion is the sidecar schema version.
const ProvenanceVersion = 1

// Provenance records which preset fields models.dev last applied, so a
// reconnect can refresh them without overwriting user edits.
type Provenance struct {
	Version int                         `json:"version"`
	Presets map[string]PresetProvenance `json:"presets"`
}

// PresetProvenance is one preset's catalog binding and managed fields.
type PresetProvenance struct {
	CatalogProvider string                `json:"catalog_provider"`
	CatalogModel    string                `json:"catalog_model"`
	Fields          map[string]FieldState `json:"fields"`
}

// FieldState is the last value Marshal applied to one field.
type FieldState struct {
	Source string `json:"source"`
	Value  string `json:"value"`
}

// ProvenancePath returns the sidecar path under dataDir.
func ProvenancePath(dataDir string) string {
	return filepath.Join(dataDir, "models-dev", "preset-provenance.json")
}

// LoadProvenance reads the sidecar. A missing file is an empty provenance; a
// corrupt file is an error so callers can fail safe.
func LoadProvenance(dataDir string) (*Provenance, error) {
	data, err := os.ReadFile(ProvenancePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return &Provenance{Version: ProvenanceVersion, Presets: map[string]PresetProvenance{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var p Provenance
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decode preset provenance: %w", err)
	}
	if p.Presets == nil {
		p.Presets = map[string]PresetProvenance{}
	}
	return &p, nil
}

// SaveProvenance writes the sidecar atomically.
func SaveProvenance(dataDir string, p *Provenance) error {
	if p == nil {
		return nil
	}
	path := ProvenancePath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

Create `apply.go` with exactly this content:

```go
package presetsetup

import (
	"strconv"

	"marshal/internal/app/config"
	"marshal/internal/llm/modelcatalog"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// Result summarises a bulk apply.
type Result struct {
	Prepared int
	Matched  int
	Fallback int
}

// Apply creates or refreshes a preset for every reported model. It never
// deletes presets and never touches a preset without provenance.
func Apply(cfg *config.Config, providerName, baseURL string, models []schema.ModelInfo, cat *modelcatalog.Catalog, prov *Provenance) Result {
	var res Result
	if cfg.Models.Presets == nil {
		cfg.Models.Presets = map[string]routing.ModelPreset{}
	}
	if prov == nil {
		prov = &Provenance{Version: ProvenanceVersion, Presets: map[string]PresetProvenance{}}
	}
	if prov.Presets == nil {
		prov.Presets = map[string]PresetProvenance{}
	}
	catalogProvider, bound := modelcatalog.ResolveBinding(providerName, baseURL)
	for _, mi := range models {
		if mi.ID == "" {
			continue
		}
		name := providerName + "/" + mi.ID
		meta := Resolve(providerName, baseURL, mi.ID, mi, cat)
		preset, exists := cfg.Models.Presets[name]
		if !exists {
			preset = routing.ModelPreset{
				Name:      name,
				Provider:  providerName,
				Model:     mi.ID,
				LocalOnly: routing.IsLocalProvider(baseURL),
			}
		} else if _, managed := prov.Presets[name]; !managed {
			// A preset created before this feature (or by hand) has no
			// provenance: never touch it.
			continue
		}
		pp := prov.Presets[name]
		if pp.Fields == nil {
			pp.Fields = map[string]FieldState{}
		}
		preset.ContextWindow = applyIntField(preset.ContextWindow, pp.Fields, "context_window", meta.ContextWindow, meta.ContextSource)
		preset.MaxOutputTokens = applyIntField(preset.MaxOutputTokens, pp.Fields, "max_output_tokens", meta.MaxOutputTokens, meta.OutputSource)
		preset.ToolCalling = applyToolField(preset.ToolCalling, pp.Fields, meta.ToolCalling, meta.ToolSource)
		applyPricing(&preset, pp.Fields, meta.Pricing, meta.PricingSource)
		if bound {
			pp.CatalogProvider = catalogProvider
			pp.CatalogModel = mi.ID
		}
		prov.Presets[name] = pp
		cfg.Models.Presets[name] = preset
		res.Prepared++
		if meta.ContextSource == SourceModelsDev || meta.OutputSource == SourceModelsDev ||
			meta.ToolSource == SourceModelsDev || meta.PricingSource == SourceModelsDev {
			res.Matched++
		} else {
			res.Fallback++
		}
	}
	return res
}

// applyIntField updates a managed integer field. A field whose current value
// no longer matches the last applied value is a user edit: it is preserved and
// dropped from management. A non-positive next value never erases a value.
func applyIntField(current int, fields map[string]FieldState, key string, next int, source Source) int {
	prev, managed := fields[key]
	if managed && prev.Value != strconv.Itoa(current) {
		delete(fields, key)
		return current
	}
	if next <= 0 {
		return current
	}
	fields[key] = FieldState{Source: string(source), Value: strconv.Itoa(next)}
	return next
}

// applyToolField updates tool-calling support with the same compare-before-
// refresh rule.
func applyToolField(current string, fields map[string]FieldState, next *bool, source Source) string {
	prev, managed := fields["tool_calling"]
	if managed && prev.Value != current {
		delete(fields, "tool_calling")
		return current
	}
	if next == nil {
		return current
	}
	v := "none"
	if *next {
		v = "native"
	}
	fields["tool_calling"] = FieldState{Source: string(source), Value: v}
	return v
}

// applyPricing updates each pricing component independently. A preset whose
// Pricing holds an unrecognised shape is user-owned and is left alone.
func applyPricing(preset *routing.ModelPreset, fields map[string]FieldState, next *pricing.ModelPricing, source Source) {
	if next == nil {
		return
	}
	cur, ok := preset.Pricing.(*pricing.ModelPricing)
	if preset.Pricing != nil && !ok {
		return
	}
	if cur == nil {
		cur = &pricing.ModelPricing{}
	}
	cur.InputPerMTokCents = applyInt64Field(cur.InputPerMTokCents, fields, "pricing.input", next.InputPerMTokCents, source)
	cur.OutputPerMTokCents = applyInt64Field(cur.OutputPerMTokCents, fields, "pricing.output", next.OutputPerMTokCents, source)
	cur.ReasoningPerMTokCents = applyInt64Field(cur.ReasoningPerMTokCents, fields, "pricing.reasoning", next.ReasoningPerMTokCents, source)
	cur.CacheReadPerMTokCents = applyInt64Field(cur.CacheReadPerMTokCents, fields, "pricing.cache_read", next.CacheReadPerMTokCents, source)
	cur.CacheWritePerMTokCents = applyInt64Field(cur.CacheWritePerMTokCents, fields, "pricing.cache_write", next.CacheWritePerMTokCents, source)
	preset.Pricing = cur
}

func applyInt64Field(current int64, fields map[string]FieldState, key string, next int64, source Source) int64 {
	prev, managed := fields[key]
	if managed && prev.Value != strconv.FormatInt(current, 10) {
		delete(fields, key)
		return current
	}
	fields[key] = FieldState{Source: string(source), Value: strconv.FormatInt(next, 10)}
	return next
}
```

Create `apply_test.go` with these tests:

- `TestApplyCreatesEveryReportedModel` — three reported models, catalog match
  for two; assert three presets exist and `Result{Prepared:3, Matched:2,
  Fallback:1}`.
- `TestApplyAddsNoCatalogOnlyModels` — catalog has a model the provider did
  not report; assert no preset for it.
- `TestApplyRefreshesUneditedField` — existing preset with provenance whose
  context equals the recorded value; new catalog value differs; assert the
  preset updates.
- `TestApplyPreservesEditedField` — existing preset whose context differs from
  the recorded value; assert it is preserved and the field is removed from
  provenance.
- `TestApplyRefreshesOtherFieldsIndependently` — edited context, unedited
  output; assert output updates while context stays.
- `TestApplyLeavesPreFeaturePresetUntouched` — existing preset with no
  provenance entry; assert unchanged and no provenance added.
- `TestApplyRetainsValueWhenMetadataOmitted` — previously managed output, new
  catalog omits output; assert the old value stays.
- `TestApplyNeverDeletesUnreportedPreset` — preset for a model no longer
  reported; assert it survives.
- `TestApplyRecordsCatalogBinding` — assert `CatalogProvider`/`CatalogModel`
  are recorded for a bound provider.
- `TestApplySkipsUnboundProvider` — unbound provider; assert presets are
  created with defaults and `CatalogProvider` stays empty.
- `TestLoadProvenanceMissingFileIsEmpty` — assert no error and empty map.
- `TestLoadProvenanceCorruptFileErrors` — write `{`, assert an error.
- `TestSaveProvenanceRoundTrips` — save then load, assert equality.

**Verify.**

```bash
go test ./internal/app/presetsetup/ -count=1
```

Expected: all tests pass.

---

## Task 5: config — tri-state `privacy.models_dev_enrichment`

**Goal.** The consent preference is a tri-state user-global setting: absent
asks, `true` allows, `false` denies. It round-trips through load, merge, save
and the Settings privacy frame.

**Files.**
- `internal/app/config/types.go`
- `internal/app/config/file_types.go`
- `internal/app/config/merge.go`
- `internal/app/config/save.go`
- `internal/app/tui/settings/frames_basic.go`
- `internal/app/config/config_test.go` (or the existing privacy test file)
- `internal/app/tui/settings/frames_basic_test.go`

**Steps.**

1. In `internal/app/config/types.go`, add to `PrivacyConfig` (anchor:
   `type PrivacyConfig struct` at line 614):

   ```go
   // ModelsDevEnrichment is tri-state: nil asks before the first fetch,
   // true allows it, false denies it.
   ModelsDevEnrichment *bool `toml:"models_dev_enrichment"`
   ```

2. In `internal/app/config/file_types.go`, add to `filePrivacy` (anchor:
   `type filePrivacy struct` at line 96):

   ```go
   ModelsDevEnrichment *bool `toml:"models_dev_enrichment"`
   ```

3. In `internal/app/config/merge.go`, inside `if file.Privacy != nil`
   (anchor: line 61), add:

   ```go
   if file.Privacy.ModelsDevEnrichment != nil {
       cfg.Privacy.ModelsDevEnrichment = file.Privacy.ModelsDevEnrichment
   }
   ```

   Do **not** use `set` here: `set` copies the pointed-to value and would
   collapse the tri-state.

4. In `internal/app/config/save.go`, inside the `privacy := &filePrivacy{}`
   block (anchor: line 254), add after the three `putKey` calls:

   ```go
   if cfg.Privacy.ModelsDevEnrichment != nil {
       privacy.ModelsDevEnrichment = cfg.Privacy.ModelsDevEnrichment
   }
   ```

   This emits the key only when the user has answered, so an unanswered
   preference stays absent.

5. In `internal/app/tui/settings/frames_basic.go`, add a fourth field to
   `privacyFrame` after the `privacy.include_gitignored` entry:

   ```go
   func() *field {
       f := NewField("privacy.models_dev_enrichment", "models.dev enrichment", KindEnum)
       SetFieldDesc(f, "enrich new provider presets from the public models.dev catalog")
       SetFieldGetStr(f, func() string {
           if s.cfg.Privacy.ModelsDevEnrichment == nil {
               return "ask"
           }
           if *s.cfg.Privacy.ModelsDevEnrichment {
               return "allow"
           }
           return "deny"
       })
       SetFieldOptions(f, func() []string { return []string{"ask", "allow", "deny"} })
       SetFieldSetStr(f, func(v string) error {
           switch v {
           case "ask":
               s.cfg.Privacy.ModelsDevEnrichment = nil
           case "allow":
               b := true
               s.cfg.Privacy.ModelsDevEnrichment = &b
           case "deny":
               b := false
               s.cfg.Privacy.ModelsDevEnrichment = &b
           default:
               return fmt.Errorf("unknown models.dev enrichment value %q", v)
           }
           return nil
       })
       f.TomlPath = "privacy.models_dev_enrichment"
       return f
   }(),
   ```

   `fmt` is already imported in this file.

6. Add tests:
   - `TestPrivacyModelsDevEnrichmentTriState` in the config package: load a
     file with the key absent → nil; `true` → non-nil true; `false` → non-nil
     false.
   - `TestSaveOmitsUnansweredModelsDevEnrichment` in the config package: save
     a config with nil and assert the key is absent from the written TOML.
   - `TestPrivacyFrameModelsDevEnrichmentCycles` in
     `frames_basic_test.go`: cycle the field through ask → allow → deny and
     assert the pointer state each time.

**Verify.**

```bash
go test ./internal/app/config/ ./internal/app/tui/settings/ -count=1
```

Expected: all tests pass.

---

## Task 6: connect — consent, bulk prepare, no limit confirmation

**Goal.** `/connect` asks for models.dev consent once, prepares a preset for
every reported model, applies the active model's Ollama probe automatically,
and finishes without a limit-confirmation screen.

**Files.**
- `internal/app/tui/connect/connect.go`
- `internal/app/tui/connect/connect_test.go`

**Steps.**

1. **Extend the step enum.** In the `const` block starting at `stepPickTemplate`
   (line 58), add `stepConsent` and `stepPreparing` after `stepPickModel`.
   Remove `stepConfirmLimits`.

2. **Extend `DoneMsg`** (line 349) with the three fields from Contracts:
   `Presets map[string]routing.ModelPreset`,
   `Provenance *presetsetup.Provenance`, `EnrichmentConsent *bool`.

3. **Add a prepared-result message** next to `DoneMsg`:

   ```go
   // PreparedMsg carries the bulk-prepared presets back to the wizard.
   type PreparedMsg struct {
       Presets    map[string]routing.ModelPreset
       Provenance *presetsetup.Provenance
       Result     presetsetup.Result
       Err        error
   }
   ```

4. **Replace the confirm-limits transition.** In `handlePickerPicked`
   (line 781), replace `return m.enterConfirmLimits()` with
   `return m.afterModelChosen()`.

5. **Add the new flow methods** near `enterSummary` (line 595):

   ```go
   // afterModelChosen asks for models.dev consent once, then prepares presets.
   func (m *Model) afterModelChosen() (*Model, tea.Cmd) {
       if m.cfg.Privacy.ModelsDevEnrichment == nil {
           return m.enterConsent()
       }
       return m.enterPrepare()
   }

   func (m *Model) enterConsent() (*Model, tea.Cmd) {
       m.step = stepConsent
       m.title = "models.dev enrichment"
       m.subtitle = "fetch public model metadata for every reported model?"
       m.footer = "[y] allow  [n] skip  [Esc] back"
       m.err = ""
       m.picker = nil
       return m, nil
   }

   func (m *Model) enterPrepare() (*Model, tea.Cmd) {
       m.step = stepPreparing
       m.title = "Preparing presets"
       m.subtitle = m.providerName
       m.footer = "please wait…"
       m.err = ""
       m.picker = nil
       return m, m.prepareCmd()
   }

   // reportedModels returns the provider's live model list, preferring the
   // discovered map so the all-providers flow works too.
   func (m *Model) reportedModels() []schema.ModelInfo {
       if models := m.discovered[m.providerName]; len(models) > 0 {
           return models
       }
       return m.models
   }

   // prepareCmd loads the catalog when allowed, applies the active model's
   // capability probe, and bulk-creates presets on a copy of the config.
   func (m *Model) prepareCmd() tea.Cmd {
       providerName := m.providerName
       baseURL := m.providerCfg.BaseURL
       modelChosen := m.modelChosen
       models := append([]schema.ModelInfo(nil), m.reportedModels()...)
       cfg := m.cfg
       // The preset map is shared with the TUI's live config. Copy it before
       // Apply mutates it, or a cancelled connect would still change presets.
       if cfg.Models.Presets != nil {
           presets := make(map[string]routing.ModelPreset, len(cfg.Models.Presets))
           for k, v := range cfg.Models.Presets {
               presets[k] = v
           }
           cfg.Models.Presets = presets
       }
       dataDir := m.dataDir
       allowed := m.cfg.Privacy.ModelsDevEnrichment != nil && *m.cfg.Privacy.ModelsDevEnrichment
       prov, err := provider.NewFromConfigWithAuth(providerName, m.providerCfg, dataDir, m.cfg.Agent.ThinkingBudgetMargin, "", m.auth)
       return func() tea.Msg {
           if err == nil {
               if prober, ok := prov.(provider.CapabilityProber); ok {
                   ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
                   caps := prober.ProbeCapabilities(ctx, modelChosen)
                   cancel()
                   for i := range models {
                       if models[i].ID != modelChosen {
                           continue
                       }
                       if caps.ToolCalling != nil {
                           models[i].ToolCalling = caps.ToolCalling
                       }
                       if caps.ContextWindow > 0 {
                           models[i].ContextWindow = caps.ContextWindow
                       }
                       break
                   }
               }
           }
           var cat *modelcatalog.Catalog
           if allowed {
               ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
               cat, _ = modelcatalog.NewCache(dataDir).Load(ctx)
               cancel()
           }
           provenance, _ := presetsetup.LoadProvenance(dataDir)
           res := presetsetup.Apply(&cfg, providerName, baseURL, models, cat, provenance)
           return PreparedMsg{
               Presets:    cfg.Models.Presets,
               Provenance: provenance,
               Result:     res,
           }
       }
   }
   ```

   `provider.CapabilityProber` is
   `ProbeCapabilities(ctx context.Context, model string) ModelCapabilities`
   (`internal/llm/provider/capabilities.go:22`).

6. **Handle `PreparedMsg`** in `Update` (line 190), before the
   `presetflow.CapabilityProbedMsg` case:

   ```go
   case PreparedMsg:
       if m.step != stepPreparing {
           return m, nil
       }
       m.prepared = msg
       m.enterSummary()
       return m, nil
   ```

   Add `prepared PreparedMsg` to the `Model` struct (line 95).

7. **Handle consent keys** in `handleKey` (line 976), in the `stepConsent`
   case:

   ```go
   case stepConsent:
       switch ks {
       case "y":
           b := true
           m.cfg.Privacy.ModelsDevEnrichment = &b
           return m.enterPrepare()
       case "n":
           b := false
           m.cfg.Privacy.ModelsDevEnrichment = &b
           return m.enterPrepare()
       case "esc":
           return m, m.back()
       }
       return m, nil
   ```

   Add a `stepConsent` case to `back()` (line 437) so Esc returns to model
   selection:

   ```go
   case stepConsent:
       m.enterPickModelStep(m.providerName)
   ```

   Use the existing `enterPickModelStep(m, m.providerName)` helper; do not
   call `advanceToPickModel`, which returns a command this signature cannot
   return.

8. **Remove the confirm-limits machinery.** Delete `enterConfirmLimits`,
   `probeCapabilities`, `cancelCapabilityProbe`, `finishCapabilityProbe`, the
   `presetflow.CapabilityProbedMsg` case in `Update`, the `stepConfirmLimits`
   branches in `View`, `handleKey`, and `tea.PasteMsg`, and the `confirm`,
   `detectingCaps`, `capProbeID`, `cancelCapProbe` fields. Remove the
   `presetflow` import if it becomes unused.

9. **Fix imports.** `connect.go` currently imports neither `fmt` nor the two
   new packages. Add `"fmt"` to the standard-library group, and
   `"marshal/internal/app/presetsetup"` and
   `"marshal/internal/llm/modelcatalog"` to the Marshal group. `context`,
   `time`, `routing`, `schema` and `provider` are already imported.

10. **Render the new steps.** In `View` (line 260), add:

   ```go
   case stepConsent:
       b.WriteString(mutedStyle().Render("models.dev publishes context limits, tool support and prices."))
       b.WriteString("\n")
       b.WriteString(mutedStyle().Render("Only the public catalog is downloaded; no keys or model ids are sent."))
   case stepPreparing:
       b.WriteString(mutedStyle().Render("preparing presets…"))
   ```

11. **Show counts in the summary.** In `renderSummary` (line 633), after the
    model line, add:

    ```go
    if m.prepared.Result.Prepared > 0 {
        b.WriteString(mutedStyle().Render("presets:  ") +
            titleStyle().Render(fmt.Sprintf("%d prepared · %d models.dev matches · %d using fallbacks",
                m.prepared.Result.Prepared, m.prepared.Result.Matched, m.prepared.Result.Fallback)) + "\n")
    }
    ```

11. **Carry the results in `done()`** (line 1228): add `Presets:
    m.prepared.Presets`, `Provenance: m.prepared.Provenance`,
    `EnrichmentConsent: m.cfg.Privacy.ModelsDevEnrichment` to the returned
    `DoneMsg`.

12. **Update tests** in `connect_test.go`:
    - Replace every test that drives `stepConfirmLimits` with the new flow:
      pick a model → consent (or pre-set the preference) → `PreparedMsg` →
      summary → `DoneMsg`.
    - `TestConnectAsksConsentOnce` — with `ModelsDevEnrichment == nil`, picking
      a model enters `stepConsent`; answering `y` sets the pointer true and
      enters `stepPreparing`.
    - `TestConnectSkipsConsentWhenAnswered` — with the pointer already set,
      picking a model goes straight to `stepPreparing`.
    - `TestConnectDoneCarriesPreparedPresets` — feed a `PreparedMsg` with two
      presets, confirm, and assert `DoneMsg.Presets` has both.
    - `TestConnectReachesSummaryWithoutConfirmScreen` — pick a model with the
      preference already answered, deliver `PreparedMsg`, and assert the step
      is `stepSummary` (there is no confirm-limits step to reach).

**Verify.**

```bash
go test ./internal/app/tui/connect/ -count=1
```

Expected: all tests pass.

---

## Task 7: applyConnectDone — save presets, provenance and consent

**Goal.** Completing `/connect` merges the prepared presets, saves them with
the provider, persists the consent answer, and writes the provenance sidecar
without failing the connect when provenance cannot be saved.

**Files.**
- `internal/app/tui/model.go`
- `internal/app/tui/model_test.go`

**Steps.**

1. In `applyConnectDone` (line 7445), after the `newCfg.Models.Presets` copy
   block and before the `presetflow.Materialize` call, merge the prepared
   presets:

   ```go
   for name, preset := range msg.Presets {
       if _, exists := newCfg.Models.Presets[name]; !exists {
           newCfg.Models.Presets[name] = preset
       }
   }
   ```

   Keep the existing `Materialize` call: it still creates or updates the
   active model's preset, including a manually typed model that the provider
   did not report.

2. After the successful `SaveUserConfigPresets` call (anchor: the
   `if err := config.SaveUserConfigPresets(...)` block), add:

   ```go
   if msg.EnrichmentConsent != nil {
       if err := config.SaveUserConfigValue(userPath, "privacy.models_dev_enrichment", *msg.EnrichmentConsent); err != nil {
           m.state.AddMessage(session.RoleSystem,
               fmt.Sprintf("⚠ Could not save models.dev preference: %v", err), session.ContentTypePlain)
       }
   }
   dataDir := m.modelCacheDir
   if dataDir == "" {
       dataDir = config.DataDir(home)
   }
   if msg.Provenance != nil {
       if err := presetsetup.SaveProvenance(dataDir, msg.Provenance); err != nil {
           m.state.AddMessage(session.RoleSystem,
               fmt.Sprintf("⚠ Presets saved, but models.dev provenance was not: %v", err), session.ContentTypePlain)
       }
   }
   ```

   The provenance failure is a warning, not a return: the presets are already
   saved and the next refresh fails safe.

3. Add tests in `model_test.go`:
   - `TestApplyConnectDoneMergesPreparedPresets` — a `DoneMsg` with two
     prepared presets and an active model; assert both are in the saved
     config and the active preset is set.
   - `TestApplyConnectDonePersistsConsent` — a `DoneMsg` with
     `EnrichmentConsent` true; assert the written user config contains
     `models_dev_enrichment = true`.
   - `TestApplyConnectDoneWarnsOnProvenanceFailure` — point the data dir at a
     path that cannot be created; assert the presets are still saved and a
     warning message is added.

**Verify.**

```bash
go test ./internal/app/tui/ -count=1
```

Expected: all tests pass.

---

## Task 8: full-project verification

**Goal.** The whole repository builds, vets, formats and tests cleanly.

**Files.** None.

**Steps.** Run, in order:

```bash
gofmt -l . | grep -v '^$'
CGO_ENABLED=1 go build ./...
go vet ./...
go test ./... -count=1
```

Expected: `gofmt -l` prints nothing; build, vet and tests pass. If a package
outside the touched set fails, confirm it also fails on the base commit before
changing anything.

**Verify.** The commands above are the verification.

---

## Integration notes

- The catalog is fetched only when `privacy.models_dev_enrichment` is `true`.
  An unanswered preference asks once in `/connect`; a denied preference makes
  no request at all.
- `presetsetup.Apply` never deletes a preset and never touches one without a
  provenance entry, so every preset created before this feature is inert.
- Provenance is written after the provider and preset config. A provenance
  write failure leaves the presets saved and disables future refreshes for
  those presets rather than risking a user edit.
- The active model's Ollama `/api/show` probe runs inside `prepareCmd` and
  feeds the reported `schema.ModelInfo` before `Apply`, so it wins the
  provider-precedence rule without a confirmation screen.
