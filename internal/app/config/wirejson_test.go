package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"marshal/internal/interaction"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/routing"
)

// A Config that crosses the client seam as JSON must come back identical:
// the wire carries the decoded struct, not a re-loaded one.
func TestTheDefaultConfigRoundTripsThroughJSON(t *testing.T) {
	want := Default()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal default config: %v", err)
	}
	var got Config
	if err := UnmarshalConfigJSON(data, &got); err != nil {
		t.Fatalf("UnmarshalConfigJSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-tripped config differs\n got: %+v\nwant: %+v", got, want)
	}
}

// The layers a session holds travel together, and a preset's Pricing survives
// the trip as the pointer the load path produced rather than a map.
func TestLoadedLayersRoundTripThroughJSON(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, UserConfigPath(home), `
[providers.openai]
type = "openai_compatible"
base_url = "http://127.0.0.1:8080/v1"
api_key = "sk-test"

[providers.ollama]
type = "ollama"
base_url = "http://127.0.0.1:11434"

[models.presets."openai/gpt-4o"]
provider = "openai"
model = "gpt-4o"

[models.presets."openai/gpt-4o".pricing]
InputPerMTokCents = 250
OutputPerMTokCents = 1000

[mcp.servers.docs]
command = "docs-mcp"

[web]
search_key = "search-key"
`)

	want, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if len(want.Merged.Providers) != 2 {
		t.Fatalf("loaded %d providers, want 2", len(want.Merged.Providers))
	}
	if want.Merged.Providers["openai"].APIKey != "sk-test" {
		t.Fatalf("openai api_key = %q, want the fixture's", want.Merged.Providers["openai"].APIKey)
	}
	if want.Merged.Web.SearchKey != "search-key" {
		t.Fatalf("web.search_key = %q, want the fixture's", want.Merged.Web.SearchKey)
	}
	if _, ok := want.Merged.MCP.Servers["docs"]; !ok {
		t.Fatal("mcp.servers.docs missing from the merged config")
	}
	loadedPricing, ok := want.Merged.Models.Presets["openai/gpt-4o"].Pricing.(*pricing.ModelPricing)
	if !ok {
		t.Fatalf("loaded Pricing has type %T, want *pricing.ModelPricing", want.Merged.Models.Presets["openai/gpt-4o"].Pricing)
	}

	// The load path coerces only the merged layer's Pricing; the seam's decode
	// restores every layer's, so the value that comes back is uniformly typed.
	expect := want
	priced := expect.User.Models.Presets["openai/gpt-4o"]
	priced.Pricing = loadedPricing
	expect.User.Models.Presets["openai/gpt-4o"] = priced

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal layers: %v", err)
	}
	var got Layers
	if err := UnmarshalLayersJSON(data, &got); err != nil {
		t.Fatalf("UnmarshalLayersJSON: %v", err)
	}
	if !reflect.DeepEqual(got, expect) {
		t.Fatalf("round-tripped layers differ\n got: %+v\nwant: %+v", got, expect)
	}
	for _, layer := range []struct {
		name string
		cfg  Config
	}{
		{"user", got.User},
		{"merged", got.Merged},
	} {
		gotPricing, ok := layer.cfg.Models.Presets["openai/gpt-4o"].Pricing.(*pricing.ModelPricing)
		if !ok {
			t.Fatalf("%s Pricing has type %T, want *pricing.ModelPricing", layer.name, layer.cfg.Models.Presets["openai/gpt-4o"].Pricing)
		}
		if !reflect.DeepEqual(gotPricing, loadedPricing) {
			t.Fatalf("%s Pricing = %+v, want %+v", layer.name, gotPricing, loadedPricing)
		}
	}
	// The merged config alone crosses the seam the same way, Pricing included.
	mergedJSON, err := json.Marshal(want.Merged)
	if err != nil {
		t.Fatalf("marshal merged config: %v", err)
	}
	var merged Config
	if err := UnmarshalConfigJSON(mergedJSON, &merged); err != nil {
		t.Fatalf("UnmarshalConfigJSON: %v", err)
	}
	if !reflect.DeepEqual(merged, want.Merged) {
		t.Fatalf("round-tripped merged config differs\n got: %+v\nwant: %+v", merged, want.Merged)
	}
}

// HoistError is an error, which JSON cannot decode; the tag keeps it off the
// wire and a reloaded copy reports no hoist failure.
func TestAHoistErrorIsNotEncoded(t *testing.T) {
	layers := Layers{
		Default:    Default(),
		User:       Default(),
		Merged:     Default(),
		HoistError: errors.New("hoist failed"),
	}
	data, err := json.Marshal(layers)
	if err != nil {
		t.Fatalf("marshal layers: %v", err)
	}
	var got Layers
	if err := UnmarshalLayersJSON(data, &got); err != nil {
		t.Fatalf("UnmarshalLayersJSON: %v", err)
	}
	if got.HoistError != nil {
		t.Fatalf("HoistError = %v, want nil", got.HoistError)
	}
}

func TestPresetsRoundTripWithTheirPricing(t *testing.T) {
	want := map[string]routing.ModelPreset{
		"priced": {
			Name:     "priced",
			Provider: "openai",
			Model:    "gpt-4o",
			Pricing: &pricing.ModelPricing{
				InputPerMTokCents:      250,
				OutputPerMTokCents:     1000,
				ReasoningPerMTokCents:  0,
				CacheReadPerMTokCents:  125,
				CacheWritePerMTokCents: 250,
			},
		},
		"unpriced": {
			Name:     "unpriced",
			Provider: "ollama",
			Model:    "qwen3",
		},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal presets: %v", err)
	}
	var got map[string]routing.ModelPreset
	if err := UnmarshalPresetsJSON(data, &got); err != nil {
		t.Fatalf("UnmarshalPresetsJSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-tripped presets differ\n got: %+v\nwant: %+v", got, want)
	}
	if _, ok := got["priced"].Pricing.(*pricing.ModelPricing); !ok {
		t.Fatalf("priced Pricing has type %T, want *pricing.ModelPricing", got["priced"].Pricing)
	}
	if got["unpriced"].Pricing != nil {
		t.Fatalf("unpriced Pricing = %v, want nil", got["unpriced"].Pricing)
	}
}

func TestPathTypeNamesTheFieldsType(t *testing.T) {
	cases := []struct {
		path string
		want reflect.Type
	}{
		// A map entry that does not exist yet still has a type.
		{"providers.newname.api_key", reflect.TypeOf("")},
		// A pointer field's type is reported with the pointer removed.
		{"privacy.models_dev_enrichment", reflect.TypeOf(false)},
		{"budgets.daily_usd", reflect.TypeOf(float64(0))},
		// A named type stays named: the decoder must not treat it as a string.
		{"agent.mode", reflect.TypeOf(interaction.Mode(""))},
	}
	for _, tc := range cases {
		got, ok := PathType(tc.path)
		if !ok {
			t.Errorf("PathType(%q) reported no type", tc.path)
			continue
		}
		if got != tc.want {
			t.Errorf("PathType(%q) = %s, want %s", tc.path, got, tc.want)
		}
	}
	if got, ok := PathType("nope.nope"); ok {
		t.Fatalf("PathType(unknown) = %s, true, want false", got)
	}
	// A field typed any has no type to decode into: a value would arrive as a
	// map and lose the pointer, so the path reports no type.
	if got, ok := PathType("models.presets.newname.pricing"); ok {
		t.Fatalf("PathType(an any field) = %s, true, want false", got)
	}
}
