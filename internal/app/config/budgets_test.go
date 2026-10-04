package config

import (
	"marshal/internal/llm/routing"
	"marshal/internal/trust"
	"os"
	"strings"
	"testing"
)

func TestBudgetsDefaults(t *testing.T) {
	b := Default().Budgets
	if b.DailyUSD != 0 || b.PerAgentUSD != 0 || b.OnDailyCap != "warn" || b.OnAgentCap != "warn" {
		t.Fatalf("defaults = %+v", b)
	}
}

func TestBudgetsLoadFromUserTOML(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml",
		"[budgets]\ndaily_usd = 25.5\nper_agent_usd = 5\non_daily_cap = \"block\"\non_agent_cap = \"pause\"\n")
	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatal(err)
	}
	b := l.Merged.Budgets
	if b.DailyUSD != 25.5 || b.PerAgentUSD != 5 || b.OnDailyCap != "block" || b.OnAgentCap != "pause" {
		t.Fatalf("budgets = %+v", b)
	}
}

func TestBudgetsInvalidActionFallsBackWithDiagnostic(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml",
		"[budgets]\non_daily_cap = \"pause\"\non_agent_cap = \"explode\"\ndaily_usd = -3\n")
	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatal(err)
	}
	b := l.Merged.Budgets
	if b.OnDailyCap != "warn" || b.OnAgentCap != "warn" || b.DailyUSD != 0 {
		t.Fatalf("budgets = %+v", b)
	}
	found := map[string]bool{}
	for _, d := range Diagnose(l.Merged, l) {
		found[d.Path] = true
	}
	for _, p := range []string{"budgets.on_daily_cap", "budgets.on_agent_cap", "budgets.daily_usd"} {
		if !found[p] {
			t.Errorf("no diagnostic for %s", p)
		}
	}
}

func TestBudgetsProjectSectionIgnored(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeFile(t, home+"/.config/marshal/config.toml", "[budgets]\ndaily_usd = 10\n")
	writeFile(t, work+"/.marshal/config.toml", "[budgets]\ndaily_usd = 999\nper_agent_usd = 50\n")
	l, err := LoadLayers(LoadOptions{
		HomeDir: home, WorkingDir: work,
		TrustResolver: staticTrustResolver{decision: trust.DecisionTrustPermanent},
	})
	if err != nil {
		t.Fatal(err)
	}
	if b := l.Merged.Budgets; b.DailyUSD != 10 || b.PerAgentUSD != 0 {
		t.Fatalf("project budgets applied: %+v", b)
	}
	if !l.ProjectBudgetsIgnored {
		t.Fatal("ProjectBudgetsIgnored = false")
	}
	found := false
	for _, d := range Diagnose(l.Merged, l) {
		found = found || d.Path == "budgets"
	}
	if !found {
		t.Fatal("no diagnostic for ignored project [budgets]")
	}
}

func TestBudgetsSaveRoundTrip(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	path := UserConfigPath(home)
	cfg := Default()
	cfg.Budgets = BudgetsConfig{DailyUSD: 12, PerAgentUSD: 3.5, OnDailyCap: "block", OnAgentCap: "pause"}
	if err := SaveUserConfigSection(path, cfg); err != nil {
		t.Fatal(err)
	}
	l, err := LoadLayers(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatal(err)
	}
	if l.Merged.Budgets != cfg.Budgets {
		t.Fatalf("round trip = %+v", l.Merged.Budgets)
	}
	// Resetting to defaults removes the section.
	if err := SaveUserConfigSection(path, Default()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "[budgets]") {
		t.Fatalf("budgets section kept after reset:\n%s", data)
	}
}

func TestSessionRoutingAppliesToRoutingConfig(t *testing.T) {
	cfg := Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"p/a": {Name: "p/a", Provider: "p", Model: "a", LocalOnly: true},
		"p/b": {Name: "p/b", Provider: "p", Model: "b", LocalOnly: true},
	}
	cfg.Providers = map[string]ProviderConfig{"p": {BaseURL: "http://localhost:1"}}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"base":   {Name: "base", Roles: map[routing.AgentRole]routing.RoleBinding{routing.RoleImplementer: {Preset: "p/a"}}},
		"strong": {Name: "strong", Roles: map[routing.AgentRole]routing.RoleBinding{routing.RoleImplementer: {Preset: "p/b"}}},
	}
	cfg.Profile.Default = "base"
	cfg.Profile.ActivePreset = "p/a"

	resolve := func(c Config) string {
		r, err := routing.NewStaticRouter(c.RoutingConfig()).ResolveRole(routing.RoleImplementer)
		if err != nil {
			t.Fatal(err)
		}
		return r.Preset.Name
	}
	if got := resolve(cfg); got != "p/a" {
		t.Fatalf("baseline = %s", got)
	}
	cfg.SessionRouting = SessionRouting{Profile: "strong"}
	if got := resolve(cfg); got != "p/b" {
		t.Fatalf("session profile = %s", got)
	}
	cfg.SessionRouting = SessionRouting{Overrides: map[routing.AgentRole]string{routing.RoleImplementer: "p/b"}}
	if got := resolve(cfg); got != "p/b" {
		t.Fatalf("session override = %s", got)
	}
	if err := cfg.ValidateSessionRouting(SessionRouting{Profile: "nope"}); err == nil {
		t.Fatal("unknown profile accepted")
	}
	if err := cfg.ValidateSessionRouting(SessionRouting{Overrides: map[routing.AgentRole]string{"bogus": "p/a"}}); err == nil {
		t.Fatal("unknown role accepted")
	}
}
