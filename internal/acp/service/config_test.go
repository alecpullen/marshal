package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/presetsetup"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// newConfigState builds a session state over dir, carrying cfg.
func newConfigState(t *testing.T, id string, cfg config.Config, dir string) *session.State {
	t.Helper()
	return session.New(cfg, dir, time.Unix(100, 0), session.Persistence{SessionID: id})
}

// loadUserConfig reads the user config a write under home produced.
func loadUserConfig(t *testing.T, home, work string) config.Config {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{HomeDir: home, WorkingDir: work, SkipProjectConfig: true})
	if err != nil {
		t.Fatalf("load user config: %v", err)
	}
	return cfg
}

// TestConfigReadsTheSessionsConfigAndLayers pins the two reads: they answer with
// what the state holds, and a closed session keeps answering with its final
// values (C4).
func TestConfigReadsTheSessionsConfigAndLayers(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Project.Name = "read-me"
	state := newConfigState(t, "s1", cfg, dir)
	layers := config.Default()
	layers.Project.Name = "merged"
	state.SetLayers(config.Layers{Default: config.Default(), Merged: layers})

	h, sess := New(Deps{State: state})

	if got := sess.Config().Project.Name; got != "read-me" {
		t.Errorf("Config().Project.Name = %q, want %q", got, "read-me")
	}
	if got := sess.Layers().Merged.Project.Name; got != "merged" {
		t.Errorf("Layers().Merged.Project.Name = %q, want %q", got, "merged")
	}

	h.swap(newConfigState(t, "s2", config.Default(), t.TempDir()))

	if got := sess.Config().Project.Name; got != "read-me" {
		t.Errorf("closed session Config().Project.Name = %q, want its final value %q", got, "read-me")
	}
	if got := sess.Layers().Merged.Project.Name; got != "merged" {
		t.Errorf("closed session Layers().Merged.Project.Name = %q, want its final value %q", got, "merged")
	}
}

// TestStatusNamesTheConfigFiles pins the two status fields: the user config is
// found under the home, or is the one the caller injected, and the project
// config is the working directory's.
func TestStatusNamesTheConfigFiles(t *testing.T) {
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	dir := t.TempDir()
	state := newConfigState(t, "s1", config.Default(), dir)

	_, sess := New(Deps{State: state, HomeDir: home})
	status := sess.Status()
	if want := config.UserConfigPath(home); status.UserConfigPath != want {
		t.Errorf("UserConfigPath = %q, want %q", status.UserConfigPath, want)
	}
	if want := filepath.Join(dir, ".marshal", "config.toml"); status.ProjectConfigPath != want {
		t.Errorf("ProjectConfigPath = %q, want %q", status.ProjectConfigPath, want)
	}

	explicit := filepath.Join(t.TempDir(), "explicit.toml")
	_, injected := New(Deps{State: state, HomeDir: home, UserConfigPath: explicit})
	if got := injected.Status().UserConfigPath; got != explicit {
		t.Errorf("UserConfigPath = %q, want the injected %q", got, explicit)
	}
}

// TestSaveProjectConfigRefreshesTrustThenLayers pins the order: the project
// file is written, the trust record's hash advances, and only then are the
// layers re-read, so the reload sees the save.
func TestSaveProjectConfigRefreshesTrustThenLayers(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Project.Name = "saved-project"
	state := newConfigState(t, "s1", cfg, dir)

	var order []string
	marked := config.Default()
	marked.Project.Name = "marked"
	_, sess := New(Deps{
		State: state,
		TrustRefresh: func(got string) {
			order = append(order, "trust:"+got)
		},
		ReloadLayers: func() (config.Layers, bool) {
			order = append(order, "layers")
			return config.Layers{Merged: marked}, true
		},
	})

	if err := sess.SaveProjectConfig(context.Background(), cfg); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	data, err := os.ReadFile(config.ProjectConfigPath(dir))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if !strings.Contains(string(data), "saved-project") {
		t.Errorf("project config does not carry the saved name:\n%s", data)
	}
	if want := []string{"trust:" + dir, "layers"}; !reflect.DeepEqual(order, want) {
		t.Errorf("hook order = %v, want %v", order, want)
	}
	if got := sess.Layers().Merged.Project.Name; got != "marked" {
		t.Errorf("Layers().Merged.Project.Name = %q, want the reloaded %q", got, "marked")
	}

	// A working directory under a regular file cannot be created, so the save
	// fails before either hook runs.
	parent := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(parent, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write the blocking file: %v", err)
	}
	badDir := filepath.Join(parent, "work")
	calls := 0
	_, badSess := New(Deps{
		State:        newConfigState(t, "s2", cfg, badDir),
		TrustRefresh: func(string) { calls++ },
		ReloadLayers: func() (config.Layers, bool) { calls++; return config.Layers{}, true },
	})
	if err := badSess.SaveProjectConfig(context.Background(), cfg); err == nil {
		t.Fatal("SaveProjectConfig under a regular file = nil, want an error")
	}
	if calls != 0 {
		t.Errorf("hooks ran %d times after a failed save, want 0", calls)
	}
}

// TestSaveVerifyCommandsRefreshesOnlyLayers pins the verify save: the file gains
// the commands, the layers are re-read, and the trust record is left alone.
func TestSaveVerifyCommandsRefreshesOnlyLayers(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	state := newConfigState(t, "s1", config.Default(), dir)

	refreshes := 0
	trustCalls := 0
	_, sess := New(Deps{
		State:        state,
		HomeDir:      home,
		TrustRefresh: func(string) { trustCalls++ },
		ReloadLayers: func() (config.Layers, bool) {
			refreshes++
			layers, err := config.LoadSessionLayers(home, dir, true)
			return layers, err == nil
		},
	})

	if err := sess.SaveVerifyCommands(context.Background(), "go build ./...", "go test ./..."); err != nil {
		t.Fatalf("SaveVerifyCommands: %v", err)
	}
	layers := sess.Layers()
	if got := layers.Merged.SDD.Verify.Build; got != "go build ./..." {
		t.Errorf("Layers().Merged.SDD.Verify.Build = %q, want the saved command", got)
	}
	if got := layers.Merged.SDD.Verify.Test; got != "go test ./..." {
		t.Errorf("Layers().Merged.SDD.Verify.Test = %q, want the saved command", got)
	}
	if refreshes != 1 {
		t.Errorf("layer reloads = %d, want 1", refreshes)
	}
	if trustCalls != 0 {
		t.Errorf("TrustRefresh calls = %d, want 0: a verify save changes no trust hash", trustCalls)
	}
}

// TestUserWritesUseTheUserConfigPathAndRefreshLayers pins the four user-config
// writes: each lands in Deps.UserConfigPath, keeps what another process wrote,
// and refreshes the session's layers.
func TestUserWritesUseTheUserConfigPathAndRefreshLayers(t *testing.T) {
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	dir := t.TempDir()
	path := config.UserConfigPath(home)
	state := newConfigState(t, "s1", config.Default(), dir)

	refreshes := 0
	marked := config.Default()
	marked.Project.Name = "marked"
	host, _ := New(Deps{
		State:          state,
		HomeDir:        home,
		UserConfigPath: path,
		ReloadLayers: func() (config.Layers, bool) {
			refreshes++
			return config.Layers{Merged: marked}, true
		},
	})
	ctx := context.Background()

	if err := host.SaveUserValue(ctx, "tools.shell.allow_network", true); err != nil {
		t.Fatalf("SaveUserValue: %v", err)
	}
	if got := loadUserConfig(t, home, dir); !got.Tools.Shell.AllowNetwork {
		t.Error("tools.shell.allow_network = false after SaveUserValue, want true")
	}

	// The disk entry "beta" is in neither the write nor the baseline, so it was
	// written by another process and survives.
	disk := map[string]config.ProviderConfig{
		"alpha": {BaseURL: "https://alpha.test/v1"},
		"beta":  {BaseURL: "https://beta.test/v1"},
	}
	if err := config.SaveUserConfigProviders(path, disk); err != nil {
		t.Fatalf("seed providers: %v", err)
	}
	write := map[string]config.ProviderConfig{"alpha": disk["alpha"]}
	baseline := map[string]config.ProviderConfig{"alpha": disk["alpha"]}
	if err := host.SaveUserProviders(ctx, write, baseline, nil); err != nil {
		t.Fatalf("SaveUserProviders: %v", err)
	}
	got := loadUserConfig(t, home, dir)
	if _, ok := got.Providers["beta"]; !ok {
		t.Errorf("providers = %v, want the foreign entry beta kept", got.Providers)
	}
	if _, ok := got.Providers["alpha"]; !ok {
		t.Errorf("providers = %v, want the written entry alpha", got.Providers)
	}

	preset := routing.ModelPreset{Provider: "alpha", Model: "m1"}
	err := host.SaveUserPresets(ctx, map[string]routing.ModelPreset{"alpha/m1": preset}, map[string]routing.ModelPreset{})
	if err != nil {
		t.Fatalf("SaveUserPresets: %v", err)
	}
	got = loadUserConfig(t, home, dir)
	if _, ok := got.Models.Presets["alpha/m1"]; !ok {
		t.Errorf("presets = %v, want the written %q", got.Models.Presets, "alpha/m1")
	}

	if err := host.SaveProviderKey(ctx, "alpha", "sk-test"); err != nil {
		t.Fatalf("SaveProviderKey: %v", err)
	}
	entry := loadUserConfig(t, home, dir).Providers["alpha"]
	if entry.BaseURL != "https://alpha.test/v1" {
		t.Errorf("alpha base URL = %q, want the entry's own %q", entry.BaseURL, "https://alpha.test/v1")
	}
	if entry.APIKey != "sk-test" {
		t.Errorf("alpha API key = %q, want %q", entry.APIKey, "sk-test")
	}

	if refreshes != 4 {
		t.Errorf("layer reloads = %d, want one per successful write (4)", refreshes)
	}
	if got := state.Layers().Merged.Project.Name; got != "marked" {
		t.Errorf("session layers = %q, want the reloaded %q", got, "marked")
	}
}

// TestAFailedLayerReloadKeepsTheLayers pins ReloadLayers' false: a failed read
// leaves the session with the layers it had.
func TestAFailedLayerReloadKeepsTheLayers(t *testing.T) {
	dir := t.TempDir()
	state := newConfigState(t, "s1", config.Default(), dir)
	previous := config.Default()
	previous.Project.Name = "previous"
	state.SetLayers(config.Layers{Merged: previous})

	_, sess := New(Deps{
		State:        state,
		ReloadLayers: func() (config.Layers, bool) { return config.Layers{}, false },
	})
	if err := sess.SaveVerifyCommands(context.Background(), "go build", "go test"); err != nil {
		t.Fatalf("SaveVerifyCommands: %v", err)
	}
	if got := sess.Layers().Merged.Project.Name; got != "previous" {
		t.Errorf("Layers().Merged.Project.Name = %q after a failed reload, want %q", got, "previous")
	}
}

// TestReloadConfigReportsTheReloadPath pins ReloadConfig: without a reloader it
// reports false and changes nothing, and with one it hands cfg over and returns
// the rebuild's error.
func TestReloadConfigReportsTheReloadPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Default()
	state := newConfigState(t, "s1", cfg, dir)
	next := config.Default()
	next.Project.Name = "next"

	_, sess := New(Deps{State: state})
	ok, err := sess.ReloadConfig(ctx, next)
	if ok || err != nil {
		t.Fatalf("ReloadConfig without a reloader = (%v, %v), want (false, nil)", ok, err)
	}
	if got := sess.Config().Project.Name; got != cfg.Project.Name {
		t.Errorf("Config().Project.Name = %q after a refused reload, want the unchanged %q", got, cfg.Project.Name)
	}

	reloadErr := errors.New("rebuild failed")
	var got config.Config
	_, withReload := New(Deps{
		State:        state,
		ReloadConfig: func(c config.Config) error { got = c; return reloadErr },
	})
	ok, err = withReload.ReloadConfig(ctx, next)
	if !ok {
		t.Error("ReloadConfig with a reloader reported false, want true")
	}
	if !errors.Is(err, reloadErr) {
		t.Errorf("ReloadConfig error = %v, want the reloader's %v", err, reloadErr)
	}
	if got.Project.Name != "next" {
		t.Errorf("reloader received project name %q, want %q", got.Project.Name, "next")
	}
}

// TestApplyConfigInstallsWithoutSaving pins ApplyConfig: the session's config
// changes and no file is written.
func TestApplyConfigInstallsWithoutSaving(t *testing.T) {
	dir := t.TempDir()
	state := newConfigState(t, "s1", config.Default(), dir)
	_, sess := New(Deps{State: state})

	next := config.Default()
	next.Project.Name = "installed"
	if err := sess.ApplyConfig(context.Background(), next); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	if got := sess.Config().Project.Name; got != "installed" {
		t.Errorf("Config().Project.Name = %q, want the installed %q", got, "installed")
	}
	if _, err := os.Stat(config.ProjectConfigPath(dir)); !os.IsNotExist(err) {
		t.Errorf("project config stat = %v, want it not to exist", err)
	}
}

// TestReconnectMCPClearsNoticesBeforeTheReload pins the order the MCP sign-in
// needs: the notices are gone when the rebuild runs, so a fresh notice it sets
// survives.
func TestReconnectMCPClearsNoticesBeforeTheReload(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	state := newConfigState(t, "s1", config.Default(), dir)
	state.SetNotice(session.Notice{Category: session.NoticeProvider, Message: "needs OAuth"})

	sawNotice := true
	reloadErr := errors.New("the server still fails")
	_, sess := New(Deps{
		State: state,
		ReloadConfig: func(cfg config.Config) error {
			_, sawNotice = state.Notice()
			state.SetNotice(session.Notice{Category: session.NoticeConfig, Message: "rebuild failed"})
			return reloadErr
		},
	})
	reloaded, err := sess.ReconnectMCP(ctx)
	if !reloaded {
		t.Error("ReconnectMCP with a reloader reported false, want true")
	}
	if !errors.Is(err, reloadErr) {
		t.Errorf("ReconnectMCP error = %v, want the reloader's %v", err, reloadErr)
	}
	if sawNotice {
		t.Error("the rebuild saw a notice, want the provider and config notices cleared first")
	}
	notice := sess.Status().Notice
	if notice == nil || notice.Category != acpwire.NoticeConfig {
		t.Errorf("Status().Notice = %+v, want the rebuild's fresh config notice kept", notice)
	}

	// Without a reloader the call reports false and leaves the notice alone.
	state.SetNotice(session.Notice{Category: session.NoticeProvider, Message: "needs OAuth"})
	_, plain := New(Deps{State: state})
	reloaded, err = plain.ReconnectMCP(ctx)
	if reloaded || err != nil {
		t.Fatalf("ReconnectMCP without a reloader = (%v, %v), want (false, nil)", reloaded, err)
	}
	if got, ok := state.Notice(); !ok || got.Category != session.NoticeProvider {
		t.Errorf("notice = (%+v, %v) after a refused reconnect, want the provider notice kept", got, ok)
	}
}

// TestPreparePresetsRunsPresetSetup pins preset setup over the seam: one preset
// per reported model, the provenance returned raw, and the caller's config left
// alone. A sidecar it cannot read yields no provenance at all.
func TestPreparePresetsRunsPresetSetup(t *testing.T) {
	ctx := context.Background()
	state := newConfigState(t, "s1", config.Default(), t.TempDir())
	models := []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}, {ID: "llama3"}}

	dir := t.TempDir()
	host, _ := New(Deps{State: state, ModelCacheDir: dir})
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{"keep/me": {Provider: "keep", Model: "me"}}

	presets, setup, err := host.PreparePresets(ctx, cfg, "ollama", "http://127.0.0.1:11434", models)
	if err != nil {
		t.Fatalf("PreparePresets: %v", err)
	}
	for _, name := range []string{"ollama/qwen2.5-coder:7b", "ollama/llama3"} {
		if _, ok := presets[name]; !ok {
			t.Errorf("presets = %v, want one for %q", presets, name)
		}
	}
	if got := len(cfg.Models.Presets); got != 1 {
		t.Errorf("the caller's preset map holds %d entries, want the one it passed: Apply must work on a copy", got)
	}
	if got, want := setup.Managed, []string{"ollama/llama3", "ollama/qwen2.5-coder:7b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Managed = %v, want %v", got, want)
	}
	if setup.Prepared != 2 {
		t.Errorf("Prepared = %d, want 2", setup.Prepared)
	}
	var provenance presetsetup.Provenance
	if err := json.Unmarshal(setup.Provenance, &provenance); err != nil {
		t.Fatalf("decode Provenance: %v", err)
	}
	if got := len(provenance.Presets); got != 2 {
		t.Errorf("provenance holds %d presets, want 2", got)
	}

	// A corrupt sidecar is an error to read, so no provenance is reported and
	// nothing may be saved.
	corrupt := t.TempDir()
	path := presetsetup.ProvenancePath(corrupt)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create the sidecar's directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write the corrupt sidecar: %v", err)
	}
	corruptHost, _ := New(Deps{State: state, ModelCacheDir: corrupt})
	presets, setup, err = corruptHost.PreparePresets(ctx, config.Default(), "ollama", "http://127.0.0.1:11434", models)
	if err != nil {
		t.Fatalf("PreparePresets with a corrupt sidecar: %v", err)
	}
	if got := len(presets); got != 2 {
		t.Errorf("presets = %d, want the two reported models created anyway", got)
	}
	if setup.Provenance != nil {
		t.Errorf("Provenance = %s, want nil for a sidecar it cannot read", setup.Provenance)
	}
	if len(setup.Managed) != 0 {
		t.Errorf("Managed = %v, want empty without a provenance", setup.Managed)
	}
}

// TestSaveProvenanceWritesTheSidecar pins the write: the raw value lands at the
// sidecar's path, and a nil value writes nothing.
func TestSaveProvenanceWritesTheSidecar(t *testing.T) {
	ctx := context.Background()
	state := newConfigState(t, "s1", config.Default(), t.TempDir())
	dir := t.TempDir()
	host, _ := New(Deps{State: state, ModelCacheDir: dir})

	raw := json.RawMessage(`{"version":1,"presets":{"ollama/m":{"catalog_provider":"ollama","catalog_model":"m"}}}`)
	if err := host.SaveProvenance(ctx, raw); err != nil {
		t.Fatalf("SaveProvenance: %v", err)
	}
	data, err := os.ReadFile(presetsetup.ProvenancePath(dir))
	if err != nil {
		t.Fatalf("read the sidecar: %v", err)
	}
	var p presetsetup.Provenance
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("decode the sidecar: %v", err)
	}
	if _, ok := p.Presets["ollama/m"]; !ok {
		t.Errorf("sidecar presets = %v, want %q", p.Presets, "ollama/m")
	}

	empty := t.TempDir()
	emptyHost, _ := New(Deps{State: state, ModelCacheDir: empty})
	if err := emptyHost.SaveProvenance(ctx, nil); err != nil {
		t.Fatalf("SaveProvenance(nil): %v", err)
	}
	if _, err := os.Stat(presetsetup.ProvenancePath(empty)); !os.IsNotExist(err) {
		t.Errorf("sidecar stat after a nil save = %v, want it not to exist", err)
	}
}

// TestClosedSessionConfigCallsFail pins C4 for the calls this plan adds: a
// session a swap replaced refuses them all.
func TestClosedSessionConfigCallsFail(t *testing.T) {
	ctx := context.Background()
	state := newConfigState(t, "s1", config.Default(), t.TempDir())
	h, old := New(Deps{State: state})
	h.swap(newConfigState(t, "s2", config.Default(), t.TempDir()))

	calls := map[string]error{
		"ApplyConfig":        old.ApplyConfig(ctx, config.Default()),
		"SaveProjectConfig":  old.SaveProjectConfig(ctx, config.Default()),
		"SaveVerifyCommands": old.SaveVerifyCommands(ctx, "go build", "go test"),
	}
	_, reloadErr := old.ReloadConfig(ctx, config.Default())
	calls["ReloadConfig"] = reloadErr
	_, reconnectErr := old.ReconnectMCP(ctx)
	calls["ReconnectMCP"] = reconnectErr

	for name, err := range calls {
		if !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
			t.Errorf("%s on a closed session = %v, want %q", name, err, acpwire.CodeSessionClosed)
		}
	}
}
