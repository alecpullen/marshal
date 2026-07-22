package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

func TestPresetProviderFieldIsKindPicker(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b"},
	}
	st := newState(cfg)
	drill := presetsFrame(st).list.Rows()[0]
	detail := drill.build()

	var providerRow *field
	for _, r := range detail.list.Rows() {
		if r.title == "Provider" {
			providerRow = r
			break
		}
	}
	if providerRow == nil {
		t.Fatal("preset detail must have a Provider row")
	}
	if providerRow.kind != kindPicker {
		t.Fatalf("preset Provider row kind = %v, want kindPicker", providerRow.kind)
	}
}

func TestProvidersAddAndEditType(t *testing.T) {
	s := newState(config.Default())
	// Add a provider directly via config
	s.cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	ps := newPaneStack(providersFrame(s))
	ps.SetSize(80, 24)
	ps.top().list.Refresh()
	pc, ok := s.cfg.Providers["ollama"]
	if !ok {
		t.Fatalf("should have provider, got %v", s.cfg.Providers)
	}
	if pc.Type != "openai_compatible" {
		t.Fatalf("provider should default to openai_compatible, got %q", pc.Type)
	}
	// drill into it and edit Type
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(ps.stack) != 2 {
		t.Fatalf("enter should drill into the provider, depth=%d", len(ps.stack))
	}
	ps.Update(kp("j"))                             // skip Name row (row 0) to reach Type
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // edit Type row
	ps.top().list.input.SetValue("anthropic")
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Providers["ollama"].Type != "anthropic" {
		t.Fatalf("type edit should apply immediately, got %q", s.cfg.Providers["ollama"].Type)
	}
}

func TestProviderBaseURLEditInvalidatesDiscovery(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	st := newState(cfg)
	st.discovered["ollama"] = []string{"qwen2.5:7b", "llama3.1:8b"}

	f := providersFrame(st)
	drill := f.list.Rows()[0]
	detail := drill.build()
	for _, r := range detail.list.Rows() {
		if r.id == "providers.ollama.base_url" {
			if err := r.setStr("http://localhost:9999/v1"); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if _, ok := st.discovered["ollama"]; ok {
		t.Fatal("editing base_url should invalidate the discovery cache for ollama")
	}
}

func TestProviderDetailHasTestConnectionRow(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	st := newState(cfg)
	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var found *field
	for _, r := range detail.list.Rows() {
		if r.title == "Test connection" {
			found = r
			break
		}
	}
	if found == nil {
		t.Fatal("provider detail must have a Test connection row")
	}
	if found.kind != kindAction {
		t.Fatalf("Test connection row kind = %v, want kindAction", found.kind)
	}
}

func TestRemoteProviderTestConnectionBlockedByPrivacy(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Type: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1"},
	}
	st := newState(cfg)
	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var tc *field
	for _, r := range detail.list.Rows() {
		if r.title == "Test connection" {
			tc = r
			break
		}
	}
	label := tc.actLabel()
	if !strings.Contains(label, "blocked") {
		t.Fatalf("remote provider with privacy off: label = %q, want 'blocked'", label)
	}
	if cmd := tc.act(); cmd != nil {
		t.Fatal("blocked test connection act() should return nil")
	}
}

func TestLocalProviderTestConnectionNotBlocked(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	st := newState(cfg)
	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var tc *field
	for _, r := range detail.list.Rows() {
		if r.title == "Test connection" {
			tc = r
			break
		}
	}
	if strings.Contains(tc.actLabel(), "blocked") {
		t.Fatal("local provider should not be blocked")
	}
}

func TestProvidersFrameHasOnAddMsg(t *testing.T) {
	st := newState(config.Default())
	f := providersFrame(st)
	if f.list.onAddMsg == nil {
		t.Fatal("providers root frame must have onAddMsg set")
	}
	msg := f.list.onAddMsg()
	if _, ok := msg.(OpenConnectMsg); !ok {
		t.Fatalf("onAddMsg should return OpenConnectMsg, got %T", msg)
	}
}

func TestProviderPickerAddProviderSetsConnectRequested(t *testing.T) {
	st := newState(config.Default())
	setProvider := func(v string) error { return nil }
	f := providerPickerField(st, "test.provider", func() string { return "" }, setProvider)
	err := f.pickOnPick("__add_provider__")
	if err != nil {
		t.Fatalf("pickOnPick(__add_provider__) = %v, want nil", err)
	}
	if !st.connectRequested {
		t.Fatal("pickOnPick(__add_provider__) should set state.connectRequested")
	}
}

func TestProviderNameFieldRenamesKey(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "secret"},
	}
	st := newState(cfg)

	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var nameRow *field
	for _, r := range detail.list.Rows() {
		if r.title == "Name" {
			nameRow = r
			break
		}
	}
	if nameRow == nil {
		t.Fatal("provider detail must have a Name row")
	}
	if nameRow.getStr() != "ollama" {
		t.Fatalf("Name = %q, want ollama", nameRow.getStr())
	}
	if err := nameRow.setStr("my-ollama"); err != nil {
		t.Fatalf("rename err = %v", err)
	}
	if _, ok := st.cfg.Providers["ollama"]; ok {
		t.Fatal("old key should be deleted after rename")
	}
	pc, ok := st.cfg.Providers["my-ollama"]
	if !ok {
		t.Fatal("new key should exist after rename")
	}
	if pc.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("renamed provider BaseURL = %q, want preserved", pc.BaseURL)
	}
}

func TestProviderNameFieldRejectsCollision(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama":     {Type: "openai_compatible"},
		"openrouter": {Type: "openai_compatible"},
	}
	st := newState(cfg)

	drill := providersFrame(st).list.Rows()[0]
	detail := drill.build()

	var nameRow *field
	for _, r := range detail.list.Rows() {
		if r.title == "Name" {
			nameRow = r
			break
		}
	}
	if err := nameRow.setStr("openrouter"); err == nil {
		t.Fatal("rename to existing key should error")
	}
}

func TestProviderYankPasteDuplicates(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "sk-1234"},
	}
	st := newState(cfg)

	f := providersFrame(st)
	rows := f.list.Rows()
	var ollamaRow *field
	for _, r := range rows {
		if r.id == "providers.ollama" {
			ollamaRow = r
			break
		}
	}
	if ollamaRow == nil {
		t.Fatal("no ollama row")
	}
	data := ollamaRow.yank()
	pc, ok := data.(yankedMapEntry)
	if !ok {
		t.Fatalf("yank data = %T, want yankedMapEntry", data)
	}
	if pc.key != "ollama" {
		t.Fatalf("yanked key = %q, want ollama", pc.key)
	}

	if err := ollamaRow.paste(data); err != nil {
		t.Fatalf("paste err = %v", err)
	}
	cp, ok := st.cfg.Providers["ollama-copy"]
	if !ok {
		t.Fatal("paste should create ollama-copy")
	}
	if cp.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("copy BaseURL = %q, want preserved", cp.BaseURL)
	}
}

func TestHooksReorderMoveUp(t *testing.T) {
	cfg := config.Default()
	cfg.Hooks.Entries = []config.HookConfig{
		{Event: "pre_tool", Command: "a.sh"},
		{Event: "turn_end", Command: "b.sh"},
	}
	st := newState(cfg)

	f := hooksFrame(st)
	rows := f.list.Rows()
	var row1 *field
	for _, r := range rows {
		if r.id == "hooks.1" {
			row1 = r
			break
		}
	}
	if row1 == nil {
		t.Fatal("no hooks.1 row")
	}
	if row1.moveUp == nil {
		t.Fatal("hooks rows must support moveUp")
	}
	row1.moveUp()
	if st.cfg.Hooks.Entries[0].Command != "b.sh" {
		t.Fatalf("after moveUp, entries[0].Command = %q, want b.sh", st.cfg.Hooks.Entries[0].Command)
	}
}

func TestPermissionsDuplicateInPlace(t *testing.T) {
	cfg := config.Default()
	cfg.Permissions.Rules = []config.PermissionRule{
		{Permission: "shell", Pattern: "*", Action: "confirm"},
	}
	st := newState(cfg)

	f := permissionsFrame(st)
	rows := f.list.Rows()
	var row0 *field
	for _, r := range rows {
		if r.id == "permissions.0" {
			row0 = r
			break
		}
	}
	if row0 == nil {
		t.Fatal("no permissions.0 row")
	}
	data := row0.yank()
	if err := row0.paste(data); err != nil {
		t.Fatalf("paste err = %v", err)
	}
	if len(st.cfg.Permissions.Rules) != 2 {
		t.Fatalf("rules len = %d, want 2 after duplicate", len(st.cfg.Permissions.Rules))
	}
	if st.cfg.Permissions.Rules[1].Action != "confirm" {
		t.Fatalf("duplicated rule Action = %q, want confirm", st.cfg.Permissions.Rules[1].Action)
	}
}

func TestLanguagesReorderMoveDown(t *testing.T) {
	cfg := config.Default()
	cfg.Project.Languages = []string{"go", "markdown"}
	st := newState(cfg)

	f := commandsFrame(st)
	var langDrill *field
	for _, r := range f.list.Rows() {
		if r.id == "project.languages" {
			langDrill = r
			break
		}
	}
	detail := langDrill.build()
	rows := detail.list.Rows()
	var row0 *field
	for _, r := range rows {
		if r.id == "project.languages.0" {
			row0 = r
			break
		}
	}
	if row0.moveDown == nil {
		t.Fatal("languages items must support moveDown")
	}
	row0.moveDown()
	if st.cfg.Project.Languages[0] != "markdown" {
		t.Fatalf("after moveDown, languages[0] = %q, want markdown", st.cfg.Project.Languages[0])
	}
}

func TestModelPickerDiscoverRunsProbe(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b"},
	}
	st := newState(cfg)
	drill := presetsFrame(st).list.Rows()[0]
	detail := drill.build()

	var modelRow *field
	for _, r := range detail.list.Rows() {
		if r.title == "Model" {
			modelRow = r
			break
		}
	}
	if modelRow == nil {
		t.Fatal("preset detail must have a Model row")
	}

	// pickOnPick with __discover__ for a local provider should queue a command
	// and return nil error.
	err := modelRow.pickOnPick("__discover__")
	if err != nil {
		t.Fatalf("pickOnPick(__discover__) for local provider = %v, want nil", err)
	}
	if st.pendingCmd == nil {
		t.Fatal("pickOnPick(__discover__) should set state.pendingCmd")
	}
}

func TestModelPickerDiscoverBlockedForRemote(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Type: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "openrouter", Model: "claude-3.5-sonnet"},
	}
	st := newState(cfg)
	drill := presetsFrame(st).list.Rows()[0]
	detail := drill.build()

	var modelRow *field
	for _, r := range detail.list.Rows() {
		if r.title == "Model" {
			modelRow = r
			break
		}
	}
	if modelRow == nil {
		t.Fatal("preset detail must have a Model row")
	}

	// pickOnPick with __discover__ for a remote provider with privacy off
	// should return an error and NOT queue a command.
	err := modelRow.pickOnPick("__discover__")
	if err == nil {
		t.Fatal("pickOnPick(__discover__) for remote provider with privacy off should return error")
	}
	if st.pendingCmd != nil {
		t.Fatal("pickOnPick(__discover__) for remote provider should NOT set state.pendingCmd")
	}
}

func TestHooksAddWithoutPromptAndDelete(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(hooksFrame(s))
	ps.SetSize(80, 24)
	ps.Update(kp("a")) // no key prompt: adds immediately
	if len(s.cfg.Hooks.Entries) != 1 || s.cfg.Hooks.Entries[0].Event != "pre_tool" {
		t.Fatalf("a should append a pre_tool hook, got %v", s.cfg.Hooks.Entries)
	}
	ps.Update(kp("d"))
	if len(s.cfg.Hooks.Entries) != 0 {
		t.Fatalf("d should delete the hook, got %v", s.cfg.Hooks.Entries)
	}
}
