package config

import (
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/llm/routing"
)

// staleMergeUserPath returns the user-config path under a temp home with no
// pre-existing file.
func staleMergeUserPath(t *testing.T) string {
	t.Helper()
	path := UserConfigPath(filepath.Join(t.TempDir(), "home"))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// loadUserProvidersAt reads the [providers] map back from the user file.
func loadUserProvidersAt(t *testing.T, path string) map[string]ProviderConfig {
	t.Helper()
	file, err := loadFile(path)
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	return file.Providers
}

func loadUserPresetsAt(t *testing.T, path string) map[string]routing.ModelPreset {
	t.Helper()
	file, err := loadFile(path)
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if file.Models == nil {
		return nil
	}
	return file.Models.Presets
}

// TestSaveUserConfigProvidersPreservesDiskOnlyEntry pins the cross-process
// regression: a second marshal process holding a config snapshot that
// predates a foreign write must not erase that write when it saves its own
// provider changes. The September 2026 incident: [providers.ollama] written
// by another session vanished from ~/.config/marshal/config.toml after a
// long-lived TUI saved its own provider section.
func TestSaveUserConfigProvidersPreservesDiskOnlyEntry(t *testing.T) {
	path := staleMergeUserPath(t)

	// Baseline: the second process's load-time snapshot — foreign entry absent.
	baseline := map[string]ProviderConfig{
		"work": {Type: "openai_compatible", BaseURL: "https://api.work.example/v1"},
	}
	// Disk carries the foreign entry the snapshot never saw.
	disk := map[string]ProviderConfig{
		"work": baseline["work"],
		"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
	}
	if err := SaveUserConfigProviders(path, disk, nil); err != nil {
		t.Fatalf("seed disk: %v", err)
	}

	// next: the second process edited its own entry only.
	next := map[string]ProviderConfig{
		"work": {Type: "openai_compatible", BaseURL: "https://api.work.example/v2"},
	}
	if err := SaveUserConfigProviders(path, next, baseline); err != nil {
		t.Fatalf("SaveUserConfigProviders: %v", err)
	}

	got := loadUserProvidersAt(t, path)
	if _, ok := got["ollama"]; !ok {
		t.Fatalf("disk-only provider 'ollama' was erased by a save from a stale snapshot; got providers %v", got)
	}
	if got["work"].BaseURL != "https://api.work.example/v2" {
		t.Fatalf("caller's own edit must win; work.BaseURL = %q", got["work"].BaseURL)
	}
}

func TestSaveUserConfigPresetsPreservesDiskOnlyEntry(t *testing.T) {
	path := staleMergeUserPath(t)

	baseline := map[string]routing.ModelPreset{
		"ollama-cloud/deepseek-v4-flash": {Provider: "ollama-cloud", Model: "deepseek-v4-flash", ContextWindow: 0},
	}
	disk := map[string]routing.ModelPreset{
		"ollama-cloud/deepseek-v4-flash": baseline["ollama-cloud/deepseek-v4-flash"],
		"ollama/nomic-embed-text":       {Provider: "ollama", Model: "nomic-embed-text", LocalOnly: true},
	}
	if err := SaveUserConfigPresets(path, disk, nil); err != nil {
		t.Fatalf("seed disk: %v", err)
	}

	// Another process raised the flash preset's limits — absent from this
	// process's snapshot, so a naive save would revert them.
	next := map[string]routing.ModelPreset{
		"ollama-cloud/deepseek-v4-flash": {Provider: "ollama-cloud", Model: "deepseek-v4-flash", ContextWindow: 1000000, MaxOutputTokens: 128000},
	}
	if err := SaveUserConfigPresets(path, next, baseline); err != nil {
		t.Fatalf("SaveUserConfigPresets: %v", err)
	}

	got := loadUserPresetsAt(t, path)
	if _, ok := got["ollama/nomic-embed-text"]; !ok {
		t.Fatalf("disk-only preset 'ollama/nomic-embed-text' was erased; got presets %v", got)
	}
	if got["ollama-cloud/deepseek-v4-flash"].ContextWindow != 1000000 {
		t.Fatalf("caller's edit must win; ContextWindow = %d", got["ollama-cloud/deepseek-v4-flash"].ContextWindow)
	}
}

// TestSaveUserConfigProvidersRemovesIntentionallyDeletedKey pins the other
// side of the contract: an entry present in the caller's baseline but absent
// from next is an intentional deletion (config.providers.delete tool, a
// settings rename) and MUST disappear.
func TestSaveUserConfigProvidersRemovesIntentionallyDeletedKey(t *testing.T) {
	path := staleMergeUserPath(t)

	baseline := map[string]ProviderConfig{
		"work": {Type: "openai_compatible", BaseURL: "https://api.work.example/v1"},
		"old": {Type: "openai_compatible", BaseURL: "https://api.old.example/v1"},
	}
	if err := SaveUserConfigProviders(path, baseline, nil); err != nil {
		t.Fatalf("seed disk: %v", err)
	}

	next := map[string]ProviderConfig{
		"work": baseline["work"],
	}
	if err := SaveUserConfigProviders(path, next, baseline); err != nil {
		t.Fatalf("SaveUserConfigProviders: %v", err)
	}

	got := loadUserProvidersAt(t, path)
	if _, ok := got["old"]; ok {
		t.Fatalf("intentionally deleted provider 'old' survived the merge; got %v", got)
	}
	if _, ok := got["work"]; !ok {
		t.Fatal("kept provider 'work' must survive")
	}
}

func TestSaveUserConfigPresetsRemovesIntentionallyDeletedKey(t *testing.T) {
	path := staleMergeUserPath(t)

	baseline := map[string]routing.ModelPreset{
		"a/one": {Provider: "a", Model: "one"},
		"a/gone": {Provider: "a", Model: "gone"},
	}
	if err := SaveUserConfigPresets(path, baseline, nil); err != nil {
		t.Fatalf("seed disk: %v", err)
	}

	next := map[string]routing.ModelPreset{
		"a/one": {Provider: "a", Model: "one"},
	}
	if err := SaveUserConfigPresets(path, next, baseline); err != nil {
		t.Fatalf("SaveUserConfigPresets: %v", err)
	}

	got := loadUserPresetsAt(t, path)
	if _, ok := got["a/gone"]; ok {
		t.Fatalf("intentionally deleted preset 'a/gone' survived the merge; got %v", got)
	}
	if _, ok := got["a/one"]; !ok {
		t.Fatal("kept preset 'a/one' must survive")
	}
}

// TestSaveUserConfigProvidersNilBaselinePreservesDiskOnly pins the
// fresh-process edge: a snapshot that predates ANY provider entry (nil map)
// must still not clobber foreign disk entries when it adds its own — a nil
// baseline map means "the section did not exist at snapshot time", so every
// disk entry is foreign and must survive.
func TestSaveUserConfigProvidersNilBaselinePreservesDiskOnly(t *testing.T) {
	path := staleMergeUserPath(t)
	disk := map[string]ProviderConfig{
		"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
	}
	if err := SaveUserConfigProviders(path, disk); err != nil {
		t.Fatalf("seed disk: %v", err)
	}

	// Baseline map is nil: the process had no providers when it loaded.
	next := map[string]ProviderConfig{
		"work": {Type: "openai_compatible", BaseURL: "https://api.work.example/v1"},
	}
	if err := SaveUserConfigProviders(path, next, nil); err != nil {
		t.Fatalf("SaveUserConfigProviders: %v", err)
	}

	got := loadUserProvidersAt(t, path)
	if _, ok := got["ollama"]; !ok {
		t.Fatalf("disk-only provider erased despite nil baseline; got %v", got)
	}
	if _, ok := got["work"]; !ok {
		t.Fatal("caller's own provider must be written")
	}
}

func TestSaveUserConfigPresetsNilBaselinePreservesDiskOnly(t *testing.T) {
	path := staleMergeUserPath(t)
	disk := map[string]routing.ModelPreset{
		"ollama/nomic-embed-text": {Provider: "ollama", Model: "nomic-embed-text", LocalOnly: true},
	}
	if err := SaveUserConfigPresets(path, disk); err != nil {
		t.Fatalf("seed disk: %v", err)
	}

	next := map[string]routing.ModelPreset{
		"work/model-a": {Provider: "work", Model: "model-a"},
	}
	if err := SaveUserConfigPresets(path, next, nil); err != nil {
		t.Fatalf("SaveUserConfigPresets: %v", err)
	}

	got := loadUserPresetsAt(t, path)
	if _, ok := got["ollama/nomic-embed-text"]; !ok {
		t.Fatalf("disk-only preset erased despite nil baseline; got %v", got)
	}
	if _, ok := got["work/model-a"]; !ok {
		t.Fatal("caller's own preset must be written")
	}
}

// TestSaveUserConfigSectionNilBaselineMapsPreserveDiskOnly pins the same
// edge through the section saver used by the config.* tools: a tool process
// whose snapshot carried no providers/presets must not erase foreign disk
// entries when it writes its own first provider.
func TestSaveUserConfigSectionNilBaselineMapsPreserveDiskOnly(t *testing.T) {
	path := staleMergeUserPath(t)

	// Foreign disk entries.
	seed := Default()
	seed.Providers = map[string]ProviderConfig{
		"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
	}
	seed.Models.Presets = map[string]routing.ModelPreset{
		"ollama/nomic-embed-text": {Provider: "ollama", Model: "nomic-embed-text", LocalOnly: true},
	}
	if err := SaveUserConfigSection(path, seed); err != nil {
		t.Fatalf("seed disk: %v", err)
	}

	// The fresh process's snapshot: no providers or presets at all.
	snapshot := Default()
	next := deepCopyConfig(snapshot)
	next.Providers = map[string]ProviderConfig{
		"work": {Type: "openai_compatible", BaseURL: "https://api.work.example/v1"},
	}
	if err := SaveUserConfigSection(path, next, snapshot); err != nil {
		t.Fatalf("SaveUserConfigSection: %v", err)
	}

	if _, ok := loadUserProvidersAt(t, path)["ollama"]; !ok {
		t.Fatal("disk-only provider erased despite nil baseline maps")
	}
	if _, ok := loadUserPresetsAt(t, path)["ollama/nomic-embed-text"]; !ok {
		t.Fatal("disk-only preset erased despite nil baseline maps")
	}
}

// TestSaveUserConfigSectionPreservesDiskOnlyGlobals pins the agent config.*
// tool path (W1): section writes through SaveUserConfigSection must not
// drop providers/presets that exist on disk but are absent from the tool's
// in-memory config snapshot.
func TestSaveUserConfigSectionPreservesDiskOnlyGlobals(t *testing.T) {
	path := staleMergeUserPath(t)

	// The tool process's snapshot: only its own provider.
	snapshot := Default()
	snapshot.Providers = map[string]ProviderConfig{
		"ollama-cloud": {Type: "openai_compatible", BaseURL: "https://api.ollama-cloud.example/v1"},
	}
	snapshot.Models.Presets = map[string]routing.ModelPreset{
		"ollama-cloud/deepseek-v4-flash": {Provider: "ollama-cloud", Model: "deepseek-v4-flash"},
	}
	if err := SaveUserConfigSection(path, snapshot); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	// A second process persists entries this snapshot never saw.
	foreign := Default()
	foreign.Providers = map[string]ProviderConfig{
		"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
	}
	foreign.Models.Presets = map[string]routing.ModelPreset{
		"ollama/nomic-embed-text": {Provider: "ollama", Model: "nomic-embed-text", LocalOnly: true},
	}
	if err := SaveUserConfigSection(path, foreign); err != nil {
		t.Fatalf("seed foreign: %v", err)
	}

	// The first process now writes an unrelated section via the tools path
	// with its stale whole-map snapshot. Baseline = its own snapshot.
	next := deepCopyConfig(snapshot)
	next.Agent.MaxToolIterations = 25
	if err := SaveUserConfigSection(path, next, snapshot); err != nil {
		t.Fatalf("SaveUserConfigSection with baseline: %v", err)
	}

	got := loadUserProvidersAt(t, path)
	if _, ok := got["ollama"]; !ok {
		t.Fatalf("disk-only provider 'ollama' erased by section write; got %v", got)
	}
	gotPresets := loadUserPresetsAt(t, path)
	if _, ok := gotPresets["ollama/nomic-embed-text"]; !ok {
		t.Fatalf("disk-only preset erased by section write; got %v", gotPresets)
	}
}
