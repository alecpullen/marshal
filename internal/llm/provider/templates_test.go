package provider

import "testing"

func TestAllDeterministic(t *testing.T) {
	first := All()
	second := All()
	if len(first) != len(second) {
		t.Fatalf("length mismatch: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("order differs at index %d: %q vs %q", i, first[i].ID, second[i].ID)
		}
	}
	// Also verify it's sorted by ID.
	for i := 1; i < len(first); i++ {
		if first[i-1].ID > first[i].ID {
			t.Errorf("not sorted by ID at index %d: %q > %q", i, first[i-1].ID, first[i].ID)
		}
	}
}

func TestLookupKnownTemplates(t *testing.T) {
	for _, id := range []string{"ollama", "ollama-cloud", "lmstudio", "openrouter", "groq", "openai", "openai_compatible"} {
		tpl, ok := Lookup(id)
		if !ok {
			t.Fatalf("Lookup(%q) = not found", id)
		}
		if tpl.ID == "" || tpl.Label == "" || tpl.Type == "" {
			t.Fatalf("Lookup(%q) returned incomplete template: %+v", id, tpl)
		}
	}
}

func TestLookupUnknownReturnsFalse(t *testing.T) {
	if _, ok := Lookup("nonexistent"); ok {
		t.Fatal("Lookup(unknown) should return false")
	}
}

func TestOllamaIsLocal(t *testing.T) {
	tpl, _ := Lookup("ollama")
	if !tpl.Local {
		t.Fatal("ollama template must be Local=true")
	}
	if tpl.BaseURL == "" {
		t.Fatal("ollama template must have a BaseURL")
	}
}

func TestOpenrouterIsRemoteWithKeyEnv(t *testing.T) {
	tpl, _ := Lookup("openrouter")
	if tpl.Local {
		t.Fatal("openrouter template must be Local=false")
	}
	if tpl.KeyEnv == "" {
		t.Fatal("openrouter template must suggest a KeyEnv")
	}
}

func TestOllamaCloudIsRemoteWithKeyEnv(t *testing.T) {
	tpl, ok := Lookup("ollama-cloud")
	if !ok {
		t.Fatal("Lookup(ollama-cloud) should succeed")
	}
	if tpl.Local {
		t.Fatal("ollama-cloud template must be Local=false")
	}
	if tpl.KeyEnv == "" {
		t.Fatal("ollama-cloud template must suggest a KeyEnv")
	}
	if tpl.BaseURL == "" {
		t.Fatal("ollama-cloud template must have a BaseURL")
	}
	if tpl.Type != "ollama" {
		t.Fatalf("ollama-cloud template type = %q, want ollama", tpl.Type)
	}
}

func TestAllReturnsAll(t *testing.T) {
	all := All()
	if len(all) < 17 {
		t.Fatalf("All() returned %d templates, want >= 17", len(all))
	}
	ids := map[string]bool{}
	for _, tpl := range all {
		ids[tpl.ID] = true
	}
	for _, id := range []string{"ollama", "ollama-cloud", "lmstudio", "openrouter", "groq", "openai", "openai_compatible"} {
		if !ids[id] {
			t.Fatalf("All() missing template %q", id)
		}
	}
}

func TestTemplatesCoverPopularProviders(t *testing.T) {
	want := []string{
		"anthropic", "gemini", "deepseek", "mistral", "together",
		"fireworks", "xai", "cerebras", "vllm", "llamacpp",
	}
	for _, id := range want {
		if _, ok := Lookup(id); !ok {
			t.Errorf("missing template %q", id)
		}
	}
}

func TestTemplatesAreWellFormed(t *testing.T) {
	knownTypes := map[string]bool{
		"openai_compatible": true,
		"ollama":            true,
		"anthropic":         true,
	}
	for _, tpl := range All() {
		if tpl.ID == "" {
			t.Errorf("template with empty ID: %+v", tpl)
		}
		if tpl.Label == "" {
			t.Errorf("template %q has no label", tpl.ID)
		}
		if !knownTypes[tpl.Type] {
			t.Errorf("template %q type = %q, want one of openai_compatible/ollama/anthropic",
				tpl.ID, tpl.Type)
		}
		// The generic custom template intentionally has no base URL.
		if tpl.BaseURL == "" && tpl.ID != "openai_compatible" {
			t.Errorf("template %q has no base URL", tpl.ID)
		}
		if !tpl.Local && tpl.KeyEnv == "" && tpl.ID != "openai_compatible" {
			t.Errorf("remote template %q has no KeyEnv", tpl.ID)
		}
	}
}

func TestLocalTemplatesNeedNoKey(t *testing.T) {
	for _, id := range []string{"ollama", "lmstudio", "vllm", "llamacpp"} {
		tpl, ok := Lookup(id)
		if !ok {
			t.Fatalf("missing %q", id)
		}
		if !tpl.Local {
			t.Errorf("template %q should be marked Local", id)
		}
	}
}

func TestLocalToolCapableTemplatesAdvertiseToolCalling(t *testing.T) {
	for _, id := range []string{"ollama", "ollama-cloud"} {
		tpl, ok := Lookup(id)
		if !ok {
			t.Fatalf("template %q not found", id)
		}
		if !tpl.ToolCalling {
			t.Errorf("template %q ToolCalling = false, want true", id)
		}
	}
	// Deployment-dependent local servers stay envelope by default.
	for _, id := range []string{"lmstudio", "vllm", "llamacpp", "openai_compatible"} {
		tpl, ok := Lookup(id)
		if !ok {
			t.Fatalf("template %q not found", id)
		}
		if tpl.ToolCalling {
			t.Errorf("template %q ToolCalling = true, want false", id)
		}
	}
}

func TestUniqueNameNoCollision(t *testing.T) {
	got := UniqueName("ollama", map[string]bool{})
	if got != "ollama" {
		t.Fatalf("UniqueName with no collision = %q, want %q", got, "ollama")
	}
}

func TestUniqueNameWithCollision(t *testing.T) {
	got := UniqueName("ollama", map[string]bool{"ollama": true})
	if got != "ollama-2" {
		t.Fatalf("UniqueName with one collision = %q, want %q", got, "ollama-2")
	}
	got = UniqueName("ollama", map[string]bool{"ollama": true, "ollama-2": true})
	if got != "ollama-3" {
		t.Fatalf("UniqueName with two collisions = %q, want %q", got, "ollama-3")
	}
}

func TestOpencodeGoTemplate(t *testing.T) {
	tmpl, ok := Lookup("opencode-go")
	if !ok {
		t.Fatal("opencode-go template missing")
	}
	if tmpl.Type != "openai_compatible" {
		t.Fatalf("type = %q, want openai_compatible", tmpl.Type)
	}
	if tmpl.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("base URL = %q", tmpl.BaseURL)
	}
	if tmpl.KeyEnv != "OPENCODE_API_KEY" {
		t.Fatalf("key env = %q", tmpl.KeyEnv)
	}
}
