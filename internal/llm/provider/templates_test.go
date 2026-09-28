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
	if len(all) < 18 {
		t.Fatalf("All() returned %d templates, want >= 18", len(all))
	}
	ids := map[string]bool{}
	for _, tpl := range all {
		ids[tpl.ID] = true
	}
	for _, id := range []string{"ollama", "ollama-cloud", "lmstudio", "openrouter", "groq", "openai", "openai_compatible", "kimi"} {
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
		"openai_codex":      true,
	}
	for _, tpl := range All() {
		if tpl.ID == "" {
			t.Errorf("template with empty ID: %+v", tpl)
		}
		if tpl.Label == "" {
			t.Errorf("template %q has no label", tpl.ID)
		}
		if !knownTypes[tpl.Type] {
			t.Errorf("template %q type = %q, want one of openai_compatible/ollama/anthropic/openai_codex",
				tpl.ID, tpl.Type)
		}
		// The generic custom template intentionally has no base URL.
		if tpl.BaseURL == "" && tpl.ID != "openai_compatible" {
			t.Errorf("template %q has no base URL", tpl.ID)
		}
		// A remote template needs a credential source: either an env var
		// for an API key, or Auth = "oauth" for a browser login. The
		// generic custom template is the one exception (the user supplies
		// everything).
		if !tpl.Local && tpl.KeyEnv == "" && tpl.Auth != "oauth" && tpl.ID != "openai_compatible" {
			t.Errorf("remote template %q has neither KeyEnv nor Auth = \"oauth\"", tpl.ID)
		}
		// An OAuth template must not also advertise a key env: the connect
		// flow would prompt for a key that is never used.
		if tpl.Auth == "oauth" && tpl.KeyEnv != "" {
			t.Errorf("template %q sets Auth = \"oauth\" and KeyEnv = %q; they are mutually exclusive",
				tpl.ID, tpl.KeyEnv)
		}
	}
}

// TestCodexTemplate pins the codex template's contract: it is the only
// OAuth template, it targets the openai_codex backend, and its static model
// list is the visibility == "list" subset from the spike (with the retiring
// gpt-5.5 excluded).
func TestCodexTemplate(t *testing.T) {
	tpl, ok := Lookup("openai-codex")
	if !ok {
		t.Fatal("openai-codex template missing")
	}
	if tpl.Type != "openai_codex" {
		t.Fatalf("type = %q, want openai_codex", tpl.Type)
	}
	if tpl.Auth != "oauth" {
		t.Fatalf("auth = %q, want oauth", tpl.Auth)
	}
	if tpl.BaseURL != "https://chatgpt.com/backend-api" {
		t.Fatalf("base URL = %q", tpl.BaseURL)
	}
	if tpl.KeyEnv != "" {
		t.Fatalf("key env = %q, want empty (OAuth template)", tpl.KeyEnv)
	}
	if !tpl.ToolCalling || !tpl.StructuredOutput {
		t.Fatalf("capabilities = tool:%v structured:%v, want both true", tpl.ToolCalling, tpl.StructuredOutput)
	}
	want := []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}
	if len(tpl.Models) != len(want) {
		t.Fatalf("models = %v, want %v", tpl.Models, want)
	}
	for i, m := range want {
		if tpl.Models[i] != m {
			t.Fatalf("models[%d] = %q, want %q", i, tpl.Models[i], m)
		}
	}
	for _, m := range tpl.Models {
		if m == "gpt-5.5" {
			t.Fatal("gpt-5.5 retires 2026-10-14 and must not be in the static list")
		}
	}
}

// TestOnlyCodexTemplateUsesOAuth guards the invariant the connect flow
// relies on: exactly one template is OAuth-backed today.
func TestOnlyCodexTemplateUsesOAuth(t *testing.T) {
	var oauth []string
	for _, tpl := range All() {
		if tpl.Auth == "oauth" {
			oauth = append(oauth, tpl.ID)
		}
	}
	if len(oauth) != 1 || oauth[0] != "openai-codex" {
		t.Fatalf("OAuth templates = %v, want exactly [openai-codex]", oauth)
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

func TestKimiTemplateLocksTemperature(t *testing.T) {
	tpl, ok := Lookup("kimi")
	if !ok {
		t.Fatal("Lookup(kimi) should succeed")
	}
	if !tpl.TemperatureLocked {
		t.Fatal("kimi template must set TemperatureLocked")
	}
	if !tpl.ToolCalling {
		t.Fatal("kimi template must set ToolCalling")
	}
	if tpl.Type != "openai_compatible" {
		t.Fatalf("kimi template type = %q, want openai_compatible", tpl.Type)
	}
	if tpl.BaseURL != "https://api.kimi.com/coding/v1" {
		t.Fatalf("kimi BaseURL = %q", tpl.BaseURL)
	}
	if tpl.KeyEnv != "KIMI_API_KEY" {
		t.Fatalf("kimi KeyEnv = %q", tpl.KeyEnv)
	}
}
