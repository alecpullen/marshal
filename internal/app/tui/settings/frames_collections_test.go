package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

func TestPresetsFrameHasNoManualAddAffordance(t *testing.T) {
	s := profilesTestState() // reuse the existing helper from frames_profiles_test.go
	frame := presetsFrame(s)
	built := frame
	if built.List.OnAdd != nil {
		t.Error("presetsFrame's collection must not allow manual add — presets are materialized automatically")
	}
}

func TestPresetsFrameHasNoManualCopyAffordance(t *testing.T) {
	s := profilesTestState()
	row := presetsFrame(s).List.Rows()[0]
	if row.Yank != nil || row.Paste != nil {
		t.Fatal("presets must not be manually copied; materialization owns preset creation")
	}
}

func TestPresetProviderAndModelFieldsAreReadOnly(t *testing.T) {
	s := profilesTestState()
	detail := presetsFrame(s).List.Rows()[0].Build()
	for _, title := range []string{"Provider", "Model"} {
		var found *field
		for _, row := range detail.List.Rows() {
			if row.Title == title {
				found = row
				break
			}
		}
		if found == nil {
			t.Fatalf("preset detail is missing %s field", title)
		}
		if found.Kind != kindScalar || found.SetStr != nil || found.PickOnPick != nil {
			t.Fatalf("preset %s field must be read-only, got kind=%v set=%v pick=%v", title, found.Kind, found.SetStr != nil, found.PickOnPick != nil)
		}
	}
}

func TestProviderRowShowsEndpointAndKeySource(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama":     {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
		"openrouter": {Type: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_KEY"},
		"anthropic":  {Type: "openai_compatible", BaseURL: "https://api.anthropic.com", APIKey: "sk-ant-xxxx"},
	}
	st := newState(cfg)
	f := providersFrame(st)
	rows := f.List.Rows()

	tests := []struct {
		name     string
		wantPart string // substring the row label must contain
	}{
		{"ollama", "localhost"},
		{"ollama", "local"},
		{"ollama", "no key"},
		{"openrouter", "openrouter.ai"},
		{"openrouter", "remote"},
		{"openrouter", "$OPENROUTER_KEY"},
		{"anthropic", "api.anthropic.com"},
		{"anthropic", "remote"},
		{"anthropic", "key stored"},
	}

	for _, tt := range tests {
		var row *field
		for _, r := range rows {
			if r.ID == "providers."+tt.name {
				row = r
				break
			}
		}
		if row == nil {
			t.Fatalf("no row for provider %q", tt.name)
		}
		label := row.Title
		if !strings.Contains(label, tt.wantPart) {
			t.Errorf("provider %q row label = %q, want substring %q", tt.name, label, tt.wantPart)
		}
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
	ps.Top().List.Refresh()
	pc, ok := s.cfg.Providers["ollama"]
	if !ok {
		t.Fatalf("should have provider, got %v", s.cfg.Providers)
	}
	if pc.Type != "openai_compatible" {
		t.Fatalf("provider should default to openai_compatible, got %q", pc.Type)
	}
	// drill into it and edit Type
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(ps.Stack) != 2 {
		t.Fatalf("enter should drill into the provider, depth=%d", len(ps.Stack))
	}
	ps.Update(kp("j"))                                      // skip Name row (row 0) to reach Type
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})          // edit Type row
	ps.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}) // clear pre-populated value
	for _, r := range "anthropic" {
		ps.Update(kp(string(r)))
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.cfg.Providers["ollama"].Type != "anthropic" {
		t.Fatalf("type edit should apply immediately, got %q", s.cfg.Providers["ollama"].Type)
	}
}

func TestProviderRenameCascadesToPresetsAndDiscovered(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen": {Name: "ollama/qwen", Provider: "ollama", Model: "qwen2.5-coder:7b"},
		"other/gpt":   {Name: "other/gpt", Provider: "other", Model: "gpt-4o"},
	}
	st := newState(cfg)
	st.discovered["ollama"] = []schema.ModelInfo{{ID: "qwen2.5-coder:7b"}}

	drill := providersFrame(st).List.Rows()[0]
	detail := drill.Build()

	var nameRow *field
	for _, r := range detail.List.Rows() {
		if r.Title == "Name" {
			nameRow = r
			break
		}
	}
	if nameRow == nil {
		t.Fatal("provider detail must have a Name row")
	}
	if err := nameRow.SetStr("my-ollama"); err != nil {
		t.Fatalf("rename err = %v", err)
	}
	if _, ok := st.cfg.Providers["ollama"]; ok {
		t.Fatal("old provider key should be deleted")
	}
	if _, ok := st.cfg.Providers["my-ollama"]; !ok {
		t.Fatal("new provider key should exist")
	}
	if st.cfg.Models.Presets["my-ollama/qwen"].Provider != "my-ollama" {
		t.Fatalf("preset provider not updated: %+v", st.cfg.Models.Presets["my-ollama/qwen"])
	}
	if _, ok := st.cfg.Models.Presets["ollama/qwen"]; ok {
		t.Fatal("old pair-keyed preset should be renamed")
	}
	if models, ok := st.discovered["my-ollama"]; !ok || len(models) != 1 {
		t.Fatal("discovered map should be renamed")
	}
	if _, ok := st.discovered["ollama"]; ok {
		t.Fatal("old discovered entry should be deleted")
	}
}

func TestSettingsModelPickerUsesStoredTemplateID(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"work-openai": {Type: "openai_compatible", BaseURL: "https://api.openai.com/v1", Template: "openai"},
	}
	st := newState(cfg)
	f := modelPickerField(st, "test.model",
		func() string { return "work-openai" },
		func() string { return "gpt-4o" },
		func(string) error { return nil })
	found := false
	for _, it := range f.PickOptions() {
		if it.Label == "gpt-4o" && strings.Contains(it.Badge, "catalog") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected catalog model from stored template ID")
	}
}

func TestProviderBaseURLEditInvalidatesDiscovery(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	st := newState(cfg)
	st.discovered["ollama"] = []schema.ModelInfo{{ID: "qwen2.5:7b"}, {ID: "llama3.1:8b"}}

	f := providersFrame(st)
	drill := f.List.Rows()[0]
	detail := drill.Build()
	for _, r := range detail.List.Rows() {
		if r.ID == "providers.ollama.base_url" {
			if err := r.SetStr("http://localhost:9999/v1"); err != nil {
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
	drill := providersFrame(st).List.Rows()[0]
	detail := drill.Build()

	var found *field
	for _, r := range detail.List.Rows() {
		if r.Title == "Test connection" {
			found = r
			break
		}
	}
	if found == nil {
		t.Fatal("provider detail must have a Test connection row")
	}
	if found.Kind != kindAction {
		t.Fatalf("Test connection row kind = %v, want kindAction", found.Kind)
	}
}

func TestRemoteProviderTestConnectionBlockedByPrivacy(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = false
	cfg.Providers = map[string]config.ProviderConfig{
		"openrouter": {Type: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1"},
	}
	st := newState(cfg)
	drill := providersFrame(st).List.Rows()[0]
	detail := drill.Build()

	var tc *field
	for _, r := range detail.List.Rows() {
		if r.Title == "Test connection" {
			tc = r
			break
		}
	}
	label := tc.ActLabel()
	if !strings.Contains(label, "blocked") {
		t.Fatalf("remote provider with privacy off: label = %q, want 'blocked'", label)
	}
	if cmd := tc.Act(); cmd != nil {
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
	drill := providersFrame(st).List.Rows()[0]
	detail := drill.Build()

	var tc *field
	for _, r := range detail.List.Rows() {
		if r.Title == "Test connection" {
			tc = r
			break
		}
	}
	if strings.Contains(tc.ActLabel(), "blocked") {
		t.Fatal("local provider should not be blocked")
	}
}

func TestProvidersFrameHasOnAddMsg(t *testing.T) {
	st := newState(config.Default())
	f := providersFrame(st)
	if f.List.OnAddMsg == nil {
		t.Fatal("providers root frame must have onAddMsg set")
	}
	msg := f.List.OnAddMsg()
	if _, ok := msg.(OpenConnectMsg); !ok {
		t.Fatalf("onAddMsg should return OpenConnectMsg, got %T", msg)
	}
}

func TestProviderPickerAddProviderSetsConnectRequested(t *testing.T) {
	st := newState(config.Default())
	setProvider := func(v string) error { return nil }
	f := providerPickerField(st, "test.provider", func() string { return "" }, setProvider)
	err := f.PickOnPick("__add_provider__")
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

	drill := providersFrame(st).List.Rows()[0]
	detail := drill.Build()

	var nameRow *field
	for _, r := range detail.List.Rows() {
		if r.Title == "Name" {
			nameRow = r
			break
		}
	}
	if nameRow == nil {
		t.Fatal("provider detail must have a Name row")
	}
	if nameRow.GetStr() != "ollama" {
		t.Fatalf("Name = %q, want ollama", nameRow.GetStr())
	}
	if err := nameRow.SetStr("my-ollama"); err != nil {
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

	drill := providersFrame(st).List.Rows()[0]
	detail := drill.Build()

	var nameRow *field
	for _, r := range detail.List.Rows() {
		if r.Title == "Name" {
			nameRow = r
			break
		}
	}
	if err := nameRow.SetStr("openrouter"); err == nil {
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
	rows := f.List.Rows()
	var ollamaRow *field
	for _, r := range rows {
		if r.ID == "providers.ollama" {
			ollamaRow = r
			break
		}
	}
	if ollamaRow == nil {
		t.Fatal("no ollama row")
	}
	data := ollamaRow.Yank()
	pc, ok := data.(yankedMapEntry)
	if !ok {
		t.Fatalf("yank data = %T, want yankedMapEntry", data)
	}
	if pc.Key != "ollama" {
		t.Fatalf("yanked key = %q, want ollama", pc.Key)
	}

	if err := ollamaRow.Paste(data); err != nil {
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
	rows := f.List.Rows()
	var row1 *field
	for _, r := range rows {
		if r.ID == "hooks.1" {
			row1 = r
			break
		}
	}
	if row1 == nil {
		t.Fatal("no hooks.1 row")
	}
	if row1.MoveUp == nil {
		t.Fatal("hooks rows must support moveUp")
	}
	row1.MoveUp()
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
	rows := f.List.Rows()
	var row0 *field
	for _, r := range rows {
		if r.ID == "permissions.0" {
			row0 = r
			break
		}
	}
	if row0 == nil {
		t.Fatal("no permissions.0 row")
	}
	data := row0.Yank()
	if err := row0.Paste(data); err != nil {
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

	f := projectFrame(st)
	var langDrill *field
	for _, r := range f.List.Rows() {
		if r.ID == "project.languages" {
			langDrill = r
			break
		}
	}
	detail := langDrill.Build()
	rows := detail.List.Rows()
	var row0 *field
	for _, r := range rows {
		if r.ID == "project.languages.0" {
			row0 = r
			break
		}
	}
	if row0.MoveDown == nil {
		t.Fatal("languages items must support moveDown")
	}
	row0.MoveDown()
	if st.cfg.Project.Languages[0] != "markdown" {
		t.Fatalf("after moveDown, languages[0] = %q, want markdown", st.cfg.Project.Languages[0])
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
