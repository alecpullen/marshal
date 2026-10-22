package settings

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
	"marshal/internal/llm/routing"
)

func TestAgentFrameHasScalarRows(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama":     {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
		"openrouter": {Type: "openai_compatible", BaseURL: "https://openrouter.ai/api/v1"},
	}
	st := newState(cfg)
	f := agentFrame(st)

	titles := map[string]*field{}
	for _, r := range f.List.Rows() {
		titles[r.Title] = r
	}
	for _, want := range []string{"Default profile", "Preset", "Max tool iterations", "Max retries", "Max turn context tokens", "Max tool result chars", "Subtask iterations", "Plan first", "Mode", "Auto"} {
		if titles[want] == nil {
			t.Fatalf("Agent frame missing row %q; rows: %v", want, titles)
		}
	}
	if titles["Approval mode"] != nil {
		t.Fatal("Agent frame must not expose the retired Approval mode row")
	}
	// The legacy provider/model rows are gone.
	if titles["Provider"] != nil || titles["Model"] != nil {
		t.Fatal("Agent frame must not expose legacy provider/model rows")
	}
}

func TestAgentFramePresetRowShowsActivePreset(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets["ollama/llama3.1:8b"] = routing.ModelPreset{
		Name:     "ollama/llama3.1:8b",
		Provider: "ollama",
		Model:    "llama3.1:8b",
	}
	cfg.AgentProfiles["local_balanced"] = routing.AgentProfile{
		Name:  "local_balanced",
		Roles: map[routing.AgentRole]routing.RoleBinding{routing.RoleImplementer: {Preset: "ollama/llama3.1:8b"}},
	}
	st := newState(cfg)
	f := agentFrame(st)

	var presetRow *field
	for _, r := range f.List.Rows() {
		if r.Title == "Preset" {
			presetRow = r
		}
	}
	if presetRow == nil {
		t.Fatal("Agent frame must have a Preset row")
	}
	if got := presetRow.GetStr(); got != "ollama/llama3.1:8b" {
		t.Fatalf("Preset row = %q, want ollama/llama3.1:8b", got)
	}
}

func TestAgentFramePresetRowShowsNoneWithoutPreset(t *testing.T) {
	// Default config has no AgentProfiles, so no preset is active.
	st := newState(config.Default())
	f := agentFrame(st)

	var presetRow *field
	for _, r := range f.List.Rows() {
		if r.Title == "Preset" {
			presetRow = r
		}
	}
	if presetRow == nil {
		t.Fatal("Agent frame must have a Preset row")
	}
	if got := presetRow.GetStr(); got != "(none)" {
		t.Fatalf("Preset row = %q, want (none)", got)
	}
}

func TestShellFrameHasEnumAndLists(t *testing.T) {
	s := newState(config.Default())
	ps := newPaneStack(shellFrame(s))
	ps.SetSize(80, 24)
	var titles []string
	for _, f := range ps.Top().List.Rows() {
		titles = append(titles, f.Title)
	}
	for _, want := range []string{"Allow network", "Allow commands", "Confirm commands", "Deny patterns"} {
		found := false
		for _, ti := range titles {
			if ti == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("shell frame missing row %q; rows: %v", want, titles)
		}
	}
	for _, retired := range []string{"Auto-approve shell", "Dynamic argv0 guardrail"} {
		for _, ti := range titles {
			if ti == retired {
				t.Fatalf("shell frame still exposes retired row %q; rows: %v", retired, titles)
			}
		}
	}
}

func TestInteractionSettingsOnlyAskActAndAuto(t *testing.T) {
	st := newState(config.Default())
	f := agentFrame(st)

	var mode, auto *field
	for _, r := range f.List.Rows() {
		switch r.Title {
		case "Mode":
			mode = r
		case "Auto":
			auto = r
		case "Approval mode":
			t.Fatal("retired Approval mode row is still present")
		}
	}
	if mode == nil || auto == nil {
		t.Fatalf("Agent frame missing Mode/Auto: mode=%v auto=%v", mode, auto)
	}
	if got := mode.Options(); len(got) != 2 || got[0] != "ask" || got[1] != "act" {
		t.Fatalf("Mode options = %v, want [ask act]", got)
	}

	st.cfg.Agent.Mode = interaction.ModeAsk
	st.cfg.Agent.Auto = false
	auto.SetBool(true)
	if st.cfg.Agent.Mode != interaction.ModeAct || !st.cfg.Agent.Auto {
		t.Fatalf("Auto on = mode %q auto %v, want act/true", st.cfg.Agent.Mode, st.cfg.Agent.Auto)
	}
}

func TestAskSettingClearsAutoDefault(t *testing.T) {
	st := newState(config.Default())
	st.cfg.Agent.Mode = interaction.ModeAct
	st.cfg.Agent.Auto = true

	var mode *field
	for _, r := range agentFrame(st).List.Rows() {
		if r.Title == "Mode" {
			mode = r
		}
	}
	if mode == nil {
		t.Fatal("Agent frame missing Mode row")
	}
	if err := mode.SetStr("ask"); err != nil {
		t.Fatalf("Mode.SetStr(ask): %v", err)
	}
	if st.cfg.Agent.Mode != interaction.ModeAsk {
		t.Fatalf("mode = %q, want ask", st.cfg.Agent.Mode)
	}
	if st.cfg.Agent.Auto {
		t.Fatal("selecting Ask must clear Auto in the draft")
	}
}

func TestCustomAgentInteractionSettingsPreserveInheritance(t *testing.T) {
	cfg := config.Default()
	cfg.CustomAgents = map[string]routing.CustomAgent{"worker": {Name: "worker"}}
	st := newState(cfg)

	var mode, auto *Field
	for _, f := range CustomAgentFields(st, "worker") {
		switch f.Title {
		case "Mode":
			mode = f
		case "Auto":
			auto = f
		}
	}
	if mode == nil || auto == nil {
		t.Fatalf("custom agent missing Mode/Auto: mode=%v auto=%v", mode, auto)
	}
	if got := mode.GetStr(); got != "inherit" {
		t.Fatalf("initial mode = %q, want inherit", got)
	}
	if got := auto.GetStr(); got != "inherit" {
		t.Fatalf("initial auto = %q, want inherit", got)
	}

	if err := auto.SetStr("off"); err != nil {
		t.Fatalf("auto off: %v", err)
	}
	ca := st.cfg.CustomAgents["worker"]
	if ca.Auto == nil || *ca.Auto {
		t.Fatalf("explicit auto=false was not preserved: %+v", ca.Auto)
	}
	if got := auto.GetStr(); got != "off" {
		t.Fatalf("auto after off = %q, want off", got)
	}

	if err := mode.SetStr("act"); err != nil {
		t.Fatalf("mode act: %v", err)
	}
	ca = st.cfg.CustomAgents["worker"]
	if ca.Mode == nil || *ca.Mode != interaction.ModeAct {
		t.Fatalf("mode = %+v, want act", ca.Mode)
	}
	if err := auto.SetStr("on"); err != nil {
		t.Fatalf("auto on: %v", err)
	}
	ca = st.cfg.CustomAgents["worker"]
	if ca.Auto == nil || !*ca.Auto {
		t.Fatalf("auto on = %+v, want true", ca.Auto)
	}

	if err := mode.SetStr("ask"); err != nil {
		t.Fatalf("mode ask: %v", err)
	}
	ca = st.cfg.CustomAgents["worker"]
	if ca.Mode == nil || *ca.Mode != interaction.ModeAsk {
		t.Fatalf("mode = %+v, want ask", ca.Mode)
	}
	if ca.Auto == nil || *ca.Auto {
		t.Fatalf("selecting Ask must clear an explicit true Auto, got %+v", ca.Auto)
	}

	if err := auto.SetStr("inherit"); err != nil {
		t.Fatalf("auto inherit: %v", err)
	}
	if got := st.cfg.CustomAgents["worker"].Auto; got != nil {
		t.Fatalf("inherit must clear the pointer, got %+v", got)
	}
}
