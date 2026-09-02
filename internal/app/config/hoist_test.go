package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"marshal/internal/trust"
)

// hoistPaths returns the user and project config paths under fresh temp dirs.
func hoistPaths(t *testing.T) (home, work, userPath, projectPath string) {
	t.Helper()
	home = t.TempDir()
	work = t.TempDir()
	return home, work, UserConfigPath(home), ProjectConfigPath(work)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestHoistMovesProviderAbsentFromUserConfig(t *testing.T) {
	home, work, userPath, projectPath := hoistPaths(t)
	writeFile(t, projectPath, `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "local-key"

[project]
name = "repo"
`)

	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if l.HoistError != nil {
		t.Fatalf("HoistError: %v", l.HoistError)
	}
	if !slices.Equal(l.HoistedProviders, []string{"ollama"}) {
		t.Fatalf("HoistedProviders = %v", l.HoistedProviders)
	}
	if len(l.HoistConflicts) != 0 {
		t.Fatalf("HoistConflicts = %v", l.HoistConflicts)
	}

	// The merged config still sees the provider (it merged before the hoist).
	if got := l.Merged.Providers["ollama"].APIKey; got != "local-key" {
		t.Fatalf("merged api_key = %q, want local-key", got)
	}

	// The entry, credential included, landed in the user file…
	user := readFile(t, userPath)
	for _, want := range []string{"[providers.ollama]", `api_key = 'local-key'`, "# Marshal global configuration"} {
		if !strings.Contains(user, want) {
			t.Errorf("user config missing %q:\n%s", want, user)
		}
	}
	// …and left the project file, which keeps its other sections.
	project := readFile(t, projectPath)
	if strings.Contains(project, "providers") {
		t.Errorf("project config still carries providers:\n%s", project)
	}
	if !strings.Contains(project, `name = 'repo'`) {
		t.Errorf("project config lost [project] section:\n%s", project)
	}

	// The user file holds credentials, so it must be owner-only.
	info, err := os.Stat(userPath)
	if err != nil {
		t.Fatalf("stat user config: %v", err)
	}
	if info.Mode().Perm() != userConfigFileMode {
		t.Errorf("user config mode = %o, want %o", info.Mode().Perm(), userConfigFileMode)
	}
}

func TestHoistMovesPresetAbsentFromUserConfig(t *testing.T) {
	home, work, userPath, projectPath := hoistPaths(t)
	writeFile(t, projectPath, `
[models.presets."ollama/qwen3"]
context_window = 32768
local_only = true
`)

	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if !slices.Equal(l.HoistedPresets, []string{"ollama/qwen3"}) {
		t.Fatalf("HoistedPresets = %v", l.HoistedPresets)
	}

	user := readFile(t, userPath)
	if !strings.Contains(user, "ollama/qwen3") || !strings.Contains(user, "context_window = 32768") {
		t.Errorf("user config missing moved preset:\n%s", user)
	}
	if project := readFile(t, projectPath); strings.Contains(project, "models") {
		t.Errorf("project config still carries presets:\n%s", project)
	}

	// The moved preset round-trips: a fresh load (nothing left to hoist)
	// still resolves it with provider/model derived from the canonical key.
	l2, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("second LoadLayers: %v", err)
	}
	preset := l2.Merged.Models.Presets["ollama/qwen3"]
	if preset.Provider != "ollama" || preset.Model != "qwen3" || !preset.LocalOnly {
		t.Fatalf("preset after reload = %#v", preset)
	}
	if len(l2.HoistedPresets) != 0 || len(l2.HoistedProviders) != 0 {
		t.Errorf("second load re-hoisted: %v %v", l2.HoistedProviders, l2.HoistedPresets)
	}
}

func TestHoistDropsIdenticalEntries(t *testing.T) {
	home, work, userPath, projectPath := hoistPaths(t)
	writeFile(t, userPath, `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "user-key"

[models.presets."ollama/qwen3"]
provider = "ollama"
model = "qwen3"
local_only = true
`)
	writeFile(t, projectPath, `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"

[models.presets."ollama/qwen3"]
provider = "ollama"
model = "qwen3"
local_only = true
`)

	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if !slices.Equal(l.HoistedProviders, []string{"ollama"}) || !slices.Equal(l.HoistedPresets, []string{"ollama/qwen3"}) {
		t.Fatalf("hoisted = %v / %v", l.HoistedProviders, l.HoistedPresets)
	}
	if len(l.HoistConflicts) != 0 {
		t.Fatalf("HoistConflicts = %v", l.HoistConflicts)
	}

	// The duplicates are pruned from the project file; the user entry keeps
	// its credential (the project copy named none).
	project := readFile(t, projectPath)
	if strings.Contains(project, "providers") || strings.Contains(project, "models") {
		t.Errorf("project config not pruned:\n%s", project)
	}
	if got := l.Merged.Providers["ollama"].APIKey; got != "user-key" {
		t.Errorf("merged api_key = %q, want user-key", got)
	}
	if !strings.Contains(readFile(t, userPath), `api_key = 'user-key'`) {
		t.Error("user config lost its api_key")
	}
}

func TestHoistCarriesCredentialUpOnDrop(t *testing.T) {
	home, work, userPath, projectPath := hoistPaths(t)
	writeFile(t, userPath, `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
`)
	writeFile(t, projectPath, `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "project-key"
`)

	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if len(l.HoistConflicts) != 0 {
		t.Fatalf("HoistConflicts = %v", l.HoistConflicts)
	}
	if !strings.Contains(readFile(t, userPath), `api_key = 'project-key'`) {
		t.Errorf("credential not carried up:\n%s", readFile(t, userPath))
	}
	if project := readFile(t, projectPath); strings.Contains(project, "providers") {
		t.Errorf("project config not pruned:\n%s", project)
	}
	// The merge layer's inheritance already gave the merged config the key.
	if got := l.Merged.Providers["ollama"].APIKey; got != "project-key" {
		t.Errorf("merged api_key = %q, want project-key", got)
	}
}

func TestHoistKeepsConflictingEntries(t *testing.T) {
	home, work, userPath, projectPath := hoistPaths(t)
	writeFile(t, userPath, `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"

[models.presets."ollama/qwen3"]
context_window = 32768
`)
	writeFile(t, projectPath, `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:9999/v1"

[models.presets."ollama/qwen3"]
context_window = 65536
`)

	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	want := []string{"models.presets.ollama/qwen3", "providers.ollama"}
	if !slices.Equal(l.HoistConflicts, want) {
		t.Fatalf("HoistConflicts = %v, want %v", l.HoistConflicts, want)
	}
	if len(l.HoistedProviders) != 0 || len(l.HoistedPresets) != 0 {
		t.Errorf("unexpected hoists: %v %v", l.HoistedProviders, l.HoistedPresets)
	}

	// Conflicting entries stay project-local and still win the merge.
	project := readFile(t, projectPath)
	if !strings.Contains(project, "9999") || !strings.Contains(project, "65536") {
		t.Errorf("conflicting entries removed from project config:\n%s", project)
	}
	if got := l.Merged.Providers["ollama"].BaseURL; got != "http://localhost:9999/v1" {
		t.Errorf("merged base_url = %q, want project override", got)
	}
	if got := l.Merged.Models.Presets["ollama/qwen3"].ContextWindow; got != 65536 {
		t.Errorf("merged context_window = %d, want project override", got)
	}
}

func TestHoistWriteFailureLeavesFilesAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	home, work, userPath, projectPath := hoistPaths(t)
	writeFile(t, projectPath, `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
`)
	// Make the user config's parent directory unwritable so the hoist's
	// writeUserConfigFile fails.
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(filepath.Dir(userPath), 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(userPath), 0o700) })

	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers must not fail on a hoist write error: %v", err)
	}
	if l.HoistError == nil {
		t.Fatal("HoistError not recorded")
	}
	// The merged config is unaffected…
	if _, ok := l.Merged.Providers["ollama"]; !ok {
		t.Error("merged config lost the provider on hoist failure")
	}
	// …and neither file changed: the next load retries the hoist.
	if _, statErr := os.Stat(userPath); !os.IsNotExist(statErr) {
		t.Error("user config written despite the failure")
	}
	if project := readFile(t, projectPath); !strings.Contains(project, "[providers.ollama]") {
		t.Errorf("project config modified despite the failure:\n%s", project)
	}
}

func TestHoistNeverRunsForUntrustedFolder(t *testing.T) {
	home, work, userPath, projectPath := hoistPaths(t)
	writeFile(t, projectPath, `
[providers.ollama]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
`)

	l, err := LoadLayers(LoadOptions{
		HomeDir:       home,
		WorkingDir:    work,
		TrustResolver: trust.FixedResolver{Decision: trust.DecisionDontTrust},
	})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if len(l.HoistedProviders) != 0 || len(l.HoistConflicts) != 0 {
		t.Errorf("untrusted load hoisted: %v %v", l.HoistedProviders, l.HoistConflicts)
	}
	if _, ok := l.Merged.Providers["ollama"]; ok {
		t.Error("untrusted project config contributed a provider")
	}
	if _, statErr := os.Stat(userPath); !os.IsNotExist(statErr) {
		t.Error("user config written for an untrusted folder")
	}
	if project := readFile(t, projectPath); !strings.Contains(project, "[providers.ollama]") {
		t.Errorf("untrusted project config modified:\n%s", project)
	}
}

func TestHoistNoChangesWritesNothing(t *testing.T) {
	home, work, userPath, projectPath := hoistPaths(t)
	writeFile(t, projectPath, "[project]\nname = \"repo\"\n")

	before := readFile(t, projectPath)
	info, err := os.Stat(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	mtime := info.ModTime()

	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("LoadLayers: %v", err)
	}
	if len(l.HoistedProviders) != 0 || len(l.HoistedPresets) != 0 || len(l.HoistConflicts) != 0 {
		t.Errorf("unexpected hoist activity: %v %v %v", l.HoistedProviders, l.HoistedPresets, l.HoistConflicts)
	}
	if _, statErr := os.Stat(userPath); !os.IsNotExist(statErr) {
		t.Error("user config created for a project without global sections")
	}
	if after := readFile(t, projectPath); after != before {
		t.Errorf("project config rewritten without changes:\n%s", after)
	}
	if info2, _ := os.Stat(projectPath); !info2.ModTime().Equal(mtime) {
		t.Error("project config mtime changed without changes")
	}
}

func TestDiagnoseReportsHoistOutcome(t *testing.T) {
	layers := Layers{
		HoistedProviders: []string{"a", "b"},
		HoistedPresets:   []string{"p"},
		HoistConflicts:   []string{"providers.x"},
	}
	ds := Diagnose(Default(), layers)

	var conflict, providerInfo, presetInfo *Diagnostic
	for i := range ds {
		switch ds[i].Path {
		case "providers.x":
			conflict = &ds[i]
		case "providers":
			providerInfo = &ds[i]
		case "models.presets":
			presetInfo = &ds[i]
		}
	}
	if conflict == nil || conflict.Severity != SeverityWarning {
		t.Errorf("conflict diagnostic = %+v, want a warning at providers.x", conflict)
	}
	if conflict != nil && !strings.Contains(conflict.Message, `"providers.x"`) {
		t.Errorf("conflict message = %q, want the entry named", conflict.Message)
	}
	if providerInfo == nil || providerInfo.Severity != SeverityInfo ||
		!strings.Contains(providerInfo.Message, "2 provider(s)") {
		t.Errorf("provider hoist diagnostic = %+v, want info naming 2 providers", providerInfo)
	}
	if presetInfo == nil || presetInfo.Severity != SeverityInfo ||
		!strings.Contains(presetInfo.Message, "1 preset(s)") {
		t.Errorf("preset hoist diagnostic = %+v, want info naming 1 preset", presetInfo)
	}

	// Empty hoist state stays silent.
	if ds := Diagnose(Default(), Layers{}); len(ds) != 0 {
		t.Errorf("empty hoist state produced %v", diagPaths(ds))
	}
}
