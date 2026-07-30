package connect

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"charm.land/bubbles/v2/textinput"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/probe"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/strutil"
)

func TestPanelSatisfiesDock(t *testing.T) {
	var _ dock.Panel = Panel{}
}

func TestNewStartsAtPickTemplate(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	if m.step != stepPickTemplate {
		t.Fatalf("step = %v, want stepPickTemplate", m.step)
	}
	if m.title == "" {
		t.Fatal("title must be set for the pickTemplate step")
	}
}

func TestNewScopedProviderStartsAtPickModel(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), SkipToIntroModel: true, ScopedProvider: "ollama"})
	if m.step != stepPickModel {
		t.Fatalf("step = %v, want stepPickModel", m.step)
	}
}

func TestEscAtPickTemplateEmitsCancelled(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
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
	m := New(Opts{Cfg: config.Default()})
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
	m := New(Opts{Cfg: cfg})
	updated, _ := m.Update(pickerPicked("openrouter"))
	if updated.step != stepAPIKey {
		t.Fatalf("remote template should enter apiKey, got step = %v", updated.step)
	}
	if updated.template.KeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("KeyEnv = %q", updated.template.KeyEnv)
	}
}

func TestPickCustomEntersBaseURL(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	updated, _ := m.Update(pickerPicked("custom"))
	if updated.step != stepBaseURL {
		t.Fatalf("custom should enter baseURL, got step = %v", updated.step)
	}
}

func TestAPIKeyEnterAdvancesToProbing(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := New(Opts{Cfg: cfg})
	m, _ = m.Update(pickerPicked("openrouter"))
	m.input.SetValue("sk-test-1234")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepProbing {
		t.Fatalf("apiKey Enter should advance to probing, got step = %v", updated.step)
	}
	if updated.providerCfg.APIKey != "sk-test-1234" {
		t.Fatalf("api key not captured: %q", updated.providerCfg.APIKey)
	}
	if updated.providerCfg.APIKeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("api_key_env not set from template: %q", updated.providerCfg.APIKeyEnv)
	}
}

func TestCustomBaseURLThenKey(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := New(Opts{Cfg: cfg})
	m, _ = m.Update(pickerPicked("custom"))
	m.input.SetValue("https://myhost/v1")
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
	if m.step != stepAPIKey {
		t.Fatalf("after baseURL should be apiKey, got %v", m.step)
	}
	if m.providerCfg.BaseURL != "https://myhost/v1" {
		t.Fatalf("base_url not captured: %q", m.providerCfg.BaseURL)
	}
	m.input.SetValue("sk-x")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepProbing {
		t.Fatalf("after apiKey should be probing, got %v", updated.step)
	}
}

func TestProbeSuccessAdvancesToPickModel(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}, {ID: "llama3.1:8b"}}})
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
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(probe.ResultMsg{Provider: m.providerName, Err: errors.New("connection refused")})
	if updated.step != stepProbing {
		t.Fatalf("failure should stay probing, got %v", updated.step)
	}
	if updated.err == "" {
		t.Fatal("expected inline error text set")
	}
}

func TestRetryReRunsProbe(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Err: errors.New("boom")})
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 114})
	if updated.step != stepProbing {
		t.Fatalf("retry should stay probing, got %v", updated.step)
	}
	if cmd == nil {
		t.Fatal("retry should re-arm the probe cmd")
	}
}

func TestSkipProbeUsesCatalogAndAdvances(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
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
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}})
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	// Non-scoped flow lands on confirm limits; press Enter to advance to summary.
	if m.step != stepConfirmLimits {
		t.Fatalf("after pickModel should be stepConfirmLimits, got %v", m.step)
	}
	// Press Enter on confirm limits to advance to summary.
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
	if m.step != stepSummary {
		t.Fatalf("after confirm limits Enter should be stepSummary, got %v", m.step)
	}
	// Press Enter on summary to emit DoneMsg.
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

func TestPickTemplateKeyForwardedToPicker(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
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
	m := New(Opts{Cfg: config.Default(), SkipToIntroModel: true, ScopedProvider: "ollama"})
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
	m := New(Opts{Cfg: cfg})
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
	m := New(Opts{Cfg: config.Default()})
	m, _ = m.Update(pickerPicked("custom"))
	if m.step != stepBaseURL {
		t.Fatalf("expected stepBaseURL, got %v", m.step)
	}
	updated, _ := m.Update(tea.PasteMsg{Content: "https://example.com/v1"})
	if got := updated.input.Value(); got != "https://example.com/v1" {
		t.Fatalf("input.Value() = %q, want %q", got, "https://example.com/v1")
	}
}

// TestTickThrottlesSpinner pins the tick delay: an immediate tick is a
// busy loop that spins a core for the duration of every probe.
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
	m := New(Opts{Cfg: config.Default()})
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
	m := New(Opts{Cfg: cfg})
	updated, _ := m.Update(pickerPicked("openrouter"))
	if updated.step != stepAPIKey {
		t.Fatalf("remote template should skip gate and enter apiKey, got step = %v", updated.step)
	}
}

func TestRemoteGateEscGoesBackToTemplates(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
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
	m := New(Opts{Cfg: config.Default()})
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
	// Simulate: pick remote template -> gate -> y -> apiKey -> enter -> probe -> pick model -> confirm limits -> summary enter
	cfg := config.Default()
	m := New(Opts{Cfg: cfg, Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("openrouter"))
	m, _ = m.Update(tea.KeyPressMsg{Code: 121}) // y
	m.input.SetValue("sk-test")
	m, _ = m.Update(tea.KeyPressMsg{Code: 13}) // enter -> probing
	// Skip probe
	m, _ = m.Update(tea.KeyPressMsg{Code: 115}) // s -> skip
	// Pick a model -> lands on confirm limits
	m, _ = m.Update(pickerPicked("gpt-4o"))
	if m.step != stepConfirmLimits {
		t.Fatalf("after pickModel should be stepConfirmLimits, got %v", m.step)
	}
	// Press Enter on confirm limits to advance to summary
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
	if m.step != stepSummary {
		t.Fatalf("after confirm limits should be stepSummary, got %v", m.step)
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
	m := New(Opts{Cfg: config.Default()})
	m, _ = m.Update(pickerPicked("custom"))
	m.input.SetValue("https://remotehost/v1")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepRemoteGate {
		t.Fatalf("non-localhost baseURL should land on stepRemoteGate, got step = %v", updated.step)
	}
}

func TestCustomLocalhostBaseURLSkipsGate(t *testing.T) {
	m := New(Opts{Cfg: config.Default()})
	m, _ = m.Update(pickerPicked("custom"))
	m.input.SetValue("http://localhost:11434/v1")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 13})
	if updated.step != stepAPIKey {
		t.Fatalf("localhost baseURL should skip gate and enter apiKey, got step = %v", updated.step)
	}
}

func TestSummaryShowsNameModelAndDestination(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}, CfgPath: "/tmp/.marshal/config.toml"})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}})
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	// Confirm limits then land on summary.
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
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
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}})
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	// Confirm limits then land on summary.
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
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
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}})
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	// Confirm limits then land on summary.
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
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

func TestScopedModelSwitchGoesThroughConfirmLimits(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), SkipToIntroModel: true, ScopedProvider: "ollama", Discovered: map[string][]schema.ModelInfo{}})
	if m.step != stepPickModel {
		t.Fatalf("expected stepPickModel, got %v", m.step)
	}
	// Picking a model now lands on confirm limits, not stepDone.
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	if m.step != stepConfirmLimits {
		t.Fatalf("expected stepConfirmLimits, got %v", m.step)
	}
	// Press Enter on confirm limits to emit DoneMsg.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 13})
	if cmd == nil {
		t.Fatal("Enter on confirm limits should emit a DoneMsg cmd")
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
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}})
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	// Confirm limits then land on summary.
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
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
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}})
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	// Confirm limits then land on summary.
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
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
	m := New(Opts{Cfg: cfg, Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}})
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	// Confirm limits then land on summary.
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
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
	m := New(Opts{Cfg: cfg, Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}})
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	// Confirm limits then land on summary.
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
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
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}})
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	// Confirm limits then land on summary.
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
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
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	m, _ = m.Update(probe.ResultMsg{Provider: m.providerName, Models: []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}})
	m, _ = m.Update(pickerPicked("qwen2.5-coder:7b"))
	// Confirm limits then land on summary.
	m, _ = m.Update(tea.KeyPressMsg{Code: 13})
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

// newConnectForModelPick builds a Model at stepPickModel with a discovered
// model that has known limits, ready to test the confirm-limits step.
func newConnectForModelPick(t *testing.T) *Model {
	t.Helper()
	m := New(Opts{
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

func TestPickingAModelEntersConfirmLimits(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))

	if m.step != stepConfirmLimits {
		t.Fatalf("step = %v, want stepConfirmLimits", m.step)
	}
	if m.limits.ContextWindow != 128000 {
		t.Errorf("ContextWindow = %d, want the discovered figure", m.limits.ContextWindow)
	}
}

func TestConfirmLimitsReachedFromScopedModelsFlow(t *testing.T) {
	m := newConnectForModelPick(t)
	m.scopedProvider = "openai" // the /models flow
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))

	if m.step != stepConfirmLimits {
		t.Fatalf("step = %v, want stepConfirmLimits — /models must not skip it", m.step)
	}
}

func TestConfirmLimitsViewShowsFiguresAndSources(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))

	view := ansi.Strip(m.View(80, 24))
	for _, want := range []string{"128000", "context", "max output", string(SourceFetched)} {
		if !strings.Contains(strings.ToLower(view), strings.ToLower(want)) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestConfirmLimitsRendersUnknownNotZero(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "totally-unknown-model"))

	view := ansi.Strip(m.View(80, 24))
	if !strings.Contains(strings.ToLower(view), "unknown") {
		t.Errorf("unknown limits must say so:\n%s", view)
	}
	// A bare "0" would read as a configured value.
	for _, line := range strings.Split(view, "\n") {
		l := strings.ToLower(line)
		if (strings.Contains(l, "context") || strings.Contains(l, "max output")) &&
			strings.Contains(line, " 0") {
			t.Errorf("rendered a confident zero: %q", line)
		}
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

func TestConfirmLimitsPrefersSavedPresetFigures(t *testing.T) {
	// Re-picking a model whose fresh probe returned no limits must surface
	// the figures saved in the existing preset (possibly hand-edited),
	// not "unknown".
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"openai/acme-ultra-3": {
			Name: "openai/acme-ultra-3", Provider: "openai", Model: "acme-ultra-3",
			ContextWindow: 200000, MaxOutputTokens: 8192,
		},
	}
	m := New(Opts{
		Cfg: cfg,
		Discovered: map[string][]schema.ModelInfo{
			"openai": {{ID: "acme-ultra-3"}}, // probed, but with zero limits
		},
		SkipToIntroModel: true,
		ScopedProvider:   "openai",
	})
	m.handlePickerPicked(encodeModelValue("openai", "acme-ultra-3"))

	if m.step != stepConfirmLimits {
		t.Fatalf("step = %v, want stepConfirmLimits", m.step)
	}
	if m.limits.ContextWindow != 200000 || m.limits.MaxOutputTokens != 8192 {
		t.Errorf("limits = %d/%d, want the saved preset's 200000/8192",
			m.limits.ContextWindow, m.limits.MaxOutputTokens)
	}
	if m.limits.ContextSource != SourcePreset || m.limits.OutputSource != SourcePreset {
		t.Errorf("sources = %q/%q, want both %q", m.limits.ContextSource, m.limits.OutputSource, SourcePreset)
	}
}

func TestConfirmLimitsFetchedFiguresBeatSavedPreset(t *testing.T) {
	// A fresh fetch is newer than the saved preset: non-zero discovered
	// figures win.
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"openai/acme-ultra-3": {
			Name: "openai/acme-ultra-3", Provider: "openai", Model: "acme-ultra-3",
			ContextWindow: 200000, MaxOutputTokens: 8192,
		},
	}
	m := New(Opts{
		Cfg: cfg,
		Discovered: map[string][]schema.ModelInfo{
			"openai": {{ID: "acme-ultra-3", ContextWindow: 256000, MaxOutputTokens: 4096}},
		},
		SkipToIntroModel: true,
		ScopedProvider:   "openai",
	})
	m.handlePickerPicked(encodeModelValue("openai", "acme-ultra-3"))

	if m.limits.ContextWindow != 256000 || m.limits.MaxOutputTokens != 4096 {
		t.Errorf("limits = %d/%d, want the fetched 256000/4096",
			m.limits.ContextWindow, m.limits.MaxOutputTokens)
	}
	if m.limits.ContextSource != SourceFetched || m.limits.OutputSource != SourceFetched {
		t.Errorf("sources = %q/%q, want both %q", m.limits.ContextSource, m.limits.OutputSource, SourceFetched)
	}
}

func pickerPicked(value string) tea.Msg { return picker.PickedMsg{Value: value} }

func TestAPIKeyInputIsMasked(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	m := New(Opts{Cfg: cfg})
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
	m := New(Opts{Cfg: cfg})
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
	m := New(Opts{Cfg: cfg})
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
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(probe.ResultMsg{Provider: m.providerName, Err: errors.New("connection refused")})
	if updated.step != stepProbing {
		t.Fatalf("failure should stay probing, got %v", updated.step)
	}
	if !strings.Contains(updated.err, "is the server running") {
		t.Fatalf("expected probe hint in error, got: %q", updated.err)
	}
}

func TestProbeFailureHintForNoSuchHost(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(probe.ResultMsg{Provider: m.providerName, Err: errors.New("no such host")})
	if !strings.Contains(updated.err, "hostname not found") {
		t.Fatalf("expected hostname hint, got: %q", updated.err)
	}
}

func TestProbeFailureHintForUnauthorized(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(probe.ResultMsg{Provider: m.providerName, Err: errors.New("401 unauthorized")})
	if !strings.Contains(updated.err, "key rejected") {
		t.Fatalf("expected key rejected hint, got: %q", updated.err)
	}
}

func TestProbeFailureHintForTimeout(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(probe.ResultMsg{Provider: m.providerName, Err: errors.New("deadline exceeded")})
	if !strings.Contains(updated.err, "timed out") {
		t.Fatalf("expected timeout hint, got: %q", updated.err)
	}
}

func TestProbeFailureHintForCertificate(t *testing.T) {
	m := New(Opts{Cfg: config.Default(), Discovered: map[string][]schema.ModelInfo{}})
	m, _ = m.Update(pickerPicked("ollama"))
	updated, _ := m.Update(probe.ResultMsg{Provider: m.providerName, Err: errors.New("certificate signed by unknown authority")})
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
		template: provider.ProviderTemplate{ID: "custom", Type: "openai_compatible"},
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
		template: provider.ProviderTemplate{ID: "custom", Type: "openai_compatible"},
		cfg:      config.Config{Providers: map[string]config.ProviderConfig{}},
	}
	if got := m.uniqueName(); got != "custom" {
		t.Errorf("uniqueName() = %q, want %q", got, "custom")
	}
}

func TestAPIKeyStepDefaultsToEnvVarWhenTemplateHasOne(t *testing.T) {
	m := &Model{template: provider.ProviderTemplate{
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
	m := &Model{template: provider.ProviderTemplate{ID: "openai", KeyEnv: "OPENAI_API_KEY"}}
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
	m := &Model{template: provider.ProviderTemplate{ID: "openai", KeyEnv: "OPENAI_API_KEY"}}
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
	m := New(Opts{
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
	m := New(Opts{
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
	// should land on stepConfirmLimits, not stepDone.
	if m.step != stepConfirmLimits {
		t.Errorf("step = %v, want stepConfirmLimits", m.step)
	}
	_ = cmd
}

func TestUniqueNameEmptyTemplateIDBehavesAsCustom(t *testing.T) {
	m := &Model{
		template: provider.ProviderTemplate{ID: "", Type: "openai_compatible"},
		cfg: config.Config{Providers: map[string]config.ProviderConfig{
			"custom": {}, "custom-2": {},
		}},
	}
	if got := m.uniqueName(); got != "custom-3" {
		t.Errorf("uniqueName() = %q, want %q", got, "custom-3")
	}
}

func TestModelStepEmitsRefreshOnCtrlR(t *testing.T) {
	m := New(Opts{
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
	m := New(Opts{
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
	m := New(Opts{
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

func TestEditingContextWindowMarksItEdited(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))

	m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m.input.SetValue("200000")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.limits.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", m.limits.ContextWindow)
	}
	if m.limits.ContextSource != SourceEdited {
		t.Errorf("ContextSource = %q, want %q", m.limits.ContextSource, SourceEdited)
	}
	if m.limits.OutputSource == SourceEdited {
		t.Error("editing the context window must not relabel the output cap")
	}
}

func TestEditingMaxOutputMarksItEdited(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))

	m.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	m.input.SetValue("32768")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.limits.MaxOutputTokens != 32768 {
		t.Errorf("MaxOutputTokens = %d, want 32768", m.limits.MaxOutputTokens)
	}
	if m.limits.OutputSource != SourceEdited {
		t.Errorf("OutputSource = %q, want %q", m.limits.OutputSource, SourceEdited)
	}
}

func TestRejectsNonNumericEdit(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))
	before := m.limits.ContextWindow

	m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m.input.SetValue("not a number")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.limits.ContextWindow != before {
		t.Errorf("ContextWindow = %d, want it unchanged at %d", m.limits.ContextWindow, before)
	}
	if m.err == "" {
		t.Error("want an error message for a non-numeric edit")
	}
}

func TestRejectsNegativeEdit(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))
	before := m.limits.ContextWindow

	m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m.input.SetValue("-1")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.limits.ContextWindow != before {
		t.Errorf("ContextWindow = %d, want it unchanged", m.limits.ContextWindow)
	}
}

func TestEditingAnUnknownFigureResolvesIt(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "totally-unknown-model"))
	if m.limits.ContextSource != SourceUnknown {
		t.Fatalf("precondition: want unknown, got %q", m.limits.ContextSource)
	}

	m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m.input.SetValue("8192")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.limits.ContextWindow != 8192 || m.limits.ContextSource != SourceEdited {
		t.Errorf("got %d/%q, want 8192/%q", m.limits.ContextWindow, m.limits.ContextSource, SourceEdited)
	}
}

func TestEditingZeroClearsToUnknown(t *testing.T) {
	m := newConnectForModelPick(t)
	m.handlePickerPicked(encodeModelValue("openai", "gpt-4o"))
	if m.limits.ContextSource != SourceFetched {
		t.Fatalf("precondition: want fetched, got %q", m.limits.ContextSource)
	}

	m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m.input.SetValue("0")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.limits.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0 (cleared)", m.limits.ContextWindow)
	}
	if m.limits.ContextSource != SourceUnknown {
		t.Errorf("ContextSource = %q, want %q", m.limits.ContextSource, SourceUnknown)
	}
}

func TestModelStepSubtitleNamesTheProviderWhenScoped(t *testing.T) {
	m := New(Opts{
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
	m := New(Opts{Cfg: config.Config{}}) // starts at stepPickTemplate
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if cmd != nil {
		if _, ok := cmd().(RefreshMsg); ok {
			t.Error("refresh should only be bound in the model step")
		}
	}
}
