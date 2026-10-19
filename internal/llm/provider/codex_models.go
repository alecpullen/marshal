package provider

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"time"

	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/schema"
)

// codexModelsPath is the catalog endpoint, appended to the provider base URL.
const codexModelsPath = "/codex/models"

// codexModelsClientVersion is sent as the client_version query parameter.
// The server gates the catalog on it: too old a version still gets HTTP 200
// but a legacy catalog — older models only, without context windows or
// reasoning levels. 0.160.0 was verified current on 2026-10-04 and already
// returned the legacy catalog by 2026-10-19, so the cutoff moves within weeks
// of a release. 0.163.0 is the current codex CLI release (2026-10-15). When
// Codex models go missing, bump this to the latest CLI release and watch for
// the legacy-catalog warning fetchLiveModels logs.
const codexModelsClientVersion = "0.163.0"

// codexModelEntry is one entry in the catalog's models array. Only the
// fields marshal consumes are decoded; the catalog carries much more
// (model_messages.instructions_template, input_modalities, shell_type, …)
// and unknown fields are ignored so catalog additions do not break us.
type codexModelEntry struct {
	Slug             string `json:"slug"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	ContextWindow    int    `json:"context_window"`
	MaxContextWindow int    `json:"max_context_window"`
	Visibility       string `json:"visibility"`
	SupportedInAPI   bool   `json:"supported_in_api"`
	Priority         int    `json:"priority"`
	// Reasoning controls are retained in ModelInfo and the model cache.
	DefaultReasoningLevel   string `json:"default_reasoning_level"`
	SupportedReasoningLevel []struct {
		Effort      string `json:"effort"`
		Description string `json:"description"`
	} `json:"supported_reasoning_levels"`
}

// codexModelsResponse is the catalog envelope.
type codexModelsResponse struct {
	Models []codexModelEntry `json:"models"`
}

// resolveModels returns the model list via the live → cache → static chain.
//
// It never returns an error: model listing is a convenience, and a provider
// whose catalog is unreachable should still offer its fallback list rather
// than failing the picker. Auth failures fall through silently for the same
// reason — the user learns about a missing login when they try to chat.
func (p *OpenAICodex) resolveModels(ctx context.Context) []schema.ModelInfo {
	if models, etag := p.fetchLiveModels(ctx); len(models) > 0 {
		p.saveModelsToCache(models, etag)
		return models
	}
	if models, ok := p.cachedModels(); ok {
		return models
	}
	return p.staticModels
}

// fetchLiveModels performs the catalog GET. It returns the filtered list and
// the X-Models-Etag header (for future conditional requests). Any failure —
// no token, transport error, non-2xx, malformed body — yields an empty list
// so the caller falls through to the cache. Failures are logged: Debug for
// the auth cases the picker deliberately ignores, Warn for the rest. A
// successful fetch of a legacy-shaped catalog also warns — the server serves
// it to too-old client versions, and without the warning the missing models
// are silent.
func (p *OpenAICodex) fetchLiveModels(ctx context.Context) ([]schema.ModelInfo, string) {
	token, err := p.engine.TokenSource(ctx)
	if err != nil {
		// Expected while logged out; chat surfaces the missing login.
		slog.Default().Debug("codex model catalog: no token available", "provider", p.name)
		return nil, ""
	}

	u := p.baseURL + codexModelsPath + "?client_version=" + url.QueryEscape(codexModelsClientVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		slog.Default().Warn("codex model catalog: build request", "provider", p.name, "error", err)
		return nil, ""
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(codexOriginatorHeader, codexOriginator)
	if acct := accountIDFromAccessToken(token); acct != "" {
		req.Header.Set(codexAccountIDHeader, acct)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		slog.Default().Warn("codex model catalog: request failed", "provider", p.name, "error", err)
		return nil, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			// Token expired or revoked: the refresh worker or the next chat
			// handles re-auth; the picker stays quiet by design.
			slog.Default().Debug("codex model catalog: unauthorized", "provider", p.name, "status", resp.StatusCode)
		} else {
			slog.Default().Warn("codex model catalog: unexpected status", "provider", p.name, "status", resp.StatusCode)
		}
		return nil, ""
	}

	var parsed codexModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		slog.Default().Warn("codex model catalog: malformed body", "provider", p.name, "error", err)
		return nil, ""
	}
	models := filterCodexModels(parsed.Models)
	if len(models) == 0 {
		slog.Default().Warn("codex model catalog: no selectable models", "provider", p.name, "entries", len(parsed.Models))
		return nil, ""
	}
	if catalogLooksLegacy(parsed.Models) {
		slog.Default().Warn("codex model catalog looks legacy: no context windows; newer models may be hidden from this client_version — bump codexModelsClientVersion",
			"provider", p.name, "client_version", codexModelsClientVersion, "models", len(models))
	}
	return models, resp.Header.Get("X-Models-Etag")
}

// catalogLooksLegacy reports whether a fetched catalog carries no context
// windows at all. The current catalog sets context_window on every model
// (docs/codex-spike-findings-2026-09-14.md §4); the legacy catalog served to
// too-old client versions omits it (observed live 2026-10-19). That fetch
// still succeeds, so this signature is what makes the degradation visible.
func catalogLooksLegacy(entries []codexModelEntry) bool {
	for _, e := range entries {
		if e.ContextWindow > 0 {
			return false
		}
	}
	return true
}

// filterCodexModels keeps only the models a user may select, sorted by the
// catalog's priority.
//
// Two filters, both required by the spike (§4): visibility == "list" drops
// internal models (gpt-reserve, codex-auto-review), and supported_in_api
// drops models the API will not serve. Offering a model the endpoint rejects
// with a 400 is worse than omitting it.
func filterCodexModels(entries []codexModelEntry) []schema.ModelInfo {
	kept := make([]codexModelEntry, 0, len(entries))
	for _, e := range entries {
		if e.Visibility != "list" || !e.SupportedInAPI || e.Slug == "" {
			continue
		}
		kept = append(kept, e)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return kept[i].Priority < kept[j].Priority
	})

	out := make([]schema.ModelInfo, 0, len(kept))
	for _, e := range kept {
		info := schema.ModelInfo{
			ID:            e.Slug,
			OwnedBy:       "openai",
			ContextWindow: e.ContextWindow,
		}

		if e.SupportedReasoningLevel != nil {
			info.Thinking = &schema.ThinkingOptions{Default: e.DefaultReasoningLevel, Mode: "effort"}
			for _, level := range e.SupportedReasoningLevel {
				if level.Effort != "" {
					info.Thinking.Levels = append(info.Thinking.Levels, level.Effort)
				}
			}
		}
		// The catalog reports a max context window larger than the default
		// for the 5.6/6 family (872k vs 272k). ModelInfo has one field, so
		// the default window is what the agent budgets against.
		if info.ContextWindow == 0 {
			info.ContextWindow = e.MaxContextWindow
		}
		out = append(out, info)
	}
	return out
}

// cachedModels reads the disk model cache for this provider.
func (p *OpenAICodex) cachedModels() ([]schema.ModelInfo, bool) {
	if p.dataDir == "" {
		return nil, false
	}
	c := modelcache.Load(p.dataDir)
	return c.Lookup(p.name, p.provCfg, modelcache.DefaultTTL, time.Now())
}

// saveModelsToCache records a successful live fetch. Cache writes are
// best-effort: a failure costs a re-fetch next launch, nothing more.
func (p *OpenAICodex) saveModelsToCache(models []schema.ModelInfo, etag string) {
	if p.dataDir == "" {
		return
	}
	c := modelcache.Load(p.dataDir)
	c.Providers[p.name] = modelcache.Entry{
		ConfigHash: modelcache.HashProvider(p.provCfg),
		Models:     models,
		FetchedAt:  time.Now(),
	}
	_ = modelcache.Save(p.dataDir, c)
	// The etag is recorded for a future conditional-request optimization;
	// it is not yet used to skip a fetch.
	_ = etag
}
