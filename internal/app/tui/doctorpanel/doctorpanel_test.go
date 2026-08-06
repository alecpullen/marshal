package doctorpanel

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/listpanel"
)

func TestPanelRendersCleanState(t *testing.T) {
	state := &session.State{}
	p := New(state, nil)
	if p == nil {
		t.Fatal("New returned nil")
	}
	fields := diagsToFields(nil)
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
	fields := diagsToFields(diags)
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
