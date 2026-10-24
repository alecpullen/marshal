package presetsetup

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/modelcatalog"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

const (
	geminiBaseURL  = "https://generativelanguage.googleapis.com"
	unboundBaseURL = "https://api.example.com/v1"
)

// newApplyConfig returns an empty config for bulk-apply tests.
func newApplyConfig() *config.Config {
	return &config.Config{Models: config.ModelsConfig{Presets: map[string]routing.ModelPreset{}}}
}

// newProvenance returns an empty provenance for bulk-apply tests.
func newProvenance() *Provenance {
	return &Provenance{Version: ProvenanceVersion, Presets: map[string]PresetProvenance{}}
}

// catalogWithModels builds a one-provider catalog holding models.
func catalogWithModels(providerID string, models map[string]modelcatalog.Model) *modelcatalog.Catalog {
	return &modelcatalog.Catalog{
		Providers: map[string]modelcatalog.Provider{
			providerID: {ID: providerID, Models: models},
		},
	}
}

func TestApplyCreatesEveryReportedModel(t *testing.T) {
	cat := catalogWithModels("google", map[string]modelcatalog.Model{
		"gemini-2.5-pro":   {ID: "gemini-2.5-pro", Limit: modelcatalog.Limit{Context: 1048576, Output: 65536}},
		"gemini-2.5-flash": {ID: "gemini-2.5-flash", Limit: modelcatalog.Limit{Context: 1048576, Output: 65536}},
	})
	cfg := newApplyConfig()
	prov := newProvenance()
	models := []schema.ModelInfo{
		{ID: "gemini-2.5-pro"},
		{ID: "gemini-2.5-flash"},
		{ID: "gemini-9-experimental"},
	}
	res := Apply(cfg, "gemini", geminiBaseURL, models, cat, prov)
	if len(cfg.Models.Presets) != 3 {
		t.Fatalf("presets = %d, want 3", len(cfg.Models.Presets))
	}
	if want := (Result{Prepared: 3, Matched: 2, Fallback: 1}); res != want {
		t.Fatalf("Result = %+v, want %+v", res, want)
	}
}

func TestApplyAddsNoCatalogOnlyModels(t *testing.T) {
	cat := catalogWithModels("google", map[string]modelcatalog.Model{
		"gemini-2.5-pro": {ID: "gemini-2.5-pro", Limit: modelcatalog.Limit{Context: 1048576, Output: 65536}},
		"gemini-3-pro":   {ID: "gemini-3-pro", Limit: modelcatalog.Limit{Context: 2000000, Output: 65536}},
	})
	cfg := newApplyConfig()
	Apply(cfg, "gemini", geminiBaseURL, []schema.ModelInfo{{ID: "gemini-2.5-pro"}}, cat, newProvenance())
	if _, ok := cfg.Models.Presets["gemini/gemini-3-pro"]; ok {
		t.Fatal("preset created for a catalog model the provider did not report")
	}
	if len(cfg.Models.Presets) != 1 {
		t.Fatalf("presets = %d, want 1", len(cfg.Models.Presets))
	}
}

func TestApplyRefreshesUneditedField(t *testing.T) {
	const name = "gemini/gemini-2.5-pro"
	cfg := newApplyConfig()
	cfg.Models.Presets[name] = routing.ModelPreset{
		Name:            name,
		Provider:        "gemini",
		Model:           "gemini-2.5-pro",
		ContextWindow:   1000,
		MaxOutputTokens: 500,
	}
	prov := newProvenance()
	prov.Presets[name] = PresetProvenance{Fields: map[string]FieldState{
		"context_window":    {Source: string(SourceModelsDev), Value: "1000"},
		"max_output_tokens": {Source: string(SourceModelsDev), Value: "500"},
	}}
	cat := catalogWithModels("google", map[string]modelcatalog.Model{
		"gemini-2.5-pro": {ID: "gemini-2.5-pro", Limit: modelcatalog.Limit{Context: 2000, Output: 500}},
	})
	Apply(cfg, "gemini", geminiBaseURL, []schema.ModelInfo{{ID: "gemini-2.5-pro"}}, cat, prov)
	if got := cfg.Models.Presets[name].ContextWindow; got != 2000 {
		t.Fatalf("ContextWindow = %d, want 2000", got)
	}
}

func TestApplyPreservesEditedField(t *testing.T) {
	const name = "gemini/gemini-2.5-pro"
	cfg := newApplyConfig()
	cfg.Models.Presets[name] = routing.ModelPreset{
		Name:          name,
		Provider:      "gemini",
		Model:         "gemini-2.5-pro",
		ContextWindow: 1500,
	}
	prov := newProvenance()
	prov.Presets[name] = PresetProvenance{Fields: map[string]FieldState{
		"context_window": {Source: string(SourceModelsDev), Value: "1000"},
	}}
	cat := catalogWithModels("google", map[string]modelcatalog.Model{
		"gemini-2.5-pro": {ID: "gemini-2.5-pro", Limit: modelcatalog.Limit{Context: 2000, Output: 65536}},
	})
	Apply(cfg, "gemini", geminiBaseURL, []schema.ModelInfo{{ID: "gemini-2.5-pro"}}, cat, prov)
	if got := cfg.Models.Presets[name].ContextWindow; got != 1500 {
		t.Fatalf("ContextWindow = %d, want 1500 (user edit preserved)", got)
	}
	if _, ok := prov.Presets[name].Fields["context_window"]; ok {
		t.Fatal("context_window still managed after a user edit")
	}
}

func TestApplyRefreshesOtherFieldsIndependently(t *testing.T) {
	const name = "gemini/gemini-2.5-pro"
	cfg := newApplyConfig()
	cfg.Models.Presets[name] = routing.ModelPreset{
		Name:            name,
		Provider:        "gemini",
		Model:           "gemini-2.5-pro",
		ContextWindow:   1500,
		MaxOutputTokens: 500,
	}
	prov := newProvenance()
	prov.Presets[name] = PresetProvenance{Fields: map[string]FieldState{
		"context_window":    {Source: string(SourceModelsDev), Value: "1000"},
		"max_output_tokens": {Source: string(SourceModelsDev), Value: "500"},
	}}
	cat := catalogWithModels("google", map[string]modelcatalog.Model{
		"gemini-2.5-pro": {ID: "gemini-2.5-pro", Limit: modelcatalog.Limit{Context: 2000, Output: 800}},
	})
	Apply(cfg, "gemini", geminiBaseURL, []schema.ModelInfo{{ID: "gemini-2.5-pro"}}, cat, prov)
	preset := cfg.Models.Presets[name]
	if preset.ContextWindow != 1500 {
		t.Fatalf("ContextWindow = %d, want 1500 (edited)", preset.ContextWindow)
	}
	if preset.MaxOutputTokens != 800 {
		t.Fatalf("MaxOutputTokens = %d, want 800 (unedited)", preset.MaxOutputTokens)
	}
}

func TestApplyLeavesPreFeaturePresetUntouched(t *testing.T) {
	const name = "gemini/gemini-2.5-pro"
	cfg := newApplyConfig()
	cfg.Models.Presets[name] = routing.ModelPreset{
		Name:            name,
		Provider:        "gemini",
		Model:           "gemini-2.5-pro",
		ContextWindow:   1000,
		MaxOutputTokens: 500,
	}
	prov := newProvenance()
	cat := catalogWithModels("google", map[string]modelcatalog.Model{
		"gemini-2.5-pro": {ID: "gemini-2.5-pro", Limit: modelcatalog.Limit{Context: 2000, Output: 65536}},
	})
	Apply(cfg, "gemini", geminiBaseURL, []schema.ModelInfo{{ID: "gemini-2.5-pro"}}, cat, prov)
	preset := cfg.Models.Presets[name]
	if preset.ContextWindow != 1000 || preset.MaxOutputTokens != 500 {
		t.Fatalf("preset = %d/%d, want 1000/500 untouched", preset.ContextWindow, preset.MaxOutputTokens)
	}
	if _, ok := prov.Presets[name]; ok {
		t.Fatal("provenance added for a preset that had none")
	}
}

func TestApplyRetainsValueWhenMetadataOmitted(t *testing.T) {
	const name = "gemini/gemini-2.5-pro"
	cfg := newApplyConfig()
	cfg.Models.Presets[name] = routing.ModelPreset{
		Name:            name,
		Provider:        "gemini",
		Model:           "gemini-2.5-pro",
		ContextWindow:   1048576,
		MaxOutputTokens: 128000,
	}
	prov := newProvenance()
	prov.Presets[name] = PresetProvenance{Fields: map[string]FieldState{
		"context_window":    {Source: string(SourceModelsDev), Value: "1048576"},
		"max_output_tokens": {Source: string(SourceModelsDev), Value: "128000"},
	}}
	// The catalog entry omits the output limit.
	cat := catalogWithModels("google", map[string]modelcatalog.Model{
		"gemini-2.5-pro": {ID: "gemini-2.5-pro", Limit: modelcatalog.Limit{Context: 1048576}},
	})
	Apply(cfg, "gemini", geminiBaseURL, []schema.ModelInfo{{ID: "gemini-2.5-pro"}}, cat, prov)
	if got := cfg.Models.Presets[name].MaxOutputTokens; got != 128000 {
		t.Fatalf("MaxOutputTokens = %d, want 128000 retained", got)
	}
	if _, ok := prov.Presets[name].Fields["max_output_tokens"]; !ok {
		t.Fatal("max_output_tokens dropped from provenance")
	}
}

func TestApplyNeverDeletesUnreportedPreset(t *testing.T) {
	const retired = "gemini/gemini-1.0-pro"
	cfg := newApplyConfig()
	cfg.Models.Presets[retired] = routing.ModelPreset{
		Name:          retired,
		Provider:      "gemini",
		Model:         "gemini-1.0-pro",
		ContextWindow: 32000,
	}
	prov := newProvenance()
	prov.Presets[retired] = PresetProvenance{CatalogProvider: "google", CatalogModel: "gemini-1.0-pro"}
	cat := catalogWithModels("google", map[string]modelcatalog.Model{
		"gemini-2.5-pro": {ID: "gemini-2.5-pro", Limit: modelcatalog.Limit{Context: 1048576, Output: 65536}},
	})
	Apply(cfg, "gemini", geminiBaseURL, []schema.ModelInfo{{ID: "gemini-2.5-pro"}}, cat, prov)
	if _, ok := cfg.Models.Presets[retired]; !ok {
		t.Fatal("preset for an unreported model was deleted")
	}
}

func TestApplyRecordsCatalogBinding(t *testing.T) {
	cat := catalogWithModels("google", map[string]modelcatalog.Model{
		"gemini-2.5-pro": {ID: "gemini-2.5-pro", Limit: modelcatalog.Limit{Context: 1048576, Output: 65536}},
	})
	cfg := newApplyConfig()
	prov := newProvenance()
	Apply(cfg, "gemini", geminiBaseURL, []schema.ModelInfo{{ID: "gemini-2.5-pro"}}, cat, prov)
	pp := prov.Presets["gemini/gemini-2.5-pro"]
	if pp.CatalogProvider != "google" {
		t.Fatalf("CatalogProvider = %q, want %q", pp.CatalogProvider, "google")
	}
	if pp.CatalogModel != "gemini-2.5-pro" {
		t.Fatalf("CatalogModel = %q, want %q", pp.CatalogModel, "gemini-2.5-pro")
	}
}

func TestApplySkipsUnboundProvider(t *testing.T) {
	cat := catalogWithModels("my-endpoint", map[string]modelcatalog.Model{
		"my-model": {ID: "my-model", Limit: modelcatalog.Limit{Context: 999, Output: 111}},
	})
	cfg := newApplyConfig()
	prov := newProvenance()
	Apply(cfg, "my-endpoint", unboundBaseURL, []schema.ModelInfo{{ID: "my-model"}}, cat, prov)
	preset, ok := cfg.Models.Presets["my-endpoint/my-model"]
	if !ok {
		t.Fatal("preset not created for unbound provider")
	}
	if preset.ContextWindow != 1_000_000 || preset.MaxOutputTokens != 128_000 {
		t.Fatalf("limits = %d/%d, want defaults 1000000/128000", preset.ContextWindow, preset.MaxOutputTokens)
	}
	if pp := prov.Presets["my-endpoint/my-model"]; pp.CatalogProvider != "" {
		t.Fatalf("CatalogProvider = %q, want empty", pp.CatalogProvider)
	}
}

func TestLoadProvenanceMissingFileIsEmpty(t *testing.T) {
	p, err := LoadProvenance(t.TempDir())
	if err != nil {
		t.Fatalf("LoadProvenance() error = %v", err)
	}
	if p.Version != ProvenanceVersion {
		t.Fatalf("Version = %d, want %d", p.Version, ProvenanceVersion)
	}
	if len(p.Presets) != 0 {
		t.Fatalf("Presets = %d, want 0", len(p.Presets))
	}
}

func TestLoadProvenanceCorruptFileErrors(t *testing.T) {
	dir := t.TempDir()
	path := ProvenancePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProvenance(dir); err == nil {
		t.Fatal("LoadProvenance() error = nil, want error")
	}
}

func TestSaveProvenanceRoundTrips(t *testing.T) {
	dir := t.TempDir()
	want := &Provenance{
		Version: ProvenanceVersion,
		Presets: map[string]PresetProvenance{
			"gemini/gemini-2.5-pro": {
				CatalogProvider: "google",
				CatalogModel:    "gemini-2.5-pro",
				Fields: map[string]FieldState{
					"context_window": {Source: string(SourceModelsDev), Value: "1048576"},
					"pricing.input":  {Source: string(SourceModelsDev), Value: "140"},
				},
			},
		},
	}
	if err := SaveProvenance(dir, want); err != nil {
		t.Fatalf("SaveProvenance() error = %v", err)
	}
	got, err := LoadProvenance(dir)
	if err != nil {
		t.Fatalf("LoadProvenance() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}
