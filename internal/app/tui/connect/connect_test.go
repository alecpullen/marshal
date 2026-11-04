package connect

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"charm.land/bubbles/v2/textinput"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/presetsetup"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/credentials"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/oauth"
	"marshal/internal/strutil"
)

func TestPanelSatisfiesDock(t *testing.T) {
	var _ dock.Panel = Panel{}
}

func TestNewStartsAtPickTemplate(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	if m.step != stepPickTemplate {
		t.Fatalf("step = %v, want stepPickTemplate", m.step)
	}
	if m.title == "" {
		t.Fatal("title must be set for the pickTemplate step")
	}
}

func TestNewScopedProviderStartsAtPickModel(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), SkipToIntroModel: true, ScopedProvider: "ollama"})
	if m.step != stepPickModel {
		t.Fatalf("step = %v, want stepPickModel", m.step)
	}
}

// TestPickTemplatePinsNewCustomProviderOnTop verifies the template list
// opens with a pinned "New custom provider…" row on top (so users don't
// have to scroll to the buried catalog entry), that the catalog's plain
// openai_compatible entry is not duplicated, and that picking the pinned
// row enters the base-URL step.
func TestPickTemplatePinsNewCustomProviderOnTop(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	items := m.picker.Items()
	if len(items) == 0 || !items[0].Pinned {
		t.Fatalf("first template item should be the pinned custom row: %+v", items[0])
	}
	if items[0].Value != "openai_compatible" {
		t.Fatalf("pinned row Value = %q, want openai_compatible", items[0].Value)
	}
	count := 0
	for _, it := range items {
		if it.Value == "openai_compatible" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("openai_compatible should appear exactly once, got %d", count)
	}
	updated, _ := m.Update(pickerPicked("openai_compatible"))
	if updated.step != stepBaseURL {
		t.Fatalf("picking the pinned custom row should enter baseURL, got %v", updated.step)
	}
}

func TestEscAtPickTemplateEmitsCancelled(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 27})
	if cmd == nil {
		t.Fatal("expected a cmd emitting CancelledMsg")
	}
	msg := cmd()
	if _, ok := msg.(CancelledMsg); !ok {
		t.Fatalf("cmd produced %T, want CancelledMsg", msg)
	}
	_ = updated
}

func TestPickTemplateOllamaSkipsAPIKey(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	updated, _ := m.Update(pickerPicked("ollama"))
	if updated.step != stepProbing {
		t.Fatalf("local template should skip apiKey, got step = %v", updated.step)
	}
	if updated.template.ID != "ollama" {
		t.Fatalf("template = %q, want ollama", updated.template.ID)
	}
}

func TestPickTemplateOpenRouterEntersAPIKey(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	updated, _ := m.Update(pickerPicked("openrouter"))
	if updated.step != stepAPIKey {
		t.Fatalf("remote template should enter apiKey, got step = %v", updated.step)
	}
	if updated.template.KeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("KeyEnv = %q", updated.template.KeyEnv)
	}
}

func TestPickCustomEntersBaseURL(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	updated, _ := m.Update(pickerPicked("custom"))
	if updated.step != stepBaseURL {
		t.Fatalf("custom should enter baseURL, got step = %v", updated.step)
	}
}

func TestPickOpenAICompatibleEntersBaseURL(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	updated, _ := m.Update(pickerPicked("openai_compatible"))
	if updated.step != stepBaseURL {
		t.Fatalf("openai_compatible template should enter baseURL, got step = %v", updated.step)
	}
}

func TestAPIKeyEnterAdvancesToProbing(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	m, _ = m.Update(pickerPicked("openrouter"))
	m.input.SetValue("sk-test-1234")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepProbing {
		t.Fatalf("apiKey Enter should advance to probing, got step = %v", updated.step)
	}
	if updated.providerCfg.APIKey != "sk-test-1234" {
		t.Fatalf("api key not captured: %q", updated.providerCfg.APIKey)
	}
	if updated.providerCfg.APIKeyEnv != "" {
		t.Fatalf("api_key_env should be cleared after literal key entry: %q", updated.providerCfg.APIKeyEnv)
	}
}

func TestCustomBaseURLThenKey(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	m, _ = m.Update(pickerPicked("custom"))
	m.input.SetValue("https://myhost/v1")
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
	if m.step != stepRename {
		t.Fatalf("after baseURL should be rename, got %v", m.step)
	}
	if m.providerCfg.BaseURL != "https://myhost/v1" {
		t.Fatalf("base_url not captured: %q", m.providerCfg.BaseURL)
	}
	// Confirm the rename to advance to the API key step.
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
	if m.step != stepAPIKey {
		t.Fatalf("after rename should be apiKey, got %v", m.step)
	}
	m.input.SetValue("sk-x")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepProbing {
		t.Fatalf("after apiKey should be probing, got %v", updated.step)
	}
}

// wizardResult wraps a setup probe result in the correlation envelope the
// wizard's runProbe command produces. Use it wherever a test delivers a probe
// for the *current* attempt; hand-build WizardProbeResult with an older
// Generation to construct a deliberately stale result.
func wizardResult(m *Model, msg probe.ResultMsg) WizardProbeResult {
	return WizardProbeResult{Owner: m, Generation: m.probeGen, Result: msg}
}

// testHost builds a client host over a fresh test state whose credential/OAuth
// dependency is auth, or a memory resolver when auth is nil. A wizard run over
// it never probes through the developer's keychain.
func testHost(t *testing.T, auth provider.AuthResolver) acpclient.Host {
	t.Helper()
	if auth == nil {
		auth = newMemAuth()
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	host, _ := inproc.New(inproc.Deps{State: state, Auth: auth})
	return host
}

// newTestWizard builds a wizard with a test client host when opts named none,
// so no wizard in this package is built without one.
func newTestWizard(t *testing.T, opts Opts) *Model {
	t.Helper()
	if opts.Host == nil {
		opts.Host = testHost(t, nil)
	}
	return New(opts)
}

func TestProbeSuccessAdvancesToPickModel(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}, {ID: "llama3.1:8b"}}}))
	if updated.step != stepPickModel {
		t.Fatalf("success should advance to pickModel, got %v", updated.step)
	}
	if len(updated.models) != 2 {
		t.Fatalf("models not stored: %v", updated.models)
	}
	if got := updated.discovered[updated.providerName]; len(got) != 2 {
		t.Fatalf("discovered cache not populated: %v", got)
	}
}

func TestProbeFailureStaysWithRetrySkip(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Err: errors.New("connection refused")}))
	if updated.step != stepProbing {
		t.Fatalf("failure should stay probing, got %v", updated.step)
	}
	if updated.err == "" {
		t.Fatal("expected inline error text set")
	}
}

func TestRetryReRunsProbe(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Err: errors.New("boom")}))
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 114})
	if updated.step != stepProbing {
		t.Fatalf("retry should stay probing, got %v", updated.step)
	}
	if cmd == nil {
		t.Fatal("retry should re-arm the probe cmd")
	}
}

func TestSkipProbeUsesCatalogAndAdvances(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(tea.KeyPressMsg{Code: 115})
	if updated.step != stepPickModel {
		t.Fatalf("skip should advance to pickModel, got %v", updated.step)
	}
	if len(updated.models) == 0 && len(updated.template.Models) > 0 {
		t.Fatal("skip should seed models from template catalog")
	}
}

func TestPickModelEmitsDone(t *testing.T) {
	cfg := config.Default()
	deny := false
	cfg.Privacy.ModelsDevEnrichment = &deny
	m := newTestWizard(t, Opts{Cfg: cfg, Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
	m, cmd := m.Update(pickerPicked("qwen2.5-coder:7b"))
	if m.step != stepPreparing {
		t.Fatalf("after pickModel should be stepPreparing, got %v", m.step)
	}
	if cmd == nil {
		t.Fatal("expected a prepare cmd")
	}

	// Deliver the prepared result directly: the prepare cmd itself would
	// probe the provider, which this unit test must not do.
	m, _ = m.Update(PreparedMsg{
		Provider: m.providerName,
		Presets: map[string]routing.ModelPreset{
			"ollama/qwen2.5-coder:7b": {Name: "ollama/qwen2.5-coder:7b", Provider: "ollama", Model: "qwen2.5-coder:7b"},
		},
		Result: presetsetup.Result{Prepared: 1, Fallback: 1},
	})
	if m.step != stepSummary {
		t.Fatalf("after prepare should be stepSummary, got %v", m.step)
	}

	// Press Enter on summary to emit DoneMsg.
	_, doneCmd := m.Update(tea.KeyPressMsg{Code: 13})
	if doneCmd == nil {
		t.Fatal("Enter on summary should emit a DoneMsg cmd")
	}
	doneMsg := doneCmd()
	dm, ok := doneMsg.(DoneMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want DoneMsg", doneMsg)
	}
	if dm.Model != "qwen2.5-coder:7b" {
		t.Fatalf("DoneMsg.Model = %q", dm.Model)
	}
	if _, ok := dm.Presets["ollama/qwen2.5-coder:7b"]; !ok {
		t.Fatalf("DoneMsg.Presets missing the prepared preset: %v", dm.Presets)
	}
}

func TestConsentEscReturnsToModelPick(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen3"}}}))
	m, _ = m.Update(pickerPicked("qwen3"))
	if m.step != stepConsent {
		t.Fatalf("step = %v, want stepConsent", m.step)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.step != stepPickModel {
		t.Fatalf("Esc should return to the model picker, got %v", m.step)
	}
}

func TestPickTemplateKeyForwardedToPicker(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	if m.picker == nil {
		t.Fatal("expected picker at stepPickTemplate")
	}
	// Send Enter; without the fix this returns (m, nil) because handleKey had
	// no case for stepPickTemplate. With the fix the key is forwarded to the
	// picker, which returns a cmd that produces picker.PickedMsg.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 13})
	if cmd == nil {
		t.Fatal("Enter on pickTemplate should forward to picker and return a cmd")
	}
	msg := cmd()
	if _, ok := msg.(picker.PickedMsg); !ok {
		t.Fatalf("expected picker.PickedMsg, got %T", msg)
	}
}

func TestPickModelKeyForwardedToPicker(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), SkipToIntroModel: true, ScopedProvider: "ollama"})
	if m.picker == nil {
		t.Fatal("expected picker at stepPickModel")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 13})
	if cmd == nil {
		t.Fatal("Enter on pickModel should forward to picker and return a cmd")
	}
	msg := cmd()
	if _, ok := msg.(picker.PickedMsg); !ok {
		t.Fatalf("expected picker.PickedMsg, got %T", msg)
	}
}

func TestPasteMsgIntoAPIKeyInput(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	m, _ = m.Update(pickerPicked("openrouter"))
	if m.step != stepAPIKey {
		t.Fatalf("expected stepAPIKey, got %v", m.step)
	}
	updated, _ := m.Update(tea.PasteMsg{Content: "sk-pasted-key"})
	if got := updated.input.Value(); got != "sk-pasted-key" {
		t.Fatalf("input.Value() = %q, want %q", got, "sk-pasted-key")
	}
}

func TestPasteMsgIntoBaseURLInput(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	m, _ = m.Update(pickerPicked("custom"))
	if m.step != stepBaseURL {
		t.Fatalf("expected stepBaseURL, got %v", m.step)
	}
	updated, _ := m.Update(tea.PasteMsg{Content: "https://example.com/v1"})
	if got := updated.input.Value(); got != "https://example.com/v1" {
		t.Fatalf("input.Value() = %q, want %q", got, "https://example.com/v1")
	}
}

func TestPickKimiPropagatesTemperatureLocked(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	updated, _ := m.Update(pickerPicked("kimi"))
	if updated.step != stepAPIKey {
		t.Fatalf("kimi (remote, KeyEnv set) should enter apiKey, got step = %v", updated.step)
	}
	if updated.template.ID != "kimi" {
		t.Fatalf("template ID = %q", updated.template.ID)
	}
	if !updated.providerCfg.TemperatureLocked {
		t.Fatal("kimi template must propagate TemperatureLocked into the provider config")
	}
	if updated.providerCfg.APIKeyEnv != "KIMI_API_KEY" {
		t.Fatalf("APIKeyEnv = %q", updated.providerCfg.APIKeyEnv)
	}
}

func TestTickThrottlesSpinner(t *testing.T) {
	start := time.Now()
	msg := tick()()
	elapsed := time.Since(start)
	if _, ok := msg.(TickMsg); !ok {
		t.Fatalf("tick produced %T, want TickMsg", msg)
	}
	if elapsed < 90*time.Millisecond {
		t.Fatalf("tick returned after %v — unthrottled busy loop", elapsed)
	}
}

func TestRemoteTemplateGatedWhenRemoteDisallowed(t *testing.T) {
	// Default config has RemoteProvidersAllowed = false.
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	updated, _ := m.Update(pickerPicked("openrouter"))
	if updated.step != stepRemoteGate {
		t.Fatalf("remote template should land on stepRemoteGate, got step = %v", updated.step)
	}
	if updated.title != "Remote providers are disabled" {
		t.Fatalf("title = %q, want 'Remote providers are disabled'", updated.title)
	}
}

func TestRemoteTemplateNotGatedWhenAllowed(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	updated, _ := m.Update(pickerPicked("openrouter"))
	if updated.step != stepAPIKey {
		t.Fatalf("remote template should skip gate and enter apiKey, got step = %v", updated.step)
	}
}

func TestRemoteGateEscGoesBackToTemplates(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	m, _ = m.Update(pickerPicked("openrouter"))
	if m.step != stepRemoteGate {
		t.Fatalf("expected stepRemoteGate, got %v", m.step)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: 27})
	if updated.step != stepPickTemplate {
		t.Fatalf("Esc on remote gate should go back to pickTemplate, got step = %v", updated.step)
	}
}

func TestRemoteGateYEnablesAndEntersAPIKey(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	m, _ = m.Update(pickerPicked("openrouter"))
	if m.step != stepRemoteGate {
		t.Fatalf("expected stepRemoteGate, got %v", m.step)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: 121}) // 'y'
	if updated.step != stepAPIKey {
		t.Fatalf("y on remote gate should enter apiKey, got step = %v", updated.step)
	}
	if !updated.remoteEnabled {
		t.Fatal("remoteEnabled should be true after pressing y")
	}
}

func TestDoneMsgCarriesEnabledRemote(t *testing.T) {
	// Simulate: pick remote template -> gate -> y -> apiKey -> enter -> probe -> pick model -> prepare -> summary enter
	cfg := config.Default()
	deny := false
	cfg.Privacy.ModelsDevEnrichment = &deny
	m := newTestWizard(t, Opts{Cfg: cfg, Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("openrouter"))
	m, _ = m.Update(tea.KeyPressMsg{Code: 121}) // y
	m.input.SetValue("sk-test")
	m, _ = m.Update(tea.KeyPressMsg{Code: 13}) // enter -> probing
	// Skip probe
	m, _ = m.Update(tea.KeyPressMsg{Code: 115}) // s -> skip
	// Pick a model -> lands on preparing
	m, _ = m.Update(pickerPicked("gpt-4o"))
	if m.step != stepPreparing {
		t.Fatalf("after pickModel should be stepPreparing, got %v", m.step)
	}
	m, _ = m.Update(PreparedMsg{Provider: m.providerName, Result: presetsetup.Result{Prepared: 1, Fallback: 1}})
	if m.step != stepSummary {
		t.Fatalf("after prepare should be stepSummary, got %v", m.step)
	}
	// Press Enter on summary to emit DoneMsg
	_, cmd := m.Update(tea.KeyPressMsg{Code: 13})
	if cmd == nil {
		t.Fatal("expected a DoneMsg cmd")
	}
	msg := cmd()
	dm, ok := msg.(DoneMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want DoneMsg", msg)
	}
	if !dm.EnabledRemote {
		t.Fatal("DoneMsg.EnabledRemote should be true after gate flow")
	}
}

func TestCustomBaseURLGatedWhenRemoteDisallowed(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	m, _ = m.Update(pickerPicked("custom"))
	m.input.SetValue("https://remotehost/v1")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepRemoteGate {
		t.Fatalf("non-localhost baseURL should land on stepRemoteGate, got step = %v", updated.step)
	}
}

func TestCustomLocalhostBaseURLSkipsGate(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	m, _ = m.Update(pickerPicked("custom"))
	m.input.SetValue("http://localhost:11434/v1")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepRename {
		t.Fatalf("localhost baseURL should skip gate and enter rename, got step = %v", updated.step)
	}
}

func TestSummaryShowsNameModelAndDestination(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}, CfgPath: "/tmp/.marshal/config.toml"})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	advanceToSummary(m)
	if m.step != stepSummary {
		t.Fatalf("expected stepSummary, got %v", m.step)
	}
	view := m.View(80, 24)
	if !strings.Contains(view, m.providerName) {
		t.Fatalf("summary should show provider name %q", m.providerName)
	}
	if !strings.Contains(view, "qwen2.5-coder:7b") {
		t.Fatal("summary should show model name")
	}
	if !strings.Contains(view, "/tmp/.marshal/config.toml") {
		t.Fatal("summary should show destination config path")
	}
}

func TestSummaryEnterEmitsDone(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	advanceToSummary(m)
	if m.step != stepSummary {
		t.Fatalf("expected stepSummary, got %v", m.step)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 13})
	if cmd == nil {
		t.Fatal("Enter on summary should emit a DoneMsg cmd")
	}
	msg := cmd()
	if _, ok := msg.(DoneMsg); !ok {
		t.Fatalf("cmd produced %T, want DoneMsg", msg)
	}
}

func TestSummaryRenameChangesProviderName(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	advanceToSummary(m)
	if m.step != stepSummary {
		t.Fatalf("expected stepSummary, got %v", m.step)
	}
	// Press 'n' to enter rename
	m, _ = m.Update(tea.KeyPressMsg{Code: 110}) // 'n'
	if m.step != stepRename {
		t.Fatalf("expected stepRename, got %v", m.step)
	}
	// Change the name
	m.renameInput.SetValue("my-ollama")
	// Press Enter to confirm rename
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
	if m.step != stepSummary {
		t.Fatalf("after rename should return to stepSummary, got %v", m.step)
	}
	if m.providerName != "my-ollama" {
		t.Fatalf("providerName = %q, want %q", m.providerName, "my-ollama")
	}
	// Press Enter on summary to emit DoneMsg
	_, cmd := m.Update(tea.KeyPressMsg{Code: 13})
	if cmd == nil {
		t.Fatal("Enter on summary should emit a DoneMsg cmd")
	}
	msg := cmd()
	dm, ok := msg.(DoneMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want DoneMsg", msg)
	}
	if dm.Provider != "my-ollama" {
		t.Fatalf("DoneMsg.Provider = %q, want %q", dm.Provider, "my-ollama")
	}
}

func TestScopedModelSwitchPreparesPresets(t *testing.T) {
	cfg := config.Default()
	deny := false
	cfg.Privacy.ModelsDevEnrichment = &deny
	m := newTestWizard(t, Opts{Cfg: cfg, SkipToIntroModel: true, ScopedProvider: "ollama", Discovered: map[string][]schema.ModelInfo{}})
	if m.step != stepPickModel {
		t.Fatalf("expected stepPickModel, got %v", m.step)
	}
	// Picking a model now lands on preparing, not stepDone.
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	if m.step != stepPreparing {
		t.Fatalf("expected stepPreparing, got %v", m.step)
	}
	m, _ = m.Update(PreparedMsg{Provider: m.providerName, Result: presetsetup.Result{Prepared: 1, Fallback: 1}})
	if m.step != stepSummary {
		t.Fatalf("expected stepSummary, got %v", m.step)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 13})
	if cmd == nil {
		t.Fatal("Enter on summary should emit a DoneMsg cmd")
	}
	msg := cmd()
	dm, ok := msg.(DoneMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want DoneMsg", msg)
	}
	if dm.Model != "qwen2.5-coder:7b" {
		t.Fatalf("DoneMsg.Model = %q", dm.Model)
	}
}

func TestSummaryEscGoesBackToModelPick(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	advanceToSummary(m)
	if m.step != stepSummary {
		t.Fatalf("expected stepSummary, got %v", m.step)
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 27})
	if cmd != nil {
		t.Fatal("Esc on summary should not emit a cmd")
	}
	if updated.step != stepPickModel {
		t.Fatalf("Esc on summary should go back to pickModel, got %v", updated.step)
	}
}

func TestRenameEmptyNameShowsError(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	advanceToSummary(m)
	m, _ = m.Update(tea.KeyPressMsg{Code: 110}) // 'n'
	m.renameInput.SetValue("")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.err == "" {
		t.Fatal("expected error for empty rename")
	}
}

func TestRenameDuplicateNameShowsError(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	m := newTestWizard(t, Opts{Cfg: cfg, Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	advanceToSummary(m)
	m, _ = m.Update(tea.KeyPressMsg{Code: 110}) // 'n'
	m.renameInput.SetValue("ollama")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.err == "" {
		t.Fatal("expected error for duplicate provider name")
	}
	if !strings.Contains(updated.err, "already exists") {
		t.Fatalf("error message = %q, want 'already exists'", updated.err)
	}
}

func TestRenameToOwnNameIsAllowed(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	m := newTestWizard(t, Opts{Cfg: cfg, Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	advanceToSummary(m)
	m, _ = m.Update(tea.KeyPressMsg{Code: 110}) // 'n'
	// The uniqueName() will produce "ollama-2" or similar, but we set it to "ollama" to test
	// that renaming to the same name is allowed.
	m.renameInput.SetValue("ollama")
	// Set providerName to "ollama" to match the existing provider
	m.providerName = "ollama"
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.err != "" {
		t.Fatalf("unexpected error for renaming to own name: %q", updated.err)
	}
	if updated.step != stepSummary {
		t.Fatalf("expected stepSummary after rename, got %v", updated.step)
	}
}

func TestRenameEscReturnsToSummary(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	advanceToSummary(m)
	m, _ = m.Update(tea.KeyPressMsg{Code: 110}) // 'n'
	if m.step != stepRename {
		t.Fatalf("expected stepRename, got %v", m.step)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: 27})
	if updated.step != stepSummary {
		t.Fatalf("Esc on rename should return to summary, got %v", updated.step)
	}
}

func TestPasteMsgIntoRenameInput(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	advanceToSummary(m)
	if m.step != stepSummary {
		t.Fatalf("expected stepSummary, got %v", m.step)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: 110}) // 'n'
	if m.step != stepRename {
		t.Fatalf("expected stepRename, got %v", m.step)
	}
	m.renameInput.SetValue("")
	m, _ = m.Update(tea.PasteMsg{Content: "renamed-provider"})
	if got := m.renameInput.Value(); got != "renamed-provider" {
		t.Fatalf("renameInput.Value() = %q, want %q", got, "renamed-provider")
	}
}

func TestCustomFlowPromptsForNameAfterBaseURL(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	m, _ = m.Update(pickerPicked("custom"))
	m.input.SetValue("https://api.coolmodels.com/v1")
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
	if m.step != stepRename {
		t.Fatalf("custom flow should prompt for name after baseURL, got %v", m.step)
	}
	if got := m.providerName; got != "coolmodels.com" {
		t.Errorf("default providerName = %q, want coolmodels.com", got)
	}
	if got := m.renameInput.Value(); got != m.providerName {
		t.Errorf("rename input = %q, want %q", got, m.providerName)
	}
}

func TestCustomFlowRenameAdvancesToAPIKey(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	m, _ = m.Update(pickerPicked("custom"))
	m.input.SetValue("https://api.coolmodels.com/v1")
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
	m.renameInput.SetValue("cool")
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
	if m.step != stepAPIKey {
		t.Fatalf("after custom rename should be apiKey, got %v", m.step)
	}
	if m.providerName != "cool" {
		t.Errorf("providerName = %q, want cool", m.providerName)
	}
}

func TestCustomFlowRenameEscReturnsToBaseURL(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	m, _ = m.Update(pickerPicked("custom"))
	m.input.SetValue("https://api.coolmodels.com/v1")
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
	m, _ = m.Update(tea.KeyPressMsg{Code: 27})
	if m.step != stepBaseURL {
		t.Fatalf("Esc from custom rename should return to baseURL, got %v", m.step)
	}
}

func TestSummaryRenameHintIsProminent(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	advanceToSummary(m)
	view := m.View(80, 24)
	if !strings.Contains(view, "rename provider") {
		t.Fatalf("summary view should mention rename provider, got:\n%s", view)
	}
}

func TestModelPickerUsesStoredTemplateID(t *testing.T) {
	m := newTestWizard(t, Opts{
		Cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"work-openai": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1", Template: "openai"},
		}},
		Discovered:       map[string][]schema.ModelInfo{},
		SkipToIntroModel: true,
		ScopedProvider:   "work-openai",
	})
	view := m.View(80, 24)
	for _, want := range []string{"gpt-4o", "gpt-4o-mini", "o3-mini"} {
		if !strings.Contains(view, want) {
			t.Errorf("picker view missing catalog model %q", want)
		}
	}
}

// advanceToSummary drives the consent/prepare flow to the summary step
// without running the prepare cmd, which would probe the provider.
func advanceToSummary(m *Model) {
	if m.step == stepConsent {
		m.Update(tea.KeyPressMsg{Code: 110}) // n
	}
	m.Update(PreparedMsg{Provider: m.providerName, Result: presetsetup.Result{Prepared: 1, Fallback: 1}})
}

// newConnectForModelPick builds a Model at stepPickModel with a discovered
// model that has known limits, ready to test the consent/prepare flow.
func newConnectForModelPick(t *testing.T) *Model {
	t.Helper()
	m := newTestWizard(t, Opts{
		Cfg: config.Default(),
		Discovered: map[string][]schema.ModelInfo{
			"openai": {
				{ID: "gpt-4o", ContextWindow: 128000, MaxOutputTokens: 16384},
			},
		},
		SkipToIntroModel: true,
		ScopedProvider:   "openai",
	})
	if m.step != stepPickModel {
		t.Fatalf("newConnectForModelPick: step = %v, want stepPickModel", m.step)
	}
	return m
}

func TestPickingAModelEntersConsent(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))

	if m.step != stepConsent {
		t.Fatalf("step = %v, want stepConsent", m.step)
	}
}

func TestScopedModelsFlowReachesConsent(t *testing.T) {
	m := newConnectForModelPick(t)
	m.scopedProvider = "openai" // the /models flow
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))

	if m.step != stepConsent {
		t.Fatalf("step = %v, want stepConsent — /models must not skip it", m.step)
	}
}

func TestConsentAllowSetsPreferenceAndPrepares(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))
	m, _ = m.Update(tea.KeyPressMsg{Code: 121}) // y

	if m.step != stepPreparing {
		t.Fatalf("step = %v, want stepPreparing", m.step)
	}
	if m.cfg.Privacy.ModelsDevEnrichment == nil || !*m.cfg.Privacy.ModelsDevEnrichment {
		t.Fatal("answering y must record an allow preference")
	}
}

func TestConsentDenySetsPreferenceAndPrepares(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))
	m, _ = m.Update(tea.KeyPressMsg{Code: 110}) // n

	if m.step != stepPreparing {
		t.Fatalf("step = %v, want stepPreparing", m.step)
	}
	if m.cfg.Privacy.ModelsDevEnrichment == nil || *m.cfg.Privacy.ModelsDevEnrichment {
		t.Fatal("answering n must record a deny preference")
	}
}

func TestConnectSkipsConsentWhenAnswered(t *testing.T) {
	cfg := config.Default()
	deny := false
	cfg.Privacy.ModelsDevEnrichment = &deny
	m := newTestWizard(t, Opts{
		Cfg: cfg,
		Discovered: map[string][]schema.ModelInfo{
			"openai": {{ID: "gpt-4o", ContextWindow: 128000}},
		},
		SkipToIntroModel: true,
		ScopedProvider:   "openai",
	})
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))

	if m.step != stepPreparing {
		t.Fatalf("step = %v, want stepPreparing (consent already answered)", m.step)
	}
}

func TestConnectReachesSummaryWithoutConfirmScreen(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))
	m, _ = m.Update(tea.KeyPressMsg{Code: 110}) // n
	m, _ = m.Update(PreparedMsg{Provider: m.providerName, Result: presetsetup.Result{Prepared: 1, Fallback: 1}})

	if m.step != stepSummary {
		t.Fatalf("step = %v, want stepSummary", m.step)
	}
}

func TestConnectDoneCarriesPreparedPresets(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))
	m, _ = m.Update(tea.KeyPressMsg{Code: 110}) // n
	m, _ = m.Update(PreparedMsg{
		Provider: m.providerName,
		Presets: map[string]routing.ModelPreset{
			"openai/gpt-4o":      {Name: "openai/gpt-4o", Provider: "openai", Model: "gpt-4o"},
			"openai/gpt-4o-mini": {Name: "openai/gpt-4o-mini", Provider: "openai", Model: "gpt-4o-mini"},
		},
		Result: presetsetup.Result{Prepared: 2, Matched: 2},
	})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 13})
	if cmd == nil {
		t.Fatal("Enter on summary should emit a DoneMsg cmd")
	}
	dm, ok := cmd().(DoneMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want DoneMsg", cmd())
	}
	if len(dm.Presets) != 2 {
		t.Fatalf("DoneMsg.Presets = %v, want 2 entries", dm.Presets)
	}
}

func TestConfirmLimitsEscGoesBackToModelPick(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.step != stepPickModel {
		t.Errorf("step = %v, want stepPickModel after Esc", m.step)
	}
}

func pickerPicked(value string) tea.Msg { return picker.PickedMsg{Value: value} }

// TestPickCodexTemplateEntersOAuthLogin: the codex template has no API key,
// so picking it must walk the browser login rather than the key prompt.
func TestPickCodexTemplateEntersOAuthLogin(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg, Auth: newMemAuth(), OpenBrowser: func(string) error { return nil }})
	updated, _ := m.Update(pickerPicked("openai-codex"))
	if updated.step != stepOAuthLogin {
		t.Fatalf("codex template should enter the OAuth login step, got %v", updated.step)
	}
	if updated.template.Auth != "oauth" {
		t.Fatalf("template auth = %q, want oauth", updated.template.Auth)
	}
	if updated.providerCfg.Auth != "oauth" {
		t.Fatalf("provider config auth = %q, want oauth", updated.providerCfg.Auth)
	}
	if updated.providerCfg.Type != "openai_codex" {
		t.Fatalf("provider config type = %q, want openai_codex", updated.providerCfg.Type)
	}
	// The provider name must be settled before the flow starts: the token
	// is stored under marshal:provider:<name>.
	if updated.providerName == "" {
		t.Fatal("provider name must be set before the OAuth flow starts")
	}
}

// TestOAuthTemplateNeverPromptsForAPIKey guards the invariant that an OAuth
// template cannot reach the API-key step.
func TestOAuthTemplateNeverPromptsForAPIKey(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg, Auth: newMemAuth(), OpenBrowser: func(string) error { return nil }})
	updated, _ := m.Update(pickerPicked("openai-codex"))
	if updated.step == stepAPIKey {
		t.Fatal("an OAuth template must not enter the API-key step")
	}
}

// TestOAuthDoneFailureStaysOnLoginStep: a failed sign-in must show the error
// and stay put so the user can retry or cancel, not silently advance.
func TestOAuthDoneFailureStaysOnLoginStep(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg, Auth: newMemAuth(), OpenBrowser: func(string) error { return nil }})
	updated, _ := m.Update(pickerPicked("openai-codex"))
	name := updated.providerName

	updated, _ = updated.Update(OAuthDoneMsg{Provider: name, Err: errors.New("user cancelled")})
	if updated.step != stepOAuthLogin {
		t.Fatalf("step = %v, want stepOAuthLogin after a failed sign-in", updated.step)
	}
	if !strings.Contains(updated.err, "sign-in failed") {
		t.Fatalf("err = %q, want a sign-in failure message", updated.err)
	}
}

func TestOAuthCopyLinkCopiesFullURL(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	m.step = stepOAuthLogin
	m.picker = nil
	m.oauthDisplay = &oauthDisplay{}
	full := "https://auth.openai.com/oauth/authorize?state=" + strings.Repeat("x", 180) + "&code_challenge=complete"
	m.oauthDisplay.ShowURL(full)
	if view := ansi.Strip(m.View(80, 24)); !strings.Contains(view, "[c] Copy sign-in link") {
		t.Fatalf("missing copy action: %s", view)
	}
	for _, key := range []tea.KeyPressMsg{tea.KeyPressMsg{Code: 'c'}, tea.KeyPressMsg{Code: tea.KeyEnter}} {
		_, cmd := m.Update(key)
		if cmd == nil {
			t.Fatal("copy action returned no command")
		}
		msg, ok := cmd().(CopyLinkMsg)
		if !ok || msg.URL != full {
			t.Fatalf("copy action did not preserve the complete URL: %T", msg)
		}
	}
}

func TestOAuthCopyWaitsForURL(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	m.step = stepOAuthLogin
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c'})
	if cmd != nil {
		t.Fatal("copy should wait until the authorization URL exists")
	}
}

// TestOAuthDoneSuccessAdvancesToProbing: a successful sign-in proceeds to
// the model probe, which is where the token is first exercised.
func TestOAuthDoneSuccessAdvancesToProbing(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg, Auth: newMemAuth(), OpenBrowser: func(string) error { return nil }})
	updated, _ := m.Update(pickerPicked("openai-codex"))
	name := updated.providerName

	updated, _ = updated.Update(OAuthDoneMsg{Provider: name})
	if updated.step != stepProbing {
		t.Fatalf("step = %v, want stepProbing after a successful sign-in", updated.step)
	}
}

// TestOAuthDoneStaleResultIgnored: a result from an abandoned flow must not
// advance the wizard.
func TestOAuthDoneStaleResultIgnored(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg, Auth: newMemAuth(), OpenBrowser: func(string) error { return nil }})
	updated, _ := m.Update(pickerPicked("openai-codex"))

	updated, _ = updated.Update(OAuthDoneMsg{Provider: "some-other-provider"})
	if updated.step != stepOAuthLogin {
		t.Fatalf("step = %v, want stepOAuthLogin (stale result must be ignored)", updated.step)
	}
}

// TestOAuthLoginViewRendersWaitingState: the login step must render
// something, so the user is not staring at an empty panel while the browser
// flow runs.
func TestOAuthLoginViewRendersWaitingState(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg, Auth: newMemAuth(), OpenBrowser: func(string) error { return nil }})
	updated, _ := m.Update(pickerPicked("openai-codex"))
	view := updated.View(80, 24)
	if !strings.Contains(view, "signing in") {
		t.Fatalf("view should mention signing in, got:\n%s", view)
	}
}

// TestOAuthLoginShowsAuthorizationURL: the panel must surface the
// authorization URL. Without it the flow is unrecoverable whenever the
// browser does not open (headless session, no xdg-open, sandboxed terminal).
func TestOAuthLoginShowsAuthorizationURL(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg, Auth: newMemAuth(), OpenBrowser: func(string) error { return nil }})
	updated, _ := m.Update(pickerPicked("openai-codex"))

	// Before the engine reports a URL the panel says it is opening one.
	if view := updated.View(80, 24); !strings.Contains(view, "Opening your browser") {
		t.Fatalf("view should say the browser is opening, got:\n%s", view)
	}

	// Once ShowURL fires, the URL must be rendered.
	updated.oauthDisplay.ShowURL("https://auth.openai.com/oauth/authorize?client_id=app_test&state=xyz")
	view := updated.View(80, 24)
	if !strings.Contains(view, "auth.openai.com") {
		t.Fatalf("view should show the authorization URL, got:\n%s", view)
	}
}

// TestOAuthLoginWiresBrowserOpener: the engine's Open hook must be set, or
// Authorize waits on a loopback callback the user was never told how to
// trigger. The provider package deliberately leaves Open nil (launching a
// browser is a UI concern), so the connect flow is the only place it can be
// wired.
func TestOAuthLoginWiresBrowserOpener(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	auth := newMemAuth()
	m := newTestWizard(t, Opts{Cfg: cfg, Auth: auth, OpenBrowser: func(string) error { return nil }})
	updated, _ := m.Update(pickerPicked("openai-codex"))

	// The engine must come from the injected resolver — looking it up through
	// the process-wide constructor would both touch the real keychain and miss
	// the instance the wizard actually wired.
	engine, err := auth.CodexOAuthEngine(updated.providerName)
	if err != nil {
		t.Fatalf("CodexOAuthEngine: %v", err)
	}
	if engine.Open == nil {
		t.Fatal("engine.Open is nil; the browser would never open and the flow would dead-end")
	}
}

// TestRemoteGateRoutesOAuthTemplateToLogin: chatgpt.com is not localhost and
// remote providers default to disabled, so the codex template reaches the
// remote gate on a fresh install. Enabling remote providers there must route
// to the OAuth login, not to an API-key prompt the provider ignores —
// otherwise the wizard completes unauthenticated and only fails on the first
// chat.
func TestRemoteGateRoutesOAuthTemplateToLogin(t *testing.T) {
	// Default config: remote providers disabled. The injected resolver keeps
	// this from reaching the process-wide OAuth engine (and its real keychain).
	m := newTestWizard(t, Opts{Cfg: config.Default(), Auth: newMemAuth(), OpenBrowser: func(string) error { return nil }})
	updated, _ := m.Update(pickerPicked("openai-codex"))
	if updated.step != stepRemoteGate {
		t.Fatalf("step = %v, want stepRemoteGate with remote providers disabled", updated.step)
	}

	updated, _ = updated.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if updated.step != stepOAuthLogin {
		t.Fatalf("step = %v, want stepOAuthLogin after enabling remote providers", updated.step)
	}
	if updated.step == stepAPIKey {
		t.Fatal("an OAuth template must never reach the API-key step")
	}
}

// TestRemoteGateStillRoutesKeyedTemplateToAPIKey guards the pre-existing
// behavior for non-OAuth templates.
func TestRemoteGateStillRoutesKeyedTemplateToAPIKey(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default()})
	updated, _ := m.Update(pickerPicked("openrouter"))
	if updated.step != stepRemoteGate {
		t.Fatalf("step = %v, want stepRemoteGate", updated.step)
	}
	updated, _ = updated.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if updated.step != stepAPIKey {
		t.Fatalf("step = %v, want stepAPIKey for a keyed template", updated.step)
	}
}

func TestAPIKeyInputIsMasked(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	m, _ = m.Update(pickerPicked("openrouter"))
	if m.step != stepAPIKey {
		t.Fatalf("expected stepAPIKey, got %v", m.step)
	}
	if m.input.EchoMode != textinput.EchoPassword {
		t.Fatalf("input.EchoMode = %v, want EchoPassword", m.input.EchoMode)
	}
}

func TestDollarPrefixSetsEnvVarKey(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	m, _ = m.Update(pickerPicked("openrouter"))
	if m.step != stepAPIKey {
		t.Fatalf("expected stepAPIKey, got %v", m.step)
	}
	m.input.SetValue("$MY_CUSTOM_KEY")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepProbing {
		t.Fatalf("after $ENV Enter should advance to probing, got step = %v", updated.step)
	}
	if updated.providerCfg.APIKey != "" {
		t.Fatalf("APIKey should be empty when using env var, got %q", updated.providerCfg.APIKey)
	}
	if updated.providerCfg.APIKeyEnv != "MY_CUSTOM_KEY" {
		t.Fatalf("APIKeyEnv = %q, want %q", updated.providerCfg.APIKeyEnv, "MY_CUSTOM_KEY")
	}
}

func TestDollarPrefixEmptyNameShowsError(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := newTestWizard(t, Opts{Cfg: cfg})
	m, _ = m.Update(pickerPicked("openrouter"))
	m.input.SetValue("$")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.err == "" {
		t.Fatal("expected error for empty env var name")
	}
	if !strings.Contains(updated.err, "empty") {
		t.Fatalf("error message = %q, want 'empty'", updated.err)
	}
}

func TestProbeFailureShowsHint(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Err: errors.New("connection refused")}))
	if updated.step != stepProbing {
		t.Fatalf("failure should stay probing, got %v", updated.step)
	}
	if !strings.Contains(updated.err, "is the server running") {
		t.Fatalf("expected probe hint in error, got: %q", updated.err)
	}
}

func TestProbeFailureHintForNoSuchHost(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Err: errors.New("no such host")}))
	if !strings.Contains(updated.err, "hostname not found") {
		t.Fatalf("expected hostname hint, got: %q", updated.err)
	}
}

func TestProbeFailureHintForUnauthorized(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Err: errors.New("401 unauthorized")}))
	if !strings.Contains(updated.err, "key rejected") {
		t.Fatalf("expected key rejected hint, got: %q", updated.err)
	}
}

func TestProbeFailureHintForTimeout(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Err: errors.New("deadline exceeded")}))
	if !strings.Contains(updated.err, "timed out") {
		t.Fatalf("expected timeout hint, got: %q", updated.err)
	}
}

func TestProbeFailureHintForCertificate(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Err: errors.New("certificate signed by unknown authority")}))
	if !strings.Contains(updated.err, "TLS problem") {
		t.Fatalf("expected TLS hint, got: %q", updated.err)
	}
}

func TestTruncateErrRuneSafe(t *testing.T) {
	out := strutil.Truncate(strings.Repeat("é", 60), 48, true) // 2-byte runes
	if !utf8.ValidString(out) {
		t.Fatalf("strutil.Truncate produced invalid UTF-8: %q", out)
	}
	// 48 runes + ellipsis
	if n := utf8.RuneCountInString(out); n != 49 {
		t.Fatalf("strutil.Truncate length = %d runes, want 49", n)
	}
}

func TestUniqueNameDisambiguatesCustomProviders(t *testing.T) {
	m := &Model{
		template: acpwire.ProviderTemplate{ID: "custom", Type: "openai_compatible"},
		cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"custom": {BaseURL: "https://first.example/v1"},
		}},
	}
	if got := m.uniqueName(); got != "custom-2" {
		t.Errorf("uniqueName() = %q, want %q — the first custom provider must survive", got, "custom-2")
	}
}

func TestUniqueNameFirstCustomProviderKeepsBaseName(t *testing.T) {
	m := &Model{
		template: acpwire.ProviderTemplate{ID: "custom", Type: "openai_compatible"},
		cfg:      config.Config{Providers: map[string]config.ProviderConfig{}},
	}
	if got := m.uniqueName(); got != "custom" {
		t.Errorf("uniqueName() = %q, want %q", got, "custom")
	}
}

func TestAPIKeyStepDefaultsToEnvVarWhenTemplateHasOne(t *testing.T) {
	m := &Model{template: acpwire.ProviderTemplate{
		ID: "openai", KeyEnv: "OPENAI_API_KEY",
	}}
	m.enterAPIKey()

	if !strings.Contains(m.input.Placeholder, "OPENAI_API_KEY") {
		t.Errorf("placeholder %q should lead with the env var", m.input.Placeholder)
	}
	if strings.HasPrefix(strings.ToLower(m.input.Placeholder), "paste key") {
		t.Errorf("placeholder %q still leads with pasting a literal", m.input.Placeholder)
	}
}

func TestAPIKeyStepStillAcceptsPastedLiteral(t *testing.T) {
	m := &Model{template: acpwire.ProviderTemplate{ID: "openai", KeyEnv: "OPENAI_API_KEY"}}
	m.enterAPIKey()
	m.input.SetValue("sk-literal-key")
	m.confirmInput()

	if m.providerCfg.APIKey != "sk-literal-key" {
		t.Errorf("APIKey = %q, want the pasted literal", m.providerCfg.APIKey)
	}
	if m.providerCfg.APIKeyEnv != "" {
		t.Errorf("APIKeyEnv = %q, want empty when a literal was pasted", m.providerCfg.APIKeyEnv)
	}
}

func TestAPIKeyStepBlankUsesTemplateEnvVar(t *testing.T) {
	m := &Model{template: acpwire.ProviderTemplate{ID: "openai", KeyEnv: "OPENAI_API_KEY"}}
	m.enterAPIKey()
	m.input.SetValue("")
	m.confirmInput()

	if m.providerCfg.APIKeyEnv != "OPENAI_API_KEY" {
		t.Errorf("APIKeyEnv = %q, want the template default", m.providerCfg.APIKeyEnv)
	}
	if m.providerCfg.APIKey != "" {
		t.Errorf("APIKey = %q, want empty", m.providerCfg.APIKey)
	}
}

func TestModelValueRoundTrip(t *testing.T) {
	for _, tc := range []struct{ provider, model string }{
		{"openai", "gpt-4o"},
		{"openrouter", "anthropic/claude-sonnet-4"},
		{"custom-2", "some/nested/model:tag"},
	} {
		p, mdl := decodeModelValue(encodeModelValue(tc.provider, tc.model))
		if p != tc.provider || mdl != tc.model {
			t.Errorf("round trip (%q,%q) → (%q,%q)", tc.provider, tc.model, p, mdl)
		}
	}
}

func TestModelPickerListsEveryProvider(t *testing.T) {
	m := newTestWizard(t, Opts{
		Cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"openai": {BaseURL: "https://api.openai.com/v1"},
			"groq":   {BaseURL: "https://api.groq.com/openai/v1"},
		}},
		Discovered: map[string][]schema.ModelInfo{
			"openai": {{ID: "gpt-4o"}},
			"groq":   {{ID: "llama-3.3-70b"}},
		},
		SkipToIntroModel: true,
		AllProviders:     true,
	})
	view := m.View(80, 24)
	for _, want := range []string{"openai", "groq"} {
		if !strings.Contains(view, want) {
			t.Errorf("picker view missing group %q", want)
		}
	}
}

func TestModelPickerPickAttributesTheRightProvider(t *testing.T) {
	m := newTestWizard(t, Opts{
		Cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"openai": {BaseURL: "https://api.openai.com/v1"},
			"groq":   {BaseURL: "https://api.groq.com/openai/v1"},
		}},
		Discovered: map[string][]schema.ModelInfo{
			"openai": {{ID: "gpt-4o"}},
			"groq":   {{ID: "llama-3.3-70b"}},
		},
		SkipToIntroModel: true,
		AllProviders:     true,
	})
	// Send a PickedMsg through the public Update path.
	_, cmd := m.Update(picker.PickedMsg{Value: encodeModelValue("groq", "llama-3.3-70b")})
	if m.providerName != "groq" {
		t.Errorf("providerName = %q, want groq", m.providerName)
	}
	if m.modelChosen != "llama-3.3-70b" {
		t.Errorf("modelChosen = %q, want llama-3.3-70b", m.modelChosen)
	}
	// The scoped flow (scopedProvider is empty since AllProviders doesn't set it)
	// should land on the consent step, not stepDone.
	if m.step != stepConsent {
		t.Errorf("step = %v, want stepConsent", m.step)
	}
	_ = cmd
}

func TestUniqueNameEmptyTemplateIDBehavesAsCustom(t *testing.T) {
	m := &Model{
		template: acpwire.ProviderTemplate{ID: "", Type: "openai_compatible"},
		cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"custom": {}, "custom-2": {},
		}},
	}
	if got := m.uniqueName(); got != "custom-3" {
		t.Errorf("uniqueName() = %q, want %q", got, "custom-3")
	}
}

func TestUniqueNameDerivesFromBaseURLHost(t *testing.T) {
	m := &Model{
		template:    acpwire.ProviderTemplate{ID: "custom", Type: "openai_compatible"},
		providerCfg: config.ProviderConfig{BaseURL: "https://api.coolmodels.com/v1"},
		cfg:         config.Config{Providers: map[string]config.ProviderConfig{}},
	}
	if got := m.uniqueName(); got != "coolmodels.com" {
		t.Errorf("uniqueName() = %q, want %q", got, "coolmodels.com")
	}
}

func TestUniqueNameDisambiguatesHostDerivedName(t *testing.T) {
	m := &Model{
		template:    acpwire.ProviderTemplate{ID: "openai_compatible", Type: "openai_compatible"},
		providerCfg: config.ProviderConfig{BaseURL: "https://api.openai.com/v1"},
		cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"openai.com": {},
		}},
	}
	if got := m.uniqueName(); got != "openai.com-2" {
		t.Errorf("uniqueName() = %q, want %q", got, "openai.com-2")
	}
}

func TestUniqueNameFallsBackToCustomForInvalidURL(t *testing.T) {
	m := &Model{
		template:    acpwire.ProviderTemplate{ID: "custom", Type: "openai_compatible"},
		providerCfg: config.ProviderConfig{BaseURL: "not a url"},
		cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"custom": {},
		}},
	}
	if got := m.uniqueName(); got != "custom-2" {
		t.Errorf("uniqueName() = %q, want %q", got, "custom-2")
	}
}

func TestModelStepEmitsRefreshOnCtrlR(t *testing.T) {
	m := newTestWizard(t, Opts{
		Cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"openai": {Type: "openai_compatible", BaseURL: "https://a/v1"},
		}},
		Discovered:       map[string][]schema.ModelInfo{"openai": {{ID: "gpt-4o"}}},
		SkipToIntroModel: true,
		AllProviders:     true,
	})

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+r produced no command")
	}
	if _, ok := cmd().(RefreshMsg); !ok {
		t.Errorf("got %T, want RefreshMsg", cmd())
	}
}

func TestPlainRStillFiltersTheModelList(t *testing.T) {
	m := newTestWizard(t, Opts{
		Cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"openai": {Type: "openai_compatible", BaseURL: "https://a/v1"},
		}},
		Discovered:       map[string][]schema.ModelInfo{"openai": {{ID: "gpt-4o"}}},
		SkipToIntroModel: true,
		AllProviders:     true,
	})

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd != nil {
		if _, ok := cmd().(RefreshMsg); ok {
			t.Error("plain r must reach the picker filter, not refresh")
		}
	}
}

func TestModelStepSubtitleNamesNoProviderInAllProvidersMode(t *testing.T) {
	m := newTestWizard(t, Opts{
		Cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"openai": {Type: "openai_compatible", BaseURL: "https://a/v1"},
			"groq":   {Type: "openai_compatible", BaseURL: "https://b/v1"},
		}},
		Discovered: map[string][]schema.ModelInfo{
			"openai": {{ID: "gpt-4o"}},
			"groq":   {{ID: "llama-3.3-70b"}},
		},
		SkipToIntroModel: true,
		AllProviders:     true,
		ScopedProvider:   "groq",
	})

	if strings.Contains(m.subtitle, "groq") || strings.Contains(m.subtitle, "openai") {
		t.Errorf("subtitle %q names one provider while listing all of them", m.subtitle)
	}
	if m.subtitle == "" {
		t.Error("subtitle should say something in all-providers mode")
	}
}

func TestProbeResultAttributesToMsgProviderInAllProvidersMode(t *testing.T) {
	m := newTestWizard(t, Opts{
		Cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"kimi":   {Type: "openai_compatible", BaseURL: "https://api.moonshot.cn/v1"},
			"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
		}},
		Discovered:       map[string][]schema.ModelInfo{},
		SkipToIntroModel: true,
		AllProviders:     true,
		ScopedProvider:   "kimi",
	})
	// Ollama's probe result arrives while the panel is scoped to kimi (the
	// first sorted provider). It must land under "ollama", not clobber kimi.
	updated, _ := m.Update(probe.ResultMsg{FieldID: parentModelProbeFieldID, Provider: "ollama", Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}})
	if got := updated.discovered["ollama"]; len(got) != 1 || got[0].ID != "qwen2.5-coder:7b" {
		t.Errorf("discovered[ollama] = %v, want the probed model", got)
	}
	if got := updated.discovered["kimi"]; len(got) != 0 {
		t.Errorf("discovered[kimi] = %v, want untouched by ollama's result", got)
	}
	if len(updated.models) != 0 {
		t.Errorf("scoped models = %v, want unset in all-providers mode", updated.models)
	}
	if updated.providerName != "kimi" {
		t.Errorf("providerName = %q, want still scoped to kimi", updated.providerName)
	}
}

func TestProbeFailureInAllProvidersModeKeepsPickerAndNotesProvider(t *testing.T) {
	m := newTestWizard(t, Opts{
		Cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"kimi":   {Type: "openai_compatible", BaseURL: "https://api.moonshot.cn/v1"},
			"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
		}},
		Discovered:       map[string][]schema.ModelInfo{},
		SkipToIntroModel: true,
		AllProviders:     true,
		ScopedProvider:   "kimi",
	})
	updated, _ := m.Update(probe.ResultMsg{FieldID: parentModelProbeFieldID, Provider: "kimi", Err: errors.New("401 unauthorized")})
	if updated.step != stepPickModel {
		t.Errorf("step = %v, want to stay on pickModel", updated.step)
	}
	if updated.err != "" {
		t.Errorf("err = %q, want unset; failures are per-provider notes", updated.err)
	}
	if _, ok := updated.probeErrs["kimi"]; !ok {
		t.Error("probeErrs should record kimi's failure")
	}
	if view := updated.View(80, 24); !strings.Contains(view, "probe failed") {
		t.Errorf("picker view should note kimi's failed probe:\n%s", view)
	}
	// A later success clears the failure note.
	updated, _ = updated.Update(probe.ResultMsg{FieldID: parentModelProbeFieldID, Provider: "kimi", Models: []schema.ModelInfo{{ID: "kimi-k2"}}})
	if _, ok := updated.probeErrs["kimi"]; ok {
		t.Error("probeErrs should forget kimi after a successful probe")
	}
	if got := updated.discovered["kimi"]; len(got) != 1 || got[0].ID != "kimi-k2" {
		t.Errorf("discovered[kimi] = %v, want the probed model", got)
	}
}

func TestModelPickerKeepsUndiscoveredProviderVisible(t *testing.T) {
	m := newTestWizard(t, Opts{
		Cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"ollama":       {Type: "ollama", BaseURL: "http://localhost:11434"},
			"ollama cloud": {Type: "ollama", BaseURL: "https://ollama.com"},
		}},
		Discovered:       map[string][]schema.ModelInfo{"ollama": {{ID: "qwen2.5-coder:7b"}}},
		SkipToIntroModel: true,
		AllProviders:     true,
		ScopedProvider:   "ollama",
	})
	view := m.View(80, 24)
	if !strings.Contains(view, "ollama cloud") {
		t.Errorf("provider with no discovered models should still appear:\n%s", view)
	}
	if !strings.Contains(view, "remote discovery disabled") {
		t.Errorf("remote provider gated by privacy should say why it is empty:\n%s", view)
	}
}

func TestManualRowPickScopesToItsProvider(t *testing.T) {
	m := newTestWizard(t, Opts{
		Cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"kimi":   {Type: "openai_compatible", BaseURL: "https://api.moonshot.cn/v1"},
			"ollama": {Type: "ollama", BaseURL: "http://localhost:11434"},
		}},
		Discovered:       map[string][]schema.ModelInfo{"ollama": {{ID: "qwen2.5-coder:7b"}}},
		SkipToIntroModel: true,
		AllProviders:     true,
		ScopedProvider:   "kimi",
	})
	updated, _ := m.Update(picker.PickedMsg{Value: encodeModelValue("kimi", "__manual__")})
	if updated.step != stepPickModel {
		t.Errorf("step = %v, manual row should stay on the picker", updated.step)
	}
	if updated.providerName != "kimi" {
		t.Errorf("providerName = %q, want scoped to kimi", updated.providerName)
	}
	// A typed-in id then binds to kimi, not the first sorted provider.
	updated, _ = updated.Update(picker.PickedMsg{Value: "kimi-k2"})
	if updated.modelChosen != "kimi-k2" {
		t.Errorf("modelChosen = %q, want kimi-k2", updated.modelChosen)
	}
	if updated.providerName != "kimi" {
		t.Errorf("providerName = %q, want kimi for the typed id", updated.providerName)
	}
}

func TestModelStepSubtitleNamesTheProviderWhenScoped(t *testing.T) {
	m := newTestWizard(t, Opts{
		Cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"openai": {Type: "openai_compatible", BaseURL: "https://a/v1"},
		}},
		Discovered:       map[string][]schema.ModelInfo{"openai": {{ID: "gpt-4o"}}},
		SkipToIntroModel: true,
		ScopedProvider:   "openai",
	})

	if m.subtitle != "openai" {
		t.Errorf("subtitle = %q, want the scoped provider name", m.subtitle)
	}
}

func TestRefreshKeyInactiveOutsideModelStep(t *testing.T) {
	m := newTestWizard(t, Opts{Cfg: config.Config{}}) // starts at stepPickTemplate
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if cmd != nil {
		if _, ok := cmd().(RefreshMsg); ok {
			t.Error("refresh should only be bound in the model step")
		}
	}
}

func TestLiteralKeyClearsTemplateEnvRef(t *testing.T) {
	m := &Model{
		step:        stepAPIKey,
		template:    acpwire.ProviderTemplate{ID: "openai", KeyEnv: "OPENAI_API_KEY"},
		providerCfg: config.ProviderConfig{Type: "openai_compatible", BaseURL: "https://api.openai.com", APIKeyEnv: "OPENAI_API_KEY"},
		input:       textfield.New(),
	}
	m.input.SetValue("sk-test-123")
	m.input.Focus()
	m.confirmInput()
	if m.providerCfg.APIKey != "sk-test-123" {
		t.Fatalf("APIKey = %q, want sk-test-123", m.providerCfg.APIKey)
	}
	if m.providerCfg.APIKeyEnv != "" {
		t.Fatalf("APIKeyEnv = %q, want empty after literal key entry", m.providerCfg.APIKeyEnv)
	}
}

func TestRenderInputTruncatesSafely(t *testing.T) {
	m := &Model{}
	m.input.SetValue("日本語テスト文字列abcdef")
	// Truncate to a small width that lands inside a multi-byte rune.
	result := m.renderInput(6)
	if !utf8.ValidString(result) {
		t.Fatalf("renderInput produced invalid UTF-8: %q", result)
	}
}

func TestRenderRenameInputTruncatesSafely(t *testing.T) {
	m := &Model{}
	m.renameInput.SetValue("日本語テスト文字列abcdef")
	result := m.renderRenameInput(6)
	if !utf8.ValidString(result) {
		t.Fatalf("renderRenameInput produced invalid UTF-8: %q", result)
	}
}

func TestDoneWithoutPreparedResultDoesNotPanic(t *testing.T) {
	m := &Model{} // no prepare has run, so prepared is the zero value
	cmd := m.done()
	if cmd == nil {
		t.Fatal("done() should return a tea.Cmd")
	}
	// Executing the returned closure must not dereference a nil prepared result.
	msg := cmd()
	if _, ok := msg.(DoneMsg); !ok {
		t.Fatalf("done() cmd returned %T, want DoneMsg", msg)
	}
}

func TestDoneCarriesProbedCaps(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))
	m, _ = m.Update(tea.KeyPressMsg{Code: 110}) // n
	toolCalling := true
	m, _ = m.Update(PreparedMsg{
		Provider: m.providerName,
		Caps:     acpwire.ModelCapabilities{ContextWindow: 32768, ToolCalling: &toolCalling},
		Result:   presetsetup.Result{Prepared: 1, Fallback: 1},
	})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 13})
	if cmd == nil {
		t.Fatal("Enter on summary should emit a DoneMsg cmd")
	}
	dm, ok := cmd().(DoneMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want DoneMsg", cmd())
	}
	if dm.ContextWindow != 32768 {
		t.Fatalf("DoneMsg.ContextWindow = %d, want 32768", dm.ContextWindow)
	}
	if dm.ToolCalling == nil || !*dm.ToolCalling {
		t.Fatalf("DoneMsg.ToolCalling = %v, want true", dm.ToolCalling)
	}
}

func TestPreparedMsgFromAnotherProviderIsIgnored(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))
	m, _ = m.Update(tea.KeyPressMsg{Code: 110}) // n
	if m.step != stepPreparing {
		t.Fatalf("step = %v, want stepPreparing", m.step)
	}
	m, _ = m.Update(PreparedMsg{Provider: "some-other-provider", Result: presetsetup.Result{Prepared: 1}})
	if m.step != stepPreparing {
		t.Fatalf("stale PreparedMsg moved the wizard to %v, want stepPreparing", m.step)
	}
}

func TestPrepareCmdDoesNotMutateLivePresetMap(t *testing.T) {
	cfg := config.Default()
	deny := false
	cfg.Privacy.ModelsDevEnrichment = &deny
	const name = "openai/gpt-4o"
	cfg.Models.Presets = map[string]routing.ModelPreset{
		name: {Name: name, Provider: "openai", Model: "gpt-4o", ContextWindow: 128000},
	}
	m := newTestWizard(t, Opts{
		Cfg: cfg,
		Discovered: map[string][]schema.ModelInfo{
			"openai": {{ID: "gpt-4o"}, {ID: "gpt-4o-mini"}},
		},
		SkipToIntroModel: true,
		ScopedProvider:   "openai",
		DataDir:          t.TempDir(),
	})
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))
	if m.step != stepPreparing {
		t.Fatalf("step = %v, want stepPreparing", m.step)
	}
	msg := m.prepareCmd()()
	pm, ok := msg.(PreparedMsg)
	if !ok {
		t.Fatalf("prepareCmd produced %T, want PreparedMsg", msg)
	}
	if len(pm.Presets) != 2 {
		t.Fatalf("prepared %d presets, want 2", len(pm.Presets))
	}
	// The wizard works on a copy: a cancelled connect must leave the live
	// config's preset map exactly as it was.
	if _, ok := cfg.Models.Presets["openai/gpt-4o-mini"]; ok {
		t.Fatal("prepareCmd added a preset to the live config")
	}
	if got := cfg.Models.Presets[name].ContextWindow; got != 128000 {
		t.Fatalf("live preset ContextWindow = %d, want 128000", got)
	}
}

func TestPrepareCmdSkipsCatalogWhenDenied(t *testing.T) {
	cfg := config.Default()
	deny := false
	cfg.Privacy.ModelsDevEnrichment = &deny
	dataDir := t.TempDir()
	m := newTestWizard(t, Opts{
		Cfg: cfg,
		Discovered: map[string][]schema.ModelInfo{
			"openai": {{ID: "gpt-4o"}},
		},
		SkipToIntroModel: true,
		ScopedProvider:   "openai",
		DataDir:          dataDir,
	})
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))
	if m.step != stepPreparing {
		t.Fatalf("step = %v, want stepPreparing", m.step)
	}
	msg := m.prepareCmd()()
	pm, ok := msg.(PreparedMsg)
	if !ok {
		t.Fatalf("prepareCmd produced %T, want PreparedMsg", msg)
	}
	if pm.ProvenanceErr != nil {
		t.Fatalf("ProvenanceErr = %v, want nil for a missing sidecar", pm.ProvenanceErr)
	}
	// A denied consent must not touch the network, so no catalog cache is
	// written.
	if _, err := os.Stat(filepath.Join(dataDir, "models-dev", "catalog.json")); err == nil {
		t.Fatal("denied consent still fetched the models.dev catalog")
	}
}

// ── Auth/browser dependency injection ────────────────────────────────
//
// Every test below supplies an isolated resolver built on an in-memory
// store and a stub browser opener, so a default `go test ./...` run opens
// neither the developer's real OS keychain nor a real browser.

// memAuth is a test AuthResolver: a private in-memory token store plus
// whatever CodexOAuthEngine behavior the test wants (real-with-store, or a
// failure that stands in for an unavailable keychain).
//
// Engines are cached per provider name, mirroring provider.scopedAuthResolver:
// the wizard wires Open on the engine it receives, and a caller that asks
// again must get that same engine for the wiring to be meaningful.
type memAuth struct {
	store *credentials.MemStore
	// engineErr, when set, is returned instead of building an engine.
	engineErr error
	// engines records the provider names the wizard asked an engine for.
	engines []string
	cache   map[string]*oauth.Engine
}

func newMemAuth() *memAuth {
	return &memAuth{store: credentials.NewMemStore(), cache: map[string]*oauth.Engine{}}
}

func (a *memAuth) OpenStore(string) (credentials.Store, error) {
	if a.store == nil {
		return nil, credentials.ErrKeyringUnavailable
	}
	return a.store, nil
}

func (a *memAuth) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	a.engines = append(a.engines, name)
	if a.engineErr != nil {
		return nil, a.engineErr
	}
	if a.store == nil {
		// Return the failure explicitly. Handing a typed-nil *MemStore to the
		// provider constructor would pass its `store == nil` check (the
		// interface is non-nil) and produce a store that panics on first use.
		return nil, fmt.Errorf("openai_codex: OAuth token store is required: %w", credentials.ErrKeyringUnavailable)
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

// stubOpener records the URLs a login flow asked to open instead of
// launching a real browser. The engine calls the opener from its own
// goroutine while the test polls, so access is mutex-guarded for -race.
type stubOpener struct {
	mu   sync.Mutex
	urls []string
}

func (o *stubOpener) open(url string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.urls = append(o.urls, url)
	return nil
}

func (o *stubOpener) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.urls...)
}

// oauthTestModel returns a wizard wired to isolated auth/browser dependencies
// and positioned on the OAuth sign-in step for a codex provider.
func oauthTestModel(t *testing.T, auth provider.AuthResolver) (*Model, *stubOpener) {
	t.Helper()
	if auth == nil {
		auth = newMemAuth()
	}
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	opener := &stubOpener{}
	m := newTestWizard(t, Opts{Cfg: cfg, Auth: auth, OpenBrowser: opener.open})
	updated, _ := m.Update(pickerPicked("openai-codex"))
	if updated.step != stepOAuthLogin {
		t.Fatalf("step = %v, want stepOAuthLogin", updated.step)
	}
	return updated, opener
}

// TestNewInjectsAuthDependency pins step 1: a supplied resolver is retained
// by the wizard, and an omitted one selects the production adapter rather
// than leaving the field nil (which would panic on first probe).
func TestNewInjectsAuthDependency(t *testing.T) {
	auth := newMemAuth()
	m := newTestWizard(t, Opts{Cfg: config.Default(), Auth: auth})
	if m.auth != provider.AuthResolver(auth) {
		t.Fatalf("auth = %v, want the injected resolver", m.auth)
	}

	plain := newTestWizard(t, Opts{Cfg: config.Default()})
	if plain.auth == nil {
		t.Fatal("an omitted resolver must select the production adapter, not nil")
	}
	if plain.openBrowser == nil {
		t.Fatal("an omitted browser opener must select mcpauth.OpenBrowser, not nil")
	}
}

// TestEnterOAuthLoginUsesInjectedResolver: picking the codex template must
// build the sign-in engine from the supplied resolver. Reaching the
// process-wide engine instead would touch the developer's real keychain.
func TestEnterOAuthLoginUsesInjectedResolver(t *testing.T) {
	auth := newMemAuth()
	m, _ := oauthTestModel(t, auth)
	if len(auth.engines) != 1 {
		t.Fatalf("engine requests = %v, want exactly one for the picked provider", auth.engines)
	}
	if auth.engines[0] != m.providerName {
		t.Fatalf("engine requested for %q, want %q", auth.engines[0], m.providerName)
	}
	if m.err != "" {
		t.Fatalf("injected resolver should succeed, got err %q", m.err)
	}
}

// TestEnterOAuthLoginSurfacesResolverError: when the resolver cannot build an
// engine (its store is unavailable) the failure must surface on the panel —
// and must not fall back to the process-wide engine.
func TestEnterOAuthLoginSurfacesResolverError(t *testing.T) {
	auth := newMemAuth()
	auth.engineErr = fmt.Errorf("OS keychain unavailable — OAuth tokens cannot be stored: %w", credentials.ErrKeyringUnavailable)
	m, opener := oauthTestModel(t, auth)

	if m.step != stepOAuthLogin {
		t.Fatalf("step = %v, want stepOAuthLogin", m.step)
	}
	if !strings.Contains(m.err, "keychain unavailable") {
		t.Fatalf("err = %q, want the resolver failure surfaced", m.err)
	}
	if len(auth.engines) != 1 {
		t.Fatalf("engine requests = %v, want the failing resolver to be the only source", auth.engines)
	}
	if got := opener.snapshot(); len(got) != 0 {
		t.Fatalf("no browser should open when the engine is unavailable, got %v", got)
	}
}

// TestOAuthLoginWithUnavailableStoreFailsClosed: a resolver that reports an
// unavailable store must produce an error, never a silent switch to the
// production keychain.
func TestOAuthLoginWithUnavailableStoreFailsClosed(t *testing.T) {
	auth := &memAuth{store: nil} // OpenStore reports ErrKeyringUnavailable
	m := newTestWizard(t, Opts{Cfg: config.Default(), Auth: auth, OpenBrowser: func(string) error { return nil }})
	m.providerName = "codex-nil-store"
	updated, cmd := m.enterOAuthLogin()

	if cmd != nil {
		t.Fatal("no login command should start when the store is unavailable")
	}
	if updated.err == "" {
		t.Fatal("a failed engine build must surface an error")
	}
}

// TestOAuthLoginCommandRunsAgainstTestOwnedServer is the executed-command
// test the plan asks for: the real login Cmd runs through a test-owned
// authorization server, and the assertion is on the URL handed to the
// injected opener — no browser, no real keychain, and no contact with
// auth.openai.com.
func TestOAuthLoginCommandRunsAgainstTestOwnedServer(t *testing.T) {
	var tokenCalls int
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenCalls++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"test-at","refresh_token":"test-rt","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer tokenSrv.Close()

	store := credentials.NewMemStore()
	engine := &oauth.Engine{
		ServerURL:  "https://auth.test.invalid",
		Store:      store,
		Open:       nil, // oauthLogin wires it
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

	opener := &stubOpener{}
	display := &oauthDisplay{}
	cmd := oauthLogin(engine, "codex-test", display, opener.open)
	if engine.Open == nil {
		t.Fatal("oauthLogin must wire the engine's Open hook")
	}

	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	// Drive the loopback callback the way a browser would.
	var callback string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		callback = display.URL()
		if callback != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if callback == "" {
		t.Fatal("the flow never produced an authorization URL")
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
	case msg := <-done:
		oauthMsg, ok := msg.(OAuthDoneMsg)
		if !ok {
			t.Fatalf("cmd returned %T, want OAuthDoneMsg", msg)
		}
		if oauthMsg.Err != nil {
			t.Fatalf("authorize: %v", oauthMsg.Err)
		}
		if oauthMsg.Provider != "codex-test" {
			t.Fatalf("provider = %q, want codex-test", oauthMsg.Provider)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("login command did not return")
	}

	// The assertion the plan asks for: the opener saw the authorization URL.
	urls := opener.snapshot()
	if len(urls) != 1 {
		t.Fatalf("opener calls = %v, want exactly one", urls)
	}
	if urls[0] != callback {
		t.Fatalf("opened %q, want %q", urls[0], callback)
	}
	// The token store is the test's own, and no real keychain was touched.
	if tokenCalls != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", tokenCalls)
	}
	if _, err := store.Get("marshal:provider:codex-test"); err != nil {
		t.Fatalf("tokens should be persisted in the injected store: %v", err)
	}
}

// TestOAuthLoginNilOpenerFallsBackToProduction pins the production default:
// oauthLogin must still wire the real launcher when a caller passes none.
func TestOAuthLoginNilOpenerFallsBackToProduction(t *testing.T) {
	engine := &oauth.Engine{ServerURL: "https://auth.test.invalid", Store: credentials.NewMemStore()}
	cmd := oauthLogin(engine, "codex", &oauthDisplay{}, nil)
	if cmd == nil {
		t.Fatal("oauthLogin should return a command")
	}
	if engine.Open == nil {
		t.Fatal("a nil opener must fall back to mcpauth.OpenBrowser, not leave Open unset")
	}
}

// ── Discovery arriving while the model picker is open ────────────────
//
// /models probes run in the parent model and arrive here as a
// probe.ResultMsg with FieldID "models". They may refresh the all-provider
// model list, but they must never move the user's choice.

// newDiscoveryModel builds an all-providers picker whose two providers offer
// the same model id, so a selection is only identifiable by provider *and*
// model. Rows: groq/llama-3.3-70b, groq/llama-3.3-70b-versatile,
// openai/gpt-4o, openai/llama-3.3-70b.
func newDiscoveryModel(t *testing.T) *Model {
	t.Helper()
	m := newTestWizard(t, Opts{
		Cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"groq":   {Type: "openai_compatible", BaseURL: "https://api.groq.com/openai/v1"},
			"openai": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1"},
		}},
		Discovered: map[string][]schema.ModelInfo{
			"groq":   {{ID: "llama-3.3-70b"}, {ID: "llama-3.3-70b-versatile"}},
			"openai": {{ID: "gpt-4o"}, {ID: "llama-3.3-70b"}},
		},
		SkipToIntroModel: true,
		AllProviders:     true,
		ScopedProvider:   "groq",
	})
	if m.step != stepPickModel {
		t.Fatalf("newDiscoveryModel: step = %v, want stepPickModel", m.step)
	}
	return m
}

// pressKeys drives the wizard through real key presses, the way the docked
// panel is driven.
func pressKeys(m *Model, keys ...tea.KeyPressMsg) *Model {
	for _, k := range keys {
		m, _ = m.Update(k)
	}
	return m
}

// typeFilter types text into the picker's filter box through the Update path.
func typeFilter(m *Model, text string) *Model {
	keys := make([]tea.KeyPressMsg, 0, len(text))
	for _, r := range text {
		keys = append(keys, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return pressKeys(m, keys...)
}

// deliverProbe feeds one discovery result through the Update path.
func deliverProbe(m *Model, msg probe.ResultMsg) *Model {
	m, _ = m.Update(msg)
	return m
}

// enterCmd runs the command an Enter press produced and applies its message,
// so a pick travels the same path the docked panel would take.
func enterCmd(t *testing.T, m *Model) (*Model, tea.Msg) {
	t.Helper()
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		return m, nil
	}
	msg := cmd()
	m, _ = m.Update(msg)
	return m, msg
}

// wizardState is a value snapshot of the state a stale probe result could
// move. Update mutates the wizard in place and returns the same pointer, so a
// "before" copy must be values, not a pointer.
type wizardState struct {
	step       step
	err        string
	chosen     string
	discovered int
	models     string
	pickerNil  bool
	pickerRows int
	filter     string
	pickerErr  string
}

func snapshotWizard(m *Model) wizardState {
	s := wizardState{
		step:       m.step,
		err:        m.err,
		chosen:     m.modelChosen,
		discovered: len(m.discovered["ollama"]),
		pickerNil:  m.picker == nil,
	}
	ids := make([]string, len(m.models))
	for i, mi := range m.models {
		ids[i] = mi.ID
	}
	s.models = strings.Join(ids, ",")
	if m.picker != nil {
		s.pickerRows = len(m.picker.Items())
		s.filter = m.picker.FilterValue()
		s.pickerErr = m.picker.ErrMsg()
	}
	return s
}

func assertWizardUnchanged(t *testing.T, want, got wizardState, label string) {
	t.Helper()
	if got.step != want.step {
		t.Errorf("%s: step = %v, want unchanged %v", label, got.step, want.step)
	}
	if got.err != want.err {
		t.Errorf("%s: err = %q, want unchanged %q", label, got.err, want.err)
	}
	if got.chosen != want.chosen {
		t.Errorf("%s: chosen model = %q, want unchanged %q", label, got.chosen, want.chosen)
	}
	if got.discovered != want.discovered {
		t.Errorf("%s: discovered entries = %d, want unchanged %d", label, got.discovered, want.discovered)
	}
	if got.models != want.models {
		t.Errorf("%s: models = %q, want unchanged %q", label, got.models, want.models)
	}
	if got.pickerNil != want.pickerNil {
		t.Errorf("%s: picker presence changed", label)
	}
	if got.pickerRows != want.pickerRows {
		t.Errorf("%s: picker rows = %d, want unchanged %d", label, got.pickerRows, want.pickerRows)
	}
	if got.filter != want.filter {
		t.Errorf("%s: filter = %q, want unchanged %q", label, got.filter, want.filter)
	}
	if got.pickerErr != want.pickerErr {
		t.Errorf("%s: picker error = %q, want unchanged %q", label, got.pickerErr, want.pickerErr)
	}
}

func TestSetupProbeRejectsSkippedAndSupersededResults(t *testing.T) {
	// newProbingWizard lands on the setup-probing step with the ollama
	// template. Its probe command is never executed here: results are
	// delivered by hand so the test can hold two attempts at once.
	newProbingWizard := func(t *testing.T) *Model {
		t.Helper()
		m := newTestWizard(t, Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
		m, _ = m.Update(pickerPicked("ollama"))
		if m.step != stepProbing {
			t.Fatalf("step = %v, want stepProbing", m.step)
		}
		return m
	}
	// staleSuccess and staleFailure are built against the wizard's *current*
	// generation; the test then moves the wizard on before delivering them.
	staleSuccess := func(m *Model) WizardProbeResult {
		return wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "stale-model"}}})
	}
	staleFailure := func(m *Model) WizardProbeResult {
		return wizardResult(m, probe.ResultMsg{Provider: m.providerName, Err: errors.New("stale failure")})
	}

	t.Run("skipped probe", func(t *testing.T) {
		m := newProbingWizard(t)
		oldSuccess, oldFailure := staleSuccess(m), staleFailure(m)
		if !m.AcceptsWizardProbe(oldSuccess) {
			t.Fatal("precondition: the result matches the wizard before it is skipped")
		}

		m = pressKeys(m, tea.KeyPressMsg{Code: 115}) // s = skip
		if m.step != stepPickModel {
			t.Fatalf("skip: step = %v, want stepPickModel", m.step)
		}
		before := snapshotWizard(m)
		m, _ = m.Update(oldSuccess)
		assertWizardUnchanged(t, before, snapshotWizard(m), "stale success after skip")
		m, _ = m.Update(oldFailure)
		assertWizardUnchanged(t, before, snapshotWizard(m), "stale failure after skip")
	})

	t.Run("superseded by retry", func(t *testing.T) {
		m := newProbingWizard(t)
		oldSuccess, oldFailure := staleSuccess(m), staleFailure(m)

		m = pressKeys(m, tea.KeyPressMsg{Code: 114}) // r = retry
		if m.step != stepProbing {
			t.Fatalf("retry: step = %v, want stepProbing", m.step)
		}
		if m.AcceptsWizardProbe(oldSuccess) {
			t.Fatal("the superseded attempt must no longer be accepted")
		}
		before := snapshotWizard(m)
		m, _ = m.Update(oldSuccess)
		assertWizardUnchanged(t, before, snapshotWizard(m), "superseded success")
		m, _ = m.Update(oldFailure)
		assertWizardUnchanged(t, before, snapshotWizard(m), "superseded failure")

		// The retry's own result is the current attempt and lands normally.
		m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
		if m.step != stepPickModel {
			t.Fatalf("current retry: step = %v, want stepPickModel", m.step)
		}
		if len(m.models) != 1 || m.models[0].ID != "qwen2.5-coder:7b" {
			t.Fatalf("current retry: models = %v, want the retried model", m.models)
		}
		if got := m.discovered[m.providerName]; len(got) != 1 || got[0].ID != "qwen2.5-coder:7b" {
			t.Fatalf("current retry: discovered = %v, want the retried model", got)
		}
	})

	t.Run("after a later wizard step", func(t *testing.T) {
		// models.dev enrichment is denied so picking a model goes straight to
		// preparation instead of stopping at the consent step; the stale
		// result is then delivered at the summary, well past model selection.
		cfg := config.Default()
		deny := false
		cfg.Privacy.ModelsDevEnrichment = &deny
		m := newTestWizard(t, Opts{Cfg: cfg, Discovered: map[string][]schema.ModelInfo{}})
		m, _ = m.Update(pickerPicked("ollama"))
		if m.step != stepProbing {
			t.Fatalf("step = %v, want stepProbing", m.step)
		}
		oldSuccess, oldFailure := staleSuccess(m), staleFailure(m)

		// Land a normal probe, pick its model and prepare presets, so the
		// wizard sits well past both probing and model selection.
		m, _ = m.Update(wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}}))
		m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
		m, _ = m.Update(PreparedMsg{
			Provider: m.providerName,
			Presets: map[string]routing.ModelPreset{
				"ollama/qwen2.5-coder:7b": {Name: "ollama/qwen2.5-coder:7b", Provider: "ollama", Model: "qwen2.5-coder:7b"},
			},
			Result: presetsetup.Result{Prepared: 1, Fallback: 1},
		})
		if m.step != stepSummary {
			t.Fatalf("step = %v, want stepSummary", m.step)
		}
		before := snapshotWizard(m)
		m, _ = m.Update(oldSuccess)
		assertWizardUnchanged(t, before, snapshotWizard(m), "stale success at summary")
		m, _ = m.Update(oldFailure)
		assertWizardUnchanged(t, before, snapshotWizard(m), "stale failure at summary")
	})

	t.Run("replaced wizard", func(t *testing.T) {
		first := newProbingWizard(t)
		second := newProbingWizard(t)
		// Both instances sit at stepProbing on the same generation, so only
		// the owner identity tells their results apart.
		before := snapshotWizard(second)
		second, _ = second.Update(wizardResult(first, probe.ResultMsg{Provider: first.providerName, Models: []schema.ModelInfo{{ID: "stale-model"}}}))
		assertWizardUnchanged(t, before, snapshotWizard(second), "replaced wizard's result")
	})

	t.Run("other provider", func(t *testing.T) {
		m := newProbingWizard(t)
		// Right instance and generation, wrong provider: the result belongs to
		// a different wizard's probe and must not touch this one.
		wr := wizardResult(m, probe.ResultMsg{Provider: "someone-else", Models: []schema.ModelInfo{{ID: "elsewhere"}}})
		if m.AcceptsWizardProbe(wr) {
			t.Fatal("a result naming another provider must be rejected")
		}
		before := snapshotWizard(m)
		m, _ = m.Update(wr)
		assertWizardUnchanged(t, before, snapshotWizard(m), "other provider's result")
	})

	t.Run("cancelled wizard", func(t *testing.T) {
		// Esc at the probing step abandons the wizard; the probe it launched
		// is still running and must not land afterwards.
		m := newProbingWizard(t)
		old := wizardResult(m, probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "stale-model"}}})
		if !m.AcceptsWizardProbe(old) {
			t.Fatal("precondition: the result matches the wizard before it is cancelled")
		}
		m = pressKeys(m, tea.KeyPressMsg{Code: tea.KeyEscape})
		if m.step != stepCancelled {
			t.Fatalf("esc: step = %v, want stepCancelled", m.step)
		}
		if m.AcceptsWizardProbe(old) {
			t.Fatal("a cancelled wizard must reject its in-flight probe")
		}
		before := snapshotWizard(m)
		m, _ = m.Update(old)
		assertWizardUnchanged(t, before, snapshotWizard(m), "result after cancel")
	})
}

func TestDiscoveryKeepsSelectedProviderModel(t *testing.T) {
	t.Run("a model row under a nonempty filter", func(t *testing.T) {
		m := newDiscoveryModel(t)
		// Typing a filter inserts the synthetic custom row first, so the
		// matches are [custom, groq/llama-3.3-70b,
		// groq/llama-3.3-70b-versatile, openai/llama-3.3-70b]. Three Downs
		// land on openai's row, which shares its model id with groq's row.
		m = typeFilter(m, "llama-3.3-70b")
		m = pressKeys(m,
			tea.KeyPressMsg{Code: tea.KeyDown},
			tea.KeyPressMsg{Code: tea.KeyDown},
			tea.KeyPressMsg{Code: tea.KeyDown})
		want := encodeModelValue("openai", "llama-3.3-70b")

		// Another provider's success, then its failure, arrive while the
		// picker is open.
		m = deliverProbe(m, probe.ResultMsg{
			FieldID:  parentModelProbeFieldID,
			Provider: "groq",
			Models:   []schema.ModelInfo{{ID: "llama-3.3-70b"}, {ID: "llama-3.3-70b-versatile"}, {ID: "mixtral-8x7b"}},
		})
		m = deliverProbe(m, probe.ResultMsg{
			FieldID:  parentModelProbeFieldID,
			Provider: "groq",
			Err:      errors.New("connection refused"),
		})

		if got := m.picker.FilterValue(); got != "llama-3.3-70b" {
			t.Fatalf("filter = %q, want it preserved across discoveries", got)
		}
		_, msg := enterCmd(t, m)
		picked, ok := msg.(picker.PickedMsg)
		if !ok {
			t.Fatalf("Enter produced %T, want picker.PickedMsg", msg)
		}
		if picked.Value != want {
			t.Fatalf("picked %q, want the originally selected %q", picked.Value, want)
		}
	})

	t.Run("the custom row", func(t *testing.T) {
		m := newDiscoveryModel(t)
		m = typeFilter(m, "brand-new-model")
		// Nothing matches yet, so the custom row is the only choice and the
		// cursor sits on it. When a provider then reports a model with
		// exactly that id, the row appears — and the custom selection must
		// still be the one Enter returns, not the new model row.
		m = deliverProbe(m, probe.ResultMsg{
			FieldID:  parentModelProbeFieldID,
			Provider: "groq",
			Models:   []schema.ModelInfo{{ID: "brand-new-model"}},
		})

		_, msg := enterCmd(t, m)
		picked, ok := msg.(picker.PickedMsg)
		if !ok {
			t.Fatalf("Enter produced %T, want picker.PickedMsg", msg)
		}
		if picked.Value != "brand-new-model" {
			t.Fatalf("picked %q, want the typed custom value %q", picked.Value, "brand-new-model")
		}
	})
}

func TestDiscoveryRemovedSelectionRequiresChoice(t *testing.T) {
	const missing = "Selected model is no longer available; choose another"

	m := newDiscoveryModel(t)
	// Select the last row: openai/llama-3.3-70b.
	m = pressKeys(m,
		tea.KeyPressMsg{Code: tea.KeyDown},
		tea.KeyPressMsg{Code: tea.KeyDown},
		tea.KeyPressMsg{Code: tea.KeyDown})

	// openai no longer offers that model: the chosen row is gone.
	m = deliverProbe(m, probe.ResultMsg{
		FieldID:  parentModelProbeFieldID,
		Provider: "openai",
		Models:   []schema.ModelInfo{{ID: "gpt-4o"}},
	})
	if got := m.picker.ErrMsg(); got != missing {
		t.Fatalf("picker error = %q, want %q", got, missing)
	}
	if view := ansi.Strip(m.View(80, 24)); !strings.Contains(view, missing) {
		t.Fatalf("the picker must show the missing-selection message:\n%s", view)
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatalf("Enter after the selection vanished must do nothing, got %T", cmd())
	}

	// A second result arriving before the user chooses must not select a row
	// on their behalf either.
	m = deliverProbe(m, probe.ResultMsg{
		FieldID:  parentModelProbeFieldID,
		Provider: "groq",
		Models:   []schema.ModelInfo{{ID: "llama-3.3-70b"}, {ID: "mixtral-8x7b"}},
	})
	if got := m.picker.ErrMsg(); got != missing {
		t.Fatalf("a second result changed the message to %q, want %q", got, missing)
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatalf("Enter before the user picks again must do nothing, got %T", cmd())
	}

	// Explicit recovery: an arrow re-selects the first available row.
	m = pressKeys(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.picker.ErrMsg(); got != "" {
		t.Fatalf("picker error = %q, want it cleared once the user moved", got)
	}
	_, msg := enterCmd(t, m)
	picked, ok := msg.(picker.PickedMsg)
	if !ok {
		t.Fatalf("Enter after an explicit choice produced %T, want picker.PickedMsg", msg)
	}
	if want := encodeModelValue("groq", "llama-3.3-70b"); picked.Value != want {
		t.Fatalf("picked %q, want %q", picked.Value, want)
	}
}

func TestAllProviderResultDoesNotReopenLaterWizardStep(t *testing.T) {
	m := newDiscoveryModel(t)
	// Enter on the first row accepts groq/llama-3.3-70b and leaves the picker
	// for the consent step.
	m, msg := enterCmd(t, m)
	if _, ok := msg.(picker.PickedMsg); !ok {
		t.Fatalf("Enter on the picker produced %T, want picker.PickedMsg", msg)
	}
	if m.step != stepConsent {
		t.Fatalf("step = %v, want stepConsent after the pick", m.step)
	}
	if m.picker != nil {
		t.Fatal("the picker should be gone once the selection advanced")
	}

	// A live success for another provider must not reopen the picker, move
	// the wizard back, or touch the discovery state behind the closed panel.
	m = deliverProbe(m, probe.ResultMsg{
		FieldID:  parentModelProbeFieldID,
		Provider: "openai",
		Models:   []schema.ModelInfo{{ID: "gpt-4o-mini"}},
	})
	if m.step != stepConsent {
		t.Fatalf("success moved the wizard to %v, want stepConsent", m.step)
	}
	if m.picker != nil {
		t.Fatal("success reopened the model picker")
	}
	if got := m.discovered["openai"]; len(got) != 2 {
		t.Fatalf("discovered[openai] = %v, want the pre-existing two models", got)
	}
	if m.err != "" {
		t.Fatalf("err = %q, want empty", m.err)
	}

	// Advance to the summary, then deliver a failure.
	m = pressKeys(m, tea.KeyPressMsg{Code: 110}) // n: skip models.dev
	if m.step != stepPreparing {
		t.Fatalf("step = %v, want stepPreparing", m.step)
	}
	m, _ = m.Update(PreparedMsg{Provider: m.providerName, Result: presetsetup.Result{Prepared: 1, Fallback: 1}})
	if m.step != stepSummary {
		t.Fatalf("step = %v, want stepSummary", m.step)
	}

	m = deliverProbe(m, probe.ResultMsg{
		FieldID:  parentModelProbeFieldID,
		Provider: "openai",
		Err:      errors.New("401 unauthorized"),
	})
	if m.step != stepSummary {
		t.Fatalf("failure moved the wizard to %v, want stepSummary", m.step)
	}
	if m.picker != nil {
		t.Fatal("failure reopened the model picker")
	}
	if m.err != "" {
		t.Fatalf("err = %q, want a discovery failure to stay out of the step's error line", m.err)
	}
	if _, ok := m.probeErrs["openai"]; ok {
		t.Fatal("a discovery failure must not be recorded as a setup probe failure")
	}
}
