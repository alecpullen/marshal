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
