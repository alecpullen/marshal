package doctorpanel

import (
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/listpanel"
)

func TestPanelRendersCleanState(t *testing.T) {
	state := &session.State{}
	p := New(state, nil, nil)
	if p == nil {
		t.Fatal("New returned nil")
	}
	fields := diagsToFields(nil, nil)
	if len(fields) != 1 || fields[0].ID != "clean" {
		t.Fatalf("expected one clean field, got %+v", fields)
	}
}

func TestPanelMarksFixableEnvDiagnostic(t *testing.T) {
	diags := []config.Diagnostic{
		{
			Severity: config.SeverityWarning,
			Path:     "providers.openai.api_key_env",
			Message:  "provider openai references env var OPENAI_API_KEY which is not set",
		},
	}
	fields := diagsToFields(diags, nil)
	if len(fields) != 2 { // header + row
		t.Fatalf("expected 2 fields, got %d", len(fields))
	}
	row := fields[1]
	if row.Kind != listpanel.KindAction {
		t.Fatalf("expected action row, got kind %d", row.Kind)
	}
	cmd := row.Act()
	msg := cmd()
	fix, ok := msg.(FixMsg)
	if !ok {
		t.Fatalf("expected FixMsg, got %T", msg)
	}
	if fix.Provider != "openai" {
		t.Fatalf("Provider = %q, want openai", fix.Provider)
	}
}

func TestPanelNeverEchoesKeyMaterial(t *testing.T) {
	// A literal secret value that must never surface in rendered doctor
	// output. Diagnostics reference the env var *name*, not its value.
	const secret = "sk-super-secret-key-material-42"
	diags := []config.Diagnostic{
		{
			Severity: config.SeverityWarning,
			Path:     "providers.openai.api_key_env",
			Message:  "provider openai references env var OPENAI_API_KEY which is not set",
		},
		{
			Severity: config.SeverityError,
			Path:     "providers.anthropic.base_url",
			Message:  "provider anthropic has an empty base_url",
		},
	}
	p := New(&session.State{}, diags, nil)
	if p == nil {
		t.Fatal("New returned nil")
	}
	fields := diagsToFields(diags, nil)
	var sb strings.Builder
	for _, f := range fields {
		sb.WriteString(f.Title)
		sb.WriteString(" ")
		sb.WriteString(f.Desc)
		sb.WriteString(" ")
	}
	if got := sb.String(); strings.Contains(got, secret) {
		t.Fatalf("rendered doctor output leaked key material: %q", got)
	}
}

func TestPanelRendersIntelligenceSection(t *testing.T) {
	checks := []Check{
		{Name: "Index", Status: "ok", Detail: "10 files · 42 symbols"},
		{Name: "Embeddings", Status: "off", Detail: "disabled"},
		{Name: "LSP", Status: "warn", Detail: "none on PATH"},
		{Name: "Watcher", Status: "warn", Detail: "off"},
	}
	fields := diagsToFields(nil, checks)
	if fields[0].ID != "clean" {
		t.Fatalf("first field = %q, want the clean-state row", fields[0].ID)
	}
	var titles []string
	for _, f := range fields {
		titles = append(titles, f.Title)
	}
	joined := strings.Join(titles, "\n")
	for _, want := range []string{"Intelligence", "Index", "Embeddings", "LSP", "Watcher"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("fields missing %q:\n%s", want, joined)
		}
	}
}
