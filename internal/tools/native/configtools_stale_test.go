package native

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

// TestConfigToolPreservesForeignDiskEntries pins the cross-process incident
// at the tool level: a config.models.preset.set write from a process whose
// snapshot predates another session's persisted entries must keep those
// entries on disk. Before the baseline merge, SaveUserConfigSection replaced
// [providers] and [models.presets] wholesale, so the tool's stale snapshot
// erased whatever other marshal processes had written meanwhile.
func TestConfigToolPreservesForeignDiskEntries(t *testing.T) {
	tool, home, projectPath, _, _ := setupGlobalOnlyTool(t, config.Default(), "config.models.preset.set", (*toolSet).configModelsPresetSetTool)

	// Another marshal process persists these entries after this tool
	// process captured its (empty) snapshot.
	userPath := config.UserConfigPath(home)
	if err := config.SaveUserConfigProviders(userPath, map[string]config.ProviderConfig{
		"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
	}); err != nil {
		t.Fatalf("seed foreign provider: %v", err)
	}
	if err := config.SaveUserConfigPresets(userPath, map[string]routing.ModelPreset{
		"ollama/nomic-embed-text": {Provider: "ollama", Model: "nomic-embed-text", LocalOnly: true},
	}); err != nil {
		t.Fatalf("seed foreign preset: %v", err)
	}

	// The stale-snapshot process writes an unrelated preset.
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "1",
		Name: "config.models.preset.set",
		Args: json.RawMessage(`{"name":"ollama-cloud/glm-5.3","provider":"ollama-cloud","model":"glm-5.3"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	loaded, err := config.Load(config.LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := loaded.Providers["ollama"]; !ok {
		t.Fatalf("foreign provider 'ollama' erased by stale-snapshot tool write; providers = %v", loaded.Providers)
	}
	if _, ok := loaded.Models.Presets["ollama/nomic-embed-text"]; !ok {
		t.Fatalf("foreign preset 'ollama/nomic-embed-text' erased by stale-snapshot tool write; presets = %v", loaded.Models.Presets)
	}
	if _, ok := loaded.Models.Presets["ollama-cloud/glm-5.3"]; !ok {
		t.Fatalf("the tool's own write must land; presets = %v", loaded.Models.Presets)
	}
	if _, err := os.Stat(projectPath); !os.IsNotExist(err) {
		t.Fatalf("global write must not create the project config: %v", err)
	}
}

// TestConfigDeleteToolStillDeletesDiskEntry guards the merge's other side at
// the tool level: config.providers.delete must still remove the entry from
// disk even though the saver now merges — the entry is in the baseline
// snapshot and absent from the write, which the merge reads as an
// intentional deletion.
func TestConfigDeleteToolStillDeletesDiskEntry(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"openai": {BaseURL: "https://api.openai.com"},
		"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
	}
	tool, home, _, _, _ := setupGlobalOnlyTool(t, cfg, "config.providers.delete", (*toolSet).configProvidersDeleteTool)

	// Both entries are on disk (the tool snapshot is also the merged config,
	// so seed the file from it).
	userPath := config.UserConfigPath(home)
	if err := config.SaveUserConfigProviders(userPath, cfg.Providers); err != nil {
		t.Fatalf("seed providers: %v", err)
	}

	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "1",
		Name: "config.providers.delete",
		Args: json.RawMessage(`{"name":"ollama"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	loaded, err := config.Load(config.LoadOptions{HomeDir: home, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := loaded.Providers["ollama"]; ok {
		t.Fatalf("deleted provider 'ollama' must not survive the merge; providers = %v", loaded.Providers)
	}
	if _, ok := loaded.Providers["openai"]; !ok {
		t.Fatalf("untouched provider 'openai' must survive; providers = %v", loaded.Providers)
	}
}
