package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/presetsetup"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/credentials"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/modelcache"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/oauth"
	"marshal/internal/strutil"
	"marshal/internal/trust"
)

// openSettings opens one arm of both seams: the service itself, or the
// client's mirror of it over a pipe (V1).
type openSettings func(t *testing.T, d service.Deps) (hostSeam, seam)

// newConfigState builds a session state over dir, carrying cfg. It is the
// service's own fixture, copied here so the harness does not depend on its
// package.
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

const (
	secretKey    = "sk-test-SECRET-1234"
	secretSearch = "search-SECRET-2345"
	secretCDP    = "ws://user:cdp-SECRET-pw@127.0.0.1:9222"
)

// secretConfig is a config carrying a literal value in each secret-bearing
// field: a provider's API key, the web search key and the desktop CDP URL's
// userinfo.
func secretConfig() config.Config {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"probe": {Type: "openai_compatible", BaseURL: "https://probe.test/v1", APIKey: secretKey},
	}
	cfg.Web.SearchKey = secretSearch
	cfg.Desktop.CDPURL = secretCDP
	return cfg
}

// testAuth is a test AuthResolver: a private in-memory token store, the
// failures a test asks for, and a record of what was asked for. Engines are
// cached by provider name, mirroring provider.scopedAuthResolver: a caller that
// asks twice gets the same engine, so a pinned test engine stays pinned.
type testAuth struct {
	store     *credentials.MemStore
	storeErr  error
	engineErr error
	services  []string
	engines   []string
	cache     map[string]*oauth.Engine
}

func newTestAuth() *testAuth {
	return &testAuth{store: credentials.NewMemStore(), cache: map[string]*oauth.Engine{}}
}

func (a *testAuth) OpenStore(service string) (credentials.Store, error) {
	a.services = append(a.services, service)
	if a.storeErr != nil {
		return nil, a.storeErr
	}
	return a.store, nil
}

func (a *testAuth) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	a.engines = append(a.engines, name)
	if a.engineErr != nil {
		return nil, a.engineErr
	}
	if eng, ok := a.cache[name]; ok {
		return eng, nil
	}
	eng, err := provider.NewCodexOAuthEngineWithStore(name, a.store)
	if err != nil {
		return nil, err
	}
	a.cache[name] = eng
	return eng, nil
}

// cacheState builds a state holding one provider called "local".
func cacheState(t *testing.T) *session.State {
	t.Helper()
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	return newConfigState(t, "s1", cfg, t.TempDir())
}

// settingsTemplateWire is the wire form of a catalog entry: what the service's
// own mapping produces, repeated here because the case compares every field of
// it and the mapping is unexported there.
func settingsTemplateWire(tpl provider.ProviderTemplate) acpwire.ProviderTemplate {
	return acpwire.ProviderTemplate{
		ID:                tpl.ID,
		Label:             tpl.Label,
		Type:              tpl.Type,
		BaseURL:           tpl.BaseURL,
		Local:             tpl.Local,
		ToolCalling:       tpl.ToolCalling,
		StructuredOutput:  tpl.StructuredOutput,
		TemperatureLocked: tpl.TemperatureLocked,
		Auth:              tpl.Auth,
		KeyEnv:            tpl.KeyEnv,
		KeyHint:           tpl.KeyHint,
		Models:            tpl.Models,
	}
}

// TestSettingsConformance runs every settings case against both arms of the
// seam: the service itself, and the client's mirror of it over a pipe. The
// same answers from both is V1.
func TestSettingsConformance(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, open openSettings)
	}{
		{name: "the config reads are the session's", run: configReadsTheSessionsConfigAndLayers},
		{name: "a project save refreshes trust then layers", run: saveProjectConfigRefreshesTrustThenLayers},
		{name: "a verify save refreshes only layers", run: saveVerifyCommandsRefreshesOnlyLayers},
		{name: "the user writes use the user config path", run: userWritesUseTheUserConfigPathAndRefreshLayers},
		{name: "a failed layer reload keeps the layers", run: aFailedLayerReloadKeepsTheLayers},
		{name: "a reload reports its own path", run: reloadConfigReportsTheReloadPath},
		{name: "an apply installs without saving", run: applyConfigInstallsWithoutSaving},
		{name: "a reconnect clears the notices first", run: reconnectMCPClearsNoticesBeforeTheReload},
		{name: "prepare presets runs preset setup", run: preparePresetsRunsPresetSetup},
		{name: "a provenance save writes the sidecar", run: saveProvenanceWritesTheSidecar},
		{name: "the provider templates are the catalog", run: providerTemplatesListTheCatalog},
		{name: "a probe lists the models", run: probeProviderListsModels},
		{name: "a failed probe reports it", run: probeProviderReportsFailures},
		{name: "a probe keeps no limits", run: probeProviderPreservesModelLimits},
		{name: "a probe uses the adapters resolver", run: probeProviderUsesTheAdaptersResolver},
		{name: "a model can be probed", run: canProbeModel},
		{name: "a probe reads ollama's capabilities", run: probeModelProbesOllama},
		{name: "the model cache round trips", run: modelCacheRoundTrip},
		{name: "the model cache ignores what persist ignored", run: modelCacheIgnoresWhatPersistIgnored},
		{name: "a prepared login reports the resolver", run: prepareProviderLoginReportsTheResolver},
		{name: "a sign-in runs the flow", run: loginProviderRunsTheFlow},
		{name: "a sign-in codes an unavailable keychain", run: loginProviderCodesAnUnavailableKeychain},
		{name: "an mcp sign-in composes the server and the store", run: loginMCPComposesTheServerAndTheStore},
		{name: "a prepared mcp login says why", run: prepareMCPLoginSentences},
		{name: "a trust decision passes through", run: decideTrustPassesTheDecision},
		{name: "a trust decision for what is not pending is refused", run: decideTrustRefusesWhatIsNotPending},
		{name: "the trust prompt is the deps", run: trustPromptIsTheDeps},
		{name: "tools reads the runtime", run: toolsReadsTheRuntime},
		{name: "the config reads carry no secret", run: configReadsCarryNoSecret},
		{name: "an install restores the stored key", run: installsRestoreTheStoredKey},
		{name: "a project save restores the search key", run: saveProjectConfigRestoresTheSearchKey},
		{name: "a provider save restores the keys", run: saveUserProvidersRestoresKeys},
		{name: "a user value restores a masked key", run: saveUserValueRestoresAMaskedKey},
		{name: "a user value rejects an unmatched mask", run: saveUserValueRejectsAnUnmatchedMask},
		{name: "a provider key rejects a mask", run: saveProviderKeyRejectsAMask},
		{name: "an unmatched mask changes nothing", run: anUnmatchedMaskChangesNothing},
		{name: "a probe uses the stored key", run: probesUseTheStoredKey},
	}
	for _, arm := range hostArms() {
		for _, tc := range cases {
			t.Run(arm.name+"/"+tc.name, func(t *testing.T) {
				tc.run(t, arm.open)
			})
		}
	}
}

// configReadsTheSessionsConfigAndLayers pins the two reads: they answer with
// what the state holds. The closed-session half of the service's own case
// stays there: it drives the in-process host's swap.
func configReadsTheSessionsConfigAndLayers(t *testing.T, open openSettings) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Project.Name = "read-me"
	state := newConfigState(t, "s1", cfg, dir)
	layers := config.Default()
	layers.Project.Name = "merged"
	state.SetLayers(config.Layers{Default: config.Default(), Merged: layers})

	_, sess := open(t, service.Deps{State: state})

	if got := sess.Config().Project.Name; got != "read-me" {
		t.Errorf("Config().Project.Name = %q, want %q", got, "read-me")
	}
	if got := sess.Layers().Merged.Project.Name; got != "merged" {
		t.Errorf("Layers().Merged.Project.Name = %q, want %q", got, "merged")
	}
}

// saveProjectConfigRefreshesTrustThenLayers pins the order: the project file is
// written, the trust record's hash advances, and only then are the layers
// re-read, so the reload sees the save.
func saveProjectConfigRefreshesTrustThenLayers(t *testing.T, open openSettings) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Project.Name = "saved-project"
	state := newConfigState(t, "s1", cfg, dir)

	var order []string
	marked := config.Default()
	marked.Project.Name = "marked"
	_, sess := open(t, service.Deps{
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
	_, badSess := open(t, service.Deps{
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

// saveVerifyCommandsRefreshesOnlyLayers pins the verify save: the file gains
// the commands, the layers are re-read, and the trust record is left alone.
func saveVerifyCommandsRefreshesOnlyLayers(t *testing.T, open openSettings) {
	home := t.TempDir()
	dir := t.TempDir()
	state := newConfigState(t, "s1", config.Default(), dir)

	refreshes := 0
	trustCalls := 0
	_, sess := open(t, service.Deps{
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

// userWritesUseTheUserConfigPathAndRefreshLayers pins the four user-config
// writes: each lands in Deps.UserConfigPath, keeps what another process wrote,
// and refreshes the session's layers.
func userWritesUseTheUserConfigPathAndRefreshLayers(t *testing.T, open openSettings) {
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	dir := t.TempDir()
	path := config.UserConfigPath(home)
	state := newConfigState(t, "s1", config.Default(), dir)

	refreshes := 0
	marked := config.Default()
	marked.Project.Name = "marked"
	host, _ := open(t, service.Deps{
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

// aFailedLayerReloadKeepsTheLayers pins ReloadLayers' false: a failed read
// leaves the session with the layers it had.
func aFailedLayerReloadKeepsTheLayers(t *testing.T, open openSettings) {
	dir := t.TempDir()
	state := newConfigState(t, "s1", config.Default(), dir)
	previous := config.Default()
	previous.Project.Name = "previous"
	state.SetLayers(config.Layers{Merged: previous})

	_, sess := open(t, service.Deps{
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

// reloadConfigReportsTheReloadPath pins ReloadConfig: without a reloader it
// reports false and changes nothing, and with one it hands cfg over and returns
// the rebuild's error. Over the wire only the error's message survives, so the
// two arms are compared by it (plan 1).
func reloadConfigReportsTheReloadPath(t *testing.T, open openSettings) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Default()
	state := newConfigState(t, "s1", cfg, dir)
	next := config.Default()
	next.Project.Name = "next"

	_, sess := open(t, service.Deps{State: state})
	ok, err := sess.ReloadConfig(ctx, next)
	if ok || err != nil {
		t.Fatalf("ReloadConfig without a reloader = (%v, %v), want (false, nil)", ok, err)
	}
	if got := sess.Config().Project.Name; got != cfg.Project.Name {
		t.Errorf("Config().Project.Name = %q after a refused reload, want the unchanged %q", got, cfg.Project.Name)
	}

	reloadErr := errors.New("rebuild failed")
	var got config.Config
	_, withReload := open(t, service.Deps{
		State:        state,
		ReloadConfig: func(c config.Config) error { got = c; return reloadErr },
	})
	ok, err = withReload.ReloadConfig(ctx, next)
	if !ok {
		t.Error("ReloadConfig with a reloader reported false, want true")
	}
	if err == nil || err.Error() != reloadErr.Error() {
		t.Errorf("ReloadConfig error = %v, want %q", err, reloadErr)
	}
	if got.Project.Name != "next" {
		t.Errorf("reloader received project name %q, want %q", got.Project.Name, "next")
	}
}

// applyConfigInstallsWithoutSaving pins ApplyConfig: the session's config
// changes and no file is written.
func applyConfigInstallsWithoutSaving(t *testing.T, open openSettings) {
	dir := t.TempDir()
	state := newConfigState(t, "s1", config.Default(), dir)
	_, sess := open(t, service.Deps{State: state})

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

// reconnectMCPClearsNoticesBeforeTheReload pins the order the MCP sign-in
// needs: the notices are gone when the rebuild runs, so a fresh notice it sets
// survives.
func reconnectMCPClearsNoticesBeforeTheReload(t *testing.T, open openSettings) {
	ctx := context.Background()
	dir := t.TempDir()
	state := newConfigState(t, "s1", config.Default(), dir)
	state.SetNotice(session.Notice{Category: session.NoticeProvider, Message: "needs OAuth"})

	sawNotice := true
	reloadErr := errors.New("the server still fails")
	_, sess := open(t, service.Deps{
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
	if err == nil || err.Error() != reloadErr.Error() {
		t.Errorf("ReconnectMCP error = %v, want %q", err, reloadErr)
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
	_, plain := open(t, service.Deps{State: state})
	reloaded, err = plain.ReconnectMCP(ctx)
	if reloaded || err != nil {
		t.Fatalf("ReconnectMCP without a reloader = (%v, %v), want (false, nil)", reloaded, err)
	}
	if got, ok := state.Notice(); !ok || got.Category != session.NoticeProvider {
		t.Errorf("notice = (%+v, %v) after a refused reconnect, want the provider notice kept", got, ok)
	}
}

// preparePresetsRunsPresetSetup pins preset setup over the seam: one preset
// per reported model, the provenance returned raw, and the caller's config left
// alone. A sidecar it cannot read yields no provenance at all.
func preparePresetsRunsPresetSetup(t *testing.T, open openSettings) {
	ctx := context.Background()
	state := newConfigState(t, "s1", config.Default(), t.TempDir())
	models := []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}, {ID: "llama3"}}

	dir := t.TempDir()
	host, _ := open(t, service.Deps{State: state, ModelCacheDir: dir})
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
	corruptHost, _ := open(t, service.Deps{State: state, ModelCacheDir: corrupt})
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

// saveProvenanceWritesTheSidecar pins the write: the raw value lands at the
// sidecar's path, and a nil value writes nothing.
func saveProvenanceWritesTheSidecar(t *testing.T, open openSettings) {
	ctx := context.Background()
	state := newConfigState(t, "s1", config.Default(), t.TempDir())
	dir := t.TempDir()
	host, _ := open(t, service.Deps{State: state, ModelCacheDir: dir})

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
	emptyHost, _ := open(t, service.Deps{State: state, ModelCacheDir: empty})
	if err := emptyHost.SaveProvenance(ctx, nil); err != nil {
		t.Fatalf("SaveProvenance(nil): %v", err)
	}
	if _, err := os.Stat(presetsetup.ProvenancePath(empty)); !os.IsNotExist(err) {
		t.Errorf("sidecar stat after a nil save = %v, want it not to exist", err)
	}
}

// providerTemplatesListTheCatalog pins decision 2's read: every entry with
// every field, and openai-codex's wire form whole.
func providerTemplatesListTheCatalog(t *testing.T, open openSettings) {
	host, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir())})
	got := host.ProviderTemplates()
	want := provider.All()
	if len(got) != len(want) {
		t.Fatalf("templates = %d, want %d", len(got), len(want))
	}
	for i, tpl := range want {
		if !reflect.DeepEqual(got[i], settingsTemplateWire(tpl)) {
			t.Fatalf("template %d = %+v, want every field of %+v", i, got[i], tpl)
		}
	}

	var codex acpwire.ProviderTemplate
	for _, tpl := range got {
		if tpl.ID == "openai-codex" {
			codex = tpl
		}
	}
	if codex.ID == "" {
		t.Fatal("the catalog does not hold openai-codex")
	}
	data, err := json.Marshal(codex)
	if err != nil {
		t.Fatalf("encode openai-codex: %v", err)
	}
	const wantJSON = `{"id":"openai-codex","label":"OpenAI (ChatGPT subscription)","type":"openai_codex","baseUrl":"https://chatgpt.com/backend-api","toolCalling":true,"structuredOutput":true,"auth":"oauth","keyHint":"Uses your ChatGPT subscription via browser login — no API key required.","models":["gpt-6-astra","gpt-5.6-sol","gpt-5.6-terra","gpt-5.6-luna"]}`
	if string(data) != wantJSON {
		t.Fatalf("openai-codex encodes as\n%s\nwant\n%s", data, wantJSON)
	}
}

// probeProviderListsModels pins the probe: the endpoint's model list, in its
// order.
func probeProviderListsModels(t *testing.T, open openSettings) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"qwen2.5-coder:7b","owned_by":"ollama"},{"id":"llama3.1:8b","owned_by":"meta"}]}`))
	}))
	defer srv.Close()

	host, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), Auth: newTestAuth()})
	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL + "/v1"}
	models, err := host.ProbeProvider(context.Background(), "testprov", pc)
	if err != nil {
		t.Fatalf("ProbeProvider: %v", err)
	}
	if len(models) != 2 || models[0].ID != "qwen2.5-coder:7b" {
		t.Fatalf("models = %v, want the server's list", models)
	}
}

// probeProviderReportsFailures pins that a probe that cannot list models is an
// error, not an empty list.
func probeProviderReportsFailures(t *testing.T, open openSettings) {
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer forbidden.Close()

	host, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), Auth: newTestAuth()})
	cases := map[string]string{
		"non-200":            forbidden.URL + "/v1",
		"connection refused": "http://127.0.0.1:1/v1",
	}
	for name, baseURL := range cases {
		t.Run(name, func(t *testing.T) {
			pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: baseURL}
			if _, err := host.ProbeProvider(context.Background(), "testprov", pc); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// probeProviderPreservesModelLimits pins that a listing leaves the limits the
// endpoint does not report at zero, for the preset defaults.
func probeProviderPreservesModelLimits(t *testing.T, open openSettings) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"gpt-4o","owned_by":"openai"}]}`)
	}))
	defer srv.Close()

	host, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), Auth: newTestAuth()})
	models, err := host.ProbeProvider(context.Background(), "openai", config.ProviderConfig{
		Type: "openai_compatible", BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("ProbeProvider: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ID != "gpt-4o" {
		t.Errorf("ID = %q, want gpt-4o", models[0].ID)
	}
	// /models reports no limits, so discovery leaves them for the preset
	// defaults.
	if models[0].ContextWindow != 0 || models[0].MaxOutputTokens != 0 {
		t.Errorf("limits = %d/%d, want both zero", models[0].ContextWindow, models[0].MaxOutputTokens)
	}
}

// probeProviderUsesTheAdaptersResolver pins that an openai_codex probe builds
// its token store through the adapter's resolver, so a probe launched from an
// isolated runtime never opens the developer's keychain. Over the wire only the
// message survives, so the arms are compared by it (plan 1).
func probeProviderUsesTheAdaptersResolver(t *testing.T, open openSettings) {
	auth := newTestAuth()
	auth.engineErr = fmt.Errorf("injected store failure: %w", credentials.ErrKeyringUnavailable)

	host, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), Auth: auth})
	pc := config.ProviderConfig{
		Type:     "openai_codex",
		Template: "openai-codex",
		BaseURL:  "https://chatgpt.com/backend-api",
		Auth:     "oauth",
	}
	_, err := host.ProbeProvider(context.Background(), "codex", pc)
	if len(auth.engines) != 1 || auth.engines[0] != "codex" {
		t.Fatalf("engine requests = %v, want one for \"codex\"", auth.engines)
	}
	if err == nil {
		t.Fatal("a failing injected resolver must produce an error")
	}
	if !strings.Contains(err.Error(), credentials.ErrKeyringUnavailable.Error()) {
		t.Fatalf("err = %v, want the injected keychain failure", err)
	}
}

// canProbeModel pins the read: only a provider whose client can probe a model
// answers true.
func canProbeModel(t *testing.T, open openSettings) {
	auth := newTestAuth()
	auth.engineErr = fmt.Errorf("OS keychain unavailable — OAuth tokens cannot be stored: %w", credentials.ErrKeyringUnavailable)
	host, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), Auth: auth})
	ctx := context.Background()

	if !host.CanProbeModel(ctx, "ollama", config.ProviderConfig{
		Type: "ollama", BaseURL: "http://127.0.0.1:11434",
	}) {
		t.Error("an ollama provider can probe a model")
	}
	if host.CanProbeModel(ctx, "openai", config.ProviderConfig{
		Type: "openai_compatible", BaseURL: "http://127.0.0.1:1234/v1",
	}) {
		t.Error("an openai_compatible provider cannot probe a model")
	}
	if host.CanProbeModel(ctx, "codex", config.ProviderConfig{
		Type: "openai_codex", BaseURL: "https://chatgpt.com/backend-api", Auth: "oauth",
	}) {
		t.Error("a provider whose client cannot be built cannot probe a model")
	}
}

// probeModelProbesOllama pins the two shapes of the capability read: ollama's
// answer converted, and a client that cannot probe answering nothing at all.
func probeModelProbesOllama(t *testing.T, open openSettings) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/show" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"capabilities":["completion","tools"],"model_info":{"qwen3.context_length":40960}}`)
	}))
	defer srv.Close()

	host, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), Auth: newTestAuth()})
	caps, err := host.ProbeModel(context.Background(), "ollama", config.ProviderConfig{
		Type: "ollama", BaseURL: srv.URL,
	}, "qwen3:8b")
	if err != nil {
		t.Fatalf("ProbeModel: %v", err)
	}
	if caps.ToolCalling == nil || !*caps.ToolCalling {
		t.Errorf("ToolCalling = %v, want true", caps.ToolCalling)
	}
	if caps.Reasoning == nil || *caps.Reasoning {
		t.Errorf("Reasoning = %v, want false", caps.Reasoning)
	}
	if caps.ContextWindow != 40960 {
		t.Errorf("ContextWindow = %d, want 40960", caps.ContextWindow)
	}

	other, err := host.ProbeModel(context.Background(), "openai", config.ProviderConfig{
		Type: "openai_compatible", BaseURL: "http://127.0.0.1:1234/v1",
	}, "gpt-4o")
	if err != nil {
		t.Fatalf("ProbeModel for a non-prober: %v", err)
	}
	if other.ToolCalling != nil || other.ContextWindow != 0 || other.Reasoning != nil {
		t.Errorf("capabilities = %+v, want the zero value", other)
	}

	auth := newTestAuth()
	auth.engineErr = fmt.Errorf("OS keychain unavailable — OAuth tokens cannot be stored: %w", credentials.ErrKeyringUnavailable)
	broken, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), Auth: auth})
	codex, err := broken.ProbeModel(context.Background(), "codex", config.ProviderConfig{
		Type: "openai_codex", BaseURL: "https://chatgpt.com/backend-api", Auth: "oauth",
	}, "gpt-6-astra")
	if err == nil {
		t.Fatal("a client that cannot be built must return its error")
	}
	if codex.ToolCalling != nil || codex.ContextWindow != 0 || codex.Reasoning != nil {
		t.Errorf("capabilities = %+v, want the zero value", codex)
	}
}

// modelCacheRoundTrip pins the cache's four calls against the file they share:
// a save the matching config finds, a changed config that does not, a rename
// that moves the entry and a forget that drops it.
func modelCacheRoundTrip(t *testing.T, open openSettings) {
	dir := t.TempDir()
	st := cacheState(t)
	host, _ := open(t, service.Deps{State: st, Auth: newTestAuth(), ModelCacheDir: dir})
	pc := st.ConfigSnapshot().Providers["local"]
	ctx := context.Background()

	models := []schema.ModelInfo{{ID: "qwen3:8b"}, {ID: "llama3.1:8b"}}
	if err := host.SaveModels(ctx, "local", models); err != nil {
		t.Fatalf("SaveModels: %v", err)
	}
	got, ok := host.ListModels(ctx, "local")
	if !ok || len(got) != 2 || got[0].ID != "qwen3:8b" {
		t.Fatalf("ListModels = %v, %v, want the saved list", got, ok)
	}
	c := modelcache.Load(dir)
	entry, ok := c.Providers["local"]
	if !ok {
		t.Fatal("the cache holds no entry for local")
	}
	if want := modelcache.HashProvider(pc); entry.ConfigHash != want {
		t.Errorf("ConfigHash = %q, want %q", entry.ConfigHash, want)
	}

	// A new base URL is a different config, so the entry no longer matches.
	moved := pc
	moved.BaseURL = "http://localhost:11434/other/v1"
	st.Config.Providers["local"] = moved
	if _, ok := host.ListModels(ctx, "local"); ok {
		t.Fatal("a changed config must not match the cached entry")
	}
	st.Config.Providers["local"] = pc

	if err := host.RenameModels(ctx, "local", "renamed"); err != nil {
		t.Fatalf("RenameModels: %v", err)
	}
	c = modelcache.Load(dir)
	if _, ok := c.Providers["local"]; ok {
		t.Error("the rename left the old name in the cache")
	}
	if _, ok := c.Providers["renamed"]; !ok {
		t.Error("the rename did not move the entry")
	}
	if err := host.RenameModels(ctx, "missing", "other"); err != nil {
		t.Fatalf("renaming a name the cache does not hold: %v", err)
	}

	if err := host.ForgetModels(ctx, "renamed"); err != nil {
		t.Fatalf("ForgetModels: %v", err)
	}
	if _, ok := modelcache.Load(dir).Providers["renamed"]; ok {
		t.Error("ForgetModels left the entry in the cache")
	}
}

// modelCacheIgnoresWhatPersistIgnored pins the writes the cache refuses, and
// the four calls on a runtime with no cache directory.
func modelCacheIgnoresWhatPersistIgnored(t *testing.T, open openSettings) {
	dir := t.TempDir()
	st := cacheState(t)
	host, _ := open(t, service.Deps{State: st, Auth: newTestAuth(), ModelCacheDir: dir})
	ctx := context.Background()

	if err := host.SaveModels(ctx, "local", nil); err != nil {
		t.Fatalf("SaveModels with an empty list: %v", err)
	}
	if err := host.SaveModels(ctx, "absent", []schema.ModelInfo{{ID: "m"}}); err != nil {
		t.Fatalf("SaveModels for a name the config lacks: %v", err)
	}
	if _, err := os.Stat(modelcache.Path(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a write persisted for nothing: %v", err)
	}

	noCache, _ := open(t, service.Deps{State: st, Auth: newTestAuth()})
	if _, ok := noCache.ListModels(ctx, "local"); ok {
		t.Error("ListModels without a cache directory found something")
	}
	if err := noCache.SaveModels(ctx, "local", []schema.ModelInfo{{ID: "m"}}); err != nil {
		t.Errorf("SaveModels without a cache directory: %v", err)
	}
	if err := noCache.ForgetModels(ctx, "local"); err != nil {
		t.Errorf("ForgetModels without a cache directory: %v", err)
	}
	if err := noCache.RenameModels(ctx, "local", "renamed"); err != nil {
		t.Errorf("RenameModels without a cache directory: %v", err)
	}
}

// prepareProviderLoginReportsTheResolver pins the three answers of the prepared
// sign-in: an unavailable keychain is coded, another failure keeps its text,
// and a working resolver passes. Over the wire only the message of an uncoded
// failure survives, so that arm's answer is compared by it (plan 1).
func prepareProviderLoginReportsTheResolver(t *testing.T, open openSettings) {
	t.Run("unavailable keychain is coded", func(t *testing.T) {
		auth := newTestAuth()
		auth.engineErr = fmt.Errorf("OS keychain unavailable — OAuth tokens cannot be stored: %w", credentials.ErrKeyringUnavailable)
		host, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), Auth: auth})

		err := host.PrepareProviderLogin(context.Background(), "codex")
		var coded *acpwire.Error
		if !errors.As(err, &coded) || coded.Code != acpwire.CodeKeyringUnavailable {
			t.Fatalf("err = %v, want a coded keyring_unavailable error", err)
		}
		if coded.Message != auth.engineErr.Error() {
			t.Fatalf("message = %q, want the resolver's %q", coded.Message, auth.engineErr)
		}
		if len(auth.engines) != 1 || auth.engines[0] != "codex" {
			t.Fatalf("engine requests = %v, want one for \"codex\"", auth.engines)
		}
	})

	t.Run("other failures keep their text", func(t *testing.T) {
		auth := newTestAuth()
		auth.engineErr = errors.New("injected engine failure")
		host, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), Auth: auth})

		err := host.PrepareProviderLogin(context.Background(), "codex")
		if err == nil {
			t.Fatalf("err = nil, want the resolver's error")
		}
		// Over the wire the server's own rendering precedes a plain error's
		// sentence, so the two arms are compared by the sentence they carry.
		if !strings.Contains(err.Error(), auth.engineErr.Error()) {
			t.Fatalf("err = %v, want the resolver's %q", err, auth.engineErr)
		}
	})

	t.Run("a working resolver passes", func(t *testing.T) {
		auth := newTestAuth()
		host, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), Auth: auth})

		if err := host.PrepareProviderLogin(context.Background(), "codex"); err != nil {
			t.Fatalf("PrepareProviderLogin: %v", err)
		}
		if len(auth.engines) != 1 || auth.engines[0] != "codex" {
			t.Fatalf("engine requests = %v, want one for \"codex\"", auth.engines)
		}
	})
}

// loginProviderRunsTheFlow runs the real sign-in through a test-owned
// authorization server: the URL reaches show once, and the tokens land in the
// adapter's store.
func loginProviderRunsTheFlow(t *testing.T, open openSettings) {
	var tokenCalls int
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenCalls++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"test-at","refresh_token":"test-rt","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer tokenSrv.Close()

	auth := newTestAuth()
	engine := &oauth.Engine{
		ServerURL:  "https://auth.test.invalid",
		Store:      auth.store,
		StorageKey: "marshal:provider:codex-test",
		Flow: oauth.FlowConfig{
			Issuer:        "https://auth.test.invalid",
			AuthorizeURL:  "https://auth.test.invalid/oauth/authorize",
			TokenURL:      tokenSrv.URL,
			ClientID:      "app_test",
			RedirectPorts: []int{0}, // ephemeral port: no fixed-port collision
			RedirectPath:  "/auth/callback",
		},
		CacheDir: t.TempDir(), // never the repo's .marshal/oauth-metadata
	}
	engine.LoopbackTimeout = 10 * time.Second
	auth.cache["codex-test"] = engine

	host, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), Auth: auth})

	var mu sync.Mutex
	var shown []string
	done := make(chan error, 1)
	go func() {
		done <- host.LoginProvider(context.Background(), "codex-test", func(u string) {
			mu.Lock()
			defer mu.Unlock()
			shown = append(shown, u)
		})
	}()

	shownURLs := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), shown...)
	}

	// Drive the loopback callback the way a browser would.
	var callback string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if urls := shownURLs(); len(urls) > 0 {
			callback = urls[0]
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if callback == "" {
		t.Fatal("the flow never reported an authorization URL")
	}
	u, err := url.Parse(callback)
	if err != nil {
		t.Fatalf("authorize URL %q: %v", callback, err)
	}
	if state := u.Query().Get("state"); state != "" {
		redirect := u.Query().Get("redirect_uri")
		resp, err := http.Get(redirect + "?code=test-code&state=" + url.QueryEscape(state))
		if err != nil {
			t.Fatalf("loopback callback: %v", err)
		}
		resp.Body.Close()
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("LoginProvider: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("LoginProvider did not return")
	}

	urls := shownURLs()
	if len(urls) != 1 {
		t.Fatalf("show calls = %v, want exactly one", urls)
	}
	if urls[0] != callback {
		t.Fatalf("showed %q, want %q", urls[0], callback)
	}
	if engine.Open != nil {
		t.Fatal("the adapter must leave the engine's Open nil: show reports the URL")
	}
	if tokenCalls != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", tokenCalls)
	}
	if _, err := auth.store.Get("marshal:provider:codex-test"); err != nil {
		t.Fatalf("tokens should be persisted in the adapter's store: %v", err)
	}
}

// loginProviderCodesAnUnavailableKeychain pins the coding Prepare also applies:
// a client that calls LoginProvider alone still gets the error it can act on.
func loginProviderCodesAnUnavailableKeychain(t *testing.T, open openSettings) {
	auth := newTestAuth()
	auth.engineErr = fmt.Errorf("OS keychain unavailable — OAuth tokens cannot be stored: %w", credentials.ErrKeyringUnavailable)
	host, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), Auth: auth})

	err := host.LoginProvider(context.Background(), "codex", func(string) {})
	var coded *acpwire.Error
	if !errors.As(err, &coded) || coded.Code != acpwire.CodeKeyringUnavailable {
		t.Fatalf("err = %v, want a coded keyring_unavailable error", err)
	}
	if coded.Message != auth.engineErr.Error() {
		t.Fatalf("message = %q, want the resolver's %q", coded.Message, auth.engineErr)
	}
	if len(auth.engines) != 1 || auth.engines[0] != "codex" {
		t.Fatalf("engine requests = %v, want one for \"codex\"", auth.engines)
	}
}

// loginMCPComposesTheServerAndTheStore drives the seam's own LoginMCP — not
// mcpEngine directly — against a test-owned TLS server whose certificate the
// engine's client does not trust, so the flow stops after resolving the server
// and opening its token store. The engine's successful path is
// TestMCPEngineRunsAgainstATestServer's, which stays in the service's package.
func loginMCPComposesTheServerAndTheStore(t *testing.T, open openSettings) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"linear": {URL: srv.URL + "/mcp", Auth: "oauth", Trust: "unrestricted"},
	}
	st := newConfigState(t, "s1", cfg, t.TempDir())
	auth := newTestAuth()
	_, sess := open(t, service.Deps{State: st, Auth: auth})

	var shown []string
	err := sess.LoginMCP(context.Background(), "linear", func(u string) { shown = append(shown, u) })
	if err == nil {
		t.Fatal("a server with no OAuth metadata should fail the sign-in")
	}
	if len(shown) != 0 {
		t.Fatalf("show calls = %v, want none without an authorization URL", shown)
	}
	if len(auth.services) != 1 || auth.services[0] != "marshal" {
		t.Fatalf("store requests = %v, want one for \"marshal\"", auth.services)
	}
	if _, err := auth.store.Get("marshal:mcp:" + srv.URL + "/mcp"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("a failed flow stored a token: %v", err)
	}
}

// prepareMCPLoginSentences pins the four answers of the prepared MCP sign-in:
// the resolution failure's own sentence, the keychain guidance naming the
// server, the generic store sentence, and success.
func prepareMCPLoginSentences(t *testing.T, open openSettings) {
	configured := func() config.Config {
		cfg := config.Default()
		cfg.MCP.Servers = map[string]config.MCPServerConfig{
			"linear": {URL: "https://mcp.example.com/sse", Auth: "oauth", Trust: "unrestricted"},
		}
		return cfg
	}

	t.Run("the server does not resolve", func(t *testing.T) {
		cfg := configured()
		auth := newTestAuth()
		_, sess := open(t, service.Deps{State: newConfigState(t, "s1", cfg, t.TempDir()), Auth: auth})

		wantErr := func() error {
			_, err := commands.ResolveOAuthServer(cfg.MCP.Servers, "absent")
			return err
		}()
		if wantErr == nil {
			t.Fatal("the fixture resolves \"absent\" unexpectedly")
		}
		err := sess.PrepareMCPLogin(context.Background(), "absent")
		if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
			t.Fatalf("err = %v, want %q", err, wantErr)
		}
		if len(auth.services) != 0 {
			t.Fatalf("store requests = %v, want none before the server resolves", auth.services)
		}
	})

	t.Run("an unavailable keychain is a coded sentence", func(t *testing.T) {
		auth := newTestAuth()
		auth.storeErr = credentials.ErrKeyringUnavailable
		_, sess := open(t, service.Deps{State: newConfigState(t, "s1", configured(), t.TempDir()), Auth: auth})

		err := sess.PrepareMCPLogin(context.Background(), "linear")
		var coded *acpwire.Error
		if !errors.As(err, &coded) || coded.Code != acpwire.CodeKeyringUnavailable {
			t.Fatalf("err = %v, want a coded keyring_unavailable error", err)
		}
		if !strings.Contains(coded.Message, "OS keychain") || !strings.Contains(coded.Message, "/mcp auth linear") {
			t.Fatalf("message = %q, want the keychain guidance naming the server", coded.Message)
		}
	})

	t.Run("another store failure is the generic sentence", func(t *testing.T) {
		auth := newTestAuth()
		auth.storeErr = errors.New("injected store failure")
		_, sess := open(t, service.Deps{State: newConfigState(t, "s1", configured(), t.TempDir()), Auth: auth})

		err := sess.PrepareMCPLogin(context.Background(), "linear")
		if err == nil || !strings.Contains(err.Error(), "Cannot open the credential store: injected store failure") {
			t.Fatalf("err = %v, want the generic store sentence", err)
		}
	})

	t.Run("a working store passes", func(t *testing.T) {
		auth := newTestAuth()
		_, sess := open(t, service.Deps{State: newConfigState(t, "s1", configured(), t.TempDir()), Auth: auth})

		if err := sess.PrepareMCPLogin(context.Background(), "linear"); err != nil {
			t.Fatalf("PrepareMCPLogin: %v", err)
		}
		if len(auth.services) != 1 || auth.services[0] != "marshal" {
			t.Fatalf("store requests = %v, want one for \"marshal\"", auth.services)
		}
	})
}

// decideTrustPassesTheDecision pins the conversion: each of the three wire
// decisions reaches the runtime's DecideTrust as the matching trust.Decision.
func decideTrustPassesTheDecision(t *testing.T, open openSettings) {
	dir := t.TempDir()
	for _, decision := range []acpwire.TrustDecision{
		acpwire.TrustPermanent,
		acpwire.TrustSession,
		acpwire.TrustDecline,
	} {
		var got []trust.Decision
		host, _ := open(t, service.Deps{
			State:       newConfigState(t, "s1", config.Default(), t.TempDir()),
			TrustPrompt: dir,
			DecideTrust: func(d trust.Decision) { got = append(got, d) },
		})
		if err := host.DecideTrust(context.Background(), dir, decision); err != nil {
			t.Fatalf("DecideTrust(%q): %v", decision, err)
		}
		want := []trust.Decision{trust.Decision(decision)}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("DecideTrust(%q) recorded %v, want %v", decision, got, want)
		}
	}
}

// decideTrustRefusesWhatIsNotPending pins the three refusals: with nothing
// pending, for another directory, with a decision the seam does not carry, and
// without a recorder, DecideTrust fails and calls nothing.
func decideTrustRefusesWhatIsNotPending(t *testing.T, open openSettings) {
	dir := t.TempDir()
	other := t.TempDir()

	calls := 0
	decide := func(trust.Decision) { calls++ }

	tests := []struct {
		name     string
		deps     service.Deps
		dir      string
		decision acpwire.TrustDecision
		want     string
	}{
		{
			name:     "no prompt",
			deps:     service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), DecideTrust: decide},
			dir:      dir,
			decision: acpwire.TrustPermanent,
			want:     "inproc: no trust decision is pending",
		},
		{
			name:     "another directory",
			deps:     service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), TrustPrompt: dir, DecideTrust: decide},
			dir:      other,
			decision: acpwire.TrustPermanent,
			want:     "inproc: no trust decision is pending for " + other,
		},
		{
			name:     "unknown decision",
			deps:     service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), TrustPrompt: dir, DecideTrust: decide},
			dir:      dir,
			decision: acpwire.TrustDecision("maybe"),
			want:     `inproc: unknown trust decision "maybe"`,
		},
		{
			name:     "nil DecideTrust",
			deps:     service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir()), TrustPrompt: dir},
			dir:      dir,
			decision: acpwire.TrustPermanent,
			want:     "inproc: no trust decision is pending",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := calls
			host, _ := open(t, tt.deps)
			err := host.DecideTrust(context.Background(), tt.dir, tt.decision)
			if err == nil {
				t.Fatalf("DecideTrust: no error, want %q", tt.want)
			}
			if got := err.Error(); !strings.Contains(got, tt.want) {
				t.Errorf("DecideTrust error = %q, want %q", got, tt.want)
			}
			if calls != before {
				t.Errorf("DecideTrust called the recorder %d times, want none", calls-before)
			}
		})
	}
}

// trustPromptIsTheDeps pins the read: the host reports the prompt it was built
// with, and "" when it was built with none.
func trustPromptIsTheDeps(t *testing.T, open openSettings) {
	dir := t.TempDir()

	withPrompt, _ := open(t, service.Deps{
		State:       newConfigState(t, "s1", config.Default(), t.TempDir()),
		TrustPrompt: dir,
	})
	if got := withPrompt.TrustPrompt(); got != dir {
		t.Errorf("TrustPrompt() = %q, want %q", got, dir)
	}

	without, _ := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir())})
	if got := without.TrustPrompt(); got != "" {
		t.Errorf("TrustPrompt() = %q, want \"\"", got)
	}
}

// toolsReadsTheRuntime pins both halves of the read: without Deps.Tools the
// session reports no registry, and with it the session reports the runtime's
// names, copied so a caller cannot change what the next read returns. The
// closed-session half of the service's own case stays there: it drives the
// in-process host's current session.
func toolsReadsTheRuntime(t *testing.T, open openSettings) {
	_, sess := open(t, service.Deps{State: newConfigState(t, "s1", config.Default(), t.TempDir())})
	if names, ok := sess.Tools(); ok {
		t.Errorf("Tools() = %v, true without Deps.Tools; want false", names)
	}

	names := []string{"file.edit", "file.read", "shell.run"}
	_, sess = open(t, service.Deps{
		State: newConfigState(t, "s1", config.Default(), t.TempDir()),
		Tools: func() ([]string, bool) { return names, true },
	})
	got, ok := sess.Tools()
	if !ok {
		t.Fatalf("Tools() reported no registry, want the runtime's names")
	}
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("Tools() = %v, want %v", got, names)
	}
	got[0] = "changed"
	if next, _ := sess.Tools(); !reflect.DeepEqual(next, names) {
		t.Errorf("Tools() after the caller changed its result = %v, want %v", next, names)
	}
}

// configReadsCarryNoSecret pins T7: neither read hands a literal secret across
// the seam, and the mask the settings browser shows is what arrives in its
// place.
func configReadsCarryNoSecret(t *testing.T, open openSettings) {
	state := newConfigState(t, "s1", secretConfig(), t.TempDir())
	state.SetLayers(config.Layers{
		Default: secretConfig(),
		User:    secretConfig(),
		Merged:  secretConfig(),
	})
	_, sess := open(t, service.Deps{State: state})

	encoded, err := json.Marshal(sess.Config())
	if err != nil {
		t.Fatalf("encode Config(): %v", err)
	}
	layers, err := json.Marshal(sess.Layers())
	if err != nil {
		t.Fatalf("encode Layers(): %v", err)
	}
	for name, data := range map[string][]byte{"Config": encoded, "Layers": layers} {
		for _, secret := range []string{"SECRET-1234", "SECRET-2345", "cdp-SECRET-pw"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s carries the literal %q: %s", name, secret, data)
			}
		}
		if !strings.Contains(string(data), strutil.MaskKey(secretKey)) {
			t.Errorf("%s does not carry the key's mask: %s", name, data)
		}
	}
}

// installsRestoreTheStoredKey pins the installs: a config the client hands back
// is restored before it is installed or handed to the reloader.
func installsRestoreTheStoredKey(t *testing.T, open openSettings) {
	ctx := context.Background()
	state := newConfigState(t, "s1", secretConfig(), t.TempDir())
	var reloaded config.Config
	_, sess := open(t, service.Deps{
		State:        state,
		ReloadConfig: func(cfg config.Config) error { reloaded = cfg; return nil },
	})

	if err := sess.ApplyConfig(ctx, sess.Config()); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	if got := state.ConfigSnapshot().Providers["probe"].APIKey; got != secretKey {
		t.Errorf("installed api_key = %q, want the literal %q", got, secretKey)
	}
	if got := state.ConfigSnapshot().Web.SearchKey; got != secretSearch {
		t.Errorf("installed search_key = %q, want the literal %q", got, secretSearch)
	}

	ok, err := sess.ReloadConfig(ctx, sess.Config())
	if !ok || err != nil {
		t.Fatalf("ReloadConfig = (%v, %v), want (true, nil)", ok, err)
	}
	if got := reloaded.Providers["probe"].APIKey; got != secretKey {
		t.Errorf("reloader received api_key %q, want the literal %q", got, secretKey)
	}
}

// saveProjectConfigRestoresTheSearchKey pins the project save: a masked search
// key is restored before the diff, so no mask reaches the project file.
func saveProjectConfigRestoresTheSearchKey(t *testing.T, open openSettings) {
	dir := t.TempDir()
	state := newConfigState(t, "s1", secretConfig(), dir)
	state.SetLayers(config.Layers{Default: config.Default(), User: secretConfig(), Merged: secretConfig()})
	_, sess := open(t, service.Deps{State: state})

	if err := sess.SaveProjectConfig(context.Background(), sess.Config()); err != nil {
		t.Fatalf("SaveProjectConfig: %v", err)
	}
	data, err := os.ReadFile(config.ProjectConfigPath(dir))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(data), "search_key") {
		t.Errorf("project config carries a search_key:\n%s", data)
	}
	if strings.Contains(string(data), strutil.MaskKey(secretSearch)) {
		t.Errorf("project config carries a mask:\n%s", data)
	}
}

// saveUserProvidersRestoresKeys pins the provider write: a masked key is
// restored, a rename carries the old name's key to the new name, and a cleared
// key stays cleared.
func saveUserProvidersRestoresKeys(t *testing.T, open openSettings) {
	ctx := context.Background()
	home := t.TempDir()
	dir := t.TempDir()
	path := config.UserConfigPath(home)
	state := newConfigState(t, "s1", secretConfig(), dir)
	host, sess := open(t, service.Deps{State: state, HomeDir: home, UserConfigPath: path})

	disk := map[string]config.ProviderConfig{
		"probe": {Type: "openai_compatible", BaseURL: "https://probe.test/v1", APIKey: secretKey},
	}
	if err := config.SaveUserConfigProviders(path, disk); err != nil {
		t.Fatalf("seed providers: %v", err)
	}

	masked := sess.Config().Providers
	if got := masked["probe"].APIKey; got != strutil.MaskKey(secretKey) {
		t.Fatalf("the read does not carry the mask: %q", got)
	}
	if err := host.SaveUserProviders(ctx, masked, masked, nil); err != nil {
		t.Fatalf("SaveUserProviders: %v", err)
	}
	if got := loadUserConfig(t, home, dir).Providers["probe"].APIKey; got != secretKey {
		t.Errorf("saved api_key = %q, want the literal %q", got, secretKey)
	}

	// A provider renamed in the client carries the old name's key.
	renamed := map[string]config.ProviderConfig{
		"moved": {Type: "openai_compatible", BaseURL: "https://probe.test/v1", APIKey: strutil.MaskKey(secretKey)},
	}
	baseline := map[string]config.ProviderConfig{"probe": disk["probe"]}
	if err := host.SaveUserProviders(ctx, renamed, baseline, map[string]string{"moved": "probe"}); err != nil {
		t.Fatalf("SaveUserProviders after a rename: %v", err)
	}
	saved := loadUserConfig(t, home, dir)
	if got := saved.Providers["moved"].APIKey; got != secretKey {
		t.Errorf("renamed api_key = %q, want the literal %q", got, secretKey)
	}
	if _, ok := saved.Providers["probe"]; ok {
		t.Errorf("providers = %v, want the old name dropped", saved.Providers)
	}

	// A cleared key is saved empty, not restored.
	cleared := map[string]config.ProviderConfig{
		"moved": {Type: "openai_compatible", BaseURL: "https://probe.test/v1"},
	}
	if err := host.SaveUserProviders(ctx, cleared, saved.Providers, nil); err != nil {
		t.Fatalf("SaveUserProviders with a cleared key: %v", err)
	}
	if got := loadUserConfig(t, home, dir).Providers["moved"].APIKey; got != "" {
		t.Errorf("cleared api_key = %q, want empty", got)
	}
}

// saveUserValueRestoresAMaskedKey pins the user-value write: at a key's path
// the mask written back is restored, and a typed value is written as given.
func saveUserValueRestoresAMaskedKey(t *testing.T, open openSettings) {
	ctx := context.Background()
	home := t.TempDir()
	dir := t.TempDir()
	state := newConfigState(t, "s1", secretConfig(), dir)
	host, _ := open(t, service.Deps{State: state, HomeDir: home, UserConfigPath: config.UserConfigPath(home)})

	if err := host.SaveUserValue(ctx, "providers.probe.api_key", strutil.MaskKey(secretKey)); err != nil {
		t.Fatalf("SaveUserValue with a mask: %v", err)
	}
	if got := loadUserConfig(t, home, dir).Providers["probe"].APIKey; got != secretKey {
		t.Errorf("saved api_key = %q, want the literal %q", got, secretKey)
	}

	if err := host.SaveUserValue(ctx, "providers.probe.api_key", "sk-typed-9999"); err != nil {
		t.Fatalf("SaveUserValue with a literal: %v", err)
	}
	if got := loadUserConfig(t, home, dir).Providers["probe"].APIKey; got != "sk-typed-9999" {
		t.Errorf("saved api_key = %q, want the typed %q", got, "sk-typed-9999")
	}

	// The other two secret paths are restored the same way.
	if err := host.SaveUserValue(ctx, "web.search_key", strutil.MaskKey(secretSearch)); err != nil {
		t.Fatalf("SaveUserValue with a masked search key: %v", err)
	}
	if got := loadUserConfig(t, home, dir).Web.SearchKey; got != secretSearch {
		t.Errorf("saved search_key = %q, want the literal %q", got, secretSearch)
	}

	maskedCDP := config.MaskSecrets(config.Config{Desktop: config.DesktopConfig{CDPURL: secretCDP}}).Desktop.CDPURL
	if err := host.SaveUserValue(ctx, "desktop.cdp_url", maskedCDP); err != nil {
		t.Fatalf("SaveUserValue with a masked cdp url: %v", err)
	}
	if got := loadUserConfig(t, home, dir).Desktop.CDPURL; got != secretCDP {
		t.Errorf("saved cdp_url = %q, want the literal %q", got, secretCDP)
	}
}

// saveUserValueRejectsAnUnmatchedMask pins the other half of decision 5: at a
// secret's path a mask with nothing behind it fails the write and leaves the
// file byte-identical.
func saveUserValueRejectsAnUnmatchedMask(t *testing.T, open openSettings) {
	ctx := context.Background()
	home := t.TempDir()
	dir := t.TempDir()
	path := config.UserConfigPath(home)
	state := newConfigState(t, "s1", secretConfig(), dir)
	host, _ := open(t, service.Deps{State: state, HomeDir: home, UserConfigPath: path})

	if err := config.SaveUserConfigValue(path, "web.search_key", secretSearch); err != nil {
		t.Fatalf("seed search key: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	want := "config: web.search_key: no stored secret matches its mask"
	if err := host.SaveUserValue(ctx, "web.search_key", "••••zzzz"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("SaveUserValue error = %v, want %q", err, want)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("a failed write changed the user config:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// saveProviderKeyRejectsAMask pins decision 5 for the doctor's key write: a
// mask is never persisted, where it would become the stored key every later
// restore matches.
func saveProviderKeyRejectsAMask(t *testing.T, open openSettings) {
	home := t.TempDir()
	state := newConfigState(t, "s1", secretConfig(), t.TempDir())
	host, _ := open(t, service.Deps{State: state, HomeDir: home, UserConfigPath: config.UserConfigPath(home)})

	err := host.SaveProviderKey(context.Background(), "probe", strutil.MaskKey(secretKey))
	want := "config: providers.probe.api_key: no stored secret matches its mask"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("SaveProviderKey error = %v, want %q", err, want)
	}
	if _, statErr := os.Stat(config.UserConfigPath(home)); !os.IsNotExist(statErr) {
		t.Errorf("a rejected key wrote the user config: %v", statErr)
	}
}

// anUnmatchedMaskChangesNothing pins decision 5: a mask with no stored secret
// behind it fails the read's inverse and writes nothing.
func anUnmatchedMaskChangesNothing(t *testing.T, open openSettings) {
	ctx := context.Background()
	home := t.TempDir()
	dir := t.TempDir()
	path := config.UserConfigPath(home)
	state := newConfigState(t, "s1", secretConfig(), dir)
	host, sess := open(t, service.Deps{State: state, HomeDir: home, UserConfigPath: path})

	if err := config.SaveUserConfigProviders(path, map[string]config.ProviderConfig{
		"probe": {Type: "openai_compatible", BaseURL: "https://probe.test/v1", APIKey: secretKey},
	}); err != nil {
		t.Fatalf("seed providers: %v", err)
	}
	userBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}

	client := config.RedactSecrets(state.ConfigSnapshot())
	client.Providers["probe"] = config.ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "https://probe.test/v1",
		APIKey:  "••••zzzz",
	}
	want := "config: providers.probe.api_key: no stored secret matches its mask"

	if err := sess.ApplyConfig(ctx, client); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ApplyConfig error = %v, want %q", err, want)
	}
	if got := state.ConfigSnapshot().Providers["probe"].APIKey; got != secretKey {
		t.Errorf("api_key = %q after a failed install, want the unchanged %q", got, secretKey)
	}
	if err := sess.SaveProjectConfig(ctx, client); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("SaveProjectConfig error = %v, want %q", err, want)
	}
	if _, err := os.Stat(config.ProjectConfigPath(dir)); !os.IsNotExist(err) {
		t.Errorf("project config stat = %v, want it not to exist", err)
	}
	if err := host.SaveUserProviders(ctx, client.Providers, nil, nil); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("SaveUserProviders error = %v, want %q", err, want)
	}
	userAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if string(userAfter) != string(userBefore) {
		t.Errorf("a failed write changed the user config:\nbefore:\n%s\nafter:\n%s", userBefore, userAfter)
	}
}

// probesUseTheStoredKey pins the probes: a masked key is restored, so the
// request carries the stored key, and a mask with nothing behind it probes
// without one.
func probesUseTheStoredKey(t *testing.T, open openSettings) {
	var mu sync.Mutex
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"m1","owned_by":"test"}]}`)
	}))
	defer srv.Close()

	header := func() string {
		mu.Lock()
		defer mu.Unlock()
		return auth
	}
	resetHeader := func(v string) {
		mu.Lock()
		defer mu.Unlock()
		auth = v
	}

	state := newConfigState(t, "s1", secretConfig(), t.TempDir())
	host, _ := open(t, service.Deps{State: state, Auth: newTestAuth()})

	pc := config.ProviderConfig{Type: "openai_compatible", BaseURL: srv.URL, APIKey: strutil.MaskKey(secretKey)}
	if _, err := host.ProbeProvider(context.Background(), "probe", pc); err != nil {
		t.Fatalf("ProbeProvider: %v", err)
	}
	if want := "Bearer " + secretKey; header() != want {
		t.Errorf("Authorization = %q, want %q", header(), want)
	}

	resetHeader("unset")
	pc.APIKey = "••••zzzz"
	if _, err := host.ProbeProvider(context.Background(), "probe", pc); err != nil {
		t.Fatalf("ProbeProvider with an unmatched mask: %v", err)
	}
	if got := header(); got != "" {
		t.Errorf("Authorization = %q, want none at all", got)
	}
}

// TestASecretCrossesTheWireOnlyInItsCall is V5 for the settings group: each
// secret travels in its own call's params and nowhere else, and the bytes the
// agent writes carry the mask instead.
func TestASecretCrossesTheWireOnlyInItsCall(t *testing.T) {
	const sessionID = "s1"
	const storedKey = "sk-p3-7a-stored-4b8c"
	const savedKey = "sk-p3-7a-canary-5d1e"
	const probedKey = "sk-p3-7a-probe-91c4"

	home := t.TempDir()
	cfg := config.Default()
	// The session's own key is stored, so the config the agent pushes carries
	// its mask and never it.
	cfg.Providers = map[string]config.ProviderConfig{
		"probe": {Type: "openai_compatible", BaseURL: "https://probe.test/v1", APIKey: storedKey},
	}
	state := newConfigState(t, sessionID, cfg, t.TempDir())
	deps := service.Deps{
		State:          state,
		Auth:           newTestAuth(),
		HomeDir:        home,
		UserConfigPath: config.UserConfigPath(home),
		// The save refreshes the session's layers, so the forwarder has a
		// changed config to send, carrying the key's mask.
		ReloadLayers: func() (config.Layers, bool) {
			layers, err := config.LoadLayers(config.LoadOptions{
				HomeDir:           home,
				WorkingDir:        state.WorkingDir,
				SkipProjectConfig: true,
			})
			return layers, err == nil
		},
	}

	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"probed-model","owned_by":"test"}]}`)
	}))
	t.Cleanup(endpoint.Close)

	servedHost, served := service.New(deps)

	client, agent := rpc.Pipe()
	tap := &tappedStream{inner: agent, read: &syncBuffer{}, written: &syncBuffer{}}
	logs := &syncBuffer{}
	srv := NewServer(tap, tap, WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	// The agent's end sets its own cap; neither end inherits the default (P3 R2).
	srv.MaxLineBytes = rpc.MaxFrameBytes
	registerInitialize(srv)
	statuses := NewStatusManager(StatusManagerConfig{
		Lookup: func(id string) (*service.Session, bool) {
			return served, id == sessionID
		},
		Notify:  srv.Notify,
		Marshal: srv.marshalClient,
	})
	registerStatus(srv, statuses)
	settings := serveSettings(t, srv, servedHost, served, sessionID)
	srv.SetResponseBarrier(responseBarrier(statuses, settings))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		_ = tap.Close()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})

	host, err := rpc.Dial(ctx, client, rpc.DialOptions{SharedFilesystem: true})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		statuses.Close()
		served.Release()
		_ = host.Close()
	})

	sess, err := host.Attach(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("attach %q: %v", sessionID, err)
	}
	// One read subscribes the config mirror, so the save's own update reaches
	// the agent before the call returns.
	if got := sess.Config(); len(got.Providers) == 0 {
		t.Fatalf("the config read carries no providers: %+v", got)
	}

	if err := host.SaveProviderKey(context.Background(), "probe", savedKey); err != nil {
		t.Fatalf("config/set_provider_key: %v", err)
	}
	if _, err := host.ProbeProvider(context.Background(), "probe", config.ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: endpoint.URL,
		APIKey:  probedKey,
	}); err != nil {
		t.Fatalf("config/probe_provider: %v", err)
	}

	read := tap.read.String()
	written := tap.written.String()
	logged := logs.String()

	saveCall := strings.Index(read, acpwire.MethodSetProviderKey)
	probeCall := strings.Index(read, acpwire.MethodProbeProvider)
	if saveCall < 0 {
		t.Fatalf("the agent read no %s call", acpwire.MethodSetProviderKey)
	}
	if probeCall < 0 {
		t.Fatalf("the agent read no %s call", acpwire.MethodProbeProvider)
	}
	// Each secret travels exactly once, inside its own call's params.
	for _, secret := range []string{savedKey, probedKey} {
		if n := strings.Count(read, secret); n != 1 {
			t.Errorf("%q appears %d times in the bytes the agent read, want exactly 1", secret, n)
		}
	}
	savedAt := strings.Index(read, savedKey)
	probedAt := strings.Index(read, probedKey)
	if savedAt < saveCall || savedAt > probeCall {
		t.Errorf("the saved key at %d is not inside the %s request at %d", savedAt, acpwire.MethodSetProviderKey, saveCall)
	}
	if probedAt < probeCall {
		t.Errorf("the probed key at %d is not inside the %s request at %d", probedAt, acpwire.MethodProbeProvider, probeCall)
	}

	// Nothing the agent wrote and nothing it logged carries either secret: a
	// session_config update carries the mask instead.
	for name, got := range map[string]string{"the bytes the agent wrote": written, "the log": logged} {
		for _, secret := range []string{storedKey, savedKey, probedKey} {
			if strings.Contains(got, secret) {
				t.Errorf("%q appears in %s", secret, name)
			}
		}
	}
	// The session's own key crossed no wire at all: only its mask did, in the
	// config the agent pushes.
	if n := strings.Count(read, storedKey); n != 0 {
		t.Errorf("the stored key appears %d times in the bytes the agent read, want 0", n)
	}
	storedMask := strutil.MaskKey(storedKey)
	if !strings.Contains(written, storedMask) {
		t.Errorf("no session_config update carries the stored key's mask %q", storedMask)
	}
	const configKind = `"kind":"session_config"`
	configAt := strings.LastIndex(written, configKind)
	if configAt < 0 {
		t.Fatal("no session_config update was written after the save")
	}
	mask := strutil.MaskKey(savedKey)
	maskAt := strings.LastIndex(written, mask)
	if maskAt < 0 || maskAt < configAt {
		t.Errorf("the session_config update written after the save does not carry the key's mask %q", mask)
	}
}

// TestSettingsCallBudget holds R10's 1ms budget for a settings call over the
// pipe on the 2,000-call session: ApplyConfig's call includes its barrier and
// every fence, and a user write's pipe overhead is the difference the two arms
// measure.
func TestSettingsCallBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing budget: skipped under -short and with the race detector")
	}
	st := largeStackSession(t)
	logLargeStackShape(t, st)
	home := t.TempDir()
	deps := service.Deps{State: st, HomeDir: home, UserConfigPath: config.UserConfigPath(home)}

	const runs = 30
	ctx := context.Background()

	pipeHost, pipeSess := openPipeArms(t, deps)
	// The session's own config, and the read that subscribes the mirror, so the
	// barrier has a forwarder to fence.
	cfg := pipeSess.Config()
	durations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		start := time.Now()
		if err := pipeSess.ApplyConfig(ctx, cfg); err != nil {
			t.Fatalf("ApplyConfig: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	apply := medianDuration(durations)
	t.Logf("apply_config call: median %v over %d runs (budget 1ms)", apply, runs)
	if apply >= time.Millisecond {
		t.Fatalf("apply_config call median = %v, budget 1ms (R10): do not loosen it", apply)
	}

	// The same write on both arms: what the pipe adds is the difference.
	serviceHost, _ := openServiceArms(t, deps)
	pipeDurations := make([]time.Duration, 0, runs)
	serviceDurations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		on := i%2 == 0
		start := time.Now()
		if err := pipeHost.SaveUserValue(ctx, "tools.shell.allow_network", on); err != nil {
			t.Fatalf("SaveUserValue over the pipe: %v", err)
		}
		pipeDurations = append(pipeDurations, time.Since(start))

		start = time.Now()
		if err := serviceHost.SaveUserValue(ctx, "tools.shell.allow_network", !on); err != nil {
			t.Fatalf("SaveUserValue on the service: %v", err)
		}
		serviceDurations = append(serviceDurations, time.Since(start))
	}
	pipeMedian := medianDuration(pipeDurations)
	serviceMedian := medianDuration(serviceDurations)
	t.Logf("save_user_value call: pipe median %v, service median %v, difference %v over %d runs (budget 1ms)",
		pipeMedian, serviceMedian, pipeMedian-serviceMedian, runs)
	if diff := pipeMedian - serviceMedian; diff >= time.Millisecond {
		t.Fatalf("save_user_value pipe overhead = %v, budget 1ms (R10): do not loosen it", diff)
	}
}
